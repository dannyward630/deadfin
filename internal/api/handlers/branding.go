// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
)

// RegisterBranding registers public branding reads and elevated splash writes on rt.
func RegisterBranding(rt *router.Router, manager *config.Manager) {
	h := branding{manager: manager}

	rt.Handle(http.MethodGet, "/Branding/Configuration", router.Route{
		OperationID: "GetBrandingOptions",
		Anonymous:   true,
	}, h.configuration)
	rt.Handle(http.MethodGet, "/Branding/Css", router.Route{
		OperationID: "GetBrandingCss",
		Anonymous:   true,
	}, h.css)
	rt.Handle(http.MethodGet, "/Branding/Css.css", router.Route{
		OperationID: "GetBrandingCss_2",
		Anonymous:   true,
	}, h.css)
	rt.Handle(http.MethodGet, "/Branding/Splashscreen", router.Route{
		OperationID: "GetSplashscreen",
		Anonymous:   true,
	}, h.splashscreen)
	rt.Handle(http.MethodPost, "/Branding/Splashscreen", router.Route{
		OperationID: "UploadCustomSplashscreen",
		Policy:      string(auth.PolicyRequiresElevation),
	}, h.uploadSplashscreen)
	rt.Handle(http.MethodDelete, "/Branding/Splashscreen", router.Route{
		OperationID: "DeleteCustomSplashscreen",
		Policy:      string(auth.PolicyRequiresElevation),
	}, h.deleteSplashscreen)
}

type branding struct {
	manager *config.Manager
}

func (h branding) configuration(w http.ResponseWriter, r *http.Request) {
	branding := h.manager.Branding()
	_ = render.JSON(w, r, http.StatusOK, dto.BrandingConfiguration{
		LoginDisclaimer:     optionalBrandingString(branding.LoginDisclaimer),
		CustomCss:           optionalBrandingString(branding.CustomCss),
		SplashscreenEnabled: branding.SplashscreenEnabled,
	})
}

func (h branding) css(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, h.manager.Branding().CustomCss)
}

const maxSplashscreenBytes int64 = 32 << 20

func (h branding) splashscreen(w http.ResponseWriter, r *http.Request) {
	location := strings.TrimSpace(h.manager.Branding().SplashscreenLocation)
	if location == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	file, err := os.Open(location)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if info.Size() > maxSplashscreenBytes {
		http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		return
	}

	contentType := mime.TypeByExtension(filepath.Ext(location))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, info.Name(), info.ModTime(), io.NewSectionReader(file, 0, info.Size()))
}

func (branding) uploadSplashscreen(w http.ResponseWriter, r *http.Request) {
	renderProblem(w, r, http.StatusBadRequest, "Custom splashscreens are not supported.")
}

func (branding) deleteSplashscreen(w http.ResponseWriter, _ *http.Request) {
	// With no writable custom splashscreen store, deletion is idempotently
	// complete when the default configuration has no custom splashscreen.
	w.WriteHeader(http.StatusNoContent)
}

func optionalBrandingString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
