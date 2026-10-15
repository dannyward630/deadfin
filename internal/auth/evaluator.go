// SPDX-License-Identifier: GPL-2.0-only

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

// Policy names the declarative authorization policy attached to a route.
type Policy string

const (
	PolicyRequiresElevation              Policy = "RequiresElevation"
	PolicyFirstTimeSetupOrElevated       Policy = "FirstTimeSetupOrElevated"
	PolicyIgnoreParentalControl          Policy = "IgnoreParentalControl"
	PolicyDownload                       Policy = "Download"
	PolicyFirstTimeSetupOrDefault        Policy = "FirstTimeSetupOrDefault"
	PolicyLocalAccessOrRequiresElevation Policy = "LocalAccessOrRequiresElevation"
	PolicyAnonymousLANAccess             Policy = "AnonymousLanAccessPolicy"
	PolicyFirstTimeSetupOrIgnoreParental Policy = "FirstTimeSetupOrIgnoreParentalControl"
	PolicySyncPlayHasAccess              Policy = "SyncPlayHasAccess"
	PolicySyncPlayCreateGroup            Policy = "SyncPlayCreateGroup"
	PolicySyncPlayJoinGroup              Policy = "SyncPlayJoinGroup"
	PolicySyncPlayIsInGroup              Policy = "SyncPlayIsInGroup"
	PolicyCollectionManagement           Policy = "CollectionManagement"
	PolicyLiveTVAccess                   Policy = "LiveTvAccess"
	PolicyLiveTVManagement               Policy = "LiveTvManagement"
	PolicySubtitleManagement             Policy = "SubtitleManagement"
	PolicyLyricManagement                Policy = "LyricManagement"
	PolicyDefaultAuthorization           Policy = "DefaultAuthorization"

	// PolicyLocalAccessOnly is declared by upstream but unregistered in the
	// pinned authorization services, so Evaluate deliberately rejects it.
	PolicyLocalAccessOnly Policy = "LocalAccessOnly"
)

// Decision distinguishes the route outcomes that map to success, 401, 403, and
// evaluator misconfiguration respectively.
type Decision uint8

const (
	DecisionAllowed Decision = iota
	DecisionUnauthenticated
	DecisionForbidden
	DecisionConfigurationError
)

var ErrUnknownPolicy = errors.New("unknown authorization policy")

type startupState interface {
	StartupWizardCompleted() bool
}

// LocalNetwork classifies peer addresses for authorization-sensitive flows.
// Implementations must apply the same locality policy wherever remote access
// eligibility is evaluated.
type LocalNetwork interface {
	IsInLocalNetwork(netip.Addr) bool
}

// remoteAccessNetwork is an optional policy capability for network adapters
// that can read the server-wide remote-access setting. Keeping it optional
// preserves the locality-only evaluator seam for callers that do not own that
// configuration.
type remoteAccessNetwork interface {
	RemoteAccessEnabled() bool
}

type accessSchedules interface {
	AccessSchedules(context.Context, dto.UUID) ([]store.AccessSchedule, error)
}

// PolicyRequest holds the route and request facts needed to evaluate a policy.
type PolicyRequest struct {
	Policy         string
	Anonymous      bool
	Identity       Identity
	User           store.User
	RemoteIP       netip.Addr
	SyncPlayActive bool
}

// Evaluator applies Jellyfin's registered route policies without depending on
// the HTTP router, configuration manager, or concrete identity store.
type Evaluator struct {
	startup   startupState
	network   LocalNetwork
	schedules accessSchedules
	now       func() time.Time
}

// NewEvaluator constructs an authorization policy evaluator from small read-only
// seams owned by startup, networking, and identity persistence.
func NewEvaluator(startup startupState, network LocalNetwork, schedules accessSchedules, now func() time.Time) *Evaluator {
	return &Evaluator{startup: startup, network: network, schedules: schedules, now: now}
}

// Evaluate decides whether request satisfies its declarative route policy.
func (evaluator *Evaluator) Evaluate(ctx context.Context, request PolicyRequest) (Decision, error) {
	if request.Anonymous {
		return DecisionAllowed, nil
	}
	if !knownPolicy(request.Policy) {
		return DecisionConfigurationError, fmt.Errorf("%w: %s", ErrUnknownPolicy, request.Policy)
	}
	if request.Policy == string(PolicyAnonymousLANAccess) {
		if evaluator.isLocal(request.RemoteIP) {
			return DecisionAllowed, nil
		}
		return DecisionForbidden, nil
	}

	firstTimePolicy := isFirstTimePolicy(request.Policy)
	if firstTimePolicy && !evaluator.startup.StartupWizardCompleted() && !request.Identity.Authenticated {
		return DecisionAllowed, nil
	}
	if !request.Identity.Authenticated {
		return DecisionUnauthenticated, nil
	}
	if !evaluator.isLocal(request.RemoteIP) && !evaluator.remoteAccessEnabled() {
		return DecisionForbidden, nil
	}
	if request.Identity.APIKey {
		return evaluator.evaluateAPIKey(request)
	}

	policy, err := decodeUserPolicy(request.User.Policy)
	if err != nil {
		return DecisionConfigurationError, err
	}
	if policy.IsDisabled {
		return DecisionUnauthenticated, nil
	}

	administrator := request.Identity.Administrator || policy.IsAdministrator
	switch request.Policy {
	case string(PolicyRequiresElevation):
		return decisionFor(administrator), nil
	case string(PolicyLocalAccessOrRequiresElevation):
		return decisionFor(evaluator.isLocal(request.RemoteIP) || administrator), nil
	}

	validateSchedule := request.Policy != string(PolicyIgnoreParentalControl) && !firstTimePolicy
	if decision, err := evaluator.defaultAuthorization(ctx, request, policy, administrator, validateSchedule); err != nil || decision != DecisionAllowed {
		return decision, err
	}

	switch request.Policy {
	case string(PolicyDefaultAuthorization), string(PolicyIgnoreParentalControl):
		return DecisionAllowed, nil
	case string(PolicyFirstTimeSetupOrElevated):
		return decisionFor(!evaluator.startup.StartupWizardCompleted() || administrator), nil
	case string(PolicyFirstTimeSetupOrDefault), string(PolicyFirstTimeSetupOrIgnoreParental):
		return DecisionAllowed, nil
	case string(PolicyDownload):
		return decisionFor(administrator || policy.EnableContentDownloading), nil
	case string(PolicyCollectionManagement):
		return decisionFor(administrator || policy.EnableCollectionManagement), nil
	case string(PolicyLiveTVAccess):
		return decisionFor(administrator || policy.EnableLiveTVAccess), nil
	case string(PolicyLiveTVManagement):
		return decisionFor(administrator || policy.EnableLiveTVManagement), nil
	case string(PolicySubtitleManagement):
		return decisionFor(administrator || policy.EnableSubtitleManagement), nil
	case string(PolicyLyricManagement):
		return decisionFor(administrator || policy.EnableLyricManagement), nil
	case string(PolicySyncPlayHasAccess):
		return decisionFor(administrator || policy.SyncPlayAccess == syncPlayCreateAndJoin || policy.SyncPlayAccess == syncPlayJoin || request.SyncPlayActive), nil
	case string(PolicySyncPlayCreateGroup):
		return decisionFor(administrator || policy.SyncPlayAccess == syncPlayCreateAndJoin), nil
	case string(PolicySyncPlayJoinGroup):
		return decisionFor(administrator || policy.SyncPlayAccess == syncPlayCreateAndJoin || policy.SyncPlayAccess == syncPlayJoin), nil
	case string(PolicySyncPlayIsInGroup):
		return decisionFor(administrator || request.SyncPlayActive), nil
	default:
		return DecisionConfigurationError, fmt.Errorf("%w: %s", ErrUnknownPolicy, request.Policy)
	}
}

func (evaluator *Evaluator) evaluateAPIKey(request PolicyRequest) (Decision, error) {
	switch request.Policy {
	case string(PolicyRequiresElevation), string(PolicyDefaultAuthorization), string(PolicyIgnoreParentalControl),
		string(PolicyDownload), string(PolicyCollectionManagement), string(PolicyLiveTVAccess), string(PolicyLiveTVManagement),
		string(PolicySubtitleManagement), string(PolicyLyricManagement):
		return DecisionAllowed, nil
	case string(PolicyFirstTimeSetupOrElevated), string(PolicyFirstTimeSetupOrDefault), string(PolicyFirstTimeSetupOrIgnoreParental):
		return DecisionAllowed, nil
	case string(PolicyLocalAccessOrRequiresElevation):
		return DecisionAllowed, nil
	case string(PolicySyncPlayHasAccess), string(PolicySyncPlayCreateGroup), string(PolicySyncPlayJoinGroup), string(PolicySyncPlayIsInGroup):
		return DecisionForbidden, nil
	default:
		return DecisionConfigurationError, fmt.Errorf("%w: %s", ErrUnknownPolicy, request.Policy)
	}
}

func (evaluator *Evaluator) defaultAuthorization(ctx context.Context, request PolicyRequest, policy userPolicy, administrator, validateSchedule bool) (Decision, error) {
	if !evaluator.isLocal(request.RemoteIP) && !policy.EnableRemoteAccess {
		return DecisionForbidden, nil
	}
	if administrator || !validateSchedule {
		return DecisionAllowed, nil
	}

	schedules, err := evaluator.schedules.AccessSchedules(ctx, request.User.ID)
	if err != nil {
		return DecisionConfigurationError, fmt.Errorf("read access schedules: %w", err)
	}
	return decisionFor(scheduleAllowed(schedules, evaluator.now())), nil
}

func (evaluator *Evaluator) isLocal(address netip.Addr) bool {
	return !address.IsValid() || evaluator.network.IsInLocalNetwork(address)
}

func (evaluator *Evaluator) remoteAccessEnabled() bool {
	configuration, ok := evaluator.network.(remoteAccessNetwork)
	return !ok || configuration.RemoteAccessEnabled()
}

func knownPolicy(policy string) bool {
	switch policy {
	case string(PolicyRequiresElevation), string(PolicyFirstTimeSetupOrElevated), string(PolicyIgnoreParentalControl), string(PolicyDownload),
		string(PolicyFirstTimeSetupOrDefault), string(PolicyLocalAccessOrRequiresElevation), string(PolicyAnonymousLANAccess),
		string(PolicyFirstTimeSetupOrIgnoreParental), string(PolicySyncPlayHasAccess), string(PolicySyncPlayCreateGroup),
		string(PolicySyncPlayJoinGroup), string(PolicySyncPlayIsInGroup), string(PolicyCollectionManagement), string(PolicyLiveTVAccess),
		string(PolicyLiveTVManagement), string(PolicySubtitleManagement), string(PolicyLyricManagement), string(PolicyDefaultAuthorization):
		return true
	default:
		return false
	}
}

func isFirstTimePolicy(policy string) bool {
	switch policy {
	case string(PolicyFirstTimeSetupOrElevated), string(PolicyFirstTimeSetupOrDefault), string(PolicyFirstTimeSetupOrIgnoreParental):
		return true
	default:
		return false
	}
}

func decisionFor(allowed bool) Decision {
	if allowed {
		return DecisionAllowed
	}
	return DecisionForbidden
}

type syncPlayAccess int32

const (
	syncPlayCreateAndJoin syncPlayAccess = iota
	syncPlayJoin
	syncPlayNone
)

func (access *syncPlayAccess) UnmarshalJSON(value []byte) error {
	if strings.TrimSpace(string(value)) == "null" {
		return errors.New("SyncPlayAccess cannot be null")
	}
	var name string
	if err := json.Unmarshal(value, &name); err == nil {
		switch {
		case strings.EqualFold(name, "CreateAndJoinGroups"):
			*access = syncPlayCreateAndJoin
			return nil
		case strings.EqualFold(name, "JoinGroups"):
			*access = syncPlayJoin
			return nil
		case strings.EqualFold(name, "None"):
			*access = syncPlayNone
			return nil
		default:
			return fmt.Errorf("invalid SyncPlayAccess %q", name)
		}
	}
	var number int32
	if err := json.Unmarshal(value, &number); err != nil {
		return fmt.Errorf("invalid SyncPlayAccess: %w", err)
	}
	*access = syncPlayAccess(number)
	return nil
}

// userPolicy is the subset of opaque persisted policy JSON used by registered
// Jellyfin authorization policies. Its initial value reproduces UserPolicy's
// constructor defaults before JSON overlays explicit persisted values.
type userPolicy struct {
	IsAdministrator            bool
	IsDisabled                 bool
	EnableRemoteAccess         bool
	EnableContentDownloading   bool
	EnableCollectionManagement bool
	EnableLiveTVAccess         bool
	EnableLiveTVManagement     bool
	EnableSubtitleManagement   bool
	EnableLyricManagement      bool
	SyncPlayAccess             syncPlayAccess
}

func decodeUserPolicy(document []byte) (userPolicy, error) {
	policy := userPolicy{
		EnableRemoteAccess:       true,
		EnableContentDownloading: true,
		EnableLiveTVAccess:       true,
		EnableLiveTVManagement:   true,
		SyncPlayAccess:           syncPlayCreateAndJoin,
	}
	if len(document) == 0 {
		return policy, nil
	}
	if err := dto.DecodeUserPolicy(document, &policy); err != nil {
		return userPolicy{}, err
	}
	return policy, nil
}

// UserDisabled reports whether a persisted user policy disables its owner.
// It validates the policy with the same decoding rules as Evaluator.
func UserDisabled(user store.User) (bool, error) {
	policy, err := decodeUserPolicy(user.Policy)
	if err != nil {
		return false, err
	}
	return policy.IsDisabled, nil
}

// UserAdministrator resolves the persisted administrator permission for a
// request identity after its token has been bound to a user record.
func UserAdministrator(user store.User) (bool, error) {
	policy, err := decodeUserPolicy(user.Policy)
	if err != nil {
		return false, err
	}
	return policy.IsAdministrator, nil
}

func scheduleAllowed(schedules []store.AccessSchedule, now time.Time) bool {
	if len(schedules) == 0 {
		return true
	}
	hour := float64(now.Hour()) + float64(now.Minute())/60 + float64(now.Second())/3600 + float64(now.Nanosecond())/(3600*1e9)
	day := int(now.Weekday())
	for _, schedule := range schedules {
		if scheduleContainsDay(schedule.DayOfWeek, day) && hour >= schedule.StartHour && hour <= schedule.EndHour {
			return true
		}
	}
	return false
}

func scheduleContainsDay(scheduleDay, day int) bool {
	switch scheduleDay {
	case 7:
		return true
	case 8:
		return day >= int(time.Monday) && day <= int(time.Friday)
	case 9:
		return day == int(time.Saturday) || day == int(time.Sunday)
	default:
		return scheduleDay == day
	}
}
