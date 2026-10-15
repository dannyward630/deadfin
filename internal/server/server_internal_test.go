// SPDX-License-Identifier: GPL-2.0-only

package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

func TestNewSetsConnectionDeadlinesWithoutStreamTimeouts(t *testing.T) {
	s, err := New(Deps{Clock: clock.NewFake(time.Time{}), Handler: http.NotFoundHandler(), Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.httpServer.ReadHeaderTimeout != readHeaderTimeout || s.httpServer.IdleTimeout != idleTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, IdleTimeout = %v; want %v, %v", s.httpServer.ReadHeaderTimeout, s.httpServer.IdleTimeout, readHeaderTimeout, idleTimeout)
	}
	if s.httpServer.ReadTimeout != 0 || s.httpServer.WriteTimeout != 0 {
		t.Fatalf("ReadTimeout = %v, WriteTimeout = %v; want none, so media streams and WebSockets are not cut", s.httpServer.ReadTimeout, s.httpServer.WriteTimeout)
	}
}
