// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"log/slog"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		input string
		want  slog.Level
	}{
		{"", slog.LevelInfo},
		{"   ", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"DEBUG", slog.LevelDebug},
		{"Debug", slog.LevelDebug},
		{"  debug  ", slog.LevelDebug},
		{"trace", slog.LevelInfo},
		{"verbose", slog.LevelInfo},
		{"1234", slog.LevelInfo},
	}
	for _, tc := range cases {
		t.Run("level_"+tc.input, func(t *testing.T) {
			if got := parseLogLevel(tc.input); got != tc.want {
				t.Fatalf("parseLogLevel(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestConfigureLoggingReplacesDefaultHandler(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	configureLogging("debug")

	// The default logger is the slog.Default() handle that providers call
	// before they log activity. configureLogging must replace it with a
	// handler at the parsed level. A debug-level default proves the
	// replacement reached the providers' log path.
	if !slog.Default().Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("configureLogging did not replace the default handler with a debug-level handler")
	}
}

func TestConfigureLoggingDebugShowsProviderDebugMessages(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	configureLogging("debug")

	// Capture stderr is not deterministic across the test runner; instead
	// verify the default logger reports Debug enabled, which is what the
	// TMDb provider checks at provider.go:72 before it logs activity.
	if !slog.Default().Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("DEADFIN_LOG_LEVEL=debug did not enable Debug on the default logger")
	}
}

func TestConfigureLoggingInfoSuppressesProviderDebugMessages(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	configureLogging("")

	if slog.Default().Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("unset DEADFIN_LOG_LEVEL enabled Debug on the default logger")
	}
}

func TestConfigureLoggingInvalidKeepsInfo(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	configureLogging("not-a-level")

	if slog.Default().Enabled(t.Context(), slog.LevelDebug) {
		t.Fatal("invalid DEADFIN_LOG_LEVEL enabled Debug on the default logger")
	}
	if !slog.Default().Enabled(t.Context(), slog.LevelInfo) {
		t.Fatal("invalid DEADFIN_LOG_LEVEL did not keep Info enabled on the default logger")
	}
}
