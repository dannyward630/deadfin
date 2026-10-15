// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestFFmpegErrorExitIsLoggedWithJobAndLogPath(t *testing.T) {
	fixture := newProgressiveFixture(t, "early-error")
	var records lockedBuffer
	manager, err := NewTranscodeManager(fixture.ffmpeg, fixture.dir, TranscodeManagerOptions{
		Clock:                  progressiveTestClock(clock.NewReal()),
		OutputPollInterval:     100 * time.Millisecond,
		ReaderRetryInterval:    50 * time.Millisecond,
		ReaderStallTimeout:     30 * time.Second,
		ProgressiveIdleTimeout: 10 * time.Second,
		KillGracePeriod:        5 * time.Second,
		Logger:                 slog.New(slog.NewTextHandler(&records, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveManager(t, manager)
	request := fixture.request("broken.ts")
	if _, err := manager.OpenProgressive(context.Background(), request); err == nil {
		t.Fatal("OpenProgressive succeeded despite early ffmpeg failure")
	}
	got := records.String()
	for _, want := range []string{"level=ERROR", "ffmpeg exited with an error", "/broken.ts error=", "/broken.ts.stderr.log", "exit status 23"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log records = %q, want %q", got, want)
		}
	}
}
