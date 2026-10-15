// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"context"
	"errors"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// TestPlaybackInfoApplicationStaticSourceSelectsFromOrderedGraph preserves the
// static-file controller's source-selection rules without starting a playback
// session or applying device negotiation.
func TestPlaybackInfoApplicationStaticSourceSelectsFromOrderedGraph(t *testing.T) {
	itemID := staticSourceUUID(t, "00000000-0000-0000-0000-0000000006f0")
	firstID := staticSourceUUID(t, "00000000-0000-0000-0000-0000000006f1")
	secondID := staticSourceUUID(t, "00000000-0000-0000-0000-0000000006f2")
	records := []playback.MediaSourceRecord{
		staticSourceRecord(firstID, "/media/first.mkv"),
		staticSourceRecord(secondID, "/media/alternate.mkv"),
	}
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources: staticSourceGraphReader{records: map[dto.UUID][]playback.MediaSourceRecord{itemID: records}},
	})

	for _, tc := range []struct {
		name, mediaSourceID, wantID string
		wantNotFound                bool
	}{
		{name: "absent source ID selects ordered first source", wantID: firstID.String()},
		{name: "exact case-sensitive source ID selects alternate", mediaSourceID: secondID.String(), wantID: secondID.String()},
		{name: "route item GUID falls back to ordered first source", mediaSourceID: itemID.Dashed(), wantID: firstID.String()},
		{name: "case-variant source ID does not select alternate", mediaSourceID: "00000000-0000-0000-0000-0000000006F2", wantNotFound: true},
		{name: "malformed source ID is missing", mediaSourceID: "not-a-guid", wantNotFound: true},
		{name: "different source GUID is missing", mediaSourceID: "00000000-0000-0000-0000-0000000006f3", wantNotFound: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := service.StaticSource(t.Context(), dto.NilUUID, itemID, tc.mediaSourceID)
			if tc.wantNotFound {
				if !errors.Is(err, playback.ErrItemNotFound) {
					t.Fatalf("StaticSource(%q) error = %v, want ErrItemNotFound", tc.mediaSourceID, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("StaticSource(%q): %v", tc.mediaSourceID, err)
			}
			if got.ID != tc.wantID {
				t.Errorf("StaticSource(%q).ID = %q, want %q", tc.mediaSourceID, got.ID, tc.wantID)
			}
		})
	}
}

func TestPlaybackInfoApplicationStaticSourceHidesMissingAndInaccessibleGraphs(t *testing.T) {
	itemID := staticSourceUUID(t, "00000000-0000-0000-0000-0000000006f4")
	userID := staticSourceUUID(t, "00000000-0000-0000-0000-0000000006f5")
	records := []playback.MediaSourceRecord{staticSourceRecord(itemID, "/media/only.mkv")}

	for _, tc := range []struct {
		name   string
		reader staticSourceGraphReader
		access playback.PlaybackItemAccessReader
		userID dto.UUID
	}{
		{
			name:   "empty graph",
			reader: staticSourceGraphReader{records: map[dto.UUID][]playback.MediaSourceRecord{itemID: {}}},
		},
		{
			name:   "missing item",
			reader: staticSourceGraphReader{errors: map[dto.UUID]error{itemID: playback.ErrItemNotFound}},
		},
		{
			name:   "inaccessible item",
			reader: staticSourceGraphReader{records: map[dto.UUID][]playback.MediaSourceRecord{itemID: records}},
			access: &staticSourceAccessReader{},
			userID: userID,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{Sources: tc.reader, Access: tc.access})
			_, err := service.StaticSource(t.Context(), tc.userID, itemID, "")
			if !errors.Is(err, playback.ErrItemNotFound) {
				t.Fatalf("StaticSource error = %v, want ErrItemNotFound", err)
			}
		})
	}
}

// TestPlaybackInfoApplicationStaticSourcePreservesDomainErrors makes the
// source boundary own the distinction between a missing item and an
// infrastructure failure. An incoming playback.ErrItemNotFound remains
// not-found. A distinct marker error returns unchanged so that the HTTP
// transport maps it to 500 rather than hiding it as a 404.
func TestPlaybackInfoApplicationStaticSourcePreservesDomainErrors(t *testing.T) {
	itemID := staticSourceUUID(t, "00000000-0000-0000-0000-0000000006f7")
	marker := errors.New("distinct infrastructure failure")
	for _, tc := range []struct {
		name      string
		sourceErr error
		want      error
	}{
		{name: "not-found passes through", sourceErr: playback.ErrItemNotFound, want: playback.ErrItemNotFound},
		{name: "distinct error unchanged", sourceErr: marker, want: marker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
				Sources: staticSourceGraphReader{errors: map[dto.UUID]error{itemID: tc.sourceErr}},
			})
			_, err := service.StaticSource(t.Context(), dto.NilUUID, itemID, "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("StaticSource source error %v mapped to %v, want %v", tc.sourceErr, err, tc.want)
			}
		})
	}
}

func TestPlaybackInfoApplicationStaticSourceAnonymousSkipsAccessFilter(t *testing.T) {
	itemID := staticSourceUUID(t, "00000000-0000-0000-0000-0000000006f6")
	access := &staticSourceAccessReader{err: errors.New("anonymous resolution must not consult access filter")}
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources: staticSourceGraphReader{records: map[dto.UUID][]playback.MediaSourceRecord{itemID: {staticSourceRecord(itemID, "/media/anonymous.mkv")}}},
		Access:  access,
	})

	got, err := service.StaticSource(t.Context(), dto.NilUUID, itemID, "")
	if err != nil {
		t.Fatalf("anonymous StaticSource: %v", err)
	}
	if got.ID != itemID.String() {
		t.Errorf("anonymous StaticSource ID = %q, want %q", got.ID, itemID.String())
	}
	if access.calls != 0 {
		t.Errorf("anonymous StaticSource access calls = %d, want 0", access.calls)
	}
}

type staticSourceGraphReader struct {
	records map[dto.UUID][]playback.MediaSourceRecord
	errors  map[dto.UUID]error
}

func (reader staticSourceGraphReader) MediaSourceRecords(_ context.Context, itemID dto.UUID) ([]playback.MediaSourceRecord, error) {
	if err := reader.errors[itemID]; err != nil {
		return nil, err
	}
	return reader.records[itemID], nil
}

type staticSourceAccessReader struct {
	accessible []dto.UUID
	err        error
	calls      int
}

func (reader *staticSourceAccessReader) AccessiblePlaybackItemIDs(_ context.Context, _ dto.UUID, _ []dto.UUID) ([]dto.UUID, error) {
	reader.calls++
	if reader.err != nil {
		return nil, reader.err
	}
	return reader.accessible, nil
}

func staticSourceRecord(id dto.UUID, path string) playback.MediaSourceRecord {
	container := "mkv"
	return playback.MediaSourceRecord{ID: id, Path: &path, Container: &container}
}

func staticSourceUUID(t *testing.T, value string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return id
}
