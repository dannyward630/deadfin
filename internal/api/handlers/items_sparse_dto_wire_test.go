// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	vpath "github.com/deadfin-org/deadfin/internal/path"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestItemsSparseBaseItemDtoWireShape protects the sparse BaseItemDto shape
// emitted by stock for movie-production-order: requested path and image facts
// remain on the wire, while unrelated empty DTO fields do not expand the item.
func TestItemsSparseBaseItemDtoWireShape(t *testing.T) {
	id := itemsHandlerUUID(t, 400)
	runtimeTicks := int64(10_000_000)
	productionYear := 2024
	isFolder := false
	videoType := dto.VideoType("VideoFile")
	locationType := dto.LocationType("FileSystem")
	fixture := dto.BaseItemDto{
		ID:                id,
		ServerID:          "server-400",
		Name:              "HLG Sample",
		Container:         "mkv",
		Path:              "/media/movies/HLG Sample (2024)/HLG Sample (2024).mkv",
		ProductionYear:    &productionYear,
		RunTimeTicks:      &runtimeTicks,
		Type:              dto.Kind("Movie"),
		VideoType:         &videoType,
		LocationType:      &locationType,
		MediaType:         dto.MediaType("Video"),
		IsFolder:          &isFolder,
		BackdropImageTags: []string{},
		ImageTags:         map[string]string{"Primary": "entity-tag"},
		ImageBlurHashes:   map[string]map[string]string{"Primary": {"entity-tag": "blur-hash"}},
		UserData: &dto.UserItemDataDto{
			ItemID: id,
			Key:    "user-400",
		},
	}
	reader := &itemsHandlerReader{result: store.ItemQueryResult{Items: []item.Item{{ID: id, Kind: item.KindMovie}}, TotalRecordCount: 1}}
	builder := &itemsHandlerBuilder{items: []dto.BaseItemDto{fixture}}
	rt := router.New()
	handlers.RegisterItems(rt, reader, builder, clock.NewReal())

	for _, tc := range []struct {
		name, accept, itemsKey string
		keys                   sparseDTOKeys
		absent                 []string
	}{
		{
			name:     "Pascal",
			accept:   "application/json",
			itemsKey: "Items",
			keys: sparseDTOKeys{
				backdrops: "BackdropImageTags", channelID: "ChannelId", imageTags: "ImageTags", imageBlurHashes: "ImageBlurHashes",
				isFolder: "IsFolder", path: "Path", userData: "UserData", isFavorite: "IsFavorite", played: "Played",
				playCount: "PlayCount", playbackPositionTicks: "PlaybackPositionTicks", container: "Container", id: "Id",
				locationType: "LocationType", mediaType: "MediaType", name: "Name", productionYear: "ProductionYear",
				runtimeTicks: "RunTimeTicks", serverID: "ServerId", itemType: "Type", videoType: "VideoType",
			},
			absent: []string{"AirDays", "AlbumCount", "CanDelete", "CommunityRating", "CriticRating", "IndexNumber", "ParentId", "IsPlaceHolder"},
		},
		{
			name:     "Camel",
			accept:   `application/json; profile="CamelCase"`,
			itemsKey: "items",
			keys: sparseDTOKeys{
				backdrops: "backdropImageTags", channelID: "channelId", imageTags: "imageTags", imageBlurHashes: "imageBlurHashes",
				isFolder: "isFolder", path: "path", userData: "userData", isFavorite: "isFavorite", played: "played",
				playCount: "playCount", playbackPositionTicks: "playbackPositionTicks", container: "container", id: "id",
				locationType: "locationType", mediaType: "mediaType", name: "name", productionYear: "productionYear",
				runtimeTicks: "runTimeTicks", serverID: "serverId", itemType: "type", videoType: "videoType",
			},
			absent: []string{"airDays", "albumCount", "canDelete", "communityRating", "criticRating", "indexNumber", "parentId", "isPlaceHolder"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/Items?Recursive=true&IncludeItemTypes=Movie&SortBy=ProductionYear,SortName&SortOrder=Descending,Ascending&Fields=Path", nil)
			request.Header.Set("Accept", tc.accept)
			rt.ServeHTTP(record, request)
			if record.Code != http.StatusOK {
				t.Fatalf("GET /Items status = %d, want 200; body=%s", record.Code, record.Body.String())
			}

			row := sparseDTOResponseItem(t, record.Body.Bytes(), tc.itemsKey)
			sparseDTOAssertWireShape(t, row, tc.keys, tc.absent, id.String())
		})
	}
}

// TestItemsMetadataPathRenderingAcrossWireProfiles makes sure that stored
// paths are emitted as configured filesystem values across both wire
// profiles. Virtual-path tokens expand before the response, while literal
// percent sequences and unicode remain unchanged.
func TestItemsMetadataPathRenderingAcrossWireProfiles(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open path rendering store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close path rendering store: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	expander := vpath.NewVirtualPathExpander("/srv/data", "/srv/metadata")
	paths := []struct {
		id                 dto.UUID
		kind               item.Kind
		name, stored, want string
	}{
		{
			kind:   item.KindPerson,
			name:   "metadata placeholder with spaces",
			stored: "%MetadataPath%/People/G/Goldenrec Generator",
			want:   "/srv/metadata/People/G/Goldenrec Generator",
		},
		{
			kind:   item.KindMovie,
			name:   "existing percent sequences remain literal",
			stored: "/metadata/Caf%C3%A9%20A",
			want:   "/metadata/Caf%C3%A9%20A",
		},
		{
			kind:   item.KindMovie,
			name:   "spaces and unicode remain literal",
			stored: "/metadata/Música/Café déjà-vu.mkv",
			want:   "/metadata/Música/Café déjà-vu.mkv",
		},
		{
			kind:   item.KindMovie,
			name:   "plus remains literal",
			stored: "/metadata/C++/A+B.mp3",
			want:   "/metadata/C++/A+B.mp3",
		},
		{
			kind:   item.KindMovie,
			name:   "malformed percent sequences remain literal",
			stored: "/metadata/bad%2G/lone%/truncated%2",
			want:   "/metadata/bad%2G/lone%/truncated%2",
		},
	}
	ids := make([]string, 0, len(paths))
	for index := range paths {
		paths[index].id = itemsHandlerUUID(t, 960+index)
		path := &paths[index]
		name := path.name
		if err := items.Upsert(t.Context(), item.Item{ID: path.id, Kind: path.kind, Name: &name, Path: &path.stored}); err != nil {
			t.Fatalf("upsert %s path fixture: %v", path.name, err)
		}
		ids = append(ids, path.id.Dashed())
	}

	routes := router.New()
	handlers.RegisterItems(routes, items, dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)).WithPaths(expander), clock.NewReal())
	for _, profile := range []struct {
		name, accept, itemsKey, idKey, pathKey string
	}{
		{name: "native", accept: "application/json", itemsKey: "Items", idKey: "Id", pathKey: "Path"},
		{name: "Camel", accept: `application/json; profile="CamelCase"`, itemsKey: "items", idKey: "id", pathKey: "path"},
	} {
		t.Run(profile.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/Items?ids="+strings.Join(ids, ",")+"&Fields=Path", nil)
			// IDs are an explicit scope, so this query must retain the Person path
			// even though the canonical unscoped inventory excludes Person rows.
			request.Header.Set("Accept", profile.accept)
			routes.ServeHTTP(record, request)
			if record.Code != http.StatusOK {
				t.Fatalf("GET /Items = %d, want 200; body=%s", record.Code, record.Body.String())
			}

			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(record.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode %s Items response: %v; body=%s", profile.name, err, record.Body.String())
			}
			var rows []map[string]json.RawMessage
			if err := json.Unmarshal(envelope[profile.itemsKey], &rows); err != nil {
				t.Fatalf("decode %s response rows: %v; body=%s", profile.name, err, record.Body.String())
			}
			if len(rows) != len(paths) {
				t.Fatalf("%s response rows = %d, want %d; body=%s", profile.name, len(rows), len(paths), record.Body.String())
			}
			wantByID := make(map[string]struct {
				name, path string
			}, len(paths))
			for _, path := range paths {
				wantByID[path.id.String()] = struct{ name, path string }{name: path.name, path: path.want}
			}
			for _, row := range rows {
				var id string
				if err := json.Unmarshal(row[profile.idKey], &id); err != nil {
					t.Fatalf("decode %s item ID: %v; row=%s", profile.name, err, row)
				}
				want, found := wantByID[id]
				if !found {
					t.Fatalf("%s response included unexpected item ID %q", profile.name, id)
				}
				wantPath, err := json.Marshal(want.path)
				if err != nil {
					t.Fatalf("encode %s %s expected path: %v", profile.name, want.name, err)
				}
				if got := row[profile.pathKey]; !bytes.Equal(got, wantPath) {
					t.Errorf("%s %s path JSON bytes = %s, want %s", profile.name, want.name, got, wantPath)
				}
				delete(wantByID, id)
			}
			if len(wantByID) != 0 {
				t.Errorf("%s response omitted path fixtures: %#v", profile.name, wantByID)
			}
		})
	}
}

type sparseDTOKeys struct {
	backdrops, channelID, imageTags, imageBlurHashes                               string
	isFolder, path, userData, isFavorite, played, playCount, playbackPositionTicks string
	container, id, locationType, mediaType, name, productionYear, runtimeTicks     string
	serverID, itemType, videoType                                                  string
}

func sparseDTOResponseItem(t *testing.T, body []byte, itemsKey string) map[string]json.RawMessage {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode Items response: %v; body=%s", err, body)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(envelope[itemsKey], &rows); err != nil || len(rows) != 1 {
		t.Fatalf("response %s = %s, want one movie; err=%v", itemsKey, envelope[itemsKey], err)
	}
	return rows[0]
}

func sparseDTOAssertWireShape(t *testing.T, row map[string]json.RawMessage, keys sparseDTOKeys, absent []string, id string) {
	t.Helper()
	for key, want := range map[string]string{
		keys.backdrops:      "[]",
		keys.channelID:      "null",
		keys.container:      `"mkv"`,
		keys.id:             `"` + id + `"`,
		keys.isFolder:       "false",
		keys.locationType:   `"FileSystem"`,
		keys.mediaType:      `"Video"`,
		keys.name:           `"HLG Sample"`,
		keys.path:           `"/media/movies/HLG Sample (2024)/HLG Sample (2024).mkv"`,
		keys.productionYear: "2024",
		keys.runtimeTicks:   "10000000",
		keys.serverID:       `"server-400"`,
		keys.itemType:       `"Movie"`,
		keys.videoType:      `"VideoFile"`,
	} {
		if got, found := row[key]; !found || string(got) != want {
			t.Errorf("item %q = %s (present=%t), want %s", key, got, found, want)
		}
	}
	sparseDTOAssertImageFacts(t, row, keys)

	var userData map[string]json.RawMessage
	if err := json.Unmarshal(row[keys.userData], &userData); err != nil {
		t.Fatalf("decode item %q: %v", keys.userData, err)
	}
	for key, want := range map[string]string{
		keys.isFavorite:            "false",
		keys.played:                "false",
		keys.playCount:             "0",
		keys.playbackPositionTicks: "0",
	} {
		if got, found := userData[key]; !found || string(got) != want {
			t.Errorf("item %s.%s = %s (present=%t), want %s", keys.userData, key, got, found, want)
		}
	}

	for _, key := range absent {
		if _, found := row[key]; found {
			t.Errorf("sparse movie unexpectedly emitted unrelated field %q", key)
		}
	}
}

func sparseDTOAssertImageFacts(t *testing.T, row map[string]json.RawMessage, keys sparseDTOKeys) {
	t.Helper()
	var imageTags map[string]string
	if err := json.Unmarshal(row[keys.imageTags], &imageTags); err != nil {
		t.Fatalf("decode item %q: %v", keys.imageTags, err)
	}
	if got, want := imageTags["Primary"], "entity-tag"; got != want {
		t.Errorf("item %s.Primary = %q, want %q", keys.imageTags, got, want)
	}
	var imageBlurHashes map[string]map[string]string
	if err := json.Unmarshal(row[keys.imageBlurHashes], &imageBlurHashes); err != nil {
		t.Fatalf("decode item %q: %v", keys.imageBlurHashes, err)
	}
	if got, want := imageBlurHashes["Primary"]["entity-tag"], "blur-hash"; got != want {
		t.Errorf("item %s.Primary.entity-tag = %q, want %q", keys.imageBlurHashes, got, want)
	}
}

// TestItemsCanonicalSparseMediaWireProfiles fixes the route-level casing and
// omission contract for the representative media shapes returned by /Items.
// DTO construction is intentionally supplied at the handler seam: this test
// verifies the public wire encoder without duplicating builder behavior.
func TestItemsCanonicalSparseMediaWireProfiles(t *testing.T) {
	seriesID, seasonID := itemsHandlerUUID(t, 420), itemsHandlerUUID(t, 421)
	albumID, artistID := itemsHandlerUUID(t, 422), itemsHandlerUUID(t, 423)
	episodeID, audioID := itemsHandlerUUID(t, 424), itemsHandlerUUID(t, 425)
	movieID, photoID, discID := itemsHandlerUUID(t, 426), itemsHandlerUUID(t, 427), itemsHandlerUUID(t, 428)
	videoFile, bluray := dto.VideoType("VideoFile"), dto.VideoType("BluRay")
	orientation := dto.ImageOrientation("RightBottom")
	falseValue, trueValue := false, true
	fixtures := []dto.BaseItemDto{
		{ID: episodeID, Name: "Stored Episode", Type: dto.Kind("Episode"), SeriesID: &seriesID, SeasonID: &seasonID, SeriesName: "Stored Series", SeasonName: "Stored Season", VideoType: &videoFile},
		{ID: audioID, Name: "Stored Track", Type: dto.Kind("Audio"), MediaType: dto.MediaType("Audio"), Album: "Stored Album", AlbumID: &albumID, Artists: []string{"Stored Artist"}, ArtistItems: []dto.NameGuidPair{{Name: "Stored Artist", ID: artistID}}, AlbumArtist: "Stored Artist", AlbumArtists: []dto.NameGuidPair{{Name: "Stored Artist", ID: artistID}}, HasLyrics: &falseValue},
		{ID: movieID, Name: "Stored Movie", Type: dto.Kind("Movie"), MediaType: dto.MediaType("Video"), VideoType: &videoFile},
		{ID: photoID, Name: "Stored Photo", Type: dto.Kind("Photo"), ImageOrientation: &orientation},
		{ID: discID, Name: "Stored Disc", Type: dto.Kind("Movie"), MediaType: dto.MediaType("Video"), IsPlaceHolder: &trueValue, VideoType: &bluray},
	}
	reader := &itemsHandlerReader{result: store.ItemQueryResult{Items: []item.Item{
		{ID: episodeID, Kind: item.KindEpisode}, {ID: audioID, Kind: item.KindAudio}, {ID: movieID, Kind: item.KindMovie}, {ID: photoID, Kind: item.KindPhoto}, {ID: discID, Kind: item.KindMovie},
	}, TotalRecordCount: len(fixtures)}}
	rt := router.New()
	handlers.RegisterItems(rt, reader, &itemsHandlerBuilder{items: fixtures}, clock.NewReal())

	shapes := make(map[string]any, 2)
	for _, profile := range []struct {
		name, accept, itemsKey string
	}{
		{name: "Pascal", accept: "application/json", itemsKey: "Items"},
		{name: "Camel", accept: `application/json; profile="CamelCase"`, itemsKey: "items"},
	} {
		t.Run(profile.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/Items?Recursive=true", nil)
			request.Header.Set("Accept", profile.accept)
			rt.ServeHTTP(record, request)
			if record.Code != http.StatusOK {
				t.Fatalf("GET /Items status = %d, want 200; body=%s", record.Code, record.Body.String())
			}
			var shape any
			if err := json.Unmarshal(record.Body.Bytes(), &shape); err != nil {
				t.Fatalf("decode %s profile shape: %v", profile.name, err)
			}
			shapes[profile.name] = sparseDTOPascalizeShape(shape)
			rows := sparseDTOResponseItems(t, record.Body.Bytes(), profile.itemsKey)
			byID := make(map[string]map[string]json.RawMessage, len(rows))
			for _, row := range rows {
				byID[strings.Trim(string(row[sparseDTOWireKey(profile.name, "Id")]), `"`)] = row
			}
			sparseDTOAssertCanonicalWire(t, byID[episodeID.String()], profile.name, map[string]string{
				"SeriesId": `"` + seriesID.String() + `"`, "SeasonId": `"` + seasonID.String() + `"`, "SeriesName": `"Stored Series"`, "SeasonName": `"Stored Season"`, "VideoType": `"VideoFile"`,
			})
			sparseDTOAssertCanonicalWire(t, byID[audioID.String()], profile.name, map[string]string{
				"Album": `"Stored Album"`, "AlbumId": `"` + albumID.String() + `"`, "Artists": `["Stored Artist"]`, "ArtistItems": `[{"Name":"Stored Artist","Id":"` + artistID.String() + `"}]`, "AlbumArtist": `"Stored Artist"`, "AlbumArtists": `[{"Name":"Stored Artist","Id":"` + artistID.String() + `"}]`, "HasLyrics": "false",
			})
			if _, found := byID[audioID.String()][sparseDTOWireKey(profile.name, "VideoType")]; found {
				t.Errorf("Audio unexpectedly emitted VideoType in %s profile", profile.name)
			}
			sparseDTOAssertCanonicalWire(t, byID[movieID.String()], profile.name, map[string]string{"VideoType": `"VideoFile"`})
			sparseDTOAssertCanonicalWire(t, byID[photoID.String()], profile.name, map[string]string{"ImageOrientation": `"RightBottom"`})
			sparseDTOAssertCanonicalWire(t, byID[discID.String()], profile.name, map[string]string{"IsPlaceHolder": "true", "VideoType": `"BluRay"`})
		})
	}
	if !reflect.DeepEqual(shapes["Pascal"], shapes["Camel"]) {
		t.Errorf("Pascal and Camel canonical media response shapes differ: Pascal=%#v Camel=%#v", shapes["Pascal"], shapes["Camel"])
	}
}

func sparseDTOResponseItems(t *testing.T, body []byte, itemsKey string) []map[string]json.RawMessage {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode Items response: %v; body=%s", err, body)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(envelope[itemsKey], &rows); err != nil || len(rows) != 5 {
		t.Fatalf("response %s = %s, want five canonical media rows; err=%v", itemsKey, envelope[itemsKey], err)
	}
	return rows
}

func sparseDTOAssertCanonicalWire(t *testing.T, row map[string]json.RawMessage, profile string, want map[string]string) {
	t.Helper()
	for pascalKey, expected := range want {
		key := sparseDTOWireKey(profile, pascalKey)
		if got, found := row[key]; !found || !sparseDTOEquivalentJSON(got, []byte(expected)) {
			t.Errorf("%s profile %s = %s (present=%t), want %s", profile, key, got, found, expected)
		}
	}
}

func sparseDTOEquivalentJSON(actual, expected []byte) bool {
	var gotValue, wantValue any
	if json.Unmarshal(actual, &gotValue) != nil || json.Unmarshal(expected, &wantValue) != nil {
		return false
	}
	return reflect.DeepEqual(sparseDTOPascalizeShape(gotValue), sparseDTOPascalizeShape(wantValue))
}

func sparseDTOWireKey(profile, pascal string) string {
	if profile == "Camel" {
		return strings.ToLower(pascal[:1]) + pascal[1:]
	}
	return pascal
}

func sparseDTOPascalizeShape(value any) any {
	switch typed := value.(type) {
	case []any:
		result := make([]any, len(typed))
		for index := range typed {
			result[index] = sparseDTOPascalizeShape(typed[index])
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			result[strings.ToUpper(key[:1])+key[1:]] = sparseDTOPascalizeShape(child)
		}
		return result
	default:
		return value
	}
}
