// SPDX-License-Identifier: GPL-2.0-only

package remote

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

// cachedResponse is a stored, replayable copy of an upstream response.
type cachedResponse struct {
	status  int
	header  http.Header
	body    []byte
	expires time.Time
	stored  time.Time
}

// responseCache stores cacheable GET responses in memory keyed by absolute URL.
// Freshness follows the upstream response's own Cache-Control max-age / Expires
// headers (respecting no-store / no-cache), so Deadfin reuses exactly what the
// provider declared reusable. Expiry is measured against an injected clock.
type responseCache struct {
	clock    clock.Clock
	max      int
	fallback time.Duration

	mu      sync.Mutex
	entries map[string]cachedResponse
}

// newResponseCache builds a cache holding at most max entries. fallback is the
// freshness lifetime applied to a cacheable response that carries no max-age or
// Expires of its own; a non-positive fallback means such responses are not
// cached. A non-positive max disables caching entirely.
func newResponseCache(clk clock.Clock, max int, fallback time.Duration) *responseCache {
	if clk == nil {
		clk = clock.NewReal()
	}
	return &responseCache{
		clock:    clk,
		max:      max,
		fallback: fallback,
		entries:  make(map[string]cachedResponse),
	}
}

// get returns a stored response for key when one exists and is still fresh.
func (cache *responseCache) get(key string) (cachedResponse, bool) {
	if cache == nil || cache.max <= 0 {
		return cachedResponse{}, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, found := cache.entries[key]
	if !found {
		return cachedResponse{}, false
	}
	if !cache.clock.Now().Before(entry.expires) {
		delete(cache.entries, key)
		return cachedResponse{}, false
	}
	return entry, true
}

// store records body under key if the response headers permit caching. It
// returns whether the response was stored.
func (cache *responseCache) store(key string, status int, header http.Header, body []byte) bool {
	if cache == nil || cache.max <= 0 {
		return false
	}
	lifetime, ok := freshnessLifetime(header, cache.fallback)
	if !ok {
		return false
	}
	now := cache.clock.Now()
	entry := cachedResponse{
		status:  status,
		header:  header.Clone(),
		body:    body,
		expires: now.Add(lifetime),
		stored:  now,
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.entries[key] = entry
	cache.evictLocked(now)
	return true
}

// evictLocked drops expired entries first, then the oldest-stored entries until
// the cache is within its size bound. Callers hold cache.mu.
func (cache *responseCache) evictLocked(now time.Time) {
	for key, entry := range cache.entries {
		if !now.Before(entry.expires) {
			delete(cache.entries, key)
		}
	}
	for len(cache.entries) > cache.max {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range cache.entries {
			if oldestKey == "" || entry.stored.Before(oldest) {
				oldestKey, oldest = key, entry.stored
			}
		}
		delete(cache.entries, oldestKey)
	}
}

// freshnessLifetime derives how long a response stays fresh from its
// Cache-Control and Expires headers, following the precedence RFC 9111 uses:
// no-store / no-cache forbid reuse, then max-age, then Expires, then the
// caller's fallback. It reports ok=false when the response must not be cached.
func freshnessLifetime(header http.Header, fallback time.Duration) (time.Duration, bool) {
	directives := parseCacheControl(header.Get("Cache-Control"))
	if _, forbidden := directives["no-store"]; forbidden {
		return 0, false
	}
	if _, forbidden := directives["no-cache"]; forbidden {
		return 0, false
	}
	if value, ok := directives["max-age"]; ok {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds <= 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if expires := strings.TrimSpace(header.Get("Expires")); expires != "" {
		when, err := http.ParseTime(expires)
		if err != nil {
			return 0, false
		}
		if date, err := http.ParseTime(header.Get("Date")); err == nil {
			if lifetime := when.Sub(date); lifetime > 0 {
				return lifetime, true
			}
			return 0, false
		}
		if lifetime := time.Until(when); lifetime > 0 {
			return lifetime, true
		}
		return 0, false
	}
	if fallback > 0 {
		return fallback, true
	}
	return 0, false
}

// parseCacheControl splits a Cache-Control header into its lower-cased
// directives, mapping token=value pairs and bare tokens alike.
func parseCacheControl(value string) map[string]string {
	directives := make(map[string]string)
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, argument, found := strings.Cut(part, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if found {
			directives[name] = strings.Trim(strings.TrimSpace(argument), `"`)
		} else {
			directives[name] = ""
		}
	}
	return directives
}
