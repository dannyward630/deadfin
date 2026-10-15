// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/go-chi/chi/v5"
)

const latestItemsDefaultLimit = 20

// LatestUserPreferences supplies the one persisted user preference consulted
// by UserLibraryController.GetLatestMedia when isPlayed is omitted.
type LatestUserPreferences interface {
	HidePlayedInLatest(context.Context, dto.UUID) (bool, error)
}

type latestUserReader interface {
	Get(context.Context, dto.UUID) (store.User, error)
}

// NewLatestUserPreferences adapts the existing user-service read seam without
// exposing user configuration parsing to the HTTP handler.
func NewLatestUserPreferences(users latestUserReader) LatestUserPreferences {
	return latestUserPreferences{users: users}
}

type latestUserPreferences struct{ users latestUserReader }

func (preferences latestUserPreferences) HidePlayedInLatest(ctx context.Context, userID dto.UUID) (bool, error) {
	if preferences.users == nil {
		return false, errors.New("latest user preferences: user reader is required")
	}
	user, err := preferences.users.Get(ctx, userID)
	if err != nil {
		return false, err
	}
	configuration := dto.DefaultUserConfiguration()
	if len(user.Configuration) != 0 {
		if err := json.Unmarshal(user.Configuration, &configuration); err != nil {
			return false, err
		}
	}
	return configuration.HidePlayedInLatest, nil
}

type latestItemsRequest struct {
	UserID           *dto.UUID `query:"userId"`
	ParentID         *dto.UUID `query:"parentId"`
	Fields           []string  `query:"fields"`
	IncludeItemTypes []string  `query:"includeItemTypes"`
	IsPlayed         *bool     `query:"isPlayed"`
	EnableImages     *bool     `query:"enableImages"`
	ImageTypeLimit   *int      `query:"imageTypeLimit"`
	EnableImageTypes []string  `query:"enableImageTypes"`
	EnableUserData   *bool     `query:"enableUserData"`
	Limit            *int      `query:"limit"`
	GroupItems       *bool     `query:"groupItems"`
}

// latest serves UserLibraryController.GetLatestMedia. RegisterItems owns the
// route ledger; its shared registration constructs this handler and registers
// the two methods below.
func (h itemHandler) latest(w http.ResponseWriter, r *http.Request) {
	h.latestForUser(w, r, "")
}

// legacyLatest serves UserLibraryController.GetLatestMediaLegacy. The route
// user deliberately wins over a conflicting query userId.
func (h itemHandler) legacyLatest(w http.ResponseWriter, r *http.Request) {
	h.latestForUser(w, r, chi.URLParam(r, "userId"))
}

func (h itemHandler) latestForUser(w http.ResponseWriter, r *http.Request, routeUser string) {
	if h.reader == nil || h.build == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	var request latestItemsRequest
	if err := bind.QueryRequest(r, &request); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	query, limit, groupItems, err := latestItemQuery(request)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	requestedUser := ""
	if routeUser != "" {
		requestedUser = routeUser
	} else if request.UserID != nil {
		requestedUser = request.UserID.Dashed()
	}
	if !h.applyRequestUser(w, r, requestedUser, &query) {
		return
	}
	if query.IsPlayed == nil && query.UserID != nil {
		if h.latestPreferences == nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		hidePlayed, err := h.latestPreferences.HidePlayedInLatest(r.Context(), *query.UserID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		case err != nil:
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		case hidePlayed:
			isPlayed := false
			query.IsPlayed = &isPlayed
		}
	}
	if excluded := latestExcludedRootIDs(query.User); len(excluded) != 0 {
		roots, err := h.reader.Query(r.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindCollectionFolder}, EnableTotalRecordCount: false})
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		query.TopParentIDs = allowedLatestRoots(roots.Items, excluded)
	}

	result, err := h.latestCandidates(r.Context(), query, limit, groupItems)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	result.Items = filterLatestExcludedItems(result.Items, query.User)
	items, childCounts, err := h.composeLatest(r, result.Items, limit, groupItems)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	page, err := h.build.Build(r.Context(), items, query.DtoOptions, query.UserID)
	if err != nil || len(page.Items) != len(items) {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	for index, count := range childCounts {
		if count == 0 {
			continue
		}
		value := count
		page.Items[index].ChildCount = &value
	}
	page.Items = projectLatestSubtitleScores(page.Items)
	if page.Items == nil {
		page.Items = make([]dto.BaseItemDto, 0)
	}
	_ = render.JSON(w, r, http.StatusOK, page.Items)
}

// latestItemReader is the grouped query behind GetLatestItemList.
type latestItemReader interface {
	LatestItems(context.Context, store.ItemQuery) ([]item.Item, bool, error)
}

// latestCandidates ports the end of UserViewManager.GetItemsForLatestItems.
// With GroupItems and a tvshows, movies, or music parent, the repository
// groups first and returns at most limit groups. Otherwise the candidates are
// the newest limit*2 items, which GetLatestItems groups afterwards.
func (h itemHandler) latestCandidates(ctx context.Context, query store.ItemQuery, limit int, groupItems bool) (store.ItemQueryResult, error) {
	if latest, ok := h.reader.(latestItemReader); ok && groupItems {
		grouped := query
		grouped.Limit = &limit
		values, found, err := latest.LatestItems(ctx, grouped)
		if err != nil {
			return store.ItemQueryResult{}, err
		}
		if found {
			return store.ItemQueryResult{Items: values, TotalRecordCount: len(values)}, nil
		}
	}
	return h.reader.Query(ctx, query)
}

// projectLatestSubtitleScores applies Jellyfin's static subtitle score policy
// only to the latest-route response. It clones DTO rows and stream slices only
// when a score changes, leaving builder-owned projections and nested sources intact.
func projectLatestSubtitleScores(items []dto.BaseItemDto) []dto.BaseItemDto {
	projectedItems := items
	copied := false
	for itemIndex := range items {
		streams, changed := projectLatestTopLevelSubtitleScores(items[itemIndex].MediaStreams)
		if !changed {
			continue
		}
		if !copied {
			projectedItems = append([]dto.BaseItemDto(nil), items...)
			copied = true
		}
		projectedItems[itemIndex].MediaStreams = streams
	}
	return projectedItems
}

func projectLatestTopLevelSubtitleScores(values []dto.MediaStreamInfo) ([]dto.MediaStreamInfo, bool) {
	var projected []dto.MediaStreamInfo
	for index := range values {
		stream := values[index]
		if stream.Type != dto.MediaStreamType("Subtitle") {
			continue
		}
		codec := ""
		if stream.Codec != nil {
			codec = *stream.Codec
		}
		candidate := playback.MediaStream{
			Codec:      codec,
			Type:       playback.StreamTypeSubtitle,
			IsDefault:  stream.IsDefault != nil && *stream.IsDefault,
			IsForced:   stream.IsForced != nil && *stream.IsForced,
			IsExternal: stream.IsExternal != nil && *stream.IsExternal,
		}
		if !playback.ApplyStaticSubtitleScore(&candidate) {
			continue
		}
		if stream.Score != nil && *stream.Score == *candidate.Score {
			continue
		}
		if projected == nil {
			projected = append([]dto.MediaStreamInfo(nil), values...)
		}
		projected[index].Score = candidate.Score
	}
	return projected, projected != nil
}

func latestItemQuery(request latestItemsRequest) (store.ItemQuery, int, bool, error) {
	fields := latestItemFields(request.Fields)
	includeKinds := bind.DelimitedEnums("includeItemTypes", request.IncludeItemTypes, bind.ParseKind)
	imageTypes := bind.DelimitedEnums("enableImageTypes", request.EnableImageTypes, bind.ParseImageType)

	limit := latestItemsDefaultLimit
	if request.Limit != nil {
		limit = *request.Limit
	}
	if limit < 0 || limit > math.MaxInt/2 {
		return store.ItemQuery{}, 0, false, fmt.Errorf("invalid latest limit")
	}
	groupItems := true
	if request.GroupItems != nil {
		groupItems = *request.GroupItems
	}

	falseValue := false
	options := bind.DefaultItemQueryDTOOptions()
	options.Fields = fields
	options.ImageTypes = latestAllImageTypes
	options.ImageTypeLimit = math.MaxInt
	options.AddCurrentProgram = true
	if request.EnableImages != nil {
		options.EnableImages = *request.EnableImages
	}
	if request.EnableUserData != nil {
		options.EnableUserData = *request.EnableUserData
	}
	if request.ImageTypeLimit != nil {
		options.ImageTypeLimit = *request.ImageTypeLimit
	}
	if len(imageTypes) != 0 {
		options.ImageTypes = imageTypes
	}

	candidateLimit := limit * 2
	// GetLatestItems queries the parent's descendants (GetItemList with the
	// parent as an ancestor), not only its direct children: episodes sit
	// under series and seasons, and most movies under their own folders.
	query := store.ItemQuery{
		ParentID:               request.ParentID,
		Recursive:              true,
		IncludeItemTypes:       includeKinds,
		IsPlayed:               request.IsPlayed,
		IsVirtualItem:          &falseValue,
		Limit:                  &candidateLimit,
		EnableTotalRecordCount: false,
		OrderBy: []store.ItemOrder{
			{SortBy: store.ItemSortByDateCreated, SortOrder: store.SortOrderDescending},
			{SortBy: store.ItemSortBySortName, SortOrder: store.SortOrderDescending},
			{SortBy: store.ItemSortByProductionYear, SortOrder: store.SortOrderDescending},
		},
		DtoOptions: options,
	}
	if len(includeKinds) == 0 {
		query.IsFolder = &falseValue
	}
	return query, limit, groupItems, nil
}

// composeLatest implements UserViewManager.GetLatestItems after the source
// ordered candidate list is available. Grouping is deliberately per source
// item's LatestItemsIndexContainer, not ItemQuery's alternate-version grouping.
func (h itemHandler) composeLatest(r *http.Request, candidates []item.Item, limit int, groupItems bool) ([]item.Item, []int, error) {
	if !groupItems {
		if len(candidates) > limit {
			candidates = candidates[:limit]
		}
		return candidates, make([]int, len(candidates)), nil
	}

	containers, err := h.latestContainers(r, candidates)
	if err != nil {
		return nil, nil, err
	}
	entries := make([]latestEntry, 0, min(limit, len(candidates)))
	entryByContainer := make(map[dto.UUID]int)
	for _, candidate := range candidates {
		container, hasContainer := containers[candidate.ID]
		if !hasContainer || candidate.IsFolder {
			entries = append(entries, latestEntry{item: candidate})
		} else if index, found := entryByContainer[container.ID]; found {
			entries[index].children++
			if entries[index].children > 1 {
				entries[index].item = container
			}
		} else {
			entryByContainer[container.ID] = len(entries)
			entry := latestEntry{item: candidate, container: container, children: 1}
			if container.Kind == item.KindMusicAlbum {
				entry.item = container
			}
			entries = append(entries, entry)
		}
		if len(entries) >= limit {
			break
		}
	}

	items := make([]item.Item, len(entries))
	childCounts := make([]int, len(entries))
	for index, entry := range entries {
		items[index] = entry.item
		if entry.container.ID.IsNil() {
			continue
		}
		if entry.children > 1 || entry.container.Kind == item.KindMusicAlbum {
			childCounts[index] = entry.children
		}
	}
	return items, childCounts, nil
}

type latestEntry struct {
	item      item.Item
	container item.Item
	children  int
}

func (h itemHandler) latestContainers(r *http.Request, candidates []item.Item) (map[dto.UUID]item.Item, error) {
	containers := make(map[dto.UUID]item.Item)
	parents := make(map[dto.UUID]item.Item)
	pending := make(map[dto.UUID]struct{})
	targets := make(map[dto.UUID]dto.UUID)
	candidateByID := make(map[dto.UUID]item.Item, len(candidates))
	for _, candidate := range candidates {
		candidateByID[candidate.ID] = candidate
		switch candidate.Kind {
		case item.KindEpisode:
			if candidate.SeriesID != nil && !candidate.SeriesID.IsNil() {
				targets[candidate.ID] = *candidate.SeriesID
				pending[*candidate.SeriesID] = struct{}{}
			}
		case item.KindAudio, item.KindPhoto:
			if candidate.ParentID != nil && !candidate.ParentID.IsNil() {
				targets[candidate.ID] = *candidate.ParentID
				pending[*candidate.ParentID] = struct{}{}
			}
		}
	}
	for len(pending) != 0 {
		ids := make([]dto.UUID, 0, len(pending))
		for id := range pending {
			ids = append(ids, id)
		}
		pending = make(map[dto.UUID]struct{})
		result, err := h.reader.Query(r.Context(), store.ItemQuery{ItemIDs: ids, EnableTotalRecordCount: false})
		if err != nil {
			return nil, err
		}
		for _, parent := range result.Items {
			parents[parent.ID] = parent
		}
		for candidateID, targetID := range targets {
			candidate := candidateByID[candidateID]
			parent, found := parents[targetID]
			if !found {
				continue
			}
			switch candidate.Kind {
			case item.KindEpisode:
				if parent.Kind == item.KindSeries {
					containers[candidateID] = parent
				}
			case item.KindAudio:
				if parent.Kind == item.KindMusicAlbum {
					containers[candidateID] = parent
				} else if parent.ParentID != nil && !parent.ParentID.IsNil() {
					targets[candidateID] = *parent.ParentID
					if _, known := parents[*parent.ParentID]; !known {
						pending[*parent.ParentID] = struct{}{}
					}
				}
			case item.KindPhoto:
				if parent.Kind == item.KindPhotoAlbum {
					containers[candidateID] = parent
				} else if parent.ParentID != nil && !parent.ParentID.IsNil() {
					targets[candidateID] = *parent.ParentID
					if _, known := parents[*parent.ParentID]; !known {
						pending[*parent.ParentID] = struct{}{}
					}
				}
			}
		}
	}
	return containers, nil
}

// latestItemFields keeps the default field set when the request names no
// fields. A list of only unknown values binds as an empty list, as upstream
// does.
func latestItemFields(values []string) []string {
	if len(values) == 0 {
		return append([]string(nil), latestDefaultItemFields...)
	}
	return bind.ItemFields("fields", values)
}

var latestDefaultItemFields = []string{
	"AirTime", "CanDelete", "CanDownload", "ChannelInfo", "Chapters", "Trickplay", "ChildCount", "CumulativeRunTimeTicks", "CustomRating", "DateCreated", "DateLastMediaAdded", "DisplayPreferencesId", "Etag", "ExternalUrls", "Genres", "ItemCounts", "MediaSourceCount", "MediaSources", "OriginalTitle", "Overview", "ParentId", "Path", "People", "PlayAccess", "ProductionLocations", "ProviderIds", "PrimaryImageAspectRatio", "RecursiveItemCount", "Settings", "SeriesStudio", "SortName", "SpecialEpisodeNumbers", "Studios", "Taglines", "Tags", "RemoteTrailers", "MediaStreams", "DateLastRefreshed", "DateLastSaved", "ChannelImage", "EnableMediaSourceDisplay", "Width", "Height", "ExtraIds", "LocalTrailerCount", "IsHD", "SpecialFeatureCount",
}

var latestAllImageTypes = []item.ImageType{
	item.ImageTypePrimary, item.ImageTypeArt, item.ImageTypeBackdrop, item.ImageTypeBanner, item.ImageTypeLogo, item.ImageTypeThumb, item.ImageTypeDisc, item.ImageTypeBox, item.ImageTypeScreenshot, item.ImageTypeMenu, item.ImageTypeChapter, item.ImageTypeBoxRear, item.ImageTypeProfile,
}

func latestExcludedRootIDs(user *store.User) []dto.UUID {
	if user == nil || len(user.Configuration) == 0 {
		return nil
	}
	var configuration struct{ LatestItemsExcludes []dto.UUID }
	if json.Unmarshal(user.Configuration, &configuration) != nil || len(configuration.LatestItemsExcludes) == 0 {
		return nil
	}
	return configuration.LatestItemsExcludes
}

func allowedLatestRoots(values []item.Item, excluded []dto.UUID) []dto.UUID {
	excludedSet := make(map[dto.UUID]struct{}, len(excluded))
	for _, id := range excluded {
		excludedSet[id] = struct{}{}
	}
	roots := make([]dto.UUID, 0, len(values))
	for _, value := range values {
		if _, found := excludedSet[value.ID]; !found {
			roots = append(roots, value.ID)
		}
	}
	return roots
}

func filterLatestExcludedItems(values []item.Item, user *store.User) []item.Item {
	excludedRoots := latestExcludedRootIDs(user)
	if len(excludedRoots) == 0 || len(values) == 0 {
		return values
	}
	excluded := make(map[dto.UUID]struct{}, len(excludedRoots))
	for _, id := range excludedRoots {
		excluded[id] = struct{}{}
	}
	filtered := make([]item.Item, 0, len(values))
	for _, value := range values {
		if _, found := excluded[value.ID]; found {
			continue
		}
		if value.ParentID != nil {
			if _, found := excluded[*value.ParentID]; found {
				continue
			}
		}
		if value.TopParentID != nil {
			if _, found := excluded[*value.TopParentID]; found {
				continue
			}
		}
		filtered = append(filtered, value)
	}
	return filtered
}
