// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

func (h itemNamesHandler) person(w http.ResponseWriter, r *http.Request) {
	h.byName(w, r, "name", item.KindPerson)
}

func (h itemNamesHandler) genre(w http.ResponseWriter, r *http.Request) {
	h.byName(w, r, "genreName", item.KindGenre)
}

func (h itemNamesHandler) musicGenre(w http.ResponseWriter, r *http.Request) {
	h.byName(w, r, "genreName", item.KindMusicGenre)
}

func (h itemNamesHandler) studio(w http.ResponseWriter, r *http.Request) {
	h.byName(w, r, "name", item.KindStudio)
}

func (h itemNamesHandler) artist(w http.ResponseWriter, r *http.Request) {
	h.byName(w, r, "name", item.KindMusicArtist)
}

// byName ports the singleton GET of PersonsController, GenresController,
// MusicGenresController, StudiosController, and ArtistsController. The store
// matches the clean name, which also covers GetItemFromSlugName's '&', '/',
// and '?' slug alternatives because CleanName folds all of them to a space.
//
// GetPerson answers 404 for an unknown person. The other controllers call
// LibraryManager.CreateItemByName, which creates the entity on read. Deadfin
// answers the same entity identity without persisting it (divergence
// D-38); an unknown artist answers 404 because the artist
// identity derives from the music library, not from the name alone.
func (h itemNamesHandler) byName(w http.ResponseWriter, r *http.Request, param string, kind item.Kind) {
	if h.reader == nil || h.build == nil {
		internalError(w)
		return
	}
	name := routeName(r, param)
	if strings.TrimSpace(name) == "" {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	query, _, ok := h.itemNamesQuery(w, r)
	if !ok {
		return
	}
	entity, err := lookupNamedEntity(r.Context(), h.reader, kind, name, query.UserID)
	if err != nil {
		internalError(w)
		return
	}
	if entity == nil {
		switch kind {
		case item.KindGenre, item.KindMusicGenre, item.KindStudio:
			synthetic := library.TaxonomyEntity(kind, name)
			entity = &synthetic
		default:
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
	}
	options := bind.DefaultItemQueryDTOOptions()
	options.EnableUserData = query.UserID != nil
	page, err := h.build.BuildDetailed(r.Context(), []item.Item{*entity}, options, query.UserID)
	if err != nil || len(page.Items) == 0 {
		internalError(w)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, page.Items[0])
}

// lookupNamedEntity returns the stored by-name entity of kind whose clean name
// matches name, or nil. The entity is global, so the lookup carries UserID for
// projection but no access filter, as namedValueItems does.
func lookupNamedEntity(ctx context.Context, reader ItemReader, kind item.Kind, name string, userID *dto.UUID) (*item.Item, error) {
	result, err := reader.Query(ctx, store.ItemQuery{Name: name, IncludeItemTypes: []item.Kind{kind}, UserID: userID, EnableTotalRecordCount: false})
	if err != nil {
		return nil, err
	}
	for index := range result.Items {
		if result.Items[index].Kind == kind {
			return &result.Items[index], nil
		}
	}
	return nil, nil
}

// routeName returns a decoded route name. chi matches on RawPath when the
// request path was percent-encoded (for example "AC%2FDC"), so the parameter
// can still hold escapes.
func routeName(r *http.Request, param string) string {
	raw := chi.URLParam(r, param)
	if r.URL.RawPath == "" {
		return raw
	}
	if decoded, err := url.PathUnescape(raw); err == nil {
		return decoded
	}
	return raw
}
