// SPDX-License-Identifier: GPL-2.0-only

// Package tasks publishes the process's scheduled-task catalog and execution
// state. Task execution itself remains supervised by each task's owning service.
package tasks

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
)

// State is Jellyfin's TaskState wire enum.
type State string

const (
	StateIdle       State = "Idle"
	StateCancelling State = "Cancelling"
	StateRunning    State = "Running"
)

// TriggerInfo is a scheduled task trigger projection.
type TriggerInfo struct {
	Type            string  `json:"Type"`
	TimeOfDayTicks  *int64  `json:"TimeOfDayTicks,omitzero"`
	IntervalTicks   *int64  `json:"IntervalTicks,omitzero"`
	DayOfWeek       *string `json:"DayOfWeek,omitzero"`
	MaxRuntimeTicks *int64  `json:"MaxRuntimeTicks,omitzero"`
}

// Result is the last task execution result.
type Result struct {
	StartTimeUTC     dto.Time `json:"StartTimeUtc"`
	EndTimeUTC       dto.Time `json:"EndTimeUtc"`
	Status           string   `json:"Status"`
	Name             string   `json:"Name"`
	Key              string   `json:"Key"`
	ID               string   `json:"Id"`
	ErrorMessage     string   `json:"ErrorMessage"`
	LongErrorMessage string   `json:"LongErrorMessage"`
}

// Info is the Jellyfin TaskInfo wire shape.
type Info struct {
	Name                      string        `json:"Name"`
	State                     State         `json:"State"`
	CurrentProgressPercentage *float64      `json:"CurrentProgressPercentage,omitzero"`
	ID                        string        `json:"Id"`
	LastExecutionResult       *Result       `json:"LastExecutionResult,omitzero"`
	Triggers                  []TriggerInfo `json:"Triggers"`
	Description               string        `json:"Description"`
	Category                  string        `json:"Category"`
	IsHidden                  bool          `json:"IsHidden"`
	Key                       string        `json:"Key"`
}

// Catalog owns current scheduled-task projections and change notifications.
type Catalog struct {
	bus *events.Bus[[]Info]

	mu     sync.Mutex
	closed bool
	byID   map[string]Info
}

// New constructs an empty task catalog.
func New(c clock.Clock) *Catalog {
	if c == nil {
		panic("tasks: clock is required")
	}
	return &Catalog{bus: events.NewBus[[]Info](c), byID: make(map[string]Info)}
}

// Upsert commits task state and publishes the complete visible catalog.
func (catalog *Catalog) Upsert(_ context.Context, info Info) error {
	if info.ID == "" {
		return errors.New("tasks: id is required")
	}
	if info.Name == "" {
		return errors.New("tasks: name is required")
	}
	if info.State == "" {
		info.State = StateIdle
	}
	if info.Triggers == nil {
		info.Triggers = []TriggerInfo{}
	}

	catalog.mu.Lock()
	if catalog.closed {
		catalog.mu.Unlock()
		return errors.New("tasks: catalog is closed")
	}
	catalog.byID[info.ID] = cloneInfo(info)
	snapshot := catalog.snapshotLocked()
	catalog.mu.Unlock()

	catalog.bus.Publish(snapshot)
	return nil
}

// Snapshot returns visible tasks ordered by name and id.
func (catalog *Catalog) Snapshot() []Info {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	return catalog.snapshotLocked()
}

// Subscribe registers for complete visible-catalog changes.
func (catalog *Catalog) Subscribe(options events.SubscriptionOptions) *events.Subscription[[]Info] {
	return catalog.bus.Subscribe(options)
}

// Close rejects future updates and stops every subscription.
func (catalog *Catalog) Close(ctx context.Context) error {
	catalog.mu.Lock()
	catalog.closed = true
	catalog.mu.Unlock()
	return catalog.bus.Close(ctx)
}

func (catalog *Catalog) snapshotLocked() []Info {
	result := make([]Info, 0, len(catalog.byID))
	for _, info := range catalog.byID {
		if !info.IsHidden {
			result = append(result, cloneInfo(info))
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Name == result[right].Name {
			return result[left].ID < result[right].ID
		}
		return result[left].Name < result[right].Name
	})
	return result
}

func cloneInfo(info Info) Info {
	if info.CurrentProgressPercentage != nil {
		value := *info.CurrentProgressPercentage
		info.CurrentProgressPercentage = &value
	}
	if info.LastExecutionResult != nil {
		value := *info.LastExecutionResult
		info.LastExecutionResult = &value
	}
	if info.Triggers != nil {
		info.Triggers = append([]TriggerInfo{}, info.Triggers...)
	}
	return info
}
