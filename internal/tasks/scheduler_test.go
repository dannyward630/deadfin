// SPDX-License-Identifier: GPL-2.0-only

package tasks_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

const refreshLibraryID = "7738148ffcd07979c7ceb148e06b3aed"

// realClock is the wall-clock time source for test polling deadlines. The
// convention gate permits time.Now only in internal/clock, so test
// infrastructure that needs the real wall clock uses the clock package.
var realClock = clock.NewReal()

func newTestScheduler(t *testing.T, c clock.Clock) (*tasks.Scheduler, *tasks.Catalog, string, string) {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config", "ScheduledTasks")
	dataDir := filepath.Join(dir, "data", "ScheduledTasks")
	catalog := tasks.New(c)
	scheduler, err := tasks.NewScheduler(c, time.UTC, catalog, configDir, dataDir)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })
	return scheduler, catalog, configDir, dataDir
}

// dotNetGUID computes MD5(UTF-16LE(typeName)), then applies the .NET Guid
// little-endian transform on the first three fields, and renders the result
// as a lowercase "N" (no dashes) hex string. This mirrors Jellyfin's
// ScheduledTaskWorker ID derivation (BaseExtensions.cs:30-34):
//
//	new Guid(MD5.HashData(Encoding.Unicode.GetBytes(typeName))).ToString("N")
//
// .NET Guid(byte[16]) stores the first three fields little-endian, so the
// byte swaps are: [0-3] reversed, [4-5] reversed, [6-7] reversed.
func dotNetGUID(typeName string) string {
	// UTF-16LE encoding of the type name.
	le := utf16Encode(typeName)
	sum := md5.Sum(le)
	// .NET Guid little-endian transform on first three fields.
	b := sum
	b[0], b[1], b[2], b[3] = sum[3], sum[2], sum[1], sum[0]
	b[4], b[5] = sum[5], sum[4]
	b[6], b[7] = sum[7], sum[6]
	return hex.EncodeToString(b[:])
}

// utf16Encode encodes s as UTF-16LE bytes (matching .NET Encoding.Unicode).
func utf16Encode(s string) []byte {
	runes := []rune(s)
	buf := make([]byte, 0, len(runes)*2)
	for _, r := range runes {
		r2 := uint16(r)
		buf = append(buf, byte(r2), byte(r2>>8))
	}
	return buf
}

func TestSchedulerStockTaskIdentity(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	if err := tasks.RegisterBuiltins(s, tasks.BuiltinDeps{}); err != nil {
		t.Fatalf("RegisterBuiltins: %v", err)
	}

	list := s.List()
	if len(list) != 12 {
		t.Fatalf("List() has %d tasks, want 12", len(list))
	}

	// Re-derive every stock ID from the C# type name using the exact
	// upstream algorithm: MD5(UTF-16LE(type.FullName)) → .NET Guid(byte[16])
	want := map[string]string{ // ID → Key
		dotNetGUID("Emby.Server.Implementations.ScheduledTasks.Tasks.RefreshMediaLibraryTask"):    "RefreshLibrary",
		dotNetGUID("Emby.Server.Implementations.ScheduledTasks.Tasks.PeopleValidationTask"):       "RefreshPeople",
		dotNetGUID("Emby.Server.Implementations.ScheduledTasks.Tasks.ChapterImagesTask"):          "RefreshChapterImages",
		dotNetGUID("Emby.Server.Implementations.ScheduledTasks.Tasks.AudioNormalizationTask"):     "AudioNormalization",
		dotNetGUID("Emby.Server.Implementations.ScheduledTasks.Tasks.MediaSegmentExtractionTask"): "TaskExtractMediaSegments",
		dotNetGUID("Emby.Server.Implementations.ScheduledTasks.Tasks.OptimizeDatabaseTask"):       "OptimizeDatabaseTask",
		dotNetGUID("Emby.Server.Implementations.ScheduledTasks.Tasks.CleanActivityLogTask"):       "CleanActivityLog",
		dotNetGUID("Emby.Server.Implementations.ScheduledTasks.Tasks.CleanupUserDataTask"):        "CleanupUserDataTask",
		dotNetGUID("Emby.Server.Implementations.ScheduledTasks.Tasks.DeleteLogFileTask"):          "CleanLogFiles",
		dotNetGUID("Emby.Server.Implementations.ScheduledTasks.Tasks.DeleteCacheFileTask"):        "DeleteCacheFiles",
		dotNetGUID("Emby.Server.Implementations.ScheduledTasks.Tasks.DeleteTranscodeFileTask"):    "DeleteTranscodeFiles",
		dotNetGUID("MediaBrowser.Providers.MediaInfo.SubtitleScheduledTask"):                      "DownloadSubtitles",
	}
	got := make(map[string]string)
	for _, info := range list {
		got[info.ID] = info.Key
	}
	for id, key := range want {
		if g, ok := got[id]; !ok {
			t.Errorf("task ID %s (key %s) missing from List()", id, key)
		} else if g != key {
			t.Errorf("task ID %s key = %q, want %q", id, g, key)
		}
	}
}

func TestSchedulerGetCaseInsensitive(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)
	_ = tasks.RegisterBuiltins(s, tasks.BuiltinDeps{})

	info, ok := s.Get(strings.ToUpper(refreshLibraryID))
	if !ok {
		t.Fatal("Get with uppercase ID failed")
	}
	if info.ID != refreshLibraryID {
		t.Fatalf("Get returned ID %q, want %q", info.ID, refreshLibraryID)
	}
}

func TestSchedulerStartupTriggerFires(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	startupTick := int64(0)
	ran := make(chan struct{}, 1)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		startupTick++
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}}

	if err := s.Register(tasks.Definition{
		ID:              "startup-test-000000000000000000000000001",
		Key:             "startup",
		Name:            "Startup Test",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerStartup}},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	c.Advance(2999 * time.Millisecond)
	if startupTick != 0 {
		t.Fatalf("task ran after 2.999s, should not have fired yet; startupTick=%d", startupTick)
	}

	c.Advance(1 * time.Millisecond)
	// Deterministically wait for the execution goroutine to signal (no
	// wall-clock sleep).
	waitForRan(t, ran, "startup trigger after 3s")
	if startupTick != 1 {
		t.Fatalf("startupTick = %d, want 1", startupTick)
	}
}

func TestSchedulerStartupTriggerFiresOnce(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	var runs atomic.Int32
	ran := make(chan struct{}, 4)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		runs.Add(1)
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}}

	if err := s.Register(tasks.Definition{
		ID:              "startup-once-00000000000000000000000001",
		Key:             "startup-once",
		Name:            "Startup Once Test",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerStartup}},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Fire at 3s.
	c.Advance(3 * time.Second)
	waitForRan(t, ran, "startup first fire")

	// Wait for the execution to complete (observable state, no wall-clock sleep).
	waitForTaskIdle(t, s, "startup-once-00000000000000000000000001")
	// Advance well past another 3s — the startup trigger must NOT rearm.
	c.Advance(10 * time.Second)
	// No timer is armed (startup triggers never rearm), so the channel
	// must be empty — confirmed deterministically with a gosched yield.
	assertNotFired(t, ran, "startup trigger re-fired")

	if got := runs.Load(); got != 1 {
		t.Fatalf("startup trigger ran %d times, want 1", got)
	}
}

func TestSchedulerIntervalFirstFireAtOneHour(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	// Configure a 12h interval. First fire (never-completed) must be at
	// +1h, not +12h (IntervalTrigger.cs:44-48).
	intervalTicks := int64(12 * time.Hour / 100)
	ran := make(chan struct{}, 2)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}}

	if err := s.Register(tasks.Definition{
		ID:              "first-fire-00000000000000000000000000001",
		Key:             "first-fire",
		Name:            "First Fire Test",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerInterval, IntervalTicks: &intervalTicks}},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Advance 59m59s — should not fire (first fire is at +1h).
	c.Advance(time.Hour - time.Second)
	assertNotFired(t, ran, "interval fired before 1h")

	// Advance 1s — now at exactly +1h, should fire.
	c.Advance(time.Second)
	waitForRan(t, ran, "first interval fire at +1h")
}

func TestSchedulerIntervalFirstFireAfterUpdateBeforeCompletion(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	// A 12h interval. First fire (never-completed) must be at +1h regardless
	// of arming: UpdateTriggers before the first completion re-arms but the
	// task is still never-completed, so it fires at +1h, not now+interval
	// (IntervalTrigger.cs:44-48).
	intervalTicks := int64(12 * time.Hour / 100)
	ran := make(chan struct{}, 2)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}}

	if err := s.Register(tasks.Definition{
		ID:              "first-update-000000000000000000000000001",
		Key:             "first-update",
		Name:            "First Update Test",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerInterval, IntervalTicks: &intervalTicks}},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Re-arm via UpdateTriggers before any completion. The task is still
	// never-completed, so the next fire must be +1h.
	if err := s.UpdateTriggers("first-update-000000000000000000000000001", []tasks.TriggerInfo{
		{Type: tasks.TriggerInterval, IntervalTicks: &intervalTicks},
	}); err != nil {
		t.Fatalf("UpdateTriggers: %v", err)
	}

	// Advance 59m59s — should not fire (first fire is at +1h).
	c.Advance(time.Hour - time.Second)
	assertNotFired(t, ran, "interval fired before 1h after UpdateTriggers")

	// Advance 1s — now at exactly +1h, should fire.
	c.Advance(time.Second)
	waitForRan(t, ran, "first interval fire at +1h after UpdateTriggers")
}

func TestSchedulerManualRunHasNoMaxRuntime(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	// A task with a startup trigger carrying MaxRuntimeTicks. Manual Run
	// must not inherit the trigger's max runtime (spec §1: "Manual starts
	// have no max runtime").
	maxTicks := int64(5 * time.Second / 100)
	started := make(chan struct{}, 1)
	runnable := testRunnable{fn: func(ctx context.Context, _ func(float64)) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}}

	if err := s.Register(tasks.Definition{
		ID:   "manual-maxrt-000000000000000000000000001",
		Key:  "manual-maxrt",
		Name: "Manual Max Runtime Test",
		DefaultTriggers: []tasks.TriggerInfo{
			{Type: tasks.TriggerStartup, MaxRuntimeTicks: &maxTicks},
		},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Do NOT Start — no trigger fires. Run manually.
	if err := s.Run("manual-maxrt-000000000000000000000000001"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitForChan(t, started, time.Second, "manual run did not start")

	// Advance past the trigger's MaxRuntimeTicks. A manual run must not
	// be timed out.
	c.Advance(10 * time.Second)
	// The task should still be running — a manual run has no max-runtime
	// timer, so advancing the fake clock cannot cancel it (no wall-clock
	// sleep needed; the absence of a timer is deterministic).
	if !s.IsRunning("manual-maxrt-000000000000000000000000001") {
		t.Fatal("manual run was timed out by max runtime; should have no max runtime")
	}

	// Clean up: cancel the task.
	if err := s.Cancel("manual-maxrt-000000000000000000000000001"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitForNotRunning(t, s, "manual-maxrt-000000000000000000000000001", "manual run after Cancel")
}

func TestSchedulerDailyTriggerLocalTime(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("America/New_York not available")
	}

	// Start at 10:00 EST (winter, standard time).
	start := time.Date(2026, 1, 15, 10, 0, 0, 0, loc)
	c := clock.NewFake(start)
	configDir := filepath.Join(t.TempDir(), "config", "ScheduledTasks")
	dataDir := filepath.Join(t.TempDir(), "data", "ScheduledTasks")
	catalog := tasks.New(c)
	scheduler, err := tasks.NewScheduler(c, loc, catalog, configDir, dataDir)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })

	// Daily trigger at 14:00 (2 PM). Today's 14:00 hasn't happened yet, so
	// it should fire in 4 hours.
	timeOfDayTicks := int64(14 * time.Hour / 100) // 14:00 in .NET ticks (100ns each)
	ran := make(chan struct{}, 2)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}}

	if err := scheduler.Register(tasks.Definition{
		ID:              "daily-test-00000000000000000000000000001",
		Key:             "daily",
		Name:            "Daily Test",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerDaily, TimeOfDayTicks: &timeOfDayTicks}},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Advance 3h59m — should not fire.
	c.Advance(3*time.Hour + 59*time.Minute)
	assertNotFired(t, ran, "daily trigger fired before 14:00")

	// Advance 1m — now 14:00, should fire.
	c.Advance(time.Minute)
	waitForRan(t, ran, "daily trigger at 14:00")
	// Wait for the execution to complete and rearm before advancing, so the
	// next timer is armed at the rearmed instant (observable state, no
	// wall-clock sleep).
	waitForTaskIdle(t, scheduler, "daily-test-00000000000000000000000000001")

	// Verify the next fire is tomorrow 14:00 (24h later).
	c.Advance(23*time.Hour + 59*time.Minute)
	assertNotFired(t, ran, "daily trigger re-fired early")
	c.Advance(time.Minute)
	waitForRan(t, ran, "daily trigger next day")
}

func TestSchedulerDailyTriggerDSTTransition(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("America/New_York not available")
	}

	// 2026-03-07 is the day before DST (spring forward on March 8 at 2:00 AM).
	// A daily trigger at 2:30 AM on March 8 hits a skipped local time (2:00→3:00).
	// Go normalizes it forward to 3:30 AM EDT.
	start := time.Date(2026, 3, 7, 12, 0, 0, 0, loc)
	c := clock.NewFake(start)
	configDir := filepath.Join(t.TempDir(), "config", "ScheduledTasks")
	dataDir := filepath.Join(t.TempDir(), "data", "ScheduledTasks")
	catalog := tasks.New(c)
	scheduler, err := tasks.NewScheduler(c, loc, catalog, configDir, dataDir)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })

	timeOfDayTicks := int64((2*3600 + 30*60) * 1e7) // 02:30 in .NET ticks
	ran := make(chan struct{}, 2)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}}

	if err := scheduler.Register(tasks.Definition{
		ID:              "dst-test-00000000000000000000000000000001",
		Key:             "dst",
		Name:            "DST Test",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerDaily, TimeOfDayTicks: &timeOfDayTicks}},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// March 7 12:00 → March 8 02:30 is 14h30m, but DST skips 2:00–3:00,
	// so Go normalizes to 3:30. Advance to just past the target.
	// Advance 14h + 29m — not yet.
	c.Advance(14*time.Hour + 29*time.Minute)
	assertNotFired(t, ran, "DST daily fired too early")

	// Advance past the skipped hour. The delay should be computed to land
	// at 3:30 EDT (the normalized forward time), which is 14h30m + 1h from start
	// in wall-clock terms (because 2:00–3:00 is skipped). Total delay from
	// start is 15h30m.
	c.Advance(time.Hour + 30*time.Minute)
	waitForRan(t, ran, "DST daily through skipped hour")
}

// TestSchedulerDailyRearmSpringForwardDST proves the daily trigger rearms to
// the correct wall-clock time the day after a spring-forward transition. The
// bug (fixed) used a fixed 24 h addition, which on the day after spring
// forward lands one hour late in wall-clock terms. The fix re-computes the
// next calendar date via time.Date so the target midnight is in the new
// offset.
//
// Scenario: daily at 01:00 in America/New_York. Fire on Mar 8 01:00 EST
// (the day of spring forward — 01:00 is before the 02:00→03:00 transition).
// The rearm must fire on Mar 9 01:00 EDT (not 02:00 EDT, which a fixed 24 h
// from Mar 8 01:00 EST would produce).
func TestSchedulerDailyRearmSpringForwardDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("America/New_York not available")
	}

	// Mar 8 2026: spring forward at 02:00 EST → 03:00 EDT.
	// Start the clock at Mar 8 00:30 so today's 01:00 target is in the future.
	start := time.Date(2026, 3, 8, 0, 30, 0, 0, loc)
	c := clock.NewFake(start)
	configDir := filepath.Join(t.TempDir(), "config", "ScheduledTasks")
	dataDir := filepath.Join(t.TempDir(), "data", "ScheduledTasks")
	catalog := tasks.New(c)
	scheduler, err := tasks.NewScheduler(c, loc, catalog, configDir, dataDir)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })

	timeOfDayTicks := int64(1 * time.Hour / 100) // 01:00 in .NET ticks
	taskID := "daily-spring-000000000000000000000001"
	ran := make(chan struct{}, 4)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}}

	if err := scheduler.Register(tasks.Definition{
		ID:              taskID,
		Key:             "daily-spring",
		Name:            "Daily Spring Forward",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerDaily, TimeOfDayTicks: &timeOfDayTicks}},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First fire: Mar 8 01:00 EST (30 minutes from start).
	c.Advance(30 * time.Minute)
	waitForRan(t, ran, "daily spring first fire at 01:00 EST")
	// Wait for the execution to complete and rearm before advancing.
	waitForTaskIdle(t, scheduler, taskID)

	// The rearm must fire at Mar 9 01:00 EDT. From Mar 8 01:00 EST to Mar 9
	// 01:00 EDT is 23 h (spring forward loses one hour). A fixed 24 h would
	// land at Mar 9 02:00 EDT — one hour late.
	//
	// Advance 22h59m — not yet (would be 23h59m elapsed, but target is 23h).
	c.Advance(22*time.Hour + 59*time.Minute)
	assertNotFired(t, ran, "daily spring re-fired early before Mar 9 01:00 EDT")
	// Advance 1m — now at Mar 9 01:00 EDT (23h elapsed), should fire.
	c.Advance(time.Minute)
	waitForRan(t, ran, "daily spring rearm at Mar 9 01:00 EDT")
	// Wait for the execution to complete so LastExecutionResult reflects
	// the second fire, not the first.
	waitForTaskIdle(t, scheduler, taskID)

	// Verify the fire landed at the correct wall-clock time.
	info, ok := scheduler.Get(taskID)
	if !ok || info.LastExecutionResult == nil {
		t.Fatalf("no result for %s", taskID)
	}
	got := info.LastExecutionResult.StartTimeUTC.Time.In(loc)
	want := time.Date(2026, 3, 9, 1, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("spring-forward rearm fired at %v, want %v (wall-clock 01:00 EDT)", got, want)
	}
}

// TestSchedulerDailyRearmFallBackDST proves the daily trigger rearms to the
// correct wall-clock time the day after a fall-back transition. A fixed 24 h
// addition from a pre-transition target lands one hour early in wall-clock
// terms. The fix re-computes the next calendar date via time.Date.
//
// Scenario: daily at 03:00 in America/New_York. Fire on Nov 1 (fall-back day)
// — the 03:00 target is after the 02:00 EDT→01:00 EST transition, so it
// lands at 02:00 EST (3 h elapsed from midnight EDT, crossing the gap). The
// rearm must fire on Nov 2 03:00 EST (not 02:00 EST, which a fixed 24 h from
// Nov 1 02:00 EST would produce).
func TestSchedulerDailyRearmFallBackDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("America/New_York not available")
	}

	// Nov 1 2026: fall back at 02:00 EDT → 01:00 EST.
	// Start at Nov 1 00:30 so today's target (03:00 wall-clock) is future.
	// time.Date(2026,11,1,0,0,0,0,loc).Add(3h) = 07:00 UTC = 02:00 EST
	// (3 h elapsed from midnight EDT crosses the fall-back gap).
	start := time.Date(2026, 11, 1, 0, 30, 0, 0, loc)
	c := clock.NewFake(start)
	configDir := filepath.Join(t.TempDir(), "config", "ScheduledTasks")
	dataDir := filepath.Join(t.TempDir(), "data", "ScheduledTasks")
	catalog := tasks.New(c)
	scheduler, err := tasks.NewScheduler(c, loc, catalog, configDir, dataDir)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })

	timeOfDayTicks := int64(3 * time.Hour / 100) // 03:00 in .NET ticks
	taskID := "daily-fallback-000000000000000000001"
	ran := make(chan struct{}, 4)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}}

	if err := scheduler.Register(tasks.Definition{
		ID:              taskID,
		Key:             "daily-fallback",
		Name:            "Daily Fall Back",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerDaily, TimeOfDayTicks: &timeOfDayTicks}},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First fire: Nov 1 02:00 EST (the 03:00 target, normalized through the
	// fall-back gap). From 00:30 EDT to 02:00 EST is 2h30m elapsed (crossing
	// the fall-back transition at 02:00 EDT → 01:00 EST).
	c.Advance(2*time.Hour + 30*time.Minute)
	waitForRan(t, ran, "daily fall-back first fire at 02:00 EST")
	waitForTaskIdle(t, scheduler, taskID)

	// The rearm must fire at Nov 2 03:00 EST. From Nov 1 02:00 EST to Nov 2
	// 03:00 EST is 25 h (fall-back day is 25 h). A fixed 24 h would land at
	// Nov 2 02:00 EST — one hour early.
	//
	// Advance 24h59m — not yet (would be 24h59m elapsed, target is 25h).
	c.Advance(24*time.Hour + 59*time.Minute)
	assertNotFired(t, ran, "daily fall-back re-fired early before Nov 2 03:00 EST")
	// Advance 1m — now at Nov 2 03:00 EST (25h elapsed), should fire.
	c.Advance(time.Minute)
	waitForRan(t, ran, "daily fall-back rearm at Nov 2 03:00 EST")
	// Wait for the execution to complete so LastExecutionResult reflects
	// the second fire, not the first.
	waitForTaskIdle(t, scheduler, taskID)

	// Verify the fire landed at the correct wall-clock time.
	info, ok := scheduler.Get(taskID)
	if !ok || info.LastExecutionResult == nil {
		t.Fatalf("no result for %s", taskID)
	}
	got := info.LastExecutionResult.StartTimeUTC.Time.In(loc)
	want := time.Date(2026, 11, 2, 3, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("fall-back rearm fired at %v, want %v (wall-clock 03:00 EST)", got, want)
	}
}

// TestSchedulerWeeklyRearmSpringForwardDST proves the weekly trigger rearms to
// the correct wall-clock time across a spring-forward DST transition. A fixed
// 7×24 h addition from a pre-transition Friday lands one hour late.
//
// Scenario: weekly on Friday at 01:00 in America/New_York. Fire on Friday
// Mar 6 01:00 EST. The rearm must fire on Friday Mar 13 01:00 EDT (not
// 02:00 EDT, which a fixed 7×24 h from Mar 6 01:00 EST would produce).
func TestSchedulerWeeklyRearmSpringForwardDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("America/New_York not available")
	}

	// Mar 6 2026 is Friday. Spring forward is Mar 8 (Sunday).
	// Start at Mar 6 00:30 so today's 01:00 target is in the future.
	start := time.Date(2026, 3, 6, 0, 30, 0, 0, loc)
	c := clock.NewFake(start)
	configDir := filepath.Join(t.TempDir(), "config", "ScheduledTasks")
	dataDir := filepath.Join(t.TempDir(), "data", "ScheduledTasks")
	catalog := tasks.New(c)
	scheduler, err := tasks.NewScheduler(c, loc, catalog, configDir, dataDir)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })

	timeOfDayTicks := int64(1 * time.Hour / 100) // 01:00 in .NET ticks
	taskID := "weekly-spring-000000000000000000001"
	ran := make(chan struct{}, 4)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}}

	if err := scheduler.Register(tasks.Definition{
		ID:              taskID,
		Key:             "weekly-spring",
		Name:            "Weekly Spring Forward",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerWeekly, DayOfWeek: ptrString("Friday"), TimeOfDayTicks: &timeOfDayTicks}},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First fire: Mar 6 01:00 EST (30 minutes from start).
	c.Advance(30 * time.Minute)
	waitForRan(t, ran, "weekly spring first fire at Mar 6 01:00 EST")
	waitForTaskIdle(t, scheduler, taskID)

	// The rearm must fire at Mar 13 01:00 EDT. From Mar 6 01:00 EST to Mar 13
	// 01:00 EDT is 7 days minus 1 hour (spring forward) = 167 h. A fixed
	// 7×24 h = 168 h would land at Mar 13 02:00 EDT — one hour late.
	//
	// Advance 166h59m — not yet.
	c.Advance(166*time.Hour + 59*time.Minute)
	assertNotFired(t, ran, "weekly spring re-fired early before Mar 13 01:00 EDT")
	// Advance 1m — now at Mar 13 01:00 EDT (167h elapsed), should fire.
	c.Advance(time.Minute)
	waitForRan(t, ran, "weekly spring rearm at Mar 13 01:00 EDT")
	// Wait for the execution to complete so LastExecutionResult reflects
	// the second fire, not the first.
	waitForTaskIdle(t, scheduler, taskID)

	// Verify the fire landed at the correct wall-clock time.
	info, ok := scheduler.Get(taskID)
	if !ok || info.LastExecutionResult == nil {
		t.Fatalf("no result for %s", taskID)
	}
	got := info.LastExecutionResult.StartTimeUTC.Time.In(loc)
	want := time.Date(2026, 3, 13, 1, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("spring-forward weekly rearm fired at %v, want %v (wall-clock 01:00 EDT)", got, want)
	}
}

// TestSchedulerWeeklyRearmFallBackDST proves the weekly trigger rearms to the
// correct wall-clock time across a fall-back DST transition. A fixed 7×24 h
// addition from a pre-transition Friday lands one hour early.
//
// Scenario: weekly on Friday at 03:00 in America/New_York. Fire on Friday
// Oct 30 03:00 EDT. The rearm must fire on Friday Nov 6 03:00 EST (not
// 02:00 EST, which a fixed 7×24 h from Oct 30 03:00 EDT would produce).
func TestSchedulerWeeklyRearmFallBackDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("America/New_York not available")
	}

	// Oct 30 2026 is Friday. Fall back is Nov 1 (Sunday).
	// Start at Oct 30 02:30 so today's 03:00 target is in the future.
	start := time.Date(2026, 10, 30, 2, 30, 0, 0, loc)
	c := clock.NewFake(start)
	configDir := filepath.Join(t.TempDir(), "config", "ScheduledTasks")
	dataDir := filepath.Join(t.TempDir(), "data", "ScheduledTasks")
	catalog := tasks.New(c)
	scheduler, err := tasks.NewScheduler(c, loc, catalog, configDir, dataDir)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })

	timeOfDayTicks := int64(3 * time.Hour / 100) // 03:00 in .NET ticks
	taskID := "weekly-fallback-0000000000000000001"
	ran := make(chan struct{}, 4)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}}

	if err := scheduler.Register(tasks.Definition{
		ID:              taskID,
		Key:             "weekly-fallback",
		Name:            "Weekly Fall Back",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerWeekly, DayOfWeek: ptrString("Friday"), TimeOfDayTicks: &timeOfDayTicks}},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First fire: Oct 30 03:00 EDT (30 minutes from start).
	c.Advance(30 * time.Minute)
	waitForRan(t, ran, "weekly fall-back first fire at Oct 30 03:00 EDT")
	waitForTaskIdle(t, scheduler, taskID)

	// The rearm must fire at Nov 6 03:00 EST. From Oct 30 03:00 EDT to Nov 6
	// 03:00 EST is 7 days plus 1 hour (fall back) = 169 h. A fixed 7×24 h =
	// 168 h would land at Nov 6 02:00 EST — one hour early.
	//
	// Advance 168h59m — not yet (target is 169h).
	c.Advance(168*time.Hour + 59*time.Minute)
	assertNotFired(t, ran, "weekly fall-back re-fired early before Nov 6 03:00 EST")
	// Advance 1m — now at Nov 6 03:00 EST (169h elapsed), should fire.
	c.Advance(time.Minute)
	waitForRan(t, ran, "weekly fall-back rearm at Nov 6 03:00 EST")
	// Wait for the execution to complete so LastExecutionResult reflects
	// the second fire, not the first.
	waitForTaskIdle(t, scheduler, taskID)

	// Verify the fire landed at the correct wall-clock time.
	info, ok := scheduler.Get(taskID)
	if !ok || info.LastExecutionResult == nil {
		t.Fatalf("no result for %s", taskID)
	}
	got := info.LastExecutionResult.StartTimeUTC.Time.In(loc)
	want := time.Date(2026, 11, 6, 3, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("fall-back weekly rearm fired at %v, want %v (wall-clock 03:00 EST)", got, want)
	}
}

func TestSchedulerIntervalReschedulesFromCompletion(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	// 1-hour interval in .NET ticks (time.Duration / 100ns).
	intervalTicks := int64(time.Hour / 100)
	ran := make(chan struct{}, 10)
	release := make(chan struct{})
	runnable := testRunnable{fn: func(ctx context.Context, _ func(float64)) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		<-release
		return nil
	}}

	if err := s.Register(tasks.Definition{
		ID:              "interval-test-00000000000000000000000000001",
		Key:             "interval",
		Name:            "Interval Test",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerInterval, IntervalTicks: &intervalTicks}},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First run: never-completed → now + 1h.
	c.Advance(time.Hour)
	waitForRan(t, ran, "first interval run")

	// Release the first run so it completes and rearms.
	close(release)
	waitForTaskIdle(t, s, "interval-test-00000000000000000000000000001")

	// Next run: max(end, armTime, now+1m) + 1h interval = T+1h+1m. The
	// now+1m minimum (IntervalTrigger.cs:51) shifts the fire 1m past
	c.Advance(time.Hour)
	assertNotFired(t, ran, "interval re-fired too early")

	// Advance 1m — now at completion+1h+1m, should fire.
	c.Advance(time.Minute)
	waitForRan(t, ran, "interval reschedule from completion")
}

func TestSchedulerIntervalSevenDayCap(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// Set a large interval (30 days) and a last result far in the past.
	// The cap should limit to 7 days.
	intervalTicks := int64(30 * 24 * time.Hour / 100) // 30 days in .NET ticks
	// Use a 32-hex ID so dashedID produces the dashed GUID filename.
	taskID := "cafe1234000000000000000000000001"
	ran := make(chan struct{}, 2)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}}

	// Pre-seed a last result 10 days ago via the result file.
	dataDir := filepath.Join(t.TempDir(), "data", "ScheduledTasks")
	configDir := filepath.Join(t.TempDir(), "config", "ScheduledTasks")
	catalog := tasks.New(c)
	scheduler, err := tasks.NewScheduler(c, time.UTC, catalog, configDir, dataDir)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close(context.Background()) })
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })
	// Seed a last result 10 days ago so the else branch (post-completion
	// rearm with 7-day cap) is taken, not the never-completed +1h branch.
	pastEnd := time.Date(2025, 12, 22, 0, 0, 0, 0, time.UTC)
	pastResult := tasks.Result{
		EndTimeUTC: dto.NewTime(pastEnd),
		Status:     "Completed",
		Name:       "Cap Test",
		Key:        "cap",
		ID:         taskID,
	}
	pastResultJSON, _ := json.Marshal(pastResult)
	resultPath := filepath.Join(dataDir, "cafe1234-0000-0000-0000-000000000001.js")
	_ = os.MkdirAll(dataDir, 0o700)
	_ = os.WriteFile(resultPath, pastResultJSON, 0o600)

	if err := scheduler.Register(tasks.Definition{
		ID:              taskID,
		Key:             "cap",
		Name:            "Cap Test",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerInterval, IntervalTicks: &intervalTicks}},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The cap is 7 days. Advance 6d23h59m — should not fire.
	c.Advance(6*24*time.Hour + 23*time.Hour + 59*time.Minute)
	assertNotFired(t, ran, "interval fired before 7-day cap")

	// Advance 1m — now 7 days, should fire.
	c.Advance(time.Minute)
	waitForRan(t, ran, "interval at 7-day cap")
}

func TestSchedulerCoalescesTriggerWhileRunning(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	var runs atomic.Int32
	release := make(chan struct{})
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		runs.Add(1)
		<-release
		return nil
	}}

	taskID := "coalesce-test-0000000000000000000000000001"
	if err := s.Register(tasks.Definition{
		ID:   taskID,
		Key:  "coalesce",
		Name: "Coalesce Test",
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Manual start — the task is now running and blocked on release.
	if err := s.Run(taskID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Deterministically wait for the goroutine to start (observable counter,
	// no wall-clock sleep).
	waitForRuns(t, &runs, 1, "after first Run")

	// A second Run while running coalesces — sets pending.
	if err := s.Run(taskID); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	// Release the first run. It should re-execute once.
	close(release)
	// Deterministically wait for the coalesced re-execution to start
	// (observable counter, no wall-clock sleep).
	waitForRuns(t, &runs, 2, "after release coalesced re-execution")
}

func TestSchedulerMaxRuntimeAborts(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	started := make(chan struct{}, 1)
	var cancelled atomic.Int32
	runnable := testRunnable{fn: func(ctx context.Context, _ func(float64)) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		cancelled.Add(1)
		return ctx.Err()
	}}

	// MaxRuntimeTicks = 5 seconds.
	maxTicks := int64(5 * time.Second / 100) // 5 seconds in .NET ticks
	if err := s.Register(tasks.Definition{
		ID:   "maxrt-test-0000000000000000000000000000001",
		Key:  "maxrt",
		Name: "Max Runtime Test",
		DefaultTriggers: []tasks.TriggerInfo{
			{Type: tasks.TriggerStartup, MaxRuntimeTicks: &maxTicks},
		},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Startup fires at 3s; max runtime at 3s + 5s = 8s.
	c.Advance(3 * time.Second)
	waitForChan(t, started, time.Second, "task did not start at 3s")

	// Advance 4s — max runtime not yet hit (at 8s).
	c.Advance(4 * time.Second)
	if cancelled.Load() != 0 {
		t.Fatal("task was cancelled before max runtime")
	}

	// Advance 1s — max runtime fires (at 8s).
	c.Advance(time.Second)
	// Deterministically wait for the execution to be cancelled and the
	// Aborted result to be recorded (observable scheduler state, no
	// wall-clock sleep).
	waitForResultStatus(t, s, "maxrt-test-0000000000000000000000000000001", tasks.StatusAborted)
}

// TestSchedulerMaxRuntimeProvenance proves a triggered execution records
// Aborted when its firing trigger's MaxRuntimeTicks expires, and that the
// trigger provenance is retained across the start→execute boundary.
func TestSchedulerMaxRuntimeProvenance(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	maxTicks := int64(5 * time.Second / 100)
	started := make(chan struct{}, 1)
	var cancelled atomic.Int32
	runnable := testRunnable{fn: func(ctx context.Context, _ func(float64)) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		cancelled.Add(1)
		return ctx.Err()
	}}

	if err := s.Register(tasks.Definition{
		ID:   "maxrt-prov-00000000000000000000000000001",
		Key:  "maxrt-prov",
		Name: "Max Runtime Provenance Test",
		DefaultTriggers: []tasks.TriggerInfo{
			{Type: tasks.TriggerStartup, MaxRuntimeTicks: &maxTicks},
		},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Startup fires at 3s; max runtime at 3s + 5s = 8s.
	c.Advance(3 * time.Second)
	waitForChan(t, started, time.Second, "task did not start at 3s")

	// Advance past max runtime; the firing trigger's limit aborts the run.
	c.Advance(6 * time.Second)
	waitForResultStatus(t, s, "maxrt-prov-00000000000000000000000000001", tasks.StatusAborted)
}

// TestSchedulerMaxRuntimePendingTrigger proves a coalesced triggered
// execution inherits its firing trigger's MaxRuntimeTicks: a long manual run
// holds the task; an interval trigger with a short max-runtime fires during
// the run (coalesces); when the manual run completes, the pending re-execution
// uses the trigger's limit and is Aborted. An explicit barrier
// (waitForPendingTrigger) proves the coalesce is committed before the manual
// run is released, because Fake.Advance only queues the timer delivery.
func TestSchedulerMaxRuntimePendingTrigger(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	maxTicks := int64(3 * time.Second / 100)
	intervalTicks := int64(time.Hour / 100) // fires at +1h (never-completed)
	manualStarted := make(chan struct{}, 1)
	pendingStarted := make(chan struct{}, 1)
	var runIndex atomic.Int32 // 0 = manual run, 1 = pending triggered run
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseManual := func() { releaseOnce.Do(func() { close(release) }) }
	// Registered after newTestScheduler so LIFO cleanup releases the manual
	// run before scheduler.Close; a failed assertion cannot park execute on
	// <-release and hang Close.
	t.Cleanup(releaseManual)
	runnable := testRunnable{fn: func(ctx context.Context, _ func(float64)) error {
		// Distinguish the manual run (first) from the pending triggered run
		// using an atomic counter (no closure-variable data race).
		if runIndex.Add(1) == 1 {
			select {
			case manualStarted <- struct{}{}:
			default:
			}
			<-release
			return nil
		}
		select {
		case pendingStarted <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}}

	if err := s.Register(tasks.Definition{
		ID:   "maxrt-pend-00000000000000000000000000001",
		Key:  "maxrt-pend",
		Name: "Max Runtime Pending Trigger Test",
		DefaultTriggers: []tasks.TriggerInfo{
			{Type: tasks.TriggerInterval, IntervalTicks: &intervalTicks, MaxRuntimeTicks: &maxTicks},
		},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Manual Run starts immediately and holds (no max runtime).
	if err := s.Run("maxrt-pend-00000000000000000000000000001"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitForChan(t, manualStarted, time.Second, "manual run did not start")

	// Advance 1h: the interval trigger fires, coalescing into the pending run
	// and retaining its firing trigger (with the 3s max-runtime).
	c.Advance(time.Hour)

	// Barrier: the armWorker must commit the coalesce (startLocked sets
	// pending and retains the firing trigger) before the manual run is
	// released. Fake.Advance only queues the timer delivery; without this
	// wait execute's armLocked can retire the timer first and armWorker
	// correctly rejects the stale arm, so no pending run exists.
	waitForPendingTrigger(t, s, "maxrt-pend-00000000000000000000000000001", maxTicks, "interval trigger did not coalesce into a pending run")

	// Release the manual run; the pending triggered re-execution starts.
	releaseManual()
	waitForChan(t, pendingStarted, time.Second, "pending triggered run did not start")

	// Advance past the pending run's 3s max-runtime; it must abort.
	c.Advance(4 * time.Second)
	waitForResultStatus(t, s, "maxrt-pend-00000000000000000000000000001", tasks.StatusAborted)
}

// TestSchedulerMaxRuntimeDifferentTrigger proves two triggers with different
// max-runtimes apply their own limits: the trigger that actually fires
// controls the execution, not a stale or unrelated trigger.
func TestSchedulerMaxRuntimeDifferentTrigger(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	shortMax := int64(2 * time.Second / 100)
	longMax := int64(10 * time.Second / 100)
	started := make(chan struct{}, 2)
	var cancelled atomic.Int32
	runnable := testRunnable{fn: func(ctx context.Context, _ func(float64)) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		cancelled.Add(1)
		return ctx.Err()
	}}

	// Two startup triggers: the short one fires first (3s), the long one is a
	// fallback. The firing (short) trigger's 2s limit must apply.
	if err := s.Register(tasks.Definition{
		ID:   "maxrt-diff-00000000000000000000000000001",
		Key:  "maxrt-diff",
		Name: "Max Runtime Different Trigger Test",
		DefaultTriggers: []tasks.TriggerInfo{
			{Type: tasks.TriggerStartup, MaxRuntimeTicks: &shortMax},
			{Type: tasks.TriggerStartup, MaxRuntimeTicks: &longMax},
		},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	c.Advance(3 * time.Second)
	waitForChan(t, started, time.Second, "task did not start at 3s")

	// The short trigger's 2s limit fires at 5s; the long limit would be 13s.
	c.Advance(3 * time.Second)
	waitForResultStatus(t, s, "maxrt-diff-00000000000000000000000000001", tasks.StatusAborted)
}

func TestSchedulerTriggerPersistenceRoundTrip(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, configDir, _ := newTestScheduler(t, c)

	taskID := refreshLibraryID
	if err := tasks.RegisterBuiltins(s, tasks.BuiltinDeps{}); err != nil {
		t.Fatalf("RegisterBuiltins: %v", err)
	}

	// UpdateTriggers → file should exist.
	customTicks := int64(3 * 36 * 1e8) // 3 hours
	newTriggers := []tasks.TriggerInfo{
		{Type: tasks.TriggerInterval, IntervalTicks: &customTicks},
	}
	if err := s.UpdateTriggers(taskID, newTriggers); err != nil {
		t.Fatalf("UpdateTriggers: %v", err)
	}

	triggerFile := filepath.Join(configDir, "7738148f-fcd0-7979-c7ce-b148e06b3aed.js")
	body, err := os.ReadFile(triggerFile)
	if err != nil {
		t.Fatalf("trigger file not found: %v", err)
	}

	// Verify it's PascalCase JSON.
	var raw []map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal trigger file: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("trigger file has %d entries, want 1", len(raw))
	}
	if raw[0]["Type"] != "IntervalTrigger" {
		t.Errorf("trigger Type = %v, want IntervalTrigger", raw[0]["Type"])
	}
	if _, has := raw[0]["IntervalTicks"]; !has {
		t.Error("trigger file missing IntervalTicks (PascalCase)")
	}

	// New scheduler over same dirs should load the overrides.
	c2 := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	catalog2 := tasks.New(c2)
	s2, err := tasks.NewScheduler(c2, time.UTC, catalog2, configDir, filepath.Join(t.TempDir(), "data", "ScheduledTasks"))
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close(context.Background()) })
	t.Cleanup(func() { _ = catalog2.Close(context.Background()) })

	_ = tasks.RegisterBuiltins(s2, tasks.BuiltinDeps{})
	info, ok := s2.Get(taskID)
	if !ok {
		t.Fatal("task not found in new scheduler")
	}
	if len(info.Triggers) != 1 || info.Triggers[0].Type != tasks.TriggerInterval {
		t.Fatalf("loaded triggers = %v, want one IntervalTrigger", info.Triggers)
	}
	if info.Triggers[0].IntervalTicks == nil || *info.Triggers[0].IntervalTicks != customTicks {
		t.Fatalf("loaded IntervalTicks = %v, want %d", info.Triggers[0].IntervalTicks, customTicks)
	}

	// Corrupt file → defaults, no error.
	corrupt := []byte("{not valid json")
	if err := os.WriteFile(triggerFile, corrupt, 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	c3 := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	catalog3 := tasks.New(c3)
	s3, err := tasks.NewScheduler(c3, time.UTC, catalog3, configDir, filepath.Join(t.TempDir(), "data2", "ScheduledTasks"))
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	t.Cleanup(func() { _ = s3.Close(context.Background()) })
	t.Cleanup(func() { _ = catalog3.Close(context.Background()) })

	if err := tasks.RegisterBuiltins(s3, tasks.BuiltinDeps{}); err != nil {
		t.Fatalf("RegisterBuiltins with corrupt trigger: %v", err)
	}
	info3, ok := s3.Get(taskID)
	if !ok {
		t.Fatal("task not found after corrupt load")
	}
	// Should fall back to default (12h interval).
	if len(info3.Triggers) != 1 || info3.Triggers[0].Type != tasks.TriggerInterval {
		t.Fatalf("corrupt fallback triggers = %v, want default IntervalTrigger", info3.Triggers)
	}
}

func TestSchedulerCancelIsNoOpWhenIdle(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)
	_ = tasks.RegisterBuiltins(s, tasks.BuiltinDeps{})

	if err := s.Cancel(refreshLibraryID); err != nil {
		t.Fatalf("Cancel on idle task: %v", err)
	}
}

func TestSchedulerRunUnknownTask(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)
	_ = tasks.RegisterBuiltins(s, tasks.BuiltinDeps{})

	if err := s.Run("nonexistent"); !errors.Is(err, tasks.ErrTaskNotFound) {
		t.Fatalf("Run unknown = %v, want ErrTaskNotFound", err)
	}
}

// TestSchedulerCloseRejectsAndDrainsConcurrentStart proves Close linearizes
// against concurrent Run: no execution starts after Close returns, no worker
// is left after return, and every start linearized after shutdown returns
// ErrSchedulerClosed.
func TestSchedulerCloseRejectsAndDrainsConcurrentStart(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	block := make(chan struct{})
	started := make(chan struct{}, 100)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-block
		return nil
	}}
	if err := s.Register(tasks.Definition{
		ID:  "close-lin-0000000000000000000000000000001",
		Key: "close-lin", Name: "Close Linearization Test",
	}, runnable); err != nil {
		t.Fatal(err)
	}
	// Start a long run that blocks on `block`.
	if err := s.Run("close-lin-0000000000000000000000000000001"); err != nil {
		t.Fatal(err)
	}
	waitForChan(t, started, time.Second, "run did not start")

	// Concurrently: close (cancels the running execution) and try more starts.
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- s.Close(context.Background())
	}()

	// Every start after Close must eventually return ErrSchedulerClosed; some
	// may coalesce into the running execution before Close observes them.
	for range 10 {
		_ = s.Run("close-lin-0000000000000000000000000000001")
	}

	// Let the blocked execution finish (Close cancelled its context).
	close(block)

	err := <-closeDone
	if err != nil {
		t.Fatalf("Close returned %v, want nil", err)
	}

	// After Close returns, no new execution can start.
	if err := s.Run("close-lin-0000000000000000000000000000001"); !errors.Is(err, tasks.ErrSchedulerClosed) {
		t.Fatalf("Run after Close = %v, want ErrSchedulerClosed", err)
	}
	if s.IsRunning("close-lin-0000000000000000000000000000001") {
		t.Error("task still running after Close returned")
	}
}

// TestSchedulerCloseReturnsContextErrorWhileWorkerRemains proves that a
// scheduler shutdown with a canceled context returns the context error while
// a worker remains. The fake clock does not advance, so the deadline loop
// removed earlier would have kept the yield loop active indefinitely.
// After the worker drains, a repeated Close returns nil.
func TestSchedulerCloseReturnsContextErrorWhileWorkerRemains(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	block := make(chan struct{})
	started := make(chan struct{}, 1)
	runnable := testRunnable{fn: func(_ context.Context, _ func(float64)) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-block
		return nil
	}}
	if err := s.Register(tasks.Definition{
		ID:  "close-deadline-0000000000000000000000000001",
		Key: "close-deadline", Name: "Close Deadline Test",
	}, runnable); err != nil {
		t.Fatal(err)
	}
	// Start a long run that blocks on `block`. The fake clock does not
	// advance, so the worker cannot drain until `block` is closed.
	if err := s.Run("close-deadline-0000000000000000000000000001"); err != nil {
		t.Fatal(err)
	}
	waitForChan(t, started, time.Second, "run did not start")

	// Close with a canceled context while the worker remains. The
	// deadline loop is gone, so Close returns the context error
	// immediately instead of spinning on the fixed fake clock.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Close = %v, want context.Canceled", err)
	}

	// The worker is still alive because `block` has not been closed.
	if !s.IsRunning("close-deadline-0000000000000000000000000001") {
		t.Fatal("task not running while worker remains")
	}

	// Release the worker. doneWorker closes the drain channel after
	// the last worker exits. The drain channel already supplies the
	// completion signal, so no clock advancement is needed.
	close(block)

	// After the worker drains, a repeated Close returns nil. The
	// second Close calls waitForDrain, which blocks on the drain
	// channel until doneWorker closes it.
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}

// TestBuiltinRetentionCutoffsUseInjectedClock proves every backend receives the
// exact cutoff derived from the injected fake clock (spec §3: all timing
// through clock.Clock).
func TestBuiltinRetentionCutoffsUseInjectedClock(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	logDir := t.TempDir()
	cacheDir := t.TempDir()
	transcodeDir := filepath.Join(cacheDir, "transcodes")
	if err := os.MkdirAll(transcodeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var pruneBefore, transcodeBefore time.Time
	pruner := testPruner{fn: func(_ context.Context, before time.Time) (int, error) {
		pruneBefore = before
		return 0, nil
	}}
	cleaner := testCleaner{fn: func(_ context.Context, olderThan time.Time) error {
		transcodeBefore = olderThan
		return nil
	}}
	deps := tasks.BuiltinDeps{
		ActivityLogPruner: pruner,
		TranscodeCleaner:  cleaner,
		Clock:             c,
		LogPath:           logDir,
		CachePath:         cacheDir,
		TranscodePath:     transcodeDir,
	}
	if err := tasks.RegisterBuiltins(s, deps); err != nil {
		t.Fatal(err)
	}

	// Seed files immediately before and after each fake-clock-derived
	// boundary so the test proves the exact cutoff is applied for all
	// four retention tasks. The fake clock is at 2026-01-01 00:00 UTC.
	//   DeleteLogFiles   (3-day):  cutoff 2025-12-29
	//   DeleteCacheFiles (30-day): cutoff 2025-12-02
	logCutoff := c.Now().AddDate(0, 0, -3)
	cacheCutoff := c.Now().AddDate(0, 0, -30)
	seedOldFileAt(t, logDir, "old.log", logCutoff.Add(-time.Second))   // before cutoff → deleted
	seedOldFileAt(t, logDir, "recent.log", logCutoff.Add(time.Second)) // after cutoff → survives
	seedOldFileAt(t, cacheDir, "old.cache", cacheCutoff.Add(-time.Second))
	seedOldFileAt(t, cacheDir, "recent.cache", cacheCutoff.Add(time.Second))

	// CleanActivityLog (30-day retention) — pruner records the clock-derived cutoff.
	if err := s.Run(tasks.CleanActivityLogID); err != nil {
		t.Fatal(err)
	}
	waitForTaskIdle(t, s, tasks.CleanActivityLogID)
	if !pruneBefore.Equal(c.Now().AddDate(0, 0, -30)) {
		t.Errorf("CleanActivityLog cutoff = %v, want %v (clock-derived)", pruneBefore, c.Now().AddDate(0, 0, -30))
	}

	// DeleteLogFiles (3-day retention) — fsclean deletes only files before the cutoff.
	if err := s.Run(tasks.DeleteLogFilesID); err != nil {
		t.Fatal(err)
	}
	waitForTaskIdle(t, s, tasks.DeleteLogFilesID)
	if _, err := os.Stat(filepath.Join(logDir, "old.log")); !os.IsNotExist(err) {
		t.Error("DeleteLogFiles did not delete old.log (mtime before 3-day cutoff)")
	}
	if _, err := os.Stat(filepath.Join(logDir, "recent.log")); err != nil {
		t.Error("DeleteLogFiles deleted recent.log (mtime after 3-day cutoff)")
	}

	// DeleteCacheFiles (30-day retention) — fsclean deletes only files before the cutoff.
	if err := s.Run(tasks.DeleteCacheFilesID); err != nil {
		t.Fatal(err)
	}
	waitForTaskIdle(t, s, tasks.DeleteCacheFilesID)
	if _, err := os.Stat(filepath.Join(cacheDir, "old.cache")); !os.IsNotExist(err) {
		t.Error("DeleteCacheFiles did not delete old.cache (mtime before 30-day cutoff)")
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "recent.cache")); err != nil {
		t.Error("DeleteCacheFiles deleted recent.cache (mtime after 30-day cutoff)")
	}

	// DeleteTranscodeFiles (1-day retention) — cleaner records the clock-derived cutoff.
	if err := s.Run(tasks.DeleteTranscodeFilesID); err != nil {
		t.Fatal(err)
	}
	waitForTaskIdle(t, s, tasks.DeleteTranscodeFilesID)
	if !transcodeBefore.Equal(c.Now().AddDate(0, 0, -1)) {
		t.Errorf("DeleteTranscodeFiles cutoff = %v, want %v (clock-derived)", transcodeBefore, c.Now().AddDate(0, 0, -1))
	}
}

// testRunnable implements Runnable for tests.
type testRunnable struct {
	fn func(ctx context.Context, progress func(float64)) error
}

func (r testRunnable) Run(ctx context.Context, progress func(float64)) error {
	return r.fn(ctx, progress)
}

func waitForRan(t *testing.T, ran chan struct{}, msg string) {
	t.Helper()
	waitForChan(t, ran, 2*time.Second, msg+" did not fire")
}

// waitForChan waits for a signal on ch using a blocking select backed by a
// time.NewTimer deadline. The blocking select parks the goroutine so the
// scheduler's worker goroutine (which fires on the fake clock's Advance)
// is actually scheduled and can signal ch. The timer is a pure deadlock
// guard: it never fires in the passing case because the signal arrives as
// soon as the worker goroutine runs. No wall-clock behavior wait is used
// (no After channel or Sleep); time.NewTimer is the deadline primitive,
// not a behavior wait.
func waitForChan(t *testing.T, ch <-chan struct{}, timeout time.Duration, msg string) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
		t.Fatalf("%s", msg)
	}
}

// assertNotFired deterministically confirms a signal channel is empty (the
// expected case for a negative assertion under a fake clock). After Advance,
// all due fake timers fire synchronously; if the trigger should not have
// fired, the channel is empty. A few gosched yields let any timer goroutine
// that might have been scheduled run, then the empty channel is proof. No
// wall-clock sleep is needed.
func assertNotFired(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	for range 8 {
		runtime.Gosched()
		select {
		case <-ch:
			t.Fatalf("%s", msg)
		default:
		}
	}
}

// waitForResultStatus deterministically waits for a task's last execution
// result to reach the expected status, polling observable scheduler state
// with a bounded gosched loop (no wall-clock sleeps).
func waitForResultStatus(t *testing.T, s *tasks.Scheduler, id, want string) {
	t.Helper()
	deadline := realClock.Now().Add(5 * time.Second)
	for {
		info, ok := s.Get(id)
		if ok && info.LastExecutionResult != nil && string(info.LastExecutionResult.Status) == want {
			return
		}
		if realClock.Now().After(deadline) {
			info, _ := s.Get(id)
			got := "nil"
			if info.LastExecutionResult != nil {
				got = string(info.LastExecutionResult.Status)
			}
			t.Fatalf("task %s result status = %s, want %q (state=%v)", id, got, want, info.State)
		}
		runtime.Gosched()
	}
}

// waitForRuns deterministically waits for an atomic counter to reach the
// expected value, polling with a bounded gosched loop (no wall-clock sleeps).
func waitForRuns(t *testing.T, runs *atomic.Int32, want int32, msg string) {
	t.Helper()
	deadline := realClock.Now().Add(5 * time.Second)
	for {
		if runs.Load() == want {
			return
		}
		if realClock.Now().After(deadline) {
			t.Fatalf("%s: runs = %d, want %d", msg, runs.Load(), want)
		}
		runtime.Gosched()
	}
}

// waitForRunning deterministically confirms a task is running (or times out
// if it unexpectedly stopped), polling observable scheduler state with a
// bounded gosched loop (no wall-clock sleeps).
func waitForRunning(t *testing.T, s *tasks.Scheduler, id, msg string) {
	t.Helper()
	deadline := realClock.Now().Add(2 * time.Second)
	for {
		if s.IsRunning(id) {
			return
		}
		if realClock.Now().After(deadline) {
			t.Fatalf("%s: task %s is not running", msg, id)
		}
		runtime.Gosched()
	}
}

// waitForPendingTrigger blocks until the scheduler has committed a coalesced
// pending run for id whose retained trigger carries wantMaxTicks. It polls
// committed state under the scheduler's locks with a bounded gosched loop
// (no wall-clock sleeps) and fails the test at the deadline. A nil trigger
// means a manual coalesce and is a provenance failure, not a timing one.
func waitForPendingTrigger(t *testing.T, s *tasks.Scheduler, id string, wantMaxTicks int64, msg string) {
	t.Helper()
	deadline := realClock.Now().Add(2 * time.Second)
	for {
		pending, trigger := s.PendingTriggerForTest(id)
		if pending {
			if trigger == nil || trigger.MaxRuntimeTicks == nil {
				t.Fatalf("%s: pending run retained trigger %+v, want MaxRuntimeTicks=%d", msg, trigger, wantMaxTicks)
			}
			if *trigger.MaxRuntimeTicks != wantMaxTicks {
				t.Fatalf("%s: pending trigger MaxRuntimeTicks = %d, want %d", msg, *trigger.MaxRuntimeTicks, wantMaxTicks)
			}
			return
		}
		if realClock.Now().After(deadline) {
			t.Fatalf("%s", msg)
		}
		runtime.Gosched()
	}
}

// waitForNotRunning deterministically confirms a task is no longer running,
// polling observable scheduler state with a bounded gosched loop (no
// wall-clock sleeps).
func waitForNotRunning(t *testing.T, s *tasks.Scheduler, id, msg string) {
	t.Helper()
	deadline := realClock.Now().Add(5 * time.Second)
	for {
		if !s.IsRunning(id) {
			return
		}
		if realClock.Now().After(deadline) {
			t.Fatalf("%s: task %s is still running", msg, id)
		}
		runtime.Gosched()
	}
}

func TestSchedulerBuiltinMatrixSevenRealFiveStub(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, _, _, _ := newTestScheduler(t, c)

	logDir := t.TempDir()
	cacheDir := t.TempDir()
	transcodeDir := filepath.Join(cacheDir, "transcodes")
	if err := os.MkdirAll(transcodeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed an old file in each real-fsclean root so DeleteLogFiles and
	// DeleteCacheFiles actually perform deletion work against fixtures. The
	// mtime is well before the fake clock's now minus any retention window.
	seedOldFileAt(t, logDir, "old.log", c.Now().Add(-100*24*time.Hour))
	seedOldFileAt(t, cacheDir, "old.cache", c.Now().Add(-100*24*time.Hour))

	var scanCalled, maintainCalled, pruneCalled, transcodeCalled, userDataCalled atomic.Bool
	deps := tasks.BuiltinDeps{
		ScanLibrary:       testScanLibrary{fn: func(context.Context, func(float64)) (any, error) { scanCalled.Store(true); return nil, nil }},
		StoreMaintainer:   testMaintainer{fn: func(context.Context) error { maintainCalled.Store(true); return nil }},
		ActivityLogPruner: testPruner{fn: func(context.Context, time.Time) (int, error) { pruneCalled.Store(true); return 0, nil }},
		TranscodeCleaner:  testCleaner{fn: func(context.Context, time.Time) error { transcodeCalled.Store(true); return nil }},
		UserDataCleaner:   testUserDataCleaner{fn: func(context.Context) (int64, error) { userDataCalled.Store(true); return 0, nil }},
		Clock:             c,
		LogPath:           logDir,
		CachePath:         cacheDir,
		TranscodePath:     transcodeDir,
	}
	if err := tasks.RegisterBuiltins(s, deps); err != nil {
		t.Fatalf("RegisterBuiltins: %v", err)
	}

	list := s.List()
	if len(list) != 12 {
		t.Fatalf("List() has %d tasks, want 12", len(list))
	}

	// Verify the complete trigger set for every one of the 12 tasks (spec §4:
	// every stub retains its stock default triggers). Compare full
	// TriggerInfo values — Type, IntervalTicks, TimeOfDayTicks, and
	// MaxRuntimeTicks — so any change to a stock default is detected.
	ticksPerHour := int64(36 * 1e9) // matches builtins.go: ticksPerHour
	ticksPerDay := 24 * ticksPerHour
	interval := func(ticks int64) *int64 { return &ticks }
	ticks := func(d time.Duration) *int64 { t := d.Nanoseconds() / 100; return &t }
	wantTriggers := map[string][]tasks.TriggerInfo{
		refreshLibraryID:                   {{Type: tasks.TriggerInterval, IntervalTicks: interval(12 * ticksPerHour)}},
		"31de9ce83b9223d338c77b1a635e144b": {{Type: tasks.TriggerInterval, IntervalTicks: interval(6 * ticksPerHour)}},                                // OptimizeDatabase
		"b461ef918ab28520928183e794350e3c": nil,                                                                                                       // CleanActivityLog (no triggers)
		"8240e309407dbbf43c99d30a6cbd239e": nil,                                                                                                       // CleanupUserData (no triggers)
		"1c8ede62c521bea0bf851344f5b8ca40": {{Type: tasks.TriggerInterval, IntervalTicks: interval(ticksPerDay)}},                                     // DeleteLogFiles
		"241d4fcb19a1d557ee62428e411da609": {{Type: tasks.TriggerInterval, IntervalTicks: interval(ticksPerDay)}},                                     // DeleteCacheFiles
		"7d8088c10902f1bf4072ded42437bcfb": {{Type: tasks.TriggerStartup}, {Type: tasks.TriggerInterval, IntervalTicks: interval(ticksPerDay)}},       // DeleteTranscodeFiles
		"866456ed0d44e15468124ce33d85961e": {{Type: tasks.TriggerInterval, IntervalTicks: interval(7 * ticksPerDay)}},                                 // RefreshPeople (7-day interval)
		"4e6637c832ed644d1af3370a2506e80a": {{Type: tasks.TriggerDaily, TimeOfDayTicks: ticks(2 * time.Hour), MaxRuntimeTicks: ticks(4 * time.Hour)}}, // RefreshChapterImages
		"ec2f221fd8e7706b3d3afd2c4591b4d7": {{Type: tasks.TriggerInterval, IntervalTicks: interval(ticksPerDay)}},                                     // AudioNormalization (24h interval)
		"f861734dd71b37f9482b52a820e39013": {{Type: tasks.TriggerInterval, IntervalTicks: interval(12 * ticksPerHour)}},                               // MediaSegmentExtraction (12h interval)
		"2c66a88bca43e565d7f8099f825478f1": {{Type: tasks.TriggerInterval, IntervalTicks: interval(ticksPerDay)}},                                     // SubtitleScheduled (24h interval)
	}
	for _, info := range list {
		want, ok := wantTriggers[info.ID]
		if !ok {
			t.Errorf("unexpected task ID %s in matrix", info.ID)
			continue
		}
		if !reflect.DeepEqual(info.Triggers, want) {
			t.Errorf("task %s triggers = %+v, want %+v", info.ID, info.Triggers, want)
		}
	}

	// The five honest stubs are no-op runnables: RefreshPeople,
	// RefreshChapterImages, AudioNormalization, MediaSegmentExtraction,
	// SubtitleScheduled. Run them first and confirm none invokes a backend.
	noopIDs := map[string]bool{
		"866456ed0d44e15468124ce33d85961e": true, // RefreshPeople
		"4e6637c832ed644d1af3370a2506e80a": true, // RefreshChapterImages
		"ec2f221fd8e7706b3d3afd2c4591b4d7": true, // AudioNormalization
		"f861734dd71b37f9482b52a820e39013": true, // MediaSegmentExtraction
		"2c66a88bca43e565d7f8099f825478f1": true, // SubtitleScheduled
	}
	for _, info := range list {
		if !noopIDs[info.ID] {
			continue
		}
		if err := s.Run(info.ID); err != nil {
			t.Fatalf("Run stub %s (%s): %v", info.ID, info.Key, err)
		}
		waitForTaskIdle(t, s, info.ID)
	}
	if scanCalled.Load() || maintainCalled.Load() || pruneCalled.Load() || transcodeCalled.Load() || userDataCalled.Load() {
		t.Fatal("a stub task invoked a backend")
	}

	// Now run the seven real tasks and verify each invokes its backend.
	for _, info := range list {
		if noopIDs[info.ID] {
			continue
		}
		if err := s.Run(info.ID); err != nil {
			t.Fatalf("Run real %s (%s): %v", info.ID, info.Key, err)
		}
		waitForTaskIdle(t, s, info.ID)
	}
	if !scanCalled.Load() {
		t.Error("RefreshLibrary did not invoke ScanLibrary")
	}
	if !maintainCalled.Load() {
		t.Error("OptimizeDatabase did not invoke StoreMaintainer")
	}
	if !pruneCalled.Load() {
		t.Error("CleanActivityLog did not invoke ActivityLogPruner")
	}
	if !transcodeCalled.Load() {
		t.Error("DeleteTranscodeFiles did not invoke TranscodeCleaner")
	}
	if !userDataCalled.Load() {
		t.Error("CleanupUserData did not invoke UserDataCleaner")
	}
	// DeleteLogFiles and DeleteCacheFiles ran fsclean against the fixtures.
	if _, err := os.Stat(filepath.Join(logDir, "old.log")); !os.IsNotExist(err) {
		t.Error("DeleteLogFiles did not delete the seeded old log file")
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "old.cache")); !os.IsNotExist(err) {
		t.Error("DeleteCacheFiles did not delete the seeded old cache file")
	}
	// The transcodes subtree survived DeleteCacheFiles' empty-dir removal.
	if _, err := os.Stat(transcodeDir); err != nil {
		t.Error("DeleteCacheFiles removed the transcodes subtree")
	}
}

// taskByID returns the Info with the given ID from a list, failing the test
// if it is absent.
func taskByID(t *testing.T, list []tasks.Info, id string) tasks.Info {
	t.Helper()
	for _, info := range list {
		if info.ID == id {
			return info
		}
	}
	t.Fatalf("task %s not found in list", id)
	return tasks.Info{}
}

// seedOldFileAt writes a file with the given mtime so fsclean (cutoff derived
// from the injected clock) deletes it.
func seedOldFileAt(t *testing.T, dir, name string, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// waitForTaskIdle deterministically waits for a task to return to Idle with a
// recorded result, polling observable scheduler state with a bounded gosched
// loop (no wall-clock sleeps).
func waitForTaskIdle(t *testing.T, s *tasks.Scheduler, id string) {
	t.Helper()
	deadline := realClock.Now().Add(5 * time.Second)
	for {
		info, ok := s.Get(id)
		if ok && info.State == tasks.StateIdle && info.LastExecutionResult != nil {
			return
		}
		if realClock.Now().After(deadline) {
			info, _ := s.Get(id)
			t.Fatalf("task %s did not return to idle; state=%v hasResult=%v", id, info.State, info.LastExecutionResult != nil)
		}
		runtime.Gosched()
	}
}

// Test helper types for the builtin matrix test.
type testScanLibrary struct {
	fn func(context.Context, func(float64)) (any, error)
}

func (t testScanLibrary) Scan(ctx context.Context, progress func(float64)) (any, error) {
	return t.fn(ctx, progress)
}

type testMaintainer struct{ fn func(context.Context) error }

func (t testMaintainer) Maintain(ctx context.Context) error { return t.fn(ctx) }

type testPruner struct {
	fn func(context.Context, time.Time) (int, error)
}

func (t testPruner) Prune(ctx context.Context, before time.Time) (int, error) {
	return t.fn(ctx, before)
}

type testCleaner struct {
	fn func(context.Context, time.Time) error
}

func (t testCleaner) CleanupInactive(ctx context.Context, olderThan time.Time) error {
	return t.fn(ctx, olderThan)
}

type testUserDataCleaner struct {
	fn func(context.Context) (int64, error)
}

func (t testUserDataCleaner) DeleteDetachedUserData(ctx context.Context) (int64, error) {
	return t.fn(ctx)
}

// ptrString returns a pointer to s, for TriggerInfo fields like DayOfWeek.
func ptrString(s string) *string { return &s }

// stopRecordingClock wraps a clock.Clock and records every timer Stop call
// with the fake-clock time at which Stop occurred. This lets tests prove that
// a timer was stopped before a fake-time deadline without wall-clock sleeps.
type stopRecordingClock struct {
	clock.Clock
	mu    sync.Mutex
	stops []time.Time
}

func (c *stopRecordingClock) NewTimer(d time.Duration) clock.Timer {
	t := c.Clock.NewTimer(d)
	return &stopRecordingTimer{Timer: t, parent: c}
}

func (c *stopRecordingClock) recordStop() {
	c.mu.Lock()
	c.stops = append(c.stops, c.Now())
	c.mu.Unlock()
}

func (c *stopRecordingClock) stopCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.stops)
}

// stopRecordingTimer wraps a clock.Timer and records each Stop call on the
// parent clock.
type stopRecordingTimer struct {
	clock.Timer
	parent *stopRecordingClock
}

func (t *stopRecordingTimer) Stop() bool {
	t.parent.recordStop()
	return t.Timer.Stop()
}

// TestSchedulerUpdateTriggersCancelsSupersededTimerWorker proves that
// UpdateTriggers cancels the superseded arm worker so it exits before
// Scheduler.Close. A long-delay interval trigger is armed at Start; an empty
// trigger list replaces it. goleak.Find, called before Close, must find no
// replaced worker. The stop-recording clock proves the old arm timer was
// stopped.
func TestSchedulerUpdateTriggersCancelsSupersededTimerWorker(t *testing.T) {
	base := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := &stopRecordingClock{Clock: base}

	// Capture the goroutine baseline before Start so goleak attributes only
	// the scheduler's arm workers to this test.
	ignore := goleak.IgnoreCurrent()

	s, _, _, _ := newTestScheduler(t, c)

	// A 12-hour interval fires at +1h (never-completed). The arm worker
	// blocks on the timer channel until cancel or shutdown.
	intervalTicks := int64(12 * time.Hour / 100)
	taskID := "cancel-arm-00000000000000000000000000001"
	if err := s.Register(tasks.Definition{
		ID:              taskID,
		Key:             "cancel-arm",
		Name:            "Cancel Arm Worker Test",
		DefaultTriggers: []tasks.TriggerInfo{{Type: tasks.TriggerInterval, IntervalTicks: &intervalTicks}},
	}, testRunnable{fn: func(_ context.Context, _ func(float64)) error { return nil }}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The arm timer was created at Start. Stop is not called yet — the timer
	// is armed and waiting.
	if got := c.stopCount(); got != 0 {
		t.Fatalf("stopCount after Start = %d, want 0", got)
	}

	// Replace the trigger list with an empty list. armLocked cancels the old
	// arm (stops the timer and closes the arm-cancel channel) and finds no
	// trigger to arm.
	if err := s.UpdateTriggers(taskID, nil); err != nil {
		t.Fatalf("UpdateTriggers: %v", err)
	}
	// The old arm timer was stopped by cancelArmLocked.
	if got := c.stopCount(); got < 1 {
		t.Fatalf("stopCount after UpdateTriggers = %d, want >= 1", got)
	}

	// Give the superseded arm worker a scheduling turn to observe the
	// closed cancel channel, exit, and call doneWorker (no wall-clock sleep).
	for range 64 {
		runtime.Gosched()
	}

	// goleak.Find before Close must find no replaced worker.
	if err := goleak.Find(ignore); err != nil {
		t.Fatalf("goleak.Find before Close found a leaked goroutine: %v", err)
	}
}

// TestSchedulerCompletedRunStopsMaxRuntimeTimer proves that execute stops the
// max-runtime timer when the task finishes before expiry. A startup trigger
// with MaxRuntimeTicks arms a max-runtime timer at fire time. The task
// returns immediately. The stop-recording clock must record Stop before the
// fake clock reaches the max-runtime deadline.
func TestSchedulerCompletedRunStopsMaxRuntimeTimer(t *testing.T) {
	base := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c := &stopRecordingClock{Clock: base}
	s, _, _, _ := newTestScheduler(t, c)

	// MaxRuntimeTicks = 10 seconds (in .NET ticks).
	maxTicks := int64(10 * time.Second / 100)
	done := make(chan struct{}, 1)
	runnable := testRunnable{fn: func(ctx context.Context, _ func(float64)) error {
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	}}

	taskID := "maxrt-stop-00000000000000000000000000001"
	if err := s.Register(tasks.Definition{
		ID:   taskID,
		Key:  "maxrt-stop",
		Name: "Max Runtime Stop Test",
		DefaultTriggers: []tasks.TriggerInfo{
			{Type: tasks.TriggerStartup, MaxRuntimeTicks: &maxTicks},
		},
	}, runnable); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Startup fires at 3s; max runtime at 3s + 10s = 13s.
	c.Clock.(*clock.Fake).Advance(3 * time.Second)
	waitForChan(t, done, time.Second, "task did not start at 3s")

	// Wait for the task to complete (returns immediately). The deferred
	// maxTimer.Stop() runs when execute returns, which is after the result
	// is recorded. Poll for the stop record with a bounded gosched loop so
	// the test does not race the defer (no wall-clock sleep).
	waitForResultStatus(t, s, taskID, tasks.StatusCompleted)
	deadline := realClock.Now().Add(5 * time.Second)
	for c.stopCount() == 0 {
		if realClock.Now().After(deadline) {
			t.Fatal("max-runtime timer Stop not recorded after task completion")
		}
		runtime.Gosched()
	}

	// The max-runtime timer must have been stopped before the fake clock
	// reaches the deadline (3s + 10s = 13s). The fake clock is at 3s now.
	stopTime := base.Now()
	maxDeadline := time.Date(2026, 1, 1, 0, 0, 13, 0, time.UTC)
	if stopTime.After(maxDeadline) {
		t.Fatalf("Stop recorded at %v, after the deadline %v", stopTime, maxDeadline)
	}

	// Advance past the max-runtime deadline. The stopped timer must not fire
	// and the task must remain Completed (not Aborted).
	c.Clock.(*clock.Fake).Advance(20 * time.Second)
	info, ok := s.Get(taskID)
	if !ok {
		t.Fatalf("Get(%q) returned false", taskID)
	}
	if info.LastExecutionResult == nil || string(info.LastExecutionResult.Status) != tasks.StatusCompleted {
		got := "nil"
		if info.LastExecutionResult != nil {
			got = string(info.LastExecutionResult.Status)
		}
		t.Fatalf("after advancing past deadline: status = %s, want %q", got, tasks.StatusCompleted)
	}
}

// TestNewSchedulerReturnsPersistenceRootErrors makes sure that NewScheduler
// returns a nil scheduler and a wrapped error when the trigger or the result
// persistence directory cannot be created. A regular file blocks MkdirAll.
func TestNewSchedulerReturnsPersistenceRootErrors(t *testing.T) {
	t.Run("trigger root", func(t *testing.T) {
		root := t.TempDir()
		blocker := filepath.Join(root, "config", "ScheduledTasks")
		if err := os.MkdirAll(filepath.Dir(blocker), 0o700); err != nil {
			t.Fatalf("mkdir parent: %v", err)
		}
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatalf("write blocker file: %v", err)
		}
		validResult := filepath.Join(root, "data", "ScheduledTasks")
		c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		catalog := tasks.New(c)
		t.Cleanup(func() { _ = catalog.Close(context.Background()) })
		scheduler, err := tasks.NewScheduler(c, time.UTC, catalog, blocker, validResult)
		if scheduler != nil {
			t.Fatalf("scheduler = %v, want nil", scheduler)
		}
		if err == nil {
			t.Fatal("err = nil, want an error")
		}
		if !strings.Contains(err.Error(), "tasks: create trigger dir") {
			t.Fatalf("err = %v, want context %q", err, "tasks: create trigger dir")
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("err = %v, want an error that unwraps to *os.PathError", err)
		}
	})
	t.Run("result root", func(t *testing.T) {
		root := t.TempDir()
		blocker := filepath.Join(root, "data", "ScheduledTasks")
		if err := os.MkdirAll(filepath.Dir(blocker), 0o700); err != nil {
			t.Fatalf("mkdir parent: %v", err)
		}
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatalf("write blocker file: %v", err)
		}
		validTrigger := filepath.Join(root, "config", "ScheduledTasks")
		c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		catalog := tasks.New(c)
		t.Cleanup(func() { _ = catalog.Close(context.Background()) })
		scheduler, err := tasks.NewScheduler(c, time.UTC, catalog, validTrigger, blocker)
		if scheduler != nil {
			t.Fatalf("scheduler = %v, want nil", scheduler)
		}
		if err == nil {
			t.Fatal("err = nil, want an error")
		}
		if !strings.Contains(err.Error(), "tasks: create result dir") {
			t.Fatalf("err = %v, want context %q", err, "tasks: create result dir")
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("err = %v, want an error that unwraps to *os.PathError", err)
		}
	})
}

// TestSchedulerRejectsRegistrationAfterStart proves Register after Start
// returns ErrSchedulerStarted without changing the task map or catalog,
// and that ErrSchedulerClosed keeps precedence after Close.
func TestSchedulerRejectsRegistrationAfterStart(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, catalog, _, _ := newTestScheduler(t, c)

	lateID := "late-registration-00000000000000000001"

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	err := s.Register(tasks.Definition{
		ID:   lateID,
		Key:  "late",
		Name: "Late Registration Test",
	}, testRunnable{fn: func(_ context.Context, _ func(float64)) error { return nil }})
	if !errors.Is(err, tasks.ErrSchedulerStarted) {
		t.Fatalf("Register after Start = %v, want ErrSchedulerStarted", err)
	}

	// The rejected call must leave the scheduler map and catalog unchanged.
	if _, ok := s.Get(lateID); ok {
		t.Fatal("Scheduler.Get found the late task; the map must not change")
	}
	for _, info := range catalog.Snapshot() {
		if info.ID == lateID {
			t.Fatal("Catalog.Snapshot contains the late task; the catalog must not change")
		}
	}

	// After shutdown, ErrSchedulerClosed keeps precedence over
	// ErrSchedulerStarted.
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err = s.Register(tasks.Definition{
		ID:   lateID,
		Key:  "late",
		Name: "Late Registration Test",
	}, testRunnable{fn: func(_ context.Context, _ func(float64)) error { return nil }})
	if !errors.Is(err, tasks.ErrSchedulerClosed) {
		t.Fatalf("Register after Close = %v, want ErrSchedulerClosed", err)
	}
}
