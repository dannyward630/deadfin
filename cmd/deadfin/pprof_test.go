// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStartProfilingOffByDefault(t *testing.T) {
	stop, err := startProfiling(t.Context(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || stop(t.Context()) != nil {
		t.Fatalf("disabled profiling: err=%v", err)
	}
}

func TestStartProfilingServesIndexAndStops(t *testing.T) {
	var logs strings.Builder
	stop, err := startProfiling(t.Context(), "127.0.0.1:0", slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	addr := logs.String()[strings.Index(logs.String(), "addr=")+5:]
	addr = strings.TrimSpace(addr)
	resp, err := http.Get("http://" + addr + "/debug/pprof/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index status = %d", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := http.Get("http://" + addr + "/debug/pprof/"); err == nil {
		t.Fatal("profiling endpoint still serving after stop")
	}
}

func TestStartProfilingRefusesNonLoopbackBinds(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, addr := range []string{":6060", "0.0.0.0:6060", "[::]:6060", "10.0.0.5:6060", "deadfin.example:6060", "6060"} {
		if stop, err := startProfiling(t.Context(), addr, logger); err == nil {
			_ = stop(t.Context())
			t.Fatalf("startProfiling(%q) succeeded, want refusal", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0", "localhost:0"} {
		if err := requireLoopbackBind(addr); err != nil {
			t.Fatalf("requireLoopbackBind(%q) = %v, want accepted", addr, err)
		}
	}
}
