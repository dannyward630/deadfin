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
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/trickplay"
)

// RegisterTrickplay registers the authenticated HLS image playlist and tile
// routes. Artifact lookup remains entirely owned by the reader service.
func RegisterTrickplay(rt *router.Router, reader trickplayReader) {
	handler := trickplayHandler{reader: reader}
	rt.Handle(http.MethodGet, "/Videos/{itemId}/Trickplay/{width}/tiles.m3u8", router.Route{
		OperationID: "GetTrickplayHlsPlaylist",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}, handler.playlist)
	rt.Handle(http.MethodGet, "/Videos/{itemId}/Trickplay/{width}/{index}.jpg", router.Route{
		OperationID: "GetTrickplayTileImage",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}, handler.tile)
}

type trickplayReader interface {
	Playlist(context.Context, trickplay.ReadRequest) (string, bool, error)
	Tile(context.Context, trickplay.ReadRequest, int) (trickplay.Artifact, error)
}

type trickplayHandler struct {
	reader trickplayReader
}

type trickplayQuery struct {
	MediaSourceID *dto.UUID `query:"mediaSourceId"`
}

func (h trickplayHandler) playlist(w http.ResponseWriter, r *http.Request) {
	request, ok := trickplayReadRequest(w, r)
	if !ok {
		return
	}
	if h.reader == nil {
		internalError(w)
		return
	}

	playlist, found, err := h.reader.Playlist(r.Context(), request)
	if err != nil {
		trickplayError(w, err)
		return
	}
	if !found {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/x-mpegURL; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = io.WriteString(w, playlist)
}

func (h trickplayHandler) tile(w http.ResponseWriter, r *http.Request) {
	request, ok := trickplayReadRequest(w, r)
	if !ok {
		return
	}
	index, ok := trickplayPathInt(w, r, "index")
	if !ok {
		return
	}
	if h.reader == nil {
		internalError(w)
		return
	}

	artifact, err := h.reader.Tile(r.Context(), request, index)
	if err != nil {
		trickplayError(w, err)
		return
	}
	if artifact.Body == nil {
		internalError(w)
		return
	}
	defer func() { _ = artifact.Body.Close() }()

	w.Header().Set("Content-Type", artifact.ContentType)
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("ETag", strconv.Quote(artifact.GenerationToken))
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "tile.jpg", artifact.Modified, artifact.Body)
}

func trickplayReadRequest(w http.ResponseWriter, r *http.Request) (trickplay.ReadRequest, bool) {
	itemID, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil {
		badRequest(w)
		return trickplay.ReadRequest{}, false
	}
	width, ok := trickplayPathInt(w, r, "width")
	if !ok {
		return trickplay.ReadRequest{}, false
	}
	var query trickplayQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		badRequest(w)
		return trickplay.ReadRequest{}, false
	}

	identity, _ := middleware.IdentityFromContext(r.Context())
	return trickplay.ReadRequest{
		ItemID:        itemID,
		MediaSourceID: query.MediaSourceID,
		UserID:        identity.UserID,
		Administrator: identity.Administrator,
		Width:         width,
		APIKey:        identity.Token,
	}, true
}

func trickplayPathInt(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	value, err := strconv.ParseInt(chi.URLParam(r, name), 10, 32)
	if err != nil {
		badRequest(w)
		return 0, false
	}
	return int(value), true
}

func trickplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, trickplay.ErrNotFound) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	internalError(w)
}
