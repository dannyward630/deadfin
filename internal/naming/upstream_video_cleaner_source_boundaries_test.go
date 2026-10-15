// SPDX-License-Identifier: GPL-2.0-only

// Exact cleaner boundaries from upstream Emby.Naming/Common/NamingOptions.cs:148-150,159 and Video cleaner parsers.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoDefaultCleanerStripsSpaceSeparatedSample(t *testing.T) {
	got := mustResolveVideo(t, "Movie sample.mkv", naming.DefaultOptions())
	if got == nil || got.Name != "Movie" {
		t.Fatalf("ResolveVideo space-separated sample = %#v, want name Movie", got)
	}
}

// CleanDateTimeParser.cs:16-52 runs rules against the caller input; it does not remove extensions.
func TestUpstreamVideoCleanDateTimeDoesNotTruncateCommaContainingInput(t *testing.T) {
	got := naming.CleanDateTime("Movie,2013.mkv", naming.DefaultOptions())
	if got.Name != "Movie,2013.mkv" || got.Year != nil {
		t.Fatalf("CleanDateTime comma input = %#v, want unchanged without year", got)
	}
}

func TestUpstreamVideoCleanDateTimePreservesLeadingCustomCaptureWhitespace(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.CleanDateTimeRegexes = []string{`^(  Title)ZZ(2024)()()$`}
	got := naming.CleanDateTime("  TitleZZ2024", opts)
	if got.Name != "  Title" || !reflect.DeepEqual(got.Year, intp(2024)) {
		t.Fatalf("CleanDateTime leading custom capture whitespace = %#v, want leading spaces and year 2024", got)
	}
}

func TestUpstreamVideoCleanStringReportsMatchWhenCaptureIsUnchanged(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.CleanStringRegexes = []string{`^(?<cleaned>.+)$`}
	got, ok := naming.CleanString("Title", opts)
	if !ok || got != "Title" {
		t.Fatalf("CleanString unchanged named capture = (%q, %t), want (Title, true)", got, ok)
	}
}
