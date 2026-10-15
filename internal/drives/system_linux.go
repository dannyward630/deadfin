// SPDX-License-Identifier: GPL-2.0-only

//go:build linux

package drives

import (
	"context"
	"os"

	"golang.org/x/sys/unix"
)

// systemMounts reads the mount table of the process mount namespace, the
// source .NET DriveInfo.GetDrives reads on Linux.
func systemMounts(ctx context.Context) ([]Mount, error) {
	file, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return ParseMounts(ctx, file)
}

func totalSize(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Blocks * uint64(stat.Bsize), nil
}
