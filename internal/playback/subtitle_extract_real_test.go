// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/playback"
)

const realASS = `[Script Info]
ScriptType: v4.00+
PlayResX: 640
PlayResY: 360

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Sign,Komika,24,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,8,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:00.50,0:00:01.50,Sign,,0,0,0,,{\pos(320,50)\fnKomika}Sign text
`

// TestRealFFmpegExtractsASSAndFontFromMKV builds an MKV with an ASS track and
// a font attachment whose container starts at 10 s, then serves both through
// the real ffmpeg extractor. An external sidecar comes first in MediaStreams,
// so the ASS stream (MediaStream index 3) is ffmpeg input stream 2.
func TestRealFFmpegExtractsASSAndFontFromMKV(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	assPath := filepath.Join(dir, "track.ass")
	fontPath := filepath.Join(dir, "Komika.ttf")
	media := filepath.Join(dir, "Show S01E01.mkv")
	sidecar := filepath.Join(dir, "Show S01E01.en.srt")
	font := []byte("\x00\x01\x00\x00fake font table")
	for path, body := range map[string][]byte{assPath: []byte(realASS), fontPath: font, sidecar: []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n")} {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, ffmpegPath, "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=2",
		"-f", "lavfi", "-t", "2", "-i", "anullsrc=r=48000:cl=mono",
		"-i", assPath,
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "mpeg4", "-c:a", "flac", "-c:s", "copy",
		"-attach", fontPath, "-metadata:s:t", "mimetype=application/x-truetype-font",
		"-output_ts_offset", "10", media)
	if output, err := build.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot build the MKV fixture: %v: %s", err, output)
	}

	source := playback.MediaSourceInfo{
		ID:   extractSourceID,
		Path: media,
		MediaStreams: []playback.MediaStream{
			{Index: 0, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, SupportsExternalStream: true, Path: sidecar},
			{Index: 1, Type: playback.StreamTypeVideo, Codec: "mpeg4"},
			{Index: 2, Type: playback.StreamTypeAudio, Codec: "flac"},
			{Index: 3, Type: playback.StreamTypeSubtitle, Codec: "ass", SupportsExternalStream: true},
		},
		MediaAttachments: []playback.MediaAttachment{{Index: 3, Codec: "ttf", FileName: "Komika.ttf", MimeType: "application/x-truetype-font"}},
	}
	sources := &subtitleServiceSource{source: source}
	extractor := ffmpeg.Extractor{Path: ffmpegPath}
	data := t.TempDir()

	subtitles := playback.NewSubtitleService(playback.SubtitleServiceConfig{
		Sources: sources, Extractor: extractor, ExtractionPath: filepath.Join(data, "subtitles"),
	})
	response, err := subtitles.Stream(ctx, subtitleStreamRequest(3, "ass"))
	if err != nil {
		t.Fatalf("Stream(ass): %v", err)
	}
	if response.ContentType != "text/x-ssa" {
		t.Errorf("ContentType = %q, want text/x-ssa", response.ContentType)
	}
	body := readSubtitleResponse(t, response)
	if !strings.HasPrefix(body, "[Script Info]") {
		t.Fatalf("extracted ASS does not start with [Script Info]: %q", body)
	}
	// Override tags survive, and the 10 s container start is rebased to the
	// zero-based timeline of the served video.
	if !strings.Contains(body, `Dialogue: 0,0:00:00.50,0:00:01.50,Sign,,0,0,0,,{\pos(320,50)\fnKomika}Sign text`) {
		t.Fatalf("extracted ASS lost the dialogue or its timing: %q", body)
	}
	if !strings.Contains(body, "[V4+ Styles]") || !strings.Contains(body, "Style: Sign,Komika,24,") {
		t.Fatalf("extracted ASS lost its styles: %q", body)
	}

	attachments := playback.NewAttachmentService(playback.AttachmentServiceConfig{
		Sources: sources, Extractor: extractor, Path: filepath.Join(data, "attachments"),
	})
	attachment, err := attachments.Attachment(ctx, playback.AttachmentRequest{MediaSourceID: extractSourceID, Index: 3})
	if err != nil {
		t.Fatalf("Attachment(3): %v", err)
	}
	defer func() { _ = attachment.File.Close() }()
	got, err := io.ReadAll(attachment.File)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(font) || attachment.ContentType != "application/x-truetype-font" {
		t.Fatalf("Attachment(3) = %q %q, want the font bytes", got, attachment.ContentType)
	}
}

// TestRealFFmpegExtractAllKeepsFontsBesideActiveAttachments builds an MKV
// with a font, a text/html and an image/svg+xml attachment. The burn-in pass
// (ExtractAll) still writes the font into the fontsdir unchanged; the D-64
// response type mapping applies only to the HTTP route.
func TestRealFFmpegExtractAllKeepsFontsBesideActiveAttachments(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	font := []byte("\x00\x01\x00\x00fake font table")
	files := map[string][]byte{
		filepath.Join(dir, "Komika.ttf"): font,
		filepath.Join(dir, "page.html"):  []byte("<script>alert(1)</script>"),
		filepath.Join(dir, "image.svg"):  []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
	}
	for path, body := range files {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	media := filepath.Join(dir, "Movie.mkv")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, ffmpegPath, "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=1", "-c:v", "mpeg4",
		"-attach", filepath.Join(dir, "Komika.ttf"), "-metadata:s:t:0", "mimetype=application/x-truetype-font",
		"-attach", filepath.Join(dir, "page.html"), "-metadata:s:t:1", "mimetype=text/html",
		"-attach", filepath.Join(dir, "image.svg"), "-metadata:s:t:2", "mimetype=image/svg+xml",
		media)
	if output, err := build.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot build the MKV fixture: %v: %s", err, output)
	}
	source := playback.MediaSourceInfo{
		ID:           extractSourceID,
		Path:         media,
		MediaStreams: []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "mpeg4"}},
		MediaAttachments: []playback.MediaAttachment{
			{Index: 1, Codec: "ttf", FileName: "Komika.ttf", MimeType: "application/x-truetype-font"},
			{Index: 2, FileName: "page.html", MimeType: "text/html"},
			{Index: 3, FileName: "image.svg", MimeType: "image/svg+xml"},
		},
	}
	attachments := playback.NewAttachmentService(playback.AttachmentServiceConfig{
		Sources: &subtitleServiceSource{source: source}, Extractor: ffmpeg.Extractor{Path: ffmpegPath}, Path: t.TempDir(),
	})
	fontsdir, err := attachments.ExtractAll(ctx, source)
	if err != nil {
		t.Fatalf("ExtractAll(): %v", err)
	}
	got, err := os.ReadFile(filepath.Join(fontsdir, "Komika.ttf"))
	if err != nil || string(got) != string(font) {
		t.Fatalf("fontsdir Komika.ttf = %q, %v; want the font bytes", got, err)
	}
}
