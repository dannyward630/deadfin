// SPDX-License-Identifier: GPL-2.0-only

package config

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// Default ports from upstream/jellyfin/MediaBrowser.Common/Net/
// NetworkConfiguration.cs:17-22.
const (
	DefaultHTTPPort  = 8096
	DefaultHTTPSPort = 8920
)

// NetworkConfiguration is the persisted Jellyfin network configuration
// (network.xml). It matches the full public surface and declaration order of
// upstream/jellyfin/MediaBrowser.Common/Net/NetworkConfiguration.cs:12-218,
// so a dashboard save of the Networking page keeps every member it shows.
type NetworkConfiguration struct {
	BaseUrl                           string
	EnableHttps                       bool
	RequireHttps                      bool
	CertificatePath                   string
	CertificatePassword               string
	InternalHttpPort                  int
	InternalHttpsPort                 int
	PublicHttpPort                    int
	PublicHttpsPort                   int
	AutoDiscovery                     bool
	EnableUPnP                        bool
	EnableIPv4                        bool
	EnableIPv6                        bool
	EnableRemoteAccess                bool
	LocalNetworkSubnets               []string
	LocalNetworkAddresses             []string
	KnownProxies                      []string
	IgnoreVirtualInterfaces           bool
	VirtualInterfaceNames             []string
	EnablePublishedServerUriByRequest bool
	PublishedServerUriBySubnet        []string
	RemoteIPFilter                    []string
	IsRemoteIPFilterBlacklist         bool
}

// DefaultNetworkConfiguration returns the upstream member initializers
// (NetworkConfiguration.cs:24-218): ports 8096/8920, AutoDiscovery, IPv4,
// remote access and IgnoreVirtualInterfaces on, VirtualInterfaceNames
// ["veth"], and empty arrays elsewhere. An absent BaseUrl remains empty.
func DefaultNetworkConfiguration() NetworkConfiguration {
	return NetworkConfiguration{
		InternalHttpPort:           DefaultHTTPPort,
		InternalHttpsPort:          DefaultHTTPSPort,
		PublicHttpPort:             DefaultHTTPPort,
		PublicHttpsPort:            DefaultHTTPSPort,
		AutoDiscovery:              true,
		EnableIPv4:                 true,
		EnableRemoteAccess:         true,
		LocalNetworkSubnets:        []string{},
		LocalNetworkAddresses:      []string{},
		KnownProxies:               []string{},
		IgnoreVirtualInterfaces:    true,
		VirtualInterfaceNames:      []string{"veth"},
		PublishedServerUriBySubnet: []string{},
		RemoteIPFilter:             []string{},
	}
}

// NormalizedBaseURL returns the configured BaseUrl after applying the
// pinned normalization from upstream/jellyfin/MediaBrowser.Common/Net/
// NetworkConfiguration.cs:31-54: blank or whitespace-only becomes empty,
// a non-blank value gets one leading slash, and one trailing slash is removed.
// Surrounding whitespace on a non-blank value is preserved, matching the
// upstream setter, which tests IsNullOrWhiteSpace but never trims.
func (configuration NetworkConfiguration) NormalizedBaseURL() string {
	if strings.TrimSpace(configuration.BaseUrl) == "" {
		return ""
	}
	value := configuration.BaseUrl
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	if strings.HasSuffix(value, "/") {
		value = value[:len(value)-1]
	}
	return value
}

// UnmarshalJSON decodes a posted network configuration over the upstream
// defaults and applies the BaseUrl setter normalization, as System.Text.Json
// does when it assigns the property (NetworkConfiguration.cs:31-54).
func (configuration *NetworkConfiguration) UnmarshalJSON(data []byte) error {
	type fields NetworkConfiguration
	decoded := fields(DefaultNetworkConfiguration())
	if err := decodeJSONOverDefaults(data, &decoded); err != nil {
		return err
	}
	*configuration = NetworkConfiguration(decoded)
	configuration.BaseUrl = configuration.NormalizedBaseURL()
	configuration.normalizeArrays()
	return nil
}

// normalizeArrays replaces nil arrays with empty ones. Upstream initializes
// every array, and encoding/xml decodes an empty element to nil.
func (configuration *NetworkConfiguration) normalizeArrays() {
	configuration.LocalNetworkSubnets = nonNilStrings(configuration.LocalNetworkSubnets)
	configuration.LocalNetworkAddresses = nonNilStrings(configuration.LocalNetworkAddresses)
	configuration.KnownProxies = nonNilStrings(configuration.KnownProxies)
	configuration.VirtualInterfaceNames = nonNilStrings(configuration.VirtualInterfaceNames)
	configuration.PublishedServerUriBySubnet = nonNilStrings(configuration.PublishedServerUriBySubnet)
	configuration.RemoteIPFilter = nonNilStrings(configuration.RemoteIPFilter)
}

// clone returns a copy whose arrays do not alias the receiver.
func (configuration NetworkConfiguration) clone() NetworkConfiguration {
	configuration.LocalNetworkSubnets = append([]string{}, configuration.LocalNetworkSubnets...)
	configuration.LocalNetworkAddresses = append([]string{}, configuration.LocalNetworkAddresses...)
	configuration.KnownProxies = append([]string{}, configuration.KnownProxies...)
	configuration.VirtualInterfaceNames = append([]string{}, configuration.VirtualInterfaceNames...)
	configuration.PublishedServerUriBySubnet = append([]string{}, configuration.PublishedServerUriBySubnet...)
	configuration.RemoteIPFilter = append([]string{}, configuration.RemoteIPFilter...)
	return configuration
}

// networkConfigurationXML is the network.xml document in upstream
// declaration order. Upstream's XmlSerializer writes
// <KnownProxies><string>...</string></KnownProxies> and an empty element for
// an empty array. Pointer members record presence, so an absent element keeps
// the caller default and a present array element replaces the default array.
type networkConfigurationXML struct {
	BaseUrl                           *string         `xml:"BaseUrl"`
	EnableHttps                       *bool           `xml:"EnableHttps"`
	RequireHttps                      *bool           `xml:"RequireHttps"`
	CertificatePath                   *string         `xml:"CertificatePath"`
	CertificatePassword               *string         `xml:"CertificatePassword"`
	InternalHttpPort                  *int            `xml:"InternalHttpPort"`
	InternalHttpsPort                 *int            `xml:"InternalHttpsPort"`
	PublicHttpPort                    *int            `xml:"PublicHttpPort"`
	PublicHttpsPort                   *int            `xml:"PublicHttpsPort"`
	AutoDiscovery                     *bool           `xml:"AutoDiscovery"`
	EnableUPnP                        *bool           `xml:"EnableUPnP"`
	EnableIPv4                        *bool           `xml:"EnableIPv4"`
	EnableIPv6                        *bool           `xml:"EnableIPv6"`
	EnableRemoteAccess                *bool           `xml:"EnableRemoteAccess"`
	LocalNetworkSubnets               *stringArrayXML `xml:"LocalNetworkSubnets"`
	LocalNetworkAddresses             *stringArrayXML `xml:"LocalNetworkAddresses"`
	KnownProxies                      *stringArrayXML `xml:"KnownProxies"`
	IgnoreVirtualInterfaces           *bool           `xml:"IgnoreVirtualInterfaces"`
	VirtualInterfaceNames             *stringArrayXML `xml:"VirtualInterfaceNames"`
	EnablePublishedServerUriByRequest *bool           `xml:"EnablePublishedServerUriByRequest"`
	PublishedServerUriBySubnet        *stringArrayXML `xml:"PublishedServerUriBySubnet"`
	RemoteIPFilter                    *stringArrayXML `xml:"RemoteIPFilter"`
	IsRemoteIPFilterBlacklist         *bool           `xml:"IsRemoteIPFilterBlacklist"`
}

// MarshalXML writes the document root used by Jellyfin's network.xml.
func (configuration NetworkConfiguration) MarshalXML(encoder *xml.Encoder, start xml.StartElement) error {
	start.Name.Local = "NetworkConfiguration"
	c := configuration
	document := networkConfigurationXML{
		BaseUrl:                           &c.BaseUrl,
		EnableHttps:                       &c.EnableHttps,
		RequireHttps:                      &c.RequireHttps,
		CertificatePath:                   &c.CertificatePath,
		CertificatePassword:               &c.CertificatePassword,
		InternalHttpPort:                  &c.InternalHttpPort,
		InternalHttpsPort:                 &c.InternalHttpsPort,
		PublicHttpPort:                    &c.PublicHttpPort,
		PublicHttpsPort:                   &c.PublicHttpsPort,
		AutoDiscovery:                     &c.AutoDiscovery,
		EnableUPnP:                        &c.EnableUPnP,
		EnableIPv4:                        &c.EnableIPv4,
		EnableIPv6:                        &c.EnableIPv6,
		EnableRemoteAccess:                &c.EnableRemoteAccess,
		LocalNetworkSubnets:               &stringArrayXML{Values: c.LocalNetworkSubnets},
		LocalNetworkAddresses:             &stringArrayXML{Values: c.LocalNetworkAddresses},
		KnownProxies:                      &stringArrayXML{Values: c.KnownProxies},
		IgnoreVirtualInterfaces:           &c.IgnoreVirtualInterfaces,
		VirtualInterfaceNames:             &stringArrayXML{Values: c.VirtualInterfaceNames},
		EnablePublishedServerUriByRequest: &c.EnablePublishedServerUriByRequest,
		PublishedServerUriBySubnet:        &stringArrayXML{Values: c.PublishedServerUriBySubnet},
		RemoteIPFilter:                    &stringArrayXML{Values: c.RemoteIPFilter},
		IsRemoteIPFilterBlacklist:         &c.IsRemoteIPFilterBlacklist,
	}
	return encoder.EncodeElement(document, start)
}

// UnmarshalXML accepts only the network configuration document root.
// Fields omitted by the source retain the defaults supplied by the caller.
func (configuration *NetworkConfiguration) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "NetworkConfiguration" {
		return fmt.Errorf("config: unexpected network configuration root %q", start.Name.Local)
	}
	var document networkConfigurationXML
	if err := decoder.DecodeElement(&document, &start); err != nil {
		return err
	}
	decoded := *configuration
	if document.BaseUrl != nil {
		decoded.BaseUrl = *document.BaseUrl
	}
	if document.EnableHttps != nil {
		decoded.EnableHttps = *document.EnableHttps
	}
	if document.RequireHttps != nil {
		decoded.RequireHttps = *document.RequireHttps
	}
	if document.CertificatePath != nil {
		decoded.CertificatePath = *document.CertificatePath
	}
	if document.CertificatePassword != nil {
		decoded.CertificatePassword = *document.CertificatePassword
	}
	if document.InternalHttpPort != nil {
		decoded.InternalHttpPort = *document.InternalHttpPort
	}
	if document.InternalHttpsPort != nil {
		decoded.InternalHttpsPort = *document.InternalHttpsPort
	}
	if document.PublicHttpPort != nil {
		decoded.PublicHttpPort = *document.PublicHttpPort
	}
	if document.PublicHttpsPort != nil {
		decoded.PublicHttpsPort = *document.PublicHttpsPort
	}
	if document.AutoDiscovery != nil {
		decoded.AutoDiscovery = *document.AutoDiscovery
	}
	if document.EnableUPnP != nil {
		decoded.EnableUPnP = *document.EnableUPnP
	}
	if document.EnableIPv4 != nil {
		decoded.EnableIPv4 = *document.EnableIPv4
	}
	if document.EnableIPv6 != nil {
		decoded.EnableIPv6 = *document.EnableIPv6
	}
	if document.EnableRemoteAccess != nil {
		decoded.EnableRemoteAccess = *document.EnableRemoteAccess
	}
	decoded.LocalNetworkSubnets = replaceStrings(decoded.LocalNetworkSubnets, document.LocalNetworkSubnets)
	decoded.LocalNetworkAddresses = replaceStrings(decoded.LocalNetworkAddresses, document.LocalNetworkAddresses)
	decoded.KnownProxies = replaceStrings(decoded.KnownProxies, document.KnownProxies)
	if document.IgnoreVirtualInterfaces != nil {
		decoded.IgnoreVirtualInterfaces = *document.IgnoreVirtualInterfaces
	}
	decoded.VirtualInterfaceNames = replaceStrings(decoded.VirtualInterfaceNames, document.VirtualInterfaceNames)
	if document.EnablePublishedServerUriByRequest != nil {
		decoded.EnablePublishedServerUriByRequest = *document.EnablePublishedServerUriByRequest
	}
	decoded.PublishedServerUriBySubnet = replaceStrings(decoded.PublishedServerUriBySubnet, document.PublishedServerUriBySubnet)
	decoded.RemoteIPFilter = replaceStrings(decoded.RemoteIPFilter, document.RemoteIPFilter)
	if document.IsRemoteIPFilterBlacklist != nil {
		decoded.IsRemoteIPFilterBlacklist = *document.IsRemoteIPFilterBlacklist
	}
	decoded.normalizeArrays()
	*configuration = decoded
	return nil
}

// replaceStrings returns the decoded array when its element was present and
// the caller default otherwise.
func replaceStrings(fallback []string, decoded *stringArrayXML) []string {
	if decoded == nil {
		return fallback
	}
	return append([]string{}, decoded.Values...)
}
