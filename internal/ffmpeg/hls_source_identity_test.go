// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

const (
	hlsEmbeddedSubtitleToken = "subtitles=f='/media/it'\\''s\\: weird,dir/input'"
	hlsExternalSubtitleToken = "subtitles=f='/media/external\\: weird,dir/input.srt'"
)

func TestHLSSessionKeyIsCollisionFree(t *testing.T) {
	first := hlsSessionKey("a\x00b", "c")
	second := hlsSessionKey("a", "b\x00c")
	if first == second {
		t.Fatalf("NUL-delimited collision candidates shared session key %q", first)
	}
	if got := hlsSessionKey("a\x00b", "c"); got != first {
		t.Fatalf("identical session facts key = %q, want stable %q", got, first)
	}
}

func TestHLSEmbeddedSubtitleDescriptorExecutesViaFD4AcrossPathReplacement(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	t.Setenv("HLS_COPY_FD4", "1")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)

	const original = "original source bytes"
	input := writeHLSSource(t, fixture.root, "fd4-input", original)
	replacement := filepath.Join(fixture.root, "fd4-input.replacement")
	if err := os.WriteFile(replacement, []byte("replacement source bytes"), 0o600); err != nil {
		t.Fatalf("write replacement source: %v", err)
	}
	probe := newHLSSourceProbe(input, sourcefile.Expected{})
	request := fixture.request("fd4-input", 4)
	filter := "setparams=range=tv," + hlsEmbeddedSubtitleToken + ":si=5,setpts=PTS-0/TB"
	request.Args = []string{"--fixture-label=fd4-input", "-i", "file:" + input, "-vf", filter}
	request.SourceRevision = "revision-fd4"
	request.OpenSource = probe.Open

	request.EmbeddedSubtitleSource = hlsEmbeddedSubtitleToken
	var inherited []*os.File
	var auxiliaryOffset int64
	manager.startCommand = func(command *exec.Cmd) error {
		inherited = append([]*os.File(nil), command.ExtraFiles...)
		var err error
		auxiliaryOffset, err = inherited[1].Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if err := os.Rename(replacement, input); err != nil {
			return err
		}
		return command.Start()
	}
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("first StartHLS: %v", err)
	}
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("joined StartHLS: %v", err)
	}
	fixture.waitStarted(t, "fd4-input")
	waitForFile(t, fixture.marker("fd4", "fd4-input"))

	wantArgv := []string{"--fixture-label=fd4-input", "-i", sourcefile.FDPath(3), "-vf", strings.Replace(filter, hlsEmbeddedSubtitleToken, "subtitles=f="+sourcefile.FDPath(4), 1)}
	if got := readLines(t, fixture.marker("argv", "fd4-input")); !reflect.DeepEqual(got, wantArgv) {
		t.Fatalf("child argv = %#v, want %#v", got, wantArgv)
	}
	if auxiliaryOffset != 0 {
		t.Fatalf("auxiliary descriptor offset before child launch = %d, want zero", auxiliaryOffset)
	}
	if _, err := os.Stat(fixture.marker("fd4-missing", "fd4-input")); err == nil {
		t.Fatal("child could not read inherited fd4")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat child fd4 failure marker: %v", err)
	}
	if got, err := os.ReadFile(fixture.marker("fd4", "fd4-input")); err != nil || string(got) != original {
		t.Fatalf("child fd4 bytes/error = %q/%v, want original pinned bytes", got, err)
	}
	if len(inherited) != 2 {
		t.Fatalf("inherited descriptors = %d, want primary fd3 and independent fd4", len(inherited))
	}
	if _, err := inherited[0].Stat(); err != nil {
		t.Fatalf("retained primary descriptor Stat after start = %v", err)
	}
	if _, err := inherited[1].Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("launch-only auxiliary descriptor Stat after start = %v, want os.ErrClosed", err)
	}
	if got := probe.OpenCount(); got != 1 {
		t.Fatalf("source opens across joined family = %d, want one", got)
	}
	canonicalPlaylist, err := manager.normalizeOutputPath(request.PlaylistPath)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	job := manager.jobs[canonicalPlaylist]
	var canonicalArgs []string
	if job != nil {
		canonicalArgs = append([]string(nil), job.args...)
	}
	manager.mu.Unlock()
	if job == nil || !reflect.DeepEqual(canonicalArgs, request.Args) {
		t.Fatalf("canonical HLS argv = %#v, want path-shaped request argv %#v", canonicalArgs, request.Args)
	}

	pid := fixture.waitPID(t, "fd4-input")
	if err := manager.StopPlaySession(t.Context(), request.PlaySessionID, request.DeviceID); err != nil {
		t.Fatalf("StopPlaySession: %v", err)
	}
	fixture.waitQ(t, "fd4-input")
	waitForProcessExit(t, pid)
	probe.AssertClosed(t)
}

func TestHLSExternalSubtitleDescriptorExecutesViaPinnedFD4(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	t.Setenv("HLS_COPY_FD4", "1")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)

	input := writeHLSSource(t, fixture.root, "external-fd4-input", "primary source bytes")
	subtitle := writeHLSSource(t, fixture.root, "external-fd4.srt", "original subtitle bytes")
	replacement := filepath.Join(fixture.root, "external-fd4.replacement")
	if err := os.WriteFile(replacement, []byte("replacement subtitle bytes"), 0o600); err != nil {
		t.Fatalf("write replacement subtitle: %v", err)
	}
	sourceProbe := newHLSSourceProbe(input, sourcefile.Expected{})
	subtitleProbe := newHLSSourceProbe(subtitle, sourcefile.Expected{})
	request := fixture.request("external-fd4", 4)
	filter := "scale=1280:720," + hlsExternalSubtitleToken
	request.Args = []string{"--fixture-label=external-fd4", "-i", "file:" + input, "-vf", filter}
	request.SourceRevision = "external-fd4-source-revision"
	request.OpenSource = sourceProbe.Open
	request.ExternalSubtitleSource = hlsExternalSubtitleToken
	request.ExternalSubtitleRevision = "external-fd4-subtitle-revision"
	request.OpenExternalSubtitle = subtitleProbe.Open

	var inherited []*os.File
	manager.startCommand = func(command *exec.Cmd) error {
		inherited = append([]*os.File(nil), command.ExtraFiles...)
		if err := os.Rename(replacement, subtitle); err != nil {
			return err
		}
		return command.Start()
	}
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("StartHLS: %v", err)
	}
	fixture.waitStarted(t, "external-fd4")
	waitForFile(t, fixture.marker("fd4", "external-fd4"))

	wantArgv := []string{
		"--fixture-label=external-fd4", "-i", sourcefile.FDPath(3), "-vf",
		strings.Replace(filter, hlsExternalSubtitleToken, "subtitles=f="+sourcefile.FDPath(4), 1),
	}
	if got := readLines(t, fixture.marker("argv", "external-fd4")); !reflect.DeepEqual(got, wantArgv) {
		t.Fatalf("child argv = %#v, want %#v", got, wantArgv)
	}
	if got, err := os.ReadFile(fixture.marker("fd4", "external-fd4")); err != nil || string(got) != "original subtitle bytes" {
		t.Fatalf("child fd4 bytes/error = %q/%v, want original pinned subtitle", got, err)
	}
	if len(inherited) != 2 {
		t.Fatalf("inherited descriptors = %d, want primary fd3 and subtitle fd4", len(inherited))
	}
	if _, err := inherited[1].Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("parent subtitle descriptor Stat after start = %v, want os.ErrClosed", err)
	}
	subtitleProbe.AssertClosed(t)

	pid := fixture.waitPID(t, "external-fd4")
	if err := manager.StopPlaySession(t.Context(), request.PlaySessionID, request.DeviceID); err != nil {
		t.Fatalf("StopPlaySession: %v", err)
	}
	fixture.waitQ(t, "external-fd4")
	waitForProcessExit(t, pid)
	sourceProbe.AssertClosed(t)
}

func TestHLSSourceDescriptorExecutesViaFD3AndJoinsOnce(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)

	input := writeHLSSource(t, fixture.root, "fd3-input", "source bytes")
	probe := newHLSSourceProbe(input, sourcefile.Expected{})
	request := fixture.request("fd3-input", 4)
	request.Args = []string{"--fixture-label=fd3-input", "-i", "file:" + input}
	request.SourceRevision = "revision-fd3"
	request.OpenSource = probe.Open

	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("first StartHLS: %v", err)
	}
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("joined StartHLS: %v", err)
	}
	fixture.waitStarted(t, "fd3-input")

	if got := readLines(t, fixture.marker("argv", "fd3-input")); !reflect.DeepEqual(got, []string{"--fixture-label=fd3-input", "-i", sourcefile.FDPath(3)}) {
		t.Fatalf("child argv = %#v, want inherited descriptor input only", got)
	}
	canonicalPlaylist, err := manager.normalizeOutputPath(request.PlaylistPath)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	job := manager.jobs[canonicalPlaylist]
	var canonicalArgs []string
	if job != nil {
		canonicalArgs = append(canonicalArgs, job.args...)
	}
	manager.mu.Unlock()
	if job == nil {
		t.Fatal("started HLS family is absent")
	}
	if !reflect.DeepEqual(canonicalArgs, request.Args) {
		t.Fatalf("canonical HLS argv = %#v, want path-shaped request argv %#v", canonicalArgs, request.Args)
	}
	if got := probe.OpenCount(); got != 1 {
		t.Fatalf("source opens = %d, want one leader-owned descriptor across join", got)
	}

	pid := fixture.waitPID(t, "fd3-input")
	if err := manager.StopPlaySession(t.Context(), request.PlaySessionID, request.DeviceID); err != nil {
		t.Fatalf("StopPlaySession: %v", err)
	}
	fixture.waitQ(t, "fd3-input")
	waitForProcessExit(t, pid)
	probe.AssertClosed(t)
}

func TestHLSEmbeddedSubtitleRestartReopensPinnedDescriptorPair(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)

	input := writeHLSSource(t, fixture.root, "restart-fd4-input", "source bytes")
	probe := newHLSSourceProbe(input, sourcefile.Expected{})
	request := fixture.request("restart-fd4", 4)
	filter := "scale=1280:720," + hlsEmbeddedSubtitleToken + ":si=5"
	request.Args = []string{"--fixture-label=restart-fd4", "-i", "file:" + input, "-vf", filter}
	request.SourceRevision = "revision-restart-fd4"
	request.OpenSource = probe.Open

	request.EmbeddedSubtitleSource = hlsEmbeddedSubtitleToken
	var inherited [][]*os.File
	manager.startCommand = func(command *exec.Cmd) error {
		inherited = append(inherited, append([]*os.File(nil), command.ExtraFiles...))
		return command.Start()
	}
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("StartHLS: %v", err)
	}
	fixture.waitStarted(t, "restart-fd4")
	oldPID := fixture.waitPID(t, "restart-fd4")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "old segment")
	fixture.writeSegment(t, request, 1, "old successor")

	if err := manager.RestartHLSAt(t.Context(), request, 17); err != nil {
		t.Fatalf("RestartHLSAt: %v", err)
	}
	if got := len(waitForHLSStarts(t, fixture, "restart-fd4", 2)); got != 2 {
		t.Fatalf("starts after forced restart = %d, want two", got)
	}
	waitForProcessExit(t, oldPID)
	if got := probe.OpenCount(); got != 2 {
		t.Fatalf("source opens across successful restart = %d, want fresh primary pair", got)
	}
	if len(inherited) != 2 {
		t.Fatalf("launch descriptor pairs = %d, want one pair per process", len(inherited))
	}
	for generation, pair := range inherited {
		if len(pair) != 2 {
			t.Fatalf("generation %d inherited descriptors = %d, want fd3/fd4", generation, len(pair))
		}
		if _, err := pair[1].Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("generation %d auxiliary Stat after successful start = %v, want os.ErrClosed", generation, err)
		}
	}
	if got := readLines(t, fixture.marker("argv", "restart-fd4")); !reflect.DeepEqual(got, []string{"--fixture-label=restart-fd4", "-i", sourcefile.FDPath(3), "-vf", strings.Replace(filter, hlsEmbeddedSubtitleToken, "subtitles=f="+sourcefile.FDPath(4), 1)}) {
		t.Fatalf("replacement argv = %#v, want fd3/fd4 rewrite", got)
	}

	newPID := fixture.waitPID(t, "restart-fd4")
	if err := manager.StopPlaySession(t.Context(), request.PlaySessionID, request.DeviceID); err != nil {
		t.Fatalf("StopPlaySession after restart: %v", err)
	}
	fixture.waitQ(t, "restart-fd4")
	waitForProcessExit(t, newPID)
	probe.AssertClosed(t)
}

func TestStartHLSRejectsEqualArgsWithDifferentSourceRevision(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)

	input := writeHLSSource(t, fixture.root, "revision-input", "source bytes")
	firstProbe := newHLSSourceProbe(input, sourcefile.Expected{})
	request := fixture.request("revision-family", 4)
	request.Args = []string{"--fixture-label=revision-family", "-i", "file:" + input}
	request.SourceRevision = "revision-a"
	request.OpenSource = firstProbe.Open
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("first StartHLS: %v", err)
	}
	fixture.waitStarted(t, "revision-family")

	secondProbe := newHLSSourceProbe(input, sourcefile.Expected{})
	changed := request
	changed.SourceRevision = "revision-b"
	changed.OpenSource = secondProbe.Open
	if err := manager.StartHLS(t.Context(), changed); err == nil {
		t.Fatal("StartHLS joined an equal argv family with a different source revision")
	}
	if got := hlsStartCount(t, fixture, "revision-family"); got != 1 {
		t.Fatalf("family starts = %d, want one process after revision conflict", got)
	}
	if got := firstProbe.OpenCount(); got != 1 {
		t.Fatalf("first revision source opens = %d, want one", got)
	}
	if got := secondProbe.OpenCount(); got != 0 {
		t.Fatalf("conflicting revision source opens = %d, want none", got)
	}
}

func TestRestartHLSAfterAtomicSourceReplacementTearsDownStaleFamily(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)

	input := writeHLSSource(t, fixture.root, "replacement-input", "original bytes")
	probe := newHLSSourceProbe(input, hlsSourceExpected(t, input))
	request := fixture.request("replacement-family", 4)
	request.Args = []string{"--fixture-label=replacement-family", "-i", "file:" + input, "-vf", "scale=1280:720," + hlsEmbeddedSubtitleToken + ":si=5"}
	request.SourceRevision = "revision-before-replacement"
	request.OpenSource = probe.Open
	request.EmbeddedSubtitleSource = hlsEmbeddedSubtitleToken
	var inherited []*os.File
	manager.startCommand = func(command *exec.Cmd) error {
		inherited = append([]*os.File(nil), command.ExtraFiles...)
		return command.Start()
	}
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("StartHLS: %v", err)
	}
	fixture.waitStarted(t, "replacement-family")
	pid := fixture.waitPID(t, "replacement-family")
	fixture.writePlaylist(t, request)
	fixture.writeSegment(t, request, 0, "old segment")
	fixture.writeSegment(t, request, 1, "old successor")

	replacement := filepath.Join(fixture.root, "replacement-input.next")
	if err := os.WriteFile(replacement, []byte("replacement bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, input); err != nil {
		t.Fatalf("atomically replace source: %v", err)
	}

	err := manager.RestartHLSAt(t.Context(), request, 100)
	if !errors.Is(err, sourcefile.ErrStaleSource) {
		t.Fatalf("RestartHLSAt after source replacement = %v, want ErrStaleSource", err)
	}
	fixture.waitQ(t, "replacement-family")
	waitForProcessExit(t, pid)
	if got := hlsStartCount(t, fixture, "replacement-family"); got != 1 {
		t.Fatalf("family starts after stale replacement = %d, want original process only", got)
	}
	if got := probe.OpenCount(); got != 1 {
		t.Fatalf("source opens after stale replacement = %d, want original descriptor only", got)
	}
	if len(inherited) != 2 {
		t.Fatalf("stale family inherited descriptors = %d, want fd3/fd4", len(inherited))
	}
	if _, statErr := inherited[1].Stat(); !errors.Is(statErr, os.ErrClosed) {
		t.Fatalf("stale family auxiliary Stat = %v, want os.ErrClosed", statErr)
	}
	probe.AssertClosed(t)
	for _, path := range []string{request.PlaylistPath, fixture.segmentPath(request, 0), fixture.segmentPath(request, 1)} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("stale family artifact %q remains available after teardown: %v", path, statErr)
		}
	}
	if _, waitErr := manager.WaitSegment(t.Context(), request.PlaylistPath, 0); waitErr == nil {
		t.Fatal("WaitSegment served a segment from the stale family")
	}
	manager.mu.Lock()
	jobs := len(manager.jobs)
	manager.mu.Unlock()
	if jobs != 0 {
		t.Fatalf("stale family remains registered after teardown: jobs=%d", jobs)
	}
}

func TestHLSSourceDescriptorClosesAfterStartupFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inject func(*TranscodeManager, error)
	}{
		{name: "stdin pipe", inject: func(manager *TranscodeManager, sentinel error) {
			manager.stdinPipe = func(*exec.Cmd) (io.WriteCloser, error) { return nil, sentinel }
		}},
		{name: "process start", inject: func(manager *TranscodeManager, sentinel error) {
			manager.startCommand = func(*exec.Cmd) error { return sentinel }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHLSFixture(t, "hold")
			manager := fixture.manager(t, clock.NewReal())
			defer closeHLSManager(t, manager)

			input := writeHLSSource(t, fixture.root, "failed-input", "source bytes")
			probe := newHLSSourceProbe(input, sourcefile.Expected{})
			request := fixture.request("failed-"+tc.name, 4)
			request.Args = []string{"--fixture-label=failed-" + tc.name, "-i", "file:" + input}
			request.SourceRevision = "revision-failed-" + tc.name
			request.OpenSource = probe.Open
			sentinel := errors.New("injected " + tc.name + " failure")
			tc.inject(manager, sentinel)

			if err := manager.StartHLS(t.Context(), request); !errors.Is(err, sentinel) {
				t.Fatalf("StartHLS error = %v, want injected failure", err)
			}
			if got := probe.OpenCount(); got != 1 {
				t.Fatalf("source opens = %d, want one before injected failure", got)
			}
			probe.AssertClosed(t)

			if got := hlsStartCount(t, fixture, "failed-"+tc.name); got != 0 {
				t.Fatalf("failed start launched %d child processes, want none", got)
			}
		})
	}
}

func TestHLSEmbeddedSubtitleDescriptorClosesBothDescriptorsAfterStartupFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inject func(*TranscodeManager, error, *[]*os.File)
	}{
		{name: "stdin pipe", inject: func(manager *TranscodeManager, sentinel error, inherited *[]*os.File) {
			manager.stdinPipe = func(command *exec.Cmd) (io.WriteCloser, error) {
				*inherited = append([]*os.File(nil), command.ExtraFiles...)
				return nil, sentinel
			}
		}},
		{name: "process start", inject: func(manager *TranscodeManager, sentinel error, inherited *[]*os.File) {
			manager.startCommand = func(command *exec.Cmd) error {
				*inherited = append([]*os.File(nil), command.ExtraFiles...)
				return sentinel
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHLSFixture(t, "hold")
			manager := fixture.manager(t, clock.NewReal())
			defer closeHLSManager(t, manager)
			input := writeHLSSource(t, fixture.root, "failed-fd4-input", "source bytes")
			probe := newHLSSourceProbe(input, sourcefile.Expected{})
			request := fixture.request("failed-fd4-"+tc.name, 4)
			request.Args = []string{"--fixture-label=failed-fd4-" + tc.name, "-i", "file:" + input, "-vf", "scale=1280:720," + hlsEmbeddedSubtitleToken + ":si=5"}
			request.SourceRevision = "revision-failed-fd4-" + tc.name
			request.OpenSource = probe.Open
			request.EmbeddedSubtitleSource = hlsEmbeddedSubtitleToken
			sentinel := errors.New("injected " + tc.name + " failure")
			var inherited []*os.File
			tc.inject(manager, sentinel, &inherited)

			if err := manager.StartHLS(t.Context(), request); !errors.Is(err, sentinel) {
				t.Fatalf("StartHLS error = %v, want injected failure", err)
			}
			if len(inherited) != 2 {
				t.Fatalf("failed start inherited descriptors = %d, want fd3/fd4", len(inherited))
			}
			for descriptor, file := range inherited {
				if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("failed start descriptor %d Stat = %v, want os.ErrClosed", descriptor+3, err)
				}
			}
			probe.AssertClosed(t)
			if got := hlsStartCount(t, fixture, "failed-fd4-"+tc.name); got != 0 {
				t.Fatalf("failed start launched %d child processes, want none", got)
			}
		})
	}
}

func TestStartHLSRejectsInvalidEmbeddedSubtitleSourceOperand(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter string
	}{
		{name: "absent", filter: "scale=1280:720"},
		{name: "duplicated", filter: "scale=1280:720," + hlsEmbeddedSubtitleToken + "," + hlsEmbeddedSubtitleToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHLSFixture(t, "hold")
			manager := fixture.manager(t, clock.NewReal())
			defer closeHLSManager(t, manager)
			input := writeHLSSource(t, fixture.root, "invalid-fd4-input", "source bytes")
			probe := newHLSSourceProbe(input, sourcefile.Expected{})
			request := fixture.request("invalid-fd4-"+tc.name, 4)
			request.Args = []string{"--fixture-label=invalid-fd4-" + tc.name, "-i", "file:" + input, "-vf", tc.filter}
			request.OpenSource = probe.Open
			request.EmbeddedSubtitleSource = hlsEmbeddedSubtitleToken

			if err := manager.StartHLS(t.Context(), request); err == nil {
				t.Fatal("StartHLS accepted a missing or duplicated embedded subtitle operand")
			}
			if got := probe.OpenCount(); got != 0 {
				t.Fatalf("invalid operand source opens = %d, want validation before opening", got)
			}
			if got := hlsStartCount(t, fixture, "invalid-fd4-"+tc.name); got != 0 {
				t.Fatalf("invalid operand launched %d child processes, want none", got)
			}
		})
	}

	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)
	request := fixture.request("missing-fd4-opener", 4)
	request.Args = []string{"--fixture-label=missing-fd4-opener", "-i", "file:/media/input.mkv", "-vf", "scale=1280:720," + hlsEmbeddedSubtitleToken + ":si=5"}
	request.EmbeddedSubtitleSource = hlsEmbeddedSubtitleToken
	if err := manager.StartHLS(t.Context(), request); err == nil {
		t.Fatal("StartHLS accepted an embedded subtitle descriptor without a source opener")
	}
	if got := hlsStartCount(t, fixture, "missing-fd4-opener"); got != 0 {
		t.Fatalf("missing-opener request launched %d child processes, want none", got)
	}
}

func TestStartHLSRejectsIncompleteExternalSubtitleAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*HLSRequest)
	}{
		{name: "missing revision", mutate: func(request *HLSRequest) {
			request.ExternalSubtitleRevision = ""
		}},
		{name: "missing subtitle opener", mutate: func(request *HLSRequest) {
			request.OpenExternalSubtitle = nil
		}},
		{name: "missing primary opener", mutate: func(request *HLSRequest) {
			request.OpenSource = nil
		}},
		{name: "missing filter source", mutate: func(request *HLSRequest) {
			request.ExternalSubtitleSource = ""
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHLSFixture(t, "hold")
			manager := fixture.manager(t, clock.NewReal())
			defer closeHLSManager(t, manager)
			input := writeHLSSource(t, fixture.root, "external-authority-"+test.name, "source bytes")
			subtitle := writeHLSSource(t, fixture.root, "external-authority-"+test.name+".srt", "subtitle bytes")
			sourceProbe := newHLSSourceProbe(input, sourcefile.Expected{})
			subtitleProbe := newHLSSourceProbe(subtitle, sourcefile.Expected{})
			request := fixture.request("external-authority-"+test.name, 4)
			request.Args = []string{
				"--fixture-label=external-authority-" + test.name,
				"-i", "file:" + input, "-vf", "scale=1280:720," + hlsExternalSubtitleToken,
			}
			request.SourceRevision = "external-authority-source-revision"
			request.OpenSource = sourceProbe.Open
			request.ExternalSubtitleSource = hlsExternalSubtitleToken
			request.ExternalSubtitleRevision = "external-authority-subtitle-revision"
			request.OpenExternalSubtitle = subtitleProbe.Open
			test.mutate(&request)

			if err := manager.StartHLS(t.Context(), request); err == nil {
				t.Fatal("StartHLS accepted incomplete external subtitle authority")
			}
			if sourceProbe.OpenCount() != 0 || subtitleProbe.OpenCount() != 0 {
				t.Fatal("invalid external subtitle authority opened a descriptor")
			}
		})
	}
}

func TestStartHLSRejectsEqualArgsWithDifferentEmbeddedSubtitleSource(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)

	const alternateToken = "subtitles=f='/media/alternate\\: source.mkv'"
	input := writeHLSSource(t, fixture.root, "subtitle-conflict-input", "source bytes")
	firstProbe := newHLSSourceProbe(input, sourcefile.Expected{})
	request := fixture.request("subtitle-conflict", 4)
	request.Args = []string{"--fixture-label=subtitle-conflict", "-i", "file:" + input, "-vf", "scale=1280:720," + hlsEmbeddedSubtitleToken + ":si=5," + alternateToken + ":si=6"}
	request.SourceRevision = "subtitle-conflict-revision"
	request.OpenSource = firstProbe.Open
	request.EmbeddedSubtitleSource = hlsEmbeddedSubtitleToken
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("first StartHLS: %v", err)
	}
	fixture.waitStarted(t, "subtitle-conflict")

	secondProbe := newHLSSourceProbe(input, sourcefile.Expected{})
	changed := request
	changed.OpenSource = secondProbe.Open
	changed.EmbeddedSubtitleSource = alternateToken
	if err := manager.StartHLS(t.Context(), changed); err == nil {
		t.Fatal("StartHLS joined equal argv family with a different embedded subtitle source")
	}
	if got := hlsStartCount(t, fixture, "subtitle-conflict"); got != 1 {
		t.Fatalf("subtitle source conflict starts = %d, want one", got)
	}
	if got := secondProbe.OpenCount(); got != 0 {
		t.Fatalf("conflicting subtitle source opens = %d, want none", got)
	}
}

func TestStartHLSRejectsEqualArgsWithDifferentExternalSubtitleRevision(t *testing.T) {
	fixture := newHLSFixture(t, "hold")
	manager := fixture.manager(t, clock.NewReal())
	defer closeHLSManager(t, manager)

	input := writeHLSSource(t, fixture.root, "external-conflict-input", "source bytes")
	subtitle := writeHLSSource(t, fixture.root, "external-conflict.srt", "subtitle bytes")
	sourceProbe := newHLSSourceProbe(input, sourcefile.Expected{})
	firstSubtitleProbe := newHLSSourceProbe(subtitle, sourcefile.Expected{})
	request := fixture.request("external-conflict", 4)
	request.Args = []string{
		"--fixture-label=external-conflict", "-i", "file:" + input,
		"-vf", "scale=1280:720," + hlsExternalSubtitleToken,
	}
	request.SourceRevision = "external-conflict-source-revision"
	request.OpenSource = sourceProbe.Open
	request.ExternalSubtitleSource = hlsExternalSubtitleToken
	request.ExternalSubtitleRevision = "external-subtitle-revision-a"
	request.OpenExternalSubtitle = firstSubtitleProbe.Open
	if err := manager.StartHLS(t.Context(), request); err != nil {
		t.Fatalf("first StartHLS: %v", err)
	}
	fixture.waitStarted(t, "external-conflict")

	secondSubtitleProbe := newHLSSourceProbe(subtitle, sourcefile.Expected{})
	changed := request
	changed.ExternalSubtitleRevision = "external-subtitle-revision-b"
	changed.OpenExternalSubtitle = secondSubtitleProbe.Open
	if err := manager.StartHLS(t.Context(), changed); err == nil {
		t.Fatal("StartHLS joined a family with a different external subtitle revision")
	}
	if got := hlsStartCount(t, fixture, "external-conflict"); got != 1 {
		t.Fatalf("family starts = %d, want one after subtitle revision conflict", got)
	}
	if got := secondSubtitleProbe.OpenCount(); got != 0 {
		t.Fatalf("conflicting external subtitle opens = %d, want zero", got)
	}
}

type hlsSourceProbe struct {
	path     string
	expected sourcefile.Expected

	mu      sync.Mutex
	opens   int
	sources []*sourcefile.Source
}

func newHLSSourceProbe(path string, expected sourcefile.Expected) *hlsSourceProbe {
	return &hlsSourceProbe{path: path, expected: expected}
}

func (probe *hlsSourceProbe) Open() (*sourcefile.Source, error) {
	source, err := sourcefile.Open(probe.path, probe.expected)
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if err == nil {
		probe.opens++
		probe.sources = append(probe.sources, source)
	}
	return source, err
}

func (probe *hlsSourceProbe) OpenCount() int {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return probe.opens
}

func (probe *hlsSourceProbe) AssertClosed(t *testing.T) {
	t.Helper()
	probe.mu.Lock()
	sources := append([]*sourcefile.Source(nil), probe.sources...)
	probe.mu.Unlock()
	if len(sources) == 0 {
		t.Fatal("source opener did not return a descriptor")
	}
	for index, source := range sources {
		if _, err := source.File().Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("manager-owned source descriptor %d Stat after teardown = %v, want os.ErrClosed", index, err)
		}
	}
}

func writeHLSSource(t *testing.T, root, name, contents string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func hlsSourceExpected(t *testing.T, path string) sourcefile.Expected {
	t.Helper()
	source, err := sourcefile.Open(path, sourcefile.Expected{})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := source.Fingerprint()
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	size, inode := fingerprint.Size, int64(fingerprint.Ino)
	modified, changed := fingerprint.MTime, fingerprint.CTime
	return sourcefile.Expected{
		Size:       &size,
		ModifiedAt: &modified,
		Inode:      &inode,
		ChangedAt:  &changed,
	}
}
