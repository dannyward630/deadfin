// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

// endpointResolver mirrors systemInfoResolver but uses a distinct header
// to avoid coupling the two test families.
type endpointResolver struct{}

func (endpointResolver) Resolve(_ context.Context, request *http.Request) (auth.Identity, error) {
	if request.Header.Get("X-Endpoint-Actor") == "authenticated" {
		return auth.Identity{Authenticated: true, UserID: dto.UUID{0x02}}, nil
	}
	return auth.Identity{}, nil
}

type endpointEvaluator struct{}

func (endpointEvaluator) Evaluate(_ context.Context, request auth.PolicyRequest) (auth.Decision, error) {
	if request.Identity.Authenticated {
		return auth.DecisionAllowed, nil
	}
	return auth.DecisionUnauthenticated, nil
}

type endpointUsers struct{}

func (endpointUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	return store.User{ID: id, Policy: []byte(`{}`)}, nil
}

// lanNetwork reports true for a configured set of LAN addresses and false
// for everything else, so the IsInNetwork matrix can exercise both
// branches without a real network manager.
type lanNetwork struct {
	lan map[string]bool
}

func (n lanNetwork) IsInLocalNetwork(addr netip.Addr) bool {
	return n.lan[addr.String()]
}

func newEndpointRouter(network auth.LocalNetwork) http.Handler {
	authorizer := middleware.NewAuthorizer(endpointResolver{}, endpointEvaluator{}, endpointUsers{})
	rt := router.New(middleware.NoCache("GetBitrateTestBytes", "GetEndpointInfo"), authorizer.Wrap)
	paths, _ := config.NewPaths("/tmp/unused")
	handlers.RegisterSystemWithConfig(rt, config.DefaultServerIdentity(), nil, handlers.SystemInfoConfig{
		Paths:               paths,
		WebSocketPortNumber: 8096,
		Network:             network,
	})
	return rt
}

// endpointRequestWithAddresses sends an authenticated request and injects
// the remote and local addresses into the request context so the endpoint
// handler can derive IsLocal and IsInNetwork from them. The authorizer
// installs the client address from RemoteAddr, so we set RemoteAddr to the
// remote peer's address for the normal path. For the context-injection
// path (when remote is already set), we pre-install it via
// WithClientAddress.
func endpointRequest(handler http.Handler, remoteAddr, localAddr string, authenticated bool, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/System/Endpoint", nil)
	if remoteAddr != "" {
		req = req.WithContext(middleware.WithClientAddress(req.Context(), parseAddr(remoteAddr)))
		req.RemoteAddr = remoteAddr
	} else {
		req.RemoteAddr = remoteAddr
	}
	if localAddr != "" {
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, localAddrFrom(localAddr)))
	}
	if authenticated {
		req.Header.Set("X-Endpoint-Actor", "authenticated")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func localAddrFrom(addr string) net.Addr {
	return &net.TCPAddr{IP: net.ParseIP(addr), Port: 8096}
}

func parseAddr(s string) netip.Addr {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return addr
}

func TestEndpointRoutesRegisterExactMetadata(t *testing.T) {
	t.Parallel()
	rt := router.New()
	paths, _ := config.NewPaths("/tmp/unused")
	handlers.RegisterSystemWithConfig(rt, config.DefaultServerIdentity(), nil, handlers.SystemInfoConfig{
		Paths:               paths,
		WebSocketPortNumber: 8096,
	})
	want := router.Route{
		Method:      http.MethodGet,
		Pattern:     "/System/Endpoint",
		OperationID: "GetEndpointInfo",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}
	got, found := routeByOperation(rt.Routes(), want.OperationID)
	if !found {
		t.Fatalf("operation %q is not registered", want.OperationID)
	}
	if got != want {
		t.Fatalf("Endpoint route = %#v, want %#v", got, want)
	}
}

func TestEndpointLocalityMatrix(t *testing.T) {
	t.Parallel()
	network := lanNetwork{lan: map[string]bool{
		"192.168.1.100": true,
		"127.0.0.1":     true,
	}}
	handler := newEndpointRouter(network)

	for _, tc := range []struct {
		name        string
		remote      string
		local       string
		isLocal     bool
		isInNetwork bool
	}{
		{
			name:    "same address peer is local",
			remote:  "192.168.1.50",
			local:   "192.168.1.50",
			isLocal: true,
			// 192.168.1.50 is not in the configured LAN set; but IsLocal is
			// transport-derived (same IP), so it is true regardless of network.
			isInNetwork: false,
		},
		{
			name:        "distinct LAN peer is not local but in network",
			remote:      "192.168.1.100",
			local:       "192.168.1.1",
			isLocal:     false,
			isInNetwork: true,
		},
		{
			name:        "distinct non-LAN peer is neither local nor in network",
			remote:      "8.8.8.8",
			local:       "192.168.1.1",
			isLocal:     false,
			isInNetwork: false,
		},
		{
			name:    "both addresses unavailable is local",
			remote:  "",
			local:   "",
			isLocal: true,
			// With no remote, isInLocalNetwork defaults to loopback (127.0.0.1)
			// which is in the LAN set, so IsInNetwork is true.
			isInNetwork: true,
		},
		{
			name:    "4-in-6 mapped remote equals unmapped v4 local",
			remote:  "::ffff:192.168.1.50",
			local:   "192.168.1.50",
			isLocal: true,
			// The unmapped remote is 192.168.1.50 which is not in the LAN set.
			isInNetwork: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := endpointRequest(handler, tc.remote, tc.local, true, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
			}
			var info struct {
				IsLocal     bool `json:"IsLocal"`
				IsInNetwork bool `json:"IsInNetwork"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
				t.Fatalf("response is not JSON: %v; body=%q", err, rec.Body.String())
			}
			if info.IsLocal != tc.isLocal {
				t.Errorf("IsLocal = %v, want %v (remote=%q local=%q)", info.IsLocal, tc.isLocal, tc.remote, tc.local)
			}
			if info.IsInNetwork != tc.isInNetwork {
				t.Errorf("IsInNetwork = %v, want %v (remote=%q)", info.IsInNetwork, tc.isInNetwork, tc.remote)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want %q (bare, no charset)", got, "application/json")
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
			}
		})
	}
}

func TestEndpointNilNetworkReturnsFalseWithoutPanic(t *testing.T) {
	t.Parallel()
	handler := newEndpointRouter(nil)
	rec := endpointRequest(handler, "8.8.8.8", "192.168.1.1", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var info struct {
		IsInNetwork bool `json:"IsInNetwork"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("response is not JSON: %v; body=%q", err, rec.Body.String())
	}
	if info.IsInNetwork {
		t.Errorf("IsInNetwork = true, want false for nil Network")
	}
}

func TestEndpointDefaultAcceptProducesPascalCaseBareJSON(t *testing.T) {
	t.Parallel()
	network := lanNetwork{lan: map[string]bool{"192.168.1.100": true}}
	handler := newEndpointRouter(network)
	rec := endpointRequest(handler, "192.168.1.100", "192.168.1.1", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v; body=%q", err, rec.Body.String())
	}
	if _, ok := body["IsLocal"]; !ok {
		t.Errorf("PascalCase response missing IsLocal: %s", rec.Body.String())
	}
	if _, ok := body["IsInNetwork"]; !ok {
		t.Errorf("PascalCase response missing IsInNetwork: %s", rec.Body.String())
	}
	if _, ok := body["isLocal"]; ok {
		t.Errorf("PascalCase response unexpectedly contains camelCase isLocal: %s", rec.Body.String())
	}
	if _, ok := body["isInNetwork"]; ok {
		t.Errorf("PascalCase response unexpectedly contains camelCase isInNetwork: %s", rec.Body.String())
	}
}

func TestEndpointCamelCaseAcceptProducesCamelCaseJSON(t *testing.T) {
	t.Parallel()
	network := lanNetwork{lan: map[string]bool{"192.168.1.100": true}}
	handler := newEndpointRouter(network)
	rec := endpointRequest(handler, "192.168.1.100", "192.168.1.1", true, `application/json; profile="CamelCase"`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != `application/json; profile="CamelCase"` {
		t.Fatalf("Content-Type = %q, want %q", got, `application/json; profile="CamelCase"`)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v; body=%q", err, rec.Body.String())
	}
	if _, ok := body["isLocal"]; !ok {
		t.Errorf("CamelCase response missing isLocal: %s", rec.Body.String())
	}
	if _, ok := body["isInNetwork"]; !ok {
		t.Errorf("CamelCase response missing isInNetwork: %s", rec.Body.String())
	}
	if _, ok := body["IsLocal"]; ok {
		t.Errorf("CamelCase response unexpectedly contains PascalCase IsLocal: %s", rec.Body.String())
	}
	if _, ok := body["IsInNetwork"]; ok {
		t.Errorf("CamelCase response unexpectedly contains PascalCase IsInNetwork: %s", rec.Body.String())
	}
}

func TestEndpointUnauthenticatedReturns401WithNoCacheHeader(t *testing.T) {
	t.Parallel()
	network := lanNetwork{lan: map[string]bool{"192.168.1.100": true}}
	handler := newEndpointRouter(network)
	server := httptest.NewServer(handler)
	defer server.Close()
	client := server.Client()
	resp, err := client.Get(server.URL + "/System/Endpoint")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body length = %d, want 0 (bodiless 401)", len(body))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		t.Errorf("Content-Type = %q, want empty", ct)
	}
	if got := resp.Header.Get("Content-Length"); got != "0" {
		t.Errorf("Content-Length = %q, want %q", got, "0")
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
	}
}

func TestEndpointBareJSONHasNoCharset(t *testing.T) {
	t.Parallel()
	network := lanNetwork{lan: map[string]bool{"192.168.1.100": true}}
	handler := newEndpointRouter(network)
	rec := endpointRequest(handler, "192.168.1.100", "192.168.1.1", true, "")
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want bare %q (no charset)", got, "application/json")
	}
}
