// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestP6ApplicationCloseAdmissionHonorsWaitingCallerContext protects callers
// from a shutdown transition already owned by another Close. A canceled caller
// must not wait for the owner to finish draining dependencies; after the owner
// releases admission, a fresh caller must still complete cleanup.
func TestP6ApplicationCloseAdmissionHonorsWaitingCallerContext(t *testing.T) {
	app := &application{}
	transition := app.closeGate()
	<-transition.gate // Simulate the first Close owning the shutdown transition.

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	result := make(chan error, 1)
	go func() { result <- app.Close(canceled) }()

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("concurrent Close with canceled context = %v, want context.Canceled", err)
		}
	case <-timer.C:
		t.Fatal("concurrent Close with canceled context waited for the active shutdown transition")
	}

	transition.gate <- struct{}{}
	if err := app.Close(context.Background()); err != nil {
		t.Fatalf("fresh Close after admission release: %v", err)
	}
}
