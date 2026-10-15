// SPDX-License-Identifier: GPL-2.0-only

package itemedit_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/itemedit"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/localization"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

func editNow() time.Time { return time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC) }

type fixture struct {
	items     *store.ItemStore
	service   *itemedit.Service
	published *recordingPublisher
	manager   *config.Manager
}

type recordingPublisher struct{ ids [][]dto.UUID }

func (p *recordingPublisher) PublishItemUpdated(_, _ dto.UUID, ids []dto.UUID) {
	p.ids = append(p.ids, append([]dto.UUID(nil), ids...))
}

type staticLibraries []library.VirtualFolder

func (s staticLibraries) VirtualFolders(context.Context) ([]library.VirtualFolder, error) {
	return s, nil
}

func newFixture(t *testing.T, libraries staticLibraries) fixture {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	paths, err := config.NewPaths(filepath.Join(t.TempDir(), "program-data"))
	if err != nil {
		t.Fatalf("paths: %v", err)
	}
	bus := events.NewBus[config.Change](clock.NewFake(editNow()))
	t.Cleanup(func() { _ = bus.Close(context.Background()) })
	manager, err := config.Open(t.Context(), paths, bus)
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	languages, err := localization.NewService()
	if err != nil {
		t.Fatalf("localization: %v", err)
	}
	items := store.NewItemStore(stores)
	published := &recordingPublisher{}
	service := itemedit.New(itemedit.Options{
		Items:        items,
		Libraries:    libraries,
		Config:       manager,
		Localization: languages,
		Publisher:    published,
		Clock:        clock.NewFake(editNow()),
	})
	return fixture{items: items, service: service, published: published, manager: manager}
}

func id(value byte) dto.UUID {
	var result dto.UUID
	result[15] = value
	result[0] = 0x37
	return result
}

func text(value string) *string { return &value }

func mustUpsert(t *testing.T, items *store.ItemStore, values ...item.Item) {
	t.Helper()
	for _, value := range values {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}
}

func mustGet(t *testing.T, items *store.ItemStore, itemID dto.UUID) item.Item {
	t.Helper()
	value, err := items.Get(t.Context(), itemID)
	if err != nil {
		t.Fatalf("get %s: %v", itemID, err)
	}
	return value
}

func decodeRequest(t *testing.T, body string) dto.ItemUpdateRequest {
	t.Helper()
	var request dto.ItemUpdateRequest
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return request
}

// seedSeries stores collection folder → series → season → two episodes. The
// second episode locks Tags and OfficialRating.
func seedSeries(t *testing.T, items *store.ItemStore) (series, season, episode, lockedEpisode dto.UUID) {
	t.Helper()
	collection := id(1)
	series, season, episode, lockedEpisode = id(2), id(3), id(4), id(5)
	tv := item.CollectionTypeTVShows
	mustUpsert(t, items,
		item.Item{ID: collection, Kind: item.KindCollectionFolder, Name: text("Shows"), Path: text("/media/shows"), IsFolder: true, Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: &tv}}},
		item.Item{ID: series, Kind: item.KindSeries, ParentID: &collection, Name: text("Wrong Show"), Path: text("/media/shows/Show"), IsFolder: true, Values: item.ItemValues{Tags: []string{"old", "keep"}}},
		item.Item{ID: season, Kind: item.KindSeason, ParentID: &series, Name: text("Season 1"), Path: text("/media/shows/Show/Season 1"), IsFolder: true, IndexNumber: intPtr(1), Values: item.ItemValues{Tags: []string{"old"}}},
		item.Item{ID: episode, Kind: item.KindEpisode, ParentID: &season, Name: text("Pilot"), Path: text("/media/shows/Show/Season 1/e1.mkv"), Values: item.ItemValues{Tags: []string{"old", "episode"}}},
		item.Item{ID: lockedEpisode, Kind: item.KindEpisode, ParentID: &season, Name: text("Second"), Path: text("/media/shows/Show/Season 1/e2.mkv"), OfficialRating: text("TV-MA"), LockedFields: []item.MetadataField{item.MetadataFieldTags, item.MetadataFieldOfficialRating}, Values: item.ItemValues{Tags: []string{"old"}}},
	)
	return series, season, episode, lockedEpisode
}

func intPtr(value int) *int { return &value }

func TestUpdateSeriesCascadesToSeasonsAndEpisodes(t *testing.T) {
	f := newFixture(t, nil)
	series, season, episode, lockedEpisode := seedSeries(t, f.items)

	request := decodeRequest(t, `{
		"Name": "Right Show",
		"ForcedSortName": "Show, Right",
		"OriginalTitle": "  ",
		"OfficialRating": "TV-14",
		"CustomRating": "Family",
		"Tags": [" new ", "keep", "KEEP"],
		"Genres": ["Drama", "drama", "Comedy"],
		"Studios": [{"Name": "HBO", "Id": ""}],
		"ProviderIds": {"Tvdb": "123", "Tmdb": "", "Imdb": null},
		"LockedFields": ["Name", "overview"],
		"LockData": true,
		"Status": "Ended",
		"AirDays": ["Monday"],
		"AirTime": "20:00",
		"RunTimeTicks": 36000000000,
		"PremiereDate": "2020-01-02T00:00:00.0000000Z",
		"People": [{"Name": " Jane Doe ", "Role": " Lead ", "Type": "Actor"}, {"Name": "jane doe", "Type": "Actor"}, {"Name": "John Roe", "Type": "Director"}]
	}`)
	if err := f.service.Update(t.Context(), series, request); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got := mustGet(t, f.items, series)
	if got.Name == nil || *got.Name != "Right Show" || got.SortName == nil || *got.SortName != "show right" {
		t.Fatalf("series name/sort = %v / %v; want Right Show / show right", got.Name, got.SortName)
	}
	if got.OriginalTitle != nil {
		t.Fatalf("whitespace OriginalTitle stored %q; want nil", *got.OriginalTitle)
	}
	if !slices.Equal(got.Values.Tags, []string{"new", "keep"}) {
		t.Fatalf("series tags = %v; want [new keep]", got.Values.Tags)
	}
	if !slices.Equal(got.Values.Genres, []string{"Drama", "Comedy"}) || !slices.Equal(got.Values.Studios, []string{"HBO"}) {
		t.Fatalf("series genres/studios = %v / %v", got.Values.Genres, got.Values.Studios)
	}
	if ids := got.ProviderIDs.Snapshot(); len(ids) != 1 || ids[0].Name != "Tvdb" || ids[0].Value != "123" {
		t.Fatalf("provider ids = %+v; want only Tvdb=123", ids)
	}
	if !got.IsLocked || !slices.Equal(got.LockedFields, []item.MetadataField{item.MetadataFieldName, item.MetadataFieldOverview}) {
		t.Fatalf("locks = %t %v", got.IsLocked, got.LockedFields)
	}
	if got.SeriesStatus == nil || *got.SeriesStatus != item.SeriesStatusEnded || got.RunTimeTicks == nil || *got.RunTimeTicks != 36000000000 {
		t.Fatalf("series status/runtime = %v / %v", got.SeriesStatus, got.RunTimeTicks)
	}
	if got.Extra.Series == nil || !slices.Equal(got.Extra.Series.AirDays, []item.DayOfWeek{item.DayOfWeekMonday}) || got.Extra.Series.AirTime == nil || *got.Extra.Series.AirTime != "20:00" {
		t.Fatalf("series air schedule = %+v", got.Extra.Series)
	}
	if got.PremiereDate == nil || !got.PremiereDate.Equal(time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("premiere = %v", got.PremiereDate)
	}
	if len(got.People) != 2 || got.People[0].Name != "Jane Doe" || got.People[0].Role == nil || *got.People[0].Role != "Lead" || got.People[1].Type != item.PersonKindDirector {
		t.Fatalf("people = %+v", got.People)
	}
	if _, err := f.items.Get(t.Context(), library.PersonEntity("Jane Doe").ID); err != nil {
		t.Fatalf("person entity not stored: %v", err)
	}
	if got.DateLastSaved == nil || !got.DateLastSaved.Equal(editNow()) {
		t.Fatalf("DateLastSaved = %v; want %v", got.DateLastSaved, editNow())
	}

	gotSeason := mustGet(t, f.items, season)
	if gotSeason.SeriesName == nil || *gotSeason.SeriesName != "Right Show" || gotSeason.OfficialRating == nil || *gotSeason.OfficialRating != "TV-14" {
		t.Fatalf("season cascade = series %v rating %v", gotSeason.SeriesName, gotSeason.OfficialRating)
	}
	if !slices.Equal(gotSeason.Values.Tags, []string{"new"}) {
		t.Fatalf("season tags = %v; want [new]", gotSeason.Values.Tags)
	}
	gotEpisode := mustGet(t, f.items, episode)
	if !slices.Equal(gotEpisode.Values.Tags, []string{"episode", "new"}) || gotEpisode.CustomRating == nil || *gotEpisode.CustomRating != "Family" {
		t.Fatalf("episode cascade = tags %v custom %v", gotEpisode.Values.Tags, gotEpisode.CustomRating)
	}
	gotLocked := mustGet(t, f.items, lockedEpisode)
	if !slices.Equal(gotLocked.Values.Tags, []string{"old"}) || gotLocked.OfficialRating == nil || *gotLocked.OfficialRating != "TV-MA" {
		t.Fatalf("locked episode = tags %v rating %v; want unchanged", gotLocked.Values.Tags, gotLocked.OfficialRating)
	}
	// LockData changed on a folder: every descendant takes the new lock.
	for _, descendant := range []dto.UUID{season, episode, lockedEpisode} {
		if !mustGet(t, f.items, descendant).IsLocked {
			t.Fatalf("descendant %s not locked", descendant)
		}
	}
	if len(f.published.ids) != 1 || len(f.published.ids[0]) != 4 || f.published.ids[0][0] != series {
		t.Fatalf("published = %v; want one event for series and three children", f.published.ids)
	}
}

func TestUpdateNullListsKeepStoredValues(t *testing.T) {
	f := newFixture(t, nil)
	movie := id(10)
	mustUpsert(t, f.items, item.Item{ID: movie, Kind: item.KindMovie, Name: text("Movie"), Path: text("/media/movies/m.mkv"), Overview: text("Old"),
		Values: item.ItemValues{Genres: []string{"Action"}, Tags: []string{"tag"}}, LockedFields: []item.MetadataField{item.MetadataFieldGenres}})
	if err := f.service.Update(t.Context(), movie, decodeRequest(t, `{"Name":"Movie","Genres":null,"RunTimeTicks":5}`)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := mustGet(t, f.items, movie)
	if !slices.Equal(got.Values.Genres, []string{"Action"}) || !slices.Equal(got.Values.Tags, []string{"tag"}) || len(got.LockedFields) != 1 {
		t.Fatalf("null lists changed stored values: %+v locks %v", got.Values, got.LockedFields)
	}
	if got.Overview != nil {
		t.Fatalf("absent Overview kept %q; upstream assigns null", *got.Overview)
	}
	if got.RunTimeTicks != nil {
		t.Fatalf("movie runtime = %v; only a series accepts a runtime edit", *got.RunTimeTicks)
	}
}

func TestUpdateRejectsMissingItemAndUnknownEnums(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.service.Update(t.Context(), id(99), dto.ItemUpdateRequest{}); !errors.Is(err, itemedit.ErrItemNotFound) {
		t.Fatalf("Update(missing) = %v; want ErrItemNotFound", err)
	}
	movie := id(11)
	mustUpsert(t, f.items, item.Item{ID: movie, Kind: item.KindMovie, Name: text("Movie"), Path: text("/media/movies/m.mkv")})
	if err := f.service.Update(t.Context(), movie, decodeRequest(t, `{"LockedFields":["Bogus"]}`)); !errors.Is(err, itemedit.ErrInvalidRequest) {
		t.Fatalf("Update(bad LockedFields) = %v; want ErrInvalidRequest", err)
	}
}

func TestMetadataEditorInfo(t *testing.T) {
	collection := id(20)
	libraries := staticLibraries{{CollectionFolderID: collection, CollectionType: "movies"}}
	f := newFixture(t, libraries)
	movie := id(21)
	mustUpsert(t, f.items,
		item.Item{ID: collection, Kind: item.KindCollectionFolder, Name: text("Movies"), Path: text("/media/movies"), IsFolder: true},
		item.Item{ID: movie, Kind: item.KindMovie, ParentID: &collection, Name: text("Movie"), Path: text("/media/movies/Movie/movie.mkv")},
	)
	info, err := f.service.MetadataEditorInfo(t.Context(), movie)
	if err != nil {
		t.Fatalf("MetadataEditorInfo: %v", err)
	}
	if len(info.ParentalRatingOptions) == 0 || len(info.Countries) == 0 || len(info.Cultures) == 0 {
		t.Fatalf("localization lists are empty: %d ratings %d countries %d cultures", len(info.ParentalRatingOptions), len(info.Countries), len(info.Cultures))
	}
	if !slices.ContainsFunc(info.ExternalIdInfos, func(value dto.ExternalIdInfo) bool { return value.Key == "Tmdb" }) {
		t.Fatalf("external ids = %+v; want a Tmdb entry", info.ExternalIdInfos)
	}
	// An inherited library type with no override offers no content-type choice.
	if info.ContentType != nil || len(info.ContentTypeOptions) != 0 {
		t.Fatalf("content type = %v options %v; want none for a typed library", info.ContentType, info.ContentTypeOptions)
	}

	if err := f.service.UpdateContentType(t.Context(), movie, "tvshows"); err != nil {
		t.Fatalf("UpdateContentType: %v", err)
	}
	if got := f.manager.System().ContentTypes; len(got) != 1 || got[0].Name != "/media/movies/Movie" || got[0].Value != "tvshows" {
		t.Fatalf("ContentTypes = %+v; want the containing folder override", got)
	}
	info, err = f.service.MetadataEditorInfo(t.Context(), movie)
	if err != nil {
		t.Fatalf("MetadataEditorInfo after override: %v", err)
	}
	if info.ContentType == nil || *info.ContentType != "tvshows" {
		t.Fatalf("content type = %v; want tvshows", info.ContentType)
	}
	var values []string
	for _, option := range info.ContentTypeOptions {
		values = append(values, option.Value)
	}
	if !slices.Equal(values, []string{"", "movies", "tvshows"}) {
		t.Fatalf("content type options = %v; want inherit, movies, tvshows", values)
	}

	if err := f.service.UpdateContentType(t.Context(), movie, ""); err != nil {
		t.Fatalf("UpdateContentType(clear): %v", err)
	}
	if got := f.manager.System().ContentTypes; len(got) != 0 {
		t.Fatalf("ContentTypes after clear = %+v; want empty", got)
	}
}
