// SPDX-License-Identifier: GPL-2.0-only

// BaseUrl prefix mount middleware. See Mount and PathBase.
package router

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// pathBaseContextKey is the private, immutable context key for the normalized
// configured BaseUrl prefix. It is unexported so no package-level mutable
// state escapes and no external code can set a forged prefix.
type pathBaseContextKey struct{}

// PathBase returns the normalized configured BaseUrl prefix stored in ctx by
// Mount, or the empty string when no prefix was set (empty BaseUrl or an
// unprefixed request that bypassed the mount).
func PathBase(ctx context.Context) string {
	base, _ := ctx.Value(pathBaseContextKey{}).(string)
	return base
}

// Mount wraps inner with the configured BaseUrl prefix behavior, reproducing
// the outer middleware plus ASP.NET Map behavior shown at
// upstream/jellyfin/Jellyfin.Server/Startup.cs:171-175 and
// upstream/jellyfin/Jellyfin.Api/Middleware/BaseUrlRedirectionMiddleware.cs:43-76.
//
// When the normalized base is empty, Mount returns a wrapper that redirects
// GET / to the relative "web/" target with status 302, and delegates every
// other request to inner unchanged with an empty PathBase. This matches the
// stock capture. For a non-empty base it:
//   - matches the configured prefix case-insensitively and only at a
//     path-segment boundary,
//   - redirects the bare base and base-plus-slash to the relative
//     "{base}/web/" target (form confirmed by stock captures),
//   - redirects every unprefixed method/path to the same target using
//     RFC 3986 relative Location written directly (not via http.Redirect).
//     The Location values match the stock captures,
//   - special-cases unprefixed /health and /health/ (case-insensitive) to
//     302 Location: {base}/health,
//   - strips the matched prefix before calling inner, preserving the query,
//     clearing stale RawPath, and storing the prefix in request context.
//
// A path such as /jfish for base /jf follows the pinned Map non-match behavior
// and does not dispatch as /ish.
func Mount(base string, inner http.Handler) http.Handler {
	normalized := normalizeMountBase(base)
	if normalized == "" {
		return &emptyBaseHandler{inner: inner}
	}
	return &mountHandler{
		base:  normalized,
		inner: inner,
	}
}

func normalizeMountBase(base string) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	value := base
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	if strings.HasSuffix(value, "/") {
		value = value[:len(value)-1]
	}
	return value
}

type mountHandler struct {
	base  string
	inner http.Handler
}

// emptyBaseHandler wraps inner for the empty-BaseUrl case. It redirects GET /
// to the relative "web/" target with status 302, and delegates every other
// request to inner unchanged. PathBase is empty because no prefix is set.
type emptyBaseHandler struct {
	inner http.Handler
}

func (h *emptyBaseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		w.Header().Set("Location", "web/")
		w.WriteHeader(http.StatusFound)
		return
	}
	h.inner.ServeHTTP(w, r)
}

func (h *mountHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	lower := strings.ToLower(path)
	baseLower := strings.ToLower(h.base)

	// Unprefixed /health special case: redirect to {base}/health.
	if lower == "/health" || lower == "/health/" {
		w.Header().Set("Location", h.base+"/health")
		w.WriteHeader(http.StatusFound)
		return
	}

	// Bare base or base-plus-slash: redirect to {base}/web/ using the
	// relative form. The bare base is treated as an unprefixed request
	// by BaseUrlRedirectionMiddleware (it equals the prefix), so it shares
	// the same relative redirect target as the root path.
	if len(path) == len(h.base) && lower == baseLower {
		h.redirectUnprefixed(w, path)
		return
	}
	if len(path) == len(h.base)+1 && lower == baseLower+"/" {
		h.redirectUnprefixed(w, path)
		return
	}

	// Prefixed request: strip and dispatch.
	if strings.HasPrefix(lower, baseLower+"/") {
		h.dispatchStripped(w, r, path)
		return
	}

	// All other unprefixed requests: redirect to {base}/web/.
	h.redirectUnprefixed(w, path)
}

// redirectUnprefixed writes the RFC 3986 relative Location for an
// unprefixed request, matching BaseUrlRedirectionMiddleware.cs:66-72. The
// target path is "{base}/web/". Compute the relative form with
// Uri.MakeRelativeUri semantics. Write Location directly. http.Redirect
// values match the stock captures.
func (h *mountHandler) redirectUnprefixed(w http.ResponseWriter, path string) {
	w.Header().Set("Location", makeRelativeWeb(path, h.base+"/web/"))
	w.WriteHeader(http.StatusFound)
}

// makeRelativeWeb computes the RFC 3986 relative path from the request path's
// directory to the target path, matching .NET's Uri.MakeRelativeUri. The
// source path's last segment is treated as a file and stripped, unless the
// path ends in a slash (directory). The common directory prefix is found at
// segment boundaries. The result is the "../" steps up from the source
// directory to the common prefix, followed by the remaining target segments.
func makeRelativeWeb(sourcePath, targetPath string) string {
	sourceDir := directoryOf(sourcePath)
	sourceSegs := splitSegments(sourceDir)
	targetSegs := splitSegments(targetPath)

	// Find the common prefix length at segment boundaries.
	common := 0
	for common < len(sourceSegs) && common < len(targetSegs) && sourceSegs[common] == targetSegs[common] {
		common++
	}

	up := len(sourceSegs) - common
	down := targetSegs[common:]

	var b strings.Builder
	for range up {
		b.WriteString("../")
	}
	for _, seg := range down {
		b.WriteString(seg)
		b.WriteString("/")
	}
	return b.String()
}

// directoryOf returns the directory portion of a path: for paths ending in
// "/", the path itself. Otherwise the path with the last segment removed.
func directoryOf(path string) string {
	if strings.HasSuffix(path, "/") {
		return path
	}
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return "/"
	}
	return path[:idx+1]
}

// splitSegments returns the non-empty path segments, preserving order.
func splitSegments(path string) []string {
	parts := strings.Split(path, "/")
	segs := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			segs = append(segs, p)
		}
	}
	return segs
}

// dispatchStripped strips the matched prefix and dispatches to inner.
func (h *mountHandler) dispatchStripped(w http.ResponseWriter, r *http.Request, path string) {
	stripped := path[len(h.base):]
	if stripped == "" {
		stripped = "/"
	}
	newReq := r.Clone(r.Context())
	newReq.URL = &url.URL{
		Path:     stripped,
		RawQuery: r.URL.RawQuery,
		// Clear stale RawPath after rewriting. The inner router re-parses.
		RawPath: "",
	}
	newReq = newReq.WithContext(context.WithValue(newReq.Context(), pathBaseContextKey{}, h.base))
	h.inner.ServeHTTP(w, newReq)
}
