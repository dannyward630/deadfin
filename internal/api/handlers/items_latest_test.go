// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestLatestItemsReturnsSourceGroupedBareArrayInBothProfiles(t *testing.T) {
	userID := latestUUID(t, 1)
	seriesID, firstID, secondID, movieID := latestUUID(t, 2), latestUUID(t, 3), latestUUID(t, 4), latestUUID(t, 5)
	reader := &latestReader{latest: []item.Item{
		{ID: firstID, Kind: item.KindEpisode, SeriesID: &seriesID},
		{ID: secondID, Kind: item.KindEpisode, SeriesID: &seriesID},
		{ID: movieID, Kind: item.KindMovie},
	}, byID: map[dto.UUID]item.Item{seriesID: {ID: seriesID, Kind: item.KindSeries}}}
	builder := &latestBuilder{}
	rt := latestRouter(reader, builder)

	for _, tc := range []struct {
		name, accept, idKey, childCountKey string
	}{
		{name: "pascal", idKey: "Id", childCountKey: "ChildCount"},
		{name: "camel", accept: `application/json; profile="CamelCase"`, idKey: "id", childCountKey: "childCount"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/Items/Latest?userId="+userID.Dashed(), nil)
			request.Header.Set("Accept", tc.accept)
			rt.ServeHTTP(record, request)
			if record.Code != http.StatusOK {
				t.Fatalf("GET /Items/Latest status = %d, want 200; body=%s", record.Code, record.Body.String())
			}
			if len(record.Body.Bytes()) == 0 || record.Body.Bytes()[0] != '[' {
				t.Fatalf("latest response = %s, want bare JSON array", record.Body.String())
			}
			var body []map[string]json.RawMessage
			if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode latest response: %v; body=%s", err, record.Body.String())
			}
			if len(body) != 2 {
				t.Fatalf("latest items = %s, want grouped series and movie", record.Body.String())
			}
			if got := string(body[0][tc.idKey]); got != `"`+seriesID.String()+`"` {
				t.Fatalf("grouped first ID = %s, want series %s", got, seriesID)
			}
			if got := string(body[0][tc.childCountKey]); got != "2" {
				t.Fatalf("grouped child count = %s, want 2", got)
			}
			if got := string(body[1][tc.idKey]); got != `"`+movieID.String()+`"` {
				t.Fatalf("second ID = %s, want movie %s", got, movieID)
			}
		})
	}

	if got := reader.queries; got != 4 {
		t.Fatalf("reader queries = %d, want 4 (two candidates plus two container batches)", got)
	}
	query := reader.seen[0]
	if query.UserID == nil || *query.UserID != userID || query.IsPlayed == nil || *query.IsPlayed || query.Limit == nil || *query.Limit != 40 || query.EnableTotalRecordCount || query.IsFolder == nil || *query.IsFolder || query.IsVirtualItem == nil || *query.IsVirtualItem {
		t.Fatalf("latest query = %#v, want source default user/limit/filter overlay", query)
	}
	wantOrder := []store.ItemOrder{{SortBy: store.ItemSortByDateCreated, SortOrder: store.SortOrderDescending}, {SortBy: store.ItemSortBySortName, SortOrder: store.SortOrderDescending}, {SortBy: store.ItemSortByProductionYear, SortOrder: store.SortOrderDescending}}
	if !reflect.DeepEqual(query.OrderBy, wantOrder) {
		t.Fatalf("latest order = %#v, want %#v", query.OrderBy, wantOrder)
	}
	if !query.DtoOptions.EnableImages || !query.DtoOptions.EnableUserData || query.DtoOptions.ImageTypeLimit != int(^uint(0)>>1) || len(query.DtoOptions.Fields) != len(latestDefaultItemFields) || !reflect.DeepEqual(query.DtoOptions.ImageTypes, latestAllImageTypes) {
		t.Fatalf("latest DTO options = %#v, want source DtoOptions defaults", query.DtoOptions)
	}
}

func TestLatestItemsMapsSelectedOptionsAndLegacyUserWins(t *testing.T) {
	routeUser, queryUser, movieID := latestUUID(t, 20), latestUUID(t, 21), latestUUID(t, 22)
	reader := &latestReader{latest: []item.Item{{ID: movieID, Kind: item.KindMovie}}}
	builder := &latestBuilder{}
	rt := latestRouter(reader, builder)
	record := httptest.NewRecorder()
	path := "/Users/" + routeUser.Dashed() + "/Items/Latest?userId=" + queryUser.Dashed() + "&limit=1&groupItems=false&parentId=" + movieID.Dashed() + "&fields=parentid,people&enableImages=false&enableUserData=false&imageTypeLimit=3&enableImageTypes=primary,backdrop&includeItemTypes=movie&isPlayed=true"
	rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET legacy latest status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	if reader.queries != 1 {
		t.Fatalf("legacy ungrouped reader queries = %d, want 1", reader.queries)
	}
	query := reader.seen[0]
	if query.UserID == nil || *query.UserID != routeUser || query.ParentID == nil || *query.ParentID != movieID || !query.Recursive || query.Limit == nil || *query.Limit != 2 || query.IsFolder != nil || query.IsPlayed == nil || !*query.IsPlayed || !reflect.DeepEqual(query.IncludeItemTypes, []item.Kind{item.KindMovie}) {
		t.Fatalf("legacy latest query = %#v, want route user and selected source filters", query)
	}
	if !reflect.DeepEqual(query.DtoOptions.Fields, []string{"ParentId", "People"}) || query.DtoOptions.EnableImages || query.DtoOptions.EnableUserData || query.DtoOptions.ImageTypeLimit != 3 || !reflect.DeepEqual(query.DtoOptions.ImageTypes, []item.ImageType{item.ImageTypePrimary, item.ImageTypeBackdrop}) {
		t.Fatalf("legacy latest DTO options = %#v", query.DtoOptions)
	}
}

func TestLatestItemsUsesTargetUserHidePlayedPreferenceOnlyWhenIsPlayedIsOmitted(t *testing.T) {
	userID, movieID := latestUUID(t, 31), latestUUID(t, 32)
	t.Run("omitted is played uses target preference", func(t *testing.T) {
		preferences := &latestPreferences{hidePlayed: true}
		reader := &latestReader{latest: []item.Item{{ID: movieID, Kind: item.KindMovie}}}
		record := httptest.NewRecorder()
		latestRouterWithPreferences(reader, &latestBuilder{}, preferences).ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Items/Latest?userId="+userID.Dashed()+"&groupItems=false", nil))
		if record.Code != http.StatusOK || preferences.calls != 1 || reader.seen[0].IsPlayed == nil || *reader.seen[0].IsPlayed {
			t.Fatalf("omitted isPlayed status/preferences/query = %d/%d/%#v, want 200/1/false", record.Code, preferences.calls, reader.seen)
		}
	})
	t.Run("explicit is played wins", func(t *testing.T) {
		preferences := &latestPreferences{hidePlayed: true, err: errors.New("must not be read")}
		reader := &latestReader{latest: []item.Item{{ID: movieID, Kind: item.KindMovie}}}
		record := httptest.NewRecorder()
		latestRouterWithPreferences(reader, &latestBuilder{}, preferences).ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Items/Latest?userId="+userID.Dashed()+"&isPlayed=true&groupItems=false", nil))
		if record.Code != http.StatusOK || preferences.calls != 0 || reader.seen[0].IsPlayed == nil || !*reader.seen[0].IsPlayed {
			t.Fatalf("explicit isPlayed status/preferences/query = %d/%d/%#v, want 200/0/true", record.Code, preferences.calls, reader.seen)
		}
	})
	t.Run("missing target user maps not found", func(t *testing.T) {
		preferences := &latestPreferences{err: store.ErrNotFound}
		reader := &latestReader{}
		record := httptest.NewRecorder()
		latestRouterWithPreferences(reader, &latestBuilder{}, preferences).ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Items/Latest?userId="+userID.Dashed(), nil))
		if record.Code != http.StatusNotFound || reader.queries != 0 {
			t.Fatalf("missing target status/reads = %d/%d, want 404/0", record.Code, reader.queries)
		}
	})
}

func TestLatestUserPreferencesPreservesUserConfigurationDefaults(t *testing.T) {
	userID := latestUUID(t, 33)
	users := &latestUsers{user: store.User{ID: userID, Configuration: []byte(`{}`)}}
	preferences := NewLatestUserPreferences(users)
	hidePlayed, err := preferences.HidePlayedInLatest(t.Context(), userID)
	if err != nil || !hidePlayed {
		t.Fatalf("empty configuration HidePlayedInLatest = %t, %v; want true, nil", hidePlayed, err)
	}
	users.user.Configuration = []byte(`{"HidePlayedInLatest":false}`)
	hidePlayed, err = preferences.HidePlayedInLatest(t.Context(), userID)
	if err != nil || hidePlayed {
		t.Fatalf("configured HidePlayedInLatest = %t, %v; want false, nil", hidePlayed, err)
	}
}

func TestLatestItemsResolvesAudioAndPhotoContainersWithoutAlternateVersionGrouping(t *testing.T) {
	folderID, albumID, photoAlbumID := latestUUID(t, 40), latestUUID(t, 41), latestUUID(t, 42)
	audioID, photoID := latestUUID(t, 43), latestUUID(t, 44)
	reader := &latestReader{
		latest: []item.Item{
			{ID: audioID, Kind: item.KindAudio, ParentID: &folderID},
			{ID: photoID, Kind: item.KindPhoto, ParentID: &photoAlbumID},
		},
		byID: map[dto.UUID]item.Item{
			folderID:     {ID: folderID, Kind: item.KindFolder, ParentID: &albumID},
			albumID:      {ID: albumID, Kind: item.KindMusicAlbum},
			photoAlbumID: {ID: photoAlbumID, Kind: item.KindPhotoAlbum},
		},
	}
	record := httptest.NewRecorder()
	latestRouter(reader, &latestBuilder{}).ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Items/Latest?limit=2", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET /Items/Latest status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	var body []map[string]json.RawMessage
	if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode latest response: %v", err)
	}
	if len(body) != 2 || string(body[0]["Id"]) != `"`+albumID.String()+`"` || string(body[0]["ChildCount"]) != "1" || string(body[1]["Id"]) != `"`+photoID.String()+`"` {
		t.Fatalf("latest per-kind grouping = %s, want music album count then ungrouped photo", record.Body.String())
	}
	if _, present := body[1]["ChildCount"]; present {
		t.Fatalf("single-photo ChildCount must be omitted: %s", record.Body.String())
	}
}

func TestLatestItemsProjectsEligibleSubtitleScoreOnlyAtTopLevelInBothProfiles(t *testing.T) {
	userID, itemID := latestUUID(t, 50), latestUUID(t, 51)
	external, score := true, 111222
	codec := "srt"
	builder := &latestBuilder{items: []dto.BaseItemDto{{
		ID: itemID,
		MediaStreams: []dto.MediaStreamInfo{
			{Index: 0, Type: dto.MediaStreamType("Subtitle"), Codec: &codec, IsExternal: &external},
			{Index: 1, Type: dto.MediaStreamType("Subtitle"), Codec: &codec},
			{Index: 2, Type: dto.MediaStreamType("Video")},
		},
		MediaSources: []dto.MediaSourceInfo{{MediaStreams: []dto.MediaStreamInfo{
			{Index: 0, Type: dto.MediaStreamType("Subtitle"), Codec: &codec, IsExternal: &external, Score: &score},
			{Index: 1, Type: dto.MediaStreamType("Subtitle"), Codec: &codec},
			{Index: 2, Type: dto.MediaStreamType("Video")},
		}}},
	}}}
	reader := &latestReader{latest: []item.Item{{ID: itemID, Kind: item.KindMovie}}}
	rt := latestRouter(reader, builder)
	for _, tc := range []struct {
		name, accept, streamsKey, sourcesKey, scoreKey string
	}{
		{name: "Pascal", streamsKey: "MediaStreams", sourcesKey: "MediaSources", scoreKey: "Score"},
		{name: "Camel", accept: `application/json; profile="CamelCase"`, streamsKey: "mediaStreams", sourcesKey: "mediaSources", scoreKey: "score"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/Users/"+userID.Dashed()+"/Items/Latest?groupItems=false&fields=MediaStreams,MediaSources", nil)
			request.Header.Set("Accept", tc.accept)
			rt.ServeHTTP(record, request)
			if record.Code != http.StatusOK {
				t.Fatalf("GET latest status = %d, want 200; body=%s", record.Code, record.Body.String())
			}
			var body []map[string]json.RawMessage
			if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil || len(body) != 1 {
				t.Fatalf("decode latest response = %s, %v; want one item", record.Body.String(), err)
			}
			var topLevel []map[string]json.RawMessage
			if err := json.Unmarshal(body[0][tc.streamsKey], &topLevel); err != nil || len(topLevel) != 3 {
				t.Fatalf("decode top-level streams = %s, %v; want three", body[0][tc.streamsKey], err)
			}
			if got := string(topLevel[0][tc.scoreKey]); got != "111222" {
				t.Errorf("eligible top-level sidecar Score = %s, want 111222", got)
			}
			for _, stream := range topLevel[1:] {
				if _, present := stream[tc.scoreKey]; present {
					t.Errorf("noneligible top-level stream Score = %s, want omitted", stream[tc.scoreKey])
				}
			}
			var sources []map[string]json.RawMessage
			if err := json.Unmarshal(body[0][tc.sourcesKey], &sources); err != nil || len(sources) != 1 {
				t.Fatalf("decode nested sources = %s, %v; want one", body[0][tc.sourcesKey], err)
			}
			var sourceStreams []map[string]json.RawMessage
			if err := json.Unmarshal(sources[0][tc.streamsKey], &sourceStreams); err != nil || len(sourceStreams) != 3 {
				t.Fatalf("decode nested streams = %s, %v; want three", sources[0][tc.streamsKey], err)
			}
			if got := string(sourceStreams[0][tc.scoreKey]); got != "111222" {
				t.Errorf("nested source sidecar Score = %s, want retained 111222", got)
			}
			for _, stream := range sourceStreams[1:] {
				if _, present := stream[tc.scoreKey]; present {
					t.Errorf("noneligible nested source stream Score = %s, want omitted", stream[tc.scoreKey])
				}
			}
		})
	}
	if builder.items[0].MediaStreams[0].Score != nil {
		t.Fatalf("latest projection mutated builder-owned top-level streams: %#v", builder.items[0].MediaStreams)
	}
}

func TestLatestItemsMapsMalformedMissingAndServiceFailures(t *testing.T) {
	validID := latestUUID(t, 30)
	for _, tc := range []struct {
		name   string
		path   string
		reader *latestReader
		build  *latestBuilder
		status int
		reads  int
		builds int
		body   string
	}{
		{name: "malformed query user", path: "/Items/Latest?userId=not-a-guid", reader: &latestReader{}, build: &latestBuilder{}, status: http.StatusBadRequest},
		{name: "malformed legacy user", path: "/Users/not-a-guid/Items/Latest", reader: &latestReader{}, build: &latestBuilder{}, status: http.StatusBadRequest},
		{name: "unknown type is dropped", path: "/Items/Latest?includeItemTypes=not-a-kind", reader: &latestReader{}, build: &latestBuilder{}, status: http.StatusOK, reads: 1, builds: 1, body: "[]"},
		{name: "negative limit", path: "/Items/Latest?limit=-1", reader: &latestReader{}, build: &latestBuilder{}, status: http.StatusBadRequest},
		{name: "reader failure", path: "/Items/Latest", reader: &latestReader{err: errors.New("unavailable")}, build: &latestBuilder{}, status: http.StatusInternalServerError, reads: 1},
		{name: "projection failure", path: "/Items/Latest", reader: &latestReader{latest: []item.Item{{ID: validID, Kind: item.KindMovie}}}, build: &latestBuilder{err: errors.New("unavailable")}, status: http.StatusInternalServerError, reads: 1, builds: 1},
		{name: "empty latest is array", path: "/Items/Latest", reader: &latestReader{}, build: &latestBuilder{}, status: http.StatusOK, reads: 1, builds: 1, body: "[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := latestRouter(tc.reader, tc.build)
			record := httptest.NewRecorder()
			rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if record.Code != tc.status {
				t.Fatalf("GET %s status = %d, want %d; body=%s", tc.path, record.Code, tc.status, record.Body.String())
			}
			if tc.reader.queries != tc.reads || tc.build.calls != tc.builds {
				t.Fatalf("calls reader/build = %d/%d, want %d/%d", tc.reader.queries, tc.build.calls, tc.reads, tc.builds)
			}
			if tc.body != "" && record.Body.String() != tc.body {
				t.Fatalf("latest body = %q, want %q", record.Body.String(), tc.body)
			}
		})
	}
}

func latestRouter(reader ItemReader, build ItemDTOBuilder) *router.Router {
	return latestRouterWithPreferences(reader, build, &latestPreferences{hidePlayed: true})
}

func latestRouterWithPreferences(reader ItemReader, build ItemDTOBuilder, preferences LatestUserPreferences) *router.Router {
	rt := router.New()
	h := itemHandler{reader: reader, build: build, latestPreferences: preferences}
	policy := string(auth.PolicyDefaultAuthorization)
	rt.Handle(http.MethodGet, "/Items/Latest", router.Route{OperationID: "GetLatestMedia", Policy: policy}, h.latest)
	rt.Handle(http.MethodGet, "/Users/{userId}/Items/Latest", router.Route{OperationID: "SHADOW:GetLatestMediaLegacy", Policy: policy}, h.legacyLatest)
	return rt
}

type latestReader struct {
	latest  []item.Item
	byID    map[dto.UUID]item.Item
	err     error
	queries int
	seen    []store.ItemQuery
}

func (reader *latestReader) Query(_ context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	reader.queries++
	reader.seen = append(reader.seen, query)
	if reader.err != nil {
		return store.ItemQueryResult{}, reader.err
	}
	if len(query.ItemIDs) == 0 {
		return store.ItemQueryResult{Items: append([]item.Item(nil), reader.latest...)}, nil
	}
	items := make([]item.Item, 0, len(query.ItemIDs))
	for _, id := range query.ItemIDs {
		if item, found := reader.byID[id]; found {
			items = append(items, item)
		}
	}
	return store.ItemQueryResult{Items: items}, nil
}

type latestPreferences struct {
	hidePlayed bool
	err        error
	calls      int
}

func (preferences *latestPreferences) HidePlayedInLatest(_ context.Context, _ dto.UUID) (bool, error) {
	preferences.calls++
	return preferences.hidePlayed, preferences.err
}

type latestBuilder struct {
	err   error
	calls int
	items []dto.BaseItemDto
}

func (builder *latestBuilder) Build(_ context.Context, values []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	builder.calls++
	if builder.err != nil {
		return dto.QueryResult[dto.BaseItemDto]{}, builder.err
	}
	out := dto.NewQueryResult[dto.BaseItemDto]()
	if builder.items != nil {
		out.Items = append([]dto.BaseItemDto(nil), builder.items...)
		return out, nil
	}
	out.Items = make([]dto.BaseItemDto, 0, len(values))
	for _, value := range values {
		out.Items = append(out.Items, dto.BaseItemDto{ID: value.ID})
	}
	return out, nil
}

func (builder *latestBuilder) BuildDetailed(ctx context.Context, values []item.Item, options store.ItemQueryDTOOptions, userID *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	return builder.Build(ctx, values, options, userID)
}

type latestUsers struct {
	user store.User
	err  error
}

func (users *latestUsers) Get(_ context.Context, _ dto.UUID) (store.User, error) {
	if users.err != nil {
		return store.User{}, users.err
	}
	return users.user, nil
}

func latestUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse latest fixture %d: %v", value, err)
	}
	return id
}
