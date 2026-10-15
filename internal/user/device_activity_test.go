// SPDX-License-Identifier: GPL-2.0-only

package user

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/store"
)

// blockingDeviceStore models the SQLite writer held by a library scan: each
// RefreshDeviceIfCurrent call waits until the test releases the writer or the
// call context ends.
type blockingDeviceStore struct {
	mu       sync.Mutex
	current  store.Device
	refresh  []store.Device
	nows     []time.Time
	entered  chan struct{}
	release  chan struct{}
	canceled bool
}

func (s *blockingDeviceStore) DeviceByToken(context.Context, string) (store.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current, nil
}

func (s *blockingDeviceStore) RefreshDeviceIfCurrent(ctx context.Context, device store.Device, _ time.Duration, now time.Time) (store.Device, bool, error) {
	s.entered <- struct{}{}
	select {
	case <-s.release:
	case <-ctx.Done():
		s.mu.Lock()
		s.canceled = true
		s.mu.Unlock()
		return store.Device{}, false, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh = append(s.refresh, device)
	s.nows = append(s.nows, now)
	return device, true, nil
}

func newBlockingDeviceStore(current store.Device) *blockingDeviceStore {
	return &blockingDeviceStore{current: current, entered: make(chan struct{}, 8), release: make(chan struct{})}
}

func deviceActivityFixture(start time.Time) store.Device {
	return store.Device{
		ID:               7,
		AccessToken:      "token",
		AppName:          "Jellyfin Web",
		AppVersion:       "10.11.0",
		DeviceName:       "Browser",
		DeviceID:         "device",
		DateCreated:      start.Add(-time.Hour),
		DateModified:     start.Add(-time.Hour),
		DateLastActivity: start.Add(-time.Minute),
	}
}

func TestDeviceActivitySkipsTheWriterWhenNoRefreshIsDue(t *testing.T) {
	defer goleak.VerifyNone(t)
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(start)
	current := deviceActivityFixture(start)
	devices := newBlockingDeviceStore(current)
	activity := NewDeviceActivity(devices, fake, nil)

	// Exactly three minutes is not stale (upstream `TotalMinutes > 3`).
	fake.Advance(2 * time.Minute)
	got, queued, err := activity.RefreshDeviceIfCurrent(t.Context(), current)
	if err != nil || queued || got != current {
		t.Fatalf("RefreshDeviceIfCurrent = %+v, %t, %v; want current row, not queued", got, queued, err)
	}
	if err := activity.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(devices.refresh) != 0 {
		t.Fatalf("writer refreshes = %d, want 0", len(devices.refresh))
	}
}

func TestDeviceActivityReturnsWhileTheWriterIsHeldAndCoalescesPerDevice(t *testing.T) {
	defer goleak.VerifyNone(t)
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(start)
	current := deviceActivityFixture(start)
	devices := newBlockingDeviceStore(current)
	activity := NewDeviceActivity(devices, fake, nil)

	// The first due refresh enters the store and waits on the held writer.
	fake.Advance(4 * time.Minute)
	if _, queued, err := activity.RefreshDeviceIfCurrent(t.Context(), current); err != nil || !queued {
		t.Fatalf("first refresh queued = %t, %v; want queued", queued, err)
	}
	<-devices.entered

	// Later requests return at once although the writer is still held. The
	// two that follow coalesce to the latest one.
	fake.Advance(time.Second)
	renamed := current
	renamed.DeviceName = "Renamed"
	if _, queued, err := activity.RefreshDeviceIfCurrent(t.Context(), renamed); err != nil || !queued {
		t.Fatalf("second refresh queued = %t, %v; want queued", queued, err)
	}
	fake.Advance(time.Second)
	upgraded := renamed
	upgraded.AppVersion = "10.11.1"
	if _, queued, err := activity.RefreshDeviceIfCurrent(t.Context(), upgraded); err != nil || !queued {
		t.Fatalf("third refresh queued = %t, %v; want queued", queued, err)
	}

	// Close drains the queue once the writer is free.
	closed := make(chan error, 1)
	go func() { closed <- activity.Close(context.Background()) }()
	devices.release <- struct{}{}
	<-devices.entered
	devices.release <- struct{}{}
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(devices.refresh) != 2 {
		t.Fatalf("writer refreshes = %d, want 2 (first, then the coalesced latest)", len(devices.refresh))
	}
	last := devices.refresh[1]
	if last.DeviceName != "Renamed" || last.AppVersion != "10.11.1" {
		t.Fatalf("coalesced refresh = %+v, want latest name and version", last)
	}
	if want := start.Add(4*time.Minute + 2*time.Second); !devices.nows[1].Equal(want) {
		t.Fatalf("coalesced refresh time = %s, want request time %s", devices.nows[1], want)
	}

	// After Close, requests neither queue nor block.
	if _, queued, err := activity.RefreshDeviceIfCurrent(t.Context(), upgraded); err != nil || queued {
		t.Fatalf("refresh after Close queued = %t, %v; want not queued", queued, err)
	}
}

func TestDeviceActivityCloseCancelsAHeldWriteWhenItsContextEnds(t *testing.T) {
	defer goleak.VerifyNone(t)
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(start)
	current := deviceActivityFixture(start)
	devices := newBlockingDeviceStore(current)
	activity := NewDeviceActivity(devices, fake, nil)

	fake.Advance(4 * time.Minute)
	if _, queued, err := activity.RefreshDeviceIfCurrent(t.Context(), current); err != nil || !queued {
		t.Fatalf("refresh queued = %t, %v; want queued", queued, err)
	}
	<-devices.entered

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := activity.Close(ctx); err != context.Canceled {
		t.Fatalf("Close with ended context = %v, want context.Canceled", err)
	}
	if !devices.canceled {
		t.Fatal("held write was not canceled")
	}
}

func TestDeviceActivityIgnoresARowReplacedSinceTheSnapshot(t *testing.T) {
	defer goleak.VerifyNone(t)
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(start)
	current := deviceActivityFixture(start)
	devices := newBlockingDeviceStore(current)
	activity := NewDeviceActivity(devices, fake, nil)
	defer func() {
		if err := activity.Close(t.Context()); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	fake.Advance(4 * time.Minute)
	stale := current
	stale.DateModified = current.DateModified.Add(-time.Minute)
	if _, queued, err := activity.RefreshDeviceIfCurrent(t.Context(), stale); err != nil || queued {
		t.Fatalf("refresh of an old snapshot queued = %t, %v; want not queued", queued, err)
	}
	other := current
	other.ID = current.ID + 1
	if _, queued, err := activity.RefreshDeviceIfCurrent(t.Context(), other); err != nil || queued {
		t.Fatalf("refresh of a replaced row queued = %t, %v; want not queued", queued, err)
	}
}
