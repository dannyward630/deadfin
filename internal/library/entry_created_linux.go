// SPDX-License-Identifier: GPL-2.0-only

//go:build linux

package library

import (
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// entryCreatedAt exposes the creation-time filesystem fact used by Jellyfin's
// resolver. Linux filesystems and container bind mounts do not universally
// expose STATX_BTIME. FileInfo.CreationTimeUtc, which Jellyfin uses upstream,
// falls back to the older of inode status-change and modification times in
// that case; both are filesystem facts, rather than scan-time substitutions.
func entryCreatedAt(path string, info os.FileInfo) time.Time {
	var status unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &status); err == nil {
		if status.Mask&unix.STATX_BTIME != 0 {
			return statxTime(status.Btime)
		}
		if status.Mask&unix.STATX_CTIME != 0 {
			return earlierFilesystemTime(statxTime(status.Ctime), info.ModTime().UTC())
		}
		return info.ModTime().UTC()
	}

	modified := info.ModTime().UTC()
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return earlierFilesystemTime(time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec).UTC(), modified)
	}
	return modified
}

func statxTime(value unix.StatxTimestamp) time.Time {
	return time.Unix(value.Sec, int64(value.Nsec)).UTC()
}

func earlierFilesystemTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}
