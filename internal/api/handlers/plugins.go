// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"net/http"
	"strings"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
)

// RegisterPlugins registers the complete pinned Plugin-tagged surface. Deadfin
// reports its four in-tree metadata providers as installed plugins but has no
// plugin loader, so install, uninstall, package, and repository operations
// report that plugin support is not available.
func RegisterPlugins(rt *router.Router) {
	h := pluginStubHandler{}
	policy := string(auth.PolicyRequiresElevation)

	rt.Handle(http.MethodGet, "/web/ConfigurationPage", router.Route{OperationID: "GetDashboardConfigurationPage", Anonymous: true}, h.configurationPage)
	rt.Handle(http.MethodGet, "/web/ConfigurationPages", router.Route{OperationID: "GetConfigurationPages", Policy: policy}, h.configurationPages)
	rt.Handle(http.MethodGet, "/Packages", router.Route{OperationID: "GetPackages", Policy: policy}, h.emptyList)
	rt.Handle(http.MethodGet, "/Packages/{name}", router.Route{OperationID: "GetPackageInfo", Policy: policy}, h.missingPackage)
	rt.Handle(http.MethodPost, "/Packages/Installed/{name}", router.Route{OperationID: "InstallPackage", Policy: policy}, h.pluginSupportUnavailable)
	rt.Handle(http.MethodDelete, "/Packages/Installing/{packageId}", router.Route{OperationID: "CancelPackageInstallation", Policy: policy}, h.noContent)
	rt.Handle(http.MethodGet, "/Repositories", router.Route{OperationID: "GetRepositories", Policy: policy}, h.emptyList)
	rt.Handle(http.MethodPost, "/Repositories", router.Route{OperationID: "SetRepositories", Policy: policy}, h.setRepositories)
	rt.Handle(http.MethodGet, "/Plugins", router.Route{OperationID: "GetPlugins", Policy: policy}, h.plugins)
	rt.Handle(http.MethodDelete, "/Plugins/{pluginId}", router.Route{OperationID: "UninstallPlugin", Policy: policy}, h.pluginSupportUnavailable)
	rt.Handle(http.MethodDelete, "/Plugins/{pluginId}/{version}", router.Route{OperationID: "UninstallPluginByVersion", Policy: policy}, h.pluginSupportUnavailable)
	rt.Handle(http.MethodPost, "/Plugins/{pluginId}/{version}/Disable", router.Route{OperationID: "DisablePlugin", Policy: policy}, h.missingPlugin)
	rt.Handle(http.MethodPost, "/Plugins/{pluginId}/{version}/Enable", router.Route{OperationID: "EnablePlugin", Policy: policy}, h.missingPlugin)
	// Upstream marks the image action AllowAnonymous even though the generated
	// OpenAPI security block inherits the controller's elevation policy.
	rt.Handle(http.MethodGet, "/Plugins/{pluginId}/{version}/Image", router.Route{OperationID: "GetPluginImage", Anonymous: true}, h.missingPlugin)
	rt.Handle(http.MethodGet, "/Plugins/{pluginId}/Configuration", router.Route{OperationID: "GetPluginConfiguration", Policy: policy}, h.missingPlugin)
	rt.Handle(http.MethodPost, "/Plugins/{pluginId}/Configuration", router.Route{OperationID: "UpdatePluginConfiguration", Policy: policy}, h.missingPlugin)
	rt.Handle(http.MethodPost, "/Plugins/{pluginId}/Manifest", router.Route{OperationID: "GetPluginManifest", Policy: policy}, h.missingPlugin)
}

type pluginStubHandler struct{}

type repositoryInfo struct {
	Name    *string `json:"Name,omitzero"`
	URL     *string `json:"Url,omitzero"`
	Enabled bool    `json:"Enabled"`
}

type problemDetails struct {
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

// pluginInfo is the wire shape of MediaBrowser.Model.Plugins.PluginInfo. It
// reports the stable identity of one in-tree metadata provider.
type pluginInfo struct {
	Name                  string `json:"Name"`
	Version               string `json:"Version"`
	ConfigurationFileName string `json:"ConfigurationFileName,omitempty"`
	Description           string `json:"Description"`
	ID                    string `json:"Id"`
	CanUninstall          bool   `json:"CanUninstall"`
	HasImage              bool   `json:"HasImage"`
	Status                string `json:"Status"`
}

// configurationPageInfo is the wire shape of Jellyfin.Api.Models
// .ConfigurationPageInfo. It advertises one configuration page for one plugin.
type configurationPageInfo struct {
	Name             string  `json:"Name"`
	EnableInMainMenu bool    `json:"EnableInMainMenu"`
	MenuSection      *string `json:"MenuSection,omitzero"`
	MenuIcon         *string `json:"MenuIcon,omitzero"`
	DisplayName      string  `json:"DisplayName,omitempty"`
	PluginID         *string `json:"PluginId,omitzero"`
}

// inTreePlugin describes one in-tree metadata provider that Deadfin reports as
// an installed plugin. The Name, Id, and ConfigurationFileName values match
// the pinned Jellyfin 12 source (MediaBrowser.Providers/Plugins/*/Plugin.cs).
// The id is stored in N format (32 lowercase hex digits, no dashes) because the
// pinned JsonGuidConverter writes Guid values in that format on the wire.
type inTreePlugin struct {
	name                  string
	id                    string
	description           string
	configurationFileName string
	pageName              string
	configHTML            string
}

// inTreePlugins is the ordered list of providers Deadfin bundles. Upstream
// orders GetPlugins by Name; the handler sorts at read time to match.
var inTreePlugins = []inTreePlugin{
	{
		name:                  "TMDb",
		id:                    "b8715ed16c4745289ad3f72deb539cd4",
		description:           "Get metadata for movies and other video content from TheMovieDb.",
		configurationFileName: "Jellyfin.Plugin.Tmdb.xml",
		pageName:              "TMDb",
		configHTML:            tmdbConfigHTML,
	},
	{
		name:                  "MusicBrainz",
		id:                    "8c95c4d2e50c4fb0a4f36c06ff0f9a1a",
		description:           "Get artist and album metadata from any MusicBrainz server.",
		configurationFileName: "Jellyfin.Plugin.MusicBrainz.xml",
		pageName:              "MusicBrainz",
		configHTML:            musicbrainzConfigHTML,
	},
	{
		name:                  "AudioDB",
		id:                    "a629c0dafac54c7e931a7174223f14c8",
		description:           "Get artist and album metadata or images from AudioDB.",
		configurationFileName: "Jellyfin.Plugin.AudioDb.xml",
		pageName:              "AudioDB",
		configHTML:            audiodbConfigHTML,
	},
	{
		name:                  "ListenBrainz Similarity Provider",
		id:                    "a5b2e8c19d4f4a3b8c7e6f1a2b3c4d5e",
		description:           "Get similar artist recommendations from ListenBrainz Labs.",
		configurationFileName: "Jellyfin.Plugin.ListenBrainz.xml",
		pageName:              "ListenBrainz Similarity Provider",
		configHTML:            listenbrainzConfigHTML,
	},
}

func (pluginStubHandler) emptyList(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, []struct{}{})
}

func (pluginStubHandler) plugins(w http.ResponseWriter, r *http.Request) {
	plugins := make([]pluginInfo, 0, len(inTreePlugins))
	for _, p := range sortedInTreePlugins() {
		plugins = append(plugins, pluginInfo{
			Name:                  p.name,
			Version:               serverVersion,
			ConfigurationFileName: p.configurationFileName,
			Description:           p.description,
			ID:                    p.id,
			CanUninstall:          false,
			HasImage:              true,
			Status:                "Active",
		})
	}
	_ = render.JSON(w, r, http.StatusOK, plugins)
}

func (pluginStubHandler) configurationPages(w http.ResponseWriter, r *http.Request) {
	pages := make([]configurationPageInfo, 0, len(inTreePlugins))
	for _, p := range sortedInTreePlugins() {
		pluginID := p.id
		pages = append(pages, configurationPageInfo{
			Name:        p.pageName,
			DisplayName: p.name,
			PluginID:    &pluginID,
		})
	}
	_ = render.JSON(w, r, http.StatusOK, pages)
}

func (h pluginStubHandler) configurationPage(w http.ResponseWriter, r *http.Request) {
	name, err := bind.QueryScalar(r, "name")
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		renderProblem(w, r, http.StatusNotFound, "Plugin configuration page not found.")
		return
	}
	for _, p := range inTreePlugins {
		if strings.EqualFold(p.pageName, name) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(p.configHTML))
			return
		}
	}
	renderProblem(w, r, http.StatusNotFound, "Plugin configuration page not found.")
}

func (pluginStubHandler) missingPackage(w http.ResponseWriter, r *http.Request) {
	renderProblem(w, r, http.StatusNotFound, "Package not found.")
}

func (pluginStubHandler) missingPlugin(w http.ResponseWriter, r *http.Request) {
	renderProblem(w, r, http.StatusNotFound, "Plugin not found.")
}

func (pluginStubHandler) pluginSupportUnavailable(w http.ResponseWriter, r *http.Request) {
	renderProblem(w, r, http.StatusNotFound, "Plugin support is not available.")
}

func (pluginStubHandler) noContent(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (pluginStubHandler) setRepositories(w http.ResponseWriter, r *http.Request) {
	var repositories *[]repositoryInfo
	if err := bind.JSONBody(r, &repositories); err != nil || repositories == nil {
		renderProblem(w, r, http.StatusBadRequest, "A repository list is required.")
		return
	}
	// Repository configuration has no effect without plugin loading. Accepting
	// the valid shape keeps the dashboard save action idempotent.
	w.WriteHeader(http.StatusNoContent)
}

// sortedInTreePlugins returns the in-tree providers ordered by Name to match
// upstream GetPlugins, which sorts _pluginManager.Plugins by p.Name.
func sortedInTreePlugins() []inTreePlugin {
	sorted := make([]inTreePlugin, len(inTreePlugins))
	copy(sorted, inTreePlugins)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && strings.ToLower(sorted[j-1].name) > strings.ToLower(sorted[j].name); j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	return sorted
}

func renderProblem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	_ = render.JSON(w, r, status, problemDetails{
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	})
}
