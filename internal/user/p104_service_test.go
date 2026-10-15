// SPDX-License-Identifier: GPL-2.0-only

package user_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/user"
)

// Sources: upstream/jellyfin/Jellyfin.Api/Controllers/UserController.cs:84-142,575-639;
// MediaBrowser.Model/Dto/UserDto.cs:12-115; MediaBrowser.Model/Users/UserPolicy.cs:70-150.
// User listing is a client-visible login surface: startup state changes the
// public filter, while device and remote facts further narrow the completed
// wizard view. Projection must retain the stored policy/configuration documents
// byte-for-byte so later policy fields are not silently discarded.
func TestServiceListsPublicUsersByStartupDeviceAndRemoteEligibility(t *testing.T) {
	service, _, _ := openUserService(t)
	alphaPolicy := json.RawMessage(`{"IsHidden":false,"IsDisabled":false,"EnableRemoteAccess":true,"EnableAllDevices":false,"EnabledDevices":["living-room"]}`)
	betaPolicy := json.RawMessage(`{"IsHidden":false,"IsDisabled":false,"EnableRemoteAccess":false,"EnableAllDevices":true}`)
	alphaConfig := json.RawMessage(`{"SubtitleMode":"Always","PlayDefaultAudioTrack":false,"FutureConfiguration":7}`)
	alpha := p104CreateUser(t, service, "Alpha", "alpha password", alphaPolicy, alphaConfig)
	beta := p104CreateUser(t, service, "Beta", "beta password", betaPolicy, json.RawMessage(`{"SubtitleMode":"Default"}`))
	_ = p104CreateUser(t, service, "Hidden", "hidden password", json.RawMessage(`{"IsHidden":true,"IsDisabled":false,"EnableRemoteAccess":true}`), json.RawMessage(`{}`))
	_ = p104CreateUser(t, service, "Disabled", "disabled password", json.RawMessage(`{"IsHidden":false,"IsDisabled":true,"EnableRemoteAccess":true}`), json.RawMessage(`{}`))

	remoteLivingRoom := user.Actor{DeviceID: "living-room", Remote: true}
	incomplete, err := service.List(t.Context(), remoteLivingRoom, user.UserListOptions{Public: true, StartupComplete: false})
	if err != nil {
		t.Fatalf("List(public before startup completion): %v", err)
	}
	assertP104Users(t, incomplete, []dto.UUID{alpha.ID, beta.ID})
	if got := incomplete[0]; !reflect.DeepEqual(got.Policy, alphaPolicy) || !reflect.DeepEqual(got.Configuration, alphaConfig) {
		t.Fatalf("Alpha UserDto projection = policy %s configuration %s, want exact stored policy %s configuration %s", got.Policy, got.Configuration, alphaPolicy, alphaConfig)
	}

	complete, err := service.List(t.Context(), remoteLivingRoom, user.UserListOptions{Public: true, StartupComplete: true})
	if err != nil {
		t.Fatalf("List(public after startup completion): %v", err)
	}
	assertP104Users(t, complete, []dto.UUID{alpha.ID})

	// A missing current identity is not a synthetic user. /Users/Me maps this
	// public service domain to the upstream 400 response rather than leaking a
	// persistence 404 (UserController.cs:585-599).
	if _, err := service.Me(t.Context(), user.Actor{}); !errors.Is(err, user.ErrBadRequest) {
		t.Fatalf("Me(without authenticated user) error = %v, want ErrBadRequest", err)
	}
	if _, err := service.Me(t.Context(), user.Actor{UserID: p104UUID(t, "cccccccccccccccccccccccccccccccc")}); !errors.Is(err, user.ErrBadRequest) {
		t.Fatalf("Me(unknown authenticated user) error = %v, want ErrBadRequest", err)
	}
	me, err := service.Me(t.Context(), user.Actor{UserID: beta.ID})
	if err != nil {
		t.Fatalf("Me(existing authenticated user): %v", err)
	}
	if me.ID != beta.ID || me.Name != "Beta" {
		t.Fatalf("Me() = %#v, want Beta identity", me)
	}
	if _, err := service.GetDTO(t.Context(), p104UUID(t, "ffffffffffffffffffffffffffffffff")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetDTO(missing user) error = %v, want ErrNotFound for HTTP 404 mapping", err)
	}
}

// Source: upstream/jellyfin/Jellyfin.Api/Controllers/UserController.cs:84-100,602-638.
// The authenticated collection is ordered by username and applies explicit
// hidden/disabled predicates independently; neither predicate may be inferred
// from the other when clients manage account visibility.
func TestServiceListOrdersUsersAndAppliesExplicitVisibilityFilters(t *testing.T) {
	service, _, _ := openUserService(t)
	alpha := p104CreateUser(t, service, "Alpha", "alpha password", json.RawMessage(`{"IsHidden":true,"IsDisabled":false}`), json.RawMessage(`{}`))
	bravo := p104CreateUser(t, service, "Bravo", "bravo password", json.RawMessage(`{"IsHidden":false,"IsDisabled":true}`), json.RawMessage(`{}`))
	charlie := p104CreateUser(t, service, "Charlie", "charlie password", json.RawMessage(`{"IsHidden":false,"IsDisabled":false}`), json.RawMessage(`{}`))

	all, err := service.List(t.Context(), user.Actor{UserID: charlie.ID}, user.UserListOptions{})
	if err != nil {
		t.Fatalf("List(all users): %v", err)
	}
	assertP104Users(t, all, []dto.UUID{alpha.ID, bravo.ID, charlie.ID})

	visible := false
	enabled := false
	filtered, err := service.List(t.Context(), user.Actor{UserID: charlie.ID}, user.UserListOptions{IsHidden: &visible, IsDisabled: &enabled})
	if err != nil {
		t.Fatalf("List(visible enabled users): %v", err)
	}
	assertP104Users(t, filtered, []dto.UUID{charlie.ID})
}

// UserController.cs:460-509. A self update requires the target's preference
// grant; an administrator may update any target; an ordinary user may never
// update another account. Each rejected authorization must leave the document
// untouched.
func TestServiceConfigurationUpdateEnforcesSelfAdminAndPreferenceAccess(t *testing.T) {
	service, _, _ := openUserService(t)
	owner := p104CreateUser(t, service, "Owner", "owner password", json.RawMessage(`{"EnableUserPreferenceAccess":true}`), json.RawMessage(`{"SubtitleMode":"Original"}`))
	locked := p104CreateUser(t, service, "Locked", "locked password", json.RawMessage(`{"EnableUserPreferenceAccess":false}`), json.RawMessage(`{"SubtitleMode":"Locked"}`))
	administrator := p104CreateUser(t, service, "Administrator", "administrator password", json.RawMessage(`{"IsAdministrator":true,"EnableUserPreferenceAccess":true}`), json.RawMessage(`{"SubtitleMode":"Admin"}`))

	updated := json.RawMessage(`{"SubtitleMode":"Always","PlayDefaultAudioTrack":false}`)
	if err := service.UpdateConfiguration(t.Context(), user.Actor{UserID: owner.ID}, owner.ID, updated); err != nil {
		t.Fatalf("self configuration update: %v", err)
	}
	got, err := service.GetDTO(t.Context(), owner.ID)
	if err != nil {
		t.Fatalf("get self-updated user: %v", err)
	}
	if !reflect.DeepEqual(got.Configuration, updated) {
		t.Fatalf("self-updated configuration = %s, want %s", got.Configuration, updated)
	}

	beforeLocked, err := service.GetDTO(t.Context(), locked.ID)
	if err != nil {
		t.Fatalf("get locked user: %v", err)
	}
	for _, tc := range []struct {
		name   string
		actor  user.Actor
		target dto.UUID
		err    error
	}{
		{name: "missing actor", actor: user.Actor{}, target: owner.ID, err: user.ErrUnauthorized},
		{name: "ordinary other user", actor: user.Actor{UserID: owner.ID}, target: locked.ID, err: user.ErrForbidden},
		{name: "self without preference access", actor: user.Actor{UserID: locked.ID}, target: locked.ID, err: user.ErrForbidden},
		{name: "missing target", actor: user.Actor{UserID: administrator.ID, Administrator: true}, target: p104UUID(t, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"), err: store.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := service.UpdateConfiguration(t.Context(), tc.actor, tc.target, json.RawMessage(`{"SubtitleMode":"Forbidden"}`))
			if !errors.Is(err, tc.err) {
				t.Fatalf("UpdateConfiguration error = %v, want %v", err, tc.err)
			}
		})
	}
	afterLocked, err := service.GetDTO(t.Context(), locked.ID)
	if err != nil {
		t.Fatalf("get locked user after denied updates: %v", err)
	}
	if !reflect.DeepEqual(afterLocked.Configuration, beforeLocked.Configuration) {
		t.Fatalf("denied configuration update mutated target = %s, want %s", afterLocked.Configuration, beforeLocked.Configuration)
	}

	adminUpdate := json.RawMessage(`{"SubtitleMode":"AdministratorOverride"}`)
	if err := service.UpdateConfiguration(t.Context(), user.Actor{UserID: administrator.ID, Administrator: true}, locked.ID, adminUpdate); err != nil {
		t.Fatalf("administrator configuration override: %v", err)
	}
	afterAdmin, err := service.GetDTO(t.Context(), locked.ID)
	if err != nil || !reflect.DeepEqual(afterAdmin.Configuration, adminUpdate) {
		t.Fatalf("administrator configuration override = (%s, %v), want (%s, nil)", afterAdmin.Configuration, err, adminUpdate)
	}
}

// Sources: upstream/jellyfin/Jellyfin.Api/Controllers/UserController.cs:261-318.
// Wrong self credentials are 403, absence of an authenticated caller is 401,
// and a missing target remains 404. A winning password change invalidates every
// other token for that account while retaining the caller's current token.
func TestServicePasswordUpdateAuthorizesCurrentResetAndTokenRevocation(t *testing.T) {
	service, _, _ := openUserService(t)
	member := p104CreateUser(t, service, "Member", "old password", json.RawMessage(`{}`), json.RawMessage(`{}`))
	administrator := p104CreateUser(t, service, "Administrator", "administrator password", json.RawMessage(`{"IsAdministrator":true}`), json.RawMessage(`{}`))
	current := issueServiceDevice(t, service, member.ID, "member-current")
	other := issueServiceDevice(t, service, member.ID, "member-other")

	for _, tc := range []struct {
		name   string
		actor  user.Actor
		target dto.UUID
		update user.PasswordUpdate
		err    error
	}{
		{name: "unauthenticated", actor: user.Actor{}, target: member.ID, update: user.PasswordUpdate{Current: "old password", New: "new password"}, err: user.ErrUnauthorized},
		{name: "wrong self password", actor: user.Actor{UserID: member.ID, Token: current.AccessToken}, target: member.ID, update: user.PasswordUpdate{Current: "wrong", New: "new password"}, err: user.ErrForbidden},
		{name: "ordinary other user", actor: user.Actor{UserID: member.ID, Token: current.AccessToken}, target: administrator.ID, update: user.PasswordUpdate{New: "new password"}, err: user.ErrForbidden},
		{name: "missing target", actor: user.Actor{UserID: administrator.ID, Administrator: true}, target: p104UUID(t, "dddddddddddddddddddddddddddddddd"), update: user.PasswordUpdate{New: "new password"}, err: store.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := service.UpdatePassword(t.Context(), tc.actor, tc.target, tc.update); !errors.Is(err, tc.err) {
				t.Fatalf("UpdatePassword error = %v, want %v", err, tc.err)
			}
		})
	}
	if verified, err := service.VerifyPassword(t.Context(), member.ID, "old password"); err != nil || !verified {
		t.Fatalf("denied password updates changed member credential = (verified=%t, err=%v), want original password", verified, err)
	}

	if err := service.UpdatePassword(t.Context(), user.Actor{UserID: member.ID, Token: current.AccessToken}, member.ID, user.PasswordUpdate{Current: "old password", New: "new password"}); err != nil {
		t.Fatalf("self password update: %v", err)
	}
	if verified, _ := service.VerifyPassword(t.Context(), member.ID, "old password"); verified {
		t.Fatal("old credential remained valid after password update")
	}
	if verified, err := service.VerifyPassword(t.Context(), member.ID, "new password"); err != nil || !verified {
		t.Fatalf("new credential verification = (verified=%t, err=%v), want true", verified, err)
	}
	if _, err := service.Token(t.Context(), current.AccessToken); err != nil {
		t.Fatalf("current token after password update: %v", err)
	}
	if _, err := service.Token(t.Context(), other.AccessToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other token after password update = %v, want ErrNotFound", err)
	}
	resetToken := issueServiceDevice(t, service, member.ID, "member-reset-session")

	if err := service.UpdatePassword(t.Context(), user.Actor{UserID: administrator.ID, Administrator: true}, member.ID, user.PasswordUpdate{Reset: true}); err != nil {
		t.Fatalf("administrator password reset: %v", err)
	}
	if verified, err := service.VerifyPassword(t.Context(), member.ID, ""); err != nil || !verified {
		t.Fatalf("reset member password verification = (verified=%t, err=%v), want blank password", verified, err)
	}
	if _, err := service.Token(t.Context(), resetToken.AccessToken); err != nil {
		t.Fatalf("reset retained target token: %v", err)
	}
	if err := service.UpdatePassword(t.Context(), user.Actor{UserID: administrator.ID, Administrator: true}, administrator.ID, user.PasswordUpdate{Reset: true}); !errors.Is(err, user.ErrAdministratorPasswordRequired) {
		t.Fatalf("reset administrator to blank password error = %v, want ErrAdministratorPasswordRequired", err)
	}
}

// Source: upstream/jellyfin/Jellyfin.Api/Controllers/UserController.cs:291-316.
// The current-password check, credential write, and other-token revocation form
// one security transition. Competing requests that both present the old secret
// must leave exactly one winning credential; a stale loser must not overwrite
// it or leave a revoked alternate session usable.
func TestServicePasswordUpdateSerializesCompetingCurrentCredentials(t *testing.T) {
	service, _, _ := openUserService(t)
	member := p104CreateUser(t, service, "Race Member", "old password", json.RawMessage(`{}`), json.RawMessage(`{}`))
	current := issueServiceDevice(t, service, member.ID, "race-current")
	other := issueServiceDevice(t, service, member.ID, "race-other")

	type result struct {
		password string
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, password := range []string{"first replacement", "second replacement"} {
		go func(password string) {
			<-start
			results <- result{password: password, err: service.UpdatePassword(t.Context(), user.Actor{UserID: member.ID, Token: current.AccessToken}, member.ID, user.PasswordUpdate{Current: "old password", New: password})}
		}(password)
	}
	close(start)

	winner := ""
	for range 2 {
		result := <-results
		if result.err == nil {
			if winner != "" {
				t.Fatalf("concurrent password updates both succeeded: %q and %q", winner, result.password)
			}
			winner = result.password
		} else if !errors.Is(result.err, user.ErrForbidden) {
			t.Fatalf("concurrent password update error = %v, want nil for one winner or ErrForbidden for stale loser", result.err)
		}
	}
	if winner == "" {
		t.Fatal("concurrent password updates produced no winning credential")
	}
	if verified, _ := service.VerifyPassword(t.Context(), member.ID, "old password"); verified {
		t.Fatal("old password remained valid after a concurrent winning update")
	}
	if verified, err := service.VerifyPassword(t.Context(), member.ID, winner); err != nil || !verified {
		t.Fatalf("winning concurrent credential verification = (verified=%t, err=%v), want true", verified, err)
	}
	if _, err := service.Token(t.Context(), current.AccessToken); err != nil {
		t.Fatalf("current token after concurrent password update: %v", err)
	}
	if _, err := service.Token(t.Context(), other.AccessToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("alternate token after concurrent password update = %v, want ErrNotFound", err)
	}
}

func p104CreateUser(t *testing.T, service *user.Service, name, password string, policy, configuration json.RawMessage) store.User {
	t.Helper()
	created, err := service.Create(t.Context(), name, password)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	created.Policy = policy
	created.Configuration = configuration
	if err := service.Update(t.Context(), created); err != nil {
		t.Fatalf("apply %s policy/configuration: %v", name, err)
	}
	return created
}

func p104UUID(t *testing.T, text string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(text)
	if err != nil {
		t.Fatalf("parse fixture UUID %q: %v", text, err)
	}
	return id
}

func assertP104Users(t *testing.T, got []user.UserDTO, want []dto.UUID) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("listed users = %#v, want IDs %v", got, want)
	}
	for index, wantID := range want {
		if got[index].ID != wantID {
			t.Fatalf("listed user %d = %#v, want ID %s", index, got[index], wantID)
		}
	}
}
