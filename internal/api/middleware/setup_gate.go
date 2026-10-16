// SPDX-License-Identifier: GPL-2.0-only

package middleware

import (
	"crypto/subtle"
	"net/http"
	"net/netip"

	"github.com/deadfin-org/deadfin/internal/api/router"
)

// SetupCookieName is the cookie that carries the first-run setup token from
// the unlock route to the startup wizard requests of the same browser.
const SetupCookieName = "deadfin-setup"

// setupOpenOperation reports the routes a client needs before it can know
// that setup is pending: server discovery, branding, the web client files, and
// the unlock route. They carry no account data.
func setupOpenOperation(operationID string) bool {
	switch operationID {
	case "GetPublicSystemInfo", "GetPingSystem", "PostPingSystem",
		"GetBrandingOptions", "GetBrandingCss", "GetBrandingCss_2", "GetSplashscreen",
		"NATIVE:GetBuild", "NATIVE:UnlockSetup",
		"SHADOW:RedirectRootToWebClient", "SHADOW:RedirectWebClientRoot", "SHADOW:GetWebClientResource", "SHADOW:RedirectRobotsTxt":
		return true
	default:
		return false
	}
}

// relayed reports a request that a proxy forwarded. Deadfin does not trust
// forwarded addresses, so the transport peer of such a request is the proxy,
// not the client.
func relayed(header http.Header) bool {
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip"} {
		if len(header.Values(name)) != 0 {
			return true
		}
	}
	return false
}

type setupStartup interface {
	StartupWizardCompleted() bool
}

// SetupNetwork classifies the transport peer of a first-run request.
type SetupNetwork interface {
	IsInLocalNetwork(netip.Addr) bool
}

// SetupGate confines the first-run surface while the startup wizard is not
// complete (divergence D-53). Pinned Jellyfin serves its wizard routes to
// every peer until setup completes (FirstTimeSetupHandler.cs:29), so the first
// remote caller of a fresh public instance could choose the administrator
// password. The gate admits a request on any other route only when the
// transport peer is in the local network and no proxy relayed the request, or
// when the request carries the one-time setup token in SetupCookieName. After
// setup completes the gate passes every request unchanged.
type SetupGate struct {
	startup setupStartup
	network SetupNetwork
	proxies KnownProxies
	token   string
}

// NewSetupGate constructs the first-run gate. token is the one-time secret the
// composition root logs at startup; an empty token disables the cookie path.
// proxies, when not nil, lets SecureCookie honor X-Forwarded-Proto from a
// known proxy.
func NewSetupGate(startup setupStartup, network SetupNetwork, proxies KnownProxies, token string) *SetupGate {
	return &SetupGate{startup: startup, network: network, proxies: proxies, token: token}
}

// SecureCookie reports whether the setup cookie for request needs the Secure
// attribute: the client reached Deadfin over HTTPS, directly or through a
// known proxy that terminates TLS and sends X-Forwarded-Proto: https. A
// direct plain-HTTP request gets a cookie without Secure, so first-run setup
// over plain HTTP on the LAN still works.
func (gate *SetupGate) SecureCookie(request *http.Request) bool {
	var proxies KnownProxies
	if gate != nil {
		proxies = gate.proxies
	}
	return RequestIsHTTPS(request, proxies)
}

// Pending reports whether the startup wizard is not complete.
func (gate *SetupGate) Pending() bool {
	return gate != nil && gate.startup != nil && !gate.startup.StartupWizardCompleted()
}

// ValidToken reports whether candidate is the setup token, in constant time.
func (gate *SetupGate) ValidToken(candidate string) bool {
	if gate == nil || gate.token == "" || len(candidate) != len(gate.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(gate.token)) == 1
}

// Wrap returns next for the open routes. Other routes refuse a request from an
// ineligible peer with 403 while setup is pending.
func (gate *SetupGate) Wrap(route router.Route, next http.Handler) http.Handler {
	if gate == nil {
		return next
	}
	if setupOpenOperation(route.OperationID) {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if gate.Pending() && !gate.eligible(request) {
			http.Error(w, "First-run setup is open only to the local network or with the setup link in the server log.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, request)
	})
}

func (gate *SetupGate) eligible(request *http.Request) bool {
	if cookie, err := request.Cookie(SetupCookieName); err == nil && gate.ValidToken(cookie.Value) {
		return true
	}
	if relayed(request.Header) {
		return false
	}
	peer := ClientAddressFromContext(request.Context())
	if !peer.IsValid() {
		peer = NormalizeClientAddress(request.RemoteAddr)
	}
	return peer.IsValid() && gate.network != nil && gate.network.IsInLocalNetwork(peer)
}
