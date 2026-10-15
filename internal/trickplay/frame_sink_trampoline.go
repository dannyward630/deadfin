// SPDX-License-Identifier: GPL-2.0-only

package trickplay

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	frameSinkFDEnvironment = "DEADFIN_FRAME_SINK_FD"
	frameSinkIDEnvironment = "DEADFIN_FRAME_SINK_ID"
	frameSinkFD            = 3
	sourceFDEnvironment    = "DEADFIN_SOURCE_FD"
	sourceIDEnvironment    = "DEADFIN_SOURCE_ID"
	sourceInputFD          = 4
)

// MaybeRunFrameSinkTrampoline replaces this process with ffmpeg after changing
// into the descriptor-pinned frame sink inherited from FFmpegFrameExtractor.
// It is a no-op for ordinary process entry. Call it first in main or TestMain.
func MaybeRunFrameSinkTrampoline() {
	fdValue := os.Getenv(frameSinkFDEnvironment)
	if fdValue == "" {
		return
	}
	if err := runFrameSinkTrampoline(fdValue, os.Getenv(frameSinkIDEnvironment), os.Getenv(sourceFDEnvironment), os.Getenv(sourceIDEnvironment)); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "deadfin frame sink trampoline: %v\n", err)
		os.Exit(125)
	}
}

func runFrameSinkTrampoline(fdValue, identityValue, sourceFDValue, sourceIdentityValue string) error {
	fd, err := strconv.Atoi(fdValue)
	if err != nil || fd != frameSinkFD {
		return fmt.Errorf("invalid frame sink fd %q", fdValue)
	}
	wantDevice, wantInode, err := parseFrameSinkIdentity(identityValue)
	if err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("stat frame sink fd: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("frame sink fd is not a directory")
	}
	if uint64(st.Dev) != wantDevice || uint64(st.Ino) != wantInode {
		return fmt.Errorf("frame sink fd identity does not match expected directory")
	}
	if err := unix.Fchdir(fd); err != nil {
		return fmt.Errorf("change to frame sink directory: %w", err)
	}
	if err := unix.Close(fd); err != nil {
		return fmt.Errorf("close frame sink fd: %w", err)
	}
	if sourceFDValue != "" {
		sourceFD, err := strconv.Atoi(sourceFDValue)
		if err != nil || sourceFD != sourceInputFD {
			return fmt.Errorf("invalid source fd %q", sourceFDValue)
		}
		wantDevice, wantInode, err := parseFrameSinkIdentity(sourceIdentityValue)
		if err != nil {
			return fmt.Errorf("invalid source identity: %w", err)
		}
		var source unix.Stat_t
		if err := unix.Fstat(sourceFD, &source); err != nil {
			return fmt.Errorf("stat source fd: %w", err)
		}
		if source.Mode&unix.S_IFMT != unix.S_IFREG {
			return errors.New("source fd is not a regular file")
		}
		if uint64(source.Dev) != wantDevice || uint64(source.Ino) != wantInode {
			return errors.New("source fd identity does not match expected source")
		}
	}
	if len(os.Args) < 2 || strings.TrimSpace(os.Args[1]) == "" {
		return errors.New("missing ffmpeg executable")
	}
	if err := os.Unsetenv(frameSinkFDEnvironment); err != nil {
		return fmt.Errorf("clear frame sink fd environment: %w", err)
	}
	if err := os.Unsetenv(frameSinkIDEnvironment); err != nil {
		return fmt.Errorf("clear frame sink identity environment: %w", err)
	}
	if err := os.Unsetenv(sourceFDEnvironment); err != nil {
		return fmt.Errorf("clear source fd environment: %w", err)
	}
	if err := os.Unsetenv(sourceIDEnvironment); err != nil {
		return fmt.Errorf("clear source identity environment: %w", err)
	}
	return unix.Exec(os.Args[1], os.Args[1:], os.Environ())
}

func parseFrameSinkIdentity(value string) (uint64, uint64, error) {
	device, inode, found := strings.Cut(value, ":")
	if !found || device == "" || inode == "" || strings.Contains(inode, ":") {
		return 0, 0, fmt.Errorf("invalid frame sink identity %q", value)
	}
	parsedDevice, err := strconv.ParseUint(device, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid frame sink device %q: %w", device, err)
	}
	parsedInode, err := strconv.ParseUint(inode, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid frame sink inode %q: %w", inode, err)
	}
	return parsedDevice, parsedInode, nil
}
