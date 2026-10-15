// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"net/http"
	"net/netip"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/events"
)

// fakeHardwareProbe records each device probe and reports a driver for a
// configured VAAPI device, as probeVaapiDriver does.
type fakeHardwareProbe struct {
	mu    sync.Mutex
	calls []config.EncodingOptions
}

func (probe *fakeHardwareProbe) probe(_ context.Context, capabilities encoding.Capabilities, options config.EncodingOptions) encoding.Capabilities {
	probe.mu.Lock()
	probe.calls = append(probe.calls, options)
	probe.mu.Unlock()
	if options.HardwareAccelerationType == encoding.HardwareAccelerationVAAPI && options.VaapiDevice != "" {
		capabilities.VaapiDriver = "iHD"
	}
	return capabilities
}

func (probe *fakeHardwareProbe) count() int {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return len(probe.calls)
}

// The dashboard Playback/Transcoding page posts encoding.xml. The next
// transcode reads the new hardware options at once, and the configuration
// consumer re-probes the device facts, without a restart.
func TestEncodingSaveChangesNextTranscodeHardwareWithoutRestart(t *testing.T) {
	probe := &fakeHardwareProbe{}
	applied := make(chan config.Change, 4)
	base := encoding.Capabilities{EncoderMajorVersion: 7, HWAccels: []string{"qsv", "vaapi"}, Encoders: []string{"h264_vaapi", "libx264"}}
	app, err := newApplicationWithListen(t.Context(), filepath.Join(t.TempDir(), "program-data"), defaultListenAddress,
		withEncodingCapabilities(base),
		withHardwareProbe(probe.probe),
		withConfigurationApplied(func(change config.Change) { applied <- change }),
	)
	if err != nil {
		t.Fatalf("newApplicationWithListen: %v", err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	handler := app.Router()
	configureComposedAdministrator(t, handler)
	token := loginComposedAdministrator(t, handler)

	if got := app.transcodeEncoding.Options().Type; got != "none" {
		t.Fatalf("startup hardware type = %q, want none", got)
	}
	if got := app.transcodeEncoding.CurrentCapabilities().VaapiDriver; got != "" {
		t.Fatalf("startup VAAPI driver = %q, want none", got)
	}
	if got := probe.count(); got != 1 {
		t.Fatalf("startup probes = %d, want 1", got)
	}

	assertAuthenticatedStatus(t, handler, http.MethodPost, "/System/Configuration/encoding",
		[]byte(`{"HardwareAccelerationType":"vaapi","VaapiDevice":"/dev/dri/renderD128","EnableHardwareEncoding":true}`), token, http.StatusNoContent)

	// Encoding options apply synchronously: every job reads the snapshot.
	options := app.transcodeEncoding.Options()
	if options.Type != encoding.HardwareAccelerationVAAPI || !options.EnableHardwareEncoding {
		t.Fatalf("next transcode hardware options = %+v, want vaapi with hardware encoding", options)
	}
	if got := app.transcodeEncoding.Options().HardwareDecodingCodecs; len(got) != 2 {
		t.Fatalf("omitted HardwareDecodingCodecs = %v, want upstream default [h264 vc1]", got)
	}

	select {
	case change := <-applied:
		if change.File != config.EncodingFile {
			t.Fatalf("applied change = %+v, want encoding.xml", change)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("configuration consumer did not apply the encoding change")
	}
	if got := app.transcodeEncoding.CurrentCapabilities().VaapiDriver; got != "iHD" {
		t.Fatalf("VAAPI driver after save = %q, want re-probed iHD", got)
	}
	if got := probe.count(); got != 2 {
		t.Fatalf("probes after save = %d, want 2", got)
	}

	// A save that does not change a probed member does not re-probe.
	assertAuthenticatedStatus(t, handler, http.MethodPost, "/System/Configuration/encoding",
		[]byte(`{"HardwareAccelerationType":"vaapi","VaapiDevice":"/dev/dri/renderD128","H264Crf":20}`), token, http.StatusNoContent)
	<-applied
	if got := probe.count(); got != 2 {
		t.Fatalf("probes after an unrelated save = %d, want 2", got)
	}
}

func TestHardwareCapabilitiesRefreshesOnlyForProbedMembers(t *testing.T) {
	probe := &fakeHardwareProbe{}
	options := config.DefaultEncodingOptions()
	capabilities := newHardwareCapabilities(t.Context(), encoding.Capabilities{HWAccels: []string{"vaapi"}}, options, probe.probe)
	if probe.count() != 1 {
		t.Fatalf("startup probes = %d, want 1", probe.count())
	}
	options.H264Crf = 18
	if capabilities.Refresh(t.Context(), options) {
		t.Fatal("Refresh re-probed for a member the probe does not read")
	}
	options.HardwareAccelerationType = encoding.HardwareAccelerationVAAPI
	if !capabilities.Refresh(t.Context(), options) || capabilities.Current().VaapiDriver != "iHD" {
		t.Fatalf("Refresh after an acceleration change = %+v", capabilities.Current())
	}
	options.VaapiDevice = "/dev/dri/renderD129"
	if !capabilities.Refresh(t.Context(), options) {
		t.Fatal("Refresh did not re-probe after a device change")
	}
}

func TestLANSubnetsApplyLiveLocalNetworkConfiguration(t *testing.T) {
	manager := openTestConfiguration(t)
	lan := &lanSubnets{manager: manager}
	addr := netip.MustParseAddr

	for _, tc := range []struct {
		address string
		want    bool
	}{
		{"127.0.0.1", true},
		{"192.168.1.20", true},
		{"10.1.2.3", true},
		{"203.0.113.9", false},
	} {
		if got := lan.IsInLocalNetwork(addr(tc.address)); got != tc.want {
			t.Errorf("default IsInLocalNetwork(%s) = %t, want %t", tc.address, got, tc.want)
		}
	}

	if err := manager.UpdateNetwork(t.Context(), func(network *config.NetworkConfiguration) error {
		network.LocalNetworkSubnets = []string{"203.0.113.0/24", "!203.0.113.66", "10.0.0.0/8", "!10.9.0.0/16", "not-a-subnet"}
		return nil
	}); err != nil {
		t.Fatalf("UpdateNetwork: %v", err)
	}
	for _, tc := range []struct {
		address string
		want    bool
	}{
		{"127.0.0.1", true},
		{"::ffff:203.0.113.9", true},
		{"203.0.113.66", false},
		{"10.1.2.3", true},
		{"10.9.2.3", false},
		{"192.168.1.20", false},
	} {
		if got := lan.IsInLocalNetwork(addr(tc.address)); got != tc.want {
			t.Errorf("configured IsInLocalNetwork(%s) = %t, want %t", tc.address, got, tc.want)
		}
	}

	if err := manager.UpdateNetwork(t.Context(), func(network *config.NetworkConfiguration) error {
		network.LocalNetworkSubnets = []string{"!192.168.7.0/24"}
		return nil
	}); err != nil {
		t.Fatalf("UpdateNetwork: %v", err)
	}
	if lan.IsInLocalNetwork(addr("192.168.7.4")) || !lan.IsInLocalNetwork(addr("192.168.1.4")) {
		t.Error("exclusion-only configuration must keep the default LAN and exclude its range")
	}
}

func TestConfigurationConsumerAppliesChangesAndStops(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	manager, changes := openTestConfigurationWithBus(t)
	probe := &fakeHardwareProbe{}
	hardware := newHardwareCapabilities(t.Context(), encoding.Capabilities{HWAccels: []string{"vaapi"}}, manager.Encoding(), probe.probe)
	var appliedCount atomic.Int32
	applied := make(chan config.Change, 8)
	consumer := startConfigurationConsumer(changes, manager, hardware, func(change config.Change) {
		appliedCount.Add(1)
		applied <- change
	})

	if err := manager.UpdateBranding(t.Context(), func(branding *config.BrandingOptions) error {
		branding.CustomCss = "body {}"
		return nil
	}); err != nil {
		t.Fatalf("UpdateBranding: %v", err)
	}
	if err := manager.UpdateNetwork(t.Context(), func(network *config.NetworkConfiguration) error {
		network.BaseUrl = "/jf"
		return nil
	}); err != nil {
		t.Fatalf("UpdateNetwork: %v", err)
	}
	if got := <-applied; got.File != config.NetworkFile {
		t.Fatalf("first applied change = %+v, want network.xml (branding is filtered)", got)
	}
	if err := manager.UpdateEncoding(t.Context(), func(options *config.EncodingOptions) error {
		options.HardwareAccelerationType = encoding.HardwareAccelerationVAAPI
		return nil
	}); err != nil {
		t.Fatalf("UpdateEncoding: %v", err)
	}
	if got := <-applied; got.File != config.EncodingFile {
		t.Fatalf("second applied change = %+v, want encoding.xml", got)
	}
	if hardware.Current().VaapiDriver != "iHD" {
		t.Fatalf("hardware after encoding change = %+v", hardware.Current())
	}

	if err := consumer.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := consumer.Close(t.Context()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := appliedCount.Load(); got != 2 {
		t.Fatalf("applied changes = %d, want 2", got)
	}
}

func openTestConfiguration(t *testing.T) *config.Manager {
	t.Helper()
	manager, _ := openTestConfigurationWithBus(t)
	return manager
}

func openTestConfigurationWithBus(t *testing.T) (*config.Manager, *events.Bus[config.Change]) {
	t.Helper()
	paths, err := config.NewPaths(filepath.Join(t.TempDir(), "program-data"))
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	changes := events.NewBus[config.Change](clock.NewFake(time.Unix(0, 0).UTC()))
	t.Cleanup(func() { _ = changes.Close(context.Background()) })
	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("config.Open: %v", err)
	}
	return manager, changes
}
