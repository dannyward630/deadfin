// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

// RegisterSubtitleStreams registers the HLS subtitle rendition playlist and stream routes.
func RegisterSubtitleStreams(rt *router.Router, streamer playback.SubtitleStreamer) {
	h := subtitleStreamHandler{streamer: streamer}
	policy := string(auth.PolicyDefaultAuthorization)

	rt.Handle(http.MethodGet, "/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/subtitles.m3u8", router.Route{
		OperationID: "GetSubtitlePlaylist",
		Policy:      policy,
	}, h.playlist)
	rt.Handle(http.MethodGet, "/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/Stream.{routeFormat}", router.Route{
		OperationID: "GetSubtitle",
		Policy:      policy,
	}, h.stream)
	rt.Handle(http.MethodGet, "/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/{routeStartPositionTicks}/Stream.{routeFormat}", router.Route{
		OperationID: "GetSubtitleWithTicks",
		Policy:      policy,
	}, h.streamWithTicks)
}

type subtitleStreamHandler struct {
	streamer playback.SubtitleStreamer
}

type subtitlePlaylistQuery struct {
	SegmentLength *int `query:"segmentLength"`
}

type subtitleStreamQuery struct {
	ItemID             *string `query:"itemId"`
	MediaSourceID      *string `query:"mediaSourceId"`
	Index              *int    `query:"index"`
	Format             *string `query:"format"`
	StartPositionTicks *int64  `query:"startPositionTicks"`
	EndPositionTicks   *int64  `query:"endPositionTicks"`
	CopyTimestamps     *bool   `query:"copyTimestamps"`
	AddVTTTimeMap      *bool   `query:"addVttTimeMap"`
}

func subtitleAuthenticatedIdentity(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	identity, authenticated := middleware.IdentityFromContext(r.Context())
	if !authenticated || !identity.Authenticated {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return auth.Identity{}, false
	}
	return identity, true
}

func (h subtitleStreamHandler) playlist(w http.ResponseWriter, r *http.Request) {
	identity, ok := subtitleAuthenticatedIdentity(w, r)
	if !ok {
		return
	}
	itemID, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil {
		badRequest(w)
		return
	}
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil || !validHLSHandlerInt(index) {
		badRequest(w)
		return
	}

	var query subtitlePlaylistQuery
	if err := bind.QueryRequest(r, &query); err != nil || query.SegmentLength == nil || !validHLSHandlerInt(*query.SegmentLength) || *query.SegmentLength <= 0 {
		badRequest(w)
		return
	}

	response, err := h.streamer.Playlist(r.Context(), playback.SubtitlePlaylistRequest{
		UserID:               identity.UserID,
		ItemID:               itemID,
		MediaSourceID:        chi.URLParam(r, "mediaSourceId"),
		Index:                index,
		SegmentLengthSeconds: *query.SegmentLength,
		APIKey:               identity.Token,
	})
	if err != nil {
		subtitleStreamError(w, err, true)
		return
	}
	subtitleStreamResponse(w, response)
}

func (h subtitleStreamHandler) stream(w http.ResponseWriter, r *http.Request) {
	identity, ok := subtitleAuthenticatedIdentity(w, r)
	if !ok {
		return
	}
	h.streamRequest(w, r, identity, 0)
}

func (h subtitleStreamHandler) streamWithTicks(w http.ResponseWriter, r *http.Request) {
	identity, ok := subtitleAuthenticatedIdentity(w, r)
	if !ok {
		return
	}
	routeStartPositionTicks, err := strconv.ParseInt(chi.URLParam(r, "routeStartPositionTicks"), 10, 64)
	if err != nil {
		badRequest(w)
		return
	}
	h.streamRequest(w, r, identity, routeStartPositionTicks)
}

func (h subtitleStreamHandler) streamRequest(w http.ResponseWriter, r *http.Request, identity auth.Identity, routeStartPositionTicks int64) {
	itemID, err := dto.ParseUUID(chi.URLParam(r, "routeItemId"))
	if err != nil {
		badRequest(w)
		return
	}
	index, err := strconv.Atoi(chi.URLParam(r, "routeIndex"))
	if err != nil || !validHLSHandlerInt(index) {
		badRequest(w)
		return
	}

	var query subtitleStreamQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		badRequest(w)
		return
	}
	if query.ItemID != nil {
		itemID, err = dto.ParseUUID(*query.ItemID)
		if err != nil {
			badRequest(w)
			return
		}
	}
	if query.Index != nil {
		if !validHLSHandlerInt(*query.Index) {
			badRequest(w)
			return
		}
		index = *query.Index
	}

	mediaSourceID := chi.URLParam(r, "routeMediaSourceId")
	if query.MediaSourceID != nil {
		mediaSourceID = *query.MediaSourceID
	}
	format := chi.URLParam(r, "routeFormat")
	if query.Format != nil {
		format = *query.Format
	}
	startPositionTicks := routeStartPositionTicks
	if query.StartPositionTicks != nil {
		startPositionTicks = *query.StartPositionTicks
	}
	endPositionTicks := int64(0)
	if query.EndPositionTicks != nil {
		endPositionTicks = *query.EndPositionTicks
	}
	copyTimestamps := false
	if query.CopyTimestamps != nil {
		copyTimestamps = *query.CopyTimestamps
	}
	addVTTTimeMap := false
	if query.AddVTTTimeMap != nil {
		addVTTTimeMap = *query.AddVTTTimeMap
	}

	response, err := h.streamer.Stream(r.Context(), playback.SubtitleStreamRequest{
		UserID:             identity.UserID,
		ItemID:             itemID,
		MediaSourceID:      mediaSourceID,
		Index:              index,
		Format:             format,
		StartPositionTicks: startPositionTicks,
		EndPositionTicks:   endPositionTicks,
		CopyTimestamps:     copyTimestamps,
		AddVTTTimeMap:      addVTTTimeMap,
	})
	if err != nil {
		subtitleStreamError(w, err, false)
		return
	}
	subtitleStreamResponse(w, response)
}

func subtitleStreamResponse(w http.ResponseWriter, response playback.SubtitleResponse) {
	w.Header().Set("Content-Type", response.ContentType)
	if response.File != nil {
		defer func() { _ = response.File.Close() }()
		w.Header().Set("Content-Length", strconv.FormatInt(response.Size, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, response.File)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(response.Body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response.Body)
}

func subtitleStreamError(w http.ResponseWriter, err error, playlist bool) {
	switch {
	case errors.Is(err, playback.ErrMediaSourceNotFound):
		internalError(w)
	case errors.Is(err, playback.ErrItemNotFound):
		if playlist {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		badRequest(w)
	case errors.Is(err, playback.ErrSubtitleStreamNotFound):
		internalError(w)
	case errors.Is(err, sourcefile.ErrStaleSource):
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
	case errors.Is(err, playback.ErrSubtitleInvalid):
		badRequest(w)
	case errors.Is(err, playback.ErrExtractionUnavailable):
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
	default:
		internalError(w)
	}
}
