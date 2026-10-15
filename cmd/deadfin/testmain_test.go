// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"os"
	"testing"

	"github.com/deadfin-org/deadfin/internal/trickplay"
)

// TestMain lets composition tests run the real FFmpegFrameExtractor, which
// re-executes this binary as its frame-sink trampoline. The main() helper
// test dispatches the trampoline itself, so it is left to main().
func TestMain(m *testing.M) {
	if os.Getenv("DEADFIN_TEST_FRAME_SINK_TRAMPOLINE_HELPER") != "1" {
		trickplay.MaybeRunFrameSinkTrampoline()
	}
	os.Exit(m.Run())
}
