// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/itemedit"
)

// ItemImageEditor is the item image write seam behind ImageController.
// *itemedit.Service satisfies it.
type ItemImageEditor interface {
	SetImage(ctx context.Context, id dto.UUID, imageType item.ImageType, mediaType string, body io.Reader) error
	DeleteImage(ctx context.Context, id dto.UUID, imageType item.ImageType, index int) error
	UpdateImageIndex(ctx context.Context, id dto.UUID, imageType item.ImageType, index, newIndex int) error
}

// RegisterItemImageWrites registers the ImageController item image writes.
// Each carries [Authorize(Policy = Policies.RequiresElevation)].
func RegisterItemImageWrites(rt *router.Router, editor ItemImageEditor) {
	h := itemImageWriteHandler{editor: editor}
	policy := string(auth.PolicyRequiresElevation)
	rt.Handle(http.MethodDelete, "/Items/{itemId}/Images/{imageType}", router.Route{OperationID: "DeleteItemImage", Policy: policy}, h.delete)
	rt.Handle(http.MethodDelete, "/Items/{itemId}/Images/{imageType}/{imageIndex}", router.Route{OperationID: "DeleteItemImageByIndex", Policy: policy}, h.delete)
	rt.Handle(http.MethodPost, "/Items/{itemId}/Images/{imageType}", router.Route{OperationID: "SetItemImage", Policy: policy}, h.set)
	rt.Handle(http.MethodPost, "/Items/{itemId}/Images/{imageType}/{imageIndex}", router.Route{OperationID: "SetItemImageByIndex", Policy: policy}, h.set)
	rt.Handle(http.MethodPost, "/Items/{itemId}/Images/{imageType}/{imageIndex}/Index", router.Route{OperationID: "UpdateItemImageIndex", Policy: policy}, h.updateIndex)
}

type itemImageWriteHandler struct {
	editor ItemImageEditor
}

type deleteImageQuery struct {
	ImageIndex *int `query:"imageIndex"`
}

type updateImageIndexQuery struct {
	NewIndex *int `query:"newIndex"`
}

// itemImageTarget binds the itemId, imageType, and optional imageIndex route
// values. imageIndex is absent on the routes that do not carry it.
func itemImageTarget(r *http.Request) (dto.UUID, item.ImageType, *int, bool) {
	id, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil {
		return dto.UUID{}, 0, nil, false
	}
	imageType, ok := parseImageType(chi.URLParam(r, "imageType"))
	if !ok {
		return dto.UUID{}, 0, nil, false
	}
	raw := chi.URLParam(r, "imageIndex")
	if raw == "" {
		return id, imageType, nil, true
	}
	index, err := strconv.Atoi(raw)
	if err != nil {
		return dto.UUID{}, 0, nil, false
	}
	return id, imageType, &index, true
}

func (h itemImageWriteHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, imageType, index, ok := itemImageTarget(r)
	if !ok {
		badRequest(w)
		return
	}
	if index == nil {
		var query deleteImageQuery
		if err := bind.QueryRequest(r, &query); err != nil {
			badRequest(w)
			return
		}
		index = query.ImageIndex
	}
	if h.editor == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	position := 0
	if index != nil {
		position = *index
	}
	writeItemImageResult(w, h.editor.DeleteImage(r.Context(), id, imageType, position))
}

// set saves a Base64 image body. The ByIndex route's index is unused, as
// upstream (ImageController.cs:402-433).
func (h itemImageWriteHandler) set(w http.ResponseWriter, r *http.Request) {
	id, imageType, _, ok := itemImageTarget(r)
	if !ok {
		badRequest(w)
		return
	}
	if h.editor == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	body, mediaType, err := bind.ImageBody(r)
	if err != nil {
		// Upstream checks the item first, then answers "Incorrect ContentType."
		writeItemImageResult(w, h.editor.SetImage(r.Context(), id, imageType, "", nil))
		return
	}
	writeItemImageResult(w, h.editor.SetImage(r.Context(), id, imageType, mediaType, body))
}

func (h itemImageWriteHandler) updateIndex(w http.ResponseWriter, r *http.Request) {
	id, imageType, index, ok := itemImageTarget(r)
	if !ok || index == nil {
		badRequest(w)
		return
	}
	var query updateImageIndexQuery
	if err := bind.QueryRequest(r, &query); err != nil || query.NewIndex == nil {
		badRequest(w)
		return
	}
	if h.editor == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writeItemImageResult(w, h.editor.UpdateImageIndex(r.Context(), id, imageType, *index, *query.NewIndex))
}

func writeItemImageResult(w http.ResponseWriter, err error) {
	if errors.Is(err, itemedit.ErrUnsupportedImage) {
		http.Error(w, "Incorrect ContentType.", http.StatusBadRequest)
		return
	}
	writeItemEditResult(w, err)
}
