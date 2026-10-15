// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/user"
)

var libraryManagementID = dto.UUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

type libraryManagementCall struct {
	operation      string
	id             dto.UUID
	name           string
	collectionType string
	path           string
	options        library.LibraryOptions
}

type fakeLibraryTopology struct {
	folders       []library.VirtualFolder
	calls         []libraryManagementCall
	listErr       error
	addErr        error
	removeErr     error
	addPathErr    error
	removePathErr error
	updateErr     error
}

func (f *fakeLibraryTopology) AddVirtualFolder(_ context.Context, name, collectionType string, options library.LibraryOptions) (library.VirtualFolder, error) {
	f.calls = append(f.calls, libraryManagementCall{operation: "add folder", name: name, collectionType: collectionType, options: options})
	return library.VirtualFolder{}, f.addErr
}

func (f *fakeLibraryTopology) RemoveVirtualFolder(_ context.Context, id dto.UUID) error {
	f.calls = append(f.calls, libraryManagementCall{operation: "remove folder", id: id})
	return f.removeErr
}

func (f *fakeLibraryTopology) AddMediaPath(_ context.Context, id dto.UUID, path string) error {
	f.calls = append(f.calls, libraryManagementCall{operation: "add path", id: id, path: path})
	return f.addPathErr
}

func (f *fakeLibraryTopology) RemoveMediaPath(_ context.Context, id dto.UUID, path string) error {
	f.calls = append(f.calls, libraryManagementCall{operation: "remove path", id: id, path: path})
	return f.removePathErr
}

func (f *fakeLibraryTopology) UpdateLibraryOptions(_ context.Context, id dto.UUID, options library.LibraryOptions) error {
	f.calls = append(f.calls, libraryManagementCall{operation: "update options", id: id, options: options})
	return f.updateErr
}

func (f *fakeLibraryTopology) VirtualFolders(context.Context) ([]library.VirtualFolder, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]library.VirtualFolder(nil), f.folders...), nil
}

type fakeLibraryRefreshTrigger struct {
	calls int
	err   error
}

func (f *fakeLibraryRefreshTrigger) Trigger() error {
	f.calls++
	return f.err
}

func (f *fakeLibraryRefreshTrigger) RefreshStates() map[dto.UUID]library.FolderRefreshState {
	return map[dto.UUID]library.FolderRefreshState{}
}

// alwaysLocalNetwork reports every address as local so the evaluator's
// remote-access gate never blocks the composed-router policy probe.
type alwaysLocalNetwork struct{}

func (alwaysLocalNetwork) IsInLocalNetwork(netip.Addr) bool { return true }

func TestLibraryManagementRoutesRegisterStockMetadata(t *testing.T) {
	rt := router.New()
	handlers.RegisterLibrary(rt, nil, &fakeLibraryTopology{})

	want := map[string]string{
		"GET /Library/VirtualFolders":                 "GetVirtualFolders",
		"POST /Library/VirtualFolders":                "AddVirtualFolder",
		"DELETE /Library/VirtualFolders":              "RemoveVirtualFolder",
		"POST /Library/VirtualFolders/Paths":          "AddMediaPath",
		"DELETE /Library/VirtualFolders/Paths":        "RemoveMediaPath",
		"POST /Library/VirtualFolders/LibraryOptions": "UpdateLibraryOptions",
	}
	for _, route := range rt.Routes() {
		key := route.Method + " " + route.Pattern
		operationID, found := want[key]
		if !found {
			continue
		}
		if route.OperationID != operationID || route.Policy != string(auth.PolicyFirstTimeSetupOrElevated) || route.Anonymous {
			t.Fatalf("route %s metadata = %#v, want operation %q and FirstTimeSetupOrElevated", key, route, operationID)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing library management routes: %#v", want)
	}
}

func TestLibraryManagementEndpointsBindAndDelegate(t *testing.T) {
	collectionFolderID := dto.UUID{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80, 0x90, 0xa0}
	folder := library.VirtualFolder{
		ID:                 libraryManagementID,
		CollectionFolderID: collectionFolderID,
		Name:               "Cinema",
		CollectionType:     "movies",
		Options:            library.LibraryOptions{Enabled: true},
		MediaPaths:         []string{"/media/one"},
	}
	topology := &fakeLibraryTopology{folders: []library.VirtualFolder{folder}}
	trigger := &fakeLibraryRefreshTrigger{}
	rt := router.New()
	handlers.RegisterLibrary(rt, trigger, topology)

	t.Run("get stock virtual folder shape and case-insensitive route", func(t *testing.T) {
		response := libraryRequest(t, rt, http.MethodGet, "/lIbRaRy/vIrTuAlFoLdErS", "", "")
		assertLibraryStatus(t, response, http.StatusOK)
		var body []map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode virtual folders: %v", err)
		}
		if len(body) != 1 || body[0]["Name"] != "Cinema" || body[0]["CollectionType"] != "movies" || body[0]["ItemId"] != collectionFolderID.String() {
			t.Fatalf("virtual folder response = %#v", body)
		}
		if got := body[0]["Locations"]; !reflect.DeepEqual(got, []any{"/media/one"}) {
			t.Fatalf("Locations = %#v, want one media path", got)
		}
		// Stock omits nullable response fields when absent and supplies
		// RefreshStatus (Idle) for a resolved folder. ItemId is the scanned
		// CollectionFolder identity, not the topology registry UUID.
		if _, present := body[0]["PrimaryImageItemId"]; present {
			t.Fatalf("virtual folder response included absent nullable PrimaryImageItemId: %#v", body[0])
		}
		if _, present := body[0]["RefreshProgress"]; present {
			t.Fatalf("virtual folder response included absent nullable RefreshProgress: %#v", body[0])
		}
		if body[0]["RefreshStatus"] != "Idle" {
			t.Fatalf("RefreshStatus = %#v, want Idle", body[0]["RefreshStatus"])
		}
		if _, present := body[0]["LibraryOptions"]; !present {
			t.Fatalf("virtual folder response omitted LibraryOptions: %#v", body[0])
		}
	})

	t.Run("add virtual folder query paths override body path infos", func(t *testing.T) {
		path := "/Library/VirtualFolders?NaMe=Shows&collectionTYPE=tvshows&paths=" + url.QueryEscape("/media/a,/media/b") + "&PATHS=" + url.QueryEscape("/media/c") + "&refreshLibrary=TRUE"
		response := libraryRequest(t, rt, http.MethodPost, path, `{"LibraryOptions":{"Enabled":true,"PathInfos":[{"Path":"/ignored"}]}}`, "application/json")
		assertLibraryStatus(t, response, http.StatusNoContent)
		call := topology.calls[len(topology.calls)-1]
		// The binder merges differently-cased aliases (paths/PATHS) in
		// url.Values map-iteration order, so the cross-alias sequence is
		// intentionally undefined; compare the bound path set, not its order.
		gotPaths := append([]library.MediaPathInfo(nil), call.options.PathInfos...)
		slices.SortFunc(gotPaths, func(a, b library.MediaPathInfo) int { return strings.Compare(a.Path, b.Path) })
		wantPaths := []library.MediaPathInfo{{Path: "/media/a"}, {Path: "/media/b"}, {Path: "/media/c"}}
		if call.operation != "add folder" || call.name != "Shows" || call.collectionType != "tvshows" || !call.options.Enabled || !reflect.DeepEqual(gotPaths, wantPaths) {
			t.Fatalf("add folder call = %#v, want bound query and overridden paths %#v", call, wantPaths)
		}
	})

	t.Run("add virtual folder omits nullable collection type", func(t *testing.T) {
		response := libraryRequest(t, rt, http.MethodPost, "/Library/VirtualFolders?name=Untyped", "", "")
		assertLibraryStatus(t, response, http.StatusNoContent)
		call := topology.calls[len(topology.calls)-1]
		if call.collectionType != "" {
			t.Fatalf("omitted collection type = %q, want empty (null) creating no .collection marker", call.collectionType)
		}
	})

	t.Run("add virtual folder accepts invalid collection type as null", func(t *testing.T) {
		response := libraryRequest(t, rt, http.MethodPost, "/Library/VirtualFolders?name=Bad&collectionType=photos", "", "")
		assertLibraryStatus(t, response, http.StatusNoContent)
		call := topology.calls[len(topology.calls)-1]
		// Stock's nullable enum binding treats unparseable values as null:
		// no 400, no .collection marker. The topology receives an empty
		// collectionType, matching stock's AddVirtualFolder(collectionType: null).
		if call.collectionType != "" {
			t.Fatalf("invalid collection type = %q, want empty (null) creating no .collection marker", call.collectionType)
		}
	})

	t.Run("add virtual folder accepts empty collection type as null", func(t *testing.T) {
		response := libraryRequest(t, rt, http.MethodPost, "/Library/VirtualFolders?name=Empty&collectionType=", "", "")
		assertLibraryStatus(t, response, http.StatusNoContent)
		call := topology.calls[len(topology.calls)-1]
		if call.collectionType != "" {
			t.Fatalf("empty collection type = %q, want empty (null) creating no .collection marker", call.collectionType)
		}
	})

	t.Run("add virtual folder binds each valid collection type", func(t *testing.T) {
		for _, valid := range []string{"movies", "tvshows", "music", "musicvideos", "homevideos", "boxsets", "books", "mixed"} {
			response := libraryRequest(t, rt, http.MethodPost, "/Library/VirtualFolders?name=Lib-"+valid+"&collectionType="+valid, "", "")
			assertLibraryStatus(t, response, http.StatusNoContent)
			call := topology.calls[len(topology.calls)-1]
			if call.collectionType != valid {
				t.Fatalf("valid collection type %q = %q, want exact marker", valid, call.collectionType)
			}
		}
	})

	t.Run("remove virtual folder resolves name case-insensitively", func(t *testing.T) {
		response := libraryRequest(t, rt, http.MethodDelete, "/Library/VirtualFolders?name=cinema&refreshLibrary=false", "", "")
		assertLibraryStatus(t, response, http.StatusNoContent)
		call := topology.calls[len(topology.calls)-1]
		if call.operation != "remove folder" || call.id != libraryManagementID {
			t.Fatalf("remove folder call = %#v", call)
		}
	})

	t.Run("add media path prefers PathInfo", func(t *testing.T) {
		response := libraryRequest(t, rt, http.MethodPost, "/Library/VirtualFolders/Paths?refreshLibrary=true", `{"Name":"Cinema","Path":"/legacy","PathInfo":{"Path":"/chosen"}}`, "text/json")
		assertLibraryStatus(t, response, http.StatusNoContent)
		call := topology.calls[len(topology.calls)-1]
		if call.operation != "add path" || call.id != libraryManagementID || call.path != "/chosen" {
			t.Fatalf("add path call = %#v", call)
		}
	})

	t.Run("remove media path binds case-insensitive query", func(t *testing.T) {
		response := libraryRequest(t, rt, http.MethodDelete, "/Library/VirtualFolders/Paths?NAME=Cinema&PATH="+url.QueryEscape("/media/one"), "", "")
		assertLibraryStatus(t, response, http.StatusNoContent)
		call := topology.calls[len(topology.calls)-1]
		if call.operation != "remove path" || call.id != libraryManagementID || call.path != "/media/one" {
			t.Fatalf("remove path call = %#v", call)
		}
	})

	t.Run("update library options binds scanned collection folder id", func(t *testing.T) {
		body := `{"Id":"` + collectionFolderID.Dashed() + `","LibraryOptions":{"Enabled":true,"PreferredMetadataLanguage":"ja"}}`
		response := libraryRequest(t, rt, http.MethodPost, "/Library/VirtualFolders/LibraryOptions", body, "application/vnd.deadfin+json")
		assertLibraryStatus(t, response, http.StatusNoContent)
		call := topology.calls[len(topology.calls)-1]
		if call.operation != "update options" || call.id != collectionFolderID || !call.options.Enabled || call.options.PreferredMetadataLanguage != "ja" {
			t.Fatalf("update options call = %#v, want scanned CollectionFolder id %s", call, collectionFolderID)
		}
	})
}

// TestLibraryManagementCollectionTypeSpelling covers the
// collectionType query value spelling matrix: numeric ordinals 0 through 7,
// the zero-padded "01" and signed "+5" spellings, mixed-case canonical names,
// surrounding whitespace, and rejected inputs (8, -1, 1.0, photos, empty, and
// comma lists). Accepted inputs must produce the canonical lowercase name.
// Rejected inputs must keep the null behavior: HTTP 204 and an empty
// topology collectionType that creates no .collection marker.
func TestLibraryManagementCollectionTypeSpelling(t *testing.T) {
	accepted := []struct {
		input string
		want  string
	}{
		{"0", "movies"},
		{"1", "tvshows"},
		{"2", "music"},
		{"3", "musicvideos"},
		{"4", "homevideos"},
		{"5", "boxsets"},
		{"6", "books"},
		{"7", "mixed"},
		{"01", "tvshows"},
		{"+5", "boxsets"},
		{"Movies", "movies"},
		{" TVSHOWS ", "tvshows"},
		{"MuSiC", "music"},
		{"movies", "movies"},
		{"tvshows", "tvshows"},
	}
	for _, test := range accepted {
		t.Run("accept "+test.input, func(t *testing.T) {
			topology := &fakeLibraryTopology{}
			rt := router.New()
			handlers.RegisterLibrary(rt, nil, topology)
			path := "/Library/VirtualFolders?name=Lib&collectionType=" + url.QueryEscape(test.input)
			response := libraryRequest(t, rt, http.MethodPost, path, "", "")
			assertLibraryStatus(t, response, http.StatusNoContent)
			call := topology.calls[len(topology.calls)-1]
			if call.collectionType != test.want {
				t.Fatalf("input %q = %q, want canonical %q", test.input, call.collectionType, test.want)
			}
		})
	}

	rejected := []string{"8", "-1", "1.0", "photos", "", "0,1"}
	for _, input := range rejected {
		t.Run("reject "+input, func(t *testing.T) {
			topology := &fakeLibraryTopology{}
			rt := router.New()
			handlers.RegisterLibrary(rt, nil, topology)
			path := "/Library/VirtualFolders?name=Lib&collectionType=" + url.QueryEscape(input)
			response := libraryRequest(t, rt, http.MethodPost, path, "", "")
			assertLibraryStatus(t, response, http.StatusNoContent)
			call := topology.calls[len(topology.calls)-1]
			if call.collectionType != "" {
				t.Fatalf("input %q = %q, want empty (null) creating no .collection marker", input, call.collectionType)
			}
		})
	}
}

func TestLibraryManagementRefreshFlagTriggersAfterEveryMutation(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body string
	}{
		{name: "add folder", method: http.MethodPost, path: "/Library/VirtualFolders?name=New&collectionType=movies&refreshLibrary=true"},
		{name: "remove folder", method: http.MethodDelete, path: "/Library/VirtualFolders?name=Cinema&refreshLibrary=true"},
		{name: "add path", method: http.MethodPost, path: "/Library/VirtualFolders/Paths?refreshLibrary=true", body: `{"Name":"Cinema","Path":"/media/new"}`},
		{name: "remove path", method: http.MethodDelete, path: "/Library/VirtualFolders/Paths?name=Cinema&path=%2Fmedia%2Fold&refreshLibrary=true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			topology := &fakeLibraryTopology{folders: []library.VirtualFolder{{ID: libraryManagementID, Name: "Cinema"}}}
			trigger := &fakeLibraryRefreshTrigger{}
			rt := router.New()
			handlers.RegisterLibrary(rt, trigger, topology)
			contentType := ""
			if test.body != "" {
				contentType = "application/json"
			}
			response := libraryRequest(t, rt, test.method, test.path, test.body, contentType)
			assertLibraryStatus(t, response, http.StatusNoContent)
			if trigger.calls != 1 {
				t.Fatalf("refresh trigger calls = %d, want 1", trigger.calls)
			}
			if len(topology.calls) != 1 {
				t.Fatalf("topology mutation calls = %#v, want one before refresh", topology.calls)
			}
		})
	}

	topology := &fakeLibraryTopology{}
	trigger := &fakeLibraryRefreshTrigger{}
	rt := router.New()
	handlers.RegisterLibrary(rt, trigger, topology)
	response := libraryRequest(t, rt, http.MethodPost, "/Library/VirtualFolders?name=New&collectionType=movies&refreshLibrary=false", "", "")
	assertLibraryStatus(t, response, http.StatusNoContent)
	if trigger.calls != 0 {
		t.Fatalf("refreshLibrary=false trigger calls = %d, want 0", trigger.calls)
	}
}

func TestLibraryManagementFolderLookupPrefersExactAndRejectsAmbiguousFold(t *testing.T) {
	otherID := dto.UUID{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	topology := &fakeLibraryTopology{folders: []library.VirtualFolder{
		{ID: libraryManagementID, Name: "Cinema"},
		{ID: otherID, Name: "CINEMA"},
	}}
	rt := router.New()
	handlers.RegisterLibrary(rt, nil, topology)

	exact := libraryRequest(t, rt, http.MethodDelete, "/Library/VirtualFolders?name=Cinema", "", "")
	assertLibraryStatus(t, exact, http.StatusNoContent)
	if len(topology.calls) != 1 || topology.calls[0].id != libraryManagementID {
		t.Fatalf("exact lookup mutation calls = %#v, want exact-case folder", topology.calls)
	}

	ambiguous := libraryRequest(t, rt, http.MethodDelete, "/Library/VirtualFolders?name=cinEMA", "", "")
	assertLibraryStatus(t, ambiguous, http.StatusConflict)
	if len(topology.calls) != 1 {
		t.Fatalf("ambiguous lookup mutated topology: %#v", topology.calls)
	}
}

func TestLibraryManagementRejectsMalformedRequests(t *testing.T) {
	topology := &fakeLibraryTopology{folders: []library.VirtualFolder{{ID: libraryManagementID, Name: "Cinema"}}}
	rt := router.New()
	handlers.RegisterLibrary(rt, nil, topology)

	for _, test := range []struct {
		name, method, path, body, contentType string
		status                                int
	}{
		{name: "add missing name", method: http.MethodPost, path: "/Library/VirtualFolders", status: http.StatusBadRequest},
		{name: "add padded name", method: http.MethodPost, path: "/Library/VirtualFolders?name=%20Cinema%20", status: http.StatusBadRequest},
		{name: "add malformed json", method: http.MethodPost, path: "/Library/VirtualFolders?name=Cinema", body: `{"LibraryOptions":`, contentType: "application/json", status: http.StatusBadRequest},
		{name: "remove invalid refresh", method: http.MethodDelete, path: "/Library/VirtualFolders?name=Cinema&refreshLibrary=sometimes", status: http.StatusBadRequest},
		{name: "add path null body", method: http.MethodPost, path: "/Library/VirtualFolders/Paths", body: "null", contentType: "application/json", status: http.StatusBadRequest},
		{name: "add path missing path", method: http.MethodPost, path: "/Library/VirtualFolders/Paths", body: `{"Name":"Cinema"}`, contentType: "application/json", status: http.StatusBadRequest},
		{name: "remove path missing path", method: http.MethodDelete, path: "/Library/VirtualFolders/Paths?name=Cinema", status: http.StatusBadRequest},
		{name: "options missing options", method: http.MethodPost, path: "/Library/VirtualFolders/LibraryOptions", body: `{"Id":"` + libraryManagementID.String() + `"}`, contentType: "application/json", status: http.StatusBadRequest},
		{name: "options invalid id", method: http.MethodPost, path: "/Library/VirtualFolders/LibraryOptions", body: `{"Id":"invalid","LibraryOptions":{}}`, contentType: "application/json", status: http.StatusBadRequest},
		{name: "unsupported content type", method: http.MethodPost, path: "/Library/VirtualFolders/Paths", body: `{}`, contentType: "text/plain", status: http.StatusUnsupportedMediaType},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := libraryRequest(t, rt, test.method, test.path, test.body, test.contentType)
			assertLibraryStatus(t, response, test.status)
			if got, want := response.Body.String(), http.StatusText(test.status)+"\n"; got != want {
				t.Fatalf("error body = %q, want %q", got, want)
			}
		})
	}
}

func TestLibraryManagementMapsMissingResourcesAndTopologyErrors(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body string
		topology                 *fakeLibraryTopology
		status                   int
	}{
		{name: "remove missing folder", method: http.MethodDelete, path: "/Library/VirtualFolders?name=Missing", topology: &fakeLibraryTopology{}, status: http.StatusNotFound},
		{name: "add path missing folder", method: http.MethodPost, path: "/Library/VirtualFolders/Paths", body: `{"Name":"Missing","Path":"/media"}`, topology: &fakeLibraryTopology{}, status: http.StatusNotFound},
		{name: "remove path missing folder", method: http.MethodDelete, path: "/Library/VirtualFolders/Paths?name=Missing&path=%2Fmedia", topology: &fakeLibraryTopology{}, status: http.StatusNotFound},
		{name: "options missing id", method: http.MethodPost, path: "/Library/VirtualFolders/LibraryOptions", body: `{"Id":"` + libraryManagementID.String() + `","LibraryOptions":{}}`, topology: &fakeLibraryTopology{updateErr: store.ErrNotFound}, status: http.StatusNotFound},
		{name: "closed topology", method: http.MethodGet, path: "/Library/VirtualFolders", topology: &fakeLibraryTopology{listErr: library.ErrTopologyClosed}, status: http.StatusServiceUnavailable},
		{name: "list topology failure", method: http.MethodGet, path: "/Library/VirtualFolders", topology: &fakeLibraryTopology{listErr: errors.New("disk failed")}, status: http.StatusInternalServerError},
		{name: "mutator topology failure", method: http.MethodPost, path: "/Library/VirtualFolders?name=Cinema&collectionType=movies", topology: &fakeLibraryTopology{addErr: errors.New("write failed")}, status: http.StatusInternalServerError},
		{name: "wrapped missing media path", method: http.MethodPost, path: "/Library/VirtualFolders/Paths", body: `{"Name":"Cinema","Path":"/offline"}`, topology: &fakeLibraryTopology{folders: []library.VirtualFolder{{ID: libraryManagementID, Name: "Cinema"}}, addPathErr: fmt.Errorf("stat media path: %w", os.ErrNotExist)}, status: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt := router.New()
			handlers.RegisterLibrary(rt, nil, test.topology)
			contentType := ""
			if test.body != "" {
				contentType = "application/json"
			}
			response := libraryRequest(t, rt, test.method, test.path, test.body, contentType)
			assertLibraryStatus(t, response, test.status)
		})
	}
	rt := router.New()

	handlers.RegisterLibrary(rt, nil, nil)
	response := libraryRequest(t, rt, http.MethodGet, "/Library/VirtualFolders", "", "")
	assertLibraryStatus(t, response, http.StatusServiceUnavailable)
}

// startupAdminResolver resolves a known Authorization token to the
// authenticated first-user identity from the real user store. It lets the
// composed probe exercise the post-wizard admin path through the real
// evaluator without the full auth-login handler machinery.
type startupAdminResolver struct {
	users *user.Service
	token string
}

func (r startupAdminResolver) Resolve(_ context.Context, request *http.Request) (auth.Identity, error) {
	credentials := auth.Extract(request, true)
	if credentials.Token == "" || credentials.Token != r.token {
		return auth.Identity{}, nil
	}
	first, err := r.users.FirstUser(context.Background())
	if err != nil {
		return auth.Identity{}, err
	}
	admin, err := isStartupAdministrator(first)
	if err != nil {
		return auth.Identity{}, err
	}
	if admin {
		return auth.Identity{Authenticated: true, UserID: first.ID, Token: credentials.Token}, nil
	}
	return auth.Identity{}, nil
}

func isStartupAdministrator(candidate store.User) (bool, error) {
	var policy struct {
		IsAdministrator bool `json:"IsAdministrator"`
	}
	if err := json.Unmarshal(candidate.Policy, &policy); err != nil {
		return false, err
	}
	return policy.IsAdministrator, nil
}

// newComposedRouter builds a router with the real config.Manager-backed
// evaluator, real user service, startup handlers, and library handlers. The
// resolver maps the bootstrap token to the first admin from the real store.
func newComposedRouter(t *testing.T, manager *config.Manager, users *user.Service, trigger *fakeLibraryRefreshTrigger, topology *fakeLibraryTopology) (*router.Router, *auth.Evaluator) {
	t.Helper()
	network := alwaysLocalNetwork{}
	evaluator := auth.NewEvaluator(manager, network, nil, nil)
	resolver := startupAdminResolver{users: users, token: composedAdminToken}
	authorizer := middleware.NewAuthorizer(resolver, evaluator, users)
	rt := router.New(authorizer.Wrap)
	handlers.RegisterStartup(rt, manager, users)
	handlers.RegisterLibrary(rt, trigger, topology)
	return rt, evaluator
}

const composedAdminToken = "composed-router-admin-token"

// TestLibraryManagementComposedRouterStartupPolicyProbe exercises the
// FirstTimeSetupOrElevated policy through the real config.Manager-backed
// evaluator, real user service, real startup handlers, and library handlers.
// The documented deploy order (wizard then add-library then refresh) is
// asserted together with the post-configuration lockdown of
// POST /Startup/User.
func TestLibraryManagementComposedRouterStartupPolicyProbe(t *testing.T) {
	paths, err := config.NewPaths(filepath.Join(t.TempDir(), "program-data"))
	if err != nil {
		t.Fatalf("create config paths: %v", err)
	}
	changes := events.NewBus[config.Change](clock.NewFake(time.Unix(0, 0).UTC()))
	t.Cleanup(func() {
		if err := changes.Close(context.Background()); err != nil {
			t.Errorf("close configuration changes: %v", err)
		}
	})
	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("open manager: %v", err)
	}
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open identity store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close identity store: %v", err)
		}
	})
	users := user.New(store.NewIdentity(stores), clock.NewFake(time.Unix(0, 0).UTC()), &composedEntropy{})
	if _, err := users.Initialize(t.Context()); err != nil {
		t.Fatalf("initialize startup user: %v", err)
	}
	topology := &fakeLibraryTopology{}
	trigger := &fakeLibraryRefreshTrigger{}

	t.Run("no library present: first admin configured then wizard completed", func(t *testing.T) {
		topology.calls = nil
		trigger.calls = 0
		rt, _ := newComposedRouter(t, manager, users, trigger, topology)
		// (a) with no library present, GET /Startup/User then POST /Startup/User
		// succeed (200, then 204) — first admin configured.
		getFirst := request(rt, http.MethodGet, "/Startup/User", "", "", "")
		assertStatus(t, getFirst, http.StatusOK)
		postUser := request(rt, http.MethodPost, "/Startup/User", `{"Name":"admin","Password":"secret"}`, "application/json", "")
		assertStatus(t, postUser, http.StatusNoContent)

		// (b) POST /Startup/Complete → 204.
		complete := request(rt, http.MethodPost, "/Startup/Complete", "", "", "")
		assertStatus(t, complete, http.StatusNoContent)
		if !manager.StartupWizardCompleted() {
			t.Fatalf("wizard not marked complete after POST /Startup/Complete")
		}
		// (c) authenticated admin: POST /Library/VirtualFolders → 204, then
		// POST /Library/Refresh → 204.
		addResponse := adminRequest(rt, http.MethodPost, "/Library/VirtualFolders?name=Cinema&collectionType=movies", "")
		assertStatus(t, addResponse, http.StatusNoContent)
		refresh := adminRequest(rt, http.MethodPost, "/Library/Refresh", "")
		assertStatus(t, refresh, http.StatusNoContent)
		if trigger.calls != 1 {
			t.Fatalf("refresh trigger calls = %d, want 1 after wizard then add-library then refresh", trigger.calls)
		}

		// (d) a repeated POST /Startup/User after configuration → 403.
		repeat := adminRequest(rt, http.MethodPost, "/Startup/User", `{"Name":"second","Password":"secret"}`)
		assertStatus(t, repeat, http.StatusForbidden)

		// (e) an unauthenticated management request post-wizard is denied.
		unauthGet := request(rt, http.MethodGet, "/Library/VirtualFolders", "", "", "")
		assertStatus(t, unauthGet, http.StatusUnauthorized)
	})

	t.Run("pre-seeded library before wizard: POST /Startup/User still succeeds", func(t *testing.T) {
		// Reset to a fresh manager and user store to test the pre-seed path.
		freshPaths, err := config.NewPaths(filepath.Join(t.TempDir(), "program-data"))
		if err != nil {
			t.Fatalf("create fresh config paths: %v", err)
		}
		freshChanges := events.NewBus[config.Change](clock.NewFake(time.Unix(0, 0).UTC()))
		t.Cleanup(func() {
			if err := freshChanges.Close(context.Background()); err != nil {
				t.Errorf("close fresh configuration changes: %v", err)
			}
		})
		freshManager, err := config.Open(t.Context(), freshPaths, freshChanges)
		if err != nil {
			t.Fatalf("open fresh manager: %v", err)
		}
		freshStores, err := sqlite.Open(t.Context(), ":memory:")
		if err != nil {
			t.Fatalf("open fresh identity store: %v", err)
		}
		t.Cleanup(func() {
			if err := freshStores.Close(); err != nil {
				t.Errorf("close fresh identity store: %v", err)
			}
		})
		freshUsers := user.New(store.NewIdentity(freshStores), clock.NewFake(time.Unix(0, 0).UTC()), &composedEntropy{})
		if _, err := freshUsers.Initialize(t.Context()); err != nil {
			t.Fatalf("initialize fresh startup user: %v", err)
		}
		freshTopology := &fakeLibraryTopology{}
		freshTrigger := &fakeLibraryRefreshTrigger{}
		rt, _ := newComposedRouter(t, freshManager, freshUsers, freshTrigger, freshTopology)

		// Seed a library before the wizard (allowed pre-wizard).
		preSeed := request(rt, http.MethodPost, "/Library/VirtualFolders?name=PreSeed&collectionType=movies", "", "", "")
		assertStatus(t, preSeed, http.StatusNoContent)

		// POST /Startup/User still succeeds — a pre-existing library does
		// not block first-admin creation. The 403 lockdown is the
		// already-configured-user condition (ErrStartupUserConfigured),
		// not a library-present condition.
		postUser := request(rt, http.MethodPost, "/Startup/User", `{"Name":"admin","Password":"secret"}`, "application/json", "")
		assertStatus(t, postUser, http.StatusNoContent)

		// Completing the wizard then re-posting Startup/User → 403 lockdown.
		complete := request(rt, http.MethodPost, "/Startup/Complete", "", "", "")
		assertStatus(t, complete, http.StatusNoContent)
		repeat := adminRequest(rt, http.MethodPost, "/Startup/User", `{"Name":"second","Password":"secret"}`)
		assertStatus(t, repeat, http.StatusForbidden)
	})
}

// composedEntropy is a deterministic entropy source for the composed-router
// probe so user IDs and password hashes are reproducible.
type composedEntropy struct {
	mu   sync.Mutex
	next byte
}

func (entropy *composedEntropy) Read(data []byte) (int, error) {
	entropy.mu.Lock()
	defer entropy.mu.Unlock()
	for index := range data {
		entropy.next++
		if entropy.next == 0 {
			entropy.next++
		}
		data[index] = entropy.next
	}
	return len(data), nil
}

func libraryRequest(t *testing.T, handler http.Handler, method, path, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertLibraryStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status = %d, want %d; body=%q", response.Code, want, response.Body.String())
	}
}

// adminRequest sends a request with the composed-router admin Authorization
// header so the resolver maps the token to the first-user identity.
func adminRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", `MediaBrowser Client="Composed", Device="Composed", DeviceId="composed", Version="1.0.0", Token="`+composedAdminToken+`"`)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestLibraryManagementEmptyOptionCollections covers the nil-to-empty
// projection of LibraryOptions collection fields in the GET
// /Library/VirtualFolders response. The topology XML decoder leaves an
// omitted collection as nil, and encoding/json projects a nil slice as null.
// Stock returns nonnull JSON arrays for the empty collections. The response
// projection must convert each nil top-level and nested collection to a
// nonnil empty slice so the read-back returns [] rather than null.
// A nonempty collection must pass through unchanged.
func TestLibraryManagementEmptyOptionCollections(t *testing.T) {
	collectionFolderID := dto.UUID{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80, 0x90, 0xa0}
	emptyOptionsFolder := library.VirtualFolder{
		ID:                 libraryManagementID,
		CollectionFolderID: collectionFolderID,
		Name:               "Golden Movies",
		CollectionType:     "movies",
		// All collection fields stay nil to model the XML round trip that
		// omits an empty collection and decodes it back as nil.
	}
	nonemptyOptionsFolder := library.VirtualFolder{
		ID:                 dto.UUID{0x20, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20},
		CollectionFolderID: dto.UUID{0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30},
		Name:               "Local Movies",
		CollectionType:     "movies",
		Options: library.LibraryOptions{
			DisabledLocalMetadataReaders: []string{"local"},
			TypeOptions: []library.TypeOptions{{
				Type:             "movies",
				MetadataFetchers: []string{"omdb"},
			}},
		},
	}
	topology := &fakeLibraryTopology{folders: []library.VirtualFolder{emptyOptionsFolder, nonemptyOptionsFolder}}
	rt := router.New()
	handlers.RegisterLibrary(rt, &fakeLibraryRefreshTrigger{}, topology)

	response := libraryRequest(t, rt, http.MethodGet, "/Library/VirtualFolders", "", "")
	assertLibraryStatus(t, response, http.StatusOK)

	var body []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode virtual folders: %v", err)
	}
	if len(body) != 2 {
		t.Fatalf("response length = %d, want 2", len(body))
	}

	// The nil-collection folder must project JSON arrays instead of null.
	emptyOptions, ok := body[0]["LibraryOptions"].(map[string]any)
	if !ok {
		t.Fatalf("empty folder LibraryOptions = %#v, want a JSON object", body[0]["LibraryOptions"])
	}
	// The top-level collections that the goldenrec policy compares must be
	// JSON arrays. DisabledLocalMetadataReaders is the field that blocked
	// the read-back; the other collections share the same projection.
	for _, field := range []string{
		"PathInfos",
		"MetadataSavers",
		"DisabledLocalMetadataReaders",
		"LocalMetadataReaderOrder",
		"DisabledSubtitleFetchers",
		"SubtitleFetcherOrder",
		"DisabledMediaSegmentProviders",
		"MediaSegmentProviderOrder",
		"SubtitleDownloadLanguages",
		"DisabledLyricFetchers",
		"LyricFetcherOrder",
		"CustomTagDelimiters",
		"DelimiterWhitelist",
		"TypeOptions",
	} {
		got, present := emptyOptions[field]
		if !present {
			t.Fatalf("empty folder LibraryOptions omitted %q", field)
		}
		if _, isArray := got.([]any); !isArray {
			t.Fatalf("empty folder %q = %#v, want a JSON array", field, got)
		}
	}
	// A nil TypeOptions collection must project an empty JSON array, and
	// a nonnil TypeOptions entry must carry normalized nested collections.
	emptyTypeOptions, ok := emptyOptions["TypeOptions"].([]any)
	if !ok || len(emptyTypeOptions) != 0 {
		t.Fatalf("empty folder TypeOptions = %#v, want an empty JSON array", emptyOptions["TypeOptions"])
	}

	// The nonempty-collection folder must keep its values unchanged.
	nonemptyOptions, ok := body[1]["LibraryOptions"].(map[string]any)
	if !ok {
		t.Fatalf("nonempty folder LibraryOptions = %#v, want a JSON object", body[1]["LibraryOptions"])
	}
	if got := nonemptyOptions["DisabledLocalMetadataReaders"]; !reflect.DeepEqual(got, []any{"local"}) {
		t.Fatalf("nonempty folder DisabledLocalMetadataReaders = %#v, want [\"local\"]", got)
	}
	nonemptyTypeOptions, ok := nonemptyOptions["TypeOptions"].([]any)
	if !ok || len(nonemptyTypeOptions) != 1 {
		t.Fatalf("nonempty folder TypeOptions = %#v, want a one-element JSON array", nonemptyOptions["TypeOptions"])
	}
	firstEntry, ok := nonemptyTypeOptions[0].(map[string]any)
	if !ok {
		t.Fatalf("nonempty folder first TypeOptions entry = %#v, want a JSON object", nonemptyTypeOptions[0])
	}
	if got := firstEntry["MetadataFetchers"]; !reflect.DeepEqual(got, []any{"omdb"}) {
		t.Fatalf("nonempty folder MetadataFetchers = %#v, want [\"omdb\"]", got)
	}
	// A nonnil but empty nested collection in the nonempty entry must also
	// project a JSON array, not null.
	if got, present := firstEntry["ImageFetchers"]; !present {
		t.Fatalf("nonempty folder ImageFetchers omitted, want a JSON array")
	} else if _, isArray := got.([]any); !isArray {
		t.Fatalf("nonempty folder ImageFetchers = %#v, want a JSON array", got)
	}
}
