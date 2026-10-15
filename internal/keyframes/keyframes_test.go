// SPDX-License-Identifier: GPL-2.0-only

package keyframes_test

// These fixtures port Jellyfin b04614e18d2aeed8d3d3de920c381c6bd5b97bdb:
// Jellyfin.MediaEncoding.Keyframes/FfProbe/FfProbeKeyframeExtractor.cs:21-117
// and FfProbeKeyframeExtractorTests.cs:8-25.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/keyframes"
	"go.uber.org/goleak"
)

const keyframeFixtureTimeout = 5 * time.Second

// openFixtureSource supplies a real regular file whose descriptor is borrowed
// by Extract. The test owns and closes it after the extraction finishes.
func openFixtureSource(t *testing.T) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.mkv")
	if err := os.WriteFile(path, []byte("fixture media source"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return source
}

type keyframeFixtureInventory struct {
	SourceCommit string `json:"source_commit"`
	Fixtures     []struct {
		Raw    string `json:"raw"`
		Result string `json:"result"`
	} `json:"fixtures"`
}

func TestExtractorPortsPinnedCSVFixtures(t *testing.T) {
	fixtureRoot := filepath.Join("testdata", "upstream-b04614e")
	inventoryBytes, err := os.ReadFile(filepath.Join(fixtureRoot, "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory keyframeFixtureInventory
	if err := json.Unmarshal(inventoryBytes, &inventory); err != nil {
		t.Fatal(err)
	}
	if got, want := inventory.SourceCommit, "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb"; got != want {
		t.Fatalf("fixture source commit = %q, want %q", got, want)
	}

	for _, fixture := range inventory.Fixtures {
		t.Run(strings.TrimSuffix(fixture.Raw, filepath.Ext(fixture.Raw)), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(fixtureRoot, fixture.Raw))
			if err != nil {
				t.Fatal(err)
			}
			want := readKeyframeData(t, filepath.Join(fixtureRoot, fixture.Result))
			got := extractKeyframes(t, raw, keyframeFixtureTimeout, 4096)
			if got.TotalDurationTicks != want.TotalDurationTicks {
				t.Fatalf("total duration ticks = %d, want %d", got.TotalDurationTicks, want.TotalDurationTicks)
			}
			if !reflect.DeepEqual(got.KeyframeTicks, want.KeyframeTicks) {
				t.Fatalf("keyframe ticks = %v, want %v", got.KeyframeTicks, want.KeyframeTicks)
			}
		})
	}
}

func TestExtractorPreservesCSVParsingBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want keyframes.Data
	}{
		{
			name: "keyframe times round to even and retain source order including duplicates",
			raw:  "packet,0.00000005,K_\npacket,0.00000015,K_\npacket,0.00000025,K_\nformat,1\n",
			want: keyframes.Data{TotalDurationTicks: 10_000_000, KeyframeTicks: []int64{0, 2, 2}},
		},
		{
			name: "format duration is used when every stream duration is nonpositive",
			raw:  "stream,-2\nstream,0\nformat,4.25\n",
			want: keyframes.Data{TotalDurationTicks: 42_500_000, KeyframeTicks: []int64{}},
		},
		{
			name: "invalid numeric rows are ignored without discarding valid keyframes",
			raw:  "packet,N/A,K_\npacket,1,K_\nstream,N/A\nformat,not-a-number\n",
			want: keyframes.Data{KeyframeTicks: []int64{10_000_000}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := extractKeyframes(t, []byte(tc.raw), keyframeFixtureTimeout, 4096)
			if got.TotalDurationTicks != tc.want.TotalDurationTicks {
				t.Fatalf("total duration ticks = %d, want %d", got.TotalDurationTicks, tc.want.TotalDurationTicks)
			}
			if !reflect.DeepEqual(got.KeyframeTicks, tc.want.KeyframeTicks) {
				t.Fatalf("keyframe ticks = %v, want %v", got.KeyframeTicks, tc.want.KeyframeTicks)
			}
		})
	}
}

func TestExtractorReturnsNonNilEmptyKeyframes(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{
			name: "empty probe output",
		},
		{
			name: "duration-only output",
			raw:  "stream,5\nformat,4.25\n",
		},
		{
			name: "all packet timestamps invalid",
			raw:  "packet,N/A,K_\npacket,not-a-number,K_\nformat,4.25\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := extractKeyframes(t, []byte(tc.raw), keyframeFixtureTimeout, 4096)
			if data.KeyframeTicks == nil {
				t.Fatal("KeyframeTicks = nil, want a non-nil empty slice for persistence")
			}
			if got := len(data.KeyframeTicks); got != 0 {
				t.Fatalf("KeyframeTicks length = %d, want 0", got)
			}
		})
	}
}

func TestExtractorIgnoresSignedAndExponentCSVNumbers(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want keyframes.Data
	}{
		{
			name: "signed packet timestamp is ignored",
			raw:  "packet,1,K_\npacket,-2,K_\nformat,4.25\n",
			want: keyframes.Data{TotalDurationTicks: 42_500_000, KeyframeTicks: []int64{10_000_000}},
		},
		{
			name: "exponent packet timestamp is ignored",
			raw:  "packet,1,K_\npacket,2e1,K_\nformat,4.25\n",
			want: keyframes.Data{TotalDurationTicks: 42_500_000, KeyframeTicks: []int64{10_000_000}},
		},
		{
			name: "signed stream duration is ignored without replacing prior stream duration",
			raw:  "stream,5\nstream,-2\nformat,4.25\n",
			want: keyframes.Data{TotalDurationTicks: 50_000_000, KeyframeTicks: []int64{}},
		},
		{
			name: "exponent stream duration is ignored without replacing prior stream duration",
			raw:  "stream,5\nstream,2e1\nformat,4.25\n",
			want: keyframes.Data{TotalDurationTicks: 50_000_000, KeyframeTicks: []int64{}},
		},
		{
			name: "signed format duration is ignored without replacing prior format duration",
			raw:  "format,4.25\nformat,-2\n",
			want: keyframes.Data{TotalDurationTicks: 42_500_000, KeyframeTicks: []int64{}},
		},
		{
			name: "exponent format duration is ignored without replacing prior format duration",
			raw:  "format,4.25\nformat,2e1\n",
			want: keyframes.Data{TotalDurationTicks: 42_500_000, KeyframeTicks: []int64{}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := extractKeyframes(t, []byte(tc.raw), keyframeFixtureTimeout, 4096)
			if got.TotalDurationTicks != tc.want.TotalDurationTicks {
				t.Fatalf("total duration ticks = %d, want %d", got.TotalDurationTicks, tc.want.TotalDurationTicks)
			}
			if !reflect.DeepEqual(got.KeyframeTicks, tc.want.KeyframeTicks) {
				t.Fatalf("keyframe ticks = %v, want %v", got.KeyframeTicks, tc.want.KeyframeTicks)
			}
		})
	}
}

func TestExtractorTruncatesDurationsAndRoundsPacketTimes(t *testing.T) {
	for _, tc := range []struct {
		name              string
		raw               string
		wantDurationTicks int64
		wantKeyframeTicks []int64
	}{
		{
			name:              "stream duration truncates toward zero",
			raw:               "packet,0.00000015,K_\npacket,0.00000035,K_\nstream,0.00000015\nformat,1\n",
			wantDurationTicks: 1,
			wantKeyframeTicks: []int64{2, 4},
		},
		{
			name:              "format duration truncates toward zero",
			raw:               "packet,0.00000015,K_\npacket,0.00000035,K_\nformat,0.00000035\n",
			wantDurationTicks: 3,
			wantKeyframeTicks: []int64{2, 4},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := extractKeyframes(t, []byte(tc.raw), keyframeFixtureTimeout, 4096)
			if data.TotalDurationTicks != tc.wantDurationTicks {
				t.Fatalf("total duration ticks = %d, want %d", data.TotalDurationTicks, tc.wantDurationTicks)
			}
			if !reflect.DeepEqual(data.KeyframeTicks, tc.wantKeyframeTicks) {
				t.Fatalf("keyframe ticks = %v, want %v", data.KeyframeTicks, tc.wantKeyframeTicks)
			}
		})
	}
}

func TestExtractorHonorsPositiveDurationTickBoundary(t *testing.T) {
	for _, tc := range []struct {
		name              string
		raw               string
		wantDurationTicks int64
	}{
		{
			name:              "maximum duration tick is accepted",
			raw:               "format,922337203685.4775808\n",
			wantDurationTicks: math.MaxInt64,
		},
		{
			name:              "duration above maximum tick is ignored",
			raw:               "format,922337203685.4778\n",
			wantDurationTicks: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := extractKeyframes(t, []byte(tc.raw), keyframeFixtureTimeout, 4096)
			if data.TotalDurationTicks != tc.wantDurationTicks {
				t.Fatalf("total duration ticks = %d, want %d", data.TotalDurationTicks, tc.wantDurationTicks)
			}
		})
	}
}

func TestExtractorRejectsStructurallyMalformedCSV(t *testing.T) {
	for _, raw := range []string{
		"packet without commas\n",
		"packet,1\n",
	} {
		t.Run(strings.TrimSpace(raw), func(t *testing.T) {
			extractor, _ := newFixtureExtractor(t, []byte(raw), keyframeFixtureTimeout, 4096)
			_, err := extractor.Extract(context.Background(), openFixtureSource(t))
			if err == nil {
				t.Fatal("Extract accepted structurally malformed ffprobe CSV")
			}
			if !strings.Contains(strings.ToLower(err.Error()), "malformed") {
				t.Fatalf("malformed CSV error = %q, want a descriptive malformed error", err)
			}
		})
	}
}

func TestExtractorUsesPinnedFFprobeCommand(t *testing.T) {
	argv := filepath.Join(t.TempDir(), "argv")
	t.Setenv("ARGV_FILE", argv)
	extractor, _ := newFixtureExtractor(t, []byte("packet,0,K_\nformat,1\n"), keyframeFixtureTimeout, 4096)
	if _, err := extractor.Extract(context.Background(), openFixtureSource(t)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-fflags", "+genpts", "-v", "error", "-skip_frame", "nokey",
		"-show_entries", "format=duration", "-show_entries", "stream=duration",
		"-show_entries", "packet=pts_time,flags", "-select_streams", "v", "-of", "csv",
		"/dev/fd/3",
	}
	if got := readLines(t, argv); !reflect.DeepEqual(got, want) {
		t.Fatalf("ffprobe argv = %#v\nwant %#v", got, want)
	}
}

func TestExtractorReadsPinnedInheritedSourceAfterPathReplacement(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "input.csv")
	original := []byte("packet,1,K_\nformat,4\n")
	if err := os.WriteFile(sourcePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })

	readyPath := filepath.Join(directory, "probe-ready")
	releasePath := filepath.Join(directory, "probe-release")
	t.Setenv("KEYFRAME_PROBE_READY", readyPath)
	t.Setenv("KEYFRAME_PROBE_RELEASE", releasePath)
	probePath := writeFakeFFProbe(t, `: > "$KEYFRAME_PROBE_READY"
while [ ! -e "$KEYFRAME_PROBE_RELEASE" ]; do sleep 0.01; done
exec cat /dev/fd/3`)
	extractor, err := keyframes.NewExtractor(probePath, keyframeFixtureTimeout, 4096)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		data keyframes.Data
		err  error
	}
	completed := make(chan result, 1)
	go func() {
		data, extractErr := extractor.Extract(context.Background(), source)
		completed <- result{data: data, err: extractErr}
	}()
	waitForFile(t, readyPath, keyframeFixtureTimeout)

	replacement := filepath.Join(directory, "replacement.csv")
	if err := os.WriteFile(replacement, []byte("packet,9,K_\nformat,9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, sourcePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-completed:
		if got.err != nil {
			t.Fatalf("Extract error = %v", got.err)
		}
		want := keyframes.Data{TotalDurationTicks: 40_000_000, KeyframeTicks: []int64{10_000_000}}
		if !reflect.DeepEqual(got.data, want) {
			t.Fatalf("Extract data = %#v, want original inherited source %#v", got.data, want)
		}
	case <-time.After(keyframeFixtureTimeout):
		t.Fatal("Extract did not complete after releasing fake ffprobe")
	}
}

func TestExtractorMapsTimeoutAndOutputCap(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		path := writeFakeFFProbe(t, `: > "$LAUNCH_FILE"
exec sleep 1`)
		t.Setenv("LAUNCH_FILE", filepath.Join(t.TempDir(), "launched"))
		extractor, err := keyframes.NewExtractor(path, 10*time.Millisecond, 4096)
		if err != nil {
			t.Fatal(err)
		}
		_, err = extractor.Extract(context.Background(), openFixtureSource(t))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Extract error = %v, want deadline exceeded", err)
		}
	})
	t.Run("stdout output cap", func(t *testing.T) {
		extractor, _ := newFixtureExtractor(t, []byte("packet,0,K_\nformat,1\n"), keyframeFixtureTimeout, 16)
		_, err := extractor.Extract(context.Background(), openFixtureSource(t))
		if !errors.Is(err, keyframes.ErrOutputTooLarge) {
			t.Fatalf("Extract error = %v, want %v", err, keyframes.ErrOutputTooLarge)
		}
	})
	t.Run("stderr output cap", func(t *testing.T) {
		path := writeFakeFFProbe(t, "printf '01234567890123456789' >&2")
		extractor, err := keyframes.NewExtractor(path, keyframeFixtureTimeout, 16)
		if err != nil {
			t.Fatal(err)
		}
		_, err = extractor.Extract(context.Background(), openFixtureSource(t))
		if !errors.Is(err, keyframes.ErrOutputTooLarge) {
			t.Fatalf("Extract error = %v, want %v", err, keyframes.ErrOutputTooLarge)
		}
	})
}

func TestExtractorResetsNoOutputWatchdogOnBytes(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	for _, tc := range []struct {
		name   string
		helper string
	}{
		{
			name: "stdout progress",
			helper: `for i in 1 2 3 4 5; do
/bin/echo "progress,$i"
sleep 0.25
done
printf 'packet,1,K_\nformat,4.25\n'`,
		},
		{
			name: "stderr progress",
			helper: `for i in 1 2 3 4 5; do
/bin/echo "progress,$i" >&2
sleep 0.25
done
printf 'packet,1,K_\nformat,4.25\n'`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFakeFFProbe(t, tc.helper)
			extractor, err := keyframes.NewExtractor(path, time.Second, 4096)
			if err != nil {
				t.Fatal(err)
			}
			testClock := clock.NewReal()
			started := testClock.Now()
			data, err := extractor.Extract(context.Background(), openFixtureSource(t))
			if elapsed := testClock.Now().Sub(started); elapsed > 3*time.Second {
				t.Fatalf("Extract returned after %s, want byte-progress probe completion within 3s", elapsed)
			}
			if err != nil {
				t.Fatalf("Extract error = %v, want byte progress to prevent watchdog timeout", err)
			}
			want := keyframes.Data{TotalDurationTicks: 42_500_000, KeyframeTicks: []int64{10_000_000}}
			if !reflect.DeepEqual(data, want) {
				t.Fatalf("Extract data = %#v, want %#v", data, want)
			}
		})
	}
}

func TestExtractorWatchdogKillsSilentProcessGroup(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process groups are unavailable on Windows")
	}
	const (
		helperReadinessTimeout = keyframeFixtureTimeout
		silentProcessWatchdog  = 2 * helperReadinessTimeout
		completionTimeout      = silentProcessWatchdog + helperReadinessTimeout
	)

	pidFile := filepath.Join(t.TempDir(), "pids")
	readyFile := filepath.Join(t.TempDir(), "ready")
	t.Setenv("PID_FILE", pidFile)
	t.Setenv("READY_FILE", readyFile)
	path := writeFakeFFProbe(t, `sleep 30 &
descendant_pid=$!
printf '%s\n%s\n' "$$" "$descendant_pid" > "$PID_FILE"
: > "$READY_FILE"
wait "$descendant_pid"`)
	extractor, err := keyframes.NewExtractor(path, silentProcessWatchdog, 4096)
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	completionDeadline := time.NewTimer(completionTimeout)
	defer completionDeadline.Stop()
	go func() {
		_, err := extractor.Extract(context.Background(), openFixtureSource(t))
		result <- err
	}()

	waitForFile(t, readyFile, helperReadinessTimeout)
	pids := readHelperPIDs(t, pidFile)
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = exec.Command("kill", "-KILL", strconv.Itoa(pid)).Run()
		}
	})

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Extract error = %v, want deadline exceeded", err)
		}
	case <-completionDeadline.C:
		t.Fatalf("silent process extraction did not complete within %s", completionTimeout)
	}
	for _, pid := range pids {
		waitForProcessExit(t, pid, 2*time.Second)
	}
}

func TestExtractorCancelsOutputCapProcessGroup(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process groups are unavailable on Windows")
	}

	pidFile := filepath.Join(t.TempDir(), "pids")
	readyFile := filepath.Join(t.TempDir(), "ready")
	t.Setenv("PID_FILE", pidFile)
	t.Setenv("READY_FILE", readyFile)
	path := writeFakeFFProbe(t, `sleep 30 &
descendant_pid=$!
printf '%s\n%s\n' "$$" "$descendant_pid" > "$PID_FILE"
: > "$READY_FILE"
printf 'packet,1,K_\nformat,4\n'
while :; do
printf '0123456789'
sleep 0.025
done`)
	extractor, err := keyframes.NewExtractor(path, 250*time.Millisecond, 32)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type extractionResult struct {
		data keyframes.Data
		err  error
	}
	result := make(chan extractionResult, 1)
	go func() {
		data, err := extractor.Extract(ctx, openFixtureSource(t))
		result <- extractionResult{data: data, err: err}
	}()

	waitForFile(t, readyFile, 10*time.Second)
	pids := readHelperPIDs(t, pidFile)
	defer func() {
		cancel()
		for _, pid := range pids {
			_ = exec.Command("kill", "-KILL", strconv.Itoa(pid)).Run()
		}
	}()

	testClock := clock.NewReal()
	started := testClock.Now()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	var got extractionResult
	select {
	case got = <-result:
	case <-timer.C:
		cancel()
		select {
		case <-result:
		case <-time.After(2 * time.Second):
		}
		t.Fatal("Extract did not promptly reject output beyond the configured cap")
	}
	if elapsed := testClock.Now().Sub(started); elapsed > 2*time.Second {
		t.Fatalf("Extract returned after %s, want prompt output-cap cancellation", elapsed)
	}
	if !errors.Is(got.err, keyframes.ErrOutputTooLarge) {
		t.Fatalf("Extract error = %v, want %v", got.err, keyframes.ErrOutputTooLarge)
	}
	if !reflect.DeepEqual(got.data, keyframes.Data{}) {
		t.Fatalf("Extract data = %#v, want no partial result", got.data)
	}
	for _, pid := range pids {
		waitForProcessExit(t, pid, 2*time.Second)
	}
}

func TestExtractorReapsDescendantAfterLeaderExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process groups are unavailable on Windows")
	}

	for _, tc := range []struct {
		name      string
		helper    string
		assertErr func(*testing.T, error)
	}{
		{
			name: "nonzero leader with descendant holding inherited pipes",
			helper: `sleep 30 &
descendant_pid=$!
printf '%s\n%s\n' "$$" "$descendant_pid" > "$PID_FILE"
: > "$READY_FILE"
exit 23`,
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				if err == nil {
					t.Fatal("Extract succeeded after the ffprobe leader exited with status 23")
				}
				if errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Extract error = %v, want the leader exit error rather than deadline exceeded", err)
				}
			},
		},
		{
			name: "successful leader with descendant closing inherited pipes",
			helper: `sleep 30 >/dev/null 2>&1 &
descendant_pid=$!
printf '%s\n%s\n' "$$" "$descendant_pid" > "$PID_FILE"
: > "$READY_FILE"
exit 0`,
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("Extract error = %v, want successful leader result", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pids")
			readyFile := filepath.Join(t.TempDir(), "ready")
			t.Setenv("PID_FILE", pidFile)
			t.Setenv("READY_FILE", readyFile)
			path := writeFakeFFProbe(t, tc.helper)
			extractor, err := keyframes.NewExtractor(path, 3*time.Second, 4096)
			if err != nil {
				t.Fatal(err)
			}

			result := make(chan error, 1)
			go func() {
				_, err := extractor.Extract(context.Background(), openFixtureSource(t))
				result <- err
			}()

			waitForFile(t, readyFile, 2*time.Second)
			pids := readHelperPIDs(t, pidFile)
			t.Cleanup(func() {
				for _, pid := range pids {
					_ = exec.Command("kill", "-KILL", strconv.Itoa(pid)).Run()
				}
			})

			tc.assertErr(t, <-result)
			for _, pid := range pids {
				waitForProcessExit(t, pid, 2*time.Second)
			}
		})
	}
}

func TestExtractorParsesSuccessfulLeaderOutputAfterReapingPipeHoldingDescendant(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process groups are unavailable on Windows")
	}

	pidFile := filepath.Join(t.TempDir(), "pids")
	readyFile := filepath.Join(t.TempDir(), "ready")
	t.Setenv("PID_FILE", pidFile)
	t.Setenv("READY_FILE", readyFile)
	path := writeFakeFFProbe(t, `sleep 30 &
descendant_pid=$!
printf '%s\n%s\n' "$$" "$descendant_pid" > "$PID_FILE"
: > "$READY_FILE"
printf 'packet,1,K_\nformat,4.25\n'
exit 0`)
	extractor, err := keyframes.NewExtractor(path, 3*time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}

	type extractionResult struct {
		data keyframes.Data
		err  error
	}
	result := make(chan extractionResult, 1)
	go func() {
		data, err := extractor.Extract(context.Background(), openFixtureSource(t))
		result <- extractionResult{data: data, err: err}
	}()

	waitForFile(t, readyFile, 2*time.Second)
	pids := readHelperPIDs(t, pidFile)
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = exec.Command("kill", "-KILL", strconv.Itoa(pid)).Run()
		}
	})

	got := <-result
	if errors.Is(got.err, exec.ErrWaitDelay) {
		t.Fatal("Extract returned ErrWaitDelay after a successful ffprobe leader")
	}
	if got.err != nil {
		t.Fatalf("Extract error = %v, want successful parsed output", got.err)
	}
	want := keyframes.Data{TotalDurationTicks: 42_500_000, KeyframeTicks: []int64{10_000_000}}
	if !reflect.DeepEqual(got.data, want) {
		t.Fatalf("Extract data = %#v, want %#v", got.data, want)
	}
	for _, pid := range pids {
		waitForProcessExit(t, pid, 2*time.Second)
	}
}

func TestExtractorDoesNotWaitPastCallerDeadlineForExitedLeader(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process groups are unavailable on Windows")
	}

	const callerDeadline = 500 * time.Millisecond
	const deadlineTolerance = 300 * time.Millisecond
	pidFile := filepath.Join(t.TempDir(), "pids")
	readyFile := filepath.Join(t.TempDir(), "ready")
	t.Setenv("PID_FILE", pidFile)
	t.Setenv("READY_FILE", readyFile)
	path := writeFakeFFProbe(t, `sleep 30 &
descendant_pid=$!
printf '%s\n%s\n' "$$" "$descendant_pid" > "$PID_FILE"
: > "$READY_FILE"
exit 0`)
	extractor, err := keyframes.NewExtractor(path, 3*time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), callerDeadline)
	defer cancel()
	testClock := clock.NewReal()
	started := testClock.Now()
	result := make(chan error, 1)
	go func() {
		_, err := extractor.Extract(ctx, openFixtureSource(t))
		result <- err
	}()

	waitForFile(t, readyFile, 2*time.Second)
	if err := ctx.Err(); err != nil {
		t.Fatalf("helper did not exit before caller deadline: %v", err)
	}
	pids := readHelperPIDs(t, pidFile)
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = exec.Command("kill", "-KILL", strconv.Itoa(pid)).Run()
		}
	})

	err = <-result
	elapsed := testClock.Now().Sub(started)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Extract error = %v, want success or caller deadline exceeded", err)
	}
	if maximum := callerDeadline + deadlineTolerance; elapsed > maximum {
		t.Fatalf("Extract returned after %s, want no later than caller deadline tolerance %s", elapsed, maximum)
	}
	for _, pid := range pids {
		waitForProcessExit(t, pid, 2*time.Second)
	}
}

func extractKeyframes(t *testing.T, raw []byte, timeout time.Duration, maxOutput int) keyframes.Data {
	t.Helper()
	extractor, _ := newFixtureExtractor(t, raw, timeout, maxOutput)
	data, err := extractor.Extract(context.Background(), openFixtureSource(t))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newFixtureExtractor(t *testing.T, raw []byte, timeout time.Duration, maxOutput int) (*keyframes.Extractor, string) {
	t.Helper()
	path := writeFakeFFProbe(t, `[ -z "${ARGV_FILE:-}" ] || printf '%s\n' "$@" > "$ARGV_FILE"
printf '%s' "$PROBE_OUTPUT"`)
	t.Setenv("PROBE_OUTPUT", string(raw))
	extractor, err := keyframes.NewExtractor(path, timeout, maxOutput)
	if err != nil {
		t.Fatal(err)
	}
	return extractor, path
}

func readKeyframeData(t *testing.T, path string) keyframes.Data {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value keyframes.Data
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func writeFakeFFProbe(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for helper signal %q", path)
		case <-poll.C:
		}
	}
}

func readHelperPIDs(t *testing.T, path string) []int {
	t.Helper()
	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("helper PID lines = %q, want leader and descendant", lines)
	}
	pids := make([]int, len(lines))
	for index, line := range lines {
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 {
			t.Fatalf("helper PID %q = %d, %v; want a positive PID", line, pid, err)
		}
		pids[index] = pid
	}
	return pids
}

func waitForProcessExit(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		if !processExists(pid) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("process %d survived extractor completion", pid)
		case <-poll.C:
		}
	}
}

func processExists(pid int) bool {
	return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil
}
