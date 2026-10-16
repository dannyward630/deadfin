// SPDX-License-Identifier: GPL-2.0-only

package middleware

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// KnownProxies classifies a peer against the network.xml KnownProxies list.
type KnownProxies interface {
	IsKnownProxy(netip.Addr) bool
}

// RequestIsHTTPS reports whether the client reached Deadfin over HTTPS: the
// connection is TLS, or the scheme after X-Forwarded-Proto from known
// proxies is https (ForwardedScheme).
func RequestIsHTTPS(request *http.Request, proxies KnownProxies) bool {
	if request.TLS != nil {
		return true
	}
	return strings.EqualFold(ForwardedScheme(request, proxies), "https")
}

// ForwardedScheme returns the scheme that X-Forwarded-For and
// X-Forwarded-Proto give the request when they come from known proxies, or
// "" when no forwarded scheme applies. It follows the ASP.NET Core
// ForwardedHeadersMiddleware.ApplyForwarders walk that pinned Jellyfin
// configures from KnownProxies (ApiServiceCollectionExtensions.cs:166-187,
// Startup.cs:182): with no KnownProxies no header is honored. Otherwise the
// entries are read from the right; an entry applies only while the current
// peer (first the transport peer, then the previous entry's
// X-Forwarded-For address) is a known proxy, and an unparsable address or
// scheme stops the walk. ForwardLimit is unset, so the chain has no length
// limit, and RequireHeaderSymmetry is false, so the two headers can have
// different counts.
func ForwardedScheme(request *http.Request, proxies KnownProxies) string {
	if proxies == nil {
		return ""
	}
	forwardedFor := forwardedValues(request.Header, "X-Forwarded-For")
	forwardedProto := forwardedValues(request.Header, "X-Forwarded-Proto")
	entries := max(len(forwardedFor), len(forwardedProto))
	peer := NormalizeClientAddress(request.RemoteAddr)
	scheme := ""
	for i := range entries {
		if !peer.IsValid() || !proxies.IsKnownProxy(peer) {
			break
		}
		if address := entryAt(forwardedFor, i); address != "" {
			parsed, ok := parseForwardedAddress(address)
			if !ok {
				break
			}
			peer = parsed
		}
		if proto := entryAt(forwardedProto, i); proto != "" {
			if !validScheme(proto) {
				break
			}
			scheme = proto
		}
	}
	return scheme
}

// forwardedValues splits every value of a comma-separated forwarding header
// into trimmed entries, in header order.
func forwardedValues(header http.Header, name string) []string {
	var entries []string
	for _, value := range header.Values(name) {
		for entry := range strings.SplitSeq(value, ",") {
			entries = append(entries, strings.TrimSpace(entry))
		}
	}
	return entries
}

// entryAt returns the i-th entry from the right, or "" past the start.
func entryAt(entries []string, i int) string {
	if i >= len(entries) {
		return ""
	}
	return entries[len(entries)-1-i]
}

// parseForwardedAddress parses an X-Forwarded-For entry as IPEndPoint.TryParse
// does: an address, an address with a port, or a bracketed IPv6 address.
func parseForwardedAddress(value string) (netip.Addr, bool) {
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Unmap(), true
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		if address, err := netip.ParseAddr(host); err == nil {
			return address.Unmap(), true
		}
	}
	if address, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")); err == nil {
		return address.Unmap(), true
	}
	return netip.Addr{}, false
}

// validScheme mirrors ForwardedHeadersMiddleware.TryValidateScheme: letters,
// digits, '+', '-' and '.'.
func validScheme(scheme string) bool {
	for _, character := range scheme {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9', character == '+', character == '-', character == '.':
		default:
			return false
		}
	}
	return scheme != ""
}
