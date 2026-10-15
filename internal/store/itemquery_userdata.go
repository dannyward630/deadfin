// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// addUserDataFiltersWithAggregates translates user-relative facts, including
// folder aggregates and alternate-version resume selection. It is the complete
// replacement for addUserDataFilters: callers must not invoke both methods.
func (b *itemQueryFilterBuilder) addUserDataFiltersWithAggregates(query ItemQuery) {
	if query.UserID == nil {
		return
	}
	if query.IsPlayed != nil {
		addPlayedUserDataFilter(b, query, *query.IsPlayed)
	}
	if query.IsResumable != nil {
		addResumableUserDataFilter(b, query, *query.IsResumable)
	}
	if query.IsFavorite != nil {
		b.addUserDataPredicate(*query.IsFavorite, func() string {
			return "is_favorite = " + b.argument(boolArg(b.d, true))
		})
	}
	if query.IsFavoriteOrLiked != nil {
		b.addUserDataPredicate(*query.IsFavoriteOrLiked, func() string {
			return "is_favorite = " + b.argument(boolArg(b.d, true))
		})
	}
	if query.IsLiked != nil {
		b.addUserDataPredicate(*query.IsLiked, func() string {
			return "rating >= " + b.argument(float32(6.5))
		})
	}
}

func addPlayedUserDataFilter(b *itemQueryFilterBuilder, query ItemQuery, played bool) {
	hasSeries := itemQueryIncludesKind(query.IncludeItemTypes, item.KindSeries)
	hasBoxSet := itemQueryIncludesKind(query.IncludeItemTypes, item.KindBoxSet)
	if !hasSeries && !hasBoxSet {
		b.addUserDataPredicate(played, func() string {
			return "played = " + b.argument(boolArg(b.d, true))
		})
		return
	}
	branches := make([]string, 0, 3)
	if hasSeries {
		predicate := seriesFullyPlayedForUser(b)
		if !played {
			predicate = "NOT (" + predicate + ")"
		}
		branches = append(branches, "(items.kind = 'Series' AND "+predicate+")")
	}
	if hasBoxSet {
		predicate := boxSetFullyPlayedForUser(b)
		if !played {
			predicate = "NOT (" + predicate + ")"
		}
		branches = append(branches, "(items.kind = 'BoxSet' AND "+predicate+")")
	}
	ordinary := userDataPlayedForItem(b, "items")
	if !played {
		ordinary = "NOT (" + ordinary + ")"
	}
	branches = append(branches, "(items.kind <> 'Series' AND items.kind <> 'BoxSet' AND "+ordinary+")")
	b.clauses = append(b.clauses, "("+joinSQLOr(branches)+")")
}

func addResumableUserDataFilter(b *itemQueryFilterBuilder, query ItemQuery, resumable bool) {
	hasSeries := itemQueryIncludesKind(query.IncludeItemTypes, item.KindSeries)
	if !hasSeries {
		if resumable {
			// The Resume path uses a bounded CTE that starts from the user's
			// in-progress user_data rows. Translate detects this sentinel
			// clause and injects the resume_progress CTEs before filtered_ids
			// with the user_id argument at the correct placeholder position.
			// No arguments are added here; the CTE carries its own.
			b.clauses = append(b.clauses, resumeWinnerCTEReference)
		} else {
			b.clauses = append(b.clauses, "NOT ("+primaryVersionHasInProgressForUser(b, "items")+")")
		}
		return
	}
	if resumable {
		b.clauses = append(b.clauses, "((items.kind = 'Series' AND "+seriesResumableForUser(b)+") OR (items.kind <> 'Series' AND "+userDataInProgressForItem(b, "items")+" AND "+alternateVersionWinnerForUser(b)+"))")
		return
	}
	b.clauses = append(b.clauses, "((items.kind = 'Series' AND NOT ("+seriesResumableForUser(b)+")) OR (items.kind <> 'Series' AND NOT ("+primaryVersionHasInProgressForUser(b, "items")+")))")
}

// resumeWinnerCTEReference is the sentinel WHERE clause for the bounded
// Resume relation. Translate detects this string and replaces it with a
// reference to the injected resume_winner_ids CTE.
const resumeWinnerCTEReference = "items.id IN (SELECT item_id FROM resume_winner_ids)"

func itemQueryIncludesKind(kinds []item.Kind, want item.Kind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func joinSQLOr(clauses []string) string {
	joined := clauses[0]
	for _, clause := range clauses[1:] {
		joined += " OR " + clause
	}
	return joined
}

// userDataFactForItem is true when one of the user's rows matching predicate
// belongs to the item, by stored item id or by any custom key the item
// carries. It is an uncorrelated set the database evaluates once per query:
// a per-row EXISTS whose OR blocked index use scanned all of the user's rows
// for every candidate item (32s for Latest with hidePlayed on ~47k items).
// Arguments are added in text order because SQLite binds "?" by position.
//
// A query over a bounded candidate page (ItemQuery.latestWindow) checks each
// candidate with two indexed EXISTS lookups instead: building the set costs
// one pass over all of the user's rows, which then dominates. Item ids are
// never NULL, so both forms select the same items.
func userDataFactForItem(b *itemQueryFilterBuilder, itemAlias string, predicate func() string) string {
	keyFilter := ""
	if b.disableLiveTVChannelUserDataName {
		keyFilter = " AND item_key.is_live_tv_channel_name = " + b.d.Boolean(false)
	}
	if b.boundedCandidates {
		byID := "EXISTS (SELECT 1 FROM user_data AS user_data WHERE user_data.user_id = " + b.argument(uuidArgDialect(b.d, *b.userID)) + " AND user_data.item_id = " + itemAlias + ".id AND " + predicate() + ")"
		byKey := "EXISTS (SELECT 1 FROM item_user_data_keys AS item_key JOIN user_data AS user_data ON user_data.custom_key = item_key.custom_key AND user_data.user_id = " + b.argument(uuidArgDialect(b.d, *b.userID)) +
			" WHERE item_key.item_id = " + itemAlias + ".id" + keyFilter + " AND " + predicate() + ")"
		return "(" + byID + " OR " + byKey + ")"
	}
	byID := "SELECT user_data.item_id FROM user_data AS user_data WHERE user_data.user_id = " + b.argument(uuidArgDialect(b.d, *b.userID)) + " AND " + predicate()
	byKey := "SELECT item_key.item_id FROM user_data AS user_data JOIN item_user_data_keys AS item_key ON item_key.custom_key = user_data.custom_key" + keyFilter +
		" WHERE user_data.user_id = " + b.argument(uuidArgDialect(b.d, *b.userID)) + " AND " + predicate()
	return itemAlias + ".id IN (" + byID + " UNION " + byKey + ")"
}

func userDataPlayedForItem(b *itemQueryFilterBuilder, itemAlias string) string {
	return userDataFactForItem(b, itemAlias, func() string {
		return "played = " + b.argument(boolArg(b.d, true))
	})
}

func userDataInProgressForItem(b *itemQueryFilterBuilder, itemAlias string) string {
	return userDataFactForItem(b, itemAlias, func() string {
		return "position_ticks > 0"
	})
}

func directUserDataFactForItem(b *itemQueryFilterBuilder, itemAlias string, predicate func() string) string {
	userID := b.argument(uuidArgDialect(b.d, *b.userID))
	return "EXISTS (SELECT 1 FROM user_data AS user_data WHERE user_data.user_id = " + userID + " AND user_data.item_id = " + itemAlias + ".id AND " + predicate() + ")"
}

func directUserDataPlayedForItem(b *itemQueryFilterBuilder, itemAlias string) string {
	return directUserDataFactForItem(b, itemAlias, func() string {
		return "played = " + b.argument(boolArg(b.d, true))
	})
}

func directUserDataInProgressForItem(b *itemQueryFilterBuilder, itemAlias string) string {
	return directUserDataFactForItem(b, itemAlias, func() string {
		return "position_ticks > 0"
	})
}

func userDataCandidateClauseForItem(b *itemQueryFilterBuilder, userDataAlias, itemAlias string) string {
	keyFilter := ""
	if b.disableLiveTVChannelUserDataName {
		keyFilter = " AND item_key.is_live_tv_channel_name = " + b.d.Boolean(false)
	}
	return "(" + userDataAlias + ".item_id = " + itemAlias + ".id OR EXISTS (SELECT 1 FROM item_user_data_keys AS item_key WHERE item_key.item_id = " + itemAlias + ".id AND item_key.custom_key = " + userDataAlias + ".custom_key" + keyFilter + "))"
}

func seriesLeafScope(b *itemQueryFilterBuilder, alias string) string {
	return alias + ".series_id = items.id AND " + alias + ".is_folder = " + b.argument(boolArg(b.d, false)) + " AND " + alias + ".is_virtual_item = " + b.argument(boolArg(b.d, false))
}

func seriesFullyPlayedForUser(b *itemQueryFilterBuilder) string {
	hasLeaf := "EXISTS (SELECT 1 FROM items AS series_leaf WHERE " + seriesLeafScope(b, "series_leaf") + ")"
	unplayedLeaf := "NOT EXISTS (SELECT 1 FROM items AS series_leaf WHERE " + seriesLeafScope(b, "series_leaf") + " AND NOT (" + directUserDataPlayedForItem(b, "series_leaf") + "))"
	return hasLeaf + " AND " + unplayedLeaf
}

func seriesResumableForUser(b *itemQueryFilterBuilder) string {
	inProgress := "EXISTS (SELECT 1 FROM items AS series_leaf WHERE " + seriesLeafScope(b, "series_leaf") + " AND " + directUserDataInProgressForItem(b, "series_leaf") + ")"
	played := "EXISTS (SELECT 1 FROM items AS series_leaf WHERE " + seriesLeafScope(b, "series_leaf") + " AND " + directUserDataPlayedForItem(b, "series_leaf") + ")"
	unplayed := "EXISTS (SELECT 1 FROM items AS series_leaf WHERE " + seriesLeafScope(b, "series_leaf") + " AND NOT (" + directUserDataPlayedForItem(b, "series_leaf") + "))"
	return "(" + inProgress + " OR (" + played + " AND " + unplayed + "))"
}

func boxSetLeafScope(b *itemQueryFilterBuilder, alias string) string {
	return alias + ".is_folder = " + b.argument(boolArg(b.d, false)) + " AND " + alias + ".is_virtual_item = " + b.argument(boolArg(b.d, false)) + " AND (EXISTS (SELECT 1 FROM ancestors AS box_ancestor WHERE box_ancestor.ancestor_id = items.id AND box_ancestor.item_id = " + alias + ".id) OR EXISTS (SELECT 1 FROM linked_children AS box_link WHERE box_link.parent_item_id = items.id AND box_link.child_item_id = " + alias + ".id) OR EXISTS (SELECT 1 FROM linked_children AS box_link WHERE box_link.parent_item_id = items.id AND EXISTS (SELECT 1 FROM items AS box_child WHERE box_child.id = box_link.child_item_id AND box_child.is_folder = " + b.argument(boolArg(b.d, true)) + ") AND EXISTS (SELECT 1 FROM ancestors AS box_descendant WHERE box_descendant.ancestor_id = box_link.child_item_id AND box_descendant.item_id = " + alias + ".id)))"
}

func boxSetFullyPlayedForUser(b *itemQueryFilterBuilder) string {
	hasLeaf := "EXISTS (SELECT 1 FROM items AS box_leaf WHERE " + boxSetLeafScope(b, "box_leaf") + ")"
	unplayedLeaf := "NOT EXISTS (SELECT 1 FROM items AS box_leaf WHERE " + boxSetLeafScope(b, "box_leaf") + " AND NOT (" + userDataPlayedForItem(b, "box_leaf") + "))"
	return hasLeaf + " AND " + unplayedLeaf
}

func primaryVersionHasInProgressForUser(b *itemQueryFilterBuilder, itemAlias string) string {
	return "EXISTS (SELECT 1 FROM items AS primary_version WHERE COALESCE(primary_version.primary_version_id, primary_version.id) = COALESCE(" + itemAlias + ".primary_version_id, " + itemAlias + ".id) AND " + userDataInProgressForItem(b, "primary_version") + ")"
}

func alternateVersionWinnerForUser(b *itemQueryFilterBuilder) string {
	return "NOT EXISTS (SELECT 1 FROM items AS alternate_sibling WHERE alternate_sibling.id <> items.id AND COALESCE(alternate_sibling.primary_version_id, alternate_sibling.id) = COALESCE(items.primary_version_id, items.id) AND " + userDataInProgressForItem(b, "alternate_sibling") + " AND ((" + userDataMaxLastPlayedForItem(b, "alternate_sibling") + " IS NOT NULL AND (" + userDataMaxLastPlayedForItem(b, "items") + " IS NULL OR " + userDataMaxLastPlayedForItem(b, "alternate_sibling") + " > " + userDataMaxLastPlayedForItem(b, "items") + ")) OR ((" + userDataMaxLastPlayedForItem(b, "alternate_sibling") + " = " + userDataMaxLastPlayedForItem(b, "items") + " OR (" + userDataMaxLastPlayedForItem(b, "alternate_sibling") + " IS NULL AND " + userDataMaxLastPlayedForItem(b, "items") + " IS NULL)) AND alternate_sibling.id < items.id)))"
}

func userDataMaxLastPlayedForItem(b *itemQueryFilterBuilder, itemAlias string) string {
	userID := b.argument(uuidArgDialect(b.d, *b.userID))
	return "(SELECT MAX(user_data.last_played) FROM user_data AS user_data WHERE user_data.user_id = " + userID + " AND " + userDataCandidateClauseForItem(b, "user_data", itemAlias) + " AND user_data.position_ticks > 0)"
}

// resumeProgressCTEs builds the CTE SQL for the bounded Resume progress
// relation. The relation starts from the user's in-progress user_data rows,
// maps them to item IDs through direct matches and shared custom keys, groups
// alternate versions by COALESCE(primary_version_id, id), and selects the
// winner: greatest last_played, then lowest item ID as tie-break. The
// placeholderBase is the 1-indexed position of the first user_id argument;
// the two user_id placeholders use base and base+1.
func resumeProgressCTEs(d Dialect, userID dto.UUID, placeholderBase int) string {
	p1 := d.Placeholder(placeholderBase)
	p2 := d.Placeholder(placeholderBase + 1)
	directProgress := "SELECT user_data.item_id AS item_id, user_data.last_played AS last_played FROM user_data WHERE user_data.user_id = " + p1 + " AND user_data.position_ticks > 0"
	sharedProgress := "SELECT item_key.item_id AS item_id, user_data.last_played AS last_played FROM user_data JOIN item_user_data_keys AS item_key ON item_key.custom_key = user_data.custom_key WHERE user_data.user_id = " + p2 + " AND user_data.position_ticks > 0 AND item_key.item_id <> user_data.item_id"
	progressUnion := "SELECT item_id, MAX(last_played) AS last_played FROM (" + directProgress + " UNION ALL " + sharedProgress + ") AS progress GROUP BY item_id"
	winnerRank := "SELECT item_id, ROW_NUMBER() OVER (PARTITION BY COALESCE(items.primary_version_id, items.id) ORDER BY progress.last_played DESC " + d.NullsLast() + ", items.id ASC) AS rn FROM resume_progress_ids AS progress JOIN items ON items.id = progress.item_id"
	return "resume_progress_ids AS (" + progressUnion + "), resume_winner_ids AS (SELECT item_id FROM (" + winnerRank + ") AS ranked WHERE ranked.rn = 1)"
}
