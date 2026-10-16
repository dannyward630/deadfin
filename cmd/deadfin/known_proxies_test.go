// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"net/netip"
	"testing"

	"github.com/deadfin-org/deadfin/internal/config"
)

func TestKnownProxiesApplyLiveNetworkConfiguration(t *testing.T) {
	manager := openTestConfiguration(t)
	proxies := &knownProxies{manager: manager}
	addr := netip.MustParseAddr

	// An empty KnownProxies trusts no peer, loopback included
	// (ApiServiceCollectionExtensions.cs:171-176).
	for _, address := range []string{"127.0.0.1", "::1", "10.42.0.7", "192.168.1.20"} {
		if proxies.IsKnownProxy(addr(address)) {
			t.Errorf("default IsKnownProxy(%s) = true, want false", address)
		}
	}

	if err := manager.UpdateNetwork(t.Context(), func(network *config.NetworkConfiguration) error {
		network.KnownProxies = []string{"10.42.0.0/16", "192.168.1.5", "ingress.example", "fd00::/8"}
		return nil
	}); err != nil {
		t.Fatalf("UpdateNetwork: %v", err)
	}
	for _, tc := range []struct {
		address string
		want    bool
	}{
		{"10.42.3.4", true},
		{"192.168.1.5", true},
		{"::ffff:192.168.1.5", true},
		{"fd00::7", true},
		{"127.0.0.1", true},
		{"127.8.0.1", true},
		{"::1", true},
		{"192.168.1.6", false},
		{"10.43.0.1", false},
		{"203.0.113.9", false},
	} {
		if got := proxies.IsKnownProxy(addr(tc.address)); got != tc.want {
			t.Errorf("configured IsKnownProxy(%s) = %t, want %t", tc.address, got, tc.want)
		}
	}
}
