// SPDX-License-Identifier: GPL-2.0-only

// Package quickconnect implements the pinned Jellyfin QuickConnect flow as an
// in-memory, mutex-protected state machine. A clock.Clock drives every time
// comparison; wall time and background expiry are never used.
//
// State transitions follow
// Emby.Server.Implementations/QuickConnect/QuickConnectManager.cs:58-228:
// metadata validation precedes the enabled check; a six-digit code in
// [100000,999999] and a 32-byte uppercase hexadecimal secret are generated;
// pending requests are stored by code with duplicate-code last-write-wins
// replacement; a sweep runs on every state operation; and the source's strict
// expiration comparison is used (a pending request remains valid at exactly
// creation + 10 minutes and expires after that boundary).
package quickconnect

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
)

// CodeLength is the number of digits in the user-facing code
// (QuickConnectManager.cs:26).
const CodeLength = 6

// Timeout is the number of minutes a pending request or authorized secret
// remains valid (QuickConnectManager.cs:31).
const Timeout = 10 * time.Minute

// ErrDisabled is returned when QuickConnect is not active on the server. It
// maps to the 401 "Quick connect is disabled" response.
var ErrDisabled = errors.New("Quick connect is disabled")

// ErrNotFound is returned when a code or secret does not match a stored
// request. It maps to the 404 response.
var ErrNotFound = errors.New("Unable to find request")

// ErrAlreadyAuthorized is returned when an already-authorized request is
// authorized again.
var ErrAlreadyAuthorized = errors.New("Request is already authorized")

// ErrUnknownSecret is returned when Connect is polled with a secret that does
// not match any stored request. It maps to the 404 "Unknown secret" response.
var ErrUnknownSecret = errors.New("Unknown secret")

// ErrMetadataRequired is returned when the client/device metadata is missing.
var ErrMetadataRequired = errors.New("login metadata is required")

// ErrMetadataTooLong is returned when a client/device metadata value exceeds
// MaxMetadataLength (divergence D-55).
var ErrMetadataTooLong = errors.New("login metadata is too long")

// ErrTooManyRequests is returned when Initiate would exceed MaxPending or
// MaxPendingPerSource (divergence D-55).
var ErrTooManyRequests = errors.New("too many pending quick connect requests")

// Limits on the anonymous Initiate route (divergence D-55). Pinned
// QuickConnectManager stores every request for ten minutes without a bound, so
// an anonymous caller could fill memory with long metadata. The per-source
// limit is generous because every peer behind one reverse proxy shares a
// source address.
const (
	// MaxMetadataLength bounds each of DeviceId, Device, Client and Version,
	// in bytes.
	MaxMetadataLength = 256
	// MaxPending bounds the stored requests, pending and authorized.
	MaxPending = 256
	// MaxPendingPerSource bounds the stored requests of one source address.
	MaxPendingPerSource = 16
)

// Request is the pending QuickConnect request, ported from
// MediaBrowser.Model/QuickConnect/QuickConnectResult.cs.
type Request struct {
	Authenticated bool
	Secret        string
	Code          string
	DateAdded     time.Time
	DeviceID      string
	DeviceName    string
	AppName       string
	AppVersion    string

	// source is the caller address that initiated the request; it is only
	// counted against MaxPendingPerSource.
	source string
}

// AuthenticationResult is the complete stored authentication snapshot that
// Connect redemption returns without consuming.
type AuthenticationResult = dto.AuthenticationResult

// Entropy generates random bytes. Production uses crypto/rand; tests inject a
// deterministic source.
type Entropy interface {
	Read([]byte) (int, error)
}

// cryptoEntropy is the default Entropy backed by crypto/rand.
type cryptoEntropy struct{}

func (cryptoEntropy) Read(p []byte) (int, error) { return rand.Read(p) }

// Config supplies the independently owned seams for the QuickConnect state
// machine.
type Config struct {
	Clock   clock.Clock
	Enabled func() bool
	// Entropy generates code/secret randomness. Production uses crypto/rand;
	// nil selects crypto/rand. Tests inject a deterministic source.
	Entropy Entropy
}

// Manager is an in-memory QuickConnect state machine. It is safe for
// concurrent use. No background goroutine performs expiry; sweeps happen on
// every state operation.
type Manager struct {
	clock   clock.Clock
	enabled func() bool
	entropy Entropy

	mu                sync.Mutex
	currentRequests   map[string]Request          // keyed by code
	authorizedSecrets map[string]authorizedSecret // keyed by secret
}

type authorizedSecret struct {
	timestamp time.Time
	result    AuthenticationResult
}

// New constructs a QuickConnect state machine from explicit seams. The
// enabled flag is read live from the supplied function, which should proxy
// config.Manager.System().QuickConnectAvailable.
func New(config Config) *Manager {
	entropy := config.Entropy
	if entropy == nil {
		entropy = cryptoEntropy{}
	}
	return &Manager{
		clock:             config.Clock,
		enabled:           config.Enabled,
		entropy:           entropy,
		currentRequests:   make(map[string]Request),
		authorizedSecrets: make(map[string]authorizedSecret),
	}
}

// Enabled reports whether QuickConnect is active, reading the live config
// flag (QuickConnectManager.cs:58).
func (manager *Manager) Enabled() bool {
	return manager.enabled()
}

// Initiate validates the client/device metadata, asserts that QuickConnect is
// active, sweeps expired requests, generates a code and secret, and stores
// the pending request by code (QuickConnectManager.cs:72-95). source is the
// caller address; it and the stored total are bounded (D-55).
//
// Metadata validation runs before the enabled check, matching the source's
// argument-null checks preceding AssertActive.
func (manager *Manager) Initiate(source, deviceID, device, client, version string) (Request, error) {
	if err := validateMetadata(deviceID, device, client, version); err != nil {
		return Request{}, err
	}
	if err := manager.assertNotActive(); err != nil {
		return Request{}, err
	}
	now := manager.clock.Now()
	manager.sweep(now)
	secret, err := generateSecret(manager.entropy)
	if err != nil {
		return Request{}, err
	}
	code, err := generateCode(manager.entropy)
	if err != nil {
		return Request{}, err
	}
	result := Request{
		Secret:     secret,
		Code:       code,
		DateAdded:  now,
		DeviceID:   deviceID,
		DeviceName: device,
		AppName:    client,
		AppVersion: version,
		source:     source,
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, replaced := manager.currentRequests[code]; !replaced {
		fromSource := 0
		for _, pending := range manager.currentRequests {
			if pending.source == source {
				fromSource++
			}
		}
		if len(manager.currentRequests) >= MaxPending || fromSource >= MaxPendingPerSource {
			return Request{}, ErrTooManyRequests
		}
	}
	manager.currentRequests[code] = result
	return result, nil
}

// Connect returns the pending request matching the secret. The request
// remains stored so it can be polled repeatedly until it expires
// (QuickConnectManager.cs:98-111).
func (manager *Manager) Connect(secret string) (Request, error) {
	if err := manager.assertNotActive(); err != nil {
		return Request{}, err
	}
	now := manager.clock.Now()
	manager.sweep(now)

	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, request := range manager.currentRequests {
		if request.Secret == secret {
			return request, nil
		}
	}
	return Request{}, ErrUnknownSecret
}

// Lookup returns the pending request matching code without modifying state.
// It validates that QuickConnect is enabled, sweeps expired entries, and
// checks that the code exists and is not already authorized
// (QuickConnectManager.cs:141-149). The handler uses the returned request's
// device metadata to build the direct-authentication request before calling
// Authorize to store the result.
func (manager *Manager) Lookup(code string) (Request, error) {
	if err := manager.assertNotActive(); err != nil {
		return Request{}, err
	}
	now := manager.clock.Now()
	manager.sweep(now)

	manager.mu.Lock()
	defer manager.mu.Unlock()
	request, ok := manager.currentRequests[code]
	if !ok {
		return Request{}, ErrNotFound
	}
	if request.Authenticated {
		return Request{}, ErrAlreadyAuthorized
	}
	return request, nil
}

// Authorize marks the pending request matching code as authorized for the
// given user, stores the authentication result under the request's secret, and
// moves the pending request's DateAdded one minute into the future so the
// client can observe the authenticated transition before the entry expires
// (QuickConnectManager.cs:136-170).
//
// The authenticationResult is supplied by the caller, who builds it using the
// same user/session projection dependencies as password login.
func (manager *Manager) Authorize(code string, userID dto.UUID, authenticationResult AuthenticationResult) error {
	if err := manager.assertNotActive(); err != nil {
		return err
	}
	now := manager.clock.Now()
	manager.sweep(now)

	manager.mu.Lock()
	defer manager.mu.Unlock()
	request, ok := manager.currentRequests[code]
	if !ok {
		return ErrNotFound
	}
	if request.Authenticated {
		return ErrAlreadyAuthorized
	}
	// Change DateAdded one minute into the future so the client sees the
	// authenticated transition; the pending result thus lasts approximately
	// eleven minutes from authorization (QuickConnectManager.cs:151).
	request.DateAdded = now.Add(1 * time.Minute)
	request.Authenticated = true
	manager.authorizedSecrets[request.Secret] = authorizedSecret{
		timestamp: now,
		result:    authenticationResult,
	}
	manager.currentRequests[code] = request
	return nil
}

// Redeem returns the stored authentication result for the given secret. The
// result is not consumed and can be replayed during its lifetime
// (QuickConnectManager.cs:173-184).
func (manager *Manager) Redeem(secret string) (AuthenticationResult, error) {
	if err := manager.assertNotActive(); err != nil {
		return AuthenticationResult{}, err
	}
	now := manager.clock.Now()
	manager.sweep(now)

	manager.mu.Lock()
	defer manager.mu.Unlock()
	entry, ok := manager.authorizedSecrets[secret]
	if !ok {
		return AuthenticationResult{}, ErrNotFound
	}
	return entry.result, nil
}

// assertNotActive returns ErrDisabled when QuickConnect is not enabled. It
// does not purge stored state; re-enabling exposes only state that remains
// unexpired.
func (manager *Manager) assertNotActive() error {
	if !manager.enabled() {
		return ErrDisabled
	}
	return nil
}

// sweep removes expired pending requests and authorized secrets. Entries
// whose DateAdded or timestamp is before now - Timeout are removed; the
// boundary itself remains valid (QuickConnectManager.cs:198-228).
func (manager *Manager) sweep(now time.Time) {
	minTime := now.Add(-Timeout)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for code, request := range manager.currentRequests {
		if request.DateAdded.Before(minTime) {
			delete(manager.currentRequests, code)
		}
	}
	for secret, entry := range manager.authorizedSecrets {
		if entry.timestamp.Before(minTime) {
			delete(manager.authorizedSecrets, secret)
		}
	}
}

// validateMetadata rejects a missing DeviceId, Device, Client, or Version
// (QuickConnectManager.cs:74-77), and a value longer than MaxMetadataLength
// (D-55). Validation precedes the enabled check.
func validateMetadata(deviceID, device, client, version string) error {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(device) == "" || strings.TrimSpace(client) == "" || strings.TrimSpace(version) == "" {
		return ErrMetadataRequired
	}
	if len(deviceID) > MaxMetadataLength || len(device) > MaxMetadataLength || len(client) > MaxMetadataLength || len(version) > MaxMetadataLength {
		return ErrMetadataTooLong
	}
	return nil
}

// generateSecret produces a 32-byte, uppercase hexadecimal secret
// (QuickConnectManager.cs:186-192).
func generateSecret(entropy Entropy) (string, error) {
	raw := make([]byte, 32)
	if err := readFull(entropy, raw); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(raw)), nil
}

// generateCode produces a six-digit code in [100000, 999999]
// (QuickConnectManager.cs:117-133).
func generateCode(entropy Entropy) (string, error) {
	raw := make([]byte, 4)
	if err := readFull(entropy, raw); err != nil {
		return "", err
	}
	min := int(math.Pow10(CodeLength - 1))
	max := int(math.Pow10(CodeLength))
	// Use the same rejection-sampling approach as the source: redraw while
	// the full uint32 range is hit (scale == uint.MaxValue), so the scale
	// maps uniformly into [min, max).
	scale := uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
	for scale == math.MaxUint32 {
		if err := readFull(entropy, raw); err != nil {
			return "", err
		}
		scale = uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
	}
	code := int(float64(min) + float64(max-min)*(float64(scale)/float64(math.MaxUint32)))
	return fmt.Sprintf("%d", code), nil
}

// readFull fills p completely from entropy, returning an error if the
// reader stops early or fails. A conforming short reader that returns
// fewer bytes than requested must not silently leave zero bytes.
func readFull(entropy Entropy, p []byte) error {
	for offset := 0; offset < len(p); {
		n, err := entropy.Read(p[offset:])
		if err != nil {
			return err
		}
		if n == 0 {
			return errors.New("entropy reader returned zero bytes")
		}
		offset += n
	}
	return nil
}

// DeterministicEntropy is an Entropy backed by a fixed byte sequence, used by
// tests that need deterministic code/secret generation. It cycles through its
// data when exhausted. It is safe for concurrent use.
type DeterministicEntropy struct {
	data []byte
	pos  int
	mu   sync.Mutex
}

// NewDeterministicEntropy returns an Entropy that repeats the given bytes.
func NewDeterministicEntropy(data []byte) *DeterministicEntropy {
	return &DeterministicEntropy{data: data}
}

func (e *DeterministicEntropy) Read(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.data) == 0 {
		return 0, nil
	}
	written := 0
	for written < len(p) {
		n := copy(p[written:], e.data[e.pos:])
		written += n
		e.pos += n
		if e.pos >= len(e.data) {
			e.pos = 0
		}
	}
	return written, nil
}
