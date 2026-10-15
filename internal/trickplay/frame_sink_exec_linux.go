// SPDX-License-Identifier: GPL-2.0-only

//go:build linux

package trickplay

func frameSinkSelfExecPath() (string, error) {
	return "/proc/self/exe", nil
}
