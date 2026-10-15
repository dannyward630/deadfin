// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"net/http"
	"sort"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/go-chi/chi/v5"
)

// RegisterItemExtras registers the source-specific related-media operations.
// Call it beside RegisterItems so they share the established reader/projection seam.
func RegisterItemExtras(rt *router.Router, reader ItemReader, build ItemDTOBuilder, dependencies ...any) {
	similar := SimilarItemsService(NewLocalSimilarItemsService(reader))
	var users ItemUserResolver
	for _, dependency := range dependencies {
		switch value := dependency.(type) {
		case SimilarItemsService:
			if value != nil {
				similar = value
			}
		case ItemUserResolver:
			users = value
		}
	}
	h := itemExtrasHandler{reader: reader, build: build, similarity: similar, users: users}
	policy := string(auth.PolicyDefaultAuthorization)
	rt.Handle(http.MethodGet, "/Items/{itemId}/Similar", router.Route{OperationID: "GetSimilarItems", Policy: policy}, h.similar)
	rt.Handle(http.MethodGet, "/Artists/{itemId}/Similar", router.Route{OperationID: "GetSimilarArtists", Policy: policy}, h.similar)
	rt.Handle(http.MethodGet, "/Albums/{itemId}/Similar", router.Route{OperationID: "GetSimilarAlbums", Policy: policy}, h.similar)
	rt.Handle(http.MethodGet, "/Shows/{itemId}/Similar", router.Route{OperationID: "GetSimilarShows", Policy: policy}, h.similar)
	rt.Handle(http.MethodGet, "/Movies/{itemId}/Similar", router.Route{OperationID: "GetSimilarMovies", Policy: policy}, h.similar)
	rt.Handle(http.MethodGet, "/Trailers/{itemId}/Similar", router.Route{OperationID: "GetSimilarTrailers", Policy: policy}, h.similar)
	rt.Handle(http.MethodGet, "/Items/{itemId}/SpecialFeatures", router.Route{OperationID: "GetSpecialFeatures", Policy: policy}, h.specialFeatures)
	rt.Handle(http.MethodGet, "/Items/{itemId}/LocalTrailers", router.Route{OperationID: "GetLocalTrailers", Policy: policy}, h.localTrailers)
	rt.Handle(http.MethodGet, "/Users/{userId}/Items/{itemId}/LocalTrailers", router.Route{OperationID: "SHADOW:GetLocalTrailersLegacy", Policy: policy}, h.localTrailersLegacy)
	rt.Handle(http.MethodGet, "/Users/{userId}/Items/{itemId}/SpecialFeatures", router.Route{OperationID: "SHADOW:GetSpecialFeaturesLegacy", Policy: policy}, h.specialFeaturesLegacy)
	rt.Handle(http.MethodGet, "/Items/{itemId}/Intros", router.Route{OperationID: "GetIntros", Policy: policy}, h.intros)
	rt.Handle(http.MethodGet, "/Users/{userId}/Items/{itemId}/Intros", router.Route{OperationID: "SHADOW:GetIntrosLegacy", Policy: policy}, h.introsLegacy)
	rt.Handle(http.MethodGet, "/Videos/{itemId}/AdditionalParts", router.Route{OperationID: "GetAdditionalPart", Policy: policy}, h.additionalParts)
	rt.Handle(http.MethodGet, "/Items/{itemId}/Ancestors", router.Route{OperationID: "GetAncestors", Policy: policy}, h.ancestors)
	rt.Handle(http.MethodGet, "/Items/{itemId}/ThemeMedia", router.Route{OperationID: "GetThemeMedia", Policy: policy}, h.themeMedia)
	rt.Handle(http.MethodGet, "/MediaSegments/{itemId}", router.Route{OperationID: "GetItemSegments", Policy: policy}, h.mediaSegments)
}

type itemExtrasHandler struct {
	users      ItemUserResolver
	reader     ItemReader
	build      ItemDTOBuilder
	similarity SimilarItemsService
}

type extrasQuery struct {
	UserID *dto.UUID `query:"userId"`
}

type similarQuery struct {
	ExcludeArtistIDs []dto.UUID `query:"excludeArtistIds"`
	UserID           *dto.UUID  `query:"userId"`
	Limit            *int       `query:"limit"`
	Fields           []string   `query:"fields"`
}

func (h itemExtrasHandler) localTrailers(w http.ResponseWriter, r *http.Request) {
	h.extras(w, r, item.ExtraTypeTrailer, false, nil)
}

func (h itemExtrasHandler) localTrailersLegacy(w http.ResponseWriter, r *http.Request) {
	h.extras(w, r, item.ExtraTypeTrailer, false, routeExtrasUser(r))
}

func (h itemExtrasHandler) specialFeatures(w http.ResponseWriter, r *http.Request) {
	h.extras(w, r, item.ExtraTypeUnknown, true, nil)
}

func (h itemExtrasHandler) specialFeaturesLegacy(w http.ResponseWriter, r *http.Request) {
	h.extras(w, r, item.ExtraTypeUnknown, true, routeExtrasUser(r))
}

func (h itemExtrasHandler) extras(w http.ResponseWriter, r *http.Request, extra item.ExtraType, display bool, routeUser *string) {
	if h.reader == nil || h.build == nil {
		internalItemExtrasError(w)
		return
	}
	user, ok := h.itemExtrasUser(w, r, routeUser)
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
	ownerIDs, ok := h.extraOwnerIDs(w, r, source, user)
	if !ok {
		return
	}
	relation := store.ItemQuery{
		OwnerIDs:  ownerIDs,
		SortBy:    []store.ItemSortBy{store.ItemSortBySortName},
		SortOrder: []store.SortOrder{store.SortOrderAscending},
		User:      user,
		UserID:    itemExtrasUserID(user),
	}
	if display {
		relation.ExtraTypes = displayExtraTypes
	} else {
		relation.ExtraTypes = []item.ExtraType{extra}
	}
	h.renderArray(w, r, relation)
}

func (h itemExtrasHandler) additionalParts(w http.ResponseWriter, r *http.Request) {
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
	if !isVideo(source) || len(source.AdditionalParts) == 0 {
		h.renderItems(w, r, nil, store.ItemQueryDTOOptions{}, itemExtrasUserID(user), false)
		return
	}

	// Video.GetAdditionalParts resolves every persisted source path as a Video,
	// filters invisible/missing rows, and finally orders the resolved videos by
	// SortName. Path lookup is the ItemReader equivalent of that library lookup.
	parts := make([]item.Item, 0, len(source.AdditionalParts))
	for _, path := range source.AdditionalParts {
		result, err := h.reader.Query(r.Context(), store.ItemQuery{Path: path, IncludeOwnedItems: true, User: user, UserID: itemExtrasUserID(user)})
		if err != nil {
			internalItemExtrasError(w)
			return
		}
		for _, candidate := range result.Items {
			if isVideo(candidate) {
				parts = append(parts, candidate)
			}
		}
	}
	sort.SliceStable(parts, func(left, right int) bool { return itemExtrasSortName(parts[left]) < itemExtrasSortName(parts[right]) })
	h.renderItems(w, r, parts, store.ItemQueryDTOOptions{}, itemExtrasUserID(user), false)
}

func (h itemExtrasHandler) similar(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil || h.build == nil || h.similarity == nil {
		internalItemExtrasError(w)
		return
	}
	request := similarQuery{}
	if err := bind.QueryRequest(r, &request); err != nil || request.Limit != nil && *request.Limit < 0 {
		badItemExtrasRequest(w)
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
	fields := bind.ItemFields("fields", request.Fields)
	if !supportsSimilar(source.Kind) {
		h.renderItems(w, r, nil, store.ItemQueryDTOOptions{Fields: fields}, itemExtrasUserID(user), false)
		return
	}
	items, err := h.similarity.SimilarItems(r.Context(), SimilarItemsRequest{
		Item:             source,
		ExcludeArtistIDs: request.ExcludeArtistIDs,
		User:             user,
		UserID:           itemExtrasUserID(user),
		Limit:            request.Limit,
		DtoOptions:       store.ItemQueryDTOOptions{Fields: fields},
	})
	if err != nil {
		internalItemExtrasError(w)
		return
	}
	page, err := h.build.Build(r.Context(), items, store.ItemQueryDTOOptions{Fields: fields}, itemExtrasUserID(user))
	if err != nil {
		internalItemExtrasError(w)
		return
	}
	page.TotalRecordCount = len(items)
	_ = render.JSON(w, r, http.StatusOK, page)
}

// intros serves GET /Items/{itemId}/Intros. The current path binds the
// optional query userId through itemExtrasUser, matching upstream
// UserLibraryController.GetIntros. No intro providers are registered, so the
// response is an empty query result with a non-nil Items array.
func (h itemExtrasHandler) intros(w http.ResponseWriter, r *http.Request) {
	h.introsWith(w, r, nil)
}

// introsLegacy serves GET /Users/{userId}/Items/{itemId}/Intros. The legacy
// path uses the route userId and ignores any conflicting query userId, matching
// upstream UserLibraryController.GetIntros (the second route delegates to the
// same handler).
func (h itemExtrasHandler) introsLegacy(w http.ResponseWriter, r *http.Request) {
	h.introsWith(w, r, routeExtrasUser(r))
}

func (h itemExtrasHandler) introsWith(w http.ResponseWriter, r *http.Request, routeUser *string) {
	if h.reader == nil || h.build == nil {
		internalItemExtrasError(w)
		return
	}
	user, ok := h.itemExtrasUser(w, r, routeUser)
	if !ok {
		return
	}
	itemID, ok := itemExtrasItemID(w, r)
	if !ok {
		return
	}
	if _, ok := h.source(w, r, itemID, user); !ok {
		return
	}
	h.renderItems(w, r, nil, store.ItemQueryDTOOptions{}, itemExtrasUserID(user), false)
}

func (h itemExtrasHandler) source(w http.ResponseWriter, r *http.Request, itemID dto.UUID, user *store.User) (item.Item, bool) {
	query := store.ItemQuery{ItemIDs: []dto.UUID{itemID}, User: user, UserID: itemExtrasUserID(user)}
	root := itemID == dto.NilUUID
	if root {
		query.ItemIDs = nil
		query.IncludeItemTypes = []item.Kind{item.KindUserRootFolder}
	}
	result, err := h.reader.Query(r.Context(), query)
	if err != nil {
		internalItemExtrasError(w)
		return item.Item{}, false
	}
	if len(result.Items) == 0 {
		if root {
			internalItemExtrasError(w)
		} else {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		}
		return item.Item{}, false
	}
	if root && len(result.Items) != 1 {
		internalItemExtrasError(w)
		return item.Item{}, false
	}
	return result.Items[0], true
}

type itemLinkedChildrenReader interface {
	LinkedChildren(context.Context, dto.UUID) ([]item.LinkedChildRef, error)
}

func (h itemExtrasHandler) extraLinkedChildren(ctx context.Context, value item.Item) (item.Item, error) {
	reader, ok := h.reader.(itemLinkedChildrenReader)
	if !ok {
		return value, nil
	}
	linked, err := reader.LinkedChildren(ctx, value.ID)
	if err != nil {
		return item.Item{}, err
	}
	value.LinkedChildren = linked
	return value, nil
}

// extraOwnerIDs ports Video.GetExtraOwnerIds: extras attached to one version
// are visible from the requested video, its linked alternates, and—when this
// is an alternate—the primary video and that primary's linked alternates.
func (h itemExtrasHandler) extraOwnerIDs(w http.ResponseWriter, r *http.Request, source item.Item, user *store.User) ([]dto.UUID, bool) {
	source, err := h.extraLinkedChildren(r.Context(), source)
	if err != nil {
		internalItemExtrasError(w)
		return nil, false
	}
	owners := []dto.UUID{source.ID}
	linked := extraLinkedVideoIDs(source)
	if linkedReader, ok := h.reader.(itemLinkedChildrenReader); ok {
		if source.PrimaryVersionID != nil {
			owners = appendOwnerID(owners, *source.PrimaryVersionID)
			primaryChildren, err := linkedReader.LinkedChildren(r.Context(), *source.PrimaryVersionID)
			if err != nil {
				internalItemExtrasError(w)
				return nil, false
			}
			linked = appendOwnerIDs(linked, extraLinkedVideoIDs(item.Item{LinkedChildren: primaryChildren}))
		}
		for _, id := range linked {
			owners = appendOwnerID(owners, id)
			children, err := linkedReader.LinkedChildren(r.Context(), id)
			if err != nil {
				internalItemExtrasError(w)
				return nil, false
			}
			for _, child := range children {
				if child.Type == item.LinkedChildTypeLocalAlternateVersion {
					owners = appendOwnerID(owners, child.ItemID)
				}
			}
		}
		return owners, true
	}
	if source.PrimaryVersionID != nil {
		result, err := h.reader.Query(r.Context(), store.ItemQuery{ItemIDs: []dto.UUID{*source.PrimaryVersionID}, User: user, UserID: itemExtrasUserID(user)})
		if err != nil {
			internalItemExtrasError(w)
			return nil, false
		}
		if len(result.Items) != 0 && isVideo(result.Items[0]) {
			primary, err := h.extraLinkedChildren(r.Context(), result.Items[0])
			if err != nil {
				internalItemExtrasError(w)
				return nil, false
			}
			owners = appendOwnerID(owners, primary.ID)
			linked = appendOwnerIDs(linked, extraLinkedVideoIDs(primary))
		}
	}
	if len(linked) == 0 {
		return owners, true
	}
	result, err := h.reader.Query(r.Context(), store.ItemQuery{ItemIDs: linked, User: user, UserID: itemExtrasUserID(user)})
	if err != nil {
		internalItemExtrasError(w)
		return nil, false
	}
	byID := make(map[dto.UUID]item.Item, len(result.Items))
	for _, candidate := range result.Items {
		if isVideo(candidate) {
			byID[candidate.ID] = candidate
		}
	}
	local := make([]dto.UUID, 0)
	for _, id := range linked {
		candidate, found := byID[id]
		if !found {
			continue
		}
		candidate, err = h.extraLinkedChildren(r.Context(), candidate)
		if err != nil {
			internalItemExtrasError(w)
			return nil, false
		}
		owners = appendOwnerID(owners, candidate.ID)
		for _, localID := range extraLocalAlternateVideoIDs(candidate) {
			if !containsOwnerID(owners, localID) && !containsOwnerID(local, localID) {
				local = append(local, localID)
			}
		}
	}
	if len(local) == 0 {
		return owners, true
	}
	localResult, err := h.reader.Query(r.Context(), store.ItemQuery{ItemIDs: local, User: user, UserID: itemExtrasUserID(user)})
	if err != nil {
		internalItemExtrasError(w)
		return nil, false
	}
	localByID := make(map[dto.UUID]item.Item, len(localResult.Items))
	for _, candidate := range localResult.Items {
		if isVideo(candidate) {
			localByID[candidate.ID] = candidate
		}
	}
	for _, id := range local {
		if candidate, found := localByID[id]; found {
			owners = appendOwnerID(owners, candidate.ID)
		}
	}
	return owners, true
}

func extraLinkedVideoIDs(value item.Item) []dto.UUID {
	ids := make([]dto.UUID, 0, len(value.LinkedChildren))
	for _, linked := range value.LinkedChildren {
		if linked.Type == item.LinkedChildTypeLinkedAlternateVersion || linked.Type == item.LinkedChildTypeLocalAlternateVersion {
			ids = appendOwnerID(ids, linked.ItemID)
		}
	}
	return ids
}

func extraLocalAlternateVideoIDs(value item.Item) []dto.UUID {
	ids := make([]dto.UUID, 0, len(value.LinkedChildren))
	for _, linked := range value.LinkedChildren {
		if linked.Type == item.LinkedChildTypeLocalAlternateVersion {
			ids = appendOwnerID(ids, linked.ItemID)
		}
	}
	return ids
}

func appendOwnerID(ids []dto.UUID, value dto.UUID) []dto.UUID {
	for _, id := range ids {
		if id == value {
			return ids
		}
	}
	return append(ids, value)
}

func containsOwnerID(ids []dto.UUID, value dto.UUID) bool {
	for _, id := range ids {
		if id == value {
			return true
		}
	}
	return false
}

func appendOwnerIDs(ids []dto.UUID, values []dto.UUID) []dto.UUID {
	for _, value := range values {
		ids = appendOwnerID(ids, value)
	}
	return ids
}

func (h itemExtrasHandler) renderArray(w http.ResponseWriter, r *http.Request, query store.ItemQuery) {
	result, err := h.reader.Query(r.Context(), query)
	if err != nil {
		internalItemExtrasError(w)
		return
	}
	h.renderItems(w, r, result.Items, query.DtoOptions, query.UserID, true)
}

func (h itemExtrasHandler) renderItems(w http.ResponseWriter, r *http.Request, values []item.Item, options store.ItemQueryDTOOptions, userID *dto.UUID, array bool) {
	page, err := h.build.Build(r.Context(), values, options, userID)
	if err != nil {
		internalItemExtrasError(w)
		return
	}
	if page.Items == nil {
		page.Items = make([]dto.BaseItemDto, 0)
	}
	if array {
		_ = render.JSON(w, r, http.StatusOK, page.Items)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, page)
}

func (h itemExtrasHandler) itemExtrasUser(w http.ResponseWriter, r *http.Request, routeUser *string) (*store.User, bool) {
	requested := ""
	if routeUser != nil {
		requested = *routeUser
	} else {
		query := extrasQuery{}
		if err := bind.QueryRequest(r, &query); err != nil {
			badItemExtrasRequest(w)
			return nil, false
		}
		if query.UserID != nil {
			requested = query.UserID.Dashed()
		}
	}
	if h.users != nil {
		if isGlobalAPIKeyItemRequest(r, requested) {
			return nil, true
		}
		if identity, authenticated := middleware.IdentityFromContext(r.Context()); authenticated && identity.Authenticated {
			user, ok := resolveItemUser(w, r, requested, h.users)
			if !ok {
				return nil, false
			}
			return &user, true
		}
	}
	if requested == "" {
		return authenticatedExtrasUser(r), true
	}
	id, err := dto.ParseUUID(requested)
	if err != nil {
		badItemExtrasRequest(w)
		return nil, false
	}
	return &store.User{ID: id}, true
}

func authenticatedExtrasUser(r *http.Request) *store.User {
	identity, ok := middleware.IdentityFromContext(r.Context())
	if !ok || !identity.Authenticated {
		return nil
	}
	return &store.User{ID: identity.UserID}
}

func itemExtrasUserID(user *store.User) *dto.UUID {
	if user == nil {
		return nil
	}
	return &user.ID
}

func routeExtrasUser(r *http.Request) *string {
	value := chi.URLParam(r, "userId")
	return &value
}

func itemExtrasItemID(w http.ResponseWriter, r *http.Request) (dto.UUID, bool) {
	id, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil {
		badItemExtrasRequest(w)
		return dto.NilUUID, false
	}
	return id, true
}

func itemExtrasSortName(value item.Item) string {
	if value.SortName == nil {
		return ""
	}
	return *value.SortName
}

func isVideo(value item.Item) bool {
	switch value.Kind {
	case item.KindVideo, item.KindMovie, item.KindMusicVideo, item.KindEpisode, item.KindTrailer, item.KindRecording:
		return true
	default:
		return false
	}
}

func supportsSimilar(kind item.Kind) bool {
	switch kind {
	case item.KindEpisode, item.KindGenre, item.KindMusicGenre, item.KindPerson, item.KindStudio, item.KindYear:
		return false
	default:
		return true
	}
}

var displayExtraTypes = []item.ExtraType{
	item.ExtraTypeUnknown,
	item.ExtraTypeBehindTheScenes,
	item.ExtraTypeClip,
	item.ExtraTypeDeletedScene,
	item.ExtraTypeInterview,
	item.ExtraTypeSample,
	item.ExtraTypeScene,
	item.ExtraTypeFeaturette,
	item.ExtraTypeShort,
}

func badItemExtrasRequest(w http.ResponseWriter) {
	http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
}

func internalItemExtrasError(w http.ResponseWriter) {
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
