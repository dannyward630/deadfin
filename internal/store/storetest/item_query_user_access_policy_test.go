// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemStoreQueryFullUserAccessPolicySQLite verifies that ItemQuery applies
// a user's library and content policy to the selected-ID relation. The PostgreSQL
// twin exercises the same public store contract when DEADFIN_TEST_POSTGRES_DSN
// is available.
func TestItemStoreQueryFullUserAccessPolicySQLite(t *testing.T) {
	runItemStoreQueryFullUserAccessPolicy(t, openItemStore)
}

func TestItemStoreQueryFullUserAccessPolicyPostgres(t *testing.T) {
	runItemStoreQueryFullUserAccessPolicy(t, openPostgresItemStore)
}

func runItemStoreQueryFullUserAccessPolicy(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	fixture := newFullUserAccessFixture(t, open(t))

	t.Run("enabled root admits its descendants and rejects another root globally recursively and by explicit ID", func(t *testing.T) {
		user := fixture.user(`{"EnableAllFolders":false,"EnabledFolders":["` + fixture.rootA.ID.String() + `"]}`)
		fixture.assertQuery(t, store.ItemQuery{
			User:                   user,
			ItemIDs:                []dto.UUID{fixture.directAllowed.ID, fixture.deniedRoot.ID},
			IncludeItemTypes:       []item.Kind{item.KindMovie},
			EnableTotalRecordCount: true,
		}, []dto.UUID{fixture.directAllowed.ID}, 1)
		fixture.assertQuery(t, store.ItemQuery{
			User:                   user,
			ParentID:               &fixture.rootB.ID,
			Recursive:              true,
			IncludeItemTypes:       []item.Kind{item.KindMovie},
			EnableTotalRecordCount: true,
		}, []dto.UUID{}, 0)
	})

	t.Run("blocked tags exclude direct and inherited matches", func(t *testing.T) {
		user := fixture.user(`{"EnableAllFolders":true,"BlockedTags":["blocked"]}`)
		fixture.assertQuery(t, store.ItemQuery{
			User:                   user,
			ItemIDs:                []dto.UUID{fixture.directAllowed.ID, fixture.directBlocked.ID, fixture.inheritedBlocked.ID},
			IncludeItemTypes:       []item.Kind{item.KindMovie},
			EnableTotalRecordCount: true,
		}, []dto.UUID{fixture.directAllowed.ID}, 1)
	})

	t.Run("allowed tags admit direct and inherited matches but reject untagged content", func(t *testing.T) {
		user := fixture.user(`{"EnableAllFolders":true,"AllowedTags":["allowed"]}`)
		fixture.assertQuery(t, store.ItemQuery{
			User:                   user,
			ItemIDs:                []dto.UUID{fixture.directAllowed.ID, fixture.inheritedAllowed.ID, fixture.untagged.ID},
			IncludeItemTypes:       []item.Kind{item.KindMovie},
			EnableTotalRecordCount: true,
		}, []dto.UUID{fixture.directAllowed.ID, fixture.inheritedAllowed.ID}, 2)
	})

	t.Run("parental maximum includes its boundary and excludes greater ratings", func(t *testing.T) {
		user := fixture.user(`{"EnableAllFolders":true,"MaxParentalRating":10,"MaxParentalSubRating":0}`)
		fixture.assertQuery(t, store.ItemQuery{
			User:                   user,
			ItemIDs:                []dto.UUID{fixture.ratingBelow.ID, fixture.ratingAtBoundary.ID, fixture.ratingAbove.ID},
			IncludeItemTypes:       []item.Kind{item.KindMovie},
			EnableTotalRecordCount: true,
		}, []dto.UUID{fixture.ratingBelow.ID, fixture.ratingAtBoundary.ID}, 2)
	})

	t.Run("blocked concrete unrated classes reject while NULL classifications pass", func(t *testing.T) {
		user := fixture.user(`{"EnableAllFolders":true,"BlockUnratedItems":[0]}`)
		fixture.assertQuery(t, store.ItemQuery{
			User:                   user,
			ItemIDs:                []dto.UUID{fixture.directAllowed.ID, fixture.unratedMovie.ID, fixture.unclassifiedMovie.ID},
			IncludeItemTypes:       []item.Kind{item.KindMovie},
			EnableTotalRecordCount: true,
		}, []dto.UUID{fixture.directAllowed.ID, fixture.unclassifiedMovie.ID}, 2)
	})

	t.Run("all user restrictions precede the search explicit-ID union ordering paging and count", func(t *testing.T) {
		user := fixture.user(`{"EnableAllFolders":false,"EnabledFolders":["` + fixture.rootA.ID.String() + `"],"BlockedTags":["blocked"],"AllowedTags":["allowed"],"MaxParentalRating":10,"MaxParentalSubRating":0,"BlockUnratedItems":[0]}`)
		limit := 1
		fixture.assertQuery(t, store.ItemQuery{
			User: user,
			ItemIDs: []dto.UUID{
				fixture.unionDeniedRoot.ID,
				fixture.unionBlocked.ID,
				fixture.unionAboveRating.ID,
				fixture.unionUnrated.ID,
				fixture.unionAllowed.ID,
			},
			SearchTerm:             "union search",
			IncludeItemTypes:       []item.Kind{item.KindMovie},
			Limit:                  &limit,
			EnableTotalRecordCount: true,
		}, []dto.UUID{fixture.unionAllowed.ID}, 1)
	})

	t.Run("ordinary playlist visibility remains separate from library access", func(t *testing.T) {
		viewer := fixture.user(`{"EnableAllFolders":true}`)
		fixture.assertQuery(t, store.ItemQuery{
			User:                   viewer,
			IncludeItemTypes:       []item.Kind{item.KindPlaylist},
			EnableTotalRecordCount: true,
		}, []dto.UUID{fixture.publicPlaylist.ID}, 1)
	})

	t.Run("administrator and source callers bypass user visibility rules", func(t *testing.T) {
		administrator := fixture.user(`{"IsAdministrator":true,"EnableAllFolders":false,"EnabledFolders":["` + fixture.rootA.ID.String() + `"],"BlockedTags":["blocked"],"AllowedTags":["allowed"],"MaxParentalRating":10,"BlockUnratedItems":[0]}`)
		want := []dto.UUID{fixture.unionDeniedRoot.ID, fixture.unionBlocked.ID, fixture.unionAboveRating.ID, fixture.unionUnrated.ID, fixture.unionAllowed.ID}
		query := store.ItemQuery{
			IncludeItemTypes:       []item.Kind{item.KindMovie},
			ItemIDs:                want,
			EnableTotalRecordCount: true,
		}
		query.User = administrator
		fixture.assertQuery(t, query, want, len(want))
		query.User = nil
		fixture.assertQuery(t, query, want, len(want))
		playlistQuery := store.ItemQuery{User: administrator, IncludeItemTypes: []item.Kind{item.KindPlaylist}, EnableTotalRecordCount: true}
		fixture.assertQuery(t, playlistQuery, []dto.UUID{fixture.privatePlaylist.ID, fixture.publicPlaylist.ID}, 2)
		playlistQuery.User = nil
		fixture.assertQuery(t, playlistQuery, []dto.UUID{fixture.privatePlaylist.ID, fixture.publicPlaylist.ID}, 2)
	})
}

type fullUserAccessFixture struct {
	items                                                                       *store.ItemStore
	userID                                                                      dto.UUID
	rootA, rootB                                                                item.Item
	directAllowed, deniedRoot                                                   item.Item
	directBlocked, inheritedBlocked                                             item.Item
	inheritedAllowed, untagged                                                  item.Item
	ratingBelow, ratingAtBoundary, ratingAbove                                  item.Item
	unratedMovie, unclassifiedMovie                                             item.Item
	unionAllowed, unionDeniedRoot, unionBlocked, unionAboveRating, unionUnrated item.Item
	publicPlaylist, privatePlaylist                                             item.Item
}

func newFullUserAccessFixture(t *testing.T, items *store.ItemStore) fullUserAccessFixture {
	t.Helper()
	rootA := fullUserAccessItem(t, 1000, item.KindFolder, "library a", nil)
	rootA.IsFolder = true
	rootB := fullUserAccessItem(t, 1001, item.KindFolder, "library b", nil)
	rootB.IsFolder = true

	directAllowed := fullUserAccessItem(t, 1010, item.KindMovie, "a direct allowed", &rootA.ID)
	directAllowed.Values.Tags = []string{"allowed"}
	directAllowed.InheritedParentalRatingValue = fullUserAccessInt(5)
	deniedRoot := fullUserAccessItem(t, 1011, item.KindMovie, "b denied root", &rootB.ID)
	directBlocked := fullUserAccessItem(t, 1012, item.KindMovie, "c direct blocked", &rootA.ID)
	directBlocked.Values.Tags = []string{"allowed", "blocked"}
	blockedParent := fullUserAccessItem(t, 1013, item.KindFolder, "blocked parent", &rootA.ID)
	blockedParent.IsFolder = true
	blockedParent.Values.Tags = []string{"blocked"}
	inheritedBlocked := fullUserAccessItem(t, 1014, item.KindMovie, "d inherited blocked", &blockedParent.ID)
	allowedParent := fullUserAccessItem(t, 1015, item.KindFolder, "allowed parent", &rootA.ID)
	allowedParent.IsFolder = true
	allowedParent.Values.Tags = []string{"allowed"}
	inheritedAllowed := fullUserAccessItem(t, 1016, item.KindMovie, "e inherited allowed", &allowedParent.ID)
	untagged := fullUserAccessItem(t, 1017, item.KindMovie, "f untagged", &rootA.ID)

	ratingBelow := fullUserAccessItem(t, 1020, item.KindMovie, "g rating below", &rootA.ID)
	ratingBelow.InheritedParentalRatingValue = fullUserAccessInt(9)
	ratingAtBoundary := fullUserAccessItem(t, 1021, item.KindMovie, "h rating boundary", &rootA.ID)
	ratingAtBoundary.InheritedParentalRatingValue = fullUserAccessInt(10)
	ratingAtBoundary.InheritedParentalRatingSubValue = fullUserAccessInt(0)
	ratingAbove := fullUserAccessItem(t, 1022, item.KindMovie, "i rating above", &rootA.ID)
	ratingAbove.InheritedParentalRatingValue = fullUserAccessInt(11)
	unrated := item.UnratedItemMovie
	unratedMovie := fullUserAccessItem(t, 1023, item.KindMovie, "j unrated movie", &rootA.ID)
	unratedMovie.UnratedType = &unrated
	unclassifiedMovie := fullUserAccessItem(t, 1024, item.KindMovie, "k unclassified movie", &rootA.ID)

	unionAllowed := fullUserAccessItem(t, 1030, item.KindMovie, "z union search allowed", &rootA.ID)
	unionAllowed.Values.Tags = []string{"allowed"}
	unionAllowed.InheritedParentalRatingValue = fullUserAccessInt(5)
	unionDeniedRoot := fullUserAccessItem(t, 1031, item.KindMovie, "a union search denied root", &rootB.ID)
	unionDeniedRoot.Values.Tags = []string{"allowed"}
	unionBlocked := fullUserAccessItem(t, 1032, item.KindMovie, "b union search blocked", &rootA.ID)
	unionBlocked.Values.Tags = []string{"allowed", "blocked"}
	unionAboveRating := fullUserAccessItem(t, 1033, item.KindMovie, "c union search high rating", &rootA.ID)
	unionAboveRating.Values.Tags = []string{"allowed"}
	unionAboveRating.InheritedParentalRatingValue = fullUserAccessInt(11)
	unionUnrated := fullUserAccessItem(t, 1034, item.KindMovie, "d union search unrated", &rootA.ID)
	unionUnrated.Values.Tags = []string{"allowed"}
	unionUnrated.UnratedType = &unrated

	owner := fullUserAccessUUID(t, 1090)
	publicPlaylist := fullUserAccessItem(t, 1091, item.KindPlaylist, "public playlist", nil)
	publicPlaylist.PlaylistOwnerUserID, publicPlaylist.OpenAccess = owner, true
	privatePlaylist := fullUserAccessItem(t, 1092, item.KindPlaylist, "private playlist", nil)
	privatePlaylist.PlaylistOwnerUserID = owner

	for _, value := range []item.Item{
		rootA, rootB,
		directAllowed, deniedRoot, directBlocked, blockedParent, inheritedBlocked, allowedParent, inheritedAllowed, untagged,
		ratingBelow, ratingAtBoundary, ratingAbove, unratedMovie, unclassifiedMovie,
		unionAllowed, unionDeniedRoot, unionBlocked, unionAboveRating, unionUnrated,
		publicPlaylist, privatePlaylist,
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upserting full user-access fixture item %s: %v", value.ID, err)
		}
	}
	return fullUserAccessFixture{
		items: items, userID: fullUserAccessUUID(t, 1099), rootA: rootA, rootB: rootB,
		directAllowed: directAllowed, deniedRoot: deniedRoot, directBlocked: directBlocked, inheritedBlocked: inheritedBlocked,
		inheritedAllowed: inheritedAllowed, untagged: untagged, ratingBelow: ratingBelow, ratingAtBoundary: ratingAtBoundary,
		ratingAbove: ratingAbove, unratedMovie: unratedMovie, unclassifiedMovie: unclassifiedMovie,
		unionAllowed: unionAllowed, unionDeniedRoot: unionDeniedRoot, unionBlocked: unionBlocked, unionAboveRating: unionAboveRating, unionUnrated: unionUnrated,
		publicPlaylist: publicPlaylist, privatePlaylist: privatePlaylist,
	}
}

func (f fullUserAccessFixture) user(policy string) *store.User {
	return &store.User{ID: f.userID, Policy: []byte(policy)}
}

func (f fullUserAccessFixture) assertQuery(t *testing.T, query store.ItemQuery, want []dto.UUID, total int) {
	t.Helper()
	query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
	query.SortOrder = []store.SortOrder{store.SortOrderAscending}
	got, err := f.items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("Query(%#v): %v", query, err)
	}
	ids := make([]dto.UUID, len(got.Items))
	for index, value := range got.Items {
		ids[index] = value.ID
	}
	if !reflect.DeepEqual(ids, want) || got.TotalRecordCount != total {
		t.Fatalf("query IDs/count = %v/%d, want %v/%d", ids, got.TotalRecordCount, want, total)
	}
}

func fullUserAccessItem(t *testing.T, value int, kind item.Kind, name string, parentID *dto.UUID) item.Item {
	t.Helper()
	return item.Item{ID: fullUserAccessUUID(t, value), Kind: kind, Name: fullUserAccessString(name), SortName: fullUserAccessString(name), ParentID: parentID}
}

func fullUserAccessUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing full user-access fixture UUID %d: %v", value, err)
	}
	return id
}

func fullUserAccessInt(value int) *int          { return &value }
func fullUserAccessString(value string) *string { return &value }
