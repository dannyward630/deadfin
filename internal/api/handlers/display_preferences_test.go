// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/displaypreferences"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestDisplayPreferenceRoutesPinUpstreamLedger(t *testing.T) {
	rt, _ := newDisplayPreferenceRouter(t, authenticatedDisplayUser())

	for _, want := range []router.Route{
		{Method: http.MethodGet, Pattern: "/DisplayPreferences/{displayPreferencesId}", OperationID: "GetDisplayPreferences", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/DisplayPreferences/{displayPreferencesId}", OperationID: "UpdateDisplayPreferences", Policy: string(auth.PolicyDefaultAuthorization)},
	} {
		t.Run(want.OperationID, func(t *testing.T) {
			got, found := routeByOperation(rt.Routes(), want.OperationID)
			if !found {
				t.Fatalf("operation %q is not registered", want.OperationID)
			}
			if got.Method != want.Method || got.Pattern != want.Pattern || got.Policy != want.Policy || got.Anonymous {
				t.Fatalf("route %q = %#v, want %#v and non-anonymous", want.OperationID, got, want)
			}
		})
	}
}

func TestDisplayPreferenceGetUsesAuthenticatedUserAndPassesRawRouteIDToService(t *testing.T) {
	owner := authenticatedDisplayUser()
	rt, persistence := newDisplayPreferenceRouter(t, owner)
	const path = "/dIsPlAyPrEfErEnCeS/usersettings?client=Web"

	for _, tc := range []struct {
		name      string
		accept    string
		idField   string
		clientKey string
		wrongID   string
	}{
		{name: "pascal", idField: "Id", clientKey: "Client", wrongID: "id"},
		{name: "camel", accept: `application/json; profile="CamelCase"`, idField: "id", clientKey: "client", wrongID: "Id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodGet, path, "", "", tc.accept)
			assertStatus(t, rec, http.StatusOK)
			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("GET response JSON: %v; body=%q", err, rec.Body.String())
			}
			if _, found := body[tc.wrongID]; found {
				t.Fatalf("GET %s response unexpectedly contains %q: %s", tc.name, tc.wrongID, rec.Body.String())
			}
			assertJSONFields(t, body, map[string]any{
				tc.idField:   mustDisplayID(t, "usersettings").Dashed(),
				tc.clientKey: "Web",
			})
		})
	}
	if got := persistence.getCount(); got != 2 {
		t.Fatalf("GET display preference store reads = %d, want one read per successful request", got)
	}
}

func TestDisplayPreferenceMissingDocumentUsesExactFreshStockDefaults(t *testing.T) {
	owner := authenticatedDisplayUser()
	rt, _ := newDisplayPreferenceRouter(t, owner)
	const client = "JellyportReplay"

	for _, tc := range []struct {
		name        string
		accept      string
		contentType string
		camel       bool
	}{
		{name: "Pascal", contentType: "application/json; charset=utf-8"},
		{name: "CamelCase", accept: `application/json; profile="CamelCase"`, contentType: `application/json; profile="CamelCase"; charset=utf-8`, camel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodGet, "/DisplayPreferences/usersettings?client="+client, "", "", tc.accept)
			assertStatus(t, rec, http.StatusOK)
			assertJSONResponseWithContentType(t, rec, missingDisplayPreferenceWireFields(t, tc.camel), tc.contentType)
		})
	}
}

// Jellyfin's DisplayPreferencesDto renders enum values as names and omits
// unset nullable fields. This is the persisted source document captured by the
// login replay before Deadfin writes a preference of its own.
func TestDisplayPreferenceInitialReadPreservesSourceDTOWireShape(t *testing.T) {
	owner := authenticatedDisplayUser()
	rt, persistence := newDisplayPreferenceRouter(t, owner)
	const client = "JellyportReplay"
	seedDisplayPreference(t, persistence, owner.UserID, "usersettings", client, sourceDisplayPreferenceWireFields(t, false))

	for _, tc := range []struct {
		name        string
		accept      string
		contentType string
		camel       bool
	}{
		{name: "Pascal", contentType: "application/json; charset=utf-8"},
		{name: "CamelCase", accept: `application/json; profile="CamelCase"`, contentType: `application/json; profile="CamelCase"; charset=utf-8`, camel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodGet, "/DisplayPreferences/usersettings?client="+client, "", "", tc.accept)
			assertStatus(t, rec, http.StatusOK)
			assertJSONResponseWithContentType(t, rec, sourceDisplayPreferenceWireFields(t, tc.camel), tc.contentType)
		})
	}
}

// A seeded login client has already persisted every source custom preference
// before the replay reads it. The read must retain that complete document;
// defaults or a partial later write must not replace the seeded keys.
func TestDisplayPreferenceSeededLoginReadRetainsEveryPersistedCustomPreference(t *testing.T) {
	owner := authenticatedDisplayUser()
	rt, persistence := newDisplayPreferenceRouter(t, owner)
	const client = "P0-05-AuthReplay"
	seeded := sourceDisplayPreferenceWireFields(t, false)
	seeded["Client"] = client
	seedDisplayPreference(t, persistence, owner.UserID, "usersettings", client, seeded)

	rec := request(rt, http.MethodGet, "/DisplayPreferences/usersettings?client="+client, "", "", `application/json; profile="CamelCase"`)
	assertStatus(t, rec, http.StatusOK)
	want := sourceDisplayPreferenceWireFields(t, true)
	want["client"] = client
	assertJSONResponseWithContentType(t, rec, want, `application/json; profile="CamelCase"; charset=utf-8`)
}

// An explicit client write must remain exact on the following reads; request
// casing only changes the negotiated field names, never persisted values.
func TestDisplayPreferenceRoundTripRendersExactSavedWireFields(t *testing.T) {
	owner := authenticatedDisplayUser()
	rt, _ := newDisplayPreferenceRouter(t, owner)
	const client = "JellyportReplay"
	body, err := json.Marshal(sourceDisplayPreferenceWireFields(t, false))
	if err != nil {
		t.Fatalf("marshal saved display preferences: %v", err)
	}

	post := request(rt, http.MethodPost, "/DisplayPreferences/usersettings?client="+client, string(body), "application/json", `application/json; profile="CamelCase"`)
	assertStatus(t, post, http.StatusNoContent)

	for _, tc := range []struct {
		name        string
		accept      string
		contentType string
		camel       bool
	}{
		{name: "Pascal", contentType: "application/json; charset=utf-8"},
		{name: "CamelCase", accept: `application/json; profile="CamelCase"`, contentType: `application/json; profile="CamelCase"; charset=utf-8`, camel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodGet, "/DisplayPreferences/usersettings?client="+client, "", "", tc.accept)
			assertStatus(t, rec, http.StatusOK)
			assertJSONResponseWithContentType(t, rec, sourceDisplayPreferenceWireFields(t, tc.camel), tc.contentType)
		})
	}
}

// Jellyfin merges the managed usersettings CustomPrefs defaults into a partial
// HTTP update. That request-time behavior must not change Service.Get's
// treatment of a directly persisted partial document as opaque client state.
func TestDisplayPreferenceHTTPPostMergesManagedCustomDefaults(t *testing.T) {
	owner := authenticatedDisplayUser()
	rt, _ := newDisplayPreferenceRouter(t, owner)
	const client = "P0-05-AuthReplay"
	const path = "/DisplayPreferences/usersettings?client=" + client

	post := request(rt, http.MethodPost, path, `{
		"SortBy":"DateCreated",
		"RememberSorting":true,
		"CustomPrefs":{"replay":"login","third-party":null}
	}`, "application/json", `application/json; profile="CamelCase"`)
	assertStatus(t, post, http.StatusNoContent)

	get := request(rt, http.MethodGet, path, "", "", `application/json; profile="CamelCase"`)
	assertStatus(t, get, http.StatusOK)
	want := sourceDisplayPreferenceWireFields(t, true)
	want["client"] = client
	customPrefs := sourceDisplayPreferenceCustomPrefs()
	customPrefs["third-party"] = nil
	want["customPrefs"] = customPrefs
	assertJSONResponseWithContentType(t, get, want, `application/json; profile="CamelCase"; charset=utf-8`)
}

// Display preference enum members are named JSON values. Every supported pair
// must survive the public POST-to-GET boundary; unknown names must leave the
// previously stored document untouched.
func TestDisplayPreferencePostValidatesAndPersistsNamedEnums(t *testing.T) {
	owner := authenticatedDisplayUser()
	const path = "/DisplayPreferences/usersettings?client=Web"

	for _, tc := range []struct {
		name            string
		scrollDirection string
		sortOrder       string
	}{
		{name: "horizontal ascending", scrollDirection: "Horizontal", sortOrder: "Ascending"},
		{name: "horizontal descending", scrollDirection: "Horizontal", sortOrder: "Descending"},
		{name: "vertical ascending", scrollDirection: "Vertical", sortOrder: "Ascending"},
		{name: "vertical descending", scrollDirection: "Vertical", sortOrder: "Descending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := newDisplayPreferenceRouter(t, owner)
			body := `{"ScrollDirection":"` + tc.scrollDirection + `","SortOrder":"` + tc.sortOrder + `"}`

			post := request(rt, http.MethodPost, path, body, "application/json", "")
			assertStatus(t, post, http.StatusNoContent)

			get := request(rt, http.MethodGet, path, "", "", "")
			assertStatus(t, get, http.StatusOK)
			var persisted map[string]json.RawMessage
			if err := json.Unmarshal(get.Body.Bytes(), &persisted); err != nil {
				t.Fatalf("decode persisted display preferences: %v; body=%q", err, get.Body.String())
			}
			assertJSONFields(t, persisted, map[string]any{
				"ScrollDirection": tc.scrollDirection,
				"SortOrder":       tc.sortOrder,
			})
		})
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "unknown scroll direction", body: `{"ScrollDirection":"Diagonal","SortOrder":"Ascending"}`},
		{name: "unknown sort order", body: `{"ScrollDirection":"Horizontal","SortOrder":"NewestFirst"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := newDisplayPreferenceRouter(t, owner)
			baseline := request(rt, http.MethodPost, path, `{"ScrollDirection":"Vertical","SortOrder":"Descending"}`, "application/json", "")
			assertStatus(t, baseline, http.StatusNoContent)

			rejected := request(rt, http.MethodPost, path, tc.body, "application/json", "")
			assertStatus(t, rejected, http.StatusBadRequest)

			get := request(rt, http.MethodGet, path, "", "", "")
			assertStatus(t, get, http.StatusOK)
			var persisted map[string]json.RawMessage
			if err := json.Unmarshal(get.Body.Bytes(), &persisted); err != nil {
				t.Fatalf("decode display preferences after rejected update: %v; body=%q", err, get.Body.String())
			}
			assertJSONFields(t, persisted, map[string]any{
				"ScrollDirection": "Vertical",
				"SortOrder":       "Descending",
			})
		})
	}
}

func sourceDisplayPreferenceWireFields(t *testing.T, camel bool) map[string]any {
	t.Helper()
	if camel {
		return map[string]any{
			"client":             "JellyportReplay",
			"customPrefs":        sourceDisplayPreferenceCustomPrefs(),
			"id":                 mustDisplayID(t, "usersettings").Dashed(),
			"primaryImageHeight": 250,
			"primaryImageWidth":  250,
			"rememberIndexing":   false,
			"rememberSorting":    true,
			"scrollDirection":    "Horizontal",
			"showBackdrop":       true,
			"showSidebar":        false,
			"sortBy":             "DateCreated",
			"sortOrder":          "Ascending",
		}
	}
	return map[string]any{
		"Client":             "JellyportReplay",
		"CustomPrefs":        sourceDisplayPreferenceCustomPrefs(),
		"Id":                 mustDisplayID(t, "usersettings").Dashed(),
		"PrimaryImageHeight": 250,
		"PrimaryImageWidth":  250,
		"RememberIndexing":   false,
		"RememberSorting":    true,
		"ScrollDirection":    "Horizontal",
		"ShowBackdrop":       true,
		"ShowSidebar":        false,
		"SortBy":             "DateCreated",
		"SortOrder":          "Ascending",
	}
}

func missingDisplayPreferenceWireFields(t *testing.T, camel bool) map[string]any {
	t.Helper()
	return freshDisplayPreferenceWireFields(t, camel)
}

func freshDisplayPreferenceWireFields(t *testing.T, camel bool) map[string]any {
	t.Helper()
	customPrefs := map[string]any{
		"chromecastVersion":          "stable",
		"skipForwardLength":          "30000",
		"skipBackLength":             "10000",
		"enableNextVideoInfoOverlay": "False",
		"tvhome":                     nil,
		"dashboardTheme":             nil,
	}
	if camel {
		return map[string]any{
			"client": "JellyportReplay", "customPrefs": customPrefs, "id": mustDisplayID(t, "usersettings").Dashed(),
			"primaryImageHeight": 250, "primaryImageWidth": 250, "rememberIndexing": false, "rememberSorting": false,
			"scrollDirection": "Horizontal", "showBackdrop": true, "showSidebar": false, "sortBy": "SortName", "sortOrder": "Ascending",
		}
	}
	return map[string]any{
		"Client": "JellyportReplay", "CustomPrefs": customPrefs, "Id": mustDisplayID(t, "usersettings").Dashed(),
		"PrimaryImageHeight": 250, "PrimaryImageWidth": 250, "RememberIndexing": false, "RememberSorting": false,
		"ScrollDirection": "Horizontal", "ShowBackdrop": true, "ShowSidebar": false, "SortBy": "SortName", "SortOrder": "Ascending",
	}
}

func sourceDisplayPreferenceCustomPrefs() map[string]any {
	return map[string]any{
		"chromecastVersion":          "stable",
		"dashboardTheme":             "",
		"enableNextVideoInfoOverlay": "True",
		"replay":                     "login",
		"skipBackLength":             "15000",
		"skipForwardLength":          "15000",
		"tvhome":                     "",
	}
}

func seedDisplayPreference(t *testing.T, persistence *displayPreferenceStore, userID dto.UUID, routeID, client string, document map[string]any) {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode source display-preference document: %v", err)
	}
	if err := persistence.PutDisplayPreference(context.Background(), store.DisplayPreference{
		UserID: userID,
		ItemID: mustDisplayID(t, routeID),
		Client: client,
		Data:   data,
	}); err != nil {
		t.Fatalf("seed source display-preference document: %v", err)
	}
}

func TestDisplayPreferenceQueryAndAuthorizationFailuresDoNotAccessStore(t *testing.T) {
	owner := authenticatedDisplayUser()
	for _, tc := range []struct {
		name     string
		identity auth.Identity
		path     string
		status   int
	}{
		{name: "missing required client", identity: owner, path: "/DisplayPreferences/usersettings", status: http.StatusBadRequest},
		{name: "blank required client", identity: owner, path: "/DisplayPreferences/usersettings?client=", status: http.StatusBadRequest},
		{name: "malformed supplied user ID", identity: owner, path: "/DisplayPreferences/usersettings?client=Web&userId=not-a-guid", status: http.StatusBadRequest},
		{name: "unauthenticated", identity: auth.Identity{}, path: "/DisplayPreferences/usersettings?client=Web", status: http.StatusUnauthorized},
		{name: "cross-user access", identity: owner, path: "/DisplayPreferences/usersettings?client=Web&userId=bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, persistence := newDisplayPreferenceRouter(t, tc.identity)
			rec := request(rt, http.MethodGet, tc.path, "", "", "")
			assertStatus(t, rec, tc.status)
			if got := persistence.accesses(); got != 0 {
				t.Fatalf("GET %s accessed display-preference store %d times", tc.name, got)
			}
		})
	}
}

func TestDisplayPreferencePostRejectsInvalidQueryAndCrossUserBeforePersistence(t *testing.T) {
	owner := authenticatedDisplayUser()
	for _, tc := range []struct {
		name   string
		path   string
		status int
	}{
		{name: "missing required client", path: "/DisplayPreferences/usersettings", status: http.StatusBadRequest},
		{name: "blank required client", path: "/DisplayPreferences/usersettings?client=", status: http.StatusBadRequest},
		{name: "malformed supplied user ID", path: "/DisplayPreferences/usersettings?client=Web&userId=not-a-guid", status: http.StatusBadRequest},
		{name: "cross-user write", path: "/DisplayPreferences/usersettings?client=Web&userId=bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, persistence := newDisplayPreferenceRouter(t, owner)
			rec := request(rt, http.MethodPost, tc.path, `{"SortBy":"DateCreated"}`, "application/json", "")
			assertStatus(t, rec, tc.status)
			if got := persistence.accesses(); got != 0 {
				t.Fatalf("POST %s accessed display-preference store %d times", tc.name, got)
			}
		})
	}
}

// RequestHelpers permits an administrator to select another user's preferences,
// while an ordinary caller remains confined to its own document. The selected
// user is part of the persistence key, so a successful cross-user write must
// be observable only through that target's subsequent read.
func TestDisplayPreferenceTargetSelectionHonorsAdministratorAndSelfAccess(t *testing.T) {
	owner := authenticatedDisplayUser()
	target := mustDisplayID(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	const client = "Web"

	t.Run("ordinary caller cannot select another user", func(t *testing.T) {
		rt, persistence := newDisplayPreferenceRouter(t, owner)
		rec := request(rt, http.MethodGet, "/DisplayPreferences/usersettings?client="+client+"&userId="+target.Dashed(), "", "", "")
		assertStatus(t, rec, http.StatusForbidden)
		if got := persistence.accesses(); got != 0 {
			t.Fatalf("cross-user request accessed display-preference store %d times", got)
		}
	})

	t.Run("administrator can select target and caller can select self", func(t *testing.T) {
		administrator := owner
		administrator.Administrator = true
		rt, persistence := newDisplayPreferenceRouter(t, administrator)
		seedDisplayPreference(t, persistence, target, "usersettings", client, map[string]any{"SortBy": "TargetInitial"})

		readTarget := request(rt, http.MethodGet, "/DisplayPreferences/usersettings?client="+client+"&userId="+target.Dashed(), "", "", "")
		assertStatus(t, readTarget, http.StatusOK)
		var targetBefore map[string]json.RawMessage
		if err := json.Unmarshal(readTarget.Body.Bytes(), &targetBefore); err != nil {
			t.Fatalf("decode administrator target response: %v", err)
		}
		assertJSONFields(t, targetBefore, map[string]any{"Client": client, "SortBy": "TargetInitial"})

		writeTarget := request(rt, http.MethodPost, "/DisplayPreferences/usersettings?client="+client+"&userId="+target.Dashed(), `{"SortBy":"TargetUpdated"}`, "application/json", "")
		assertStatus(t, writeTarget, http.StatusNoContent)
		readTarget = request(rt, http.MethodGet, "/DisplayPreferences/usersettings?client="+client+"&userId="+target.Dashed(), "", "", "")
		assertStatus(t, readTarget, http.StatusOK)
		var targetAfter map[string]json.RawMessage
		if err := json.Unmarshal(readTarget.Body.Bytes(), &targetAfter); err != nil {
			t.Fatalf("decode updated administrator target response: %v", err)
		}
		assertJSONFields(t, targetAfter, map[string]any{"Client": client, "SortBy": "TargetUpdated"})

		writeSelf := request(rt, http.MethodPost, "/DisplayPreferences/usersettings?client="+client, `{"SortBy":"SelfUpdated"}`, "application/json", "")
		assertStatus(t, writeSelf, http.StatusNoContent)
		readSelf := request(rt, http.MethodGet, "/DisplayPreferences/usersettings?client="+client, "", "", "")
		assertStatus(t, readSelf, http.StatusOK)
		var self map[string]json.RawMessage
		if err := json.Unmarshal(readSelf.Body.Bytes(), &self); err != nil {
			t.Fatalf("decode self response: %v", err)
		}
		assertJSONFields(t, self, map[string]any{"Client": client, "SortBy": "SelfUpdated"})
	})
}

func TestDisplayPreferencePostBindsDocumentAndReturnsNoContent(t *testing.T) {
	owner := authenticatedDisplayUser()
	rt, persistence := newDisplayPreferenceRouter(t, owner)
	const path = "/DisplayPreferences/usersettings?client=Web"
	body := `{
		"Id":"untrusted",
		"Client":"untrusted",
		"SortBy":"DateCreated",
		"RememberSorting":true,
		"CustomPrefs":{"third-party":"saved"}
	}`

	rec := request(rt, http.MethodPost, path, body, "application/json", "")
	assertStatus(t, rec, http.StatusNoContent)
	if got := persistence.putCount(); got != 1 {
		t.Fatalf("POST display preference store writes = %d, want 1", got)
	}

	rec = request(rt, http.MethodGet, path, "", "", "")
	assertStatus(t, rec, http.StatusOK)
	var response map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("GET persisted response JSON: %v", err)
	}
	assertJSONFields(t, response, map[string]any{
		"Id":              mustDisplayID(t, "usersettings").Dashed(),
		"Client":          "Web",
		"SortBy":          "DateCreated",
		"RememberSorting": true,
	})
	var custom map[string]*string
	if err := json.Unmarshal(response["CustomPrefs"], &custom); err != nil {
		t.Fatalf("GET CustomPrefs: %v", err)
	}
	if got := custom["third-party"]; got == nil || *got != "saved" {
		t.Fatalf("GET CustomPrefs third-party = %#v, want saved", got)
	}
}

func TestDisplayPreferencePostMapsBindingValidationAndStoreFailures(t *testing.T) {
	owner := authenticatedDisplayUser()
	const path = "/DisplayPreferences/usersettings?client=Web"
	for _, tc := range []struct {
		name           string
		body           string
		configureStore func(*displayPreferenceStore)
		status         int
	}{
		{name: "missing required body", status: http.StatusBadRequest},
		{name: "malformed JSON", body: `{"SortBy":`, status: http.StatusBadRequest},
		{name: "literal custom preference values", body: `{"CustomPrefs":{"skipForwardLength":"nope"}}`, status: http.StatusNoContent},
		{
			name: "store failure",
			body: `{"SortBy":"DateCreated"}`,
			configureStore: func(persistence *displayPreferenceStore) {
				persistence.err = errors.New("database unavailable")
			},
			status: http.StatusInternalServerError,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, persistence := newDisplayPreferenceRouter(t, owner)
			if tc.configureStore != nil {
				tc.configureStore(persistence)
			}
			rec := request(rt, http.MethodPost, path, tc.body, "application/json", "")
			assertStatus(t, rec, tc.status)
			if tc.status == http.StatusBadRequest && persistence.putCount() != 0 {
				t.Fatalf("rejected %s POST wrote a preference", tc.name)
			}
		})
	}
}

type displayPreferenceStore struct {
	mu          sync.Mutex
	preferences map[displayPreferenceKey]store.DisplayPreference
	err         error
	gets        int
	puts        int
}

type displayPreferenceKey struct {
	userID dto.UUID
	itemID dto.UUID
	client string
}

func newDisplayPreferenceStore() *displayPreferenceStore {
	return &displayPreferenceStore{preferences: make(map[displayPreferenceKey]store.DisplayPreference)}
}

func (persistence *displayPreferenceStore) GetDisplayPreference(_ context.Context, userID, itemID dto.UUID, client string) (store.DisplayPreference, error) {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	persistence.gets++
	if persistence.err != nil {
		return store.DisplayPreference{}, persistence.err
	}
	preference, found := persistence.preferences[displayPreferenceKey{userID: userID, itemID: itemID, client: client}]
	if !found {
		return store.DisplayPreference{}, store.ErrNotFound
	}
	preference.Data = append([]byte(nil), preference.Data...)
	return preference, nil
}

func (persistence *displayPreferenceStore) PutDisplayPreference(_ context.Context, preference store.DisplayPreference) error {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	persistence.puts++
	if persistence.err != nil {
		return persistence.err
	}
	preference.Data = append([]byte(nil), preference.Data...)
	persistence.preferences[displayPreferenceKey{userID: preference.UserID, itemID: preference.ItemID, client: preference.Client}] = preference
	return nil
}

func (persistence *displayPreferenceStore) accesses() int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return persistence.gets + persistence.puts
}

func (persistence *displayPreferenceStore) getCount() int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return persistence.gets
}

func (persistence *displayPreferenceStore) putCount() int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return persistence.puts
}

type displayPreferenceResolver struct{ identity auth.Identity }

func (resolver displayPreferenceResolver) Resolve(context.Context, *http.Request) (auth.Identity, error) {
	return resolver.identity, nil
}

type displayPreferenceUsers struct{}

func (displayPreferenceUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	return store.User{ID: id, Policy: []byte(`{}`)}, nil
}

func newDisplayPreferenceRouter(t *testing.T, identity auth.Identity) (*router.Router, *displayPreferenceStore) {
	t.Helper()
	_, manager, _ := newManagedRouter(t)
	persistence := newDisplayPreferenceStore()
	service, err := displaypreferences.New(persistence)
	if err != nil {
		t.Fatalf("New display preference service: %v", err)
	}
	evaluator := auth.NewEvaluator(manager, startupUserLocalNetwork{}, startupUserSchedules{}, func() time.Time { return time.Unix(0, 0).UTC() })
	authorizer := middleware.NewAuthorizer(displayPreferenceResolver{identity: identity}, evaluator, displayPreferenceUsers{})
	rt := router.New(authorizer.Wrap)
	handlers.RegisterDisplayPreferences(rt, service)
	return rt, persistence
}

func authenticatedDisplayUser() auth.Identity {
	return auth.Identity{Authenticated: true, UserID: mustDisplayIDValue("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}
}

func mustDisplayID(t *testing.T, value string) dto.UUID {
	t.Helper()
	return mustDisplayIDValue(value)
}

func mustDisplayIDValue(value string) dto.UUID {
	id, err := displaypreferences.ResolveID(value)
	if err != nil {
		panic(err)
	}
	return id
}
