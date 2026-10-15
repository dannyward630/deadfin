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
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestUniversalAudioGlobalAPIKeyWithoutUserIDUsesNilUserContext preserves the
// global API-key browse behavior: no selected persisted user is needed, and
// universal source selection receives Guid.Empty.
func TestUniversalAudioGlobalAPIKeyWithoutUserIDUsesNilUserContext(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000709")
	service := &globalAPIKeyUniversalAudioService{
		source: parityAudioSource(t, "global-api-key.mp3", "global-api-key-universal-source", "mp3", []playback.MediaStream{{
			Type: playback.StreamTypeAudio, Codec: "mp3", IsDefault: true,
		}}),
	}
	authorizer := middleware.NewAuthorizer(
		playbackResolver{identity: auth.Identity{Authenticated: true, APIKey: true, Administrator: true, UserID: dto.NilUUID}},
		playbackEvaluator{},
		playbackUsers{},
	)
	routes := router.New(authorizer.Wrap)
	handlers.RegisterDirectAudioStreams(routes, service, nilUUIDRejectingUserResolver{})

	record := parityRequest(routes, http.MethodGet, "/Audio/"+itemID.Dashed()+"/universal?Container=mp3")
	if record.Code != http.StatusOK {
		t.Fatalf("global API-key universal audio status = %d, want %d; body=%q", record.Code, http.StatusOK, record.Body.String())
	}
	if got, want := record.Body.String(), "global-api-key-universal-source"; got != want {
		t.Errorf("global API-key universal audio body = %q, want selected source bytes %q", got, want)
	}
	if got := service.selectedUser; got != dto.NilUUID {
		t.Errorf("global API-key universal source user = %v, want dto.NilUUID", got)
	}
}

type nilUUIDRejectingUserResolver struct{}

func (nilUUIDRejectingUserResolver) Get(_ context.Context, id dto.UUID) (store.User, error) {
	if id.IsNil() {
		return store.User{}, store.ErrNotFound
	}
	return store.User{ID: id}, nil
}

type globalAPIKeyUniversalAudioService struct {
	source       playback.MediaSourceInfo
	selectedUser dto.UUID
}

func (service *globalAPIKeyUniversalAudioService) StaticSource(_ context.Context, userID, _ dto.UUID, _ string) (playback.MediaSourceInfo, error) {
	service.selectedUser = userID
	return service.source, nil
}

// TestStaticStreamsScopeSourceToTheRequestUser covers D-54: a user token
// scopes the static source lookup to that user's libraries, and an API key
// keeps the unscoped server-wide lookup.
func TestStaticStreamsScopeSourceToTheRequestUser(t *testing.T) {
	itemID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000710")
	userID := playbackTestUUID(t, "00000000-0000-0000-0000-000000000711")
	for _, test := range []struct {
		name     string
		identity auth.Identity
		want     dto.UUID
	}{
		{"user token", auth.Identity{Authenticated: true, UserID: userID}, userID},
		{"API key", auth.Identity{Authenticated: true, APIKey: true, Administrator: true}, dto.NilUUID},
	} {
		for _, path := range []string{"/Audio/" + itemID.Dashed() + "/stream?Static=true", "/Videos/" + itemID.Dashed() + "/stream.mp3?Static=true"} {
			t.Run(test.name+" "+path, func(t *testing.T) {
				service := &globalAPIKeyUniversalAudioService{
					source:       parityAudioSource(t, "scoped.mp3", "scoped-source", "mp3", []playback.MediaStream{{Type: playback.StreamTypeAudio, Codec: "mp3", IsDefault: true}}),
					selectedUser: playbackTestUUID(t, "00000000-0000-0000-0000-0000000007ff"),
				}
				routes := router.New(middleware.NewAuthorizer(playbackResolver{identity: test.identity}, playbackEvaluator{}, playbackUsers{}).Wrap)
				handlers.RegisterDirectAudioStreams(routes, service)
				handlers.RegisterDirectVideoStreams(routes, service)

				record := parityRequest(routes, http.MethodGet, path)
				if record.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body=%q", record.Code, record.Body.String())
				}
				if service.selectedUser != test.want {
					t.Fatalf("source lookup user = %v, want %v", service.selectedUser, test.want)
				}
			})
		}
	}
}
