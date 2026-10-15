// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestApplicationComposesPersistentItemStoreEnrichment exercises the production
// composition root. It deliberately does not supply a test Builder or enricher:
// the runtime must materialize source, stream, user, artwork, and folder facts
// from the persistent ItemStore before and after restart.
func TestApplicationComposesPersistentItemStoreEnrichment(t *testing.T) {
	programData := filepath.Join(t.TempDir(), "program-data")
	app := openItemEnrichmentApplication(t, programData)
	configureComposedAdministrator(t, app.Router())
	user := composedAdministrator(t, app)
	fixture := seedComposedItemEnrichment(t, app, user.ID)

	assertComposedItemEnrichment(t, app, user.ID, fixture)

	closeComposedItemEnrichmentApplication(t, app)
	app = openItemEnrichmentApplication(t, programData)
	t.Cleanup(func() { closeComposedItemEnrichmentApplication(t, app) })
	user = composedAdministrator(t, app)
	assertComposedItemEnrichment(t, app, user.ID, fixture)
}

type composedItemEnrichmentFixture struct {
	movieID  dto.UUID
	folderID dto.UUID
}

func openItemEnrichmentApplication(t *testing.T, programData string) *application {
	t.Helper()

	app, err := newApplication(t.Context(), programData)
	if err != nil {
		t.Fatalf("start item-enrichment application: %v", err)
	}
	return app
}

func closeComposedItemEnrichmentApplication(t *testing.T, app *application) {
	t.Helper()
	if err := app.Close(context.Background()); err != nil {
		t.Fatalf("close item-enrichment application: %v", err)
	}
}

func composedAdministrator(t *testing.T, app *application) store.User {
	t.Helper()

	user, err := app.UserService().GetByName(t.Context(), "captain")
	if err != nil {
		t.Fatalf("load composed administrator: %v", err)
	}
	return user
}

func seedComposedItemEnrichment(t *testing.T, app *application, userID dto.UUID) composedItemEnrichmentFixture {
	t.Helper()

	items := store.NewItemStore(app.stores)
	folderID := composedItemEnrichmentUUID(t, 0xd001)
	movieID := composedItemEnrichmentUUID(t, 0xd002)
	nestedFolderID := composedItemEnrichmentUUID(t, 0xd003)
	nestedMovieID := composedItemEnrichmentUUID(t, 0xd004)
	folderName, movieName := "Real source folder", "Store-backed movie"
	nestedFolderName, nestedMovieName := "Nested folder", "Nested movie"
	moviePath, container := "/library/store-backed-movie.mkv", "mkv"
	modified := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	primaryBlur, backdropBlurA, backdropBlurB := "primary-blur", "backdrop-a", "backdrop-b"

	for _, value := range []item.Item{
		{ID: folderID, Kind: item.KindFolder, IsFolder: true, Name: &folderName},
		{
			ID: movieID, Kind: item.KindMovie, ParentID: &folderID, IsMovie: true, Name: &movieName, Path: &moviePath, Container: &container,
			ImageInfos: []item.ImageInfo{
				{Type: item.ImageTypePrimary, Index: 0, Path: "/library/poster.jpg", DateModified: &modified, BlurHash: &primaryBlur},
				{Type: item.ImageTypeBackdrop, Index: 0, Path: "/library/backdrop-a.jpg", DateModified: &modified, BlurHash: &backdropBlurA},
				{Type: item.ImageTypeBackdrop, Index: 1, Path: "/library/backdrop-b.jpg", DateModified: &modified, BlurHash: &backdropBlurB},
			},
		},
		{ID: nestedFolderID, Kind: item.KindFolder, ParentID: &folderID, IsFolder: true, Name: &nestedFolderName},
		{ID: nestedMovieID, Kind: item.KindMovie, ParentID: &nestedFolderID, IsMovie: true, Name: &nestedMovieName},
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert item-enrichment fixture %s: %v", value.ID, err)
		}
	}
	if err := items.ReplaceStreams(t.Context(), movieID, []playback.MediaStream{{Index: 4, Type: playback.MediaStreamVideo, Codec: "h264"}}); err != nil {
		t.Fatalf("store media stream fixture: %v", err)
	}
	if err := items.UpsertUserData(t.Context(), store.UserData{
		UserID: userID, CustomKey: movieID.Dashed(), ItemID: movieID, Played: true, PlayCount: 3, IsFavorite: true, PositionTicks: 9_876_543,
	}); err != nil {
		t.Fatalf("store user-data fixture: %v", err)
	}
	return composedItemEnrichmentFixture{movieID: movieID, folderID: folderID}
}

func assertComposedItemEnrichment(t *testing.T, app *application, userID dto.UUID, fixture composedItemEnrichmentFixture) {
	t.Helper()

	token := loginComposedAdministrator(t, app.Router())
	moviePath := fmt.Sprintf("/Items/%s?userId=%s&Fields=MediaSources,MediaStreams&EnableUserData=true&EnableImages=true&EnableImageTypes=Primary,Backdrop&ImageTypeLimit=1", fixture.movieID.Dashed(), userID.Dashed())
	movie := composedItemEnrichmentResponse(t, app, token, moviePath)
	assertComposedMediaProjection(t, movie, fixture.movieID)
	assertComposedUserProjection(t, movie)
	assertComposedImageProjection(t, movie, "backdrop-a")

	defaultOptions := fmt.Sprintf("/Items/%s?userId=%s", fixture.movieID.Dashed(), userID.Dashed())
	defaultDetail := composedItemEnrichmentResponse(t, app, token, defaultOptions)
	assertComposedMediaProjection(t, defaultDetail, fixture.movieID)
	assertComposedUserProjection(t, defaultDetail)

	explicitlyDisabled := fmt.Sprintf("%s&EnableUserData=false", defaultOptions)
	disabledUserData := composedItemEnrichmentResponse(t, app, token, explicitlyDisabled)
	assertComposedMediaProjection(t, disabledUserData, fixture.movieID)
	assertComposedUserDataOmission(t, disabledUserData)
	assertComposedImageProjection(t, disabledUserData, "backdrop-a", "backdrop-b")

	folderPath := fmt.Sprintf("/Items/%s?Fields=ChildCount,RecursiveItemCount", fixture.folderID.Dashed())
	assertComposedFolderCounts(t, composedItemEnrichmentResponse(t, app, token, folderPath))
}

func composedItemEnrichmentResponse(t *testing.T, app *application, token, path string) map[string]json.RawMessage {
	t.Helper()

	rec := authenticatedCompositionRequest(app.Router(), http.MethodGet, path, nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d; body=%s", path, rec.Code, http.StatusOK, rec.Body.String())
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GET %s response: %v; body=%s", path, err, rec.Body.String())
	}
	return response
}

func assertComposedMediaProjection(t *testing.T, response map[string]json.RawMessage, movieID dto.UUID) {
	t.Helper()

	var sources []struct{ ID string }
	if err := json.Unmarshal(response["MediaSources"], &sources); err != nil || len(sources) != 1 || sources[0].ID != movieID.String() {
		t.Fatalf("source-backed MediaSources = %s, want one source with ID %q; err=%v", response["MediaSources"], movieID.String(), err)
	}
	var streams []struct{ Index int }
	if err := json.Unmarshal(response["MediaStreams"], &streams); err != nil || len(streams) != 1 || streams[0].Index != 4 {
		t.Fatalf("source-backed MediaStreams = %s, want one persisted stream at index 4; err=%v", response["MediaStreams"], err)
	}
}

func assertComposedUserProjection(t *testing.T, response map[string]json.RawMessage) {
	t.Helper()

	var userData struct {
		Played                bool
		PlayCount             int
		IsFavorite            bool
		PlaybackPositionTicks int64
	}
	if err := json.Unmarshal(response["UserData"], &userData); err != nil || !userData.Played || userData.PlayCount != 3 || !userData.IsFavorite || userData.PlaybackPositionTicks != 9_876_543 {
		t.Fatalf("source-backed UserData = %s, want persisted played/favorite/resume facts; err=%v", response["UserData"], err)
	}
}

func assertComposedImageProjection(t *testing.T, response map[string]json.RawMessage, wantBackdropBlurHashes ...string) {
	t.Helper()

	var tags map[string]string
	if err := json.Unmarshal(response["ImageTags"], &tags); err != nil || tags["Primary"] == "" {
		t.Fatalf("source-backed ImageTags = %s, want a persisted primary image tag; err=%v", response["ImageTags"], err)
	}
	var backdrops []string
	if err := json.Unmarshal(response["BackdropImageTags"], &backdrops); err != nil || len(backdrops) != len(wantBackdropBlurHashes) {
		t.Fatalf("source-backed BackdropImageTags = %s, want %d persisted backdrops; err=%v", response["BackdropImageTags"], len(wantBackdropBlurHashes), err)
	}
	var hashes map[string]map[string]string
	if err := json.Unmarshal(response["ImageBlurHashes"], &hashes); err != nil || hashes["Primary"][tags["Primary"]] != "primary-blur" {
		t.Fatalf("source-backed ImageBlurHashes = %s, want source primary blur hash; err=%v", response["ImageBlurHashes"], err)
	}
	for index, want := range wantBackdropBlurHashes {
		if backdrops[index] == "" || hashes["Backdrop"][backdrops[index]] != want {
			t.Fatalf("source-backed ImageBlurHashes = %s, want blur hash %q for backdrop %d", response["ImageBlurHashes"], want, index)
		}
	}
}

func assertComposedUserDataOmission(t *testing.T, response map[string]json.RawMessage) {
	t.Helper()

	if actual, present := response["UserData"]; present {
		t.Errorf("disabled UserData = %s, want key omitted", actual)
	}
}

func assertComposedFolderCounts(t *testing.T, response map[string]json.RawMessage) {
	t.Helper()

	var childCount, recursiveItemCount int
	if err := json.Unmarshal(response["ChildCount"], &childCount); err != nil || childCount != 2 {
		t.Fatalf("source-backed ChildCount = %s, want 2 direct children; err=%v", response["ChildCount"], err)
	}
	if err := json.Unmarshal(response["RecursiveItemCount"], &recursiveItemCount); err != nil || recursiveItemCount != 3 {
		t.Fatalf("source-backed RecursiveItemCount = %s, want 3 descendants; err=%v", response["RecursiveItemCount"], err)
	}
}

func composedItemEnrichmentUUID(t *testing.T, value int) dto.UUID {
	t.Helper()

	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse item-enrichment UUID %d: %v", value, err)
	}
	return id
}

// TestApplicationItemsProjectsFullPersistedMediaSources exercises the normal
// /Items list projection against the production composition root. It compares
// the complete nested wire objects to the pinned playback-source projection,
// not merely their identifiers and indexes.
func TestApplicationItemsProjectsFullPersistedMediaSources(t *testing.T) {
	app := openItemEnrichmentApplication(t, filepath.Join(t.TempDir(), "program-data"))
	t.Cleanup(func() { closeComposedItemEnrichmentApplication(t, app) })
	configureComposedAdministrator(t, app.Router())
	user := composedAdministrator(t, app)
	fixture := seedComposedItemEnrichment(t, app, user.ID)
	items := store.NewItemStore(app.stores)
	seedFullComposedMedia(t, items, fixture.movieID)

	wantSources, err := playback.AssembleMediaSources(t.Context(), items, fixture.movieID)
	if err != nil {
		t.Fatalf("assemble pinned stored sources: %v", err)
	}
	if len(wantSources) != 2 {
		t.Fatalf("pinned stored source order = %#v, want requested and alternate source", wantSources)
	}
	// Generic /Items preserves the stored top-level stream facts. Nested
	// MediaSources retain playback-source subtitle scores independently.
	wantTopLevelStreams, err := items.Streams(t.Context(), fixture.movieID)
	if err != nil {
		t.Fatalf("load persisted top-level streams: %v", err)
	}
	token := loginComposedAdministrator(t, app.Router())
	path := fmt.Sprintf("/Items?ids=%s&Fields=MediaSources,MediaStreams", fixture.movieID.Dashed())
	for _, tc := range []struct {
		name, accept string
		camel        bool
		itemsKey     string
		sourcesKey   string
		streamsKey   string
	}{
		{name: "Pascal", itemsKey: "Items", sourcesKey: "MediaSources", streamsKey: "MediaStreams"},
		{name: "Camel", accept: `application/json; profile="CamelCase"`, camel: true, itemsKey: "items", sourcesKey: "mediaSources", streamsKey: "mediaStreams"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := composedItemsMediaResponse(t, app, token, path, tc.accept, tc.itemsKey)
			wantSourceBytes, err := json.Marshal(wantSources)
			if err != nil {
				t.Fatalf("marshal pinned %s sources: %v", tc.name, err)
			}
			wantStreamBytes, err := json.Marshal(wantTopLevelStreams)
			if err != nil {
				t.Fatalf("marshal pinned %s top-level streams: %v", tc.name, err)
			}
			if tc.camel {
				assertComposedMediaJSON(t, tc.name+" MediaSources", got[tc.sourcesKey], composedCamelJSON(t, wantSourceBytes))
				assertComposedMediaJSON(t, tc.name+" MediaStreams", got[tc.streamsKey], composedCamelJSON(t, wantStreamBytes))
			} else {
				if actual := got[tc.sourcesKey]; string(actual) != string(wantSourceBytes) {
					t.Errorf("%s MediaSources = %s, want exact pinned stored source bytes %s", tc.name, actual, wantSourceBytes)
				}
				if actual := got[tc.streamsKey]; string(actual) != string(wantStreamBytes) {
					t.Errorf("%s MediaStreams = %s, want exact pinned stored stream bytes %s", tc.name, actual, wantStreamBytes)
				}
			}
			withoutFields := composedItemsMediaResponse(t, app, token, fmt.Sprintf("/Items?ids=%s", fixture.movieID.Dashed()), tc.accept, tc.itemsKey)
			for _, key := range []string{tc.sourcesKey, tc.streamsKey} {
				if actual, present := withoutFields[key]; present {
					t.Errorf("%s unrequested %s = %s, want key omitted", tc.name, key, actual)
				}
			}
		})
	}
}

func seedFullComposedMedia(t *testing.T, items *store.ItemStore, movieID dto.UUID) {
	t.Helper()
	name, path, container := "Store-backed movie", "/library/store-backed-movie.mkv", "mkv"
	size, ticks, bitrate := int64(1_000_000), int64(90_000_000), 8_500_000
	altID := composedItemEnrichmentUUID(t, 0xd005)
	altName, altPath := "Store-backed movie commentary", "/library/store-backed-movie-commentary.mkv"
	if err := items.Upsert(t.Context(), item.Item{ID: movieID, Kind: item.KindMovie, IsMovie: true, Name: &name, Path: &path, Container: &container, Size: &size, RunTimeTicks: &ticks, TotalBitrate: &bitrate, LinkedChildren: []item.LinkedChildRef{{ItemID: altID, Type: item.LinkedChildTypeLocalAlternateVersion}}}); err != nil {
		t.Fatalf("upsert complete primary media source: %v", err)
	}
	if err := items.Upsert(t.Context(), item.Item{ID: altID, Kind: item.KindMovie, IsMovie: true, Name: &altName, Path: &altPath, Container: &container, Size: &size, RunTimeTicks: &ticks, TotalBitrate: &bitrate}); err != nil {
		t.Fatalf("upsert complete alternate media source: %v", err)
	}
	if err := items.ReplaceStreams(t.Context(), movieID, []playback.MediaStream{
		{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264", Language: composedString("eng"), BitRate: composedInt(8_500_000), Width: composedInt(1920), Height: composedInt(1080), IsDefault: true, TimeBase: "1/90000"},
		{Index: 1, Type: playback.StreamTypeAudio, Codec: "eac3", Language: composedString("eng"), BitRate: composedInt(640_000), Channels: composedInt(6), ChannelLayout: composedString("5.1"), IsDefault: true},
		{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "subrip", Language: composedString("spa"), IsForced: true, IsExternal: true, DeliveryURL: "/Items/source/ExternalSubtitles/2"},
	}); err != nil {
		t.Fatalf("store complete primary streams: %v", err)
	}
	if err := items.ReplaceAttachments(t.Context(), movieID, []playback.MediaAttachment{{Index: 3, Codec: "mjpeg", CodecTag: "MJPG", Comment: "cover", FileName: "cover.jpg", MimeType: "image/jpeg"}}); err != nil {
		t.Fatalf("store complete primary attachment: %v", err)
	}
	if err := items.ReplaceStreams(t.Context(), altID, []playback.MediaStream{{Index: 0, Type: playback.StreamTypeAudio, Codec: "aac", Language: composedString("fra"), Channels: composedInt(2), IsDefault: true}}); err != nil {
		t.Fatalf("store alternate streams: %v", err)
	}
}

func composedItemsMediaResponse(t *testing.T, app *application, token, path, accept, itemsKey string) map[string]json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:8096"
	req.Header.Set("Authorization", `MediaBrowser Client="Deadfin Test", Device="Deadfin Browser", DeviceId="deadfin-test-device", Version="1.0", Token="`+token+`"`)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200; body=%s", path, rec.Code, rec.Body.String())
	}
	var page map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode /Items page: %v; body=%s", err, rec.Body.String())
	}
	var projected []map[string]json.RawMessage
	if err := json.Unmarshal(page[itemsKey], &projected); err != nil || len(projected) != 1 {
		t.Fatalf("/Items %s = %s, %v; want one projection", itemsKey, page[itemsKey], err)
	}
	return projected[0]
}

func composedString(value string) *string { return &value }
func composedInt(value int) *int          { return &value }

func composedCamelJSON(t *testing.T, source []byte) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(source, &value); err != nil {
		t.Fatalf("decode pinned Pascal media JSON: %v", err)
	}
	encoded, err := json.Marshal(composedCamelKeys(value))
	if err != nil {
		t.Fatalf("encode pinned Camel media JSON: %v", err)
	}
	return encoded
}

func composedCamelKeys(value any) any {
	switch node := value.(type) {
	case []any:
		for index := range node {
			node[index] = composedCamelKeys(node[index])
		}
		return node
	case map[string]any:
		result := make(map[string]any, len(node))
		for key, child := range node {
			result[strings.ToLower(key[:1])+key[1:]] = composedCamelKeys(child)
		}
		return result
	default:
		return value
	}
}

func assertComposedMediaJSON(t *testing.T, name string, actual, expected []byte) {
	t.Helper()
	var got, want any
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatalf("decode %s actual JSON: %v", name, err)
	}
	if err := json.Unmarshal(expected, &want); err != nil {
		t.Fatalf("decode %s expected JSON: %v", name, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %s, want profile-shaped source %s", name, actual, expected)
	}
}
