// SPDX-License-Identifier: GPL-2.0-only

// Definitive source-pinned Video regressions from the final naming review.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

// ExtraRuleResolver.cs:43-56 evaluates ordered rules lazily after media filtering.
func TestUpstreamVideoExtraRulesAreLazyAndUseDotNetRegexSemantics(t *testing.T) {
	t.Run("earlier suffix prevents later invalid regex", func(t *testing.T) {
		opts := naming.DefaultOptions()
		opts.VideoExtraRules = []naming.ExtraRule{
			{Type: naming.ExtraTrailer, Kind: naming.ExtraRuleSuffix, Token: "-trailer"},
			{Type: naming.ExtraUnknown, Kind: naming.ExtraRuleRegex, Token: `[`},
		}
		got, err := naming.ResolveExtraWithError("Movie-trailer.mkv", "", opts)
		if err != nil || got == nil || got.ExtraType != naming.ExtraTrailer {
			t.Fatalf("ResolveExtraWithError lazy suffix = (%#v, %v), want Trailer without error", got, err)
		}
	})

	t.Run("wrong media skips invalid audio rule", func(t *testing.T) {
		opts := naming.DefaultOptions()
		opts.VideoExtraRules = []naming.ExtraRule{{Type: naming.ExtraUnknown, Kind: naming.ExtraRuleRegex, Token: `[`, AudioOnly: true}}
		got, err := naming.ResolveExtraWithError("Movie.mkv", "", opts)
		if err != nil || got != nil {
			t.Fatalf("ResolveExtraWithError wrong-media regex = (%#v, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("reached dotnet lookahead matches", func(t *testing.T) {
		opts := naming.DefaultOptions()
		opts.VideoExtraRules = []naming.ExtraRule{{Type: naming.ExtraUnknown, Kind: naming.ExtraRuleRegex, Token: `(?=Movie).*\.mkv$`}}
		got, err := naming.ResolveExtraWithError("Movie.mkv", "", opts)
		if err != nil || got == nil || got.ExtraType != naming.ExtraUnknown {
			t.Fatalf("ResolveExtraWithError dotnet lookahead = (%#v, %v), want Unknown without error", got, err)
		}
	})
}

// StackResolver.cs:72-133 consumes stacking rules only, and FileStackRule.cs:18-46 uses .NET regex.
func TestUpstreamVideoStacksIgnoreExtraRulesAndSupportLookbehind(t *testing.T) {
	invalidExtra := naming.DefaultOptions()
	invalidExtra.VideoExtraRules = []naming.ExtraRule{{Type: naming.ExtraUnknown, Kind: naming.ExtraRuleRegex, Token: `[`}}
	paths := []string{"Movie part1.mkv", "Movie part2.mkv"}
	if got, err := naming.ResolveStacks(paths, invalidExtra); err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].Files, paths) {
		t.Fatalf("ResolveStacks ignores VideoExtraRules = (%#v, %v), want one stack without error", got, err)
	}
	infos := []naming.VideoFileInfo{{Path: paths[0]}, {Path: paths[1]}}
	if got, err := naming.ResolveVideoInfoStacks(infos, invalidExtra); err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].Files, paths) {
		t.Fatalf("ResolveVideoInfoStacks ignores VideoExtraRules = (%#v, %v), want one stack without error", got, err)
	}

	lookbehind := naming.DefaultOptions()
	lookbehind.VideoFileStackingRules = []naming.FileStackRule{{Token: `^(?<filename>Movie)(?<=Movie) (?<number>[0-9]+)\.mkv$`, IsNumerical: true}}
	lookbehindPaths := []string{"Movie 1.mkv", "Movie 2.mkv"}
	if got, err := naming.ResolveStacks(lookbehindPaths, lookbehind); err != nil || len(got) != 1 || got[0].Name != "Movie" || !reflect.DeepEqual(got[0].Files, lookbehindPaths) {
		t.Fatalf("ResolveStacks dotnet lookbehind = (%#v, %v), want Movie stack", got, err)
	}
}

// CleanDateTimeParser.cs:16-52 applies expressions to supplied input and TrimEnd()s captures.
func TestUpstreamVideoCleanDateTimePreservesCommaInputAndTrimsOnlyTrailingCaptureWhitespace(t *testing.T) {
	if got := naming.CleanDateTime("Movie, edition.2013", naming.DefaultOptions()); got.Name != "Movie, edition" || !reflect.DeepEqual(got.Year, intp(2013)) {
		t.Fatalf("CleanDateTime comma edition = %#v, want Movie, edition/2013", got)
	}
	opts := naming.DefaultOptions()
	opts.CleanDateTimeRegexes = []string{`^(Title  )ZZ(2024)()()$`}
	if got := naming.CleanDateTime("Title  ZZ2024", opts); got.Name != "Title" || !reflect.DeepEqual(got.Year, intp(2024)) {
		t.Fatalf("CleanDateTime trailing custom capture whitespace = %#v, want Title/2024", got)
	}
}

// CleanStringParser.cs:21-48 continues after a successful but unchanged capture.
func TestUpstreamVideoCleanStringContinuesAfterUnchangedCapture(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.CleanStringRegexes = []string{`^(?<cleaned>.+)$`, `^(?<cleaned>Title)ZZ$`}
	got, ok := naming.CleanString("TitleZZ", opts)
	if !ok || got != "Title" {
		t.Fatalf("CleanString unchanged then later rule = (%q, %t), want (Title, true)", got, ok)
	}
}

// VideoListResolver.cs:34-35,275-295 orders full digit sequences, not saturated ints.
func TestUpstreamVideoVersionsOrderHugeResolutionsByLastDigit(t *testing.T) {
	paths := []string{
		"/movies/Movie/Movie-999999999999999999998p.mkv",
		"/movies/Movie/Movie-999999999999999999999p.mkv",
	}
	got, err := naming.ResolveVideoList(paths, naming.DefaultOptions())
	if err != nil || len(got) != 1 || len(got[0].Files) != 1 || got[0].Files[0].Path != paths[1] {
		t.Fatalf("ResolveVideoList huge resolution last digit = (%#v, %v), want primary %q", got, err, paths[1])
	}
}
