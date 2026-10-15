// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// PlaybackStartInfo, PlaybackProgressInfo, and PlaybackStopInfo carry fields
// which the server must bind for model-validation parity but must not trust to
// select its session or item. A malformed value for any recognized upstream
// field rejects the entire body before the application is invoked; a well-typed
// value remains non-authoritative.
func TestModernPlaystateBindsRecognizedIgnoredUpstreamFieldsBeforeDispatch(t *testing.T) {
	const itemID = "00000000-0000-0000-0000-0000000005e1"

	for _, tc := range []struct {
		name, path, operation, body string
		valid                       bool
	}{
		{
			name:      "start accepts typed ignored session and item",
			path:      "/Sessions/Playing",
			operation: "start",
			body:      `{"ItemId":"` + itemID + `","SessionId":"untrusted-session","Item":{"Id":"00000000-0000-0000-0000-0000000005e2","Name":"untrusted item"}}`,
			valid:     true,
		},
		{
			name:      "progress accepts typed ignored session and item",
			path:      "/Sessions/Playing/Progress",
			operation: "progress",
			body:      `{"ItemId":"` + itemID + `","SessionId":"untrusted-session","Item":{"Id":"00000000-0000-0000-0000-0000000005e2","Name":"untrusted item"}}`,
			valid:     true,
		},
		{
			name:      "stop accepts typed ignored session item and next media type",
			path:      "/Sessions/Playing/Stopped",
			operation: "stop",
			body:      `{"ItemId":"` + itemID + `","SessionId":"untrusted-session","Item":{"Id":"00000000-0000-0000-0000-0000000005e2","Name":"untrusted item"},"NextMediaType":"Episode"}`,
			valid:     true,
		},
		{
			name:      "start rejects object session ID",
			path:      "/Sessions/Playing",
			operation: "start",
			body:      `{"ItemId":"` + itemID + `","SessionId":{}}`,
		},
		{
			name:      "start rejects numeric session ID",
			path:      "/Sessions/Playing",
			operation: "start",
			body:      `{"ItemId":"` + itemID + `","SessionId":17}`,
		},
		{
			name:      "progress rejects object session ID",
			path:      "/Sessions/Playing/Progress",
			operation: "progress",
			body:      `{"ItemId":"` + itemID + `","SessionId":{}}`,
		},
		{
			name:      "progress rejects numeric session ID",
			path:      "/Sessions/Playing/Progress",
			operation: "progress",
			body:      `{"ItemId":"` + itemID + `","SessionId":17}`,
		},
		{
			name:      "stop rejects object session ID",
			path:      "/Sessions/Playing/Stopped",
			operation: "stop",
			body:      `{"ItemId":"` + itemID + `","SessionId":{}}`,
		},
		{
			name:      "stop rejects numeric session ID",
			path:      "/Sessions/Playing/Stopped",
			operation: "stop",
			body:      `{"ItemId":"` + itemID + `","SessionId":17}`,
		},
		{
			name:      "start rejects scalar item",
			path:      "/Sessions/Playing",
			operation: "start",
			body:      `{"ItemId":"` + itemID + `","Item":"not-an-item-object"}`,
		},
		{
			name:      "start rejects item object with invalid identifier",
			path:      "/Sessions/Playing",
			operation: "start",
			body:      `{"ItemId":"` + itemID + `","Item":{"Id":17}}`,
		},
		{
			name:      "progress rejects scalar item",
			path:      "/Sessions/Playing/Progress",
			operation: "progress",
			body:      `{"ItemId":"` + itemID + `","Item":"not-an-item-object"}`,
		},
		{
			name:      "progress rejects item object with invalid identifier",
			path:      "/Sessions/Playing/Progress",
			operation: "progress",
			body:      `{"ItemId":"` + itemID + `","Item":{"Id":17}}`,
		},
		{
			name:      "stop rejects scalar item",
			path:      "/Sessions/Playing/Stopped",
			operation: "stop",
			body:      `{"ItemId":"` + itemID + `","Item":"not-an-item-object"}`,
		},
		{
			name:      "stop rejects item object with invalid identifier",
			path:      "/Sessions/Playing/Stopped",
			operation: "stop",
			body:      `{"ItemId":"` + itemID + `","Item":{"Id":17}}`,
		},
		{
			name:      "stop rejects object next media type",
			path:      "/Sessions/Playing/Stopped",
			operation: "stop",
			body:      `{"ItemId":"` + itemID + `","NextMediaType":{}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &playstateHandlerFake{}
			rec := playstateRequest(newPlaystateRouter(service), http.MethodPost, tc.path, tc.body, "application/json")

			if tc.valid {
				assertPlaystateStatus(t, rec, http.StatusNoContent)
				if got := service.callCount(); got != 1 {
					t.Fatalf("playstate application calls = %d, want 1 for accepted request", got)
				}
				call := service.lastCall(t)
				if got := call.operation; got != tc.operation {
					t.Errorf("application operation = %q, want %q", got, tc.operation)
				}
				if got := call.actor.UserID; got != (dto.UUID{0x50, 0x1}) {
					t.Errorf("application actor user ID = %s, want authenticated user", got.Dashed())
				}
				if got := call.actor.Client; got != "Authenticated Client" {
					t.Errorf("application actor client = %q, want authenticated client", got)
				}
				if got := call.actor.DeviceID; got != "authenticated-device" {
					t.Errorf("application actor device ID = %q, want authenticated device", got)
				}
				if got := call.report.PlaybackState.ItemID; got != playstateHandlerUUID(t, itemID) {
					t.Errorf("report item ID = %s, want authoritative ItemId %s", got.Dashed(), itemID)
				}
				return
			}

			if got := rec.Code; got != http.StatusBadRequest {
				t.Errorf("status = %d, want %d; body=%q", got, http.StatusBadRequest, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "text/plain" {
				t.Errorf("Content-Type = %q, want %q", got, "text/plain")
			}
			if got := rec.Body.String(); got != "Error processing request." {
				t.Errorf("body = %q, want exact stock error body", got)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "" {
				t.Errorf("X-Content-Type-Options = %q, want absent", got)
			}
			if got := service.callCount(); got != 0 {
				t.Errorf("playstate application calls = %d, want 0 for rejected request", got)
			}
		})
	}
}
