// SPDX-License-Identifier: GPL-2.0-only

// Package service is an inert fixture for verify-convention-gate-fixtures.
package service

import wallclock "time"

func readsAliasedWallClock() wallclock.Time {
	return wallclock.Now()
}
