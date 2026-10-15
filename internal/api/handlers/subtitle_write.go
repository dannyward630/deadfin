// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/subtitleedit"
)

// SubtitleEditor is the external subtitle write seam behind
// SubtitleController. *subtitleedit.Service satisfies it.
type SubtitleEditor interface {
	Upload(ctx context.Context, userID, id dto.UUID, upload subtitleedit.Upload) error
	Delete(ctx context.Context, userID, id dto.UUID, index int) error
}

// RegisterSubtitleWrites registers SubtitleController.UploadSubtitle
// ([Authorize(Policy = Policies.SubtitleManagement)]) and DeleteSubtitle
// ([Authorize(Policy = Policies.RequiresElevation)]).
func RegisterSubtitleWrites(rt *router.Router, editor SubtitleEditor) {
	h := subtitleWriteHandler{editor: editor}
	rt.Handle(http.MethodPost, "/Videos/{itemId}/Subtitles", router.Route{OperationID: "UploadSubtitle", Policy: string(auth.PolicySubtitleManagement)}, h.upload)
	rt.Handle(http.MethodDelete, "/Videos/{itemId}/Subtitles/{index}", router.Route{OperationID: "DeleteSubtitle", Policy: string(auth.PolicyRequiresElevation)}, h.delete)
}

type subtitleWriteHandler struct {
	editor SubtitleEditor
}

// uploadSubtitleBody is UploadSubtitleDto. Every member carries [Required].
type uploadSubtitleBody struct {
	Language          *string `json:"Language"`
	Format            *string `json:"Format"`
	IsForced          *bool   `json:"IsForced"`
	IsHearingImpaired *bool   `json:"IsHearingImpaired"`
	Data              *string `json:"Data"`
}

func (body uploadSubtitleBody) valid() bool {
	// [Required] rejects a missing member and an empty string.
	return body.Language != nil && *body.Language != "" &&
		body.Format != nil && *body.Format != "" &&
		body.IsForced != nil && body.IsHearingImpaired != nil &&
		body.Data != nil && *body.Data != ""
}

func (h subtitleWriteHandler) upload(w http.ResponseWriter, r *http.Request) {
	id, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil {
		badRequest(w)
		return
	}
	var body uploadSubtitleBody
	if err := bind.JSONBody(r, &body); err != nil {
		if errors.Is(err, bind.ErrUnsupportedMediaType) {
			http.Error(w, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
			return
		}
		badRequest(w)
		return
	}
	if !body.valid() {
		badRequest(w)
		return
	}
	if h.editor == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	err = h.editor.Upload(r.Context(), subtitleWriteUserID(r), id, subtitleedit.Upload{
		Language:          *body.Language,
		Format:            *body.Format,
		IsForced:          *body.IsForced,
		IsHearingImpaired: *body.IsHearingImpaired,
		Data:              *body.Data,
	})
	writeSubtitleWriteResult(w, err)
}

func (h subtitleWriteHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil {
		badRequest(w)
		return
	}
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil {
		badRequest(w)
		return
	}
	if h.editor == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writeSubtitleWriteResult(w, h.editor.Delete(r.Context(), subtitleWriteUserID(r), id, index))
}

// subtitleWriteUserID is the requesting user, or the nil UUID for an API key
// (User.GetUserId() is empty for an API key).
func subtitleWriteUserID(r *http.Request) dto.UUID {
	identity, ok := middleware.IdentityFromContext(r.Context())
	if !ok || identity.APIKey {
		return dto.NilUUID
	}
	return identity.UserID
}

func writeSubtitleWriteResult(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, subtitleedit.ErrItemNotFound):
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
	// Upstream indexes an empty stream list (ArgumentOutOfRangeException) and
	// deletes a null path (ArgumentNullException); both are 400.
	case errors.Is(err, subtitleedit.ErrInvalidRequest), errors.Is(err, subtitleedit.ErrSubtitleNotFound), errors.Is(err, subtitleedit.ErrEmbeddedSubtitle):
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
	default:
		w.WriteHeader(http.StatusInternalServerError)
	}
}
