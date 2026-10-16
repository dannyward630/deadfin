// SPDX-License-Identifier: GPL-2.0-only

package session_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"

	"go.uber.org/goleak"
)

var (
	sessionTestUserA = dto.UUID{0x24, 0x15, 0x5a, 0xc3, 0x3e, 0x64, 0x49, 0x90, 0x85, 0x3b, 0x16, 0x65, 0x0f, 0xd9, 0xd6, 0x1a}
	sessionTestUserB = dto.UUID{0x87, 0xb8, 0x42, 0x84, 0x38, 0x75, 0x42, 0x91, 0xa9, 0x80, 0xc1, 0xc6, 0x09, 0xae, 0x90, 0x0b}
)

// Source: upstream/jellyfin/Emby.Server.Implementations/Session/SessionManager.cs:478-546
// and upstream/jellyfin/MediaBrowser.Common/Extensions/BaseExtensions.cs:25-35.
// Session identities are an MD5-derived .NET Guid over the UTF-16LE bytes of the
// concatenated client/device key. The vectors include non-ASCII input to pin both
// UTF-16 encoding and Guid's little-endian first three fields.
func TestManagerDerivesJellyfinSessionIDsFromClientAndDevice(t *testing.T) {
	for _, tc := range []struct {
		client   string
		deviceID string
		wantID   string
	}{
		{"Client", "Device", "6320c089bfd8af4e90ab046cd5fc150c"},
		{"Jellyfin", "android-123", "b1cd3db3d1b1e97ac2df847c704f9fd6"},
		{"客户端", "device-π", "70d7282158579b2dbfc91f3ec844aa90"},
	} {
		t.Run(tc.client+"/"+tc.deviceID, func(t *testing.T) {
			manager, _ := newManager(t)
			got := logActivity(t, manager, tc.client, tc.deviceID, sessionTestUserA)
			if got.ID != tc.wantID {
				t.Fatalf("session ID for client=%q device=%q = %q, want %q", tc.client, tc.deviceID, got.ID, tc.wantID)
			}
		})
	}
}

func TestManagerKeysSessionsByClientAndDeviceAndRefreshesActivity(t *testing.T) {
	manager, fake := newManager(t)
	first := logActivity(t, manager, "Jellyfin Web", "shared-device", sessionTestUserA)

	fake.Advance(17 * time.Second)
	refreshed, err := manager.LogActivity(t.Context(), session.Activity{
		Client: "Jellyfin Web", Version: "10.11.0", DeviceID: "shared-device", DeviceName: "Renamed Browser", UserID: sessionTestUserA,
	})
	if err != nil {
		t.Fatalf("refresh session activity: %v", err)
	}
	if refreshed.ID != first.ID {
		t.Fatalf("refreshed session ID = %q, want existing %q", refreshed.ID, first.ID)
	}
	if !refreshed.LastActivity.Equal(fake.Now()) || refreshed.DeviceName != "Renamed Browser" || refreshed.Version != "10.11.0" {
		t.Fatalf("refreshed session = %#v, want current activity metadata at %s", refreshed, fake.Now())
	}

	logActivity(t, manager, "Jellyfin Android", "shared-device", sessionTestUserA)
	logActivity(t, manager, "Jellyfin Web", "other-device", sessionTestUserA)
	if got := manager.Sessions(session.Query{}); len(got) != 3 {
		t.Fatalf("session count after distinct client/device pairs = %d, want 3", len(got))
	}
}

// Case-insensitive matching applies independently to each identity component;
// it must not erase the boundary between a client and its device ID.
func TestManagerSeparatesCaseInsensitiveCompositeSessionIdentities(t *testing.T) {
	manager, _ := newManager(t)
	first := logActivity(t, manager, "ab", "c", sessionTestUserA)
	second := logActivity(t, manager, "a", "bc", sessionTestUserB)

	if first.ID == second.ID {
		t.Fatalf("distinct client/device pairs received the same session ID %q", first.ID)
	}
	if got := manager.Sessions(session.Query{}); len(got) != 2 {
		t.Fatalf("sessions for (ab, c) and (a, bc) = %#v, want two distinct sessions", got)
	}

	refreshed, err := manager.LogActivity(t.Context(), session.Activity{
		Client: "AB", Version: "10.11.0", DeviceID: "C", DeviceName: "first refreshed", UserID: sessionTestUserA,
	})
	if err != nil {
		t.Fatalf("refresh first composite identity: %v", err)
	}
	if refreshed.ID != first.ID {
		t.Fatalf("refresh (AB, C) session ID = %q, want existing first ID %q", refreshed.ID, first.ID)
	}

	resolvedFirst, found := manager.Session("aB", "c")
	if !found || resolvedFirst.ID != first.ID || resolvedFirst.Version != "10.11.0" || resolvedFirst.UserID != sessionTestUserA {
		t.Fatalf("Session(aB, c) = %#v, found=%t, want independently refreshed first session", resolvedFirst, found)
	}
	resolvedSecond, found := manager.Session("A", "bC")
	if !found || resolvedSecond.ID != second.ID || resolvedSecond.Version != "10.10.10" || resolvedSecond.UserID != sessionTestUserB {
		t.Fatalf("Session(A, bC) = %#v, found=%t, want untouched second session", resolvedSecond, found)
	}
}

func TestManagerCapabilityAndSessionSnapshotsAreDefensive(t *testing.T) {
	manager, _ := newManager(t)
	created := logActivity(t, manager, "Jellyfin Web", "snapshot-device", sessionTestUserA)
	id := created.ID
	input := session.Capabilities{
		PlayableMediaTypes:           []string{"Video", "Audio"},
		SupportedCommands:            []string{"Play", "Stop"},
		SupportsMediaControl:         true,
		SupportsPersistentIdentifier: true,
	}
	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{ID: &id, CurrentID: id, Capabilities: input}); err != nil {
		t.Fatalf("report capabilities: %v", err)
	}

	input.PlayableMediaTypes[0] = "Mutated input"
	input.SupportedCommands[0] = "Mutated input"
	first := manager.Sessions(session.Query{})
	if len(first) != 1 {
		t.Fatalf("snapshot session count = %d, want 1", len(first))
	}
	if first[0].SupportsRemoteControl {
		t.Fatalf("capability report set root SupportsRemoteControl without a live controller: %#v", first[0])
	}
	if !first[0].Capabilities.SupportsMediaControl || !first[0].Capabilities.SupportsPersistentIdentifier {
		t.Fatalf("reported capabilities = %#v, want media control and persistent identifier true", first[0].Capabilities)
	}
	first[0].Capabilities.PlayableMediaTypes[1] = "Mutated snapshot"
	first[0].Capabilities.SupportedCommands[1] = "Mutated snapshot"
	first[0].DeviceName = "Mutated snapshot"

	second := manager.Sessions(session.Query{})
	if got, want := second[0].Capabilities.PlayableMediaTypes, []string{"Video", "Audio"}; !sameStrings(got, want) {
		t.Fatalf("playable media snapshot = %#v, want %#v", got, want)
	}
	if got, want := second[0].Capabilities.SupportedCommands, []string{"Play", "Stop"}; !sameStrings(got, want) {
		t.Fatalf("supported commands snapshot = %#v, want %#v", got, want)
	}
	if second[0].DeviceName == "Mutated snapshot" {
		t.Fatalf("mutating returned session snapshot changed manager state: %#v", second[0])
	}
}

// Device profiles are stored against the active client/device pair rather than
// a caller's session ID; playback selection must retain both identity facts.
func TestManagerStoresDeviceProfilesByCaseInsensitiveClientAndDeviceAndDefensivelyCopiesBytes(t *testing.T) {
	manager, _ := newManager(t)
	created := logActivity(t, manager, "Jellyfin Web", "Profile-Device", sessionTestUserA)
	id := created.ID
	want := []byte(`{"MaxStreamingBitrate":12000000,"SupportsDirectPlay":true}`)
	wantProfile := string(want)
	capabilities := session.Capabilities{DeviceProfile: want}
	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{ID: &id, CurrentID: id, Capabilities: capabilities}); err != nil {
		t.Fatalf("report device profile: %v", err)
	}

	capabilities.DeviceProfile[2] = 'x'
	profile, found := manager.DeviceProfile("jellyfin web", "profile-device")
	if !found {
		t.Fatal("DeviceProfile(case-insensitive client and active device ID) = missing, want profile")
	}
	if got := string(profile); got != wantProfile {
		t.Fatalf("stored device profile = %q, want %q", got, wantProfile)
	}

	profile[2] = 'y'
	profileAgain, found := manager.DeviceProfile("JELLYFIN WEB", "PROFILE-DEVICE")
	if !found || string(profileAgain) != wantProfile {
		t.Fatalf("DeviceProfile after mutating returned bytes = %q, found=%t, want immutable %q", profileAgain, found, wantProfile)
	}

	snapshot := sessionByID(t, manager.Sessions(session.Query{}), id)
	if got := string(snapshot.Capabilities.DeviceProfile); got != wantProfile {
		t.Fatalf("snapshot device profile = %q, want %q", got, wantProfile)
	}
	snapshot.Capabilities.DeviceProfile[2] = 'z'
	if profileAfterSnapshot, found := manager.DeviceProfile("Jellyfin Web", "profile-device"); !found || string(profileAfterSnapshot) != wantProfile {
		t.Fatalf("DeviceProfile after mutating snapshot bytes = %q, found=%t, want immutable %q", profileAfterSnapshot, found, wantProfile)
	}
}

// A device can host separate Jellyfin clients. Playback must select the
// stored profile associated with the requesting client/device session, rather
// than returning an arbitrary profile from another client on that device.
func TestManagerSelectsStoredDeviceProfilesByExactClientAndDevice(t *testing.T) {
	manager, _ := newManager(t)
	browser := logActivity(t, manager, "Browser", "shared-device", sessionTestUserA)
	television := logActivity(t, manager, "Television", "shared-device", sessionTestUserA)

	for _, report := range []struct {
		id      string
		profile string
	}{
		{id: browser.ID, profile: `{"Name":"browser-profile","MaxStreamingBitrate":12000000}`},
		{id: television.ID, profile: `{"Name":"television-profile","MaxStreamingBitrate":24000000}`},
	} {
		id := report.id
		if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{ID: &id, CurrentID: id, Capabilities: session.Capabilities{DeviceProfile: []byte(report.profile)}}); err != nil {
			t.Fatalf("store %s profile: %v", report.id, err)
		}
	}

	for _, tc := range []struct {
		name   string
		client string
		want   string
	}{
		{name: "browser", client: "bRoWsEr", want: `{"Name":"browser-profile","MaxStreamingBitrate":12000000}`},
		{name: "television", client: "TELEVISION", want: `{"Name":"television-profile","MaxStreamingBitrate":24000000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, found := manager.DeviceProfile(tc.client, "SHARED-DEVICE")
			if !found || string(profile) != tc.want {
				t.Fatalf("DeviceProfile(%q, shared-device) = %q, found=%t, want exact client profile %q", tc.client, profile, found, tc.want)
			}
		})
	}
	if profile, found := manager.DeviceProfile("Mobile", "shared-device"); found || profile != nil {
		t.Fatalf("DeviceProfile(missing client, shared-device) = %q, found=%t, want no device-only fallback", profile, found)
	}
}

func TestManagerDeviceProfileIsMissingForUnknownOrRemovedClientDevicePair(t *testing.T) {
	manager, _ := newManager(t)
	if profile, found := manager.DeviceProfile("Jellyfin Web", "unknown-device"); found || profile != nil {
		t.Fatalf("DeviceProfile(Jellyfin Web, unknown device) = %q, found=%t, want nil and false", profile, found)
	}

	created := logActivity(t, manager, "Jellyfin Web", "removed-profile-device", sessionTestUserA)
	id := created.ID
	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{ID: &id, CurrentID: id, Capabilities: session.Capabilities{DeviceProfile: []byte(`{"Name":"removed"}`)}}); err != nil {
		t.Fatalf("report device profile: %v", err)
	}
	if !manager.Remove(id) {
		t.Fatal("Remove(existing profile session) = false, want true")
	}
	if profile, found := manager.DeviceProfile("Jellyfin Web", "removed-profile-device"); found || profile != nil {
		t.Fatalf("DeviceProfile(removed client/device pair) = %q, found=%t, want nil and false", profile, found)
	}
}

func TestManagerFiltersSessionsByDeviceAndActivityWindow(t *testing.T) {
	manager, fake := newManager(t)
	old := logActivity(t, manager, "Jellyfin Web", "old-device", sessionTestUserA)
	fake.Advance(time.Nanosecond)
	exactCutoff := logActivity(t, manager, "Jellyfin Android", "fresh-device", sessionTestUserA)
	fake.Advance(10 * time.Second)

	if got := manager.Sessions(session.Query{DeviceID: "FRESH-DEVICE"}); len(got) != 1 || got[0].ID != exactCutoff.ID {
		t.Fatalf("case-insensitive device filter = %#v, want only fresh device %q", got, exactCutoff.ID)
	}
	zero, negative, positive := 0, -1, 10
	for _, tc := range []struct {
		name   string
		window *int
		want   int
	}{
		{"nil window", nil, 2},
		{"zero window", &zero, 2},
		{"negative window", &negative, 2},
		{"positive inclusive cutoff", &positive, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := manager.Sessions(session.Query{ActiveWithinSeconds: tc.window})
			if len(got) != tc.want {
				t.Fatalf("activeWithinSeconds=%v returned %#v, want %d sessions", tc.window, got, tc.want)
			}
			if tc.window != nil && *tc.window > 0 && (len(got) != 1 || got[0].ID != exactCutoff.ID || containsSession(got, old.ID)) {
				t.Fatalf("positive inclusive activity filter = %#v, want cutoff session %q only", got, exactCutoff.ID)
			}
		})
	}
}

func TestManagerControllableQueriesRequireInjectedSourcePolicy(t *testing.T) {
	manager, _ := newManager(t)
	primary := logActivity(t, manager, "Jellyfin Web", "primary-device", sessionTestUserA)
	additional, err := manager.LogActivity(t.Context(), session.Activity{Client: "Jellyfin Android", Version: "10.10.10", DeviceID: "additional-device", DeviceName: "additional-device", UserID: sessionTestUserB, AdditionalUserIDs: []dto.UUID{sessionTestUserA}})
	if err != nil {
		t.Fatalf("create additional-user session: %v", err)
	}
	foreign := logActivity(t, manager, "Jellyfin TV", "foreign-device", sessionTestUserB)
	anonymous := logActivity(t, manager, "Jellyfin Roku", "anonymous-device", dto.UUID{})

	if got := manager.Sessions(session.Query{ControllableUserID: &sessionTestUserB}); len(got) != 0 {
		t.Fatalf("controllable query without injected policy = %#v, want no matches", got)
	}
	for _, tc := range []struct {
		name  string
		facts controlFacts
		want  []string
	}{
		{"ordinary requester includes primary additional and anonymous", controlFacts{requester: sessionTestUserA, targetSharesDevice: true, deviceAccess: true}, []string{primary.ID, additional.ID, anonymous.ID}},
		{"target with sharing disabled excludes anonymous", controlFacts{requester: sessionTestUserA, targetSharesDevice: false, deviceAccess: true}, []string{primary.ID, additional.ID}},
		{"remote-control-other permission includes foreign", controlFacts{requester: sessionTestUserA, requesterCanControlOthers: true, targetSharesDevice: true, deviceAccess: true}, []string{primary.ID, additional.ID, foreign.ID, anonymous.ID}},
		{"administrator without external device access sees none", controlFacts{requester: sessionTestUserA, requesterAdministrator: true, targetSharesDevice: true, deviceAccess: false}, nil},
		{"API key bypasses requester and device checks", controlFacts{apiKey: true}, []string{primary.ID, additional.ID, foreign.ID, anonymous.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := manager.Sessions(session.Query{ControllableUserID: &sessionTestUserB, CanControl: session.ControlEvaluator(tc.facts.evaluate)})
			if !sameSessionIDs(got, tc.want) {
				t.Fatalf("controllable query = %#v, want session IDs %#v", got, tc.want)
			}
		})
	}
}

func TestManagerRunsControlEvaluatorAfterReleasingItsRegistryLock(t *testing.T) {
	manager, _ := newManager(t)
	created := logActivity(t, manager, "Jellyfin Web", "callback-device", sessionTestUserA)
	if got := manager.Sessions(session.Query{
		ControllableUserID: &sessionTestUserA,
		CanControl: session.ControlEvaluator(func(snapshot session.Snapshot, _ dto.UUID) bool {
			snapshot.DeviceName = "mutated callback snapshot"
			id := snapshot.ID
			if err := manager.ReportCapabilities(context.Background(), session.CapabilityReport{ID: &id, CurrentID: id, Capabilities: session.Capabilities{PlayableMediaTypes: []string{"Video"}, SupportsMediaControl: true}}); err != nil {
				t.Fatalf("ReportCapabilities inside control evaluator: %v", err)
			}
			return true
		}),
	}); len(got) != 1 || got[0].ID != created.ID {
		t.Fatalf("control-evaluated snapshot = %#v, want session %q", got, created.ID)
	}
	after := sessionByID(t, manager.Sessions(session.Query{}), created.ID)
	if after.DeviceName == "mutated callback snapshot" || !sameStrings(after.Capabilities.PlayableMediaTypes, []string{"Video"}) {
		t.Fatalf("control evaluator escaped snapshot or could not update registry: %#v", after)
	}
}

func TestManagerCapabilityReportsUseOptionalIDOrCurrentSessionAndRejectMissingSession(t *testing.T) {
	manager, _ := newManager(t)
	current := logActivity(t, manager, "Jellyfin Web", "current-device", sessionTestUserA)
	other := logActivity(t, manager, "Jellyfin Android", "other-device", sessionTestUserA)

	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{
		CurrentID:    current.ID,
		Capabilities: session.Capabilities{PlayableMediaTypes: []string{"Audio"}},
	}); err != nil {
		t.Fatalf("report capabilities using current session: %v", err)
	}
	byID := other.ID
	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{
		ID:           &byID,
		CurrentID:    current.ID,
		Capabilities: session.Capabilities{PlayableMediaTypes: []string{"Video"}},
	}); err != nil {
		t.Fatalf("report capabilities using explicit ID: %v", err)
	}
	listed := manager.Sessions(session.Query{})
	if !sameStrings(sessionByID(t, listed, current.ID).Capabilities.PlayableMediaTypes, []string{"Audio"}) || !sameStrings(sessionByID(t, listed, other.ID).Capabilities.PlayableMediaTypes, []string{"Video"}) {
		t.Fatalf("optional-ID capability updates landed on wrong snapshots: %#v", listed)
	}

	missing := "missing-session"
	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{ID: &missing, CurrentID: current.ID}); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("report capabilities for missing session error = %v, want ErrNotFound", err)
	}
	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{}); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("report capabilities with no target error = %v, want ErrNotFound", err)
	}
}

func TestManagerRemovalDropsSessionAndReplacementStartsClean(t *testing.T) {
	manager, _ := newManager(t)
	created := logActivity(t, manager, "Jellyfin Web", "replace-device", sessionTestUserA)
	id := created.ID
	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{ID: &id, CurrentID: id, Capabilities: session.Capabilities{PlayableMediaTypes: []string{"Video"}}}); err != nil {
		t.Fatalf("seed capabilities: %v", err)
	}
	if !manager.Remove(id) {
		t.Fatal("Remove(existing session) = false, want removal")
	}
	if got := manager.Sessions(session.Query{}); len(got) != 0 {
		t.Fatalf("sessions after removal = %#v, want none", got)
	}
	if manager.Remove(id) {
		t.Fatal("Remove(missing session) = true, want false")
	}

	replacement := logActivity(t, manager, "Jellyfin Web", "replace-device", sessionTestUserA)
	if replacement.ID != id {
		t.Fatalf("replacement session ID = %q, want deterministic key ID %q", replacement.ID, id)
	}
	if len(replacement.Capabilities.PlayableMediaTypes) != 0 || len(replacement.Capabilities.SupportedCommands) != 0 || replacement.Capabilities.SupportsMediaControl {
		t.Fatalf("replacement retained removed or non-default capabilities: %#v", replacement.Capabilities)
	}
}

func TestManagerConcurrentActivityAndCapabilityReportsPreserveOneValidSession(t *testing.T) {
	manager, _ := newManager(t)
	created := logActivity(t, manager, "Jellyfin Web", "concurrent-device", sessionTestUserA)
	id := created.ID
	const workers = 24
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(workers * 2)
	for range workers {
		go func() {
			defer wait.Done()
			<-start
			if _, err := manager.LogActivity(context.Background(), session.Activity{Client: "Jellyfin Web", Version: "10.11.0", DeviceID: "concurrent-device", DeviceName: "Concurrent", UserID: sessionTestUserA}); err != nil {
				t.Errorf("concurrent LogActivity: %v", err)
			}
		}()
		go func() {
			defer wait.Done()
			<-start
			if err := manager.ReportCapabilities(context.Background(), session.CapabilityReport{ID: &id, CurrentID: id, Capabilities: session.Capabilities{PlayableMediaTypes: []string{"Video"}, SupportedCommands: []string{"Play"}}}); err != nil {
				t.Errorf("concurrent ReportCapabilities: %v", err)
			}
		}()
	}
	close(start)
	wait.Wait()

	got := manager.Sessions(session.Query{})
	if len(got) != 1 || got[0].ID != id || !sameStrings(got[0].Capabilities.PlayableMediaTypes, []string{"Video"}) || !sameStrings(got[0].Capabilities.SupportedCommands, []string{"Play"}) {
		t.Fatalf("concurrent final snapshot = %#v, want one complete session %q", got, id)
	}
}

func TestManagerConcurrentDeviceProfileReadsOwnReturnedBytes(t *testing.T) {
	manager, _ := newManager(t)
	created := logActivity(t, manager, "Jellyfin Web", "concurrent-profile-device", sessionTestUserA)
	id := created.ID
	want := `{"Name":"concurrent","MaxStreamingBitrate":12000000}`
	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{ID: &id, CurrentID: id, Capabilities: session.Capabilities{DeviceProfile: []byte(want)}}); err != nil {
		t.Fatalf("seed device profile: %v", err)
	}

	const workers = 24
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(workers * 2)
	for range workers {
		go func() {
			defer wait.Done()
			<-start
			if err := manager.ReportCapabilities(context.Background(), session.CapabilityReport{ID: &id, CurrentID: id, Capabilities: session.Capabilities{DeviceProfile: []byte(want)}}); err != nil {
				t.Errorf("concurrent ReportCapabilities: %v", err)
			}
		}()
		go func() {
			defer wait.Done()
			<-start
			profile, found := manager.DeviceProfile("JELLYFIN WEB", "CONCURRENT-PROFILE-DEVICE")
			if !found || string(profile) != want {
				t.Errorf("concurrent DeviceProfile = %q, found=%t, want %q", profile, found, want)
				return
			}
			profile[0] = 'x'
		}()
	}
	close(start)
	wait.Wait()

	profile, found := manager.DeviceProfile("Jellyfin Web", "concurrent-profile-device")
	if !found || string(profile) != want {
		t.Fatalf("DeviceProfile after concurrent readers mutated returned bytes = %q, found=%t, want %q", profile, found, want)
	}
}

func TestManagerCloseStopsWorkersAndRejectsFurtherActivity(t *testing.T) {
	defer goleak.VerifyNone(t)

	manager, fake := newManager(t)
	logActivity(t, manager, "Jellyfin Web", "close-device", sessionTestUserA)
	fake.Advance(time.Hour)
	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := manager.LogActivity(t.Context(), session.Activity{Client: "Jellyfin Web", Version: "10.11.0", DeviceID: "close-device", DeviceName: "Closed", UserID: sessionTestUserA}); !errors.Is(err, session.ErrClosed) {
		t.Fatalf("LogActivity after Close error = %v, want ErrClosed", err)
	}
}

func newManager(t *testing.T) (*session.Manager, *clock.Fake) {
	t.Helper()
	fake := clock.NewFake(time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC))
	manager := session.New(fake)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil && !errors.Is(err, session.ErrClosed) {
			t.Errorf("Close test manager: %v", err)
		}
	})
	return manager, fake
}

func logActivity(t *testing.T, manager *session.Manager, client, deviceID string, userID dto.UUID) session.Snapshot {
	t.Helper()
	got, err := manager.LogActivity(t.Context(), session.Activity{Client: client, Version: "10.10.10", DeviceID: deviceID, DeviceName: deviceID, UserID: userID})
	if err != nil {
		t.Fatalf("LogActivity(%q, %q): %v", client, deviceID, err)
	}
	return got
}

func containsSession(sessions []session.Snapshot, id string) bool {
	for _, snapshot := range sessions {
		if snapshot.ID == id {
			return true
		}
	}
	return false
}

func sessionByID(t *testing.T, sessions []session.Snapshot, id string) session.Snapshot {
	t.Helper()
	for _, snapshot := range sessions {
		if snapshot.ID == id {
			return snapshot
		}
	}
	t.Fatalf("session %q missing from %#v", id, sessions)
	return session.Snapshot{}
}

type controlFacts struct {
	apiKey                    bool
	requester                 dto.UUID
	requesterAdministrator    bool
	requesterCanControlOthers bool
	targetSharesDevice        bool
	deviceAccess              bool
}

func (facts controlFacts) evaluate(snapshot session.Snapshot, _ dto.UUID) bool {
	if facts.apiKey {
		return true
	}
	if facts.requester == (dto.UUID{}) || !facts.deviceAccess {
		return false
	}
	if !facts.targetSharesDevice && snapshot.UserID == (dto.UUID{}) {
		return false
	}
	if facts.requesterAdministrator || facts.requesterCanControlOthers {
		return true
	}
	if snapshot.UserID == facts.requester {
		return true
	}
	for _, userID := range snapshot.AdditionalUserIDs {
		if userID == facts.requester {
			return true
		}
	}
	return snapshot.UserID == (dto.UUID{})
}

func sameSessionIDs(snapshots []session.Snapshot, want []string) bool {
	if len(snapshots) != len(want) {
		return false
	}
	seen := make(map[string]struct{}, len(snapshots))
	for _, snapshot := range snapshots {
		seen[snapshot.ID] = struct{}{}
	}
	for _, id := range want {
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func TestManagerCapabilityReportOwnerRejectsAnotherUsersSession(t *testing.T) {
	manager, _ := newManager(t)
	own := logActivity(t, manager, "Jellyfin Web", "own-device", sessionTestUserA)
	other := logActivity(t, manager, "Jellyfin Web", "other-device", sessionTestUserB)
	owner := own.UserID

	otherID := other.ID
	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{
		ID: &otherID, CurrentID: own.ID, Owner: &owner,
		Capabilities: session.Capabilities{PlayableMediaTypes: []string{"Video"}},
	}); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("owner-restricted report for another user's session error = %v, want ErrNotFound", err)
	}
	if got := sessionByID(t, manager.Sessions(session.Query{}), other.ID).Capabilities.PlayableMediaTypes; len(got) != 0 {
		t.Fatalf("another user's session capabilities = %#v, want unchanged", got)
	}
	ownID := own.ID
	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{
		ID: &ownID, Owner: &owner,
		Capabilities: session.Capabilities{PlayableMediaTypes: []string{"Audio"}},
	}); err != nil {
		t.Fatalf("owner-restricted report for own session: %v", err)
	}
	if got := sessionByID(t, manager.Sessions(session.Query{}), own.ID).Capabilities.PlayableMediaTypes; !sameStrings(got, []string{"Audio"}) {
		t.Fatalf("own session capabilities = %#v, want Audio", got)
	}
}
