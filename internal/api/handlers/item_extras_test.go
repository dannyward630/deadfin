// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

func TestItemExtrasRoutesRegisterSourceOperations(t *testing.T) {
	rt := router.New()
	handlers.RegisterItemExtras(rt, nil, nil)
	for _, want := range []router.Route{
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/Similar", OperationID: "GetSimilarItems", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/SpecialFeatures", OperationID: "GetSpecialFeatures", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/LocalTrailers", OperationID: "GetLocalTrailers", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Users/{userId}/Items/{itemId}/LocalTrailers", OperationID: "SHADOW:GetLocalTrailersLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Users/{userId}/Items/{itemId}/SpecialFeatures", OperationID: "SHADOW:GetSpecialFeaturesLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/Intros", OperationID: "GetIntros", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Users/{userId}/Items/{itemId}/Intros", OperationID: "SHADOW:GetIntrosLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Videos/{itemId}/AdditionalParts", OperationID: "GetAdditionalPart", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/Ancestors", OperationID: "GetAncestors", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/ThemeMedia", OperationID: "GetThemeMedia", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/MediaSegments/{itemId}", OperationID: "GetItemSegments", Policy: string(auth.PolicyDefaultAuthorization)},
	} {
		got, found := routeByOperation(rt.Routes(), want.OperationID)
		if !found || got != want {
			t.Fatalf("route %q = %#v, found %t, want %#v", want.OperationID, got, found, want)
		}
	}
}

// TestSimilarAliasesRegisterExactOperationsAndUseSharedService pins the six
// upstream aliases on LibraryController.GetSimilarItems. Each must retain its
// OpenAPI operation ID while dispatching the requested source kind through the
// same similarity service and returning the standard query-result payload.
func TestSimilarAliasesRegisterExactOperationsAndUseSharedService(t *testing.T) {
	itemID := itemsHandlerUUID(t, 800)
	aliases := []struct {
		path        string
		operationID string
		kind        item.Kind
	}{
		{path: "/Items/{itemId}/Similar", operationID: "GetSimilarItems", kind: item.KindMovie},
		{path: "/Artists/{itemId}/Similar", operationID: "GetSimilarArtists", kind: item.KindMusicArtist},
		{path: "/Albums/{itemId}/Similar", operationID: "GetSimilarAlbums", kind: item.KindMusicAlbum},
		{path: "/Shows/{itemId}/Similar", operationID: "GetSimilarShows", kind: item.KindSeries},
		{path: "/Movies/{itemId}/Similar", operationID: "GetSimilarMovies", kind: item.KindMovie},
		{path: "/Trailers/{itemId}/Similar", operationID: "GetSimilarTrailers", kind: item.KindTrailer},
	}

	ledger := router.New()
	handlers.RegisterItemExtras(ledger, nil, nil)
	for _, alias := range aliases {
		t.Run("ledger "+alias.operationID, func(t *testing.T) {
			want := router.Route{
				Method:      http.MethodGet,
				Pattern:     alias.path,
				OperationID: alias.operationID,
				Policy:      string(auth.PolicyDefaultAuthorization),
			}
			got, found := routeByOperation(ledger.Routes(), alias.operationID)
			if !found || got != want {
				t.Fatalf("route %q = %#v, found %t, want %#v", alias.operationID, got, found, want)
			}
		})
	}

	service := &itemExtrasSimilarRecorder{}
	for _, alias := range aliases {
		t.Run("dispatch "+alias.operationID, func(t *testing.T) {
			service.requests = nil
			reader := &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{{ID: itemID, Kind: alias.kind}}}}}
			build := &itemExtrasBuilder{}
			rt := router.New()
			handlers.RegisterItemExtras(rt, reader, build, service)

			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, strings.Replace(alias.path, "{itemId}", itemID.Dashed(), 1), nil))
			if rec.Code != http.StatusOK || len(rec.Body.Bytes()) == 0 || rec.Body.Bytes()[0] != '{' {
				t.Fatalf("GET %s = %d %q, want QueryResult", alias.path, rec.Code, rec.Body.String())
			}
			if len(service.requests) != 1 || service.requests[0].Item.ID != itemID || service.requests[0].Item.Kind != alias.kind {
				t.Fatalf("similarity requests = %#v, want one %s source through shared service", service.requests, alias.kind)
			}
			if build.calls != 1 {
				t.Fatalf("projection calls = %d, want one shared Similar response projection", build.calls)
			}
		})
	}
}

func TestItemExtrasUseSourceScopedRelationsAndBareArrays(t *testing.T) {
	userID, itemID := itemsHandlerUUID(t, 801), itemsHandlerUUID(t, 802)
	source := item.Item{ID: itemID, Kind: item.KindMovie}
	projected := dto.BaseItemDto{ID: itemsHandlerUUID(t, 803), Name: "Trailer"}
	for _, tc := range []struct {
		name, path string
		wantTypes  []item.ExtraType
		legacy     bool
	}{
		{name: "trailers", path: "/Items/" + itemID.Dashed() + "/LocalTrailers?userId=" + userID.Dashed(), wantTypes: []item.ExtraType{item.ExtraTypeTrailer}},
		{name: "special features", path: "/Items/" + itemID.Dashed() + "/SpecialFeatures?userId=" + userID.Dashed(), wantTypes: []item.ExtraType{item.ExtraTypeUnknown, item.ExtraTypeBehindTheScenes, item.ExtraTypeClip, item.ExtraTypeDeletedScene, item.ExtraTypeInterview, item.ExtraTypeSample, item.ExtraTypeScene, item.ExtraTypeFeaturette, item.ExtraTypeShort}},
		{name: "legacy ignores query user", path: "/Users/" + userID.Dashed() + "/Items/" + itemID.Dashed() + "/LocalTrailers?userId=" + itemsHandlerUUID(t, 804).Dashed(), wantTypes: []item.ExtraType{item.ExtraTypeTrailer}, legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{source}}, {Items: []item.Item{{ID: projected.ID, Kind: item.KindTrailer}}}}}
			builder := &itemExtrasBuilder{items: []dto.BaseItemDto{projected}}
			rt := router.New()
			handlers.RegisterItemExtras(rt, reader, builder)
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200: %s", tc.path, rec.Code, rec.Body.String())
			}
			if len(rec.Body.Bytes()) == 0 || rec.Body.Bytes()[0] != '[' {
				t.Fatalf("GET %s body = %q, want bare array", tc.path, rec.Body.String())
			}
			var rows []dto.BaseItemDto
			if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].ID != projected.ID {
				t.Fatalf("decode bare related array = %#v, %v", rows, err)
			}
			if len(reader.queries) != 2 {
				t.Fatalf("reader queries = %d, want source plus relation", len(reader.queries))
			}
			relation := reader.queries[1]
			if !reflect.DeepEqual(relation.OwnerIDs, []dto.UUID{itemID}) || !reflect.DeepEqual(relation.ExtraTypes, tc.wantTypes) || !reflect.DeepEqual(relation.SortBy, []store.ItemSortBy{store.ItemSortBySortName}) || !reflect.DeepEqual(relation.SortOrder, []store.SortOrder{store.SortOrderAscending}) || relation.UserID == nil || *relation.UserID != userID {
				t.Fatalf("related ItemQuery = %#v, want owner/type/sort/user overlay", relation)
			}
			if builder.calls != 1 || !reflect.DeepEqual(builder.lastItems, reader.results[1].Items) {
				t.Fatalf("projection calls/input = %d/%#v, want one relation page", builder.calls, builder.lastItems)
			}
		})
	}
}

func TestVideoExtrasUseCurrentPrimaryAndLinkedAlternateOwners(t *testing.T) {
	userID, currentID, primaryID, currentLinkedID, primaryLinkedID := itemsHandlerUUID(t, 805), itemsHandlerUUID(t, 806), itemsHandlerUUID(t, 807), itemsHandlerUUID(t, 808), itemsHandlerUUID(t, 809)
	current := item.Item{ID: currentID, Kind: item.KindMovie, PrimaryVersionID: &primaryID, LinkedChildren: []item.LinkedChildRef{{ItemID: currentLinkedID, Type: item.LinkedChildTypeLinkedAlternateVersion}}}
	primary := item.Item{ID: primaryID, Kind: item.KindMovie, LinkedChildren: []item.LinkedChildRef{{ItemID: primaryLinkedID, Type: item.LinkedChildTypeLinkedAlternateVersion}}}
	reader := &itemExtrasReader{results: []store.ItemQueryResult{
		{Items: []item.Item{current}},
		{Items: []item.Item{primary}},
		{Items: []item.Item{{ID: currentLinkedID, Kind: item.KindMovie}, {ID: primaryLinkedID, Kind: item.KindVideo}, {ID: itemsHandlerUUID(t, 810), Kind: item.KindAudio}}},
		{},
	}}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+currentID.Dashed()+"/LocalTrailers?userId="+userID.Dashed(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET LocalTrailers = %d: %s", rec.Code, rec.Body.String())
	}
	if len(reader.queries) != 4 || !reflect.DeepEqual(reader.queries[1].ItemIDs, []dto.UUID{primaryID}) || !reflect.DeepEqual(reader.queries[2].ItemIDs, []dto.UUID{currentLinkedID, primaryLinkedID}) {
		t.Fatalf("video owner graph reads = %#v, want primary and forward alternate lookups", reader.queries)
	}
	if got := reader.queries[3].OwnerIDs; !reflect.DeepEqual(got, []dto.UUID{currentID, primaryID, currentLinkedID, primaryLinkedID}) {
		t.Fatalf("extras owner IDs = %#v, want current/primary/linked graph", got)
	}
}

func TestExtrasZeroItemIDUsesOnlyPersistedUserRoot(t *testing.T) {
	rootID := itemsHandlerUUID(t, 811)
	reader := &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{{ID: rootID, Kind: item.KindUserRootFolder}}}, {}}}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/00000000-0000-0000-0000-000000000000/LocalTrailers", nil))
	if rec.Code != http.StatusOK || len(reader.queries) != 2 || !reflect.DeepEqual(reader.queries[0].IncludeItemTypes, []item.Kind{item.KindUserRootFolder}) || len(reader.queries[0].ItemIDs) != 0 {
		t.Fatalf("zero item root resolution = status %d queries %#v, want persisted UserRootFolder only", rec.Code, reader.queries)
	}

	missing := &itemExtrasReader{results: []store.ItemQueryResult{{}}}
	rt = router.New()
	handlers.RegisterItemExtras(rt, missing, &itemExtrasBuilder{})
	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/00000000-0000-0000-0000-000000000000/LocalTrailers", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("missing persisted root = %d, want invariant 500", rec.Code)
	}
}

func TestVideoExtrasIgnoreMissingPrimaryVersion(t *testing.T) {
	currentID, missingPrimaryID := itemsHandlerUUID(t, 812), itemsHandlerUUID(t, 813)
	reader := &itemExtrasReader{results: []store.ItemQueryResult{
		{Items: []item.Item{{ID: currentID, Kind: item.KindMovie, PrimaryVersionID: &missingPrimaryID}}},
		{},
		{},
	}}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+currentID.Dashed()+"/SpecialFeatures", nil))
	if rec.Code != http.StatusOK || len(reader.queries) != 3 || !reflect.DeepEqual(reader.queries[2].OwnerIDs, []dto.UUID{currentID}) {
		t.Fatalf("missing primary extras = status %d queries %#v, want current owner only", rec.Code, reader.queries)
	}
}

func TestAdditionalPartsResolvePersistedPathsFilterNonVideoAndSort(t *testing.T) {
	userID, itemID := itemsHandlerUUID(t, 810), itemsHandlerUUID(t, 811)
	firstPath, secondPath := "/media/part-two.mkv", "/media/part-one.mkv"
	zName, aName := "z", "a"
	partZ := item.Item{ID: itemsHandlerUUID(t, 812), Kind: item.KindVideo, SortName: &zName}
	partA := item.Item{ID: itemsHandlerUUID(t, 813), Kind: item.KindMovie, SortName: &aName}
	reader := &itemExtrasReader{results: []store.ItemQueryResult{
		{Items: []item.Item{{ID: itemID, Kind: item.KindMovie, AdditionalParts: []string{firstPath, secondPath}}}},
		{Items: []item.Item{partZ, {ID: itemsHandlerUUID(t, 814), Kind: item.KindAudio}}},
		{Items: []item.Item{partA}},
	}}
	builder := &itemExtrasBuilder{items: []dto.BaseItemDto{{ID: partA.ID}, {ID: partZ.ID}}}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, builder)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Videos/"+itemID.Dashed()+"/AdditionalParts?userId="+userID.Dashed(), nil))
	if rec.Code != http.StatusOK || len(rec.Body.Bytes()) == 0 || rec.Body.Bytes()[0] != '{' {
		t.Fatalf("GET AdditionalParts = %d %q, want QueryResult", rec.Code, rec.Body.String())
	}
	if len(reader.queries) != 3 || reader.queries[1].Path != firstPath || reader.queries[2].Path != secondPath {
		t.Fatalf("additional-part path reads = %#v, want ordered persisted path lookups", reader.queries)
	}
	if reader.queries[1].UserID == nil || *reader.queries[1].UserID != userID || reader.queries[2].UserID == nil || *reader.queries[2].UserID != userID {
		t.Fatalf("additional-part path lookups did not carry user visibility: %#v", reader.queries)
	}
	if !reflect.DeepEqual(builder.lastItems, []item.Item{partA, partZ}) {
		t.Fatalf("additional projection items = %#v, want visible videos sorted by SortName", builder.lastItems)
	}
}

func TestAdditionalPartsReturnsEmptyQueryResultForNonVideoAndMissingSourceMaps404(t *testing.T) {
	itemID := itemsHandlerUUID(t, 820)
	for _, tc := range []struct {
		name   string
		reader *itemExtrasReader
		status int
	}{
		{name: "non video", reader: &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{{ID: itemID, Kind: item.KindAudio}}}}}, status: http.StatusOK},
		{name: "missing", reader: &itemExtrasReader{results: []store.ItemQueryResult{{}}}, status: http.StatusNotFound},
		{name: "reader failure", reader: &itemExtrasReader{errs: []error{errors.New("offline")}}, status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := router.New()
			handlers.RegisterItemExtras(rt, tc.reader, &itemExtrasBuilder{})
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Videos/"+itemID.Dashed()+"/AdditionalParts", nil))
			if rec.Code != tc.status {
				t.Fatalf("GET AdditionalParts %s = %d, want %d: %s", tc.name, rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusOK {
				var page dto.QueryResult[dto.BaseItemDto]
				if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || page.Items == nil || len(page.Items) != 0 || page.TotalRecordCount != 0 || page.StartIndex != 0 {
					t.Fatalf("empty AdditionalParts page = %#v, %v", page, err)
				}
			}
		})
	}
}

// TestItemExtrasRestrictedUserPoliciesDoNotLeakRelatedRows verifies source,
// relation, and additional-part lookups carry the resolved target user rather
// than only its ID. Otherwise a caller allowed to see a movie can enumerate
// blocked extras and sidecar parts.
func TestItemExtrasRestrictedUserPoliciesDoNotLeakRelatedRows(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open extras access store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close extras access store: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	userID, rootID, sourceID := itemsHandlerUUID(t, 940), itemsHandlerUUID(t, 941), itemsHandlerUUID(t, 942)
	policy := `{"EnableAllFolders":false,"EnabledFolders":["` + rootID.String() + `"],"BlockedTags":["blocked"],"AllowedTags":["allowed"]}`
	user := store.User{ID: userID, Policy: []byte(policy)}
	allowedPath, blockedPath := "/media/allowed.mkv", "/media/blocked.mkv"
	trailerExtraType := item.ExtraTypeTrailer
	allowedPartID, blockedPartID := itemsHandlerUUID(t, 943), itemsHandlerUUID(t, 944)
	root := item.Item{ID: rootID, Kind: item.KindFolder, IsFolder: true, Name: itemsHandlerString("visible root"), SortName: itemsHandlerString("visible root")}
	source := item.Item{ID: sourceID, Kind: item.KindMovie, ParentID: &rootID, Name: itemsHandlerString("source"), SortName: itemsHandlerString("source"), Values: item.ItemValues{Tags: []string{"allowed"}}, AdditionalParts: []string{allowedPath, blockedPath}}
	allowedExtra := item.Item{ID: itemsHandlerUUID(t, 945), Kind: item.KindTrailer, ParentID: &rootID, OwnerID: &sourceID, ExtraType: &trailerExtraType, Name: itemsHandlerString("allowed trailer"), SortName: itemsHandlerString("allowed trailer"), Values: item.ItemValues{Tags: []string{"allowed"}}}
	blockedExtra := item.Item{ID: itemsHandlerUUID(t, 946), Kind: item.KindTrailer, ParentID: &rootID, OwnerID: &sourceID, ExtraType: &trailerExtraType, Name: itemsHandlerString("blocked trailer"), SortName: itemsHandlerString("blocked trailer"), Values: item.ItemValues{Tags: []string{"allowed", "blocked"}}}
	allowedPart := item.Item{ID: allowedPartID, Kind: item.KindMovie, ParentID: &rootID, Path: &allowedPath, Name: itemsHandlerString("allowed part"), SortName: itemsHandlerString("allowed part"), Values: item.ItemValues{Tags: []string{"allowed"}}}
	blockedPart := item.Item{ID: blockedPartID, Kind: item.KindMovie, ParentID: &rootID, Path: &blockedPath, Name: itemsHandlerString("blocked part"), SortName: itemsHandlerString("blocked part"), Values: item.ItemValues{Tags: []string{"allowed", "blocked"}}}
	for _, value := range []item.Item{root, source, allowedExtra, blockedExtra, allowedPart, blockedPart} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("seed %s: %v", value.ID, err)
		}
	}
	persistedExtra, err := items.Get(t.Context(), allowedExtra.ID)
	if err != nil {
		t.Fatalf("load persisted allowed extra: %v", err)
	}
	if persistedExtra.OwnerID == nil || *persistedExtra.OwnerID != sourceID || persistedExtra.ExtraType == nil || *persistedExtra.ExtraType != item.ExtraTypeTrailer {
		t.Fatalf("persisted allowed extra = %#v, want owner %s and trailer type", persistedExtra, sourceID)
	}
	allExtras, err := items.Query(t.Context(), store.ItemQuery{OwnerIDs: []dto.UUID{sourceID}, ExtraTypes: []item.ExtraType{item.ExtraTypeTrailer}, SortBy: []store.ItemSortBy{store.ItemSortBySortName}, SortOrder: []store.SortOrder{store.SortOrderAscending}})
	if err != nil {
		t.Fatalf("query unrestricted extras fixture: %v", err)
	}
	if got, want := itemExtrasItemIDs(allExtras.Items), []dto.UUID{allowedExtra.ID, blockedExtra.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unrestricted extras store IDs = %v, want %v", got, want)
	}
	visibleExtras, err := items.Query(t.Context(), store.ItemQuery{User: &user, OwnerIDs: []dto.UUID{sourceID}, ExtraTypes: []item.ExtraType{item.ExtraTypeTrailer}, SortBy: []store.ItemSortBy{store.ItemSortBySortName}, SortOrder: []store.SortOrder{store.SortOrderAscending}})
	if err != nil {
		t.Fatalf("query restricted extras fixture: %v", err)
	}
	if got, want := itemExtrasItemIDs(visibleExtras.Items), []dto.UUID{allowedExtra.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restricted extras store IDs = %v, want %v", got, want)
	}

	reader := &itemsAccessReader{items: items}
	users := itemsAccessUsers{values: map[dto.UUID]store.User{userID: user}}
	authorizer := middleware.NewAuthorizer(itemsAccessResolver{identities: map[string]auth.Identity{userID.String(): {Authenticated: true, UserID: userID}}}, itemsAccessPolicy{}, users)
	rt := router.New(authorizer.Wrap)
	handlers.RegisterItemExtras(rt, reader, itemsAccessBuilder{}, users)
	request := func(path string) *httptest.ResponseRecorder {
		record := httptest.NewRecorder()
		httpRequest := httptest.NewRequest(http.MethodGet, path+"?userId="+userID.Dashed(), nil)
		httpRequest.Header.Set("X-Item-Access-Actor", userID.String())
		rt.ServeHTTP(record, httpRequest)
		return record
	}

	extras := request("/Items/" + sourceID.Dashed() + "/LocalTrailers")
	if extras.Code != http.StatusOK {
		t.Fatalf("GET LocalTrailers = %d, want 200: %s", extras.Code, extras.Body.String())
	}
	var extraRows []dto.BaseItemDto
	if err := json.Unmarshal(extras.Body.Bytes(), &extraRows); err != nil {
		t.Fatalf("decode LocalTrailers: %v", err)
	}
	if got, want := itemExtrasDTOIDs(extraRows), []dto.UUID{allowedExtra.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restricted extras = %v, want %v; queries=%#v", got, want, reader.queries)
	}

	parts := request("/Videos/" + sourceID.Dashed() + "/AdditionalParts")
	if parts.Code != http.StatusOK {
		t.Fatalf("GET AdditionalParts = %d, want 200: %s", parts.Code, parts.Body.String())
	}
	var partPage dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(parts.Body.Bytes(), &partPage); err != nil {
		t.Fatalf("decode AdditionalParts: %v", err)
	}
	if got, want := itemExtrasDTOIDs(partPage.Items), []dto.UUID{allowedPartID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restricted additional parts = %v, want %v", got, want)
	}
	for _, query := range reader.queries {
		if query.UserID != nil && *query.UserID == userID && !reflect.DeepEqual(query.User, &user) {
			t.Fatalf("extras query user = %#v, want persisted target %#v for %#v", query.User, user, query)
		}
	}
}

func itemExtrasDTOIDs(values []dto.BaseItemDto) []dto.UUID {
	ids := make([]dto.UUID, len(values))
	for index := range values {
		ids[index] = values[index].ID
	}
	return ids
}

func itemExtrasItemIDs(values []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(values))
	for index := range values {
		ids[index] = values[index].ID
	}
	return ids
}

func TestSimilarUsesWeightedMovieProviderAndReturnsQueryResult(t *testing.T) {
	userID, itemID, candidateID := itemsHandlerUUID(t, 830), itemsHandlerUUID(t, 831), itemsHandlerUUID(t, 832)
	excluded := itemsHandlerUUID(t, 833)
	reader := &itemExtrasReader{results: []store.ItemQueryResult{
		{Items: []item.Item{{ID: itemID, Kind: item.KindMovie, Values: item.ItemValues{Genres: []string{"Drama"}}}}},
		{Items: []item.Item{{ID: candidateID, Kind: item.KindMovie, Values: item.ItemValues{Genres: []string{"Drama"}}}}, TotalRecordCount: 9},
	}}
	builder := &itemExtrasBuilder{items: []dto.BaseItemDto{{ID: candidateID}}}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, builder)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/Similar?userId="+userID.Dashed()+"&excludeArtistIds="+excluded.Dashed()+"&limit=3&fields=People,AlternateMediaSources,overview", nil))
	if rec.Code != http.StatusOK || len(rec.Body.Bytes()) == 0 || rec.Body.Bytes()[0] != '{' {
		t.Fatalf("GET Similar = %d %q, want QueryResult", rec.Code, rec.Body.String())
	}
	candidate := reader.queries[1]
	if !reflect.DeepEqual(candidate.ExcludeItemIDs, []dto.UUID{itemID}) || !reflect.DeepEqual(candidate.ExcludeArtistIDs, []dto.UUID{excluded}) || !reflect.DeepEqual(candidate.IncludeItemTypes, []item.Kind{item.KindMovie}) || candidate.Limit != nil || candidate.UserID == nil || *candidate.UserID != userID || !reflect.DeepEqual(candidate.DtoOptions.Fields, []string{"People", "Overview"}) || !reflect.DeepEqual(candidate.Genres, []string{"Drama"}) || candidate.IsMovie == nil || !*candidate.IsMovie || candidate.IsPlayed == nil || *candidate.IsPlayed || candidate.EnableTotalRecordCount {
		t.Fatalf("weighted movie candidate query = %#v, want unbounded deterministic candidate collection before score limiting", candidate)
	}
	var page dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || page.TotalRecordCount != 1 || len(page.Items) != 1 {
		t.Fatalf("similar page = %#v, %v", page, err)
	}

	unsupported := &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{{ID: itemID, Kind: item.KindEpisode}}}}}
	rt = router.New()
	handlers.RegisterItemExtras(rt, unsupported, &itemExtrasBuilder{})
	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/Similar", nil))
	if rec.Code != http.StatusOK || len(unsupported.queries) != 1 {
		t.Fatalf("unsupported similar = %d reads=%d, want empty successful source gate", rec.Code, len(unsupported.queries))
	}
}

func TestSimilarMovieWeightsDirectorsAboveGenreAndDeduplicates(t *testing.T) {
	itemID, genreID, directorID, personID := itemsHandlerUUID(t, 850), itemsHandlerUUID(t, 851), itemsHandlerUUID(t, 852), itemsHandlerUUID(t, 853)
	reader := &itemExtrasReader{results: []store.ItemQueryResult{
		{Items: []item.Item{{ID: itemID, Kind: item.KindMovie, Values: item.ItemValues{Genres: []string{"Drama"}}, People: []item.PersonCredit{{PersonID: personID, Type: item.PersonKindDirector}}}}},
		{Items: []item.Item{{ID: genreID, Kind: item.KindMovie, Values: item.ItemValues{Genres: []string{"Drama"}}}}},
		{Items: []item.Item{{ID: directorID, Kind: item.KindMovie, People: []item.PersonCredit{{PersonID: personID, Type: item.PersonKindDirector}}}, {ID: genreID, Kind: item.KindMovie, Values: item.ItemValues{Genres: []string{"Drama"}}}}},
	}}
	builder := &itemExtrasBuilder{items: []dto.BaseItemDto{{ID: directorID}, {ID: genreID}}}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, builder)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/Similar?limit=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET Similar = %d: %s", rec.Code, rec.Body.String())
	}
	if len(builder.lastItems) != 2 || builder.lastItems[0].ID != directorID || builder.lastItems[1].ID != genreID {
		t.Fatalf("weighted movie order = %#v, want director then genre", builder.lastItems)
	}
	if len(reader.queries) != 3 || !reflect.DeepEqual(reader.queries[2].PersonIDs, []dto.UUID{personID}) {
		t.Fatalf("movie person candidate query = %#v, want scored director lookup", reader.queries)
	}
}

// TestSimilarMovieRanksFullCandidateUnionDeterministically proves that movie
// ranking happens after every eligible dimension has contributed candidates.
// Each dimension places the best multi-match item after more than the former
// 3× requested-limit candidate window; the two genre-only runners-up also pin
// the ID-ascending tie break independently of reader order.
func TestSimilarMovieRanksFullCandidateUnionDeterministically(t *testing.T) {
	sourceID := itemsHandlerUUID(t, 875)
	highID := itemsHandlerUUID(t, 999)
	source := item.Item{
		ID:   sourceID,
		Kind: item.KindMovie,
		Values: item.ItemValues{
			Genres: []string{"Drama"},
			Tags:   []string{"Award winner"},
		},
	}
	high := item.Item{
		ID:   highID,
		Kind: item.KindMovie,
		Values: item.ItemValues{
			Genres: []string{"Drama"},
			Tags:   []string{"Award winner"},
		},
	}
	genreCandidates := make([]item.Item, 0, 11)
	tagCandidates := make([]item.Item, 0, 11)
	for offset := range 10 {
		genreCandidates = append(genreCandidates, item.Item{
			ID:     itemsHandlerUUID(t, 880+offset),
			Kind:   item.KindMovie,
			Values: item.ItemValues{Genres: []string{"Drama"}},
		})
		tagCandidates = append(tagCandidates, item.Item{
			ID:     itemsHandlerUUID(t, 900+offset),
			Kind:   item.KindMovie,
			Values: item.ItemValues{Tags: []string{"Award winner"}},
		})
	}
	genreCandidates = append(genreCandidates, high)
	tagCandidates = append(tagCandidates, high)

	reverse := func(values []item.Item) []item.Item {
		result := append([]item.Item(nil), values...)
		for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
			result[left], result[right] = result[right], result[left]
		}
		return result
	}
	want := []dto.UUID{highID, genreCandidates[0].ID, genreCandidates[1].ID}
	var first []dto.UUID
	for _, tc := range []struct {
		name  string
		genre []item.Item
		tag   []item.Item
	}{
		{name: "reader order", genre: genreCandidates, tag: tagCandidates},
		{name: "reversed reader order", genre: reverse(genreCandidates), tag: reverse(tagCandidates)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &itemExtrasReader{results: []store.ItemQueryResult{
				{Items: []item.Item{source}},
				{Items: tc.genre},
				{Items: tc.tag},
			}}
			builder := &itemExtrasBuilder{}
			rt := router.New()
			handlers.RegisterItemExtras(rt, reader, builder)
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+sourceID.Dashed()+"/Similar?limit=3", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET Similar = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			got := make([]dto.UUID, len(builder.lastItems))
			for index := range builder.lastItems {
				got[index] = builder.lastItems[index].ID
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ranked similar IDs = %v, want high multi-match then ID-stable genre tie %v", got, want)
			}
			if first == nil {
				first = got
			} else if !reflect.DeepEqual(got, first) {
				t.Fatalf("ranked similar IDs changed with reader order: %v then %v", first, got)
			}
		})
	}
}

func TestSimilarMusicProviderPreservesGenreTagQuerySemantics(t *testing.T) {
	itemID, candidateID := itemsHandlerUUID(t, 860), itemsHandlerUUID(t, 861)
	reader := &itemExtrasReader{results: []store.ItemQueryResult{
		{Items: []item.Item{{ID: itemID, Kind: item.KindMusicAlbum, Values: item.ItemValues{Genres: []string{"Jazz"}, Tags: []string{"Live"}}}}},
		{Items: []item.Item{{ID: candidateID, Kind: item.KindMusicAlbum}}},
	}}
	builder := &itemExtrasBuilder{items: []dto.BaseItemDto{{ID: candidateID}}}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, builder)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/Similar?limit=7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET Similar = %d: %s", rec.Code, rec.Body.String())
	}
	if len(reader.queries) != 2 {
		t.Fatalf("music similar reads = %d, want source plus provider", len(reader.queries))
	}
	query := reader.queries[1]
	if !reflect.DeepEqual(query.Genres, []string{"Jazz"}) || !reflect.DeepEqual(query.Tags, []string{"Live"}) || !reflect.DeepEqual(query.IncludeItemTypes, []item.Kind{item.KindMusicAlbum}) || query.Limit == nil || *query.Limit != 7 || !query.EnableTotalRecordCount || query.IsMovie != nil || query.IsPlayed != nil || !reflect.DeepEqual(query.SortBy, []store.ItemSortBy{store.ItemSortByRandom}) || !reflect.DeepEqual(query.SortOrder, []store.SortOrder{store.SortOrderAscending}) {
		t.Fatalf("music provider query = %#v, want upstream local genre/tag query", query)
	}
}

func TestSimilarMapsProviderFailureToInternalServerError(t *testing.T) {
	itemID := itemsHandlerUUID(t, 870)
	reader := &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{{ID: itemID, Kind: item.KindMovie}}}}}
	builder := &itemExtrasBuilder{}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, builder, itemExtrasSimilarService{err: errors.New("provider unavailable")})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/Similar", nil))
	if rec.Code != http.StatusInternalServerError || builder.calls != 0 {
		t.Fatalf("GET Similar provider failure = %d builds=%d, want 500/no projection", rec.Code, builder.calls)
	}
}

// TestSimilarMovieUsesPersistedItemValueCleanValue covers the Store-backed
// scoring boundary: candidate discovery and final weighting must use the same
// normalized metadata fact even when the persisted display spellings differ.
func TestSimilarMovieUsesPersistedItemValueCleanValue(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open similar-items store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close similar-items store: %v", err)
		}
	})

	userID, sourceID, highScoreID, lowScoreID := itemsHandlerUUID(t, 880), itemsHandlerUUID(t, 881), itemsHandlerUUID(t, 882), itemsHandlerUUID(t, 883)
	items := store.NewItemStore(stores)
	source := item.Item{
		ID:       sourceID,
		Kind:     item.KindMovie,
		IsMovie:  true,
		Name:     itemsHandlerString("source raw spelling"),
		SortName: itemsHandlerString("source raw spelling"),
		Values: item.ItemValues{
			Genres: []string{"Génre / Target"},
			Tags:   []string{"Täg / Target"},
		},
	}
	highScore := item.Item{
		ID:       highScoreID,
		Kind:     item.KindMovie,
		IsMovie:  true,
		Name:     itemsHandlerString("canonical genre and tag"),
		SortName: itemsHandlerString("canonical genre and tag"),
		Values: item.ItemValues{
			Genres: []string{"genre target"},
			Tags:   []string{"tag target"},
		},
	}
	lowScore := item.Item{
		ID:       lowScoreID,
		Kind:     item.KindMovie,
		IsMovie:  true,
		Name:     itemsHandlerString("canonical genre"),
		SortName: itemsHandlerString("canonical genre"),
		Values:   item.ItemValues{Genres: []string{"genre target"}},
	}
	for _, value := range []item.Item{source, highScore, lowScore} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("seed similar item %s: %v", value.ID, err)
		}
	}

	// Confirm this remains a persisted raw-value regression: the fixtures share
	// normalized lookup keys but do not share their hydrated display values.
	persistedSource, err := items.Get(t.Context(), sourceID)
	if err != nil {
		t.Fatalf("get persisted source: %v", err)
	}
	persistedHighScore, err := items.Get(t.Context(), highScoreID)
	if err != nil {
		t.Fatalf("get persisted high-score candidate: %v", err)
	}
	if persistedSource.Values.Genres[0] == persistedHighScore.Values.Genres[0] || item.CleanName(persistedSource.Values.Genres[0]) != item.CleanName(persistedHighScore.Values.Genres[0]) || persistedSource.Values.Tags[0] == persistedHighScore.Values.Tags[0] || item.CleanName(persistedSource.Values.Tags[0]) != item.CleanName(persistedHighScore.Values.Tags[0]) {
		t.Fatalf("persisted item values = %#v / %#v, want distinct raw spellings with equal clean values", persistedSource.Values, persistedHighScore.Values)
	}

	reader := &itemExtrasStoreReader{items: items}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, dtobuild.NewBuilder(items, nil))
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+sourceID.Dashed()+"/Similar?userId="+userID.Dashed()+"&limit=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET Similar = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var page dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode Similar response: %v; body=%s", err, rec.Body.String())
	}
	got := make([]dto.UUID, len(page.Items))
	for index := range page.Items {
		got[index] = page.Items[index].ID
	}
	if want := []dto.UUID{highScoreID, lowScoreID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("similar movie IDs = %v, want clean-value weighted order %v", got, want)
	}
}

// TestVideoExtrasUseLocalAndLinkedAlternateOwnerGraphStableOrderAndDedupe
// follows Video.GetExtraOwnerIds through persisted linked and local alternate
// relations. A shared local alternate proves first-seen deduplication.
func TestVideoExtrasUseLocalAndLinkedAlternateOwnerGraphStableOrderAndDedupe(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open extras store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close extras store: %v", err)
		}
	})

	currentID, currentLinkedID, currentLocalID, currentLinkedLocalID := itemsHandlerUUID(t, 891), itemsHandlerUUID(t, 892), itemsHandlerUUID(t, 893), itemsHandlerUUID(t, 894)
	items := store.NewItemStore(stores)
	current := item.Item{
		ID:       currentID,
		Kind:     item.KindMovie,
		Name:     itemsHandlerString("current"),
		SortName: itemsHandlerString("current"),
		LinkedChildren: []item.LinkedChildRef{
			{ItemID: currentLinkedID, Type: item.LinkedChildTypeLinkedAlternateVersion},
			{ItemID: currentLocalID, Type: item.LinkedChildTypeLocalAlternateVersion},
		},
	}
	currentLinked := item.Item{
		ID:       currentLinkedID,
		Kind:     item.KindMovie,
		Name:     itemsHandlerString("current linked"),
		SortName: itemsHandlerString("a current linked"),
		LinkedChildren: []item.LinkedChildRef{
			{ItemID: currentLocalID, Type: item.LinkedChildTypeLocalAlternateVersion},
			{ItemID: currentLinkedLocalID, Type: item.LinkedChildTypeLocalAlternateVersion},
		},
	}
	for _, value := range []item.Item{
		current,
		currentLinked,
		{ID: currentLocalID, Kind: item.KindMovie, Name: itemsHandlerString("current local"), SortName: itemsHandlerString("current local")},
		{ID: currentLinkedLocalID, Kind: item.KindMovie, Name: itemsHandlerString("current linked local"), SortName: itemsHandlerString("current linked local")},
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("seed alternate graph item %s: %v", value.ID, err)
		}
	}

	for _, tc := range []struct {
		name string
		id   dto.UUID
		want []item.LinkedChildRef
	}{
		{name: "source", id: currentID, want: current.LinkedChildren},
		{name: "linked alternate", id: currentLinkedID, want: currentLinked.LinkedChildren},
	} {
		t.Run("persisted "+tc.name, func(t *testing.T) {
			got, err := items.LinkedChildren(t.Context(), tc.id)
			if err != nil {
				t.Fatalf("load persisted links: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("persisted links = %#v, want %#v", got, tc.want)
			}
		})
	}

	owners := []dto.UUID{currentID, currentLinkedID, currentLocalID, currentLinkedLocalID}
	reader := &itemExtrasStoreReader{items: items}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+currentID.Dashed()+"/LocalTrailers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET LocalTrailers = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var relation *store.ItemQuery
	for index := range reader.queries {
		if len(reader.queries[index].OwnerIDs) != 0 {
			relation = &reader.queries[index]
			break
		}
	}
	if relation == nil {
		t.Fatalf("extras reads = %#v, want an owner-scoped relation query", reader.queries)
	}
	if !reflect.DeepEqual(relation.OwnerIDs, owners) {
		t.Fatalf("extras owner IDs = %v, want first-seen linked graph plus local alternates %v", relation.OwnerIDs, owners)
	}
}

func TestItemExtrasRejectMalformedRouteAndQueryBeforeReads(t *testing.T) {
	for _, path := range []string{
		"/Items/not-a-guid/LocalTrailers",
		"/Users/not-a-guid/Items/00000000-0000-0000-0000-000000000840/SpecialFeatures",
		"/Items/00000000-0000-0000-0000-000000000840/Similar?limit=-1",
		"/Items/00000000-0000-0000-0000-000000000840/Similar?excludeArtistIds=not-a-guid",
		"/Items/not-a-guid/Intros",
		"/Users/not-a-guid/Items/00000000-0000-0000-0000-000000000840/Intros",
	} {
		t.Run(path, func(t *testing.T) {
			reader := &itemExtrasReader{}
			rt := router.New()
			handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusBadRequest || len(reader.queries) != 0 {
				t.Fatalf("GET %s = %d reads=%d, want 400/no reads", path, rec.Code, len(reader.queries))
			}
		})
	}
}

// TestItemIntrosCurrentAndLegacy covers both intro aliases, legacy user
// precedence, missing sources, malformed identifiers, and the empty query
// result. Both routes dispatch to the same handler and return an empty query
// result with a non-nil Items array when the source exists.
func TestItemIntrosCurrentAndLegacy(t *testing.T) {
	itemID := itemsHandlerUUID(t, 840)
	otherID := itemsHandlerUUID(t, 841)
	userID := itemsHandlerUUID(t, 842)

	t.Run("current returns empty query result for valid source", func(t *testing.T) {
		reader := &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{{ID: itemID, Kind: item.KindMovie}}}}}
		builder := &itemExtrasBuilder{}
		rt := router.New()
		handlers.RegisterItemExtras(rt, reader, builder)
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/Intros", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /Items/{id}/Intros = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var page dto.QueryResult[dto.BaseItemDto]
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("unmarshal intros page: %v", err)
		}
		if page.Items == nil || len(page.Items) != 0 || page.TotalRecordCount != 0 || page.StartIndex != 0 {
			t.Fatalf("empty intros page = %#v, want Items: [] TotalRecordCount: 0 StartIndex: 0", page)
		}
		if builder.calls != 1 {
			t.Fatalf("builder calls = %d, want 1", builder.calls)
		}
	})

	t.Run("legacy returns empty query result for valid source", func(t *testing.T) {
		reader := &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{{ID: itemID, Kind: item.KindMovie}}}}}
		builder := &itemExtrasBuilder{}
		rt := router.New()
		handlers.RegisterItemExtras(rt, reader, builder)
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Users/"+userID.Dashed()+"/Items/"+itemID.Dashed()+"/Intros", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /Users/{uid}/Items/{id}/Intros = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var page dto.QueryResult[dto.BaseItemDto]
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("unmarshal legacy intros page: %v", err)
		}
		if page.Items == nil || len(page.Items) != 0 || page.TotalRecordCount != 0 || page.StartIndex != 0 {
			t.Fatalf("empty legacy intros page = %#v, want Items: [] TotalRecordCount: 0 StartIndex: 0", page)
		}
	})

	t.Run("legacy uses route user and ignores conflicting query user", func(t *testing.T) {
		reader := &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{{ID: itemID, Kind: item.KindMovie}}}}}
		rt := router.New()
		handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
		rec := httptest.NewRecorder()
		path := "/Users/" + userID.Dashed() + "/Items/" + itemID.Dashed() + "/Intros?userId=" + otherID.Dashed()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET legacy intros with conflicting query user = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if len(reader.queries) == 0 {
			t.Fatal("no reader queries recorded")
		}
		query := reader.queries[0]
		if query.UserID == nil || *query.UserID != userID {
			t.Fatalf("legacy intros query UserID = %v, want route user %s", query.UserID, userID)
		}
	})

	t.Run("missing source returns 404", func(t *testing.T) {
		reader := &itemExtrasReader{results: []store.ItemQueryResult{{}}}
		builder := &itemExtrasBuilder{}
		rt := router.New()
		handlers.RegisterItemExtras(rt, reader, builder)
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/Intros", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET intros missing source = %d, want 404: %s", rec.Code, rec.Body.String())
		}
		if builder.calls != 0 {
			t.Fatalf("builder calls = %d, want 0 for missing source", builder.calls)
		}
	})

	t.Run("malformed item id returns 400 without reads", func(t *testing.T) {
		reader := &itemExtrasReader{}
		rt := router.New()
		handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/not-a-guid/Intros", nil))
		if rec.Code != http.StatusBadRequest || len(reader.queries) != 0 {
			t.Fatalf("GET intros malformed item = %d reads=%d, want 400/0", rec.Code, len(reader.queries))
		}
	})

	t.Run("malformed legacy user id returns 400 without reads", func(t *testing.T) {
		reader := &itemExtrasReader{}
		rt := router.New()
		handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Users/not-a-guid/Items/"+itemID.Dashed()+"/Intros", nil))
		if rec.Code != http.StatusBadRequest || len(reader.queries) != 0 {
			t.Fatalf("GET legacy intros malformed user = %d reads=%d, want 400/0", rec.Code, len(reader.queries))
		}
	})
}

// TestAncestorsReturnsBareArrayNearestParentFirst pins the GetAncestors
// response shape and chain order. The response is a bare JSON array of
// BaseItemDto, nearest parent first. An item with no parents returns [].
func TestAncestorsReturnsBareArrayNearestParentFirst(t *testing.T) {
	userID := itemsHandlerUUID(t, 850)
	itemID, parentID, grandparentID := itemsHandlerUUID(t, 851), itemsHandlerUUID(t, 852), itemsHandlerUUID(t, 853)
	source := item.Item{ID: itemID, Kind: item.KindMovie, ParentID: &parentID}
	parent := item.Item{ID: parentID, Kind: item.KindFolder, ParentID: &grandparentID}
	grandparent := item.Item{ID: grandparentID, Kind: item.KindFolder}
	reader := &itemExtrasReader{results: []store.ItemQueryResult{
		{Items: []item.Item{source}},
		{Items: []item.Item{parent}},
		{Items: []item.Item{grandparent}},
		{},
	}}
	builder := &itemExtrasIDBuilder{}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, builder)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/Ancestors?userId="+userID.Dashed(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET Ancestors = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(rec.Body.Bytes()) == 0 || rec.Body.Bytes()[0] != '[' {
		t.Fatalf("GET Ancestors body = %q, want bare array", rec.Body.String())
	}
	var rows []dto.BaseItemDto
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode Ancestors: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != parentID || rows[1].ID != grandparentID {
		t.Fatalf("ancestor order = %v, want nearest parent first: parent=%s grandparent=%s", rows, parentID, grandparentID)
	}
	if len(reader.queries) != 3 || !reflect.DeepEqual(reader.queries[1].ItemIDs, []dto.UUID{parentID}) || !reflect.DeepEqual(reader.queries[2].ItemIDs, []dto.UUID{grandparentID}) {
		t.Fatalf("ancestor chain reads = %d queries, want 3 (source + parent + grandparent)", len(reader.queries))
	}
	if reader.queries[1].UserID == nil || *reader.queries[1].UserID != userID {
		t.Fatalf("ancestor chain lookups did not carry user visibility: %#v", reader.queries)
	}
}

// TestAncestorsReturnsEmptyArrayForItemWithoutParents verifies that an item
// with no ParentID returns a bare empty array.
func TestAncestorsReturnsEmptyArrayForItemWithoutParents(t *testing.T) {
	itemID := itemsHandlerUUID(t, 854)
	source := item.Item{ID: itemID, Kind: item.KindMovie}
	reader := &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{source}}}}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/Ancestors", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET Ancestors = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(rec.Body.Bytes()) == 0 || rec.Body.Bytes()[0] != '[' {
		t.Fatalf("GET Ancestors body = %q, want bare array", rec.Body.String())
	}
	var rows []dto.BaseItemDto
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || len(rows) != 0 {
		t.Fatalf("ancestors without parents = %v, want empty array", rows)
	}
}

// TestAncestorsReturns404ForUnknownItem verifies that a missing item maps to
// 404.
func TestAncestorsReturns404ForUnknownItem(t *testing.T) {
	reader := &itemExtrasReader{results: []store.ItemQueryResult{{}}}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemsHandlerUUID(t, 855).Dashed()+"/Ancestors", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET Ancestors unknown item = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// TestAncestorsStopsBeforeAggregateFolderForUserRequest verifies that a user
// request stops before the AggregateFolder. A nil-user (global API key)
// request emits the aggregate root. This is the stock null-translate break.
func TestAncestorsStopsBeforeAggregateFolderForUserRequest(t *testing.T) {
	userID := itemsHandlerUUID(t, 856)
	itemID, folderID, aggregateID := itemsHandlerUUID(t, 857), itemsHandlerUUID(t, 858), itemsHandlerUUID(t, 859)
	source := item.Item{ID: itemID, Kind: item.KindMovie, ParentID: &folderID}
	folder := item.Item{ID: folderID, Kind: item.KindFolder, ParentID: &aggregateID}
	aggregate := item.Item{ID: aggregateID, Kind: item.KindAggregateFolder}
	for _, tc := range []struct {
		name    string
		useUser bool
		want    []dto.UUID
	}{
		{name: "user stops before aggregate", useUser: true, want: []dto.UUID{folderID}},
		{name: "nil user emits aggregate", useUser: false, want: []dto.UUID{folderID, aggregateID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/Items/" + itemID.Dashed() + "/Ancestors"
			if tc.useUser {
				path += "?userId=" + userID.Dashed()
			}
			reader := &itemExtrasReader{results: []store.ItemQueryResult{
				{Items: []item.Item{source}},
				{Items: []item.Item{folder}},
				{Items: []item.Item{aggregate}},
				{},
			}}
			builder := &itemExtrasIDBuilder{}
			rt := router.New()
			handlers.RegisterItemExtras(rt, reader, builder)
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET Ancestors %s = %d, want 200: %s", tc.name, rec.Code, rec.Body.String())
			}
			var rows []dto.BaseItemDto
			if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
				t.Fatalf("decode %s: %v", tc.name, err)
			}
			got := make([]dto.UUID, len(rows))
			for i := range rows {
				got[i] = rows[i].ID
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ancestors %s = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// itemExtrasIDBuilder maps each input item to a BaseItemDto with the same ID.
type itemExtrasIDBuilder struct {
	calls int
}

func (b *itemExtrasIDBuilder) Build(_ context.Context, values []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	b.calls++
	items := make([]dto.BaseItemDto, len(values))
	for i := range values {
		items[i] = dto.BaseItemDto{ID: values[i].ID}
	}
	return dto.QueryResult[dto.BaseItemDto]{Items: items}, nil
}

func (b *itemExtrasIDBuilder) BuildDetailed(ctx context.Context, values []item.Item, options store.ItemQueryDTOOptions, userID *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	return b.Build(ctx, values, options, userID)
}

// TestAncestorsPolicyBlockedFolderEndsChain verifies that a policy-blocked
// folder ends the chain. The store user-access clause hides the folder, so the
// reader returns an empty result and the walk stops.
func TestAncestorsPolicyBlockedFolderEndsChain(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open ancestors access store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close ancestors access store: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	userID, rootID, blockedRootID, itemID, blockedItemID := itemsHandlerUUID(t, 860), itemsHandlerUUID(t, 861), itemsHandlerUUID(t, 862), itemsHandlerUUID(t, 863), itemsHandlerUUID(t, 864)
	policy := `{"EnableAllFolders":false,"EnabledFolders":["` + rootID.String() + `"],"BlockedTags":["blocked"],"AllowedTags":["allowed"]}`
	user := store.User{ID: userID, Policy: []byte(policy)}
	root := item.Item{ID: rootID, Kind: item.KindFolder, IsFolder: true, Name: itemsHandlerString("visible root"), SortName: itemsHandlerString("visible root")}
	blockedRoot := item.Item{ID: blockedRootID, Kind: item.KindFolder, IsFolder: true, Name: itemsHandlerString("blocked root"), SortName: itemsHandlerString("blocked root"), Values: item.ItemValues{Tags: []string{"blocked"}}}
	source := item.Item{ID: itemID, Kind: item.KindMovie, ParentID: &blockedRootID, Name: itemsHandlerString("source"), SortName: itemsHandlerString("source"), Values: item.ItemValues{Tags: []string{"allowed", "blocked"}}}
	blockedItem := item.Item{ID: blockedItemID, Kind: item.KindMovie, ParentID: &blockedRootID, Name: itemsHandlerString("blocked item"), SortName: itemsHandlerString("blocked item"), Values: item.ItemValues{Tags: []string{"allowed", "blocked"}}}
	for _, value := range []item.Item{root, blockedRoot, source, blockedItem} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("seed %s: %v", value.ID, err)
		}
	}

	reader := &itemsAccessReader{items: items}
	users := itemsAccessUsers{values: map[dto.UUID]store.User{userID: user}}
	authorizer := middleware.NewAuthorizer(itemsAccessResolver{identities: map[string]auth.Identity{userID.String(): {Authenticated: true, UserID: userID}}}, itemsAccessPolicy{}, users)
	rt := router.New(authorizer.Wrap)
	handlers.RegisterItemExtras(rt, reader, itemsAccessBuilder{}, users)
	request := func(path string) *httptest.ResponseRecorder {
		record := httptest.NewRecorder()
		httpRequest := httptest.NewRequest(http.MethodGet, path+"?userId="+userID.Dashed(), nil)
		httpRequest.Header.Set("X-Item-Access-Actor", userID.String())
		rt.ServeHTTP(record, httpRequest)
		return record
	}

	// The blocked source item is invisible to the user, so the chain stops.
	rec := request("/Items/" + blockedItemID.Dashed() + "/Ancestors")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET Ancestors for blocked item = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// TestThemeMediaReturnsAllThreeResultObjects verifies that GetThemeMedia
// returns ThemeVideosResult, ThemeSongsResult, and SoundtrackSongsResult
// objects.
func TestThemeMediaReturnsAllThreeResultObjects(t *testing.T) {
	userID := itemsHandlerUUID(t, 870)
	itemID := itemsHandlerUUID(t, 871)
	source := item.Item{ID: itemID, Kind: item.KindMovie}
	themeSong := item.Item{ID: itemsHandlerUUID(t, 872), Kind: item.KindAudio, OwnerID: &itemID}
	themeVideo := item.Item{ID: itemsHandlerUUID(t, 873), Kind: item.KindVideo, OwnerID: &itemID}
	songType := item.ExtraTypeThemeSong
	videoType := item.ExtraTypeThemeVideo
	themeSong.ExtraType = &songType
	themeVideo.ExtraType = &videoType
	reader := &itemExtrasReader{results: []store.ItemQueryResult{
		{Items: []item.Item{source}},
		{Items: []item.Item{themeSong}},
		{Items: []item.Item{themeVideo}},
	}}
	builder := &itemExtrasIDBuilder{}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, builder)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/ThemeMedia?userId="+userID.Dashed(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ThemeMedia = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var result dto.AllThemeMediaResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode ThemeMedia: %v", err)
	}
	if len(result.ThemeSongsResult.Items) != 1 || result.ThemeSongsResult.Items[0].ID != themeSong.ID {
		t.Fatalf("theme songs = %v, want one song %s", result.ThemeSongsResult.Items, themeSong.ID)
	}
	if len(result.ThemeVideosResult.Items) != 1 || result.ThemeVideosResult.Items[0].ID != themeVideo.ID {
		t.Fatalf("theme videos = %v, want one video %s", result.ThemeVideosResult.Items, themeVideo.ID)
	}
	if len(result.SoundtrackSongsResult.Items) != 0 {
		t.Fatalf("soundtrack songs = %v, want empty", result.SoundtrackSongsResult.Items)
	}
}

func TestThemeMediaBindsAndDropsUnknownSorts(t *testing.T) {
	itemID := itemsHandlerUUID(t, 899)
	for _, tc := range []struct {
		name       string
		query      string
		wantStatus int
		wantSort   []store.ItemSortBy
		wantOrder  []store.SortOrder
	}{
		{name: "case insensitive enums", query: "?sortBy=datEcreated&sortOrder=descENDing", wantStatus: http.StatusOK, wantSort: []store.ItemSortBy{store.ItemSortByDateCreated}, wantOrder: []store.SortOrder{store.SortOrderDescending}},
		{name: "unknown sort", query: "?sortBy=not-a-sort", wantStatus: http.StatusOK, wantSort: []store.ItemSortBy{store.ItemSortBySortName}, wantOrder: []store.SortOrder{store.SortOrderAscending}},
		{name: "unknown order", query: "?sortOrder=sideways", wantStatus: http.StatusOK, wantSort: []store.ItemSortBy{store.ItemSortBySortName}, wantOrder: []store.SortOrder{store.SortOrderAscending}},
		{name: "mixed", query: "?sortBy=not-a-sort,DateCreated&sortOrder=sideways,Descending", wantStatus: http.StatusOK, wantSort: []store.ItemSortBy{store.ItemSortByDateCreated}, wantOrder: []store.SortOrder{store.SortOrderDescending}},
		{name: "empty", query: "?sortBy=&sortOrder=", wantStatus: http.StatusOK, wantSort: []store.ItemSortBy{store.ItemSortBySortName}, wantOrder: []store.SortOrder{store.SortOrderAscending}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &itemExtrasReader{results: []store.ItemQueryResult{{Items: []item.Item{{ID: itemID, Kind: item.KindMovie}}}, {}, {}}}
			rt := router.New()
			handlers.RegisterItemExtras(rt, reader, &itemExtrasIDBuilder{})
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/ThemeMedia"+tc.query, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusOK && (!reflect.DeepEqual(reader.queries[1].SortBy, tc.wantSort) || !reflect.DeepEqual(reader.queries[1].SortOrder, tc.wantOrder)) {
				t.Fatalf("sort = %v/%v, want %v/%v", reader.queries[1].SortBy, reader.queries[1].SortOrder, tc.wantSort, tc.wantOrder)
			}
		})
	}
}

// TestThemeMediaOwnerIdNamesSupplyingItem verifies that the OwnerId field
// names the item that supplied the themes.
func TestThemeMediaOwnerIdNamesSupplyingItem(t *testing.T) {
	itemID := itemsHandlerUUID(t, 874)
	source := item.Item{ID: itemID, Kind: item.KindMovie}
	reader := &itemExtrasReader{results: []store.ItemQueryResult{
		{Items: []item.Item{source}},
		{},
		{},
	}}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/ThemeMedia", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ThemeMedia = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var result dto.AllThemeMediaResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode ThemeMedia: %v", err)
	}
	if result.ThemeSongsResult.OwnerID != itemID {
		t.Fatalf("theme songs owner = %s, want %s", result.ThemeSongsResult.OwnerID, itemID)
	}
	if result.ThemeVideosResult.OwnerID != itemID {
		t.Fatalf("theme videos owner = %s, want %s", result.ThemeVideosResult.OwnerID, itemID)
	}
	if result.ThemeSongsResult.TotalRecordCount != 0 || len(result.ThemeSongsResult.Items) != 0 {
		t.Fatalf("empty theme songs = %#v, want zero count and empty items", result.ThemeSongsResult)
	}
}

// TestThemeMediaInheritFromParentWalksToParent verifies that the
// inheritFromParent parameter walks to the parent when the item has no themes.
func TestThemeMediaInheritFromParentWalksToParent(t *testing.T) {
	itemID, parentID := itemsHandlerUUID(t, 875), itemsHandlerUUID(t, 876)
	source := item.Item{ID: itemID, Kind: item.KindMovie, ParentID: &parentID}
	parent := item.Item{ID: parentID, Kind: item.KindFolder}
	themeSong := item.Item{ID: itemsHandlerUUID(t, 877), Kind: item.KindAudio, OwnerID: &parentID}
	songType := item.ExtraTypeThemeSong
	themeSong.ExtraType = &songType
	reader := &itemExtrasReader{results: []store.ItemQueryResult{
		{Items: []item.Item{source}},
		{},                              // song query on source: empty
		{Items: []item.Item{parent}},    // parent lookup for songs
		{Items: []item.Item{themeSong}}, // song query on parent: found
		{},                              // video query on source: empty
		{Items: []item.Item{parent}},    // parent lookup for videos
		{},                              // video query on parent: empty
	}}
	builder := &itemExtrasIDBuilder{}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, builder)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemID.Dashed()+"/ThemeMedia?inheritFromParent=true", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ThemeMedia = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var result dto.AllThemeMediaResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode ThemeMedia: %v", err)
	}
	if len(result.ThemeSongsResult.Items) != 1 || result.ThemeSongsResult.Items[0].ID != themeSong.ID {
		t.Fatalf("inherited theme songs = %v, want one song from parent", result.ThemeSongsResult.Items)
	}
	if result.ThemeSongsResult.OwnerID != parentID {
		t.Fatalf("inherited theme songs owner = %s, want parent %s", result.ThemeSongsResult.OwnerID, parentID)
	}
	if len(result.ThemeVideosResult.Items) != 0 {
		t.Fatalf("inherited theme videos = %v, want empty", result.ThemeVideosResult.Items)
	}
}

// TestThemeMediaUnknownItemReturns404 verifies that an unknown item returns 404.
func TestThemeMediaUnknownItemReturns404(t *testing.T) {
	reader := &itemExtrasReader{results: []store.ItemQueryResult{{}}}
	rt := router.New()
	handlers.RegisterItemExtras(rt, reader, &itemExtrasBuilder{})
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/"+itemsHandlerUUID(t, 878).Dashed()+"/ThemeMedia", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET ThemeMedia unknown item = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

type itemExtrasReader struct {
	results []store.ItemQueryResult
	errs    []error
	queries []store.ItemQuery
}

func (reader *itemExtrasReader) Query(_ context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	reader.queries = append(reader.queries, query)
	index := len(reader.queries) - 1
	if index < len(reader.errs) && reader.errs[index] != nil {
		return store.ItemQueryResult{}, reader.errs[index]
	}
	if index < len(reader.results) {
		return reader.results[index], nil
	}
	return store.ItemQueryResult{}, nil
}

// itemExtrasStoreReader retains the real SQL-backed query behavior while
// recording the final owner relation selected by the public extras route.
type itemExtrasStoreReader struct {
	items   *store.ItemStore
	queries []store.ItemQuery
}

func (reader *itemExtrasStoreReader) Query(ctx context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	reader.queries = append(reader.queries, query)
	return reader.items.Query(ctx, query)
}

func (reader *itemExtrasStoreReader) LinkedChildren(ctx context.Context, id dto.UUID) ([]item.LinkedChildRef, error) {
	return reader.items.LinkedChildren(ctx, id)
}

type itemExtrasSimilarService struct {
	err error
}

func (service itemExtrasSimilarService) SimilarItems(_ context.Context, _ handlers.SimilarItemsRequest) ([]item.Item, error) {
	return nil, service.err
}

type itemExtrasSimilarRecorder struct {
	requests []handlers.SimilarItemsRequest
}

func (service *itemExtrasSimilarRecorder) SimilarItems(_ context.Context, request handlers.SimilarItemsRequest) ([]item.Item, error) {
	service.requests = append(service.requests, request)
	return nil, nil
}

type itemExtrasBuilder struct {
	items     []dto.BaseItemDto
	err       error
	calls     int
	lastItems []item.Item
}

func (builder *itemExtrasBuilder) Build(_ context.Context, values []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	builder.calls++
	builder.lastItems = append([]item.Item(nil), values...)
	if builder.err != nil {
		return dto.QueryResult[dto.BaseItemDto]{}, builder.err
	}
	return dto.QueryResult[dto.BaseItemDto]{Items: append([]dto.BaseItemDto(nil), builder.items...)}, nil
}

func (builder *itemExtrasBuilder) BuildDetailed(ctx context.Context, values []item.Item, options store.ItemQueryDTOOptions, userID *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	return builder.Build(ctx, values, options, userID)
}
