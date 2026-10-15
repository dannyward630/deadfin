// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/probe"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

const packagedMP3RuntimeImage = "deadfin-final-replay-app:local"

// TestScannerPersistsPackagedFFprobeMP3RuntimeTicks runs real generated media
// through the packaged Debian ffprobe, scanner, SQLite store, and /Items
// projection. Debian ffprobe 5.1.9 reports the Xing/LAME stream and format as
// 1.044898 seconds, then exposes the encoder delay and trailing padding only in
// packet Skip Samples side data. Stock persists the playable 1.000000 seconds.
func TestScannerPersistsPackagedFFprobeMP3RuntimeTicks(t *testing.T) {
	packagedMP3RuntimePrerequisite(t)
	stage := packagedMP3RuntimeStage(t)
	gaplessPaths := make([]string, 0, 11)
	for number := 1; number <= 11; number++ {
		track := strconv.Itoa(number)
		gaplessPaths = append(gaplessPaths, packagedMP3CopyFixture(t, stage, "gapless-"+track+".mp3", "tools/goldenrec/fixtures/out/music/Goldenrec Generator/Synthetic Album/"+track+" - Synthetic Track "+track+".mp3"))
	}
	videoPath := packagedMP3CopyFixture(t, stage, "video-control.mkv", "tools/goldenrec/fixtures/out/movies/Standalone Movie (2023)/Standalone Movie (2023).mkv")
	nonGaplessPath := packagedMP3WriteNonGaplessFixture(t, stage)

	packagedFFprobe := packagedMP3RuntimeWrapper(t, stage)
	prober, err := ffmpeg.NewProber(t.Context(), packagedFFprobe)
	if err != nil {
		t.Fatalf("initialize packaged Debian ffprobe: %v", err)
	}
	mediaProbe := &packagedMP3RuntimeProbe{prober: prober, raw: make(map[string][]byte)}
	items, scanner := packagedMP3RuntimeScanner(t, stage, mediaProbe)

	result, err := scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan real packaged-ffprobe fixtures: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("real packaged-ffprobe fixture scan errors = %v", result.Errors)
	}

	assertPackagedGaplessMP3ProbeFacts(t, mediaProbe.probeOutput(t, gaplessPaths[0]), packagedMP3PacketOutput(t, packagedFFprobe, gaplessPaths[0]))
	assertPackagedNonGaplessMP3ProbeFacts(t, mediaProbe.probeOutput(t, nonGaplessPath), packagedMP3PacketOutput(t, packagedFFprobe, nonGaplessPath))
	assertPackagedVideoProbeFacts(t, mediaProbe.probeOutput(t, videoPath))

	gapless := make([]item.Item, len(gaplessPaths))
	for index, path := range gaplessPaths {
		gapless[index] = scannerItemAtPath(t, items, path)
		track := strconv.Itoa(index + 1)
		t.Run("generated Xing/LAME MP3 track "+track+" persistence", func(t *testing.T) {
			if gapless[index].RunTimeTicks == nil || *gapless[index].RunTimeTicks != 10_000_000 {
				t.Fatalf("persisted RunTimeTicks = %v, want 10000000", gapless[index].RunTimeTicks)
			}
			if gapless[index].Kind != item.KindAudio {
				t.Fatalf("persisted item kind = %s, want Audio", gapless[index].Kind)
			}
		})
	}
	nonGapless := scannerItemAtPath(t, items, nonGaplessPath)
	video := scannerItemAtPath(t, items, videoPath)
	for _, tc := range []struct {
		name string
		item item.Item
		want int64
	}{
		{name: "non-gapless MP3", item: nonGapless, want: 10_448_750},
		{name: "video container control", item: video, want: 10_210_000},
	} {
		t.Run(tc.name+" persistence", func(t *testing.T) {
			if tc.item.RunTimeTicks == nil || *tc.item.RunTimeTicks != tc.want {
				t.Fatalf("persisted RunTimeTicks = %v, want %d", tc.item.RunTimeTicks, tc.want)
			}
		})
	}
	if nonGapless.Kind != item.KindAudio || video.Kind != item.KindMovie {
		t.Fatalf("scanned control kinds = non-gapless:%s video:%s, want Audio/Movie", nonGapless.Kind, video.Kind)
	}

	routes := router.New()
	handlers.RegisterItems(routes, items, dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)), clock.NewReal())
	for _, profile := range freshMediaWireProfiles {
		t.Run(profile.name+" Items", func(t *testing.T) {
			for index, candidate := range gapless {
				track, candidate := strconv.Itoa(index+1), candidate
				t.Run("generated Xing/LAME MP3 track "+track, func(t *testing.T) {
					row := freshMediaItemsRow(t, routes, profile, candidate.ID, "")
					freshMediaAssertRaw(t, row, profile.key("RunTimeTicks"), "10000000")
				})
			}
			for _, tc := range []struct {
				name string
				id   dto.UUID
				want int64
			}{
				{name: "non-gapless MP3", id: nonGapless.ID, want: 10_448_750},
				{name: "video container control", id: video.ID, want: 10_210_000},
			} {
				t.Run(tc.name, func(t *testing.T) {
					row := freshMediaItemsRow(t, routes, profile, tc.id, "")
					freshMediaAssertRaw(t, row, profile.key("RunTimeTicks"), strconv.FormatInt(tc.want, 10))
				})
			}
		})
	}
}

func packagedMP3RuntimePrerequisite(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("packaged MP3 runtime integration requires Docker: %v", err)
	}
	if output, err := exec.Command("docker", "image", "inspect", packagedMP3RuntimeImage).CombinedOutput(); err != nil {
		t.Skipf("packaged MP3 runtime integration requires %s: %v\n%s", packagedMP3RuntimeImage, err, output)
	}
	output, err := exec.Command("docker", "run", "--rm", "--entrypoint", "ffprobe", packagedMP3RuntimeImage, "-version").CombinedOutput()
	if err != nil {
		t.Fatalf("run packaged ffprobe: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "ffprobe version 5.1.") {
		t.Fatalf("packaged ffprobe version = %q, want Debian ffprobe 5.1", strings.SplitN(string(output), "\n", 2)[0])
	}
}

func packagedMP3RuntimeStage(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate MP3 runtime test source")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	stage, err := os.MkdirTemp(repository, ".packaged-mp3-runtime-")
	if err != nil {
		t.Fatalf("create Docker-visible MP3 fixture stage: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(stage); err != nil {
			t.Errorf("remove packaged MP3 fixture stage: %v", err)
		}
	})
	return stage
}

func packagedMP3CopyFixture(t *testing.T, stage, name, relative string) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate MP3 runtime test source")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	bytes, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read real generated fixture %q: %v", relative, err)
	}
	path := filepath.Join(stage, name)
	if err := os.WriteFile(path, bytes, 0o600); err != nil {
		t.Fatalf("stage real generated fixture %q: %v", relative, err)
	}
	return path
}

func packagedMP3WriteNonGaplessFixture(t *testing.T, stage string) string {
	t.Helper()
	path := filepath.Join(stage, "non-gapless.mp3")
	output, err := exec.Command("docker", "run", "--rm", "--entrypoint", "ffmpeg", "-v", stage+":/media", packagedMP3RuntimeImage,
		"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100", "-t", "1", "-c:a", "libmp3lame", "-write_xing", "0", "/media/non-gapless.mp3").CombinedOutput()
	if err != nil {
		t.Fatalf("generate real non-gapless MP3 with packaged ffmpeg: %v\n%s", err, output)
	}
	return path
}

// packagedMP3RuntimeWrapper is a transport adapter only: it maps host fixture
// paths into the package-matched Debian runtime and executes its real ffprobe.
func packagedMP3RuntimeWrapper(t *testing.T, stage string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	script := "#!/usr/bin/env python3\n" +
		"import os\nimport sys\n" +
		"root = " + strconv.Quote(stage) + "\n" +
		"image = " + strconv.Quote(packagedMP3RuntimeImage) + "\n" +
		"args = sys.argv[1:]\n" +
		"if len(args) >= 2 and args[-2] == '-i' and args[-1] == '/dev/fd/3':\n" +
		"    descriptor = os.fstat(3)\n" +
		"    for directory, _, names in os.walk(root):\n" +
		"        for name in names:\n" +
		"            candidate = os.path.join(directory, name)\n" +
		"            candidate_stat = os.stat(candidate)\n" +
		"            if candidate_stat.st_dev == descriptor.st_dev and candidate_stat.st_ino == descriptor.st_ino:\n" +
		"                args[-1] = candidate\n" +
		"                break\n" +
		"        if args[-1] != '/dev/fd/3':\n" +
		"            break\n" +
		"    if args[-1] == '/dev/fd/3':\n" +
		"        raise RuntimeError('pinned fixture descriptor is outside stage root')\n" +
		"if len(args) >= 2 and args[-2] == '-i' and (args[-1] == root or args[-1].startswith(root + os.sep)):\n" +
		"    args[-1] = '/media/' + os.path.relpath(args[-1], root)\n" +
		"os.execvp('docker', ['docker', 'run', '--rm', '-v', root + ':/media:ro', '--entrypoint', 'ffprobe', image, *args])\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write packaged ffprobe wrapper: %v", err)
	}
	return path
}

func packagedMP3RuntimeScanner(t *testing.T, source string, mediaProbe library.MediaProbe) (*store.ItemStore, *library.Scanner) {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open packaged MP3 scanner SQLite stores: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close packaged MP3 scanner SQLite stores: %v", err)
		}
	})
	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		t.Fatalf("open packaged MP3 scanner topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close packaged MP3 scanner topology: %v", err)
		}
	})
	if _, err := topology.AddVirtualFolder(t.Context(), "Packaged MP3", "music", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add packaged MP3 scanner source: %v", err)
	}
	items := store.NewItemStore(stores)
	scanner := newScanner(t, topology, items, library.NewChain(&packagedMP3RuntimeResolver{}), library.ScannerOptions{Concurrency: 1, MediaProbe: mediaProbe})
	t.Cleanup(func() {
		if err := scanner.Close(); err != nil {
			t.Errorf("close packaged MP3 scanner: %v", err)
		}
	})
	return items, scanner
}

type packagedMP3RuntimeResolver struct{ scannerFixtureResolver }

func (r *packagedMP3RuntimeResolver) Resolve(ctx context.Context, input library.ResolveContext) (*item.Item, error) {
	value, err := r.scannerFixtureResolver.Resolve(ctx, input)
	if err != nil || input.Entry.IsDir {
		return value, err
	}
	if strings.EqualFold(filepath.Ext(input.Entry.Path), ".mp3") {
		value.Kind = item.KindAudio
	}
	return value, nil
}

type packagedMP3RuntimeProbe struct {
	prober ffmpeg.Prober
	mu     sync.Mutex
	raw    map[string][]byte
}

func (p *packagedMP3RuntimeProbe) Probe(ctx context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	source, err := sourcefile.Open(input.Entry.Path, packagedMP3RuntimeExpected(input.Entry))
	if err != nil {
		return library.MediaProbeResult{}, err
	}
	defer func() { _ = source.Close() }()
	raw, err := p.prober.ProbeSourceAndValidate(ctx, source)
	if err != nil {
		return library.MediaProbeResult{}, err
	}
	p.mu.Lock()
	p.raw[input.Entry.Path] = append([]byte(nil), raw...)
	p.mu.Unlock()
	definition, found := item.Definition(input.Item.Kind)
	info, err := probe.Normalize(ctx, raw, probe.Options{IsAudio: found && definition.DefaultMediaType == item.MediaTypeAudio, Path: input.Entry.Path, Protocol: "File", VideoType: item.VideoTypeVideoFile.String()})
	if err != nil {
		return library.MediaProbeResult{}, err
	}
	return library.MediaProbeResult{Container: info.Container, Size: info.Size, RunTimeTicks: info.RunTimeTicks, TotalBitrate: info.Bitrate, Streams: info.MediaStreams}, nil
}

func packagedMP3RuntimeExpected(entry library.Entry) sourcefile.Expected {
	size := entry.Size
	expected := sourcefile.Expected{Size: &size}
	if !entry.Modified.IsZero() {
		modified := entry.Modified
		expected.ModifiedAt = &modified
	}
	if entry.Identity.Valid {
		inode := int64(entry.Identity.Inode)
		changed := entry.Identity.Changed
		expected.Inode, expected.ChangedAt = &inode, &changed
	}
	return expected
}

func (p *packagedMP3RuntimeProbe) probeOutput(t *testing.T, path string) []byte {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	raw := p.raw[path]
	if len(raw) == 0 {
		t.Fatalf("packaged ffprobe did not inspect %q", path)
	}
	return append([]byte(nil), raw...)
}

func packagedMP3PacketOutput(t *testing.T, ffprobePath, inputPath string) []byte {
	t.Helper()
	output, err := exec.Command(ffprobePath, "-v", "warning", "-print_format", "json", "-show_packets", "-show_entries", "packet=stream_index,duration,side_data_list", "-i", inputPath).Output()
	if err != nil {
		t.Fatalf("read real packaged MP3 packets: %v", err)
	}
	return output
}

type packagedMP3RawProbe struct {
	Streams []struct {
		Duration string `json:"duration"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Packets []struct {
		SideData []struct {
			Type           string `json:"side_data_type"`
			SkipSamples    int    `json:"skip_samples"`
			DiscardPadding int    `json:"discard_padding"`
		} `json:"side_data_list"`
	} `json:"packets"`
}

func decodePackagedMP3RawProbe(t *testing.T, raw []byte) packagedMP3RawProbe {
	t.Helper()
	var result packagedMP3RawProbe
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode real packaged ffprobe output: %v", err)
	}
	return result
}

func assertPackagedGaplessMP3ProbeFacts(t *testing.T, raw, packets []byte) {
	t.Helper()
	result := decodePackagedMP3RawProbe(t, raw)
	packetResult := decodePackagedMP3RawProbe(t, packets)
	if result.Format.Duration != "1.044898" || len(result.Streams) != 1 || result.Streams[0].Duration != "1.044898" {
		t.Fatalf("packaged ffprobe Xing/LAME duration = format:%q streams:%#v, want padded 1.044898", result.Format.Duration, result.Streams)
	}
	if !packagedMP3HasSkipSamples(packetResult, 1105, 0) || !packagedMP3HasSkipSamples(packetResult, 0, 875) {
		t.Fatalf("packaged ffprobe Xing/LAME packets = %#v, want encoder delay 1105 and padding 875", packetResult.Packets)
	}
}

func assertPackagedNonGaplessMP3ProbeFacts(t *testing.T, raw, packets []byte) {
	t.Helper()
	result := decodePackagedMP3RawProbe(t, raw)
	packetResult := decodePackagedMP3RawProbe(t, packets)
	if result.Format.Duration != "1.044875" || len(result.Streams) != 1 || result.Streams[0].Duration != "1.044875" {
		t.Fatalf("packaged ffprobe non-gapless duration = format:%q streams:%#v, want 1.044875", result.Format.Duration, result.Streams)
	}
	for _, packet := range packetResult.Packets {
		for _, sideData := range packet.SideData {
			if sideData.Type == "Skip Samples" {
				t.Fatalf("non-gapless MP3 unexpectedly contains Skip Samples: %#v", packetResult.Packets)
			}
		}
	}
}

func assertPackagedVideoProbeFacts(t *testing.T, raw []byte) {
	t.Helper()
	result := decodePackagedMP3RawProbe(t, raw)
	if result.Format.Duration != "1.021000" {
		t.Fatalf("packaged ffprobe video format duration = %q, want 1.021000", result.Format.Duration)
	}
	if len(result.Packets) != 0 {
		t.Fatalf("video control unexpectedly requested MP3 packet data: %#v", result.Packets)
	}
}

func packagedMP3HasSkipSamples(result packagedMP3RawProbe, skip, discard int) bool {
	for _, packet := range result.Packets {
		for _, sideData := range packet.SideData {
			if sideData.Type == "Skip Samples" && sideData.SkipSamples == skip && sideData.DiscardPadding == discard {
				return true
			}
		}
	}
	return false
}
