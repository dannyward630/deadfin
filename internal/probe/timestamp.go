// SPDX-License-Identifier: GPL-2.0-only

package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/deadfin-org/deadfin/internal/playback"
	"golang.org/x/sys/unix"
)

// MpegTimestamp ports ProbeResultNormalizer.GetMpegTimestamp. It intentionally
// reads exactly the first 197 bytes: this is the upstream wire-level heuristic
// for distinguishing valid, zero, and absent TS timestamps. Context is required
// because opening and reading the file can block.
func MpegTimestamp(ctx context.Context, path string) (playback.TransportStreamTimestamp, error) {
	if err := ctx.Err(); err != nil {
		return playback.TimestampNone, err
	}
	packet, err := readMpegHeader(ctx, path)
	if err != nil {
		return playback.TimestampNone, err
	}
	if packet[0] == 71 {
		return playback.TimestampNone, nil
	}
	if packet[4] != 71 || packet[196] != 71 {
		return playback.TimestampNone, nil
	}
	if packet[0] == 0 && packet[1] == 0 && packet[2] == 0 && packet[3] == 0 {
		return playback.TimestampZero, nil
	}
	return playback.TimestampValid, nil
}

// readMpegHeader performs a non-blocking open and bounded polling rather than
// leaving an uninterruptible opener goroutine behind on a blocked FIFO.
func readMpegHeader(ctx context.Context, path string) (packet [197]byte, err error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return packet, fmt.Errorf("opening MPEG transport stream: %w", err)
	}
	defer func() {
		if closeErr := unix.Close(fd); closeErr != nil && err == nil {
			err = fmt.Errorf("closing MPEG transport stream: %w", closeErr)
		}
	}()

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return packet, fmt.Errorf("statting MPEG transport stream: %w", err)
	}
	fifo := stat.Mode&unix.S_IFMT == unix.S_IFIFO
	for offset := 0; offset < len(packet); {
		if err := ctx.Err(); err != nil {
			return packet, err
		}
		count, readErr := unix.Read(fd, packet[offset:])
		if count > 0 {
			offset += count
			continue
		}
		if readErr != nil && !errors.Is(readErr, unix.EAGAIN) && !errors.Is(readErr, unix.EWOULDBLOCK) {
			return packet, fmt.Errorf("reading MPEG transport stream header: %w", readErr)
		}
		if count == 0 && (!fifo || offset > 0) {
			return packet, fmt.Errorf("reading MPEG transport stream header: %w", io.ErrUnexpectedEOF)
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(poll, 10); err != nil && !errors.Is(err, unix.EINTR) {
			return packet, fmt.Errorf("waiting for MPEG transport stream header: %w", err)
		}
		if poll[0].Revents&unix.POLLIN == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return packet, nil
}
