// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/migrate"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	moderncsqlite "modernc.org/sqlite"
)

func TestItemStoreNextUpBatchesSeriesHistoryThenOrdersResumesAndPages(t *testing.T) {
	items := openNextUpItemStore(t)
	user := nextUpID(t, 1)
	seriesA, seriesB := nextUpID(t, 2), nextUpID(t, 3)
	a1, a2, b1, b2 := nextUpID(t, 10), nextUpID(t, 11), nextUpID(t, 12), nextUpID(t, 13)
	for _, value := range []item.Item{
		{ID: seriesA, Kind: item.KindSeries, PresentationUniqueKey: nextUpString("series-a")},
		{ID: seriesB, Kind: item.KindSeries, PresentationUniqueKey: nextUpString("series-b")},
		nextUpEpisode(a1, seriesA, "series-a", 1, 1),
		nextUpEpisode(a2, seriesA, "series-a", 1, 2),
		nextUpEpisode(b1, seriesB, "series-b", 1, 1),
		nextUpEpisode(b2, seriesB, "series-b", 1, 2),
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s: %v", value.ID, err)
		}
	}
	oldPlayed := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	recentPlayed := time.Date(2025, 1, 4, 0, 0, 0, 0, time.UTC)
	for _, data := range []store.UserData{
		{UserID: user, CustomKey: "a-one", ItemID: a1, Played: true, LastPlayed: &oldPlayed},
		{UserID: user, CustomKey: "b-one", ItemID: b1, Played: true, LastPlayed: &recentPlayed},
		// An in-progress next B episode remains a valid unplayed candidate when
		// EnableResumable is true, but source DetermineNextEpisode drops it when
		// resumable output is disabled.
		{UserID: user, CustomKey: "b-two", ItemID: b2, PositionTicks: 1},
	} {
		if err := items.UpsertUserData(t.Context(), data); err != nil {
			t.Fatalf("upsert user data %q: %v", data.CustomKey, err)
		}
	}

	loadedSeries, err := items.Get(t.Context(), seriesA)
	if err != nil || loadedSeries.PresentationUniqueKey == nil || *loadedSeries.PresentationUniqueKey != "series-a" {
		t.Fatalf("loaded series presentation key = %#v, err=%v", loadedSeries.PresentationUniqueKey, err)
	}
	loadedEpisode, err := items.Get(t.Context(), a2)
	if err != nil || loadedEpisode.SeriesPresentationUniqueKey == nil || *loadedEpisode.SeriesPresentationUniqueKey != "series-a" {
		t.Fatalf("loaded episode series key = %#v, err=%v", loadedEpisode.SeriesPresentationUniqueKey, err)
	}

	seriesOnly, err := items.NextUp(t.Context(), store.NextUpQuery{UserID: user, SeriesID: &seriesA, EnableResumable: true})
	if err != nil {
		t.Fatalf("series next up: %v", err)
	}
	if got, want := nextUpIDs(seriesOnly.Items), []dto.UUID{a2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("series next up = %v, want %v", got, want)
	}

	all, err := items.NextUp(t.Context(), store.NextUpQuery{UserID: user, EnableTotalRecordCount: true, EnableResumable: true})
	if err != nil {
		t.Fatalf("next up with resumable: %v", err)
	}
	if got, want := nextUpIDs(all.Items), []dto.UUID{b2, a2}; !reflect.DeepEqual(got, want) || all.TotalRecordCount != 2 {
		t.Fatalf("next up = %v total=%d, want %v total=2", got, all.TotalRecordCount, want)
	}

	noResume, err := items.NextUp(t.Context(), store.NextUpQuery{UserID: user, EnableTotalRecordCount: true, EnableResumable: false})
	if err != nil {
		t.Fatalf("next up without resumable: %v", err)
	}
	if got, want := nextUpIDs(noResume.Items), []dto.UUID{a2}; !reflect.DeepEqual(got, want) || noResume.TotalRecordCount != 1 {
		t.Fatalf("non-resumable next up = %v total=%d, want %v total=1", got, noResume.TotalRecordCount, want)
	}

	cutoff := time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC)
	filtered, err := items.NextUp(t.Context(), store.NextUpQuery{UserID: user, NextUpDateCutoff: &cutoff, EnableResumable: true, EnableTotalRecordCount: true, StartIndex: nextUpInt(0), Limit: nextUpInt(1)})
	if err != nil {
		t.Fatalf("next up with cutoff: %v", err)
	}
	if got, want := nextUpIDs(filtered.Items), []dto.UUID{b2}; !reflect.DeepEqual(got, want) || filtered.TotalRecordCount != 1 {
		t.Fatalf("cutoff next up = %v total=%d, want %v total=1", got, filtered.TotalRecordCount, want)
	}

	// Last watched by date can differ from last watched by aired position.
	// Rewatching must choose the first played episode after the date cursor.
	b3 := nextUpID(t, 14)
	if err := items.Upsert(t.Context(), nextUpEpisode(b3, seriesB, "series-b", 1, 3)); err != nil {
		t.Fatalf("upsert rewatch episode: %v", err)
	}
	rewatchPlayed := time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC)
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: user, CustomKey: "b-three", ItemID: b3, Played: true, LastPlayed: &rewatchPlayed}); err != nil {
		t.Fatalf("upsert rewatch history: %v", err)
	}
	rewatch, err := items.NextUp(t.Context(), store.NextUpQuery{UserID: user, EnableResumable: true, EnableRewatching: true})
	if err != nil {
		t.Fatalf("next up rewatching: %v", err)
	}
	if got, want := nextUpIDs(rewatch.Items), []dto.UUID{b3, a2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rewatching next up = %v, want %v", got, want)
	}
}

// TestItemStoreNextUpPlacesSpecialsFromDisplaySpecialsWithinSeasons pins
// TVSeriesManager.DetermineNextEpisode: positioned season-zero specials join
// the aired order only when the source server preference enables them. The
// special before the watched episode proves selection advances from the current
// episode rather than treating a season-zero index as an unconditional cursor.
func TestItemStoreNextUpPlacesSpecialsFromDisplaySpecialsWithinSeasons(t *testing.T) {
	for _, test := range []struct {
		name               string
		includeSpecials    bool
		interstitialPlayed bool
		want               int
	}{
		{name: "disabled selects next regular episode", includeSpecials: false, want: 124},
		{name: "enabled selects unplayed special placed before next episode", includeSpecials: true, want: 123},
		{name: "enabled skips played placed special and selects next regular episode", includeSpecials: true, interstitialPlayed: true, want: 124},
	} {
		t.Run(test.name, func(t *testing.T) {
			items := openNextUpItemStore(t)
			user, series := nextUpID(t, 120), nextUpID(t, 121)
			watched := nextUpEpisode(nextUpID(t, 122), series, "special-order", 1, 1)
			interstitial := nextUpEpisode(nextUpID(t, 123), series, "special-order", 0, 2)
			interstitial.AirsBeforeSeasonNumber = nextUpInt(1)
			interstitial.AirsBeforeEpisodeNumber = nextUpInt(2)
			beforeWatched := nextUpEpisode(nextUpID(t, 125), series, "special-order", 0, 1)
			beforeWatched.AirsBeforeSeasonNumber = nextUpInt(1)
			beforeWatched.AirsBeforeEpisodeNumber = nextUpInt(1)
			next := nextUpEpisode(nextUpID(t, 124), series, "special-order", 1, 2)
			key := nextUpString("special-order")

			for _, value := range []item.Item{
				{ID: series, Kind: item.KindSeries, PresentationUniqueKey: key},
				beforeWatched,
				watched,
				interstitial,
				next,
			} {
				if err := items.Upsert(t.Context(), value); err != nil {
					t.Fatalf("upsert fixture item %s: %v", value.ID, err)
				}
			}
			lastPlayed := time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC)
			if err := items.UpsertUserData(t.Context(), store.UserData{UserID: user, CustomKey: "watched-regular", ItemID: watched.ID, Played: true, LastPlayed: &lastPlayed}); err != nil {
				t.Fatalf("mark watched regular episode: %v", err)
			}
			if test.interstitialPlayed {
				if err := items.UpsertUserData(t.Context(), store.UserData{UserID: user, CustomKey: "watched-special", ItemID: interstitial.ID, Played: true, LastPlayed: &lastPlayed}); err != nil {
					t.Fatalf("mark placed special watched: %v", err)
				}
			}

			result, err := items.NextUp(t.Context(), store.NextUpQuery{UserID: user, SeriesID: &series, IncludeSpecials: test.includeSpecials})
			if err != nil {
				t.Fatalf("select next up: %v", err)
			}
			if got, want := nextUpIDs(result.Items), []dto.UUID{nextUpID(t, test.want)}; !reflect.DeepEqual(got, want) {
				t.Fatalf("next up IDs = %v, want %v", got, want)
			}
		})
	}
}

// TestItemStoreNextUpFullUserAccessPolicySQLite keeps every NextUp candidate
// relation subject to the target user's complete visibility policy. PostgreSQL
// runs the same fixture when DEADFIN_TEST_POSTGRES_DSN is configured.
func TestItemStoreNextUpFullUserAccessPolicySQLite(t *testing.T) {
	runItemStoreNextUpFullUserAccessPolicy(t, openItemStore)
}

func TestItemStoreNextUpFullUserAccessPolicyPostgres(t *testing.T) {
	runItemStoreNextUpFullUserAccessPolicy(t, openPostgresItemStore)
}

func runItemStoreNextUpFullUserAccessPolicy(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	fixture := newNextUpAccessFixture(t, open(t))
	query := func(user *store.User) store.NextUpQuery {
		return store.NextUpQuery{User: user, UserID: fixture.userID, EnableResumable: false, IncludeSpecials: true, EnableTotalRecordCount: true}
	}

	t.Run("target policy filters played unplayed specials and resumed candidates before selection count and paging", func(t *testing.T) {
		result, err := fixture.items.NextUp(t.Context(), query(fixture.restrictedUser()))
		if err != nil {
			t.Fatalf("select restricted next up: %v", err)
		}
		fixture.assertResult(t, result, []dto.UUID{fixture.historyNext.ID, fixture.unplayedNext.ID, fixture.specialRegularNext.ID, fixture.resumeNext.ID}, 4)

		start, limit := 1, 2
		pageQuery := query(fixture.restrictedUser())
		pageQuery.StartIndex, pageQuery.Limit = &start, &limit
		page, err := fixture.items.NextUp(t.Context(), pageQuery)
		if err != nil {
			t.Fatalf("page restricted next up: %v", err)
		}
		fixture.assertResult(t, page, []dto.UUID{fixture.unplayedNext.ID, fixture.specialRegularNext.ID}, 4)
	})

	t.Run("series-specific selection does not disclose an inaccessible series", func(t *testing.T) {
		deniedQuery := query(fixture.restrictedUser())
		deniedQuery.SeriesID = &fixture.folderDeniedSeries.ID
		denied, err := fixture.items.NextUp(t.Context(), deniedQuery)
		if err != nil {
			t.Fatalf("select inaccessible series next up: %v", err)
		}
		fixture.assertResult(t, denied, []dto.UUID{}, 0)

		allowedQuery := query(fixture.restrictedUser())
		allowedQuery.SeriesID = &fixture.unplayedSeries.ID
		allowed, err := fixture.items.NextUp(t.Context(), allowedQuery)
		if err != nil {
			t.Fatalf("select accessible series next up: %v", err)
		}
		fixture.assertResult(t, allowed, []dto.UUID{fixture.unplayedNext.ID}, 1)
	})

	t.Run("administrator bypass remains unchanged", func(t *testing.T) {
		administrator := &store.User{ID: fixture.userID, Policy: []byte(`{"IsAdministrator":true,"EnableAllFolders":false,"EnabledFolders":["` + fixture.allowedRoot.ID.String() + `"],"AllowedTags":["allowed"],"BlockedTags":["blocked"],"MaxParentalRating":10,"BlockUnratedItems":[8]}`)}
		result, err := fixture.items.NextUp(t.Context(), query(administrator))
		if err != nil {
			t.Fatalf("select administrator next up: %v", err)
		}
		fixture.assertResult(t, result, []dto.UUID{
			fixture.folderDeniedNext.ID, fixture.untaggedNext.ID, fixture.blockedSeriesNext.ID, fixture.ratingDeniedNext.ID,
			fixture.unratedNext.ID, fixture.historyTail.ID, fixture.unplayedBlockedNext.ID, fixture.specialDenied.ID,
		}, 8)
	})
}

type nextUpAccessFixture struct {
	items                                                                                          *store.ItemStore
	userID                                                                                         dto.UUID
	allowedRoot                                                                                    item.Item
	folderDeniedSeries, unplayedSeries                                                             item.Item
	folderDeniedNext, untaggedNext, blockedSeriesNext, ratingDeniedNext, unratedNext               item.Item
	historyNext, historyTail, unplayedBlockedNext, unplayedNext, specialDenied, specialRegularNext item.Item
	resumeNext                                                                                     item.Item
}

func newNextUpAccessFixture(t *testing.T, items *store.ItemStore) nextUpAccessFixture {
	t.Helper()
	userID := nextUpID(t, 20_000)
	allowedRoot := nextUpAccessFolder(t, 20_001, "allowed library", nil)
	deniedRoot := nextUpAccessFolder(t, 20_002, "denied library", nil)
	allowedSeries := func(value int, name string, root *dto.UUID) item.Item {
		series := item.Item{ID: nextUpID(t, value), Kind: item.KindSeries, Name: nextUpString(name), ParentID: root, PresentationUniqueKey: nextUpString(name)}
		series.Values.Tags = []string{"allowed"}
		series.InheritedParentalRatingValue = nextUpInt(5)
		return series
	}
	plainSeries := func(value int, name string, root *dto.UUID) item.Item {
		return item.Item{ID: nextUpID(t, value), Kind: item.KindSeries, Name: nextUpString(name), ParentID: root, PresentationUniqueKey: nextUpString(name)}
	}
	visibleEpisode := func(value int, series item.Item, season, episode int) item.Item {
		result := nextUpEpisode(nextUpID(t, value), series.ID, *series.PresentationUniqueKey, season, episode)
		result.ParentID = &series.ID
		result.InheritedParentalRatingValue = nextUpInt(5)
		return result
	}

	folderDeniedSeries := allowedSeries(20_010, "folder-denied", &deniedRoot.ID)
	folderDeniedWatched, folderDeniedNext := visibleEpisode(20_011, folderDeniedSeries, 1, 1), visibleEpisode(20_012, folderDeniedSeries, 1, 2)
	untaggedSeries := plainSeries(20_020, "untagged", &allowedRoot.ID)
	untaggedWatched, untaggedNext := visibleEpisode(20_021, untaggedSeries, 1, 1), visibleEpisode(20_022, untaggedSeries, 1, 2)
	blockedSeries := allowedSeries(20_030, "blocked-series", &allowedRoot.ID)
	blockedSeries.Values.Tags = append(blockedSeries.Values.Tags, "blocked")
	blockedSeriesWatched, blockedSeriesNext := visibleEpisode(20_031, blockedSeries, 1, 1), visibleEpisode(20_032, blockedSeries, 1, 2)
	ratingSeries := allowedSeries(20_040, "rating-denied", &allowedRoot.ID)
	ratingDeniedWatched, ratingDeniedNext := visibleEpisode(20_041, ratingSeries, 1, 1), visibleEpisode(20_042, ratingSeries, 1, 2)
	ratingDeniedNext.InheritedParentalRatingValue = nextUpInt(11)
	unratedSeries := allowedSeries(20_050, "unrated-denied", &allowedRoot.ID)
	unratedWatched, unratedNext := visibleEpisode(20_051, unratedSeries, 1, 1), visibleEpisode(20_052, unratedSeries, 1, 2)
	unratedNext.InheritedParentalRatingValue = nil
	unrated := item.UnratedItemOther
	unratedNext.UnratedType = &unrated

	historySeries := allowedSeries(20_060, "history", &allowedRoot.ID)
	historyWatched, historyNext, historyDeniedPlayed := visibleEpisode(20_061, historySeries, 1, 1), visibleEpisode(20_062, historySeries, 1, 2), visibleEpisode(20_063, historySeries, 1, 3)
	historyDeniedPlayed.InheritedParentalRatingValue = nextUpInt(11)
	historyTail := visibleEpisode(20_064, historySeries, 1, 4)
	unplayedSeries := allowedSeries(20_070, "unplayed", &allowedRoot.ID)
	unplayedWatched, unplayedBlockedNext, unplayedNext := visibleEpisode(20_071, unplayedSeries, 1, 1), visibleEpisode(20_072, unplayedSeries, 1, 2), visibleEpisode(20_073, unplayedSeries, 1, 3)
	unplayedBlockedNext.Values.Tags = []string{"blocked"}
	specialSeries := allowedSeries(20_080, "special", &allowedRoot.ID)
	specialWatched := visibleEpisode(20_081, specialSeries, 1, 1)
	specialDenied := visibleEpisode(20_082, specialSeries, 0, 2)
	specialDenied.AirsBeforeSeasonNumber, specialDenied.AirsBeforeEpisodeNumber = nextUpInt(1), nextUpInt(2)
	specialDenied.Values.Tags = []string{"blocked"}
	specialRegularNext := visibleEpisode(20_083, specialSeries, 1, 2)
	resumeSeries := allowedSeries(20_090, "resume", &allowedRoot.ID)
	resumeWatched, resumeDenied, resumeNext := visibleEpisode(20_091, resumeSeries, 1, 1), visibleEpisode(20_092, resumeSeries, 1, 2), visibleEpisode(20_093, resumeSeries, 1, 3)
	resumeDenied.Values.Tags = []string{"blocked"}
	resumeDenied.PrimaryVersionID = &resumeNext.ID

	for _, value := range []item.Item{
		allowedRoot, deniedRoot, folderDeniedSeries, folderDeniedWatched, folderDeniedNext, untaggedSeries, untaggedWatched, untaggedNext,
		blockedSeries, blockedSeriesWatched, blockedSeriesNext, ratingSeries, ratingDeniedWatched, ratingDeniedNext, unratedSeries, unratedWatched, unratedNext,
		historySeries, historyWatched, historyNext, historyDeniedPlayed, historyTail, unplayedSeries, unplayedWatched, unplayedBlockedNext, unplayedNext,
		specialSeries, specialWatched, specialDenied, specialRegularNext, resumeSeries, resumeWatched, resumeDenied, resumeNext,
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert next up access fixture item %s: %v", value.ID, err)
		}
	}
	for _, data := range []struct {
		item       dto.UUID
		lastPlayed time.Time
	}{
		{folderDeniedWatched.ID, time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC)},
		{untaggedWatched.ID, time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC)},
		{blockedSeriesWatched.ID, time.Date(2026, time.July, 28, 0, 0, 0, 0, time.UTC)},
		{ratingDeniedWatched.ID, time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC)},
		{unratedWatched.ID, time.Date(2026, time.July, 26, 0, 0, 0, 0, time.UTC)},
		{historyWatched.ID, time.Date(2026, time.July, 25, 0, 0, 0, 0, time.UTC)},
		{historyDeniedPlayed.ID, time.Date(2026, time.July, 25, 1, 0, 0, 0, time.UTC)},
		{unplayedWatched.ID, time.Date(2026, time.July, 24, 0, 0, 0, 0, time.UTC)},
		{specialWatched.ID, time.Date(2026, time.July, 23, 0, 0, 0, 0, time.UTC)},
		{resumeWatched.ID, time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC)},
	} {
		if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: "next-up-access-" + data.item.String(), ItemID: data.item, Played: true, LastPlayed: &data.lastPlayed}); err != nil {
			t.Fatalf("mark next up access history for %s: %v", data.item, err)
		}
	}
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: "next-up-access-resume", ItemID: resumeDenied.ID, PositionTicks: 1}); err != nil {
		t.Fatalf("mark inaccessible resumed episode: %v", err)
	}
	return nextUpAccessFixture{
		items: items, userID: userID, allowedRoot: allowedRoot, folderDeniedSeries: folderDeniedSeries, unplayedSeries: unplayedSeries,
		folderDeniedNext: folderDeniedNext, untaggedNext: untaggedNext, blockedSeriesNext: blockedSeriesNext, ratingDeniedNext: ratingDeniedNext, unratedNext: unratedNext,
		historyNext: historyNext, historyTail: historyTail, unplayedBlockedNext: unplayedBlockedNext, unplayedNext: unplayedNext, specialDenied: specialDenied, specialRegularNext: specialRegularNext, resumeNext: resumeNext,
	}
}

func (f nextUpAccessFixture) restrictedUser() *store.User {
	return &store.User{ID: f.userID, Policy: []byte(`{"EnableAllFolders":false,"EnabledFolders":["` + f.allowedRoot.ID.String() + `"],"AllowedTags":["allowed"],"BlockedTags":["blocked"],"MaxParentalRating":10,"MaxParentalSubRating":0,"BlockUnratedItems":[8]}`)}
}

func (f nextUpAccessFixture) assertResult(t *testing.T, got store.NextUpResult, want []dto.UUID, total int) {
	t.Helper()
	if ids := nextUpIDs(got.Items); !reflect.DeepEqual(ids, want) || got.TotalRecordCount != total {
		t.Fatalf("next up IDs/count = %v/%d, want %v/%d", ids, got.TotalRecordCount, want, total)
	}
}

func nextUpAccessFolder(t *testing.T, value int, name string, parentID *dto.UUID) item.Item {
	t.Helper()
	return item.Item{ID: nextUpID(t, value), Kind: item.KindFolder, Name: nextUpString(name), ParentID: parentID, IsFolder: true}
}

// TestItemStoreNextUpChunksLargeHydrationAndSpecialPlayedLookupSQLite protects
// the SQLite variable ceiling in both of NextUp's large special paths. The
// fixture has 1,001 specials: their played-state lookup has a user binding as
// well as IDs, and hydration includes those specials plus watched/unplayed
// episodes. Pagination must not allow either query to skip batching.
func TestItemStoreNextUpChunksLargeHydrationAndSpecialPlayedLookupSQLite(t *testing.T) {
	recorder := &nextUpStatementRecorder{}
	items := openNextUpStatementRecordingSQLite(t, recorder)
	runItemStoreNextUpLargeHydrationAndSpecialPlayedLookup(t, items)

	statements := recorder.snapshot()
	var hydration, specialPlayed, maximumArguments int
	for _, statement := range statements {
		if statement.arguments > maximumArguments {
			maximumArguments = statement.arguments
		}
		if statement.arguments > 900 {
			t.Fatalf("NextUp reader statement has %d arguments, want at most SQLite-safe 900: %s", statement.arguments, statement.query)
		}
		if strings.Contains(statement.query, "SELECT item_id FROM user_data WHERE user_id =") {
			specialPlayed++
		}
		if strings.Contains(statement.query, "FROM items WHERE items.id IN (") {
			hydration++
		}
	}
	if hydration != 2 || specialPlayed != 2 || maximumArguments != 900 {
		t.Fatalf("NextUp bounded statements = hydration:%d special-played:%d maximum-arguments:%d, want 2/2/900", hydration, specialPlayed, maximumArguments)
	}
	// Selection reads item columns only. Satellites load for the one-item
	// page, not for the 1,003 candidates: every satellite statement binds
	// one item ID.
	satellites := 0
	for _, statement := range statements {
		if !strings.Contains(statement.query, "FROM people_map") && !strings.Contains(statement.query, "FROM provider_ids") {
			continue
		}
		satellites++
		if statement.arguments != 1 {
			t.Fatalf("NextUp satellite statement binds %d arguments, want 1 for the one-item page: %s", statement.arguments, statement.query)
		}
	}
	if satellites != 2 {
		t.Fatalf("NextUp satellite statements = %d, want people and providers once each", satellites)
	}
}

// TestItemStoreNextUpChunksLargeHydrationAndSpecialPlayedLookupPostgres runs
// the same public behavior against PostgreSQL when DEADFIN_TEST_POSTGRES_DSN
// is configured. SQLite-specific argument instrumentation belongs above.
func TestItemStoreNextUpChunksLargeHydrationAndSpecialPlayedLookupPostgres(t *testing.T) {
	runItemStoreNextUpLargeHydrationAndSpecialPlayedLookup(t, openPostgresItemStore(t))
}

func runItemStoreNextUpLargeHydrationAndSpecialPlayedLookup(t *testing.T, items *store.ItemStore) {
	t.Helper()
	fixture := seedNextUpLargeHydrationAndSpecialPlayedLookup(t, items)
	start, limit := 0, 1
	result, err := items.NextUp(t.Context(), store.NextUpQuery{
		UserID:                 fixture.userID,
		IncludeSpecials:        true,
		EnableTotalRecordCount: true,
		StartIndex:             &start,
		Limit:                  &limit,
	})
	if err != nil {
		t.Fatalf("select next up with 1,001 specials: %v", err)
	}
	if got, want := nextUpIDs(result.Items), []dto.UUID{fixture.expectedID}; !reflect.DeepEqual(got, want) || result.TotalRecordCount != 1 {
		t.Fatalf("large next up page/count = %v/%d, want %v/1", got, result.TotalRecordCount, want)
	}
	if got, _ := result.Items[0].ProviderIDs.Get("Tvdb"); got != "next-up-expected" {
		t.Fatalf("next up page item Tvdb provider = %q, want its stored satellite", got)
	}
}

type nextUpLargeFixture struct {
	userID, expectedID dto.UUID
}

func seedNextUpLargeHydrationAndSpecialPlayedLookup(t *testing.T, items *store.ItemStore) nextUpLargeFixture {
	t.Helper()
	const specialCount = 1_001
	userID, seriesID := nextUpID(t, 30_000), nextUpID(t, 30_001)
	key := "next-up-large-specials"
	if err := items.Upsert(t.Context(), item.Item{ID: seriesID, Kind: item.KindSeries, PresentationUniqueKey: &key}); err != nil {
		t.Fatalf("upsert large next up series: %v", err)
	}

	watched := nextUpEpisode(nextUpID(t, 30_002), seriesID, key, 1, 1)
	watched.ParentID = &seriesID
	next := nextUpEpisode(nextUpID(t, 30_003), seriesID, key, 1, 2)
	next.ParentID = &seriesID
	children := make([]item.Item, 0, specialCount+2)
	children = append(children, watched, next)
	var expectedID dto.UUID
	for index := range specialCount {
		special := nextUpEpisode(nextUpID(t, 30_010+index), seriesID, key, 0, index+1)
		special.ParentID = &seriesID
		special.AirsBeforeSeasonNumber = nextUpInt(1)
		special.AirsBeforeEpisodeNumber = nextUpInt(2)
		if index == 1 {
			expectedID = special.ID
			special.ProviderIDs.Set("Tvdb", "next-up-expected")
		}
		children = append(children, special)
	}
	if _, err := items.ReconcileChildren(t.Context(), seriesID, children); err != nil {
		t.Fatalf("batch seed watched, unplayed, and %d special episodes: %v", specialCount, err)
	}
	lastPlayed := time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC)
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: "next-up-large-watched", ItemID: watched.ID, Played: true, LastPlayed: &lastPlayed}); err != nil {
		t.Fatalf("mark regular episode watched: %v", err)
	}
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: "next-up-large-special-played", ItemID: children[2].ID, Played: true}); err != nil {
		t.Fatalf("mark first special played: %v", err)
	}
	return nextUpLargeFixture{userID: userID, expectedID: expectedID}
}

type nextUpStatement struct {
	query     string
	arguments int
}

type nextUpStatementRecorder struct {
	mu         sync.Mutex
	statements []nextUpStatement
}

func (r *nextUpStatementRecorder) record(query string, arguments int) {
	r.mu.Lock()
	r.statements = append(r.statements, nextUpStatement{query: query, arguments: arguments})
	r.mu.Unlock()
}

func (r *nextUpStatementRecorder) snapshot() []nextUpStatement {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]nextUpStatement(nil), r.statements...)
}

var (
	nextUpStatementDriverRegistration sync.Once
	nextUpStatementRecorders          = struct {
		sync.Mutex
		byDSN map[string]*nextUpStatementRecorder
	}{byDSN: make(map[string]*nextUpStatementRecorder)}
)

func openNextUpStatementRecordingSQLite(t *testing.T, recorder *nextUpStatementRecorder) *store.ItemStore {
	t.Helper()
	nextUpStatementDriverRegistration.Do(func() {
		sql.Register("storetest-nextup-statement-recording-sqlite", nextUpStatementDriver{driver: &moderncsqlite.Driver{}})
	})
	path := filepath.Join(t.TempDir(), "nextup-statement-recording.db")
	writer, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open NextUp statement fixture writer: %v", err)
	}
	readerDSN := "file:" + path + "?_pragma=foreign_keys(ON)&_pragma=query_only(ON)"
	nextUpStatementRecorders.Lock()
	nextUpStatementRecorders.byDSN[readerDSN] = recorder
	nextUpStatementRecorders.Unlock()
	reader, err := sql.Open("storetest-nextup-statement-recording-sqlite", readerDSN)
	if err != nil {
		nextUpStatementRecorders.Lock()
		delete(nextUpStatementRecorders.byDSN, readerDSN)
		nextUpStatementRecorders.Unlock()
		_ = writer.Close()
		t.Fatalf("open NextUp statement fixture reader: %v", err)
	}
	stores := satelliteQueryCountingStores{writer: writer, reader: reader}
	if err := migrate.Up(t.Context(), writer, migrate.SQLite); err != nil {
		_ = stores.Close()
		_ = writer.Close()
		t.Fatalf("migrate NextUp statement fixture: %v", err)
	}
	if err := store.BackfillMissingItemUserDataKeys(t.Context(), stores); err != nil {
		_ = stores.Close()
		_ = writer.Close()
		t.Fatalf("backfill NextUp statement fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close NextUp statement fixture reader: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Errorf("close NextUp statement fixture writer: %v", err)
		}
		nextUpStatementRecorders.Lock()
		delete(nextUpStatementRecorders.byDSN, readerDSN)
		nextUpStatementRecorders.Unlock()
	})
	return store.NewItemStore(stores)
}

type nextUpStatementDriver struct{ driver driver.Driver }

func (d nextUpStatementDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.driver.Open(name)
	if err != nil {
		return nil, err
	}
	nextUpStatementRecorders.Lock()
	recorder := nextUpStatementRecorders.byDSN[name]
	nextUpStatementRecorders.Unlock()
	if recorder == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("NextUp statement recorder not configured for SQLite connection")
	}
	return nextUpStatementConn{Conn: conn, recorder: recorder}, nil
}

type nextUpStatementConn struct {
	driver.Conn
	recorder *nextUpStatementRecorder
}

func (c nextUpStatementConn) QueryContext(ctx context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	c.recorder.record(query, len(arguments))
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, arguments)
}

func (c nextUpStatementConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	return beginner.BeginTx(ctx, options)
}

// TestItemStoreNextUpChunksSeriesKeyRelationsSQLite exercises every
// series-key-expanded NextUp relation past SQLite's conservative bind ceiling.
// Its PostgreSQL twin runs the same public contract against the live backend
// when configured, so PostgreSQL placeholder and UUID behavior cannot diverge.
func TestItemStoreNextUpChunksSeriesKeyRelationsSQLite(t *testing.T) {
	runItemStoreNextUpSeriesKeyBatching(t, openItemStore)
}

func TestItemStoreNextUpChunksSeriesKeyRelationsPostgres(t *testing.T) {
	runItemStoreNextUpSeriesKeyBatching(t, openPostgresItemStore)
}

func runItemStoreNextUpSeriesKeyBatching(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	fixture := seedNextUpSeriesKeyBatchFixture(t, open(t))
	query := store.NextUpQuery{
		User:                   &fixture.restrictedUser,
		UserID:                 fixture.userID,
		EnableResumable:        true,
		IncludeSpecials:        true,
		EnableTotalRecordCount: true,
	}

	result, err := fixture.items.NextUp(t.Context(), query)
	if err != nil {
		t.Fatalf("select %d batched restricted NextUp series: %v", nextUpSeriesKeyBatchCount, err)
	}
	fixture.assertPage(t, result, fixture.restrictedExpected, len(fixture.restrictedExpected))
	assertNextUpUniqueIDs(t, result.Items)

	start, limit := 896, 5
	query.StartIndex, query.Limit = &start, &limit
	page, err := fixture.items.NextUp(t.Context(), query)
	if err != nil {
		t.Fatalf("page batched restricted NextUp series: %v", err)
	}
	fixture.assertPage(t, page, fixture.restrictedExpected[start:start+limit], len(fixture.restrictedExpected))
	assertNextUpUniqueIDs(t, page.Items)

	adminQuery := store.NextUpQuery{
		User:                   &fixture.administrator,
		UserID:                 fixture.userID,
		EnableResumable:        true,
		IncludeSpecials:        true,
		EnableTotalRecordCount: true,
	}
	admin, err := fixture.items.NextUp(t.Context(), adminQuery)
	if err != nil {
		t.Fatalf("select batched administrator NextUp series: %v", err)
	}
	fixture.assertPage(t, admin, fixture.administratorExpected, len(fixture.administratorExpected))
	assertNextUpUniqueIDs(t, admin.Items)
}

const nextUpSeriesKeyBatchCount = 901

type nextUpSeriesKeyBatchFixture struct {
	items                 *store.ItemStore
	userID                dto.UUID
	restrictedUser        store.User
	administrator         store.User
	restrictedExpected    []dto.UUID
	administratorExpected []dto.UUID
}

func seedNextUpSeriesKeyBatchFixture(t *testing.T, items *store.ItemStore) nextUpSeriesKeyBatchFixture {
	t.Helper()
	userID := nextUpID(t, 40_000)
	allowedRoot := nextUpAccessFolder(t, 40_001, "next-up-batched-allowed", nil)
	deniedRoot := nextUpAccessFolder(t, 40_002, "next-up-batched-denied", nil)
	for _, root := range []item.Item{allowedRoot, deniedRoot} {
		if err := items.Upsert(t.Context(), root); err != nil {
			t.Fatalf("upsert NextUp batch root %s: %v", root.ID, err)
		}
	}

	base := time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC)
	children := make([]item.Item, 0, nextUpSeriesKeyBatchCount*2+2)
	played := make([]store.UserData, 0, nextUpSeriesKeyBatchCount+1)
	candidates := make([]dto.UUID, nextUpSeriesKeyBatchCount)
	for index := range nextUpSeriesKeyBatchCount {
		series := nextUpID(t, 41_000+index)
		key := fmt.Sprintf("next-up-series-key-%04d", index)
		watched := nextUpEpisode(nextUpID(t, 50_000+index*3), series, key, 1, 1)
		next := nextUpEpisode(nextUpID(t, 50_001+index*3), series, key, 1, 2)
		watched.ParentID = &allowedRoot.ID
		next.ParentID = &allowedRoot.ID
		children = append(children, watched, next)
		lastPlayed := base.Add(time.Duration(index) * time.Minute)
		played = append(played, store.UserData{UserID: userID, CustomKey: fmt.Sprintf("next-up-series-key-watched-%04d", index), ItemID: watched.ID, Played: true, LastPlayed: &lastPlayed})
		candidates[index] = next.ID

		switch index {
		case 0:
			// A played row with no timestamp remains watched: E3, not E2, is
			// selected. The timestamped E1 keeps this series in the date-keyed
			// NextUp relation while exercising nullable MAX(last_played) scanning.
			following := nextUpEpisode(nextUpID(t, 60_000), series, key, 1, 3)
			following.ParentID = &allowedRoot.ID
			children = append(children, following)
			played = append(played, store.UserData{UserID: userID, CustomKey: "next-up-series-key-played-null", ItemID: next.ID, Played: true})
			candidates[index] = following.ID
		case nextUpSeriesKeyBatchCount - 1:
			// The positioned special is selected ahead of the regular next episode.
			special := nextUpEpisode(nextUpID(t, 60_001), series, key, 0, 1)
			special.ParentID = &allowedRoot.ID
			special.AirsBeforeSeasonNumber = nextUpInt(1)
			special.AirsBeforeEpisodeNumber = nextUpInt(2)
			children = append(children, special)
			candidates[index] = special.ID
		case nextUpSeriesKeyBatchCount - 2:
			// A resumed next episode remains selected only because this query enables it.
			played = append(played, store.UserData{UserID: userID, CustomKey: "next-up-series-key-resumed", ItemID: next.ID, PositionTicks: 1})
		}
	}
	if _, err := items.ReconcileChildren(t.Context(), allowedRoot.ID, children); err != nil {
		t.Fatalf("reconcile %d batched NextUp series: %v", nextUpSeriesKeyBatchCount, err)
	}
	for _, data := range played {
		if err := items.UpsertUserData(t.Context(), data); err != nil {
			t.Fatalf("upsert NextUp batch user data %q: %v", data.CustomKey, err)
		}
	}

	deniedSeries := nextUpID(t, 70_000)
	deniedKey := "next-up-series-key-denied"
	deniedWatched := nextUpEpisode(nextUpID(t, 70_001), deniedSeries, deniedKey, 1, 1)
	deniedNext := nextUpEpisode(nextUpID(t, 70_002), deniedSeries, deniedKey, 1, 2)
	deniedWatched.ParentID = &deniedRoot.ID
	deniedNext.ParentID = &deniedRoot.ID
	if _, err := items.ReconcileChildren(t.Context(), deniedRoot.ID, []item.Item{deniedWatched, deniedNext}); err != nil {
		t.Fatalf("reconcile denied NextUp batch series: %v", err)
	}
	deniedPlayed := base.Add(time.Duration(nextUpSeriesKeyBatchCount+1) * time.Minute)
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: "next-up-series-key-denied-watched", ItemID: deniedWatched.ID, Played: true, LastPlayed: &deniedPlayed}); err != nil {
		t.Fatalf("upsert denied NextUp batch user data: %v", err)
	}

	restrictedExpected := make([]dto.UUID, 0, nextUpSeriesKeyBatchCount)
	for index := nextUpSeriesKeyBatchCount - 1; index >= 0; index-- {
		restrictedExpected = append(restrictedExpected, candidates[index])
	}
	administratorExpected := make([]dto.UUID, 0, len(restrictedExpected)+1)
	administratorExpected = append(administratorExpected, deniedNext.ID)
	administratorExpected = append(administratorExpected, restrictedExpected...)
	return nextUpSeriesKeyBatchFixture{
		items:                 items,
		userID:                userID,
		restrictedUser:        store.User{ID: userID, Policy: []byte(`{"EnableAllFolders":false,"EnabledFolders":["` + allowedRoot.ID.String() + `"]}`)},
		administrator:         store.User{ID: userID, Policy: []byte(`{"IsAdministrator":true,"EnableAllFolders":false,"EnabledFolders":["` + allowedRoot.ID.String() + `"]}`)},
		restrictedExpected:    restrictedExpected,
		administratorExpected: administratorExpected,
	}
}

func (f nextUpSeriesKeyBatchFixture) assertPage(t *testing.T, got store.NextUpResult, want []dto.UUID, total int) {
	t.Helper()
	if ids := nextUpIDs(got.Items); !reflect.DeepEqual(ids, want) || got.TotalRecordCount != total {
		t.Fatalf("batched NextUp IDs/count = %v/%d, want %v/%d", ids, got.TotalRecordCount, want, total)
	}
}

func assertNextUpUniqueIDs(t *testing.T, values []item.Item) {
	t.Helper()
	seen := make(map[dto.UUID]struct{}, len(values))
	for _, value := range values {
		if _, found := seen[value.ID]; found {
			t.Fatalf("NextUp emitted duplicate item %s in %d candidates", value.ID, len(values))
		}
		seen[value.ID] = struct{}{}
	}
}

func openNextUpItemStore(t *testing.T) *store.ItemStore {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open next up store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close next up store: %v", err)
		}
	})
	return store.NewItemStore(stores)
}

func nextUpEpisode(id, series dto.UUID, key string, season, episode int) item.Item {
	return item.Item{ID: id, Kind: item.KindEpisode, SeriesID: &series, SeriesPresentationUniqueKey: &key, ParentIndexNumber: &season, IndexNumber: &episode}
}

func nextUpID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse next up ID: %v", err)
	}
	return id
}

func nextUpString(value string) *string { return &value }
func nextUpInt(value int) *int          { return &value }

func nextUpIDs(values []item.Item) []dto.UUID {
	out := make([]dto.UUID, len(values))
	for index := range values {
		out[index] = values[index].ID
	}
	return out
}
