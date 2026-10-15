// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
	"github.com/deadfin-org/deadfin/internal/subtitles"
)

func TestSubtitleServicePlaylistRendersGoldenAndIgnoresRenditionIndex(t *testing.T) {
	runtime := int64(1_385_240_000)
	sources := &subtitleServiceSource{source: playback.MediaSourceInfo{RunTimeTicks: &runtime}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources})
	request := subtitlePlaylistRequest(91)

	response, err := service.Playlist(t.Context(), request)
	if err != nil {
		t.Fatalf("Playlist(): %v", err)
	}
	const want = "#EXTM3U\n#EXT-X-TARGETDURATION:30\n#EXT-X-VERSION:3\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:30,\nstream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=0&EndPositionTicks=300000000&ApiKey=subtitle-token\n#EXTINF:30,\nstream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=300000000&EndPositionTicks=600000000&ApiKey=subtitle-token\n#EXTINF:30,\nstream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=600000000&EndPositionTicks=900000000&ApiKey=subtitle-token\n#EXTINF:30,\nstream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=900000000&EndPositionTicks=1200000000&ApiKey=subtitle-token\n#EXTINF:18.524,\nstream.vtt?CopyTimestamps=true&AddVttTimeMap=true&StartPositionTicks=1200000000&EndPositionTicks=1385240000&ApiKey=subtitle-token\n#EXT-X-ENDLIST\n"
	if got := string(response.Body); got != want {
		t.Fatalf("Playlist() body = %q\nwant %q", got, want)
	}
	if response.ContentType != "application/x-mpegURL" {
		t.Errorf("Playlist() ContentType = %q, want application/x-mpegURL", response.ContentType)
	}

	request.Index = -27
	if _, err := service.Playlist(t.Context(), request); err != nil {
		t.Fatalf("Playlist() with out-of-range rendition index: %v", err)
	}
	if sources.calls != 2 || sources.userID != request.UserID || sources.itemID != request.ItemID || sources.mediaSourceID != request.MediaSourceID {
		t.Errorf("StaticSource calls/facts = %d, %s, %s, %q; want two calls with exact request identity", sources.calls, sources.userID, sources.itemID, sources.mediaSourceID)
	}
}

func TestSubtitleServicePlaylistPreservesSourceErrorsAndClassifiesInvalidInput(t *testing.T) {
	request := subtitlePlaylistRequest(0)
	sourceFailure := errors.New("source unavailable")
	for _, tc := range []struct {
		name      string
		source    playback.MediaSourceInfo
		sourceErr error
		mutate    func(*playback.SubtitlePlaylistRequest)
		want      error
	}{
		{name: "source error", sourceErr: sourceFailure, want: sourceFailure},
		{name: "missing runtime", want: playback.ErrSubtitleInvalid},
		{name: "zero segment length", source: subtitleSource(1), mutate: func(r *playback.SubtitlePlaylistRequest) { r.SegmentLengthSeconds = 0 }, want: playback.ErrSubtitleInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources := &subtitleServiceSource{source: tc.source, err: tc.sourceErr}
			service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources})
			gotRequest := request
			if tc.mutate != nil {
				tc.mutate(&gotRequest)
			}
			_, err := service.Playlist(t.Context(), gotRequest)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Playlist() error = %v, want errors.Is(_, %v)", err, tc.want)
			}
		})
	}
}

func TestSubtitleServiceStreamEmbeddedBorrowsContainerDescriptorAndClosesIt(t *testing.T) {
	container := filepath.Join(t.TempDir(), "embedded.mkv")
	if err := os.WriteFile(container, []byte("container bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.Path = container
	sources.source.MediaStreams = []playback.MediaStream{
		{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
		{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac"},
		{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "srt"},
		{Index: 3, Type: playback.StreamTypeSubtitle, Codec: "srt"},
		{Index: 4, Type: playback.StreamTypeSubtitle, Codec: "srt"},
		{Index: 5, Type: playback.StreamTypeSubtitle, Codec: "srt"},
		{Index: 6, Type: playback.StreamTypeSubtitle, Codec: "srt"},
		{Index: 7, Type: playback.StreamTypeSubtitle, Codec: "srt"},
	}
	converter := &subtitleServiceConverter{output: []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nembedded\n")}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})

	response, err := service.Stream(t.Context(), subtitleStreamRequest(7, "vtt"))
	if err != nil {
		t.Fatalf("Stream(): %v", err)
	}
	if converter.calls != 1 {
		t.Fatalf("Convert calls = %d, want 1", converter.calls)
	}
	job := converter.jobs[0]
	if job.Source == nil {
		t.Fatal("ConvertJob.Source = nil, want borrowed container descriptor")
	}
	if job.Source.Name() != container || job.StreamIndex != 7 || job.OutputFormat != "webvtt" {
		t.Errorf("ConvertJob = %#v, want borrowed %q descriptor, stream 7, webvtt", job, container)
	}
	if _, err := job.Source.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("request-owned descriptor Stat() error = %v, want os.ErrClosed after Stream", err)
	}
	if response.ContentType != "text/vtt" {
		t.Errorf("Stream() ContentType = %q, want text/vtt", response.ContentType)
	}
	if got, want := string(response.Body), "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nembedded\n"; got != want {
		t.Errorf("Stream() body = %q, want %q", got, want)
	}
}

func TestSubtitleServiceStreamExternalSRTConvertsWindowsAndAddsTimeMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "captions.srt")
	if err := os.WriteFile(path, []byte("source srt"), 0o600); err != nil {
		t.Fatal(err)
	}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.MediaStreams = []playback.MediaStream{{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: path}}
	converter := &subtitleServiceConverter{output: []byte("WEBVTT\n\nold\n00:00:00.000 --> 00:00:00.999\ndrop\n\ninside\n00:00:01.250 --> 00:00:01.750\nkeep\n\nlate\n00:00:02.001 --> 00:00:03.000\ndrop\n")}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})
	request := subtitleStreamRequest(2, "vtt")
	request.StartPositionTicks, request.EndPositionTicks = 10_000_000, 20_000_000
	request.CopyTimestamps, request.AddVTTTimeMap = true, true

	response, err := service.Stream(t.Context(), request)
	if err != nil {
		t.Fatalf("Stream(): %v", err)
	}
	if len(converter.jobs) != 1 {
		t.Fatalf("Convert jobs = %d, want 1", len(converter.jobs))
	}
	job := converter.jobs[0]
	if job.Source == nil {
		t.Fatal("ConvertJob.Source = nil, want borrowed external descriptor")
	}
	if job.Source.Name() != path || job.StreamIndex != -1 || job.OutputFormat != "webvtt" {
		t.Fatalf("ConvertJob = %#v, want borrowed %q descriptor, stream -1, webvtt", job, path)
	}
	const want = "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000\n\ninside\n00:00:01.250 --> 00:00:01.750\nkeep\n"
	if got := string(response.Body); got != want {
		t.Errorf("Stream() body = %q, want exact windowed/timemapped VTT %q", got, want)
	}
	if response.ContentType != "text/vtt" {
		t.Errorf("Stream() ContentType = %q, want text/vtt", response.ContentType)
	}
}

func TestSubtitleServiceStreamExternalVTTPassthroughIsUnwindowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "captions.vtt")
	const source = "WEBVTT\n\nold\n00:00:00.000 --> 00:00:00.500\nretained despite window\n\nlate\n00:00:05.000 --> 00:00:06.000\nalso retained\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.MediaStreams = []playback.MediaStream{{Index: 3, Type: playback.StreamTypeSubtitle, Codec: "webvtt", IsExternal: true, Path: path}}
	converter := &subtitleServiceConverter{err: errors.New("passthrough must not convert")}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})
	request := subtitleStreamRequest(3, "vtt")
	request.StartPositionTicks, request.EndPositionTicks, request.AddVTTTimeMap = 10_000_000, 20_000_000, true

	response, err := service.Stream(t.Context(), request)
	if err != nil {
		t.Fatalf("Stream(): %v", err)
	}
	if converter.calls != 0 {
		t.Fatalf("Convert calls = %d, want 0 for external same-format VTT", converter.calls)
	}
	const want = "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000\n\nold\n00:00:00.000 --> 00:00:00.500\nretained despite window\n\nlate\n00:00:05.000 --> 00:00:06.000\nalso retained\n"
	if got := string(response.Body); got != want {
		t.Errorf("Stream() body = %q, want whole external VTT plus timestamp map %q", got, want)
	}
}

func TestSubtitleServiceStreamClassifiesUnknownNonTextAndUnsupportedRequests(t *testing.T) {
	for _, tc := range []struct {
		name    string
		streams []playback.MediaStream
		request playback.SubtitleStreamRequest
		want    error
	}{
		{name: "unknown index", streams: []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt"}}, request: subtitleStreamRequest(9, "vtt"), want: playback.ErrSubtitleStreamNotFound},
		{name: "PGS is not text", streams: []playback.MediaStream{{Index: 4, Type: playback.StreamTypeSubtitle, Codec: "pgs"}}, request: subtitleStreamRequest(4, "vtt"), want: playback.ErrSubtitleInvalid},
		{name: "unsupported conversion format", streams: []playback.MediaStream{{Index: 5, Type: playback.StreamTypeSubtitle, Codec: "srt"}}, request: subtitleStreamRequest(5, "json"), want: playback.ErrSubtitleInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources := &subtitleServiceSource{source: subtitleSource(0)}
			sources.source.MediaStreams = tc.streams
			service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: &subtitleServiceConverter{}})
			_, err := service.Stream(t.Context(), tc.request)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Stream() error = %v, want errors.Is(_, %v)", err, tc.want)
			}
		})
	}
}

func TestSubtitleServiceStreamUsesStockPassthroughContentTypes(t *testing.T) {
	for _, tc := range []struct {
		name, extension, codec, format, wantType string
	}{
		{name: "SubRip", extension: "srt", codec: "srt", format: "srt", wantType: "application/x-subrip"},
		{name: "SSA", extension: "ass", codec: "ass", format: "ass", wantType: "text/x-ssa"},
		{name: "unknown text", extension: "txt", codec: "txt", format: "txt", wantType: "application/octet-stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "captions."+tc.extension)
			if err := os.WriteFile(path, []byte("same format"), 0o600); err != nil {
				t.Fatal(err)
			}
			sources := &subtitleServiceSource{source: subtitleSource(0)}
			sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: tc.codec, IsExternal: true, Path: path}}
			service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: &subtitleServiceConverter{err: errors.New("must not convert")}})
			response, err := service.Stream(t.Context(), subtitleStreamRequest(1, tc.format))
			if err != nil {
				t.Fatalf("Stream(): %v", err)
			}
			if response.ContentType != tc.wantType {
				t.Errorf("Stream() ContentType = %q, want %q", response.ContentType, tc.wantType)
			}
		})
	}
}

// TestSubtitleServiceStreamNormalizesSubripAliasAsSRT proves that the subrip
// alias from a live Swiftfin Stream.subrip request is normalized to srt so the
// conversion pipeline produces SRT bytes. The response keeps the raw alias for
// the content type, yielding application/octet-stream (the stock default).
func TestSubtitleServiceStreamNormalizesSubripAliasAsSRT(t *testing.T) {
	container := filepath.Join(t.TempDir(), "embedded.mkv")
	if err := os.WriteFile(container, []byte("container bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.Path = container
	sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "subrip"}}
	converter := &subtitleServiceConverter{output: []byte("1\n00:00:00,000 --> 00:00:01,000\ncaption\n")}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})

	response, err := service.Stream(t.Context(), subtitleStreamRequest(1, "subrip"))
	if err != nil {
		t.Fatalf("Stream(): %v", err)
	}
	if converter.calls != 1 {
		t.Fatalf("Convert calls = %d, want 1", converter.calls)
	}
	job := converter.jobs[0]
	if job.OutputFormat != "srt" {
		t.Errorf("ConvertJob OutputFormat = %q, want srt", job.OutputFormat)
	}
	if response.ContentType != "application/octet-stream" {
		t.Errorf("Stream() ContentType = %q, want application/octet-stream", response.ContentType)
	}
	if got, want := string(response.Body), "1\n00:00:00,000 --> 00:00:01,000\ncaption\n"; got != want {
		t.Errorf("Stream() body = %q, want SRT bytes %q", got, want)
	}
}

// TestSubtitleServiceStreamNormalizesSubripAliasForExternalSRTConversion proves
// that a subrip request for an external .srt sidecar does not fall through to
// passthrough (the codec is srt, not subrip) and instead converts to SRT bytes.
// The response keeps the raw subrip alias for the content type.
func TestSubtitleServiceStreamNormalizesSubripAliasForExternalSRTConversion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "captions.srt")
	if err := os.WriteFile(path, []byte("source srt"), 0o600); err != nil {
		t.Fatal(err)
	}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.MediaStreams = []playback.MediaStream{{Index: 0, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: path}}
	converter := &subtitleServiceConverter{output: []byte("1\n00:00:00,000 --> 00:00:02,000\ncaption\n")}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})

	response, err := service.Stream(t.Context(), subtitleStreamRequest(0, "subrip"))
	if err != nil {
		t.Fatalf("Stream(): %v", err)
	}
	if converter.calls != 1 {
		t.Fatalf("Convert calls = %d, want 1", converter.calls)
	}
	job := converter.jobs[0]
	if job.OutputFormat != "srt" {
		t.Errorf("ConvertJob OutputFormat = %q, want srt", job.OutputFormat)
	}
	if response.ContentType != "application/octet-stream" {
		t.Errorf("Stream() ContentType = %q, want application/octet-stream", response.ContentType)
	}
	if got, want := string(response.Body), "1\n00:00:00,000 --> 00:00:02,000\ncaption\n"; got != want {
		t.Errorf("Stream() body = %q, want SRT bytes %q", got, want)
	}
}

// Same-format external subtitle delivery is still bounded: a caller must not
// be able to force the service to materialize an unbounded file just because
// ffmpeg conversion is bypassed.
func TestSubtitleServiceStreamRejectsOversizeSameFormatPassthrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversize.vtt")
	const maxPassthroughBytes = 8 << 20
	if err := os.WriteFile(path, []byte("WEBVTT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxPassthroughBytes+1); err != nil {
		t.Fatal(err)
	}

	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "webvtt", IsExternal: true, Path: path}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: &subtitleServiceConverter{err: errors.New("same-format passthrough must not convert")}})

	_, err := service.Stream(t.Context(), subtitleStreamRequest(1, "vtt"))
	if !errors.Is(err, subtitles.ErrOutputTooLarge) {
		t.Fatalf("Stream() error = %v, want ErrOutputTooLarge for a same-format file over 8 MiB", err)
	}
}

// Replacing an external subtitle atomically can retain both its byte count and
// modification time. Cached conversion output must nevertheless be tied to the
// replaced file rather than reused for the new source.
func TestSubtitleServiceStreamInvalidatesCacheAfterSameSizeMtimePreservingReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "captions.srt")
	const original = "first-cue-001"
	const replacement = "newer-cue-001"
	if len(original) != len(replacement) {
		t.Fatal("test fixture must preserve byte length")
	}
	modified := time.Date(2026, time.July, 28, 12, 0, 0, 123_456_000, time.UTC)
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}

	converter := &subtitleServiceConverter{output: []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nconverted\n")}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: path}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})
	request := subtitleStreamRequest(1, "vtt")

	if _, err := service.Stream(t.Context(), request); err != nil {
		t.Fatalf("first Stream(): %v", err)
	}
	replacementPath := filepath.Join(t.TempDir(), "replacement.srt")
	if err := os.WriteFile(replacementPath, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacementPath, modified, modified); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacementPath, path); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Stream(t.Context(), request); err != nil {
		t.Fatalf("Stream() after replacement: %v", err)
	}
	if converter.calls != 2 {
		t.Fatalf("Convert calls after same-size, same-mtime replacement = %d, want cache miss and 2", converter.calls)
	}
}

// Two sidecar names that are hard links to the same regular file identify the
// same pinned bytes. The descriptor cache must share their conversion output.
func TestSubtitleServiceStreamSharesCacheAcrossHardlinkedSidecars(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.srt")
	second := filepath.Join(dir, "second.srt")
	if err := os.WriteFile(first, []byte("hardlinked subtitle"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, second); err != nil {
		t.Fatal(err)
	}

	converter := &subtitleServiceConverter{output: []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nhardlink\n")}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: first}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})
	request := subtitleStreamRequest(1, "vtt")

	if _, err := service.Stream(t.Context(), request); err != nil {
		t.Fatalf("first hardlink Stream(): %v", err)
	}
	sources.source.MediaStreams[0].Path = second
	if _, err := service.Stream(t.Context(), request); err != nil {
		t.Fatalf("second hardlink Stream(): %v", err)
	}
	if converter.calls != 1 {
		t.Fatalf("Convert calls for two hard links = %d, want shared descriptor identity cache entry", converter.calls)
	}
}

// An external subtitle replacement after source selection must reject the
// request and must not publish the old conversion under the replacement's
// cache identity. The converter callback marks the precise boundary at which
// ffmpeg would begin consuming the selected source.
func TestSubtitleServiceStreamRejectsReplacementDuringExternalConversionWithoutCachingOldResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "captions.srt")
	const original = "first-cue-001"
	const replacement = "newer-cue-001"
	modified := time.Date(2026, time.July, 28, 12, 0, 0, 123_456_000, time.UTC)
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	replacementPath := filepath.Join(t.TempDir(), "replacement.srt")
	if err := os.WriteFile(replacementPath, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacementPath, modified, modified); err != nil {
		t.Fatal(err)
	}

	converter := &subtitleServiceConverter{
		outputs: [][]byte{
			[]byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nold conversion\n"),
			[]byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nreplacement conversion\n"),
		},
		beforeConvert: func(call int, _ subtitles.ConvertJob) {
			if call != 1 {
				return
			}
			if err := os.Rename(replacementPath, path); err != nil {
				t.Fatalf("replace selected subtitle: %v", err)
			}
		},
	}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: path}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})
	request := subtitleStreamRequest(1, "vtt")

	if _, err := service.Stream(t.Context(), request); !errors.Is(err, sourcefile.ErrStaleSource) {
		t.Fatalf("Stream() after replacement at conversion start error = %v, want ErrStaleSource", err)
	}
	response, err := service.Stream(t.Context(), request)
	if err != nil {
		t.Fatalf("Stream() after rejected replacement: %v", err)
	}
	if converter.calls != 2 {
		t.Fatalf("Convert calls after rejected replacement = %d, want 2 so stale output is not cached", converter.calls)
	}
	const want = "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nreplacement conversion\n"
	if got := string(response.Body); got != want {
		t.Fatalf("Stream() after replacement body = %q, want fresh conversion %q", got, want)
	}
}

// Retargeting a selected symlink is the same stale-source boundary as a
// pathname replacement. The old target cannot be served or retained under the
// symlink's cache key once the link points to a replacement target.
func TestSubtitleServiceStreamRejectsSymlinkRetargetDuringExternalConversion(t *testing.T) {
	dir := t.TempDir()
	firstTarget := filepath.Join(dir, "first.srt")
	secondTarget := filepath.Join(dir, "second.srt")
	const original = "first-cue-001"
	const replacement = "newer-cue-001"
	modified := time.Date(2026, time.July, 28, 12, 0, 0, 123_456_000, time.UTC)
	for path, contents := range map[string]string{firstTarget: original, secondTarget: replacement} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "captions.srt")
	if err := os.Symlink(firstTarget, path); err != nil {
		t.Fatal(err)
	}
	nextLink := filepath.Join(dir, "next.srt")
	if err := os.Symlink(secondTarget, nextLink); err != nil {
		t.Fatal(err)
	}

	converter := &subtitleServiceConverter{beforeConvert: func(call int, _ subtitles.ConvertJob) {
		if call != 1 {
			return
		}
		if err := os.Rename(nextLink, path); err != nil {
			t.Fatalf("retarget selected subtitle link: %v", err)
		}
	}}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: path}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})

	if _, err := service.Stream(t.Context(), subtitleStreamRequest(1, "vtt")); !errors.Is(err, sourcefile.ErrStaleSource) {
		t.Fatalf("Stream() after symlink retarget at conversion start error = %v, want ErrStaleSource", err)
	}
}

func TestSubtitleServiceStreamCachesByStatFactsAndEvictsToBudget(t *testing.T) {
	pathA := filepath.Join(t.TempDir(), "a.srt")
	pathB := filepath.Join(t.TempDir(), "b.srt")
	for _, path := range []string{pathA, pathB} {
		if err := os.WriteFile(path, []byte("subtitle input"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	converter := &subtitleServiceConverter{output: []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nthis output is intentionally cache sized\n")}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: pathA}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter, CacheBudget: 100})
	request := subtitleStreamRequest(1, "vtt")

	if _, err := service.Stream(t.Context(), request); err != nil {
		t.Fatalf("first Stream(): %v", err)
	}
	if _, err := service.Stream(t.Context(), request); err != nil {
		t.Fatalf("cached Stream(): %v", err)
	}
	if converter.calls != 1 {
		t.Fatalf("Convert calls after reuse = %d, want 1", converter.calls)
	}

	changed := clock.NewFake(time.Date(2026, time.July, 28, 0, 0, 0, 0, time.UTC)).Now().Add(2 * time.Second)
	if err := os.Chtimes(pathA, changed, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Stream(t.Context(), request); err != nil {
		t.Fatalf("mtime-invalidated Stream(): %v", err)
	}
	if converter.calls != 2 {
		t.Fatalf("Convert calls after mtime change = %d, want 2", converter.calls)
	}

	sources.source.MediaStreams[0].Path = pathB
	if _, err := service.Stream(t.Context(), request); err != nil {
		t.Fatalf("budget-filling Stream(): %v", err)
	}
	sources.source.MediaStreams[0].Path = pathA
	if _, err := service.Stream(t.Context(), request); err != nil {
		t.Fatalf("evicted Stream(): %v", err)
	}
	if converter.calls != 4 {
		t.Errorf("Convert calls after budget eviction = %d, want 4", converter.calls)
	}
}

// WEBVTT's timestamp map is a global literal replacement, including literals
// in non-cue payload. The request response cap therefore applies after the
// expansion, rather than only to converter stdout.
func TestSubtitleServiceStreamRejectsPostTransformConvertedVTTOverProductionCapWithoutCaching(t *testing.T) {
	path := filepath.Join(t.TempDir(), "captions.srt")
	if err := os.WriteFile(path, []byte("source srt"), 0o600); err != nil {
		t.Fatal(err)
	}

	overLimit := subtitleVTTWithMappedSize(t, subtitleResponseLimit+1)
	const safeOutput = "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nsecond conversion\n"
	converter := &subtitleServiceConverter{outputs: [][]byte{overLimit, []byte(safeOutput)}}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: path}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})
	request := subtitleStreamRequest(1, "vtt")
	request.AddVTTTimeMap = true

	if _, err := service.Stream(t.Context(), request); !errors.Is(err, subtitles.ErrOutputTooLarge) {
		t.Fatalf("first Stream() error = %v, want ErrOutputTooLarge after VTT map expansion", err)
	}
	response, err := service.Stream(t.Context(), request)
	if err != nil {
		t.Fatalf("second Stream(): %v", err)
	}
	const want = "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000\n\n00:00:01.000 --> 00:00:02.000\nsecond conversion\n"
	if got := string(response.Body); got != want {
		t.Fatalf("second Stream() body = %q, want fresh mapped conversion %q", got, want)
	}
	if _, err := service.Stream(t.Context(), request); err != nil {
		t.Fatalf("cached Stream(): %v", err)
	}
	if converter.calls != 2 {
		t.Fatalf("Convert calls after rejected overflow and cached safe retry = %d, want 2", converter.calls)
	}
}

func TestSubtitleServiceStreamAcceptsConvertedVTTAtAndBelowPostTransformCap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mapped int
	}{
		{name: "exact cap", mapped: subtitleResponseLimit},
		{name: "one byte below cap", mapped: subtitleResponseLimit - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "captions.srt")
			if err := os.WriteFile(path, []byte("source srt"), 0o600); err != nil {
				t.Fatal(err)
			}
			converter := &subtitleServiceConverter{output: subtitleVTTWithMappedSize(t, tc.mapped)}
			sources := &subtitleServiceSource{source: subtitleSource(0)}
			sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: path}}
			service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})
			request := subtitleStreamRequest(1, "vtt")
			request.AddVTTTimeMap = true

			response, err := service.Stream(t.Context(), request)
			if err != nil {
				t.Fatalf("Stream(): %v", err)
			}
			if len(response.Body) != tc.mapped {
				t.Fatalf("Stream() mapped body length = %d, want %d", len(response.Body), tc.mapped)
			}
			if converter.calls != 1 {
				t.Fatalf("Convert calls = %d, want 1", converter.calls)
			}
		})
	}
}

func TestSubtitleServiceStreamCapsPassthroughVTTAfterRepeatedTimestampMapExpansion(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mapped    int
		wantError error
	}{
		{name: "expanded over cap", mapped: subtitleResponseLimit + 1, wantError: subtitles.ErrOutputTooLarge},
		{name: "exact cap", mapped: subtitleResponseLimit},
		{name: "one byte below cap", mapped: subtitleResponseLimit - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "captions.vtt")
			if err := os.WriteFile(path, subtitleVTTWithMappedSize(t, tc.mapped), 0o600); err != nil {
				t.Fatal(err)
			}
			converter := &subtitleServiceConverter{err: errors.New("same-format VTT must not convert")}
			sources := &subtitleServiceSource{source: subtitleSource(0)}
			sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "webvtt", IsExternal: true, Path: path}}
			service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})
			request := subtitleStreamRequest(1, "vtt")
			request.AddVTTTimeMap = true

			response, err := service.Stream(t.Context(), request)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("Stream() error = %v, want errors.Is(_, %v)", err, tc.wantError)
			}
			if tc.wantError == nil && len(response.Body) != tc.mapped {
				t.Fatalf("Stream() mapped body length = %d, want %d", len(response.Body), tc.mapped)
			}
			if converter.calls != 0 {
				t.Fatalf("Convert calls = %d, want 0 for same-format VTT passthrough", converter.calls)
			}
		})
	}
}

func TestSubtitleServiceStreamRejectsInvalidWindowsBeforeSourceReadOrConversion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end int64
	}{
		{name: "minimum start", start: math.MinInt64, end: 0},
		{name: "negative start", start: -1, end: 0},
		{name: "negative end", start: 0, end: -1},
		{name: "end before start", start: 2, end: 1},
		{name: "minimum to maximum range", start: math.MinInt64, end: math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			converter := &subtitleServiceConverter{err: errors.New("invalid window must not convert")}
			sources := &subtitleServiceSource{source: subtitleSource(0)}
			sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: filepath.Join(t.TempDir(), "must-not-read.srt")}}
			service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})
			request := subtitleStreamRequest(1, "vtt")
			request.StartPositionTicks, request.EndPositionTicks = tc.start, tc.end

			if _, err := service.Stream(t.Context(), request); !errors.Is(err, playback.ErrSubtitleInvalid) {
				t.Fatalf("Stream() error = %v, want ErrSubtitleInvalid", err)
			}
			if sources.calls != 0 {
				t.Fatalf("StaticSource calls = %d, want 0 before an invalid window can read its source", sources.calls)
			}
			if converter.calls != 0 {
				t.Fatalf("Convert calls = %d, want 0 for an invalid window", converter.calls)
			}
		})
	}
}

func TestSubtitleServiceStreamPreservesSafeMaxEndWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "captions.srt")
	if err := os.WriteFile(path, []byte("source srt"), 0o600); err != nil {
		t.Fatal(err)
	}
	const converted = "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\ninside maximum end\n"
	converter := &subtitleServiceConverter{output: []byte(converted)}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.MediaStreams = []playback.MediaStream{{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, Path: path}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources, Converter: converter})
	request := subtitleStreamRequest(1, "vtt")
	request.EndPositionTicks = math.MaxInt64
	request.CopyTimestamps = true

	response, err := service.Stream(t.Context(), request)
	if err != nil {
		t.Fatalf("Stream() with maximum valid end: %v", err)
	}
	if got := string(response.Body); got != converted {
		t.Fatalf("Stream() body = %q, want unchanged safe maximum-end window %q", got, converted)
	}
}

const subtitleResponseLimit = 8 << 20

func subtitleVTTWithMappedSize(t *testing.T, mappedSize int) []byte {
	t.Helper()
	const mapReplacement = "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000"
	const occurrences = 3
	if mappedSize < occurrences*len(mapReplacement) {
		t.Fatalf("mapped size %d cannot contain %d timestamp-map replacements", mappedSize, occurrences)
	}
	return []byte(strings.Repeat("WEBVTT", occurrences) + strings.Repeat("x", mappedSize-occurrences*len(mapReplacement)))
}

func subtitlePlaylistRequest(index int) playback.SubtitlePlaylistRequest {
	return playback.SubtitlePlaylistRequest{
		UserID:               subtitleServiceUUID("00000000-0000-0000-0000-000000000801"),
		ItemID:               subtitleServiceUUID("00000000-0000-0000-0000-000000000802"),
		MediaSourceID:        "subtitle-source",
		Index:                index,
		SegmentLengthSeconds: 30,
		APIKey:               "subtitle-token",
	}
}

func subtitleStreamRequest(index int, format string) playback.SubtitleStreamRequest {
	return playback.SubtitleStreamRequest{
		UserID:        subtitleServiceUUID("00000000-0000-0000-0000-000000000801"),
		ItemID:        subtitleServiceUUID("00000000-0000-0000-0000-000000000802"),
		MediaSourceID: "subtitle-source",
		Index:         index,
		Format:        format,
	}
}

func subtitleSource(runtime int64) playback.MediaSourceInfo {
	return playback.MediaSourceInfo{Path: "/media/source.mkv", RunTimeTicks: &runtime}
}

func subtitleServiceUUID(raw string) dto.UUID {
	value, err := dto.ParseUUID(raw)
	if err != nil {
		panic(err)
	}
	return value
}

type subtitleServiceSource struct {
	source         playback.MediaSourceInfo
	err            error
	calls          int
	ctx            context.Context
	userID, itemID dto.UUID
	mediaSourceID  string
}

func (source *subtitleServiceSource) StaticSource(ctx context.Context, userID, itemID dto.UUID, mediaSourceID string) (playback.MediaSourceInfo, error) {
	source.calls++
	source.ctx, source.userID, source.itemID, source.mediaSourceID = ctx, userID, itemID, mediaSourceID
	if source.err != nil {
		return playback.MediaSourceInfo{}, source.err
	}
	return source.source, nil
}

type subtitleServiceConverter struct {
	output        []byte
	outputs       [][]byte
	err           error
	calls         int
	jobs          []subtitles.ConvertJob
	beforeConvert func(call int, job subtitles.ConvertJob)
}

func (converter *subtitleServiceConverter) Convert(_ context.Context, job subtitles.ConvertJob) ([]byte, error) {
	converter.calls++
	converter.jobs = append(converter.jobs, job)
	if converter.beforeConvert != nil {
		converter.beforeConvert(converter.calls, job)
	}
	if converter.err != nil {
		return nil, converter.err
	}
	if index := converter.calls - 1; index < len(converter.outputs) {
		return converter.outputs[index], nil
	}
	return converter.output, nil
}
