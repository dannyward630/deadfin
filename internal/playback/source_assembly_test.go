// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// A playback source must retain the persisted media identity and every stream
// satellite: StreamBuilder makes its direct-play decision from this result.
func TestAssembleMediaSourceReadsPersistedItemAndSatellites(t *testing.T) {
	items := openPlaybackItemStore(t)
	id := playbackTestUUID(t, "cf602d5d-39a7-450e-9400-000000000401")
	path, name, container := "/media/Feature.mkv", "Feature", "mkv"
	size, runtime, bitrate := int64(4_000_000_001), int64(72_000_000_000), 9_500_000
	stored := item.Item{
		ID:           id,
		Kind:         item.KindMovie,
		Path:         &path,
		Name:         &name,
		Container:    &container,
		Size:         &size,
		RunTimeTicks: &runtime,
		TotalBitrate: &bitrate,
	}
	if err := items.Upsert(t.Context(), stored); err != nil {
		t.Fatalf("seed playback item: %v", err)
	}
	language, title := "eng", "English"
	streams := []playback.MediaStream{
		{Index: 0, Type: playback.StreamTypeVideo, Codec: "hevc", Width: playbackInt(3840), Height: playbackInt(2160), BitRate: playbackInt(8_000_000)},
		{Index: 1, Type: playback.StreamTypeAudio, Codec: "eac3", Language: &language, Title: &title, IsDefault: true, Channels: playbackInt(6), BitRate: playbackInt(640_000)},
	}
	attachments := []playback.MediaAttachment{{Index: 2, Codec: "mjpeg", CodecTag: "MJPG", FileName: "cover.jpg", MimeType: "image/jpeg", Comment: "cover"}}
	if err := items.ReplaceStreams(t.Context(), id, streams); err != nil {
		t.Fatalf("seed playback streams: %v", err)
	}
	if err := items.ReplaceAttachments(t.Context(), id, attachments); err != nil {
		t.Fatalf("seed playback attachments: %v", err)
	}

	got, err := playback.AssembleMediaSource(t.Context(), items, id)
	if err != nil {
		t.Fatalf("assemble media source: %v", err)
	}
	if got.ID != id.String() || got.Path != path || got.Name != name || got.Container != container {
		t.Fatalf("source identity = %#v, want ID %q path %q name %q container %q", got, id.String(), path, name, container)
	}
	if got.Size == nil || *got.Size != size || got.RunTimeTicks == nil || *got.RunTimeTicks != runtime || got.Bitrate == nil || *got.Bitrate != bitrate {
		t.Fatalf("source media facts = size:%v runtime:%v bitrate:%v, want %d %d %d", got.Size, got.RunTimeTicks, got.Bitrate, size, runtime, bitrate)
	}
	if got.Protocol != playback.MediaProtocolFile || got.Type != playback.MediaSourceDefault {
		t.Fatalf("source transport = protocol:%v type:%v, want file/default", got.Protocol, got.Type)
	}
	if !reflect.DeepEqual(got.MediaStreams, streams) {
		t.Fatalf("source streams = %#v, want %#v", got.MediaStreams, streams)
	}
	if !reflect.DeepEqual(got.MediaAttachments, attachments) {
		t.Fatalf("source attachments = %#v, want %#v", got.MediaAttachments, attachments)
	}
}

func TestAssembleMediaSourceRejectsUnknownItem(t *testing.T) {
	items := openPlaybackItemStore(t)
	missing := playbackTestUUID(t, "cf602d5d-39a7-450e-9400-000000000499")

	if _, err := playback.AssembleMediaSource(t.Context(), items, missing); err == nil {
		t.Fatalf("assemble missing item %s succeeded", missing)
	}
}

// Alternate-version playback must expose the requested source first, then keep
// the remaining graph in the client-visible upstream preference order. The
// primary is discoverable both through PrimaryVersionID and the presentation
// key, so its single source ID also proves graph traversal is deduplicated.
func TestAssembleMediaSourcesReturnsOrderedDeduplicatedVersionGraph(t *testing.T) {
	items := openPlaybackItemStore(t)
	family := "movie:alternate-family"
	primaryID := playbackTestUUID(t, "cf602d5d-39a7-450e-9400-000000000510")
	requestedID := playbackTestUUID(t, "cf602d5d-39a7-450e-9400-000000000511")
	highFlatID := playbackTestUUID(t, "cf602d5d-39a7-450e-9400-000000000512")
	threeDimensionalID := playbackTestUUID(t, "cf602d5d-39a7-450e-9400-000000000513")
	isoID := playbackTestUUID(t, "cf602d5d-39a7-450e-9400-000000000514")
	linkedID := playbackTestUUID(t, "cf602d5d-39a7-450e-9400-000000000515")

	primary := playbackGraphItem(primaryID, "Primary", family, item.VideoTypeVideoFile, 1920, nil, nil)
	requested := playbackGraphItem(requestedID, "Requested", family, item.VideoTypeIso, 640, nil, &primaryID)
	highFlat := playbackGraphItem(highFlatID, "High flat", family, item.VideoTypeVideoFile, 3840, nil, &primaryID)
	threeDimensional := playbackGraphItem(threeDimensionalID, "3D", family, item.VideoTypeVideoFile, 7680, playbackVideo3D(item.Video3DFormatHalfSideBySide), &primaryID)
	isoVersion := playbackGraphItem(isoID, "Keyed ISO", family, item.VideoTypeIso, 8000, nil, nil)
	linked := playbackGraphItem(linkedID, "Linked", family, item.VideoTypeVideoFile, 1920, nil, nil)
	primary.LinkedChildren = []item.LinkedChildRef{{ItemID: linkedID, Type: item.LinkedChildTypeLinkedAlternateVersion}}
	for _, value := range []item.Item{requested, highFlat, threeDimensional, isoVersion, linked, primary} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("seed graph item %s: %v", value.ID, err)
		}
		if err := items.ReplaceStreams(t.Context(), value.ID, []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264", Width: playbackInt(*value.Width)}}); err != nil {
			t.Fatalf("seed graph stream %s: %v", value.ID, err)
		}
	}

	got, err := playback.AssembleMediaSources(t.Context(), items, requestedID)
	if err != nil {
		t.Fatalf("assemble version graph: %v", err)
	}
	want := []dto.UUID{requestedID, highFlatID, primaryID, linkedID, threeDimensionalID, isoID}
	if len(got) != len(want) {
		t.Fatalf("source graph length = %d, want %d: %#v", len(got), len(want), got)
	}
	for index, id := range want {
		if got[index].ID != id.String() {
			t.Fatalf("source graph[%d] = %q, want %q; full graph %#v", index, got[index].ID, id.String(), got)
		}
	}
	if got[2].Type != playback.MediaSourceDefault || got[3].Type != playback.MediaSourceGrouping {
		t.Fatalf("primary/linked source types = %v/%v, want default/grouping", got[2].Type, got[3].Type)
	}
}

func playbackGraphItem(id dto.UUID, name, family string, videoType item.VideoType, width int, threeDimensional *item.Video3DFormat, primary *dto.UUID) item.Item {
	path, container := "/media/"+name+".mkv", "mkv"
	return item.Item{
		ID:                    id,
		Kind:                  item.KindMovie,
		Path:                  &path,
		Name:                  &name,
		Container:             &container,
		PresentationUniqueKey: &family,
		PrimaryVersionID:      primary,
		VideoType:             &videoType,
		Video3DFormat:         threeDimensional,
		Width:                 &width,
	}
}

func playbackVideo3D(value item.Video3DFormat) *item.Video3DFormat { return &value }

func openPlaybackItemStore(t *testing.T) *store.ItemStore {
	t.Helper()
	var stores store.Stores
	storetest.Run(t, func(t *testing.T) store.Stores {
		opened, err := sqlite.Open(t.Context(), ":memory:")
		if err != nil {
			t.Fatalf("open SQLite ItemStore: %v", err)
		}
		stores = opened
		return opened
	})
	return store.NewItemStore(stores)
}

func playbackTestUUID(t *testing.T, text string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(text)
	if err != nil {
		t.Fatalf("parse test UUID %q: %v", text, err)
	}
	return id
}

func playbackInt(value int) *int { return &value }
