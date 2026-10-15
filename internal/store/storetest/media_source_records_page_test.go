// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestMediaSourceRecordsPageSQLite verifies that a page-sized media-source
// graph binds every requested source and its alternate relationships in SQLite.
func TestMediaSourceRecordsPageSQLite(t *testing.T) {
	runMediaSourceRecordsPage(t, openItemStore)
}

// TestMediaSourceRecordsPagePostgres verifies the page-sized source graph uses
// PostgreSQL-compatible placeholder binding for multiple requested IDs.
func TestMediaSourceRecordsPagePostgres(t *testing.T) {
	runMediaSourceRecordsPage(t, openPostgresItemStore)
}

func runMediaSourceRecordsPage(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	fixture := newMediaSourceRecordsPageFixture(t, open(t))

	t.Run("page source types are relative to each requested version", func(t *testing.T) {
		records, err := fixture.items.MediaSourceRecordsPage(t.Context(), []dto.UUID{fixture.first.ID, fixture.second.ID})
		if err != nil {
			t.Fatalf("loading primary page media-source records: %v", err)
		}
		fixture.assertRecords(t, records)

		records, err = fixture.items.MediaSourceRecordsPage(t.Context(), []dto.UUID{fixture.firstLinked.ID})
		if err != nil {
			t.Fatalf("loading linked-alternate page media-source records: %v", err)
		}
		fixture.assertRequestedTypes(t, fixture.firstLinked.ID, records[fixture.firstLinked.ID], map[dto.UUID]playback.MediaSourceType{
			fixture.firstLinked.ID: playback.MediaSourceDefault,
			fixture.first.ID:       playback.MediaSourceGrouping,
			fixture.firstLocal.ID:  playback.MediaSourceGrouping,
		})
	})

	t.Run("single requested source APIs keep the request default", func(t *testing.T) {
		for _, requestedID := range []dto.UUID{fixture.first.ID, fixture.firstLinked.ID} {
			record, err := fixture.items.MediaSourceRecord(t.Context(), requestedID)
			if err != nil {
				t.Fatalf("loading single source %s: %v", requestedID, err)
			}
			if record.ID != requestedID || record.Type != playback.MediaSourceDefault {
				t.Errorf("single source %s = %#v, want requested Default source", requestedID, record)
			}
		}
	})

	t.Run("source graphs keep the request default and related versions grouping", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			requested dto.UUID
			want      map[dto.UUID]playback.MediaSourceType
		}{
			{name: "primary", requested: fixture.first.ID, want: map[dto.UUID]playback.MediaSourceType{fixture.first.ID: playback.MediaSourceDefault, fixture.firstLocal.ID: playback.MediaSourceDefault, fixture.firstLinked.ID: playback.MediaSourceGrouping}},
			{name: "linked alternate", requested: fixture.firstLinked.ID, want: map[dto.UUID]playback.MediaSourceType{fixture.firstLinked.ID: playback.MediaSourceDefault, fixture.first.ID: playback.MediaSourceGrouping, fixture.firstLocal.ID: playback.MediaSourceGrouping}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				records, err := fixture.items.MediaSourceRecords(t.Context(), tc.requested)
				if err != nil {
					t.Fatalf("loading source graph for %s: %v", tc.requested, err)
				}
				fixture.assertRequestedTypes(t, tc.requested, records, tc.want)
			})
		}
	})
	t.Run("absent item returns store.ErrNotFound from both single and graph APIs", func(t *testing.T) {
		absent := contractUUID(t, 0x9999)
		if _, err := fixture.items.MediaSourceRecord(t.Context(), absent); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("MediaSourceRecord absent item error = %v, want store.ErrNotFound", err)
		}
		if _, err := fixture.items.MediaSourceRecords(t.Context(), absent); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("MediaSourceRecords absent item error = %v, want store.ErrNotFound", err)
		}
	})
}

type mediaSourceRecordsPageFixture struct {
	items *store.ItemStore

	first, firstLocal, firstLinked, second, secondEquivalent, secondLocal item.Item
	expected                                                              map[dto.UUID]playback.MediaSourceRecord
}

func newMediaSourceRecordsPageFixture(t *testing.T, items *store.ItemStore) mediaSourceRecordsPageFixture {
	t.Helper()
	first := mediaSourcePageItem(t, 9101, "first requested", "first-presentation")
	firstLocal := mediaSourcePageItem(t, 9102, "first local alternate", "")
	firstLinked := mediaSourcePageItem(t, 9103, "first linked alternate", "")
	second := mediaSourcePageItem(t, 9104, "second requested", "second-presentation")
	secondEquivalent := mediaSourcePageItem(t, 9105, "second presentation equivalent", "second-presentation")
	secondLocal := mediaSourcePageItem(t, 9106, "second local alternate", "")

	first.LinkedChildren = []item.LinkedChildRef{
		{ItemID: firstLocal.ID, Type: item.LinkedChildTypeLocalAlternateVersion},
		{ItemID: firstLinked.ID, Type: item.LinkedChildTypeLinkedAlternateVersion},
	}
	secondEquivalent.LinkedChildren = []item.LinkedChildRef{{ItemID: secondLocal.ID, Type: item.LinkedChildTypeLocalAlternateVersion}}
	firstLinked.PrimaryVersionID = &first.ID
	firstLinked.PresentationUniqueKey = first.PresentationUniqueKey

	for _, value := range []item.Item{firstLocal, firstLinked, secondLocal, secondEquivalent, first, second} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upserting media-source page fixture item %s: %v", value.ID, err)
		}
	}

	streams := map[dto.UUID][]playback.MediaStream{
		first.ID:            {mediaSourcePageStream(1920)},
		firstLocal.ID:       {mediaSourcePageStream(2160)},
		firstLinked.ID:      {mediaSourcePageStream(1080)},
		second.ID:           {mediaSourcePageStream(1080)},
		secondEquivalent.ID: {mediaSourcePageStream(2160)},
		secondLocal.ID:      {mediaSourcePageStream(720)},
	}
	attachments := map[dto.UUID][]playback.MediaAttachment{
		first.ID:            {{Index: 0, FileName: "first.jpg", Codec: "mjpeg"}},
		firstLocal.ID:       {{Index: 0, FileName: "first-local.jpg", Codec: "mjpeg"}},
		firstLinked.ID:      {{Index: 0, FileName: "first-linked.jpg", Codec: "mjpeg"}},
		second.ID:           {{Index: 0, FileName: "second.jpg", Codec: "mjpeg"}},
		secondEquivalent.ID: {{Index: 0, FileName: "second-equivalent.jpg", Codec: "mjpeg"}},
		secondLocal.ID:      {{Index: 0, FileName: "second-local.jpg", Codec: "mjpeg"}},
	}
	for id, values := range streams {
		if err := items.ReplaceStreams(t.Context(), id, values); err != nil {
			t.Fatalf("replacing page source streams for %s: %v", id, err)
		}
	}
	for id, values := range attachments {
		if err := items.ReplaceAttachments(t.Context(), id, values); err != nil {
			t.Fatalf("replacing page source attachments for %s: %v", id, err)
		}
	}

	all := []item.Item{first, firstLocal, firstLinked, second, secondEquivalent, secondLocal}
	types := map[dto.UUID]playback.MediaSourceType{
		firstLinked.ID: playback.MediaSourceGrouping,
	}
	expected := make(map[dto.UUID]playback.MediaSourceRecord, len(all))
	for _, value := range all {
		sourceType := playback.MediaSourceDefault
		if groupedType, ok := types[value.ID]; ok {
			sourceType = groupedType
		}
		snapshot, err := items.DerivedSourceSnapshot(t.Context(), value.ID)
		if err != nil {
			t.Fatalf("reading canonical revision for media-source fixture %s: %v", value.ID, err)
		}
		expected[value.ID] = playback.MediaSourceRecord{
			ID:               value.ID,
			Path:             value.Path,
			Name:             value.Name,
			Container:        value.MediaSourceContainer,
			Size:             value.Size,
			RunTimeTicks:     value.RunTimeTicks,
			TotalBitrate:     value.TotalBitrate,
			DateModified:     value.DateModified,
			FileDevice:       value.FileDevice,
			FileInode:        value.FileInode,
			FileChangedAt:    value.FileChangedAt,
			Type:             sourceType,
			SourceRevision:   snapshot.SourceRevision,
			Downloadable:     item.CanDownload(value),
			MediaStreams:     streams[value.ID],
			MediaAttachments: attachments[value.ID],
		}
	}
	return mediaSourceRecordsPageFixture{
		items:            items,
		first:            first,
		firstLocal:       firstLocal,
		firstLinked:      firstLinked,
		second:           second,
		secondEquivalent: secondEquivalent,
		secondLocal:      secondLocal,
		expected:         expected,
	}
}

func (f mediaSourceRecordsPageFixture) assertRecords(t *testing.T, got map[dto.UUID][]playback.MediaSourceRecord) {
	t.Helper()
	want := map[dto.UUID][]dto.UUID{
		f.first.ID:  {f.first.ID, f.firstLocal.ID, f.firstLinked.ID},
		f.second.ID: {f.second.ID, f.secondEquivalent.ID, f.secondLocal.ID},
	}
	if len(got) != len(want) {
		t.Fatalf("page source graph has %d requested entries, want %d: %#v", len(got), len(want), got)
	}
	for requestedID, wantIDs := range want {
		records, ok := got[requestedID]
		if !ok {
			t.Errorf("page source graph missing requested item %s", requestedID)
			continue
		}
		gotIDs := mediaSourcePageRecordIDs(records)
		if !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Errorf("page source records for %s = %v, want %v", requestedID, gotIDs, wantIDs)
		}
		for index, record := range records {
			wantRecord, ok := f.expected[record.ID]
			if !ok {
				t.Errorf("page source record %d for %s has unexpected item %s", index, requestedID, record.ID)
				continue
			}
			if !reflect.DeepEqual(record, wantRecord) {
				t.Errorf("page source record %d for %s = %#v, want %#v", index, requestedID, record, wantRecord)
			}
		}
	}
}

func (f mediaSourceRecordsPageFixture) assertRequestedTypes(t *testing.T, requestedID dto.UUID, got []playback.MediaSourceRecord, want map[dto.UUID]playback.MediaSourceType) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("source graph for %s has %d records, want %d: %#v", requestedID, len(got), len(want), got)
	}
	for _, record := range got {
		wantType, found := want[record.ID]
		if !found {
			t.Errorf("source graph for %s unexpectedly includes %s", requestedID, record.ID)
			continue
		}
		if record.Type != wantType {
			t.Errorf("source %s for requested %s has type %v, want %v", record.ID, requestedID, record.Type, wantType)
		}
		delete(want, record.ID)
	}
	for missing := range want {
		t.Errorf("source graph for %s omitted %s", requestedID, missing)
	}
}

func mediaSourcePageItem(t *testing.T, value int, name, presentationKey string) item.Item {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing media-source page fixture UUID %d: %v", value, err)
	}
	path := fmt.Sprintf("/media/%d.mkv", value)
	container := "mkv"
	size := int64(value * 100)
	ticks := int64(value * 1_000)
	bitrate := value * 10
	device := int64(0x1000)
	inode := int64(value + 0x2000)
	changed := time.Date(2026, time.July, 28, 12, 0, 0, value*1_000, time.UTC)
	result := item.Item{
		ID:                   id,
		Kind:                 item.KindMovie,
		Name:                 &name,
		Path:                 &path,
		MediaSourceContainer: &container,
		Size:                 &size,
		RunTimeTicks:         &ticks,
		TotalBitrate:         &bitrate,
		FileDevice:           &device,
		FileInode:            &inode,
		FileChangedAt:        &changed,
	}
	if presentationKey != "" {
		result.PresentationUniqueKey = &presentationKey
	}
	return result
}

func mediaSourcePageStream(width int) playback.MediaStream {
	height := width / 2
	return playback.MediaStream{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264", Width: &width, Height: &height}
}

func mediaSourcePageRecordIDs(records []playback.MediaSourceRecord) []dto.UUID {
	result := make([]dto.UUID, len(records))
	for index, record := range records {
		result[index] = record.ID
	}
	return result
}
