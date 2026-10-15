// SPDX-License-Identifier: GPL-2.0-only

package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenAppliesCacheOptionsToEveryConnection(t *testing.T) {
	opened, err := Open(t.Context(), filepath.Join(t.TempDir(), "deadfin.db"), WithCache(CacheOptions{CacheMiB: 32, MmapMiB: 256}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	for name, pool := range map[string]*sql.DB{"writer": opened.Writer(), "reader": opened.Reader()} {
		var cacheSize, mmapSize int64
		if err := pool.QueryRowContext(t.Context(), "PRAGMA cache_size").Scan(&cacheSize); err != nil {
			t.Fatalf("%s cache_size: %v", name, err)
		}
		if err := pool.QueryRowContext(t.Context(), "PRAGMA mmap_size").Scan(&mmapSize); err != nil {
			t.Fatalf("%s mmap_size: %v", name, err)
		}
		if cacheSize != -32*1024 {
			t.Errorf("%s cache_size = %d, want %d", name, cacheSize, -32*1024)
		}
		if mmapSize != 256<<20 {
			t.Errorf("%s mmap_size = %d, want %d", name, mmapSize, 256<<20)
		}
	}
}

func TestOpenKeepsSQLiteCacheDefaultsWithoutOptions(t *testing.T) {
	opened, err := Open(t.Context(), filepath.Join(t.TempDir(), "deadfin.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	var cacheSize, mmapSize int64
	if err := opened.Reader().QueryRowContext(t.Context(), "PRAGMA cache_size").Scan(&cacheSize); err != nil {
		t.Fatalf("cache_size: %v", err)
	}
	if err := opened.Reader().QueryRowContext(t.Context(), "PRAGMA mmap_size").Scan(&mmapSize); err != nil {
		t.Fatalf("mmap_size: %v", err)
	}
	if cacheSize != -2000 || mmapSize != 0 {
		t.Errorf("defaults: cache_size = %d, mmap_size = %d; want -2000, 0", cacheSize, mmapSize)
	}
}

func TestOpenRejectsNegativeCacheOptions(t *testing.T) {
	if _, err := Open(t.Context(), filepath.Join(t.TempDir(), "deadfin.db"), WithCache(CacheOptions{CacheMiB: -1})); err == nil {
		t.Fatal("Open accepted a negative cache size")
	}
}
