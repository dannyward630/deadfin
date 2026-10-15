// SPDX-License-Identifier: GPL-2.0-only

package sqlite

import (
	"fmt"
	"strconv"
)

// CacheOptions sizes the per-connection page cache and the memory map of
// every pool connection. A zero field keeps the SQLite default: a 2 MiB page
// cache and no memory map.
//
// The page cache is per connection, so the reader pool can hold up to
// readerConnections+1 times CacheMiB. A memory map lets a connection read
// database pages from the operating-system page cache without a pread copy;
// the mapped pages count toward the process memory only as shared file cache.
type CacheOptions struct {
	// CacheMiB is the page cache of each connection, in MiB.
	CacheMiB int
	// MmapMiB is the memory-map size of each connection, in MiB.
	MmapMiB int
}

// WithCache sets the page cache and the memory map of every connection.
func WithCache(options CacheOptions) Option {
	return func(opened *openOptions) {
		opened.cache = options
	}
}

// cachePragmas returns the connection pragmas for options.
func cachePragmas(options CacheOptions) ([]string, error) {
	if options.CacheMiB < 0 || options.MmapMiB < 0 {
		return nil, fmt.Errorf("SQLite cache options must not be negative: cache %d MiB, mmap %d MiB", options.CacheMiB, options.MmapMiB)
	}
	var pragmas []string
	if options.CacheMiB > 0 {
		// A negative cache_size is a size in KiB, not a page count.
		pragmas = append(pragmas, "cache_size(-"+strconv.Itoa(options.CacheMiB*1024)+")")
	}
	if options.MmapMiB > 0 {
		pragmas = append(pragmas, "mmap_size("+strconv.FormatInt(int64(options.MmapMiB)<<20, 10)+")")
	}
	return pragmas, nil
}
