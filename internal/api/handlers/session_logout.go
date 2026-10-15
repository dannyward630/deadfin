// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/user"
)

// SessionSockets closes the live WebSocket controllers of an ended session.
type SessionSockets interface {
	CloseSession(ctx context.Context, sessionID string) error
}

// RegisterSessionLogout registers POST /Sessions/Logout. It ports
// SessionController.ReportSessionEnded.
func RegisterSessionLogout(rt *router.Router, users *user.Service, sessions *session.Manager, sockets SessionSockets) {
	handler := sessionLogoutHandler{users: users, sessions: sessions, sockets: sockets}
	rt.Handle(http.MethodPost, "/Sessions/Logout", router.Route{OperationID: "ReportSessionEnded", Policy: string(auth.PolicyDefaultAuthorization)}, handler.logout)
}

type sessionLogoutHandler struct {
	users    *user.Service
	sessions *session.Manager
	sockets  SessionSockets
}

// logout ports SessionManager.Logout(string): it deletes the device row of the
// caller's token, then ends every session of that device. A token without a
// device row (an API key) is a no-op, as upstream.
func (h sessionLogoutHandler) logout(w http.ResponseWriter, r *http.Request) {
	identity, _ := middleware.IdentityFromContext(r.Context())
	if h.users == nil || identity.Token == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	device, err := h.users.Token(r.Context(), identity.Token)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if err := h.users.Logout(r.Context(), device.AccessToken); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if h.sessions != nil {
		for _, id := range h.sessions.EndDeviceSessions(device.DeviceID) {
			if h.sockets != nil {
				_ = h.sockets.CloseSession(r.Context(), id)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
