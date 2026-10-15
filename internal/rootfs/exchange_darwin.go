// SPDX-License-Identifier: GPL-2.0-only

//go:build darwin

package rootfs

import "golang.org/x/sys/unix"

func exchangeAt(fromFD int, from string, toFD int, to string) error {
	return unix.RenameatxNp(fromFD, from, toFD, to, unix.RENAME_SWAP)
}
