// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
)

// TestUserImageRoutesLedger asserts the route table registers GET and HEAD
// /UserImage with the stock OperationIDs and Anonymous: true (the
// images_test.go:60-68 style).
func TestUserImageRoutesLedger(t *testing.T) {
	rt := router.New()
	handlers.RegisterUserImage(rt)

	want := []router.Route{
		{Method: http.MethodGet, Pattern: "/UserImage", OperationID: "GetUserImage", Anonymous: true},
		{Method: http.MethodHead, Pattern: "/UserImage", OperationID: "HeadUserImage", Anonymous: true},
	}
	for _, expected := range want {
		t.Run(expected.OperationID+"_"+expected.Method, func(t *testing.T) {
			got, found := routeByOperation(rt.Routes(), expected.OperationID)
			if !found {
				t.Fatalf("operation %q not registered", expected.OperationID)
			}
			if got.Method != expected.Method || got.Pattern != expected.Pattern || got.Policy != expected.Policy || got.Anonymous != expected.Anonymous {
				t.Fatalf("route %q = %#v, want %#v", expected.OperationID, got, expected)
			}
		})
	}
}

const userImageValidUUID = "aabbccddeeff00112233445566778899"

func TestUserImage(t *testing.T) {
	rt := router.New()
	handlers.RegisterUserImage(rt)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "no query returns 400",
			path:       "/UserImage",
			wantStatus: http.StatusBadRequest,
			wantBody:   "UserId is required if unauthenticated\n",
		},
		{
			name:       "valid userId returns 404",
			path:       "/UserImage?userId=" + userImageValidUUID,
			wantStatus: http.StatusNotFound,
			wantBody:   "Not Found\n",
		},
		{
			name:       "all-zero userId returns 400",
			path:       "/UserImage?userId=00000000-0000-0000-0000-000000000000",
			wantStatus: http.StatusBadRequest,
			wantBody:   "UserId is required if unauthenticated\n",
		},
		{
			name:       "malformed userId returns 400",
			path:       "/UserImage?userId=not-a-uuid",
			wantStatus: http.StatusBadRequest,
			wantBody:   "Bad Request\n",
		},
		{
			name:       "tag and format with valid userId return 404",
			path:       "/UserImage?userId=" + userImageValidUUID + "&tag=primary&format=png",
			wantStatus: http.StatusNotFound,
			wantBody:   "Not Found\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rt.ServeHTTP(recorder, request)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			if recorder.Body.String() != tc.wantBody {
				t.Fatalf("body = %q, want %q", recorder.Body.String(), tc.wantBody)
			}
		})
	}
}
