// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"testing"
)

// TestQuickConnectVolatiles proves that the quickconnect-volatiles normalizer
// replaces only correctly typed root QuickConnect-result Secret, Code, and
// DateAdded fields. It preserves Authenticated, device/client metadata, field
// presence, wrong types, nested fields, error bodies, and all authentication-
// result fields.

func TestQuickConnectVolatiles(t *testing.T) {
	rule := Rule{Name: "quickconnect-volatiles", Action: "quickconnect_volatiles"}
	normalizer := newNormalizer([]Rule{rule})

	t.Run("normalizes root Secret Code DateAdded strings", func(t *testing.T) {
		response := Response{
			Status:  200,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"Authenticated":false,"Secret":"ABC123","Code":"676868","DeviceId":"dev","DeviceName":"iPhone","AppName":"Swiftfin","AppVersion":"1.0.0","DateAdded":"2026-01-01T00:00:00Z"}`),
		}
		normalized, err := normalizer.normalize(response, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		body, ok := normalized.Body.(map[string]any)
		if !ok {
			t.Fatalf("normalized body = %#v, want object", normalized.Body)
		}
		if body["Secret"] != "<quickconnect-secret>" {
			t.Fatalf("Secret = %#v, want token", body["Secret"])
		}
		if body["Code"] != "<quickconnect-code>" {
			t.Fatalf("Code = %#v, want token", body["Code"])
		}
		if body["DateAdded"] != "<quickconnect-dateadded>" {
			t.Fatalf("DateAdded = %#v, want token", body["DateAdded"])
		}
		if body["Authenticated"] != false {
			t.Fatalf("Authenticated = %#v, want false preserved", body["Authenticated"])
		}
		if body["DeviceId"] != "dev" {
			t.Fatalf("DeviceId = %#v, want preserved", body["DeviceId"])
		}
		if body["DeviceName"] != "iPhone" {
			t.Fatalf("DeviceName = %#v, want preserved", body["DeviceName"])
		}
		if body["AppName"] != "Swiftfin" {
			t.Fatalf("AppName = %#v, want preserved", body["AppName"])
		}
		if body["AppVersion"] != "1.0.0" {
			t.Fatalf("AppVersion = %#v, want preserved", body["AppVersion"])
		}
	})

	t.Run("preserves CamelCase-profile DTO casing", func(t *testing.T) {
		response := Response{
			Status:  200,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"authenticated":false,"secret":"camel-secret","code":"123456","deviceId":"dev","deviceName":"iPhone","appName":"Swiftfin","appVersion":"1.0.0","dateAdded":"2026-01-01T00:00:00Z"}`),
		}
		normalized, err := normalizer.normalize(response, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		body := normalized.Body.(map[string]any)
		if body["secret"] != "<quickconnect-secret>" {
			t.Fatalf("camel secret = %#v, want token", body["secret"])
		}
		if body["code"] != "<quickconnect-code>" {
			t.Fatalf("camel code = %#v, want token", body["code"])
		}
		if body["dateAdded"] != "<quickconnect-dateadded>" {
			t.Fatalf("camel dateAdded = %#v, want token", body["dateAdded"])
		}
		if body["authenticated"] != false {
			t.Fatalf("camel authenticated = %#v, want preserved", body["authenticated"])
		}
	})

	t.Run("preserves wrong-typed volatile fields", func(t *testing.T) {
		response := Response{
			Status:  200,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"Secret":123,"Code":true,"DateAdded":null,"Authenticated":false}`),
		}
		normalized, err := normalizer.normalize(response, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		body := normalized.Body.(map[string]any)
		if body["Secret"] != json.Number("123") {
			t.Fatalf("wrong-typed Secret = %#v, want preserved number", body["Secret"])
		}
		if body["Code"] != true {
			t.Fatalf("wrong-typed Code = %#v, want preserved bool", body["Code"])
		}
		if body["DateAdded"] != nil {
			t.Fatalf("wrong-typed DateAdded = %#v, want preserved null", body["DateAdded"])
		}
		if body["Authenticated"] != false {
			t.Fatalf("Authenticated = %#v, want preserved", body["Authenticated"])
		}
	})

	t.Run("does not normalize authentication-result body", func(t *testing.T) {
		response := Response{
			Status:  200,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"AccessToken":"real-token","User":{"Id":"user-id","Name":"admin"},"SessionInfo":{"Id":"session-id"}}`),
		}
		normalized, err := normalizer.normalize(response, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		body := normalized.Body.(map[string]any)
		if body["AccessToken"] != "real-token" {
			t.Fatalf("AccessToken = %#v, want preserved", body["AccessToken"])
		}
	})

	t.Run("does not normalize error body", func(t *testing.T) {
		response := Response{
			Status:  404,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"Secret":"should-stay","error":"Unknown secret"}`),
		}
		normalized, err := normalizer.normalize(response, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		body := normalized.Body.(map[string]any)
		if body["Secret"] != "should-stay" {
			t.Fatalf("error body Secret = %#v, want preserved", body["Secret"])
		}
	})

	t.Run("does not normalize error body with Secret and Code", func(t *testing.T) {
		response := Response{
			Status:  400,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"Secret":"err-secret","Code":"err-code","error":"Error processing request"}`),
		}
		normalized, err := normalizer.normalize(response, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		body := normalized.Body.(map[string]any)
		if body["Secret"] != "err-secret" {
			t.Fatalf("error body Secret = %#v, want preserved", body["Secret"])
		}
		if body["Code"] != "err-code" {
			t.Fatalf("error body Code = %#v, want preserved", body["Code"])
		}
		if body["error"] != "Error processing request" {
			t.Fatalf("error body error = %#v, want preserved", body["error"])
		}
	})
	t.Run("does not normalize error body with Authenticated Secret Code and error", func(t *testing.T) {
		response := Response{
			Status:  400,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"Authenticated":false,"Secret":"err-secret","Code":"err-code","error":"Error processing request"}`),
		}
		normalized, err := normalizer.normalize(response, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		body := normalized.Body.(map[string]any)
		if body["Secret"] != "err-secret" {
			t.Fatalf("error body Secret = %#v, want preserved", body["Secret"])
		}
		if body["Code"] != "err-code" {
			t.Fatalf("error body Code = %#v, want preserved", body["Code"])
		}
		if body["Authenticated"] != false {
			t.Fatalf("error body Authenticated = %#v, want preserved", body["Authenticated"])
		}
		if body["error"] != "Error processing request" {
			t.Fatalf("error body error = %#v, want preserved", body["error"])
		}
	})

	t.Run("does not normalize nested QuickConnect-shaped objects", func(t *testing.T) {
		response := Response{
			Status:  200,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"Outer":{"Secret":"nested-secret","Code":"nested-code"},"Authenticated":true}`),
		}
		normalized, err := normalizer.normalize(response, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		body := normalized.Body.(map[string]any)
		outer := body["Outer"].(map[string]any)
		if outer["Secret"] != "nested-secret" {
			t.Fatalf("nested Secret = %#v, want preserved", outer["Secret"])
		}
		if outer["Code"] != "nested-code" {
			t.Fatalf("nested Code = %#v, want preserved", outer["Code"])
		}
	})

	t.Run("preserves absent volatile fields", func(t *testing.T) {
		response := Response{
			Status:  200,
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"Secret":"present","Code":"123456","Authenticated":false}`),
		}
		normalized, err := normalizer.normalize(response, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		body := normalized.Body.(map[string]any)
		if body["Secret"] != "<quickconnect-secret>" {
			t.Fatalf("Secret = %#v, want token", body["Secret"])
		}
		if body["Code"] != "<quickconnect-code>" {
			t.Fatalf("Code = %#v, want token (present field is normalized)", body["Code"])
		}
		if _, ok := body["DateAdded"]; ok {
			t.Fatal("DateAdded must remain absent, not be added by normalization")
		}
	})
}
