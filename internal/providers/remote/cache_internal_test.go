// SPDX-License-Identifier: GPL-2.0-only

package remote

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

func freshHeader(maxAge int) http.Header {
	return http.Header{"Cache-Control": {"max-age=" + strconv.Itoa(maxAge)}}
}

func TestResponseCacheEvictsOldestBeyondBound(t *testing.T) {
	fake := clock.NewFake(time.Unix(0, 0))
	cache := newResponseCache(fake, 2, 0)

	cache.store("a", 200, freshHeader(3600), []byte("a"))
	fake.Advance(time.Second)
	cache.store("b", 200, freshHeader(3600), []byte("b"))
	fake.Advance(time.Second)
	cache.store("c", 200, freshHeader(3600), []byte("c")) // evicts "a", the oldest

	if _, ok := cache.get("a"); ok {
		t.Fatal("cache retained oldest entry past its size bound")
	}
	if _, ok := cache.get("b"); !ok {
		t.Fatal("cache evicted a within-bound entry")
	}
	if _, ok := cache.get("c"); !ok {
		t.Fatal("cache dropped the most recently stored entry")
	}
}

func TestResponseCacheExpiresByInjectedClock(t *testing.T) {
	fake := clock.NewFake(time.Unix(0, 0))
	cache := newResponseCache(fake, 8, 0)
	cache.store("k", 200, freshHeader(60), []byte("v"))

	if _, ok := cache.get("k"); !ok {
		t.Fatal("fresh entry missing immediately after store")
	}
	fake.Advance(61 * time.Second)
	if _, ok := cache.get("k"); ok {
		t.Fatal("expired entry served after its max-age elapsed")
	}
}
