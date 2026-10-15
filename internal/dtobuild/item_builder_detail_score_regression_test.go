// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestBuilderDetailDefaultsUseSaveTimeEtagAndFolderZeroDate locks the values
// that direct item detail supplies independently of a caller's Fields list.
// A saved item gets BaseItem's DateLastSaved-ticks entity tag, while folders
// serialize both zero and persisted DateLastMediaAdded values in the exact
// stock seven-fractional-digit wire format.
func TestBuilderDetailDefaultsUseSaveTimeEtagAndFolderZeroDate(t *testing.T) {
	movieID, folderID, datedFolderID := builderUUID(t, 190), builderUUID(t, 191), builderUUID(t, 194)
	movieName, folderName, datedFolderName, moviePath := "Saved detail", "Unsaved folder", "Saved folder", "/library/saved-detail.mkv"
	saved := time.Date(2026, time.July, 27, 17, 55, 42, 0, time.UTC)
	added := time.Date(2026, time.July, 27, 17, 55, 42, 123_456_700, time.UTC)
	width, height := 320, 180
	values := []item.Item{
		{
			ID: movieID, Kind: item.KindMovie, IsMovie: true, Name: &movieName, Path: &moviePath,
			DateLastSaved: &saved, Width: &width, Height: &height,
		},
		{ID: folderID, Kind: item.KindFolder, IsFolder: true, Name: &folderName},
		{ID: datedFolderID, Kind: item.KindFolder, IsFolder: true, Name: &datedFolderName, DateLastMediaAdded: &added},
	}
	builder := dtobuild.NewBuilder(nil, projectionEnricher{values: map[dto.UUID]dtobuild.Enrichment{}})

	detailed, err := builder.BuildDetailed(t.Context(), values, store.ItemQueryDTOOptions{}, nil)
	if err != nil {
		t.Fatalf("build default detail projection: %v", err)
	}
	if got, want := len(detailed.Items), 3; got != want {
		t.Fatalf("detailed item count = %d, want %d", got, want)
	}

	movieWire, folderWire, datedFolderWire := builderWireObject(t, detailed.Items[0]), builderWireObject(t, detailed.Items[1]), builderWireObject(t, detailed.Items[2])
	for field, want := range map[string]string{
		"Etag":   `"db4ba06410389735531db1d1e8515235"`,
		"Width":  "320",
		"Height": "180",
	} {
		if got := string(movieWire[field]); got != want {
			t.Errorf("saved detail %s = %s, want %s", field, got, want)
		}
	}
	if got, want := string(folderWire["Etag"]), `"543b6ca4c9f21c87d81daf7a932499c0"`; got != want {
		t.Errorf("unsaved folder Etag = %s, want .NET zero-DateLastSaved tag %s", got, want)
	}
	if got, want := string(folderWire["DateLastMediaAdded"]), `"0001-01-01T00:00:00.0000000Z"`; got != want {
		t.Errorf("folder DateLastMediaAdded = %s, want explicit zero date %s", got, want)
	}
	if got, want := string(datedFolderWire["DateLastMediaAdded"]), `"2026-07-27T17:55:42.1234567Z"`; got != want {
		t.Errorf("persisted folder DateLastMediaAdded = %s, want exact seven-digit fraction %s", got, want)
	}
	for _, field := range []string{"Width", "Height"} {
		if _, found := folderWire[field]; found {
			t.Errorf("folder unexpectedly emitted %s", field)
		}
	}

	sparse, err := builder.Build(t.Context(), values, store.ItemQueryDTOOptions{}, nil)
	if err != nil {
		t.Fatalf("build sparse list projection: %v", err)
	}
	if got, want := sparse.TotalRecordCount, 3; got != want {
		t.Errorf("sparse TotalRecordCount = %d, want %d", got, want)
	}
	for _, projected := range sparse.Items {
		wire := builderWireObject(t, projected)
		for _, field := range []string{"Etag", "Width", "Height", "DateLastMediaAdded", "Path", "MediaSources", "MediaStreams"} {
			if _, found := wire[field]; found {
				t.Errorf("sparse item %s unexpectedly emitted %s", projected.ID, field)
			}
		}
	}
}

// TestBuilderDetailIncludesPathAndMediaByDefault protects DtoOptions() parity
// for direct item routes. Swiftfin's full-item refetch does not send Fields,
// so Path, MediaSources, and MediaStreams must still be populated. Sparse
// /Items projection remains controlled by the caller's Fields list.
func TestBuilderDetailIncludesPathAndMediaByDefault(t *testing.T) {
	id := builderUUID(t, 192)
	name, path := "Requested detail", "/library/requested-detail.mkv"
	width, height := 854, 480
	value := item.Item{ID: id, Kind: item.KindMovie, IsMovie: true, Name: &name, Path: &path, Width: &width, Height: &height}
	builder := dtobuild.NewBuilder(nil, projectionEnricher{values: map[dto.UUID]dtobuild.Enrichment{
		id: {
			MediaSources: []dto.MediaSourceInfo{{ID: builderString("requested-source")}},
			MediaStreams: []dto.MediaStreamInfo{{Index: 7, Type: dto.MediaStreamType("Video")}},
		},
	}})

	unrequested, err := builder.BuildDetailed(t.Context(), []item.Item{value}, store.ItemQueryDTOOptions{}, nil)
	if err != nil {
		t.Fatalf("build unrequested direct detail: %v", err)
	}
	unrequestedWire := builderWireObject(t, unrequested.Items[0])
	for field, want := range map[string]string{
		"Path":         `"/library/requested-detail.mkv"`,
		"MediaSources": `[{"Id":"requested-source"}]`,
		"MediaStreams": `[{"Type":"Video","Index":7}]`,
		"Width":        "854",
		"Height":       "480",
	} {
		if got := string(unrequestedWire[field]); got != want {
			t.Errorf("unrequested direct detail %s = %s, want %s", field, got, want)
		}
	}

	requested, err := builder.BuildDetailed(t.Context(), []item.Item{value}, store.ItemQueryDTOOptions{Fields: []string{"Path", "MediaSources", "MediaStreams"}}, nil)
	if err != nil {
		t.Fatalf("build requested direct detail: %v", err)
	}
	requestedWire := builderWireObject(t, requested.Items[0])
	for field, want := range map[string]string{
		"Path":         `"/library/requested-detail.mkv"`,
		"MediaSources": `[{"Id":"requested-source"}]`,
		"MediaStreams": `[{"Type":"Video","Index":7}]`,
	} {
		if got := string(requestedWire[field]); got != want {
			t.Errorf("requested direct detail %s = %s, want %s", field, got, want)
		}
	}

	sparse, err := builder.Build(t.Context(), []item.Item{value}, store.ItemQueryDTOOptions{}, nil)
	if err != nil {
		t.Fatalf("build sparse list item: %v", err)
	}
	sparseWire := builderWireObject(t, sparse.Items[0])
	for _, field := range []string{"Path", "MediaSources", "MediaStreams", "Width", "Height"} {
		if _, found := sparseWire[field]; found {
			t.Errorf("sparse list item unexpectedly emitted %s", field)
		}
	}
}

// TestBuilderDirectProjectionOmitsTopLevelSubtitleScores keeps generic item
// projections separate from latest-item subtitle ranking. MediaSources retain
// their playback-source score while /Items-style top-level streams do not.
func TestBuilderDirectProjectionOmitsTopLevelSubtitleScores(t *testing.T) {
	items := openBuilderItemStore(t)
	id := builderUUID(t, 193)
	name, path := "Subtitle score", "/library/subtitle-score.mkv"
	if err := items.Upsert(t.Context(), item.Item{ID: id, Kind: item.KindMovie, IsMovie: true, Name: &name, Path: &path}); err != nil {
		t.Fatalf("upsert subtitle-score fixture: %v", err)
	}
	if err := items.ReplaceStreams(t.Context(), id, []playback.MediaStream{
		{Index: 0, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: "/library/subtitle-score.en.srt"},
		{Index: 1, Type: playback.StreamTypeVideo, Codec: "mpeg4"},
		{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "srt"},
	}); err != nil {
		t.Fatalf("seed subtitle-score streams: %v", err)
	}
	loaded, err := items.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("load subtitle-score fixture: %v", err)
	}
	result, err := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)).Build(t.Context(), []item.Item{loaded}, store.ItemQueryDTOOptions{Fields: []string{"MediaSources", "MediaStreams"}}, nil)
	if err != nil {
		t.Fatalf("build subtitle-score projection: %v", err)
	}
	wire := builderWireObject(t, result.Items[0])

	var topLevel []builderScoredStream
	if err := json.Unmarshal(wire["MediaStreams"], &topLevel); err != nil {
		t.Fatalf("decode top-level streams: %v", err)
	}
	var sources []struct {
		MediaStreams []builderScoredStream `json:"MediaStreams"`
	}
	if err := json.Unmarshal(wire["MediaSources"], &sources); err != nil {
		t.Fatalf("decode media sources: %v", err)
	}
	if got, want := len(sources), 1; got != want {
		t.Fatalf("MediaSources = %d, want %d", got, want)
	}
	builderAssertNoSubtitleScores(t, "top-level", topLevel)
	builderAssertSourceSubtitleScores(t, sources[0].MediaStreams)
}

type builderScoredStream struct {
	Index int  `json:"Index"`
	Score *int `json:"Score"`
}

func builderAssertNoSubtitleScores(t *testing.T, scope string, streams []builderScoredStream) {
	t.Helper()
	if got, want := len(streams), 3; got != want {
		t.Fatalf("%s stream count = %d, want %d", scope, got, want)
	}
	for _, stream := range streams {
		if stream.Score != nil {
			t.Errorf("%s stream index %d Score = %d, want omitted", scope, stream.Index, *stream.Score)
		}
	}
}

func builderAssertSourceSubtitleScores(t *testing.T, streams []builderScoredStream) {
	t.Helper()
	if got, want := len(streams), 3; got != want {
		t.Fatalf("source stream count = %d, want %d", got, want)
	}
	for _, stream := range streams {
		switch stream.Index {
		case 0:
			if stream.Score == nil || *stream.Score != 111222 {
				t.Errorf("source external sidecar Score = %v, want 111222", stream.Score)
			}
		case 1, 2:
			if stream.Score != nil {
				t.Errorf("source stream index %d Score = %d, want omitted", stream.Index, *stream.Score)
			}
		default:
			t.Errorf("source unexpected stream index %d", stream.Index)
		}
	}
}
