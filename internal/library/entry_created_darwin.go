// SPDX-License-Identifier: GPL-2.0-only

//go:build darwin

package library

import (
	"os"
	"syscall"
	"time"
)

// entryCreatedAt exposes the creation-time filesystem fact used by Jellyfin's
// resolver when the host provides it. It intentionally returns zero when the
// platform cannot report a birth time rather than substituting another date.
func entryCreatedAt(_ string, info os.FileInfo) time.Time {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}
	}
	created := stat.Birthtimespec
	if created.Sec == 0 && created.Nsec == 0 {
		return time.Time{}
	}
	return time.Unix(created.Sec, created.Nsec).UTC()
}
