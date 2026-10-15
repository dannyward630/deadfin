// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/itemdelete"
)

// ItemDeleter is the service behind DELETE /Items and DELETE /Items/{itemId}.
type ItemDeleter interface {
	DeleteItem(context.Context, itemdelete.Actor, dto.UUID) error
	DeleteItems(context.Context, itemdelete.Actor, []dto.UUID) error
}

// RegisterItemDelete registers LibraryController.DeleteItem and DeleteItems.
// Both need only an authenticated caller; the service applies
// BaseItem.CanDelete to the user.
func RegisterItemDelete(rt *router.Router, deleter ItemDeleter) {
	h := itemDeleteHandler{deleter: deleter}
	rt.Handle(http.MethodDelete, "/Items/{itemId}", router.Route{
		OperationID: "DeleteItem",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}, h.deleteItem)
	rt.Handle(http.MethodDelete, "/Items", router.Route{
		OperationID: "DeleteItems",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}, h.deleteItems)
}

type itemDeleteHandler struct {
	deleter ItemDeleter
}

func (h itemDeleteHandler) deleteItem(w http.ResponseWriter, r *http.Request) {
	id, ok := routeUUID(w, r, "itemId")
	if !ok {
		return
	}
	if h.deleter == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	writeItemDeleteResult(w, h.deleter.DeleteItem(r.Context(), itemDeleteActor(r), id))
}

func (h itemDeleteHandler) deleteItems(w http.ResponseWriter, r *http.Request) {
	var query struct {
		IDs []dto.UUID `query:"ids"`
	}
	if err := bind.QueryRequest(r, &query); err != nil {
		badRequest(w)
		return
	}
	if h.deleter == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	writeItemDeleteResult(w, h.deleter.DeleteItems(r.Context(), itemDeleteActor(r), query.IDs))
}

func itemDeleteActor(r *http.Request) itemdelete.Actor {
	identity, _ := middleware.IdentityFromContext(r.Context())
	return itemdelete.Actor{APIKey: identity.APIKey, UserID: identity.UserID}
}

// writeItemDeleteResult maps the service result to the upstream responses:
// NoContent, NotFound, and Unauthorized("Unauthorized access").
func writeItemDeleteResult(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, itemdelete.ErrNotFound):
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
	case errors.Is(err, itemdelete.ErrUnauthorized):
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Unauthorized access"))
	default:
		internalError(w)
	}
}
