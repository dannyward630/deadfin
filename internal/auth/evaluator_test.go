// SPDX-License-Identifier: GPL-2.0-only

package auth_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

// These tests define the package-local evaluator seam. The evaluator must accept
// small providers rather than a config manager, network manager, or concrete
// identity store:
//
//	NewEvaluator(startup, localNetwork, schedules, now) *Evaluator
//	(*Evaluator).Evaluate(context.Context, PolicyRequest) (Decision, error)
//
// PolicyRequest carries the declarative route policy, Anonymous route fact,
// resolved Identity, opaque persisted store.User policy document, remote address,
// and SyncPlay active-group fact. Decision distinguishes allow, unauthenticated
// (401), forbidden (403), and evaluator configuration failures.
var (
	_ interface{ StartupWizardCompleted() bool }     = fakeStartup{}
	_ interface{ IsInLocalNetwork(netip.Addr) bool } = fakeLocalNetwork{}
	_ interface {
		AccessSchedules(context.Context, dto.UUID) ([]store.AccessSchedule, error)
	} = fakeSchedules{}
)

var evaluatorTestUserID = dto.UUID{0x4c, 0x9b, 0xaa, 0x7d, 0x7f, 0xdf, 0x47, 0x64, 0x8f, 0x93, 0x2e, 0xcd, 0xca, 0xfe, 0x71, 0x06}

const (
	policyRequiresElevation              = "RequiresElevation"
	policyFirstTimeSetupOrElevated       = "FirstTimeSetupOrElevated"
	policyIgnoreParentalControl          = "IgnoreParentalControl"
	policyDownload                       = "Download"
	policyFirstTimeSetupOrDefault        = "FirstTimeSetupOrDefault"
	policyLocalAccessOrRequiresElevation = "LocalAccessOrRequiresElevation"
	policyAnonymousLAN                   = "AnonymousLanAccessPolicy"
	policyFirstTimeSetupOrIgnoreParental = "FirstTimeSetupOrIgnoreParentalControl"
	policySyncPlayHasAccess              = "SyncPlayHasAccess"
	policySyncPlayCreateGroup            = "SyncPlayCreateGroup"
	policySyncPlayJoinGroup              = "SyncPlayJoinGroup"
	policySyncPlayIsInGroup              = "SyncPlayIsInGroup"
	policyCollectionManagement           = "CollectionManagement"
	policyLiveTVAccess                   = "LiveTvAccess"
	policyLiveTVManagement               = "LiveTvManagement"
	policySubtitleManagement             = "SubtitleManagement"
	policyLyricManagement                = "LyricManagement"
	policyDefaultAuthorization           = "DefaultAuthorization"
	policyLocalAccessOnlySourceDead      = "LocalAccessOnly"
	remoteEvaluatorTestIP                = "198.51.100.7"
	localEvaluatorTestIP                 = "192.0.2.7"
)

// Source: upstream/jellyfin/MediaBrowser.Common/Api/Policies.cs and
// upstream/jellyfin/Jellyfin.Server/Extensions/ApiServiceCollectionExtensions.cs.
// LocalAccessOnly is deliberately absent: Policies.cs declares it, but the pinned
// registration installs neither a requirement nor a handler for it.
var effectivePolicyConstants = []string{
	policyRequiresElevation,
	policyFirstTimeSetupOrElevated,
	policyIgnoreParentalControl,
	policyDownload,
	policyFirstTimeSetupOrDefault,
	policyLocalAccessOrRequiresElevation,
	policyAnonymousLAN,
	policyFirstTimeSetupOrIgnoreParental,
	policySyncPlayHasAccess,
	policySyncPlayCreateGroup,
	policySyncPlayJoinGroup,
	policySyncPlayIsInGroup,
	policyCollectionManagement,
	policyLiveTVAccess,
	policyLiveTVManagement,
	policySubtitleManagement,
	policyLyricManagement,
}

func TestEvaluatorRejectsUnauthenticatedProtectedPolicies(t *testing.T) {
	for _, policy := range effectivePolicyConstants {
		if policy == policyFirstTimeSetupOrElevated || policy == policyFirstTimeSetupOrDefault || policy == policyFirstTimeSetupOrIgnoreParental || policy == policyAnonymousLAN {
			continue
		}
		t.Run(policy, func(t *testing.T) {
			decision, err := evaluate(t, evaluatorFixture{}, requestFor(policy, auth.Identity{}, `{}`))
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", policy, err)
			}
			if decision != auth.DecisionUnauthenticated {
				t.Fatalf("Evaluate(%s) = %v, want unauthenticated (401)", policy, decision)
			}
		})
	}
}

func TestEvaluatorPolicyJSONUsesJellyfinDefaultsForOmittedPermissions(t *testing.T) {
	// Source: upstream/jellyfin/MediaBrowser.Model/Users/UserPolicy.cs:16-68.
	// This is a persisted-document decoding contract, not a configuration-default
	// check: store.User.Policy is an opaque JSON blob that may omit every field.
	for _, tc := range []struct {
		policy string
		want   auth.Decision
	}{
		{policyDownload, auth.DecisionAllowed},
		{policyCollectionManagement, auth.DecisionForbidden},
		{policyLiveTVAccess, auth.DecisionAllowed},
		{policyLiveTVManagement, auth.DecisionAllowed},
		{policySubtitleManagement, auth.DecisionForbidden},
		{policyLyricManagement, auth.DecisionForbidden},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			decision, err := evaluate(t, evaluatorFixture{}, requestFor(tc.policy, authenticatedUser(), `{}`))
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", tc.policy, err)
			}
			if decision != tc.want {
				t.Fatalf("Evaluate(%s, omitted policy field) = %v, want %v", tc.policy, decision, tc.want)
			}
		})
	}
}

func TestEvaluatorPermissionPoliciesHonorExplicitPolicyJSON(t *testing.T) {
	// Source: upstream/jellyfin/Jellyfin.Server/Extensions/ApiServiceCollectionExtensions.cs:71-85.
	for _, tc := range []struct {
		name   string
		policy string
		json   string
		want   auth.Decision
	}{
		{"download disabled", policyDownload, `{"EnableContentDownloading":false}`, auth.DecisionForbidden},
		{"collection enabled", policyCollectionManagement, `{"EnableCollectionManagement":true}`, auth.DecisionAllowed},
		{"live tv access disabled", policyLiveTVAccess, `{"EnableLiveTvAccess":false}`, auth.DecisionForbidden},
		{"live tv management disabled", policyLiveTVManagement, `{"EnableLiveTvManagement":false}`, auth.DecisionForbidden},
		{"subtitle enabled", policySubtitleManagement, `{"EnableSubtitleManagement":true}`, auth.DecisionAllowed},
		{"lyric enabled", policyLyricManagement, `{"EnableLyricManagement":true}`, auth.DecisionAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := evaluate(t, evaluatorFixture{}, requestFor(tc.policy, authenticatedUser(), tc.json))
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", tc.policy, err)
			}
			if decision != tc.want {
				t.Fatalf("Evaluate(%s) = %v, want %v", tc.policy, decision, tc.want)
			}
		})
	}
}

func TestEvaluatorAuthenticatesDisabledUsersAsUnauthorized(t *testing.T) {
	// Source: upstream/jellyfin/Emby.Server.Implementations/HttpServer/Security/AuthService.cs:21-40.
	decision, err := evaluate(t, evaluatorFixture{}, requestFor(policyDownload, authenticatedUser(), `{"IsDisabled":true}`))
	if err != nil {
		t.Fatalf("Evaluate(disabled user): %v", err)
	}
	if decision != auth.DecisionUnauthenticated {
		t.Fatalf("Evaluate(disabled user) = %v, want unauthenticated (401)", decision)
	}
}

func TestEvaluatorDefaultAuthorizationOrdersRemoteCheckBeforeAdministratorBypass(t *testing.T) {
	// Source: upstream/jellyfin/Jellyfin.Api/Auth/DefaultAuthorizationPolicy/DefaultAuthorizationHandler.cs:66-84.
	for _, tc := range []struct {
		name     string
		identity auth.Identity
		json     string
		remoteIP string
		want     auth.Decision
	}{
		{"remote-disabled ordinary user", authenticatedUser(), `{"EnableRemoteAccess":false}`, remoteEvaluatorTestIP, auth.DecisionForbidden},
		{"remote-disabled administrator", authenticatedUser(), `{"EnableRemoteAccess":false,"IsAdministrator":true}`, remoteEvaluatorTestIP, auth.DecisionForbidden},
		{"remote-disabled user on LAN", authenticatedUser(), `{"EnableRemoteAccess":false}`, localEvaluatorTestIP, auth.DecisionAllowed},
		{"remote-enabled ordinary user", authenticatedUser(), `{"EnableRemoteAccess":true}`, remoteEvaluatorTestIP, auth.DecisionAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := evaluate(t, evaluatorFixture{}, requestForAt(policyDefaultAuthorization, tc.identity, tc.json, tc.remoteIP))
			if err != nil {
				t.Fatalf("Evaluate(default authorization): %v", err)
			}
			if decision != tc.want {
				t.Fatalf("Evaluate(default authorization) = %v, want %v", decision, tc.want)
			}
		})
	}
}

func TestEvaluatorAppliesAccessSchedulesInLocalTimeAtInclusiveBoundaries(t *testing.T) {
	// Source: upstream/jellyfin/Jellyfin.Data/UserEntityExtensions.cs:143-152,210-219
	// and upstream/jellyfin/Jellyfin.Data/DayOfWeekHelper.cs:10-30.
	mondayNine := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.FixedZone("policy-test", -7*60*60))
	for _, tc := range []struct {
		name      string
		now       time.Time
		schedules []store.AccessSchedule
		want      auth.Decision
	}{
		{"empty schedule allows", mondayNine, nil, auth.DecisionAllowed},
		{"start hour is inclusive", mondayNine, []store.AccessSchedule{{DayOfWeek: 1, StartHour: 9, EndHour: 10}}, auth.DecisionAllowed},
		{"end hour is inclusive", mondayNine.Add(time.Hour), []store.AccessSchedule{{DayOfWeek: 1, StartHour: 9, EndHour: 10}}, auth.DecisionAllowed},
		{"just before start is forbidden", mondayNine.Add(-time.Nanosecond), []store.AccessSchedule{{DayOfWeek: 1, StartHour: 9, EndHour: 10}}, auth.DecisionForbidden},
		{"just after end is forbidden", mondayNine.Add(time.Hour + time.Nanosecond), []store.AccessSchedule{{DayOfWeek: 1, StartHour: 9, EndHour: 10}}, auth.DecisionForbidden},
		{"weekday includes monday", mondayNine, []store.AccessSchedule{{DayOfWeek: 8, StartHour: 0, EndHour: 24}}, auth.DecisionAllowed},
		{"weekend excludes monday", mondayNine, []store.AccessSchedule{{DayOfWeek: 9, StartHour: 0, EndHour: 24}}, auth.DecisionForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := evaluatorFixture{
				now:       tc.now,
				schedules: map[dto.UUID][]store.AccessSchedule{evaluatorTestUserID: tc.schedules},
			}
			decision, err := evaluate(t, fixture, requestFor(policyDefaultAuthorization, authenticatedUser(), `{}`))
			if err != nil {
				t.Fatalf("Evaluate(default authorization): %v", err)
			}
			if decision != tc.want {
				t.Fatalf("Evaluate(default authorization) = %v, want %v", decision, tc.want)
			}
		})
	}
}

func TestEvaluatorRejectsMalformedPersistedPolicyJSONAsAConfigurationError(t *testing.T) {
	decision, err := evaluate(t, evaluatorFixture{}, requestFor(policyDefaultAuthorization, authenticatedUser(), `{"EnableRemoteAccess":`))
	if err == nil {
		t.Fatal("Evaluate(malformed policy JSON) error = nil, want decode error")
	}
	if decision != auth.DecisionConfigurationError {
		t.Fatalf("Evaluate(malformed policy JSON) = %v, want configuration error decision", decision)
	}
}

func TestEvaluatorRejectsTopLevelNullPolicyJSONAsAConfigurationError(t *testing.T) {
	decision, err := evaluate(t, evaluatorFixture{}, requestFor(policyDefaultAuthorization, authenticatedUser(), `null`))
	if err == nil {
		t.Fatal("Evaluate(null policy JSON) error = nil, want decode error")
	}
	if decision != auth.DecisionConfigurationError {
		t.Fatalf("Evaluate(null policy JSON) = %v, want configuration error decision", decision)
	}
}

func TestEvaluatorAdministratorPolicyBypassesParentalSchedule(t *testing.T) {
	fixture := evaluatorFixture{
		schedules: map[dto.UUID][]store.AccessSchedule{
			evaluatorTestUserID: {{DayOfWeek: 7, StartHour: 0, EndHour: 0}},
		},
	}
	decision, err := evaluate(t, fixture, requestFor(policyDefaultAuthorization, authenticatedUser(), `{"IsAdministrator":true}`))
	if err != nil {
		t.Fatalf("Evaluate(administrator with blocked schedule): %v", err)
	}
	if decision != auth.DecisionAllowed {
		t.Fatalf("Evaluate(administrator with blocked schedule) = %v, want allow", decision)
	}
}

func TestEvaluatorIgnoresScheduleOnlyForIgnoreParentalControlPolicies(t *testing.T) {
	blocked := []store.AccessSchedule{{DayOfWeek: 7, StartHour: 0, EndHour: 0}}
	fixture := evaluatorFixture{
		now:       time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC),
		schedules: map[dto.UUID][]store.AccessSchedule{evaluatorTestUserID: blocked},
	}
	for _, tc := range []struct {
		policy string
		json   string
		want   auth.Decision
	}{
		{policyDefaultAuthorization, `{}`, auth.DecisionForbidden},
		{policyIgnoreParentalControl, `{}`, auth.DecisionAllowed},
		{policyFirstTimeSetupOrIgnoreParental, `{}`, auth.DecisionAllowed},
		{policyIgnoreParentalControl, `{"EnableRemoteAccess":false}`, auth.DecisionForbidden},
	} {
		t.Run(tc.policy+tc.json, func(t *testing.T) {
			decision, err := evaluate(t, fixture, requestForAt(tc.policy, authenticatedUser(), tc.json, remoteEvaluatorTestIP))
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", tc.policy, err)
			}
			if decision != tc.want {
				t.Fatalf("Evaluate(%s) = %v, want %v", tc.policy, decision, tc.want)
			}
		})
	}
}

func TestEvaluatorFirstTimeSetupPoliciesUseWizardAndRoleFacts(t *testing.T) {
	blockedSchedule := map[dto.UUID][]store.AccessSchedule{evaluatorTestUserID: {{DayOfWeek: 7, StartHour: 0, EndHour: 0}}}
	for _, tc := range []struct {
		name      string
		completed bool
		policy    string
		identity  auth.Identity
		json      string
		anonymous bool
		want      auth.Decision
	}{
		{"incomplete wizard permits anonymous elevated setup", false, policyFirstTimeSetupOrElevated, auth.Identity{}, `{}`, true, auth.DecisionAllowed},
		{"incomplete wizard permits anonymous default setup", false, policyFirstTimeSetupOrDefault, auth.Identity{}, `{}`, true, auth.DecisionAllowed},
		{"incomplete wizard permits anonymous ignore parental setup", false, policyFirstTimeSetupOrIgnoreParental, auth.Identity{}, `{}`, true, auth.DecisionAllowed},
		{"completed wizard elevates only administrators", true, policyFirstTimeSetupOrElevated, authenticatedUser(), `{}`, false, auth.DecisionForbidden},
		{"completed wizard decodes administrator policy", true, policyFirstTimeSetupOrElevated, authenticatedUser(), `{"IsAdministrator":true}`, false, auth.DecisionAllowed},
		{"completed wizard permits authenticated default setup", true, policyFirstTimeSetupOrDefault, authenticatedUser(), `{}`, false, auth.DecisionAllowed},
		{"completed wizard does not authenticate protected default setup", true, policyFirstTimeSetupOrDefault, auth.Identity{}, `{}`, false, auth.DecisionUnauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := evaluate(t, evaluatorFixture{completed: tc.completed, schedules: blockedSchedule}, requestForWithAnonymous(tc.policy, tc.identity, tc.json, tc.anonymous))
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", tc.policy, err)
			}
			if decision != tc.want {
				t.Fatalf("Evaluate(%s) = %v, want %v", tc.policy, decision, tc.want)
			}
		})
	}
}

func TestEvaluatorAppliesLANPoliciesToRouteAndNetworkFacts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   string
		identity auth.Identity
		json     string
		remoteIP string
		want     auth.Decision
	}{
		{"anonymous LAN allows no credential on LAN", policyAnonymousLAN, auth.Identity{}, `{}`, localEvaluatorTestIP, auth.DecisionAllowed},
		{"anonymous LAN denies no credential remotely", policyAnonymousLAN, auth.Identity{}, `{}`, remoteEvaluatorTestIP, auth.DecisionForbidden},
		{"local or elevated allows ordinary user on LAN", policyLocalAccessOrRequiresElevation, authenticatedUser(), `{}`, localEvaluatorTestIP, auth.DecisionAllowed},
		{"local or elevated denies ordinary remote user", policyLocalAccessOrRequiresElevation, authenticatedUser(), `{}`, remoteEvaluatorTestIP, auth.DecisionForbidden},
		{"local or elevated decodes remote administrator", policyLocalAccessOrRequiresElevation, authenticatedUser(), `{"IsAdministrator":true}`, remoteEvaluatorTestIP, auth.DecisionAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := evaluate(t, evaluatorFixture{}, requestForAt(tc.policy, tc.identity, tc.json, tc.remoteIP))
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", tc.policy, err)
			}
			if decision != tc.want {
				t.Fatalf("Evaluate(%s) = %v, want %v", tc.policy, decision, tc.want)
			}
		})
	}
}

func TestEvaluatorAPIKeyAndAdministratorElevationDecisions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   string
		identity auth.Identity
		json     string
		want     auth.Decision
	}{
		{"ordinary user cannot elevate", policyRequiresElevation, authenticatedUser(), `{}`, auth.DecisionForbidden},
		{"administrator policy can elevate", policyRequiresElevation, authenticatedUser(), `{"IsAdministrator":true}`, auth.DecisionAllowed},
		{"API key can elevate", policyRequiresElevation, apiKey(), `{}`, auth.DecisionAllowed},
		{"API key bypasses denied permission", policyCollectionManagement, apiKey(), `{"EnableCollectionManagement":false}`, auth.DecisionAllowed},
		{"API key bypasses parental schedule", policyDefaultAuthorization, apiKey(), `{}`, auth.DecisionAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := evaluatorFixture{schedules: map[dto.UUID][]store.AccessSchedule{evaluatorTestUserID: {{DayOfWeek: 7, StartHour: 0, EndHour: 0}}}}
			decision, err := evaluate(t, fixture, requestFor(tc.policy, tc.identity, tc.json))
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", tc.policy, err)
			}
			if decision != tc.want {
				t.Fatalf("Evaluate(%s) = %v, want %v", tc.policy, decision, tc.want)
			}
		})
	}
}

func TestEvaluatorSyncPlayPoliciesUsePersistedAccessAndActiveGroupFacts(t *testing.T) {
	// Source: upstream/jellyfin/Jellyfin.Api/Auth/SyncPlayAccessPolicy/SyncPlayAccessHandler.cs:43-72.
	for _, tc := range []struct {
		name   string
		policy string
		json   string
		active bool
		want   auth.Decision
	}{
		{"none denies has access", policySyncPlayHasAccess, `{"SyncPlayAccess":"None"}`, false, auth.DecisionForbidden},
		{"none denies creation", policySyncPlayCreateGroup, `{"SyncPlayAccess":"None"}`, false, auth.DecisionForbidden},
		{"join-only has access", policySyncPlayHasAccess, `{"SyncPlayAccess":"JoinGroups"}`, false, auth.DecisionAllowed},
		{"join-only cannot create", policySyncPlayCreateGroup, `{"SyncPlayAccess":"JoinGroups"}`, false, auth.DecisionForbidden},
		{"join-only can join", policySyncPlayJoinGroup, `{"SyncPlayAccess":"JoinGroups"}`, false, auth.DecisionAllowed},
		{"active group has access despite no persisted access", policySyncPlayHasAccess, `{"SyncPlayAccess":"None"}`, true, auth.DecisionAllowed},
		{"active group is in group", policySyncPlayIsInGroup, `{"SyncPlayAccess":"None"}`, true, auth.DecisionAllowed},
		{"inactive user is not in group", policySyncPlayIsInGroup, `{"SyncPlayAccess":"CreateAndJoinGroups"}`, false, auth.DecisionForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestFor(tc.policy, authenticatedUser(), tc.json)
			request.SyncPlayActive = tc.active
			decision, err := evaluate(t, evaluatorFixture{}, request)
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", tc.policy, err)
			}
			if decision != tc.want {
				t.Fatalf("Evaluate(%s) = %v, want %v", tc.policy, decision, tc.want)
			}
		})
	}
}

func TestEvaluatorAdministratorsOverrideEveryPermissionPolicyAfterRemoteAuthorization(t *testing.T) {
	for _, tc := range []struct {
		policy string
		json   string
	}{
		{policyDownload, `{"IsAdministrator":true,"EnableRemoteAccess":true,"EnableContentDownloading":false}`},
		{policyCollectionManagement, `{"IsAdministrator":true,"EnableRemoteAccess":true,"EnableCollectionManagement":false}`},
		{policyLiveTVAccess, `{"IsAdministrator":true,"EnableRemoteAccess":true,"EnableLiveTvAccess":false}`},
		{policyLiveTVManagement, `{"IsAdministrator":true,"EnableRemoteAccess":true,"EnableLiveTvManagement":false}`},
		{policySubtitleManagement, `{"IsAdministrator":true,"EnableRemoteAccess":true,"EnableSubtitleManagement":false}`},
		{policyLyricManagement, `{"IsAdministrator":true,"EnableRemoteAccess":true,"EnableLyricManagement":false}`},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			decision, err := evaluate(t, evaluatorFixture{}, requestForAt(tc.policy, authenticatedUser(), tc.json, remoteEvaluatorTestIP))
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", tc.policy, err)
			}
			if decision != auth.DecisionAllowed {
				t.Fatalf("Evaluate(%s, administrator) = %v, want allow", tc.policy, decision)
			}
		})
	}
}

func TestEvaluatorSyncPlayPoliciesHonorDefaultGuardsBeforeSyncPlayAccess(t *testing.T) {
	syncPolicies := []string{policySyncPlayHasAccess, policySyncPlayCreateGroup, policySyncPlayJoinGroup, policySyncPlayIsInGroup}
	blocked := map[dto.UUID][]store.AccessSchedule{evaluatorTestUserID: {{DayOfWeek: 7, StartHour: 0, EndHour: 0}}}
	for _, policy := range syncPolicies {
		t.Run(policy+" remote denied", func(t *testing.T) {
			request := requestForAt(policy, authenticatedUser(), `{"EnableRemoteAccess":false,"SyncPlayAccess":"CreateAndJoinGroups"}`, remoteEvaluatorTestIP)
			request.SyncPlayActive = true
			decision, err := evaluate(t, evaluatorFixture{}, request)
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", policy, err)
			}
			if decision != auth.DecisionForbidden {
				t.Fatalf("Evaluate(%s, remote denied) = %v, want forbidden", policy, decision)
			}
		})
		t.Run(policy+" schedule denied", func(t *testing.T) {
			request := requestFor(policy, authenticatedUser(), `{"SyncPlayAccess":"CreateAndJoinGroups"}`)
			request.SyncPlayActive = true
			decision, err := evaluate(t, evaluatorFixture{schedules: blocked}, request)
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", policy, err)
			}
			if decision != auth.DecisionForbidden {
				t.Fatalf("Evaluate(%s, blocked schedule) = %v, want forbidden", policy, decision)
			}
		})
		t.Run(policy+" administrator bypasses schedule", func(t *testing.T) {
			request := requestFor(policy, authenticatedUser(), `{"IsAdministrator":true,"SyncPlayAccess":"CreateAndJoinGroups"}`)
			request.SyncPlayActive = true
			decision, err := evaluate(t, evaluatorFixture{schedules: blocked}, request)
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", policy, err)
			}
			if decision != auth.DecisionAllowed {
				t.Fatalf("Evaluate(%s, administrator) = %v, want allow", policy, decision)
			}
		})
	}
}

func TestEvaluatorAdministratorsBypassAllSyncPlaySpecificChecks(t *testing.T) {
	for _, policy := range []string{policySyncPlayHasAccess, policySyncPlayCreateGroup, policySyncPlayJoinGroup, policySyncPlayIsInGroup} {
		t.Run(policy, func(t *testing.T) {
			request := requestFor(policy, authenticatedUser(), `{"IsAdministrator":true,"SyncPlayAccess":"None"}`)
			decision, err := evaluate(t, evaluatorFixture{}, request)
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", policy, err)
			}
			if decision != auth.DecisionAllowed {
				t.Fatalf("Evaluate(%s, administrator with no SyncPlay access) = %v, want allow", policy, decision)
			}
		})
	}
}

func TestEvaluatorRejectsNullForEveryAuthorizationBoolean(t *testing.T) {
	for _, field := range []string{
		"IsDisabled",
		"IsAdministrator",
		"EnableRemoteAccess",
		"EnableContentDownloading",
		"EnableCollectionManagement",
		"EnableLiveTvAccess",
		"EnableLiveTvManagement",
		"EnableSubtitleManagement",
		"EnableLyricManagement",
	} {
		t.Run(field, func(t *testing.T) {
			decision, err := evaluate(t, evaluatorFixture{}, requestFor(policyDefaultAuthorization, authenticatedUser(), `{"`+field+`":null}`))
			if err == nil {
				t.Fatalf("Evaluate(policy with %s:null) error = nil, want configuration error", field)
			}
			if decision != auth.DecisionConfigurationError {
				t.Fatalf("Evaluate(policy with %s:null) = %v, want configuration error decision", field, decision)
			}
		})
	}
}

func TestEvaluatorDecodesSyncPlayAccessNamesAndNumericEnumValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy string
		json   string
		want   auth.Decision
	}{
		{"case-insensitive create-and-join name", policySyncPlayCreateGroup, `{"SyncPlayAccess":"cReAtEaNdJoInGrOuPs"}`, auth.DecisionAllowed},
		{"numeric create-and-join", policySyncPlayCreateGroup, `{"SyncPlayAccess":0}`, auth.DecisionAllowed},
		{"numeric join-only", policySyncPlayJoinGroup, `{"SyncPlayAccess":1}`, auth.DecisionAllowed},
		{"numeric none", policySyncPlayHasAccess, `{"SyncPlayAccess":2}`, auth.DecisionForbidden},
		{"numeric value outside named members", policySyncPlayHasAccess, `{"SyncPlayAccess":3}`, auth.DecisionForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := evaluate(t, evaluatorFixture{}, requestFor(tc.policy, authenticatedUser(), tc.json))
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", tc.policy, err)
			}
			if decision != tc.want {
				t.Fatalf("Evaluate(%s) = %v, want %v", tc.policy, decision, tc.want)
			}
		})
	}
}

func TestEvaluatorTreatsUnregisteredLocalAccessOnlyAndUnknownPoliciesAsConfigurationErrors(t *testing.T) {
	for _, policy := range []string{policyLocalAccessOnlySourceDead, "NotARegisteredJellyfinPolicy"} {
		t.Run(policy, func(t *testing.T) {
			decision, err := evaluate(t, evaluatorFixture{}, requestFor(policy, authenticatedUser(), `{}`))
			if !errors.Is(err, auth.ErrUnknownPolicy) {
				t.Fatalf("Evaluate(%s) error = %v, want ErrUnknownPolicy", policy, err)
			}
			if decision != auth.DecisionConfigurationError {
				t.Fatalf("Evaluate(%s) = %v, want configuration error decision", policy, decision)
			}
		})
	}
}

func TestEvaluatorAnonymousRouteBypassesFirstTimePoliciesAfterSetupCompletes(t *testing.T) {
	for _, policy := range []string{policyFirstTimeSetupOrElevated, policyFirstTimeSetupOrDefault, policyFirstTimeSetupOrIgnoreParental} {
		t.Run(policy, func(t *testing.T) {
			decision, err := evaluate(t, evaluatorFixture{completed: true}, requestForWithAnonymous(policy, auth.Identity{}, `{}`, true))
			if err != nil {
				t.Fatalf("Evaluate(anonymous %s): %v", policy, err)
			}
			if decision != auth.DecisionAllowed {
				t.Fatalf("Evaluate(anonymous %s) = %v, want allow", policy, decision)
			}
		})
	}
}

func TestEvaluatorAnonymousRouteBypassesPolicyLookupAndAuthentication(t *testing.T) {
	for _, policy := range []string{"", "NotARegisteredJellyfinPolicy"} {
		t.Run(policy, func(t *testing.T) {
			decision, err := evaluate(t, evaluatorFixture{}, requestForWithAnonymous(policy, auth.Identity{}, `{}`, true))
			if err != nil {
				t.Fatalf("Evaluate(anonymous route, %q): %v", policy, err)
			}
			if decision != auth.DecisionAllowed {
				t.Fatalf("Evaluate(anonymous route, %q) = %v, want allow", policy, decision)
			}
		})
	}
}

type evaluatorFixture struct {
	completed  bool
	now        time.Time
	schedules  map[dto.UUID][]store.AccessSchedule
	localAddrs map[netip.Addr]bool
}

func evaluate(t *testing.T, fixture evaluatorFixture, request auth.PolicyRequest) (auth.Decision, error) {
	t.Helper()
	if fixture.now.IsZero() {
		fixture.now = time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	}
	if fixture.localAddrs == nil {
		fixture.localAddrs = map[netip.Addr]bool{netip.MustParseAddr(localEvaluatorTestIP): true}
	}
	evaluator := auth.NewEvaluator(
		fakeStartup{completed: fixture.completed},
		fakeLocalNetwork{local: fixture.localAddrs},
		fakeSchedules{byUser: fixture.schedules},
		func() time.Time { return fixture.now },
	)
	return evaluator.Evaluate(t.Context(), request)
}

func requestFor(policy string, identity auth.Identity, policyJSON string) auth.PolicyRequest {
	return requestForWithAnonymous(policy, identity, policyJSON, false)
}

func requestForAt(policy string, identity auth.Identity, policyJSON, remoteIP string) auth.PolicyRequest {
	request := requestFor(policy, identity, policyJSON)
	request.RemoteIP = netip.MustParseAddr(remoteIP)
	return request
}

func requestForWithAnonymous(policy string, identity auth.Identity, policyJSON string, anonymous bool) auth.PolicyRequest {
	return auth.PolicyRequest{
		Policy:    policy,
		Anonymous: anonymous,
		Identity:  identity,
		User: store.User{
			ID:     evaluatorTestUserID,
			Policy: []byte(policyJSON),
		},
	}
}

func authenticatedUser() auth.Identity {
	return auth.Identity{Authenticated: true, UserID: evaluatorTestUserID}
}

func apiKey() auth.Identity {
	return auth.Identity{Authenticated: true, APIKey: true, Administrator: true}
}

type fakeStartup struct{ completed bool }

func (startup fakeStartup) StartupWizardCompleted() bool { return startup.completed }

type fakeLocalNetwork struct{ local map[netip.Addr]bool }

func (network fakeLocalNetwork) IsInLocalNetwork(address netip.Addr) bool {
	return !address.IsValid() || network.local[address]
}

type fakeSchedules struct {
	byUser map[dto.UUID][]store.AccessSchedule
}

func (schedules fakeSchedules) AccessSchedules(_ context.Context, userID dto.UUID) ([]store.AccessSchedule, error) {
	return schedules.byUser[userID], nil
}
