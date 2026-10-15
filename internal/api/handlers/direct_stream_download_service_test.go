// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// TestItemDownloadsUseDownloadSourceCapability preserves the download route's
// capability boundary: an eligible authenticated request must use DownloadSource
// and map a download ineligibility decision to Jellyfin's source-mismatch 400.
func TestItemDownloadsUseDownloadSourceCapability(t *testing.T) {
	userID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000710")
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000711")
	service := &downloadOnlySourceService{}
	authorizer := middleware.NewAuthorizer(
		playbackResolver{identity: auth.Identity{Authenticated: true, UserID: userID}},
		playbackEvaluator{},
		playbackUsers{},
	)
	routes := router.New(authorizer.Wrap)
	handlers.RegisterItemDownloads(routes, service)

	record := parityRequest(routes, http.MethodGet, "/Items/"+itemID.Dashed()+"/Download")

	if record.Code != http.StatusBadRequest {
		t.Fatalf("GET item download status = %d, want %d; body=%q", record.Code, http.StatusBadRequest, record.Body.String())
	}
	if got, want := record.Header().Get("Content-Type"), "text/plain"; got != want {
		t.Errorf("GET item download Content-Type = %q, want %q", got, want)
	}
	if got, want := record.Header().Get("Content-Length"), "25"; got != want {
		t.Errorf("GET item download Content-Length = %q, want %q", got, want)
	}
	if got, want := record.Body.String(), "Error processing request."; got != want {
		t.Errorf("GET item download body = %q, want %q", got, want)
	}
	if got, want := service.calls, 1; got != want {
		t.Errorf("DownloadSource calls = %d, want %d", got, want)
	}
	if got, want := service.userID, userID; got != want {
		t.Errorf("DownloadSource user ID = %v, want authenticated user %v", got, want)
	}
	if got, want := service.itemID, itemID; got != want {
		t.Errorf("DownloadSource item ID = %v, want requested item %v", got, want)
	}
}

type downloadOnlySourceService struct {
	calls          int
	userID, itemID dto.UUID
}

func (service *downloadOnlySourceService) DownloadSource(_ context.Context, userID, itemID dto.UUID) (playback.MediaSourceInfo, error) {
	service.calls++
	service.userID = userID
	service.itemID = itemID
	return playback.MediaSourceInfo{}, playback.ErrDownloadNotAllowed
}
