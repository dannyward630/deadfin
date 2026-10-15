// SPDX-License-Identifier: GPL-2.0-only

package user

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/store"
)

// DeviceActivityStore is the persistence seam of DeviceActivity.
// DeviceByToken reads the reader pool. RefreshDeviceIfCurrent takes the
// writer.
type DeviceActivityStore interface {
	DeviceByToken(context.Context, string) (store.Device, error)
	RefreshDeviceIfCurrent(context.Context, store.Device, time.Duration, time.Time) (store.Device, bool, error)
}

// DeviceActivity applies the AuthorizationContext device refresh off the
// request path. Upstream writes the device row on each authorized request when
// the name or version changes or the activity is older than three minutes.
// The response never depends on that write. On SQLite the write waits for the
// single writer connection, which a library scan can hold for a long time.
//
// A request reads the current row on the reader pool. When no write is due,
// the request continues with no writer access. When a write is due, the
// request queues it and continues. One goroutine owns the queue and applies
// the writes. Requests for the same device coalesce: the latest request wins.
// Close applies the queued writes and stops the goroutine.
type DeviceActivity struct {
	store  DeviceActivityStore
	clock  clock.Clock
	logger *slog.Logger

	mu      sync.Mutex
	pending map[int64]pendingDeviceRefresh
	closed  bool

	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
}

type pendingDeviceRefresh struct {
	device store.Device
	now    time.Time
}

// NewDeviceActivity starts the goroutine that writes queued device refreshes.
// The caller owns the result and must call Close.
func NewDeviceActivity(activityStore DeviceActivityStore, c clock.Clock, logger *slog.Logger) *DeviceActivity {
	if logger == nil {
		logger = slog.Default()
	}
	activity := &DeviceActivity{
		store:   activityStore,
		clock:   c,
		logger:  logger,
		pending: make(map[int64]pendingDeviceRefresh),
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	activity.ctx, activity.cancel = context.WithCancel(context.Background())
	go activity.run()
	return activity
}

// Token reads the device row for an access token on the reader pool.
func (a *DeviceActivity) Token(ctx context.Context, token string) (store.Device, error) {
	return a.store.DeviceByToken(ctx, token)
}

// RefreshDeviceIfCurrent queues the device refresh when a write is due and
// returns without waiting for the writer. It returns the current row and
// whether it queued a write. The queued write keeps the compare-and-swap
// semantics of store.Identity.RefreshDeviceIfCurrent, so it never recreates a
// logged-out token or overwrites a newer row.
func (a *DeviceActivity) RefreshDeviceIfCurrent(ctx context.Context, device store.Device) (store.Device, bool, error) {
	now := a.clock.Now().UTC()
	current, err := a.store.DeviceByToken(ctx, device.AccessToken)
	if err != nil {
		return store.Device{}, false, err
	}
	if current.ID != device.ID || !store.DeviceRefreshDue(current, device, activityStaleness, now) {
		return current, false, nil
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return current, false, nil
	}
	a.pending[device.ID] = pendingDeviceRefresh{device: device, now: now}
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
	return current, true, nil
}

// Close stops new queueing, writes the queued refreshes, and waits for the
// goroutine to exit. When ctx ends first, Close cancels the in-flight write
// and returns ctx.Err() after the goroutine exits.
func (a *DeviceActivity) Close(ctx context.Context) error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		a.mu.Unlock()
		close(a.stop)
	})
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		a.cancel()
		<-a.done
		return ctx.Err()
	}
}

func (a *DeviceActivity) run() {
	defer close(a.done)
	defer a.cancel()
	for {
		select {
		case <-a.wake:
			a.drain()
		case <-a.stop:
			a.drain()
			return
		}
	}
}

func (a *DeviceActivity) drain() {
	a.mu.Lock()
	batch := a.pending
	a.pending = make(map[int64]pendingDeviceRefresh)
	a.mu.Unlock()
	for _, refresh := range batch {
		if a.ctx.Err() != nil {
			return
		}
		_, _, err := a.store.RefreshDeviceIfCurrent(a.ctx, refresh.device, activityStaleness, refresh.now)
		if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, context.Canceled) {
			a.logger.Warn("device activity refresh failed", slog.Int64("device_id", refresh.device.ID), slog.Any("error", err))
		}
	}
}
