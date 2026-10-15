// SPDX-License-Identifier: GPL-2.0-only

package ws

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/session"
	"golang.org/x/net/websocket"
)

func TestWebSocketConformanceKeepAliveAndOwnSessionsOnFakeClock(t *testing.T) {
	fake := clock.NewFake(time.Unix(1_700_000_000, 0))
	sessions := session.New(fake)
	defer func() { _ = sessions.Close() }()
	ownerID := testUUID(t, "11111111111111111111111111111111")
	otherID := testUUID(t, "22222222222222222222222222222222")
	owner, err := sessions.LogActivity(t.Context(), session.Activity{
		Client: "Swiftfin", Version: "1.0", DeviceID: "apple-tv", DeviceName: "Apple TV", UserID: ownerID,
	})
	if err != nil {
		t.Fatalf("create owner session: %v", err)
	}
	if _, err := sessions.LogActivity(t.Context(), session.Activity{
		Client: "Dashboard", Version: "12.0", DeviceID: "browser", DeviceName: "Browser", UserID: otherID,
	}); err != nil {
		t.Fatalf("create other session: %v", err)
	}

	random := bytes.NewReader(bytes.Repeat([]byte{0x55}, 16*32))
	server := New(Config{Clock: fake, Random: random, Sessions: sessions})
	defer func() { _ = server.Close(t.Context()) }()
	identity := auth.Identity{
		Authenticated: true, UserID: ownerID, Client: "Swiftfin", Version: "1.0", DeviceID: "apple-tv",
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		server.serveAuthenticated(w, request, identity)
	}))
	defer httpServer.Close()

	socketURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	socket, err := websocket.Dial(socketURL, "", httpServer.URL)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if err := socket.Close(); err != nil {
			t.Errorf("close websocket: %v", err)
		}
	}()

	force := receiveTestEnvelope(t, socket)
	if force.MessageType != MessageForceKeepAlive || string(force.Data) != "60" || force.MessageID.IsNil() {
		t.Fatalf("initial envelope = %#v, want ForceKeepAlive(60) with MessageId", force)
	}
	ownerSessionID := owner.ID
	if err := sessions.ReportCapabilities(t.Context(), session.CapabilityReport{
		ID: &ownerSessionID, Capabilities: session.Capabilities{SupportsMediaControl: true, SupportedCommands: []string{"Pause"}},
	}); err != nil {
		t.Fatalf("report remote-control capability: %v", err)
	}
	if snapshot, found := sessions.SessionByID(owner.ID); !found || !snapshot.SupportsRemoteControl {
		t.Fatalf("connected controller session = %#v, found=%v; want SupportsRemoteControl", snapshot, found)
	}
	if err := server.Send(t.Context(), owner.ID, MessagePlaystate, dto.PlaystateRequest{Command: "Pause"}); err != nil {
		t.Fatalf("route playstate command: %v", err)
	}
	command := receiveTestEnvelope(t, socket)
	if command.MessageType != MessagePlaystate {
		t.Fatalf("controller envelope = %#v, want Playstate", command)
	}
	var playstate dto.PlaystateRequest
	if err := json.Unmarshal(command.Data, &playstate); err != nil || playstate.Command != "Pause" {
		t.Fatalf("controller data = %s (%#v, %v), want Pause", command.Data, playstate, err)
	}

	sendTestEnvelope(t, socket, InboundEnvelope{MessageType: MessageSessionsStart, Data: json.RawMessage(`"0,1000"`)})
	sessionMessage := receiveTestEnvelope(t, socket)
	if sessionMessage.MessageType != MessageSessions {
		t.Fatalf("periodic envelope type = %q, want Sessions", sessionMessage.MessageType)
	}
	var projected []dto.SessionInfoDto
	if err := json.Unmarshal(sessionMessage.Data, &projected); err != nil {
		t.Fatalf("decode Sessions data: %v", err)
	}
	if len(projected) != 1 || projected[0].ID == nil || *projected[0].ID != owner.ID {
		t.Fatalf("non-admin Sessions data = %#v, want only owner session %q", projected, owner.ID)
	}

	sendTestEnvelope(t, socket, InboundEnvelope{MessageType: MessageSessionsStop})
	sendTestEnvelope(t, socket, InboundEnvelope{MessageType: MessageKeepAlive})
	keepAlive := receiveTestEnvelope(t, socket)
	if keepAlive.MessageType != MessageKeepAlive || len(keepAlive.Data) != 0 {
		t.Fatalf("keepalive response = %#v, want no-data KeepAlive", keepAlive)
	}

	fake.Advance(46 * time.Second)
	secondForce := receiveTestEnvelope(t, socket)
	if secondForce.MessageType != MessageForceKeepAlive || string(secondForce.Data) != "60" {
		t.Fatalf("idle envelope = %#v, want ForceKeepAlive(60)", secondForce)
	}
	sendTestEnvelope(t, socket, InboundEnvelope{MessageType: MessageKeepAlive})
	if response := receiveTestEnvelope(t, socket); response.MessageType != MessageKeepAlive {
		t.Fatalf("post-idle response = %#v, want KeepAlive", response)
	}

	changes := sessions.Subscribe(events.SubscriptionOptions{Buffer: 2})
	defer func() { _ = changes.Close(t.Context()) }()

	fake.Advance(61 * time.Second)
	var closed testEnvelope
	if err := websocket.JSON.Receive(socket, &closed); err == nil {
		t.Fatalf("lost websocket remained open and sent %#v", closed)
	}

	for {
		select {
		case change := <-changes.Events():
			if change.Kind == session.ChangeRemoved && change.SessionID == owner.ID {
				if _, found := sessions.SessionByID(owner.ID); found {
					t.Fatalf("session %q remained after the ChangeRemoved event", owner.ID)
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("session %q was not removed after its final websocket closed", owner.ID)
		}
	}
}

type testEnvelope struct {
	MessageType MessageType     `json:"MessageType"`
	MessageID   dto.UUID        `json:"MessageId"`
	Data        json.RawMessage `json:"Data"`
}

func receiveTestEnvelope(t *testing.T, socket *websocket.Conn) testEnvelope {
	t.Helper()
	var message testEnvelope
	if err := websocket.JSON.Receive(socket, &message); err != nil {
		t.Fatalf("receive websocket envelope: %v", err)
	}
	return message
}

func sendTestEnvelope(t *testing.T, socket *websocket.Conn, message InboundEnvelope) {
	t.Helper()
	if err := websocket.JSON.Send(socket, message); err != nil {
		t.Fatalf("send websocket envelope: %v", err)
	}
}

func testUUID(t *testing.T, value string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return id
}

// Source: upstream/jellyfin/MediaBrowser.Controller/Session/SessionInfo.cs:455-477.
// Logout disposes every controller of the ended session. CloseSession closes
// each socket of that session, waits for its workers, and leaves other
// sessions' sockets open.
func TestCloseSessionClosesEveryControllerOfTheSession(t *testing.T) {
	fake := clock.NewFake(time.Unix(1_700_000_000, 0))
	sessions := session.New(fake)
	defer func() { _ = sessions.Close() }()
	userID := testUUID(t, "11111111111111111111111111111111")
	ended, err := sessions.LogActivity(t.Context(), session.Activity{Client: "Web", Version: "1.0", DeviceID: "logout", UserID: userID})
	if err != nil {
		t.Fatalf("create ended session: %v", err)
	}
	if _, err := sessions.LogActivity(t.Context(), session.Activity{Client: "Web", Version: "1.0", DeviceID: "kept", UserID: userID}); err != nil {
		t.Fatalf("create kept session: %v", err)
	}
	server := New(Config{Clock: fake, Sessions: sessions})
	defer func() { _ = server.Close(t.Context()) }()

	dial := func(deviceID string) *websocket.Conn {
		identity := auth.Identity{Authenticated: true, UserID: userID, Client: "Web", Version: "1.0", DeviceID: deviceID}
		httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			server.serveAuthenticated(w, request, identity)
		}))
		t.Cleanup(httpServer.Close)
		socket, err := websocket.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), "", httpServer.URL)
		if err != nil {
			t.Fatalf("dial %s websocket: %v", deviceID, err)
		}
		t.Cleanup(func() { _ = socket.Close() })
		receiveTestEnvelope(t, socket)
		return socket
	}
	first, second, kept := dial("logout"), dial("logout"), dial("kept")

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sessions.EndDeviceSessions("logout")
			if err := server.CloseSession(t.Context(), ended.ID); err != nil {
				t.Errorf("CloseSession: %v", err)
			}
		}()
	}
	wg.Wait()

	for name, socket := range map[string]*websocket.Conn{"first": first, "second": second} {
		var message testEnvelope
		if err := websocket.JSON.Receive(socket, &message); err == nil {
			t.Fatalf("%s socket stayed open and sent %#v", name, message)
		}
	}
	sendTestEnvelope(t, kept, InboundEnvelope{MessageType: MessageKeepAlive})
	if response := receiveTestEnvelope(t, kept); response.MessageType != MessageKeepAlive {
		t.Fatalf("kept socket response = %#v, want KeepAlive", response)
	}
	if _, found := sessions.SessionByID(ended.ID); found {
		t.Fatalf("session %q remained after logout", ended.ID)
	}
	if err := (*Server)(nil).CloseSession(t.Context(), ended.ID); err != nil {
		t.Fatalf("nil server CloseSession: %v", err)
	}
}
