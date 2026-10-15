// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

// APIKeyStore persists server API keys.
type APIKeyStore interface {
	ListAPIKeys(context.Context) ([]store.APIKey, error)
	PutAPIKey(context.Context, store.APIKey) (store.APIKey, error)
	DeleteAPIKey(context.Context, string) error
}

// RegisterAPIKeys ports ApiKeyController (/Auth/Keys). random supplies the
// token bytes; clk supplies DateCreated.
func RegisterAPIKeys(rt *router.Router, keys APIKeyStore, clk clock.Clock, random io.Reader) {
	h := apiKeyHandler{keys: keys, clock: clk, random: random}
	policy := string(auth.PolicyRequiresElevation)
	rt.Handle(http.MethodGet, "/Auth/Keys", router.Route{OperationID: "GetKeys", Policy: policy}, h.list)
	rt.Handle(http.MethodPost, "/Auth/Keys", router.Route{OperationID: "CreateKey", Policy: policy}, h.create)
	rt.Handle(http.MethodDelete, "/Auth/Keys/{key}", router.Route{OperationID: "RevokeKey", Policy: policy}, h.revoke)
}

type apiKeyHandler struct {
	keys   APIKeyStore
	clock  clock.Clock
	random io.Reader
}

func (h apiKeyHandler) list(w http.ResponseWriter, r *http.Request) {
	keys, err := h.keys.ListAPIKeys(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	result := dto.NewQueryResult[dto.AuthenticationInfo]()
	empty := ""
	for _, key := range keys {
		name, token := key.Name, key.AccessToken
		result.Items = append(result.Items, dto.AuthenticationInfo{
			AccessToken: &token, AppName: &name, DateCreated: dto.NewTime(key.DateCreated),
			DeviceID: &empty, DeviceName: &empty, AppVersion: &empty,
		})
	}
	result.TotalRecordCount = len(result.Items)
	_ = render.JSON(w, r, http.StatusOK, result)
}

// create adds a key named app with a random token (Guid "N" format), as
// new ApiKey(name) does.
func (h apiKeyHandler) create(w http.ResponseWriter, r *http.Request) {
	app, err := bind.QueryScalar(r, "app")
	if err != nil || strings.TrimSpace(app) == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	token, err := h.token()
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	now := h.clock.Now().UTC()
	if _, err := h.keys.PutAPIKey(r.Context(), store.APIKey{Name: app, AccessToken: token, DateCreated: now, DateLastActivity: now}); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h apiKeyHandler) revoke(w http.ResponseWriter, r *http.Request) {
	if err := h.keys.DeleteAPIKey(r.Context(), chi.URLParam(r, "key")); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h apiKeyHandler) token() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(h.random, raw[:]); err != nil {
		return "", fmt.Errorf("api key token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
