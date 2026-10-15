// SPDX-License-Identifier: GPL-2.0-only

// Package server owns the process-level HTTP lifecycle.
package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

const shutdownTimeout = 5 * time.Second

// Connection deadlines follow the Kestrel defaults that pinned Jellyfin runs
// with (KestrelServerLimits: RequestHeadersTimeout 30 s, KeepAliveTimeout
// 130 s). They bound a peer that opens connections and never finishes its
// headers, or holds idle keep-alive connections. There is deliberately no
// ReadTimeout or WriteTimeout: a media stream, a long upload, a WebSocket,
// or a CPU profile runs for as long as it needs.
const (
	readHeaderTimeout = 30 * time.Second
	idleTimeout       = 130 * time.Second
)

// Deps contains every dependency used by the process-level server. The command
// constructs it explicitly so services cannot become package-level singletons.
type Deps struct {
	Clock   clock.Clock
	Handler http.Handler
	Addr    string
}

// Server runs the configured HTTP handler and owns its orderly shutdown.
type Server struct {
	clock      clock.Clock
	httpServer *http.Server

	closeMu       sync.Mutex
	closeComplete bool
}

// New validates the composition root and constructs its HTTP server.
func New(deps Deps) (*Server, error) {
	if deps.Clock == nil {
		return nil, errors.New("server: clock is required")
	}
	if deps.Handler == nil {
		return nil, errors.New("server: handler is required")
	}
	if deps.Addr == "" {
		return nil, errors.New("server: address is required")
	}

	return &Server{
		clock: deps.Clock,
		httpServer: &http.Server{
			Addr:              deps.Addr,
			Handler:           deps.Handler,
			ReadHeaderTimeout: readHeaderTimeout,
			IdleTimeout:       idleTimeout,
		},
	}, nil
}

// Run listens on the configured address and serves until ctx is canceled or the
// listener fails.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, listener)
}

// Serve serves listener until ctx is canceled or the HTTP server stops. The
// goroutine it starts is owned by this Server and stopped through Close.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.httpServer.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if closeErr := s.closeBounded(); closeErr != nil {
			return closeErr
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		return s.closeBounded()
	}
}

// Close stops accepting requests and waits for in-flight handlers using ctx.
// A canceled call does not complete shutdown, so a later caller may retry it
// with a context that has enough time for handlers to drain.
func (s *Server) Close(ctx context.Context) error {
	s.closeMu.Lock()
	if s.closeComplete {
		s.closeMu.Unlock()
		return nil
	}
	s.closeMu.Unlock()

	// Avoid entering http.Server.Shutdown after the caller has already
	// abandoned its shutdown attempt, so it cannot contend with another Close.
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	err := s.httpServer.Shutdown(ctx)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.closeMu.Lock()
		completed := s.closeComplete
		s.closeMu.Unlock()
		if completed {
			return nil
		}
		return err
	}

	s.closeMu.Lock()
	s.closeComplete = true
	s.closeMu.Unlock()
	return nil
}

func (s *Server) closeBounded() error {
	shutdown := make(chan error, 1)
	go func() {
		shutdown <- s.Close(context.Background())
	}()

	timer := s.clock.NewTimer(shutdownTimeout)
	select {
	case err := <-shutdown:
		timer.Stop()
		return err
	case <-timer.C():
		closeErr := s.httpServer.Close()
		err := <-shutdown
		if closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			return closeErr
		}
		return err
	}
}
