// SPDX-License-Identifier: GPL-2.0-only

package keyframes_test

// The Matroska Cues reader ports Jellyfin b04614e18d2aeed8d3d3de920c381c6bd5b97bdb:
// Jellyfin.MediaEncoding.Keyframes/Matroska/MatroskaKeyframeExtractor.cs and
// Matroska/Extensions/EbmlReaderExtensions.cs. Upstream has no unit tests for it.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/keyframes"
)

// ebml encodes one element with a minimal-length size VINT.
func ebml(id uint64, body ...[]byte) []byte {
	payload := bytes.Join(body, nil)
	var out []byte
	for shift := 24; shift >= 0; shift -= 8 {
		if b := byte(id >> shift); b != 0 || len(out) > 0 {
			out = append(out, b)
		}
	}
	size := uint64(len(payload))
	length := 1
	for size >= (uint64(1)<<(7*length))-1 {
		length++
	}
	encoded := make([]byte, length)
	for index := length - 1; index >= 0; index-- {
		encoded[index] = byte(size)
		size >>= 8
	}
	encoded[0] |= byte(0x80 >> (length - 1))
	return append(append(out, encoded...), payload...)
}

func ebmlUint(id, value uint64) []byte {
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, value)
	raw = bytes.TrimLeft(raw, "\x00")
	if len(raw) == 0 {
		raw = []byte{0}
	}
	return ebml(id, raw)
}

func ebmlFloat(id uint64, value float64) []byte {
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, math.Float64bits(value))
	return ebml(id, raw)
}

type matroskaFixture struct {
	scale       uint64
	duration    *float64
	tracks      [][2]uint64 // number, type
	cues        [][]uint64  // time, then track numbers of each CueTrackPositions
	omitCuesRef bool
	cuesBody    []byte
}

// build lays out EBML header, Segment{SeekHead, Void, Info, Tracks, Cluster, Cues}
// with SeekHead positions relative to the Segment data start.
func (fixture matroskaFixture) build() []byte {
	var infoChildren [][]byte
	if fixture.scale != 0 {
		infoChildren = append(infoChildren, ebmlUint(0x2AD7B1, fixture.scale))
	}
	if fixture.duration != nil {
		infoChildren = append(infoChildren, ebmlFloat(0x4489, *fixture.duration))
	}
	info := ebml(0x1549A966, infoChildren...)
	var entries [][]byte
	for _, track := range fixture.tracks {
		entries = append(entries, ebml(0xAE, ebmlUint(0xD7, track[0]), ebmlUint(0x73C5, 99), ebmlUint(0x83, track[1])))
	}
	tracks := ebml(0x1654AE6B, entries...)
	cluster := ebml(0x1F43B675, ebmlUint(0xE7, 0), bytes.Repeat([]byte{0xA3, 0x84, 0x81, 0, 0, 0x80}, 16))
	cues := fixture.cuesBody
	if cues == nil {
		var points [][]byte
		for _, cue := range fixture.cues {
			children := [][]byte{ebmlUint(0xB3, cue[0])}
			for _, track := range cue[1:] {
				children = append(children, ebml(0xB7, ebmlUint(0xF7, track), ebmlUint(0xF1, 1234)))
			}
			points = append(points, ebml(0xBB, children...))
		}
		cues = ebml(0x1C53BB6B, points...)
	}

	seekEntry := func(id uint32, position uint64) []byte {
		raw := make([]byte, 4)
		binary.BigEndian.PutUint32(raw, id)
		return ebml(0x4DBB, ebml(0x53AB, raw), ebmlUint(0x53AC, position))
	}
	// Fixed-width positions keep the SeekHead size independent of its values.
	seekHeadFor := func(info, tracks, cues uint64) []byte {
		pos := func(value uint64) []byte {
			raw := make([]byte, 8)
			binary.BigEndian.PutUint64(raw, value)
			return raw
		}
		entries := [][]byte{
			ebml(0x4DBB, ebml(0x53AB, []byte{0x15, 0x49, 0xA9, 0x66}), ebml(0x53AC, pos(info))),
			ebml(0x4DBB, ebml(0x53AB, []byte{0x16, 0x54, 0xAE, 0x6B}), ebml(0x53AC, pos(tracks))),
		}
		if !fixture.omitCuesRef {
			entries = append(entries, ebml(0x4DBB, ebml(0x53AB, []byte{0x1C, 0x53, 0xBB, 0x6B}), ebml(0x53AC, pos(cues))))
		}
		entries = append(entries, seekEntry(0x1254C367, 0))
		return ebml(0x114D9B74, entries...)
	}
	void := ebml(0xEC, make([]byte, 7))
	headLength := uint64(len(seekHeadFor(0, 0, 0)) + len(void))
	infoPos := headLength
	tracksPos := infoPos + uint64(len(info))
	cuesPos := tracksPos + uint64(len(tracks)) + uint64(len(cluster))
	segment := ebml(0x18538067, seekHeadFor(infoPos, tracksPos, cuesPos), void, info, tracks, cluster, cues)
	header := ebml(0x1A45DFA3, ebml(0x4282, []byte("matroska")))
	return append(header, segment...)
}

func float(value float64) *float64 { return &value }

func extractBytes(t *testing.T, raw []byte) (keyframes.Data, error) {
	t.Helper()
	return keyframes.ExtractMatroska(t.Context(), bytes.NewReader(raw), int64(len(raw)))
}

func TestExtractMatroskaReadsFirstVideoTrackCues(t *testing.T) {
	raw := matroskaFixture{
		scale:    1_000_000,
		duration: float(12_345.6),
		tracks:   [][2]uint64{{1, 2}, {2, 1}, {3, 1}},
		cues: [][]uint64{
			{0, 2}, {0, 1}, {2_002, 2}, {4_004, 3}, {5_000, 1, 2}, {6_006, 2, 1}, {8_008, 2},
		},
	}.build()
	got, err := extractBytes(t, raw)
	if err != nil {
		t.Fatalf("ExtractMatroska() error = %v", err)
	}
	// Track 2 is the first video track. The {6006, 2, 1} cue counts because its
	// first CueTrackPositions names track 2; {5000, 1, 2} does not count because
	// upstream reads only the first CueTrackPositions.
	want := keyframes.Data{TotalDurationTicks: 123_456_000, KeyframeTicks: []int64{0, 20_020_000, 60_060_000, 80_080_000}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractMatroska() = %#v, want %#v", got, want)
	}
}

func TestExtractMatroskaScalesTimestampsAndDuration(t *testing.T) {
	cases := map[string]struct {
		fixture matroskaFixture
		want    keyframes.Data
	}{
		"default scale without duration": {
			fixture: matroskaFixture{tracks: [][2]uint64{{1, 1}}, cues: [][]uint64{{0, 1}, {1_001, 1}}},
			want:    keyframes.Data{TotalDurationTicks: 0, KeyframeTicks: []int64{0, 10_010_000}},
		},
		"microsecond scale truncates ticks": {
			fixture: matroskaFixture{scale: 1_001, duration: float(2_000.5), tracks: [][2]uint64{{1, 1}}, cues: [][]uint64{{0, 1}, {1_999, 1}}},
			// 1999*1001/100 = 20009.99 → 20009; 2000.5*1001/100 = 20025.005 → 20025.
			want: keyframes.Data{TotalDurationTicks: 20_025, KeyframeTicks: []int64{0, 20_009}},
		},
		"duration rounds half to even": {
			fixture: matroskaFixture{scale: 50, duration: float(5), tracks: [][2]uint64{{1, 1}}, cues: [][]uint64{{0, 1}}},
			want:    keyframes.Data{TotalDurationTicks: 2, KeyframeTicks: []int64{0}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := extractBytes(t, tc.fixture.build())
			if err != nil {
				t.Fatalf("ExtractMatroska() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractMatroska() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestExtractMatroskaRejectsUnusableFiles(t *testing.T) {
	valid := matroskaFixture{scale: 1_000_000, duration: float(10_000), tracks: [][2]uint64{{1, 1}}, cues: [][]uint64{{0, 1}, {5_000, 1}}}.build()
	if _, err := extractBytes(t, valid); err != nil {
		t.Fatalf("valid fixture error = %v", err)
	}
	cases := map[string]struct {
		raw  []byte
		want error
	}{
		"empty":              {raw: nil, want: keyframes.ErrMatroskaMalformed},
		"not matroska":       {raw: []byte("RIFF\x00\x00\x00\x00WAVEfmt "), want: keyframes.ErrMatroskaMalformed},
		"zero bytes":         {raw: make([]byte, 4096), want: keyframes.ErrMatroskaMalformed},
		"truncated header":   {raw: valid[:3], want: keyframes.ErrMatroskaMalformed},
		"truncated seekhead": {raw: valid[:60], want: keyframes.ErrMatroskaMalformed},
		"truncated cues":     {raw: valid[:len(valid)-5], want: keyframes.ErrMatroskaMalformed},
		"no cues reference": {
			raw:  matroskaFixture{scale: 1_000_000, tracks: [][2]uint64{{1, 1}}, cues: [][]uint64{{0, 1}}, omitCuesRef: true}.build(),
			want: keyframes.ErrMatroskaMalformed,
		},
		"no video track": {
			raw:  matroskaFixture{scale: 1_000_000, tracks: [][2]uint64{{1, 2}, {2, 17}}, cues: [][]uint64{{0, 1}}}.build(),
			want: keyframes.ErrMatroskaMalformed,
		},
		"no video cues": {
			raw:  matroskaFixture{scale: 1_000_000, tracks: [][2]uint64{{1, 1}, {2, 2}}, cues: [][]uint64{{0, 2}, {10, 2}}}.build(),
			want: keyframes.ErrNoKeyframes,
		},
		"cue point overruns cues": {
			raw:  matroskaFixture{scale: 1_000_000, tracks: [][2]uint64{{1, 1}}, cuesBody: ebml(0x1C53BB6B, []byte{0xBB, 0x8F, 0xB3, 0x81, 0x00})}.build(),
			want: keyframes.ErrMatroskaMalformed,
		},
		"invalid child id": {
			raw:  matroskaFixture{scale: 1_000_000, tracks: [][2]uint64{{1, 1}}, cuesBody: ebml(0x1C53BB6B, []byte{0x00, 0x81, 0x00})}.build(),
			want: keyframes.ErrMatroskaMalformed,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := extractBytes(t, tc.raw)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ExtractMatroska() = %#v, %v; want %v", got, err, tc.want)
			}
		})
	}
	// Every prefix of a valid file fails cleanly; none panics or succeeds with
	// partial cues.
	for length := range len(valid) {
		if _, err := extractBytes(t, valid[:length]); err == nil {
			t.Fatalf("prefix of %d/%d bytes succeeded", length, len(valid))
		}
	}
}

func TestExtractMatroskaHonorsCanceledContext(t *testing.T) {
	raw := matroskaFixture{scale: 1_000_000, tracks: [][2]uint64{{1, 1}}, cues: [][]uint64{{0, 1}}}.build()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := keyframes.ExtractMatroska(ctx, bytes.NewReader(raw), int64(len(raw))); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExtractMatroska() error = %v, want context.Canceled", err)
	}
}

// generateMatroska encodes a short clip with an irregular GOP. A seekable
// output gets Cues; a pipe output gets none.
func generateMatroska(t *testing.T, withCues bool) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	path := filepath.Join(t.TempDir(), "generated.mkv")
	args := []string{
		"-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24000/1001",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "20", "-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "37", "-keyint_min", "5", "-sc_threshold", "0",
		"-force_key_frames", "3.1,7.4,7.9",
		"-c:a", "aac", "-f", "matroska",
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	var command *exec.Cmd
	if withCues {
		command = exec.CommandContext(ctx, ffmpeg, append(args, path)...)
	} else {
		output, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer output.Close()
		command = exec.CommandContext(ctx, ffmpeg, append(args, "pipe:1")...)
		command.Stdout = output
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("generate matroska (cues %t): %v\n%s", withCues, err, stderr.Bytes())
	}
	return path
}

// TestMatroskaExtractorMatchesFfprobeOnGeneratedFile defends segment
// agreement: the Cues list the same keyframe times ffmpeg cuts at, which the
// ffprobe packet scan reports.
func TestMatroskaExtractorMatchesFfprobeOnGeneratedFile(t *testing.T) {
	path := generateMatroska(t, true)
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	got, err := keyframes.MatroskaExtractor{}.Extract(t.Context(), source)
	if err != nil {
		t.Fatalf("MatroskaExtractor.Extract() error = %v", err)
	}
	probe, err := keyframes.NewExtractor(ffprobe, 30*time.Second, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	want, err := probe.Extract(t.Context(), source)
	if err != nil {
		t.Fatalf("ffprobe Extract() error = %v", err)
	}
	if len(got.KeyframeTicks) < 10 {
		t.Fatalf("Cues keyframes = %d, want the irregular GOP plus forced keyframes", len(got.KeyframeTicks))
	}
	if !reflect.DeepEqual(got.KeyframeTicks, want.KeyframeTicks) {
		t.Fatalf("Cues keyframes = %v\nffprobe keyframes = %v", got.KeyframeTicks, want.KeyframeTicks)
	}
	if got.TotalDurationTicks < 199_000_000 || got.TotalDurationTicks > 201_000_000 {
		t.Fatalf("Cues duration = %d ticks, want about 20 s", got.TotalDurationTicks)
	}
}

func TestMatroskaExtractorRejectsGeneratedFileWithoutCues(t *testing.T) {
	path := generateMatroska(t, false)
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if got, err := (keyframes.MatroskaExtractor{}).Extract(t.Context(), source); !errors.Is(err, keyframes.ErrMatroskaMalformed) {
		t.Fatalf("MatroskaExtractor.Extract() = %#v, %v; want ErrMatroskaMalformed", got, err)
	}
}
