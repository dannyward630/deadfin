// SPDX-License-Identifier: GPL-2.0-only

package session_test

import (
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/session"
)

// Source: upstream/jellyfin/Emby.Server.Implementations/Session/SessionManager.cs:1759-1782.
// Logout(Device) ends every session whose DeviceId matches the logged-out
// device without regard to case, whatever its client or user.
func TestManagerEndDeviceSessionsRemovesEveryClientOfTheDevice(t *testing.T) {
	manager, _ := newManager(t)
	web := logActivity(t, manager, "Jellyfin Web", "shared-device", sessionTestUserA)
	other := logActivity(t, manager, "Swiftfin", "SHARED-DEVICE", sessionTestUserB)
	kept := logActivity(t, manager, "Jellyfin Web", "other-device", sessionTestUserA)
	changes := manager.Subscribe(events.SubscriptionOptions{Buffer: 4})
	defer func() { _ = changes.Close(t.Context()) }()

	ended := manager.EndDeviceSessions("Shared-Device")
	want := []string{web.ID, other.ID}
	sort.Strings(want)
	if !reflect.DeepEqual(ended, want) {
		t.Fatalf("EndDeviceSessions = %v, want %v", ended, want)
	}
	remaining := manager.Sessions(session.Query{})
	if len(remaining) != 1 || remaining[0].ID != kept.ID {
		t.Fatalf("remaining sessions = %#v, want only %q", remaining, kept.ID)
	}
	removed := map[string]bool{}
	for range want {
		change := <-changes.Events()
		if change.Kind != session.ChangeRemoved {
			t.Fatalf("change = %#v, want Removed", change)
		}
		removed[change.SessionID] = true
	}
	if !removed[web.ID] || !removed[other.ID] {
		t.Fatalf("Removed changes = %v, want %v", removed, want)
	}
	if again := manager.EndDeviceSessions("shared-device"); len(again) != 0 {
		t.Fatalf("second EndDeviceSessions = %v, want none", again)
	}
}

func TestManagerEndDeviceSessionsIsRaceFreeWithActivity(t *testing.T) {
	manager, _ := newManager(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 50 {
				_, _ = manager.LogActivity(t.Context(), session.Activity{Client: "Client", Version: "1.0", DeviceID: "device", UserID: sessionTestUserA})
				_ = manager.Sessions(session.Query{DeviceID: "device"})
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				_ = manager.EndDeviceSessions("DEVICE")
			}
		}()
	}
	wg.Wait()
	manager.EndDeviceSessions("device")
	if remaining := manager.Sessions(session.Query{}); len(remaining) != 0 {
		t.Fatalf("remaining sessions = %#v, want none", remaining)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ended := manager.EndDeviceSessions("device"); ended != nil {
		t.Fatalf("EndDeviceSessions after Close = %v, want nil", ended)
	}
}
