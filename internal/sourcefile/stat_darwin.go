// SPDX-License-Identifier: GPL-2.0-only

//go:build darwin

package sourcefile

import "golang.org/x/sys/unix"

func statTimes(st *unix.Stat_t) (mtime, ctime unix.Timespec) {
	return st.Mtim, st.Ctim
}
