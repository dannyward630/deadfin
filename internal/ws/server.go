// SPDX-License-Identifier: GPL-2.0-only

// Package ws owns Jellyfin WebSocket upgrades, envelopes, keepalive, and feeds.
package ws

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/activitylog"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/playstate"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/sessiondto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/tasks"
	"github.com/deadfin-org/deadfin/internal/user"
	"golang.org/x/net/websocket"
)

const (
	keepAliveTimeout = 60 * time.Second
	keepAliveSweep   = 12 * time.Second
	keepAliveForce   = 45 * time.Second
	connectionBuffer = 32
)

// Config contains process-owned WebSocket collaborators.
type Config struct {
	Clock            clock.Clock
	Random           io.Reader
	Sessions         *session.Manager
	Users            *user.Service
	SessionProjector *sessiondto.Projector
	Items            *store.ItemStore
	ItemBuilder      *dtobuild.Builder
	LibraryChanges   *library.ChangeBus
	RefreshProgress  *library.RefreshProgressBus
	UserDataChanges  *events.Bus[playstate.UserDataChanged]
	Activity         *activitylog.Log
	Tasks            *tasks.Catalog
}

// Server owns all live WebSocket connections and their supervised workers.
type Server struct {
	config Config

	mu          sync.Mutex
	closed      bool
	connections map[*connection]struct{}
	bySession   map[string]map[*connection]struct{}
}

// New constructs a WebSocket server.
func New(config Config) *Server {
	if config.Clock == nil {
		panic("ws: clock is required")
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	return &Server{
		config: config, connections: make(map[*connection]struct{}),
		bySession: make(map[string]map[*connection]struct{}),
	}
}

// Register adds Jellyfin's authenticated socket endpoint to the route ledger.
func Register(rt *router.Router, server *Server) {
	rt.Handle(http.MethodGet, "/socket", router.Route{
		OperationID: "OpenWebSocket",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}, func(w http.ResponseWriter, request *http.Request) {
		if server == nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		server.ServeHTTP(w, request)
	})
}

// ServeHTTP authenticates the upgraded connection against the caller's exact
// current Client+DeviceId session.
func (server *Server) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	identity, found := middleware.IdentityFromContext(request.Context())
	if !found || !identity.Authenticated {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	server.serveAuthenticated(w, request, identity)
}

func (server *Server) serveAuthenticated(w http.ResponseWriter, request *http.Request, identity auth.Identity) {
	if server.config.Sessions == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	snapshot, found := server.config.Sessions.Session(identity.Client, identity.DeviceID)
	if !found || (!identity.APIKey && snapshot.UserID != identity.UserID) {
		http.Error(w, http.StatusText(http.StatusConflict), http.StatusConflict)
		return
	}

	handler := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(socket *websocket.Conn) {
			connection := newConnection(server, socket, identity, snapshot.ID)
			if !server.add(connection) {
				_ = socket.Close()
				return
			}
			defer server.remove(connection)
			connection.run()
		},
	}
	handler.ServeHTTP(w, request)
}

// Close cancels every connection and waits for its writer and feed workers.
func (server *Server) Close(ctx context.Context) error {
	server.mu.Lock()
	if !server.closed {
		server.closed = true
	}
	connections := make([]*connection, 0, len(server.connections))
	for connection := range server.connections {
		connections = append(connections, connection)
	}
	server.mu.Unlock()

	for _, connection := range connections {
		connection.cancel()
		_ = connection.socket.Close()
	}
	for _, connection := range connections {
		select {
		case <-connection.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// CloseSession closes every open WebSocket controller of sessionID and waits
// until each connection stops. It ports the controller disposal of
// SessionInfo.DisposeAsync for an ended session.
func (server *Server) CloseSession(ctx context.Context, sessionID string) error {
	if server == nil {
		return nil
	}
	server.mu.Lock()
	connections := make([]*connection, 0, len(server.bySession[sessionID]))
	for connection := range server.bySession[sessionID] {
		connections = append(connections, connection)
	}
	server.mu.Unlock()

	for _, connection := range connections {
		connection.cancel()
		_ = connection.socket.Close()
	}
	for _, connection := range connections {
		select {
		case <-connection.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (server *Server) add(candidate *connection) bool {
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		return false
	}
	server.connections[candidate] = struct{}{}
	connections := server.bySession[candidate.sessionID]
	if connections == nil {
		connections = make(map[*connection]struct{})
		server.bySession[candidate.sessionID] = connections
	}
	connections[candidate] = struct{}{}
	server.mu.Unlock()
	if err := server.config.Sessions.ControllerConnected(candidate.sessionID); err != nil {
		server.remove(candidate)
		return false
	}
	return true
}

func (server *Server) remove(candidate *connection) {
	server.mu.Lock()
	_, found := server.connections[candidate]
	if found {
		delete(server.connections, candidate)
		connections := server.bySession[candidate.sessionID]
		delete(connections, candidate)
		if len(connections) == 0 {
			delete(server.bySession, candidate.sessionID)
		}
	}
	server.mu.Unlock()
	if found {
		if server.config.Sessions.ControllerDisconnected(candidate.sessionID) {
			server.config.Sessions.ReportSessionEnded(candidate.sessionID)
		}
	}
}

// Send routes one command to the target session's most recently active open
// WebSocket controller. A session without an open controller is a successful
// no-op, matching SessionManager.SendMessageToSession.
func (server *Server) Send(ctx context.Context, sessionID string, messageType MessageType, data any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		return session.ErrClosed
	}
	var (
		selected *connection
		latest   time.Time
	)
	for candidate := range server.bySession[sessionID] {
		candidate.lastMu.Lock()
		activity := candidate.lastKeepAlive
		candidate.lastMu.Unlock()
		if candidate.ctx.Err() == nil && (selected == nil || activity.After(latest)) {
			selected = candidate
			latest = activity
		}
	}
	server.mu.Unlock()
	if selected == nil {
		return nil
	}
	if !selected.send(messageType, data) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return session.ErrClosed
	}
	return nil
}

type connection struct {
	server    *Server
	socket    *websocket.Conn
	identity  auth.Identity
	sessionID string
	factory   envelopeFactory

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	out    chan Envelope

	lastMu        sync.Mutex
	lastKeepAlive time.Time
	feeds         map[MessageType]*periodicFeed
	wg            sync.WaitGroup
}

func newConnection(server *Server, socket *websocket.Conn, identity auth.Identity, sessionID string) *connection {
	ctx, cancel := context.WithCancel(socket.Request().Context())
	return &connection{
		server: server, socket: socket, identity: identity, sessionID: sessionID,
		factory: envelopeFactory{random: server.config.Random},
		ctx:     ctx, cancel: cancel, done: make(chan struct{}), out: make(chan Envelope, connectionBuffer),
		lastKeepAlive: server.config.Clock.Now(),
		feeds:         make(map[MessageType]*periodicFeed),
	}
}

func (connection *connection) run() {
	defer close(connection.done)
	connection.wg.Add(2)
	go connection.writeLoop()
	go connection.closeOnCancel()
	connection.startFeeds()
	connection.startKeepAlive()

	if !connection.send(MessageForceKeepAlive, int(keepAliveTimeout/time.Second)) {
		connection.cancel()
	}
	for connection.ctx.Err() == nil {
		var message InboundEnvelope
		if err := websocket.JSON.Receive(connection.socket, &message); err != nil {
			break
		}
		connection.process(message)
	}
	connection.cancel()
	_ = connection.socket.Close()
	for _, feed := range connection.feeds {
		feed.wait()
	}
	connection.wg.Wait()
}

func (connection *connection) closeOnCancel() {
	defer connection.wg.Done()
	<-connection.ctx.Done()
	_ = connection.socket.Close()
}

func (connection *connection) writeLoop() {
	defer connection.wg.Done()
	for {
		select {
		case <-connection.ctx.Done():
			return
		case message := <-connection.out:
			if err := websocket.JSON.Send(connection.socket, message); err != nil {
				connection.cancel()
				return
			}
		}
	}
}

func (connection *connection) send(messageType MessageType, data any) bool {
	message, err := connection.factory.new(messageType, data)
	if err != nil {
		connection.cancel()
		return false
	}
	select {
	case connection.out <- message:
		return true
	case <-connection.ctx.Done():
		return false
	default:
		connection.cancel()
		_ = connection.socket.Close()
		return false
	}
}

func (connection *connection) process(message InboundEnvelope) {
	if message.MessageType == MessageKeepAlive {
		connection.lastMu.Lock()
		connection.lastKeepAlive = connection.server.config.Clock.Now()
		connection.lastMu.Unlock()
		connection.send(MessageKeepAlive, nil)
		return
	}
	feedType, start, found := periodicMessage(message.MessageType)
	if !found {
		return
	}
	feed := connection.feeds[feedType]
	if feed == nil {
		return
	}
	if !start {
		feed.stop()
		return
	}
	if feedType == MessageActivityLogEntry && !connection.identity.APIKey && !connection.identity.Administrator {
		return
	}
	var raw string
	if err := json.Unmarshal(message.Data, &raw); err != nil {
		return
	}
	interval, err := parsePeriodicInterval(raw)
	if err != nil {
		return
	}
	feed.start(interval)
}

func periodicMessage(messageType MessageType) (MessageType, bool, bool) {
	switch messageType {
	case MessageSessionsStart:
		return MessageSessions, true, true
	case MessageSessionsStop:
		return MessageSessions, false, true
	case MessageActivityLogEntryStart:
		return MessageActivityLogEntry, true, true
	case MessageActivityLogEntryStop:
		return MessageActivityLogEntry, false, true
	case MessageScheduledTasksInfoStart:
		return MessageScheduledTasksInfo, true, true
	case MessageScheduledTasksInfoStop:
		return MessageScheduledTasksInfo, false, true
	default:
		return "", false, false
	}
}

func (connection *connection) startFeeds() {
	config := connection.server.config
	if config.Sessions != nil {
		connection.feeds[MessageSessions] = newPeriodicFeed(connection.ctx, config.Clock, MessageSessions, connection.loadSessions, connection.send, connection.cancel)
		connection.forwardSessionChanges()
	}
	if config.Activity != nil {
		connection.feeds[MessageActivityLogEntry] = newPeriodicFeed(connection.ctx, config.Clock, MessageActivityLogEntry, func(context.Context) (any, error) {
			return []activitylog.Entry{}, nil
		}, connection.send, connection.cancel)
		connection.forwardActivityChanges()
	}
	if config.Tasks != nil {
		connection.feeds[MessageScheduledTasksInfo] = newPeriodicFeed(connection.ctx, config.Clock, MessageScheduledTasksInfo, func(context.Context) (any, error) {
			return config.Tasks.Snapshot(), nil
		}, connection.send, connection.cancel)
		connection.forwardTaskChanges()
	}
	connection.forwardLibraryChanges()
	connection.forwardUserDataChanges()
	connection.forwardRefreshProgress()
}

func (connection *connection) loadSessions(ctx context.Context) (any, error) {
	config := connection.server.config
	snapshots := config.Sessions.Sessions(session.Query{})
	result := make([]dto.SessionInfoDto, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if !connection.identity.APIKey && !connection.identity.Administrator && !containsUser(snapshot, connection.identity.UserID) {
			continue
		}
		userName := ""
		if config.Users != nil {
			if value, err := config.Users.GetDTO(ctx, snapshot.UserID); err == nil {
				userName = value.Name
			}
		}
		if config.SessionProjector == nil {
			result = append(result, sessiondto.ProjectBase(snapshot, userName, ""))
		} else {
			result = append(result, config.SessionProjector.Project(ctx, snapshot, userName))
		}
	}
	return result, nil
}

func containsUser(snapshot session.Snapshot, userID dto.UUID) bool {
	if snapshot.UserID == userID {
		return true
	}
	for _, additional := range snapshot.AdditionalUserIDs {
		if additional == userID {
			return true
		}
	}
	return false
}

func (connection *connection) forwardSessionChanges() {
	subscription := connection.server.config.Sessions.Subscribe(events.SubscriptionOptions{Buffer: 8})
	forward(connection, subscription.Events(), func(change session.Change) {
		connection.feeds[MessageSessions].changed(change.Force)
	}, func() { _ = subscription.Close(context.Background()) })
}

func (connection *connection) forwardActivityChanges() {
	subscription := connection.server.config.Activity.Subscribe(events.SubscriptionOptions{Buffer: 8})
	forward(connection, subscription.Events(), func(activitylog.Entry) {
		connection.feeds[MessageActivityLogEntry].changed(true)
	}, func() { _ = subscription.Close(context.Background()) })
}

func (connection *connection) forwardTaskChanges() {
	subscription := connection.server.config.Tasks.Subscribe(events.SubscriptionOptions{Buffer: 8})
	forward(connection, subscription.Events(), func([]tasks.Info) {
		connection.feeds[MessageScheduledTasksInfo].changed(true)
	}, func() { _ = subscription.Close(context.Background()) })
}

func (connection *connection) forwardLibraryChanges() {
	if connection.server.config.LibraryChanges == nil {
		return
	}
	subscription := connection.server.config.LibraryChanges.Subscribe(4)
	forward(connection, subscription.Events(), func(info library.UpdateInfo) {
		connection.send(MessageLibraryChanged, info)
	}, func() { _ = subscription.Close(context.Background()) })
}

func (connection *connection) forwardUserDataChanges() {
	if connection.identity.UserID.IsNil() || connection.server.config.UserDataChanges == nil ||
		connection.server.config.Items == nil || connection.server.config.ItemBuilder == nil {
		return
	}
	subscription := playstate.SubscribeUserDataChanges(connection.server.config.UserDataChanges, connection.identity.UserID, 4)
	forward(connection, subscription.Events(), func(change playstate.UserDataChanged) {
		info, err := connection.projectUserData(connection.ctx, change)
		if err != nil {
			connection.cancel()
			return
		}
		connection.send(MessageUserDataChanged, info)
	}, func() { _ = subscription.Close(context.Background()) })
}

func (connection *connection) projectUserData(ctx context.Context, change playstate.UserDataChanged) (UserDataChangeInfo, error) {
	config := connection.server.config
	seen := make(map[dto.UUID]struct{}, len(change.ItemIDs)*2)
	values := make([]item.Item, 0, len(change.ItemIDs)*2)
	ids := append([]dto.UUID(nil), change.ItemIDs...)
	if len(ids) == 0 && !change.ItemID.IsNil() {
		ids = append(ids, change.ItemID)
	}
	for index := 0; index < len(ids); index++ {
		id := ids[index]
		if id.IsNil() {
			continue
		}
		if _, found := seen[id]; found {
			continue
		}
		value, err := config.Items.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return UserDataChangeInfo{}, err
		}
		seen[id] = struct{}{}
		values = append(values, value)
		if value.ParentID != nil {
			ids = append(ids, *value.ParentID)
		}
	}
	projected, err := config.ItemBuilder.Build(ctx, values, store.ItemQueryDTOOptions{EnableUserData: true}, &change.UserID)
	if err != nil {
		return UserDataChangeInfo{}, err
	}
	result := UserDataChangeInfo{UserID: change.UserID, UserDataList: make([]dto.UserItemDataDto, 0, len(projected.Items))}
	for _, value := range projected.Items {
		if value.UserData != nil {
			result.UserDataList = append(result.UserDataList, *value.UserData)
		}
	}
	return result, nil
}

func (connection *connection) forwardRefreshProgress() {
	if (!connection.identity.APIKey && !connection.identity.Administrator) || connection.server.config.RefreshProgress == nil {
		return
	}
	subscription := connection.server.config.RefreshProgress.Subscribe(events.SubscriptionOptions{Buffer: 16})
	forward(connection, subscription.Events(), func(progress library.RefreshProgress) {
		connection.send(MessageRefreshProgress, RefreshProgressInfo{ItemID: progress.ItemID, Progress: progress.Progress})
	}, func() { _ = subscription.Close(context.Background()) })
}

func forward[T any](connection *connection, values <-chan T, receive func(T), close func()) {
	connection.wg.Add(1)
	go func() {
		defer connection.wg.Done()
		defer close()
		for {
			select {
			case <-connection.ctx.Done():
				return
			case value, open := <-values:
				if !open {
					if connection.ctx.Err() == nil {
						connection.cancel()
					}
					return
				}
				receive(value)
			}
		}
	}()
}

func (connection *connection) startKeepAlive() {
	timer := connection.server.config.Clock.NewTimer(keepAliveSweep)
	connection.wg.Add(1)
	go func() {
		defer connection.wg.Done()
		defer timer.Stop()
		for {
			select {
			case <-connection.ctx.Done():
				return
			case <-timer.C():
				connection.lastMu.Lock()
				elapsed := connection.server.config.Clock.Now().Sub(connection.lastKeepAlive)
				connection.lastMu.Unlock()
				switch {
				case elapsed >= keepAliveTimeout:
					connection.cancel()
					return
				case elapsed > keepAliveForce:
					if !connection.send(MessageForceKeepAlive, int(keepAliveTimeout/time.Second)) {
						return
					}
				}
				timer.Reset(keepAliveSweep)
			}
		}
	}()
}
