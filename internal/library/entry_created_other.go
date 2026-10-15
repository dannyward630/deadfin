// SPDX-License-Identifier: GPL-2.0-only

//go:build !darwin && !linux

package library

import (
	"os"
	"time"
)

// entryCreatedAt returns no creation fact on platforms whose portable stat
// contract does not expose one. Callers retain the unset value rather than
// guessing from modification time.
func entryCreatedAt(_ string, _ os.FileInfo) time.Time { return time.Time{} }
