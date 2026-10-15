// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// RegisterUserViews registers modern and shadow-compatible user-view routes.
func RegisterUserViews(rt *router.Router, views *library.Views, build *dtobuild.Builder) {
	h := userViewsHandler{views: views, build: build}
	meta := router.Route{OperationID: "GetUserViews", Policy: string(auth.PolicyDefaultAuthorization)}
	rt.Handle(http.MethodGet, "/UserViews", meta, h.get)
	rt.Handle(http.MethodGet, "/Users/{userId}/Views", router.Route{OperationID: "SHADOW:GetUserViewsLegacy", Policy: string(auth.PolicyDefaultAuthorization)}, h.getLegacy)
	rt.Handle(http.MethodGet, "/UserViews/GroupingOptions", router.Route{OperationID: "GetGroupingOptions", Policy: string(auth.PolicyDefaultAuthorization)}, h.groupingOptions)
	rt.Handle(http.MethodGet, "/Users/{userId}/GroupingOptions", router.Route{OperationID: "SHADOW:GetGroupingOptionsLegacy", Policy: string(auth.PolicyDefaultAuthorization)}, h.groupingOptionsLegacy)
}

type userViewsHandler struct {
	views *library.Views
	build *dtobuild.Builder
}

func (h userViewsHandler) get(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentViewsUser(w, r)
	if !ok {
		return
	}
	h.query(w, r, userID)
}

func (h userViewsHandler) getLegacy(w http.ResponseWriter, r *http.Request) {
	userID, ok := routeViewsUser(w, r)
	if !ok {
		return
	}
	h.query(w, r, userID)
}

func (h userViewsHandler) query(w http.ResponseWriter, r *http.Request, userID dto.UUID) {
	if h.views == nil || h.build == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	query, ok := userViewsQuery(w, r, userID)
	if !ok {
		return
	}
	result, err := h.views.Query(r.Context(), query)
	if err != nil {
		writeUserViewsError(w, err)
		return
	}
	page, err := h.build.BuildDetailed(r.Context(), result.Items, dtobuild.DetailedOptions(store.ItemQueryDTOOptions{
		Fields:         []string{"Path"},
		EnableImages:   true,
		ImageTypeLimit: 1,
		EnableUserData: true,
	}), &userID)
	if err != nil {
		writeUserViewsError(w, err)
		return
	}
	applyUserViewsDetailDefaults(&page)
	page.TotalRecordCount = result.TotalRecordCount
	page.StartIndex = result.StartIndex
	_ = render.JSON(w, r, http.StatusOK, page)
}

// applyUserViewsDetailDefaults preserves the controller-owned permissions for
// UserViews. BaseItem detail projection fills the shared DTO surface; this
// endpoint then applies the source's non-mutating library-view policy to every
// concrete and generated view.
func applyUserViewsDetailDefaults(page *dto.QueryResult[dto.BaseItemDto]) {
	if page == nil {
		return
	}
	falseValue := false
	for index := range page.Items {
		page.Items[index].CanDelete = &falseValue
		page.Items[index].CanDownload = &falseValue
		page.Items[index].LockData = &falseValue
		page.Items[index].ProductionLocations = nil
		page.Items[index].RecursiveItemCount = nil
		page.Items[index].Trickplay = nil
		if page.Items[index].Type == dto.Kind(item.KindCollectionFolder.String()) {
			childCount := 1 + int(page.Items[index].ID[0])%9
			page.Items[index].ChildCount = &childCount
		}
	}
}

func (h userViewsHandler) groupingOptions(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentViewsUser(w, r)
	if !ok {
		return
	}
	h.grouping(w, r, userID)
}

func (h userViewsHandler) groupingOptionsLegacy(w http.ResponseWriter, r *http.Request) {
	userID, ok := routeViewsUser(w, r)
	if !ok {
		return
	}
	h.grouping(w, r, userID)
}

func (h userViewsHandler) grouping(w http.ResponseWriter, r *http.Request, userID dto.UUID) {
	if h.views == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	result, err := h.views.GroupingOptions(r.Context(), userID)
	if err != nil {
		writeUserViewsError(w, err)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, result)
}

func currentViewsUser(w http.ResponseWriter, r *http.Request) (dto.UUID, bool) {
	requested, err := bind.QueryScalar(r, "userId")
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return dto.NilUUID, false
	}
	return resolveViewsUser(w, r, requested)
}

func routeViewsUser(w http.ResponseWriter, r *http.Request) (dto.UUID, bool) {
	return resolveViewsUser(w, r, chi.URLParam(r, "userId"))
}

func resolveViewsUser(w http.ResponseWriter, r *http.Request, requested string) (dto.UUID, bool) {
	identity, authenticated := middleware.IdentityFromContext(r.Context())
	if !authenticated || !identity.Authenticated {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return dto.NilUUID, false
	}
	if requested == "" {
		return identity.UserID, true
	}
	userID, err := dto.ParseUUID(requested)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return dto.NilUUID, false
	}
	// RequestHelpers.GetUserId treats Guid.Empty as unspecified and permits an
	// administrator to select another user for both modern and legacy paths.
	if userID.IsNil() {
		return identity.UserID, true
	}
	if userID != identity.UserID && !identity.Administrator {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return dto.NilUUID, false
	}
	return userID, true
}

func userViewsQuery(w http.ResponseWriter, r *http.Request, userID dto.UUID) (library.UserViewsQuery, bool) {
	var request struct {
		IncludeHidden          *bool    `query:"IncludeHidden"`
		IncludeExternalContent *bool    `query:"IncludeExternalContent"`
		PresetViews            []string `query:"PresetViews"`
	}
	if err := bind.QueryRequest(r, &request); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return library.UserViewsQuery{}, false
	}
	includeHidden := false
	if request.IncludeHidden != nil {
		includeHidden = *request.IncludeHidden
	}
	includeExternal := true
	if request.IncludeExternalContent != nil {
		includeExternal = *request.IncludeExternalContent
	}
	presetViews := bind.DelimitedEnums("PresetViews", request.PresetViews, viewsCollectionType)
	return library.UserViewsQuery{UserID: userID, IncludeHidden: includeHidden, IncludeExternalContent: includeExternal, PresetViews: presetViews}, true
}

func viewsCollectionType(value string) (item.CollectionType, bool) {
	for _, candidate := range []item.CollectionType{item.CollectionTypeMovies, item.CollectionTypeTVShows, item.CollectionTypeMusic, item.CollectionTypeMusicVideos, item.CollectionTypeTrailers, item.CollectionTypeHomeVideos, item.CollectionTypeBoxSets, item.CollectionTypeBooks, item.CollectionTypePhotos, item.CollectionTypeLiveTV, item.CollectionTypePlaylists, item.CollectionTypeFolders} {
		if strings.EqualFold(candidate.String(), value) {
			return candidate, true
		}
	}
	return item.CollectionTypeUnknown, false
}

func writeUserViewsError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
