// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"log"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/deadfin-org/deadfin/internal/config"
)

// knownProxies applies network.xml KnownProxies to the transport peer, as
// pinned ApiServiceCollectionExtensions.ConfigureForwardHeaders builds
// ForwardedHeadersOptions (ApiServiceCollectionExtensions.cs:166-187,
// :281-319). An empty list trusts no peer. A non-empty list trusts each
// address and CIDR subnet entry, and also the ASP.NET Core defaults that
// upstream does not clear in that case: 127.0.0.0/8 and ::1. A host-name
// entry, which upstream resolves through DNS once at startup, is ignored.
// The list is re-parsed whenever the saved list differs from the cached
// one, so a save applies to the next request.
type knownProxies struct {
	manager *config.Manager

	mu       sync.Mutex
	source   []string
	prefixes []netip.Prefix
}

// IsKnownProxy reports whether address may supply forwarded headers.
func (proxies *knownProxies) IsKnownProxy(address netip.Addr) bool {
	address = address.Unmap()
	for _, prefix := range proxies.rules() {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (proxies *knownProxies) rules() []netip.Prefix {
	if proxies == nil || proxies.manager == nil {
		return nil
	}
	configured := proxies.manager.Network().KnownProxies
	proxies.mu.Lock()
	defer proxies.mu.Unlock()
	if proxies.source == nil || !slices.Equal(proxies.source, configured) {
		proxies.source = append([]string{}, configured...)
		proxies.prefixes = parseKnownProxies(configured)
	}
	return proxies.prefixes
}

func parseKnownProxies(entries []string) []netip.Prefix {
	if len(entries) == 0 {
		return nil
	}
	// ForwardedHeadersOptions defaults: KnownIPNetworks 127.0.0.0/8 and
	// KnownProxies ::1.
	prefixes := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	for _, entry := range entries {
		prefix, ok := parseLANSubnet(entry)
		if !ok {
			log.Printf("application: ignoring KnownProxies entry %q; only addresses and CIDR subnets are supported", strings.TrimSpace(entry))
			continue
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}
