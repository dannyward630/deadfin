// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/events"
)

// RefreshProgressBus provides typed, non-blocking progress subscriptions for a
// Scanner. Scanner owns it and closes every subscription from Scanner.Close.
type RefreshProgressBus struct {
	bus *events.Bus[RefreshProgress]
}

func newRefreshProgressBus(c clock.Clock) *RefreshProgressBus {
	return &RefreshProgressBus{bus: events.NewBus[RefreshProgress](c)}
}

func (b *RefreshProgressBus) Subscribe(options events.SubscriptionOptions) *events.Subscription[RefreshProgress] {
	return b.bus.Subscribe(options)
}

func (b *RefreshProgressBus) publish(progress RefreshProgress) {
	b.bus.Publish(progress)
}

func (b *RefreshProgressBus) close() error {
	return b.bus.Close(context.Background())
}

// Progress returns Scanner's typed refresh-progress subscription seam.
func (s *Scanner) Progress() *RefreshProgressBus {
	if s == nil {
		return nil
	}
	return s.progress
}

// Changes returns Scanner's committed library-mutation subscription seam.
func (s *Scanner) Changes() *ChangeBus {
	if s == nil {
		return nil
	}
	return s.changes
}
