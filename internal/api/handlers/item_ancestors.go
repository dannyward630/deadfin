// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"net/http"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// ancestors returns the parent chain of the requested item as a bare JSON
// array of BaseItemDto, nearest parent first. It reproduces the stock
// LibraryController.GetAncestors null-translate break: the store user-access
// clause hides policy-blocked collection folders, so an empty result stops the
// walk. A user request stops before the AggregateFolder; a nil-user (global API
// key) request emits the full chain including the aggregate root.
func (h itemExtrasHandler) ancestors(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil || h.build == nil {
		internalItemExtrasError(w)
		return
	}
	user, ok := h.itemExtrasUser(w, r, nil)
	if !ok {
		return
	}
	itemID, ok := itemExtrasItemID(w, r)
	if !ok {
		return
	}
	source, ok := h.source(w, r, itemID, user)
	if !ok {
		return
	}
	values := h.ancestorChain(r, source, user)
	h.renderItems(w, r, values, store.ItemQueryDTOOptions{}, itemExtrasUserID(user), true)
}

// ancestorChain walks the parent chain from the source item upward. Each step
// queries the reader with ItemIDs set to the current parent. The store
// user-access clause hides policy-blocked collection folders, so an empty
// result stops the walk. When the user is not nil and the parent kind is
// AggregateFolder, the walk stops without emitting that parent. When the user
// is nil, the walk emits the full chain including the aggregate root.
func (h itemExtrasHandler) ancestorChain(r *http.Request, source item.Item, user *store.User) []item.Item {
	values := make([]item.Item, 0, 4)
	current := source
	for {
		if current.ParentID == nil || *current.ParentID == dto.NilUUID {
			break
		}
		query := store.ItemQuery{
			ItemIDs: []dto.UUID{*current.ParentID},
			User:    user,
			UserID:  itemExtrasUserID(user),
		}
		result, err := h.reader.Query(r.Context(), query)
		if err != nil || len(result.Items) == 0 {
			break
		}
		parent := result.Items[0]
		if user != nil && parent.Kind == item.KindAggregateFolder {
			break
		}
		values = append(values, parent)
		current = parent
	}
	return values
}
