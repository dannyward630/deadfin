// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

func TestSQLiteCacheFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		want    sqlite.CacheOptions
		wantErr bool
	}{
		{name: "unset", env: map[string]string{}, want: sqlite.CacheOptions{}},
		{name: "both", env: map[string]string{"DEADFIN_SQLITE_CACHE_MB": "64", "DEADFIN_SQLITE_MMAP_MB": " 512 "}, want: sqlite.CacheOptions{CacheMiB: 64, MmapMiB: 512}},
		{name: "empty", env: map[string]string{"DEADFIN_SQLITE_CACHE_MB": ""}, want: sqlite.CacheOptions{}},
		{name: "negative", env: map[string]string{"DEADFIN_SQLITE_MMAP_MB": "-1"}, wantErr: true},
		{name: "not a number", env: map[string]string{"DEADFIN_SQLITE_CACHE_MB": "64M"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sqliteCacheFromEnv(func(key string) string { return tc.env[key] })
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
