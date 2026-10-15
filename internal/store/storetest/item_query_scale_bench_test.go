// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// BenchmarkItemQueryAtLibraryScale measures library pages and their DTO
// user-data projections on the library of BenchmarkReconcileAtLibraryScale
// (about 47,000 items), plus a one-series library and 1,400 played rows. A
// page of a small library must cost about the same as the page itself, not a
// scan of the whole library. The grouped Latest scenarios also read the
// page's media sources, as the DTO build does. It runs on file-backed SQLite
// and, when DEADFIN_TEST_POSTGRES_DSN is set, on PostgreSQL.
//
//	go test -run '^$' -bench ItemQueryAtLibraryScale ./internal/store/storetest/
func BenchmarkItemQueryAtLibraryScale(b *testing.B) {
	forEachScaleBackend(b, benchmarkItemQueryAtLibraryScale)
}

func benchmarkItemQueryAtLibraryScale(b *testing.B, stores store.Stores) {
	ctx := b.Context()
	items := store.NewItemStore(stores)
	writeScaleFixture(b, ctx, stores)
	db := stores.Writer()
	q := scaleSQL{stores.Dialect()}
	// writeScaleFixture numbers the 31,000 people first, then the root and
	// the Movies library.
	root, movies := scaleUUID(31_001), scaleUUID(31_002)
	small, series := scaleUUID(900_001), scaleUUID(900_002)
	insert := q.query("INSERT INTO items (id, kind, parent_id, name, playlist_owner_user_id, source_type, is_folder, is_virtual_item, is_in_mixed_folder, is_locked, is_movie, is_series, is_repeat, is_placeholder, open_access) VALUES (?, ?, ?, ?, ?, '0', TRUE, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE)")
	for _, row := range []struct {
		id, parent dto.UUID
		kind, name string
	}{{small, root, "CollectionFolder", "Small"}, {series, small, "Series", "Small Show"}} {
		if _, err := db.ExecContext(ctx, insert, q.id(row.id), row.kind, q.id(row.parent), row.name, q.id(dto.UUID{})); err != nil {
			b.Fatalf("insert %s: %v", row.name, err)
		}
	}
	if _, err := db.ExecContext(ctx, q.query(`UPDATE items SET extra = '{"collectionFolder":{"collectionType":2}}' WHERE id = ?`), q.id(small)); err != nil {
		b.Fatalf("mark the small library as tvshows: %v", err)
	}
	if err := items.RepairDerived(ctx); err != nil {
		b.Fatalf("initial global repair: %v", err)
	}
	user := store.User{ID: scaleUUID(990_001), Policy: []byte(`{"IsAdministrator":true}`)}
	if _, err := db.ExecContext(ctx, q.query("INSERT INTO user_data (user_id, custom_key, item_id, played, play_count) SELECT ?, MIN(custom_key), item_id, TRUE, 1 FROM item_user_data_keys GROUP BY item_id ORDER BY item_id LIMIT 1400"), q.id(user.ID)); err != nil {
		b.Fatalf("insert user data: %v", err)
	}
	analyzeScaleFixture(b, ctx, stores)

	limit, latestLimit, pastEnd := 100, 32, 200
	notFolder, notPlayed := false, false
	page := func(parent dto.UUID, kind item.Kind, start *int) store.ItemQuery {
		return store.ItemQuery{Recursive: true, ParentID: &parent, IncludeItemTypes: []item.Kind{kind}, StartIndex: start, Limit: &limit,
			SortBy: []store.ItemSortBy{store.ItemSortBySortName}, SortOrder: []store.SortOrder{store.SortOrderAscending},
			User: &user, UserID: &user.ID, EnableTotalRecordCount: true}
	}
	latest := store.ItemQuery{Recursive: true, ParentID: &small, IsFolder: &notFolder, IsPlayed: &notPlayed, Limit: &latestLimit,
		SortBy: []store.ItemSortBy{store.ItemSortByDateCreated}, SortOrder: []store.SortOrder{store.SortOrderDescending},
		User: &user, UserID: &user.ID}
	for _, scenario := range []struct {
		name  string
		query store.ItemQuery
	}{
		{"one-item library page", page(small, item.KindSeries, nil)},
		{"empty page", page(small, item.KindSeries, &pastEnd)},
		{"small library latest", latest},
		{"large library page", page(movies, item.KindMovie, nil)},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			for range b.N {
				result, err := items.Query(ctx, scenario.query)
				if err != nil {
					b.Fatalf("query: %v", err)
				}
				ids := make([]dto.UUID, len(result.Items))
				for index, value := range result.Items {
					ids[index] = value.ID
				}
				if _, err := items.DTOProjections(ctx, ids, &user.ID, false, true, false, false, ids, &user); err != nil {
					b.Fatalf("DTO projections: %v", err)
				}
			}
		})
	}
	// UserViews projects user data for the library roots. Jellyfin does not
	// aggregate played counts for CollectionFolder, so dtobuild requests none.
	b.Run("user views", func(b *testing.B) {
		views := []dto.UUID{movies, small}
		for range b.N {
			if _, err := items.DTOProjections(ctx, views, &user.ID, false, true, false, false, nil, &user); err != nil {
				b.Fatalf("DTO projections: %v", err)
			}
		}
	})
	groupLimit := 16
	grouped := func(parent dto.UUID) store.ItemQuery {
		return store.ItemQuery{Recursive: true, ParentID: &parent, IsFolder: &notFolder, Limit: &groupLimit,
			SortBy: []store.ItemSortBy{store.ItemSortByDateCreated}, SortOrder: []store.SortOrder{store.SortOrderDescending},
			User: &user, UserID: &user.ID}
	}
	// HidePlayedInLatest adds IsPlayed=false to Latest.
	unplayed := func(query store.ItemQuery) store.ItemQuery {
		query.IsPlayed = &notPlayed
		return query
	}
	// The Music library: 50 artists of 4 albums of 20 tracks.
	shows, music := scaleUUID(31_003), scaleUUID(31_004)
	for _, library := range []struct {
		id             dto.UUID
		collectionType int
	}{{movies, 1}, {music, 3}} {
		if _, err := db.ExecContext(ctx, q.query(fmt.Sprintf(`UPDATE items SET extra = '{"collectionFolder":{"collectionType":%d}}' WHERE id = ?`, library.collectionType)), q.id(library.id)); err != nil {
			b.Fatalf("mark library %s: %v", library.id, err)
		}
	}
	// The Shows library: 200 series of 45 episodes, each episode of a series
	// newer than the one before. Movies get the key the movies path groups by.
	episodeDate := "1700000000000000 + (COALESCE(parent_index_number, 0) * 100 + COALESCE(index_number, 0)) * 60000000"
	if q.postgres() {
		episodeDate = "TIMESTAMPTZ '2023-11-14 22:13:20+00' + (COALESCE(parent_index_number, 0) * 100 + COALESCE(index_number, 0)) * INTERVAL '1 minute'"
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{q.query(`UPDATE items SET extra = '{"collectionFolder":{"collectionType":2}}' WHERE id = ?`), []any{q.id(shows)}},
		{"UPDATE items SET presentation_unique_key = name WHERE kind = 'Movie'", nil},
		{"UPDATE items SET series_name = (SELECT series.name FROM items AS series WHERE series.id = items.series_id) WHERE kind IN ('Season', 'Episode')", nil},
		{"UPDATE items SET date_created = " + episodeDate + " WHERE kind = 'Episode'", nil},
	} {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			b.Fatalf("prepare the shows library: %v", err)
		}
	}
	// The statements above set the keys and dates the Latest queries read.
	// The store keeps planner statistics fresh after such changes.
	analyzeScaleFixture(b, ctx, stores)
	for _, scenario := range []struct {
		name  string
		query store.ItemQuery
	}{
		{"small library grouped latest", grouped(small)},
		{"large library grouped latest", grouped(movies)},
		{"large library grouped latest, played hidden", unplayed(grouped(movies))},
		{"tvshows library grouped latest", grouped(shows)},
		{"music library grouped latest, played hidden", unplayed(grouped(music))},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			for range b.N {
				values, found, err := items.LatestItems(ctx, scenario.query)
				if err != nil || !found {
					b.Fatalf("grouped latest: found %v, %v", found, err)
				}
				ids := make([]dto.UUID, len(values))
				for index, value := range values {
					ids[index] = value.ID
				}
				if _, err := items.MediaSourceRecordsPage(ctx, ids); err != nil {
					b.Fatalf("media sources: %v", err)
				}
				if _, err := items.DTOProjections(ctx, ids, &user.ID, false, true, false, false, ids, &user); err != nil {
					b.Fatalf("DTO projections: %v", err)
				}
			}
		})
	}
	// NextUp over every series with a played episode. Satellites must load
	// for the 24 returned episodes only, not for every candidate.
	for _, statement := range []string{
		"UPDATE items SET presentation_unique_key = name WHERE kind = 'Series'",
		"UPDATE items SET series_presentation_unique_key = (SELECT series.name FROM items AS series WHERE series.id = items.series_id) WHERE kind IN ('Season', 'Episode') AND series_id IS NOT NULL",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			b.Fatalf("set series keys: %v", err)
		}
	}
	// The user watched episodes 1-3 of season 1 of every series.
	lastPlayed := "1700000000000000"
	if q.postgres() {
		lastPlayed = "TIMESTAMPTZ '2023-11-14 22:13:20+00'"
	}
	if _, err := db.ExecContext(ctx, q.query("INSERT INTO user_data (user_id, custom_key, item_id, played, play_count, last_played) SELECT ?, MIN(keys.custom_key), keys.item_id, TRUE, 1, "+lastPlayed+" FROM item_user_data_keys AS keys JOIN items ON items.id = keys.item_id WHERE items.kind = 'Episode' AND items.parent_index_number = 1 AND items.index_number <= 3 GROUP BY keys.item_id ON CONFLICT DO NOTHING"), q.id(user.ID)); err != nil {
		b.Fatalf("insert watched episodes: %v", err)
	}
	analyzeScaleFixture(b, ctx, stores)
	nextUpLimit := 24
	b.Run("next up", func(b *testing.B) {
		for range b.N {
			result, err := items.NextUp(ctx, store.NextUpQuery{UserID: user.ID, User: &user, Limit: &nextUpLimit})
			if err != nil || len(result.Items) == 0 {
				b.Fatalf("next up: %d items, %v", len(result.Items), err)
			}
		}
	})
	// GET /Items/Filters2 reads the genre values of the scoped items and
	// the audio and subtitle languages of their streams. Every movie and
	// episode gets one audio and one subtitle stream.
	for _, statement := range []string{
		"INSERT INTO media_streams (item_id, stream_index, stream_type, codec, language) SELECT id, 0, 'Audio', 'aac', CASE WHEN kind = 'Movie' THEN 'eng' ELSE 'jpn' END FROM items WHERE kind IN ('Movie', 'Episode')",
		"INSERT INTO media_streams (item_id, stream_index, stream_type, codec, language) SELECT id, 1, 'Subtitle', 'srt', NULL FROM items WHERE kind IN ('Movie', 'Episode')",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			b.Fatalf("insert streams: %v", err)
		}
	}
	analyzeScaleFixture(b, ctx, stores)
	filters := func(parent dto.UUID, kinds ...item.Kind) store.ItemQuery {
		return store.ItemQuery{Recursive: true, ParentID: &parent, IncludeItemTypes: kinds, User: &user, UserID: &user.ID}
	}
	for _, scenario := range []struct {
		name           string
		genres, stream store.ItemQuery
	}{
		{"small library filters", filters(small, item.KindSeries), filters(small, item.KindSeries, item.KindEpisode)},
		{"tvshows library filters", filters(shows, item.KindSeries), filters(shows, item.KindSeries, item.KindEpisode)},
		{"large library filters", filters(movies, item.KindMovie), filters(movies, item.KindMovie)},
	} {
		scenario.stream.IncludeOwnedItems = true
		b.Run(scenario.name, func(b *testing.B) {
			for range b.N {
				if _, err := items.QueryItemValues(ctx, scenario.genres, "genre"); err != nil {
					b.Fatalf("genre values: %v", err)
				}
				for _, streamType := range []string{"Audio", "Subtitle"} {
					if languages, err := items.QueryStreamLanguages(ctx, scenario.stream, streamType); err != nil || (scenario.name != "small library filters" && len(languages) == 0) {
						b.Fatalf("%s languages: %v, %v", streamType, languages, err)
					}
				}
			}
		})
	}
}
