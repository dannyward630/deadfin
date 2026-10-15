// SPDX-License-Identifier: GPL-2.0-only

package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

const matrixAuthorizerCaseCount = 144

var matrixAuthorizerUserID = dto.UUID{0xd9, 0x8e, 0x3c, 0x2f, 0x8f, 0x31, 0x4c, 0x4a, 0xa0, 0x95, 0x8c, 0x04, 0x5e, 0xd1, 0x2b, 0x6f}

// This inventory is the 17 upstream-registered policies plus the evaluator's
// explicit DefaultAuthorization route policy. LocalAccessOnly is intentionally
// excluded: upstream declares it but does not register a handler for it.
var matrixEffectivePolicies = []matrixPolicySpec{
	{policy: "RequiresElevation", ordinaryAllow: `{"EnableRemoteAccess":true}`, ordinaryDeny: `{"EnableRemoteAccess":true}`, ordinaryCannotAllow: true},
	{policy: "FirstTimeSetupOrElevated", ordinaryAllow: `{"EnableRemoteAccess":true}`, ordinaryDeny: `{"EnableRemoteAccess":true}`, ordinaryCannotAllow: true},
	{policy: "IgnoreParentalControl", ordinaryAllow: `{"EnableRemoteAccess":true}`, ordinaryDeny: `{"EnableRemoteAccess":false}`},
	{policy: "Download", ordinaryAllow: `{"EnableRemoteAccess":true,"EnableContentDownloading":true}`, ordinaryDeny: `{"EnableRemoteAccess":true,"EnableContentDownloading":false}`},
	{policy: "FirstTimeSetupOrDefault", ordinaryAllow: `{"EnableRemoteAccess":true}`, ordinaryDeny: `{"EnableRemoteAccess":false}`},
	{policy: "LocalAccessOrRequiresElevation", ordinaryAllow: `{"EnableRemoteAccess":true}`, ordinaryDeny: `{"EnableRemoteAccess":true}`, localAllow: true},
	{policy: "AnonymousLanAccessPolicy", ordinaryAllow: `{"EnableRemoteAccess":true}`, ordinaryDeny: `{"EnableRemoteAccess":true}`, localAllow: true},
	{policy: "FirstTimeSetupOrIgnoreParentalControl", ordinaryAllow: `{"EnableRemoteAccess":true}`, ordinaryDeny: `{"EnableRemoteAccess":false}`},
	{policy: "SyncPlayHasAccess", ordinaryAllow: `{"EnableRemoteAccess":true,"SyncPlayAccess":"JoinGroups"}`, ordinaryDeny: `{"EnableRemoteAccess":true,"SyncPlayAccess":"None"}`},
	{policy: "SyncPlayCreateGroup", ordinaryAllow: `{"EnableRemoteAccess":true,"SyncPlayAccess":"CreateAndJoinGroups"}`, ordinaryDeny: `{"EnableRemoteAccess":true,"SyncPlayAccess":"JoinGroups"}`},
	{policy: "SyncPlayJoinGroup", ordinaryAllow: `{"EnableRemoteAccess":true,"SyncPlayAccess":"JoinGroups"}`, ordinaryDeny: `{"EnableRemoteAccess":true,"SyncPlayAccess":"None"}`},
	{policy: "SyncPlayIsInGroup", ordinaryAllow: `{"EnableRemoteAccess":true,"SyncPlayAccess":"None"}`, ordinaryDeny: `{"EnableRemoteAccess":true,"SyncPlayAccess":"CreateAndJoinGroups"}`, activeAllow: true},
	{policy: "CollectionManagement", ordinaryAllow: `{"EnableRemoteAccess":true,"EnableCollectionManagement":true}`, ordinaryDeny: `{"EnableRemoteAccess":true,"EnableCollectionManagement":false}`},
	{policy: "LiveTvAccess", ordinaryAllow: `{"EnableRemoteAccess":true,"EnableLiveTvAccess":true}`, ordinaryDeny: `{"EnableRemoteAccess":true,"EnableLiveTvAccess":false}`},
	{policy: "LiveTvManagement", ordinaryAllow: `{"EnableRemoteAccess":true,"EnableLiveTvManagement":true}`, ordinaryDeny: `{"EnableRemoteAccess":true,"EnableLiveTvManagement":false}`},
	{policy: "SubtitleManagement", ordinaryAllow: `{"EnableRemoteAccess":true,"EnableSubtitleManagement":true}`, ordinaryDeny: `{"EnableRemoteAccess":true,"EnableSubtitleManagement":false}`},
	{policy: "LyricManagement", ordinaryAllow: `{"EnableRemoteAccess":true,"EnableLyricManagement":true}`, ordinaryDeny: `{"EnableRemoteAccess":true,"EnableLyricManagement":false}`},
	{policy: "DefaultAuthorization", ordinaryAllow: `{"EnableRemoteAccess":true}`, ordinaryDeny: `{"EnableRemoteAccess":false}`},
}

var matrixLockedPolicyNames = []string{
	"RequiresElevation",
	"FirstTimeSetupOrElevated",
	"IgnoreParentalControl",
	"Download",
	"FirstTimeSetupOrDefault",
	"LocalAccessOrRequiresElevation",
	"AnonymousLanAccessPolicy",
	"FirstTimeSetupOrIgnoreParentalControl",
	"SyncPlayHasAccess",
	"SyncPlayCreateGroup",
	"SyncPlayJoinGroup",
	"SyncPlayIsInGroup",
	"CollectionManagement",
	"LiveTvAccess",
	"LiveTvManagement",
	"SubtitleManagement",
	"LyricManagement",
	"DefaultAuthorization",
}

var matrixLockedExtraCaseNames = []string{
	"FirstTimeSetupOrElevated/incomplete-wizard-missing",
	"FirstTimeSetupOrDefault/incomplete-wizard-missing",
	"FirstTimeSetupOrIgnoreParentalControl/incomplete-wizard-missing",
	"FirstTimeSetupOrElevated/incomplete-wizard-ordinary",
	"DefaultAuthorization/allowed-schedule",
	"DefaultAuthorization/blocked-schedule",
	"SyncPlayHasAccess/active-group-overrides-none-role",
	"LocalAccessOnly/unregistered-policy-is-configuration-error",
}

type matrixPolicySpec struct {
	policy              string
	ordinaryAllow       string
	ordinaryDeny        string
	ordinaryCannotAllow bool
	localAllow          bool
	activeAllow         bool
}

type matrixCredential uint8

const (
	matrixMissingCredential matrixCredential = iota
	matrixInvalidCredential
	matrixOrdinaryCredential
	matrixAdminCredential
	matrixDisabledCredential
	matrixAPIKeyCredential
)

type matrixCarrier uint8

const (
	matrixAuthorizationMediaBrowser matrixCarrier = iota
	matrixXEmbyAuthorization
	matrixXEmbyToken
	matrixXMediaBrowserToken
	matrixAPIKey
	matrixLegacyAPIKey
)

type matrixAuthorizerCase struct {
	name            string
	policy          string
	credential      matrixCredential
	carrier         matrixCarrier
	legacy          bool
	policyJSON      string
	completed       bool
	local           bool
	active          bool
	blockedSchedule bool
	wantStatus      int
}

func TestAuthorizerHTTPPolicyMatrix(t *testing.T) {
	cases := matrixAuthorizerCases()
	matrixAssertLockedCases(t, cases)

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			matrixRunAuthorizerCase(t, test)
		})
	}
}

func matrixAuthorizerCases() []matrixAuthorizerCase {
	cases := make([]matrixAuthorizerCase, 0, matrixAuthorizerCaseCount)
	for _, policy := range matrixEffectivePolicies {
		local := policy.policy == "AnonymousLanAccessPolicy"
		cases = append(cases,
			matrixAuthorizerCase{name: policy.policy + "/missing", policy: policy.policy, credential: matrixMissingCredential, local: false, completed: true, wantStatus: http.StatusUnauthorized},
			matrixAuthorizerCase{name: policy.policy + "/invalid", policy: policy.policy, credential: matrixInvalidCredential, local: false, completed: true, wantStatus: http.StatusUnauthorized},
			matrixAuthorizerCase{name: policy.policy + "/disabled-device", policy: policy.policy, credential: matrixDisabledCredential, policyJSON: `{"IsDisabled":true,"EnableRemoteAccess":true}`, local: local, completed: true, wantStatus: http.StatusUnauthorized},
			matrixAuthorizerCase{name: policy.policy + "/ordinary-denied", policy: policy.policy, credential: matrixOrdinaryCredential, policyJSON: policy.ordinaryDeny, local: false, completed: true, wantStatus: http.StatusForbidden},
			matrixAuthorizerCase{name: policy.policy + "/administrator-allowed", policy: policy.policy, credential: matrixAdminCredential, policyJSON: `{"IsAdministrator":true,"EnableRemoteAccess":true,"SyncPlayAccess":"None"}`, local: local, completed: true, wantStatus: http.StatusNoContent},
			matrixAuthorizerCase{name: policy.policy + "/api-key", policy: policy.policy, credential: matrixAPIKeyCredential, local: local, completed: true, wantStatus: matrixAPIKeyStatus(policy.policy)},
		)
		if !policy.ordinaryCannotAllow {
			cases = append(cases, matrixAuthorizerCase{name: policy.policy + "/ordinary-allowed", policy: policy.policy, credential: matrixOrdinaryCredential, policyJSON: policy.ordinaryAllow, local: policy.localAllow, active: policy.activeAllow, completed: true, wantStatus: http.StatusNoContent})
		}
	}

	for _, legacy := range []bool{false, true} {
		for _, carrier := range []matrixCarrier{
			matrixAuthorizationMediaBrowser,
			matrixXEmbyAuthorization,
			matrixXEmbyToken,
			matrixXMediaBrowserToken,
			matrixAPIKey,
			matrixLegacyAPIKey,
		} {
			cases = append(cases, matrixAuthorizerCase{
				name:       matrixCarrierName(carrier) + "/legacy-" + matrixLegacyName(legacy),
				policy:     "DefaultAuthorization",
				credential: matrixOrdinaryCredential,
				carrier:    carrier,
				legacy:     legacy,
				policyJSON: `{"EnableRemoteAccess":true}`,
				completed:  true,
				wantStatus: matrixCarrierStatus(carrier, legacy),
			})
		}
	}

	cases = append(cases,
		matrixAuthorizerCase{name: "FirstTimeSetupOrElevated/incomplete-wizard-missing", policy: "FirstTimeSetupOrElevated", credential: matrixMissingCredential, completed: false, wantStatus: http.StatusNoContent},
		matrixAuthorizerCase{name: "FirstTimeSetupOrDefault/incomplete-wizard-missing", policy: "FirstTimeSetupOrDefault", credential: matrixMissingCredential, completed: false, wantStatus: http.StatusNoContent},
		matrixAuthorizerCase{name: "FirstTimeSetupOrIgnoreParentalControl/incomplete-wizard-missing", policy: "FirstTimeSetupOrIgnoreParentalControl", credential: matrixMissingCredential, completed: false, wantStatus: http.StatusNoContent},
		matrixAuthorizerCase{name: "FirstTimeSetupOrElevated/incomplete-wizard-ordinary", policy: "FirstTimeSetupOrElevated", credential: matrixOrdinaryCredential, policyJSON: `{"EnableRemoteAccess":true}`, completed: false, wantStatus: http.StatusNoContent},
		matrixAuthorizerCase{name: "DefaultAuthorization/allowed-schedule", policy: "DefaultAuthorization", credential: matrixOrdinaryCredential, policyJSON: `{"EnableRemoteAccess":true}`, completed: true, wantStatus: http.StatusNoContent},
		matrixAuthorizerCase{name: "DefaultAuthorization/blocked-schedule", policy: "DefaultAuthorization", credential: matrixOrdinaryCredential, policyJSON: `{"EnableRemoteAccess":true}`, completed: true, blockedSchedule: true, wantStatus: http.StatusForbidden},
		matrixAuthorizerCase{name: "SyncPlayHasAccess/active-group-overrides-none-role", policy: "SyncPlayHasAccess", credential: matrixOrdinaryCredential, policyJSON: `{"EnableRemoteAccess":true,"SyncPlayAccess":"None"}`, completed: true, active: true, wantStatus: http.StatusNoContent},
		matrixAuthorizerCase{name: "LocalAccessOnly/unregistered-policy-is-configuration-error", policy: "LocalAccessOnly", credential: matrixOrdinaryCredential, policyJSON: `{"EnableRemoteAccess":true}`, completed: true, wantStatus: http.StatusInternalServerError},
	)
	return cases
}

func matrixAPIKeyStatus(policy string) int {
	switch policy {
	case "SyncPlayHasAccess", "SyncPlayCreateGroup", "SyncPlayJoinGroup", "SyncPlayIsInGroup":
		return http.StatusForbidden
	default:
		return http.StatusNoContent
	}
}

func matrixCarrierStatus(carrier matrixCarrier, legacy bool) int {
	if legacy || carrier == matrixAuthorizationMediaBrowser || carrier == matrixAPIKey {
		return http.StatusNoContent
	}
	return http.StatusUnauthorized
}

func matrixCarrierName(carrier matrixCarrier) string {
	switch carrier {
	case matrixAuthorizationMediaBrowser:
		return "Authorization-MediaBrowser"
	case matrixXEmbyAuthorization:
		return "X-Emby-Authorization"
	case matrixXEmbyToken:
		return "X-Emby-Token"
	case matrixXMediaBrowserToken:
		return "X-MediaBrowser-Token"
	case matrixAPIKey:
		return "ApiKey"
	case matrixLegacyAPIKey:
		return "api_key"
	default:
		return "unknown-carrier"
	}
}

func matrixLegacyName(legacy bool) string {
	if legacy {
		return "enabled"
	}
	return "disabled"
}

func matrixAssertLockedCases(t *testing.T, cases []matrixAuthorizerCase) {
	t.Helper()
	if len(matrixEffectivePolicies) != len(matrixLockedPolicyNames) {
		t.Fatalf("effective policy inventory length = %d, want %d", len(matrixEffectivePolicies), len(matrixLockedPolicyNames))
	}
	for index, want := range matrixLockedPolicyNames {
		if got := matrixEffectivePolicies[index].policy; got != want {
			t.Fatalf("effective policy[%d] = %q, want %q", index, got, want)
		}
	}
	if len(cases) != matrixAuthorizerCaseCount {
		t.Fatalf("matrix case count = %d, want %d", len(cases), matrixAuthorizerCaseCount)
	}

	seen := make(map[string]bool, len(cases))
	for _, test := range cases {
		if seen[test.name] {
			t.Fatalf("matrix case name %q is duplicated", test.name)
		}
		seen[test.name] = true
	}
	for index, policy := range matrixLockedPolicyNames {
		for _, suffix := range []string{"/missing", "/invalid", "/disabled-device", "/ordinary-denied", "/administrator-allowed", "/api-key"} {
			if !seen[policy+suffix] {
				t.Fatalf("required matrix case %q is absent", policy+suffix)
			}
		}
		if !matrixEffectivePolicies[index].ordinaryCannotAllow && !seen[policy+"/ordinary-allowed"] {
			t.Fatalf("required matrix case %q is absent", policy+"/ordinary-allowed")
		}
	}
	for _, legacy := range []bool{false, true} {
		for _, carrier := range []matrixCarrier{matrixAuthorizationMediaBrowser, matrixXEmbyAuthorization, matrixXEmbyToken, matrixXMediaBrowserToken, matrixAPIKey, matrixLegacyAPIKey} {
			name := matrixCarrierName(carrier) + "/legacy-" + matrixLegacyName(legacy)
			if !seen[name] {
				t.Fatalf("required carrier matrix case %q is absent", name)
			}
		}
	}
	for _, name := range matrixLockedExtraCaseNames {
		if !seen[name] {
			t.Fatalf("required matrix case %q is absent", name)
		}
	}
}

func matrixRunAuthorizerCase(t *testing.T, test matrixAuthorizerCase) {
	t.Helper()
	devices := &matrixDeviceTokens{devices: map[string]store.Device{
		"ordinary-device-token": {UserID: matrixAuthorizerUserID, AccessToken: "ordinary-device-token"},
		"admin-device-token":    {UserID: matrixAuthorizerUserID, AccessToken: "admin-device-token"},
		"disabled-device-token": {UserID: matrixAuthorizerUserID, AccessToken: "disabled-device-token"},
	}}
	keys := &matrixAPIKeys{keys: map[string]store.APIKey{
		"matrix-api-key": {AccessToken: "matrix-api-key", Name: "matrix administrator"},
	}}
	resolver := auth.NewResolver(auth.ResolverConfig{Devices: devices, APIKeys: keys, EnableLegacyAuthorization: test.legacy})
	localAddress := netip.MustParseAddr("192.0.2.80")
	evaluator := auth.NewEvaluator(
		matrixStartup{completed: test.completed},
		matrixNetwork{local: map[netip.Addr]bool{localAddress: true}},
		matrixSchedules{blocked: test.blockedSchedule},
		func() time.Time { return time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC) },
	)
	user := store.User{ID: matrixAuthorizerUserID, Policy: []byte(test.policyJSON)}
	authorizer := middleware.NewAuthorizer(resolver, evaluator, matrixUsers{user: user})

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		handlerCalled = true
		identity, found := middleware.IdentityFromContext(request.Context())
		if !found || (test.credential != matrixMissingCredential && !identity.Authenticated) {
			t.Errorf("handler identity = (%+v, %t), want request identity matching the credential", identity, found)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/matrix", nil)
	request.RemoteAddr = "198.51.100.80:8096"
	if test.local {
		request.RemoteAddr = "192.0.2.80:8096"
	}
	if test.active {
		request = request.WithContext(middleware.WithSyncPlayActive(request.Context(), true))
	}
	matrixApplyCredential(request, test.carrier, test.credential)

	recorder := httptest.NewRecorder()
	authorizer.Wrap(router.Route{Policy: test.policy}, handler).ServeHTTP(recorder, request)

	if recorder.Code != test.wantStatus {
		t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
	}
	wantHandler := test.wantStatus == http.StatusNoContent
	if handlerCalled != wantHandler {
		t.Errorf("handler called = %t, want %t", handlerCalled, wantHandler)
	}
}

func matrixApplyCredential(request *http.Request, carrier matrixCarrier, credential matrixCredential) {
	token := ""
	switch credential {
	case matrixInvalidCredential:
		token = "unknown-matrix-token"
	case matrixOrdinaryCredential:
		token = "ordinary-device-token"
	case matrixAdminCredential:
		token = "admin-device-token"
	case matrixDisabledCredential:
		token = "disabled-device-token"
	case matrixAPIKeyCredential:
		token = "matrix-api-key"
	}
	if token == "" {
		return
	}
	switch carrier {
	case matrixAuthorizationMediaBrowser:
		request.Header.Set("Authorization", `MediaBrowser Token="`+token+`"`)
	case matrixXEmbyAuthorization:
		request.Header.Set("X-Emby-Authorization", `MediaBrowser Token="`+token+`"`)
	case matrixXEmbyToken:
		request.Header.Set("X-Emby-Token", token)
	case matrixXMediaBrowserToken:
		request.Header.Set("X-MediaBrowser-Token", token)
	case matrixAPIKey:
		request.URL.RawQuery = "ApiKey=" + token
	case matrixLegacyAPIKey:
		request.URL.RawQuery = "api_key=" + token
	}
}

type matrixStartup struct{ completed bool }

func (startup matrixStartup) StartupWizardCompleted() bool { return startup.completed }

type matrixNetwork struct{ local map[netip.Addr]bool }

func (network matrixNetwork) IsInLocalNetwork(address netip.Addr) bool { return network.local[address] }

type matrixSchedules struct{ blocked bool }

func (schedules matrixSchedules) AccessSchedules(context.Context, dto.UUID) ([]store.AccessSchedule, error) {
	if !schedules.blocked {
		return nil, nil
	}
	return []store.AccessSchedule{{DayOfWeek: 7, StartHour: 0, EndHour: 0}}, nil
}

type matrixUsers struct{ user store.User }

func (users matrixUsers) Get(_ context.Context, userID dto.UUID) (store.User, error) {
	if userID != users.user.ID {
		return store.User{}, store.ErrNotFound
	}
	return users.user, nil
}

type matrixDeviceTokens struct{ devices map[string]store.Device }

func (tokens *matrixDeviceTokens) Token(_ context.Context, token string) (store.Device, error) {
	device, found := tokens.devices[token]
	if !found {
		return store.Device{}, store.ErrNotFound
	}
	return device, nil
}

func (tokens *matrixDeviceTokens) RefreshDeviceIfCurrent(_ context.Context, device store.Device) (store.Device, bool, error) {
	return device, true, nil
}

type matrixAPIKeys struct{ keys map[string]store.APIKey }

func (keys *matrixAPIKeys) APIKeyByToken(_ context.Context, token string) (store.APIKey, error) {
	key, found := keys.keys[token]
	if !found {
		return store.APIKey{}, store.ErrNotFound
	}
	return key, nil
}
