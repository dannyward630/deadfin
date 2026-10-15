// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"net/netip"

	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/auth/quickconnect"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/user"
)

// qcHarness assembles a focused QuickConnect test harness with real user,
// session, and identity stores behind a fake clock. The authorizer carries
// authenticated identities from the X-QC-Actor header so Authorize can select
// the caller or an administrator-specified target.
type qcHarness struct {
	rt          *router.Router
	clock       *clock.Fake
	users       *user.Service
	sessions    *session.Manager
	identities  map[string]auth.Identity
	qcEnabled   *atomic.Bool
	loginPolicy *qcLoginPolicy
	login       *auth.Login
	identity    config.ServerIdentity
	items       *store.ItemStore
	itemBuilder *dtobuild.Builder
	stores      store.Stores
}

func newQCHarness(t *testing.T) *qcHarness {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open identity store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close identity store: %v", err)
		}
	})
	fakeClock := clock.NewFake(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC))
	users := user.New(store.NewIdentity(stores), fakeClock, &p104Entropy{})
	identity := config.ServerIdentity{ServerName: "QC Test", ID: p104UUID(t, "c72d78ad-52ca-4ce0-9073-b6e687e909ce"), Version: "12.0.0-qc"}
	sessions := session.New(fakeClock)
	items := store.NewItemStore(stores)
	itemBuilder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, users, nil), identity.ID.String())
	policy := &qcLoginPolicy{}
	qcEnabled := &atomic.Bool{}
	// QuickConnect defaults enabled.
	qcEnabled.Store(true)
	login := auth.NewLogin(auth.LoginConfig{
		Users:    users,
		Policy:   policy,
		Network:  qcLoginNetwork{},
		Sessions: p104LoginSessions{manager: sessions},
		Server:   identity,
	})
	identities := map[string]auth.Identity{}
	authorizer := middleware.NewAuthorizer(qcResolver{identities: identities}, p104Evaluator{}, p104AuthorizerUsers{users: users})
	rt := router.New(authorizer.Wrap)
	handlers.RegisterQuickConnect(rt, fakeClock, qcEnabled.Load, login, users, sessions, items, itemBuilder, identity)
	return &qcHarness{
		rt:          rt,
		clock:       fakeClock,
		users:       users,
		sessions:    sessions,
		identities:  identities,
		qcEnabled:   qcEnabled,
		loginPolicy: policy,
		login:       login,
		identity:    identity,
		items:       items,
		stores:      stores,
		itemBuilder: itemBuilder,
	}
}

func (h *qcHarness) createUser(t *testing.T, name string, admin bool) store.User {
	t.Helper()
	created, err := h.users.Create(t.Context(), name, "password")
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	policy := json.RawMessage(`{}`)
	if admin {
		policy = json.RawMessage(`{"IsAdministrator":true}`)
	}
	created.Policy = policy
	if err := h.users.Update(t.Context(), created); err != nil {
		t.Fatalf("update %s policy: %v", name, err)
	}
	return created
}

func (h *qcHarness) setIdentity(actor string, identity auth.Identity) {
	h.identities[actor] = identity
}

func qcCall(rt *router.Router, method, path, body, contentType, actor, metadata string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if actor != "" {
		req.Header.Set("X-QC-Actor", actor)
	}
	if metadata != "" {
		req.Header.Set("Authorization", metadata)
	}
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	return rec
}

// qcMetadata is a valid MediaBrowser authorization header for Initiate.
const qcMetadata = `MediaBrowser Client="Swiftfin", Device="iPhone", DeviceId="swiftfin-quickconnect", Version="1.0.0"`

func TestQuickConnectRouteLedgerPinsMethodsPathsOperationsAndPolicies(t *testing.T) {
	h := newQCHarness(t)
	var qcRoutes []router.Route
	for _, route := range h.rt.Routes() {
		if strings.Contains(route.Pattern, "QuickConnect") || strings.Contains(route.Pattern, "AuthenticateWithQuickConnect") {
			qcRoutes = append(qcRoutes, route)
		}
	}
	if len(qcRoutes) != 5 {
		t.Fatalf("QuickConnect routes = %d, want 5", len(qcRoutes))
	}
	want := map[string]router.Route{
		"GET /QuickConnect/Enabled":                {Method: "GET", Pattern: "/QuickConnect/Enabled", OperationID: "GetQuickConnectEnabled", Anonymous: true},
		"POST /QuickConnect/Initiate":              {Method: "POST", Pattern: "/QuickConnect/Initiate", OperationID: "InitiateQuickConnect", Anonymous: true},
		"GET /QuickConnect/Connect":                {Method: "GET", Pattern: "/QuickConnect/Connect", OperationID: "GetQuickConnectState", Anonymous: true},
		"POST /QuickConnect/Authorize":             {Method: "POST", Pattern: "/QuickConnect/Authorize", OperationID: "AuthorizeQuickConnect", Policy: string(auth.PolicyDefaultAuthorization)},
		"POST /Users/AuthenticateWithQuickConnect": {Method: "POST", Pattern: "/Users/AuthenticateWithQuickConnect", OperationID: "AuthenticateWithQuickConnect", Anonymous: true},
	}
	for _, route := range qcRoutes {
		key := route.Method + " " + route.Pattern
		w, ok := want[key]
		if !ok {
			t.Errorf("unexpected route %s", key)
			continue
		}
		if route.OperationID != w.OperationID {
			t.Errorf("%s OperationID = %q, want %q", key, route.OperationID, w.OperationID)
		}
		if route.Anonymous != w.Anonymous {
			t.Errorf("%s Anonymous = %v, want %v", key, route.Anonymous, w.Anonymous)
		}
		if route.Policy != w.Policy {
			t.Errorf("%s Policy = %q, want %q", key, route.Policy, w.Policy)
		}
	}
}

func TestQuickConnectEnabledReturnsBareBoolean(t *testing.T) {
	h := newQCHarness(t)
	rec := qcCall(h.rt, "GET", "/QuickConnect/Enabled", "", "", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("Enabled status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	if body := rec.Body.String(); body != "true" {
		t.Fatalf("Enabled body = %q, want %q", body, "true")
	}

	// Disable.
	h.qcEnabled.Store(false)
	rec2 := qcCall(h.rt, "GET", "/QuickConnect/Enabled", "", "", "", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("disabled Enabled status = %d, want 200", rec2.Code)
	}
	if body := rec2.Body.String(); body != "false" {
		t.Fatalf("disabled Enabled body = %q, want %q", body, "false")
	}
}

func TestQuickConnectInitiateRejectsDisabledWith401(t *testing.T) {
	h := newQCHarness(t)
	h.qcEnabled.Store(false)
	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled Initiate status = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("disabled Initiate Content-Type = %q, want application/json", ct)
	}
	if body := rec.Body.String(); body != `"Quick connect is disabled"` {
		t.Fatalf("disabled Initiate body = %q, want %q", body, `"Quick connect is disabled"`)
	}
}

func TestQuickConnectInitiateRejectsMissingMetadataWith400(t *testing.T) {
	h := newQCHarness(t)
	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing metadata Initiate status = %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("missing metadata Initiate Content-Type = %q, want text/plain", ct)
	}
	if body := rec.Body.String(); body != "Error processing request." {
		t.Fatalf("missing metadata Initiate body = %q, want %q", body, "Error processing request.")
	}
}

func TestQuickConnectInitiateReturnsQuickConnectResult(t *testing.T) {
	h := newQCHarness(t)
	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	if rec.Code != http.StatusOK {
		t.Fatalf("Initiate status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Initiate Content-Type = %q, want application/json", ct)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal Initiate body: %v", err)
	}
	if auth, ok := result["Authenticated"]; !ok || string(auth) != "false" {
		t.Fatalf("Authenticated = %q, want false", result["Authenticated"])
	}
	if secret, ok := result["Secret"]; !ok || len(secret) != 66 {
		t.Fatalf("Secret length = %d (including quotes), want 66", len(secret))
	}
	if code, ok := result["Code"]; !ok || len(code) != 8 {
		t.Fatalf("Code = %q, want 6-digit string", result["Code"])
	}
}

func TestQuickConnectConnectReturnsRequestState(t *testing.T) {
	h := newQCHarness(t)
	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	var initiated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initiated)
	secret := initiated["Secret"].(string)

	rec2 := qcCall(h.rt, "GET", "/QuickConnect/Connect?secret="+secret, "", "", "", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("Connect status = %d, want 200", rec2.Code)
	}
	var polled map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &polled)
	if polled["Authenticated"] != false {
		t.Fatalf("Connect Authenticated = %v, want false", polled["Authenticated"])
	}
	if polled["Secret"] != secret {
		t.Fatalf("Connect Secret = %v, want %q", polled["Secret"], secret)
	}
}

func TestQuickConnectConnectUnknownSecretReturns404(t *testing.T) {
	h := newQCHarness(t)
	rec := qcCall(h.rt, "GET", "/QuickConnect/Connect?secret=unknown", "", "", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown secret Connect status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("unknown secret Connect Content-Type = %q, want text/plain", ct)
	}
	if body := rec.Body.String(); body != "Unknown secret" {
		t.Fatalf("unknown secret Connect body = %q, want %q", body, "Unknown secret")
	}
}

func TestQuickConnectConnectMissingSecretReturns400(t *testing.T) {
	h := newQCHarness(t)
	rec := qcCall(h.rt, "GET", "/QuickConnect/Connect", "", "", "", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing secret Connect status = %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("missing secret Connect Content-Type = %q, want application/json", ct)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("missing secret Connect body unmarshal: %v", err)
	}
	if errs, ok := body["errors"]; !ok || !strings.Contains(string(errs), "The secret field is required.") {
		t.Fatalf("missing secret Connect errors = %s, want validation error for secret", body["errors"])
	}
}

func TestQuickConnectAuthorizeReturnsTrueAndMovesDateAddedForward(t *testing.T) {
	h := newQCHarness(t)
	admin := h.createUser(t, "admin", true)
	h.setIdentity("admin", auth.Identity{Authenticated: true, Administrator: true, UserID: admin.ID, Client: "Swiftfin", Device: "admin-device", DeviceID: "admin-device-id", Version: "1.0.0", Token: "admin-token"})

	// Initiate.
	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	var initiated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initiated)
	secret := initiated["Secret"].(string)
	code := initiated["Code"].(string)
	originalDateAdded := initiated["DateAdded"].(string)

	// Authorize as the admin targeting themselves.
	rec2 := qcCall(h.rt, "POST", "/QuickConnect/Authorize?code="+code+"&userId="+admin.ID.String(), "", "", "admin", qcMetadata)
	if rec2.Code != http.StatusOK {
		t.Fatalf("Authorize status = %d, want 200", rec2.Code)
	}
	if ct := rec2.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("Authorize Content-Type = %q, want text/plain", ct)
	}
	if body := rec2.Body.String(); body != "true" {
		t.Fatalf("Authorize body = %q, want %q", body, "true")
	}

	// Poll after authorize: Authenticated=true, DateAdded moved forward.
	rec3 := qcCall(h.rt, "GET", "/QuickConnect/Connect?secret="+secret, "", "", "", "")
	var polled map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &polled)
	if polled["Authenticated"] != true {
		t.Fatalf("post-authorize Authenticated = %v, want true", polled["Authenticated"])
	}
	postDateAdded := polled["DateAdded"].(string)
	if postDateAdded == originalDateAdded {
		t.Fatal("post-authorize DateAdded did not change; want moved one minute forward")
	}
}

func TestQuickConnectAuthorizeUnknownCodeReturns404(t *testing.T) {
	h := newQCHarness(t)
	admin := h.createUser(t, "admin", true)
	h.setIdentity("admin", auth.Identity{Authenticated: true, Administrator: true, UserID: admin.ID, Client: "Swiftfin", Device: "admin-device", DeviceID: "admin-device-id", Version: "1.0.0", Token: "admin-token"})
	rec := qcCall(h.rt, "POST", "/QuickConnect/Authorize?code=000000&userId="+admin.ID.String(), "", "", "admin", qcMetadata)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown code Authorize status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("unknown code Authorize Content-Type = %q, want text/plain", ct)
	}
	if body := rec.Body.String(); body != "Error processing request." {
		t.Fatalf("unknown code Authorize body = %q, want %q", body, "Error processing request.")
	}
}

func TestQuickConnectAuthorizeRejectsNonAdminCrossUserWith403(t *testing.T) {
	h := newQCHarness(t)
	user1 := h.createUser(t, "user1", false)
	user2 := h.createUser(t, "user2", false)
	h.setIdentity("user1", auth.Identity{Authenticated: true, Administrator: false, UserID: user1.ID, Client: "Swiftfin", Device: "device", DeviceID: "device-id", Version: "1.0.0", Token: "user1-token"})

	// Initiate.
	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	var initiated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initiated)
	code := initiated["Code"].(string)

	// user1 tries to authorize for user2 (non-admin cross-user).
	rec2 := qcCall(h.rt, "POST", "/QuickConnect/Authorize?code="+code+"&userId="+user2.ID.String(), "", "", "user1", qcMetadata)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("non-admin cross-user Authorize status = %d, want 403", rec2.Code)
	}
}

func TestQuickConnectAuthorizeAllowsSelfTargetWithoutUserId(t *testing.T) {
	h := newQCHarness(t)
	admin := h.createUser(t, "admin", true)
	h.setIdentity("admin", auth.Identity{Authenticated: true, Administrator: true, UserID: admin.ID, Client: "Swiftfin", Device: "admin-device", DeviceID: "admin-device-id", Version: "1.0.0", Token: "admin-token"})

	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	var initiated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initiated)
	code := initiated["Code"].(string)

	// Authorize without userId defaults to caller.
	rec2 := qcCall(h.rt, "POST", "/QuickConnect/Authorize?code="+code, "", "", "admin", qcMetadata)
	if rec2.Code != http.StatusOK {
		t.Fatalf("self-target Authorize status = %d, want 200", rec2.Code)
	}
}

func TestQuickConnectAuthorizeAllowsAdminTargetOverride(t *testing.T) {
	h := newQCHarness(t)
	admin := h.createUser(t, "admin", true)
	target := h.createUser(t, "target-user", false)
	h.setIdentity("admin", auth.Identity{Authenticated: true, Administrator: true, UserID: admin.ID, Client: "Swiftfin", Device: "admin-device", DeviceID: "admin-device-id", Version: "1.0.0", Token: "admin-token"})

	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	var initiated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initiated)
	code := initiated["Code"].(string)
	secret := initiated["Secret"].(string)

	// Admin authorizes for a different user.
	rec2 := qcCall(h.rt, "POST", "/QuickConnect/Authorize?code="+code+"&userId="+target.ID.String(), "", "", "admin", qcMetadata)
	if rec2.Code != http.StatusOK {
		t.Fatalf("admin target override Authorize status = %d, want 200", rec2.Code)
	}

	// Redeem the authorized secret.
	rec3 := qcCall(h.rt, "POST", "/Users/AuthenticateWithQuickConnect", `{"Secret":"`+secret+`"}`, "application/json", "", "")
	if rec3.Code != http.StatusOK {
		t.Fatalf("Redeem status = %d, want 200", rec3.Code)
	}
	var result map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &result)
	if result["AccessToken"] == nil || result["AccessToken"] == "" {
		t.Fatal("Redeem AccessToken is empty")
	}
}

func TestQuickConnectRedeemReturnsStoredAuthenticationResult(t *testing.T) {
	h := newQCHarness(t)
	admin := h.createUser(t, "admin", true)
	h.setIdentity("admin", auth.Identity{Authenticated: true, Administrator: true, UserID: admin.ID, Client: "Swiftfin", Device: "admin-device", DeviceID: "admin-device-id", Version: "1.0.0", Token: "admin-token"})

	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	var initiated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initiated)
	code := initiated["Code"].(string)
	secret := initiated["Secret"].(string)

	_ = qcCall(h.rt, "POST", "/QuickConnect/Authorize?code="+code+"&userId="+admin.ID.String(), "", "", "admin", qcMetadata)

	// First redemption: decode must succeed and AccessToken must be a
	// non-empty string. The replay contract is that redemption returns the
	// exact stored authentication result, so the token must be present.
	rec2 := qcCall(h.rt, "POST", "/Users/AuthenticateWithQuickConnect", `{"Secret":"`+secret+`"}`, "application/json", "", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("first Redeem status = %d, want 200", rec2.Code)
	}
	var first map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &first); err != nil {
		t.Fatalf("first Redeem body did not decode as JSON: %v", err)
	}
	firstToken, ok := first["AccessToken"].(string)
	if !ok || firstToken == "" {
		t.Fatalf("first Redeem AccessToken is absent or empty: %v", first["AccessToken"])
	}
	firstSessionInfo, ok := first["SessionInfo"].(map[string]any)
	if !ok {
		t.Fatalf("first Redeem SessionInfo is absent or not an object: %v", first["SessionInfo"])
	}
	firstSessionID, ok := firstSessionInfo["Id"].(string)
	if !ok || firstSessionID == "" {
		t.Fatalf("first Redeem SessionInfo.Id is absent or empty: %v", firstSessionInfo["Id"])
	}

	// Second redemption must return the exact same AccessToken and
	// SessionInfo.Id — the replay is byte-identical, not merely present.
	rec3 := qcCall(h.rt, "POST", "/Users/AuthenticateWithQuickConnect", `{"Secret":"`+secret+`"}`, "application/json", "", "")
	if rec3.Code != http.StatusOK {
		t.Fatalf("second Redeem status = %d, want 200", rec3.Code)
	}
	var second map[string]any
	if err := json.Unmarshal(rec3.Body.Bytes(), &second); err != nil {
		t.Fatalf("second Redeem body did not decode as JSON: %v", err)
	}
	secondToken, ok := second["AccessToken"].(string)
	if !ok || secondToken == "" {
		t.Fatalf("second Redeem AccessToken is absent or empty: %v", second["AccessToken"])
	}
	secondSessionInfo, ok := second["SessionInfo"].(map[string]any)
	if !ok {
		t.Fatalf("second Redeem SessionInfo is absent or not an object: %v", second["SessionInfo"])
	}
	secondSessionID, ok := secondSessionInfo["Id"].(string)
	if !ok || secondSessionID == "" {
		t.Fatalf("second Redeem SessionInfo.Id is absent or empty: %v", secondSessionInfo["Id"])
	}
	if secondToken != firstToken {
		t.Fatalf("second Redeem AccessToken = %q, want %q (byte-identical replay)", secondToken, firstToken)
	}
	if secondSessionID != firstSessionID {
		t.Fatalf("second Redeem SessionInfo.Id = %q, want %q (byte-identical replay)", secondSessionID, firstSessionID)
	}
}

func TestQuickConnectRedeemUnknownSecretReturns404(t *testing.T) {
	h := newQCHarness(t)
	rec := qcCall(h.rt, "POST", "/Users/AuthenticateWithQuickConnect", `{"Secret":"unknown"}`, "application/json", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown Redeem status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("unknown Redeem Content-Type = %q, want application/json", ct)
	}
}

func TestQuickConnectDisabledRoutesRejectWithoutPurgingState(t *testing.T) {
	h := newQCHarness(t)
	// Initiate while enabled.
	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	var initiated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initiated)
	secret := initiated["Secret"].(string)

	// Disable.
	h.qcEnabled.Store(false)

	// Enabled returns false.
	recEnabled := qcCall(h.rt, "GET", "/QuickConnect/Enabled", "", "", "", "")
	if recEnabled.Body.String() != "false" {
		t.Fatalf("disabled Enabled = %q, want false", recEnabled.Body.String())
	}

	// Initiate rejects.
	if rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled Initiate status = %d, want 401", rec.Code)
	}

	// Connect rejects.
	if rec := qcCall(h.rt, "GET", "/QuickConnect/Connect?secret="+secret, "", "", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled Connect status = %d, want 401", rec.Code)
	}

	// Redeem rejects.
	if rec := qcCall(h.rt, "POST", "/Users/AuthenticateWithQuickConnect", `{"Secret":"`+secret+`"}`, "application/json", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled Redeem status = %d, want 401", rec.Code)
	}

	// Re-enable: state was not purged, the request is still there.
	h.qcEnabled.Store(true)
	recConnect := qcCall(h.rt, "GET", "/QuickConnect/Connect?secret="+secret, "", "", "", "")
	if recConnect.Code != http.StatusOK {
		t.Fatalf("re-enabled Connect status = %d, want 200 (state not purged)", recConnect.Code)
	}
}

func TestQuickConnectAuthorizedSecretExpiresAfter10Minutes(t *testing.T) {
	h := newQCHarness(t)
	admin := h.createUser(t, "admin", true)
	h.setIdentity("admin", auth.Identity{Authenticated: true, Administrator: true, UserID: admin.ID, Client: "Swiftfin", Device: "admin-device", DeviceID: "admin-device-id", Version: "1.0.0", Token: "admin-token"})

	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	var initiated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initiated)
	code := initiated["Code"].(string)
	secret := initiated["Secret"].(string)

	_ = qcCall(h.rt, "POST", "/QuickConnect/Authorize?code="+code+"&userId="+admin.ID.String(), "", "", "admin", qcMetadata)

	// At +10 minutes, still valid.
	h.clock.Advance(10 * time.Minute)
	rec2 := qcCall(h.rt, "POST", "/Users/AuthenticateWithQuickConnect", `{"Secret":"`+secret+`"}`, "application/json", "", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("Redeem at +10min status = %d, want 200", rec2.Code)
	}

	// One millisecond past: 404.
	h.clock.Advance(1 * time.Millisecond)
	rec3 := qcCall(h.rt, "POST", "/Users/AuthenticateWithQuickConnect", `{"Secret":"`+secret+`"}`, "application/json", "", "")
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("Redeem after +10min status = %d, want 404", rec3.Code)
	}
}

// TestQuickConnectAuthorizeUsesInitiatingDevice verifies that a valid
// QuickConnect authorization authenticates the device that initiated the
// request, not the device performing the authorization
// (QuickConnectManager.cs:154-161). The initiating device metadata differs
// deliberately from the authorizer's; the redeemed SessionInfo must show the
// initiator's device, client, and version.
func TestQuickConnectAuthorizeUsesInitiatingDevice(t *testing.T) {
	h := newQCHarness(t)
	admin := h.createUser(t, "admin", true)
	// The authorizer uses deliberately different metadata from the initiator.
	h.setIdentity("admin", auth.Identity{Authenticated: true, Administrator: true, UserID: admin.ID, Client: "Jellyfin Web", Device: "Chrome", DeviceID: "admin-browser-id", Version: "10.8.0", Token: "admin-token"})

	// Initiate with Swiftfin/iPhone metadata.
	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	var initiated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initiated)
	code := initiated["Code"].(string)
	secret := initiated["Secret"].(string)

	// Authorize as the admin (different device metadata).
	rec2 := qcCall(h.rt, "POST", "/QuickConnect/Authorize?code="+code+"&userId="+admin.ID.String(), "", "", "admin", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("Authorize status = %d, want 200", rec2.Code)
	}

	// Redeem the authorized secret.
	rec3 := qcCall(h.rt, "POST", "/Users/AuthenticateWithQuickConnect", `{"Secret":"`+secret+`"}`, "application/json", "", "")
	if rec3.Code != http.StatusOK {
		t.Fatalf("Redeem status = %d, want 200", rec3.Code)
	}
	var result map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &result)

	// The redeemed session must reflect the initiating device, not the
	// authorizer's device.
	si, ok := result["SessionInfo"].(map[string]any)
	if !ok {
		t.Fatal("SessionInfo is not an object")
	}
	if si["DeviceId"] != "swiftfin-quickconnect" {
		t.Fatalf("SessionInfo.DeviceId = %v, want swiftfin-quickconnect (initiating device)", si["DeviceId"])
	}
	if si["DeviceName"] != "iPhone" {
		t.Fatalf("SessionInfo.DeviceName = %v, want iPhone (initiating device)", si["DeviceName"])
	}
	if si["Client"] != "Swiftfin" {
		t.Fatalf("SessionInfo.Client = %v, want Swiftfin (initiating client)", si["Client"])
	}
	if si["ApplicationVersion"] != "1.0.0" {
		t.Fatalf("SessionInfo.ApplicationVersion = %v, want 1.0.0 (initiating version)", si["ApplicationVersion"])
	}
}

// TestQuickConnectAuthorizeInvalidCodeHasNoAuthSideEffects verifies that an
// unknown or already-authorized code does not issue a token or log session
// activity before the handler returns an error. The code and authorization
// state are checked before direct authentication runs
// (QuickConnectManager.cs:141-149).
func TestQuickConnectAuthorizeInvalidCodeHasNoAuthSideEffects(t *testing.T) {
	h := newQCHarness(t)
	admin := h.createUser(t, "admin", true)
	h.setIdentity("admin", auth.Identity{Authenticated: true, Administrator: true, UserID: admin.ID, Client: "Swiftfin", Device: "admin-device", DeviceID: "admin-device-id", Version: "1.0.0", Token: "admin-token"})

	// Initiate a valid request.
	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	var initiated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initiated)
	code := initiated["Code"].(string)

	sessionsBefore := len(h.sessions.Sessions(session.Query{}))

	// Unknown code: 404, no session created.
	recUnknown := qcCall(h.rt, "POST", "/QuickConnect/Authorize?code=000000&userId="+admin.ID.String(), "", "", "admin", "")
	if recUnknown.Code != http.StatusNotFound {
		t.Fatalf("unknown code Authorize status = %d, want 404", recUnknown.Code)
	}
	if got := len(h.sessions.Sessions(session.Query{})); got != sessionsBefore {
		t.Fatalf("sessions after unknown code = %d, want %d (no auth side effects)", got, sessionsBefore)
	}

	// Authorize the valid code once.
	recValid := qcCall(h.rt, "POST", "/QuickConnect/Authorize?code="+code+"&userId="+admin.ID.String(), "", "", "admin", "")
	if recValid.Code != http.StatusOK {
		t.Fatalf("valid Authorize status = %d, want 200", recValid.Code)
	}
	sessionsAfterAuthorize := len(h.sessions.Sessions(session.Query{}))
	if sessionsAfterAuthorize != sessionsBefore+1 {
		t.Fatalf("sessions after valid authorize = %d, want %d", sessionsAfterAuthorize, sessionsBefore+1)
	}

	// Already-authorized code: error, no additional session created.
	recAgain := qcCall(h.rt, "POST", "/QuickConnect/Authorize?code="+code+"&userId="+admin.ID.String(), "", "", "admin", "")
	if recAgain.Code == http.StatusOK {
		t.Fatal("already-authorized code returned 200, want error")
	}
	if got := len(h.sessions.Sessions(session.Query{})); got != sessionsAfterAuthorize {
		t.Fatalf("sessions after already-authorized = %d, want %d (no auth side effects)", got, sessionsAfterAuthorize)
	}
}

// TestQuickConnectAuthorizeNilUserIDDefaultsToCaller verifies that an
// explicit all-zero UUID in the userId parameter defaults to the caller,
// matching RequestHelpers.GetUserId and resolveSelectedUser
// (item_user.go:67-70).
func TestQuickConnectAuthorizeNilUserIDDefaultsToCaller(t *testing.T) {
	h := newQCHarness(t)
	admin := h.createUser(t, "admin", true)
	h.setIdentity("admin", auth.Identity{Authenticated: true, Administrator: true, UserID: admin.ID, Client: "Swiftfin", Device: "admin-device", DeviceID: "admin-device-id", Version: "1.0.0", Token: "admin-token"})

	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	var initiated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initiated)
	code := initiated["Code"].(string)
	secret := initiated["Secret"].(string)

	// Authorize with an explicit all-zero UUID — must default to the caller.
	rec2 := qcCall(h.rt, "POST", "/QuickConnect/Authorize?code="+code+"&userId="+dto.NilUUID.String(), "", "", "admin", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("nil UUID Authorize status = %d, want 200 (defaults to caller)", rec2.Code)
	}

	// Redeem and verify the access token is for the admin (the caller).
	rec3 := qcCall(h.rt, "POST", "/Users/AuthenticateWithQuickConnect", `{"Secret":"`+secret+`"}`, "application/json", "", "")
	if rec3.Code != http.StatusOK {
		t.Fatalf("Redeem status = %d, want 200", rec3.Code)
	}
	var result map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &result)
	userObj, ok := result["User"].(map[string]any)
	if !ok {
		t.Fatal("User is not an object")
	}
	if userObj["Id"] != admin.ID.String() {
		t.Fatalf("Redeem User.Id = %v, want %v (nil UUID defaults to caller)", userObj["Id"], admin.ID.String())
	}
}

// TestQuickConnectAuthorizeActorLookupError verifies that a persisted-actor
// lookup failure returns 500 without issuing a token or authorizing the
// request, matching resolveSelectedUser (item_user.go:74-77).
func TestQuickConnectAuthorizeActorLookupError(t *testing.T) {
	h := newQCHarness(t)
	user1 := h.createUser(t, "user1", false)
	user2 := h.createUser(t, "user2", false)
	h.setIdentity("user1", auth.Identity{Authenticated: true, Administrator: false, UserID: user1.ID, Client: "Swiftfin", Device: "device", DeviceID: "device-id", Version: "1.0.0", Token: "user1-token"})

	rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
	var initiated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &initiated)
	code := initiated["Code"].(string)

	sessionsBefore := len(h.sessions.Sessions(session.Query{}))

	// Close the store so the actor lookup fails with a non-NotFound error.
	_ = h.stores.Close()

	// user1 (non-admin) tries to authorize for user2 (cross-user). The
	// actor lookup fails because the store is closed; must return 500.
	rec2 := qcCall(h.rt, "POST", "/QuickConnect/Authorize?code="+code+"&userId="+user2.ID.String(), "", "", "user1", "")
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("actor lookup error Authorize status = %d, want 500", rec2.Code)
	}
	if got := len(h.sessions.Sessions(session.Query{})); got != sessionsBefore {
		t.Fatalf("sessions after actor lookup error = %d, want %d (no token issued)", got, sessionsBefore)
	}
}

// TestQuickConnectRedemptionRequiresSecret verifies that {}, empty, and absent
// redemption secrets are rejected as required input (400 JSON validation)
// rather than looked up as unknown secrets (404)
// (UserController.cs:248-259, QuickConnectDto.cs [Required]).
func TestQuickConnectRedemptionRequiresSecret(t *testing.T) {
	h := newQCHarness(t)

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty object", body: `{}`},
		{name: "empty secret", body: `{"Secret":""}`},
		{name: "absent body", body: ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := qcCall(h.rt, "POST", "/Users/AuthenticateWithQuickConnect", tc.body, "application/json", "", "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("Redeem %s status = %d, want 400", tc.name, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("Redeem %s Content-Type = %q, want application/json", tc.name, ct)
			}
			var response map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("Redeem %s body unmarshal: %v", tc.name, err)
			}
			if errs, ok := response["errors"]; !ok || !strings.Contains(string(errs), "The Secret field is required.") {
				t.Fatalf("Redeem %s errors = %s, want validation error for Secret", tc.name, response["errors"])
			}
		})
	}
}

// qcResolver resolves the identity from the X-QC-Actor header.
type qcResolver struct{ identities map[string]auth.Identity }

func (r qcResolver) Resolve(_ context.Context, request *http.Request) (auth.Identity, error) {
	return r.identities[request.Header.Get("X-QC-Actor")], nil
}

// qcLoginPolicy is a permissive login policy for the direct-auth seam.
type qcLoginPolicy struct{ err error }

func (p *qcLoginPolicy) Authorize(context.Context, store.User, netip.Addr, bool) error { return p.err }
func (p *qcLoginPolicy) AuthorizeDevice(context.Context, store.User, string) error     { return p.err }

// qcLoginNetwork admits all peers.
type qcLoginNetwork struct{}

func (qcLoginNetwork) IsLocal(netip.Addr) bool { return true }

// Ensure dto is referenced for the identity field type.
var _ = dto.NilUUID

// TestQuickConnectInitiateBoundsAnonymousCallers covers D-55: one source
// address cannot hold more than MaxPendingPerSource requests, and overlong
// metadata answers like missing metadata.
func TestQuickConnectInitiateBoundsAnonymousCallers(t *testing.T) {
	h := newQCHarness(t)
	overlong := `MediaBrowser Client="Swiftfin", Device="` + strings.Repeat("x", quickconnect.MaxMetadataLength+1) + `", DeviceId="swiftfin-quickconnect", Version="1.0.0"`
	if rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", overlong); rec.Code != http.StatusBadRequest {
		t.Fatalf("overlong metadata Initiate status = %d, want 400", rec.Code)
	}
	for i := 0; i < quickconnect.MaxPendingPerSource; i++ {
		if rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata); rec.Code != http.StatusOK {
			t.Fatalf("Initiate %d status = %d, want 200", i, rec.Code)
		}
	}
	// A random code can collide with a stored one and replace it, so allow a
	// few extra attempts before the limit must hold.
	for attempt := 0; ; attempt++ {
		rec := qcCall(h.rt, "POST", "/QuickConnect/Initiate", "", "", "", qcMetadata)
		if rec.Code == http.StatusTooManyRequests {
			if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
				t.Fatalf("limited Initiate Content-Type = %q, want text/plain", ct)
			}
			return
		}
		if attempt == 3 {
			t.Fatalf("Initiate past the per-source limit status = %d, want 429", rec.Code)
		}
	}
}
