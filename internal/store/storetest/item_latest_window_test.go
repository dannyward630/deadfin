// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetestpkg "github.com/deadfin-org/deadfin/internal/store/storetest"
)

// Latest for a movies or music library walks the library newest first and
// stops at its limit. These tests compare it with the grouping of every
// filtered item, computed here from Query. The fixture makes the walk take
// several pages: each movie's folder is newer than every movie, most new
// movies are played, many dates tie or are NULL, and some movies share a key.

func TestItemLatestWindowSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	assertLatestWindow(t, stores)
}

func TestItemLatestWindowPostgres(t *testing.T) {
	assertLatestWindow(t, storetestpkg.OpenPostgres(t))
}

type latestWindowFixture struct {
	movies, music dto.UUID
	user          store.User
	albumDates    map[dto.UUID]*time.Time
}

func newLatestWindowFixture(t *testing.T, items *store.ItemStore) latestWindowFixture {
	t.Helper()
	ctx := t.Context()
	next := 0
	// The ids do not follow the insertion order, so the id order breaks ties
	// in no particular pattern.
	id := func() dto.UUID { next++; return itemQueryPlanUUID(t, 300_000+(next*7919)%100_003) }
	name := itemQueryPlanString
	base := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	at := func(minutes int) *time.Time { value := base.Add(time.Duration(minutes) * time.Minute); return &value }
	library := func(kind item.CollectionType) *item.CollectionType { return &kind }
	aggregate := id()
	f := latestWindowFixture{movies: id(), music: id(), albumDates: map[dto.UUID]*time.Time{}}
	f.user = store.User{ID: id(), Policy: []byte(`{"IsAdministrator":true}`)}
	values := []item.Item{
		{ID: aggregate, Kind: item.KindAggregateFolder, Name: name("root")},
		{ID: f.movies, Kind: item.KindCollectionFolder, ParentID: &aggregate, Name: name("Movies"), IsFolder: true,
			Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: library(item.CollectionTypeMovies)}}},
		{ID: f.music, Kind: item.KindCollectionFolder, ParentID: &aggregate, Name: name("Music"), IsFolder: true,
			Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: library(item.CollectionTypeMusic)}}},
	}
	var played []dto.UUID
	for index := 0; index < 300; index++ {
		folderKey := fmt.Sprintf("f%03d", index)
		folder := item.Item{ID: id(), Kind: item.KindFolder, ParentID: &f.movies, Name: name("Folder"), IsFolder: true, PresentationUniqueKey: &folderKey, DateCreated: at(100_000 + index)}
		movie := item.Item{ID: id(), Kind: item.KindMovie, ParentID: &folder.ID, Name: name("Movie"), DateCreated: at(index % 40)}
		key := fmt.Sprintf("k%03d", index)
		if index < 30 {
			key = fmt.Sprintf("k%03d", index/3)
		}
		if index%17 != 5 {
			movie.PresentationUniqueKey = &key
		}
		if index%11 == 3 {
			movie.DateCreated = nil
		}
		if index%10 < 7 && index%40 > 12 {
			played = append(played, movie.ID)
		}
		values = append(values, folder, movie)
	}
	for artistIndex := 0; artistIndex < 3; artistIndex++ {
		artist := item.Item{ID: id(), Kind: item.KindMusicArtist, ParentID: &f.music, Name: name("Artist"), IsFolder: true, DateCreated: at(200_000)}
		values = append(values, artist)
		for albumIndex := 0; albumIndex < 25; albumIndex++ {
			album := item.Item{ID: id(), Kind: item.KindMusicAlbum, ParentID: &artist.ID, Name: name("Album"), IsFolder: true, DateCreated: at(albumIndex % 9)}
			if albumIndex%7 == 2 {
				album.DateCreated = nil
			}
			f.albumDates[album.ID] = album.DateCreated
			values = append(values, album)
			for track := 0; track < 3; track++ {
				audio := item.Item{ID: id(), Kind: item.KindAudio, ParentID: &album.ID, Name: name("Track"), DateCreated: at(track)}
				// Every track of most albums is played, so the walk must
				// read past them.
				if albumIndex%4 != 0 || (artistIndex == 0 && track == 0) {
					played = append(played, audio.ID)
				}
				values = append(values, audio)
			}
		}
	}
	for _, value := range values {
		if err := items.Upsert(ctx, value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}
	for _, itemID := range played {
		if err := items.UpsertUserData(ctx, store.UserData{UserID: f.user.ID, CustomKey: itemID.String(), ItemID: itemID, Played: true}); err != nil {
			t.Fatalf("upsert user data: %v", err)
		}
	}
	return f
}

// latestNewer orders by DateCreated descending with NULL last, then by
// tiebreak.
func latestNewer(a, b *time.Time, tiebreak func() bool) bool {
	switch {
	case a == nil && b == nil:
		return tiebreak()
	case a == nil || b == nil:
		return b == nil
	case !a.Equal(*b):
		return a.After(*b)
	default:
		return tiebreak()
	}
}

func latestWindowWantMovies(all []item.Item, limit int) []dto.UUID {
	newest := map[string]item.Item{}
	for _, value := range all {
		if value.PresentationUniqueKey == nil {
			continue
		}
		key := *value.PresentationUniqueKey
		current, found := newest[key]
		if !found || latestNewer(value.DateCreated, current.DateCreated, func() bool { return bytes.Compare(value.ID[:], current.ID[:]) > 0 }) {
			newest[key] = value
		}
	}
	groups := make([]item.Item, 0, len(newest))
	for _, value := range newest {
		groups = append(groups, value)
	}
	sort.Slice(groups, func(i, j int) bool {
		return latestNewer(groups[i].DateCreated, groups[j].DateCreated, func() bool { return *groups[i].PresentationUniqueKey < *groups[j].PresentationUniqueKey })
	})
	ids := []dto.UUID{}
	for _, value := range groups[:min(limit, len(groups))] {
		ids = append(ids, value.ID)
	}
	return ids
}

func latestWindowWantAlbums(all []item.Item, albumDates map[dto.UUID]*time.Time, limit int) []dto.UUID {
	seen := map[dto.UUID]bool{}
	albums := []dto.UUID{}
	for _, value := range all {
		if value.ParentID == nil || seen[*value.ParentID] {
			continue
		}
		if _, album := albumDates[*value.ParentID]; album {
			seen[*value.ParentID] = true
			albums = append(albums, *value.ParentID)
		}
	}
	sort.Slice(albums, func(i, j int) bool {
		return latestNewer(albumDates[albums[i]], albumDates[albums[j]], func() bool { return bytes.Compare(albums[i][:], albums[j][:]) > 0 })
	})
	return albums[:min(limit, len(albums))]
}

func assertLatestWindow(t *testing.T, stores store.Stores) {
	t.Helper()
	items := store.NewItemStore(stores)
	f := newLatestWindowFixture(t, items)
	notFolder, notVirtual, notPlayed := false, false, false
	for _, parent := range []struct {
		name string
		id   dto.UUID
	}{{"movies", f.movies}, {"music", f.music}} {
		for _, shape := range []struct {
			name  string
			query func() store.ItemQuery
		}{
			{"all", func() store.ItemQuery {
				return store.ItemQuery{IsFolder: &notFolder}
			}},
			{"unplayed", func() store.ItemQuery {
				return store.ItemQuery{IsFolder: &notFolder, IsPlayed: &notPlayed}
			}},
			{"typed unplayed", func() store.ItemQuery {
				return store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindMovie, item.KindAudio}, IsPlayed: &notPlayed}
			}},
		} {
			base := shape.query()
			base.ParentID, base.Recursive, base.IsVirtualItem, base.User, base.UserID = &parent.id, true, &notVirtual, &f.user, &f.user.ID
			all, err := items.Query(t.Context(), base)
			if err != nil {
				t.Fatalf("query %s/%s: %v", parent.name, shape.name, err)
			}
			for _, limit := range []int{1, 7, 16, 50, 120, 400} {
				t.Run(fmt.Sprintf("%s/%s/limit %d", parent.name, shape.name, limit), func(t *testing.T) {
					want := latestWindowWantMovies(all.Items, limit)
					if parent.id == f.music {
						want = latestWindowWantAlbums(all.Items, f.albumDates, limit)
					}
					query := base
					query.Limit = &limit
					values, grouped, err := items.LatestItems(t.Context(), query)
					if err != nil || !grouped {
						t.Fatalf("LatestItems: grouped %v, %v", grouped, err)
					}
					got := []dto.UUID{}
					for _, value := range values {
						got = append(got, value.ID)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("LatestItems ids = %v\nwant %v", got, want)
					}
				})
			}
		}
	}
}

// TestItemLatestWindowStopsEarly pins that movie Latest reads newest-first
// pages of the library through ix_items_top_parent_latest. The grouped
// statement over every item cost 78% of Latest Movies on a 1,073-movie
// library.
func TestItemLatestWindowStopsEarly(t *testing.T) {
	recorder := &nextUpStatementRecorder{}
	items := openNextUpStatementRecordingSQLite(t, recorder)
	f := newLatestWindowFixture(t, items)
	// The recording store hides its connections; plan on a twin.
	planned, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = planned.Close() })
	newLatestWindowFixture(t, store.NewItemStore(planned))
	notFolder, notVirtual, notPlayed := false, false, false
	limit := 16
	values, grouped, err := items.LatestItems(t.Context(), store.ItemQuery{ParentID: &f.movies, Recursive: true, IsFolder: &notFolder, IsVirtualItem: &notVirtual,
		IsPlayed: &notPlayed, User: &f.user, UserID: &f.user.ID, Limit: &limit})
	if err != nil || !grouped || len(values) != limit {
		t.Fatalf("LatestItems = %d items, grouped %v, %v; want %d, true, nil", len(values), grouped, err, limit)
	}
	pages := 0
	for _, statement := range recorder.snapshot() {
		if strings.Contains(statement.query, "GROUP BY") {
			t.Fatalf("LatestItems groups every item: %s", statement.query)
		}
		if !strings.Contains(statement.query, "presentation_unique_key IS NOT NULL ORDER BY date_created DESC") {
			continue
		}
		pages++
		plan := latestWindowPlan(t, planned, statement.query, f)
		if !strings.Contains(plan, "ix_items_top_parent_latest") {
			t.Fatalf("window page does not read ix_items_top_parent_latest:\n%s", plan)
		}
	}
	if pages == 0 || pages > 3 {
		t.Fatalf("LatestItems read %d window pages, want 1 to 3", pages)
	}
}

func latestWindowPlan(t *testing.T, stores store.Stores, statement string, f latestWindowFixture) string {
	t.Helper()
	// The window page is the scope subquery; plan it alone with its parent.
	start := strings.Index(statement, "SELECT id AS scope_item_id")
	end := strings.Index(statement[start:], ") AS parent_scope")
	page := statement[start : start+end]
	rows, err := stores.Reader().QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+page, f.movies[:])
	if err != nil {
		t.Fatalf("explain %s: %v", page, err)
	}
	var detail []string
	for rows.Next() {
		var id, parent, unused int
		var step string
		if err := rows.Scan(&id, &parent, &unused, &step); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		detail = append(detail, step)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close plan: %v", err)
	}
	plan := strings.Join(detail, "\n")
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("window page sorts the library:\n%s", plan)
	}
	return plan
}
