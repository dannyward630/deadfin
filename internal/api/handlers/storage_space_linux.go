// SPDX-License-Identifier: GPL-2.0-only

//go:build linux

package handlers

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// storageSpace resolves symlinks and returns free and used space in bytes.
// Linux uses Frsize as the block size. Returns ok=false on any error.
func storageSpace(path string) (free, used int64, ok bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return 0, 0, false
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(resolved, &stat); err != nil {
		return 0, 0, false
	}
	blockSize := int64(stat.Frsize)
	free = int64(stat.Bavail) * blockSize
	used = (int64(stat.Blocks) - int64(stat.Bavail)) * blockSize
	return free, used, true
}
