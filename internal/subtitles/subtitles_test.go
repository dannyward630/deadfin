// SPDX-License-Identifier: GPL-2.0-only

package subtitles_test

import (
	"bytes"
	"testing"

	"github.com/deadfin-org/deadfin/internal/subtitles"
)

const ticksPerSecond = int64(10_000_000)

func TestNormalizeFormatMatchesJellyfinParserExtensions(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		input string
		want  string
	}{
		{input: "SRT", want: "srt"},
		{input: "subrip", want: "srt"},
		{input: "WebVTT", want: "vtt"},
		{input: "SSA", want: "ssa"},
		{input: ".VTT", want: ".vtt"},
		{input: "", want: ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			if got := subtitles.NormalizeFormat(tc.input); got != tc.want {
				t.Fatalf("NormalizeFormat(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestWindowVTTStrictBoundariesAndTimestampPreservation(t *testing.T) {
	t.Parallel()

	const source = "WEBVTT\n\nNOTE retained verbatim\nmetadata --> is not a cue\n\nSTYLE\n::cue { color: lime; }\n\nfully elapsed\n00:00:00.000 --> 00:00:00.999 align:start\ndrop\n\nends at start\n00:00:00.000 --> 00:00:01.000 line:90%\nkeep-ending-boundary\n\ninside\n00:00:01.250 --> 00:00:01.750 position:10%,start\n<em>keep payload</em>\n\nstarts at end\n00:00:02.000 --> 00:00:02.500 align:end\nkeep-ending-boundary\n\nafter end\n00:00:02.001 --> 00:00:03.000\ndrop\n"

	got, err := subtitles.WindowVTT([]byte(source), ticksPerSecond, 2*ticksPerSecond, true)
	if err != nil {
		t.Fatal(err)
	}
	const want = "WEBVTT\n\nNOTE retained verbatim\nmetadata --> is not a cue\n\nSTYLE\n::cue { color: lime; }\n\nends at start\n00:00:00.000 --> 00:00:01.000 line:90%\nkeep-ending-boundary\n\ninside\n00:00:01.250 --> 00:00:01.750 position:10%,start\n<em>keep payload</em>\n\nstarts at end\n00:00:02.000 --> 00:00:02.500 align:end\nkeep-ending-boundary\n"
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("WindowVTT() bytes = %q\nwant %q", got, want)
	}
}

func TestWindowVTTShiftsKeptCuesAndClampsAtZero(t *testing.T) {
	t.Parallel()

	const source = "WEBVTT\n\nstraddles start\n00:00:00.750 --> 00:00:01.250 align:start\nfirst\n\ninside\n00:01.500 --> 00:02.250 position:10%,start\nsecond\n"
	got, err := subtitles.WindowVTT([]byte(source), ticksPerSecond, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	const want = "WEBVTT\n\nstraddles start\n00:00:00.000 --> 00:00:00.250 align:start\nfirst\n\ninside\n00:00:00.500 --> 00:00:01.250 position:10%,start\nsecond\n"
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("WindowVTT() shifted bytes = %q\nwant %q", got, want)
	}
}

func TestWindowSRTUsesIdenticalStrictWindowingAndCommaTimes(t *testing.T) {
	t.Parallel()

	const source = "1\n00:00:00,000 --> 00:00:00,999 X1:10 X2:20\ndrop\n\n2\n00:00:00,000 --> 00:00:01,000 align:start\nkept at start boundary\n\n3\n00:00:01,500 --> 00:00:02,000\nkept at end boundary\n\n4\n00:00:02,001 --> 00:00:03,000\ndrop\n"
	got, err := subtitles.WindowSRT([]byte(source), ticksPerSecond, 2*ticksPerSecond, false)
	if err != nil {
		t.Fatal(err)
	}
	const want = "2\n00:00:00,000 --> 00:00:00,000 align:start\nkept at start boundary\n\n3\n00:00:00,500 --> 00:00:01,000\nkept at end boundary\n"
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("WindowSRT() bytes = %q\nwant %q", got, want)
	}
}

func TestWindowSubtitleEndZeroIsUnbounded(t *testing.T) {
	t.Parallel()

	const source = "WEBVTT\n\nlate\n00:00:05.000 --> 00:00:06.000\nkept\n"
	got, err := subtitles.WindowVTT([]byte(source), 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(source)) {
		t.Fatalf("WindowVTT with unbounded end = %q, want unchanged %q", got, source)
	}
}

func TestAddVTTTimeMapReplacesEveryWebVTTOccurrence(t *testing.T) {
	t.Parallel()

	input := []byte("WEBVTT\n\nNOTE WEBVTT literal\nWEBVTT\n")
	const replacement = "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000"
	want := []byte(replacement + "\n\nNOTE " + replacement + " literal\n" + replacement + "\n")
	if got := subtitles.AddVTTTimeMap(input); !bytes.Equal(got, want) {
		t.Fatalf("AddVTTTimeMap() = %q\nwant %q", got, want)
	}
}
