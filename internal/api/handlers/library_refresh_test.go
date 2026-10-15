// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/library"
)

func TestLibraryRefreshStartsOnlyOneActiveScanAndRespondsBeforeCompletion(t *testing.T) {
	trigger := newBlockingRefreshTrigger()
	trigger.holdAcceptance()
	t.Cleanup(trigger.finishActive)
	t.Cleanup(trigger.acceptTrigger)

	rt := router.New()
	handlers.RegisterLibrary(rt, trigger, nil)
	assertLibraryRefreshRoute(t, rt)

	first, firstDone := startLibraryRefresh(rt, "/lIbRaRy/rEfReSh")
	select {
	case <-firstDone:
		t.Fatal("Library/Refresh responded before its trigger accepted the scan")
	case <-time.After(50 * time.Millisecond):
	}
	trigger.acceptTrigger()
	waitForLibraryRefresh(t, firstDone)
	assertLibraryRefreshResponse(t, first)
	trigger.waitStarted(t, 1)

	second := performLibraryRefresh(t, rt, "/Library/Refresh")
	assertLibraryRefreshResponse(t, second)
	trigger.assertStarts(t, 1)

	beforeCompletion := first.snapshot()
	trigger.finishActive()
	if afterCompletion := first.snapshot(); afterCompletion != beforeCompletion {
		t.Fatalf("handler wrote after its response: before=%#v after=%#v", beforeCompletion, afterCompletion)
	}

	third := performLibraryRefresh(t, rt, "/Library/Refresh")
	assertLibraryRefreshResponse(t, third)
	trigger.waitStarted(t, 2)
	trigger.finishActive()
}

func TestLibraryRefreshMapsClosedScannerToServiceUnavailable(t *testing.T) {
	trigger := &blockingRefreshTrigger{err: library.ErrScannerClosed}
	rt := router.New()
	handlers.RegisterLibrary(rt, trigger, nil)

	response := performLibraryRefresh(t, rt, "/Library/Refresh")
	if got, want := response.snapshot(), (libraryRefreshResponse{status: http.StatusServiceUnavailable, writes: 1}); got != want {
		t.Fatalf("closed scanner response = %#v, want %#v", got, want)
	}
	trigger.assertStarts(t, 0)
}

func TestLibraryRefreshMapsClosedActiveTriggerToServiceUnavailable(t *testing.T) {
	trigger := newBlockingRefreshTrigger()
	t.Cleanup(trigger.finishActive)
	rt := router.New()
	handlers.RegisterLibrary(rt, trigger, nil)

	assertLibraryRefreshResponse(t, performLibraryRefresh(t, rt, "/Library/Refresh"))
	trigger.waitStarted(t, 1)
	trigger.setError(library.ErrScannerClosed)

	response := performLibraryRefresh(t, rt, "/Library/Refresh")
	if got, want := response.snapshot(), (libraryRefreshResponse{status: http.StatusServiceUnavailable, writes: 1}); got != want {
		t.Fatalf("closed active scanner response = %#v, want %#v", got, want)
	}
	trigger.assertStarts(t, 1)
}

func assertLibraryRefreshRoute(t *testing.T, rt *router.Router) {
	t.Helper()
	for _, route := range rt.Routes() {
		if route.Method == http.MethodPost && route.Pattern == "/Library/Refresh" {
			if got, want := route.OperationID, "RefreshLibrary"; got != want {
				t.Fatalf("Library/Refresh operation ID = %q, want %q", got, want)
			}
			if got, want := route.Policy, string(auth.PolicyRequiresElevation); got != want {
				t.Fatalf("Library/Refresh policy = %q, want %q", got, want)
			}
			if route.Anonymous {
				t.Fatal("Library/Refresh must not be anonymous")
			}
			return
		}
	}
	t.Fatal("POST /Library/Refresh was not registered")
}

func performLibraryRefresh(t *testing.T, rt http.Handler, path string) *libraryRefreshRecorder {
	t.Helper()
	recorder, done := startLibraryRefresh(rt, path)
	waitForLibraryRefresh(t, done)
	return recorder
}

func startLibraryRefresh(rt http.Handler, path string) (*libraryRefreshRecorder, <-chan struct{}) {
	recorder := newLibraryRefreshRecorder()
	done := make(chan struct{})
	go func() {
		rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
		close(done)
	}()
	return recorder, done
}

func waitForLibraryRefresh(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Library/Refresh did not return promptly after trigger acceptance")
	}
}

func assertLibraryRefreshResponse(t *testing.T, recorder *libraryRefreshRecorder) {
	t.Helper()
	if got, want := recorder.snapshot(), (libraryRefreshResponse{status: http.StatusNoContent, writes: 1}); got != want {
		t.Fatalf("Library/Refresh response = %#v, want %#v", got, want)
	}
}

type blockingRefreshTrigger struct {
	mu         sync.Mutex
	active     *blockingRefreshRun
	starts     int
	err        error
	started    chan int
	acceptance chan struct{}
	acceptOnce sync.Once
}

type blockingRefreshRun struct {
	release chan struct{}
	done    chan struct{}
}

func newBlockingRefreshTrigger() *blockingRefreshTrigger {
	return &blockingRefreshTrigger{started: make(chan int, 3)}
}

func (t *blockingRefreshTrigger) Trigger() error {
	t.mu.Lock()
	acceptance := t.acceptance
	t.mu.Unlock()
	if acceptance != nil {
		<-acceptance
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	if t.active != nil {
		return nil
	}
	run := &blockingRefreshRun{release: make(chan struct{}), done: make(chan struct{})}
	t.active = run
	t.starts++
	if t.started != nil {
		t.started <- t.starts
	}
	go func() {
		<-run.release
		t.mu.Lock()
		if t.active == run {
			t.active = nil
		}
		t.mu.Unlock()
		close(run.done)
	}()
	return nil
}

func (t *blockingRefreshTrigger) holdAcceptance() {
	t.mu.Lock()
	t.acceptance = make(chan struct{})
	t.mu.Unlock()
}

func (t *blockingRefreshTrigger) acceptTrigger() {
	t.mu.Lock()
	acceptance := t.acceptance
	t.mu.Unlock()
	if acceptance != nil {
		t.acceptOnce.Do(func() { close(acceptance) })
	}
}

func (t *blockingRefreshTrigger) setError(err error) {
	t.mu.Lock()
	t.err = err
	t.mu.Unlock()
}

func (t *blockingRefreshTrigger) waitStarted(tb testing.TB, want int) {
	tb.Helper()
	select {
	case got := <-t.started:
		if got != want {
			tb.Fatalf("scanner start = %d, want %d", got, want)
		}
	case <-time.After(time.Second):
		tb.Fatalf("scanner did not start run %d", want)
	}
}

func (t *blockingRefreshTrigger) assertStarts(tb testing.TB, want int) {
	tb.Helper()
	t.mu.Lock()
	got := t.starts
	t.mu.Unlock()
	if got != want {
		tb.Fatalf("scanner starts = %d, want %d", got, want)
	}
}

func (t *blockingRefreshTrigger) finishActive() {
	t.mu.Lock()
	run := t.active
	t.mu.Unlock()
	if run == nil {
		return
	}
	close(run.release)
	<-run.done
}

func (t *blockingRefreshTrigger) RefreshStates() map[dto.UUID]library.FolderRefreshState {
	return map[dto.UUID]library.FolderRefreshState{}
}

type libraryRefreshRecorder struct {
	mu     sync.Mutex
	header http.Header
	status int
	writes int
	body   bytes.Buffer
}

type libraryRefreshResponse struct {
	status int
	writes int
	body   string
}

func newLibraryRefreshRecorder() *libraryRefreshRecorder {
	return &libraryRefreshRecorder{header: make(http.Header)}
}

func (r *libraryRefreshRecorder) Header() http.Header { return r.header }

func (r *libraryRefreshRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes++
	if r.status == 0 {
		r.status = status
	}
}

func (r *libraryRefreshRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.writes++
	return r.body.Write(data)
}

func (r *libraryRefreshRecorder) snapshot() libraryRefreshResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	return libraryRefreshResponse{status: r.status, writes: r.writes, body: r.body.String()}
}

var _ interface {
	Trigger() error
	RefreshStates() map[dto.UUID]library.FolderRefreshState
} = (*blockingRefreshTrigger)(nil)
