// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

// ChangeQueue coalesces external "media changed" reports (Sonarr/Radarr via
// /Library/Media/Updated and friends) into one library scan after a quiet
// period, as upstream LibraryMonitor.ReportFileSystemChanged waits
// LibraryMonitorDelay before it refreshes. Deadfin has no path-scoped refresh,
// so the scan covers every library; unchanged items are skipped by the scan.
type ChangeQueue struct {
	clock   clock.Clock
	delay   func() time.Duration
	trigger func() error
	logger  *slog.Logger

	reports chan struct{}
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

// NewChangeQueue starts the queue's goroutine. delay is read per burst so a
// configuration change applies to the next one. Close stops the goroutine.
func NewChangeQueue(clk clock.Clock, delay func() time.Duration, trigger func() error, logger *slog.Logger) *ChangeQueue {
	if logger == nil {
		logger = slog.Default()
	}
	q := &ChangeQueue{clock: clk, delay: delay, trigger: trigger, logger: logger,
		reports: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	go q.run()
	return q
}

// Report records one change. It never blocks.
func (q *ChangeQueue) Report() {
	select {
	case q.reports <- struct{}{}:
	default:
	}
}

// Close stops the queue without running a pending scan.
func (q *ChangeQueue) Close(ctx context.Context) error {
	q.once.Do(func() { close(q.stop) })
	select {
	case <-q.done:
		return nil
	default:
	}
	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		return errors.Join(errors.New("change queue: close"), ctx.Err())
	}
}

func (q *ChangeQueue) run() {
	defer close(q.done)
	for {
		select {
		case <-q.stop:
			return
		case <-q.reports:
		}
		timer := q.clock.NewTimer(q.delay())
	wait:
		for {
			select {
			case <-q.stop:
				timer.Stop()
				return
			case <-q.reports:
				timer.Reset(q.delay())
			case <-timer.C():
				break wait
			}
		}
		if err := q.trigger(); err != nil {
			q.logger.Warn("library change scan not started", slog.String("error", err.Error()))
		}
	}
}
