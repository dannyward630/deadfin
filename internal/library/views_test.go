// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/user"
)

// TestViewsQueryUsesPersistedCollectionFolders exercises the real ItemStore
// seam. Generated groups and preset shadows must retain their source view
// semantics while hidden roots are excluded before stable ordering.
func TestViewsQueryUsesPersistedCollectionFolders(t *testing.T) {
	fixture := newPersistedViewsFixture(t)
	fixture.configure(t, map[string]any{
		"GroupedFolders":  []dto.UUID{fixture.moviesA.ID, fixture.moviesB.ID},
		"MyMediaExcludes": []dto.UUID{fixture.hidden.ID},
		"OrderedViews":    []dto.UUID{fixture.music.ID},
	}, nil)

	result, err := fixture.views.Query(t.Context(), library.UserViewsQuery{UserID: fixture.user.ID, PresetViews: []item.CollectionType{item.CollectionTypeMusic}})
	if err != nil {
		t.Fatalf("query persisted UserViews: %v", err)
	}
	if got, want := persistedViewNames(result.Items), []string{"Music", "Movies", "Shows"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted UserViews names = %#v, want %#v", got, want)
	}
	if result.TotalRecordCount != 3 || result.StartIndex != 0 {
		t.Fatalf("persisted UserViews page = %#v, want three unpaged rows", result)
	}
	byName := persistedViewsByName(t, result.Items)
	if music := byName["Music"]; music.Kind != item.KindUserView || music.Extra.UserView == nil || music.Extra.UserView.DisplayParentID != fixture.music.ID {
		t.Fatalf("preset music view = %#v, want UserView shadowing persisted music folder %s", music, fixture.music.ID)
	}
	if movies := byName["Movies"]; movies.Kind != item.KindUserView || movies.Extra.UserView == nil || movies.Extra.UserView.ViewType == nil || *movies.Extra.UserView.ViewType != item.CollectionTypeMovies {
		t.Fatalf("grouped movies view = %#v, want generated movie UserView", movies)
	}
	if shows := byName["Shows"]; shows.Kind != item.KindCollectionFolder || shows.ID != fixture.shows.ID {
		t.Fatalf("ungrouped shows view = %#v, want persisted CollectionFolder %s", shows, fixture.shows.ID)
	}
}

// TestViewsGroupingOptionsPreservesPersistedCollectionFolderIDs proves this
// endpoint does not substitute generated UserView identities.
func TestViewsGroupingOptionsPreservesPersistedCollectionFolderIDs(t *testing.T) {
	fixture := newPersistedViewsFixture(t)
	options, err := fixture.views.GroupingOptions(t.Context(), fixture.user.ID)
	if err != nil {
		t.Fatalf("get persisted grouping options: %v", err)
	}
	want := []library.GroupingOption{
		{Name: "Alpha Movies", ID: fixture.moviesA.ID},
		{Name: "Beta Movies", ID: fixture.moviesB.ID},
		{Name: "Hidden", ID: fixture.hidden.ID},
		{Name: "Shows", ID: fixture.shows.ID},
	}
	if !reflect.DeepEqual(options, want) {
		t.Fatalf("persisted grouping options = %#v, want %#v", options, want)
	}
}

func TestViewsQueryHonorsPersistedExplicitFolderAccess(t *testing.T) {
	fixture := newPersistedViewsFixture(t)
	fixture.configure(t, nil, map[string]any{"EnableAllFolders": false, "EnabledFolders": []dto.UUID{fixture.music.ID}})
	result, err := fixture.views.Query(t.Context(), library.UserViewsQuery{UserID: fixture.user.ID})
	if err != nil {
		t.Fatalf("query restricted persisted UserViews: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != fixture.music.ID || result.Items[0].Kind != item.KindCollectionFolder {
		t.Fatalf("restricted persisted UserViews = %#v, want only music CollectionFolder %s", result, fixture.music.ID)
	}
}

type persistedViewsFixture struct {
	views                   *library.Views
	users                   *user.Service
	user                    store.User
	moviesA, moviesB, shows item.Item
	music, hidden           item.Item
}

func newPersistedViewsFixture(t *testing.T) persistedViewsFixture {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open persisted views SQLite store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close persisted views SQLite store: %v", err)
		}
	})
	users := user.New(store.NewIdentity(stores), clock.NewFake(time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC)), bytes.NewReader(bytes.Repeat([]byte{0x42}, 128)))
	created, err := users.Create(t.Context(), "views-user", "correct password")
	if err != nil {
		t.Fatalf("create persisted views user: %v", err)
	}
	items := store.NewItemStore(stores)
	folders := []item.Item{
		persistedViewFolder(t, 1, "Alpha Movies", item.CollectionTypeMovies),
		persistedViewFolder(t, 2, "Beta Movies", item.CollectionTypeMovies),
		persistedViewFolder(t, 3, "Shows", item.CollectionTypeTVShows),
		persistedViewFolder(t, 4, "Music", item.CollectionTypeMusic),
		persistedViewFolder(t, 5, "Hidden", item.CollectionTypeMovies),
	}
	for _, folder := range folders {
		if err := items.Upsert(t.Context(), folder); err != nil {
			t.Fatalf("upsert persisted view folder %s: %v", *folder.Name, err)
		}
	}
	return persistedViewsFixture{
		views: library.NewViews(items, users), users: users, user: created,
		moviesA: folders[0], moviesB: folders[1], shows: folders[2], music: folders[3], hidden: folders[4],
	}
}

func (f persistedViewsFixture) configure(t *testing.T, configuration, policy map[string]any) {
	t.Helper()
	updated, err := f.users.Get(t.Context(), f.user.ID)
	if err != nil {
		t.Fatalf("reload persisted views user: %v", err)
	}
	if configuration != nil {
		updated.Configuration = persistedViewsJSON(t, configuration)
	}
	if policy != nil {
		updated.Policy = persistedViewsJSON(t, policy)
	}
	if err := f.users.Update(t.Context(), updated); err != nil {
		t.Fatalf("update persisted views user: %v", err)
	}
}

func persistedViewFolder(t *testing.T, number int, name string, collection item.CollectionType) item.Item {
	t.Helper()
	id := viewsID(t, number)
	path := "/library/" + name
	return item.Item{
		ID: id, Kind: item.KindCollectionFolder, IsFolder: true, Name: &name, SortName: &name, Path: &path,
		Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: &collection}},
	}
}

func persistedViewNames(items []item.Item) []string {
	names := make([]string, len(items))
	for index, value := range items {
		if value.Name != nil {
			names[index] = *value.Name
		}
	}
	return names
}

func persistedViewsByName(t *testing.T, values []item.Item) map[string]item.Item {
	t.Helper()
	out := make(map[string]item.Item, len(values))
	for _, value := range values {
		if value.Name == nil {
			t.Fatalf("persisted view without a name: %#v", value)
		}
		out[*value.Name] = value
	}
	return out
}

func persistedViewsJSON(t *testing.T, value map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func viewsID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
