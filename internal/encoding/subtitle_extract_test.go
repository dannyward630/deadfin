// SPDX-License-Identifier: GPL-2.0-only

package encoding

import (
	"reflect"
	"testing"
)

// Cases port SubtitleEncoder.GetExtractableSubtitleFormat and
// GetExtractableSubtitleFileExtension (MediaBrowser.MediaEncoding/Subtitles/SubtitleEncoder.cs).
func TestExtractableSubtitleFormatAndExtension(t *testing.T) {
	for _, tc := range []struct {
		codec, format, extension string
	}{
		{"ass", "ass", "ass"},
		{"ssa", "ssa", "ssa"},
		{"PGSSUB", "PGSSUB", "sup"},
		{"pgssub", "pgssub", "sup"},
		{"DVDSUB", "mks", "mks"},
		{"subrip", "srt", "srt"},
		{"mov_text", "srt", "srt"},
		{"webvtt", "srt", "srt"},
	} {
		if got := ExtractableSubtitleFormat(tc.codec); got != tc.format {
			t.Errorf("ExtractableSubtitleFormat(%q) = %q, want %q", tc.codec, got, tc.format)
		}
		if got := ExtractableSubtitleExtension(tc.codec); got != tc.extension {
			t.Errorf("ExtractableSubtitleExtension(%q) = %q, want %q", tc.codec, got, tc.extension)
		}
	}
}

func TestSubtitleExtractionArgsCopyAndConvert(t *testing.T) {
	got := SubtitleExtractionArgs("/dev/fd/3", []SubtitleExtraction{
		{InputIndex: 2, Codec: "ass", OutputPath: "/data/subtitles/ab/id/2.ass"},
		{InputIndex: 3, Codec: "PGSSUB", OutputPath: "/data/subtitles/ab/id/3.sup"},
		{InputIndex: 4, Codec: "mov_text", OutputPath: "/data/subtitles/ab/id/4.srt"},
		{InputIndex: 5, Codec: "DVDSUB", OutputPath: "/data/subtitles/ab/id/5.mks"},
	})
	want := []string{
		"-nostdin", "-y", "-i", "/dev/fd/3",
		"-map", "0:2", "-an", "-vn", "-c:s", "copy", "-flush_packets", "1", "/data/subtitles/ab/id/2.ass",
		"-map", "0:3", "-an", "-vn", "-c:s", "copy", "-flush_packets", "1", "/data/subtitles/ab/id/3.sup",
		"-map", "0:4", "-an", "-vn", "-c:s", "srt", "-flush_packets", "1", "/data/subtitles/ab/id/4.srt",
		"-map", "0:5", "-an", "-vn", "-c:s", "copy", "-f", "matroska", "-flush_packets", "1", "/data/subtitles/ab/id/5.mks",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SubtitleExtractionArgs() =\n%q\nwant\n%q", got, want)
	}
	for _, arg := range got {
		if arg == "-copyts" {
			t.Fatal("extraction must rebase timestamps; -copyts present")
		}
	}
}

func TestAttachmentDumpArgs(t *testing.T) {
	got := AttachmentDumpArgs("/dev/fd/3", 7, "/data/attachments/ab/id/font.ttf.part", true)
	want := []string{"-nostdin", "-dump_attachment:7", "/data/attachments/ab/id/font.ttf.part", "-i", "/dev/fd/3", "-t", "0", "-f", "null", "null"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AttachmentDumpArgs(with A/V) = %q, want %q", got, want)
	}
	got = AttachmentDumpArgs("/dev/fd/3", 1, "/out", false)
	want = []string{"-nostdin", "-dump_attachment:1", "/out", "-i", "/dev/fd/3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AttachmentDumpArgs(no A/V) = %q, want %q", got, want)
	}
}
