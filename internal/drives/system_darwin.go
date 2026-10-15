// SPDX-License-Identifier: GPL-2.0-only

//go:build darwin

package drives

import (
	"context"

	"golang.org/x/sys/unix"
)

// systemMounts reads the mounted filesystems with getfsstat, the source .NET
// DriveInfo.GetDrives reads on macOS.
func systemMounts(ctx context.Context) ([]Mount, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	count, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	stats := make([]unix.Statfs_t, count)
	count, err = unix.Getfsstat(stats, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	mounts := make([]Mount, 0, count)
	for _, stat := range stats[:count] {
		mounts = append(mounts, Mount{
			Path:   unix.ByteSliceToString(stat.Mntonname[:]),
			FSType: unix.ByteSliceToString(stat.Fstypename[:]),
		})
	}
	return mounts, nil
}

func totalSize(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Blocks * uint64(stat.Bsize), nil
}
