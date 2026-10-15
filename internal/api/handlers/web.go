// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/router"
	dfpath "github.com/deadfin-org/deadfin/internal/path"
)

// registerWeb serves the web client distribution at /web, matching upstream
// Jellyfin.Server/Startup.cs:200-227 (UseDefaultFiles + UseStaticFiles mounted
// before authentication) and BaseUrlRedirectionMiddleware.cs (/ redirects to
// web/). It is a no-op when webPath is empty or not an existing directory, so
// asset-less deployments keep today's route table.
func registerWeb(rt *router.Router, webPath string) {
	if webPath == "" {
		return
	}
	if info, err := os.Stat(webPath); err != nil || !info.IsDir() {
		return
	}
	h := web{root: webPath}

	rt.Handle(http.MethodGet, "/", router.Route{
		OperationID: "SHADOW:RedirectRootToWebClient",
		Anonymous:   true,
	}, h.redirectRoot)
	rt.Handle(http.MethodGet, "/web", router.Route{
		OperationID: "SHADOW:RedirectWebClientRoot",
		Anonymous:   true,
	}, h.redirectSlash)
	rt.Handle(http.MethodGet, "/web/*", router.Route{
		OperationID: "SHADOW:GetWebClientResource",
		Anonymous:   true,
	}, h.serve)
	rt.Handle(http.MethodGet, "/robots.txt", router.Route{
		OperationID: "SHADOW:RedirectRobotsTxt",
		Anonymous:   true,
	}, h.redirectRobots)
}

type web struct {
	root string
}

// redirectRoot mirrors upstream BaseUrlRedirectionMiddleware with an empty
// BaseUrl: GET / answers 302 with the relative target "web/". The Location
// header is written directly because http.Redirect would resolve the target
// against the request path and emit "/web/" instead of upstream's literal
// relative form.
func (web) redirectRoot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Location", "web/")
	w.WriteHeader(http.StatusFound)
}

// redirectRobots redirects /robots.txt to web/robots.txt, matching
// upstream/jellyfin/Jellyfin.Api/Middleware/RobotsRedirectionMiddleware.cs:34-43.
// Write the literal relative "web/robots.txt" directly. http.Redirect
// resolves it against the request path. Under a base, /jf/robots.txt
// resolves the relative target to /jf/web/robots.txt.
func (web) redirectRobots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Location", "web/robots.txt")
	w.WriteHeader(http.StatusFound)
}

// redirectSlash mirrors ASP.NET's default-files redirect: a directory hit
// without a trailing slash answers 301 to the slashed form. Upstream's
// Microsoft.AspNetCore.StaticFiles default-files middleware emits an
// absolute Location carrying the request scheme and host
// (http://127.0.0.1:8097/web/), so the redirect must too — a path-relative
// /web/ does not match the pinned image's header shape. The query string is
// preserved (/web?a=b -> http://host/web/?a=b). Under a non-empty BaseUrl the
// configured prefix is prepended so /jf/web redirects to
// http://host/jf/web/, with its query preserved.
func (web) redirectSlash(w http.ResponseWriter, r *http.Request) {
	base := router.PathBase(r.Context())
	location := base + r.URL.EscapedPath() + "/"
	if r.URL.RawQuery != "" {
		location += "?" + r.URL.RawQuery
	}
	w.Header().Set("Location", absoluteLocation(r, location))
	w.WriteHeader(http.StatusMovedPermanently)
}

// absoluteLocation prefixes a path-relative Location with the request
// scheme and host, producing the absolute form upstream's ASP.NET middleware
// emits. It reads scheme from r.URL.Scheme (reverse proxies set this) falling
// back to the TLS check, and host from r.Host falling back to r.URL.Host.
func absoluteLocation(r *http.Request, path string) string {
	scheme := r.URL.Scheme
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	if host == "" {
		// No host to absolutize against: leave the relative form, which is
		// still a valid, followable Location.
		return path
	}
	return scheme + "://" + host + path
}

func (h web) serve(w http.ResponseWriter, r *http.Request) {
	rest := chi.URLParam(r, "*")
	// chi routes on RawPath when the request path was percent-encoded, so the
	// wildcard may arrive encoded; decode before containment and lookup.
	if decoded, err := url.PathUnescape(rest); err == nil {
		rest = decoded
	}

	// Containment: reject any request whose cleaned relative path would
	// escape the web root ("..", absolute paths). Never serve such a file.
	name := h.root
	if rest != "" {
		local, ok := dfpath.JoinLocal(h.root, filepath.FromSlash(strings.TrimSuffix(rest, "/")))
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		name = local
	}
	info, err := os.Stat(name)
	if err == nil && info.IsDir() {
		// Upstream's UseDefaultFiles maps a directory request to its
		// index.html only when that default file exists. A directory
		// without index.html is not redirected and not served — it falls
		// through to a bare 404 (verified against the pinned image:
		// /web/<no-index-dir> -> 404, not a slash redirect).
		index := filepath.Join(name, "index.html")
		indexInfo, statErr := os.Stat(index)
		if statErr != nil || !indexInfo.Mode().IsRegular() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// A default-file hit without a trailing slash redirects to the
		// slashed form first; the slashed form then serves index.html.
		if !strings.HasSuffix(r.URL.Path, "/") {
			h.redirectSlash(w, r)
			return
		}
		name = index
		info = indexInfo
	}
	if err != nil || !info.Mode().IsRegular() {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	file, err := os.Open(name)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer func() { _ = file.Close() }()

	w.Header().Set("Content-Type", webContentType(name))
	if filepath.Base(name) == "index.html" {
		// Startup.cs:219-222 sets no-cache on index.html only, matched by
		// exact filename.
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), io.NewSectionReader(file, 0, info.Size()))
}

// webContentType resolves the response content type like upstream's
// FileExtensionContentTypeProvider with the two extra mappings from
// Startup.cs:202-206 (.data and .mem for subtitles-octopus). The .NET
// provider returns bare media types with no parameters, so the Go
// mime.TypeByExtension charset parameter (text/html; charset=utf-8) is
// stripped to match the pinned image's header shape (text/html).
func webContentType(name string) string {
	extension := filepath.Ext(name)
	switch extension {
	case ".data", ".mem":
		return "application/octet-stream"
	}
	contentType := mime.TypeByExtension(extension)
	if contentType == "" {
		return "application/octet-stream"
	}
	if base, _, err := mime.ParseMediaType(contentType); err == nil {
		return base
	}
	return contentType
}
