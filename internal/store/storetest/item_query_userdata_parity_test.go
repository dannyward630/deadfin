// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryUserDataParitySQLite defends user-data filtering through
// the SQLite public store boundary.
func TestItemStoreQueryUserDataParitySQLite(t *testing.T) {
	runItemStoreQueryUserDataParity(t, openItemStore)
}

// TestItemStoreQueryUserDataParityPostgres runs the same externally observable
// contracts against PostgreSQL when its opt-in integration DSN is available.
func TestItemStoreQueryUserDataParityPostgres(t *testing.T) {
	runItemStoreQueryUserDataParity(t, openPostgresItemStore)
}

func runItemStoreQueryUserDataParity(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	fixture := newItemQueryUserDataFixture(t, open(t))
	played, unplayed := true, false
	resumable, notResumable := true, false
	favoriteOrLiked := true
	minSaved := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	t.Run("series and box sets are played only when every nonvirtual leaf is played", func(t *testing.T) {
		got := fixture.query(t, store.ItemQuery{
			UserID:           &fixture.userID,
			IncludeItemTypes: []item.Kind{item.KindSeries, item.KindBoxSet},
			IsPlayed:         &played,
		})
		fixture.assertResult(t, got, []dto.UUID{fixture.seriesAll.ID, fixture.boxSetAll.ID})
	})

	t.Run("series and box set unplayed state is the exact aggregate complement", func(t *testing.T) {
		got := fixture.query(t, store.ItemQuery{
			UserID:           &fixture.userID,
			IncludeItemTypes: []item.Kind{item.KindSeries, item.KindBoxSet},
			IsPlayed:         &unplayed,
		})
		fixture.assertResult(t, got, []dto.UUID{fixture.seriesPartial.ID, fixture.boxSetPartial.ID})
	})

	t.Run("a partially watched series is resumable without a leaf position", func(t *testing.T) {
		got := fixture.query(t, store.ItemQuery{
			UserID:           &fixture.userID,
			IncludeItemTypes: []item.Kind{item.KindSeries},
			IsResumable:      &resumable,
		})
		fixture.assertResult(t, got, []dto.UUID{fixture.seriesPartial.ID})
	})

	t.Run("resumable alternate versions select the latest state and resolve equal timestamps by ID", func(t *testing.T) {
		got := fixture.query(t, store.ItemQuery{
			UserID:           &fixture.userID,
			IncludeItemTypes: []item.Kind{item.KindMovie},
			IsResumable:      &resumable,
		})
		fixture.assertResult(t, got, []dto.UUID{fixture.alternateFirst.ID, fixture.nullAlternate.ID})
	})

	t.Run("resumable alternate versions select the non-NULL last_played over a NULL timestamp", func(t *testing.T) {
		got := fixture.query(t, store.ItemQuery{
			UserID:      &fixture.userID,
			ItemIDs:     []dto.UUID{fixture.nullPrimary.ID, fixture.nullAlternate.ID},
			IsResumable: &resumable,
		})
		fixture.assertResult(t, got, []dto.UUID{fixture.nullAlternate.ID})
	})

	t.Run("not resumable queries retain only eligible primaries", func(t *testing.T) {
		got := fixture.query(t, store.ItemQuery{
			UserID:      &fixture.userID,
			ItemIDs:     []dto.UUID{fixture.alternatePrimary.ID, fixture.alternateFirst.ID, fixture.alternateSecond.ID, fixture.idlePrimary.ID, fixture.idleAlternate.ID},
			IsResumable: &notResumable,
		})
		fixture.assertResult(t, got, []dto.UUID{fixture.idlePrimary.ID})
	})

	t.Run("ordinary item state uses positive facts and canonical shared keys", func(t *testing.T) {
		got := fixture.query(t, store.ItemQuery{
			UserID:   &fixture.userID,
			ItemIDs:  []dto.UUID{fixture.moviePlayed.ID, fixture.audioUnplayed.ID, fixture.sharedCandidate.ID},
			IsPlayed: &unplayed,
		})
		fixture.assertResult(t, got, []dto.UUID{fixture.audioUnplayed.ID})
	})

	t.Run("favorite or liked follows the favorite branch rather than rating alone", func(t *testing.T) {
		got := fixture.query(t, store.ItemQuery{
			UserID:            &fixture.userID,
			ItemIDs:           []dto.UUID{fixture.favorite.ID, fixture.highRated.ID},
			IsFavoriteOrLiked: &favoriteOrLiked,
		})
		fixture.assertResult(t, got, []dto.UUID{fixture.favorite.ID})
	})

	t.Run("minimum saved date is inclusive and still requires user context", func(t *testing.T) {
		got := fixture.query(t, store.ItemQuery{
			UserID:                  &fixture.userID,
			ItemIDs:                 []dto.UUID{fixture.savedBefore.ID, fixture.savedAtBoundary.ID},
			MinDateLastSavedForUser: &minSaved,
		})
		fixture.assertResult(t, got, []dto.UUID{fixture.savedAtBoundary.ID})
	})
}

type itemQueryUserDataFixture struct {
	items            *store.ItemStore
	userID           dto.UUID
	seriesAll        item.Item
	seriesPartial    item.Item
	boxSetAll        item.Item
	boxSetPartial    item.Item
	alternatePrimary item.Item
	alternateFirst   item.Item
	alternateSecond  item.Item
	idlePrimary      item.Item
	idleAlternate    item.Item
	nullPrimary      item.Item
	nullAlternate    item.Item
	moviePlayed      item.Item
	audioUnplayed    item.Item
	sharedCandidate  item.Item
	favorite         item.Item
	highRated        item.Item
	savedBefore      item.Item
	savedAtBoundary  item.Item
}

func newItemQueryUserDataFixture(t *testing.T, items *store.ItemStore) itemQueryUserDataFixture {
	t.Helper()
	userID := itemQueryUserDataStoreUUID(t, 100)
	// Equal SortName and Name make ID the observable final ordering tie breaker.
	aggregateName := "same aggregate"
	seriesAll := itemQueryUserDataItem(t, 1, item.KindSeries)
	seriesAll.IsFolder = true
	seriesPartial := itemQueryUserDataItem(t, 4, item.KindSeries)
	seriesPartial.IsFolder = true
	seriesAll.Name = &aggregateName
	seriesPartial.Name = &aggregateName

	seriesAllFirst := itemQueryUserDataItem(t, 2, item.KindEpisode)
	seriesAllFirst.SeriesID = &seriesAll.ID
	seriesAllFirst.ParentID = &seriesAll.ID
	seriesAllSecond := itemQueryUserDataItem(t, 3, item.KindEpisode)
	seriesAllSecond.SeriesID = &seriesAll.ID
	seriesAllSecond.ParentID = &seriesAll.ID
	seriesPartialPlayed := itemQueryUserDataItem(t, 5, item.KindEpisode)
	seriesPartialPlayed.SeriesID = &seriesPartial.ID
	seriesPartialPlayed.ParentID = &seriesPartial.ID
	seriesPartialUnplayed := itemQueryUserDataItem(t, 6, item.KindEpisode)
	seriesPartialUnplayed.SeriesID = &seriesPartial.ID
	seriesPartialUnplayed.ParentID = &seriesPartial.ID

	boxSetAllFirst := itemQueryUserDataItem(t, 8, item.KindMovie)
	boxSetAllSecond := itemQueryUserDataItem(t, 9, item.KindAudio)
	boxSetPartialFirst := itemQueryUserDataItem(t, 11, item.KindMovie)
	boxSetPartialSecond := itemQueryUserDataItem(t, 12, item.KindAudio)
	boxSetAll := itemQueryUserDataItem(t, 7, item.KindBoxSet)
	boxSetAll.IsFolder = true
	boxSetAll.LinkedChildren = []item.LinkedChildRef{
		{ItemID: boxSetAllFirst.ID, Type: item.LinkedChildTypeManual},
		{ItemID: boxSetAllSecond.ID, Type: item.LinkedChildTypeManual},
	}
	boxSetPartial := itemQueryUserDataItem(t, 10, item.KindBoxSet)
	boxSetPartial.IsFolder = true
	boxSetAll.Name = &aggregateName
	boxSetPartial.Name = &aggregateName
	boxSetPartial.LinkedChildren = []item.LinkedChildRef{
		{ItemID: boxSetPartialFirst.ID, Type: item.LinkedChildTypeManual},
		{ItemID: boxSetPartialSecond.ID, Type: item.LinkedChildTypeManual},
	}

	moviePlayed := itemQueryUserDataItem(t, 13, item.KindMovie)
	audioUnplayed := itemQueryUserDataItem(t, 14, item.KindAudio)
	sharedSource := itemQueryUserDataItem(t, 15, item.KindMovie)
	sharedSource.ProviderIDs.Set("Tmdb", "shared-user-data")
	sharedCandidate := itemQueryUserDataItem(t, 16, item.KindMovie)
	sharedCandidate.ProviderIDs.Set("Tmdb", "shared-user-data")
	favorite := itemQueryUserDataItem(t, 17, item.KindMovie)
	highRated := itemQueryUserDataItem(t, 18, item.KindMovie)
	savedBefore := itemQueryUserDataItem(t, 19, item.KindMovie)
	before := time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC)
	savedBefore.DateLastSaved = &before
	savedAtBoundary := itemQueryUserDataItem(t, 20, item.KindMovie)
	atBoundary := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	savedAtBoundary.DateLastSaved = &atBoundary

	alternatePrimary := itemQueryUserDataItem(t, 21, item.KindMovie)
	alternateFirst := itemQueryUserDataItem(t, 22, item.KindMovie)
	alternateFirst.PrimaryVersionID = &alternatePrimary.ID
	alternateSecond := itemQueryUserDataItem(t, 23, item.KindMovie)
	alternateSecond.PrimaryVersionID = &alternatePrimary.ID
	idlePrimary := itemQueryUserDataItem(t, 24, item.KindMovie)
	idleAlternate := itemQueryUserDataItem(t, 25, item.KindMovie)
	idleAlternate.PrimaryVersionID = &idlePrimary.ID
	nullPrimary := itemQueryUserDataItem(t, 26, item.KindMovie)
	nullAlternate := itemQueryUserDataItem(t, 27, item.KindMovie)
	nullAlternate.PrimaryVersionID = &nullPrimary.ID

	for _, it := range []item.Item{
		seriesAll, seriesPartial,
		seriesAllFirst, seriesAllSecond, seriesPartialPlayed, seriesPartialUnplayed,
		boxSetAllFirst, boxSetAllSecond, boxSetPartialFirst, boxSetPartialSecond,
		boxSetAll, boxSetPartial,
		moviePlayed, audioUnplayed, sharedSource, sharedCandidate, favorite, highRated, savedBefore, savedAtBoundary,
		alternatePrimary, alternateFirst, alternateSecond, idlePrimary, idleAlternate, nullPrimary, nullAlternate,
	} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting user-data fixture item %s: %v", it.ID, err)
		}
	}

	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, seriesAllFirst), ItemID: seriesAllFirst.ID, Played: true})
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, seriesAllSecond), ItemID: seriesAllSecond.ID, Played: true})
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, seriesPartialPlayed), ItemID: seriesPartialPlayed.ID, Played: true})
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, boxSetAllFirst), ItemID: boxSetAllFirst.ID, Played: true})
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, boxSetAllSecond), ItemID: boxSetAllSecond.ID, Played: true})
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, boxSetPartialFirst), ItemID: boxSetPartialFirst.ID, Played: true})
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, moviePlayed), ItemID: moviePlayed.ID, Played: true})
	sharedKey := itemQuerySharedUserDataKey(t, sharedSource, sharedCandidate)
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: sharedKey, ItemID: sharedSource.ID, Played: true})
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, favorite), ItemID: favorite.ID, IsFavorite: true})
	rating := float32(8)
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, highRated), ItemID: highRated.ID, Rating: &rating})
	firstPlayed := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	tiedPlayed := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, alternatePrimary), ItemID: alternatePrimary.ID, PositionTicks: 1, LastPlayed: &firstPlayed})
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, alternateFirst), ItemID: alternateFirst.ID, PositionTicks: 1, LastPlayed: &tiedPlayed})
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, alternateSecond), ItemID: alternateSecond.ID, PositionTicks: 1, LastPlayed: &tiedPlayed})
	// nullPrimary is in-progress with no last_played (NULL timestamp).
	// nullAlternate is in-progress with a concrete last_played. The winner
	// must be nullAlternate because a NULL timestamp sorts last under
	// DESC NULLS LAST, so the non-NULL row has the greatest last_played.
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, nullPrimary), ItemID: nullPrimary.ID, PositionTicks: 1})
	nullAlternatePlayed := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	itemQueryUserDataUpsert(t, items, store.UserData{UserID: userID, CustomKey: itemQueryUserDataKey(t, nullAlternate), ItemID: nullAlternate.ID, PositionTicks: 1, LastPlayed: &nullAlternatePlayed})

	return itemQueryUserDataFixture{
		items: items, userID: userID,
		seriesAll: seriesAll, seriesPartial: seriesPartial, boxSetAll: boxSetAll, boxSetPartial: boxSetPartial,
		alternatePrimary: alternatePrimary, alternateFirst: alternateFirst, alternateSecond: alternateSecond, idlePrimary: idlePrimary, idleAlternate: idleAlternate, nullPrimary: nullPrimary, nullAlternate: nullAlternate,
		moviePlayed: moviePlayed, audioUnplayed: audioUnplayed, sharedCandidate: sharedCandidate, favorite: favorite, highRated: highRated, savedBefore: savedBefore, savedAtBoundary: savedAtBoundary,
	}
}

func (f itemQueryUserDataFixture) query(t *testing.T, query store.ItemQuery) store.ItemQueryResult {
	t.Helper()
	query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
	query.SortOrder = []store.SortOrder{store.SortOrderAscending}
	query.EnableTotalRecordCount = true
	got, err := f.items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("querying user-data fixture: %v", err)
	}
	return got
}

func (f itemQueryUserDataFixture) assertResult(t *testing.T, got store.ItemQueryResult, want []dto.UUID) {
	t.Helper()
	ids := make([]dto.UUID, len(got.Items))
	for i, it := range got.Items {
		ids[i] = it.ID
	}
	if !reflect.DeepEqual(ids, want) || got.TotalRecordCount != len(want) {
		t.Fatalf("query IDs = %v (count %d), want %v (count %d)", ids, got.TotalRecordCount, want, len(want))
	}
}

func itemQueryUserDataItem(t *testing.T, value int, kind item.Kind) item.Item {
	t.Helper()
	name := fmt.Sprintf("item %d", value)
	sortName := "same"
	return item.Item{ID: itemQueryUserDataStoreUUID(t, value), Kind: kind, Name: &name, SortName: &sortName}
}

func itemQueryUserDataStoreUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing user-data fixture UUID %d: %v", value, err)
	}
	return id
}

func itemQueryUserDataKey(t *testing.T, it item.Item) string {
	t.Helper()
	keys := item.UserDataKeys(it, item.UserDataInputs{})
	if len(keys) == 0 {
		t.Fatalf("user-data keys for %s are empty", it.ID)
	}
	return keys[0]
}

func itemQuerySharedUserDataKey(t *testing.T, source, candidate item.Item) string {
	t.Helper()
	candidateKeys := make(map[string]struct{})
	for _, key := range item.UserDataKeys(candidate, item.UserDataInputs{}) {
		candidateKeys[key] = struct{}{}
	}
	for _, key := range item.UserDataKeys(source, item.UserDataInputs{}) {
		if _, ok := candidateKeys[key]; ok {
			return key
		}
	}
	t.Fatalf("user-data keys for %s and %s have no shared canonical key", source.ID, candidate.ID)
	return ""
}

func itemQueryUserDataUpsert(t *testing.T, items *store.ItemStore, data store.UserData) {
	t.Helper()
	if err := items.UpsertUserData(t.Context(), data); err != nil {
		t.Fatalf("upserting user data %q: %v", data.CustomKey, err)
	}
}
