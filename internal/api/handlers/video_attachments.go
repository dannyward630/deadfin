// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// AttachmentReader is the handler-facing seam for media attachments.
type AttachmentReader interface {
	Attachment(context.Context, playback.AttachmentRequest) (playback.AttachmentResponse, error)
}

// RegisterVideoAttachments registers GetAttachment. Upstream
// VideoAttachmentsController has no [Authorize] attribute, so the route is
// anonymous. A request that carries a user token resolves the item through
// that user's access filter, as GetItemById(videoId, User.GetUserId()) does.
func RegisterVideoAttachments(rt *router.Router, attachments AttachmentReader) {
	h := videoAttachmentsHandler{attachments: attachments}
	rt.Handle(http.MethodGet, "/Videos/{videoId}/{mediaSourceId}/Attachments/{index}", router.Route{
		OperationID:      "GetAttachment",
		Anonymous:        true,
		OptionalIdentity: true,
	}, h.get)
}

// attachmentContentType returns the response type for a stored attachment
// MIME type, and whether the response must be a download. Only fonts (for
// libass and client subtitle renderers) and raster cover art keep their
// stored type. Every other type, active document types included, is served
// as an octet-stream download (D-64).
func attachmentContentType(stored string) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(stored)
	if err != nil {
		return "application/octet-stream", true
	}
	switch mediaType {
	case "font/collection", "font/otf", "font/sfnt", "font/ttf", "font/woff", "font/woff2",
		"application/font-sfnt", "application/font-woff", "application/vnd.ms-fontobject",
		"application/vnd.ms-opentype", "application/x-font", "application/x-font-opentype",
		"application/x-font-otf", "application/x-font-truetype", "application/x-font-ttf",
		"application/x-truetype-font",
		"image/avif", "image/bmp", "image/gif", "image/jpeg", "image/png", "image/webp":
		return mediaType, false
	}
	return "application/octet-stream", true
}

type videoAttachmentsHandler struct {
	attachments AttachmentReader
}

func (h videoAttachmentsHandler) get(w http.ResponseWriter, r *http.Request) {
	videoID, err := dto.ParseUUID(chi.URLParam(r, "videoId"))
	if err != nil {
		badRequest(w)
		return
	}
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil || !validHLSHandlerInt(index) {
		badRequest(w)
		return
	}
	request := playback.AttachmentRequest{
		ItemID:        videoID,
		MediaSourceID: chi.URLParam(r, "mediaSourceId"),
		Index:         index,
	}
	// An API key keeps the unscoped lookup, as on the other item routes.
	if identity, ok := middleware.IdentityFromContext(r.Context()); ok && identity.Authenticated && !identity.APIKey {
		request.UserID = identity.UserID
	}
	response, err := h.attachments.Attachment(r.Context(), request)
	if err != nil {
		switch {
		case errors.Is(err, playback.ErrItemNotFound), errors.Is(err, playback.ErrMediaSourceNotFound), errors.Is(err, playback.ErrAttachmentNotFound):
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		default:
			internalError(w)
		}
		return
	}
	defer func() { _ = response.File.Close() }()
	contentType, download := attachmentContentType(response.ContentType)
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Length", strconv.FormatInt(response.Size, 10))
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	if download {
		header.Set("Content-Disposition", "attachment")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, response.File)
}
