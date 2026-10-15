// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestFreshAudioScanPersistsGraphAndProjectsItems follows a tagged audio probe
// through a real scan, durable graph, ItemStore page, StoreEnricher, and the
// public /Items renderer. It protects the fresh-corpus audio cases: graph
// identity, source runtime, zero PremiereDate, paging/sort order, both wire
// profiles, and batch-only enrichment.
func TestFreshAudioScanPersistsGraphAndProjectsItems(t *testing.T) {
	counting := openFreshAudioCountingSQLite(t)
	stores := counting.Stores()
	items := store.NewItemStore(stores)
	music := filepath.Join(t.TempDir(), "music")
	for number := 1; number <= 5; number++ {
		writeScannerFile(t, filepath.Join(music, "Goldenrec Generator", "Synthetic Album", freshAudioTrackName(number)), "synthetic tagged audio")
	}

	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("create fresh audio topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close fresh audio topology: %v", err)
		}
	})
	if _, err := topology.AddVirtualFolder(t.Context(), "Fresh Music", "music", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: music}}}); err != nil {
		t.Fatalf("add fresh audio source: %v", err)
	}
	scanner, err := library.NewScanner(topology, items, library.NewDefaultChain(naming.DefaultOptions()), library.ScannerOptions{
		Concurrency: 1,
		MediaProbe:  freshAudioProbe{},
	})
	if err != nil {
		t.Fatalf("create fresh audio scanner: %v", err)
	}
	t.Cleanup(func() {
		if err := scanner.Close(); err != nil {
			t.Errorf("close fresh audio scanner: %v", err)
		}
	})
	result, err := scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan fresh tagged audio: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("fresh tagged audio scan errors = %v", result.Errors)
	}

	persisted := freshAudioPersistedFixture(t, items)
	routes := router.New()
	handlers.RegisterItems(routes, items, dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)), clock.NewReal())

	const pagePath = "/Items?IncludeItemTypes=Audio&Recursive=true&Fields=Path,MediaStreams&EnableImages=false&EnableUserData=false&EnableTotalRecordCount=true&SortBy=IndexNumber,SortName&SortOrder=Ascending,Ascending&StartIndex=2&Limit=3"
	var pageQueries int64
	for _, profile := range []freshAudioProfile{
		{
			name:     "native",
			itemsKey: "Items", totalKey: "TotalRecordCount", startKey: "StartIndex",
			idKey: "Id", albumKey: "Album", albumIDKey: "AlbumId", artistsKey: "Artists", artistItemsKey: "ArtistItems", albumArtistKey: "AlbumArtist", albumArtistsKey: "AlbumArtists",
			runtimeKey: "RunTimeTicks", premiereKey: "PremiereDate", indexKey: "IndexNumber", streamsKey: "MediaStreams",
		},
		{
			name: "camel", accept: `application/json; profile="CamelCase"`,
			itemsKey: "items", totalKey: "totalRecordCount", startKey: "startIndex",
			idKey: "id", albumKey: "album", albumIDKey: "albumId", artistsKey: "artists", artistItemsKey: "artistItems", albumArtistKey: "albumArtist", albumArtistsKey: "albumArtists",
			runtimeKey: "runTimeTicks", premiereKey: "premiereDate", indexKey: "indexNumber", streamsKey: "mediaStreams",
		},
	} {
		t.Run(profile.name, func(t *testing.T) {
			counting.ResetReaderQueries()
			body := freshAudioItemsRequest(t, routes, pagePath, profile.accept)
			if got := freshAudioInt(t, body[profile.totalKey]); got != 5 {
				t.Fatalf("%s total record count = %d, want 5", profile.name, got)
			}
			if got := freshAudioInt(t, body[profile.startKey]); got != 2 {
				t.Fatalf("%s start index = %d, want 2", profile.name, got)
			}
			rows := freshAudioRows(t, body[profile.itemsKey])
			if got := len(rows); got != 3 {
				t.Fatalf("%s page size = %d, want 3", profile.name, got)
			}
			for offset, row := range rows {
				want := persisted.tracks[offset+2]
				freshAudioAssertProjectedTrack(t, profile, row, want, persisted)
			}
			queries := counting.ReaderQueries()
			if queries == 0 {
				t.Fatal("/Items made no persistent reader queries")
			}
			if pageQueries == 0 {
				pageQueries = queries
			} else if queries != pageQueries {
				t.Fatalf("%s profile reader query count = %d, want native profile count %d", profile.name, queries, pageQueries)
			}
		})
	}

	counting.ResetReaderQueries()
	onePage := freshAudioItemsRequest(t, routes, "/Items?IncludeItemTypes=Audio&Recursive=true&Fields=Path,MediaStreams&EnableImages=false&EnableUserData=false&EnableTotalRecordCount=true&SortBy=IndexNumber,SortName&SortOrder=Ascending,Ascending&Limit=1", "")
	if rows := freshAudioRows(t, onePage["Items"]); len(rows) != 1 {
		t.Fatalf("one-track page size = %d, want 1", len(rows))
	}
	if got := counting.ReaderQueries(); got != pageQueries {
		t.Fatalf("one-track reader query count = %d, want three-track count %d; enrichment must be batched rather than N+1", got, pageQueries)
	}
}

type freshAudioProbe struct{}

func (freshAudioProbe) Probe(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	if input.Item.Kind != item.KindAudio {
		return library.MediaProbeResult{}, nil
	}
	number := freshAudioTrackNumber(*input.Item.Path)
	runtime, bitrate, channels, sampleRate := int64(10_000_000), 64_000, 1, 44_100
	name, album, artist := "Synthetic Track "+freshAudioDecimal(number), "Synthetic Album", "Goldenrec Generator"
	index := number
	return library.MediaProbeResult{
		Name:         name,
		Album:        album,
		Artists:      []string{artist},
		AlbumArtists: []string{artist},
		IndexNumber:  &index,
		RunTimeTicks: &runtime,
		TotalBitrate: &bitrate,
		Streams: []playback.MediaStream{{
			Index: 0, Type: playback.StreamTypeAudio, Codec: "mp3", BitRate: &bitrate, Channels: &channels, SampleRate: &sampleRate,
		}},
	}, nil
}

type freshAudioFixture struct {
	album  item.Item
	artist item.Item
	tracks []item.Item
}

func freshAudioPersistedFixture(t *testing.T, items *store.ItemStore) freshAudioFixture {
	t.Helper()
	page, err := items.Query(t.Context(), store.ItemQuery{
		IncludeItemTypes:       []item.Kind{item.KindAudio},
		Recursive:              true,
		SortBy:                 []store.ItemSortBy{store.ItemSortByIndexNumber, store.ItemSortBySortName},
		SortOrder:              []store.SortOrder{store.SortOrderAscending, store.SortOrderAscending},
		EnableTotalRecordCount: true,
	})
	if err != nil {
		t.Fatalf("query persisted fresh audio: %v", err)
	}
	if len(page.Items) != 5 || page.TotalRecordCount != 5 {
		t.Fatalf("persisted fresh audio page = %#v, want five tracks", page)
	}
	first := page.Items[0]
	if first.ParentID == nil {
		t.Fatalf("first persisted audio has no album parent: %#v", first)
	}
	album, err := items.Get(t.Context(), *first.ParentID)
	if err != nil {
		t.Fatalf("get persisted audio album: %v", err)
	}
	if album.Kind != item.KindMusicAlbum || album.Name == nil || *album.Name != "Synthetic Album" {
		t.Fatalf("persisted audio album = %#v, want MusicAlbum Synthetic Album", album)
	}
	artists, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMusicArtist}, Recursive: true})
	if err != nil || len(artists.Items) != 1 {
		t.Fatalf("query persisted audio artists = %#v, %v; want one MusicArtist", artists, err)
	}
	artist := artists.Items[0]
	if artist.Name == nil || *artist.Name != "Goldenrec Generator" {
		t.Fatalf("persisted MusicArtist = %#v, want Goldenrec Generator", artist)
	}
	for index, track := range page.Items {
		if track.ParentID == nil || *track.ParentID != album.ID || track.RunTimeTicks == nil || *track.RunTimeTicks != 10_000_000 || track.IndexNumber == nil || *track.IndexNumber != index+1 || !reflect.DeepEqual(track.Values.Artists, []string{"Goldenrec Generator"}) || !reflect.DeepEqual(track.Values.AlbumArtists, []string{"Goldenrec Generator"}) {
			t.Fatalf("persisted track %d = %#v, want durable album parent, runtime, index, and artist values", index+1, track)
		}
	}
	return freshAudioFixture{album: album, artist: artist, tracks: page.Items}
}

type freshAudioProfile struct {
	name, accept                                     string
	itemsKey, totalKey, startKey, idKey              string
	albumKey, albumIDKey, artistsKey, artistItemsKey string
	albumArtistKey, albumArtistsKey                  string
	runtimeKey, premiereKey, indexKey, streamsKey    string
}

func freshAudioItemsRequest(t *testing.T, routes http.Handler, path, accept string) map[string]json.RawMessage {
	t.Helper()
	record := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	routes.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, record.Code, record.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /Items response: %v; body=%s", err, record.Body.String())
	}
	return body
}

func freshAudioRows(t *testing.T, raw json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode Items rows %s: %v", raw, err)
	}
	return rows
}

func freshAudioAssertProjectedTrack(t *testing.T, profile freshAudioProfile, row map[string]json.RawMessage, want item.Item, fixture freshAudioFixture) {
	t.Helper()
	if got := freshAudioUUID(t, row[profile.idKey]); got != want.ID {
		t.Fatalf("%s item ID = %s, want %s", profile.name, got, want.ID)
	}
	if got := freshAudioString(t, row[profile.albumKey]); got != "Synthetic Album" || freshAudioUUID(t, row[profile.albumIDKey]) != fixture.album.ID {
		t.Fatalf("%s album graph projection = %s/%s, want Synthetic Album/%s", profile.name, row[profile.albumKey], row[profile.albumIDKey], fixture.album.ID)
	}
	if got := freshAudioStrings(t, row[profile.artistsKey]); !reflect.DeepEqual(got, []string{"Goldenrec Generator"}) {
		t.Fatalf("%s artists = %v, want Goldenrec Generator", profile.name, got)
	}
	if got := freshAudioString(t, row[profile.albumArtistKey]); got != "Goldenrec Generator" {
		t.Fatalf("%s album artist = %q, want Goldenrec Generator", profile.name, got)
	}
	for _, key := range []string{profile.artistItemsKey, profile.albumArtistsKey} {
		if got := freshAudioPairs(t, row[key]); !reflect.DeepEqual(got, []dto.NameGuidPair{{ID: fixture.artist.ID, Name: "Goldenrec Generator"}}) {
			t.Fatalf("%s %s = %#v, want persisted artist ID %s", profile.name, key, got, fixture.artist.ID)
		}
	}
	if got := freshAudioInt64(t, row[profile.runtimeKey]); got != 10_000_000 {
		t.Fatalf("%s runtime = %d, want exact probe runtime 10000000", profile.name, got)
	}
	if got := freshAudioInt(t, row[profile.indexKey]); want.IndexNumber == nil || got != *want.IndexNumber {
		t.Fatalf("%s index = %d, want %v", profile.name, got, want.IndexNumber)
	}
	premiere, found := row[profile.premiereKey]
	if !found {
		t.Fatalf("%s omitted zero PremiereDate", profile.name)
	}
	if got, expected := string(premiere), `"0001-01-01T00:00:00.0000000Z"`; got != expected {
		t.Fatalf("%s premiere date wire value = %s, want %s", profile.name, got, expected)
	}
	var zero time.Time
	if err := json.Unmarshal(premiere, &zero); err != nil || !zero.IsZero() {
		t.Fatalf("%s premiere date = %s, want emitted zero time: %v", profile.name, premiere, err)
	}
	var streams []map[string]json.RawMessage
	if err := json.Unmarshal(row[profile.streamsKey], &streams); err != nil || len(streams) != 1 {
		t.Fatalf("%s streams = %s, want one probed audio stream: %v", profile.name, row[profile.streamsKey], err)
	}
	streamType, codec := profile.streamKey("Type"), profile.streamKey("Codec")
	if got := freshAudioString(t, streams[0][streamType]); got != "Audio" || freshAudioString(t, streams[0][codec]) != "mp3" {
		t.Fatalf("%s stream = %s, want persisted MP3 audio stream", profile.name, streams[0])
	}
	wrongID := "Id"
	if profile.idKey == "Id" {
		wrongID = "id"
	}
	if _, found := row[wrongID]; found {
		t.Fatalf("%s wire row included wrong-cased ID key %q", profile.name, wrongID)
	}
}

func (p freshAudioProfile) streamKey(pascal string) string {
	if p.idKey == "Id" {
		return pascal
	}
	return string(pascal[0]+32) + pascal[1:]
}

func freshAudioString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode string %s: %v", raw, err)
	}
	return value
}

func freshAudioStrings(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var value []string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode strings %s: %v", raw, err)
	}
	return value
}

func freshAudioPairs(t *testing.T, raw json.RawMessage) []dto.NameGuidPair {
	t.Helper()
	var value []dto.NameGuidPair
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode name-guid pairs %s: %v", raw, err)
	}
	return value
}

func freshAudioUUID(t *testing.T, raw json.RawMessage) dto.UUID {
	t.Helper()
	var value dto.UUID
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode UUID %s: %v", raw, err)
	}
	return value
}

func freshAudioInt(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode int %s: %v", raw, err)
	}
	return value
}

func freshAudioInt64(t *testing.T, raw json.RawMessage) int64 {
	t.Helper()
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode int64 %s: %v", raw, err)
	}
	return value
}

func freshAudioTrackName(number int) string {
	return freshAudioDecimal(number) + " - Synthetic Track " + freshAudioDecimal(number) + ".mp3"
}

func freshAudioTrackNumber(path string) int {
	for _, character := range filepath.Base(path) {
		if character >= '1' && character <= '9' {
			return int(character - '0')
		}
	}
	return 0
}

func freshAudioDecimal(value int) string {
	if value == 0 {
		return "0"
	}
	return string(rune('0' + value))
}

func openFreshAudioCountingSQLite(t *testing.T) *storetest.CountingSQLiteStores {
	t.Helper()
	return storetest.OpenCountingSQLite(t, "fresh-audio.db")
}
