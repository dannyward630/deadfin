// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

type jobLoggerProgress struct {
	Position        time.Duration
	FPS             float32
	Percent         float64
	BytesTranscoded int64
	Bitrate         int
}

type jobLoggerOptions struct {
	Clock          clock.Clock
	Stderr         io.Reader
	Log            io.Writer
	RunTimeTicks   *int64
	StartTimeTicks int64
	MediaSourceID  string
	MaxOutput      int64
	NewID          func() string
	Report         func(jobLoggerProgress)
}

type jobLogger struct{ options jobLoggerOptions }

var (
	loggerFPS     = regexp.MustCompile(`(?i)(?:^|\s)fps=\s*([0-9]+(?:\.[0-9]+)?)(?:\s|$)`)
	loggerTime    = regexp.MustCompile(`(?i)(?:^|\s)time=([0-9]+):([0-5][0-9]):([0-5][0-9](?:\.[0-9]+)?)(?:\s|$)`)
	loggerSize    = regexp.MustCompile(`(?i)(?:^|\s)size=\s*([0-9]+)kB(?:\s|$)`)
	loggerBitrate = regexp.MustCompile(`(?i)(?:^|\s)bitrate=\s*([0-9]+(?:\.[0-9]+)?)kbits/s(?:\s|$)`)
)

const maxJobLoggerLine = 32 << 10

func newJobLogger(options jobLoggerOptions) *jobLogger {
	if options.Clock == nil {
		options.Clock = clock.NewReal()
	}
	return &jobLogger{options: options}
}

func (logger *jobLogger) Filename() string {
	now := logger.options.Clock.Now().UTC()
	id := ""
	if logger.options.NewID != nil {
		id = logger.options.NewID()
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return fmt.Sprintf("ffmpeg-transcode-%s-%s-%s.log", now.Format("20060102T150405"), logger.options.MediaSourceID, id)
}

func (logger *jobLogger) Run(ctx context.Context) error {
	if logger.options.Stderr == nil {
		return nil
	}
	reader := bufio.NewReaderSize(logger.options.Stderr, maxJobLoggerLine+1)
	line := make([]byte, 0, maxJobLoggerLine)
	remaining := logger.options.MaxOutput
	for {
		var discarded bool
		var err error
		line, discarded, err = readJobLoggerLine(reader, line)
		if !discarded && len(line) != 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			logger.copyRaw(line, &remaining)
			logger.parse(string(line))
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// readJobLoggerLine reads one CR- or LF-delimited record without retaining an
// unbounded record. Oversized records are consumed through their delimiter so
// that a following progress report remains observable.
func readJobLoggerLine(reader *bufio.Reader, line []byte) ([]byte, bool, error) {
	line = line[:0]
	discarded := false
	for {
		character, err := reader.ReadByte()
		if err != nil {
			return line, discarded, err
		}
		if character == '\n' || character == '\r' {
			return line, discarded, nil
		}
		if discarded {
			continue
		}
		if len(line) == maxJobLoggerLine {
			discarded = true
			line = line[:0]
			continue
		}
		line = append(line, character)
	}
}

func (logger *jobLogger) copyRaw(data []byte, remaining *int64) {
	if logger.options.Log == nil || *remaining <= 0 || int64(len(data)) > *remaining {
		return
	}
	_, _ = logger.options.Log.Write(data)
	*remaining -= int64(len(data))
}

func (logger *jobLogger) parse(line string) {
	progress := jobLoggerProgress{}
	hasFramerate := false
	if fpsMatch := loggerFPS.FindStringSubmatch(line); fpsMatch != nil {
		if fps, err := strconv.ParseFloat(fpsMatch[1], 32); err == nil && !math.IsNaN(fps) && !math.IsInf(fps, 0) {
			progress.FPS = float32(fps)
			hasFramerate = true
		}
	}

	hasPosition := false
	if logger.options.RunTimeTicks != nil && *logger.options.RunTimeTicks > 0 {
		if timeMatch := loggerTime.FindStringSubmatch(line); timeMatch != nil {
			if elapsed, ok := loggerElapsed(timeMatch); ok {
				if start, ok := loggerTicksDuration(logger.options.StartTimeTicks); ok {
					if position, ok := loggerAddDuration(start, elapsed); ok {
						percent := float64(position) / float64(*logger.options.RunTimeTicks)
						if !math.IsNaN(percent) && !math.IsInf(percent, 0) {
							progress.Position = position
							progress.Percent = percent
							hasPosition = true
						}
					}
				}
			}
		}
	}

	if sizeMatch := loggerSize.FindStringSubmatch(line); sizeMatch != nil {
		if size, err := strconv.ParseInt(sizeMatch[1], 10, 64); err == nil && size <= int64(loggerMaxDuration)/1024 {
			progress.BytesTranscoded = size * 1024
		}
	}
	if bitrateMatch := loggerBitrate.FindStringSubmatch(line); bitrateMatch != nil {
		if bitrate, err := strconv.ParseFloat(bitrateMatch[1], 64); err == nil && !math.IsNaN(bitrate) && !math.IsInf(bitrate, 0) {
			scaled := math.Ceil(bitrate * 1024)
			if !math.IsNaN(scaled) && !math.IsInf(scaled, 0) && scaled <= float64(loggerMaxInt) {
				progress.Bitrate = int(scaled)
			}
		}
	}

	if logger.options.Report != nil && (hasFramerate || hasPosition) {
		logger.options.Report(progress)
	}
}

const (
	loggerMaxDuration = time.Duration(1<<63 - 1)
	loggerMinDuration = -loggerMaxDuration - 1
)

var loggerMaxInt = int64(^uint(0) >> 1)

func loggerElapsed(match []string) (time.Duration, bool) {
	hours, hourErr := strconv.ParseInt(match[1], 10, 64)
	minutes, minuteErr := strconv.ParseInt(match[2], 10, 64)
	seconds, secondErr := strconv.ParseFloat(match[3], 64)
	if hourErr != nil || minuteErr != nil || secondErr != nil || hours < 0 || minutes < 0 || seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0, false
	}
	if hours > int64(loggerMaxDuration/time.Hour) {
		return 0, false
	}

	elapsed := time.Duration(hours) * time.Hour
	var ok bool
	if elapsed, ok = loggerAddDuration(elapsed, time.Duration(minutes)*time.Minute); !ok {
		return 0, false
	}
	return loggerAddDuration(elapsed, time.Duration(seconds*float64(time.Second)))
}

func loggerTicksDuration(ticks int64) (time.Duration, bool) {
	if ticks > int64(loggerMaxDuration)/100 || ticks < int64(loggerMinDuration)/100 {
		return 0, false
	}
	return time.Duration(ticks * 100), true
}

func loggerAddDuration(left, right time.Duration) (time.Duration, bool) {
	if right > 0 && left > loggerMaxDuration-right || right < 0 && left < loggerMinDuration-right {
		return 0, false
	}
	return left + right, true
}
