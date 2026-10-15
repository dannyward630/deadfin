// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"testing"
)

// The user segment is not the playback authority, but it remains a UUID route
// parameter. Each legacy user-scoped operation must reject a malformed value
// before reaching the playstate application, with Jellyfin's stock 400 wire
// response.
func TestLegacyUserScopedPlaystateRejectsMalformedUserIDBeforeDispatch(t *testing.T) {
	itemID := "00000000-0000-0000-0000-0000000005d1"
	for _, tc := range []struct {
		name, method, path string
	}{
		{
			name:   "start",
			method: http.MethodPost,
			path:   "/Users/not-a-guid/PlayingItems/" + itemID,
		},
		{
			name:   "progress",
			method: http.MethodPost,
			path:   "/Users/not-a-guid/PlayingItems/" + itemID + "/Progress",
		},
		{
			name:   "stop",
			method: http.MethodDelete,
			path:   "/Users/not-a-guid/PlayingItems/" + itemID,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &playstateHandlerFake{}
			rec := playstateRequest(newPlaystateRouter(service), tc.method, tc.path, "", "")

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
				t.Errorf("playstate application calls = %d, want 0", got)
			}
		})
	}
}

// The start, progress, and stop routes use distinct upstream request DTOs.
// Fields outside an operation's DTO must be ignored rather than shared by the
// common transport projection; fields in that operation's DTO still reach the
// application.
func TestPlaystateRoutesApplyOperationSpecificFieldSets(t *testing.T) {
	itemID := "00000000-0000-0000-0000-0000000005d2"
	for _, tc := range []struct {
		name, method, path, body, contentType, operation string
		assertReport                                     func(*testing.T, playstateHandlerCall)
	}{
		{
			name:        "legacy start ignores progress position while retaining seekability",
			method:      http.MethodPost,
			path:        "/PlayingItems/" + itemID + "?CanSeek=true&PositionTicks=123",
			operation:   "start",
			contentType: "",
			assertReport: func(t *testing.T, call playstateHandlerCall) {
				t.Helper()
				if got := call.report.PlaybackState.ItemID; got != playstateHandlerUUID(t, itemID) {
					t.Errorf("start item ID = %s, want route item %s", got.Dashed(), itemID)
				}
				if !call.report.PlaybackState.CanSeek {
					t.Error("start did not retain the valid CanSeek field")
				}
				if got := call.report.PlaybackState.PositionTicks; got != nil {
					t.Errorf("start leaked progress-only PositionTicks = %d", *got)
				}
			},
		},
		{
			name:      "legacy progress ignores playback order while retaining position",
			method:    http.MethodPost,
			path:      "/PlayingItems/" + itemID + "/Progress?PositionTicks=456&PlaybackOrder=Shuffle",
			operation: "progress",
			assertReport: func(t *testing.T, call playstateHandlerCall) {
				t.Helper()
				if got := call.report.PlaybackState.PositionTicks; got == nil || *got != 456 {
					t.Errorf("progress PositionTicks = %v, want 456", got)
				}
				if got := call.report.PlaybackState.PlaybackOrder; got != "Default" {
					t.Errorf("progress PlaybackOrder = %q after unsupported Shuffle input, want source default %q", got, "Default")
				}
			},
		},
		{
			name:      "legacy progress ignores malformed unknown seekability while retaining position",
			method:    http.MethodPost,
			path:      "/PlayingItems/" + itemID + "/Progress?PositionTicks=456&CanSeek=bogus",
			operation: "progress",
			assertReport: func(t *testing.T, call playstateHandlerCall) {
				t.Helper()
				if got := call.report.PlaybackState.PositionTicks; got == nil || *got != 456 {
					t.Errorf("progress PositionTicks = %v, want 456", got)
				}
				if call.report.PlaybackState.CanSeek {
					t.Error("progress retained unsupported CanSeek")
				}
			},
		},
		{
			name:        "modern start ignores malformed unknown failure marker",
			method:      http.MethodPost,
			path:        "/Sessions/Playing",
			body:        `{"ItemId":"` + itemID + `","Failed":"bogus"}`,
			contentType: "application/json",
			operation:   "start",
			assertReport: func(t *testing.T, call playstateHandlerCall) {
				t.Helper()
				if got := call.report.PlaybackState.ItemID; got != playstateHandlerUUID(t, itemID) {
					t.Errorf("start item ID = %s, want body item %s", got.Dashed(), itemID)
				}
			},
		},
		{
			name:        "modern progress ignores malformed unknown failure marker",
			method:      http.MethodPost,
			path:        "/Sessions/Playing/Progress",
			body:        `{"ItemId":"` + itemID + `","Failed":"bogus"}`,
			contentType: "application/json",
			operation:   "progress",
			assertReport: func(t *testing.T, call playstateHandlerCall) {
				t.Helper()
				if got := call.report.PlaybackState.ItemID; got != playstateHandlerUUID(t, itemID) {
					t.Errorf("progress item ID = %s, want body item %s", got.Dashed(), itemID)
				}
			},
		},
		{
			name:        "modern stop ignores malformed unknown seekability while retaining position",
			method:      http.MethodPost,
			path:        "/Sessions/Playing/Stopped",
			body:        `{"ItemId":"` + itemID + `","PositionTicks":789,"CanSeek":"bogus"}`,
			contentType: "application/json",
			operation:   "stop",
			assertReport: func(t *testing.T, call playstateHandlerCall) {
				t.Helper()
				if got := call.report.PlaybackState.ItemID; got != playstateHandlerUUID(t, itemID) {
					t.Errorf("stop item ID = %s, want body item %s", got.Dashed(), itemID)
				}
				if got := call.report.PlaybackState.PositionTicks; got == nil || *got != 789 {
					t.Errorf("stop PositionTicks = %v, want 789", got)
				}
			},
		},
		{
			name:        "modern stop ignores malformed unknown playback start time while retaining position",
			method:      http.MethodPost,
			path:        "/Sessions/Playing/Stopped",
			body:        `{"ItemId":"` + itemID + `","PositionTicks":790,"PlaybackStartTimeTicks":"bogus"}`,
			contentType: "application/json",
			operation:   "stop",
			assertReport: func(t *testing.T, call playstateHandlerCall) {
				t.Helper()
				if got := call.report.PlaybackState.ItemID; got != playstateHandlerUUID(t, itemID) {
					t.Errorf("stop item ID = %s, want body item %s", got.Dashed(), itemID)
				}
				if got := call.report.PlaybackState.PositionTicks; got == nil || *got != 790 {
					t.Errorf("stop PositionTicks = %v, want 790", got)
				}
			},
		},
		{
			name:      "legacy stop ignores malformed unknown seekability while retaining position",
			method:    http.MethodDelete,
			path:      "/PlayingItems/" + itemID + "?PositionTicks=987&CanSeek=bogus",
			operation: "stop",
			assertReport: func(t *testing.T, call playstateHandlerCall) {
				t.Helper()
				if got := call.report.PlaybackState.ItemID; got != playstateHandlerUUID(t, itemID) {
					t.Errorf("stop item ID = %s, want route item %s", got.Dashed(), itemID)
				}
				if got := call.report.PlaybackState.PositionTicks; got == nil || *got != 987 {
					t.Errorf("stop PositionTicks = %v, want 987", got)
				}
			},
		},
		{
			name:      "legacy stop ignores malformed unknown playback start time while retaining position",
			method:    http.MethodDelete,
			path:      "/PlayingItems/" + itemID + "?PositionTicks=988&PlaybackStartTimeTicks=bogus",
			operation: "stop",
			assertReport: func(t *testing.T, call playstateHandlerCall) {
				t.Helper()
				if got := call.report.PlaybackState.ItemID; got != playstateHandlerUUID(t, itemID) {
					t.Errorf("stop item ID = %s, want route item %s", got.Dashed(), itemID)
				}
				if got := call.report.PlaybackState.PositionTicks; got == nil || *got != 988 {
					t.Errorf("stop PositionTicks = %v, want 988", got)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &playstateHandlerFake{}
			rec := playstateRequest(newPlaystateRouter(service), tc.method, tc.path, tc.body, tc.contentType)
			assertPlaystateStatus(t, rec, http.StatusNoContent)

			call := service.lastCall(t)
			if got := call.operation; got != tc.operation {
				t.Fatalf("application operation = %q, want %q", got, tc.operation)
			}
			tc.assertReport(t, call)
		})
	}
}
