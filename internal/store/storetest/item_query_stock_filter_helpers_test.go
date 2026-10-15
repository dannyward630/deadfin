// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/store"
)

// stockApplyFilters models the public GetItems Filters enum rather than letting
// recorder-only ItemQuery names bypass the public wire contract.
func stockApplyFilters(t *testing.T, query *store.ItemQuery, raw any, requiresUser *bool) {
	t.Helper()
	for _, filter := range stockAnyStrings(t, "Filters", raw) {
		switch filter {
		case "IsFolder":
			query.IsFolder = stockBoolPtr(true)
		case "IsNotFolder":
			query.IsFolder = stockBoolPtr(false)
		case "IsUnplayed":
			query.IsPlayed = stockBoolPtr(false)
			*requiresUser = true
		case "IsPlayed":
			query.IsPlayed = stockBoolPtr(true)
			*requiresUser = true
		case "IsFavorite":
			query.IsFavorite = stockBoolPtr(true)
			*requiresUser = true
		case "IsResumable":
			query.IsResumable = stockBoolPtr(true)
			*requiresUser = true
		case "Likes":
			query.IsLiked = stockBoolPtr(true)
			*requiresUser = true
		case "Dislikes":
			query.IsLiked = stockBoolPtr(false)
			*requiresUser = true
		case "IsFavoriteOrLikes":
			query.IsFavoriteOrLiked = stockBoolPtr(true)
			*requiresUser = true
		default:
			t.Fatalf("unsupported public GetItems filter %q", filter)
		}
	}
}

func stockBoolPtr(value bool) *bool { return &value }
