// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// The series selection is the most recently played episode with a saved index,
// for this user only, with that episode's streams.
func TestItemStoreSeriesStreamSelectionsUsesLatestSavedEpisode(t *testing.T) {
	items := openNextUpItemStore(t)
	user, other := nextUpID(t, 1), nextUpID(t, 2)
	series, emptySeries := nextUpID(t, 3), nextUpID(t, 4)
	e1, e2, e3, e4 := nextUpID(t, 10), nextUpID(t, 11), nextUpID(t, 12), nextUpID(t, 13)
	episode := func(id, series dto.UUID, key string, number int) item.Item {
		// The store derives series_id from the parent chain.
		value := nextUpEpisode(id, series, key, 1, number)
		value.ParentID = &series
		return value
	}
	for _, value := range []item.Item{
		{ID: series, Kind: item.KindSeries, PresentationUniqueKey: nextUpString("series")},
		{ID: emptySeries, Kind: item.KindSeries, PresentationUniqueKey: nextUpString("empty")},
		episode(e1, series, "series", 1),
		episode(e2, series, "series", 2),
		episode(e3, series, "series", 3),
		episode(e4, emptySeries, "empty", 1),
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}
	japanese := "jpn"
	e2Streams := []playback.MediaStream{
		{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
		{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", Language: &japanese},
	}
	if err := items.ReplaceStreams(t.Context(), e2, e2Streams); err != nil {
		t.Fatalf("replace streams: %v", err)
	}

	day := func(value int) *time.Time {
		at := time.Date(2026, 10, value, 0, 0, 0, 0, time.UTC)
		return &at
	}
	one, two, off := 1, 2, -1
	for _, data := range []store.UserData{
		{UserID: user, CustomKey: "e1", ItemID: e1, LastPlayed: day(1), AudioStreamIndex: &two, SubtitleStreamIndex: &two},
		{UserID: user, CustomKey: "e2", ItemID: e2, LastPlayed: day(2), AudioStreamIndex: &one, SubtitleStreamIndex: &off},
		// A later episode without a saved index does not hide the saved choice.
		{UserID: user, CustomKey: "e3", ItemID: e3, LastPlayed: day(3)},
		{UserID: user, CustomKey: "e4", ItemID: e4, LastPlayed: day(3)},
		// Another user's later choice does not apply.
		{UserID: other, CustomKey: "e1", ItemID: e1, LastPlayed: day(4), AudioStreamIndex: &two},
	} {
		if err := items.UpsertUserData(t.Context(), data); err != nil {
			t.Fatalf("upsert user data %q: %v", data.CustomKey, err)
		}
	}

	got, err := items.SeriesStreamSelections(t.Context(), user, []dto.UUID{series, emptySeries, series})
	if err != nil {
		t.Fatalf("series stream selections: %v", err)
	}
	if _, found := got[emptySeries]; found || len(got) != 1 {
		t.Fatalf("selections = %#v, want only the watched series", got)
	}
	selection := got[series]
	if selection.AudioStreamIndex == nil || *selection.AudioStreamIndex != 1 {
		t.Errorf("AudioStreamIndex = %v, want 1", selection.AudioStreamIndex)
	}
	if selection.SubtitleStreamIndex == nil || *selection.SubtitleStreamIndex != -1 {
		t.Errorf("SubtitleStreamIndex = %v, want -1", selection.SubtitleStreamIndex)
	}
	if len(selection.Streams) != 2 || selection.Streams[1].Language == nil || *selection.Streams[1].Language != japanese {
		t.Errorf("Streams = %#v, want the E02 streams", selection.Streams)
	}
}
