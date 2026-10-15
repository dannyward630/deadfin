// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// itemCountsResponse is the wire response for GetItemCounts. All twelve
// integer fields serialize unconditionally.
type itemCountsResponse struct {
	MovieCount      int `json:"MovieCount"`
	SeriesCount     int `json:"SeriesCount"`
	EpisodeCount    int `json:"EpisodeCount"`
	ArtistCount     int `json:"ArtistCount"`
	ProgramCount    int `json:"ProgramCount"`
	TrailerCount    int `json:"TrailerCount"`
	SongCount       int `json:"SongCount"`
	AlbumCount      int `json:"AlbumCount"`
	MusicVideoCount int `json:"MusicVideoCount"`
	BoxSetCount     int `json:"BoxSetCount"`
	BookCount       int `json:"BookCount"`
	ItemCount       int `json:"ItemCount"`
}

// itemCountsRequest binds the query parameters for GetItemCounts.
type itemCountsRequest struct {
	IsFavorite *bool `query:"isFavorite"`
}

// itemCounts runs the GetItemCounts handler on the existing itemHandler so
// the route shares the user-resolution seam with the other item routes.
func (h itemHandler) itemCounts(w http.ResponseWriter, request *http.Request) {
	if h.reader == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	user, err := bind.QueryScalar(request, "userId")
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	var req itemCountsRequest
	if err := bind.QueryRequest(request, &req); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	falseValue := false
	zero := 0

	// kindBuckets maps each response field to its item.Kind.
	kindBuckets := []struct {
		field func(*itemCountsResponse, int)
		kind  item.Kind
	}{
		{func(r *itemCountsResponse, v int) { r.MovieCount = v }, item.KindMovie},
		{func(r *itemCountsResponse, v int) { r.SeriesCount = v }, item.KindSeries},
		{func(r *itemCountsResponse, v int) { r.EpisodeCount = v }, item.KindEpisode},
		{func(r *itemCountsResponse, v int) { r.ArtistCount = v }, item.KindMusicArtist},
		{func(r *itemCountsResponse, v int) { r.ProgramCount = v }, item.KindLiveTvProgram},
		{func(r *itemCountsResponse, v int) { r.TrailerCount = v }, item.KindTrailer},
		{func(r *itemCountsResponse, v int) { r.SongCount = v }, item.KindAudio},
		{func(r *itemCountsResponse, v int) { r.AlbumCount = v }, item.KindMusicAlbum},
		{func(r *itemCountsResponse, v int) { r.MusicVideoCount = v }, item.KindMusicVideo},
		{func(r *itemCountsResponse, v int) { r.BoxSetCount = v }, item.KindBoxSet},
		{func(r *itemCountsResponse, v int) { r.BookCount = v }, item.KindBook},
	}

	// countQuery builds a store.ItemQuery for one count probe.
	countQuery := func(includeTypes []item.Kind) store.ItemQuery {
		q := store.ItemQuery{
			Recursive:              true,
			IsVirtualItem:          &falseValue,
			Limit:                  &zero,
			EnableTotalRecordCount: true,
		}
		if req.IsFavorite != nil {
			q.IsFavorite = req.IsFavorite
		}
		q.IncludeItemTypes = includeTypes
		return q
	}

	applyUser := func(q *store.ItemQuery) bool {
		return h.applyRequestUser(w, request, user, q)
	}

	// Total count (no IncludeItemTypes filter).
	totalQuery := countQuery(nil)
	if !applyUser(&totalQuery) {
		return
	}
	totalResult, err := h.reader.Query(request.Context(), totalQuery)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	resp := itemCountsResponse{ItemCount: totalResult.TotalRecordCount}

	for _, bucket := range kindBuckets {
		q := countQuery([]item.Kind{bucket.kind})
		if !applyUser(&q) {
			return
		}
		result, err := h.reader.Query(request.Context(), q)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		bucket.field(&resp, result.TotalRecordCount)
	}

	_ = render.JSON(w, request, http.StatusOK, resp)
}
