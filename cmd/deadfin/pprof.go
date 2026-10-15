// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"strings"
	"time"
)

// startProfiling serves the Go runtime profiles on addr when it is set
// (DEADFIN_PPROF_ADDR, e.g. 127.0.0.1:6060), for diagnosing a live server
// through kubectl port-forward. It is off by default and never shares the
// API listener. The profiles have no authentication, so addr must name a
// loopback host; any other bind is refused. The returned function stops the
// listener; it is a no-op when profiling is off.
func startProfiling(ctx context.Context, addr string, logger *slog.Logger) (func(context.Context) error, error) {
	if addr == "" {
		return func(context.Context) error { return nil }, nil
	}
	if err := requireLoopbackBind(addr); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	// WriteTimeout stays unset: a CPU profile streams for its full duration.
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	logger.Info("profiling endpoint enabled", slog.String("addr", listener.Addr().String()))
	return func(shutdownCtx context.Context) error {
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-done
	}, nil
}

// requireLoopbackBind accepts "localhost" or a loopback IP literal as the host
// of addr. An empty host binds every interface, so it is refused.
func requireLoopbackBind(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("DEADFIN_PPROF_ADDR %q: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Unmap().IsLoopback() {
		return nil
	}
	return fmt.Errorf("DEADFIN_PPROF_ADDR %q: the unauthenticated profiling endpoint must bind a loopback address such as 127.0.0.1:6060", addr)
}
