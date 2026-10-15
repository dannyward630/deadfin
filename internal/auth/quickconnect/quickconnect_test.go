// SPDX-License-Identifier: GPL-2.0-only

package quickconnect_test

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/auth/quickconnect"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
)

var (
	_ quickconnect.Entropy = (*quickconnect.DeterministicEntropy)(nil)
)

// newManager constructs a QuickConnect manager with a fake clock, a mutable
// enabled flag, and deterministic entropy so code/secret generation is
// repeatable.
func newManager(start time.Time, enabled bool, entropy *quickconnect.DeterministicEntropy) (*quickconnect.Manager, *clock.Fake, *bool) {
	fake := clock.NewFake(start)
	flag := enabled
	manager := quickconnect.New(quickconnect.Config{
		Clock:   fake,
		Enabled: func() bool { return flag },
		Entropy: entropy,
	})
	return manager, fake, &flag
}

// allZeroEntropy returns all-zero bytes so the code and secret are
// deterministic: the secret is 64 '0' characters and the code is the minimum
// value (100000).
func allZeroEntropy() *quickconnect.DeterministicEntropy {
	return quickconnect.NewDeterministicEntropy(make([]byte, 256))
}

func validMetadata() (string, string, string, string, string) {
	return "192.0.2.10", "test-device-id", "test-device", "test-client", "1.0.0"
}

func TestQuickConnectEnabledReadsLiveConfigFlag(t *testing.T) {
	manager, _, flag := newManager(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC), true, allZeroEntropy())
	if !manager.Enabled() {
		t.Fatal("enabled = false, want true")
	}
	*flag = false
	if manager.Enabled() {
		t.Fatal("enabled = true after flag flip, want false")
	}
}

func TestQuickConnectInitiateGeneratesSixDigitCodeAnd64CharUppercaseHexSecret(t *testing.T) {
	manager, _, _ := newManager(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC), true, allZeroEntropy())
	result, err := manager.Initiate(validMetadata())
	if err != nil {
		t.Fatalf("Initiate error: %v", err)
	}
	if len(result.Code) != 6 {
		t.Fatalf("code length = %d, want 6 (code=%q)", len(result.Code), result.Code)
	}
	for _, r := range result.Code {
		if r < '0' || r > '9' {
			t.Fatalf("code %q contains non-digit %q", result.Code, r)
		}
	}
	n := 0
	for _, r := range result.Code {
		n = n*10 + int(r-'0')
	}
	if n < 100000 || n > 999999 {
		t.Fatalf("code = %d, want in [100000,999999]", n)
	}
	if len(result.Secret) != 64 {
		t.Fatalf("secret length = %d, want 64", len(result.Secret))
	}
	for _, r := range result.Secret {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'F')) {
			t.Fatalf("secret %q contains non-hex-uppercase %q", result.Secret, r)
		}
	}
}

func TestQuickConnectInitiateStoresByCodeWithDuplicateReplacement(t *testing.T) {
	// Craft entropy that produces the same code on both Initiate calls but
	// different secrets, so duplicate-code last-write-wins replacement is
	// observable: the old secret no longer resolves after the second Initiate.
	// generateSecret reads 32 bytes, generateCode reads 4 bytes.
	// First Initiate: bytes 0-31 = 0x01 (secret A), bytes 32-35 = 0x00 (code 100000).
	// Second Initiate: bytes 36-67 = 0x02 (secret B), bytes 68-71 = 0x00 (code 100000).
	data := make([]byte, 72)
	for i := 0; i < 32; i++ {
		data[i] = 0x01
	}
	// bytes 32-35 already zero (code = 100000)
	for i := 36; i < 68; i++ {
		data[i] = 0x02
	}
	// bytes 68-71 already zero (code = 100000)
	entropy := quickconnect.NewDeterministicEntropy(data)
	manager, _, _ := newManager(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC), true, entropy)

	first, err := manager.Initiate(validMetadata())
	if err != nil {
		t.Fatalf("Initiate first: %v", err)
	}
	polled, err := manager.Connect(first.Secret)
	if err != nil {
		t.Fatalf("Connect first: %v", err)
	}
	if polled.Code != first.Code {
		t.Fatalf("Connect returned code %q, want %q", polled.Code, first.Code)
	}

	second, err := manager.Initiate(validMetadata())
	if err != nil {
		t.Fatalf("Initiate second: %v", err)
	}
	if second.Code != first.Code {
		t.Fatalf("duplicate code = %q, want %q (same code from crafted entropy)", second.Code, first.Code)
	}
	if second.Secret == first.Secret {
		t.Fatalf("secrets are identical; crafted entropy should produce different secrets")
	}
	// The old secret no longer resolves; the new one does.
	if _, err := manager.Connect(first.Secret); !errors.Is(err, quickconnect.ErrUnknownSecret) {
		t.Fatalf("Connect old secret = %v, want ErrUnknownSecret after duplicate-code replacement", err)
	}
	polled2, err := manager.Connect(second.Secret)
	if err != nil {
		t.Fatalf("Connect second: %v", err)
	}
	if polled2.Secret != second.Secret {
		t.Fatalf("Connect returned secret %q, want %q", polled2.Secret, second.Secret)
	}
}

func TestQuickConnectInitiateMetadataValidationBeforeEnabledCheck(t *testing.T) {
	// Disabled but with invalid metadata: metadata error should take priority.
	manager, _, _ := newManager(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC), false, allZeroEntropy())
	_, err := manager.Initiate("192.0.2.10", "", "test-device", "test-client", "1.0.0")
	if !errors.Is(err, quickconnect.ErrMetadataRequired) {
		t.Fatalf("Initiate with missing DeviceId on disabled server = %v, want ErrMetadataRequired", err)
	}
}

func TestQuickConnectInitiateRejectsDisabledServerWith401(t *testing.T) {
	manager, _, _ := newManager(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC), false, allZeroEntropy())
	_, err := manager.Initiate(validMetadata())
	if !errors.Is(err, quickconnect.ErrDisabled) {
		t.Fatalf("Initiate on disabled server = %v, want ErrDisabled", err)
	}
}

func TestQuickConnectConnectRejectsDisabledServer(t *testing.T) {
	manager, _, _ := newManager(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC), false, allZeroEntropy())
	_, err := manager.Connect("any-secret")
	if !errors.Is(err, quickconnect.ErrDisabled) {
		t.Fatalf("Connect on disabled server = %v, want ErrDisabled", err)
	}
}

func TestQuickConnectConnectUnknownSecretReturns404(t *testing.T) {
	manager, _, _ := newManager(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC), true, allZeroEntropy())
	_, err := manager.Connect("unknown-secret")
	if !errors.Is(err, quickconnect.ErrUnknownSecret) {
		t.Fatalf("Connect unknown secret = %v, want ErrUnknownSecret", err)
	}
}

func TestQuickConnectAuthorizeStoresAuthenticationResultAndMovesDateAddedOneMinuteForward(t *testing.T) {
	start := time.Date(2026, 8, 8, 17, 51, 24, 0, time.UTC)
	manager, _, _ := newManager(start, true, allZeroEntropy())
	result, err := manager.Initiate(validMetadata())
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}
	originalDateAdded := result.DateAdded

	authResult := dto.AuthenticationResult{AccessToken: "test-token", ServerID: "test-server"}
	if err := manager.Authorize(result.Code, dto.UUID{0x01}, authResult); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	// The pending request's DateAdded moved one minute forward.
	polled, err := manager.Connect(result.Secret)
	if err != nil {
		t.Fatalf("Connect after authorize: %v", err)
	}
	if !polled.Authenticated {
		t.Fatal("Connect after authorize: Authenticated = false, want true")
	}
	wantDateAdded := originalDateAdded.Add(1 * time.Minute)
	if !polled.DateAdded.Equal(wantDateAdded) {
		t.Fatalf("DateAdded after authorize = %v, want %v (original + 1min)", polled.DateAdded, wantDateAdded)
	}

	// The authorized secret is replayable via Redeem.
	redeemed, err := manager.Redeem(result.Secret)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if redeemed.AccessToken != "test-token" {
		t.Fatalf("Redeem AccessToken = %q, want %q", redeemed.AccessToken, "test-token")
	}
	// Second redemption returns the same result (not consumed).
	redeemed2, err := manager.Redeem(result.Secret)
	if err != nil {
		t.Fatalf("Redeem second: %v", err)
	}
	if redeemed2.AccessToken != redeemed.AccessToken {
		t.Fatalf("Second redemption AccessToken = %q, want %q (byte-identical replay)", redeemed2.AccessToken, redeemed.AccessToken)
	}
}

func TestQuickConnectAuthorizeUnknownCodeReturns404(t *testing.T) {
	manager, _, _ := newManager(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC), true, allZeroEntropy())
	err := manager.Authorize("000000", dto.UUID{0x01}, dto.AuthenticationResult{})
	if !errors.Is(err, quickconnect.ErrNotFound) {
		t.Fatalf("Authorize unknown code = %v, want ErrNotFound", err)
	}
}

func TestQuickConnectAuthorizeAlreadyAuthorizedReturnsError(t *testing.T) {
	manager, _, _ := newManager(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC), true, allZeroEntropy())
	result, err := manager.Initiate(validMetadata())
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}
	if err := manager.Authorize(result.Code, dto.UUID{0x01}, dto.AuthenticationResult{}); err != nil {
		t.Fatalf("first Authorize: %v", err)
	}
	err = manager.Authorize(result.Code, dto.UUID{0x01}, dto.AuthenticationResult{})
	if !errors.Is(err, quickconnect.ErrAlreadyAuthorized) {
		t.Fatalf("second Authorize = %v, want ErrAlreadyAuthorized", err)
	}
}

func TestQuickConnectAuthorizeRejectsDisabledServer(t *testing.T) {
	manager, _, _ := newManager(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC), false, allZeroEntropy())
	err := manager.Authorize("000000", dto.UUID{0x01}, dto.AuthenticationResult{})
	if !errors.Is(err, quickconnect.ErrDisabled) {
		t.Fatalf("Authorize on disabled server = %v, want ErrDisabled", err)
	}
}

func TestQuickConnectRedeemRejectsDisabledServer(t *testing.T) {
	manager, _, _ := newManager(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC), false, allZeroEntropy())
	_, err := manager.Redeem("any-secret")
	if !errors.Is(err, quickconnect.ErrDisabled) {
		t.Fatalf("Redeem on disabled server = %v, want ErrDisabled", err)
	}
}

func TestQuickConnectRedeemUnknownSecretReturns404(t *testing.T) {
	manager, _, _ := newManager(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC), true, allZeroEntropy())
	_, err := manager.Redeem("unknown-secret")
	if !errors.Is(err, quickconnect.ErrNotFound) {
		t.Fatalf("Redeem unknown secret = %v, want ErrNotFound", err)
	}
}

func TestQuickConnectPendingRequestExpiresAtExactlyCreationPlus10Minutes(t *testing.T) {
	start := time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC)
	manager, fake, _ := newManager(start, true, allZeroEntropy())
	result, err := manager.Initiate(validMetadata())
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}

	// At exactly creation + 10 minutes, the request is still valid (strict
	// comparison: DateAdded < minTime, not <=).
	fake.Advance(10 * time.Minute)
	if _, err := manager.Connect(result.Secret); err != nil {
		t.Fatalf("Connect at exactly +10min: %v, want nil (boundary is valid)", err)
	}

	// After +10 minutes, the request expires.
	fake.Advance(1 * time.Millisecond)
	if _, err := manager.Connect(result.Secret); !errors.Is(err, quickconnect.ErrUnknownSecret) {
		t.Fatalf("Connect after +10min: %v, want ErrUnknownSecret", err)
	}
}

func TestQuickConnectPostAuthorizePendingLifetimeIsApproximately11Minutes(t *testing.T) {
	start := time.Date(2026, 8, 8, 17, 51, 24, 0, time.UTC)
	manager, fake, _ := newManager(start, true, allZeroEntropy())
	result, err := manager.Initiate(validMetadata())
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}
	if err := manager.Authorize(result.Code, dto.UUID{0x01}, dto.AuthenticationResult{}); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	// Authorize moves DateAdded to T+1 minute. Because sweep removes an
	// entry only when DateAdded is before now−10 minutes, the pending
	// request is valid at T+11 minutes and expires immediately after that
	// boundary.

	// Advance to T_authorize + 11min (the pending result's boundary).
	fake.Advance(11 * time.Minute)
	if _, err := manager.Connect(result.Secret); err != nil {
		t.Fatalf("Connect at authorize+11min: %v, want nil (boundary is valid)", err)
	}

	// One millisecond past: expired.
	fake.Advance(1 * time.Millisecond)
	if _, err := manager.Connect(result.Secret); !errors.Is(err, quickconnect.ErrUnknownSecret) {
		t.Fatalf("Connect after authorize+11min: %v, want ErrUnknownSecret", err)
	}
}

func TestQuickConnectAuthorizedSecretExpiresAtAuthorizePlus10Minutes(t *testing.T) {
	start := time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC)
	manager, fake, _ := newManager(start, true, allZeroEntropy())
	result, err := manager.Initiate(validMetadata())
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}
	if err := manager.Authorize(result.Code, dto.UUID{0x01}, dto.AuthenticationResult{AccessToken: "test-token"}); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	// At authorize + 10 minutes, the authorized secret is still valid.
	fake.Advance(10 * time.Minute)
	if _, err := manager.Redeem(result.Secret); err != nil {
		t.Fatalf("Redeem at authorize+10min: %v, want nil (boundary is valid)", err)
	}

	// One millisecond past: expired (404).
	fake.Advance(1 * time.Millisecond)
	if _, err := manager.Redeem(result.Secret); !errors.Is(err, quickconnect.ErrNotFound) {
		t.Fatalf("Redeem after authorize+10min: %v, want ErrNotFound", err)
	}
}

func TestQuickConnectDisablingDoesNotPurgeStateAndReEnablingExposesUnexpiredState(t *testing.T) {
	start := time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC)
	manager, _, flag := newManager(start, true, allZeroEntropy())
	result, err := manager.Initiate(validMetadata())
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}

	// Disable: Enabled returns false, other routes reject, but state persists.
	*flag = false
	if manager.Enabled() {
		t.Fatal("Enabled = true after disable, want false")
	}
	if _, err := manager.Initiate(validMetadata()); !errors.Is(err, quickconnect.ErrDisabled) {
		t.Fatalf("Initiate on disabled = %v, want ErrDisabled", err)
	}
	if _, err := manager.Connect(result.Secret); !errors.Is(err, quickconnect.ErrDisabled) {
		t.Fatalf("Connect on disabled = %v, want ErrDisabled", err)
	}
	if err := manager.Authorize(result.Code, dto.UUID{0x01}, dto.AuthenticationResult{}); !errors.Is(err, quickconnect.ErrDisabled) {
		t.Fatalf("Authorize on disabled = %v, want ErrDisabled", err)
	}
	if _, err := manager.Redeem(result.Secret); !errors.Is(err, quickconnect.ErrDisabled) {
		t.Fatalf("Redeem on disabled = %v, want ErrDisabled", err)
	}

	// Re-enable: the unexpired request is still there.
	*flag = true
	polled, err := manager.Connect(result.Secret)
	if err != nil {
		t.Fatalf("Connect after re-enable: %v, want nil (state was not purged)", err)
	}
	if polled.Code != result.Code {
		t.Fatalf("Connect after re-enable code = %q, want %q", polled.Code, result.Code)
	}
}

func TestQuickConnectConcurrentInitiateAndConnectAreRaceFree(t *testing.T) {
	start := time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC)
	manager, _, _ := newManager(start, true, quickconnect.NewDeterministicEntropy(make([]byte, 1024)))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := manager.Initiate(validMetadata())
			if err != nil {
				t.Errorf("Initiate: %v", err)
				return
			}
			_, _ = manager.Connect(result.Secret)
		}()
	}
	wg.Wait()
}

// shortReaderEntropy returns one byte per Read call, exercising the
// full-read loop in generateSecret and generateCode. A conforming short
// reader that returns fewer bytes than requested must not silently leave
// zero bytes in the buffer.
type shortReaderEntropy struct {
	data []byte
	pos  int
}

func (e *shortReaderEntropy) Read(p []byte) (int, error) {
	if e.pos >= len(e.data) {
		return 0, io.EOF
	}
	n := copy(p, e.data[e.pos:e.pos+1])
	e.pos += n
	return n, nil
}

func TestQuickConnectInitiateRejectsShortReadEntropy(t *testing.T) {
	// A short reader that returns one byte at a time must still fill the
	// full buffer or return an error — zero bytes must not silently remain
	// in the generated code or secret.
	entropy := &shortReaderEntropy{data: make([]byte, 1024)}
	manager := quickconnect.New(quickconnect.Config{
		Clock:   clock.NewFake(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC)),
		Enabled: func() bool { return true },
		Entropy: entropy,
	})
	result, err := manager.Initiate("192.0.2.10", "device-id", "device", "client", "1.0.0")
	if err != nil {
		t.Fatalf("Initiate with short reader: %v", err)
	}
	// The secret must be 64 uppercase hex characters, not zeros from a
	// short read that left bytes uninitialised.
	if len(result.Secret) != 64 {
		t.Fatalf("secret length = %d, want 64", len(result.Secret))
	}
	for _, c := range result.Secret {
		if c < '0' || (c > '9' && c < 'A') || c > 'F' {
			t.Fatalf("secret contains non-hex character %q in %q", c, result.Secret)
		}
	}
	n, err := strconv.Atoi(result.Code)
	if err != nil {
		t.Fatalf("code = %q, not a number", result.Code)
	}
	if n < 100000 || n > 999999 {
		t.Fatalf("code = %d, want [100000, 999999]", n)
	}

	// A short reader that exhausts its data mid-generation must return an
	// error rather than producing a zero-padded secret or code.
	exhausted := &shortReaderEntropy{data: make([]byte, 10)}
	exhaustedManager := quickconnect.New(quickconnect.Config{
		Clock:   clock.NewFake(time.Date(2026, 8, 8, 17, 0, 0, 0, time.UTC)),
		Enabled: func() bool { return true },
		Entropy: exhausted,
	})
	_, err = exhaustedManager.Initiate("192.0.2.10", "device-id", "device", "client", "1.0.0")
	if err == nil {
		t.Fatal("Initiate with exhausted reader succeeded, want error")
	}
}

// distinctEntropy yields a new code on every Initiate: each 4-byte code read
// returns a scale that is 10000 steps above the previous one, wider than the
// 2^32/900000 scale step of one code.
type distinctEntropy struct {
	mu    sync.Mutex
	scale uint32
}

func (e *distinctEntropy) Read(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(p) == 4 {
		e.scale += 10000
		binary.BigEndian.PutUint32(p, e.scale)
		return 4, nil
	}
	for i := range p {
		p[i] = byte(e.scale >> 8)
	}
	return len(p), nil
}

func TestQuickConnectInitiateBoundsMetadataLength(t *testing.T) {
	manager, _, _ := newManager(time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC), true, allZeroEntropy())
	long := strings.Repeat("x", quickconnect.MaxMetadataLength+1)
	for _, metadata := range [][4]string{
		{long, "device", "client", "1.0.0"},
		{"device-id", long, "client", "1.0.0"},
		{"device-id", "device", long, "1.0.0"},
		{"device-id", "device", "client", long},
	} {
		if _, err := manager.Initiate("192.0.2.10", metadata[0], metadata[1], metadata[2], metadata[3]); !errors.Is(err, quickconnect.ErrMetadataTooLong) {
			t.Fatalf("Initiate with an overlong value = %v, want ErrMetadataTooLong", err)
		}
	}
	exact := strings.Repeat("x", quickconnect.MaxMetadataLength)
	if _, err := manager.Initiate("192.0.2.10", exact, exact, exact, exact); err != nil {
		t.Fatalf("Initiate at the length limit: %v", err)
	}
}

func TestQuickConnectInitiateBoundsPendingRequests(t *testing.T) {
	fake := clock.NewFake(time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	manager := quickconnect.New(quickconnect.Config{Clock: fake, Enabled: func() bool { return true }, Entropy: &distinctEntropy{}})

	for i := 0; i < quickconnect.MaxPendingPerSource; i++ {
		if _, err := manager.Initiate("192.0.2.10", "device-id", "device", "client", "1.0.0"); err != nil {
			t.Fatalf("Initiate %d from one source: %v", i, err)
		}
	}
	if _, err := manager.Initiate("192.0.2.10", "device-id", "device", "client", "1.0.0"); !errors.Is(err, quickconnect.ErrTooManyRequests) {
		t.Fatalf("Initiate past the per-source limit = %v, want ErrTooManyRequests", err)
	}

	stored := quickconnect.MaxPendingPerSource
	for source := 0; stored < quickconnect.MaxPending; source++ {
		for i := 0; i < quickconnect.MaxPendingPerSource && stored < quickconnect.MaxPending; i++ {
			if _, err := manager.Initiate(fmt.Sprintf("198.51.100.%d", source), "device-id", "device", "client", "1.0.0"); err != nil {
				t.Fatalf("Initiate %d: %v", stored, err)
			}
			stored++
		}
	}
	if _, err := manager.Initiate("203.0.113.1", "device-id", "device", "client", "1.0.0"); !errors.Is(err, quickconnect.ErrTooManyRequests) {
		t.Fatalf("Initiate past the global limit = %v, want ErrTooManyRequests", err)
	}

	// Expiry frees the slots: a request is valid at creation + Timeout and
	// swept after it.
	fake.Advance(quickconnect.Timeout + time.Nanosecond)
	if _, err := manager.Initiate("192.0.2.10", "device-id", "device", "client", "1.0.0"); err != nil {
		t.Fatalf("Initiate after expiry: %v", err)
	}
}
