// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/auth/quickconnect"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/sessiondto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/user"
)

// RegisterQuickConnect registers the five QuickConnect routes, including the
// redemption route. All dependencies may be nil during pure route-ledger
// construction; routes are still registered so the ledger export is complete.
// The QuickConnect state machine is constructed internally from the clock and
// enabled-func seams so the caller (application.go) does not import the
// quickconnect package.
func RegisterQuickConnect(rt *router.Router, qcClock clock.Clock, qcEnabled func() bool, login *auth.Login, users *user.Service, sessions *session.Manager, items *store.ItemStore, itemBuilder *dtobuild.Builder, identity config.ServerIdentity) {
	var qc *quickconnect.Manager
	if qcClock != nil && qcEnabled != nil {
		qc = quickconnect.New(quickconnect.Config{Clock: qcClock, Enabled: qcEnabled})
	}
	h := quickConnectHandler{
		qc:          qc,
		login:       login,
		users:       users,
		sessions:    sessions,
		items:       items,
		itemBuilder: itemBuilder,
		identity:    identity,
	}
	rt.Handle(http.MethodGet, "/QuickConnect/Enabled", router.Route{OperationID: "GetQuickConnectEnabled", Anonymous: true}, h.getEnabled)
	rt.Handle(http.MethodPost, "/QuickConnect/Initiate", router.Route{OperationID: "InitiateQuickConnect", Anonymous: true}, h.initiate)
	rt.Handle(http.MethodGet, "/QuickConnect/Connect", router.Route{OperationID: "GetQuickConnectState", Anonymous: true}, h.connect)
	rt.Handle(http.MethodPost, "/QuickConnect/Authorize", router.Route{OperationID: "AuthorizeQuickConnect", Policy: string(auth.PolicyDefaultAuthorization)}, h.authorize)
	rt.Handle(http.MethodPost, "/Users/AuthenticateWithQuickConnect", router.Route{OperationID: "AuthenticateWithQuickConnect", Anonymous: true}, h.authenticateWithQuickConnect)
}

type quickConnectHandler struct {
	qc          *quickconnect.Manager
	login       *auth.Login
	users       *user.Service
	sessions    *session.Manager
	items       *store.ItemStore
	itemBuilder *dtobuild.Builder
	identity    config.ServerIdentity
}

// getEnabled returns the bare JSON boolean QuickConnect state
// (QuickConnectController.cs:43-46). The stock response is a bare true/false
// body with Content-Type text/plain, matching the Swiftfin decoder that reads
// raw Bool and falls back to false on any other shape.
func (h quickConnectHandler) getEnabled(w http.ResponseWriter, r *http.Request) {
	enabled := h.qc != nil && h.qc.Enabled()
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(booleanBody(enabled)))
}

// booleanBody returns the bare "true" or "false" string matching the stock
// response shape (WP-P1-06.md:43,51).
func booleanBody(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// initiate creates a new QuickConnect request from the request's authorization
// metadata (QuickConnectController.cs:57-68). Metadata validation precedes the
// enabled check. A disabled server returns 401 with application/json body
// "Quick connect is disabled".
func (h quickConnectHandler) initiate(w http.ResponseWriter, r *http.Request) {
	// Legacy authorization is not relevant for the anonymous Initiate route;
	// the metadata comes from the Authorization header regardless of token
	// state.
	c := auth.Extract(r, false)
	source := middleware.ClientAddressFromContext(r.Context())
	if !source.IsValid() {
		source = middleware.NormalizeClientAddress(r.RemoteAddr)
	}
	result, err := h.qc.Initiate(source.String(), c.DeviceID, c.Device, c.Client, c.Version)
	if err != nil {
		writeQuickConnectError(w, r, err)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, dto.NewQuickConnectResult(false, result.Secret, result.Code, result.DateAdded, result.DeviceID, result.DeviceName, result.AppName, result.AppVersion))
}

// connect polls the pending QuickConnect request matching the secret
// (QuickConnectController.cs:80-94). A disabled server returns 401; an
// unknown secret returns 404 with text/plain body "Unknown secret".
func (h quickConnectHandler) connect(w http.ResponseWriter, r *http.Request) {
	var params struct {
		Secret string `query:"secret"`
	}
	if err := bind.QueryRequest(r, &params); err != nil {
		writeValidationError(w, "secret")
		return
	}
	if params.Secret == "" {
		writeValidationError(w, "secret")
		return
	}
	result, err := h.qc.Connect(params.Secret)
	if err != nil {
		writeQuickConnectError(w, r, err)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, dto.NewQuickConnectResult(result.Authenticated, result.Secret, result.Code, result.DateAdded, result.DeviceID, result.DeviceName, result.AppName, result.AppVersion))
}

// authorize authorizes a pending QuickConnect request for the caller or an
// administrator-selected target user
// (QuickConnectController.cs:108-120, RequestHelpers.cs:67-85).
//
// The target defaults to the authenticated caller. A different non-nil target
// is permitted only to an administrator; otherwise 403. The authorized
// authentication result is built and stored at authorization time using the
// direct-authentication seam.
func (h quickConnectHandler) authorize(w http.ResponseWriter, r *http.Request) {
	var params struct {
		Code   string `query:"code"`
		UserID string `query:"userId"`
	}
	if err := bind.QueryRequest(r, &params); err != nil {
		writeValidationError(w, "code")
		return
	}
	if params.Code == "" {
		writeValidationError(w, "code")
		return
	}
	identity, authenticated := middleware.IdentityFromContext(r.Context())
	if !authenticated || !identity.Authenticated {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// Validate the code and authorization state before authenticating.
	// A valid QuickConnect authorization authenticates the device that
	// initiated the request, not the device performing the authorization
	// (QuickConnectManager.cs:141-161).
	pending, err := h.qc.Lookup(params.Code)
	if err != nil {
		writeQuickConnectError(w, r, err)
		return
	}
	// Resolve the target user. The target defaults to the caller; an
	// explicit nil UUID also defaults to the caller. A different non-nil
	// target is permitted only to an administrator; otherwise 403.
	// Actor lookup failures return 500, matching resolveSelectedUser
	// (RequestHelpers.GetUserId, item_user.go:61-100).
	targetID := identity.UserID
	if params.UserID != "" {
		parsed, err := dto.ParseUUID(params.UserID)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		if !parsed.IsNil() && parsed != identity.UserID {
			administrator := identity.Administrator
			if !administrator && h.users != nil {
				actor, err := h.users.Get(r.Context(), identity.UserID)
				if err != nil {
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
					return
				}
				var policy struct{ IsAdministrator bool }
				if len(actor.Policy) != 0 && json.Unmarshal(actor.Policy, &policy) == nil {
					administrator = policy.IsAdministrator
				}
			}
			if !administrator {
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}
		}
		if !parsed.IsNil() {
			targetID = parsed
		}
	}

	// Build the authentication result using direct authentication with the
	// initiating device's metadata, not the authorizer's
	// (SessionManager.cs:1630-1704).
	result, err := h.login.AuthenticateDirect(r.Context(), auth.DirectLoginRequest{
		UserID:         targetID,
		Client:         pending.AppName,
		DeviceID:       pending.DeviceID,
		Device:         pending.DeviceName,
		Version:        pending.AppVersion,
		RemoteEndPoint: requestRemoteHost(r),
	})
	if err != nil {
		status := http.StatusInternalServerError
		var loginError *auth.LoginError
		if errors.As(err, &loginError) {
			status = loginError.Status
		}
		http.Error(w, http.StatusText(status), status)
		return
	}

	// Project the user and session into the stored AuthenticationResult.
	projection, err := h.users.GetDTO(r.Context(), result.User.ID)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	userDTO := h.projectQuickConnectUser(projection)
	userDTO.Policy.InvalidLoginAttemptCount = loginInvalidAttemptCount(result.User.Policy)
	authenticationResult := dto.AuthenticationResult{
		User:        userDTO,
		SessionInfo: h.projectQuickConnectSession(r.Context(), result.Session, result.User.Name),
		AccessToken: result.AccessToken,
		ServerID:    result.ServerID,
	}
	if err := h.qc.Authorize(params.Code, targetID, authenticationResult); err != nil {
		writeQuickConnectError(w, r, err)
		return
	}
	// Stock Authorize returns bare true with text/plain
	// (WP-P1-06.md:76).
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("true"))
}

// authenticateWithQuickConnect redeems the authorized secret for the stored
// authentication result (UserController.cs:248-259). The result is replayed
// without consumption during its lifetime.
func (h quickConnectHandler) authenticateWithQuickConnect(w http.ResponseWriter, r *http.Request) {
	var body dto.QuickConnectRedemptionRequest
	if err := bind.JSONBody(r, &body); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if body.Secret == "" {
		writeValidationError(w, "Secret")
		return
	}
	result, err := h.qc.Redeem(body.Secret)
	if err != nil {
		writeQuickConnectError(w, r, err)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, result)
}

// writeQuickConnectError maps QuickConnect state machine errors to the stock
// HTTP response shapes of QuickConnectController.cs.
func writeQuickConnectError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, quickconnect.ErrDisabled):
		// Disabled Initiate/Connect/Authorize/Redeem: 401, application/json,
		// body "Quick connect is disabled" (WP-P1-06.md:44).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`"Quick connect is disabled"`))
	case errors.Is(err, quickconnect.ErrUnknownSecret):
		// Unknown secret on Connect: 404, text/plain, body "Unknown secret"
		// (WP-P1-06.md:171).
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Unknown secret"))
	case errors.Is(err, quickconnect.ErrNotFound):
		// Unknown code on Authorize or expired secret on Redeem: 404,
		// text/plain for Authorize, application/json for Redeem
		// (WP-P1-06.md:174,141-144).
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateWithQuickConnect" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"ResourceNotFound","message":"Unable to find request"}}`))
		} else {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("Error processing request."))
		}
	case errors.Is(err, quickconnect.ErrTooManyRequests):
		// Pending-request bound reached (D-55): 429, text/plain.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("Too many pending quick connect requests."))
	case errors.Is(err, quickconnect.ErrMetadataRequired), errors.Is(err, quickconnect.ErrMetadataTooLong):
		// Missing device metadata on Initiate: 400, text/plain, body
		// "Error processing request." (WP-P1-06.md:173). Overlong metadata
		// (D-55) answers the same way.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Error processing request."))
	case errors.Is(err, quickconnect.ErrAlreadyAuthorized):
		// Already-authorized: the stock controller lets the
		// InvalidOperationException propagate. The evidence matrix does not
		// list this case separately; return 500 as the unhandled-exception
		// default.
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	default:
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

// writeValidationError writes the stock ASP.NET model-validation error
// response: 400 with Content-Type application/json and a body matching the
// HttpValidationProblemDetails shape produced by [ApiController] auto-
// validation when a [Required] parameter is missing
// (WP-P1-06.md:172, captured validation evidence from
// .diffs/deadfin-login-seeded-issues/010-...).
func writeValidationError(w http.ResponseWriter, field string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = fmt.Fprintf(w, `{"errors":{"%s":["The %s field is required."]},"status":400,"title":"One or more validation errors occurred.","type":"https://tools.ietf.org/html/rfc9110#section-15.5.1"}`, field, field)
}

// projectQuickConnectUser projects a user DTO for the stored authentication
// result, reusing the same projection as password login.
func (h quickConnectHandler) projectQuickConnectUser(v user.UserDTO) dto.UserDto {
	return authHandler{
		users:    h.users,
		identity: h.identity,
	}.projectUser(v)
}

// projectQuickConnectSession projects a session DTO for the stored
// authentication result, reusing the same projection as password login.
func (h quickConnectHandler) projectQuickConnectSession(ctx context.Context, snapshot session.Snapshot, userName string) dto.SessionInfoDto {
	return sessiondto.New(h.users, h.items, h.itemBuilder, h.identity.ID.String()).Project(ctx, snapshot, userName)
}
