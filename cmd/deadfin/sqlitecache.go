// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// sqliteCacheFromEnv reads the SQLite page cache and memory-map sizes, in
// MiB, from DEADFIN_SQLITE_CACHE_MB and DEADFIN_SQLITE_MMAP_MB. An unset or
// empty variable keeps the SQLite default.
func sqliteCacheFromEnv(getenv func(string) string) (sqlite.CacheOptions, error) {
	var options sqlite.CacheOptions
	for _, setting := range []struct {
		name  string
		value *int
	}{
		{"DEADFIN_SQLITE_CACHE_MB", &options.CacheMiB},
		{"DEADFIN_SQLITE_MMAP_MB", &options.MmapMiB},
	} {
		raw := strings.TrimSpace(getenv(setting.name))
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return sqlite.CacheOptions{}, fmt.Errorf("%s must be a non-negative integer, got %q", setting.name, raw)
		}
		*setting.value = value
	}
	return options, nil
}
