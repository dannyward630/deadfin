// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"log"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/events"
)

// hardwareCapabilities holds the encoder capabilities that apply to the
// current encoding.xml. The ffmpeg binary probe is fixed for the process.
// The device facts (the VAAPI driver) depend on the configured acceleration
// type and device, so Refresh re-runs the device probe when they change.
// Upstream probes them only at startup (MediaEncoder.SetFFmpegPath); Deadfin
// re-probes on save so the dashboard applies without a restart (D-45).
type hardwareCapabilities struct {
	base  encoding.Capabilities
	probe func(context.Context, encoding.Capabilities, config.EncodingOptions) encoding.Capabilities

	mu      sync.RWMutex
	ready   bool
	key     hardwareProbeKey
	current encoding.Capabilities
}

// hardwareProbeKey is the set of encoding options that the device probe reads.
type hardwareProbeKey struct {
	accelerationType string
	vaapiDevice      string
	qsvDevice        string
}

func newHardwareProbeKey(options config.EncodingOptions) hardwareProbeKey {
	return hardwareProbeKey{
		accelerationType: options.HardwareAccelerationType,
		vaapiDevice:      options.VaapiDevice,
		qsvDevice:        options.QsvDevice,
	}
}

// newHardwareCapabilities probes the device facts for options.
func newHardwareCapabilities(ctx context.Context, base encoding.Capabilities, options config.EncodingOptions, probe func(context.Context, encoding.Capabilities, config.EncodingOptions) encoding.Capabilities) *hardwareCapabilities {
	capabilities := &hardwareCapabilities{base: base, probe: probe}
	capabilities.Refresh(ctx, options)
	return capabilities
}

// Current returns the capabilities from the latest probe.
func (capabilities *hardwareCapabilities) Current() encoding.Capabilities {
	capabilities.mu.RLock()
	defer capabilities.mu.RUnlock()
	return capabilities.current
}

// Refresh re-runs the device probe when options change a probed member. It
// reports whether it probed.
func (capabilities *hardwareCapabilities) Refresh(ctx context.Context, options config.EncodingOptions) bool {
	key := newHardwareProbeKey(options)
	capabilities.mu.RLock()
	unchanged := capabilities.ready && capabilities.key == key
	capabilities.mu.RUnlock()
	if unchanged {
		return false
	}
	probed := capabilities.probe(ctx, capabilities.base, options)
	capabilities.mu.Lock()
	capabilities.ready = true
	capabilities.key = key
	capabilities.current = probed
	capabilities.mu.Unlock()
	return true
}

// lanSubnets classifies callers with the configured LocalNetworkSubnets. It
// ports NetworkManager.InitializeLan and IsInLocalNetwork
// (NetworkManager.cs:309-350, :968-1008): an entry is a CIDR prefix or a
// single address, and an entry that starts with "!" excludes its range. With
// no usable LAN entry the existing Deadfin classifier (loopback, private and
// link-local) applies. Loopback is always local. Upstream re-reads the
// configuration on NamedConfigurationUpdated; this type re-parses whenever
// the saved list differs from the cached one, so a save applies to the next
// request.
type lanSubnets struct {
	manager *config.Manager

	mu       sync.Mutex
	source   []string
	included []netip.Prefix
	excluded []netip.Prefix
}

func (lan *lanSubnets) rules() ([]netip.Prefix, []netip.Prefix) {
	if lan == nil || lan.manager == nil {
		return nil, nil
	}
	configured := lan.manager.Network().LocalNetworkSubnets
	lan.mu.Lock()
	defer lan.mu.Unlock()
	if lan.source == nil || !slices.Equal(lan.source, configured) {
		lan.source = append([]string{}, configured...)
		lan.included, lan.excluded = parseLANSubnets(configured)
	}
	return lan.included, lan.excluded
}

func parseLANSubnets(entries []string) (included, excluded []netip.Prefix) {
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		negated := strings.HasPrefix(entry, "!")
		prefix, ok := parseLANSubnet(strings.TrimPrefix(entry, "!"))
		if !ok {
			log.Printf("application: ignoring invalid LocalNetworkSubnets entry %q", entry)
			continue
		}
		if negated {
			excluded = append(excluded, prefix)
		} else {
			included = append(included, prefix)
		}
	}
	return included, excluded
}

func parseLANSubnet(value string) (netip.Prefix, bool) {
	value = strings.TrimSpace(value)
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Masked(), true
	}
	if address, err := netip.ParseAddr(value); err == nil {
		address = address.Unmap()
		return netip.PrefixFrom(address, address.BitLen()), true
	}
	return netip.Prefix{}, false
}

func (lan *lanSubnets) IsInLocalNetwork(address netip.Addr) bool {
	address = address.Unmap()
	if address.IsLoopback() {
		return true
	}
	included, excluded := lan.rules()
	for _, prefix := range excluded {
		if prefix.Contains(address) {
			return false
		}
	}
	if len(included) == 0 {
		return localAddressNetwork{}.IsInLocalNetwork(address)
	}
	for _, prefix := range included {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// configurationConsumer is the single subscriber that applies saved
// configuration to live services, as upstream's NamedConfigurationUpdated and
// ConfigurationUpdated handlers do (ApplicationHost.cs:426-427, :798-831;
// NetworkManager.cs:587-593). It owns one goroutine, which Close stops.
//
// Applied live without a subscriber: every reader takes a fresh snapshot
// from the configuration manager for each request or job (server name,
// metadata language and country, Quick Connect, remote access, bitrate
// limits, encoding options for the next transcode, branding, and
// LocalNetworkSubnets through lanSubnets).
//
// Applied here: the hardware device probe for encoding.xml.
//
// Logged as restart-required: network.xml BaseUrl and AutoDiscovery, which
// the HTTP mount and the discovery responder read once at startup.
type configurationConsumer struct {
	manager      *config.Manager
	hardware     *hardwareCapabilities
	subscription *events.Subscription[config.Change]
	cancel       context.CancelFunc
	done         chan struct{}
	applied      func(config.Change)

	network config.NetworkConfiguration
}

// startConfigurationConsumer subscribes to changes and starts the consumer
// goroutine. applied, when non-nil, runs after each change is handled.
func startConfigurationConsumer(changes *events.Bus[config.Change], manager *config.Manager, hardware *hardwareCapabilities, applied func(config.Change)) *configurationConsumer {
	ctx, cancel := context.WithCancel(context.Background())
	consumer := &configurationConsumer{
		manager:  manager,
		hardware: hardware,
		subscription: changes.SubscribeWith(events.SubscriptionOptions{Buffer: 16}, func(change config.Change) bool {
			return change.File == config.EncodingFile || change.File == config.NetworkFile
		}, nil),
		cancel:  cancel,
		done:    make(chan struct{}),
		applied: applied,
		network: manager.Network(),
	}
	go consumer.run(ctx)
	return consumer
}

func (consumer *configurationConsumer) run(ctx context.Context) {
	defer close(consumer.done)
	for change := range consumer.subscription.Events() {
		consumer.apply(ctx, change)
		if consumer.applied != nil {
			consumer.applied(change)
		}
	}
}

func (consumer *configurationConsumer) apply(ctx context.Context, change config.Change) {
	switch change.File {
	case config.EncodingFile:
		options := consumer.manager.Encoding()
		if consumer.hardware != nil && consumer.hardware.Refresh(ctx, options) {
			log.Printf("application: encoding configuration changed; hardware acceleration %q re-probed for the next transcode", options.HardwareAccelerationType)
		}
	case config.NetworkFile:
		next := consumer.manager.Network()
		previous := consumer.network
		consumer.network = next
		if previous.NormalizedBaseURL() != next.NormalizedBaseURL() {
			log.Printf("application: network BaseUrl changed from %q to %q; restart Deadfin to apply it", previous.NormalizedBaseURL(), next.NormalizedBaseURL())
		}
		if previous.AutoDiscovery != next.AutoDiscovery {
			log.Printf("application: network AutoDiscovery changed to %t; restart Deadfin to apply it", next.AutoDiscovery)
		}
	}
}

// Close cancels an in-flight probe, unsubscribes, and waits for the goroutine.
func (consumer *configurationConsumer) Close(ctx context.Context) error {
	if consumer == nil {
		return nil
	}
	consumer.cancel()
	err := consumer.subscription.Close(ctx)
	select {
	case <-consumer.done:
		return err
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
}
