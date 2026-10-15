// SPDX-License-Identifier: GPL-2.0-only

package config_test

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/events"
)

// Sources: upstream/jellyfin/MediaBrowser.Model/Branding/BrandingOptions.cs:6-29;
// upstream/jellyfin/Emby.Server.Implementations/Branding/BrandingConfigurationFactory.cs:17-22;
// upstream/jellyfin/Emby.Server.Implementations/AppBase/BaseConfigurationManager.cs:271-274,323-352.
//
// The names here are an external persistence contract. Jellyfin loads the named
// configuration store from branding.xml using XmlSerializer, so a local
// round-trip alone is insufficient: the saved document must retain its upstream
// root and field spellings.
func TestManagerPersistsBrandingXMLWithUpstreamDocumentIdentity(t *testing.T) {
	paths := mustPaths(t)
	if err := os.MkdirAll(paths.Config, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	const source = `<?xml version="1.0" encoding="utf-8"?>
<BrandingOptions>
  <LoginDisclaimer>Authorized viewers only.</LoginDisclaimer>
  <CustomCss>body { color: rebeccapurple; }</CustomCss>
  <SplashscreenEnabled>true</SplashscreenEnabled>
  <SplashscreenLocation>/var/lib/jellyfin/splash.png</SplashscreenLocation>
</BrandingOptions>`
	brandingPath := filepath.Join(paths.Config, "branding.xml")
	if err := os.WriteFile(brandingPath, []byte(source), 0o600); err != nil {
		t.Fatalf("write source branding.xml: %v", err)
	}

	changes := newChangeBus(t)
	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("Open(source branding.xml): %v", err)
	}
	want := config.BrandingOptions{
		LoginDisclaimer:      "Authorized viewers only.",
		CustomCss:            "body { color: rebeccapurple; }",
		SplashscreenEnabled:  true,
		SplashscreenLocation: "/var/lib/jellyfin/splash.png",
	}
	if got := manager.Branding(); got != want {
		t.Fatalf("Branding() loaded from source XML = %#v, want %#v", got, want)
	}

	want = config.BrandingOptions{
		LoginDisclaimer:      "No redistribution.",
		CustomCss:            "main { max-width: 80rem; }",
		SplashscreenEnabled:  false,
		SplashscreenLocation: "/srv/media/new-splash.webp",
	}
	subscription := changes.Subscribe(events.SubscriptionOptions{Buffer: 1})
	if err := manager.UpdateBranding(t.Context(), func(branding *config.BrandingOptions) error {
		*branding = want
		return nil
	}); err != nil {
		t.Fatalf("UpdateBranding: %v", err)
	}
	if got := receiveChange(t, subscription.Events()); got != (config.Change{File: "branding.xml"}) {
		t.Fatalf("branding change = %#v, want Change{File: %q}", got, "branding.xml")
	}
	mustNotReceiveChange(t, subscription.Events())
	assertXMLDocumentIdentity(t, brandingPath, "BrandingOptions", []string{
		"LoginDisclaimer", "CustomCss", "SplashscreenEnabled", "SplashscreenLocation",
	})

	reopened, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("reopen saved branding.xml: %v", err)
	}
	if got := reopened.Branding(); got != want {
		t.Fatalf("Branding() after reopen = %#v, want %#v", got, want)
	}
}

// Sources: upstream/jellyfin/MediaBrowser.Common/Net/NetworkConfiguration.cs:126-129,156-159;
// NetworkConfigurationStore.cs:8-22; Emby.Server.Implementations/AppBase/
// BaseConfigurationManager.cs:271-274,323-352.
//
// P1-05 owns the startup remote-access skeleton. BaseUrl publication and the
// broader listener configuration are intentionally left to their later network
// package; this test fixes the persisted root, complete supported field set,
// replacement, reopen, and notification identity that the startup endpoint
// relies on.
func TestManagerPersistsNetworkXMLWithUpstreamDocumentIdentity(t *testing.T) {
	paths := mustPaths(t)
	if err := os.MkdirAll(paths.Config, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	const source = `<?xml version="1.0" encoding="utf-8"?>
<NetworkConfiguration>
  <EnableRemoteAccess>false</EnableRemoteAccess>
  <EnablePublishedServerUriByRequest>true</EnablePublishedServerUriByRequest>
</NetworkConfiguration>`
	networkPath := filepath.Join(paths.Config, "network.xml")
	if err := os.WriteFile(networkPath, []byte(source), 0o600); err != nil {
		t.Fatalf("write source network.xml: %v", err)
	}

	changes := newChangeBus(t)
	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("Open(source network.xml): %v", err)
	}
	want := config.DefaultNetworkConfiguration()
	want.EnableRemoteAccess = false
	want.EnablePublishedServerUriByRequest = true
	if got := manager.Network(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Network() loaded from source XML = %#v, want %#v", got, want)
	}

	want.EnableRemoteAccess = true
	want.EnablePublishedServerUriByRequest = false
	subscription := changes.Subscribe(events.SubscriptionOptions{Buffer: 1})
	if err := manager.UpdateNetwork(t.Context(), func(network *config.NetworkConfiguration) error {
		*network = want
		return nil
	}); err != nil {
		t.Fatalf("UpdateNetwork: %v", err)
	}
	if got := receiveChange(t, subscription.Events()); got != (config.Change{File: "network.xml"}) {
		t.Fatalf("network change = %#v, want Change{File: %q}", got, "network.xml")
	}
	mustNotReceiveChange(t, subscription.Events())
	assertXMLDocumentIdentity(t, networkPath, "NetworkConfiguration", []string{
		"BaseUrl", "EnableHttps", "RequireHttps", "CertificatePath", "CertificatePassword",
		"InternalHttpPort", "InternalHttpsPort", "PublicHttpPort", "PublicHttpsPort",
		"AutoDiscovery", "EnableUPnP", "EnableIPv4", "EnableIPv6", "EnableRemoteAccess",
		"LocalNetworkSubnets", "LocalNetworkAddresses", "KnownProxies", "IgnoreVirtualInterfaces",
		"VirtualInterfaceNames", "EnablePublishedServerUriByRequest", "PublishedServerUriBySubnet",
		"RemoteIPFilter", "IsRemoteIPFilterBlacklist",
	})

	reopened, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("reopen saved network.xml: %v", err)
	}
	if got := reopened.Network(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Network() after reopen = %#v, want %#v", got, want)
	}
}

func TestManagerCorruptNamedConfigurationPreservesSourceAndRestoresReadableDocument(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		root    string
		corrupt string
	}{
		{name: "branding", file: "branding.xml", root: "BrandingOptions", corrupt: `<BrandingOptions><CustomCss>truncated`},
		{name: "network", file: "network.xml", root: "NetworkConfiguration", corrupt: `<NetworkConfiguration><EnableRemoteAccess>true`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := mustPaths(t)
			if err := os.MkdirAll(paths.Config, 0o700); err != nil {
				t.Fatalf("create config directory: %v", err)
			}
			path := filepath.Join(paths.Config, tc.file)
			if err := os.WriteFile(path, []byte(tc.corrupt), 0o600); err != nil {
				t.Fatalf("write corrupt %s: %v", tc.file, err)
			}

			changes := newChangeBus(t)
			if _, err := config.Open(t.Context(), paths, changes); err != nil {
				t.Fatalf("Open(corrupt %s): %v", tc.file, err)
			}
			backup, err := os.ReadFile(path + ".bak")
			if err != nil {
				t.Fatalf("read %s backup: %v", tc.file, err)
			}
			if got := string(backup); got != tc.corrupt {
				t.Fatalf("%s backup = %q, want original bytes %q", tc.file, got, tc.corrupt)
			}
			assertXMLDocumentIdentity(t, path, tc.root, nil)
			if _, err := config.Open(t.Context(), paths, changes); err != nil {
				t.Fatalf("reopen replacement %s: %v", tc.file, err)
			}
		})
	}
}

func assertXMLDocumentIdentity(t *testing.T, path, wantRoot string, wantFields []string) {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	decoder := xml.NewDecoder(bytes.NewReader(source))
	depth := 0
	root := ""
	var fields []string
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode saved %s: %v", path, err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			switch depth {
			case 0:
				root = token.Name.Local
			case 1:
				fields = append(fields, token.Name.Local)
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if root != wantRoot {
		t.Fatalf("saved XML root = %q, want upstream %q", root, wantRoot)
	}
	if wantFields != nil && !reflect.DeepEqual(fields, wantFields) {
		t.Fatalf("saved %s fields = %v, want upstream %v", wantRoot, fields, wantFields)
	}
}

// TestNormalizedBaseURL verifies the pinned normalization from
// upstream/jellyfin/MediaBrowser.Common/Net/NetworkConfiguration.cs:31-54:
// blank or whitespace-only becomes empty, a non-blank value gets one leading
// slash, and one trailing slash is removed. Surrounding whitespace on a
// non-blank value is preserved because the upstream setter never trims.
func TestNormalizedBaseURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		base string
		want string
	}{
		{name: "empty", base: "", want: ""},
		{name: "whitespace", base: "   ", want: ""},
		{name: "plain", base: "/jf", want: "/jf"},
		{name: "no leading slash", base: "jf", want: "/jf"},
		{name: "trailing slash", base: "/jf/", want: "/jf"},
		{name: "no leading slash trailing slash", base: "jf/", want: "/jf"},
		{name: "whitespace padded", base: "  /jf  ", want: "/  /jf  "},
		{name: "double slash", base: "/jf//", want: "/jf/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.NetworkConfiguration{BaseUrl: tc.base}
			if got := cfg.NormalizedBaseURL(); got != tc.want {
				t.Fatalf("NormalizedBaseURL(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

// TestDefaultNetworkConfigurationHasAutoDiscovery verifies the default has
// AutoDiscovery enabled, matching upstream NetworkConfiguration.cs:108.
func TestDefaultNetworkConfigurationHasAutoDiscovery(t *testing.T) {
	cfg := config.DefaultNetworkConfiguration()
	if !cfg.AutoDiscovery {
		t.Fatalf("DefaultNetworkConfiguration AutoDiscovery = false, want true")
	}
	if cfg.BaseUrl != "" {
		t.Fatalf("DefaultNetworkConfiguration BaseUrl = %q, want empty", cfg.BaseUrl)
	}
}

// TestNetworkConfigurationPersistsBaseUrlAndAutoDiscovery verifies BaseUrl and
// AutoDiscovery round-trip through XML.
func TestNetworkConfigurationPersistsBaseUrlAndAutoDiscovery(t *testing.T) {
	paths := mustPaths(t)
	if err := os.MkdirAll(paths.Config, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	const source = `<?xml version="1.0" encoding="utf-8"?>
<NetworkConfiguration>
  <EnableRemoteAccess>true</EnableRemoteAccess>
  <BaseUrl>/jf</BaseUrl>
  <AutoDiscovery>false</AutoDiscovery>
</NetworkConfiguration>`
	networkPath := filepath.Join(paths.Config, "network.xml")
	if err := os.WriteFile(networkPath, []byte(source), 0o600); err != nil {
		t.Fatalf("write source network.xml: %v", err)
	}

	changes := newChangeBus(t)
	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := manager.Network()
	if got.BaseUrl != "/jf" {
		t.Fatalf("BaseUrl = %q, want /jf", got.BaseUrl)
	}
	if got.AutoDiscovery {
		t.Fatalf("AutoDiscovery = true, want false")
	}
	if got.NormalizedBaseURL() != "/jf" {
		t.Fatalf("NormalizedBaseURL = %q, want /jf", got.NormalizedBaseURL())
	}
}
