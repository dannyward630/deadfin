// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestNormalizerLimitsServerIdentityToPublicSystemInfo(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "server_id"}})
	tests := []struct {
		name   string
		body   string
		wantID string
	}{
		{
			name:   "public system identity",
			body:   `{"Id":"stock-server","Version":"10.10.0","ProductName":"Jellyfin"}`,
			wantID: "<server-id>",
		},
		{
			name:   "root media item identity",
			body:   `{"Id":"movie-42","Type":"Movie","Path":"/media/movie.mkv"}`,
			wantID: "movie-42",
		},
		{
			name:   "root user identity",
			body:   `{"Id":"user-42","Name":"Ada","Policy":{"IsAdministrator":true}}`,
			wantID: "user-42",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := normalizer.normalize(Response{
				Status:  http.StatusOK,
				Headers: http.Header{"Content-Type": {"application/json"}},
				Body:    []byte(test.body),
			}, nil)
			if err != nil {
				t.Fatalf("normalize response: %v", err)
			}
			body, ok := normalized.Body.(map[string]any)
			if !ok {
				t.Fatalf("normalized JSON body = %#v, want object", normalized.Body)
			}
			if got := body["Id"]; got != test.wantID {
				t.Fatalf("normalized Id = %#v, want %#v", got, test.wantID)
			}
		})
	}
}

func TestNormalizerPreservesServerIDOutsidePublicSystemIdentity(t *testing.T) {
	normalized, err := newNormalizer([]Rule{{Action: "server_id"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"Id":"session-42","ServerId":"session-server","NowPlayingItem":{"Id":"movie-42"},"Nested":{"ServerId":"nested-server"}}`),
	}, nil)
	if err != nil {
		t.Fatalf("normalize session response: %v", err)
	}
	body, ok := normalized.Body.(map[string]any)
	if !ok {
		t.Fatalf("normalized JSON body = %#v, want object", normalized.Body)
	}
	if got := body["ServerId"]; got != "session-server" {
		t.Fatalf("normalized session ServerId = %#v, want semantic server reference", got)
	}
	if got := body["Id"]; got != "session-42" {
		t.Fatalf("normalized session Id = %#v, want semantic session identity", got)
	}
	nested, ok := body["Nested"].(map[string]any)
	if !ok {
		t.Fatalf("normalized session Nested = %#v, want object", body["Nested"])
	}
	if got := nested["ServerId"]; got != "nested-server" {
		t.Fatalf("normalized nested ServerId = %#v, want semantic server reference", got)
	}
}

func TestNormalizerNormalizesPublicSystemInfoHostFieldsAcrossCasingProfiles(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "server_id"}, {Action: "environment_fields"}})
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "Pascal case",
			body: `{
				"Id":"pascal-public-system-id",
				"ServerId":"pascal-server-id",
				"ProductName":"Jellyfin",
				"Version":"10.10.0",
				"LocalAddress":"http://192.0.2.10:8096",
				"ServerName":"pascal-host",
				"Nested":{"ServerId":"pascal-nested-server-id"}
			}`,
			want: map[string]any{
				"Id":           "<server-id>",
				"ServerId":     "<server-id>",
				"ProductName":  "Jellyfin",
				"Version":      "10.10.0",
				"LocalAddress": "<environment-local-address>",
				"ServerName":   "<environment-server-name>",
				"Nested":       map[string]any{"ServerId": "pascal-nested-server-id"},
			},
		},
		{
			name: "lower camel case",
			body: `{
				"id":"lower-camel-public-system-id",
				"serverId":"lower-camel-server-id",
				"productName":"Jellyfin",
				"version":"10.10.0",
				"localAddress":"http://192.0.2.20:8096",
				"serverName":"lower-camel-host",
				"Nested":{"serverId":"lower-camel-nested-server-id"}
			}`,
			want: map[string]any{
				"id":           "<server-id>",
				"serverId":     "<server-id>",
				"productName":  "Jellyfin",
				"version":      "10.10.0",
				"localAddress": "<environment-local-address>",
				"serverName":   "<environment-server-name>",
				"Nested":       map[string]any{"serverId": "lower-camel-nested-server-id"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := normalizer.normalize(Response{
				Status:  http.StatusOK,
				Headers: http.Header{"Content-Type": {"application/json"}},
				Body:    []byte(test.body),
			}, nil)
			if err != nil {
				t.Fatalf("normalize response: %v", err)
			}
			if !reflect.DeepEqual(normalized.Body, test.want) {
				t.Fatalf("normalized body = %#v, want %#v", normalized.Body, test.want)
			}
		})
	}
}

func TestNormalizerScopesServerIdentityAcrossSystemAndItemDTOProfiles(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "server_id"}})
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "Pascal system and item DTO fields",
			body: `{
				"Id":"pascal-system-id",
				"ServerId":"pascal-system-server-id",
				"ProductName":"Jellyfin",
				"Version":"10.10.0",
				"ProviderIds":{"ServerId":"provider-server-id"},
				"ImageTags":{"ServerId":"image-tag-server-id"},
				"Metadata":{"ServerId":"metadata-server-id","Id":"metadata-id","MediaSourceId":"metadata-source-id"},
				"Items":[{
					"Id":"pascal-item-id",
					"Type":"Movie",
					"ServerId":"pascal-item-server-id",
					"MediaSourceId":"pascal-media-source-id",
					"ProviderIds":{"ServerId":"item-provider-server-id"},
					"ImageTags":{"ServerId":"item-image-tag-server-id"},
					"Metadata":{"ServerId":"item-metadata-server-id","Id":"item-metadata-id","MediaSourceId":"item-metadata-source-id"}
				}]
			}`,
			want: map[string]any{
				"Id":          "<server-id>",
				"ServerId":    "<server-id>",
				"ProductName": "Jellyfin",
				"Version":     "10.10.0",
				"ProviderIds": map[string]any{"ServerId": "provider-server-id"},
				"ImageTags":   map[string]any{"ServerId": "image-tag-server-id"},
				"Metadata": map[string]any{
					"ServerId":      "metadata-server-id",
					"Id":            "metadata-id",
					"MediaSourceId": "metadata-source-id",
				},
				"Items": []any{map[string]any{
					"Id":            "pascal-item-id",
					"Type":          "Movie",
					"ServerId":      "<server-id>",
					"MediaSourceId": "pascal-media-source-id",
					"ProviderIds":   map[string]any{"ServerId": "item-provider-server-id"},
					"ImageTags":     map[string]any{"ServerId": "item-image-tag-server-id"},
					"Metadata": map[string]any{
						"ServerId":      "item-metadata-server-id",
						"Id":            "item-metadata-id",
						"MediaSourceId": "item-metadata-source-id",
					},
				}},
			},
		},
		{
			name: "Camel system and item DTO fields",
			body: `{
				"id":"camel-system-id",
				"serverId":"camel-system-server-id",
				"productName":"Jellyfin",
				"version":"10.10.0",
				"providerIds":{"serverId":"provider-server-id"},
				"imageTags":{"serverId":"image-tag-server-id"},
				"metadata":{"serverId":"metadata-server-id","id":"metadata-id","mediaSourceId":"metadata-source-id"},
				"items":[{
					"id":"camel-item-id",
					"type":"Movie",
					"serverId":"camel-item-server-id",
					"mediaSourceId":"camel-media-source-id",
					"providerIds":{"serverId":"item-provider-server-id"},
					"imageTags":{"serverId":"item-image-tag-server-id"},
					"metadata":{"serverId":"item-metadata-server-id","id":"item-metadata-id","mediaSourceId":"item-metadata-source-id"}
				}]
			}`,
			want: map[string]any{
				"id":          "<server-id>",
				"serverId":    "<server-id>",
				"productName": "Jellyfin",
				"version":     "10.10.0",
				"providerIds": map[string]any{"serverId": "provider-server-id"},
				"imageTags":   map[string]any{"serverId": "image-tag-server-id"},
				"metadata": map[string]any{
					"serverId":      "metadata-server-id",
					"id":            "metadata-id",
					"mediaSourceId": "metadata-source-id",
				},
				"items": []any{map[string]any{
					"id":            "camel-item-id",
					"type":          "Movie",
					"serverId":      "<server-id>",
					"mediaSourceId": "camel-media-source-id",
					"providerIds":   map[string]any{"serverId": "item-provider-server-id"},
					"imageTags":     map[string]any{"serverId": "item-image-tag-server-id"},
					"metadata": map[string]any{
						"serverId":      "item-metadata-server-id",
						"id":            "item-metadata-id",
						"mediaSourceId": "item-metadata-source-id",
					},
				}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.body)}, nil)
			if err != nil {
				t.Fatalf("normalize response: %v", err)
			}
			if !reflect.DeepEqual(normalized.Body, test.want) {
				t.Fatalf("normalized body = %#v, want %#v", normalized.Body, test.want)
			}
		})
	}
}

func TestBaselineNormalizerEquatesCamelItemServerIDsWithoutTouchingDictionaryKeys(t *testing.T) {
	rules, err := loadRules("normalize")
	if err != nil {
		t.Fatalf("load baseline rules: %v", err)
	}
	normalizer := newNormalizer(rules)
	left, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(`{
		"items":[{
			"id":"shared-item-id",
			"type":"Movie",
			"serverId":"left-server-id",
			"mediaSourceId":"shared-media-source-id",
			"providerIds":{"serverId":"provider-id"},
			"imageTags":{"serverId":"image-tag"},
			"metadata":{"serverId":"metadata-server-id","id":"metadata-id"}
		}]
	}`)}, nil)
	if err != nil {
		t.Fatalf("normalize left response: %v", err)
	}
	right, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(`{
		"items":[{
			"id":"shared-item-id",
			"type":"Movie",
			"serverId":"right-server-id",
			"mediaSourceId":"shared-media-source-id",
			"providerIds":{"serverId":"provider-id"},
			"imageTags":{"serverId":"image-tag"},
			"metadata":{"serverId":"metadata-server-id","id":"metadata-id"}
		}]
	}`)}, nil)
	if err != nil {
		t.Fatalf("normalize right response: %v", err)
	}
	if equal, reason := equalNormalized(left, right, 0); !equal {
		t.Fatalf("baseline normalized Camel item DTOs must compare equal: %s", reason)
	}

	body, ok := left.Body.(map[string]any)
	if !ok {
		t.Fatalf("normalized left body = %#v, want object", left.Body)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("normalized items = %#v, want one DTO", body["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("normalized item = %#v, want object", items[0])
	}
	if got := item["serverId"]; got != "<server-id>" {
		t.Fatalf("normalized item serverId = %#v, want server identity sentinel", got)
	}
	if got := item["providerIds"]; !reflect.DeepEqual(got, map[string]any{"serverId": "provider-id"}) {
		t.Fatalf("providerIds = %#v, want dictionary key and value unchanged", got)
	}
	if got := item["imageTags"]; !reflect.DeepEqual(got, map[string]any{"serverId": "<entity-tag>"}) {
		t.Fatalf("imageTags = %#v, want dictionary key retained while baseline tag rule normalizes its value", got)
	}
	if got := item["metadata"]; !reflect.DeepEqual(got, map[string]any{"serverId": "metadata-server-id", "id": "metadata-id"}) {
		t.Fatalf("metadata = %#v, want unrecognized nested identifiers unchanged", got)
	}
}

func TestNormalizerScopesServerIdentityAcrossLoginUserAndSessionDTOProfiles(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "server_id"}})
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "Pascal login envelope",
			body: `{
				"ServerId":"pascal-login-server-id",
				"User":{"Id":"pascal-user-id","Name":"Ada","Policy":{"ServerId":"policy-map-server-id"},"ServerId":"pascal-user-server-id","ProviderIds":{"ServerId":"user-provider-server-id"}},
				"SessionInfo":{"Id":"pascal-session-id","UserId":"pascal-user-id","Client":"Jellyfin","DeviceId":"pascal-device-id","DeviceName":"Apple TV","ServerId":"pascal-session-server-id","Metadata":{"ServerId":"session-metadata-server-id"}},
				"Metadata":{"ServerId":"envelope-metadata-server-id","Id":"envelope-metadata-id"}
			}`,
			want: `{
				"ServerId":"<server-id>",
				"User":{"Id":"pascal-user-id","Name":"Ada","Policy":{"ServerId":"policy-map-server-id"},"ServerId":"<server-id>","ProviderIds":{"ServerId":"user-provider-server-id"}},
				"SessionInfo":{"Id":"pascal-session-id","UserId":"pascal-user-id","Client":"Jellyfin","DeviceId":"pascal-device-id","DeviceName":"Apple TV","ServerId":"<server-id>","Metadata":{"ServerId":"session-metadata-server-id"}},
				"Metadata":{"ServerId":"envelope-metadata-server-id","Id":"envelope-metadata-id"}
			}`,
		},
		{
			name: "Camel login envelope",
			body: `{
				"serverId":"camel-login-server-id",
				"user":{"id":"camel-user-id","name":"Ada","policy":{"serverId":"policy-map-server-id"},"serverId":"camel-user-server-id","providerIds":{"serverId":"user-provider-server-id"}},
				"sessionInfo":{"id":"camel-session-id","userId":"camel-user-id","client":"Jellyfin","deviceId":"camel-device-id","deviceName":"Apple TV","serverId":"camel-session-server-id","metadata":{"serverId":"session-metadata-server-id"}},
				"metadata":{"serverId":"envelope-metadata-server-id","id":"envelope-metadata-id"}
			}`,
			want: `{
				"serverId":"<server-id>",
				"user":{"id":"camel-user-id","name":"Ada","policy":{"serverId":"policy-map-server-id"},"serverId":"<server-id>","providerIds":{"serverId":"user-provider-server-id"}},
				"sessionInfo":{"id":"camel-session-id","userId":"camel-user-id","client":"Jellyfin","deviceId":"camel-device-id","deviceName":"Apple TV","serverId":"<server-id>","metadata":{"serverId":"session-metadata-server-id"}},
				"metadata":{"serverId":"envelope-metadata-server-id","id":"envelope-metadata-id"}
			}`,
		},
		{
			name: "Pascal current user",
			body: `{
				"Id":"pascal-user-id","Name":"Ada","Policy":{"ServerId":"policy-map-server-id"},"ServerId":"pascal-user-server-id",
				"Metadata":{"ServerId":"user-metadata-server-id","Id":"user-metadata-id"}
			}`,
			want: `{
				"Id":"pascal-user-id","Name":"Ada","Policy":{"ServerId":"policy-map-server-id"},"ServerId":"<server-id>",
				"Metadata":{"ServerId":"user-metadata-server-id","Id":"user-metadata-id"}
			}`,
		},
		{
			name: "Camel current user",
			body: `{
				"id":"camel-user-id","name":"Ada","policy":{"serverId":"policy-map-server-id"},"serverId":"camel-user-server-id",
				"metadata":{"serverId":"user-metadata-server-id","id":"user-metadata-id"}
			}`,
			want: `{
				"id":"camel-user-id","name":"Ada","policy":{"serverId":"policy-map-server-id"},"serverId":"<server-id>",
				"metadata":{"serverId":"user-metadata-server-id","id":"user-metadata-id"}
			}`,
		},
		{
			name: "Pascal session array",
			body: `[{
				"Id":"pascal-session-id","UserId":"pascal-user-id","Client":"Jellyfin","DeviceId":"pascal-device-id","DeviceName":"Apple TV","ServerId":"pascal-session-server-id",
				"Metadata":{"ServerId":"session-metadata-server-id","Id":"session-metadata-id"}
			}]`,
			want: `[{
				"Id":"pascal-session-id","UserId":"pascal-user-id","Client":"Jellyfin","DeviceId":"pascal-device-id","DeviceName":"Apple TV","ServerId":"<server-id>",
				"Metadata":{"ServerId":"session-metadata-server-id","Id":"session-metadata-id"}
			}]`,
		},
		{
			name: "Camel session array",
			body: `[{
				"id":"camel-session-id","userId":"camel-user-id","client":"Jellyfin","deviceId":"camel-device-id","deviceName":"Apple TV","serverId":"camel-session-server-id",
				"metadata":{"serverId":"session-metadata-server-id","id":"session-metadata-id"}
			}]`,
			want: `[{
				"id":"camel-session-id","userId":"camel-user-id","client":"Jellyfin","deviceId":"camel-device-id","deviceName":"Apple TV","serverId":"<server-id>",
				"metadata":{"serverId":"session-metadata-server-id","id":"session-metadata-id"}
			}]`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.body)}, nil)
			if err != nil {
				t.Fatalf("normalize response: %v", err)
			}
			want, ok := decodeJSON([]byte(test.want))
			if !ok {
				t.Fatalf("test expected JSON is invalid: %s", test.want)
			}
			if !reflect.DeepEqual(normalized.Body, want) {
				t.Fatalf("normalized body = %#v, want %#v", normalized.Body, want)
			}
		})
	}
}

func TestNormalizerEquatesConfiguredSystemInfoPathsAcrossCasingProfiles(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "environment_fields"}})
	tests := []struct {
		name        string
		stockBody   string
		deadfinBody string
	}{
		{
			name: "Pascal case",
			stockBody: `{
				"ProgramDataPath":"/config",
				"WebPath":"/config/web",
				"ItemsByNamePath":"/config/data/Root/ItemsByName",
				"CachePath":"/config/cache",
				"LogPath":"/config/log",
				"InternalMetadataPath":"/config/metadata",
				"TranscodingTempPath":"/config/transcodes"
			}`,
			deadfinBody: `{
				"ProgramDataPath":"/var/lib/deadfin",
				"WebPath":"/var/lib/deadfin/web",
				"ItemsByNamePath":"/var/lib/deadfin/data/Root/ItemsByName",
				"CachePath":"/var/lib/deadfin/cache",
				"LogPath":"/var/lib/deadfin/log",
				"InternalMetadataPath":"/var/lib/deadfin/metadata",
				"TranscodingTempPath":"/var/lib/deadfin/transcodes"
			}`,
		},
		{
			name: "lower camel case",
			stockBody: `{
				"programDataPath":"/config",
				"webPath":"/config/web",
				"itemsByNamePath":"/config/data/Root/ItemsByName",
				"cachePath":"/config/cache",
				"logPath":"/config/log",
				"internalMetadataPath":"/config/metadata",
				"transcodingTempPath":"/config/transcodes"
			}`,
			deadfinBody: `{
				"programDataPath":"/var/lib/deadfin",
				"webPath":"/var/lib/deadfin/web",
				"itemsByNamePath":"/var/lib/deadfin/data/Root/ItemsByName",
				"cachePath":"/var/lib/deadfin/cache",
				"logPath":"/var/lib/deadfin/log",
				"internalMetadataPath":"/var/lib/deadfin/metadata",
				"transcodingTempPath":"/var/lib/deadfin/transcodes"
			}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stock, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.stockBody)}, nil)
			if err != nil {
				t.Fatalf("normalize stock SystemInfo response: %v", err)
			}
			deadfin, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.deadfinBody)}, nil)
			if err != nil {
				t.Fatalf("normalize Deadfin SystemInfo response: %v", err)
			}
			if equal, reason := equalNormalized(stock, deadfin, 0); !equal {
				t.Fatalf("configured SystemInfo paths should be environment-only differences: %s", reason)
			}
		})
	}
}

func TestNormalizerPreservesSystemInfoPathPresenceTypesAndCastReceiverApplications(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "environment_fields"}})
	tests := []struct {
		name        string
		stockBody   string
		deadfinBody string
		wantReason  string
	}{
		{
			name:        "missing configured path",
			stockBody:   `{"ProgramDataPath":"/config","WebPath":"/config/web"}`,
			deadfinBody: `{"ProgramDataPath":"/var/lib/deadfin"}`,
			wantReason:  "JSON differs at $",
		},
		{
			name:        "null and configured path string",
			stockBody:   `{"cachePath":null}`,
			deadfinBody: `{"cachePath":"/var/lib/deadfin/cache"}`,
			wantReason:  "JSON differs at $.cachePath",
		},
		{
			name:        "CastReceiverApplications",
			stockBody:   `{"TranscodingTempPath":"/config/transcodes","CastReceiverApplications":["Jellyfin Theater"]}`,
			deadfinBody: `{"TranscodingTempPath":"/var/lib/deadfin/transcodes","CastReceiverApplications":["Deadfin Receiver"]}`,
			wantReason:  "JSON differs at $.CastReceiverApplications[0]",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stock, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.stockBody)}, nil)
			if err != nil {
				t.Fatalf("normalize stock SystemInfo response: %v", err)
			}
			deadfin, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.deadfinBody)}, nil)
			if err != nil {
				t.Fatalf("normalize Deadfin SystemInfo response: %v", err)
			}
			if equal, reason := equalNormalized(stock, deadfin, 0); equal || reason != test.wantReason {
				t.Fatalf("equalNormalized = (%t, %q), want (false, %q)", equal, reason, test.wantReason)
			}
		})
	}
}

func TestNormalizerNormalizesOnlyDeclaredRootAuthenticationTokens(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "auth_tokens"}})
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "Pascal case authentication response",
			body: `{
				"AccessToken":"pascal-root-access-token",
				"Token":"pascal-root-token-must-remain",
				"access_token":"pascal-root-snake-token-must-remain",
				"SessionInfo":{
					"AccessToken":"nested-pascal-access-token-must-remain",
					"accessToken":"nested-camel-access-token-must-remain",
					"Token":"nested-pascal-token-must-remain",
					"token":"nested-camel-token-must-remain"
				}
			}`,
			want: map[string]any{
				"AccessToken":  "<auth-token>",
				"Token":        "pascal-root-token-must-remain",
				"access_token": "pascal-root-snake-token-must-remain",
				"SessionInfo": map[string]any{
					"AccessToken": "nested-pascal-access-token-must-remain",
					"accessToken": "nested-camel-access-token-must-remain",
					"Token":       "nested-pascal-token-must-remain",
					"token":       "nested-camel-token-must-remain",
				},
			},
		},
		{
			name: "lower camel case authentication response",
			body: `{
				"accessToken":"lower-camel-root-access-token",
				"token":"lower-camel-root-token-must-remain",
				"access_token":"lower-camel-root-snake-token-must-remain",
				"SessionInfo":{
					"AccessToken":"nested-pascal-access-token-must-remain",
					"accessToken":"nested-camel-access-token-must-remain",
					"Token":"nested-pascal-token-must-remain",
					"token":"nested-camel-token-must-remain"
				}
			}`,
			want: map[string]any{
				"accessToken":  "<auth-token>",
				"token":        "lower-camel-root-token-must-remain",
				"access_token": "lower-camel-root-snake-token-must-remain",
				"SessionInfo": map[string]any{
					"AccessToken": "nested-pascal-access-token-must-remain",
					"accessToken": "nested-camel-access-token-must-remain",
					"Token":       "nested-pascal-token-must-remain",
					"token":       "nested-camel-token-must-remain",
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := normalizer.normalize(Response{
				Status:  http.StatusOK,
				Headers: http.Header{"Content-Type": {"application/json"}},
				Body:    []byte(test.body),
			}, nil)
			if err != nil {
				t.Fatalf("normalize authentication response: %v", err)
			}
			if !reflect.DeepEqual(normalized.Body, test.want) {
				t.Fatalf("normalized authentication response = %#v, want %#v", normalized.Body, test.want)
			}
		})
	}
}

func TestNormalizerPreservesImageTagsMapShapeWhileNormalizingTagValues(t *testing.T) {
	normalized, err := newNormalizer([]Rule{{Action: "entity_tags"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body: []byte(`{
			"Id":"item-42",
			"ImageTags":{"Primary":"primary-cache-tag","Logo":"logo-cache-tag"},
			"Nested":{"ImageTags":{"Thumb":"thumb-cache-tag"}}
		}`),
	}, nil)
	if err != nil {
		t.Fatalf("normalize image response: %v", err)
	}

	body, ok := normalized.Body.(map[string]any)
	if !ok {
		t.Fatalf("normalized JSON body = %#v, want object", normalized.Body)
	}
	wantTopLevel := map[string]any{
		"Primary": "<entity-tag>",
		"Logo":    "<entity-tag>",
	}
	if got := body["ImageTags"]; !reflect.DeepEqual(got, wantTopLevel) {
		t.Fatalf("ImageTags = %#v, want tag values normalized without losing map keys", got)
	}
	nested, ok := body["Nested"].(map[string]any)
	if !ok {
		t.Fatalf("Nested = %#v, want object retaining nested ImageTags", body["Nested"])
	}
	wantNested := map[string]any{"Thumb": "<entity-tag>"}
	if got := nested["ImageTags"]; !reflect.DeepEqual(got, wantNested) {
		t.Fatalf("nested ImageTags = %#v, want tag values normalized without losing map keys", got)
	}
}

func TestNormalizerNormalizesOnlyItemUserDataKeysAcrossPascalAndCamelDTOs(t *testing.T) {
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "Pascal case item DTOs",
			body: `{
				"Id":"pascal-item-id",
				"Type":"Movie",
				"Key":"root-key-must-remain",
				"UserData":{"Key":"pascal-user-row-key","ItemId":"pascal-user-data-item-id","Nested":{"Key":"nested-user-data-key-must-remain"}},
				"Metadata":{"Key":"nested-key-must-remain"},
				"Items":[{"Id":"nested-item-id","Type":"Episode","Key":"nested-item-root-key-must-remain","UserData":{"Key":"nested-user-row-key","ItemId":"nested-user-data-item-id"}}]
			}`,
			want: map[string]any{
				"Id":   "pascal-item-id",
				"Type": "Movie",
				"Key":  "root-key-must-remain",
				"UserData": map[string]any{
					"Key":    "<user-data-key>",
					"ItemId": "pascal-user-data-item-id",
					"Nested": map[string]any{"Key": "nested-user-data-key-must-remain"},
				},
				"Metadata": map[string]any{"Key": "nested-key-must-remain"},
				"Items": []any{map[string]any{
					"Id":   "nested-item-id",
					"Type": "Episode",
					"Key":  "nested-item-root-key-must-remain",
					"UserData": map[string]any{
						"Key":    "<user-data-key>",
						"ItemId": "nested-user-data-item-id",
					},
				}},
			},
		},
		{
			name: "lower camel case item DTOs",
			body: `{
				"id":"camel-item-id",
				"type":"Episode",
				"key":"root-key-must-remain",
				"userData":{"key":"camel-user-row-key","itemId":"camel-user-data-item-id","nested":{"key":"nested-user-data-key-must-remain"}},
				"metadata":{"key":"nested-key-must-remain"},
				"items":[{"id":"nested-item-id","type":"Movie","key":"nested-item-root-key-must-remain","userData":{"key":"nested-user-row-key","itemId":"nested-user-data-item-id"}}]
			}`,
			want: map[string]any{
				"id":   "camel-item-id",
				"type": "Episode",
				"key":  "root-key-must-remain",
				"userData": map[string]any{
					"key":    "<user-data-key>",
					"itemId": "camel-user-data-item-id",
					"nested": map[string]any{"key": "nested-user-data-key-must-remain"},
				},
				"metadata": map[string]any{"key": "nested-key-must-remain"},
				"items": []any{map[string]any{
					"id":   "nested-item-id",
					"type": "Movie",
					"key":  "nested-item-root-key-must-remain",
					"userData": map[string]any{
						"key":    "<user-data-key>",
						"itemId": "nested-user-data-item-id",
					},
				}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := newNormalizer([]Rule{{Action: "user_data_keys"}}).normalize(Response{
				Status: http.StatusOK,
				Body:   []byte(test.body),
			}, nil)
			if err != nil {
				t.Fatalf("normalize item DTO: %v", err)
			}
			if !reflect.DeepEqual(normalized.Body, test.want) {
				t.Fatalf("normalized body = %#v, want %#v", normalized.Body, test.want)
			}
		})
	}
}

func TestNormalizerPreservesNonStringOrMissingItemUserDataKeys(t *testing.T) {
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "null key",
			body: `{"Id":"item-id","Type":"Movie","UserData":{"Key":null,"ItemId":"item-id"}}`,
			want: map[string]any{"Id": "item-id", "Type": "Movie", "UserData": map[string]any{"Key": nil, "ItemId": "item-id"}},
		},
		{
			name: "non-string key",
			body: `{"id":"item-id","type":"Movie","userData":{"key":true,"itemId":"item-id"}}`,
			want: map[string]any{"id": "item-id", "type": "Movie", "userData": map[string]any{"key": true, "itemId": "item-id"}},
		},
		{
			name: "missing key",
			body: `{"Id":"item-id","Type":"Movie","UserData":{"ItemId":"item-id","Played":false}}`,
			want: map[string]any{"Id": "item-id", "Type": "Movie", "UserData": map[string]any{"ItemId": "item-id", "Played": false}},
		},
		{
			name: "null user data",
			body: `{"id":"item-id","type":"Movie","userData":null,"Key":"root-key-must-remain"}`,
			want: map[string]any{"id": "item-id", "type": "Movie", "userData": nil, "Key": "root-key-must-remain"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := newNormalizer([]Rule{{Action: "user_data_keys"}}).normalize(Response{
				Status: http.StatusOK,
				Body:   []byte(test.body),
			}, nil)
			if err != nil {
				t.Fatalf("normalize item DTO: %v", err)
			}
			if !reflect.DeepEqual(normalized.Body, test.want) {
				t.Fatalf("normalized body = %#v, want %#v", normalized.Body, test.want)
			}
		})
	}
}

func TestNormalizerChangesOnlyPlaySessionIDValuesAndPreservesURLParameterOrder(t *testing.T) {
	normalized, err := newNormalizer([]Rule{{Action: "play_session_ids"}}).normalize(Response{
		Status: http.StatusOK,
		Body: []byte(`{
			"PlaySessionId":"response-session",
			"TranscodingUrl":"/Videos/item/master.m3u8?MediaSourceId=source&PlaySessionId=url-session&VideoCodec=h264&api_key=secret",
			"LiveStreamId":"live-stream-must-remain",
			"Nested":{"playSessionID":"nested-session","Id":"item-must-remain"}
		}`),
	}, nil)
	if err != nil {
		t.Fatalf("normalize playback response: %v", err)
	}
	want := map[string]any{
		"PlaySessionId":  "<play-session-id>",
		"TranscodingUrl": "/Videos/item/master.m3u8?MediaSourceId=source&PlaySessionId=<play-session-id>&VideoCodec=h264&api_key=secret",
		"LiveStreamId":   "live-stream-must-remain",
		"Nested":         map[string]any{"playSessionID": "<play-session-id>", "Id": "item-must-remain"},
	}
	if !reflect.DeepEqual(normalized.Body, want) {
		t.Fatalf("normalized playback body = %#v, want %#v", normalized.Body, want)
	}
}

func TestNormalizerNormalizesPlaybackInfoSessionAndTranscodingTokenWithoutChangingURLOrder(t *testing.T) {
	normalized, err := newNormalizer([]Rule{
		{Action: "play_session_ids"},
		{Action: "transcoding_url_tokens"},
	}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body: []byte(`{
			"PlaySessionId":"550e8400-e29b-41d4-a716-446655440000",
			"MediaSources":[{
				"TranscodingUrl":"/Videos/episode-42/master.m3u8?DeviceId=device-4&PlaySessionId=6ba7b810-9dad-11d1-80b4-00c04fd430c8&ApiKey=opaque-server-token&Tag=version-tag&ApiKeys=near-key-token"
			}],
			"ApiKey":"body-api-key-must-remain",
			"GenericUrl":"/Videos/episode-42/master.m3u8?ApiKey=generic-url-api-key-must-remain"
		}`),
	}, nil)
	if err != nil {
		t.Fatalf("normalize PlaybackInfo response: %v", err)
	}

	want := map[string]any{
		"PlaySessionId": "<play-session-id>",
		"MediaSources": []any{
			map[string]any{
				"TranscodingUrl": "/Videos/episode-42/master.m3u8?DeviceId=device-4&PlaySessionId=<play-session-id>&ApiKey=<transcoding-url-token>&Tag=<entity-tag>&ApiKeys=near-key-token",
			},
		},
		"ApiKey":     "body-api-key-must-remain",
		"GenericUrl": "/Videos/episode-42/master.m3u8?ApiKey=generic-url-api-key-must-remain",
	}
	if !reflect.DeepEqual(normalized.Body, want) {
		t.Fatalf("normalized PlaybackInfo body = %#v, want %#v", normalized.Body, want)
	}
}

func TestNormalizerMapsOnlyMappedPlaybackTranscodingURLItemIDs(t *testing.T) {
	const (
		mappedDashed    = "550e8400-e29b-41d4-a716-446655440000"
		mappedUndashed  = "550e8400e29b41d4a716446655440000"
		unknownDashed   = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
		unknownUndashed = "6ba7b8109dad11d180b400c04fd430c8"
	)

	primaryURL := "/videos/" + mappedDashed + "/master.m3u8?DeviceId=device-4&MediaSourceId=" + mappedUndashed + "&VideoCodec=h264%2Chevc&Other=" + mappedUndashed + "&MediaSourceIds=" + mappedUndashed + "&Tag=prefix-" + mappedUndashed + "-suffix&Encoded=%2Fvideos%2F" + mappedDashed + "%2Fmaster.m3u8"
	unknownURL := "/videos/" + unknownDashed + "/master.m3u8?MediaSourceId=" + unknownUndashed + "&Other=" + mappedUndashed
	nonURLText := "mapped IDs in ordinary text remain exact: " + mappedDashed + " / " + mappedUndashed

	normalized, err := newNormalizer([]Rule{
		{Action: "item_ids"},
		{Action: "transcoding_url_tokens"},
	}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body: []byte(`{
			"MediaSources":[
				{"TranscodingUrl":` + strconv.Quote(primaryURL) + `},
				{"TranscodingUrl":` + strconv.Quote(unknownURL) + `}
			],
			"GenericUrl":` + strconv.Quote(primaryURL) + `,
			"Description":` + strconv.Quote(nonURLText) + `
		}`),
	}, map[string]string{mappedDashed: "canonical-movie"})
	if err != nil {
		t.Fatalf("normalize PlaybackInfo response: %v", err)
	}

	want := map[string]any{
		"MediaSources": []any{
			map[string]any{
				"TranscodingUrl": "/videos/<item:canonical-movie>/master.m3u8?DeviceId=device-4&MediaSourceId=<item:canonical-movie>&VideoCodec=h264%2Chevc&Other=" + mappedUndashed + "&MediaSourceIds=" + mappedUndashed + "&Tag=<entity-tag>&Encoded=%2Fvideos%2F" + mappedDashed + "%2Fmaster.m3u8",
			},
			map[string]any{"TranscodingUrl": unknownURL},
		},
		"GenericUrl":  primaryURL,
		"Description": nonURLText,
	}
	if !reflect.DeepEqual(normalized.Body, want) {
		t.Fatalf("normalized PlaybackInfo body = %#v, want %#v", normalized.Body, want)
	}
}

func TestNormalizerReplacesOnlyNonEmptyTranscodingURLTagValues(t *testing.T) {
	populatedTagURL := "/videos/episode-42/master.m3u8?DeviceId=device-4&Tag=host-local%2Ftag%20value&VideoCodec=h264%2Chevc&Tags=near-key&Other=prefix-Tag%3Dvalue"
	emptyTagURL := "/videos/episode-42/master.m3u8?DeviceId=device-4&Tag=&VideoCodec=h264%2Chevc&Tags=near-key"
	absentTagURL := "/videos/episode-42/master.m3u8?DeviceId=device-4&VideoCodec=h264%2Chevc&Tags=near-key&Other=Tag%3Dvalue"

	normalized, err := newNormalizer([]Rule{{Action: "transcoding_url_tokens"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body: []byte(`{
			"MediaSources":[
				{"TranscodingUrl":` + strconv.Quote(populatedTagURL) + `},
				{"TranscodingUrl":` + strconv.Quote(emptyTagURL) + `},
				{"TranscodingUrl":` + strconv.Quote(absentTagURL) + `}
			],
			"GenericUrl":` + strconv.Quote(populatedTagURL) + `
		}`),
	}, nil)
	if err != nil {
		t.Fatalf("normalize PlaybackInfo response: %v", err)
	}

	want := map[string]any{
		"MediaSources": []any{
			map[string]any{
				"TranscodingUrl": "/videos/episode-42/master.m3u8?DeviceId=device-4&Tag=<entity-tag>&VideoCodec=h264%2Chevc&Tags=near-key&Other=prefix-Tag%3Dvalue",
			},
			map[string]any{"TranscodingUrl": emptyTagURL},
			map[string]any{"TranscodingUrl": absentTagURL},
		},
		"GenericUrl": populatedTagURL,
	}
	if !reflect.DeepEqual(normalized.Body, want) {
		t.Fatalf("normalized PlaybackInfo body = %#v, want %#v", normalized.Body, want)
	}
}

func TestEqualJSONLimitsScanJitterToDeclaredTimestampPaths(t *testing.T) {
	const scanJitter = 120 * time.Second
	tests := []struct {
		name      string
		left      any
		right     any
		wantEqual bool
		wantPath  string
	}{
		{
			name: "declared scan refresh timestamp within jitter",
			left: map[string]any{
				"DateLastRefreshed": "2026-07-26T12:00:00Z",
			},
			right: map[string]any{
				"DateLastRefreshed": "2026-07-26T12:01:30Z",
			},
			wantEqual: true,
			wantPath:  "$",
		},
		{
			name: "PremiereDate remains semantic within scan jitter",
			left: map[string]any{
				"PremiereDate": "2026-07-26T12:00:00Z",
			},
			right: map[string]any{
				"PremiereDate": "2026-07-26T12:01:30Z",
			},
			wantEqual: false,
			wantPath:  "$.PremiereDate",
		},
		{
			name: "DateCreated remains semantic within scan jitter",
			left: map[string]any{
				"DateCreated": "2026-07-26T12:00:00Z",
			},
			right: map[string]any{
				"DateCreated": "2026-07-26T12:01:30Z",
			},
			wantEqual: false,
			wantPath:  "$.DateCreated",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, equal := equalJSON(test.left, test.right, scanJitter, "$")
			if equal != test.wantEqual || path != test.wantPath {
				t.Fatalf("equalJSON = (%q, %t), want (%q, %t)", path, equal, test.wantPath, test.wantEqual)
			}
		})
	}
}

func TestEqualNormalizedDistinguishesJSONScalarTypes(t *testing.T) {
	tests := []struct {
		name       string
		leftBody   string
		rightBody  string
		wantEqual  bool
		wantReason string
	}{
		{
			name:       "boolean and string",
			leftBody:   `{"Item":{"Value":true}}`,
			rightBody:  `{"Item":{"Value":"true"}}`,
			wantEqual:  false,
			wantReason: "JSON differs at $.Item.Value",
		},
		{
			name:       "number and string",
			leftBody:   `{"Item":{"Value":7}}`,
			rightBody:  `{"Item":{"Value":"7"}}`,
			wantEqual:  false,
			wantReason: "JSON differs at $.Item.Value",
		},
		{
			name:       "null and string",
			leftBody:   `{"Item":{"Value":null}}`,
			rightBody:  `{"Item":{"Value":"<nil>"}}`,
			wantEqual:  false,
			wantReason: "JSON differs at $.Item.Value",
		},
		{
			name:      "equal strings",
			leftBody:  `{"Item":{"Value":"seven"}}`,
			rightBody: `{"Item":{"Value":"seven"}}`,
			wantEqual: true,
		},
		{
			name:      "equal booleans",
			leftBody:  `{"Item":{"Value":true}}`,
			rightBody: `{"Item":{"Value":true}}`,
			wantEqual: true,
		},
		{
			name:      "equal numbers",
			leftBody:  `{"Item":{"Value":7}}`,
			rightBody: `{"Item":{"Value":7}}`,
			wantEqual: true,
		},
		{
			name:      "equal nulls",
			leftBody:  `{"Item":{"Value":null}}`,
			rightBody: `{"Item":{"Value":null}}`,
			wantEqual: true,
		},
	}

	normalizer := newNormalizer(nil)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.leftBody)}, nil)
			if err != nil {
				t.Fatalf("normalize left response: %v", err)
			}
			right, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.rightBody)}, nil)
			if err != nil {
				t.Fatalf("normalize right response: %v", err)
			}

			equal, reason := equalNormalized(left, right, 0)
			if equal != test.wantEqual || reason != test.wantReason {
				t.Fatalf("equalNormalized = (%t, %q), want (%t, %q)", equal, reason, test.wantEqual, test.wantReason)
			}
		})
	}
}

func TestDecodeJSONRejectsTrailingBytesAfterDocument(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "another JSON document",
			body: `{"Id":"first"} {"Id":"second"}`,
		},
		{
			name: "non JSON suffix",
			body: `{"Id":"first"} trailing`,
		},
		{
			name: "unmatched closing array delimiter",
			body: `{"Id":"first"}]`,
		},
		{
			name: "unmatched closing object delimiter",
			body: `{"Id":"first"}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, isJSON := decodeJSON([]byte(test.body)); isJSON {
				t.Fatalf("decodeJSON accepted a valid document followed by trailing bytes: %q", test.body)
			}
		})
	}
}

func TestNormalizerNormalizesItemCacheValidatorsAcrossPascalAndCamelFields(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "entity_tags"}})
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "Pascal case fields",
			body: `{
				"ETag":"pascal-entity-cache-tag",
				"ImageTags":{"Primary":"pascal-primary-tag","Logo":"pascal-logo-tag"},
				"BackdropImageTags":["pascal-backdrop-one","pascal-backdrop-two"],
				"ImageBlurHashes":{"Primary":{"pascal-image-cache-tag":"pascal-blurhash"}},
				"ProviderIds":{"Imdb":"tt0111161"}
			}`,
			want: map[string]any{
				"ETag": "<entity-tag>",
				"ImageTags": map[string]any{
					"Primary": "<entity-tag>",
					"Logo":    "<entity-tag>",
				},
				"BackdropImageTags": []any{"<entity-tag>", "<entity-tag>"},
				"ImageBlurHashes": map[string]any{
					"Primary": map[string]any{"<entity-tag>": "pascal-blurhash"},
				},
				"ProviderIds": map[string]any{"Imdb": "tt0111161"},
			},
		},
		{
			name: "lower camel case fields",
			body: `{
				"eTag":"camel-entity-cache-tag",
				"imageTags":{"Primary":"camel-primary-tag","Logo":"camel-logo-tag"},
				"backdropImageTags":["camel-backdrop-one","camel-backdrop-two"],
				"imageBlurHashes":{"Primary":{"camel-image-cache-tag":"camel-blurhash"}},
				"providerIds":{"Imdb":"tt0111161"}
			}`,
			want: map[string]any{
				"eTag": "<entity-tag>",
				"imageTags": map[string]any{
					"Primary": "<entity-tag>",
					"Logo":    "<entity-tag>",
				},
				"backdropImageTags": []any{"<entity-tag>", "<entity-tag>"},
				"imageBlurHashes": map[string]any{
					"Primary": map[string]any{"<entity-tag>": "camel-blurhash"},
				},
				"providerIds": map[string]any{"Imdb": "tt0111161"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.body)}, nil)
			if err != nil {
				t.Fatalf("normalize item response: %v", err)
			}
			if !reflect.DeepEqual(normalized.Body, test.want) {
				t.Fatalf("normalized item response = %#v, want %#v", normalized.Body, test.want)
			}
		})
	}
}

func TestNormalizerPreservesItemCacheValidatorStructureAndBlurhashValues(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "entity_tags"}})
	tests := []struct {
		name       string
		leftBody   string
		rightBody  string
		wantReason string
	}{
		{
			name:       "missing image tag remains different",
			leftBody:   `{"ImageTags":{"Primary":"left-primary-tag"}}`,
			rightBody:  `{"ImageTags":{}}`,
			wantReason: "JSON differs at $.ImageTags",
		},
		{
			name:       "backdrop tag cardinality remains different",
			leftBody:   `{"BackdropImageTags":["left-backdrop-one"]}`,
			rightBody:  `{"BackdropImageTags":["right-backdrop-one","right-backdrop-two"]}`,
			wantReason: "JSON differs at $.BackdropImageTags",
		},
		{
			name:       "missing blurhash cache tag remains different",
			leftBody:   `{"ImageBlurHashes":{"Primary":{"left-image-cache-tag":"shared-blurhash"}}}`,
			rightBody:  `{"ImageBlurHashes":{"Primary":{}}}`,
			wantReason: "JSON differs at $.ImageBlurHashes.Primary",
		},
		{
			name:       "blurhash value remains different",
			leftBody:   `{"ImageBlurHashes":{"Primary":{"left-image-cache-tag":"left-blurhash"}}}`,
			rightBody:  `{"ImageBlurHashes":{"Primary":{"right-image-cache-tag":"right-blurhash"}}}`,
			wantReason: "JSON differs at $.ImageBlurHashes.Primary.<entity-tag>",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.leftBody)}, nil)
			if err != nil {
				t.Fatalf("normalize left item response: %v", err)
			}
			right, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.rightBody)}, nil)
			if err != nil {
				t.Fatalf("normalize right item response: %v", err)
			}
			if equal, reason := equalNormalized(left, right, 0); equal || reason != test.wantReason {
				t.Fatalf("equalNormalized = (%t, %q), want (false, %q)", equal, reason, test.wantReason)
			}
		})
	}
}

func TestNormalizerNormalizesItemDateCreatedOnlyWhenExplicitlyDeclared(t *testing.T) {
	const leftBody = `{"Items":[{"Id":"movie-42","Type":"Movie","DateCreated":"2026-07-27T04:00:49.0000000Z","PremiereDate":"2024-06-01T00:00:00.0000000Z"}]}`
	const rightBody = `{"Items":[{"Id":"movie-42","Type":"Movie","DateCreated":"2026-07-27T04:01:03.0000000Z","PremiereDate":"2024-06-01T00:00:00.0000000Z"}]}`

	tests := []struct {
		name       string
		rules      []Rule
		wantEqual  bool
		wantReason string
	}{
		{
			name:       "entity tags alone leave DateCreated semantic",
			rules:      []Rule{{Action: "entity_tags"}},
			wantEqual:  false,
			wantReason: "JSON differs at $.Items[0].DateCreated",
		},
		{
			name:      "explicit item created dates rule normalizes DateCreated",
			rules:     []Rule{{Action: "item_created_dates"}},
			wantEqual: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalizer := newNormalizer(test.rules)
			left, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(leftBody)}, nil)
			if err != nil {
				t.Fatalf("normalize left item response: %v", err)
			}
			right, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(rightBody)}, nil)
			if err != nil {
				t.Fatalf("normalize right item response: %v", err)
			}
			if equal, reason := equalNormalized(left, right, 0); equal != test.wantEqual || reason != test.wantReason {
				t.Fatalf("equalNormalized = (%t, %q), want (%t, %q)", equal, reason, test.wantEqual, test.wantReason)
			}
		})
	}
}

func TestNormalizerPreservesItemDateCreatedPresenceTypeAndPremiereDate(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "item_created_dates"}})
	tests := []struct {
		name       string
		leftBody   string
		rightBody  string
		wantReason string
	}{
		{
			name:       "missing DateCreated remains different",
			leftBody:   `{"Items":[{"Id":"movie-42","Type":"Movie","DateCreated":"2026-07-27T04:00:49.0000000Z"}]}`,
			rightBody:  `{"Items":[{"Id":"movie-42","Type":"Movie"}]}`,
			wantReason: "JSON differs at $.Items[0]",
		},
		{
			name:       "DateCreated string and null remain different",
			leftBody:   `{"Items":[{"Id":"movie-42","Type":"Movie","DateCreated":"2026-07-27T04:00:49.0000000Z"}]}`,
			rightBody:  `{"Items":[{"Id":"movie-42","Type":"Movie","DateCreated":null}]}`,
			wantReason: "JSON differs at $.Items[0].DateCreated",
		},
		{
			name:       "authored PremiereDate remains different",
			leftBody:   `{"Items":[{"Id":"movie-42","Type":"Movie","DateCreated":"2026-07-27T04:00:49.0000000Z","PremiereDate":"2024-06-01T00:00:00.0000000Z"}]}`,
			rightBody:  `{"Items":[{"Id":"movie-42","Type":"Movie","DateCreated":"2026-07-27T04:01:03.0000000Z","PremiereDate":"2024-06-02T00:00:00.0000000Z"}]}`,
			wantReason: "JSON differs at $.Items[0].PremiereDate",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.leftBody)}, nil)
			if err != nil {
				t.Fatalf("normalize left item response: %v", err)
			}
			right, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.rightBody)}, nil)
			if err != nil {
				t.Fatalf("normalize right item response: %v", err)
			}
			if equal, reason := equalNormalized(left, right, 0); equal || reason != test.wantReason {
				t.Fatalf("equalNormalized = (%t, %q), want (false, %q)", equal, reason, test.wantReason)
			}
		})
	}
}

// Login responses describe identities and activity independently created by
// each server. This rule is deliberately limited to the authenticated user
// and session DTO shapes, in either Jellyfin casing profile.
func TestNormalizerNormalizesOnlyDeclaredLoginSessionVolatiles(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "login_session_volatiles"}})
	tests := []struct {
		name      string
		leftBody  string
		rightBody string
	}{
		{
			name:      "Pascal authentication envelope",
			leftBody:  `{"User":{"Id":"stock-user","Name":"Ada","LastActivityDate":"2026-07-27T04:55:47.2222991Z","LastLoginDate":"2026-07-27T04:55:47.2222991Z","Policy":{}},"SessionInfo":{"Id":"stock-session","UserId":"stock-user","Client":"Web","DeviceId":"stock-device","DeviceName":"Stock Device","LastActivityDate":"2026-07-27T04:55:47.3629011Z","LastLoginDate":"2026-07-27T04:55:47.3629011Z","RemoteEndPoint":"172.18.0.1"}}`,
			rightBody: `{"User":{"Id":"deadfin-user","Name":"Ada","LastActivityDate":"2026-07-27T04:55:48.2222991Z","LastLoginDate":"2026-07-27T04:55:48.2222991Z","Policy":{}},"SessionInfo":{"Id":"deadfin-session","UserId":"deadfin-user","Client":"Web","DeviceId":"stock-device","DeviceName":"Stock Device","LastActivityDate":"2026-07-27T04:55:48.3629011Z","LastLoginDate":"2026-07-27T04:55:48.3629011Z","RemoteEndPoint":"172.17.0.1"}}`,
		},
		{
			name:      "camel authentication envelope",
			leftBody:  `{"user":{"id":"stock-user","name":"Ada","lastActivityDate":"2026-07-27T04:55:47.2222991Z","lastLoginDate":"2026-07-27T04:55:47.2222991Z","policy":{}},"sessionInfo":{"id":"stock-session","userId":"stock-user","client":"Web","deviceId":"stock-device","deviceName":"Stock Device","lastActivityDate":"2026-07-27T04:55:47.3629011Z","lastLoginDate":"2026-07-27T04:55:47.3629011Z","remoteEndPoint":"172.18.0.1"}}`,
			rightBody: `{"user":{"id":"deadfin-user","name":"Ada","lastActivityDate":"2026-07-27T04:55:48.2222991Z","lastLoginDate":"2026-07-27T04:55:48.2222991Z","policy":{}},"sessionInfo":{"id":"deadfin-session","userId":"deadfin-user","client":"Web","deviceId":"stock-device","deviceName":"Stock Device","lastActivityDate":"2026-07-27T04:55:48.3629011Z","lastLoginDate":"2026-07-27T04:55:48.3629011Z","remoteEndPoint":"172.17.0.1"}}`,
		},
		{
			name:      "Pascal current user",
			leftBody:  `{"Id":"stock-user","Name":"Ada","LastActivityDate":"2026-07-27T04:55:47.2222991Z","LastLoginDate":"2026-07-27T04:55:47.2222991Z","Policy":{}}`,
			rightBody: `{"Id":"deadfin-user","Name":"Ada","LastActivityDate":"2026-07-27T04:55:48.2222991Z","LastLoginDate":"2026-07-27T04:55:48.2222991Z","Policy":{}}`,
		},
		{
			name:      "camel current user",
			leftBody:  `{"id":"stock-user","name":"Ada","lastActivityDate":"2026-07-27T04:55:47.2222991Z","lastLoginDate":"2026-07-27T04:55:47.2222991Z","policy":{}}`,
			rightBody: `{"id":"deadfin-user","name":"Ada","lastActivityDate":"2026-07-27T04:55:48.2222991Z","lastLoginDate":"2026-07-27T04:55:48.2222991Z","policy":{}}`,
		},
		{
			name:      "Pascal session listing",
			leftBody:  `[{"Id":"stock-session","UserId":"stock-user","Client":"Web","DeviceId":"stock-device","DeviceName":"Stock Device","LastActivityDate":"2026-07-27T04:55:47.3629011Z","LastLoginDate":"2026-07-27T04:55:47.3629011Z","RemoteEndPoint":"172.18.0.1"}]`,
			rightBody: `[{"Id":"deadfin-session","UserId":"deadfin-user","Client":"Web","DeviceId":"stock-device","DeviceName":"Stock Device","LastActivityDate":"2026-07-27T04:55:48.3629011Z","LastLoginDate":"2026-07-27T04:55:48.3629011Z","RemoteEndPoint":"172.17.0.1"}]`,
		},
		{
			name:      "camel session listing",
			leftBody:  `[{"id":"stock-session","userId":"stock-user","client":"Web","deviceId":"stock-device","deviceName":"Stock Device","lastActivityDate":"2026-07-27T04:55:47.3629011Z","lastLoginDate":"2026-07-27T04:55:47.3629011Z","remoteEndPoint":"172.18.0.1"}]`,
			rightBody: `[{"id":"deadfin-session","userId":"deadfin-user","client":"Web","deviceId":"stock-device","deviceName":"Stock Device","lastActivityDate":"2026-07-27T04:55:48.3629011Z","lastLoginDate":"2026-07-27T04:55:48.3629011Z","remoteEndPoint":"172.17.0.1"}]`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.leftBody)}, nil)
			if err != nil {
				t.Fatalf("normalize left login response: %v", err)
			}
			right, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.rightBody)}, nil)
			if err != nil {
				t.Fatalf("normalize right login response: %v", err)
			}
			if equal, reason := equalNormalized(left, right, 0); !equal {
				t.Fatalf("equalNormalized = (%t, %q), want (true, \"\")", equal, reason)
			}
		})
	}
}

func TestNormalizerPreservesLoginSessionVolatilityStructureAndUnrelatedFields(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "login_session_volatiles"}})
	tests := []struct {
		name       string
		leftBody   string
		rightBody  string
		wantReason string
	}{
		{
			name:       "missing user ID remains different",
			leftBody:   `{"User":{"Id":"stock-user","Name":"Ada","Policy":{}}}`,
			rightBody:  `{"User":{"Name":"Ada","Policy":{}}}`,
			wantReason: "JSON differs at $.User",
		},
		{
			name:       "null activity date remains different",
			leftBody:   `{"user":{"id":"stock-user","name":"Ada","lastActivityDate":"2026-07-27T04:55:47Z","policy":{}}}`,
			rightBody:  `{"user":{"id":"deadfin-user","name":"Ada","lastActivityDate":null,"policy":{}}}`,
			wantReason: "JSON differs at $.user.lastActivityDate",
		},
		{
			name:       "numeric login date remains different",
			leftBody:   `{"User":{"Id":"stock-user","Name":"Ada","LastLoginDate":1,"Policy":{}}}`,
			rightBody:  `{"User":{"Id":"deadfin-user","Name":"Ada","LastLoginDate":2,"Policy":{}}}`,
			wantReason: "JSON differs at $.User.LastLoginDate",
		},
		{
			name:       "invalid activity timestamps remain different",
			leftBody:   `{"SessionInfo":{"Id":"stock-session","UserId":"stock-user","Client":"Web","DeviceId":"stock-device","DeviceName":"Stock Device","LastActivityDate":"not-a-timestamp","RemoteEndPoint":"172.18.0.1"}}`,
			rightBody:  `{"SessionInfo":{"Id":"deadfin-session","UserId":"deadfin-user","Client":"Web","DeviceId":"stock-device","DeviceName":"Stock Device","LastActivityDate":"also-not-a-timestamp","RemoteEndPoint":"172.17.0.1"}}`,
			wantReason: "JSON differs at $.SessionInfo.LastActivityDate",
		},
		{
			name:       "non-string endpoint remains different",
			leftBody:   `[{"id":"stock-session","userId":"stock-user","client":"Web","deviceId":"stock-device","deviceName":"Stock Device","lastActivityDate":"2026-07-27T04:55:47Z","remoteEndPoint":1}]`,
			rightBody:  `[{"id":"deadfin-session","userId":"deadfin-user","client":"Web","deviceId":"stock-device","deviceName":"Stock Device","lastActivityDate":"2026-07-27T04:55:48Z","remoteEndPoint":2}]`,
			wantReason: "JSON differs at $[0].remoteEndPoint",
		},
		{
			name:       "unrelated root ID remains semantic",
			leftBody:   `{"Id":"root-left","Type":"Folder"}`,
			rightBody:  `{"Id":"root-right","Type":"Folder"}`,
			wantReason: "JSON differs at $.Id",
		},
		{
			name:       "unrelated root date remains semantic",
			leftBody:   `{"DateCreated":"2026-07-27T04:55:47Z","Kind":"not-a-login-DTO"}`,
			rightBody:  `{"DateCreated":"2026-07-27T04:55:48Z","Kind":"not-a-login-DTO"}`,
			wantReason: "JSON differs at $.DateCreated",
		},
		{
			name:       "unrelated root endpoint remains semantic",
			leftBody:   `{"RemoteEndPoint":"root-left","Kind":"not-a-login-DTO"}`,
			rightBody:  `{"RemoteEndPoint":"root-right","Kind":"not-a-login-DTO"}`,
			wantReason: "JSON differs at $.RemoteEndPoint",
		},
		{
			name:       "unrelated nested map values remain semantic",
			leftBody:   `{"Metadata":{"Id":"map-left","LastActivityDate":"2026-07-27T04:55:47Z","RemoteEndPoint":"map-left"}}`,
			rightBody:  `{"Metadata":{"Id":"map-right","LastActivityDate":"2026-07-27T04:55:48Z","RemoteEndPoint":"map-right"}}`,
			wantReason: "JSON differs at $.Metadata.Id",
		},
		{
			name:       "unrelated item values remain semantic",
			leftBody:   `{"Items":[{"Id":"item-left","Type":"Movie","DateCreated":"2024-01-01T00:00:00Z","RemoteEndPoint":"item-left"}]}`,
			rightBody:  `{"Items":[{"Id":"item-right","Type":"Movie","DateCreated":"2024-01-02T00:00:00Z","RemoteEndPoint":"item-right"}]}`,
			wantReason: "JSON differs at $.Items[0].DateCreated",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.leftBody)}, nil)
			if err != nil {
				t.Fatalf("normalize left response: %v", err)
			}
			right, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.rightBody)}, nil)
			if err != nil {
				t.Fatalf("normalize right response: %v", err)
			}
			if equal, reason := equalNormalized(left, right, 0); equal || reason != test.wantReason {
				t.Fatalf("equalNormalized = (%t, %q), want (false, %q)", equal, reason, test.wantReason)
			}
		})
	}
}

func TestNormalizerNormalizesMediaSourceIDsByOwnPathAcrossNativeAndCamelPayloads(t *testing.T) {
	tests := []struct {
		name        string
		leftBody    string
		rightBody   string
		leftItemID  string
		rightItemID string
		want        map[string]any
	}{
		{
			name:        "native Pascal payload",
			leftItemID:  "stock-item-1080p",
			rightItemID: "deadfin-item-1080p",
			leftBody: `{
				"Id":"stock-item-1080p","Type":"Movie","Path":"/media/film-1080p.mkv",
				"MediaSources":[
					{"Id":"stock-item-1080p","Path":"/media/film-1080p.mkv"},
					{"Id":"stock-hidden-720p","Path":"/media/film-720p.mkv"},
					{"Id":"opaque-pathless-source","Name":"remote input without a path"}
				],
				"MediaSourceId":"stock-hidden-720p",
				"Nested":{"MediaSourceIds":["stock-item-1080p","stock-hidden-720p","opaque-pathless-source"]}
			}`,
			rightBody: `{
				"Id":"deadfin-item-1080p","Type":"Movie","Path":"/media/film-1080p.mkv",
				"MediaSources":[
					{"Id":"deadfin-item-1080p","Path":"/media/film-1080p.mkv"},
					{"Id":"deadfin-hidden-720p","Path":"/media/film-720p.mkv"},
					{"Id":"opaque-pathless-source","Name":"remote input without a path"}
				],
				"MediaSourceId":"deadfin-hidden-720p",
				"Nested":{"MediaSourceIds":["deadfin-item-1080p","deadfin-hidden-720p","opaque-pathless-source"]}
			}`,
			want: map[string]any{
				"Id":   "<item:Movie:/media/film-1080p.mkv>",
				"Type": "Movie",
				"Path": "/media/film-1080p.mkv",
				"MediaSources": []any{
					map[string]any{"Id": "<item:Movie:/media/film-1080p.mkv>", "Path": "/media/film-1080p.mkv"},
					map[string]any{"Id": "<item:Movie:/media/film-720p.mkv>", "Path": "/media/film-720p.mkv"},
					map[string]any{"Id": "opaque-pathless-source", "Name": "remote input without a path"},
				},
				"MediaSourceId": "<item:Movie:/media/film-720p.mkv>",
				"Nested":        map[string]any{"MediaSourceIds": []any{"<item:Movie:/media/film-1080p.mkv>", "<item:Movie:/media/film-720p.mkv>", "opaque-pathless-source"}},
			},
		},
		{
			name:        "Camel payload",
			leftItemID:  "stock-item-1080p",
			rightItemID: "deadfin-item-1080p",
			leftBody: `{
				"id":"stock-item-1080p","type":"Movie","path":"/media/film-1080p.mkv",
				"mediaSources":[
					{"id":"stock-item-1080p","path":"/media/film-1080p.mkv"},
					{"id":"stock-hidden-720p","path":"/media/film-720p.mkv"},
					{"id":"opaque-pathless-source","name":"remote input without a path"}
				],
				"mediaSourceId":"stock-hidden-720p",
				"nested":{"mediaSourceIds":["stock-item-1080p","stock-hidden-720p","opaque-pathless-source"]}
			}`,
			rightBody: `{
				"id":"deadfin-item-1080p","type":"Movie","path":"/media/film-1080p.mkv",
				"mediaSources":[
					{"id":"deadfin-item-1080p","path":"/media/film-1080p.mkv"},
					{"id":"deadfin-hidden-720p","path":"/media/film-720p.mkv"},
					{"id":"opaque-pathless-source","name":"remote input without a path"}
				],
				"mediaSourceId":"deadfin-hidden-720p",
				"nested":{"mediaSourceIds":["deadfin-item-1080p","deadfin-hidden-720p","opaque-pathless-source"]}
			}`,
			want: map[string]any{
				"id":   "<item:Movie:/media/film-1080p.mkv>",
				"type": "Movie",
				"path": "/media/film-1080p.mkv",
				"mediaSources": []any{
					map[string]any{"id": "<item:Movie:/media/film-1080p.mkv>", "path": "/media/film-1080p.mkv"},
					map[string]any{"id": "<item:Movie:/media/film-720p.mkv>", "path": "/media/film-720p.mkv"},
					map[string]any{"id": "opaque-pathless-source", "name": "remote input without a path"},
				},
				"mediaSourceId": "<item:Movie:/media/film-720p.mkv>",
				"nested":        map[string]any{"mediaSourceIds": []any{"<item:Movie:/media/film-1080p.mkv>", "<item:Movie:/media/film-720p.mkv>", "opaque-pathless-source"}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalizer := newNormalizer([]Rule{{Action: "item_ids"}})
			left, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.leftBody)}, map[string]string{test.leftItemID: "Movie:/media/film-1080p.mkv"})
			if err != nil {
				t.Fatalf("normalize stock response: %v", err)
			}
			right, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(test.rightBody)}, map[string]string{test.rightItemID: "Movie:/media/film-1080p.mkv"})
			if err != nil {
				t.Fatalf("normalize Deadfin response: %v", err)
			}
			if !reflect.DeepEqual(left.Body, test.want) {
				t.Fatalf("normalized stock body = %#v, want %#v", left.Body, test.want)
			}
			if !reflect.DeepEqual(right.Body, test.want) {
				t.Fatalf("normalized Deadfin body = %#v, want %#v", right.Body, test.want)
			}
			if equal, reason := equalNormalized(left, right, 0); !equal {
				t.Fatalf("normalized server payloads differ: %s", reason)
			}
		})
	}
}

func TestNormalizerAvoidsAmbiguousMediaSourceIDCollisions(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "item_ids"}})
	normalized, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(`{
		"Id":"inventory-primary","Type":"Movie","Path":"/media/film-1080p.mkv",
		"MediaSources":[
			{"Id":"ambiguous-source","Path":"/media/film-720p.mkv"},
			{"Id":"ambiguous-source","Path":"/media/film-480p.mkv"},
			{"Id":"duplicate-path-a","Path":"/media/film-360p.mkv"},
			{"Id":"duplicate-path-b","Path":"/media/film-360p.mkv"}
		],
		"MediaSourceId":"ambiguous-source",
		"Nested":{"MediaSourceIds":["ambiguous-source","duplicate-path-a","duplicate-path-b"]}
	}`)}, map[string]string{"inventory-primary": "Movie:/media/film-1080p.mkv"})
	if err != nil {
		t.Fatalf("normalize media source collision payload: %v", err)
	}

	want := map[string]any{
		"Id":   "<item:Movie:/media/film-1080p.mkv>",
		"Type": "Movie",
		"Path": "/media/film-1080p.mkv",
		"MediaSources": []any{
			map[string]any{"Id": "ambiguous-source", "Path": "/media/film-720p.mkv"},
			map[string]any{"Id": "ambiguous-source", "Path": "/media/film-480p.mkv"},
			map[string]any{"Id": "<item:Movie:/media/film-360p.mkv>", "Path": "/media/film-360p.mkv"},
			map[string]any{"Id": "<item:Movie:/media/film-360p.mkv>", "Path": "/media/film-360p.mkv"},
		},
		"MediaSourceId": "ambiguous-source",
		"Nested":        map[string]any{"MediaSourceIds": []any{"ambiguous-source", "<item:Movie:/media/film-360p.mkv>", "<item:Movie:/media/film-360p.mkv>"}},
	}
	if !reflect.DeepEqual(normalized.Body, want) {
		t.Fatalf("normalized collision payload = %#v, want %#v", normalized.Body, want)
	}
}

func TestNormalizerPreservesOpaqueIdentityFieldsWhenMediaSourceIDIsKnown(t *testing.T) {
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "Pascal fields",
			body: `{
				"Type":"Movie",
				"MediaSources":[{"Id":"hidden-source","Path":"/media/film-720p.mkv"}],
				"MediaSourceId":"hidden-source",
				"ServerId":"hidden-source",
				"AccessToken":"hidden-source",
				"Token":"hidden-source"
			}`,
			want: map[string]any{
				"Type":          "Movie",
				"MediaSources":  []any{map[string]any{"Id": "<item:Movie:/media/film-720p.mkv>", "Path": "/media/film-720p.mkv"}},
				"MediaSourceId": "<item:Movie:/media/film-720p.mkv>",
				"ServerId":      "hidden-source",
				"AccessToken":   "hidden-source",
				"Token":         "hidden-source",
			},
		},
		{
			name: "Camel fields",
			body: `{
				"type":"Movie",
				"mediaSources":[{"id":"hidden-source","path":"/media/film-720p.mkv"}],
				"mediaSourceId":"hidden-source",
				"serverId":"hidden-source",
				"accessToken":"hidden-source",
				"token":"hidden-source"
			}`,
			want: map[string]any{
				"type":          "Movie",
				"mediaSources":  []any{map[string]any{"id": "<item:Movie:/media/film-720p.mkv>", "path": "/media/film-720p.mkv"}},
				"mediaSourceId": "<item:Movie:/media/film-720p.mkv>",
				"serverId":      "hidden-source",
				"accessToken":   "hidden-source",
				"token":         "hidden-source",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := newNormalizer([]Rule{{Action: "item_ids"}}).normalize(Response{Status: http.StatusOK, Body: []byte(test.body)}, nil)
			if err != nil {
				t.Fatalf("normalize source identity payload: %v", err)
			}
			if !reflect.DeepEqual(normalized.Body, test.want) {
				t.Fatalf("normalized source identity payload = %#v, want %#v", normalized.Body, test.want)
			}
		})
	}
}

func TestNormalizerEnvironmentFieldsRespectDTOCasingAndJSONTypes(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "environment_fields"}})
	fields := []struct {
		pascal   string
		camel    string
		sentinel string
	}{
		{pascal: "LocalAddress", camel: "localAddress", sentinel: "<environment-local-address>"},
		{pascal: "ServerName", camel: "serverName", sentinel: "<environment-server-name>"},
		{pascal: "ProgramDataPath", camel: "programDataPath", sentinel: "<environment-program-data-path>"},
		{pascal: "WebPath", camel: "webPath", sentinel: "<environment-web-path>"},
		{pascal: "ItemsByNamePath", camel: "itemsByNamePath", sentinel: "<environment-items-by-name-path>"},
		{pascal: "CachePath", camel: "cachePath", sentinel: "<environment-cache-path>"},
		{pascal: "LogPath", camel: "logPath", sentinel: "<environment-log-path>"},
		{pascal: "InternalMetadataPath", camel: "internalMetadataPath", sentinel: "<environment-internal-metadata-path>"},
		{pascal: "TranscodingTempPath", camel: "transcodingTempPath", sentinel: "<environment-transcoding-temp-path>"},
	}
	types := []struct {
		name string
		json string
		want any
	}{
		{name: "string", json: `"/host-specific/value"`, want: "<string-normalized-by-field>"},
		{name: "null", json: `null`, want: nil},
		{name: "boolean", json: `true`, want: true},
		{name: "number", json: `42`, want: json.Number("42")},
		{name: "object", json: `{"nested":"value"}`, want: map[string]any{"nested": "value"}},
		{name: "array", json: `["value",7]`, want: []any{"value", json.Number("7")}},
	}
	const unrelated = "ordinary text and unrelated bytes: PlaySessionId=must-remain"

	for _, field := range fields {
		for _, key := range []string{field.pascal, field.camel} {
			for _, typeCase := range types {
				t.Run(field.pascal+"/"+key+"/"+typeCase.name, func(t *testing.T) {
					want := typeCase.want
					if typeCase.name == "string" {
						want = field.sentinel
					}
					body := `{"` + key + `":` + typeCase.json + `,"Unrelated":` + strconv.Quote(unrelated) + `}`
					normalized, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(body)}, nil)
					if err != nil {
						t.Fatalf("normalize %s %s: %v", key, typeCase.name, err)
					}
					got, ok := normalized.Body.(map[string]any)
					if !ok {
						t.Fatalf("normalized JSON body = %#v, want object", normalized.Body)
					}
					if !reflect.DeepEqual(got[key], want) {
						t.Errorf("normalized %s %s = %#v (%T), want %#v (%T)", key, typeCase.name, got[key], got[key], want, want)
					}
					if got["Unrelated"] != unrelated {
						t.Errorf("unrelated text = %#v, want byte-exact %#v", got["Unrelated"], unrelated)
					}
				})
			}
		}
	}
}

func TestNormalizerPlaySessionIDFieldsRespectCasingAndJSONTypes(t *testing.T) {
	normalizer := newNormalizer([]Rule{{Action: "play_session_ids"}})
	types := []struct {
		name string
		json string
		want any
	}{
		{name: "string", json: `"session-id"`, want: "<play-session-id>"},
		{name: "null", json: `null`, want: nil},
		{name: "boolean", json: `true`, want: true},
		{name: "number", json: `42`, want: json.Number("42")},
		{name: "object", json: `{"nested":"value"}`, want: map[string]any{"nested": "value"}},
		{name: "array", json: `["value",7]`, want: []any{"value", json.Number("7")}},
	}

	for _, key := range []string{"PlaySessionId", "playSessionId"} {
		for _, typeCase := range types {
			t.Run(key+"/"+typeCase.name, func(t *testing.T) {
				body := `{"` + key + `":` + typeCase.json + `,"NotPlaySessionId":"must-remain"}`
				normalized, err := normalizer.normalize(Response{Status: http.StatusOK, Body: []byte(body)}, nil)
				if err != nil {
					t.Fatalf("normalize %s %s: %v", key, typeCase.name, err)
				}
				got, ok := normalized.Body.(map[string]any)
				if !ok {
					t.Fatalf("normalized JSON body = %#v, want object", normalized.Body)
				}
				if !reflect.DeepEqual(got[key], typeCase.want) {
					t.Errorf("normalized %s %s = %#v (%T), want %#v (%T)", key, typeCase.name, got[key], got[key], typeCase.want, typeCase.want)
				}
				if got["NotPlaySessionId"] != "must-remain" {
					t.Errorf("near-miss JSON key = %#v, want exact value", got["NotPlaySessionId"])
				}
			})
		}
	}
}

func TestNormalizerPlaySessionIDNormalizesFieldsAndExactURLQueryParameters(t *testing.T) {
	const placeholder = "<play-session-id>"
	first := "https://media.example.test/stream?PlaySessionId=first-session&Keep=first%20value"
	middle := "https://media.example.test/stream?Before=one&PlaySessionId=middle-session&After=two"
	last := "https://media.example.test/stream?Before=one&PlaySessionId=last-session"
	encoded := "https://media.example.test/stream?PlaySessionId=encoded%2Fsession%3Dtoken&Keep=%2Fexact%20bytes#fragment"
	nearMiss := "https://media.example.test/stream?NotPlaySessionId=must-remain&Keep=exact"
	prose := "prose says PlaySessionId=must-remain, not a URL"
	fragment := "https://media.example.test/stream?Keep=one#PlaySessionId=must-remain"
	malformed := "https://[::1?PlaySessionId=must-remain"

	normalized, err := newNormalizer([]Rule{{Action: "play_session_ids"}}).normalize(Response{
		Status: http.StatusOK,
		Body: []byte(`{
			"PlaySessionId":"dto-session",
			"playSessionId":"camel-dto-session",
			"First":` + strconv.Quote(first) + `,
			"Middle":` + strconv.Quote(middle) + `,
			"Last":` + strconv.Quote(last) + `,
			"Encoded":` + strconv.Quote(encoded) + `,
			"NearMiss":` + strconv.Quote(nearMiss) + `,
			"NotPlaySessionId":"near-miss-session",
			"Prose":` + strconv.Quote(prose) + `,
			"Fragment":` + strconv.Quote(fragment) + `,
			"Malformed":` + strconv.Quote(malformed) + `,
			"PlaySessionId=not-a-query-key":"must-remain"
		}`),
	}, nil)
	if err != nil {
		t.Fatalf("normalize play session response: %v", err)
	}

	want := map[string]any{
		"PlaySessionId":                 placeholder,
		"playSessionId":                 placeholder,
		"First":                         "https://media.example.test/stream?PlaySessionId=" + placeholder + "&Keep=first%20value",
		"Middle":                        "https://media.example.test/stream?Before=one&PlaySessionId=" + placeholder + "&After=two",
		"Last":                          "https://media.example.test/stream?Before=one&PlaySessionId=" + placeholder,
		"Encoded":                       "https://media.example.test/stream?PlaySessionId=" + placeholder + "&Keep=%2Fexact%20bytes#fragment",
		"NearMiss":                      nearMiss,
		"NotPlaySessionId":              "near-miss-session",
		"Prose":                         prose,
		"Fragment":                      fragment,
		"Malformed":                     malformed,
		"PlaySessionId=not-a-query-key": "must-remain",
	}
	if !reflect.DeepEqual(normalized.Body, want) {
		t.Fatalf("normalized play session body = %#v, want %#v", normalized.Body, want)
	}
}

func TestNormalizerDoesNotApplyAuthenticationTokensWithoutExplicitRule(t *testing.T) {
	leftResponse := Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body: []byte(`{
			"AccessToken":"left-root-access-token",
			"accessToken":"left-root-camel-token",
			"Token":"left-root-token",
			"access_token":"left-root-snake-token",
			"SessionInfo":{"AccessToken":"left-nested-token"}
		}`),
	}
	rightResponse := Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body: []byte(`{
			"AccessToken":"right-root-access-token",
			"accessToken":"right-root-camel-token",
			"Token":"left-root-token",
			"access_token":"left-root-snake-token",
			"SessionInfo":{"AccessToken":"left-nested-token"}
		}`),
	}

	withoutAuthTokens := newNormalizer(nil)
	left, err := withoutAuthTokens.normalize(leftResponse, nil)
	if err != nil {
		t.Fatalf("normalize left response without auth token rule: %v", err)
	}
	right, err := withoutAuthTokens.normalize(rightResponse, nil)
	if err != nil {
		t.Fatalf("normalize right response without auth token rule: %v", err)
	}
	if equal, reason := equalNormalized(left, right, 0); equal || reason != "JSON differs at $.AccessToken" {
		t.Fatalf("omitted normalizers compared distinct root access tokens as (%t, %q), want (false, JSON differs at $.AccessToken)", equal, reason)
	}

	withAuthTokens := newNormalizer([]Rule{{Action: "auth_tokens"}})
	left, err = withAuthTokens.normalize(leftResponse, nil)
	if err != nil {
		t.Fatalf("normalize left response with auth token rule: %v", err)
	}
	right, err = withAuthTokens.normalize(rightResponse, nil)
	if err != nil {
		t.Fatalf("normalize right response with auth token rule: %v", err)
	}
	if equal, reason := equalNormalized(left, right, 0); !equal || reason != "" {
		t.Fatalf("explicit auth token normalizer compared authentication responses as (%t, %q), want (true, \"\")", equal, reason)
	}
}

func TestNormalizerAuthenticationTokensPreservesNonStringAndNonRootFields(t *testing.T) {
	normalized, err := newNormalizer([]Rule{{Action: "auth_tokens"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body: []byte(`{
			"AccessToken":17,
			"accessToken":["array-token"],
			"Token":"root-token",
			"access_token":"root-snake-token",
			"SessionInfo":{"AccessToken":"nested-access-token","accessToken":"nested-camel-token"}
		}`),
	}, nil)
	if err != nil {
		t.Fatalf("normalize authentication token types: %v", err)
	}
	want := map[string]any{
		"AccessToken":  json.Number("17"),
		"accessToken":  []any{"array-token"},
		"Token":        "root-token",
		"access_token": "root-snake-token",
		"SessionInfo": map[string]any{
			"AccessToken": "nested-access-token",
			"accessToken": "nested-camel-token",
		},
	}
	if !reflect.DeepEqual(normalized.Body, want) {
		t.Fatalf("auth token normalizer changed fields outside its exact root string scope: %#v, want %#v", normalized.Body, want)
	}
}

func TestNormalizerNormalizesOnlyMappedSubtitleDeliveryURLPathIDs(t *testing.T) {
	const (
		itemID   = "550e8400-e29b-41d4-a716-446655440000"
		sourceID = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	)

	knownURL := "https://media.example.invalid/Videos/" + itemID + "/" + sourceID + "/Subtitles/2/0/Stream.srt?ApiKey=opaque-token&ItemId=" + itemID + "&MediaSourceId=" + sourceID + "&ApiKeys=near-key-token&Opaque=a%2Fb%20c&StartPositionTicks=0#subtitle-fragment"
	wantKnownURL := "https://media.example.invalid/Videos/<item:movie:/library/subtitle-sample.mkv>/<item:source:/library/subtitle-sample.mkv>/Subtitles/2/0/Stream.srt?ApiKey=<transcoding-url-token>&ItemId=" + itemID + "&MediaSourceId=" + sourceID + "&ApiKeys=near-key-token&Opaque=a%2Fb%20c&StartPositionTicks=0#subtitle-fragment"
	payload := map[string]any{
		"PlaySessionId": "play-session-must-remain",
		"MediaSources": []any{map[string]any{
			"MediaStreams": []any{
				map[string]any{"Type": "Subtitle", "DeliveryUrl": knownURL},
				map[string]any{"Type": "Video", "DeliveryUrl": knownURL},
				map[string]any{"Type": "Audio", "DeliveryUrl": knownURL},
				map[string]any{"Type": "Subtitle", "DeliveryUrl": nil},
				map[string]any{"Type": "Subtitle", "DeliveryUrl": false},
				map[string]any{"Type": "Subtitle", "DeliveryUrl": 42},
				map[string]any{"Type": true, "DeliveryUrl": knownURL},
			},
		}},
		"DeliveryUrl": knownURL,
		"Nested": map[string]any{
			"MediaSources": []any{map[string]any{
				"MediaStreams": []any{map[string]any{"Type": "Subtitle", "DeliveryUrl": knownURL}},
			}},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal PlaybackInfo payload: %v", err)
	}

	normalized, err := newNormalizer([]Rule{{Name: "subtitle-delivery-url-item-ids", Action: "transcoding_url_tokens"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    body,
	}, map[string]string{
		itemID:   "movie:/library/subtitle-sample.mkv",
		sourceID: "source:/library/subtitle-sample.mkv",
	})
	if err != nil {
		t.Fatalf("normalize PlaybackInfo response: %v", err)
	}

	want := map[string]any{
		"PlaySessionId": "play-session-must-remain",
		"MediaSources": []any{map[string]any{
			"MediaStreams": []any{
				map[string]any{"Type": "Subtitle", "DeliveryUrl": wantKnownURL},
				map[string]any{"Type": "Video", "DeliveryUrl": knownURL},
				map[string]any{"Type": "Audio", "DeliveryUrl": knownURL},
				map[string]any{"Type": "Subtitle", "DeliveryUrl": nil},
				map[string]any{"Type": "Subtitle", "DeliveryUrl": false},
				map[string]any{"Type": "Subtitle", "DeliveryUrl": json.Number("42")},
				map[string]any{"Type": true, "DeliveryUrl": knownURL},
			},
		}},
		"DeliveryUrl": knownURL,
		"Nested": map[string]any{
			"MediaSources": []any{map[string]any{
				"MediaStreams": []any{map[string]any{"Type": "Subtitle", "DeliveryUrl": knownURL}},
			}},
		},
	}
	if !reflect.DeepEqual(normalized.Body, want) {
		t.Fatalf("normalized PlaybackInfo body = %#v, want %#v", normalized.Body, want)
	}
}

func TestNormalizerSubtitleDeliveryURLKeepsEncodedAPIKeyNormalizedInLiveRuleOrder(t *testing.T) {
	const (
		itemID   = "ad08623a-9ec5-8acc-2d55-41518d90a065"
		sourceID = "ad08623a9ec58acc2d5541518d90a065"
	)
	deliveryURL := "/Videos/" + itemID + "/" + sourceID + "/Subtitles/0/0/Stream.srt?ApiKey=eyJhbGciOiJIUzI1NiJ9%2Fpayload%3D&Opaque=a%2Fb"
	body, err := json.Marshal(map[string]any{
		"PlaySessionId": "live-post-session",
		"MediaSources": []any{map[string]any{
			"MediaStreams": []any{map[string]any{"Type": "Subtitle", "DeliveryUrl": deliveryURL}},
		}},
	})
	if err != nil {
		t.Fatalf("marshal PlaybackInfo payload: %v", err)
	}

	normalized, err := newNormalizer([]Rule{
		{Action: "item_ids"},
		{Action: "entity_tags"},
		{Action: "play_session_ids"},
		{Action: "transcoding_url_tokens"},
		{Name: "subtitle-delivery-url-item-ids", Action: "transcoding_url_tokens"},
	}).normalize(Response{Status: http.StatusOK, Body: body}, map[string]string{
		itemID:   "Movie:/media/movies/Subtitle Sample (2024)/Subtitle Sample (2024).mkv",
		sourceID: "Movie:/media/movies/Subtitle Sample (2024)/Subtitle Sample (2024).mkv",
	})
	if err != nil {
		t.Fatalf("normalize PlaybackInfo response in live rule order: %v", err)
	}

	payload, ok := normalized.Body.(map[string]any)
	if !ok {
		t.Fatalf("normalized PlaybackInfo body = %#v, want object", normalized.Body)
	}
	sources := payload["MediaSources"].([]any)
	streams := sources[0].(map[string]any)["MediaStreams"].([]any)
	got, ok := streams[0].(map[string]any)["DeliveryUrl"].(string)
	if !ok {
		t.Fatalf("normalized subtitle DeliveryUrl = %#v, want string", streams[0].(map[string]any)["DeliveryUrl"])
	}
	want := "/Videos/<item:Movie:/media/movies/Subtitle Sample (2024)/Subtitle Sample (2024).mkv>/<item:Movie:/media/movies/Subtitle Sample (2024)/Subtitle Sample (2024).mkv>/Subtitles/0/0/Stream.srt?ApiKey=<transcoding-url-token>&Opaque=a%2Fb"
	if got != want {
		t.Fatalf("normalized subtitle DeliveryUrl = %q, want %q", got, want)
	}
}

func TestNormalizerDoesNotBroadenMalformedSubtitleDeliveryURLNormalization(t *testing.T) {
	const (
		itemID   = "550e8400-e29b-41d4-a716-446655440000"
		sourceID = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	)
	malformedURL := "/Videos/" + itemID + "/" + sourceID + "/Subtitles/2/0/Stream.srt?Opaque=%zz&ItemId=" + itemID
	body, err := json.Marshal(map[string]any{
		"PlaySessionId": "play-session-must-remain",
		"MediaSources": []any{map[string]any{
			"MediaStreams": []any{map[string]any{"Type": "Subtitle", "DeliveryUrl": malformedURL}},
		}},
	})
	if err != nil {
		t.Fatalf("marshal malformed PlaybackInfo payload: %v", err)
	}

	normalized, err := newNormalizer([]Rule{{Name: "subtitle-delivery-url-item-ids", Action: "transcoding_url_tokens"}}).normalize(Response{Status: http.StatusOK, Body: body}, map[string]string{
		itemID:   "movie:/library/subtitle-sample.mkv",
		sourceID: "source:/library/subtitle-sample.mkv",
	})
	if err != nil {
		return // Malformed URLs may be rejected, but must never be globally normalized.
	}

	want := map[string]any{
		"PlaySessionId": "play-session-must-remain",
		"MediaSources": []any{map[string]any{
			"MediaStreams": []any{map[string]any{"Type": "Subtitle", "DeliveryUrl": malformedURL}},
		}},
	}
	if !reflect.DeepEqual(normalized.Body, want) {
		t.Fatalf("malformed subtitle DeliveryUrl broadened normalization: %#v, want %#v", normalized.Body, want)
	}
}

func TestNormalizerDoesNotBroadenUnknownSubtitleDeliveryURLNormalization(t *testing.T) {
	const unknownID = "123e4567-e89b-12d3-a456-426614174000"
	unknownURL := "/Videos/" + unknownID + "/" + unknownID + "/Subtitles/2/0/Stream.srt?ApiKey=opaque-token&ItemId=" + unknownID
	body, err := json.Marshal(map[string]any{
		"PlaySessionId": "play-session-must-remain",
		"MediaSources": []any{map[string]any{
			"MediaStreams": []any{map[string]any{"Type": "Subtitle", "DeliveryUrl": unknownURL}},
		}},
	})
	if err != nil {
		t.Fatalf("marshal unknown PlaybackInfo payload: %v", err)
	}

	normalized, err := newNormalizer([]Rule{{Name: "subtitle-delivery-url-item-ids", Action: "transcoding_url_tokens"}}).normalize(Response{Status: http.StatusOK, Body: body}, nil)
	if err != nil {
		return // Unknown IDs may be rejected, but must never be globally normalized.
	}

	want := map[string]any{
		"PlaySessionId": "play-session-must-remain",
		"MediaSources": []any{map[string]any{
			"MediaStreams": []any{map[string]any{"Type": "Subtitle", "DeliveryUrl": unknownURL}},
		}},
	}
	if !reflect.DeepEqual(normalized.Body, want) {
		t.Fatalf("unknown subtitle DeliveryUrl broadened normalization: %#v, want %#v", normalized.Body, want)
	}
}

func TestSubtitleDeliveryURLItemIDsRuleIsExplicitOnly(t *testing.T) {
	rules, err := loadRules("normalize")
	if err != nil {
		t.Fatalf("load normalizer rules: %v", err)
	}

	var subtitleRule Rule
	found := false
	for _, rule := range rules {
		if rule.Name == "subtitle-delivery-url-item-ids" {
			subtitleRule = rule
			found = true
			break
		}
	}
	if !found {
		t.Fatal("subtitle-delivery-url-item-ids normalizer rule is missing")
	}
	if subtitleRule.Action != "transcoding_url_tokens" || !subtitleRule.ExplicitOnly {
		t.Fatalf("subtitle-delivery-url-item-ids rule = %#v, want explicit-only transcoding_url_tokens action", subtitleRule)
	}

	implicit, err := selectRules(rules, nil)
	if err != nil {
		t.Fatalf("select implicit normalizers: %v", err)
	}
	for _, rule := range implicit {
		if rule.Name == subtitleRule.Name {
			t.Fatal("subtitle-delivery-url-item-ids must not be selected when normalizers are omitted")
		}
	}

	explicit, err := selectRules(rules, []string{subtitleRule.Name})
	if err != nil {
		t.Fatalf("select explicit subtitle DeliveryUrl normalizer: %v", err)
	}
	if len(explicit) != 1 || explicit[0].Name != subtitleRule.Name {
		t.Fatalf("explicit subtitle DeliveryUrl selection = %#v, want only %#v", explicit, subtitleRule.Name)
	}
}

func TestNormalizerNormalizesOnlySemanticUserViewIdentityFieldsAcrossCasing(t *testing.T) {
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "Pascal",
			body: `{"Id":"root-item-id","Items":[
				{"Id":"left-movies","Name":"Golden Movies","CollectionType":"Movies","Type":"CollectionFolder","DisplayPreferencesId":"left-movies","ParentId":"left-root","UserData":{"ItemId":"left-movies","Key":"left-key"},"Nested":{"Id":"nested-id","DisplayPreferencesId":"nested-display","UserData":{"ItemId":"nested-user-item"},"ParentId":"nested-parent"},"MediaSourceId":"left-source"},
				{"Id":"left-tv","Name":"Golden Movies","CollectionType":"TVShows","Type":"CollectionFolder","DisplayPreferencesId":"left-tv","ParentId":"left-root","UserData":{"ItemId":"left-tv","Key":"left-tv-key"}}
			],"Nested":{"Id":"root-nested-id","DisplayPreferencesId":"root-nested-display","UserData":{"ItemId":"root-nested-user-item"},"ParentId":"root-nested-parent"}}`,
			want: map[string]any{
				"Id": "root-item-id",
				"Items": []any{
					map[string]any{"Id": "<user-view-id:golden movies|movies>", "Name": "Golden Movies", "CollectionType": "Movies", "Type": "CollectionFolder", "DisplayPreferencesId": "<user-view-id:golden movies|movies>", "ParentId": "<user-views-parent-id>", "UserData": map[string]any{"ItemId": "<user-view-id:golden movies|movies>", "Key": "left-key"}, "Nested": map[string]any{"Id": "nested-id", "DisplayPreferencesId": "nested-display", "UserData": map[string]any{"ItemId": "nested-user-item"}, "ParentId": "nested-parent"}, "MediaSourceId": "left-source"},
					map[string]any{"Id": "<user-view-id:golden movies|tvshows>", "Name": "Golden Movies", "CollectionType": "TVShows", "Type": "CollectionFolder", "DisplayPreferencesId": "<user-view-id:golden movies|tvshows>", "ParentId": "<user-views-parent-id>", "UserData": map[string]any{"ItemId": "<user-view-id:golden movies|tvshows>", "Key": "left-tv-key"}},
				},
				"Nested": map[string]any{"Id": "root-nested-id", "DisplayPreferencesId": "root-nested-display", "UserData": map[string]any{"ItemId": "root-nested-user-item"}, "ParentId": "root-nested-parent"},
			},
		},
		{
			name: "Camel",
			body: `{"id":"root-item-id","items":[
				{"id":"right-movies","name":"GOLDEN MOVIES","collectionType":"movies","type":"CollectionFolder","displayPreferencesId":"right-movies","parentId":"right-root","userData":{"itemId":"right-movies","key":"right-key"},"nested":{"id":"nested-id","displayPreferencesId":"nested-display","userData":{"itemId":"nested-user-item"},"parentId":"nested-parent"},"mediaSourceId":"right-source"},
				{"id":"right-tv","name":"golden movies","collectionType":"tvshows","type":"CollectionFolder","displayPreferencesId":"right-tv","parentId":"right-root","userData":{"itemId":"right-tv","key":"right-tv-key"}}
			],"nested":{"id":"root-nested-id","displayPreferencesId":"root-nested-display","userData":{"itemId":"root-nested-user-item"},"parentId":"root-nested-parent"}}`,
			want: map[string]any{
				"id": "root-item-id",
				"items": []any{
					map[string]any{"id": "<user-view-id:golden movies|movies>", "name": "GOLDEN MOVIES", "collectionType": "movies", "type": "CollectionFolder", "displayPreferencesId": "<user-view-id:golden movies|movies>", "parentId": "<user-views-parent-id>", "userData": map[string]any{"itemId": "<user-view-id:golden movies|movies>", "key": "right-key"}, "nested": map[string]any{"id": "nested-id", "displayPreferencesId": "nested-display", "userData": map[string]any{"itemId": "nested-user-item"}, "parentId": "nested-parent"}, "mediaSourceId": "right-source"},
					map[string]any{"id": "<user-view-id:golden movies|tvshows>", "name": "golden movies", "collectionType": "tvshows", "type": "CollectionFolder", "displayPreferencesId": "<user-view-id:golden movies|tvshows>", "parentId": "<user-views-parent-id>", "userData": map[string]any{"itemId": "<user-view-id:golden movies|tvshows>", "key": "right-tv-key"}},
				},
				"nested": map[string]any{"id": "root-nested-id", "displayPreferencesId": "root-nested-display", "userData": map[string]any{"itemId": "root-nested-user-item"}, "parentId": "root-nested-parent"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := newNormalizer([]Rule{{Name: "user-view-ids", Action: "user_view_ids"}}).normalize(Response{Status: http.StatusOK, Body: []byte(test.body)}, nil)
			if err != nil {
				t.Fatalf("normalize UserViews response: %v", err)
			}
			if !reflect.DeepEqual(normalized.Body, test.want) {
				t.Fatalf("normalized UserViews = %#v, want %#v", normalized.Body, test.want)
			}
		})
	}
}

func TestNormalizerLeavesNonAggregateUserViewParentsAndNestedGroupingIDsExact(t *testing.T) {
	userViews, err := newNormalizer([]Rule{{Name: "user-view-ids", Action: "user_view_ids"}}).normalize(Response{Status: http.StatusOK, Body: []byte(`{"Items":[{"Id":"view-one","Name":"Golden Movies","CollectionType":"movies","ParentId":"root-one"},{"Id":"view-two","Name":"Golden TV","CollectionType":"tvshows"}]}`)}, nil)
	if err != nil {
		t.Fatalf("normalize one-sided UserViews parent: %v", err)
	}
	views := userViews.Body.(map[string]any)["Items"].([]any)
	if got := views[0].(map[string]any)["ParentId"]; got != "root-one" {
		t.Fatalf("one-sided UserViews ParentId = %#v, want exact root identity", got)
	}

	oneSided, err := newNormalizer([]Rule{{Name: "user-view-ids", Action: "user_view_ids"}}).normalize(Response{Status: http.StatusOK, Body: []byte(`{"Items":[{"Id":"one-sided-item-id","Type":"Movie","DisplayPreferencesId":"one-sided-display","UserData":{"ItemId":"one-sided-user-item"},"ParentId":"one-sided-parent"}]}`)}, nil)
	if err != nil {
		t.Fatalf("normalize one-sided UserViews item: %v", err)
	}
	wantOneSided := map[string]any{"Items": []any{map[string]any{"Id": "one-sided-item-id", "Type": "Movie", "DisplayPreferencesId": "one-sided-display", "UserData": map[string]any{"ItemId": "one-sided-user-item"}, "ParentId": "one-sided-parent"}}}
	if !reflect.DeepEqual(oneSided.Body, wantOneSided) {
		t.Fatalf("one-sided UserViews item identities were normalized: %#v, want %#v", oneSided.Body, wantOneSided)
	}

	grouping, err := newNormalizer([]Rule{{Name: "user-view-ids", Action: "user_view_ids"}}).normalize(Response{Status: http.StatusOK, Body: []byte(`[{"Id":"left-movies","Name":"Golden Movies","Nested":{"Id":"nested-item-id","DisplayPreferencesId":"nested-display","UserData":{"ItemId":"nested-user-item"},"ParentId":"nested-parent"}}]`)}, nil)
	if err != nil {
		t.Fatalf("normalize GroupingOptions response: %v", err)
	}
	wantGrouping := []any{map[string]any{"Id": "<user-view-id:golden movies>", "Name": "Golden Movies", "Nested": map[string]any{"Id": "nested-item-id", "DisplayPreferencesId": "nested-display", "UserData": map[string]any{"ItemId": "nested-user-item"}, "ParentId": "nested-parent"}}}
	if !reflect.DeepEqual(grouping.Body, wantGrouping) {
		t.Fatalf("normalized GroupingOptions = %#v, want only root semantic view IDs normalized %#v", grouping.Body, wantGrouping)
	}

	arbitrary, err := newNormalizer([]Rule{{Name: "user-view-ids", Action: "user_view_ids"}}).normalize(Response{Status: http.StatusOK, Body: []byte(`[{"Id":"arbitrary-item-id","Type":"Movie","DisplayPreferencesId":"arbitrary-display","UserData":{"ItemId":"arbitrary-user-item"},"ParentId":"arbitrary-parent"}]`)}, nil)
	if err != nil {
		t.Fatalf("normalize arbitrary root array: %v", err)
	}
	wantArbitrary := []any{map[string]any{"Id": "arbitrary-item-id", "Type": "Movie", "DisplayPreferencesId": "arbitrary-display", "UserData": map[string]any{"ItemId": "arbitrary-user-item"}, "ParentId": "arbitrary-parent"}}
	if !reflect.DeepEqual(arbitrary.Body, wantArbitrary) {
		t.Fatalf("arbitrary root item identities were normalized: %#v, want %#v", arbitrary.Body, wantArbitrary)
	}
}

func TestUserViewIDsRuleIsExplicitAndSelectedOnlyByLegacyViewSteps(t *testing.T) {
	rules, err := loadRules("normalize")
	if err != nil {
		t.Fatalf("load normalizer rules: %v", err)
	}

	var userViewRule Rule
	found := false
	for _, rule := range rules {
		if rule.Name == "user-view-ids" {
			userViewRule = rule
			found = true
			break
		}
	}
	if !found {
		t.Fatal("user-view-ids normalizer rule is missing")
	}
	if userViewRule.Action != "user_view_ids" || !userViewRule.ExplicitOnly {
		t.Fatalf("user-view-ids rule = %#v, want explicit-only user_view_ids action", userViewRule)
	}
	implicit, err := selectRules(rules, nil)
	if err != nil {
		t.Fatalf("select implicit normalizers: %v", err)
	}
	for _, rule := range implicit {
		if rule.Name == userViewRule.Name {
			t.Fatal("user-view-ids must not be selected when normalizers are omitted")
		}
	}

	steps := legacyItemsCorpusSteps(t)
	var selected []string
	for _, step := range steps {
		for _, normalizer := range step.selectedRules() {
			if normalizer == userViewRule.Name {
				selected = append(selected, step.Name)
			}
		}
	}
	want := []string{"legacy user views", "legacy user grouping options"}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("legacy corpus user-view-ids selections = %#v, want exactly the two legacy view steps %#v", selected, want)
	}
}

func TestNormalizeExternalIdInfosRemovesUnportedProviders(t *testing.T) {
	body := `[
		{"Name":"IMDb","Key":"Imdb"},
		{"Name":"TheMovieDb","Key":"Tmdb","Type":"Movie"},
		{"Name":"Apple iTunes","Key":"Apple","Type":"Movie"},
		{"Name":"MusicBrainz","Key":"MusicBrainzAlbum","Type":"Album"},
		{"Name":"TheAudioDb","Key":"AudioDbAlbum"},
		{"Name":"Fanart","Key":"Fanart","Type":"Movie"}
	]`
	normalized, err := newNormalizer([]Rule{{Name: "external-id-infos", Action: "external_id_infos"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(body),
	}, nil)
	if err != nil {
		t.Fatalf("normalize ExternalIdInfos: %v", err)
	}
	array, ok := normalized.Body.([]any)
	if !ok {
		t.Fatalf("normalized body = %#v, want array", normalized.Body)
	}
	if len(array) != 4 {
		t.Fatalf("normalized ExternalIdInfos count = %d, want 4 (Imdb, Tmdb, MusicBrainzAlbum, AudioDbAlbum)", len(array))
	}
	wantKeys := []string{"Imdb", "Tmdb", "MusicBrainzAlbum", "AudioDbAlbum"}
	for i, want := range wantKeys {
		entry, ok := array[i].(map[string]any)
		if !ok {
			t.Fatalf("entry %d = %#v, want object", i, array[i])
		}
		if got := entry["Key"]; got != want {
			t.Fatalf("entry %d Key = %#v, want %#v", i, got, want)
		}
	}
}

func TestNormalizeExternalIdInfosPreservesEntriesByteStrict(t *testing.T) {
	body := `[
		{"Name":"TheMovieDb","Key":"Tmdb","Type":"Movie"},
		{"Name":"IMDb","Key":"Imdb"}
	]`
	normalized, err := newNormalizer([]Rule{{Name: "external-id-infos", Action: "external_id_infos"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(body),
	}, nil)
	if err != nil {
		t.Fatalf("normalize ExternalIdInfos: %v", err)
	}
	array := normalized.Body.([]any)
	first := array[0].(map[string]any)
	if got := first["Type"]; got != "Movie" {
		t.Fatalf("Type changed: %#v, want %#v", got, "Movie")
	}
	if got := first["Name"]; got != "TheMovieDb" {
		t.Fatalf("Name changed: %#v, want %#v", got, "TheMovieDb")
	}
}

func TestNormalizeExternalIdInfosLeavesNonArrayRootIntact(t *testing.T) {
	body := `{"Name":"TheMovieDb","Key":"Tmdb","Type":"Movie"}`
	normalized, err := newNormalizer([]Rule{{Name: "external-id-infos", Action: "external_id_infos"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(body),
	}, nil)
	if err != nil {
		t.Fatalf("normalize ExternalIdInfos: %v", err)
	}
	object, ok := normalized.Body.(map[string]any)
	if !ok {
		t.Fatalf("normalized body = %#v, want object", normalized.Body)
	}
	if got := object["Key"]; got != "Tmdb" {
		t.Fatalf("non-array root was modified: Key = %#v, want %#v", got, "Tmdb")
	}
}

func TestNormalizeExternalIdInfosLeavesNonObjectEntriesIntact(t *testing.T) {
	body := `["not-an-object", {"Name":"IMDb","Key":"Imdb"}]`
	normalized, err := newNormalizer([]Rule{{Name: "external-id-infos", Action: "external_id_infos"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(body),
	}, nil)
	if err != nil {
		t.Fatalf("normalize ExternalIdInfos: %v", err)
	}
	array := normalized.Body.([]any)
	if got := array[0]; got != "not-an-object" {
		t.Fatalf("non-object entry was modified: %#v, want %#v", got, "not-an-object")
	}
	if len(array) != 2 {
		t.Fatalf("array length changed: %d, want 2", len(array))
	}
}

func TestNormalizeRemoteSearchResultOrderSortsByProviderIds(t *testing.T) {
	body := `[
		{"Name":"Result B","ProviderIds":{"Tmdb":"456"}},
		{"Name":"Result A","ProviderIds":{"Tmdb":"123"}},
		{"Name":"Result C","ProviderIds":{"Imdb":"tt789"}}
	]`
	normalized, err := newNormalizer([]Rule{{Name: "remote-search-result-order", Action: "remote_search_result_order"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(body),
	}, nil)
	if err != nil {
		t.Fatalf("normalize RemoteSearchResult: %v", err)
	}
	array := normalized.Body.([]any)
	if len(array) != 3 {
		t.Fatalf("result count changed: %d, want 3", len(array))
	}
	wantNames := []string{"Result C", "Result A", "Result B"}
	for i, want := range wantNames {
		entry := array[i].(map[string]any)
		if got := entry["Name"]; got != want {
			t.Fatalf("entry %d Name = %#v, want %#v", i, got, want)
		}
	}
}

func TestNormalizeRemoteSearchResultOrderSortsByMultipleProviderIds(t *testing.T) {
	body := `[
		{"Name":"Result A","ProviderIds":{"MusicBrainzAlbum":"b","Tmdb":"2"}},
		{"Name":"Result B","ProviderIds":{"Tmdb":"1"}}
	]`
	normalized, err := newNormalizer([]Rule{{Name: "remote-search-result-order", Action: "remote_search_result_order"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(body),
	}, nil)
	if err != nil {
		t.Fatalf("normalize RemoteSearchResult: %v", err)
	}
	array := normalized.Body.([]any)
	if len(array) != 2 {
		t.Fatalf("result count changed: %d, want 2", len(array))
	}
	// Result A key: "musicbrainzalbum=b|tmdb=2"; Result B key: "tmdb=1"
	// "musicbrainzalbum=b|tmdb=2" sorts before "tmdb=1" because m < t
	first := array[0].(map[string]any)
	if got := first["Name"]; got != "Result A" {
		t.Fatalf("first entry Name = %#v, want Result A (musicbrainzalbum=b|tmdb=2 sorts before tmdb=1)", got)
	}
}

func TestNormalizeRemoteSearchResultOrderFallsBackToNameYear(t *testing.T) {
	body := `[
		{"Name":"Zebra","ProductionYear":2020},
		{"Name":"Alpha","ProductionYear":2019}
	]`
	normalized, err := newNormalizer([]Rule{{Name: "remote-search-result-order", Action: "remote_search_result_order"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(body),
	}, nil)
	if err != nil {
		t.Fatalf("normalize RemoteSearchResult: %v", err)
	}
	array := normalized.Body.([]any)
	first := array[0].(map[string]any)
	if got := first["Name"]; got != "Alpha" {
		t.Fatalf("first entry Name = %#v, want Alpha (Name|Year fallback sort)", got)
	}
}

func TestNormalizeRemoteSearchResultOrderIsStable(t *testing.T) {
	body := `[
		{"Name":"Same","ProviderIds":{"Tmdb":"1"},"Data":"first"},
		{"Name":"Same","ProviderIds":{"Tmdb":"1"},"Data":"second"},
		{"Name":"Same","ProviderIds":{"Tmdb":"1"},"Data":"third"}
	]`
	normalized, err := newNormalizer([]Rule{{Name: "remote-search-result-order", Action: "remote_search_result_order"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(body),
	}, nil)
	if err != nil {
		t.Fatalf("normalize RemoteSearchResult: %v", err)
	}
	array := normalized.Body.([]any)
	want := []string{"first", "second", "third"}
	for i, wantData := range want {
		entry := array[i].(map[string]any)
		if got := entry["Data"]; got != wantData {
			t.Fatalf("stable sort broke equal-key order at %d: %#v, want %#v", i, got, wantData)
		}
	}
}

func TestNormalizeRemoteSearchResultOrderLeavesNonArrayRootIntact(t *testing.T) {
	body := `{"Name":"Result","ProviderIds":{"Tmdb":"1"}}`
	normalized, err := newNormalizer([]Rule{{Name: "remote-search-result-order", Action: "remote_search_result_order"}}).normalize(Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(body),
	}, nil)
	if err != nil {
		t.Fatalf("normalize RemoteSearchResult: %v", err)
	}
	object, ok := normalized.Body.(map[string]any)
	if !ok {
		t.Fatalf("normalized body = %#v, want object", normalized.Body)
	}
	if got := object["Name"]; got != "Result" {
		t.Fatalf("non-array root was modified: Name = %#v, want %#v", got, "Result")
	}
}

func TestItemLookupRulesLoadAndValidate(t *testing.T) {
	rules, err := loadRules("normalize")
	if err != nil {
		t.Fatalf("loadRules: %v", err)
	}
	var externalIDInfos, remoteSearchOrder *Rule
	for i := range rules {
		switch rules[i].Name {
		case "external-id-infos":
			externalIDInfos = &rules[i]
		case "remote-search-result-order":
			remoteSearchOrder = &rules[i]
		}
	}
	if externalIDInfos == nil {
		t.Fatal("external-id-infos rule not found in loaded rules")
	}
	if externalIDInfos.DivergenceID != "D-28" {
		t.Fatalf("external-id-infos divergence_id = %q, want D-28", externalIDInfos.DivergenceID)
	}
	if !externalIDInfos.ExplicitOnly {
		t.Fatal("external-id-infos must be explicit_only")
	}
	if remoteSearchOrder == nil {
		t.Fatal("remote-search-result-order rule not found in loaded rules")
	}
	if remoteSearchOrder.DivergenceID != "D-18" {
		t.Fatalf("remote-search-result-order divergence_id = %q, want D-18", remoteSearchOrder.DivergenceID)
	}
	if !remoteSearchOrder.ExplicitOnly {
		t.Fatal("remote-search-result-order must be explicit_only")
	}
}
