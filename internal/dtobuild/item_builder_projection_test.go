// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestBuilderSelectsRequestedSatellites defends Field selection: the core item
// remains available while ParentId and expensive media/person/provider
// satellites are materialized only when a caller requests their named fields.
func TestBuilderSelectsRequestedSatellites(t *testing.T) {
	items := openBuilderItemStore(t)
	parentID := builderUUID(t, 10)
	itemID := builderUUID(t, 11)
	name := "Episode"
	parentName := "Series"
	if err := items.Upsert(t.Context(), item.Item{ID: parentID, Kind: item.KindSeries, IsFolder: true, Name: &parentName}); err != nil {
		t.Fatalf("upsert projection parent fixture: %v", err)
	}
	stored := item.Item{ID: itemID, Kind: item.KindEpisode, ParentID: &parentID, Name: &name}
	if err := items.Upsert(t.Context(), stored); err != nil {
		t.Fatalf("upsert projection fixture: %v", err)
	}
	loaded, err := items.Get(t.Context(), itemID)
	if err != nil {
		t.Fatalf("load projection fixture: %v", err)
	}

	builder := dtobuild.NewBuilder(items, projectionEnricher{values: map[dto.UUID]dtobuild.Enrichment{
		itemID: {
			MediaSources: []dto.MediaSourceInfo{{ID: builderString("source-a")}},
			MediaStreams: []dto.MediaStreamInfo{{Index: 7, Type: dto.MediaStreamType("Video")}},
			People:       []dto.BaseItemPerson{{ID: parentID, Name: "Performer", Role: "Lead", Type: "Actor"}},
			ProviderIDs:  map[string]string{"Tmdb": "42"},
		},
	}})
	result, err := builder.Build(t.Context(), []item.Item{loaded}, store.ItemQueryDTOOptions{Fields: []string{"ParentId", "MediaSources", "MediaStreams", "People", "ProviderIds"}}, nil)
	if err != nil {
		t.Fatalf("build selected projection: %v", err)
	}
	wire := builderWireObject(t, result.Items[0])
	for key, want := range map[string]string{
		"ParentId":     `"0000000000000000000000000000000a"`,
		"MediaSources": `[{"Id":"source-a"}]`,
		"MediaStreams": `[{"Type":"Video","Index":7}]`,
		"People":       `[{"Id":"0000000000000000000000000000000a","Name":"Performer","Role":"Lead","Type":"Actor"}]`,
		"ProviderIds":  `{"Tmdb":"42"}`,
	} {
		if got := string(wire[key]); got != want {
			t.Errorf("selected %s = %s, want %s", key, got, want)
		}
	}
	unselected, err := builder.Build(t.Context(), []item.Item{loaded}, store.ItemQueryDTOOptions{}, nil)
	if err != nil {
		t.Fatalf("build unselected projection: %v", err)
	}
	unselectedWire := builderWireObject(t, unselected.Items[0])
	for _, key := range []string{"ParentId", "MediaSources", "MediaStreams", "People", "ProviderIds"} {
		if _, found := unselectedWire[key]; found {
			t.Errorf("unselected projection unexpectedly emitted %s", key)
		}
	}
}

type projectionEnricher struct {
	values map[dto.UUID]dtobuild.Enrichment
}

func (e projectionEnricher) Enrich(_ context.Context, _ []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (map[dto.UUID]dtobuild.Enrichment, error) {
	return e.values, nil
}

// TestStoreEnricherProjectsCompletePersistedMedia verifies that the DTO
// projection consumes the complete source record atomically: source and
// top-level stream facts cannot regress to ID/index-only placeholders.
func TestStoreEnricherProjectsCompletePersistedMedia(t *testing.T) {
	items := openBuilderItemStore(t)
	id := builderUUID(t, 120)
	name, path, container := "Projection feature", "/library/projection-feature.mkv", "mkv"
	size, ticks, bitrate := int64(7_000_000), int64(123_456_789), 9_000_000
	if err := items.Upsert(t.Context(), item.Item{ID: id, Kind: item.KindMovie, IsMovie: true, Name: &name, Path: &path, Container: &container, Size: &size, RunTimeTicks: &ticks, TotalBitrate: &bitrate}); err != nil {
		t.Fatalf("upsert full media projection fixture: %v", err)
	}
	if err := items.ReplaceStreams(t.Context(), id, []playback.MediaStream{
		{Index: 0, Type: playback.StreamTypeVideo, Codec: "hevc", Language: builderString("eng"), BitRate: builderInt(9_000_000), Width: builderInt(3840), Height: builderInt(2160), IsDefault: true, TimeBase: "1/90000"},
		{Index: 1, Type: playback.StreamTypeAudio, Codec: "eac3", Language: builderString("eng"), Channels: builderInt(6), IsDefault: true},
		{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "subrip", Language: builderString("spa"), IsForced: true, IsExternal: true},
	}); err != nil {
		t.Fatalf("replace full media streams: %v", err)
	}
	if err := items.ReplaceAttachments(t.Context(), id, []playback.MediaAttachment{{Index: 3, Codec: "mjpeg", CodecTag: "MJPG", Comment: "cover", FileName: "cover.jpg", MimeType: "image/jpeg"}}); err != nil {
		t.Fatalf("replace full media attachments: %v", err)
	}
	loaded, err := items.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("load full media fixture: %v", err)
	}
	result, err := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)).Build(t.Context(), []item.Item{loaded}, store.ItemQueryDTOOptions{Fields: []string{"MediaSources", "MediaStreams"}}, nil)
	if err != nil {
		t.Fatalf("build full media projection: %v", err)
	}
	wantSources, err := playback.AssembleMediaSources(t.Context(), items, id)
	if err != nil {
		t.Fatalf("assemble pinned full media source: %v", err)
	}
	wantSourceBytes, err := json.Marshal(wantSources)
	if err != nil {
		t.Fatalf("marshal pinned media sources: %v", err)
	}
	wantTopLevelStreams, err := items.Streams(t.Context(), id)
	if err != nil {
		t.Fatalf("load persisted top-level streams: %v", err)
	}
	wantStreamBytes := builderDirectStreamBytes(t, wantTopLevelStreams)
	wire := builderWireObject(t, result.Items[0])
	assertBuilderMediaJSON(t, "MediaSources", wire["MediaSources"], wantSourceBytes)
	assertBuilderMediaJSON(t, "MediaStreams", wire["MediaStreams"], wantStreamBytes)
	withoutFields, err := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)).Build(t.Context(), []item.Item{loaded}, store.ItemQueryDTOOptions{}, nil)
	if err != nil {
		t.Fatalf("build unrequested media projection: %v", err)
	}
	withoutWire := builderWireObject(t, withoutFields.Items[0])
	for _, field := range []string{"MediaSources", "MediaStreams"} {
		if _, found := withoutWire[field]; found {
			t.Errorf("unrequested media projection unexpectedly emitted %s", field)
		}
	}
}

// TestStoreEnricherPreservesAlternateSourceTypesAcrossSingleAndBatchedGraphs
// keeps the page-scoped projection equal to the single-item playback graph.
// A local alternate is an ordinary source, while an externally linked alternate
// is a grouping source; neither identity nor that distinction may be lost when
// MediaSources are loaded for a DTO page.
func TestStoreEnricherPreservesAlternateSourceTypesAcrossSingleAndBatchedGraphs(t *testing.T) {
	items := openBuilderItemStore(t)
	primaryID, localID, linkedID := builderUUID(t, 130), builderUUID(t, 131), builderUUID(t, 132)
	primaryName, localName, linkedName := "Feature 2160p", "Feature 1080p", "Feature linked 720p"
	primaryPath, localPath, linkedPath := "/library/feature-2160p.mkv", "/library/feature-1080p.mkv", "/library/feature-linked-720p.mkv"
	container := "mkv"
	localOrder, linkedOrder := 10, 20
	primary := item.Item{
		ID:        primaryID,
		Kind:      item.KindMovie,
		IsMovie:   true,
		Name:      &primaryName,
		Path:      &primaryPath,
		Container: &container,
		LinkedChildren: []item.LinkedChildRef{
			{ItemID: localID, Type: item.LinkedChildTypeLocalAlternateVersion, SortOrder: &localOrder},
			{ItemID: linkedID, Type: item.LinkedChildTypeLinkedAlternateVersion, SortOrder: &linkedOrder},
		},
	}
	local := item.Item{ID: localID, Kind: item.KindMovie, IsMovie: true, Name: &localName, Path: &localPath, Container: &container}
	linked := item.Item{ID: linkedID, Kind: item.KindMovie, IsMovie: true, Name: &linkedName, Path: &linkedPath, Container: &container}
	for _, value := range []item.Item{local, linked, primary} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert alternate source fixture %s: %v", value.ID, err)
		}
	}
	for _, source := range []struct {
		id    dto.UUID
		width int
	}{
		{id: primaryID, width: 2160},
		{id: localID, width: 1080},
		{id: linkedID, width: 720},
	} {
		if err := items.ReplaceStreams(t.Context(), source.id, []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Width: builderInt(source.width)}}); err != nil {
			t.Fatalf("seed alternate source stream %s: %v", source.id, err)
		}
	}

	wantIDs := []dto.UUID{primaryID, localID, linkedID}
	wantTypes := []playback.MediaSourceType{playback.MediaSourceDefault, playback.MediaSourceDefault, playback.MediaSourceGrouping}
	single, err := items.MediaSourceRecords(t.Context(), primaryID)
	if err != nil {
		t.Fatalf("load single-item alternate source graph: %v", err)
	}
	assertBuilderSourceRecords(t, "single-item graph", single, wantIDs, wantTypes)

	batched, err := items.MediaSourceRecordsPage(t.Context(), []dto.UUID{primaryID})
	if err != nil {
		t.Fatalf("load batched alternate source graph: %v", err)
	}
	assertBuilderSourceRecords(t, "batched graph", batched[primaryID], wantIDs, wantTypes)
	if !reflect.DeepEqual(batched[primaryID], single) {
		t.Fatalf("batched source records = %#v, want identical single-item records %#v", batched[primaryID], single)
	}

	loaded, err := items.Get(t.Context(), primaryID)
	if err != nil {
		t.Fatalf("load alternate source DTO fixture: %v", err)
	}
	result, err := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)).Build(t.Context(), []item.Item{loaded}, store.ItemQueryDTOOptions{Fields: []string{"MediaSources"}}, nil)
	if err != nil {
		t.Fatalf("build alternate source DTO projection: %v", err)
	}
	if got, want := len(result.Items), 1; got != want {
		t.Fatalf("alternate source DTO count = %d, want %d", got, want)
	}
	assertBuilderProjectedSources(t, result.Items[0].MediaSources, wantIDs, []dto.MediaSourceType{"Default", "Default", "Grouping"})
}

func assertBuilderSourceRecords(t *testing.T, name string, got []playback.MediaSourceRecord, wantIDs []dto.UUID, wantTypes []playback.MediaSourceType) {
	t.Helper()
	if len(got) != len(wantIDs) || len(wantIDs) != len(wantTypes) {
		t.Fatalf("%s sources = %#v, want %d records", name, got, len(wantIDs))
	}
	for index := range wantIDs {
		if got[index].ID != wantIDs[index] || got[index].Type != wantTypes[index] {
			t.Fatalf("%s source[%d] = ID:%s Type:%v, want ID:%s Type:%v; source ordering must remain stable", name, index, got[index].ID, got[index].Type, wantIDs[index], wantTypes[index])
		}
	}
}

func assertBuilderProjectedSources(t *testing.T, got []dto.MediaSourceInfo, wantIDs []dto.UUID, wantTypes []dto.MediaSourceType) {
	t.Helper()
	if len(got) != len(wantIDs) || len(wantIDs) != len(wantTypes) {
		t.Fatalf("projected sources = %#v, want %d records", got, len(wantIDs))
	}
	for index := range wantIDs {
		if got[index].ID == nil || got[index].Type == nil || *got[index].ID != wantIDs[index].String() || *got[index].Type != wantTypes[index] {
			t.Fatalf("projected source[%d] = ID:%v Type:%v, want ID:%s Type:%s; source ordering and link type must match the graph", index, got[index].ID, got[index].Type, wantIDs[index], wantTypes[index])
		}
	}
}

func builderString(value string) *string { return &value }
func builderInt(value int) *int          { return &value }

func assertBuilderMediaJSON(t *testing.T, name string, actual, expected []byte) {
	t.Helper()
	var got, want any
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatalf("decode %s actual JSON: %v", name, err)
	}
	if err := json.Unmarshal(expected, &want); err != nil {
		t.Fatalf("decode %s pinned JSON: %v", name, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %s, want persisted source shape %s", name, actual, expected)
	}
}

// builderDirectStreamBytes preserves the stored stream shape for generic
// /Items-style projections. Static subtitle scores belong to MediaSources and
// the latest route, not this reusable builder boundary.
func builderDirectStreamBytes(t *testing.T, streams []playback.MediaStream) []byte {
	t.Helper()
	encoded, err := json.Marshal(streams)
	if err != nil {
		t.Fatalf("marshal direct streams: %v", err)
	}
	return encoded
}

// TestBuilderEmitsSelectedEmptyCollectionsOnlyWhenRequested protects the
// stock distinction between an explicitly selected-but-empty satellite and an
// unselected satellite. Empty collection facts are API data, not an invitation
// to expand every sparse item response.
func TestBuilderEmitsSelectedEmptyCollectionsOnlyWhenRequested(t *testing.T) {
	id := builderUUID(t, 140)
	name := "Empty selected movie"
	value := item.Item{ID: id, Kind: item.KindMovie, Name: &name}
	builder := dtobuild.NewBuilder(nil, nil)
	selected, err := builder.Build(t.Context(), []item.Item{value}, store.ItemQueryDTOOptions{Fields: []string{
		"Genres", "Tags", "ProductionLocations", "Taglines", "MediaSources", "MediaStreams", "People", "ProviderIds",
	}}, nil)
	if err != nil {
		t.Fatalf("build selected empty projection: %v", err)
	}
	selectedWire := builderWireObject(t, selected.Items[0])
	for key, want := range map[string]string{
		"Genres":              "[]",
		"GenreItems":          "[]",
		"Tags":                "[]",
		"ProductionLocations": "[]",
		"Taglines":            "[]",
		"MediaSources":        "[]",
		"MediaStreams":        "[]",
		"People":              "[]",
		"ProviderIds":         "{}",
	} {
		if got, found := selectedWire[key]; !found || string(got) != want {
			t.Errorf("selected empty %s = %s (present=%t), want %s", key, got, found, want)
		}
	}

	unselected, err := builder.Build(t.Context(), []item.Item{value}, store.ItemQueryDTOOptions{}, nil)
	if err != nil {
		t.Fatalf("build unselected empty projection: %v", err)
	}
	unselectedWire := builderWireObject(t, unselected.Items[0])
	for _, key := range []string{"Genres", "GenreItems", "Tags", "ProductionLocations", "Taglines", "MediaSources", "MediaStreams", "People", "ProviderIds"} {
		if _, found := unselectedWire[key]; found {
			t.Errorf("unselected empty projection unexpectedly emitted %s", key)
		}
	}
}

// TestStoreEnricherProjectsCanonicalItemRelationsAndWireFacts pins the
// persisted graph facts stock exposes for every canonical media kind. In
// particular, runtime relation IDs and names come from the graph rather than
// filename/path inference, and a video-only field never leaks onto Audio.
func TestStoreEnricherProjectsCanonicalItemRelationsAndWireFacts(t *testing.T) {
	items := openBuilderItemStore(t)
	seriesID, seasonID := builderUUID(t, 150), builderUUID(t, 151)
	albumID, artistID := builderUUID(t, 152), builderUUID(t, 153)
	episodeID, audioID := builderUUID(t, 154), builderUUID(t, 155)
	movieID, photoID, discID, genreID := builderUUID(t, 156), builderUUID(t, 157), builderUUID(t, 158), builderUUID(t, 159)
	seriesName, seasonName := "The Stored Series", "Stored Season"
	albumName, artistName := "Stored Album", "Stored Artist"
	episodeName, audioName := "Stored Episode", "Stored Track"
	movieName, photoName, discName, genreName := "Stored Movie", "Stored Photo", "Stored Disc", "Stored Genre"
	videoFile, bluray := item.VideoTypeVideoFile, item.VideoTypeBluRay
	orientation := item.ImageOrientationRightBottom

	for _, value := range []item.Item{
		{ID: seriesID, Kind: item.KindSeries, IsFolder: true, Name: &seriesName},
		{ID: seasonID, Kind: item.KindSeason, IsFolder: true, ParentID: &seriesID, SeriesID: &seriesID, Name: &seasonName},
		{ID: albumID, Kind: item.KindMusicAlbum, IsFolder: true, Name: &albumName, Values: item.ItemValues{AlbumArtists: []string{artistName}}},
		{ID: artistID, Kind: item.KindMusicArtist, IsFolder: true, Name: &artistName},
		{ID: genreID, Kind: item.KindGenre, Name: &genreName},
		{ID: episodeID, Kind: item.KindEpisode, ParentID: &seasonID, SeriesID: &seriesID, SeasonID: &seasonID, Name: &episodeName, VideoType: &videoFile},
		{ID: audioID, Kind: item.KindAudio, ParentID: &albumID, Name: &audioName, Values: item.ItemValues{Artists: []string{artistName}, AlbumArtists: []string{artistName}}, VideoType: &videoFile},
		{ID: movieID, Kind: item.KindMovie, Name: &movieName, VideoType: &videoFile, Values: item.ItemValues{Genres: []string{genreName}}},
		{ID: photoID, Kind: item.KindPhoto, Name: &photoName, Extra: item.ExtraFields{Photo: &item.PhotoExtra{Orientation: &orientation}}},
		{ID: discID, Kind: item.KindMovie, Name: &discName, VideoType: &bluray, IsPlaceHolder: true},
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert canonical projection fixture %s: %v", value.Kind, err)
		}
	}
	values := []item.Item{
		builderProjectionLoad(t, items, episodeID),
		builderProjectionLoad(t, items, audioID),
		builderProjectionLoad(t, items, movieID),
		builderProjectionLoad(t, items, photoID),
		builderProjectionLoad(t, items, discID),
	}
	result, err := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)).Build(t.Context(), values, store.ItemQueryDTOOptions{Fields: []string{"Genres"}}, nil)
	if err != nil {
		t.Fatalf("build canonical item projection: %v", err)
	}
	wires := make(map[dto.UUID]map[string]json.RawMessage, len(result.Items))
	for _, projected := range result.Items {
		wires[projected.ID] = builderWireObject(t, projected)
	}

	for key, want := range map[string]string{
		"SeriesId":   `"00000000000000000000000000000096"`,
		"SeasonId":   `"00000000000000000000000000000097"`,
		"SeriesName": `"The Stored Series"`,
		"SeasonName": `"Stored Season"`,
		"VideoType":  `"VideoFile"`,
	} {
		if got := string(wires[episodeID][key]); got != want {
			t.Errorf("episode %s = %s, want runtime graph fact %s", key, got, want)
		}
	}
	for key, want := range map[string]string{
		"Album":        `"Stored Album"`,
		"AlbumId":      `"00000000000000000000000000000098"`,
		"Artists":      `["Stored Artist"]`,
		"ArtistItems":  `[{"Name":"Stored Artist","Id":"00000000000000000000000000000099"}]`,
		"AlbumArtist":  `"Stored Artist"`,
		"AlbumArtists": `[{"Name":"Stored Artist","Id":"00000000000000000000000000000099"}]`,
		"HasLyrics":    "false",
	} {
		if got := string(wires[audioID][key]); got != want {
			t.Errorf("audio %s = %s, want persisted graph fact %s", key, got, want)
		}
	}
	if _, found := wires[audioID]["VideoType"]; found {
		t.Errorf("audio unexpectedly emitted VideoType: %s", wires[audioID]["VideoType"])
	}
	if got, want := string(wires[movieID]["VideoType"]), `"VideoFile"`; got != want {
		t.Errorf("movie VideoType = %s, want %s", got, want)
	}
	for key, want := range map[string]string{"Genres": `["Stored Genre"]`, "GenreItems": `[{"Name":"Stored Genre","Id":"` + genreID.String() + `"}]`} {
		if got := string(wires[movieID][key]); got != want {
			t.Errorf("movie %s = %s, want persisted genre graph fact %s", key, got, want)
		}
	}
	if got, want := string(wires[photoID]["ImageOrientation"]), `"RightBottom"`; got != want {
		t.Errorf("photo ImageOrientation = %s, want %s", got, want)
	}
	for key, want := range map[string]string{"IsPlaceHolder": "true", "VideoType": `"BluRay"`} {
		if got := string(wires[discID][key]); got != want {
			t.Errorf("BluRay placeholder %s = %s, want %s", key, got, want)
		}
	}
}

func builderProjectionLoad(t *testing.T, items *store.ItemStore, id dto.UUID) item.Item {
	t.Helper()
	value, err := items.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("load canonical projection fixture %s: %v", id, err)
	}
	return value
}

// TestBuilderBatchesRelatedGraphEnrichment makes the page boundary explicit:
// relation enrichment receives the complete result once, rather than causing
// a query per episode/audio item.
func TestBuilderBatchesRelatedGraphEnrichment(t *testing.T) {
	first, second, third := builderUUID(t, 160), builderUUID(t, 161), builderUUID(t, 162)
	enricher := &builderPageEnricher{}
	values := []item.Item{{ID: first, Kind: item.KindEpisode}, {ID: second, Kind: item.KindAudio}, {ID: third, Kind: item.KindAudio}}
	result, err := dtobuild.NewBuilder(nil, enricher).Build(t.Context(), values, store.ItemQueryDTOOptions{}, nil)
	if err != nil {
		t.Fatalf("build batched related graph projection: %v", err)
	}
	if got, want := enricher.calls, 1; got != want {
		t.Errorf("related graph enricher calls = %d, want one page-level call", got)
	}
	if got, want := enricher.ids, []dto.UUID{first, second, third}; !reflect.DeepEqual(got, want) {
		t.Errorf("related graph enrichment IDs = %v, want page IDs %v", got, want)
	}
	if got, want := len(result.Items), len(values); got != want {
		t.Errorf("batched projection length = %d, want %d", got, want)
	}
}

type builderPageEnricher struct {
	calls int
	ids   []dto.UUID
}

func (e *builderPageEnricher) Enrich(_ context.Context, values []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (map[dto.UUID]dtobuild.Enrichment, error) {
	e.calls++
	e.ids = make([]dto.UUID, len(values))
	for index := range values {
		e.ids[index] = values[index].ID
	}
	return make(map[dto.UUID]dtobuild.Enrichment, len(values)), nil
}
