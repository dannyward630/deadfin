// SPDX-License-Identifier: GPL-2.0-only

//go:build darwin

package trickplay

import "os"

func frameSinkSelfExecPath() (string, error) {
	return os.Executable()
}
