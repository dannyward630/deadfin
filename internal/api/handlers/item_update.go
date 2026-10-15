// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/itemedit"
)

// ItemEditor is the metadata editor seam behind ItemUpdateController.
// *itemedit.Service satisfies it.
type ItemEditor interface {
	Update(context.Context, dto.UUID, dto.ItemUpdateRequest) error
	MetadataEditorInfo(context.Context, dto.UUID) (dto.MetadataEditorInfo, error)
	UpdateContentType(context.Context, dto.UUID, string) error
}

// RegisterItemUpdate registers ItemUpdateController. The controller carries
// [Authorize(Policy = Policies.RequiresElevation)] on the class.
func RegisterItemUpdate(rt *router.Router, editor ItemEditor) {
	h := itemUpdateHandler{editor: editor}
	policy := string(auth.PolicyRequiresElevation)
	rt.Handle(http.MethodPost, "/Items/{itemId}", router.Route{OperationID: "UpdateItem", Policy: policy}, h.update)
	rt.Handle(http.MethodGet, "/Items/{itemId}/MetadataEditor", router.Route{OperationID: "GetMetadataEditorInfo", Policy: policy}, h.editorInfo)
	rt.Handle(http.MethodPost, "/Items/{itemId}/ContentType", router.Route{OperationID: "UpdateItemContentType", Policy: policy}, h.contentType)
}

type itemUpdateHandler struct {
	editor ItemEditor
}

type contentTypeQuery struct {
	ContentType string `query:"contentType"`
}

func (h itemUpdateHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	// [FromBody, Required]: an absent body fails model binding.
	if r.ContentLength == 0 {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	var body dto.ItemUpdateRequest
	if err := bind.JSONBody(r, &body); err != nil {
		if errors.Is(err, bind.ErrUnsupportedMediaType) {
			http.Error(w, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
			return
		}
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if h.editor == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writeItemEditResult(w, h.editor.Update(r.Context(), id, body))
}

func (h itemUpdateHandler) editorInfo(w http.ResponseWriter, r *http.Request) {
	id, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if h.editor == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	info, err := h.editor.MetadataEditorInfo(r.Context(), id)
	if err != nil {
		writeItemEditResult(w, err)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, info)
}

func (h itemUpdateHandler) contentType(w http.ResponseWriter, r *http.Request) {
	id, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	var query contentTypeQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if h.editor == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writeItemEditResult(w, h.editor.UpdateContentType(r.Context(), id, query.ContentType))
}

// writeItemEditResult maps an edit result to the controller's status codes.
func writeItemEditResult(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, itemedit.ErrItemNotFound):
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
	case errors.Is(err, itemedit.ErrInvalidRequest):
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
	default:
		w.WriteHeader(http.StatusInternalServerError)
	}
}
