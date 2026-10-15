// SPDX-License-Identifier: GPL-2.0-only

package session_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/session"
)

func TestManagerPublishesForcedCapabilityChanges(t *testing.T) {
	manager := session.New(clock.NewFake(time.Unix(1_700_000_000, 0)))
	defer func() { _ = manager.Close() }()
	changes := manager.Subscribe(events.SubscriptionOptions{Buffer: 4})
	defer func() { _ = changes.Close(t.Context()) }()
	userID, err := dto.ParseUUID("11111111111111111111111111111111")
	if err != nil {
		t.Fatalf("parse user id: %v", err)
	}
	snapshot, err := manager.LogActivity(t.Context(), session.Activity{Client: "Client", Version: "1", DeviceID: "device", UserID: userID})
	if err != nil {
		t.Fatalf("log activity: %v", err)
	}
	if change := <-changes.Events(); change.Kind != session.ChangeCreated || !change.Force {
		t.Fatalf("created change = %#v, want forced", change)
	}
	if err := manager.ReportCapabilities(t.Context(), session.CapabilityReport{
		ID: &snapshot.ID, Capabilities: session.Capabilities{SupportedCommands: []string{"Pause"}},
	}); err != nil {
		t.Fatalf("report capabilities: %v", err)
	}
	if change := <-changes.Events(); change.Kind != session.ChangeCapabilities || !change.Force || change.SessionID != snapshot.ID {
		t.Fatalf("capability change = %#v, want forced for %q", change, snapshot.ID)
	}
}
