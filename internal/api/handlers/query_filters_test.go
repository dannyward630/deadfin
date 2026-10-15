// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
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
	"github.com/deadfin-org/deadfin/internal/localization"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

func (reader *itemsAccessReader) QueryStreamLanguages(ctx context.Context, query store.ItemQuery, streamType string) ([]string, error) {
	reader.queries = append(reader.queries, query)
	return reader.items.QueryStreamLanguages(ctx, query, streamType)
}

type queryFiltersFixture struct {
	router                       *router.Router
	reader                       *itemsAccessReader
	admin, restricted            dto.UUID
	shows, movies, series        dto.UUID
	drama, anime, action, unused dto.UUID
}

// newQueryFiltersFixture stores a shows library with one series and two
// episodes and a movies library with one movie, on a real SQLite store. The
// restricted user sees only the movies library.
func newQueryFiltersFixture(t *testing.T) queryFiltersFixture {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)
	id := func(n int) dto.UUID { return itemNamesUUID(t, 700+n) }
	name := itemNamesString
	f := queryFiltersFixture{admin: id(1), restricted: id(2), shows: id(11), movies: id(12), series: id(20), drama: id(40), anime: id(41), action: id(42), unused: id(43)}
	root, season, first, second, movie := id(10), id(21), id(22), id(23), id(30)
	one, two := 1, 2
	values := []item.Item{
		{ID: root, Kind: item.KindAggregateFolder, IsFolder: true, Name: name("root")},
		{ID: f.shows, Kind: item.KindCollectionFolder, IsFolder: true, ParentID: &root, Name: name("Shows")},
		{ID: f.movies, Kind: item.KindCollectionFolder, IsFolder: true, ParentID: &root, Name: name("Movies")},
		{ID: f.series, Kind: item.KindSeries, IsFolder: true, ParentID: &f.shows, Name: name("Show"), Values: item.ItemValues{Genres: []string{"Drama", "Anime"}}},
		{ID: season, Kind: item.KindSeason, IsFolder: true, ParentID: &f.series, Name: name("Season 1"), IndexNumber: &one},
		{ID: first, Kind: item.KindEpisode, ParentID: &season, Name: name("Episode 1"), IndexNumber: &one, ParentIndexNumber: &one},
		{ID: second, Kind: item.KindEpisode, ParentID: &season, Name: name("Episode 2"), IndexNumber: &two, ParentIndexNumber: &one},
		{ID: movie, Kind: item.KindMovie, ParentID: &f.movies, Name: name("Film"), Values: item.ItemValues{Genres: []string{"Action"}}},
		{ID: f.drama, Kind: item.KindGenre, Name: name("Drama")},
		{ID: f.anime, Kind: item.KindGenre, Name: name("Anime")},
		{ID: f.action, Kind: item.KindGenre, Name: name("Action")},
		{ID: f.unused, Kind: item.KindGenre, Name: name("Western")},
	}
	for _, value := range values {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}
	language := itemNamesString
	for itemID, streams := range map[dto.UUID][]playback.MediaStream{
		first: {
			{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
			{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", Language: language("jpn")},
			{Index: 2, Type: playback.StreamTypeAudio, Codec: "aac", Language: language("eng")},
			{Index: 3, Type: playback.StreamTypeSubtitle, Codec: "ass", Language: language("eng")},
		},
		second: {
			{Index: 0, Type: playback.StreamTypeAudio, Codec: "aac", Language: language("jpn")},
			{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt"},
		},
		movie: {
			{Index: 0, Type: playback.StreamTypeAudio, Codec: "ac3", Language: language("fre")},
			{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt", Language: language("qaa-custom")},
		},
	} {
		if err := items.ReplaceStreams(t.Context(), itemID, streams); err != nil {
			t.Fatalf("replace streams of %s: %v", itemID, err)
		}
	}
	if err := items.RepairDerived(t.Context()); err != nil {
		t.Fatalf("repair derived: %v", err)
	}

	languages, err := localization.NewService()
	if err != nil {
		t.Fatalf("localization: %v", err)
	}
	users := itemsAccessUsers{values: map[dto.UUID]store.User{
		f.admin:      {ID: f.admin, Policy: []byte(`{"IsAdministrator":true,"EnableAllFolders":true}`)},
		f.restricted: {ID: f.restricted, Policy: []byte(`{"EnableAllFolders":false,"EnabledFolders":["` + f.movies.String() + `"]}`)},
	}}
	authorizer := middleware.NewAuthorizer(itemsAccessResolver{identities: map[string]auth.Identity{
		f.admin.String():      {Authenticated: true, UserID: f.admin},
		f.restricted.String(): {Authenticated: true, UserID: f.restricted},
	}}, itemsAccessPolicy{}, users)
	f.reader = &itemsAccessReader{items: items}
	f.router = router.New(authorizer.Wrap)
	handlers.RegisterItemNames(f.router, f.reader, &itemNamesBuilder{}, clock.NewReal(), users, languages)
	return f
}

func (f queryFiltersFixture) get(t *testing.T, actor dto.UUID, path string) *httptest.ResponseRecorder {
	t.Helper()
	record := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("X-Item-Access-Actor", actor.String())
	f.router.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, record.Code, record.Body.String())
	}
	return record
}

// TestQueryFiltersSwiftfinRequestShapes covers the two requests Swiftfin
// sends and pins the QueryFilters body upstream returns: Genres as
// name and id, Tags always empty, and the stream languages named by culture
// and ordered by name (FilterController.cs:112-252).
func TestQueryFiltersSwiftfinRequestShapes(t *testing.T) {
	f := newQueryFiltersFixture(t)
	want := `{"Genres":[{"Name":"Anime","Id":"` + f.anime.String() + `"},{"Name":"Drama","Id":"` + f.drama.String() + `"}],` +
		`"Tags":[],` +
		`"AudioLanguages":[{"Name":"English (eng)","Value":"eng"},{"Name":"Japanese (jpn)","Value":"jpn"}],` +
		`"SubtitleLanguages":[{"Name":"English (eng)","Value":"eng"},{"Name":"Undetermined (und)","Value":"und"}]}`
	for _, path := range []string{
		"/Items/Filters2?userId=" + f.admin.String() + "&parentId=" + f.shows.String() + "&includeItemTypes=Series&recursive=true",
		"/Items/Filters2?userId=" + f.admin.String() + "&includeItemTypes=Series&recursive=true",
	} {
		got := f.get(t, f.admin, path).Body.String()
		if got != want+"\n" && got != want {
			t.Fatalf("GET %s =\n%s\nwant\n%s", path, got, want)
		}
	}

	camel := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/Items/Filters2?includeItemTypes=Series", nil)
	request.Header.Set("X-Item-Access-Actor", f.admin.String())
	request.Header.Set("Accept", `application/json; profile="CamelCase"`)
	f.router.ServeHTTP(camel, request)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(camel.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode camel body: %v", err)
	}
	for _, key := range []string{"genres", "tags", "audioLanguages", "subtitleLanguages"} {
		if _, found := body[key]; !found {
			t.Fatalf("camel body %s has no %q", camel.Body.String(), key)
		}
	}
}

func TestQueryFiltersScopesKindsAndUsers(t *testing.T) {
	f := newQueryFiltersFixture(t)
	decode := func(t *testing.T, actor dto.UUID, path string) dto.QueryFilters {
		t.Helper()
		var got dto.QueryFilters
		if err := json.Unmarshal(f.get(t, actor, path).Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return got
	}
	none := []dto.NameValuePair{}
	for _, tc := range []struct {
		name               string
		actor              dto.UUID
		path               string
		genres             []dto.NameGuidPair
		audio, subtitle    []dto.NameValuePair
		wantQueryRecursive bool
	}{
		{
			name: "movies keep an unknown language code as its name", actor: f.admin,
			path:     "/Items/Filters2?includeItemTypes=Movie",
			genres:   []dto.NameGuidPair{{Name: "Action", ID: f.action}},
			audio:    []dto.NameValuePair{{Name: "French (fre)", Value: "fre"}},
			subtitle: []dto.NameValuePair{{Name: "qaa-custom", Value: "qaa-custom"}},
		},
		{
			name: "no item types read no languages", actor: f.admin,
			path:   "/Items/Filters2",
			genres: []dto.NameGuidPair{{Name: "Action", ID: f.action}, {Name: "Anime", ID: f.anime}, {Name: "Drama", ID: f.drama}},
			audio:  none, subtitle: none,
		},
		{
			name: "parent scope excludes another library", actor: f.admin,
			path:   "/Items/Filters2?parentId=" + f.shows.String() + "&includeItemTypes=Movie",
			genres: []dto.NameGuidPair{}, audio: none, subtitle: none,
		},
		{
			name: "non-recursive series parent reads direct children", actor: f.admin,
			path:   "/Items/Filters2?parentId=" + f.series.String() + "&includeItemTypes=Episode&recursive=false",
			genres: []dto.NameGuidPair{}, audio: none, subtitle: none,
		},
		{
			name: "recursive series parent reads its episodes", actor: f.admin,
			path:     "/Items/Filters2?parentId=" + f.series.String() + "&includeItemTypes=Episode",
			genres:   []dto.NameGuidPair{},
			audio:    []dto.NameValuePair{{Name: "English (eng)", Value: "eng"}, {Name: "Japanese (jpn)", Value: "jpn"}},
			subtitle: []dto.NameValuePair{{Name: "English (eng)", Value: "eng"}, {Name: "Undetermined (und)", Value: "und"}},
		},
		{
			name: "a missing parent leaves the query unscoped", actor: f.admin,
			path:   "/Items/Filters2?parentId=" + itemNamesUUID(t, 799).String() + "&includeItemTypes=Movie",
			genres: []dto.NameGuidPair{{Name: "Action", ID: f.action}},
			audio:  []dto.NameValuePair{{Name: "French (fre)", Value: "fre"}}, subtitle: []dto.NameValuePair{{Name: "qaa-custom", Value: "qaa-custom"}},
		},
		{
			name: "restricted user sees only allowed libraries", actor: f.restricted,
			path:   "/Items/Filters2?includeItemTypes=Series,Movie",
			genres: []dto.NameGuidPair{{Name: "Action", ID: f.action}},
			audio:  []dto.NameValuePair{{Name: "French (fre)", Value: "fre"}}, subtitle: []dto.NameValuePair{{Name: "qaa-custom", Value: "qaa-custom"}},
		},
		{
			name: "restricted user cannot read a hidden library by parent", actor: f.restricted,
			path:   "/Items/Filters2?parentId=" + f.shows.String() + "&includeItemTypes=Series",
			genres: []dto.NameGuidPair{}, audio: none, subtitle: none,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decode(t, tc.actor, tc.path)
			if !reflect.DeepEqual(got.Genres, tc.genres) || !reflect.DeepEqual(got.AudioLanguages, tc.audio) || !reflect.DeepEqual(got.SubtitleLanguages, tc.subtitle) || !reflect.DeepEqual(got.Tags, []string{}) {
				t.Fatalf("GET %s = %+v, want genres %+v audio %+v subtitles %+v", tc.path, got, tc.genres, tc.audio, tc.subtitle)
			}
		})
	}
}

// TestQueryFiltersReadsTheParentSubtree pins the store query shape: a
// library parent becomes a recursive ParentId scope, which ItemStore drives
// from the parent's subtree, and the stream read includes owned items and
// episodes for a series filter.
func TestQueryFiltersReadsTheParentSubtree(t *testing.T) {
	f := newQueryFiltersFixture(t)
	f.reader.queries = nil
	f.get(t, f.admin, "/Items/Filters2?parentId="+f.shows.String()+"&includeItemTypes=Series&isSeries=true")
	var scoped []store.ItemQuery
	for _, query := range f.reader.queries {
		if len(query.ItemIDs) == 0 && query.ParentID != nil {
			scoped = append(scoped, query)
		}
	}
	if len(scoped) != 3 {
		t.Fatalf("scoped queries = %d, want genre values, audio, and subtitle reads: %+v", len(scoped), f.reader.queries)
	}
	for index, query := range scoped {
		if *query.ParentID != f.shows || !query.Recursive || len(query.AncestorIDs) != 0 || query.IsSeries == nil || !*query.IsSeries || query.User == nil {
			t.Fatalf("query %d = %+v, want a recursive ParentId scope with the user and IsSeries", index, query)
		}
	}
	if scoped[0].IncludeOwnedItems || !reflect.DeepEqual(scoped[0].IncludeItemTypes, []item.Kind{item.KindSeries}) {
		t.Fatalf("genre query = %+v, want only Series and no owned items", scoped[0])
	}
	for _, query := range scoped[1:] {
		if !query.IncludeOwnedItems || !reflect.DeepEqual(query.IncludeItemTypes, []item.Kind{item.KindSeries, item.KindEpisode}) {
			t.Fatalf("stream query = %+v, want owned items and Series plus Episode", query)
		}
	}
}
