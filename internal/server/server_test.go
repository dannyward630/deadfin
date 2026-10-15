// SPDX-License-Identifier: GPL-2.0-only

package server_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/server"

	"go.uber.org/goleak"
)

func TestServerCloseCanRetryAfterContextCancellation(t *testing.T) {
	defer goleak.VerifyNone(t)

	release := make(chan struct{})
	entered := make(chan struct{})
	server, listener := newServingServer(t, blockingHandler(release, entered))
	defer closeListener(t, listener)
	defer closeRelease(release)

	requestDone := issueRequest(listener.Addr().String())
	awaitSignal(t, entered, "handler entry")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Close(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("first close error = %v, want context canceled", err)
	}

	closeRelease(release)
	awaitRequest(t, requestDone)
	if err := server.Close(context.Background()); err != nil {
		t.Fatalf("retry close: %v", err)
	}
}

func TestServerServeCancellationIsBoundedForStuckHandler(t *testing.T) {
	defer goleak.VerifyNone(t)

	release := make(chan struct{})
	entered := make(chan struct{})
	defer closeRelease(release)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer closeListener(t, listener)

	fake := clock.NewFake(time.Unix(0, 0).UTC())
	observed := observedClock{fake: fake, timers: make(chan time.Duration, 1)}
	instance, err := server.New(server.Deps{
		Clock:   observed,
		Handler: blockingHandler(release, entered),
		Addr:    listener.Addr().String(),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- instance.Serve(ctx, listener)
	}()

	requestDone := issueRequest(listener.Addr().String())
	awaitSignal(t, entered, "handler entry")
	cancel()

	delay := receiveDuration(t, observed.timers, "shutdown timer")
	if delay <= 0 {
		t.Fatalf("shutdown timer duration = %s, want positive duration", delay)
	}
	fake.Advance(delay)
	awaitServe(t, served)

	closeRelease(release)
	awaitRequest(t, requestDone)
}

func TestServerCloseCanceledContextDoesNotWaitForAnotherClose(t *testing.T) {
	defer goleak.VerifyNone(t)

	release := make(chan struct{})
	entered := make(chan struct{})
	defer closeRelease(release)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer closeListener(t, listener)
	watched := &closeNotifyingListener{Listener: listener, closed: make(chan struct{})}
	instance, err := server.New(server.Deps{
		Clock:   clock.NewFake(time.Unix(0, 0).UTC()),
		Handler: blockingHandler(release, entered),
		Addr:    listener.Addr().String(),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go func() {
		_ = instance.Serve(context.Background(), watched)
	}()

	requestDone := issueRequest(listener.Addr().String())
	awaitSignal(t, entered, "handler entry")
	first := make(chan error, 1)
	go func() {
		first <- instance.Close(context.Background())
	}()
	awaitSignal(t, watched.closed, "first close listener shutdown")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	second := make(chan error, 1)
	go func() {
		second <- instance.Close(canceled)
	}()
	if err := awaitCloseResult(t, second, "canceled close"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled close error = %v, want context canceled", err)
	}

	closeRelease(release)
	awaitRequest(t, requestDone)
	if err := awaitCloseResultEventually(t, first, "first close"); err != nil {
		t.Fatalf("first close: %v", err)
	}
}

func closeRelease(release chan struct{}) {
	select {
	case <-release:
	default:
		close(release)
	}
}

func blockingHandler(release <-chan struct{}, entered chan<- struct{}) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(entered)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	})
}

func newServingServer(t *testing.T, handler http.Handler) (*server.Server, net.Listener) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	instance, err := server.New(server.Deps{
		Clock:   clock.NewFake(time.Unix(0, 0).UTC()),
		Handler: handler,
		Addr:    listener.Addr().String(),
	})
	if err != nil {
		if closeErr := listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			t.Errorf("close listener after server construction failure: %v", closeErr)
		}
		t.Fatalf("new server: %v", err)
	}
	go func() {
		_ = instance.Serve(context.Background(), listener)
	}()
	return instance, listener
}

type observedClock struct {
	fake   *clock.Fake
	timers chan time.Duration
}

func (c observedClock) Now() time.Time {
	return c.fake.Now()
}

func (c observedClock) NewTimer(delay time.Duration) clock.Timer {
	timer := c.fake.NewTimer(delay)
	select {
	case c.timers <- delay:
	default:
	}
	return timer
}

type closeNotifyingListener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func (l *closeNotifyingListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
	})
	return l.Listener.Close()
}

func closeListener(t *testing.T, listener net.Listener) {
	t.Helper()
	if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("close listener: %v", err)
	}
}

func issueRequest(address string) <-chan error {
	done := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + address)
		if response != nil {
			if closeErr := response.Body.Close(); closeErr != nil {
				done <- closeErr
				return
			}
		}
		done <- err
	}()
	return done
}

func awaitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
		return
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func awaitRequest(t *testing.T, request <-chan error) {
	t.Helper()
	select {
	case <-request:
		return
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish after handler release")
	}
}

func receiveDuration(t *testing.T, durations <-chan time.Duration, name string) time.Duration {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case duration := <-durations:
		return duration
	case <-timer.C:
		t.Fatalf("did not receive %s after deterministic work yielded", name)
		return 0
	}
}

func awaitServe(t *testing.T, served <-chan error) {
	t.Helper()
	// Serve's return after a force-close is NOT deterministic under the fake
	// clock: net/http.Server.Shutdown notices the force-close only on its
	// real-time poll tick (1-500ms wall clock, shutdownPollIntervalMax). A
	// bounded Gosched budget loses that race whenever the first tick lands
	// late, so this step must wait in wall-clock time.
	if err := awaitCloseResultEventually(t, served, "serve"); err != nil {
		t.Fatalf("serve after cancellation: %v", err)
	}
}

func awaitCloseResult(t *testing.T, results <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return before the cancellation deadline", name)
		return nil
	}
}

func awaitCloseResultEventually(t *testing.T, results <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return after handler release", name)
		return nil
	}
}
