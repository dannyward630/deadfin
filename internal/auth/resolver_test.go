// SPDX-License-Identifier: GPL-2.0-only

package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

var (
	resolverTestUserID = dto.UUID{0x71, 0x59, 0x19, 0x51, 0x0d, 0x3c, 0x48, 0x4d, 0x85, 0x9e, 0x15, 0x1d, 0x7b, 0x2a, 0x09, 0x31}
	resolverTestServer = config.ServerIdentity{
		ServerName: "resolver-test-server",
		ID:         dto.UUID{0x22, 0xd8, 0x4c, 0x7e, 0xf7, 0x91, 0x42, 0x98, 0xa0, 0x31, 0xa9, 0x17, 0x6e, 0xc2, 0x9d, 0x04},
		Version:    "10.10.10-resolver-test",
	}
)

// These compile-time checks keep the resolver's persistence seams minimal: the
// resolver needs device lookup/refresh and API-key lookup, not a concrete store
// or user service.
var (
	_ auth.DeviceTokens = (*fakeDeviceTokens)(nil)
	_ auth.APIKeys      = (*fakeAPIKeys)(nil)
	_ auth.DeviceTokens = (*blockingDeviceTokens)(nil)
	_ auth.DeviceTokens = (*revokingDeviceTokens)(nil)
	_ auth.DeviceTokens = (*orderedDeviceTokens)(nil)
)

func TestExtractTokenPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		legacy     bool
		headers    map[string]string
		query      url.Values
		wantToken  string
		wantClient string
	}{
		{
			name:       "MediaBrowser dictionary token wins over every fallback",
			legacy:     true,
			headers:    map[string]string{"Authorization": `MediaBrowser Client="dictionary client", Device="dictionary device", DeviceId="dictionary-id", Version="1.2.3", Token="dictionary-token"`, "X-Emby-Token": "x-emby-token", "X-MediaBrowser-Token": "x-mediabrowser-token"},
			query:      url.Values{"ApiKey": {"api-key-token"}, "api_key": {"legacy-api-key-token"}},
			wantToken:  "dictionary-token",
			wantClient: "dictionary client",
		},
		{
			name:      "empty dictionary token falls through to legacy X-Emby token",
			legacy:    true,
			headers:   map[string]string{"Authorization": `MediaBrowser Token=""`, "X-Emby-Token": "x-emby-token", "X-MediaBrowser-Token": "x-mediabrowser-token"},
			query:     url.Values{"ApiKey": {"api-key-token"}},
			wantToken: "x-emby-token",
		},
		{
			name:      "legacy dictionary carrier participates only when enabled",
			legacy:    true,
			headers:   map[string]string{"X-Emby-Authorization": `MediaBrowser Token="legacy-dictionary-token"`, "X-Emby-Token": "x-emby-token"},
			query:     url.Values{"ApiKey": {"api-key-token"}},
			wantToken: "legacy-dictionary-token",
		},
		{
			name:      "legacy dictionary carrier is ignored when disabled",
			legacy:    false,
			headers:   map[string]string{"X-Emby-Authorization": `MediaBrowser Token="legacy-dictionary-token"`, "X-Emby-Token": "x-emby-token"},
			query:     url.Values{"ApiKey": {"api-key-token"}},
			wantToken: "api-key-token",
		},
		{
			name:      "present Authorization carrier blocks conflicting legacy dictionary even when malformed",
			legacy:    true,
			headers:   map[string]string{"Authorization": "Bearer not-a-media-browser-dictionary", "X-Emby-Authorization": `MediaBrowser Token="legacy-dictionary-token"`},
			query:     url.Values{"ApiKey": {"api-key-token"}},
			wantToken: "api-key-token",
		},
		{
			name:      "legacy Emby scheme is accepted when enabled",
			legacy:    true,
			headers:   map[string]string{"Authorization": `Emby Token="emby-scheme-token"`},
			query:     url.Values{"ApiKey": {"api-key-token"}},
			wantToken: "emby-scheme-token",
		},
		{
			name:      "legacy Emby scheme is ignored when disabled",
			legacy:    false,
			headers:   map[string]string{"Authorization": `Emby Token="emby-scheme-token"`},
			query:     url.Values{"ApiKey": {"api-key-token"}},
			wantToken: "api-key-token",
		},
		{
			name:      "legacy X-Emby token wins over X-MediaBrowser token",
			legacy:    true,
			headers:   map[string]string{"X-Emby-Token": "x-emby-token", "X-MediaBrowser-Token": "x-mediabrowser-token"},
			query:     url.Values{"ApiKey": {"api-key-token"}},
			wantToken: "x-emby-token",
		},
		{
			name:      "empty X-Emby token falls through to X-MediaBrowser token",
			legacy:    true,
			headers:   map[string]string{"X-Emby-Token": "", "X-MediaBrowser-Token": "x-mediabrowser-token"},
			query:     url.Values{"ApiKey": {"api-key-token"}},
			wantToken: "x-mediabrowser-token",
		},
		{
			name:      "legacy token headers are ignored when disabled",
			legacy:    false,
			headers:   map[string]string{"X-Emby-Token": "x-emby-token", "X-MediaBrowser-Token": "x-mediabrowser-token"},
			query:     url.Values{"ApiKey": {"api-key-token"}},
			wantToken: "api-key-token",
		},
		{
			name:      "ApiKey query is always accepted",
			legacy:    false,
			query:     url.Values{"ApiKey": {"api-key-token"}, "api_key": {"legacy-api-key-token"}},
			wantToken: "api-key-token",
		},
		{
			name:      "legacy api_key query is accepted only when enabled",
			legacy:    true,
			query:     url.Values{"api_key": {"legacy-api-key-token"}},
			wantToken: "legacy-api-key-token",
		},
		{
			name:      "legacy api_key query is ignored when disabled",
			legacy:    false,
			query:     url.Values{"api_key": {"legacy-api-key-token"}},
			wantToken: "",
		},
		{
			name:      "empty ApiKey falls through to enabled legacy api_key",
			legacy:    true,
			query:     url.Values{"ApiKey": {""}, "api_key": {"legacy-api-key-token"}},
			wantToken: "legacy-api-key-token",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := resolverRequest(test.headers, test.query)

			got := auth.Extract(request, test.legacy)
			if got.Token != test.wantToken || got.Client != test.wantClient {
				t.Fatalf("Extract() = token %q, client %q; want token %q, client %q", got.Token, got.Client, test.wantToken, test.wantClient)
			}
		})
	}
}

func TestResolverReadsLegacyAuthorizationSettingForEveryRequest(t *testing.T) {
	const token = "dynamic-legacy-token"
	devices := &fakeDeviceTokens{devices: map[string]store.Device{
		token: {AccessToken: token, UserID: resolverTestUserID},
	}}
	var legacy atomic.Bool
	resolver := auth.NewResolver(auth.ResolverConfig{
		Devices:                    devices,
		Server:                     resolverTestServer,
		LegacyAuthorizationEnabled: legacy.Load,
	})

	modern := resolverRequest(map[string]string{"Authorization": `MediaBrowser Token="` + token + `"`}, nil)
	if identity, err := resolver.Resolve(t.Context(), modern); err != nil || !identity.Authenticated || identity.Token != token {
		t.Fatalf("resolve modern MediaBrowser carrier while legacy authorization is disabled = %#v, %v; want authenticated token %q", identity, err, token)
	}

	for _, test := range []struct {
		name    string
		headers map[string]string
		query   url.Values
	}{
		{name: "X-Emby-Token", headers: map[string]string{"X-Emby-Token": token}},
		{name: "X-MediaBrowser-Token", headers: map[string]string{"X-MediaBrowser-Token": token}},
		{name: "Emby authorization scheme", headers: map[string]string{"Authorization": `Emby Token="` + token + `"`}},
		{name: "X-Emby-Authorization carrier", headers: map[string]string{"X-Emby-Authorization": `MediaBrowser Token="` + token + `"`}},
		{name: "api_key query", query: url.Values{"api_key": {token}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := resolverRequest(test.headers, test.query)

			identity, err := resolver.Resolve(t.Context(), request)
			if err != nil {
				t.Fatalf("resolve disabled legacy carrier: %v", err)
			}
			if identity.Authenticated || identity.Token != "" {
				t.Fatalf("disabled legacy carrier identity = %#v, want unauthenticated empty identity", identity)
			}

			legacy.Store(true)
			identity, err = resolver.Resolve(t.Context(), request)
			if err != nil {
				t.Fatalf("resolve enabled legacy carrier: %v", err)
			}
			if !identity.Authenticated || identity.Token != token {
				t.Fatalf("enabled legacy carrier identity = %#v, want authenticated token %q", identity, token)
			}
			legacy.Store(false)
		})
	}
}

func TestExtractMatchesQueryKeysCaseInsensitivelyWithoutCollapsingApiKeyForms(t *testing.T) {
	tests := []struct {
		name      string
		legacy    bool
		query     url.Values
		wantToken string
	}{
		{
			name:      "ApiKey matches mixed casing while legacy authorization is disabled",
			legacy:    false,
			query:     url.Values{"aPiKeY": {"always-token"}},
			wantToken: "always-token",
		},
		{
			name:      "api_key matches mixed casing when legacy authorization is enabled",
			legacy:    true,
			query:     url.Values{"API_KEY": {"legacy-token"}},
			wantToken: "legacy-token",
		},
		{
			name:      "api_key remains gated despite case insensitive matching",
			legacy:    false,
			query:     url.Values{"Api_KeY": {"legacy-token"}},
			wantToken: "",
		},
		{
			name:      "ApiKey stays preferred over the distinct underscore form",
			legacy:    true,
			query:     url.Values{"APIKEY": {"always-token"}, "aPi_KeY": {"legacy-token"}},
			wantToken: "always-token",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := auth.Extract(resolverRequest(nil, test.query), test.legacy)
			if got.Token != test.wantToken {
				t.Fatalf("Extract() token = %q, want %q", got.Token, test.wantToken)
			}
		})
	}
}

func TestExtractScalarizesRepeatedFallbackValuesButUsesFirstAuthorizationCarrier(t *testing.T) {
	tests := []struct {
		name      string
		legacy    bool
		headers   http.Header
		query     url.Values
		wantToken string
	}{
		{
			name:      "repeated legacy token header is comma scalarized",
			legacy:    true,
			headers:   http.Header{"X-Emby-Token": {"first-header-token", "second-header-token"}},
			wantToken: "first-header-token,second-header-token",
		},
		{
			name:      "repeated always ApiKey query is comma scalarized",
			legacy:    false,
			query:     url.Values{"ApiKey": {"first-query-token", "second-query-token"}},
			wantToken: "first-query-token,second-query-token",
		},
		{
			name:      "repeated Authorization header uses only its first carrier",
			legacy:    true,
			headers:   http.Header{"Authorization": {`MediaBrowser Token="first-authorization-token"`, `MediaBrowser Token="second-authorization-token"`}},
			wantToken: "first-authorization-token",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := resolverRequest(nil, test.query)
			request.Header = test.headers.Clone()

			got := auth.Extract(request, test.legacy)
			if got.Token != test.wantToken {
				t.Fatalf("Extract() token = %q, want %q", got.Token, test.wantToken)
			}
		})
	}
}

func TestExtractDecodesValidEscapesAroundMalformedPercentTriplets(t *testing.T) {
	request := resolverRequest(map[string]string{
		"Authorization": `MediaBrowser Client="%22%Hörbücher", Device="%41%H", Token="decode-token"`,
	}, nil)

	got := auth.Extract(request, false)
	if got.Token != "decode-token" || got.Client != `"%Hörbücher` || got.Device != "A%H" {
		t.Fatalf("Extract() = token %q client %q device %q; want token %q client %q device %q", got.Token, got.Client, got.Device, "decode-token", `"%Hörbücher`, "A%H")
	}
}

func TestExtractAccumulatesCaseEquivalentQueryBucketsInWireOrder(t *testing.T) {
	tests := []struct {
		name      string
		rawQuery  string
		wantToken string
	}{
		{
			name:      "canonical bucket before case variant",
			rawQuery:  "ApiKey=first&APIKEY=second",
			wantToken: "first,second",
		},
		{
			name:      "neither bucket uses canonical casing",
			rawQuery:  "aPiKeY=first&APIKEY=second",
			wantToken: "first,second",
		},
		{
			name:      "case variant before canonical bucket preserves reversed wire order",
			rawQuery:  "APIKEY=second&ApiKey=first",
			wantToken: "second,first",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/resolver-test?"+test.rawQuery, nil)
			got := auth.Extract(request, false)
			if got.Token != test.wantToken {
				t.Fatalf("Extract() token = %q, want %q", got.Token, test.wantToken)
			}
		})
	}
}

func TestExtractScalarizationSkipsEmptyFallbackValues(t *testing.T) {
	tests := []struct {
		name      string
		legacy    bool
		headers   http.Header
		query     url.Values
		wantToken string
	}{
		{
			name:      "legacy header omits empty values and their separators",
			legacy:    true,
			headers:   http.Header{"X-Emby-Token": {"", "first-header-token", "", "second-header-token", ""}},
			wantToken: "first-header-token,second-header-token",
		},
		{
			name:      "ApiKey query omits empty values and their separators",
			legacy:    false,
			query:     url.Values{"ApiKey": {"", "first-query-token", "", "second-query-token", ""}},
			wantToken: "first-query-token,second-query-token",
		},
		{
			name:      "all empty legacy header values remain empty",
			legacy:    true,
			headers:   http.Header{"X-Emby-Token": {"", ""}},
			wantToken: "",
		},
		{
			name:      "all empty ApiKey query values remain empty",
			legacy:    false,
			query:     url.Values{"ApiKey": {"", ""}},
			wantToken: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := resolverRequest(nil, test.query)
			request.Header = test.headers.Clone()
			got := auth.Extract(request, test.legacy)
			if got.Token != test.wantToken {
				t.Fatalf("Extract() token = %q, want %q", got.Token, test.wantToken)
			}
		})
	}
}

func TestExtractReplacesInvalidUTF8PercentDecodedBytes(t *testing.T) {
	tests := []struct {
		name       string
		encoded    string
		wantClient string
	}{
		{name: "invalid leading byte", encoded: "%FF", wantClient: "\uFFFD"},
		{name: "truncated leading byte", encoded: "%C3", wantClient: "\uFFFD"},
		{name: "invalid leading byte before raw UTF8", encoded: "%C3ö", wantClient: "\uFFFDö"},
		{name: "valid percent encoded UTF8", encoded: "%C3%B6", wantClient: "ö"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := resolverRequest(map[string]string{
				"Authorization": `MediaBrowser Client="` + test.encoded + `", Token="decode-token"`,
			}, nil)
			got := auth.Extract(request, false)
			if got.Client != test.wantClient {
				t.Fatalf("Extract() client = %q, want %q", got.Client, test.wantClient)
			}
		})
	}
}

func TestExtractAuthorizationCarrierPresenceControlsLegacyDictionaryFallback(t *testing.T) {
	tests := []struct {
		name      string
		authority []string
		wantToken string
	}{
		{
			name:      "empty first Authorization value followed by Bearer suppresses legacy dictionary",
			authority: []string{"", "Bearer ignored"},
			wantToken: "",
		},
		{
			name:      "single empty Authorization value permits legacy dictionary",
			authority: []string{""},
			wantToken: "legacy-dictionary-token",
		},
		{
			name:      "all empty Authorization values permit legacy dictionary",
			authority: []string{"", ""},
			wantToken: "legacy-dictionary-token",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := resolverRequest(nil, nil)
			request.Header = http.Header{
				"Authorization":        test.authority,
				"X-Emby-Authorization": {`MediaBrowser Token="legacy-dictionary-token"`},
			}
			got := auth.Extract(request, true)
			if got.Token != test.wantToken {
				t.Fatalf("Extract() token = %q, want %q", got.Token, test.wantToken)
			}
		})
	}
}

func TestExtractRetainsPermissiveRawQueryTokens(t *testing.T) {
	tests := []struct {
		name      string
		rawQuery  string
		wantToken string
	}{
		{
			name:      "malformed percent escape is retained literally",
			rawQuery:  "ApiKey=abc%H",
			wantToken: "abc%H",
		},
		{
			name:      "unescaped semicolon is retained literally",
			rawQuery:  "ApiKey=abc;def",
			wantToken: "abc;def",
		},
		{
			name:      "invalid UTF8 percent FF remains literal in raw query",
			rawQuery:  "ApiKey=%FF",
			wantToken: "%FF",
		},
		{
			name:      "truncated UTF8 percent C3 remains literal in raw query",
			rawQuery:  "ApiKey=%C3",
			wantToken: "%C3",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/resolver-test?"+test.rawQuery, nil)
			got := auth.Extract(request, false)
			if got.Token != test.wantToken {
				t.Fatalf("Extract() token = %q, want %q", got.Token, test.wantToken)
			}
		})
	}
}

func TestResolverTreatsUnicodeWhitespaceTokensAsAbsent(t *testing.T) {
	tests := []struct {
		name      string
		encoded   string
		wantToken string
	}{
		{name: "ASCII space", encoded: "%20", wantToken: " "},
		{name: "non-breaking space", encoded: "%C2%A0", wantToken: "\u00a0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			devices := &fakeDeviceTokens{}
			keys := &fakeAPIKeys{}
			resolver := newResolver(devices, keys, false)
			request := cachedResolverRequest(map[string]string{
				"Authorization": `MediaBrowser Client="whitespace-client", Device="whitespace-device", DeviceId="whitespace-id", Version="whitespace-version", Token="` + test.encoded + `"`,
			}, nil)

			identity, err := resolver.Resolve(request.Context(), request)
			if err != nil {
				t.Fatalf("resolve whitespace token: %v", err)
			}
			if identity.Token != test.wantToken || identity.Client != "whitespace-client" || identity.Device != "whitespace-device" || identity.DeviceID != "whitespace-id" || identity.Version != "whitespace-version" {
				t.Fatalf("whitespace identity = %#v; want extracted metadata retained", identity)
			}
			if identity.Authenticated || identity.APIKey || identity.Administrator || devices.tokenCalls != 0 || devices.refreshCalls != 0 || keys.lookupCalls != 0 {
				t.Fatalf("whitespace token resolution = %#v, device:%d refresh:%d api-key:%d; want unauthenticated identity without persistence access", identity, devices.tokenCalls, devices.refreshCalls, keys.lookupCalls)
			}
		})
	}
}

func TestResolverPrefersDeviceCredentialOverAPIKey(t *testing.T) {
	devices := &fakeDeviceTokens{devices: map[string]store.Device{
		"collision-token": {
			AccessToken: "collision-token",
			UserID:      resolverTestUserID,
			AppName:     "stored-client",
			AppVersion:  "stored-version",
			DeviceName:  "stored-device",
			DeviceID:    "stored-device-id",
		},
	}}
	keys := &fakeAPIKeys{keys: map[string]store.APIKey{
		"collision-token": {AccessToken: "collision-token", Name: "API key must lose to device"},
	}}
	resolver := newResolver(devices, keys, true)
	request := cachedResolverRequest(map[string]string{
		"Authorization": `MediaBrowser Client="request-client", Device="request-device", DeviceId="request-device-id", Version="request-version", Token="collision-token"`,
	}, nil)

	identity, err := resolver.Resolve(request.Context(), request)
	if err != nil {
		t.Fatalf("resolve device/API-key collision: %v", err)
	}
	if !identity.Authenticated || identity.APIKey || identity.Administrator {
		t.Fatalf("collision identity flags = authenticated:%t apiKey:%t administrator:%t; want device-authenticated non-admin identity", identity.Authenticated, identity.APIKey, identity.Administrator)
	}
	if identity.Token != "collision-token" || identity.UserID != resolverTestUserID {
		t.Fatalf("collision identity = token %q user %s; want device token and user %s", identity.Token, identity.UserID, resolverTestUserID)
	}
	if identity.Client != "request-client" || identity.Device != "request-device" || identity.DeviceID != "request-device-id" || identity.Version != "request-version" {
		t.Fatalf("collision request metadata = %#v; want supplied metadata retained for the device credential", identity)
	}
	if devices.tokenCalls != 1 || devices.refreshCalls != 1 || keys.lookupCalls != 0 {
		t.Fatalf("collision lookup/side effects = device:%d refresh:%d api-key:%d; want device:1 refresh:1 api-key:0", devices.tokenCalls, devices.refreshCalls, keys.lookupCalls)
	}
}

func TestResolverBuildsAdministratorIdentityForAPIKey(t *testing.T) {
	devices := &fakeDeviceTokens{}
	keys := &fakeAPIKeys{keys: map[string]store.APIKey{
		"administrator-key": {AccessToken: "administrator-key", Name: "automation administrator"},
	}}
	resolver := newResolver(devices, keys, false)
	request := cachedResolverRequest(map[string]string{
		"Authorization": `MediaBrowser Client="spoofed-client", Token="administrator-key"`,
	}, nil)

	identity, err := resolver.Resolve(request.Context(), request)
	if err != nil {
		t.Fatalf("resolve API key: %v", err)
	}
	if !identity.Authenticated || !identity.APIKey || !identity.Administrator {
		t.Fatalf("API-key identity flags = authenticated:%t apiKey:%t administrator:%t; want authenticated API-key administrator", identity.Authenticated, identity.APIKey, identity.Administrator)
	}
	if identity.Token != "administrator-key" || identity.Client != "automation administrator" {
		t.Fatalf("API-key identity = token %q client %q; want key token and key name", identity.Token, identity.Client)
	}
	if identity.DeviceID != resolverTestServer.ID.String() || identity.Device != resolverTestServer.ServerName || identity.Version != resolverTestServer.Version {
		t.Fatalf("API-key server identity = deviceID %q device %q version %q; want server identity", identity.DeviceID, identity.Device, identity.Version)
	}
	if devices.tokenCalls != 1 || devices.refreshCalls != 0 || keys.lookupCalls != 1 {
		t.Fatalf("API-key lookup/side effects = device:%d refresh:%d api-key:%d; want device:1 refresh:0 api-key:1", devices.tokenCalls, devices.refreshCalls, keys.lookupCalls)
	}
}

func TestResolverLeavesUnknownTokenUnauthenticated(t *testing.T) {
	devices := &fakeDeviceTokens{}
	keys := &fakeAPIKeys{}
	resolver := newResolver(devices, keys, false)
	request := cachedResolverRequest(map[string]string{
		"Authorization": `MediaBrowser Token="unknown-token"`,
	}, nil)

	identity, err := resolver.Resolve(request.Context(), request)
	if err != nil {
		t.Fatalf("resolve unknown token: %v", err)
	}
	if identity.Authenticated || identity.APIKey || identity.Administrator || identity.Token != "unknown-token" {
		t.Fatalf("unknown-token identity = %#v; want unauthenticated identity retaining the presented token", identity)
	}
	if devices.tokenCalls != 1 || devices.refreshCalls != 0 || keys.lookupCalls != 1 {
		t.Fatalf("unknown-token lookup/side effects = device:%d refresh:%d api-key:%d; want device:1 refresh:0 api-key:1", devices.tokenCalls, devices.refreshCalls, keys.lookupCalls)
	}
}

func TestRequestCacheAvoidsDuplicateLookupAndRefresh(t *testing.T) {
	devices := &fakeDeviceTokens{devices: map[string]store.Device{
		"cached-token": {
			AccessToken: "cached-token",
			UserID:      resolverTestUserID,
			AppName:     "stored-client",
			AppVersion:  "stored-version",
			DeviceName:  "stored-device",
			DeviceID:    "stored-device-id",
		},
	}}
	resolver := newResolver(devices, &fakeAPIKeys{}, false)
	request := cachedResolverRequest(map[string]string{
		"Authorization": `MediaBrowser Token="cached-token"`,
	}, nil)

	first, err := resolver.Resolve(request.Context(), request)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	second, err := resolver.Resolve(request.Context(), request)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if first != second {
		t.Fatalf("cached identity = %#v after first %#v; want the same resolved request identity", second, first)
	}
	if devices.tokenCalls != 1 || devices.refreshCalls != 1 {
		t.Fatalf("same-request lookup/side effects = device:%d refresh:%d; want exactly one each", devices.tokenCalls, devices.refreshCalls)
	}

	separateRequest := cachedResolverRequest(map[string]string{
		"Authorization": `MediaBrowser Token="cached-token"`,
	}, nil)
	if _, err := resolver.Resolve(separateRequest.Context(), separateRequest); err != nil {
		t.Fatalf("resolve separate request: %v", err)
	}
	if devices.tokenCalls != 2 || devices.refreshCalls != 2 {
		t.Fatalf("separate-request lookup/side effects = device:%d refresh:%d; want one resolution per request", devices.tokenCalls, devices.refreshCalls)
	}
}

func TestRequestCacheCoalescesConcurrentResolution(t *testing.T) {
	devices := &blockingDeviceTokens{
		device: store.Device{
			AccessToken: "concurrent-token",
			UserID:      resolverTestUserID,
			AppName:     "stored-client",
			AppVersion:  "stored-version",
			DeviceName:  "stored-device",
			DeviceID:    "stored-device-id",
		},
		firstTokenLookup:   make(chan struct{}),
		releaseTokenLookup: make(chan struct{}),
	}
	resolver := newResolver(devices, &fakeAPIKeys{}, false)
	request := cachedResolverRequest(map[string]string{
		"Authorization": `MediaBrowser Token="concurrent-token"`,
	}, nil)

	type result struct {
		identity auth.Identity
		err      error
	}
	const callers = 8
	start := make(chan struct{})
	results := make(chan result, callers)
	var callersDone sync.WaitGroup
	for range callers {
		callersDone.Add(1)
		go func() {
			defer callersDone.Done()
			<-start
			identity, err := resolver.Resolve(request.Context(), request)
			results <- result{identity: identity, err: err}
		}()
	}
	close(start)
	<-devices.firstTokenLookup
	close(devices.releaseTokenLookup)
	callersDone.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent resolve: %v", result.err)
		}
		if !result.identity.Authenticated || result.identity.Token != "concurrent-token" {
			t.Fatalf("concurrent identity = %#v; want authenticated cached device identity", result.identity)
		}
	}
	if tokenCalls, refreshCalls := devices.counts(); tokenCalls != 1 || refreshCalls != 1 {
		t.Fatalf("concurrent same-request lookup/side effects = device:%d refresh:%d; want exactly one each", tokenCalls, refreshCalls)
	}
}

func TestResolverRefreshDoesNotRecreateLoggedOutDevice(t *testing.T) {
	devices := &revokingDeviceTokens{device: store.Device{
		AccessToken: "logout-race-token",
		UserID:      resolverTestUserID,
		AppName:     "stored-client",
		AppVersion:  "stored-version",
		DeviceName:  "stored-device",
		DeviceID:    "stored-device-id",
	}}
	resolver := newResolver(devices, &fakeAPIKeys{}, false)
	request := cachedResolverRequest(map[string]string{
		"Authorization": `MediaBrowser Token="logout-race-token"`,
	}, nil)

	identity, err := resolver.Resolve(request.Context(), request)
	if err != nil {
		t.Fatalf("resolve across concurrent logout: %v", err)
	}
	if !identity.Authenticated || identity.Token != "logout-race-token" {
		t.Fatalf("logout-race identity = %#v; want identity resolved before the concurrent logout", identity)
	}
	if refreshCalls, revoked, recreated := devices.state(); refreshCalls != 1 || !revoked || recreated {
		t.Fatalf("logout-race refresh state = calls:%d revoked:%t recreated:%t; want one conditional refresh that leaves the token revoked", refreshCalls, revoked, recreated)
	}
}

func TestResolverRefreshRejectsDelayedStaleDeviceMetadata(t *testing.T) {
	devices := &orderedDeviceTokens{
		current: store.Device{
			AccessToken: "metadata-race-token",
			UserID:      resolverTestUserID,
			AppName:     "stored-client",
			AppVersion:  "initial-version",
			DeviceName:  "initial-device",
			DeviceID:    "stored-device-id",
		},
		oldRefreshStarted: make(chan struct{}),
		releaseOldRefresh: make(chan struct{}),
	}
	resolver := newResolver(devices, &fakeAPIKeys{}, false)
	olderRequest := cachedResolverRequest(map[string]string{
		"Authorization": `MediaBrowser Device="older-device", Version="older-version", Token="metadata-race-token"`,
	}, nil)
	newerRequest := cachedResolverRequest(map[string]string{
		"Authorization": `MediaBrowser Device="newer-device", Version="newer-version", Token="metadata-race-token"`,
	}, nil)

	olderDone := make(chan error, 1)
	go func() {
		_, err := resolver.Resolve(olderRequest.Context(), olderRequest)
		olderDone <- err
	}()
	<-devices.oldRefreshStarted
	if _, err := resolver.Resolve(newerRequest.Context(), newerRequest); err != nil {
		t.Fatalf("resolve newer device snapshot: %v", err)
	}
	close(devices.releaseOldRefresh)
	if err := <-olderDone; err != nil {
		t.Fatalf("resolve delayed older device snapshot: %v", err)
	}

	device, staleRefreshes := devices.state()
	if device.DeviceName != "newer-device" || device.AppVersion != "newer-version" || staleRefreshes != 1 {
		t.Fatalf("device after racing refreshes = %#v with %d stale refreshes; want newer metadata retained and the delayed older snapshot rejected", device, staleRefreshes)
	}
}

func newResolver(devices auth.DeviceTokens, keys auth.APIKeys, legacy bool) *auth.Resolver {
	return auth.NewResolver(auth.ResolverConfig{
		Devices:                   devices,
		APIKeys:                   keys,
		Server:                    resolverTestServer,
		EnableLegacyAuthorization: legacy,
	})
}

func resolverRequest(headers map[string]string, query url.Values) *http.Request {
	path := "/resolver-test"
	if len(query) != 0 {
		path += "?" + query.Encode()
	}
	request := httptest.NewRequest(http.MethodGet, path, nil)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return request
}

func cachedResolverRequest(headers map[string]string, query url.Values) *http.Request {
	request := resolverRequest(headers, query)
	return request.WithContext(auth.NewRequestCache(request.Context()))
}

type fakeDeviceTokens struct {
	devices      map[string]store.Device
	tokenCalls   int
	refreshCalls int
}

func (tokens *fakeDeviceTokens) Token(_ context.Context, token string) (store.Device, error) {
	tokens.tokenCalls++
	device, found := tokens.devices[token]
	if !found {
		return store.Device{}, store.ErrNotFound
	}
	return device, nil
}

func (tokens *fakeDeviceTokens) RefreshDeviceIfCurrent(_ context.Context, device store.Device) (store.Device, bool, error) {
	tokens.refreshCalls++
	return device, true, nil
}

type fakeAPIKeys struct {
	keys        map[string]store.APIKey
	lookupCalls int
}

func (keys *fakeAPIKeys) APIKeyByToken(_ context.Context, token string) (store.APIKey, error) {
	keys.lookupCalls++
	key, found := keys.keys[token]
	if !found {
		return store.APIKey{}, store.ErrNotFound
	}
	return key, nil
}

type blockingDeviceTokens struct {
	device             store.Device
	firstTokenLookup   chan struct{}
	releaseTokenLookup chan struct{}
	mu                 sync.Mutex
	tokenCalls         int
	refreshCalls       int
}

func (tokens *blockingDeviceTokens) Token(_ context.Context, token string) (store.Device, error) {
	tokens.mu.Lock()
	tokens.tokenCalls++
	first := tokens.tokenCalls == 1
	tokens.mu.Unlock()
	if first {
		close(tokens.firstTokenLookup)
	}
	<-tokens.releaseTokenLookup
	if token != tokens.device.AccessToken {
		return store.Device{}, store.ErrNotFound
	}
	return tokens.device, nil
}

func (tokens *blockingDeviceTokens) RefreshDeviceIfCurrent(_ context.Context, device store.Device) (store.Device, bool, error) {
	tokens.mu.Lock()
	tokens.refreshCalls++
	tokens.mu.Unlock()
	return device, true, nil
}

func (tokens *blockingDeviceTokens) counts() (tokenCalls, refreshCalls int) {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	return tokens.tokenCalls, tokens.refreshCalls
}

// revokingDeviceTokens models the user-service race where Token has read a
// device row and Logout removes it before the conditional activity update.
type revokingDeviceTokens struct {
	mu           sync.Mutex
	device       store.Device
	revoked      bool
	recreated    bool
	refreshCalls int
}

func (tokens *revokingDeviceTokens) Token(_ context.Context, token string) (store.Device, error) {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	if token != tokens.device.AccessToken || tokens.revoked {
		return store.Device{}, store.ErrNotFound
	}
	device := tokens.device
	// Logout wins immediately after lookup and before refresh.
	tokens.revoked = true
	return device, nil
}

func (tokens *revokingDeviceTokens) RefreshDeviceIfCurrent(_ context.Context, device store.Device) (store.Device, bool, error) {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	tokens.refreshCalls++
	if tokens.revoked {
		return device, false, nil
	}
	tokens.recreated = true
	return device, true, nil
}

func (tokens *revokingDeviceTokens) state() (refreshCalls int, revoked, recreated bool) {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	return tokens.refreshCalls, tokens.revoked, tokens.recreated
}

// orderedDeviceTokens holds the first refresh until a second request commits
// newer metadata. Conditional refresh must then reject the older snapshot.
type orderedDeviceTokens struct {
	mu                sync.Mutex
	current           store.Device
	oldRefreshStarted chan struct{}
	releaseOldRefresh chan struct{}
	staleRefreshes    int
}

func (tokens *orderedDeviceTokens) Token(_ context.Context, token string) (store.Device, error) {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	if token != tokens.current.AccessToken {
		return store.Device{}, store.ErrNotFound
	}
	return tokens.current, nil
}

func (tokens *orderedDeviceTokens) RefreshDeviceIfCurrent(_ context.Context, device store.Device) (store.Device, bool, error) {
	if device.DeviceName == "older-device" {
		close(tokens.oldRefreshStarted)
		<-tokens.releaseOldRefresh
	}
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	if device.DateModified != tokens.current.DateModified {
		tokens.staleRefreshes++
		return tokens.current, false, nil
	}
	tokens.current.DeviceName = device.DeviceName
	tokens.current.AppVersion = device.AppVersion
	tokens.current.DateModified = tokens.current.DateModified.Add(1)
	return tokens.current, true, nil
}

func (tokens *orderedDeviceTokens) state() (store.Device, int) {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	return tokens.current, tokens.staleRefreshes
}
