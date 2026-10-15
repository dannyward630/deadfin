// SPDX-License-Identifier: GPL-2.0-only

package ws

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestWebOutboundMessageCatalogMatchesJellyfinWeb(t *testing.T) {
	want := []MessageType{
		"UserDataChanged", "LibraryChanged", "Sessions", "ScheduledTasksInfo", "RefreshProgress",
		"Play", "Playstate", "GeneralCommand", "SyncPlayCommand", "SyncPlayGroupUpdate",
		"TimerCreated", "SeriesTimerCreated", "TimerCancelled", "SeriesTimerCancelled",
		"PackageInstalling", "PackageInstallationCompleted", "PackageInstallationFailed",
		"PackageInstallationCancelled", "ServerShuttingDown", "ServerRestarting", "RestartRequired",
	}
	got := WebOutboundMessageTypes[:]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("web outbound catalog = %#v, want %#v", got, want)
	}
	seen := make(map[MessageType]struct{}, len(got))
	for _, value := range got {
		if _, found := seen[value]; found {
			t.Fatalf("web outbound catalog contains duplicate %q", value)
		}
		seen[value] = struct{}{}
	}
}

func TestEnvelopeVariantsIncludeMessageIDAndOmitAbsentData(t *testing.T) {
	factory := envelopeFactory{random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 32))}
	keepAlive, err := factory.new(MessageKeepAlive, nil)
	if err != nil {
		t.Fatalf("new keepalive envelope: %v", err)
	}
	encoded, err := json.Marshal(keepAlive)
	if err != nil {
		t.Fatalf("marshal keepalive envelope: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode keepalive fields: %v", err)
	}
	if _, found := fields["Data"]; found {
		t.Fatalf("keepalive envelope unexpectedly contains Data: %s", encoded)
	}
	if string(fields["MessageType"]) != `"KeepAlive"` || len(fields["MessageId"]) == 0 {
		t.Fatalf("keepalive envelope = %s, want MessageType and MessageId", encoded)
	}

	withData, err := factory.new(MessageForceKeepAlive, 60)
	if err != nil {
		t.Fatalf("new data envelope: %v", err)
	}
	encoded, err = json.Marshal(withData)
	if err != nil {
		t.Fatalf("marshal data envelope: %v", err)
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode data fields: %v", err)
	}
	if string(fields["Data"]) != "60" {
		t.Fatalf("force keepalive envelope = %s, want Data=60", encoded)
	}
}

func TestParsePeriodicIntervalUsesClientMilliseconds(t *testing.T) {
	got, err := parsePeriodicInterval("250,1500")
	if err != nil {
		t.Fatalf("parse valid interval: %v", err)
	}
	if got.due.Milliseconds() != 250 || got.period.Milliseconds() != 1500 {
		t.Fatalf("interval = %#v, want 250ms due and 1500ms period", got)
	}
	for _, invalid := range []string{"", "0", "0,0", "-1,1000", "one,1000", "0,-1", "0,1000,2000"} {
		if _, err := parsePeriodicInterval(invalid); err == nil {
			t.Errorf("parsePeriodicInterval(%q) error = nil", invalid)
		}
	}
}
