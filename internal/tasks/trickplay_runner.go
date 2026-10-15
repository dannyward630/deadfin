// SPDX-License-Identifier: GPL-2.0-only

package tasks

import (
	"context"
	"errors"
	"sync"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// ErrTrickplayRunnerClosed is returned when work is requested after shutdown
// has begun.
var ErrTrickplayRunnerClosed = errors.New("trickplay runner is closed")

// Executable is work that can be started by the trickplay runner.
type Executable interface {
	Execute(context.Context) error
}

// RegenerateTrickplayRequester accepts a targeted forced-regeneration intent
// before the runner starts its normal coalesced execution.
type RegenerateTrickplayRequester interface {
	RequestRegenerateTrickplay(dto.UUID)
}

type regenerationRequesterWithError interface {
	RequestRegenerateTrickplay(dto.UUID) error
}

// TrickplayRunner runs scan-time trickplay work in the background and
// coalesces repeated triggers. The scheduler owns the daily
// RefreshTrickplayImages trigger; the runner has no timer of its own.
type TrickplayRunner struct {
	executable Executable
	report     func(error)

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	closed  bool
	active  bool
	pending bool
	workers int
	drained chan struct{}
}

// NewTrickplayRunner creates a runner for executable.
func NewTrickplayRunner(executable Executable, report func(error)) *TrickplayRunner {
	ctx, cancel := context.WithCancel(context.Background())
	drained := make(chan struct{})
	close(drained)
	return &TrickplayRunner{
		executable: executable,
		report:     report,
		ctx:        ctx,
		cancel:     cancel,
		drained:    drained,
	}
}

// Trigger requests one execution. If work is already active, concurrent or
// repeated triggers collapse into one pending execution.
func (runner *TrickplayRunner) Trigger() error {
	if runner == nil {
		return errors.New("trickplay runner is required")
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.closed {
		return ErrTrickplayRunnerClosed
	}
	if runner.executable == nil {
		return errors.New("trickplay runner executable is required")
	}
	return runner.triggerLocked()
}

// TriggerRegenerateTrickplay queues one item for forced regeneration and then
// reuses the runner's normal coalesced execution path.
func (runner *TrickplayRunner) TriggerRegenerateTrickplay(itemID dto.UUID) error {
	if runner == nil {
		return errors.New("trickplay runner is required")
	}
	if itemID.IsNil() {
		return errors.New("trickplay regeneration item id is required")
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.closed {
		return ErrTrickplayRunnerClosed
	}
	switch requester := runner.executable.(type) {
	case regenerationRequesterWithError:
		if err := requester.RequestRegenerateTrickplay(itemID); err != nil {
			return err
		}
	case RegenerateTrickplayRequester:
		requester.RequestRegenerateTrickplay(itemID)
	default:
		return errors.New("trickplay runner executable does not accept regeneration requests")
	}
	return runner.triggerLocked()
}

func (runner *TrickplayRunner) triggerLocked() error {
	if runner.executable == nil {
		return errors.New("trickplay runner executable is required")
	}
	if runner.active {
		runner.pending = true
		return nil
	}
	runner.active = true
	runner.beginWorkerLocked()
	go runner.execute()
	return nil
}

// Close starts shutdown, cancels active work, and waits until runner-owned
// goroutines have exited. If ctx expires, callers may call Close again to keep
// waiting for the same shutdown.
func (runner *TrickplayRunner) Close(ctx context.Context) error {
	if runner == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	runner.mu.Lock()
	if !runner.closed {
		runner.closed = true
		runner.pending = false
		runner.cancel()
	}
	drained := runner.drained
	runner.mu.Unlock()

	select {
	case <-drained:
		return nil
	default:
	}
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait returns when no execution is active or pending, or when ctx ends.
func (runner *TrickplayRunner) Wait(ctx context.Context) error {
	if runner == nil {
		return nil
	}
	runner.mu.Lock()
	drained := runner.drained
	runner.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (runner *TrickplayRunner) execute() {
	defer runner.endWorker()

	for {
		err := runner.executable.Execute(runner.ctx)
		if err != nil && runner.ctx.Err() == nil && runner.report != nil {
			runner.report(err)
		}

		runner.mu.Lock()
		if runner.closed || !runner.pending {
			runner.active = false
			runner.pending = false
			runner.mu.Unlock()
			return
		}
		runner.pending = false
		runner.mu.Unlock()
	}
}

func (runner *TrickplayRunner) beginWorkerLocked() {
	if runner.workers == 0 {
		runner.drained = make(chan struct{})
	}
	runner.workers++
}

func (runner *TrickplayRunner) endWorker() {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.workers--
	if runner.workers < 0 {
		panic("trickplay runner worker accounting underflow")
	}
	if runner.workers == 0 {
		close(runner.drained)
	}
}
