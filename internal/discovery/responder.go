// SPDX-License-Identifier: GPL-2.0-only

// Package discovery provides a process-owned UDP discovery responder that
// matches Jellyfin's AutoDiscoveryHost protocol.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
)

// discoveryQuery is the datagram text that triggers a response, matched
// case-insensitively as a substring. Pinned at
// upstream/jellyfin/src/Jellyfin.Networking/AutoDiscoveryHost.cs:76.
const discoveryQuery = "who is JellyfinServer?"

// ServerNameFunc returns the current advertised server name. It is a live
// accessor so discovery and SystemInfo do not drift after a system-config
// update.
type ServerNameFunc func() string

// AddressFunc selects the advertised local IPv4 address for a given requester
// and returns the formatted server URL, or empty if no local route/address
// can be selected.
type AddressFunc func(requester netip.Addr) string

// Config supplies the process-owned dependencies for the UDP responder.
type Config struct {
	// Port is the UDP port to bind. Production uses 7359. Port 0 is the
	// test seam.
	Port int
	// ServerID is the advertised server identifier.
	ServerID string
	// ServerName returns the current advertised server name.
	ServerName ServerNameFunc
	// Address selects the advertised server URL for a requester, or empty
	// if no local route can be selected.
	Address AddressFunc
	// Logger receives diagnostics from the responder loop. The caller logs
	// bind errors. When nil, log.Printf is used.
	Logger *log.Logger
}

// Responder owns the UDP socket and its read goroutine. Construction binds
// the socket and starts the goroutine. Close stops both.
type Responder struct {
	conn    *net.UDPConn
	config  Config
	done    chan struct{}
	closeMu sync.Mutex
	closed  bool
}

// LocalAddr returns the bound UDP address, or nil if the responder is closed.
func (r *Responder) LocalAddr() net.Addr {
	return r.conn.LocalAddr()
}

// New binds a udp4 socket on 0.0.0.0:port and starts the owned read goroutine.
// Because it performs I/O and starts work, it takes ctx first. A canceled
// context prevents binding. A bind failure returns an error so the caller
// can log it and continue.
func New(ctx context.Context, config Config) (*Responder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	addr := &net.UDPAddr{
		IP:   net.IPv4zero,
		Port: config.Port,
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("bind udp4 %s: %w", addr, err)
	}
	r := &Responder{
		conn:   conn,
		config: config,
		done:   make(chan struct{}),
	}
	go r.readLoop()
	return r, nil
}

// Close closes the socket, unblocks ReadFromUDP, and waits for the read
// goroutine to exit. It is safe to call multiple times.
func (r *Responder) Close(ctx context.Context) error {
	r.closeMu.Lock()
	if r.closed {
		r.closeMu.Unlock()
		return nil
	}
	r.closed = true
	r.closeMu.Unlock()

	err := r.conn.Close()
	select {
	case <-r.done:
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
	return err
}

func (r *Responder) readLoop() {
	defer close(r.done)
	buf := make([]byte, 65507)
	for {
		n, remote, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			// A closed-socket error is the terminal shutdown signal.
			// Ordinary receive errors are logged and continued.
			if r.isClosed() {
				return
			}
			if !isTemporaryNetError(err) {
				r.logf("discovery: receive error: %v", err)
			}
			continue
		}
		text := string(buf[:n])
		if !strings.Contains(strings.ToLower(text), strings.ToLower(discoveryQuery)) {
			continue
		}
		r.respond(remote)
	}
}

func (r *Responder) respond(remote *net.UDPAddr) {
	requester, ok := netip.AddrFromSlice(remote.IP)
	if !ok {
		return
	}
	address := r.config.Address(requester)
	if address == "" {
		r.logf("discovery: no local address for requester %s", requester)
		return
	}
	info := serverDiscoveryInfo{
		Address:         address,
		Id:              r.config.ServerID,
		Name:            r.config.ServerName(),
		EndpointAddress: nil,
	}
	payload, err := json.Marshal(info)
	if err != nil {
		return
	}
	if _, err := r.conn.WriteToUDP(payload, remote); err != nil {
		if !r.isClosed() {
			r.logf("discovery: send error: %v", err)
		}
	}
}

// serverDiscoveryInfo serializes to the compact JSON shape from
// upstream/jellyfin/MediaBrowser.Model/ApiClient/ServerDiscoveryInfo.cs:15-41:
// declaration order Address, Id, Name, EndpointAddress with an explicit null.
// The wire bytes match the stock capture of the UDP discovery reply.
type serverDiscoveryInfo struct {
	Address         string  `json:"Address"`
	Id              string  `json:"Id"`
	Name            string  `json:"Name"`
	EndpointAddress *string `json:"EndpointAddress"`
}

func (r *Responder) isClosed() bool {
	r.closeMu.Lock()
	defer r.closeMu.Unlock()
	return r.closed
}

func (r *Responder) logf(format string, args ...any) {
	logger := r.config.Logger
	if logger == nil {
		log.Printf(format, args...)
		return
	}
	logger.Printf(format, args...)
}

// isTemporaryNetError reports whether err is a transient network error that
// the read loop must continue after. The socket-close error from Close is
// not temporary and terminates the loop.
func isTemporaryNetError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

// LocalAddressFunc returns an AddressFunc that selects the local IPv4 address
// reachable from the requester using a connected UDP routing probe, then
// formats http://<selected-ip>:<httpPort><base>. It returns empty when no
// local route or address can be selected.
func LocalAddressFunc(httpPort int, base string) AddressFunc {
	return func(requester netip.Addr) string {
		local := selectLocalIPv4(requester)
		if !local.IsValid() {
			return ""
		}
		return fmt.Sprintf("http://%s:%d%s", local.String(), httpPort, base)
	}
}

// selectLocalIPv4 performs a connected UDP probe to the requester's address
// and returns the local IPv4 address the kernel routes through, or the
// zero netip.Addr if no local route exists.
func selectLocalIPv4(requester netip.Addr) netip.Addr {
	if !requester.IsValid() {
		return netip.Addr{}
	}
	probe, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: requester.AsSlice(), Port: 7359})
	if err != nil {
		return netip.Addr{}
	}
	defer probe.Close()
	local := probe.LocalAddr()
	if local == nil {
		return netip.Addr{}
	}
	addr, err := netip.ParseAddrPort(local.String())
	if err != nil {
		return netip.Addr{}
	}
	return addr.Addr()
}
