// SPDX-License-Identifier: GPL-2.0-only

package user_test

import (
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/user"
)

// Sources: upstream/jellyfin/Jellyfin.Server.Implementations/Users/UserManager.cs:700-726;
// Jellyfin.Api/Controllers/StartupController.cs:113-121.
//
// Initialize is deliberately owned by the user service: a handler-side
// ListUsers/Create sequence races and can expose two first users. Its bool
// reports ApplicationHost's distinct startup-completion predicate, allowing the
// composition root to durably mark the wizard complete without duplicating user
// policy parsing.
// Sources: upstream/jellyfin/Jellyfin.Data/UserEntityExtensions.cs:170-196;
// Jellyfin.Server.Implementations/Users/UserManager.cs:700-726.
// A first user receives creation defaults before InitializeAsync promotes its
// three startup administrator permissions, so it remains hidden until the
// setup wizard completes.
func TestServiceInitializeHidesStartupAdministratorFromPublicLogin(t *testing.T) {
	service, _, _ := openUserService(t)
	ctx := t.Context()

	configured, err := service.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize(empty user store): %v", err)
	}
	if configured {
		t.Fatal("Initialize(empty user store) reported configured startup, want incomplete")
	}
	startup, err := service.FirstUser(ctx)
	if err != nil {
		t.Fatalf("FirstUser(initialized startup administrator): %v", err)
	}
	var policy struct {
		IsAdministrator                 bool
		EnableContentDeletion           bool
		EnableRemoteControlOfOtherUsers bool
		IsHidden                        bool
		IsDisabled                      bool
		EnableRemoteAccess              bool
		EnableAllDevices                bool
		PasswordResetProviderID         string
	}
	if err := json.Unmarshal(startup.Policy, &policy); err != nil {
		t.Fatalf("decode initialized startup policy %q: %v", startup.Policy, err)
	}
	wantPolicy := struct {
		IsAdministrator                 bool
		EnableContentDeletion           bool
		EnableRemoteControlOfOtherUsers bool
		IsHidden                        bool
		IsDisabled                      bool
		EnableRemoteAccess              bool
		EnableAllDevices                bool
		PasswordResetProviderID         string
	}{
		IsAdministrator:                 true,
		EnableContentDeletion:           true,
		EnableRemoteControlOfOtherUsers: true,
		IsHidden:                        true,
		IsDisabled:                      false,
		EnableRemoteAccess:              true,
		EnableAllDevices:                true,
		PasswordResetProviderID:         "Jellyfin.Server.Implementations.Users.DefaultPasswordResetProvider",
	}
	if policy != wantPolicy {
		t.Fatalf("initialized startup policy = %#v, want %#v", policy, wantPolicy)
	}

	publicUsers, err := service.List(ctx, user.Actor{}, user.UserListOptions{Public: true, StartupComplete: false})
	if err != nil {
		t.Fatalf("List(public before startup completion): %v", err)
	}
	for _, candidate := range publicUsers {
		if candidate.ID == startup.ID {
			t.Fatalf("public users before startup completion = %#v, unexpectedly contains initialized startup administrator %s", publicUsers, startup.ID)
		}
	}
}

func TestServiceInitializeSeedsOneBlankAdministratorAndReportsIncomplete(t *testing.T) {
	service, identity, _ := openUserService(t)
	ctx := t.Context()

	configured, err := service.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize(empty user store): %v", err)
	}
	if configured {
		t.Fatal("Initialize(empty user store) reported a configured administrator, want incomplete startup")
	}
	users := listStartupUsers(t, identity)
	if len(users) != 1 {
		t.Fatalf("users after initializing empty store = %#v, want exactly one startup user", users)
	}
	startup := users[0]
	if !startupAdministrator(t, startup) {
		t.Fatalf("startup user policy = %s, want IsAdministrator=true", startup.Policy)
	}
	verified, err := service.VerifyPassword(ctx, startup.ID, "")
	if err != nil {
		t.Fatalf("verify blank startup password: %v", err)
	}
	if !verified {
		t.Fatal("blank startup password did not authenticate the initial wizard user")
	}

	configured, err = service.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize(existing blank startup administrator): %v", err)
	}
	if configured {
		t.Fatal("Initialize(blank-password administrator) reported startup complete, want incomplete")
	}
	if got := listStartupUsers(t, identity); len(got) != 1 || got[0].ID != startup.ID {
		t.Fatalf("repeated Initialize altered startup users = %#v, want the original single user", got)
	}

	if _, err := service.Create(ctx, "startup-member", "member password"); err != nil {
		t.Fatalf("create non-administrator after initialization: %v", err)
	}
	if err := service.Delete(ctx, startup.ID); !errors.Is(err, user.ErrLastAdministrator) {
		t.Fatalf("delete initialized sole administrator error = %v, want ErrLastAdministrator", err)
	}
}

// Initial startup must not inherit the account that happens to run the server.
// The environment source is deliberately selected per platform, matching the
// host account convention used by the startup implementation.
func TestServiceInitializePersistsEnvironmentDerivedUsernameOrFallback(t *testing.T) {
	sourceVariable, inactiveVariable := "USER", "USERNAME"
	if runtime.GOOS == "windows" {
		sourceVariable, inactiveVariable = inactiveVariable, sourceVariable
	}

	for _, tc := range []struct {
		name        string
		sourceValue string
		want        string
	}{
		{
			name:        "valid environment username",
			sourceValue: "environment-startup-user",
			want:        "environment-startup-user",
		},
		{
			name: "absent environment username",
			want: "MyJellyfinUser",
		},
		{
			name:        "invalid environment username",
			sourceValue: "invalid/startup/user",
			want:        "MyJellyfinUser",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(sourceVariable, tc.sourceValue)
			t.Setenv(inactiveVariable, "invalid/inactive/username")

			service, identity, _ := openUserService(t)
			if configured, err := service.Initialize(t.Context()); err != nil {
				t.Fatalf("Initialize: %v", err)
			} else if configured {
				t.Fatal("Initialize reported configured startup for the blank initial administrator")
			}

			users := listStartupUsers(t, identity)
			if len(users) != 1 {
				t.Fatalf("users after initializing empty store = %#v, want exactly one startup user", users)
			}
			if got := users[0].Name; got != tc.want {
				t.Errorf("persisted startup username = %q, want %q", got, tc.want)
			}
			if got, want := users[0].NormalizedName, user.NormalizeName(tc.want); got != want {
				t.Errorf("persisted normalized startup username = %q, want %q", got, want)
			}
		})
	}
}

// Source: upstream/jellyfin/Emby.Server.Implementations/ApplicationHost.cs:443-458.
// A user record alone must not close the unauthenticated setup gate; only an
// administrator with a nonempty password proves a configured installation.
func TestServiceInitializeReportsOnlyConfiguredAdministratorsAsStartupComplete(t *testing.T) {
	cases := []struct {
		name      string
		seed      func(t *testing.T, service *user.Service, identity *store.Identity)
		completed bool
	}{
		{
			name: "ordinary user",
			seed: func(t *testing.T, service *user.Service, _ *store.Identity) {
				t.Helper()
				if _, err := service.Create(t.Context(), "ordinary-user", "ordinary password"); err != nil {
					t.Fatalf("create ordinary user: %v", err)
				}
			},
			completed: false,
		},
		{
			name: "blank-password administrator",
			seed: func(t *testing.T, _ *user.Service, identity *store.Identity) {
				t.Helper()
				if _, err := identity.CreateUser(t.Context(), store.User{
					ID:             startupTestUUID(t, "10000000000000000000000000000001"),
					Name:           "blank-admin",
					NormalizedName: "BLANK-ADMIN",
					Policy:         []byte(`{"IsAdministrator":true}`),
					Configuration:  []byte(`{}`),
				}); err != nil {
					t.Fatalf("seed blank-password administrator: %v", err)
				}
			},
			completed: false,
		},
		{
			name: "nonempty-password administrator",
			seed: func(t *testing.T, service *user.Service, _ *store.Identity) {
				t.Helper()
				administrator, err := service.Create(t.Context(), "configured-admin", "configured password")
				if err != nil {
					t.Fatalf("create configured administrator: %v", err)
				}
				administrator.Policy = []byte(`{"IsAdministrator":true}`)
				if err := service.Update(t.Context(), administrator); err != nil {
					t.Fatalf("promote configured administrator: %v", err)
				}
			},
			completed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, identity, _ := openUserService(t)
			tc.seed(t, service, identity)

			configured, err := service.Initialize(t.Context())
			if err != nil {
				t.Fatalf("Initialize(%s): %v", tc.name, err)
			}
			if configured != tc.completed {
				t.Fatalf("Initialize(%s) configured = %t, want %t", tc.name, configured, tc.completed)
			}
			if got := listStartupUsers(t, identity); len(got) != 1 {
				t.Fatalf("Initialize(%s) changed preexisting users = %#v, want one preserved user", tc.name, got)
			}
		})
	}
}

func TestServiceInitializeIsConcurrentAndPreservesInitialAdministratorProtection(t *testing.T) {
	service, identity, _ := openUserService(t)
	const workers = 16
	start := make(chan struct{})
	ready := make(chan struct{}, workers)
	results := make(chan startupInitializationResult, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			ready <- struct{}{}
			<-start
			configured, err := service.Initialize(t.Context())
			results <- startupInitializationResult{configured: configured, err: err}
		}()
	}
	for range workers {
		<-ready
	}
	close(start)
	group.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent Initialize: %v", result.err)
		}
		if result.configured {
			t.Fatal("concurrent Initialize reported a configured administrator after creating only blank startup credentials")
		}
	}

	users := listStartupUsers(t, identity)
	if len(users) != 1 {
		t.Fatalf("concurrent Initialize created %d users, want exactly one", len(users))
	}
	startup := users[0]
	if !startupAdministrator(t, startup) {
		t.Fatalf("concurrent startup user policy = %s, want IsAdministrator=true", startup.Policy)
	}
	if _, err := identity.CreateUser(t.Context(), store.User{
		ID:             startupTestUUID(t, "ffffffffffffffffffffffffffffffff"),
		Name:           "concurrent-startup-member",
		NormalizedName: "CONCURRENT-STARTUP-MEMBER",
		Policy:         []byte(`{}`),
		Configuration:  []byte(`{}`),
	}); err != nil {
		t.Fatalf("create member after concurrent initialization: %v", err)
	}
	if err := service.Delete(t.Context(), startup.ID); !errors.Is(err, user.ErrLastAdministrator) {
		t.Fatalf("delete concurrently initialized sole administrator error = %v, want ErrLastAdministrator", err)
	}
}

type startupInitializationResult struct {
	configured bool
	err        error
}

func listStartupUsers(t *testing.T, identity *store.Identity) []store.User {
	t.Helper()
	users, err := identity.ListUsers(t.Context())
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	return users
}

func startupAdministrator(t *testing.T, candidate store.User) bool {
	t.Helper()
	var policy struct{ IsAdministrator bool }
	if err := json.Unmarshal(candidate.Policy, &policy); err != nil {
		t.Fatalf("decode user policy %q: %v", candidate.Policy, err)
	}
	return policy.IsAdministrator
}

func startupTestUUID(t *testing.T, text string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(text)
	if err != nil {
		t.Fatalf("parse startup fixture UUID %q: %v", text, err)
	}
	return id
}
