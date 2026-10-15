// SPDX-License-Identifier: GPL-2.0-only

// Package activitylog records server activity and publishes newly committed
// entries to WebSocket feed subscribers.
package activitylog

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
)

// Entry is the Jellyfin ActivityLogEntry wire shape.
type Entry struct {
	ID                  int64    `json:"Id"`
	Name                string   `json:"Name"`
	Overview            *string  `json:"Overview,omitzero"`
	ShortOverview       *string  `json:"ShortOverview,omitzero"`
	Type                string   `json:"Type"`
	ItemID              *string  `json:"ItemId,omitzero"`
	Date                dto.Time `json:"Date"`
	UserID              dto.UUID `json:"UserId"`
	UserPrimaryImageTag *string  `json:"UserPrimaryImageTag,omitzero"`
	Severity            string   `json:"Severity"`
}

// Log owns the process-local activity sequence and its non-blocking event bus.
// P9 may add persistence behind this seam without changing WebSocket consumers.
type Log struct {
	clock clock.Clock
	bus   *events.Bus[Entry]

	mu      sync.Mutex
	closed  bool
	nextID  int64
	entries []Entry
}

// New constructs an activity log on the injected clock.
func New(c clock.Clock) *Log {
	if c == nil {
		panic("activitylog: clock is required")
	}
	return &Log{clock: c, bus: events.NewBus[Entry](c)}
}

// Write commits an entry and then publishes it to current subscribers.
func (log *Log) Write(_ context.Context, entry Entry) (Entry, error) {
	if entry.Name == "" {
		return Entry{}, errors.New("activitylog: name is required")
	}
	if entry.Type == "" {
		return Entry{}, errors.New("activitylog: type is required")
	}

	log.mu.Lock()
	if log.closed {
		log.mu.Unlock()
		return Entry{}, errors.New("activitylog: log is closed")
	}
	log.nextID++
	entry.ID = log.nextID
	entry.Date = dto.NewTime(log.clock.Now())
	if entry.Severity == "" {
		entry.Severity = "Information"
	}
	log.entries = append(log.entries, cloneEntry(entry))
	log.mu.Unlock()

	log.bus.Publish(cloneEntry(entry))
	return entry, nil
}

// Entries returns newest-first defensive copies.
func (log *Log) Entries() []Entry {
	log.mu.Lock()
	defer log.mu.Unlock()
	result := make([]Entry, len(log.entries))
	for index := range log.entries {
		result[len(log.entries)-1-index] = cloneEntry(log.entries[index])
	}
	return result
}

// Prune removes entries whose Date is before the given time and returns the
// count removed. It is the P9 scheduled-task seam reserved in the package
// doc comment. The in-memory log is pruned; persistence (if added later) must
// mirror the same cut.
func (log *Log) Prune(_ context.Context, before time.Time) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.closed {
		return 0, errors.New("activitylog: log is closed")
	}
	kept := log.entries[:0]
	removed := 0
	for _, entry := range log.entries {
		if entry.Date.Time.Before(before) {
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	log.entries = kept
	return removed, nil
}

// Subscribe registers for newly committed entries.
func (log *Log) Subscribe(options events.SubscriptionOptions) *events.Subscription[Entry] {
	return log.bus.Subscribe(options)
}

// Close rejects future writes and stops every subscription.
func (log *Log) Close(ctx context.Context) error {
	log.mu.Lock()
	log.closed = true
	log.mu.Unlock()
	return log.bus.Close(ctx)
}

func cloneEntry(entry Entry) Entry {
	entry.Overview = cloneString(entry.Overview)
	entry.ShortOverview = cloneString(entry.ShortOverview)
	entry.ItemID = cloneString(entry.ItemID)
	entry.UserPrimaryImageTag = cloneString(entry.UserPrimaryImageTag)
	return entry
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
