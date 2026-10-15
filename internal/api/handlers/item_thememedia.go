// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// themeMediaQuery carries the GetThemeMedia query parameters.
type themeMediaQuery struct {
	UserID            *dto.UUID `query:"userId"`
	InheritFromParent *bool     `query:"inheritFromParent"`
	SortBy            []string  `query:"sortBy"`
	SortOrder         []string  `query:"sortOrder"`
}

// themeMedia returns theme songs and theme videos for the requested item. It
// mirrors the stock LibraryController.GetThemeMedia walk: when the item has no
// theme extras and inheritFromParent is true, the handler steps to the parent
// and queries again. Each sub-result names the owner that supplied the themes.
func (h itemExtrasHandler) themeMedia(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil || h.build == nil {
		internalItemExtrasError(w)
		return
	}
	user, ok := h.itemExtrasUser(w, r, nil)
	if !ok {
		return
	}
	itemID, ok := itemExtrasItemID(w, r)
	if !ok {
		return
	}
	source, ok := h.source(w, r, itemID, user)
	if !ok {
		return
	}
	query := themeMediaQuery{}
	if err := bind.QueryRequest(r, &query); err != nil {
		badItemExtrasRequest(w)
		return
	}
	sorts, orders := themeMediaSorts(query)
	inherit := query.InheritFromParent != nil && *query.InheritFromParent
	userID := itemExtrasUserID(user)

	themeSongs := h.themeMediaResult(r, source, item.ExtraTypeThemeSong, inherit, user, userID, sorts, orders)
	themeVideos := h.themeMediaResult(r, source, item.ExtraTypeThemeVideo, inherit, user, userID, sorts, orders)
	soundtrack := dto.ThemeMediaResult{QueryResult: dto.NewQueryResult[dto.BaseItemDto]()}
	response := dto.AllThemeMediaResult{
		ThemeVideosResult:     themeVideos,
		ThemeSongsResult:      themeSongs,
		SoundtrackSongsResult: soundtrack,
	}
	_ = render.JSON(w, r, http.StatusOK, response)
}

func themeMediaSorts(query themeMediaQuery) ([]store.ItemSortBy, []store.SortOrder) {
	sorts := bind.DelimitedEnums("sortBy", query.SortBy, bind.ParseText[store.ItemSortBy])
	orders := bind.DelimitedEnums("sortOrder", query.SortOrder, bind.ParseText[store.SortOrder])
	if len(sorts) == 0 && len(orders) == 0 {
		return []store.ItemSortBy{store.ItemSortBySortName}, []store.SortOrder{store.SortOrderAscending}
	}
	return sorts, orders
}

// themeMediaResult queries extras of one type for the owner. When the result
// is empty and inheritFromParent is true, the handler steps to the parent by
// ParentID with a UserID-only lookup and queries again.
func (h itemExtrasHandler) themeMediaResult(r *http.Request, source item.Item, extraType item.ExtraType, inherit bool, user *store.User, userID *dto.UUID, sorts []store.ItemSortBy, orders []store.SortOrder) dto.ThemeMediaResult {
	owner := source
	for {
		query := store.ItemQuery{
			OwnerIDs:   []dto.UUID{owner.ID},
			ExtraTypes: []item.ExtraType{extraType},
			User:       user,
			UserID:     userID,
			SortBy:     sorts,
			SortOrder:  orders,
		}
		result, err := h.reader.Query(r.Context(), query)
		if err != nil || len(result.Items) == 0 {
			if !inherit || owner.ParentID == nil || *owner.ParentID == dto.NilUUID {
				return dto.ThemeMediaResult{QueryResult: dto.NewQueryResult[dto.BaseItemDto](), OwnerID: owner.ID}
			}
			parentQuery := store.ItemQuery{ItemIDs: []dto.UUID{*owner.ParentID}, UserID: userID}
			parentResult, pErr := h.reader.Query(r.Context(), parentQuery)
			if pErr != nil || len(parentResult.Items) == 0 {
				return dto.ThemeMediaResult{QueryResult: dto.NewQueryResult[dto.BaseItemDto](), OwnerID: owner.ID}
			}
			owner = parentResult.Items[0]
			continue
		}
		page, err := h.build.Build(r.Context(), result.Items, store.ItemQueryDTOOptions{}, userID)
		if err != nil {
			return dto.ThemeMediaResult{QueryResult: dto.NewQueryResult[dto.BaseItemDto](), OwnerID: owner.ID}
		}
		if page.Items == nil {
			page.Items = make([]dto.BaseItemDto, 0)
		}
		return dto.ThemeMediaResult{
			QueryResult: dto.QueryResult[dto.BaseItemDto]{Items: page.Items, TotalRecordCount: len(page.Items), StartIndex: 0},
			OwnerID:     owner.ID,
		}
	}
}
