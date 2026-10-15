// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/go-chi/chi/v5"
)

// NextUpReader owns Jellyfin's grouped watch-history selection before the
// shared builder projects its resulting episode page.
type NextUpReader interface {
	NextUp(context.Context, store.NextUpQuery) (store.NextUpResult, error)
}

// ShowsConfiguration supplies the server-wide preference controlling whether
// positioned season-zero episodes participate in NextUp selection.
type ShowsConfiguration interface {
	System() config.ServerConfiguration
}

// RegisterShows registers TV-series operations using real selection services.
func RegisterShows(rt *router.Router, reader ItemReader, build ItemDTOBuilder, nextUp NextUpReader, users ItemUserResolver, clock clock.Clock, configurations ...ShowsConfiguration) {
	if clock == nil {
		panic("handlers: show clock is required")
	}
	var configuration ShowsConfiguration
	if len(configurations) != 0 {
		configuration = configurations[0]
	}
	h := showsHandler{reader: reader, build: build, nextUp: nextUp, users: users, clock: clock, configuration: configuration}
	policy := string(auth.PolicyDefaultAuthorization)
	rt.Handle(http.MethodGet, "/Shows/NextUp", router.Route{OperationID: "GetNextUp", Policy: policy}, h.next)
	rt.Handle(http.MethodGet, "/Shows/{seriesId}/Episodes", router.Route{OperationID: "GetEpisodes", Policy: policy}, h.episodes)
	rt.Handle(http.MethodGet, "/Shows/{seriesId}/Seasons", router.Route{OperationID: "GetSeasons", Policy: policy}, h.seasons)
}

type showsHandler struct {
	reader        ItemReader
	build         ItemDTOBuilder
	nextUp        NextUpReader
	users         ItemUserResolver
	clock         clock.Clock
	configuration ShowsConfiguration
}

type showsRequest struct {
	UserID          *dto.UUID `query:"UserId"`
	Fields          []string  `query:"Fields"`
	Season          *int      `query:"Season"`
	SeasonID        *dto.UUID `query:"SeasonId"`
	IsSpecialSeason *bool     `query:"IsSpecialSeason"`
	IsMissing       *bool     `query:"IsMissing"`
	AdjacentTo      *dto.UUID `query:"AdjacentTo"`
	StartItemID     *dto.UUID `query:"StartItemId"`
}

type nextUpRequest struct {
	UserID           *dto.UUID `query:"UserId"`
	SeriesID         *dto.UUID `query:"SeriesId"`
	ParentID         *dto.UUID `query:"ParentId"`
	Fields           []string  `query:"Fields"`
	NextUpDateCutoff *dto.Time `query:"NextUpDateCutoff"`
	EnableResumable  *bool     `query:"EnableResumable"`
	EnableRewatching *bool     `query:"EnableRewatching"`
}

func (h showsHandler) next(w http.ResponseWriter, r *http.Request) {
	if h.nextUp == nil || h.build == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	query, err := bind.ItemsQueryRequest(r, h.clock)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	var request nextUpRequest
	if err := bind.QueryRequest(r, &request); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	user, ok := h.nextUpUser(w, r, request.UserID)
	if !ok {
		return
	}
	resumable, rewatching := true, false
	if request.EnableResumable != nil {
		resumable = *request.EnableResumable
	}
	if request.EnableRewatching != nil {
		rewatching = *request.EnableRewatching
	}
	includeSpecials := h.displaySpecialsWithinSeasons()
	result, err := h.nextUp.NextUp(r.Context(), store.NextUpQuery{User: &user, UserID: user.ID, SeriesID: request.SeriesID, ParentID: request.ParentID, StartIndex: query.StartIndex, Limit: query.Limit, NextUpDateCutoff: request.NextUpDateCutoff.StdPtr(), EnableTotalRecordCount: query.EnableTotalRecordCount, EnableResumable: resumable, EnableRewatching: rewatching, IncludeSpecials: includeSpecials, ExcludedTopParentIDs: latestExcludedRootIDs(&user)})
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	result.Items = filterLatestExcludedItems(result.Items, &user)
	query.DtoOptions.Fields = bind.ItemFields("Fields", request.Fields)
	page, err := h.build.Build(r.Context(), result.Items, query.DtoOptions, &user.ID)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	page.TotalRecordCount = result.TotalRecordCount
	if query.StartIndex != nil {
		page.StartIndex = *query.StartIndex
	}
	_ = render.JSON(w, r, http.StatusOK, page)
}

func (h showsHandler) nextUpUser(w http.ResponseWriter, r *http.Request, requested *dto.UUID) (store.User, bool) {
	if isGlobalAPIKeyItemRequest(r, nextUpRequestedUser(requested)) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return store.User{}, false
	}
	if h.users == nil {
		if requested == nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return store.User{}, false
		}
		return store.User{ID: *requested}, true
	}
	if _, authenticated := middleware.IdentityFromContext(r.Context()); authenticated {
		value := ""
		if requested != nil {
			value = requested.Dashed()
		}
		return resolveItemUser(w, r, value, h.users)
	}
	if requested == nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return store.User{}, false
	}
	user, err := h.users.Get(r.Context(), *requested)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return store.User{}, false
	}
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return store.User{}, false
	}
	return user, true
}

func nextUpRequestedUser(requested *dto.UUID) string {
	if requested == nil {
		return ""
	}
	return requested.Dashed()
}

func (h showsHandler) seasons(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil || h.build == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	seriesID, query, request, ok := h.showsRequestForRoute(w, r)
	if !ok {
		return
	}
	exists, err := h.seriesExists(r, seriesID, request.UserID)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}

	query.UserID = request.UserID
	query.IncludeItemTypes = []item.Kind{item.KindSeason}
	query.ParentID = &seriesID
	query.IsMissing = request.IsMissing
	query.IsSpecialSeason = request.IsSpecialSeason
	query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
	query.SortOrder = []store.SortOrder{store.SortOrderAscending}
	query.EnableTotalRecordCount = false
	result, err := h.reader.Query(r.Context(), query)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	values := adjacentShowItems(result.Items, request.AdjacentTo)
	page, err := h.build.Build(r.Context(), values, query.DtoOptions, query.UserID)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	page.TotalRecordCount = len(values)
	_ = render.JSON(w, r, http.StatusOK, page)
}

func (h showsHandler) episodes(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil || h.build == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	seriesID, query, request, ok := h.showsRequestForRoute(w, r)
	if !ok {
		return
	}

	// A season's episodes are not only its folder's children: upstream
	// Season.GetEpisodes also takes the series' episodes linked to the season
	// and, for a numbered season, its root-folder episodes of that number
	// (Series.FilterEpisodesBySeason). A virtual season has no folder.
	var seasonID *dto.UUID
	scopeSeason := func(season item.Item) {
		id := season.ID
		seasonID = &id
	}
	switch {
	case request.SeasonID != nil:
		season, exists, err := h.itemOfKind(r, *request.SeasonID, item.KindSeason, request.UserID)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if !exists {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		scopeSeason(season)
	case request.Season != nil:
		exists, err := h.seriesExists(r, seriesID, request.UserID)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if !exists {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		season, found, err := h.seasonForNumber(r, seriesID, *request.Season, request.UserID)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if !found {
			_ = render.JSON(w, r, http.StatusOK, emptyShowPage())
			return
		}
		scopeSeason(season)
	default:
		exists, err := h.seriesExists(r, seriesID, request.UserID)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if !exists {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		query.AncestorIDs = []dto.UUID{seriesID}
	}

	randomOrder := len(query.SortBy) == 1 && query.SortBy[0] == store.ItemSortByRandom
	start, limit := query.StartIndex, query.Limit
	query.UserID = request.UserID
	query.IncludeItemTypes = []item.Kind{item.KindEpisode}
	query.ParentID = nil
	query.EpisodesOfSeasonID = seasonID
	query.ExcludeSpecialsFromSeasons = !h.displaySpecialsWithinSeasons()
	query.IsMissing = request.IsMissing
	query.EnableTotalRecordCount = false
	query.StartIndex = nil
	query.Limit = nil
	if !randomOrder {
		query.SortBy = []store.ItemSortBy{store.ItemSortByParentIndexNumber, store.ItemSortByIndexNumber}
		query.SortOrder = []store.SortOrder{store.SortOrderAscending, store.SortOrderAscending}
	}
	result, err := h.reader.Query(r.Context(), query)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	cursor, err := h.primaryStartItemID(r, request.StartItemID, request.UserID)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	values := startShowItems(result.Items, cursor)
	values = adjacentShowItems(values, request.AdjacentTo)
	if randomOrder {
		randomShowItems(values)
	}
	total := len(values)
	values = pageShowItems(values, start, limit)
	page, err := h.build.Build(r.Context(), values, query.DtoOptions, query.UserID)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	page.TotalRecordCount = total
	if start != nil {
		page.StartIndex = *start
	}
	_ = render.JSON(w, r, http.StatusOK, page)
}

func (h showsHandler) seriesExists(r *http.Request, id dto.UUID, userID *dto.UUID) (bool, error) {
	return h.itemOfKindExists(r, id, item.KindSeries, userID)
}

func (h showsHandler) itemOfKindExists(r *http.Request, id dto.UUID, kind item.Kind, userID *dto.UUID) (bool, error) {
	_, exists, err := h.itemOfKind(r, id, kind, userID)
	return exists, err
}

func (h showsHandler) itemOfKind(r *http.Request, id dto.UUID, kind item.Kind, userID *dto.UUID) (item.Item, bool, error) {
	result, err := h.reader.Query(r.Context(), store.ItemQuery{ItemIDs: []dto.UUID{id}, IncludeItemTypes: []item.Kind{kind}, UserID: userID, EnableTotalRecordCount: false})
	if err != nil || len(result.Items) == 0 {
		return item.Item{}, false, err
	}
	return result.Items[0], true, nil
}

func (h showsHandler) seasonForNumber(r *http.Request, seriesID dto.UUID, number int, userID *dto.UUID) (item.Item, bool, error) {
	result, err := h.reader.Query(r.Context(), store.ItemQuery{ParentID: &seriesID, IncludeItemTypes: []item.Kind{item.KindSeason}, UserID: userID, EnableTotalRecordCount: false, SortBy: []store.ItemSortBy{store.ItemSortBySortName}, SortOrder: []store.SortOrder{store.SortOrderAscending}})
	if err != nil {
		return item.Item{}, false, err
	}
	for _, value := range result.Items {
		if value.IndexNumber != nil && *value.IndexNumber == number {
			return value, true, nil
		}
	}
	return item.Item{}, false, nil
}

// displaySpecialsWithinSeasons reads the server option; its upstream default
// is true.
func (h showsHandler) displaySpecialsWithinSeasons() bool {
	if h.configuration == nil {
		return true
	}
	return h.configuration.System().DisplaySpecialsWithinSeasons
}

func (h showsHandler) showsRequestForRoute(w http.ResponseWriter, r *http.Request) (dto.UUID, store.ItemQuery, showsRequest, bool) {
	seriesID, err := dto.ParseUUID(chi.URLParam(r, "seriesId"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return dto.NilUUID, store.ItemQuery{}, showsRequest{}, false
	}
	query, err := bind.ItemsQueryRequest(r, h.clock)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return dto.NilUUID, store.ItemQuery{}, showsRequest{}, false
	}
	var request showsRequest
	if err := bind.QueryRequest(r, &request); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return dto.NilUUID, store.ItemQuery{}, showsRequest{}, false
	}
	if h.users != nil {
		requested := ""
		if request.UserID != nil {
			requested = request.UserID.Dashed()
		}
		if _, authenticated := middleware.IdentityFromContext(r.Context()); authenticated && !isGlobalAPIKeyItemRequest(r, requested) {
			user, ok := resolveItemUser(w, r, requested, h.users)
			if !ok {
				return dto.NilUUID, store.ItemQuery{}, showsRequest{}, false
			}
			applyItemUser(&query, user)
			request.UserID = &user.ID
		} else if isGlobalAPIKeyItemRequest(r, requested) {
			query.UserID = nil
			request.UserID = nil
		} else {
			query.UserID = request.UserID
		}
	} else {
		query.UserID = request.UserID
	}
	query.DtoOptions.Fields = bind.ItemFields("Fields", request.Fields)
	return seriesID, query, request, true
}

func (h showsHandler) primaryStartItemID(r *http.Request, startItemID, userID *dto.UUID) (*dto.UUID, error) {
	if startItemID == nil {
		return nil, nil
	}
	result, err := h.reader.Query(r.Context(), store.ItemQuery{ItemIDs: []dto.UUID{*startItemID}, IncludeItemTypes: []item.Kind{item.KindEpisode}, UserID: userID, EnableTotalRecordCount: false})
	if err != nil || len(result.Items) == 0 || result.Items[0].PrimaryVersionID == nil {
		return startItemID, err
	}
	return result.Items[0].PrimaryVersionID, nil
}

func adjacentShowItems(values []item.Item, adjacentTo *dto.UUID) []item.Item {
	if adjacentTo == nil {
		return values
	}
	index := -1
	for i := range values {
		if values[i].ID == *adjacentTo {
			index = i
			break
		}
	}
	if index < 0 {
		return nil
	}
	start, end := index, index+1
	if start > 0 {
		start--
	}
	if end < len(values) {
		end++
	}
	return values[start:end]
}

func startShowItems(values []item.Item, startItemID *dto.UUID) []item.Item {
	if startItemID == nil {
		return values
	}
	for index := range values {
		if values[index].ID == *startItemID {
			return values[index:]
		}
	}
	return nil
}

func randomShowItems(values []item.Item) {
	rand.Shuffle(len(values), func(left, right int) {
		values[left], values[right] = values[right], values[left]
	})
}

func pageShowItems(values []item.Item, start, limit *int) []item.Item {
	if start != nil {
		if *start >= len(values) {
			return nil
		}
		values = values[*start:]
	}
	if limit != nil && *limit < len(values) {
		values = values[:*limit]
	}
	return values
}

func emptyShowPage() dto.QueryResult[dto.BaseItemDto] {
	return dto.NewQueryResult[dto.BaseItemDto]()
}
