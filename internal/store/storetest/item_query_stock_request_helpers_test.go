// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import "github.com/deadfin-org/deadfin/internal/store"

func stockQueryNeedsUser(query store.ItemQuery) bool {
	for _, sort := range query.SortBy {
		switch sort {
		case store.ItemSortByDatePlayed, store.ItemSortByPlayCount, store.ItemSortByIsUnplayed, store.ItemSortByIsPlayed, store.ItemSortByIsFavoriteOrLiked, store.ItemSortBySeriesDatePlayed:
			return true
		}
	}
	return false
}
