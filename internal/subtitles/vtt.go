// SPDX-License-Identifier: GPL-2.0-only

package subtitles

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

const subtitleTicksPerMillisecond = int64(10_000)

// WindowVTT filters WebVTT cues with Jellyfin SubtitleEncoder.FilterEvents
// semantics. Non-cue blocks are retained verbatim. With preserveTimestamps,
// retained cues are also byte-identical to their input.
func WindowVTT(vtt []byte, startTicks, endTicks int64, preserveTimestamps bool) ([]byte, error) {
	return windowSubtitles(vtt, startTicks, endTicks, preserveTimestamps, '.')
}

// WindowSRT filters SubRip cues with Jellyfin SubtitleEncoder.FilterEvents
// semantics. SRT uses a comma between seconds and milliseconds.
func WindowSRT(srt []byte, startTicks, endTicks int64, preserveTimestamps bool) ([]byte, error) {
	return windowSubtitles(srt, startTicks, endTicks, preserveTimestamps, ',')
}

// AddVTTTimeMap applies Jellyfin's literal, global WEBVTT replacement. In
// particular, literals in NOTE blocks are intentionally replaced too.
func AddVTTTimeMap(vtt []byte) []byte {
	return bytes.ReplaceAll(vtt, []byte("WEBVTT"), []byte("WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000"))
}

// AddVTTTimeMapCapped applies AddVTTTimeMap only when its complete result fits
// maxOutput. It detects expansion before allocating the replacement output.
func AddVTTTimeMapCapped(vtt []byte, maxOutput int) ([]byte, error) {
	const header = "WEBVTT"
	const timeMap = "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000"

	if maxOutput < 0 || len(vtt) > maxOutput {
		return nil, ErrOutputTooLarge
	}
	expansion := len(timeMap) - len(header)
	if occurrences := bytes.Count(vtt, []byte(header)); occurrences > (maxOutput-len(vtt))/expansion {
		return nil, ErrOutputTooLarge
	}
	return AddVTTTimeMap(vtt), nil
}

func windowSubtitles(raw []byte, startTicks, endTicks int64, preserveTimestamps bool, fractionSeparator byte) ([]byte, error) {
	var output bytes.Buffer
	for _, block := range splitSubtitleBlocks(raw) {
		timing, ok := findCueTiming(block.body, fractionSeparator)
		if !ok {
			if output.Len() > 0 {
				output.Write(block.prefix)
			}
			output.Write(block.body)
			continue
		}

		// FilterEvents uses strict comparisons. A cue ending exactly at start
		// and a cue beginning exactly at end are deliberately retained.
		if timing.start < startTicks && timing.end < startTicks {
			continue
		}
		if endTicks > 0 && timing.start > endTicks {
			continue
		}

		if output.Len() > 0 {
			output.Write(block.prefix)
		}
		if preserveTimestamps {
			output.Write(block.body)
		} else {
			rewritten, err := rewriteCueTiming(block.body, timing, startTicks, fractionSeparator)
			if err != nil {
				return nil, err
			}
			output.Write(rewritten)
		}
	}
	return output.Bytes(), nil
}

type subtitleBlock struct {
	prefix []byte
	body   []byte
}

// splitSubtitleBlocks keeps a blank line with the block that follows it. The
// line ending which terminates a retained cue stays with that cue, while the
// separating blank line disappears when its following cue is filtered out.
func splitSubtitleBlocks(raw []byte) []subtitleBlock {
	blocks := make([]subtitleBlock, 0, 8)
	start := 0
	prefixStart, prefixEnd := 0, 0
	for position := 0; position < len(raw); {
		firstEnd := lineEndingEnd(raw, position)
		if firstEnd == position {
			position++
			continue
		}
		secondEnd := lineEndingEnd(raw, firstEnd)
		if secondEnd == firstEnd {
			position++
			continue
		}
		blocks = append(blocks, subtitleBlock{prefix: raw[prefixStart:prefixEnd], body: raw[start:firstEnd]})
		prefixStart, prefixEnd = firstEnd, secondEnd
		start = secondEnd
		position = secondEnd
	}
	if start < len(raw) {
		blocks = append(blocks, subtitleBlock{prefix: raw[prefixStart:prefixEnd], body: raw[start:]})
	}
	return blocks
}

func lineEndingEnd(raw []byte, position int) int {
	if position >= len(raw) {
		return position
	}
	if raw[position] == '\n' {
		return position + 1
	}
	if raw[position] == '\r' && position+1 < len(raw) && raw[position+1] == '\n' {
		return position + 2
	}
	return position
}

type cueTiming struct {
	start, end                     int64
	startTokenStart, startTokenEnd int
	endTokenStart, endTokenEnd     int
}

func findCueTiming(block []byte, fractionSeparator byte) (cueTiming, bool) {
	lineStart := 0
	for lineStart < len(block) {
		lineEnd := bytes.IndexByte(block[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(block)
		} else {
			lineEnd += lineStart
		}
		line := string(bytes.TrimSuffix(block[lineStart:lineEnd], []byte("\r")))
		if timing, ok := parseCueTimingLine(line, fractionSeparator); ok {
			timing.startTokenStart += lineStart
			timing.startTokenEnd += lineStart
			timing.endTokenStart += lineStart
			timing.endTokenEnd += lineStart
			return timing, true
		}
		if lineEnd == len(block) {
			break
		}
		lineStart = lineEnd + 1
	}
	return cueTiming{}, false
}

func parseCueTimingLine(line string, fractionSeparator byte) (cueTiming, bool) {
	arrow := strings.Index(line, "-->")
	if arrow < 0 {
		return cueTiming{}, false
	}

	left := strings.TrimSpace(line[:arrow])
	if left == "" {
		return cueTiming{}, false
	}
	leftOffset := strings.Index(line[:arrow], left)
	start, ok := parseSubtitleTimestamp(left, fractionSeparator)
	if !ok {
		return cueTiming{}, false
	}

	rightStart := arrow + len("-->")
	for rightStart < len(line) && (line[rightStart] == ' ' || line[rightStart] == '\t') {
		rightStart++
	}
	rightEnd := rightStart
	for rightEnd < len(line) && line[rightEnd] != ' ' && line[rightEnd] != '\t' {
		rightEnd++
	}
	if rightStart == rightEnd {
		return cueTiming{}, false
	}
	end, ok := parseSubtitleTimestamp(line[rightStart:rightEnd], fractionSeparator)
	if !ok {
		return cueTiming{}, false
	}
	return cueTiming{
		start:           start,
		end:             end,
		startTokenStart: leftOffset,
		startTokenEnd:   leftOffset + len(left),
		endTokenStart:   rightStart,
		endTokenEnd:     rightEnd,
	}, true
}

func parseSubtitleTimestamp(value string, fractionSeparator byte) (int64, bool) {
	fraction := strings.LastIndexByte(value, fractionSeparator)
	if fraction < 0 || len(value)-fraction != 4 {
		return 0, false
	}
	milliseconds, ok := parseDecimal(value[fraction+1:])
	if !ok || milliseconds > 999 {
		return 0, false
	}
	parts := strings.Split(value[:fraction], ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, false
	}
	var hours, minutes, seconds int64
	if len(parts) == 3 {
		if hours, ok = parseDecimal(parts[0]); !ok {
			return 0, false
		}
		if minutes, ok = parseDecimal(parts[1]); !ok {
			return 0, false
		}
		if seconds, ok = parseDecimal(parts[2]); !ok {
			return 0, false
		}
	} else {
		if minutes, ok = parseDecimal(parts[0]); !ok {
			return 0, false
		}
		if seconds, ok = parseDecimal(parts[1]); !ok {
			return 0, false
		}
	}
	if minutes > 59 || seconds > 59 {
		return 0, false
	}
	const maxMilliseconds = int64(^uint64(0)>>1) / subtitleTicksPerMillisecond
	if hours > (maxMilliseconds/1000)/3600 {
		return 0, false
	}
	totalMilliseconds := ((hours*60+minutes)*60+seconds)*1000 + milliseconds
	if totalMilliseconds > maxMilliseconds {
		return 0, false
	}
	return totalMilliseconds * subtitleTicksPerMillisecond, true
}

func parseDecimal(value string) (int64, bool) {
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}

func rewriteCueTiming(block []byte, timing cueTiming, startTicks int64, fractionSeparator byte) ([]byte, error) {
	shiftedStart := timing.start - startTicks
	if shiftedStart < 0 {
		shiftedStart = 0
	}
	shiftedEnd := timing.end - startTicks
	if shiftedEnd < 0 {
		shiftedEnd = 0
	}
	start, err := formatSubtitleTimestamp(shiftedStart, fractionSeparator)
	if err != nil {
		return nil, err
	}
	end, err := formatSubtitleTimestamp(shiftedEnd, fractionSeparator)
	if err != nil {
		return nil, err
	}

	// Timestamp tokens are ASCII and live on a single line, so their byte
	// offsets remain valid in the original block regardless of UTF-8 payload.
	result := make([]byte, 0, len(block)-((timing.startTokenEnd-timing.startTokenStart)+(timing.endTokenEnd-timing.endTokenStart))+len(start)+len(end))
	result = append(result, block[:timing.startTokenStart]...)
	result = append(result, start...)
	result = append(result, block[timing.startTokenEnd:timing.endTokenStart]...)
	result = append(result, end...)
	result = append(result, block[timing.endTokenEnd:]...)
	return result, nil
}

func formatSubtitleTimestamp(ticks int64, fractionSeparator byte) (string, error) {
	if ticks < 0 {
		ticks = 0
	}
	milliseconds := ticks / subtitleTicksPerMillisecond
	hours := milliseconds / 3_600_000
	minutes := (milliseconds / 60_000) % 60
	seconds := (milliseconds / 1_000) % 60
	millis := milliseconds % 1_000
	if hours > int64(^uint(0)>>1) {
		return "", fmt.Errorf("subtitle timestamp exceeds platform integer range")
	}
	return fmt.Sprintf("%02d:%02d:%02d%c%03d", hours, minutes, seconds, fractionSeparator, millis), nil
}
