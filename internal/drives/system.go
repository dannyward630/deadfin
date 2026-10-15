// SPDX-License-Identifier: GPL-2.0-only

package drives

import (
	"context"
	"os"
)

// System lists the drives of the running host.
type System struct{}

// Drives returns the mount points that pass the upstream GetDrives filter.
func (System) Drives(ctx context.Context) ([]string, error) {
	mounts, err := systemMounts(ctx)
	if err != nil {
		return nil, err
	}
	return Filter(mounts, osProbe{}), nil
}

// osProbe reads mount-point facts from the host filesystem.
type osProbe struct{}

func (osProbe) IsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (osProbe) TotalSize(path string) (uint64, error) {
	return totalSize(path)
}
