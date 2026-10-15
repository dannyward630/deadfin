// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

// These tests exercise the progressive transcoding boundary with real child
// processes. Shell scripts are only fixtures: the manager still owns exec,
// process groups, stdin, output lifetime, and teardown.
func TestTranscodeManagerStartupWipesOnlyOwnedDirectory(t *testing.T) {
	root := t.TempDir()
	owned := filepath.Join(root, "transcodes")
	if err := os.MkdirAll(owned, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(owned, "stale.ts")
	if err := os.WriteFile(stale, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "keep")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	manager, err := NewTranscodeManager(progressiveFakeFFmpeg(t, "hold"), owned, TranscodeManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveManager(t, manager)

	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale owned output survived manager startup: %v", err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "keep" {
		t.Fatalf("outside manager directory = %q, %v; want preserved file", got, err)
	}
}

func TestTranscodeManagerCloseReturnsCompletedResultAfterShutdownWhenContextCanceled(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())

	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("initial Close: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 10_000 {
		if err := manager.Close(ctx); err != nil {
			t.Fatalf("Close after completed shutdown with canceled context: %v", err)
		}
	}
}

func TestTranscodeManagerAcceptsCanonicalizedTemporaryDirectoryAncestors(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	parent := filepath.Dir(fixture.dir)
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalParent == parent {
		t.Skip("temporary directory has no canonicalized ancestor on this host")
	}
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
}

func TestOpenProgressivePreCanceledRequestStartsNothing(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := manager.OpenProgressive(ctx, fixture.request("never.ts"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenProgressive error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(fixture.start); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-canceled OpenProgressive started ffmpeg: %v", err)
	}
}

func TestOpenProgressiveExecutesExactArgvWithoutShell(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	request := fixture.request("quoted.ts")
	injection := filepath.Join(t.TempDir(), "must-not-exist")
	request.Args = []string{"-i", "file:" + fixture.input, "-metadata", "title=space ; touch " + injection, request.OutputPath}

	reader, err := manager.OpenProgressive(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, reader)
	fixture.waitStarted(t)
	want := append([]string(nil), request.Args...)
	want[1] = sourcefile.FDPath(3)
	canonicalDirectory, err := filepath.EvalSymlinks(fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	want[len(want)-1] = filepath.Join(canonicalDirectory, "quoted.ts")
	if got := readLines(t, fixture.argv); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(injection); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell metacharacters were interpreted: %v", err)
	}
}

func TestOpenProgressivePinsSourceBeforePathReplacement(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	replacement := filepath.Join(t.TempDir(), "replacement.mp4")
	if err := os.WriteFile(replacement, []byte("replacement source"), 0o600); err != nil {
		t.Fatal(err)
	}
	var inherited []*os.File
	var pinnedBytes []byte
	manager.startCommand = func(command *exec.Cmd) error {
		inherited = append([]*os.File(nil), command.ExtraFiles...)
		if err := os.Rename(replacement, fixture.input); err != nil {
			return err
		}
		var err error
		pinnedBytes, err = io.ReadAll(inherited[0])
		if err != nil {
			return err
		}
		if _, err := inherited[0].Seek(0, io.SeekStart); err != nil {
			return err
		}
		return command.Start()
	}

	reader, err := manager.OpenProgressive(t.Context(), fixture.request("pinned.ts"))
	if err != nil {
		t.Fatalf("OpenProgressive(): %v", err)
	}
	defer closeProgressiveReader(t, reader)
	fixture.waitStarted(t)
	if got := string(pinnedBytes); got != "progressive source" {
		t.Fatalf("inherited fd3 bytes = %q, want original source", got)
	}
	if len(inherited) != 1 {
		t.Fatalf("inherited descriptors = %d, want one fd3 source", len(inherited))
	}
	if got := readLines(t, fixture.argv); len(got) < 2 || got[1] != sourcefile.FDPath(3) {
		t.Fatalf("child argv = %#v, want source operand on fd3", got)
	}
}

func TestOpenProgressiveRejectsSharedOutputWithDifferentSourceRevision(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	request := fixture.request("revision-conflict.ts")
	reader, err := manager.OpenProgressive(t.Context(), request)
	if err != nil {
		t.Fatalf("first OpenProgressive(): %v", err)
	}
	defer closeProgressiveReader(t, reader)
	fixture.waitStarted(t)

	changed := request
	changed.SourceRevision = "different-source-revision"
	if joined, err := manager.OpenProgressive(t.Context(), changed); err == nil {
		_ = joined.Close()
		t.Fatal("OpenProgressive joined a family with a different source revision")
	}
}

func TestOpenProgressiveRejectsEscapingAndMismatchedOutputs(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)

	for _, tc := range []struct {
		name string
		edit func(ProgressiveRequest) ProgressiveRequest
	}{
		{
			name: "outside owned directory",
			edit: func(request ProgressiveRequest) ProgressiveRequest {
				request.OutputPath = filepath.Join(filepath.Dir(fixture.dir), "escape.ts")
				request.Args[len(request.Args)-1] = request.OutputPath
				return request
			},
		},
		{
			name: "argv names another output",
			edit: func(request ProgressiveRequest) ProgressiveRequest {
				request.Args[len(request.Args)-1] = filepath.Join(fixture.dir, "other.ts")
				return request
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := manager.OpenProgressive(context.Background(), tc.edit(fixture.request("wanted.ts"))); err == nil {
				t.Fatal("OpenProgressive succeeded with invalid output path")
			}
			if _, err := os.Stat(fixture.start); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid request launched ffmpeg: %v", err)
			}
		})
	}
}

func TestOpenProgressiveDeduplicatesGrowingOutputForConcurrentReaders(t *testing.T) {
	fixture := newProgressiveFixture(t, "grow")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	request := fixture.request("shared.ts")

	readers := make(chan io.ReadCloser, 2)
	errs := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for range 2 {
		go func() {
			start.Wait()
			reader, err := manager.OpenProgressive(context.Background(), request)
			if err != nil {
				errs <- err
				return
			}
			readers <- reader
		}()
	}
	start.Done()
	first, second := receiveReaders(t, readers, errs)
	defer closeProgressiveReader(t, first)
	defer closeProgressiveReader(t, second)
	fixture.waitStarted(t)
	if got := readLines(t, fixture.starts); len(got) != 1 {
		t.Fatalf("ffmpeg starts = %d (%#v), want one shared process", len(got), got)
	}
	if got := readProgressiveString(t, first); got != "first" {
		t.Fatalf("first reader initial bytes = %q, want first", got)
	}
	if got := readProgressiveString(t, second); got != "first" {
		t.Fatalf("second reader initial bytes = %q, want first", got)
	}
	if err := os.WriteFile(fixture.goFile, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, fixture.appended)
	if got := readProgressiveString(t, first); got != "second" {
		t.Fatalf("first reader appended bytes = %q, want second", got)
	}
	if got := readProgressiveString(t, second); got != "second" {
		t.Fatalf("second reader appended bytes = %q, want second", got)
	}
}

func TestOpenProgressiveRejectsConflictingArgumentsForSharedOutput(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	request := fixture.request("shared.ts")
	reader, err := manager.OpenProgressive(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, reader)
	fixture.waitStarted(t)

	conflict := request
	conflict.Args = append([]string(nil), request.Args...)
	conflict.Args[0] = "-different"
	if _, err := manager.OpenProgressive(context.Background(), conflict); err == nil {
		t.Fatal("OpenProgressive accepted conflicting argv for shared output")
	}
	if got := readLines(t, fixture.starts); len(got) != 1 {
		t.Fatalf("conflicting request started another process: %#v", got)
	}
}

func TestReaderCancellationAndRepeatedCloseReleaseOneLeaseWithoutKillingJob(t *testing.T) {
	fake := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, fake)
	defer closeProgressiveManager(t, manager)
	ctx, cancel := context.WithCancel(context.Background())
	reader, err := manager.OpenProgressive(ctx, fixture.request("lease.ts"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t)
	pid := waitForPID(t, fixture.parentPID)

	blocked := make(chan error, 1)
	go func() {
		_, err := reader.Read(make([]byte, 1))
		blocked <- err
	}()
	cancel()
	if err := receiveProgressiveError(t, blocked); !errors.Is(err, context.Canceled) {
		t.Fatalf("reader error after cancellation = %v, want context.Canceled", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("first reader Close: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("second reader Close: %v", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("reader cancellation/Close killed job immediately: %v", err)
	}

	fake.Advance(9 * time.Second)
	replacement, err := manager.OpenProgressive(context.Background(), fixture.request("lease.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, replacement)
	fake.Advance(time.Second) // A duplicate Close must not leave an old idle timer armed.
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("new lease did not disarm prior idle kill: %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	fake.Advance(10 * time.Second)
	waitForProcessExit(t, pid)
}

func TestIdleKillSendsQAndCooperativeProcessExits(t *testing.T) {
	fake := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, fake)
	defer closeProgressiveManager(t, manager)
	reader, err := manager.OpenProgressive(context.Background(), fixture.request("idle.ts"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t)
	pid := waitForPID(t, fixture.parentPID)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	fake.Advance(10 * time.Second)
	waitForFile(t, fixture.qFile)
	waitForProcessExit(t, pid)
}

func TestIdleKillEscalatesToProcessGroupSIGKILLAfterGrace(t *testing.T) {
	fake := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	fixture := newProgressiveFixture(t, "ignore-q-with-child")
	manager := fixture.manager(t, fake)
	defer closeProgressiveManager(t, manager)
	reader, err := manager.OpenProgressive(context.Background(), fixture.request("stubborn.ts"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t)
	parent, child := waitForPID(t, fixture.parentPID), waitForPID(t, fixture.childPID)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	fake.Advance(10 * time.Second)
	waitForFile(t, fixture.qFile)
	fake.Advance(5 * time.Second)
	waitForProcessExit(t, parent)
	waitForProcessExit(t, child)
	waitForProgressiveRemoval(t, filepath.Join(fixture.dir, "stubborn.ts"))
}

func TestOpenProgressiveEarlyFailureIncludesBoundedDiagnostic(t *testing.T) {
	fixture := newProgressiveFixture(t, "early-error")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	_, err := manager.OpenProgressive(context.Background(), fixture.request("broken.ts"))
	if err == nil {
		t.Fatal("OpenProgressive succeeded despite early ffmpeg failure")
	}
	if !strings.Contains(err.Error(), "diagnostic-marker") {
		t.Fatalf("early failure lacks stderr diagnostic: %v", err)
	}
	if len(err.Error()) > 128<<10 {
		t.Fatalf("early failure diagnostic is unbounded (%d bytes)", len(err.Error()))
	}
}

func TestProgressiveReaderStallDeadlineSpansEOFRetryPolls(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(progressiveTimerClock{fake: base})
	fixture := newProgressiveFixture(t, "hold")
	manager, err := NewTranscodeManager(fixture.ffmpeg, fixture.dir, TranscodeManagerOptions{
		Clock:                  observed,
		OutputPollInterval:     100 * time.Millisecond,
		ReaderRetryInterval:    time.Second,
		ReaderStallTimeout:     30 * time.Second,
		ProgressiveIdleTimeout: 10 * time.Second,
		KillGracePeriod:        5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveManager(t, manager)
	reader, err := manager.OpenProgressive(context.Background(), fixture.request("stalled.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, reader)
	fixture.waitStarted(t)

	result := make(chan error, 1)
	go func() {
		_, err := reader.Read(make([]byte, 1))
		result <- err
	}()
	observed.waitForTimer(t, 30*time.Second)
	for range 30 {
		observed.waitForTimer(t, time.Second)
		base.Advance(time.Second)
	}
	if err := receiveProgressiveError(t, result); !errors.Is(err, ErrOutputStalled) {
		t.Fatalf("reader stall error = %v, want %v after cumulative retry time", err, ErrOutputStalled)
	}
}

func TestTranscodeManagerCloseReapsEveryProcessGroupAndCanRetry(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(progressiveTimerClock{fake: base})
	fixture := newProgressiveFixture(t, "ignore-q-with-child")
	manager := fixture.manager(t, observed)
	first, err := manager.OpenProgressive(context.Background(), fixture.request("one.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, first)
	second, err := manager.OpenProgressive(context.Background(), fixture.request("two.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, second)
	parents := waitForProgressiveStartRecords(t, fixture.starts, 2)

	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	waitForFile(t, fixture.qFile)
	observed.waitForTimerCount(t, 5*time.Second, 2)
	base.Advance(5 * time.Second)
	if err := receiveProgressiveError(t, closed); err != nil {
		t.Fatalf("manager Close: %v", err)
	}
	for _, raw := range parents {
		pid, err := parseProgressivePID(raw)
		if err != nil {
			t.Fatal(err)
		}
		waitForProcessExit(t, pid)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("second manager Close: %v", err)
	}
}

func TestProgressiveFixtureCleanupReapsAllConcurrentGroups(t *testing.T) {
	var parents, children []int
	t.Run("abandoned concurrent jobs", func(t *testing.T) {
		t.Cleanup(func() {
			for _, pid := range append(parents, children...) {
				waitForProcessExit(t, pid)
			}
		})

		fixture := newProgressiveFixture(t, "ignore-q-with-child")
		manager := fixture.manager(t, clock.NewReal())
		for _, name := range []string{"one.ts", "two.ts"} {
			if _, err := manager.OpenProgressive(context.Background(), fixture.request(name)); err != nil {
				t.Fatalf("OpenProgressive(%q): %v", name, err)
			}
		}
		parents = waitForPIDs(t, fixture.parentPID, 2)
		children = waitForPIDs(t, fixture.childPID, 2)
		seen := make(map[int]struct{}, len(parents)+len(children))
		for _, pid := range append(parents, children...) {
			if _, duplicate := seen[pid]; duplicate {
				t.Fatalf("fixture PID record %d is not unique: parents=%v children=%v", pid, parents, children)
			}
			seen[pid] = struct{}{}
		}
	})
}

func TestOpenProgressiveWaitsForTerminatingJobThenStartsReplacement(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(progressiveTimerClock{fake: base})
	fixture := newProgressiveFixture(t, "ignore-q-with-child")
	manager := fixture.manager(t, observed)
	first, err := manager.OpenProgressive(context.Background(), fixture.request("restarting.ts"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	base.Advance(10 * time.Second)
	waitForFile(t, fixture.qFile)
	observed.waitForTimer(t, 5*time.Second)

	secondOpen := make(chan progressiveOpenResult, 1)
	go func() {
		reader, err := manager.OpenProgressive(context.Background(), fixture.request("restarting.ts"))
		secondOpen <- progressiveOpenResult{reader: reader, err: err}
	}()
	assertProgressivePending(t, secondOpen)
	base.Advance(5 * time.Second)
	second := receiveProgressiveOpen(t, secondOpen)
	defer closeProgressiveReader(t, second)
	waitForProgressiveStartRecords(t, fixture.starts, 2)

	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	observed.waitForTimerCount(t, 5*time.Second, 2)
	base.Advance(5 * time.Second)
	if err := receiveProgressiveError(t, closed); err != nil {
		t.Fatalf("Close after replacement: %v", err)
	}
}

func TestOpenProgressiveDoesNotAttachAfterIdleExpiryClaim(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(progressiveTimerClock{fake: base})
	fixture := newProgressiveFixture(t, "ignore-q-with-child")
	manager := fixture.manager(t, observed)
	first, err := manager.OpenProgressive(context.Background(), fixture.request("idle-claim.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	claimed := make(chan struct{})
	releaseStop := make(chan struct{})
	returned := make(chan struct{})
	manager.beforeIdleStop = func() {
		close(claimed)
		<-releaseStop
		close(returned)
	}
	base.Advance(10 * time.Second)
	select {
	case <-claimed:
	case <-time.After(fixtureTimeout):
		t.Fatal("idle expiry never claimed the job")
	}

	secondOpen := make(chan progressiveOpenResult, 1)
	go func() {
		reader, err := manager.OpenProgressive(context.Background(), fixture.request("idle-claim.ts"))
		secondOpen <- progressiveOpenResult{reader: reader, err: err}
	}()
	assertProgressivePending(t, secondOpen)
	close(releaseStop)
	<-returned
	manager.beforeIdleStop = nil
	observed.waitForTimer(t, 5*time.Second)
	base.Advance(5 * time.Second)
	second := receiveProgressiveOpen(t, secondOpen)
	defer closeProgressiveReader(t, second)
	waitForProgressiveStartRecords(t, fixture.starts, 2)

	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	observed.waitForTimerCount(t, 5*time.Second, 2)
	base.Advance(5 * time.Second)
	if err := receiveProgressiveError(t, closed); err != nil {
		t.Fatalf("Close after replacement: %v", err)
	}
}

func TestOpenProgressiveRejectsStderrSymlinkSwappedAfterValidation(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	request := fixture.request("stderr-swap.ts")
	victim := filepath.Join(t.TempDir(), "victim.stderr")
	if err := os.WriteFile(victim, []byte("preserve-stderr"), 0o600); err != nil {
		t.Fatal(err)
	}
	swapped := make(chan error, 1)
	manager.beforeStderrOpen = func() {
		swapped <- os.Symlink(victim, request.OutputPath+".stderr.log")
	}
	reader, err := manager.OpenProgressive(context.Background(), request)
	if reader != nil {
		_ = reader.Close()
	}
	if err == nil {
		t.Fatal("OpenProgressive accepted a stderr symlink swapped after validation")
	}
	if err := <-swapped; err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "preserve-stderr" {
		t.Fatalf("stderr victim = %q, %v; want preserved target", got, err)
	}
}

func TestOpenProgressiveRejectsParentSwappedBeforeCreation(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	parent := filepath.Join(fixture.dir, "swapped-parent")
	outside := t.TempDir()
	request := fixture.request(filepath.Join("swapped-parent", "nested", "victim.ts"))
	swapped := make(chan error, 1)
	manager.beforeOutputMutation = func() {
		swapped <- os.Symlink(outside, parent)
	}
	reader, err := manager.OpenProgressive(context.Background(), request)
	if reader != nil {
		_ = reader.Close()
	}
	if err == nil {
		t.Fatal("OpenProgressive accepted a parent swapped to a symlink before creation")
	}
	if err := <-swapped; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parent creation escaped manager directory: %v", err)
	}
}

func TestOpenProgressiveRejectsOutputSymlinkSwappedBeforeRead(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	request := fixture.request("output-swap.ts")
	victim := filepath.Join(t.TempDir(), "outside-output")
	if err := os.WriteFile(victim, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	allowSwap := make(chan struct{})
	swapped := make(chan error, 1)
	var once sync.Once
	manager.beforeOutputRead = func() {
		once.Do(func() {
			close(entered)
			<-allowSwap
			err := os.Remove(request.OutputPath)
			if err == nil {
				err = os.Symlink(victim, request.OutputPath)
			}
			swapped <- err
		})
	}
	opened := make(chan progressiveOpenResult, 1)
	go func() {
		reader, err := manager.OpenProgressive(context.Background(), request)
		opened <- progressiveOpenResult{reader: reader, err: err}
	}()
	waitForProgressiveSignal(t, entered, "output read hook")
	fixture.waitStarted(t)
	close(allowSwap)
	if err := receiveProgressiveOpenFailure(t, opened); err == nil {
		t.Fatal("OpenProgressive streamed output through a swapped symlink")
	}
	if err := <-swapped; err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "outside-secret" {
		t.Fatalf("outside output = %q, %v; want preserved secret", got, err)
	}
}

func TestOpenProgressiveDoesNotReadDiagnosticThroughSwappedSymlink(t *testing.T) {
	fixture := newProgressiveFixture(t, "early-error")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	request := fixture.request("diagnostic-swap.ts")
	victim := filepath.Join(t.TempDir(), "outside-diagnostic")
	if err := os.WriteFile(victim, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	swapped := make(chan error, 1)
	manager.beforeDiagnosticRead = func() {
		err := os.Remove(request.OutputPath + ".stderr.log")
		if err == nil {
			err = os.Symlink(victim, request.OutputPath+".stderr.log")
		}
		swapped <- err
	}
	_, err := manager.OpenProgressive(context.Background(), request)
	if err == nil {
		t.Fatal("OpenProgressive succeeded despite early process failure")
	}
	if err := <-swapped; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(err.Error(), "outside-secret") {
		t.Fatalf("failure diagnostic read through swapped symlink: %v", err)
	}
}

func TestOpenProgressiveSameOutputReturnsPendingCleanupError(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, base)
	sentinel := errors.New("injected output unlink failure")
	reapSucceeded := make(chan struct{})
	cleanupAttempted := make(chan struct{})
	manager.reapProcessGroup = func(pid int) error {
		err := reapProcessGroup(pid)
		if err == nil {
			close(reapSucceeded)
		}
		return err
	}
	manager.removeProgressiveOutput = func(string) error {
		close(cleanupAttempted)
		return sentinel
	}
	t.Cleanup(func() {
		manager.removeProgressiveOutput = nil
		_ = manager.Close(context.Background())
		_ = manager.Close(context.Background())
	})

	request := fixture.request("cleanup-pending.ts")
	reader, err := manager.OpenProgressive(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	base.Advance(10 * time.Second)
	waitForProgressiveSignal(t, reapSucceeded, "successful process-group reap")
	waitForProgressiveSignal(t, cleanupAttempted, "failed progressive output cleanup")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if reader, err := manager.OpenProgressive(ctx, request); !errors.Is(err, sentinel) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("same-output OpenProgressive after failed cleanup = %v, want %v", err, sentinel)
	} else if reader != nil {
		_ = reader.Close()
		t.Fatal("same-output OpenProgressive returned a reader despite pending cleanup")
	}
}

func TestCloseCleanupRetryHonorsDeadlineWhileOutputRemovalIsBlocked(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, base)
	sentinel := errors.New("injected initial output unlink failure")
	firstCleanup := make(chan struct{})
	retryStarted := make(chan struct{})
	retryFinished := make(chan struct{})
	releaseRetry := make(chan struct{})
	var releaseOnce sync.Once
	cleanupAttempts := 0
	manager.removeProgressiveOutput = func(string) error {
		cleanupAttempts++
		if cleanupAttempts == 1 {
			close(firstCleanup)
			return sentinel
		}
		close(retryStarted)
		<-releaseRetry
		close(retryFinished)
		return nil
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseRetry) })
		_ = manager.Close(context.Background())
		_ = manager.Close(context.Background())
	})

	reader, err := manager.OpenProgressive(context.Background(), fixture.request("cleanup-retry.ts"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	base.Advance(10 * time.Second)
	waitForProgressiveSignal(t, firstCleanup, "initial failed progressive output cleanup")
	if err := manager.Close(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("first Close after failed cleanup = %v, want %v", err, sentinel)
	}

	ctx, cancel := newDeadlineContext()
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- manager.Close(ctx) }()
	waitForProgressiveSignal(t, retryStarted, "blocked progressive output cleanup retry")
	cancel()
	if err := receiveProgressiveError(t, result); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close while cleanup retry is blocked = %v, want context deadline", err)
	}
	releaseOnce.Do(func() { close(releaseRetry) })
	waitForProgressiveSignal(t, retryFinished, "released progressive output cleanup retry")
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Close after releasing cleanup retry: %v", err)
	}
}

func TestConcurrentCloseWaitsForSuccessfulCleanupRetryWorker(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, base)
	sentinel := errors.New("injected initial output unlink failure")
	firstCleanup := make(chan struct{})
	cleanupAttempts := 0
	manager.removeProgressiveOutput = func(string) error {
		cleanupAttempts++
		if cleanupAttempts == 1 {
			close(firstCleanup)
			return sentinel
		}
		return nil
	}
	cleanupPrepared := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var releaseOnce sync.Once
	manager.beforeCleanupAttemptDone = func() {
		close(cleanupPrepared)
		<-releaseCleanup
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseCleanup) })
		_ = manager.Close(context.Background())
		_ = manager.Close(context.Background())
	})

	request := fixture.request("cleanup-worker-barrier.ts")
	reader, err := manager.OpenProgressive(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	base.Advance(10 * time.Second)
	waitForProgressiveSignal(t, firstCleanup, "initial failed progressive output cleanup")
	if err := manager.Close(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("first Close after failed cleanup = %v, want %v", err, sentinel)
	}

	retryResult := make(chan error, 1)
	go func() { retryResult <- manager.Close(context.Background()) }()
	waitForProgressiveSignal(t, cleanupPrepared, "successful cleanup retry before worker completion")
	if reader, err := manager.OpenProgressive(context.Background(), request); !errors.Is(err, ErrManagerClosed) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("same-output OpenProgressive while closed cleanup drains = %v, want %v", err, ErrManagerClosed)
	} else if reader != nil {
		_ = reader.Close()
		t.Fatal("same-output OpenProgressive returned a reader while manager is closed")
	}
	ctx, cancel := newDeadlineContext()
	defer cancel()
	concurrentResult := make(chan error, 1)
	go func() { concurrentResult <- manager.Close(ctx) }()
	cancel()
	if err := receiveProgressiveError(t, concurrentResult); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent Close while cleanup worker remains active = %v, want context deadline", err)
	}
	releaseOnce.Do(func() { close(releaseCleanup) })
	if err := receiveProgressiveError(t, retryResult); err != nil {
		t.Fatalf("cleanup retry Close after worker completion: %v", err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Close after successful cleanup retry: %v", err)
	}
}

func TestCloseKillsProcessGroupBeforeBlockedOutputCleanup(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	fixture := newProgressiveFixture(t, "ignore-q-with-child")
	observed := newProgressiveObservedClock(progressiveTimerClock{fake: base})
	manager := fixture.manager(t, observed)
	cleanupEntered := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var releaseOnce sync.Once
	manager.removeProgressiveOutput = func(string) error {
		close(cleanupEntered)
		<-releaseCleanup
		return nil
	}

	request := fixture.request("kill-before-cleanup.ts")
	reader, err := manager.OpenProgressive(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitStarted(t)
	parent := waitForPID(t, fixture.parentPID)
	child := waitForPID(t, fixture.childPID)
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseCleanup) })
		_ = syscall.Kill(-parent, syscall.SIGKILL)
		_ = manager.Close(context.Background())
	})
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	observed.waitForTimer(t, 5*time.Second)
	base.Advance(5 * time.Second)
	waitForProgressiveSignal(t, cleanupEntered, "blocked progressive output cleanup after shutdown grace")
	waitForProcessExit(t, parent)
	waitForProcessExit(t, child)
	releaseOnce.Do(func() { close(releaseCleanup) })
	if err := receiveProgressiveError(t, closed); err != nil {
		t.Fatalf("Close after releasing output cleanup: %v", err)
	}
}

func TestCloseRetriesReapAfterTransientFailureUntilGroupIsGone(t *testing.T) {
	fixture := newProgressiveFixture(t, "q-exits-with-child")
	manager := fixture.manager(t, clock.NewReal())
	sentinel := errors.New("injected group reap failure")

	reapAttempts := 0
	manager.reapProcessGroup = func(pid int) error {
		reapAttempts++
		if reapAttempts == 1 {
			return sentinel
		}
		return reapProcessGroup(pid)
	}
	reader, err := manager.OpenProgressive(context.Background(), fixture.request("reap-retry.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, reader)
	leader := waitForPID(t, fixture.parentPID)
	child := waitForPID(t, fixture.childPID)
	defer func() {
		_ = syscall.Kill(-leader, syscall.SIGKILL)
	}()
	if err := manager.Close(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("first Close with surviving group = %v, want %v", err, sentinel)
	}
	if err := syscall.Kill(-leader, 0); err != nil {
		t.Fatalf("first Close did not leave injected surviving group observable: %v", err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("retrying Close after transient reap failure: %v", err)
	}
	if reapAttempts < 2 {
		t.Fatalf("reap attempts = %d, want retry after first failure", reapAttempts)
	}
	if err := syscall.Kill(-leader, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("retrying Close returned before process group exit: %v", err)
	}
	waitForProcessExit(t, child)
}

func TestConcurrentCloseHonorsDeadlineWhileReapIsBlocked(t *testing.T) {
	fixture := newProgressiveFixture(t, "q-exits-with-child")
	manager := fixture.manager(t, clock.NewReal())
	entered := make(chan struct{})
	release := make(chan struct{})
	sentinel := errors.New("transient blocked reap failure")
	attempts := 0
	manager.reapProcessGroup = func(pid int) error {
		attempts++
		if attempts == 1 {
			close(entered)
			<-release
			return sentinel
		}
		return reapProcessGroup(pid)
	}
	reader, err := manager.OpenProgressive(context.Background(), fixture.request("reap-blocked.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, reader)
	leader := waitForPID(t, fixture.parentPID)
	child := waitForPID(t, fixture.childPID)
	defer func() { _ = syscall.Kill(-leader, syscall.SIGKILL) }()
	first := make(chan error, 1)
	go func() { first <- manager.Close(context.Background()) }()
	waitForProgressiveSignal(t, entered, "blocking group reaper")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := manager.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent Close while reaper blocks = %v, want context deadline", err)
	}
	close(release)
	if err := receiveProgressiveError(t, first); !errors.Is(err, sentinel) {
		t.Fatalf("first Close after blocked reaper = %v, want %v", err, sentinel)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("retry Close after releasing reaper: %v", err)
	}
	if attempts < 2 {
		t.Fatalf("reap attempts = %d, want at least two", attempts)
	}
	if err := syscall.Kill(-leader, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("retry Close returned before process group exit: %v", err)
	}
	waitForProcessExit(t, child)
}

func TestCloseReportsPermissionDeniedReapWhileGroupExists(t *testing.T) {
	fixture := newProgressiveFixture(t, "q-exits-with-child")
	manager := fixture.manager(t, clock.NewReal())
	manager.reapProcessGroup = func(int) error { return syscall.EPERM }
	reader, err := manager.OpenProgressive(context.Background(), fixture.request("reap-eperm.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, reader)
	leader := waitForPID(t, fixture.parentPID)
	child := waitForPID(t, fixture.childPID)
	defer func() { _ = syscall.Kill(-leader, syscall.SIGKILL) }()
	if err := manager.Close(context.Background()); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("Close while owned group remains permission-denied = %v, want %v", err, syscall.EPERM)
	}
	if err := syscall.Kill(-leader, 0); err != nil {
		t.Fatalf("Close reported reaper failure after group was already gone: %v", err)
	}
	if err := syscall.Kill(-leader, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatal(err)
	}
	waitForProcessExit(t, child)
}

func TestOpenProgressiveRejectsOutputPathRetargetedThroughConstructorAlias(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	root := t.TempDir()
	owned := filepath.Join(root, "owned")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(owned, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(owned, alias); err != nil {
		t.Fatal(err)
	}
	manager, err := NewTranscodeManager(fixture.ffmpeg, filepath.Join(alias, "transcodes"), TranscodeManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveManager(t, manager)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(alias, "transcodes", "retargeted.ts")
	request := fixture.request("retargeted.ts")
	request.OutputPath = output
	request.Args[len(request.Args)-1] = output
	reader, err := manager.OpenProgressive(context.Background(), request)
	if reader != nil {
		_ = reader.Close()
	}
	if err == nil {
		t.Fatal("OpenProgressive accepted an output path retargeted outside its constructed owner")
	}
	if _, err := os.Stat(filepath.Join(outside, "transcodes", "retargeted.ts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retargeted alias created an outside output: %v", err)
	}
}

func TestExitedJobCancelsAnArmedIdleTimerSoCloseDoesNotNeedClockAdvance(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(progressiveTimerClock{fake: base})
	fixture := newProgressiveFixture(t, "exit-on-go")
	manager := fixture.manager(t, observed)
	reader, err := manager.OpenProgressive(context.Background(), fixture.request("finished.ts"))
	if err != nil {
		t.Fatal(err)
	}
	pid := waitForPID(t, fixture.parentPID)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	observed.waitForTimer(t, 10*time.Second)
	if err := os.WriteFile(fixture.goFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForProcessExit(t, pid)
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Close after natural process exit: %v", err)
	}
}

func TestProgressiveReaderCloseUnblocksConcurrentStalledRead(t *testing.T) {
	base := clock.NewFake(time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC))
	observed := newProgressiveObservedClock(progressiveTimerClock{fake: base})
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, observed)
	for index := range 24 {
		reader, err := manager.OpenProgressive(context.Background(), fixture.request(fmt.Sprintf("close-race-%d.ts", index)))
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			_, err := reader.Read(make([]byte, 1))
			result <- err
		}()
		observed.waitForTimerCount(t, 30*time.Second, index+1)
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		if err := receiveProgressiveError(t, result); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("stalled Read after Close = %v, want %v", err, os.ErrClosed)
		}
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Close after read/Close races: %v", err)
	}
}

func TestTranscodeManagerCloseWaitsForBlockedPreStartWork(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	before := progressiveOpenFileCount(t)
	request := fixture.request("blocked.ts")
	blockedLog := request.OutputPath + ".stderr.log"
	if err := syscall.Mkfifo(blockedLog, 0o600); err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			releaseBlockedProgressiveStart(t, blockedLog)
		}
		closeProgressiveManager(t, manager)
	}()

	opened := make(chan error, 1)
	go func() {
		_, err := manager.OpenProgressive(context.Background(), request)
		opened <- err
	}()
	assertProgressivePending(t, opened)
	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	assertProgressivePending(t, closed)
	releaseBlockedProgressiveStart(t, blockedLog)
	released = true
	if err := receiveProgressiveError(t, opened); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("blocked OpenProgressive error = %v, want %v", err, ErrManagerClosed)
	}
	if err := receiveProgressiveError(t, closed); err != nil {
		t.Fatalf("Close after blocked start: %v", err)
	}
	if after := progressiveOpenFileCount(t); after > before+1 {
		t.Fatalf("Close-racing pre-start work leaked descriptors: before=%d after=%d", before, after)
	}
}

func TestBlockedPreStartFilesystemWorkDoesNotBlockOtherRequests(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	blocked := fixture.request("blocked.ts")
	blockedLog := blocked.OutputPath + ".stderr.log"
	if err := syscall.Mkfifo(blockedLog, 0o600); err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			releaseBlockedProgressiveStart(t, blockedLog)
		}
	}()

	first := make(chan progressiveOpenResult, 1)
	go func() {
		reader, err := manager.OpenProgressive(context.Background(), blocked)
		first <- progressiveOpenResult{reader: reader, err: err}
	}()
	assertProgressivePending(t, first)
	secondResult := make(chan progressiveOpenResult, 1)
	go func() {
		reader, err := manager.OpenProgressive(context.Background(), fixture.request("responsive.ts"))
		secondResult <- progressiveOpenResult{reader: reader, err: err}
	}()
	second := receiveProgressiveOpen(t, secondResult)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	releaseBlockedProgressiveStart(t, blockedLog)
	released = true
	firstReader := receiveProgressiveOpen(t, first)
	if err := firstReader.Close(); err != nil {
		t.Fatalf("closing blocked-start reader: %v", err)
	}
}

func TestCloseKillsProcessGroupWhenLeaderExitsAfterQ(t *testing.T) {
	fixture := newProgressiveFixture(t, "q-exits-with-child")
	manager := fixture.manager(t, clock.NewReal())
	reader, err := manager.OpenProgressive(context.Background(), fixture.request("leader-exits.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, reader)
	leader := waitForPID(t, fixture.parentPID)
	child := waitForPID(t, fixture.childPID)
	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	waitForFile(t, fixture.qFile)
	if err := receiveProgressiveError(t, closed); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := syscall.Kill(-leader, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("Close returned while leader process group remains: %v", err)
	}
	if err := syscall.Kill(child, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("Close returned while child remains: %v", err)
	}
}

func TestFailedStartsCloseEveryPreStartDescriptor(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	before := progressiveOpenFileCount(t)
	for index := range 32 {
		request := fixture.request(fmt.Sprintf("failed-%d.ts", index))
		if err := os.Mkdir(request.OutputPath+".stderr.log", 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.OpenProgressive(context.Background(), request); err == nil {
			t.Fatalf("OpenProgressive succeeded despite stderr directory for %q", request.OutputPath)
		}
	}
	after := progressiveOpenFileCount(t)
	if after > before+1 {
		t.Fatalf("failed pre-start attempts leaked descriptors: before=%d after=%d", before, after)
	}
}

func TestOpenProgressiveRejectsSymlinkedParentWithoutMutatingTarget(t *testing.T) {
	fixture := newProgressiveFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.ts")
	victimLog := victim + ".stderr.log"
	if err := os.WriteFile(victim, []byte("preserve-output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victimLog, []byte("preserve-stderr"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(fixture.dir, "linked-parent")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	request := fixture.request(filepath.Join("linked-parent", "victim.ts"))
	reader, err := manager.OpenProgressive(context.Background(), request)
	if reader != nil {
		_ = reader.Close()
	}
	if err == nil {
		t.Fatal("OpenProgressive accepted an output beneath a symlinked parent")
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "preserve-output" {
		t.Fatalf("outside output = %q, %v; want preserved target", got, err)
	}
	if got, err := os.ReadFile(victimLog); err != nil || string(got) != "preserve-stderr" {
		t.Fatalf("outside stderr = %q, %v; want preserved target", got, err)
	}
}
func TestProgressiveDescriptorsAreNotInheritedByLaterTranscodes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor identity comparison requires /proc/$$/fd and /dev/fd -ef, which are Linux-only")
	}
	fixture := newProgressiveFixture(t, "check-inherited-fds")
	manager := fixture.manager(t, clock.NewReal())
	defer closeProgressiveManager(t, manager)
	first, err := manager.OpenProgressive(context.Background(), fixture.request("first.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, first)
	internal, ok := first.(*progressiveReader)
	if !ok {
		t.Fatalf("reader type = %T, want *progressiveReader", first)
	}
	if internal.job.stderr == nil {
		t.Fatal("running transcode has no stderr log descriptor")
	}
	assertProgressiveCloseOnExec(t, internal.file)
	assertProgressiveCloseOnExec(t, internal.job.stderr)
	if err := os.Remove(fixture.inherited); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.inheritedDone); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	t.Setenv("TARGET_FDS", fmt.Sprintf("%d %d", internal.file.Fd(), internal.job.stderr.Fd()))
	t.Setenv("TARGET_FILE", internal.file.Name())
	t.Setenv("TARGET_STDERR", internal.job.stderr.Name())
	t.Setenv("CONTROL_FD", "3")
	t.Setenv("CONTROL_FILE", fixture.input)
	second, err := manager.OpenProgressive(context.Background(), fixture.request("second.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeProgressiveReader(t, second)
	waitForFile(t, fixture.inheritedDone)
	tags := readLinesIfPresent(t, fixture.inherited)
	assertDescriptorTagSet(t, tags)
}

// assertDescriptorTagSet requires the exact tag set that a healthy
// check-inherited-fds run emits: one control tag and nothing else. The
// fixture script can also emit missing-control or inherited, and a future
// edit can add a new tag. Each of those must fail the test, not pass
// silently. A switch without a default case let missing-control and any
// new tag through, so the assertion proved less than it claimed.
func assertDescriptorTagSet(t *testing.T, tags []string) {
	t.Helper()
	const wantControl = "control"
	seen := make(map[string]int)
	for _, tag := range tags {
		seen[tag]++
	}
	control := seen[wantControl]
	if control == 0 {
		t.Fatal("child descriptor 3 does not match the input source; the positive control failed, so the identity check proved nothing")
	}
	if control > 1 {
		t.Fatalf("control tag emitted %d times, want exactly one", control)
	}
	for _, tag := range tags {
		switch tag {
		case wantControl:
			// the one expected tag
		case "inherited":
			t.Fatal("later transcode inherited a first-transcode descriptor by identity (same device and inode)")
		case "missing-control":
			t.Fatal("the positive control reported missing-control alongside a control tag; the fixture emitted contradictory tags")
		default:
			t.Fatalf("unexpected descriptor tag %q; the fixture emitted a tag the test does not recognize", tag)
		}
	}
}

// TestDescriptorTagSetProbe is a disposable probe. It runs only under
// PROBE_UNEXPECTED_TAG=1 and must FAIL. The parent test invokes it as a
// subprocess to prove that assertDescriptorTagSet rejects an unexpected tag.
func TestDescriptorTagSetProbe(t *testing.T) {
	if os.Getenv("PROBE_UNEXPECTED_TAG") != "1" {
		t.Skip("probe runs only under PROBE_UNEXPECTED_TAG=1; the parent test invokes it as a subprocess")
	}
	assertDescriptorTagSet(t, []string{"control", "bogus-tag"})
}

// TestDescriptorTagSetRejectsUnexpectedTag proves the hardened assertion
// fails on an unexpected tag. It runs the disposable probe as a subprocess
// and asserts the subprocess failed with the unexpected-tag diagnostic.
func TestDescriptorTagSetRejectsUnexpectedTag(t *testing.T) {
	if os.Getenv("PROBE_UNEXPECTED_TAG") == "1" {
		return // we are the subprocess
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDescriptorTagSetProbe$")
	cmd.Env = append(os.Environ(), "PROBE_UNEXPECTED_TAG=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("probe subprocess passed; expected it to fail with an unexpected-tag diagnostic")
	}
	if !strings.Contains(string(output), "unexpected descriptor tag") {
		t.Fatalf("probe output did not contain the unexpected-tag diagnostic; output: %s", output)
	}
}

type progressiveFixture struct {
	ffmpeg, dir, input                string
	argv, start, starts, parentPID    string
	childPID, qFile, goFile, appended string
	inherited, inheritedDone          string
}

func newProgressiveFixture(t *testing.T, mode string) progressiveFixture {
	t.Helper()
	root := t.TempDir()
	fixture := progressiveFixture{
		dir:           filepath.Join(root, "transcodes"),
		input:         filepath.Join(root, "input.mp4"),
		argv:          filepath.Join(root, "argv"),
		start:         filepath.Join(root, "started"),
		starts:        filepath.Join(root, "starts"),
		parentPID:     filepath.Join(root, "parent.pid"),
		childPID:      filepath.Join(root, "child.pid"),
		qFile:         filepath.Join(root, "q"),
		goFile:        filepath.Join(root, "go"),
		appended:      filepath.Join(root, "appended"),
		inherited:     filepath.Join(root, "inherited-fds"),
		inheritedDone: filepath.Join(root, "inherited-fds-done"),
	}
	if err := os.WriteFile(fixture.input, []byte("progressive source"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGV_FILE", fixture.argv)
	t.Setenv("START_FILE", fixture.start)
	t.Setenv("STARTS_FILE", fixture.starts)
	t.Setenv("PARENT_PID", fixture.parentPID)
	t.Setenv("CHILD_PID", fixture.childPID)
	t.Setenv("Q_FILE", fixture.qFile)
	t.Setenv("GO_FILE", fixture.goFile)
	t.Setenv("APPENDED_FILE", fixture.appended)
	t.Setenv("INHERITED_FDS_FILE", fixture.inherited)
	t.Setenv("INHERITED_DONE_FILE", fixture.inheritedDone)
	t.Setenv("PROGRESSIVE_MODE", mode)
	fixture.ffmpeg = progressiveFakeFFmpeg(t, mode)
	reapPIDFilesOnCleanup(t, fixture.parentPID, fixture.childPID)
	return fixture
}

func (f progressiveFixture) manager(t *testing.T, testClock clock.Clock) *TranscodeManager {
	t.Helper()
	manager, err := NewTranscodeManager(f.ffmpeg, f.dir, TranscodeManagerOptions{
		Clock:                  progressiveTestClock(testClock),
		OutputPollInterval:     100 * time.Millisecond,
		ReaderRetryInterval:    50 * time.Millisecond,
		ReaderStallTimeout:     30 * time.Second,
		ProgressiveIdleTimeout: 10 * time.Second,
		KillGracePeriod:        5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func (f progressiveFixture) request(name string) ProgressiveRequest {
	output := filepath.Join(f.dir, name)
	return ProgressiveRequest{
		OutputPath:     output,
		Args:           []string{"-i", "file:" + f.input, output},
		PlaySessionID:  "session",
		DeviceID:       "device",
		SourceRevision: "progressive-test-source-revision",
		OpenSource: func() (*sourcefile.Source, error) {
			return sourcefile.Open(f.input, sourcefile.Expected{})
		},
	}
}

func (f progressiveFixture) waitStarted(t *testing.T) {
	t.Helper()
	waitForFile(t, f.start)
}

func progressiveFakeFFmpeg(t *testing.T, _ string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	body := `out=
for argument do out=$argument; done
case "$PROGRESSIVE_MODE" in
early-error)
  printf '%s\n' "$$" >> "$STARTS_FILE"
  printf '%s\n' "$$" >> "$PARENT_PID"
  : > "$START_FILE"
  printf 'diagnostic-marker\n' >&2
  dd if=/dev/zero bs=1024 count=256 2>/dev/null | tr '\000' x >&2
  exit 23
  ;;
grow) printf first > "$out" ;;
*) : > "$out" ;;
esac
printf '%s\n' "$@" > "$ARGV_FILE"
printf '%s\n' "$$" >> "$STARTS_FILE"
printf '%s\n' "$$" >> "$PARENT_PID"
: > "$START_FILE"
case "$PROGRESSIVE_MODE" in
hold)
  while IFS= read -r command; do
    if [ "$command" = q ]; then : > "$Q_FILE"; exit 0; fi
  done
  ;;
grow)
  while [ ! -e "$GO_FILE" ]; do kill -0 "$HARNESS_PID" 2>/dev/null || exit 1; sleep 0.01; done
  printf second >> "$out"
  : > "$APPENDED_FILE"
  while IFS= read -r command; do
    if [ "$command" = q ]; then : > "$Q_FILE"; exit 0; fi
  done
  ;;
check-inherited-fds)
  # Positive control: child descriptor 3 must be the input source by
  # identity (same device and inode, compared through /proc and -ef).
  if [ -n "${CONTROL_FD:-}" ] && [ -e "/proc/$$/fd/${CONTROL_FD}" ] && [ "/proc/$$/fd/${CONTROL_FD}" -ef "${CONTROL_FILE:-}" ]; then
    printf 'control\n' >> "$INHERITED_FDS_FILE"
  else
    printf 'missing-control\n' >> "$INHERITED_FDS_FILE"
  fi
  # Target check: each first-transcode descriptor must not be present in
  # the child by identity.  Compare /proc/$$/fd/N against the expected
  # path with -ef; a number that happens to be open is not inheritance.
  fds=${TARGET_FDS:-}
  if [ -n "$fds" ]; then
    fd1=${fds%% *}
    fd2=${fds##* }
    for pair in "$fd1:${TARGET_FILE:-}" "$fd2:${TARGET_STDERR:-}"; do
      fd=${pair%%:*}
      expected=${pair#*:}
      if [ -e "/proc/$$/fd/$fd" ] && [ "/proc/$$/fd/$fd" -ef "$expected" ]; then
        printf 'inherited\n' >> "$INHERITED_FDS_FILE"
      fi
    done
  fi
  : > "$INHERITED_DONE_FILE"
  while IFS= read -r command; do
    if [ "$command" = q ]; then : > "$Q_FILE"; exit 0; fi
  done
  ;;
exit-on-go)
  while [ ! -e "$GO_FILE" ]; do kill -0 "$HARNESS_PID" 2>/dev/null || exit 1; sleep 0.01; done
  exit 0
  ;;
ignore-q-with-child)
  trap '' TERM
  sh -c 'trap "" TERM; printf "%s\n" "$$" >> "$CHILD_PID"; cat <&"$HARNESS_FD" >/dev/null' &
  while IFS= read -r command; do : > "$Q_FILE"; done
  ;;
q-exits-with-child)
  sh -c 'trap "" TERM; exec </dev/null >/dev/null 2>&1; printf "%s\n" "$$" >> "$CHILD_PID"; cat <&"$HARNESS_FD" >/dev/null' &
  while IFS= read -r command; do
    if [ "$command" = q ]; then : > "$Q_FILE"; exit 0; fi
  done
  ;;
*) exit 97 ;;
esac
`
	if err := os.WriteFile(path, []byte(fixtureShellHeader(t)+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func receiveReaders(t *testing.T, readers <-chan io.ReadCloser, errs <-chan error) (io.ReadCloser, io.ReadCloser) {
	t.Helper()
	got := make([]io.ReadCloser, 0, 2)
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	for len(got) != 2 {
		select {
		case reader := <-readers:
			got = append(got, reader)
		case err := <-errs:
			t.Fatal(err)
		case <-deadline.C:
			t.Fatal("timed out opening shared progressive readers")
		}
	}
	return got[0], got[1]
}

func readProgressiveString(t *testing.T, reader io.Reader) string {
	t.Helper()
	result := make(chan struct {
		text string
		err  error
	}, 1)
	go func() {
		buffer := make([]byte, 16)
		n, err := reader.Read(buffer)
		result <- struct {
			text string
			err  error
		}{string(buffer[:n]), err}
	}()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	select {
	case result := <-result:
		if result.err != nil && !errors.Is(result.err, io.EOF) {
			t.Fatalf("progressive reader: %v", result.err)
		}
		return result.text
	case <-deadline.C:
		t.Fatal("timed out waiting for progressive bytes")
		return ""
	}
}

func receiveProgressiveError(t *testing.T, errors <-chan error) error {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	select {
	case err := <-errors:
		return err
	case <-deadline.C:
		t.Fatal("timed out waiting for progressive operation")
		return nil
	}
}

func closeProgressiveManager(t *testing.T, manager *TranscodeManager) {
	t.Helper()
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("manager cleanup: %v", err)
	}
}

func closeProgressiveReader(t *testing.T, reader io.ReadCloser) {
	t.Helper()
	if err := reader.Close(); err != nil {
		t.Fatalf("reader cleanup: %v", err)
	}
}

type progressiveTimerClock struct {
	fake clock.Clock
}

func progressiveTestClock(value clock.Clock) clock.Clock {
	if fake, ok := value.(*clock.Fake); ok {
		return progressiveTimerClock{fake: fake}
	}
	return value
}

func (c progressiveTimerClock) Now() time.Time {
	return c.fake.Now()
}

func (c progressiveTimerClock) NewTimer(delay time.Duration) clock.Timer {
	if delay < time.Second {
		return clock.NewReal().NewTimer(delay)
	}
	return c.fake.NewTimer(delay)
}

type progressiveObservedClock struct {
	clock.Clock
	mu      sync.Mutex
	created map[time.Duration]int
}

func newProgressiveObservedClock(base clock.Clock) *progressiveObservedClock {
	return &progressiveObservedClock{Clock: base, created: make(map[time.Duration]int)}
}

// NewTimer registers the timer with the base clock before it counts it, so a
// test that sees the count can advance the fake clock past the timer.
func (c *progressiveObservedClock) NewTimer(delay time.Duration) clock.Timer {
	timer := c.Clock.NewTimer(delay)
	c.mu.Lock()
	c.created[delay]++
	c.mu.Unlock()
	return timer
}

func (c *progressiveObservedClock) timerCount(delay time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.created[delay]
}

func (c *progressiveObservedClock) waitForTimer(t *testing.T, delay time.Duration) {
	t.Helper()
	c.waitForTimerCount(t, delay, 1)
}

func (c *progressiveObservedClock) waitForTimerCount(t *testing.T, delay time.Duration, want int) {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	for c.timerCount(delay) < want {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d registrations of %s timer; got %d", want, delay, c.timerCount(delay))
		case <-time.After(time.Millisecond):
		}
	}
}

type progressiveOpenResult struct {
	reader io.ReadCloser
	err    error
}

func receiveProgressiveOpen(t *testing.T, results <-chan progressiveOpenResult) io.ReadCloser {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.reader == nil {
			t.Fatal("OpenProgressive returned nil reader without an error")
		}
		return result.reader
	case <-deadline.C:
		t.Fatal("timed out opening progressive reader")
		return nil
	}
}

func receiveProgressiveOpenFailure(t *testing.T, results <-chan progressiveOpenResult) error {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	select {
	case result := <-results:
		if result.reader != nil {
			_ = result.reader.Close()
		}
		return result.err
	case <-deadline.C:
		t.Fatal("timed out waiting for progressive open failure")
		return nil
	}
}

func waitForProgressiveSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	select {
	case <-signal:
	case <-deadline.C:
		t.Fatalf("timed out waiting for %s", description)
	}
}

func assertProgressivePending[T any](t *testing.T, results <-chan T) {
	t.Helper()
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-results:
		t.Fatal("progressive operation completed while it should remain blocked")
	case <-timer.C:
	}
}

func waitForProgressiveStartRecords(t *testing.T, path string, want int) []string {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	for {
		if lines := readLinesIfPresent(t, path); len(lines) >= want {
			return lines
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d complete start records in %q", want, path)
		case <-time.After(time.Millisecond):
		}
	}
}

func waitForProgressiveRemoval(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for progressive output removal %q", path)
		case <-time.After(time.Millisecond):
		}
	}
}

func readLinesIfPresent(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func releaseBlockedProgressiveStart(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertProgressiveCloseOnExec(t *testing.T, file *os.File) {
	t.Helper()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), uintptr(syscall.F_GETFD), 0)
	if errno != 0 {
		t.Fatalf("F_GETFD for %q: %v", file.Name(), errno)
	}
	if flags&syscall.FD_CLOEXEC == 0 {
		t.Fatalf("descriptor %q lacks FD_CLOEXEC", file.Name())
	}
}

func progressiveOpenFileCount(t *testing.T) int {
	t.Helper()
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatalf("getting descriptor limit: %v", err)
	}
	max := int(limit.Cur)
	if max > 4096 {
		max = 4096
	}
	count := 0
	for fd := range max {
		_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_GETFD), 0)
		if errno != syscall.EBADF {
			count++
		}
	}
	return count
}

func parseProgressivePID(raw string) (int, error) {
	var pid int
	_, err := fmt.Sscan(strings.TrimSpace(raw), &pid)
	return pid, err
}
