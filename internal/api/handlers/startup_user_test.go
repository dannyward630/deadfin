// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"path/filepath"
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
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/user"
)

func TestStartupUserAliasesExposeOnlyNameWithPinnedMetadataAndCasing(t *testing.T) {
	rt, _, service, _, _ := newStartupUserRouter(t)
	initialResponse := request(rt, http.MethodGet, "/Startup/User", "", "", "")
	assertStatus(t, initialResponse, http.StatusOK)
	initialized, err := service.FirstUser(t.Context())
	if err != nil {
		t.Fatalf("GET Startup/User did not initialize the first user: %v", err)
	}
	assertJSONResponse(t, initialResponse, map[string]any{"Name": initialized.Name})

	name := "Visible Startup User"
	if err := service.UpdateStartupUser(t.Context(), user.StartupUserUpdate{Name: &name, Password: "not for the response"}); err != nil {
		t.Fatalf("configure startup user: %v", err)
	}
	for _, test := range []struct {
		path        string
		operationID string
	}{
		{path: "/sTaRtUp/uSeR", operationID: "GetFirstUser"},
		{path: "/startup/fIrStUsEr", operationID: "GetFirstUser_2"},
	} {
		t.Run(test.path, func(t *testing.T) {
			rec := request(rt, http.MethodGet, test.path, "", "", "")
			assertStatus(t, rec, http.StatusOK)
			assertJSONResponse(t, rec, map[string]any{"Name": name})
			assertStartupRoute(t, rt, http.MethodGet, canonicalStartupPath(test.path), test.operationID)
		})
	}
	assertStartupRoute(t, rt, http.MethodPost, "/Startup/User", "UpdateStartupUser")
	assertStartupRoute(t, rt, http.MethodPost, "/Startup/Complete", "CompleteWizard")
}

func TestStartupUserUpdateMapsFailuresWithoutMutatingAndAcceptsOneCredential(t *testing.T) {
	t.Run("missing first user maps to not found", func(t *testing.T) {
		rt, _, service, _, _ := newStartupUserRouter(t)
		rec := request(rt, http.MethodPost, "/Startup/User", `{"Password":"first password"}`, "application/json", "")
		assertStatus(t, rec, http.StatusNotFound)
		if _, err := service.FirstUser(t.Context()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing startup-user POST created a user: %v", err)
		}
	})

	t.Run("malformed and blank bodies leave blank user unchanged", func(t *testing.T) {
		rt, _, service, _, _ := newStartupUserRouter(t)
		if _, err := service.Initialize(t.Context()); err != nil {
			t.Fatalf("initialize startup user: %v", err)
		}
		before, err := service.FirstUser(t.Context())
		if err != nil {
			t.Fatalf("read initialized user: %v", err)
		}
		for _, test := range []struct {
			name string
			body string
		}{
			{name: "malformed JSON", body: `{"Name":"Changed","Password":`},
			{name: "blank password", body: `{"Name":"Changed","Password":" \t "}`},
		} {
			t.Run(test.name, func(t *testing.T) {
				rec := request(rt, http.MethodPost, "/STARTUP/USER", test.body, "application/json", "")
				assertStatus(t, rec, http.StatusBadRequest)
				after, err := service.FirstUser(t.Context())
				if err != nil {
					t.Fatalf("read startup user after rejected %s: %v", test.name, err)
				}
				if after.Name != before.Name || after.NormalizedName != before.NormalizedName || after.PasswordHash != "" {
					t.Fatalf("rejected %s mutated startup user = %#v, want blank original user", test.name, after)
				}
			})
		}
	})

	t.Run("winner is durable and a retry cannot overwrite it", func(t *testing.T) {
		rt, _, service, _, _ := newStartupUserRouter(t)
		if _, err := service.Initialize(t.Context()); err != nil {
			t.Fatalf("initialize startup user: %v", err)
		}
		rec := request(rt, http.MethodPost, "/Startup/User", `{"Name":"Configured User","Password":"winning password"}`, "application/json", "")
		assertStatus(t, rec, http.StatusNoContent)
		configured, err := service.FirstUser(t.Context())
		if err != nil {
			t.Fatalf("read configured user: %v", err)
		}
		if configured.Name != "Configured User" {
			t.Fatalf("configured startup name = %q, want Configured User", configured.Name)
		}
		verified, err := service.VerifyPassword(t.Context(), configured.ID, "winning password")
		if err != nil || !verified {
			t.Fatalf("verify winning startup password = (verified=%t, err=%v), want (true, nil)", verified, err)
		}

		rec = request(rt, http.MethodPost, "/Startup/User", `{"Name":"Overwritten User","Password":"losing password"}`, "application/json", "")
		assertStatus(t, rec, http.StatusForbidden)
		afterRetry, err := service.FirstUser(t.Context())
		if err != nil {
			t.Fatalf("read startup user after rejected retry: %v", err)
		}
		if afterRetry.Name != "Configured User" {
			t.Fatalf("configured retry changed name to %q, want Configured User", afterRetry.Name)
		}
		verified, err = service.VerifyPassword(t.Context(), afterRetry.ID, "winning password")
		if err != nil || !verified {
			t.Fatalf("configured retry changed winning password = (verified=%t, err=%v), want (true, nil)", verified, err)
		}
	})
}

func TestStartupUserStateErrorsPrecedeValidationAndDuplicateNamesDoNotMutate(t *testing.T) {
	t.Run("absent user remains not found before password validation", func(t *testing.T) {
		rt, _, service, _, _ := newStartupUserRouter(t)
		rec := request(rt, http.MethodPost, "/Startup/User", `{"Name":"?","Password":" \t "}`, "application/json", "")
		assertStatus(t, rec, http.StatusNotFound)
		if _, err := service.FirstUser(t.Context()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("invalid absent startup-user update created a user: %v", err)
		}
	})

	t.Run("configured user remains forbidden before input validation", func(t *testing.T) {
		rt, _, service, _, _ := newStartupUserRouter(t)
		if _, err := service.Initialize(t.Context()); err != nil {
			t.Fatalf("initialize startup user: %v", err)
		}
		if err := service.UpdateStartupUser(t.Context(), user.StartupUserUpdate{Password: "configured password"}); err != nil {
			t.Fatalf("configure startup user: %v", err)
		}
		before, err := service.FirstUser(t.Context())
		if err != nil {
			t.Fatalf("read configured startup user: %v", err)
		}
		rec := request(rt, http.MethodPost, "/Startup/User", `{"Name":"?","Password":" \t "}`, "application/json", "")
		assertStatus(t, rec, http.StatusForbidden)
		after, err := service.FirstUser(t.Context())
		if err != nil {
			t.Fatalf("read configured startup user after rejected request: %v", err)
		}
		if after.Name != before.Name || after.NormalizedName != before.NormalizedName || after.PasswordHash != before.PasswordHash {
			t.Fatalf("configured startup-user validation request mutated state = %#v, want %#v", after, before)
		}
	})

	t.Run("duplicate normalized rename maps to bad request without setting password", func(t *testing.T) {
		rt, _, service, _, _ := newStartupUserRouter(t)
		if _, err := service.Initialize(t.Context()); err != nil {
			t.Fatalf("initialize startup user: %v", err)
		}
		if _, err := service.Create(t.Context(), "Existing Name", "member password"); err != nil {
			t.Fatalf("create colliding member: %v", err)
		}
		before, err := service.FirstUser(t.Context())
		if err != nil {
			t.Fatalf("read blank startup user: %v", err)
		}
		rec := request(rt, http.MethodPost, "/Startup/User", `{"Name":"existing name","Password":"attempted password"}`, "application/json", "")
		assertStatus(t, rec, http.StatusBadRequest)
		after, err := service.FirstUser(t.Context())
		if err != nil {
			t.Fatalf("read startup user after duplicate rename: %v", err)
		}
		if after.Name != before.Name || after.NormalizedName != before.NormalizedName || after.PasswordHash != "" {
			t.Fatalf("duplicate startup rename mutated first user = %#v, want blank original user", after)
		}
	})
}

func TestStartupCompletePersistsOnceAndRemainsSafeOnRetry(t *testing.T) {
	rt, manager, service, changes, paths := newStartupUserRouter(t)
	if _, err := service.Initialize(t.Context()); err != nil {
		t.Fatalf("initialize startup user: %v", err)
	}
	subscription := changes.Subscribe(events.SubscriptionOptions{Buffer: 2})
	t.Cleanup(func() {
		if err := subscription.Close(context.Background()); err != nil {
			t.Errorf("close configuration subscription: %v", err)
		}
	})

	for attempt := range 2 {
		rec := request(rt, http.MethodPost, "/Startup/Complete", "", "", "")
		assertStatus(t, rec, http.StatusNoContent)
		if !manager.StartupWizardCompleted() {
			t.Fatalf("startup completion attempt %d returned before the durable completion state was visible", attempt+1)
		}
	}
	assertChange(t, subscription.Events(), config.Change{File: "system.xml"})
	assertNoChange(t, subscription.Events())

	reopened, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("reopen completed configuration: %v", err)
	}
	if !reopened.StartupWizardCompleted() {
		t.Fatal("startup completion was not preserved after reopening system configuration")
	}
}

func TestStartupCompleteConcurrentRequestsPersistOneCompletionEvent(t *testing.T) {
	rt, manager, service, changes, _ := newStartupUserRouter(t)
	if _, err := service.Initialize(t.Context()); err != nil {
		t.Fatalf("initialize startup user: %v", err)
	}
	subscription := changes.Subscribe(events.SubscriptionOptions{Buffer: 16})
	t.Cleanup(func() {
		if err := subscription.Close(context.Background()); err != nil {
			t.Errorf("close configuration subscription: %v", err)
		}
	})

	const workers = 16
	start := make(chan struct{})
	ready := make(chan struct{}, workers)
	statuses := make(chan int, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			ready <- struct{}{}
			<-start
			statuses <- request(rt, http.MethodPost, "/Startup/Complete", "", "", "").Code
		}()
	}
	for range workers {
		<-ready
	}
	close(start)
	group.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusNoContent {
			t.Fatalf("concurrent startup completion status = %d, want %d", status, http.StatusNoContent)
		}
	}
	if !manager.StartupWizardCompleted() {
		t.Fatal("concurrent startup completion did not mark the wizard complete")
	}
	assertChange(t, subscription.Events(), config.Change{File: "system.xml"})
	assertNoChange(t, subscription.Events())
}

func TestStartupCompletionClosesAnonymousStartupRoutesOnNextRequest(t *testing.T) {
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
	service := user.New(store.NewIdentity(stores), clock.NewFake(time.Unix(0, 0).UTC()), &startupUserEntropy{})
	evaluator := auth.NewEvaluator(manager, startupUserLocalNetwork{}, startupUserSchedules{}, func() time.Time { return time.Unix(0, 0).UTC() })
	authorizer := middleware.NewAuthorizer(startupUserResolver{}, evaluator, service)
	rt := router.New(authorizer.Wrap)
	handlers.RegisterStartup(rt, manager, service)
	if _, err := service.Initialize(t.Context()); err != nil {
		t.Fatalf("initialize startup user: %v", err)
	}

	rec := request(rt, http.MethodPost, "/Startup/Complete", "", "", "")
	assertStatus(t, rec, http.StatusNoContent)
	rec = request(rt, http.MethodGet, "/Startup/User", "", "", "")
	assertStatus(t, rec, http.StatusUnauthorized)
}

func assertStartupRoute(t *testing.T, rt *router.Router, method, path, operationID string) {
	t.Helper()
	for _, route := range rt.Routes() {
		if route.Method == method && route.Pattern == path {
			if route.OperationID != operationID || route.Policy != string(auth.PolicyFirstTimeSetupOrElevated) || route.Anonymous {
				t.Fatalf("startup route %s %s metadata = %#v, want operation %q and FirstTimeSetupOrElevated", method, path, route, operationID)
			}
			return
		}
	}
	t.Fatalf("startup route %s %s was not registered", method, path)
}

func canonicalStartupPath(path string) string {
	switch path {
	case "/sTaRtUp/uSeR":
		return "/Startup/User"
	case "/startup/fIrStUsEr":
		return "/Startup/FirstUser"
	default:
		return path
	}
}

func newStartupUserRouter(t *testing.T) (*router.Router, *config.Manager, *user.Service, *events.Bus[config.Change], config.Paths) {
	t.Helper()
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
		t.Fatalf("open temporary manager: %v", err)
	}
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open temporary identity store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close temporary identity store: %v", err)
		}
	})
	service := user.New(store.NewIdentity(stores), clock.NewFake(time.Unix(0, 0).UTC()), &startupUserEntropy{})
	rt := router.New()
	handlers.RegisterStartup(rt, manager, service)
	return rt, manager, service, changes, paths
}

type startupUserEntropy struct {
	mu   sync.Mutex
	next byte
}

func (entropy *startupUserEntropy) Read(data []byte) (int, error) {
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

type startupUserResolver struct{}

func (startupUserResolver) Resolve(context.Context, *http.Request) (auth.Identity, error) {
	return auth.Identity{}, nil
}

type startupUserLocalNetwork struct{}

func (startupUserLocalNetwork) IsInLocalNetwork(netip.Addr) bool { return true }

type startupUserSchedules struct{}

func (startupUserSchedules) AccessSchedules(context.Context, dto.UUID) ([]store.AccessSchedule, error) {
	return nil, nil
}
