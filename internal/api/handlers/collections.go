// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// CollectionCreator creates a BoxSet (CollectionManager.CreateCollectionAsync
// without the items, which the handler adds).
type CollectionCreator interface {
	CreateCollection(ctx context.Context, name string, locked bool) (item.Item, error)
}

// RegisterCollections registers CollectionController. Passing a nil store
// or creator registers the routes with a 503 handler, which keeps the pure
// route ledger path valid.
func RegisterCollections(rt *router.Router, items *store.ItemStore, creator CollectionCreator) {
	h := collectionsHandler{items: items, creator: creator}
	rt.Handle(http.MethodPost, "/Collections", router.Route{
		OperationID: "CreateCollection",
		Policy:      string(auth.PolicyCollectionManagement),
	}, h.createCollection)
	rt.Handle(http.MethodPost, "/Collections/{collectionId}/Items", router.Route{
		OperationID: "AddToCollection",
		Policy:      string(auth.PolicyCollectionManagement),
	}, h.addToCollection)
	rt.Handle(http.MethodDelete, "/Collections/{collectionId}/Items", router.Route{
		OperationID: "RemoveFromCollection",
		Policy:      string(auth.PolicyCollectionManagement),
	}, h.removeFromCollection)
}

type collectionsHandler struct {
	items   *store.ItemStore
	creator CollectionCreator
}

// createCollection ports CollectionController.CreateCollection. The BoxSet
// exists before its items are added, so an unknown item id returns 400 and
// leaves the empty collection, as upstream does.
func (h collectionsHandler) createCollection(w http.ResponseWriter, r *http.Request) {
	if h.items == nil || h.creator == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	var query struct {
		Name     string     `query:"name"`
		IDs      []dto.UUID `query:"ids"`
		ParentID *dto.UUID  `query:"parentId"`
		IsLocked bool       `query:"isLocked"`
	}
	if err := bind.QueryRequest(r, &query); err != nil || query.Name == "" {
		badRequest(w)
		return
	}
	created, err := h.creator.CreateCollection(r.Context(), query.Name, query.IsLocked)
	if err != nil {
		internalError(w)
		return
	}
	if len(query.IDs) != 0 {
		if err := h.items.AddToCollection(r.Context(), created.ID, query.IDs); err != nil {
			collectionError(w, err)
			return
		}
	}
	_ = render.JSON(w, r, http.StatusOK, dto.CollectionCreationResult{ID: created.ID})
}

func (h collectionsHandler) removeFromCollection(w http.ResponseWriter, r *http.Request) {
	if h.items == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	collectionID, ok := routeUUID(w, r, "collectionId")
	if !ok {
		return
	}
	var query struct {
		IDs []dto.UUID `query:"ids"`
	}
	if err := bind.QueryRequest(r, &query); err != nil || len(query.IDs) == 0 {
		badRequest(w)
		return
	}
	if err := h.items.RemoveFromCollection(r.Context(), collectionID, query.IDs); err != nil {
		collectionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// collectionError maps upstream's ArgumentException (an unknown collection
// or item) to 400 and any other failure to 500.
func collectionError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrInvalidReference) {
		badRequest(w)
		return
	}
	internalError(w)
}

func (h collectionsHandler) addToCollection(w http.ResponseWriter, r *http.Request) {
	if h.items == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	collectionID, ok := routeUUID(w, r, "collectionId")
	if !ok {
		return
	}
	var query struct {
		IDs []dto.UUID `query:"ids"`
	}
	if err := bind.QueryRequest(r, &query); err != nil {
		badRequest(w)
		return
	}
	if len(query.IDs) == 0 {
		badRequest(w)
		return
	}
	if err := h.items.AddToCollection(r.Context(), collectionID, query.IDs); err != nil {
		collectionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
