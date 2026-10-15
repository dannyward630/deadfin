// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/user"
)

// TestP208BrowseRoutesAuthorizeSelectedUserThroughMiddleware pins the selected
// user policy shared by the P2-08 browse surface. Every request crosses the
// production authorizer: the actor is resolved from its real request identity,
// rather than directly invoking a handler.
//
// Source: upstream/jellyfin/Jellyfin.Api/Helpers/RequestHelpers.cs:67-85.
// An omitted or zero target means the authenticated user; an explicit other
// target is forbidden unless the caller is an administrator. Once authorized,
// selected-user lookup maps an absent user to 404 before library reads.
func TestP208BrowseRoutesAuthorizeSelectedUserThroughMiddleware(t *testing.T) {
	h := newP104Harness(t)
	actor := h.createUser(t, "Browse Actor", "password", json.RawMessage(`{"EnableAllFolders":true}`), json.RawMessage(`{}`))
	target := h.createUser(t, "Browse Target", "password", json.RawMessage(`{"EnableAllFolders":true}`), json.RawMessage(`{}`))
	h.identities["actor"] = auth.Identity{Authenticated: true, UserID: actor.ID}
	h.identities["admin"] = auth.Identity{Authenticated: true, Administrator: true, UserID: actor.ID}

	reader := &p208TargetingReader{itemID: p208TargetingUUID(t, "00000000-0000-0000-0000-000000000801")}
	builder := &p208TargetingBuilder{}
	nextUp := &p208TargetingNextUp{itemID: reader.itemID}
	handlers.RegisterItems(h.rt, reader, builder, clock.NewReal(), handlers.ItemServices{
		Users:             h.users,
		LatestPreferences: handlers.NewLatestUserPreferences(h.users),
	})
	handlers.RegisterShows(h.rt, reader, builder, nextUp, h.users, clock.NewReal())

	missing := p208TargetingUUID(t, "00000000-0000-0000-0000-000000000899")
	for _, route := range p208TargetingRoutes(reader.itemID) {
		t.Run(route.name+" non-administrator cannot select another user", func(t *testing.T) {
			reader.reset()
			nextUp.reset()
			rec := p104Call(h.rt, http.MethodGet, route.path(target.ID), "", "", "", "actor", p104MediaBrowserMetadata)
			assertStatus(t, rec, http.StatusForbidden)
			if got := p208TargetingReadCount(reader, nextUp); got != 0 {
				t.Fatalf("%s performed %d library reads before rejecting another user", route.name, got)
			}
		})

		t.Run(route.name+" administrator can select an existing user", func(t *testing.T) {
			reader.reset()
			nextUp.reset()
			rec := p104Call(h.rt, http.MethodGet, route.path(target.ID), "", "", "", "admin", p104MediaBrowserMetadata)
			assertStatus(t, rec, http.StatusOK)
			if !p208TargetingSelectedUser(reader, nextUp, target.ID) {
				t.Fatalf("%s did not forward the administrator-selected user %s", route.name, target.ID)
			}
		})

		t.Run(route.name+" administrator receives source not-found for absent selected user", func(t *testing.T) {
			reader.reset()
			nextUp.reset()
			rec := p104Call(h.rt, http.MethodGet, route.path(missing), "", "", "", "admin", p104MediaBrowserMetadata)
			assertStatus(t, rec, http.StatusNotFound)
			if got := p208TargetingReadCount(reader, nextUp); got != 0 {
				t.Fatalf("%s performed %d library reads before rejecting an absent selected user", route.name, got)
			}
		})
	}
}

// TestP208BrowseRoutesTreatOmittedAndZeroUserIDAsAuthenticatedUser makes the
// RequestHelpers fallback observable on the canonical Items endpoint.
func TestP208BrowseRoutesTreatOmittedAndZeroUserIDAsAuthenticatedUser(t *testing.T) {
	h := newP104Harness(t)
	actor := h.createUser(t, "Fallback Actor", "password", json.RawMessage(`{"EnableAllFolders":true}`), json.RawMessage(`{}`))
	h.identities["actor"] = auth.Identity{Authenticated: true, UserID: actor.ID}
	reader := &p208TargetingReader{itemID: p208TargetingUUID(t, "00000000-0000-0000-0000-000000000802")}
	handlers.RegisterItems(h.rt, reader, &p208TargetingBuilder{}, clock.NewReal(), handlers.ItemServices{Users: h.users, LatestPreferences: handlers.NewLatestUserPreferences(h.users)})

	for _, path := range []string{"/Items", "/Items?userId=00000000-0000-0000-0000-000000000000"} {
		t.Run(path, func(t *testing.T) {
			reader.calls = 0
			rec := p104Call(h.rt, http.MethodGet, path, "", "", "", "actor", p104MediaBrowserMetadata)
			assertStatus(t, rec, http.StatusOK)
			if reader.calls != 1 || reader.last.UserID == nil || *reader.last.UserID != actor.ID || reader.last.User == nil || reader.last.User.ID != actor.ID {
				t.Fatalf("%s selected query = %#v after %d reads, want authenticated user %s", path, reader.last, reader.calls, actor.ID)
			}
		})
	}
}

// A case variant is the same protected userId selector, not an opportunity to
// select an arbitrary target based on query-map iteration order. Rejection
// must happen before the item reader observes either account.
func TestItemsRejectConflictingCaseFoldedUserIDAliasesBeforeReadingTarget(t *testing.T) {
	h := newP104Harness(t)
	actor := h.createUser(t, "Alias Actor", "password", json.RawMessage(`{"EnableAllFolders":true}`), json.RawMessage(`{}`))
	target := h.createUser(t, "Alias Target", "password", json.RawMessage(`{"EnableAllFolders":true}`), json.RawMessage(`{}`))
	h.identities["actor"] = auth.Identity{Authenticated: true, UserID: actor.ID}

	reader := &p208TargetingReader{itemID: p208TargetingUUID(t, "00000000-0000-0000-0000-000000000804")}
	handlers.RegisterItems(h.rt, reader, &p208TargetingBuilder{}, clock.NewReal(), handlers.ItemServices{Users: h.users, LatestPreferences: handlers.NewLatestUserPreferences(h.users)})

	for _, tc := range []struct {
		name   string
		path   string
		status int
	}{
		{
			name:   "conflicting aliases reject without exposing target",
			path:   "/Items?userId=" + actor.ID.Dashed() + "&USERID=" + target.ID.Dashed(),
			status: http.StatusBadRequest,
		},
		{
			name:   "identical aliases retain selected user",
			path:   "/Items?userId=" + actor.ID.Dashed() + "&USERID=" + actor.ID.Dashed(),
			status: http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader.reset()
			rec := p104Call(h.rt, http.MethodGet, tc.path, "", "", "", "actor", p104MediaBrowserMetadata)
			assertStatus(t, rec, tc.status)
			if tc.status == http.StatusBadRequest {
				if reader.calls != 0 {
					t.Fatalf("conflicting aliases performed %d item reads before rejection", reader.calls)
				}
				if strings.Contains(rec.Body.String(), target.ID.Dashed()) {
					t.Fatalf("conflicting aliases exposed target data in response: %q", rec.Body.String())
				}
				return
			}
			if reader.calls != 1 || reader.last.UserID == nil || *reader.last.UserID != actor.ID || reader.last.User == nil || reader.last.User.ID != actor.ID {
				t.Fatalf("identical aliases selected query = %#v after %d reads, want actor %s", reader.last, reader.calls, actor.ID)
			}
		})
	}
}

// TestP208APIKeyBrowseRoutesResolveOmittedAndZeroUsersGlobally exercises the
// production credential resolver and authorizer rather than injecting an
// identity into request context. An API key is an authenticated global caller:
// its absent identity user must remain absent in downstream library queries.
func TestP208APIKeyBrowseRoutesResolveOmittedAndZeroUsersGlobally(t *testing.T) {
	h := newP208APIKeyHarness(t)
	target := h.createUser(t, "P208 selected target", `{"EnableRemoteAccess":true}`)
	member := h.createUser(t, "P208 member", `{"EnableRemoteAccess":true}`)
	apiKey := h.createAPIKey(t, "p208-api-key")
	memberToken := h.issueMemberToken(t, member.ID)

	reader := &p208TargetingReader{itemID: p208TargetingUUID(t, "00000000-0000-0000-0000-000000000803")}
	builder := &p208TargetingBuilder{}
	nextUp := &p208TargetingNextUp{itemID: reader.itemID}
	handlers.RegisterItems(h.rt, reader, builder, clock.NewReal(), handlers.ItemServices{
		Users:             h.users,
		LatestPreferences: handlers.NewLatestUserPreferences(h.users),
	})
	handlers.RegisterShows(h.rt, reader, builder, nextUp, h.users, clock.NewReal())

	missing := p208TargetingUUID(t, "00000000-0000-0000-0000-000000000899")
	for _, route := range p208APIKeyTargetingRoutes(reader.itemID) {
		for _, tc := range []struct {
			name string
			path string
		}{
			{name: "omitted userId", path: route.path},
			{name: "zero userId", path: p208WithUserID(route.path, dto.NilUUID)},
		} {
			t.Run(route.name+" API key "+tc.name+" queries globally", func(t *testing.T) {
				reader.reset()
				nextUp.reset()
				rec := p208APIKeyRequest(h.rt, tc.path, apiKey)
				assertStatus(t, rec, http.StatusOK)
				p208AssertGlobalQueries(t, route.name, reader, nextUp)
			})
		}

		t.Run(route.name+" API key explicit existing user forwards persisted target", func(t *testing.T) {
			reader.reset()
			nextUp.reset()
			rec := p208APIKeyRequest(h.rt, p208WithUserID(route.path, target.ID), apiKey)
			assertStatus(t, rec, http.StatusOK)
			p208AssertTargetQueries(t, route.name, reader, nextUp, target.ID)
		})

		t.Run(route.name+" API key missing explicit user returns not found before reads", func(t *testing.T) {
			reader.reset()
			nextUp.reset()
			rec := p208APIKeyRequest(h.rt, p208WithUserID(route.path, missing), apiKey)
			assertStatus(t, rec, http.StatusNotFound)
			if got := p208TargetingReadCount(reader, nextUp); got != 0 {
				t.Fatalf("%s performed %d library reads before rejecting an absent API-key target", route.name, got)
			}
		})

		t.Run(route.name+" member device token remains forbidden from another user", func(t *testing.T) {
			reader.reset()
			nextUp.reset()
			rec := p208MemberRequest(h.rt, p208WithUserID(route.path, target.ID), memberToken)
			assertStatus(t, rec, http.StatusForbidden)
			if got := p208TargetingReadCount(reader, nextUp); got != 0 {
				t.Fatalf("%s performed %d library reads before rejecting a member cross-user target", route.name, got)
			}
		})
	}
}

type p208APIKeyTargetingRoute struct {
	name string
	path string
}

func p208APIKeyTargetingRoutes(itemID dto.UUID) []p208APIKeyTargetingRoute {
	return []p208APIKeyTargetingRoute{
		{name: "Items", path: "/Items"},
		{name: "Latest", path: "/Items/Latest?groupItems=false"},
		{name: "Genres", path: "/Genres"},
		{name: "Show seasons", path: "/Shows/" + itemID.Dashed() + "/Seasons"},
		{name: "Show episodes", path: "/Shows/" + itemID.Dashed() + "/Episodes"},
		{name: "Special features", path: "/Items/" + itemID.Dashed() + "/SpecialFeatures"},
	}
}

func p208WithUserID(path string, userID dto.UUID) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "userId=" + userID.Dashed()
}

func p208AssertGlobalQueries(t *testing.T, route string, reader *p208TargetingReader, nextUp *p208TargetingNextUp) {
	t.Helper()
	if got := p208TargetingReadCount(reader, nextUp); got == 0 {
		t.Fatalf("%s made no library reads for a successful global API-key request", route)
	}
	for _, query := range reader.queries {
		if query.UserID != nil || query.User != nil {
			t.Fatalf("%s global API-key ItemQuery = %#v, want nil UserID and User", route, query)
		}
	}
	if nextUp.calls != 0 {
		t.Fatalf("%s unexpectedly used NextUp for global API-key request: %#v", route, nextUp.last)
	}
}

func p208AssertTargetQueries(t *testing.T, route string, reader *p208TargetingReader, nextUp *p208TargetingNextUp, target dto.UUID) {
	t.Helper()
	if got := p208TargetingReadCount(reader, nextUp); got == 0 {
		t.Fatalf("%s made no library reads for explicit API-key user %s", route, target)
	}
	for _, query := range reader.queries {
		if query.UserID == nil || *query.UserID != target {
			t.Fatalf("%s ItemQuery = %#v, want explicit API-key target %s", route, query, target)
		}
	}
	if nextUp.calls != 0 {
		t.Fatalf("%s unexpectedly used NextUp for explicit API-key target: %#v", route, nextUp.last)
	}
}

type p208APIKeyHarness struct {
	rt       *router.Router
	users    *user.Service
	identity *store.Identity
	clock    *clock.Fake
}

func newP208APIKeyHarness(t *testing.T) *p208APIKeyHarness {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open P208 identity store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close P208 identity store: %v", err)
		}
	})

	current := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(current)
	identity := store.NewIdentity(stores)
	users := user.New(identity, fakeClock, &p104Entropy{})
	resolver := auth.NewResolver(auth.ResolverConfig{Devices: users, APIKeys: identity})
	evaluator := auth.NewEvaluator(p104Startup{completed: true}, p208APIKeyNetwork{}, identity, func() time.Time { return current })
	rt := router.New(middleware.NewAuthorizer(resolver, evaluator, users).Wrap)
	return &p208APIKeyHarness{rt: rt, users: users, identity: identity, clock: fakeClock}
}

func (h *p208APIKeyHarness) createUser(t *testing.T, name, policy string) store.User {
	t.Helper()
	created, err := h.users.Create(t.Context(), name, "p208 user password")
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	created.Policy = json.RawMessage(policy)
	created.Configuration = json.RawMessage(`{}`)
	if err := h.users.Update(t.Context(), created); err != nil {
		t.Fatalf("configure %s: %v", name, err)
	}
	return created
}

func (h *p208APIKeyHarness) createAPIKey(t *testing.T, token string) string {
	t.Helper()
	if _, err := h.identity.PutAPIKey(t.Context(), store.APIKey{Name: "P208 global browse", AccessToken: token, DateCreated: h.clock.Now(), DateLastActivity: h.clock.Now()}); err != nil {
		t.Fatalf("persist trusted P208 API key: %v", err)
	}
	return token
}

func (h *p208APIKeyHarness) issueMemberToken(t *testing.T, userID dto.UUID) string {
	t.Helper()
	device, err := h.users.IssueToken(t.Context(), store.Device{UserID: userID, AppName: "P208 Tests", AppVersion: "1.0", DeviceName: "P208 Member", DeviceID: "p208-member-device"})
	if err != nil {
		t.Fatalf("issue P208 member device token: %v", err)
	}
	if device.AccessToken == "" {
		t.Fatal("issued P208 member device token is empty")
	}
	return device.AccessToken
}

func p208APIKeyRequest(handler http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, p208AppendQuery(path, "ApiKey", token), nil)
	req.RemoteAddr = "198.51.100.80:8096"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func p208MemberRequest(handler http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "198.51.100.80:8096"
	req.Header.Set("Authorization", `MediaBrowser Token="`+token+`"`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func p208AppendQuery(path, key, value string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + key + "=" + value
}

type p208APIKeyNetwork struct{}

func (p208APIKeyNetwork) IsInLocalNetwork(netip.Addr) bool { return false }

type p208TargetingRoute struct {
	name string
	path func(dto.UUID) string
}

func p208TargetingRoutes(itemID dto.UUID) []p208TargetingRoute {
	query := func(path string) func(dto.UUID) string {
		return func(userID dto.UUID) string {
			separator := "?"
			if strings.Contains(path, "?") {
				separator = "&"
			}
			return path + separator + "userId=" + userID.Dashed()
		}
	}
	legacy := func(path string) func(dto.UUID) string {
		return func(userID dto.UUID) string { return "/Users/" + userID.Dashed() + path }
	}
	return []p208TargetingRoute{
		{name: "Items", path: query("/Items")},
		{name: "Items legacy", path: legacy("/Items")},
		{name: "Item", path: query("/Items/" + itemID.Dashed())},
		{name: "Item legacy", path: legacy("/Items/" + itemID.Dashed())},
		{name: "Latest", path: query("/Items/Latest?groupItems=false")},
		{name: "Latest legacy", path: legacy("/Items/Latest?groupItems=false")},
		{name: "Resume", path: query("/UserItems/Resume")},
		{name: "Resume legacy", path: legacy("/Items/Resume")},
		{name: "Similar", path: query("/Items/" + itemID.Dashed() + "/Similar")},
		{name: "Local trailers", path: query("/Items/" + itemID.Dashed() + "/LocalTrailers")},
		{name: "Local trailers legacy", path: legacy("/Items/" + itemID.Dashed() + "/LocalTrailers")},
		{name: "Special features", path: query("/Items/" + itemID.Dashed() + "/SpecialFeatures")},
		{name: "Special features legacy", path: legacy("/Items/" + itemID.Dashed() + "/SpecialFeatures")},
		{name: "Intros", path: query("/Items/" + itemID.Dashed() + "/Intros")},
		{name: "Intros legacy", path: legacy("/Items/" + itemID.Dashed() + "/Intros")},
		{name: "Additional parts", path: query("/Videos/" + itemID.Dashed() + "/AdditionalParts")},
		{name: "Next up", path: query("/Shows/NextUp")},
		{name: "Show seasons", path: query("/Shows/" + itemID.Dashed() + "/Seasons")},
		{name: "Show episodes", path: query("/Shows/" + itemID.Dashed() + "/Episodes")},
		{name: "Genres", path: query("/Genres")},
		{name: "Persons", path: query("/Persons")},
		{name: "Studios", path: query("/Studios")},
		{name: "Query filters", path: query("/Items/Filters")},
	}
}

type p208TargetingReader struct {
	itemID  dto.UUID
	calls   int
	last    store.ItemQuery
	queries []store.ItemQuery
}

func (r *p208TargetingReader) reset() {
	r.calls = 0
	r.last = store.ItemQuery{}
	r.queries = nil
}

func (r *p208TargetingReader) Query(_ context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	r.calls++
	r.last = query
	r.queries = append(r.queries, query)
	kind := item.KindMovie
	if len(query.IncludeItemTypes) == 1 {
		kind = query.IncludeItemTypes[0]
	}
	return store.ItemQueryResult{Items: []item.Item{{ID: r.itemID, Kind: kind}}}, nil
}

type p208TargetingBuilder struct{}

func (p208TargetingBuilder) Build(_ context.Context, values []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	result := dto.QueryResult[dto.BaseItemDto]{Items: make([]dto.BaseItemDto, len(values))}
	for index, value := range values {
		result.Items[index] = dto.BaseItemDto{ID: value.ID}
	}
	return result, nil
}

func (p208TargetingBuilder) BuildDetailed(ctx context.Context, values []item.Item, options store.ItemQueryDTOOptions, userID *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error) {
	return p208TargetingBuilder{}.Build(ctx, values, options, userID)
}

type p208TargetingNextUp struct {
	itemID dto.UUID
	calls  int
	last   store.NextUpQuery
}

func (r *p208TargetingNextUp) reset() {
	r.calls = 0
	r.last = store.NextUpQuery{}
}

func (r *p208TargetingNextUp) NextUp(_ context.Context, query store.NextUpQuery) (store.NextUpResult, error) {
	r.calls++
	r.last = query
	return store.NextUpResult{Items: []item.Item{{ID: r.itemID, Kind: item.KindEpisode}}}, nil
}

func p208TargetingReadCount(reader *p208TargetingReader, nextUp *p208TargetingNextUp) int {
	return reader.calls + nextUp.calls
}

func p208TargetingSelectedUser(reader *p208TargetingReader, nextUp *p208TargetingNextUp, userID dto.UUID) bool {
	if nextUp.calls != 0 && nextUp.last.UserID == userID && nextUp.last.User != nil && nextUp.last.User.ID == userID {
		return true
	}
	for _, query := range reader.queries {
		if query.UserID != nil && *query.UserID == userID && query.User != nil && query.User.ID == userID {
			return true
		}
	}
	return false
}

func p208TargetingUUID(t *testing.T, value string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse P2-08 UUID %q: %v", value, err)
	}
	return id
}
