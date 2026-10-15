// SPDX-License-Identifier: GPL-2.0-only

package encoding

import (
	"bufio"
	"bytes"
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHLSSegmentsMatchSegmentLengthWithRealFFmpeg runs the HLS argv through
// real ffmpeg for sources whose output frame rate differs from the source
// rate (D-62). The hardware GOP encoders cannot run here, so
// the "gop" variant runs libx264 with the -g:v:0/-keyint_min:v:0 pair that
// the h264_qsv argv carries in place of -force_key_frames. Each segment
// that ffmpeg cuts must last one segment length, as the main.m3u8 EXTINF
// values state.
func TestHLSSegmentsMatchSegmentLengthWithRealFFmpeg(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("real HLS segment test requires ffmpeg: %v", err)
	}
	encoders, err := exec.CommandContext(t.Context(), ffmpegPath, "-hide_banner", "-encoders").Output()
	if err != nil || !bytes.Contains(encoders, []byte(" libx264 ")) {
		t.Skipf("real HLS segment test requires ffmpeg with libx264: %v", err)
	}

	const segmentLength = 6
	for _, test := range []struct {
		name                                  string
		sourceRate                            string
		realFrameRate, referenceRate, outRate float64
	}{
		{"47.952 capped to 23.976", "48000/1001", 47.952, 47.952, 23.976025},
		{"29.97 capped to 23.976", "30000/1001", 29.97003, 29.97003, 23.976025},
		{"59.94 capped to 30", "60000/1001", 59.94006, 59.94006, 30},
		{"23.976 uncapped", "24000/1001", 23.976025, 23.976025, 0},
		// ATV-V22: a TS VC-1 source probes RealFrameRate 47.952 for 23.976
		// frames. MaxFramerate 23.976025 sets no -r.
		{"23.976 probed as 47.952 uncapped", "24000/1001", 47.952, 23.976025, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.mkv")
			runFFmpeg(t, ffmpegPath,
				"-f", "lavfi", "-i", "testsrc2=size=320x180:rate="+test.sourceRate,
				"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
				"-t", "32", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-y", source)

			software, _ := gopJob("libx264", test.realFrameRate, test.outRate)
			software.VideoStream.ReferenceFrameRate = test.referenceRate
			software.InputPath = source
			software.OutputWidth, software.OutputHeight = 320, 180
			qsv, qsvCaps := gopJob("h264_qsv", test.realFrameRate, test.outRate)
			qsv.VideoStream.ReferenceFrameRate = test.referenceRate
			qsvArgs, err := BuildHLSArgs(qsv, qsvCaps)
			if err != nil {
				t.Fatal(err)
			}
			gop, ok := argValue(qsvArgs, "-g:v:0")
			if !ok {
				t.Fatalf("h264_qsv argv has no GOP: %q", qsvArgs)
			}

			variants := map[string]string{"forced key frames": "", "gop": gop}
			encoderRate := test.outRate
			if encoderRate <= 0 {
				encoderRate = test.referenceRate
			}
			if test.realFrameRate > encoderRate {
				// The upstream GOP, counted at the source rate. It must fail
				// the check, so the check can see the defect.
				variants["source-rate gop"] = strconv.Itoa(int(math.Ceil(segmentLength * test.realFrameRate)))
			}
			for variant, gopFrames := range variants {
				t.Run(variant, func(t *testing.T) {
					out := filepath.Join(t.TempDir(), "abc")
					software.OutputPath = out + ".m3u8"
					args, err := BuildHLSArgs(software, jellyfinFFmpegCapabilities())
					if err != nil {
						t.Fatal(err)
					}
					args = slices.Clone(args)
					for i, arg := range args {
						if arg == "/cache/transcodes/abc%d.ts" {
							args[i] = out + "%d.ts"
						}
					}
					if gopFrames != "" {
						i := slices.Index(args, "-force_key_frames:0")
						args = slices.Replace(args, i, i+2, "-g:v:0", gopFrames, "-keyint_min:v:0", gopFrames)
					}
					runFFmpeg(t, ffmpegPath, args...)

					durations := segmentDurations(t, out+".m3u8")
					if len(durations) < 2 || (variant != "source-rate gop" && len(durations) < 5) {
						t.Fatalf("got %d segments: %v", len(durations), durations)
					}
					// One output frame of slack per segment; the last segment
					// holds the remainder.
					tolerance := 1/24.0 + 0.01
					var drift bool
					for i, d := range durations[:len(durations)-1] {
						if math.Abs(d-segmentLength) > tolerance {
							drift = true
							if variant != "source-rate gop" {
								t.Errorf("segment %d lasts %.3f s, want %d s: %v", i, d, segmentLength, durations)
							}
						}
					}
					if variant == "source-rate gop" && !drift {
						t.Errorf("source-rate GOP %s made no long segment: %v", gopFrames, durations)
					}
				})
			}
		})
	}
}

func runFFmpeg(t *testing.T, ffmpegPath string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegPath, append([]string{"-nostdin", "-hide_banner", "-loglevel", "error"}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %q: %v\n%s", args, err, output)
	}
}

// segmentDurations reads the EXTINF values that ffmpeg writes for the
// segments it actually cut.
func segmentDurations(t *testing.T, playlist string) []float64 {
	t.Helper()
	file, err := os.Open(playlist)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var durations []float64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		value, ok := strings.CutPrefix(scanner.Text(), "#EXTINF:")
		if !ok {
			continue
		}
		duration, err := strconv.ParseFloat(strings.TrimSuffix(value, ","), 64)
		if err != nil {
			t.Fatalf("parse %q: %v", scanner.Text(), err)
		}
		durations = append(durations, duration)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return durations
}
