// SPDX-License-Identifier: GPL-2.0-only

package tasks_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/tasks"
	"go.uber.org/goleak"
)

func TestTrickplayRunnerCoalescesTriggersIntoOnePendingExecutionWithoutOverlap(t *testing.T) {
	executable := newTrickplayExecutable(nil)
	runner := tasks.NewTrickplayRunner(executable, func(error) {})
	t.Cleanup(func() {
		executable.release()
		if err := runner.Close(context.Background()); err != nil {
			t.Errorf("Close runner: %v", err)
		}
	})

	if err := runner.Trigger(); err != nil {
		t.Fatalf("first Trigger: %v", err)
	}
	receiveTrickplayStart(t, executable.started, "first Trigger did not start execution")
	for range 3 {
		if err := runner.Trigger(); err != nil {
			t.Fatalf("coalesced Trigger: %v", err)
		}
	}
	mustNotStartTrickplay(t, executable.started)

	executable.release()
	receiveTrickplayStart(t, executable.started, "coalesced pending Trigger did not run after active execution")
	mustNotStartTrickplay(t, executable.started)
	if got, want := executable.maxConcurrent(), 1; got != want {
		t.Fatalf("overlapping executions = %d, want at most %d", got, want)
	}
}

// TestTrickplayRunnerRequestsTargetedRegenerationBeforeDispatch proves the
// refresh bridge reuses runner coalescing while delivering the one frozen item
// identity to the task. It must not turn a targeted item refresh into a second
// scheduler or an unscoped scheduled refresh.
func TestTrickplayRunnerRequestsTargetedRegenerationBeforeDispatch(t *testing.T) {
	id, err := dto.ParseUUID("00000000-0000-0000-0000-000000000641")
	if err != nil {
		t.Fatalf("parse targeted item id: %v", err)
	}
	executable := newTrickplayExecutable(nil)
	runner := tasks.NewTrickplayRunner(executable, func(error) {})
	t.Cleanup(func() {
		executable.release()
		if err := runner.Close(context.Background()); err != nil {
			t.Errorf("Close runner: %v", err)
		}
	})

	if err := runner.TriggerRegenerateTrickplay(id); err != nil {
		t.Fatalf("TriggerRegenerateTrickplay: %v", err)
	}
	if got, want := executable.requestedRegenerations(), []dto.UUID{id}; !reflect.DeepEqual(got, want) {
		t.Fatalf("targeted regeneration requests = %#v, want %#v", got, want)
	}
	receiveTrickplayStart(t, executable.started, "targeted refresh did not dispatch runner execution")
}

func TestTrickplayRunnerCloseCancelsActiveWorkAndDrainsWithoutLeaks(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	executable := newTrickplayExecutable(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	runner := tasks.NewTrickplayRunner(executable, func(error) {})

	if err := runner.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	receiveTrickplayStart(t, executable.started, "Trigger did not start cancellable execution")
	if err := runner.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got, want := executable.completed(), 1; got != want {
		t.Fatalf("completed executions after Close = %d, want %d drained cancellation", got, want)
	}
}

func TestTrickplayRunnerCloseReturnsAtCallerCancellationUntilUncooperativeWorkDrains(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	executable := newTrickplayExecutable(nil)
	runner := tasks.NewTrickplayRunner(executable, func(error) {})
	t.Cleanup(executable.release)

	if err := runner.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	receiveTrickplayStart(t, executable.started, "Trigger did not start blocked execution")
	closeContext, cancelClose := context.WithCancel(t.Context())
	cancelClose()
	if err := runner.Close(closeContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Close error = %v, want context.Canceled while work is still blocked", err)
	}

	executable.release()
	if err := runner.Close(t.Context()); err != nil {
		t.Fatalf("Close after active execution drained: %v", err)
	}
}

// TestTrickplayRunnerWaitReturnsAfterActiveAndPendingWork proves Wait covers
// the coalesced pending execution, not only the active one.
func TestTrickplayRunnerWaitReturnsAfterActiveAndPendingWork(t *testing.T) {
	executable := newTrickplayExecutable(nil)
	runner := tasks.NewTrickplayRunner(executable, func(error) {})
	t.Cleanup(func() {
		executable.release()
		if err := runner.Close(context.Background()); err != nil {
			t.Errorf("Close runner: %v", err)
		}
	})
	if err := runner.Wait(t.Context()); err != nil {
		t.Fatalf("Wait on idle runner: %v", err)
	}
	if err := runner.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	receiveTrickplayStart(t, executable.started, "Trigger did not start execution")
	if err := runner.Trigger(); err != nil {
		t.Fatalf("pending Trigger: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runner.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait with active work and canceled context = %v, want context.Canceled", err)
	}
	executable.release()
	if err := runner.Wait(t.Context()); err != nil {
		t.Fatalf("Wait after release: %v", err)
	}
	if got, want := executable.completed(), 2; got != want {
		t.Fatalf("completed executions after Wait = %d, want active and pending %d", got, want)
	}
}

type trickplayExecutable struct {
	execute func(context.Context) error
	started chan struct{}

	mu        sync.Mutex
	active    int
	maximum   int
	finished  int
	releaseCh chan struct{}
	releaseMu sync.Once
	requested []dto.UUID
}

func newTrickplayExecutable(execute func(context.Context) error) *trickplayExecutable {
	return &trickplayExecutable{
		execute:   execute,
		started:   make(chan struct{}, 4),
		releaseCh: make(chan struct{}),
	}
}

func (e *trickplayExecutable) Execute(ctx context.Context) error {
	e.mu.Lock()
	e.active++
	if e.active > e.maximum {
		e.maximum = e.active
	}
	e.mu.Unlock()
	e.started <- struct{}{}

	defer func() {
		e.mu.Lock()
		e.active--
		e.finished++
		e.mu.Unlock()
	}()
	if e.execute != nil {
		return e.execute(ctx)
	}
	<-e.releaseCh
	return nil
}

func (e *trickplayExecutable) RequestRegenerateTrickplay(id dto.UUID) {
	e.mu.Lock()
	e.requested = append(e.requested, id)
	e.mu.Unlock()
}

func (e *trickplayExecutable) release() {
	e.releaseMu.Do(func() { close(e.releaseCh) })
}

func (e *trickplayExecutable) maxConcurrent() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.maximum
}

func (e *trickplayExecutable) completed() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.finished
}

func (e *trickplayExecutable) requestedRegenerations() []dto.UUID {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]dto.UUID(nil), e.requested...)
}

func receiveTrickplayStart(t *testing.T, started <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

func mustNotStartTrickplay(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
		t.Fatal("runner started an unexpected overlapping or duplicate execution")
	default:
	}
}
