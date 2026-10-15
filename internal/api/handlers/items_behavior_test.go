// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestItemsHandlerMapsRequestExecutesReaderBuildsOnePageAndRendersProfiles
// crosses the public route boundary: bind once, execute one ItemReader query,
// project exactly the selected rows once, and publish a QueryResult in either
// supported JSON casing. It intentionally observes ItemReader rather than SQL.
func TestItemsHandlerMapsRequestExecutesReaderBuildsOnePageAndRendersProfiles(t *testing.T) {
	userID := itemsHandlerUUID(t, 1)
	first, second := itemsHandlerUUID(t, 2), itemsHandlerUUID(t, 3)
	stored := item.Item{ID: first, Kind: item.KindMovie}
	reader := &itemsHandlerReader{result: store.ItemQueryResult{Items: []item.Item{stored}, TotalRecordCount: 7}}
	builder := &itemsHandlerBuilder{items: []dto.BaseItemDto{{ID: first, Name: "Projected"}}}
	rt := router.New()
	handlers.RegisterItems(rt, reader, builder, clock.NewReal())
	path := fmt.Sprintf("/items?userId=%s&ids=%s,%s&startIndex=1&limit=1", userID, first, second)

	for _, tc := range []struct {
		name, accept, itemsKey, totalKey, startKey, idKey string
	}{
		{name: "pascal", itemsKey: "Items", totalKey: "TotalRecordCount", startKey: "StartIndex", idKey: "Id"},
		{name: "camel", accept: `application/json; profile="CamelCase"`, itemsKey: "items", totalKey: "totalRecordCount", startKey: "startIndex", idKey: "id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			if tc.accept != "" {
				request.Header.Set("Accept", tc.accept)
			}
			rt.ServeHTTP(record, request)
			if record.Code != http.StatusOK {
				t.Fatalf("GET /Items status = %d, want 200; body=%s", record.Code, record.Body.String())
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode Items response: %v; body=%s", err, record.Body.String())
			}
			if _, found := body[tc.itemsKey]; !found {
				t.Fatalf("response keys %v, missing %q", body, tc.itemsKey)
			}
			if string(body[tc.totalKey]) != "7" || string(body[tc.startKey]) != "1" {
				t.Fatalf("result page/count = %s/%s, want 1/7", body[tc.startKey], body[tc.totalKey])
			}
			var rows []map[string]json.RawMessage
			if err := json.Unmarshal(body[tc.itemsKey], &rows); err != nil || len(rows) != 1 {
				t.Fatalf("response Items = %s, want one projected row; err=%v", body[tc.itemsKey], err)
			}
			if got := string(rows[0][tc.idKey]); got != `"`+first.String()+`"` {
				t.Fatalf("projected id = %s, want %s", got, first.String())
			}
		})
	}

	// The obsolete twin supplies the effective user from its route; an ignored
	// conflicting query userId cannot redirect a legacy request.
	legacy := httptest.NewRecorder()
	rt.ServeHTTP(legacy, httptest.NewRequest(http.MethodGet, "/Users/"+userID.Dashed()+"/Items?userId="+second.Dashed()+"&ids="+first.Dashed()+"&startIndex=1&limit=1", nil))
	if legacy.Code != http.StatusOK {
		t.Fatalf("GET legacy Items status = %d, want 200; body=%s", legacy.Code, legacy.Body.String())
	}
	if reader.last.UserID == nil || *reader.last.UserID != userID {
		t.Fatalf("legacy ItemQuery user = %v, want route user %s", reader.last.UserID, userID)
	}

	if got, want := reader.queries, 3; got != want {
		t.Fatalf("ItemReader queries = %d, want %d", got, want)
	}
	if got, want := builder.calls, 3; got != want {
		t.Fatalf("ItemDTOBuilder calls = %d, want %d", got, want)
	}
	primaryQuery := reader.seen[0]
	if primaryQuery.StartIndex == nil || *primaryQuery.StartIndex != 1 || primaryQuery.Limit == nil || *primaryQuery.Limit != 1 || !reflect.DeepEqual(primaryQuery.ItemIDs, []dto.UUID{first, second}) || primaryQuery.UserID == nil || *primaryQuery.UserID != userID {
		t.Fatalf("primary reader ItemQuery = %#v, want mapped user, IDs, and page", primaryQuery)
	}
	if !reflect.DeepEqual(reader.last.ItemIDs, []dto.UUID{first}) {
		t.Fatalf("legacy ItemQuery ids = %v, want legacy request IDs [%s]", reader.last.ItemIDs, first)
	}
}

// TestItemsHandlerForwardsProjectionFieldsSearchAndImageDefaults crosses the
// public Items route into its reader and projection seam. ItemsController
// builds DtoOptions from fields and passes SearchTerm to InternalItemsQuery;
// its DtoOptions default enables images when EnableImages is absent.
func TestItemsHandlerForwardsProjectionFieldsSearchAndImageDefaults(t *testing.T) {
	userID, itemID := itemsHandlerUUID(t, 90), itemsHandlerUUID(t, 91)
	wantFields := []string{"MediaSources", "ParentId"}
	for _, tc := range []struct {
		name, parameters string
		wantImages       bool
	}{
		{name: "case-insensitive CSV defaults images", parameters: "fIeLdS=MediaSources,ParentId&sEaRcHtErM=needle", wantImages: true},
		{name: "Swiftfin repeated fields default images", parameters: "fields=MediaSources&fields=ParentId&searchTerm=needle", wantImages: true},
		{name: "explicit false", parameters: "Fields=MediaSources,ParentId&SearchTerm=needle&EnableImages=false", wantImages: false},
		{name: "Infuse Emby field is dropped", parameters: "fields=MediaSources,AlternateMediaSources,parentid&searchTerm=needle", wantImages: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &itemsHandlerReader{result: store.ItemQueryResult{Items: []item.Item{{ID: itemID, Kind: item.KindMovie}}}}
			builder := &itemsHandlerBuilder{items: []dto.BaseItemDto{{ID: itemID}}}
			rt := router.New()
			handlers.RegisterItems(rt, reader, builder, clock.NewReal())
			record := httptest.NewRecorder()
			rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Items?userId="+userID.Dashed()+"&"+tc.parameters, nil))
			if record.Code != http.StatusOK {
				t.Fatalf("GET /Items status = %d, want 200; body=%s", record.Code, record.Body.String())
			}
			if reader.queries != 1 || builder.calls != 1 {
				t.Fatalf("reader/projection calls = %d/%d, want 1/1", reader.queries, builder.calls)
			}
			if reader.last.SearchTerm != "needle" || !reflect.DeepEqual(reader.last.DtoOptions.Fields, wantFields) || reader.last.DtoOptions.EnableImages != tc.wantImages {
				t.Fatalf("reader ItemQuery = %#v, want SearchTerm/Fields/EnableImages %q/%v/%t", reader.last, "needle", wantFields, tc.wantImages)
			}
			if len(builder.options) != 1 || !reflect.DeepEqual(builder.options[0].Fields, wantFields) || builder.options[0].EnableImages != tc.wantImages {
				t.Fatalf("projection DtoOptions = %#v, want Fields/EnableImages %v/%t", builder.options, wantFields, tc.wantImages)
			}
		})
	}
}

// TestItemsHandlerForwardsStockManifestControls verifies the handler's public
// request-to-reader seam. It intentionally observes the ItemReader input, so a
// future route-local parser cannot silently drop a control the shared binder
// already understands.
func TestItemsHandlerForwardsStockManifestControls(t *testing.T) {
	userID, itemID := itemsHandlerUUID(t, 115), itemsHandlerUUID(t, 116)
	date := "2024-07-27T12:34:56Z"
	reader := &itemsHandlerReader{result: store.ItemQueryResult{Items: []item.Item{{ID: itemID, Kind: item.KindMovie}}}}
	builder := &itemsHandlerBuilder{items: []dto.BaseItemDto{{ID: itemID}}}
	rt := router.New()
	handlers.RegisterItems(rt, reader, builder, clock.NewReal())

	path := "/Items?userId=" + userID.Dashed() + "&iSpLaCeHoLdEr=false&VideoTypes=BluRay,VideoFile&hAsSuBtItLeS=false&iSmIsSiNg=false&iSuNaIrEd=false&hAsImDbId=false&hAsTmDbId=false&iSfAvOrItE=false&mAxPrEmIeReDaTe=" + date + "&mInDaTeLaStSaVeDfOrUsEr=" + date + "&fIlTeRs=IsNotFolder,IsUnplayed,Dislikes,IsFavoriteOrLikes&SortBy=SortName,DateCreated&SortOrder=Descending&SortOrder=Ascending"
	record := httptest.NewRecorder()
	rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET /Items status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	if reader.queries != 1 || builder.calls != 1 {
		t.Fatalf("reader/projection calls = %d/%d, want 1/1", reader.queries, builder.calls)
	}

	query := reader.last
	falseValue, trueValue := false, true
	if query.IsPlaceHolder == nil || *query.IsPlaceHolder || !reflect.DeepEqual(query.VideoTypes, []item.VideoType{item.VideoTypeBluRay, item.VideoTypeVideoFile}) || query.HasSubtitles == nil || *query.HasSubtitles || query.IsMissing == nil || *query.IsMissing || query.IsUnaired == nil || *query.IsUnaired || query.HasIMDbID == nil || *query.HasIMDbID || query.HasTMDbID == nil || *query.HasTMDbID || query.IsFavorite == nil || *query.IsFavorite {
		t.Fatalf("reader ItemQuery missing explicit stock controls: %#v", query)
	}
	if !reflect.DeepEqual(query.IsFolder, &falseValue) || !reflect.DeepEqual(query.IsPlayed, &falseValue) || !reflect.DeepEqual(query.IsLiked, &falseValue) || !reflect.DeepEqual(query.IsFavoriteOrLiked, &trueValue) {
		t.Fatalf("reader ItemQuery Filters mapping = %#v, want IsNotFolder/IsUnplayed/Dislikes/IsFavoriteOrLikes", query)
	}
	if query.MaxPremiereDate == nil || query.MaxPremiereDate.Format(time.RFC3339) != date || query.MinDateLastSavedForUser == nil || query.MinDateLastSavedForUser.Format(time.RFC3339) != date {
		t.Fatalf("reader ItemQuery dates = %#v, want max premiere and min saved %s", query, date)
	}
	if want := []store.ItemSortBy{store.ItemSortBySortName, store.ItemSortByDateCreated}; !reflect.DeepEqual(query.SortBy, want) {
		t.Fatalf("reader SortBy = %v, want %v", query.SortBy, want)
	}
	if want := []store.SortOrder{store.SortOrderDescending, store.SortOrderAscending}; !reflect.DeepEqual(query.SortOrder, want) {
		t.Fatalf("reader SortOrder = %v, want independent directions %v", query.SortOrder, want)
	}
}

// TestItemsRouteResolvesFullTargetUserBeforeApplyingPlaylistVisibility keeps the
// route-to-store handoff honest: playlist visibility is keyed to the persisted
// target policy, not merely the UserId query scalar.
func TestItemsRouteResolvesFullTargetUserBeforeApplyingPlaylistVisibility(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open item access store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close item access store: %v", err)
		}
	})

	owner := store.User{ID: itemsHandlerUUID(t, 100), Policy: []byte(`{"EnableAllFolders":false,"EnabledFolders":[]}`)}
	viewer := store.User{ID: itemsHandlerUUID(t, 101), Policy: []byte(`{"EnableAllFolders":false,"EnabledFolders":[]}`)}
	administrator := store.User{ID: itemsHandlerUUID(t, 102), Policy: []byte(`{"IsAdministrator":true,"EnableAllFolders":true}`)}
	private := item.Item{ID: itemsHandlerUUID(t, 110), Kind: item.KindPlaylist, Name: itemsHandlerString("private playlist"), SortName: itemsHandlerString("private playlist"), PlaylistOwnerUserID: owner.ID}
	public := item.Item{ID: itemsHandlerUUID(t, 111), Kind: item.KindPlaylist, Name: itemsHandlerString("source public playlist"), SortName: itemsHandlerString("source public playlist"), PlaylistOwnerUserID: owner.ID, OpenAccess: true}
	items := store.NewItemStore(stores)
	for _, value := range []item.Item{private, public} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("seed %s: %v", value.ID, err)
		}
	}

	reader := &itemsAccessReader{items: items}
	users := itemsAccessUsers{values: map[dto.UUID]store.User{owner.ID: owner, viewer.ID: viewer, administrator.ID: administrator}}
	authorizer := middleware.NewAuthorizer(itemsAccessResolver{identities: map[string]auth.Identity{
		owner.ID.String():         {Authenticated: true, UserID: owner.ID},
		viewer.ID.String():        {Authenticated: true, UserID: viewer.ID},
		administrator.ID.String(): {Authenticated: true, UserID: administrator.ID, Administrator: true},
	}}, itemsAccessPolicy{}, users)
	rt := router.New(authorizer.Wrap)
	handlers.RegisterItems(rt, reader, &itemsAccessBuilder{}, clock.NewReal(), handlers.ItemServices{Users: users})

	for _, tc := range []struct {
		name string
		user store.User
		want []dto.UUID
	}{
		{name: "playlist owner sees private and source-public rows", user: owner, want: []dto.UUID{private.ID, public.ID}},
		{name: "unshared viewer cannot infer private playlist", user: viewer, want: []dto.UUID{public.ID}},
		{name: "administrator sees private and source-public rows", user: administrator, want: []dto.UUID{private.ID, public.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/Items?userId="+tc.user.ID.Dashed()+"&includeItemTypes=Playlist&sortBy=SortName", nil)
			request.Header.Set("X-Item-Access-Actor", tc.user.ID.String())
			rt.ServeHTTP(record, request)
			if record.Code != http.StatusOK {
				t.Fatalf("GET /Items status = %d, want 200; body=%s", record.Code, record.Body.String())
			}
			if len(reader.queries) == 0 {
				t.Fatal("GET /Items did not query the store")
			}
			query := reader.queries[len(reader.queries)-1]
			if query.UserID == nil || *query.UserID != tc.user.ID || query.User == nil || !reflect.DeepEqual(*query.User, tc.user) {
				t.Fatalf("store access user = UserID:%v User:%#v, want full target %#v", query.UserID, query.User, tc.user)
			}
			var body dto.QueryResult[dto.BaseItemDto]
			if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode Items response: %v", err)
			}
			got := make([]dto.UUID, len(body.Items))
			for index := range body.Items {
				got[index] = body.Items[index].ID
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("visible playlist IDs = %v, want %v", got, tc.want)
			}
		})
	}
	queries := len(reader.queries)
	unauthenticated := httptest.NewRecorder()
	rt.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/Items?userId="+viewer.ID.Dashed()+"&includeItemTypes=Playlist", nil))
	if unauthenticated.Code != http.StatusUnauthorized || len(reader.queries) != queries {
		t.Fatalf("unauthenticated Items status/queries = %d/%d, want 401/%d", unauthenticated.Code, len(reader.queries), queries)
	}
}

type itemsAccessReader struct {
	items   *store.ItemStore
	queries []store.ItemQuery
}

func (reader *itemsAccessReader) Query(ctx context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	reader.queries = append(reader.queries, query)
	return reader.items.Query(ctx, query)
}

func (reader *itemsAccessReader) QueryItemNames(ctx context.Context, query store.ItemQuery) ([]store.ItemName, error) {
	reader.queries = append(reader.queries, query)
	return reader.items.QueryItemNames(ctx, query)
}

func (reader *itemsAccessReader) QueryPersonCredits(ctx context.Context, query store.ItemQuery) ([]item.PersonCredit, error) {
	reader.queries = append(reader.queries, query)
	return reader.items.QueryPersonCredits(ctx, query)
}

func (reader *itemsAccessReader) QueryItemValues(ctx context.Context, query store.ItemQuery, kinds ...string) ([]string, error) {
	reader.queries = append(reader.queries, query)
	return reader.items.QueryItemValues(ctx, query, kinds...)
}

type itemsAccessUsers struct{ values map[dto.UUID]store.User }

func (users itemsAccessUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	value, found := users.values[id]
	if !found {
		return store.User{}, store.ErrNotFound
	}
	return value, nil
}

type itemsAccessBuilder struct{}

func (itemsAccessBuilder) Build(_ context.Context, values []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	items := make([]dto.BaseItemDto, len(values))
	for index := range values {
		items[index] = dto.BaseItemDto{ID: values[index].ID}
	}
	return dto.QueryResult[dto.BaseItemDto]{Items: items}, nil
}

func (itemsAccessBuilder) BuildDetailed(ctx context.Context, values []item.Item, options store.ItemQueryDTOOptions, userID *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	return itemsAccessBuilder{}.Build(ctx, values, options, userID)
}

type itemsAccessResolver struct{ identities map[string]auth.Identity }

func (resolver itemsAccessResolver) Resolve(_ context.Context, request *http.Request) (auth.Identity, error) {
	return resolver.identities[request.Header.Get("X-Item-Access-Actor")], nil
}

type itemsAccessPolicy struct{}

func (itemsAccessPolicy) Evaluate(_ context.Context, request auth.PolicyRequest) (auth.Decision, error) {
	if !request.Identity.Authenticated {
		return auth.DecisionUnauthenticated, nil
	}
	return auth.DecisionAllowed, nil
}

func itemsHandlerString(value string) *string { return &value }

type itemsHandlerReader struct {
	result  store.ItemQueryResult
	err     error
	queries int
	last    store.ItemQuery
	seen    []store.ItemQuery
}

func (reader *itemsHandlerReader) Query(_ context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	reader.queries++
	reader.last = query
	reader.seen = append(reader.seen, query)
	return reader.result, reader.err
}

type itemsHandlerBuilder struct {
	detailedCalls int
	items         []dto.BaseItemDto
	err           error
	calls         int
	options       []store.ItemQueryDTOOptions
}

func (builder *itemsHandlerBuilder) Build(_ context.Context, _ []item.Item, options store.ItemQueryDTOOptions, _ *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	builder.calls++
	builder.options = append(builder.options, options)
	return dto.QueryResult[dto.BaseItemDto]{Items: builder.items}, builder.err
}

func (builder *itemsHandlerBuilder) BuildDetailed(ctx context.Context, values []item.Item, options store.ItemQueryDTOOptions, userID *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	builder.detailedCalls++
	return builder.Build(ctx, values, options, userID)
}

func itemsHandlerUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing fixture UUID %d: %v", value, err)
	}
	return id
}
