// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

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

func TestItemNamesAggregateTaxonomyFactsAndProjectPages(t *testing.T) {
	userID := itemNamesUUID(t, 1)
	personID := itemNamesUUID(t, 2)
	first, second := "Drama", "Comedy"
	alpha, zeta, ada := "Alpha", "Zeta", "Ada"
	reader := &itemNamesReader{
		result: store.ItemQueryResult{Items: []item.Item{
			{ID: itemNamesUUID(t, 10), Kind: item.KindMovie, Values: item.ItemValues{Genres: []string{first, second}, Studios: []string{zeta, alpha}}, People: []item.PersonCredit{{PersonID: personID, Name: ada, Type: item.PersonKindDirector}}},
			{ID: itemNamesUUID(t, 11), Kind: item.KindSeries, Values: item.ItemValues{Genres: []string{"drama"}, Studios: []string{alpha}}, People: []item.PersonCredit{{PersonID: personID, Name: ada, Type: item.PersonKindActor}}},
		}},
		entities: map[item.Kind][]item.Item{
			item.KindGenre:  {{ID: itemNamesUUID(t, 101), Kind: item.KindGenre, Name: &first}, {ID: itemNamesUUID(t, 102), Kind: item.KindGenre, Name: &second}},
			item.KindStudio: {{ID: itemNamesUUID(t, 103), Kind: item.KindStudio, Name: &alpha}, {ID: itemNamesUUID(t, 104), Kind: item.KindStudio, Name: &zeta}},
			item.KindPerson: {{ID: personID, Kind: item.KindPerson, Name: &ada}},
		},
	}
	builder := &itemNamesBuilder{}
	rt := router.New()
	handlers.RegisterItemNames(rt, reader, builder, clock.NewReal())

	for _, tc := range []struct {
		path      string
		wantType  string
		wantNames []string
	}{
		{path: "/Genres?userId=" + userID.Dashed() + "&limit=1", wantType: "Genre", wantNames: []string{"Comedy"}},
		{path: "/Studios?userId=" + userID.Dashed(), wantType: "Studio", wantNames: []string{"Alpha", "Zeta"}},
		{path: "/Persons?userId=" + userID.Dashed() + "&personTypes=Actor", wantType: "Person", wantNames: []string{"Ada"}},
	} {
		t.Run(tc.wantType, func(t *testing.T) {
			reader.reset()
			builder.reset()
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200: %s", tc.path, rec.Code, rec.Body.String())
			}
			var got dto.QueryResult[dto.BaseItemDto]
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %s: %v", tc.path, err)
			}
			names := make([]string, len(got.Items))
			for i := range got.Items {
				names[i] = got.Items[i].Name
				if got.Items[i].Type != dto.Kind(tc.wantType) {
					t.Fatalf("item %d Type = %q, want %q", i, got.Items[i].Type, tc.wantType)
				}
			}
			if !reflect.DeepEqual(names, tc.wantNames) {
				t.Fatalf("GET %s names = %#v, want %#v", tc.path, names, tc.wantNames)
			}
			if got.TotalRecordCount != 2 && tc.wantType != "Person" {
				t.Fatalf("GET %s total = %d, want distinct taxonomy count 2", tc.path, got.TotalRecordCount)
			}
			if got.TotalRecordCount != 1 && tc.wantType == "Person" {
				t.Fatalf("GET %s total = %d, want 1", tc.path, got.TotalRecordCount)
			}
			// Source facts, entity names, and the full items of the page.
			if reader.calls != 3 || builder.calls != 1 {
				t.Fatalf("GET %s reader/build calls = %d/%d, want 3/1", tc.path, reader.calls, builder.calls)
			}
			if reader.queries[0].UserID == nil || *reader.queries[0].UserID != userID || reader.queries[0].EnableTotalRecordCount {
				t.Fatalf("GET %s source query = %#v, want user-scoped unpaged aggregation", tc.path, reader.queries[0])
			}
		})
	}
}

func TestItemNamesUseFolderAncestorAndNonFolderItemScopes(t *testing.T) {
	folderID, movieID := itemNamesUUID(t, 30), itemNamesUUID(t, 31)
	media := item.Item{ID: itemNamesUUID(t, 32), Kind: item.KindMovie, Values: item.ItemValues{Genres: []string{"Drama"}}}
	for _, tc := range []struct {
		name         string
		parent       item.Item
		wantAncestor bool
	}{
		{name: "folder", parent: item.Item{ID: folderID, Kind: item.KindFolder, IsFolder: true}, wantAncestor: true},
		{name: "non-folder", parent: item.Item{ID: movieID, Kind: item.KindMovie}, wantAncestor: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.parent.ID
			reader := &itemNamesReader{results: []store.ItemQueryResult{{Items: []item.Item{tc.parent}}, {Items: []item.Item{media}}}, entities: map[item.Kind][]item.Item{item.KindGenre: {{ID: itemNamesUUID(t, 132), Kind: item.KindGenre, Name: itemNamesString("Drama")}}}}
			builder := &itemNamesBuilder{}
			rt := router.New()
			handlers.RegisterItemNames(rt, reader, builder, clock.NewReal())
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Genres?parentId="+id.Dashed(), nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET Genres status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if reader.calls != 4 {
				t.Fatalf("parent, source, entity name, and page queries = %d, want 4", reader.calls)
			}
			aggregate := reader.queries[1]
			if tc.wantAncestor {
				if !reflect.DeepEqual(aggregate.AncestorIDs, []dto.UUID{id}) || aggregate.ParentID != nil || len(aggregate.ItemIDs) != 0 {
					t.Fatalf("folder taxonomy query = %#v, want ancestor scope", aggregate)
				}
			} else if !reflect.DeepEqual(aggregate.ItemIDs, []dto.UUID{id}) || aggregate.ParentID != nil || len(aggregate.AncestorIDs) != 0 {
				t.Fatalf("non-folder taxonomy query = %#v, want item scope", aggregate)
			}
		})
	}
}

// TestItemNamesDropsUnknownFields pins upstream
// CommaDelimitedCollectionModelBinder for the ItemFields[] fields parameter.
func TestItemNamesDropsUnknownFields(t *testing.T) {
	drama := "Drama"
	reader := &itemNamesReader{
		result:   store.ItemQueryResult{Items: []item.Item{{ID: itemNamesUUID(t, 300), Kind: item.KindMovie, Values: item.ItemValues{Genres: []string{drama}}}}},
		entities: map[item.Kind][]item.Item{item.KindGenre: {{ID: itemNamesUUID(t, 301), Kind: item.KindGenre, Name: &drama}}},
	}
	builder := &itemNamesBuilder{}
	rt := router.New()
	handlers.RegisterItemNames(rt, reader, builder, clock.NewReal())
	for _, tc := range []struct {
		name, raw string
		want      []string
	}{
		{name: "known", raw: "overview", want: []string{"Overview"}},
		{name: "unknown", raw: "AlternateMediaSources", want: []string{}},
		{name: "mixed", raw: "AlternateMediaSources,Overview", want: []string{"Overview"}},
		{name: "empty", raw: "", want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader.reset()
			builder.reset()
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Genres?fields="+tc.raw, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /Genres?fields=%s status = %d, want 200: %s", tc.raw, rec.Code, rec.Body.String())
			}
			if len(builder.options) == 0 || fmt.Sprint(builder.options[0].Fields) != fmt.Sprint(tc.want) {
				t.Fatalf("fields=%s projection options = %#v, want Fields %v", tc.raw, builder.options, tc.want)
			}
		})
	}
}

func TestItemNamesGenresHonorExplicitTotalRecordCount(t *testing.T) {
	comedyID, dramaID := itemNamesUUID(t, 201), itemNamesUUID(t, 202)
	comedy, drama := "Comedy", "Drama"
	reader := &itemNamesReader{
		result: store.ItemQueryResult{Items: []item.Item{{
			ID:     itemNamesUUID(t, 200),
			Kind:   item.KindMovie,
			Values: item.ItemValues{Genres: []string{drama, comedy}},
		}}},
		entities: map[item.Kind][]item.Item{
			item.KindGenre: {
				{ID: comedyID, Kind: item.KindGenre, Name: &comedy},
				{ID: dramaID, Kind: item.KindGenre, Name: &drama},
			},
		},
	}
	builder := &itemNamesBuilder{}
	rt := router.New()
	handlers.RegisterItemNames(rt, reader, builder, clock.NewReal())

	for _, tc := range []struct {
		name      string
		path      string
		wantTotal int
	}{
		{name: "disabled", path: "/Genres?Limit=1&EnableTotalRecordCount=false", wantTotal: 0},
		{name: "enabled", path: "/Genres?Limit=1&EnableTotalRecordCount=true", wantTotal: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader.reset()
			builder.reset()
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200: %s", tc.path, rec.Code, rec.Body.String())
			}
			var got dto.QueryResult[dto.BaseItemDto]
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %s: %v", tc.path, err)
			}
			wantItems := []dto.BaseItemDto{{ID: comedyID, Name: comedy, Type: dto.Kind("Genre")}}
			if !reflect.DeepEqual(got.Items, wantItems) || got.StartIndex != 0 || got.TotalRecordCount != tc.wantTotal {
				t.Fatalf("GET %s result = %#v, want items %#v, StartIndex 0, TotalRecordCount %d", tc.path, got, wantItems, tc.wantTotal)
			}
		})
	}
}

func TestItemNamesGenresClassifyMusicCollectionGenres(t *testing.T) {
	for _, tc := range []struct {
		name           string
		collectionType item.CollectionType
	}{
		{name: "music", collectionType: item.CollectionTypeMusic},
		{name: "music videos", collectionType: item.CollectionTypeMusicVideos},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parentID, genreID := itemNamesUUID(t, 210), itemNamesUUID(t, 211)
			genre := "Synthwave"
			collectionType := tc.collectionType
			parent := item.Item{
				ID:       parentID,
				Kind:     item.KindCollectionFolder,
				IsFolder: true,
				Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{
					CollectionType: &collectionType,
				}},
			}
			reader := &itemNamesReader{
				results: []store.ItemQueryResult{
					{Items: []item.Item{parent}},
					{Items: []item.Item{{ID: itemNamesUUID(t, 212), Kind: item.KindAudio, Values: item.ItemValues{Genres: []string{genre}}}}},
				},
				entities: map[item.Kind][]item.Item{
					item.KindMusicGenre: {{ID: genreID, Kind: item.KindMusicGenre, Name: &genre}},
				},
			}
			rt := router.New()
			handlers.RegisterItemNames(rt, reader, &itemNamesBuilder{}, clock.NewReal())
			rec := httptest.NewRecorder()
			path := "/Genres?ParentId=" + parentID.Dashed()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200: %s", path, rec.Code, rec.Body.String())
			}
			var got dto.QueryResult[dto.BaseItemDto]
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			wantItems := []dto.BaseItemDto{{ID: genreID, Name: genre, Type: dto.Kind("MusicGenre")}}
			if !reflect.DeepEqual(got.Items, wantItems) {
				t.Fatalf("GET %s items = %#v, want %#v", path, got.Items, wantItems)
			}
		})
	}
}

func TestItemNamesPersonsTreatZeroLimitAsUnlimitedAfterStartIndex(t *testing.T) {
	alphaID, deltaID, zetaID := itemNamesUUID(t, 220), itemNamesUUID(t, 221), itemNamesUUID(t, 222)
	alpha, delta, zeta := "Alpha", "Delta", "Zeta"
	reader := &itemNamesReader{
		result: store.ItemQueryResult{Items: []item.Item{{
			ID:   itemNamesUUID(t, 223),
			Kind: item.KindMovie,
			People: []item.PersonCredit{
				{PersonID: zetaID, Name: zeta, Type: item.PersonKindActor},
				{PersonID: alphaID, Name: alpha, Type: item.PersonKindActor},
				{PersonID: deltaID, Name: delta, Type: item.PersonKindActor},
			},
		}}},
		entities: map[item.Kind][]item.Item{
			item.KindPerson: {
				{ID: zetaID, Kind: item.KindPerson, Name: &zeta},
				{ID: deltaID, Kind: item.KindPerson, Name: &delta},
				{ID: alphaID, Kind: item.KindPerson, Name: &alpha},
			},
		},
	}
	rt := router.New()
	handlers.RegisterItemNames(rt, reader, &itemNamesBuilder{}, clock.NewReal())
	path := "/Persons?StartIndex=1&Limit=0&EnableTotalRecordCount=true"
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: %s", path, rec.Code, rec.Body.String())
	}
	var got dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	wantItems := []dto.BaseItemDto{
		{ID: deltaID, Name: delta, Type: dto.Kind("Person")},
		{ID: zetaID, Name: zeta, Type: dto.Kind("Person")},
	}
	if !reflect.DeepEqual(got.Items, wantItems) || got.StartIndex != 1 || got.TotalRecordCount != 3 {
		t.Fatalf("GET %s result = %#v, want items %#v, StartIndex 1, TotalRecordCount 3", path, got, wantItems)
	}
}

func TestItemNamesLegacyFiltersAggregateAndKeepArraysNonNil(t *testing.T) {
	folderID, movieID := itemNamesUUID(t, 40), itemNamesUUID(t, 41)
	folder := item.Item{ID: folderID, Kind: item.KindFolder, IsFolder: true}
	yearA, yearB := 1999, 2001
	ratingA, ratingB := "PG", "R"
	media := []item.Item{
		{ID: itemNamesUUID(t, 42), Kind: item.KindMovie, Values: item.ItemValues{Genres: []string{"Drama", "drama"}, Tags: []string{"B", "a"}}, ProductionYear: &yearB, OfficialRating: &ratingB},
		{ID: itemNamesUUID(t, 43), Kind: item.KindSeries, Values: item.ItemValues{Genres: []string{"Comedy"}, Tags: []string{"A"}}, ProductionYear: &yearA, OfficialRating: &ratingA},
	}
	reader := &itemNamesReader{results: []store.ItemQueryResult{{Items: []item.Item{folder}}, {Items: media}}}
	rt := router.New()
	handlers.RegisterItemNames(rt, reader, &itemNamesBuilder{}, clock.NewReal())
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/Filters?parentId="+folderID.Dashed()+"&includeItemTypes=Movie,Series", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET filters status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Genres          []string `json:"Genres"`
		Tags            []string `json:"Tags"`
		OfficialRatings []string `json:"OfficialRatings"`
		Years           []int    `json:"Years"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode filters: %v", err)
	}
	if !reflect.DeepEqual(got.Genres, []string{"Comedy", "Drama"}) || !reflect.DeepEqual(got.Tags, []string{"A", "B"}) || !reflect.DeepEqual(got.OfficialRatings, []string{"PG", "R"}) || !reflect.DeepEqual(got.Years, []int{1999, 2001}) {
		t.Fatalf("legacy filters = %#v, want sorted distinct modeled facts", got)
	}
	if reader.calls != 2 || !reader.queries[1].Recursive || !reflect.DeepEqual(reader.queries[1].AncestorIDs, []dto.UUID{folderID}) || reader.queries[1].EnableTotalRecordCount {
		t.Fatalf("filter query = %#v, want recursive uncounted folder scope", reader.queries[1])
	}

	nonFolder := &itemNamesReader{result: store.ItemQueryResult{Items: []item.Item{{ID: movieID, Kind: item.KindMovie}}}}
	rt = router.New()
	handlers.RegisterItemNames(rt, nonFolder, &itemNamesBuilder{}, clock.NewReal())
	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/Filters?parentId="+movieID.Dashed(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET nonfolder filters status = %d, want 200", rec.Code)
	}
	var empty map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatalf("decode empty filters: %v", err)
	}
	for _, key := range []string{"Genres", "Tags", "OfficialRatings", "Years"} {
		if string(empty[key]) != "[]" {
			t.Fatalf("nonfolder filters %s = %s, want []", key, empty[key])
		}
	}
	if nonFolder.calls != 1 {
		t.Fatalf("nonfolder filters queries = %d, want only parent lookup", nonFolder.calls)
	}
}

// TestItemNamesRestrictedUserPoliciesDoNotLeakTaxonomyOrFilterValues exercises
// the real target-user handoff from both taxonomy and legacy filters to the
// store. A user limited to one library and content policy must not infer names
// from a different folder, blocked tags, too-high ratings, or blocked unrated
// items.
func TestItemNamesRestrictedUserPoliciesDoNotLeakTaxonomyOrFilterValues(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open item names access store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close item names access store: %v", err)
		}
	})

	items := store.NewItemStore(stores)
	userID, rootID, otherRootID := itemNamesUUID(t, 500), itemNamesUUID(t, 501), itemNamesUUID(t, 502)
	policy := `{"EnableAllFolders":false,"EnabledFolders":["` + rootID.String() + `"],"BlockedTags":["blocked"],"AllowedTags":["allowed"],"MaxParentalRating":10,"MaxParentalSubRating":0,"BlockUnratedItems":[0]}`
	user := store.User{ID: userID, Policy: []byte(policy)}
	visibleGenre, outsideGenre, blockedGenre, highGenre, unratedGenre := "Visible Genre", "Outside Genre", "Blocked Genre", "High Genre", "Unrated Genre"
	visibleRating, highRating := "PG", "R"
	visibleYear, hiddenYear := 2001, 2002
	unrated := item.UnratedItemMovie
	root := item.Item{ID: rootID, Kind: item.KindFolder, IsFolder: true, Name: itemNamesString("visible root"), SortName: itemNamesString("visible root")}
	otherRoot := item.Item{ID: otherRootID, Kind: item.KindFolder, IsFolder: true, Name: itemNamesString("hidden root"), SortName: itemNamesString("hidden root")}
	visible := item.Item{ID: itemNamesUUID(t, 503), Kind: item.KindMovie, ParentID: &rootID, Name: itemNamesString("visible"), SortName: itemNamesString("visible"), Values: item.ItemValues{Genres: []string{visibleGenre}, Tags: []string{"allowed", "visible-tag"}}, OfficialRating: &visibleRating, ProductionYear: &visibleYear, InheritedParentalRatingValue: itemNamesInt(10), InheritedParentalRatingSubValue: itemNamesInt(0)}
	outside := item.Item{ID: itemNamesUUID(t, 504), Kind: item.KindMovie, ParentID: &otherRootID, Name: itemNamesString("outside"), SortName: itemNamesString("outside"), Values: item.ItemValues{Genres: []string{outsideGenre}, Tags: []string{"allowed", "outside-tag"}}, ProductionYear: &hiddenYear, InheritedParentalRatingValue: itemNamesInt(5)}
	blocked := item.Item{ID: itemNamesUUID(t, 505), Kind: item.KindMovie, ParentID: &rootID, Name: itemNamesString("blocked"), SortName: itemNamesString("blocked"), Values: item.ItemValues{Genres: []string{blockedGenre}, Tags: []string{"allowed", "blocked"}}, ProductionYear: &hiddenYear, InheritedParentalRatingValue: itemNamesInt(5)}
	high := item.Item{ID: itemNamesUUID(t, 506), Kind: item.KindMovie, ParentID: &rootID, Name: itemNamesString("high"), SortName: itemNamesString("high"), Values: item.ItemValues{Genres: []string{highGenre}, Tags: []string{"allowed", "high-tag"}}, OfficialRating: &highRating, ProductionYear: &hiddenYear, InheritedParentalRatingValue: itemNamesInt(11)}
	unratedItem := item.Item{ID: itemNamesUUID(t, 507), Kind: item.KindMovie, ParentID: &rootID, Name: itemNamesString("unrated"), SortName: itemNamesString("unrated"), Values: item.ItemValues{Genres: []string{unratedGenre}, Tags: []string{"allowed", "unrated-tag"}}, ProductionYear: &hiddenYear, UnratedType: &unrated}
	for _, value := range []item.Item{
		root, otherRoot, visible, outside, blocked, high, unratedItem,
		{ID: itemNamesUUID(t, 508), Kind: item.KindGenre, ParentID: &rootID, Name: &visibleGenre, SortName: &visibleGenre},
		{ID: itemNamesUUID(t, 509), Kind: item.KindGenre, ParentID: &rootID, Name: &outsideGenre, SortName: &outsideGenre},
		{ID: itemNamesUUID(t, 510), Kind: item.KindGenre, ParentID: &rootID, Name: &blockedGenre, SortName: &blockedGenre},
		{ID: itemNamesUUID(t, 511), Kind: item.KindGenre, ParentID: &rootID, Name: &highGenre, SortName: &highGenre},
		{ID: itemNamesUUID(t, 512), Kind: item.KindGenre, ParentID: &rootID, Name: &unratedGenre, SortName: &unratedGenre},
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("seed %s: %v", value.ID, err)
		}
	}

	reader := &itemsAccessReader{items: items}
	users := itemsAccessUsers{values: map[dto.UUID]store.User{userID: user}}
	authorizer := middleware.NewAuthorizer(itemsAccessResolver{identities: map[string]auth.Identity{userID.String(): {Authenticated: true, UserID: userID}}}, itemsAccessPolicy{}, users)
	rt := router.New(authorizer.Wrap)
	handlers.RegisterItemNames(rt, reader, &itemNamesBuilder{}, clock.NewReal(), users)
	request := func(path string) *httptest.ResponseRecorder {
		record := httptest.NewRecorder()
		httpRequest := httptest.NewRequest(http.MethodGet, path+"?userId="+userID.Dashed()+"&parentId="+rootID.Dashed(), nil)
		httpRequest.Header.Set("X-Item-Access-Actor", userID.String())
		rt.ServeHTTP(record, httpRequest)
		return record
	}

	genres := request("/Genres")
	if genres.Code != http.StatusOK {
		t.Fatalf("GET Genres = %d, want 200: %s", genres.Code, genres.Body.String())
	}
	var genrePage dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(genres.Body.Bytes(), &genrePage); err != nil {
		t.Fatalf("decode Genres: %v", err)
	}
	if got, want := genrePage.Items, []dto.BaseItemDto{{ID: itemNamesUUID(t, 508), Name: visibleGenre, Type: dto.Kind("Genre")}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restricted genres = %#v, want only visible taxonomy %#v", got, want)
	}

	filters := request("/Items/Filters")
	if filters.Code != http.StatusOK {
		t.Fatalf("GET Items/Filters = %d, want 200: %s", filters.Code, filters.Body.String())
	}
	var values struct {
		Genres          []string `json:"Genres"`
		Tags            []string `json:"Tags"`
		OfficialRatings []string `json:"OfficialRatings"`
		Years           []int    `json:"Years"`
	}
	if err := json.Unmarshal(filters.Body.Bytes(), &values); err != nil {
		t.Fatalf("decode Items/Filters: %v", err)
	}
	if got, want := values.Genres, []string{visibleGenre}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restricted filter genres = %v, want %v", got, want)
	}
	if got, want := values.Tags, []string{"allowed", "visible-tag"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restricted filter tags = %v, want %v", got, want)
	}
	if got, want := values.OfficialRatings, []string{visibleRating}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restricted filter ratings = %v, want %v", got, want)
	}
	if got, want := values.Years, []int{visibleYear}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restricted filter years = %v, want %v", got, want)
	}
	for _, query := range reader.queries {
		if query.AncestorIDs != nil && !reflect.DeepEqual(query.User, &user) {
			t.Fatalf("restricted aggregate query user = %#v, want persisted target %#v", query.User, user)
		}
	}
}

func TestItemNamesMapInvalidUserDateAndDependenciesToHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		reader  *itemNamesReader
		builder *itemNamesBuilder
		status  int
	}{
		{name: "invalid user", path: "/Genres?userId=bad", reader: &itemNamesReader{}, builder: &itemNamesBuilder{}, status: http.StatusBadRequest},
		{name: "invalid date", path: "/Studios?minPremiereDate=not-a-date", reader: &itemNamesReader{}, builder: &itemNamesBuilder{}, status: http.StatusBadRequest},
		{name: "reader", path: "/Persons", reader: &itemNamesReader{err: errors.New("unavailable")}, builder: &itemNamesBuilder{}, status: http.StatusInternalServerError},
		{name: "builder", path: "/Genres", reader: &itemNamesReader{}, builder: &itemNamesBuilder{err: errors.New("unavailable")}, status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := router.New()
			handlers.RegisterItemNames(rt, tc.reader, tc.builder, clock.NewReal())
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.status {
				t.Fatalf("GET %s status = %d, want %d: %s", tc.path, rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

// TestArtistsRouteRegistersExactOperation verifies that RegisterItemNames
// registers the /Artists route with the correct OperationID.
func TestArtistsRouteRegistersExactOperation(t *testing.T) {
	rt := router.New()
	handlers.RegisterItemNames(rt, nil, nil, clock.NewReal())
	want := router.Route{Method: http.MethodGet, Pattern: "/Artists", OperationID: "GetArtists", Policy: string(auth.PolicyDefaultAuthorization)}
	got, found := routeByOperation(rt.Routes(), want.OperationID)
	if !found || got != want {
		t.Fatalf("route %q = %#v, found %t, want %#v", want.OperationID, got, found, want)
	}
}

// TestArtistsAggregatesArtistsAndAlbumArtistsValues pins GetArtists behavior:
// the handler aggregates both Artists and AlbumArtists values into persisted
// MusicArtist rows with paging and TotalRecordCount.
func TestArtistsAggregatesArtistsAndAlbumArtistsValues(t *testing.T) {
	userID := itemNamesUUID(t, 600)
	artistA, artistB := "Alpha", "Beta"
	reader := &itemNamesReader{
		result: store.ItemQueryResult{Items: []item.Item{
			{ID: itemNamesUUID(t, 601), Kind: item.KindMusicAlbum, Values: item.ItemValues{Artists: []string{artistA}, AlbumArtists: []string{artistB}}},
			{ID: itemNamesUUID(t, 602), Kind: item.KindAudio, Values: item.ItemValues{Artists: []string{artistA}, AlbumArtists: []string{artistA}}},
		}},
		entities: map[item.Kind][]item.Item{
			item.KindMusicArtist: {
				{ID: itemNamesUUID(t, 610), Kind: item.KindMusicArtist, Name: &artistA, SortName: &artistA},
				{ID: itemNamesUUID(t, 611), Kind: item.KindMusicArtist, Name: &artistB, SortName: &artistB},
			},
		},
	}
	builder := &itemNamesBuilder{}
	rt := router.New()
	handlers.RegisterItemNames(rt, reader, builder, clock.NewReal())
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Artists?userId="+userID.Dashed(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /Artists = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var page dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode /Artists: %v", err)
	}
	names := make([]string, len(page.Items))
	for i := range page.Items {
		names[i] = page.Items[i].Name
	}
	if !reflect.DeepEqual(names, []string{artistA, artistB}) {
		t.Fatalf("artist names = %#v, want Alpha and Beta aggregated from both Artists and AlbumArtists", names)
	}
	if page.TotalRecordCount != 2 {
		t.Fatalf("total = %d, want 2 distinct artists", page.TotalRecordCount)
	}
	if reader.calls != 3 || builder.calls != 1 {
		t.Fatalf("reader/build calls = %d/%d, want 3/1", reader.calls, builder.calls)
	}
}

// TestArtistsAccessPolicyFiltersSources verifies that a restricted user does
// not see artists from blocked source items. The store access clause filters
// the source query; only visible source items contribute artist names.
func TestArtistsAccessPolicyFiltersSources(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open artists access store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close artists access store: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	userID, rootID, otherRootID := itemNamesUUID(t, 700), itemNamesUUID(t, 701), itemNamesUUID(t, 702)
	policy := `{"EnableAllFolders":false,"EnabledFolders":["` + rootID.String() + `"],"BlockedTags":["blocked"],"AllowedTags":["allowed"]}`
	user := store.User{ID: userID, Policy: []byte(policy)}
	visibleArtist, blockedArtist := "Visible Artist", "Blocked Artist"
	root := item.Item{ID: rootID, Kind: item.KindFolder, IsFolder: true, Name: itemNamesString("visible root"), SortName: itemNamesString("visible root"), Values: item.ItemValues{Tags: []string{"allowed"}}}
	otherRoot := item.Item{ID: otherRootID, Kind: item.KindFolder, IsFolder: true, Name: itemNamesString("blocked root"), SortName: itemNamesString("blocked root"), Values: item.ItemValues{Tags: []string{"allowed", "blocked"}}}
	visibleAlbum := item.Item{ID: itemNamesUUID(t, 703), Kind: item.KindMusicAlbum, ParentID: &rootID, Name: itemNamesString("visible album"), SortName: itemNamesString("visible album"), Values: item.ItemValues{Artists: []string{visibleArtist}, Tags: []string{"allowed"}}}
	blockedAlbum := item.Item{ID: itemNamesUUID(t, 704), Kind: item.KindMusicAlbum, ParentID: &otherRootID, Name: itemNamesString("blocked album"), SortName: itemNamesString("blocked album"), Values: item.ItemValues{Artists: []string{blockedArtist}, Tags: []string{"allowed", "blocked"}}}
	visibleArtistItem := item.Item{ID: itemNamesUUID(t, 705), Kind: item.KindMusicArtist, Name: &visibleArtist, SortName: &visibleArtist}
	blockedArtistItem := item.Item{ID: itemNamesUUID(t, 706), Kind: item.KindMusicArtist, Name: &blockedArtist, SortName: &blockedArtist}
	for _, value := range []item.Item{root, otherRoot, visibleAlbum, blockedAlbum, visibleArtistItem, blockedArtistItem} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("seed %s: %v", value.ID, err)
		}
	}

	reader := &itemsAccessReader{items: items}
	users := itemsAccessUsers{values: map[dto.UUID]store.User{userID: user}}
	authorizer := middleware.NewAuthorizer(itemsAccessResolver{identities: map[string]auth.Identity{userID.String(): {Authenticated: true, UserID: userID}}}, itemsAccessPolicy{}, users)
	rt := router.New(authorizer.Wrap)
	handlers.RegisterItemNames(rt, reader, &itemNamesBuilder{}, clock.NewReal(), users)
	request := func() *httptest.ResponseRecorder {
		record := httptest.NewRecorder()
		httpRequest := httptest.NewRequest(http.MethodGet, "/Artists?userId="+userID.Dashed()+"&parentId="+rootID.Dashed(), nil)
		httpRequest.Header.Set("X-Item-Access-Actor", userID.String())
		rt.ServeHTTP(record, httpRequest)
		return record
	}

	rec := request()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /Artists = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var page dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode /Artists: %v", err)
	}
	names := make([]string, len(page.Items))
	for i := range page.Items {
		names[i] = page.Items[i].Name
	}
	if len(names) != 1 || names[0] != visibleArtist {
		t.Fatalf("restricted artists = %#v, want only Visible Artist %#v", names, visibleArtist)
	}
}

type itemNamesReader struct {
	result   store.ItemQueryResult
	results  []store.ItemQueryResult
	entities map[item.Kind][]item.Item
	err      error
	calls    int
	last     store.ItemQuery
	queries  []store.ItemQuery
}

func (r *itemNamesReader) Query(_ context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	r.calls++
	r.last = query
	r.queries = append(r.queries, query)
	if r.err != nil {
		return store.ItemQueryResult{}, r.err
	}
	if len(query.IncludeItemTypes) == 1 {
		if values, found := r.entities[query.IncludeItemTypes[0]]; found {
			return store.ItemQueryResult{Items: values}, nil
		}
	}
	if len(r.results) >= r.calls {
		return r.results[r.calls-1], nil
	}
	return r.result, nil
}

func (r *itemNamesReader) reset() {
	r.calls = 0
	r.last = store.ItemQuery{}
	r.queries = nil
}

type itemNamesBuilder struct {
	err     error
	calls   int
	options []store.ItemQueryDTOOptions
}

func (b *itemNamesBuilder) Build(_ context.Context, values []item.Item, options store.ItemQueryDTOOptions, _ *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	b.calls++
	b.options = append(b.options, options)
	if b.err != nil {
		return dto.QueryResult[dto.BaseItemDto]{}, b.err
	}
	result := dto.NewQueryResult[dto.BaseItemDto]()
	for _, value := range values {
		name := ""
		if value.Name != nil {
			name = *value.Name
		}
		result.Items = append(result.Items, dto.BaseItemDto{ID: value.ID, Name: name, Type: dto.Kind(value.Kind.String())})
	}
	return result, nil
}

func (b *itemNamesBuilder) BuildDetailed(ctx context.Context, values []item.Item, options store.ItemQueryDTOOptions, userID *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	return b.Build(ctx, values, options, userID)
}

func (b *itemNamesBuilder) reset() { b.calls, b.options = 0, nil }

func itemNamesUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse fixture UUID %d: %v", value, err)
	}
	return id
}

func itemNamesString(value string) *string { return &value }
func itemNamesInt(value int) *int          { return &value }
