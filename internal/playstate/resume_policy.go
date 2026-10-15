// SPDX-License-Identifier: GPL-2.0-only

// Package playstate contains pure playback-state policy.
package playstate

import "math/bits"

const ticksPerSecond int64 = 10_000_000

// ResumeSettings contains the server-configured resume thresholds.
type ResumeSettings struct {
	MinResumePct             int
	MaxResumePct             int
	MinResumeDurationSeconds int
	MinAudiobookResume       int
	MaxAudiobookResume       int
}

// ResumeInput contains the media facts needed to apply resume policy. Tick values
// are optional because a playback update may omit either position or runtime.
type ResumeInput struct {
	PositionTicks               *int64
	RunTimeTicks                *int64
	IsAudiobook                 bool
	IsBook                      bool
	SupportsPlayedStatus        bool
	SupportsPositionTicksResume bool
}

// ResumeResult is the playback state that must be persisted. PlayedToCompletion
// identifies completion by this update, independently of whether the item can
// persist played status.
type ResumeResult struct {
	PositionTicks      int64
	Played             bool
	PlayedToCompletion bool
}

// ApplyResumePolicy calculates persisted playback state without accessing storage.
func ApplyResumePolicy(settings ResumeSettings, input ResumeInput, currentlyPlayed bool) ResumeResult {
	runtimeTicks := ticks(input.RunTimeTicks)
	positionTicks := ticks(input.PositionTicks)
	if input.PositionTicks == nil {
		positionTicks = runtimeTicks
	}

	result := ResumeResult{
		PositionTicks: positionTicks,
		Played:        currentlyPlayed,
	}
	hasRuntime := runtimeTicks > 0

	switch {
	case !hasRuntime:
		complete(&result)
	case positionTicks > 0 && input.IsAudiobook:
		applyAudiobookPolicy(settings, runtimeTicks, &result)
	case positionTicks > 0 && !input.IsBook:
		applyOrdinaryPolicy(settings, runtimeTicks, &result)
	}

	if !input.SupportsPlayedStatus {
		result.PositionTicks = 0
		result.Played = false
	}
	if !input.SupportsPositionTicksResume {
		result.PositionTicks = 0
	}

	return result
}

func applyOrdinaryPolicy(settings ResumeSettings, runtimeTicks int64, result *ResumeResult) {
	positionTicks := result.PositionTicks

	if percentageLess(positionTicks, runtimeTicks, settings.MinResumePct) {
		result.PositionTicks = 0
		return
	}
	if percentageGreater(positionTicks, runtimeTicks, settings.MaxResumePct) || positionTicks >= runtimeTicks-ticksPerSecond {
		complete(result)
		return
	}
	if ticksLessThanUnits(runtimeTicks, settings.MinResumeDurationSeconds, uint64(ticksPerSecond)) {
		complete(result)
	}
}

func applyAudiobookPolicy(settings ResumeSettings, runtimeTicks int64, result *ResumeResult) {
	positionTicks := result.PositionTicks
	const ticksPerMinute = uint64(60 * ticksPerSecond)

	if ticksLessThanUnits(positionTicks, settings.MinAudiobookResume, ticksPerMinute) {
		result.PositionTicks = 0
		return
	}
	if positionTicks >= runtimeTicks || ticksLessThanUnits(runtimeTicks-positionTicks, settings.MaxAudiobookResume, ticksPerMinute) {
		complete(result)
	}
}

func complete(result *ResumeResult) {
	result.PositionTicks = 0
	result.Played = true
	result.PlayedToCompletion = true
}

func ticks(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// percentageLess and percentageGreater compare a percentage without floating
// point rounding or overflowing 64-bit tick values.
func percentageLess(positionTicks, runtimeTicks int64, percentage int) bool {
	if percentage <= 0 {
		return false
	}
	return productLess(uint64(positionTicks), 100, uint64(runtimeTicks), uint64(percentage))
}

func percentageGreater(positionTicks, runtimeTicks int64, percentage int) bool {
	if percentage < 0 {
		return true
	}
	return productLess(uint64(runtimeTicks), uint64(percentage), uint64(positionTicks), 100)
}

func productLess(leftA, leftB, rightA, rightB uint64) bool {
	leftHigh, leftLow := bits.Mul64(leftA, leftB)
	rightHigh, rightLow := bits.Mul64(rightA, rightB)
	return leftHigh < rightHigh || leftHigh == rightHigh && leftLow < rightLow
}

// ticksLessThanUnits compares nonnegative ticks with units multiplied by a tick
// scale without overflowing. Resume thresholds below zero cannot be crossed by a
// nonnegative tick value.
func ticksLessThanUnits(value int64, units int, ticksPerUnit uint64) bool {
	if units <= 0 {
		return false
	}

	high, low := bits.Mul64(uint64(units), ticksPerUnit)
	return high != 0 || uint64(value) < low
}
