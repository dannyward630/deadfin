// SPDX-License-Identifier: GPL-2.0-only

// Package main is an inert fixture for verify-convention-gate-fixtures.
package main

import "time"

func readsCommandWallClock() time.Time {
	return time.Now()
}
