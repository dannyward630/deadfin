// SPDX-License-Identifier: GPL-2.0-only

//go:build darwin

package library

import (
	"os"
	"syscall"
	"time"
)

func entryFileIdentity(info os.FileInfo) FileIdentity {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return FileIdentity{}
	}
	changed := stat.Ctimespec
	return FileIdentity{
		Device:  uint64(stat.Dev),
		Inode:   uint64(stat.Ino),
		Changed: time.Unix(changed.Sec, changed.Nsec).UTC(),
		Valid:   true,
	}
}
