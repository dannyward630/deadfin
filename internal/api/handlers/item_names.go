// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// RegisterItemNames registers the source taxonomy aggregations separately from
// RegisterItems: these routes aggregate modeled item facts rather than aliasing
// the generic item page.
// The optional dependencies are an ItemUserResolver and a
// LanguageInfoFinder.
func RegisterItemNames(rt *router.Router, reader ItemReader, build ItemDTOBuilder, clock clock.Clock, dependencies ...any) {
	var resolver ItemUserResolver
	var languages LanguageInfoFinder
	for _, dependency := range dependencies {
		switch value := dependency.(type) {
		case ItemUserResolver:
			resolver = value
		case LanguageInfoFinder:
			languages = value
		}
	}
	facts, ok := reader.(ItemTaxonomyReader)
	if !ok && reader != nil {
		facts = queryTaxonomyReader{reader: reader}
	}
	h := itemNamesHandler{reader: reader, facts: facts, build: build, clock: clock, users: resolver, languages: languages}
	policy := string(auth.PolicyDefaultAuthorization)
	rt.Handle(http.MethodGet, "/Genres", router.Route{OperationID: "GetGenres", Policy: policy}, h.genres)
	rt.Handle(http.MethodGet, "/Persons", router.Route{OperationID: "GetPersons", Policy: policy}, h.persons)
	rt.Handle(http.MethodGet, "/Studios", router.Route{OperationID: "GetStudios", Policy: policy}, h.studios)
	rt.Handle(http.MethodGet, "/Items/Filters", router.Route{OperationID: "GetQueryFiltersLegacy", Policy: policy}, h.filters)
	rt.Handle(http.MethodGet, "/Items/Filters2", router.Route{OperationID: "GetQueryFilters", Policy: policy}, h.queryFilters)
	rt.Handle(http.MethodGet, "/Artists", router.Route{OperationID: "GetArtists", Policy: policy}, h.artists)
	rt.Handle(http.MethodGet, "/Artists/AlbumArtists", router.Route{OperationID: "GetAlbumArtists", Policy: policy}, h.albumArtists)
	rt.Handle(http.MethodGet, "/Persons/{name}", router.Route{OperationID: "GetPerson", Policy: policy}, h.person)
	rt.Handle(http.MethodGet, "/Genres/{genreName}", router.Route{OperationID: "GetGenre", Policy: policy}, h.genre)
	rt.Handle(http.MethodGet, "/MusicGenres/{genreName}", router.Route{OperationID: "GetMusicGenre", Policy: policy}, h.musicGenre)
	rt.Handle(http.MethodGet, "/Studios/{name}", router.Route{OperationID: "GetStudio", Policy: policy}, h.studio)
	rt.Handle(http.MethodGet, "/Artists/{name}", router.Route{OperationID: "GetArtistByName", Policy: policy}, h.artist)
}

// ItemTaxonomyReader reads the facts the taxonomy routes aggregate without
// loading full items. RegisterItemNames uses it when the item reader
// implements it, and otherwise derives the facts from full Query pages.
type ItemTaxonomyReader interface {
	QueryItemNames(context.Context, store.ItemQuery) ([]store.ItemName, error)
	QueryPersonCredits(context.Context, store.ItemQuery) ([]item.PersonCredit, error)
	QueryItemValues(context.Context, store.ItemQuery, ...string) ([]string, error)
}

type itemNamesHandler struct {
	reader    ItemReader
	facts     ItemTaxonomyReader
	build     ItemDTOBuilder
	clock     clock.Clock
	users     ItemUserResolver
	languages LanguageInfoFinder
}

type itemNamesRequest struct {
	SearchTerm         string    `query:"SearchTerm"`
	IsFavorite         *bool     `query:"IsFavorite"`
	EnableImages       *bool     `query:"EnableImages"`
	EnableUserData     *bool     `query:"EnableUserData"`
	Fields             []string  `query:"Fields"`
	EnableTotalRecords *bool     `query:"EnableTotalRecordCount"`
	Filters            []string  `query:"Filters"`
	PersonTypes        []string  `query:"PersonTypes"`
	ExcludePersonTypes []string  `query:"ExcludePersonTypes"`
	AppearsInItemID    *dto.UUID `query:"AppearsInItemId"`
}

func (h itemNamesHandler) genres(w http.ResponseWriter, r *http.Request) {
	h.taxonomy(w, r, item.KindGenre, false, false, true)
}

func (h itemNamesHandler) studios(w http.ResponseWriter, r *http.Request) {
	h.taxonomy(w, r, item.KindStudio, false, true, false)
}

func (h itemNamesHandler) artists(w http.ResponseWriter, r *http.Request) {
	h.taxonomy(w, r, item.KindMusicArtist, false, true, true)
}

// albumArtists ports ArtistsController.GetAlbumArtists: the artist entities
// named as album artists, rather than as any artist.
func (h itemNamesHandler) albumArtists(w http.ResponseWriter, r *http.Request) {
	h.taxonomyWith(w, r, item.KindMusicArtist, false, true, true, []string{"album_artist"})
}

func (h itemNamesHandler) persons(w http.ResponseWriter, r *http.Request) {
	h.taxonomy(w, r, item.KindPerson, true, true, false)
}

func (h itemNamesHandler) taxonomy(w http.ResponseWriter, r *http.Request, kind item.Kind, people, userData, ordering bool) {
	h.taxonomyWith(w, r, kind, people, userData, ordering, nil)
}

// taxonomyWith aggregates the names a source query's items carry. It reads
// only the credits or value names of the source items and the id and name of
// the entities, and loads full entity items only for the returned page:
// loading every source item and every entity with satellites took 2s for
// /Persons and 1.1s for /Studios on a 47k-item library. valueKinds overrides
// the item_values kinds that name the entities.
func (h itemNamesHandler) taxonomyWith(w http.ResponseWriter, r *http.Request, kind item.Kind, people, userData, ordering bool, valueKinds []string) {
	facts := h.facts
	if h.reader == nil || h.build == nil || facts == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	query, request, ok := h.itemNamesQuery(w, r)
	if !ok {
		return
	}
	query = taxonomyItemQuery(query, ordering)
	if people && request.IsFavorite == nil && hasFavoriteFilter(request.Filters) {
		favorite := true
		query.IsFavorite = &favorite
	}
	if query.IsFavorite != nil && query.UserID == nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !userData {
		query.DtoOptions.EnableUserData = false
	}
	if people {
		// PersonsController has no generic item-kind or media-type inputs.
		query.IncludeItemTypes = nil
		query.ExcludeItemTypes = nil
		query.MediaTypes = nil
	}
	if people && request.AppearsInItemID != nil {
		query.ItemIDs = []dto.UUID{*request.AppearsInItemID}
	}
	pageQuery := query
	if people && pageQuery.Limit != nil && *pageQuery.Limit == 0 {
		pageQuery.Limit = nil
	}
	query.StartIndex = nil
	query.Limit = nil
	query.EnableTotalRecordCount = false
	parent, err := h.applyParentScope(r.Context(), &query)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if kind == item.KindGenre && parent != nil && parent.Extra.CollectionFolder != nil && parent.Extra.CollectionFolder.CollectionType != nil {
		collectionType := *parent.Extra.CollectionFolder.CollectionType
		if collectionType == item.CollectionTypeMusic || collectionType == item.CollectionTypeMusicVideos {
			kind = item.KindMusicGenre
		}
	}
	var names []store.ItemName
	if people {
		names, err = h.peopleNames(r.Context(), facts, query, request)
	} else {
		names, err = h.namedValueNames(r.Context(), facts, query, kind, valueKinds)
	}
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	names = filterNamedItems(names, pageQuery)
	sortNamedItemsForQuery(names, pageQuery.SortBy, pageQuery.SortOrder)
	total := len(names)
	names = pageNamedItems(names, pageQuery.StartIndex, pageQuery.Limit)
	if !pageQuery.EnableTotalRecordCount {
		total = 0
	}
	values, err := h.namedPageItems(r.Context(), names, query.UserID)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	page, err := h.build.Build(r.Context(), values, pageQuery.DtoOptions, pageQuery.UserID)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if page.Items == nil {
		page.Items = []dto.BaseItemDto{}
	}
	page.TotalRecordCount = total
	if pageQuery.StartIndex != nil {
		page.StartIndex = *pageQuery.StartIndex
	}
	_ = render.JSON(w, r, http.StatusOK, page)
}

func (h itemNamesHandler) filters(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	query, _, ok := h.itemNamesQuery(w, r)
	if !ok {
		return
	}
	// FilterController accepts only the scope and content-type inputs below;
	// generic Items parameters are ignored by its source contract.
	query = store.ItemQuery{User: query.User, UserID: query.UserID, ParentID: query.ParentID, IncludeItemTypes: query.IncludeItemTypes, MediaTypes: query.MediaTypes}
	if query.ParentID != nil && len(query.IncludeItemTypes) == 1 && (query.IncludeItemTypes[0] == item.KindTrailer || query.IncludeItemTypes[0] == item.KindProgram) {
		_ = render.JSON(w, r, http.StatusOK, newQueryFiltersLegacy())
		return
	}
	if query.ParentID != nil {
		parent, err := h.parent(r.Context(), *query.ParentID, query.UserID)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if parent == nil || !parent.IsFolder {
			_ = render.JSON(w, r, http.StatusOK, newQueryFiltersLegacy())
			return
		}
		query.AncestorIDs = []dto.UUID{*query.ParentID}
		query.ParentID = nil
	}
	query.Recursive = true
	query.EnableTotalRecordCount = false
	query.DtoOptions = store.ItemQueryDTOOptions{}
	result, err := h.reader.Query(r.Context(), query)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, aggregateQueryFiltersLegacy(result.Items))
}

func (h itemNamesHandler) itemNamesQuery(w http.ResponseWriter, r *http.Request) (store.ItemQuery, itemNamesRequest, bool) {
	query, err := bind.ItemsQueryRequest(r, h.clock)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return store.ItemQuery{}, itemNamesRequest{}, false
	}
	request := itemNamesRequest{}
	if err := bind.QueryRequest(r, &request); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return store.ItemQuery{}, itemNamesRequest{}, false
	}
	requested, err := bind.QueryScalar(r, "userId")
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return store.ItemQuery{}, itemNamesRequest{}, false
	}
	if h.users != nil {
		if _, authenticated := middleware.IdentityFromContext(r.Context()); authenticated && !isGlobalAPIKeyItemRequest(r, requested) {
			user, ok := resolveItemUser(w, r, requested, h.users)
			if !ok {
				return store.ItemQuery{}, itemNamesRequest{}, false
			}
			applyItemUser(&query, user)
		} else if requested != "" && !isGlobalAPIKeyItemRequest(r, requested) {
			id, err := dto.ParseUUID(requested)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				return store.ItemQuery{}, itemNamesRequest{}, false
			}
			query.UserID = &id
		}
	} else if requested != "" {
		id, err := dto.ParseUUID(requested)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return store.ItemQuery{}, itemNamesRequest{}, false
		}
		query.UserID = &id
	} else if identity, authenticated := middleware.IdentityFromContext(r.Context()); authenticated && identity.Authenticated {
		id := identity.UserID
		query.UserID = &id
	}
	query.SearchTerm = request.SearchTerm
	query.IsFavorite = request.IsFavorite
	query.DtoOptions.Fields = bind.ItemFields("Fields", request.Fields)
	if request.EnableImages != nil {
		query.DtoOptions.EnableImages = *request.EnableImages
	} else {
		query.DtoOptions.EnableImages = true
	}
	if request.EnableUserData != nil {
		query.DtoOptions.EnableUserData = *request.EnableUserData
	}
	if request.EnableTotalRecords != nil {
		query.EnableTotalRecordCount = *request.EnableTotalRecords
	}
	return query, request, true
}

func hasFavoriteFilter(filters []string) bool {
	for _, filter := range filters {
		if strings.EqualFold(filter, "IsFavorite") {
			return true
		}
	}
	return false
}

func taxonomyItemQuery(query store.ItemQuery, ordering bool) store.ItemQuery {
	out := store.ItemQuery{
		StartIndex:              query.StartIndex,
		Limit:                   query.Limit,
		ParentID:                query.ParentID,
		IncludeItemTypes:        query.IncludeItemTypes,
		ExcludeItemTypes:        query.ExcludeItemTypes,
		NameStartsWithOrGreater: query.NameStartsWithOrGreater,
		NameStartsWith:          query.NameStartsWith,
		NameLessThan:            query.NameLessThan,
		SearchTerm:              query.SearchTerm,
		IsFavorite:              query.IsFavorite,
		EnableTotalRecordCount:  query.EnableTotalRecordCount,
		DtoOptions:              query.DtoOptions,
		User:                    query.User,
		UserID:                  query.UserID,
		SortBy:                  query.SortBy,
		SortOrder:               query.SortOrder,
	}
	if !ordering {
		out.SortBy = nil
		out.SortOrder = nil
	}
	return out
}

func (h itemNamesHandler) applyParentScope(ctx context.Context, query *store.ItemQuery) (*item.Item, error) {
	if query.ParentID == nil {
		return nil, nil
	}
	parent, err := h.parent(ctx, *query.ParentID, query.UserID)
	if err != nil {
		return nil, err
	}
	if parent != nil && parent.IsFolder {
		query.AncestorIDs = []dto.UUID{*query.ParentID}
		query.ParentID = nil
		return parent, nil
	}
	query.ItemIDs = []dto.UUID{*query.ParentID}
	query.ParentID = nil
	return parent, nil
}

// parent resolves only the structural scope. The subsequent source query
// carries the resolved user and determines whether the caller can observe any
// facts under that scope; this internal lookup keeps UserID for projection.
func (h itemNamesHandler) parent(ctx context.Context, id dto.UUID, userID *dto.UUID) (*item.Item, error) {
	result, err := h.reader.Query(ctx, store.ItemQuery{ItemIDs: []dto.UUID{id}, UserID: userID, EnableTotalRecordCount: false})
	if err != nil || len(result.Items) == 0 {
		return nil, err
	}
	return &result.Items[0], nil
}

// namedValueNames resolves global taxonomy entities only after the source
// items have been access-filtered. Entity rows do not inherit source tags or
// ratings, so this intentional internal lookup leaves User nil to avoid
// rejecting permitted derived values.
func (h itemNamesHandler) namedValueNames(ctx context.Context, facts ItemTaxonomyReader, source store.ItemQuery, kind item.Kind, valueKinds []string) ([]store.ItemName, error) {
	if valueKinds == nil {
		switch kind {
		case item.KindGenre, item.KindMusicGenre:
			valueKinds = []string{"genre"}
		case item.KindMusicArtist:
			valueKinds = []string{"artist", "album_artist"}
		default:
			valueKinds = []string{"studio"}
		}
	}
	values, err := facts.QueryItemValues(ctx, source, valueKinds...)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]struct{})
	for _, name := range values {
		if key := item.CleanName(strings.TrimSpace(name)); key != "" {
			wanted[key] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return []store.ItemName{}, nil
	}
	entities, err := facts.QueryItemNames(ctx, store.ItemQuery{IncludeItemTypes: []item.Kind{kind}, EnableTotalRecordCount: false})
	if err != nil {
		return nil, err
	}
	return matchingNamedEntities(entities, kind, wanted), nil
}

// peopleNamesByIDLimit bounds the credited people read by id. Larger sets
// read every Person name: one id placeholder each would pass SQLite's
// variable limit, and the full read costs less than a long id list.
const peopleNamesByIDLimit = 5000

func (h itemNamesHandler) peopleNames(ctx context.Context, facts ItemTaxonomyReader, source store.ItemQuery, request itemNamesRequest) ([]store.ItemName, error) {
	credits, err := facts.QueryPersonCredits(ctx, source)
	if err != nil {
		return nil, err
	}
	wanted := make(map[dto.UUID]struct{})
	include := personTypes(request.PersonTypes)
	exclude := personTypes(request.ExcludePersonTypes)
	for _, person := range credits {
		if !person.PersonID.IsNil() && strings.TrimSpace(person.Name) != "" && matchesPersonTypes(person, include, exclude) {
			wanted[person.PersonID] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return []store.ItemName{}, nil
	}
	entityQuery := store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindPerson}, EnableTotalRecordCount: false}
	if len(wanted) <= peopleNamesByIDLimit {
		entityQuery.ItemIDs = make([]dto.UUID, 0, len(wanted))
		for id := range wanted {
			entityQuery.ItemIDs = append(entityQuery.ItemIDs, id)
		}
	}
	entities, err := facts.QueryItemNames(ctx, entityQuery)
	if err != nil {
		return nil, err
	}
	values := make([]store.ItemName, 0, len(wanted))
	for _, value := range entities {
		if value.Kind == item.KindPerson {
			if _, found := wanted[value.ID]; found {
				values = append(values, value)
			}
		}
	}
	sortNamedItems(values)
	return values, nil
}

// namedPageItems loads the full items of one page of names, in page order.
// The lookup keeps UserID for projection.
func (h itemNamesHandler) namedPageItems(ctx context.Context, names []store.ItemName, userID *dto.UUID) ([]item.Item, error) {
	if len(names) == 0 {
		return []item.Item{}, nil
	}
	ids := make([]dto.UUID, len(names))
	kinds := make([]item.Kind, 0, 1)
	for index, name := range names {
		ids[index] = name.ID
		if !slices.Contains(kinds, name.Kind) {
			kinds = append(kinds, name.Kind)
		}
	}
	result, err := h.reader.Query(ctx, store.ItemQuery{ItemIDs: ids, IncludeItemTypes: kinds, UserID: userID, EnableTotalRecordCount: false})
	if err != nil {
		return nil, err
	}
	byID := make(map[dto.UUID]item.Item, len(result.Items))
	for _, value := range result.Items {
		byID[value.ID] = value
	}
	values := make([]item.Item, 0, len(names))
	for _, id := range ids {
		if value, found := byID[id]; found {
			values = append(values, value)
		}
	}
	return values, nil
}

func matchingNamedEntities(values []store.ItemName, kind item.Kind, wanted map[string]struct{}) []store.ItemName {
	out := make([]store.ItemName, 0, len(wanted))
	for _, value := range values {
		if value.Kind != kind || value.Name == nil {
			continue
		}
		if _, found := wanted[item.CleanName(strings.TrimSpace(*value.Name))]; found {
			out = append(out, value)
		}
	}
	sortNamedItems(out)
	return out
}

func personTypes(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	return out
}

func matchesPersonTypes(person item.PersonCredit, include, exclude map[string]struct{}) bool {
	typ := strings.ToLower(person.Type.String())
	if _, found := exclude[typ]; found {
		return false
	}
	if len(include) == 0 {
		return true
	}
	_, found := include[typ]
	return found
}

func filterNamedItems(items []store.ItemName, query store.ItemQuery) []store.ItemName {
	out := items[:0]
	for _, value := range items {
		name := ""
		if value.Name != nil {
			name = *value.Name
		}
		folded := strings.ToLower(name)
		if query.SearchTerm != "" && !strings.Contains(folded, strings.ToLower(query.SearchTerm)) {
			continue
		}
		if query.NameStartsWith != "" && !strings.HasPrefix(folded, strings.ToLower(query.NameStartsWith)) {
			continue
		}
		if query.NameStartsWithOrGreater != "" && strings.Compare(folded, strings.ToLower(query.NameStartsWithOrGreater)) < 0 {
			continue
		}
		if query.NameLessThan != "" && strings.Compare(folded, strings.ToLower(query.NameLessThan)) >= 0 {
			continue
		}
		out = append(out, value)
	}
	return out
}

func sortNamedItems(items []store.ItemName) {
	sort.Slice(items, func(i, j int) bool {
		left, right := "", ""
		if items[i].Name != nil {
			left = *items[i].Name
		}
		if items[j].Name != nil {
			right = *items[j].Name
		}
		if strings.EqualFold(left, right) {
			return items[i].ID.String() < items[j].ID.String()
		}
		return strings.ToLower(left) < strings.ToLower(right)
	})
}

func sortNamedItemsForQuery(items []store.ItemName, sorts []store.ItemSortBy, orders []store.SortOrder) {
	sortNamedItems(items)
	if len(sorts) == 0 || len(orders) == 0 || orders[0] != store.SortOrderDescending {
		return
	}
	if sorts[0] != store.ItemSortByName && sorts[0] != store.ItemSortBySortName {
		return
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
}

func pageNamedItems(items []store.ItemName, start, limit *int) []store.ItemName {
	from := 0
	if start != nil {
		from = *start
	}
	if from >= len(items) {
		return []store.ItemName{}
	}
	items = items[from:]
	if limit != nil && *limit < len(items) {
		return items[:*limit]
	}
	return items
}

type queryFiltersLegacy struct {
	Genres          []string `json:"Genres"`
	Tags            []string `json:"Tags"`
	OfficialRatings []string `json:"OfficialRatings"`
	Years           []int    `json:"Years"`
}

func newQueryFiltersLegacy() queryFiltersLegacy {
	return queryFiltersLegacy{Genres: []string{}, Tags: []string{}, OfficialRatings: []string{}, Years: []int{}}
}

func aggregateQueryFiltersLegacy(items []item.Item) queryFiltersLegacy {
	genres, tags, ratings := map[string]string{}, map[string]string{}, map[string]string{}
	years := map[int]struct{}{}
	for _, value := range items {
		collectNames(genres, value.Values.Genres)
		collectNames(tags, value.Values.Tags)
		if value.OfficialRating != nil && *value.OfficialRating != "" {
			collectNames(ratings, []string{*value.OfficialRating})
		}
		if value.ProductionYear != nil && *value.ProductionYear > 0 {
			years[*value.ProductionYear] = struct{}{}
		}
	}
	result := newQueryFiltersLegacy()
	result.Genres = sortedNames(genres)
	result.Tags = sortedNames(tags)
	result.OfficialRatings = sortedNames(ratings)
	result.Years = make([]int, 0, len(years))
	for year := range years {
		result.Years = append(result.Years, year)
	}
	sort.Ints(result.Years)
	return result
}

func collectNames(into map[string]string, names []string) {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := item.CleanName(name)
		if existing, found := into[key]; !found || name < existing {
			into[key] = name
		}
	}
}

func sortedNames(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// queryTaxonomyReader derives taxonomy facts from full Query pages for item
// readers that do not implement ItemTaxonomyReader.
type queryTaxonomyReader struct{ reader ItemReader }

func (q queryTaxonomyReader) QueryItemNames(ctx context.Context, query store.ItemQuery) ([]store.ItemName, error) {
	result, err := q.reader.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	names := make([]store.ItemName, len(result.Items))
	for index, value := range result.Items {
		names[index] = store.ItemName{ID: value.ID, Kind: value.Kind, Name: value.Name}
	}
	return names, nil
}

func (q queryTaxonomyReader) QueryPersonCredits(ctx context.Context, query store.ItemQuery) ([]item.PersonCredit, error) {
	result, err := q.reader.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	var credits []item.PersonCredit
	for _, value := range result.Items {
		credits = append(credits, value.People...)
	}
	return credits, nil
}

func (q queryTaxonomyReader) QueryItemValues(ctx context.Context, query store.ItemQuery, kinds ...string) ([]string, error) {
	result, err := q.reader.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	var values []string
	for _, value := range result.Items {
		for _, kind := range kinds {
			switch kind {
			case "genre":
				values = append(values, value.Values.Genres...)
			case "studio":
				values = append(values, value.Values.Studios...)
			case "artist":
				values = append(values, value.Values.Artists...)
			case "album_artist":
				values = append(values, value.Values.AlbumArtists...)
			}
		}
	}
	return values, nil
}
