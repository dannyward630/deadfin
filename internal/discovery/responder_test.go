// SPDX-License-Identifier: GPL-2.0-only

package discovery_test

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"

	"github.com/deadfin-org/deadfin/internal/discovery"
)

const discoveryPort = 7359

// discoveryConfig returns a test config that binds port 0 (ephemeral) and
// advertises a fixed identity. The address callback returns a fixed URL
// and records the requester address.
func discoveryConfig(t *testing.T, addressFunc discovery.AddressFunc) (discovery.Config, *atomic.Value) {
	t.Helper()
	var requester atomic.Value
	cfg := discovery.Config{
		Port:       0,
		ServerID:   "deadbeef12345678",
		ServerName: func() string { return "TestServer" },
		Address: func(r netip.Addr) string {
			requester.Store(r)
			if addressFunc != nil {
				return addressFunc(r)
			}
			return "http://127.0.0.1:8096"
		},
	}
	return cfg, &requester
}

func startDiscovery(t *testing.T, ctx context.Context, cfg discovery.Config) *discovery.Responder {
	t.Helper()
	r, err := discovery.New(ctx, cfg)
	if err != nil {
		t.Fatalf("discovery.New: %v", err)
	}
	t.Cleanup(func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Close(shutdown)
	})
	return r
}

func discoveryPortAddr(t *testing.T, r *discovery.Responder) *net.UDPAddr {
	t.Helper()
	return r.LocalAddr().(*net.UDPAddr)
}

func sendDiscoveryDatagram(t *testing.T, port int, payload string) []byte {
	t.Helper()
	serverAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	conn, err := net.DialUDP("udp4", nil, serverAddr)
	if err != nil {
		t.Fatalf("dial discovery: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write discovery query: %v", err)
	}

	conn.SetReadDeadline(clock.NewReal().Now().Add(2 * time.Second))
	buf := make([]byte, 1024)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		return nil
	}
	return buf[:n]
}

// TestDiscoveryNormalQueryReturnsExpectedJSON verifies a normal discovery query
// returns the four-field JSON including EndpointAddress:null. The field order
// and null match the stock capture of the UDP discovery reply.
func TestDiscoveryNormalQueryReturnsExpectedJSON(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg, _ := discoveryConfig(t, nil)
	r := startDiscovery(t, ctx, cfg)
	port := discoveryPortAddr(t, r).Port

	reply := sendDiscoveryDatagram(t, port, "who is JellyfinServer?")
	if reply == nil {
		t.Fatalf("no reply to discovery query")
	}

	var info map[string]json.RawMessage
	if err := json.Unmarshal(reply, &info); err != nil {
		t.Fatalf("invalid JSON: %v\nbody: %s", err, reply)
	}

	// Verify field order and values against the stock capture.
	want := `{"Address":"http://127.0.0.1:8096","Id":"deadbeef12345678","Name":"TestServer","EndpointAddress":null}`
	if got := string(reply); got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
}

// TestDiscoveryMixedCaseQueryReturnsResponse verifies a mixed-case substring
// query returns the four-field JSON.
func TestDiscoveryMixedCaseQueryReturnsResponse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg, _ := discoveryConfig(t, nil)
	r := startDiscovery(t, ctx, cfg)
	port := discoveryPortAddr(t, r).Port

	reply := sendDiscoveryDatagram(t, port, "Who IS JELLYFINserver?")
	if reply == nil {
		t.Fatalf("no reply to mixed-case query")
	}

	want := `{"Address":"http://127.0.0.1:8096","Id":"deadbeef12345678","Name":"TestServer","EndpointAddress":null}`
	if got := string(reply); got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
}

// TestDiscoveryNonMatchProducesNoReply verifies a non-matching datagram
// produces no response.
func TestDiscoveryNonMatchProducesNoReply(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg, _ := discoveryConfig(t, nil)
	r := startDiscovery(t, ctx, cfg)
	port := discoveryPortAddr(t, r).Port

	reply := sendDiscoveryDatagram(t, port, "hello world")
	if reply != nil {
		t.Fatalf("unexpected reply to non-match: %s", reply)
	}
}

// TestDiscoveryAddressCallbackReceivesRequester verifies the address callback
// receives the requester address.
func TestDiscoveryAddressCallbackReceivesRequester(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg, requester := discoveryConfig(t, nil)
	r := startDiscovery(t, ctx, cfg)
	port := discoveryPortAddr(t, r).Port

	_ = sendDiscoveryDatagram(t, port, "who is JellyfinServer?")

	got, ok := requester.Load().(netip.Addr)
	if !ok || !got.IsValid() {
		t.Fatalf("address callback did not receive requester")
	}
	if !got.IsLoopback() {
		t.Fatalf("requester = %s, want loopback", got)
	}
}

// TestDiscoveryCloseUnblocksReadAndGoroutineExits verifies Close unblocks the
// read goroutine and the goroutine exits cleanly.
func TestDiscoveryCloseUnblocksReadAndGoroutineExits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg, _ := discoveryConfig(t, nil)
	r, err := discovery.New(ctx, cfg)
	if err != nil {
		t.Fatalf("discovery.New: %v", err)
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Close(shutdown); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Closing again must be safe.
	if err := r.Close(shutdown); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestDiscoveryBindFailureReturnsError verifies that an occupied UDP port
// returns a construction error.
func TestDiscoveryBindFailureReturnsError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Occupy the port.
	occupier, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupier.Close()
	occupiedPort := occupier.LocalAddr().(*net.UDPAddr).Port

	cfg, _ := discoveryConfig(t, nil)
	cfg.Port = occupiedPort
	_, err = discovery.New(ctx, cfg)
	if err == nil {
		t.Fatalf("expected bind error on occupied port %d", occupiedPort)
	}
	if !strings.Contains(err.Error(), "bind") {
		t.Fatalf("error = %v, want text containing `bind`", err)
	}
}

// TestDiscoveryNoResponseWhenAddressEmpty verifies that when the address
// function returns empty, no response is sent.
func TestDiscoveryNoResponseWhenAddressEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg, _ := discoveryConfig(t, func(r netip.Addr) string { return "" })
	r := startDiscovery(t, ctx, cfg)
	port := discoveryPortAddr(t, r).Port

	reply := sendDiscoveryDatagram(t, port, "who is JellyfinServer?")
	if reply != nil {
		t.Fatalf("unexpected reply when address is empty: %s", reply)
	}
}

// TestDiscoveryLargeDatagramReturnsResponse verifies that a datagram larger
// than the previous 1024-byte buffer receives a response when the query
// text appears after byte 1024. The read buffer must hold the full max UDP
// payload so the query is not truncated.
func TestDiscoveryLargeDatagramReturnsResponse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg, _ := discoveryConfig(t, nil)
	r := startDiscovery(t, ctx, cfg)
	port := discoveryPortAddr(t, r).Port

	// Build a 1046-byte datagram: 1024 bytes of padding plus the query.
	padding := strings.Repeat("X", 1024)
	payload := padding + "who is JellyfinServer?"
	if len(payload) < 1046 {
		t.Fatalf("payload too short: %d", len(payload))
	}

	reply := sendDiscoveryDatagram(t, port, payload)
	if reply == nil {
		t.Fatalf("no reply to large datagram")
	}

	want := `{"Address":"http://127.0.0.1:8096","Id":"deadbeef12345678","Name":"TestServer","EndpointAddress":null}`
	if got := string(reply); got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
}
