// SPDX-License-Identifier: GPL-2.0-only

package tasks

import (
	"fmt"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

// Trigger type names matching Jellyfin's wire enum values.
const (
	TriggerDaily    = "DailyTrigger"
	TriggerWeekly   = "WeeklyTrigger"
	TriggerInterval = "IntervalTrigger"
	TriggerStartup  = "StartupTrigger"
)

// .NET ticks are 100-nanosecond units.
const ticksPerNanosecond = 100

func ticksToDuration(ticks int64) time.Duration {
	return time.Duration(ticks) * ticksPerNanosecond
}

func durationToTicks(d time.Duration) int64 {
	return int64(d) / ticksPerNanosecond
}

// validateTrigger returns nil if the trigger carries the fields its Type requires.
// The required fields mirror Jellyfin's trigger constructors:
//   - DailyTrigger: TimeOfDayTicks
//   - WeeklyTrigger: DayOfWeek + TimeOfDayTicks
//   - IntervalTrigger: IntervalTicks
//   - StartupTrigger: none
func validateTrigger(t TriggerInfo) error {
	switch t.Type {
	case TriggerDaily:
		if t.TimeOfDayTicks == nil {
			return fmt.Errorf("tasks: %s requires TimeOfDayTicks", t.Type)
		}
	case TriggerWeekly:
		if t.TimeOfDayTicks == nil {
			return fmt.Errorf("tasks: %s requires TimeOfDayTicks", t.Type)
		}
		if t.DayOfWeek == nil || *t.DayOfWeek == "" {
			return fmt.Errorf("tasks: %s requires DayOfWeek", t.Type)
		}
	case TriggerInterval:
		if t.IntervalTicks == nil {
			return fmt.Errorf("tasks: %s requires IntervalTicks", t.Type)
		}
	case TriggerStartup:
		// no required fields
	default:
		return fmt.Errorf("tasks: unknown trigger type %q", t.Type)
	}
	return nil
}

// nextFireDelay computes when a trigger should next fire, measured from now.
// It uses the injected clock for the current time and loc for local-time
// computation (Daily/Weekly). lastResultEndTime and lastArmTime drive the
// Interval trigger's reschedule-from-completion logic.
func nextFireDelay(c clock.Clock, loc *time.Location, t TriggerInfo, lastResultEndTime time.Time, lastArmTime time.Time) time.Duration {
	now := c.Now()
	switch t.Type {
	case TriggerStartup:
		// StartupTrigger fires once, 3 seconds after Start (StartupTrigger.cs:13,35).
		return 3 * time.Second
	case TriggerDaily:
		ticks := *t.TimeOfDayTicks
		timeOfDay := ticksToDuration(ticks)
		return dailyDelay(now, loc, timeOfDay)
	case TriggerWeekly:
		ticks := *t.TimeOfDayTicks
		timeOfDay := ticksToDuration(ticks)
		day := weeklyDayOffset(*t.DayOfWeek)
		return weeklyDelay(now, loc, timeOfDay, day)
	case TriggerInterval:
		interval := ticksToDuration(*t.IntervalTicks)
		return intervalDelay(now, lastResultEndTime, lastArmTime, interval)
	default:
		return 0
	}
}

// dailyDelay computes the delay to the next occurrence of timeOfDay in loc.
// If today's occurrence already passed, tomorrow's is used (DailyTrigger.cs:38-48).
//
// DST note: the next target is computed by constructing the calendar date
// (time.Date(y,m,d,0,0,0,0,loc).Add(timeOfDay)), not by adding a fixed 24 h
// duration. A DST transition day is 23 or 25 hours long; time.Date normalizes
// the wall-clock instant in the scheduler's location, so the delay accounts
// for the missing or repeated hour. A skipped local time (spring forward)
// normalizes forward to the first valid offset; an ambiguous time (fall
// back) takes Go's first mapping — both match upstream's DateTime.Now
// resolution because the target is a named local wall-clock instant.
func dailyDelay(now time.Time, loc *time.Location, timeOfDay time.Duration) time.Duration {
	nowLocal := now.In(loc)
	y, m, d := nowLocal.Date()
	todayTarget := time.Date(y, m, d, 0, 0, 0, 0, loc).Add(timeOfDay)
	if !todayTarget.After(now) {
		// Recompute tomorrow as a calendar date so time.Date applies the
		// correct DST offset (a 23 h or 25 h day), not a fixed 24 h span.
		todayTarget = time.Date(y, m, d+1, 0, 0, 0, 0, loc).Add(timeOfDay)
	}
	return todayTarget.Sub(now)
}

// weeklyDelay computes the delay to the next matching DayOfWeek at timeOfDay.
// The target is constructed as a calendar date (time.Date with the day offset
// from today), not by adding a fixed multiple of 24 h, so a DST transition
// inside the week applies the correct offset (23 h or 25 h day). If today's
// matching occurrence already passed, the same weekday next week is used
// (computed as d+7, again via time.Date for DST correctness).
func weeklyDelay(now time.Time, loc *time.Location, timeOfDay time.Duration, dayOfWeek int) time.Duration {
	nowLocal := now.In(loc)
	y, m, d := nowLocal.Date()
	offset := (dayOfWeek - int(nowLocal.Weekday()) + 7) % 7
	target := time.Date(y, m, d+offset, 0, 0, 0, 0, loc).Add(timeOfDay)
	if !target.After(now) {
		// Next week: d+7 as a calendar date so time.Date applies the DST
		// offset for each day in the intervening week.
		target = time.Date(y, m, d+offset+7, 0, 0, 0, 0, loc).Add(timeOfDay)
	}
	return target.Sub(now)
}

// intervalDelay computes the delay for an interval trigger.
// Never-completed → now + 1h (upstream IntervalTrigger.cs:44-48 uses a fixed
// one-hour first-fire regardless of the configured interval); else
// max(lastResultEndTime, lastArmTime, now+1min) + interval, capped at 7 days
// (IntervalTrigger.cs:49-61).
//
// "Never-completed" means the task has never produced a result
// (lastResultEndTime.IsZero). The arm time does not gate the first fire: a
// trigger armed at Start and then re-armed by UpdateTriggers before the first
// completion is still never-completed and fires at +1h, not now+interval.
// lastArmTime only matters in the reschedule (post-completion) branch, where it
// represents when the trigger was last armed relative to the last result.
func intervalDelay(now time.Time, lastResultEndTime, lastArmTime time.Time, interval time.Duration) time.Duration {
	if lastResultEndTime.IsZero() {
		return time.Hour
	}
	earliest := lastResultEndTime
	if lastArmTime.After(earliest) {
		earliest = lastArmTime
	}
	minNext := now.Add(time.Minute)
	if earliest.Before(minNext) {
		earliest = minNext
	}
	fire := earliest.Add(interval)
	cap := now.Add(7 * 24 * time.Hour)
	if fire.After(cap) {
		fire = cap
	}
	return fire.Sub(now)
}

// weeklyDayOffset maps Jellyfin's wire DayOfWeek string to time.Weekday int.
func weeklyDayOffset(day string) int {
	switch day {
	case "Sunday":
		return int(time.Sunday)
	case "Monday":
		return int(time.Monday)
	case "Tuesday":
		return int(time.Tuesday)
	case "Wednesday":
		return int(time.Wednesday)
	case "Thursday":
		return int(time.Thursday)
	case "Friday":
		return int(time.Friday)
	case "Saturday":
		return int(time.Saturday)
	default:
		return 0
	}
}
