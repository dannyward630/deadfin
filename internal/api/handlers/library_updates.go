// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// ChangeReporter receives "media changed" reports (library.ChangeQueue).
type ChangeReporter interface{ Report() }

// LibraryRoots lists the configured libraries.
type LibraryRoots interface {
	VirtualFolders(context.Context) ([]library.VirtualFolder, error)
}

// ItemQuerier finds items by provider id.
type ItemQuerier interface {
	Query(context.Context, store.ItemQuery) (store.ItemQueryResult, error)
}

// RegisterLibraryUpdates ports the LibraryController endpoints Sonarr and
// Radarr call after an import. Each one reports a change, as upstream reports
// LibraryMonitor.ReportFileSystemChanged; Deadfin's change queue then runs one
// library scan after the quiet period (D-35 in docs/divergences.md).
func RegisterLibraryUpdates(rt *router.Router, changes ChangeReporter, roots LibraryRoots, items ItemQuerier) {
	h := libraryUpdateHandler{changes: changes, roots: roots, items: items}
	policy := string(auth.PolicyDefaultAuthorization)
	rt.Handle(http.MethodPost, "/Library/Media/Updated", router.Route{OperationID: "PostUpdatedMedia", Policy: policy}, h.media)
	rt.Handle(http.MethodPost, "/Library/Series/Updated", router.Route{OperationID: "PostUpdatedSeries", Policy: policy}, h.series)
	rt.Handle(http.MethodPost, "/Library/Series/Added", router.Route{OperationID: "PostAddedSeries", Policy: policy}, h.series)
	rt.Handle(http.MethodPost, "/Library/Movies/Updated", router.Route{OperationID: "PostUpdatedMovies", Policy: policy}, h.movies)
	rt.Handle(http.MethodPost, "/Library/Movies/Added", router.Route{OperationID: "PostAddedMovies", Policy: policy}, h.movies)
}

type libraryUpdateHandler struct {
	changes ChangeReporter
	roots   LibraryRoots
	items   ItemQuerier
}

// media ports PostUpdatedMedia. A missing path is a bad request (upstream
// throws ArgumentException). A path outside every library is ignored, as
// LibraryMonitor ignores it.
func (h libraryUpdateHandler) media(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Updates []struct {
			Path       *string `json:"Path"`
			UpdateType *string `json:"UpdateType"`
		} `json:"Updates"`
	}
	if err := bind.JSONBody(r, &body); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	for _, update := range body.Updates {
		if update.Path == nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
	}
	folders, err := h.roots.VirtualFolders(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	for _, update := range body.Updates {
		if inLibrary(*update.Path, folders) {
			h.changes.Report()
			break
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// series ports PostUpdatedSeries: a change is reported only when a series
// carries the Tvdb id.
func (h libraryUpdateHandler) series(w http.ResponseWriter, r *http.Request) {
	tvdbID, err := bind.QueryScalar(r, "tvdbId")
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	h.reportIfFound(w, r, item.KindSeries, "Tvdb", tvdbID)
}

// movies ports PostUpdatedMovies: imdbId wins over tmdbId; with neither,
// nothing is reported.
func (h libraryUpdateHandler) movies(w http.ResponseWriter, r *http.Request) {
	imdbID, err := bind.QueryScalar(r, "imdbId")
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	tmdbID, err := bind.QueryScalar(r, "tmdbId")
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(imdbID) != "" {
		h.reportIfFound(w, r, item.KindMovie, "Imdb", imdbID)
		return
	}
	h.reportIfFound(w, r, item.KindMovie, "Tmdb", tmdbID)
}

func (h libraryUpdateHandler) reportIfFound(w http.ResponseWriter, r *http.Request, kind item.Kind, provider, id string) {
	if strings.TrimSpace(id) != "" {
		limit := 1
		found, err := h.items.Query(r.Context(), store.ItemQuery{
			Recursive:        true,
			IncludeItemTypes: []item.Kind{kind},
			HasAnyProviderID: map[string]string{provider: id},
			Limit:            &limit,
		})
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if len(found.Items) > 0 {
			h.changes.Report()
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func inLibrary(path string, folders []library.VirtualFolder) bool {
	path = filepath.Clean(path)
	for _, folder := range folders {
		for _, root := range folder.MediaPaths {
			root = filepath.Clean(root)
			if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}
