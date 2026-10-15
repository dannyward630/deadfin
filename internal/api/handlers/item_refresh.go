// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// ItemRefreshTrigger is the narrow scanner seam behind POST /Items/{itemId}/Refresh.
// TriggerRefresh owns any background scan it accepts and threads the refresh
// intent so a full/replace refresh re-runs the remote metadata stage on items
// that were already scanned.
type ItemRefreshTrigger interface {
	TriggerRefresh(library.RefreshOptions) error
}

// RegisterItemRefresh registers the identify/refresh trigger (WP-P8-05). It
// forces the provider chain — including the remote stage — to reprocess the
// target item.
func RegisterItemRefresh(rt *router.Router, reader ItemReader, trigger ItemRefreshTrigger) {
	h := itemRefreshHandler{reader: reader, trigger: trigger}
	rt.Handle(http.MethodPost, "/Items/{itemId}/Refresh", router.Route{
		OperationID: "RefreshItem",
		Policy:      string(auth.PolicyRequiresElevation),
	}, h.refresh)
}

type itemRefreshHandler struct {
	reader  ItemReader
	trigger ItemRefreshTrigger
}

// refreshQuery binds the stock ItemRefreshController query parameters. The two
// modes default to None (upstream MetadataRefreshMode default); the replace and
// trickplay flags default to false. See ItemRefreshController.RefreshItem.
type refreshQuery struct {
	MetadataRefreshMode library.RefreshMode `query:"metadataRefreshMode"`
	ImageRefreshMode    library.RefreshMode `query:"imageRefreshMode"`
	ReplaceAllMetadata  bool                `query:"replaceAllMetadata"`
	ReplaceAllImages    bool                `query:"replaceAllImages"`
	RegenerateTrickplay bool                `query:"regenerateTrickplay"`
}

func (h itemRefreshHandler) refresh(w http.ResponseWriter, r *http.Request) {
	id, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	var query refreshQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if h.reader == nil || h.trigger == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	// Upstream answers 404 when GetItemById is null, before queuing the refresh.
	result, err := h.reader.Query(r.Context(), store.ItemQuery{ItemIDs: []dto.UUID{id}, Recursive: true})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if len(result.Items) == 0 {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}

	options := library.RefreshOptions{
		Metadata:           query.MetadataRefreshMode,
		Image:              query.ImageRefreshMode,
		ReplaceAllMetadata: query.ReplaceAllMetadata,
		ReplaceAllImages:   query.ReplaceAllImages,
		Targets:            []library.RefreshTarget{refreshTarget(result.Items[0])},
	}
	if query.RegenerateTrickplay {
		options.RegenerateTrickplayItemIDs = []dto.UUID{id}
	}
	switch err := h.trigger.TriggerRefresh(options); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, library.ErrScannerClosed):
		w.WriteHeader(http.StatusServiceUnavailable)
	default:
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// refreshTarget scopes the scan to the library that holds target. Upstream
// ProviderManager.RefreshItem refreshes only the target and its descendants;
// it never refreshes other libraries.
func refreshTarget(target item.Item) library.RefreshTarget {
	scope := library.RefreshTarget{ItemID: target.ID}
	if target.Path != nil {
		scope.Path = *target.Path
	}
	return scope
}
