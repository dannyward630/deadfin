// SPDX-License-Identifier: GPL-2.0-only

package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
)

type setupStartupStub struct{ completed bool }

func (stub *setupStartupStub) StartupWizardCompleted() bool { return stub.completed }

type privateNetwork struct{}

func (privateNetwork) IsInLocalNetwork(address netip.Addr) bool {
	return address.IsLoopback() || address.IsPrivate()
}

func TestSetupGateConfinesPendingSetup(t *testing.T) {
	const token = "setup-token"
	startup := &setupStartupStub{}
	gate := middleware.NewSetupGate(startup, privateNetwork{}, token)
	reached := func(route router.Route, configure func(*http.Request)) int {
		handler := gate.Wrap(route, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		configure(request)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	wizard := router.Route{OperationID: "UpdateStartupUser", Policy: "FirstTimeSetupOrElevated"}
	login := router.Route{OperationID: "AuthenticateUserByName", Anonymous: true}
	public := router.Route{OperationID: "GetPublicSystemInfo", Anonymous: true}

	for _, test := range []struct {
		name      string
		route     router.Route
		configure func(*http.Request)
		want      int
	}{
		{"public peer is refused on the wizard", wizard, func(r *http.Request) { r.RemoteAddr = "203.0.113.4:1" }, http.StatusForbidden},
		{"public peer is refused on anonymous login", login, func(r *http.Request) { r.RemoteAddr = "203.0.113.4:1" }, http.StatusForbidden},
		{"public peer reaches discovery", public, func(r *http.Request) { r.RemoteAddr = "203.0.113.4:1" }, http.StatusNoContent},
		{"loopback peer reaches the wizard", wizard, func(r *http.Request) { r.RemoteAddr = "127.0.0.1:1" }, http.StatusNoContent},
		{"private peer reaches the wizard", wizard, func(r *http.Request) { r.RemoteAddr = "[fd00::5]:1" }, http.StatusNoContent},
		{"context address wins over RemoteAddr", wizard, func(r *http.Request) {
			r.RemoteAddr = "127.0.0.1:1"
			*r = *r.WithContext(middleware.WithClientAddress(r.Context(), netip.MustParseAddr("198.51.100.7")))
		}, http.StatusForbidden},
		{"X-Forwarded-For marks a relayed peer", wizard, func(r *http.Request) {
			r.RemoteAddr = "10.0.0.2:1"
			r.Header.Set("X-Forwarded-For", "10.0.0.9")
		}, http.StatusForbidden},
		{"Forwarded marks a relayed peer", wizard, func(r *http.Request) {
			r.RemoteAddr = "10.0.0.2:1"
			r.Header.Set("Forwarded", "for=10.0.0.9")
		}, http.StatusForbidden},
		{"X-Real-IP marks a relayed peer", wizard, func(r *http.Request) {
			r.RemoteAddr = "10.0.0.2:1"
			r.Header.Set("X-Real-IP", "10.0.0.9")
		}, http.StatusForbidden},
		{"setup cookie admits a public peer", wizard, func(r *http.Request) {
			r.RemoteAddr = "203.0.113.4:1"
			r.AddCookie(&http.Cookie{Name: middleware.SetupCookieName, Value: token})
		}, http.StatusNoContent},
		{"wrong setup cookie is refused", wizard, func(r *http.Request) {
			r.RemoteAddr = "203.0.113.4:1"
			r.AddCookie(&http.Cookie{Name: middleware.SetupCookieName, Value: "setup-tokeX"})
		}, http.StatusForbidden},
		{"unparseable peer is refused", wizard, func(r *http.Request) { r.RemoteAddr = "pipe" }, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := reached(test.route, test.configure); got != test.want {
				t.Fatalf("status = %d, want %d", got, test.want)
			}
		})
	}

	startup.completed = true
	if got := reached(wizard, func(r *http.Request) { r.RemoteAddr = "203.0.113.4:1" }); got != http.StatusNoContent {
		t.Fatalf("completed setup status = %d, want pass-through", got)
	}
}

func TestSetupGateTokenValidation(t *testing.T) {
	if middleware.NewSetupGate(&setupStartupStub{}, privateNetwork{}, "").ValidToken("") {
		t.Fatal("an empty token must never validate")
	}
	gate := middleware.NewSetupGate(&setupStartupStub{}, privateNetwork{}, "abc")
	if !gate.ValidToken("abc") || gate.ValidToken("abcd") || gate.ValidToken("abd") {
		t.Fatal("ValidToken accepted or rejected the wrong candidates")
	}
	var absent *middleware.SetupGate
	if absent.Pending() || absent.ValidToken("abc") {
		t.Fatal("a nil gate must be inert")
	}
}
