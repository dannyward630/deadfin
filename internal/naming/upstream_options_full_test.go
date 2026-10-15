// SPDX-License-Identifier: GPL-2.0-only

// Ported from upstream/jellyfin/tests/Jellyfin.Naming.Tests/Common/NamingOptionsTest.cs.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

// Jellyfin.Naming.Tests.Common.NamingOptionsTest.TestNamingOptionsCompile.
//
// The upstream test only asserts that constructor-populated regex collections
// are nonempty. At Go's public seam, the useful compatibility contract is that
// a caller-supplied rule set changes parser output, rather than exposing raw
// regex storage as implementation detail.
func TestNamingOptionsRulesConfigureParserBehavior(t *testing.T) {
	const path = "Movie.marker.custom3d.mkv"

	tests := []struct {
		name string
		opts naming.NamingOptions
		want naming.Format3DResult
	}{
		{
			name: "configured rule matches its ordered predecessor",
			opts: naming.NamingOptions{
				VideoFlagDelimiters: []rune{'.'},
				Format3DRules: []naming.Format3DRule{{
					Token:          "custom3d",
					PrecedingToken: "marker",
				}},
			},
			want: naming.Format3DResult{Is3D: true, Format: "custom3d"},
		},
		{
			name: "rule omission rejects otherwise identical filename",
			opts: naming.NamingOptions{
				VideoFlagDelimiters: []rune{'.'},
			},
			want: naming.Format3DResult{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := naming.Parse3D(path, tt.opts); got != tt.want {
				t.Fatalf("Parse3D(%q, configured options) = %#v, want %#v", path, got, tt.want)
			}
		})
	}
}

// Jellyfin.Naming.Tests.Common.NamingOptionsTest.TestNamingOptionsEpisodeExpressions.
//
// The C# Fact checks lazy Regex cache plumbing directly. This port defends the
// observable contract: expressions compile case-insensitively, a replacement
// takes effect after a previous match, and an invalid replacement is surfaced.
func TestNamingOptionsEpisodeExpressionsCompileAndReplace(t *testing.T) {
	expression := naming.NewEpisodeExpression(`^episode-[0-9]+$`, false)

	matched, err := expression.MatchString("EPISODE-42")
	if err != nil || !matched {
		t.Fatalf("initial expression MatchString(EPISODE-42) = (%t, %v), want (true, nil)", matched, err)
	}

	expression.SetExpression(`^special-[0-9]+$`)
	for _, tt := range []struct {
		input string
		want  bool
	}{
		{input: "EPISODE-42", want: false},
		{input: "SPECIAL-7", want: true},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, err := expression.MatchString(tt.input)
			if err != nil || got != tt.want {
				t.Fatalf("replaced expression MatchString(%q) = (%t, %v), want (%t, nil)", tt.input, got, err, tt.want)
			}
		})
	}

	expression.SetExpression(`[invalid`)
	if matched, err := expression.MatchString("SPECIAL-7"); err == nil || matched {
		t.Fatalf("invalid expression MatchString = (%t, %v), want (false, error)", matched, err)
	}
}

// Jellyfin.Naming.Tests.Common.NamingOptionsTest.TestNamingOptionsCompile;
// upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:320-493.
//
// Default options must return a caller-customizable episode-rule collection.
// The assertion is parser output, not the source pattern text: replacing the
// first returned rule makes that replacement the complete parser configuration.
func TestDefaultNamingOptionsExposeCustomizableEpisodeRules(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions[0].Expression = `^custom-(?<seasonnumber>[0-9]+)-(?<epnumber>[0-9]+)$`
	opts.EpisodeExpressions = opts.EpisodeExpressions[:1]

	got, err := naming.ParseEpisode("custom-02-03", false, opts)
	if err != nil || got == nil || got.SeasonNumber == nil || *got.SeasonNumber != 2 || got.EpisodeNumber == nil || *got.EpisodeNumber != 3 {
		t.Fatalf("ParseEpisode with a replaced default rule = (%#v, %v), want S02E03 with nil error", got, err)
	}
}

// Emby.Naming.Common.EpisodeExpression.EpisodeExpression(string, bool).
//
// DateTimeFormats has no parser-visible distinction between nil and an empty
// collection: both select the same fallback layouts. This narrow constructor
// assertion preserves the source's Array.Empty<string>() contract so callers
// can distinguish an initialized optional collection from an omitted one.
func TestNewEpisodeExpressionInitializesDateTimeFormats(t *testing.T) {
	if formats := naming.NewEpisodeExpression(`^episode$`, false).DateTimeFormats; formats == nil || len(formats) != 0 {
		t.Fatalf("NewEpisodeExpression(...).DateTimeFormats = %#v, want initialized empty collection", formats)
	}
}

// Jellyfin.Naming.Tests.Common.NamingOptionsTest.TestNamingOptionsCompile;
// upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:320-493, 765-780, and 140-144.
//
// These collection sizes are source-defined parser configuration, not incidental
// implementation details. The behavior tests below prove their public consumers
// honor configuration removal; this guard prevents a partial factory from being
// masked by a procedural fallback.
func TestDefaultNamingOptionsIncludeCompleteRuleFamilies(t *testing.T) {
	opts := naming.DefaultOptions()
	if got := len(opts.EpisodeExpressions); got != 26 {
		t.Fatalf("DefaultOptions().EpisodeExpressions has %d rules, want all 26 pinned source rules", got)
	}
	if got := len(opts.MultipleEpisodeExpressions); got != 10 {
		t.Fatalf("DefaultOptions().MultipleEpisodeExpressions has %d rules, want all 10 pinned source rules", got)
	}
	if got := len(opts.VideoFileStackingRules); got != 2 {
		t.Fatalf("DefaultOptions().VideoFileStackingRules has %d rules, want both pinned source rules", got)
	}
}

// Jellyfin.Naming.Tests.TV.MultiEpisodeTests.TestGetEndingEpisodeNumberFromFile;
// upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:765-780.
func TestDefaultMultipleEpisodeRulesControlEndingEpisode(t *testing.T) {
	const path = "/media/Show/Show S01E01-E02-E03.mkv"

	configured, err := naming.ParseEpisode(path, false, naming.DefaultOptions())
	if err != nil || configured == nil || configured.EndingEpisodeNumber == nil || *configured.EndingEpisodeNumber != 3 {
		t.Fatalf("ParseEpisode(%q, default rules) = (%#v, %v), want ending episode 3 with nil error", path, configured, err)
	}

	withoutRanges := naming.DefaultOptions()
	withoutRanges.MultipleEpisodeExpressions = nil
	withoutRangesResult, err := naming.ParseEpisode(path, false, withoutRanges)
	if err != nil || withoutRangesResult == nil || withoutRangesResult.EndingEpisodeNumber != nil {
		t.Fatalf("ParseEpisode(%q, no multiple-episode rules) = (%#v, %v), want no ending episode with nil error", path, withoutRangesResult, err)
	}
}

// Jellyfin.Naming.Tests.Video.StackTests.TestSimpleStack;
// upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:140-144.
func TestDefaultVideoStackingRulesControlStackResolution(t *testing.T) {
	paths := []string{"Film part1.mkv", "Film part2.mkv"}
	if stacks := mustResolveStacks(t, paths, naming.DefaultOptions()); len(stacks) != 1 || len(stacks[0].Files) != 2 {
		t.Fatalf("ResolveStacks(%v, default rules) = %#v, want one two-file stack", paths, stacks)
	}

	withoutStackRules := naming.DefaultOptions()
	withoutStackRules.VideoFileStackingRules = nil
	if stacks := mustResolveStacks(t, paths, withoutStackRules); len(stacks) != 0 {
		t.Fatalf("ResolveStacks(%v, no stacking rules) = %#v, want no stacks", paths, stacks)
	}
}

// Jellyfin.Naming.Tests.TV.MultiEpisodeTests.TestGetEndingEpisodeNumberFromFile;
// upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:765-780.
func TestConfiguredMultipleEpisodeExpressionsAreActuallyMatched(t *testing.T) {
	const path = "/media/Show/Show S01E01-E02.mkv"
	opts := naming.DefaultOptions()
	opts.MultipleEpisodeExpressions = []*naming.EpisodeExpression{naming.NewEpisodeExpression(`^never$`, false)}

	got, err := naming.ParseEpisode(path, false, opts)
	if err != nil || got == nil || got.EndingEpisodeNumber != nil {
		t.Fatalf("ParseEpisode(%q, never-match range rule) = (%#v, %v), want no ending episode with nil error", path, got, err)
	}
}

// Jellyfin.Naming.Tests.TV.SimpleEpisodeTests.TestSimple;
// upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:441-442.
func TestEpisodeExpressionAbsoluteNumberMetadataControlsMatching(t *testing.T) {
	const path = "/media/1-2.mkv"
	defaults := naming.DefaultOptions()
	source := defaults.EpisodeExpressions[18]

	matching := naming.NewEpisodeExpression(source.Expression, source.IsByDate)
	matching.IsNamed = source.IsNamed
	matching.IsOptimistic = source.IsOptimistic
	matching.SupportsAbsoluteEpisodeNumbers = false
	matching.DateTimeFormats = append([]string(nil), source.DateTimeFormats...)
	defaults.EpisodeExpressions = []*naming.EpisodeExpression{matching}
	if got, err := naming.ResolveEpisode(path, false, defaults, false); err != nil || got == nil || got.EpisodeNumber == nil || *got.EpisodeNumber != 2 {
		t.Fatalf("ResolveEpisode(%q, SupportsAbsoluteEpisodeNumbers=false) = (%#v, %v), want episode 2 with nil error", path, got, err)
	}

	matching.SupportsAbsoluteEpisodeNumbers = true
	if got, err := naming.ResolveEpisode(path, false, defaults, false); err != nil || got != nil {
		t.Fatalf("ResolveEpisode(%q, identical text with SupportsAbsoluteEpisodeNumbers=true) = (%#v, %v), want (nil, nil)", path, got, err)
	}
}

// Jellyfin.Naming.Tests.Video.CleanDateTimeTests.CleanDateTimeTest and
// Jellyfin.Naming.Tests.Video.CleanStringTests.CleanStringTest_NeedsCleaning_Success;
// upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:146-160.
func TestMutatingEveryDefaultCleanerRuleDisablesCleaning(t *testing.T) {
	opts := naming.DefaultOptions()
	for index := range opts.CleanDateTimeRegexes {
		opts.CleanDateTimeRegexes[index] += "ZZ"
	}
	for index := range opts.CleanStringRegexes {
		opts.CleanStringRegexes[index] += "ZZ"
	}

	if got := naming.CleanDateTime("Film (2020)", opts); got.Name != "Film (2020)" || got.Year != nil {
		t.Fatalf("CleanDateTime with mutated default rules = %#v, want unchanged name with no year", got)
	}
	if got, cleaned := naming.CleanString("Film 720p", opts); cleaned || got != "" {
		t.Fatalf("CleanString with mutated default rules = (%q, %t), want (empty, false)", got, cleaned)
	}
}

// Jellyfin.Naming.Tests.Video.StackTests.TestSimpleStack;
// upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:140-144.
func TestMutatingEveryDefaultStackRuleDisablesStacking(t *testing.T) {
	opts := naming.DefaultOptions()
	for index := range opts.VideoFileStackingRules {
		opts.VideoFileStackingRules[index].Token += "ZZ"
	}
	paths := []string{"Film part1.mkv", "Film part2.mkv"}
	if stacks := mustResolveStacks(t, paths, opts); len(stacks) != 0 {
		t.Fatalf("ResolveStacks(%v, mutated default stack rules) = %#v, want no stacks", paths, stacks)
	}
}

// Emby.Naming.Common.EpisodeExpression.EpisodeExpression(string, bool);
// upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:320-493 and 765-780.
func TestDefaultExpressionDateFormatCollectionsAreInitialized(t *testing.T) {
	opts := naming.DefaultOptions()
	for _, family := range []struct {
		name        string
		expressions []*naming.EpisodeExpression
	}{
		{name: "episode", expressions: opts.EpisodeExpressions},
		{name: "multiple episode", expressions: opts.MultipleEpisodeExpressions},
	} {
		for index, expression := range family.expressions {
			if expression == nil || expression.DateTimeFormats == nil {
				t.Fatalf("default %s expression %d has DateTimeFormats %#v, want initialized collection", family.name, index, expression)
			}
			if !expression.IsByDate && len(expression.DateTimeFormats) != 0 {
				t.Fatalf("default non-date %s expression %d has DateTimeFormats %#v, want empty collection", family.name, index, expression.DateTimeFormats)
			}
		}
	}
}

// Jellyfin.Naming.Tests.Video.CleanStringTests.CleanStringTest_NeedsCleaning_Success;
// upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:152-160.
func TestConfiguredCleanStringRulesRunOneForwardPass(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.CleanStringRegexes = []string{
		`^(?<cleaned>.*)A$`,
		`^(?<cleaned>.*)B$`,
	}
	if got, cleaned := naming.CleanString("TitleAB", opts); !cleaned || got != "TitleA" {
		t.Fatalf("CleanString with forward configured rules = (%q, %t), want (TitleA, true)", got, cleaned)
	}
}

// Jellyfin.Naming.Tests.TV.MultiEpisodeTests.TestGetEndingEpisodeNumberFromFile;
// upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:771-776.
// The rows exercise the six named/prefixed source continuations (rules 5-10)
// including one-digit season / three-digit episode values and x/E continuations.
func TestDefaultMultipleEpisodeRulesFiveThroughTenParseContinuations(t *testing.T) {
	for _, tt := range []struct {
		name   string
		path   string
		ending int
	}{
		{"rule5 numbered xE continuation", "/Season 2/Elementary - 02x003 - 02xE015 - Ep Name.mp4", 15},
		{"rule6 prefixed xE continuation", "/Season 2/Elementary - 02x003 - 02xE015 - Ep Name.mp4", 15},
		{"rule7 named xE continuation", "/Season 2/Elementary - 02x003 - xE015 - Ep Name.mp4", 15},
		{"rule8 named dash E continuation", "/Season 2/Elementary - 02x003-E015 - Ep Name.mp4", 15},
		{"rule9 S-prefix xE continuation", "/Season 2/Elementary - S02E003 - xE015 - Ep Name.mp4", 15},
		{"rule10 S-prefix dash E continuation", "/Season 2/Elementary - S02E003-E015 - Ep Name.mp4", 15},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := naming.ParseEpisode(tt.path, false, naming.DefaultOptions())
			if err != nil || got == nil || got.EndingEpisodeNumber == nil || *got.EndingEpisodeNumber != tt.ending {
				t.Fatalf("ParseEpisode(%q) = (%#v, %v), want ending episode %d with nil error", tt.path, got, err, tt.ending)
			}
		})
	}
}

// Emby.Naming.Common.NamingOptions.Compile; upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:782.
// Administrative regex configuration is compiled before use: an invalid rule
// must be an actionable error, never an ignored no-op.
func TestInvalidConfiguredCleanerRulesSurfaceCompilationErrors(t *testing.T) {
	t.Run("clean date", func(t *testing.T) {
		opts := naming.DefaultOptions()
		opts.CleanDateTimeRegexes = []string{"["}
		if _, err := naming.CleanDateTimeWithError("Film (2020)", opts); err == nil {
			t.Fatal("CleanDateTimeWithError(invalid configured rule) returned nil error")
		}
	})
	t.Run("clean string", func(t *testing.T) {
		opts := naming.DefaultOptions()
		opts.CleanStringRegexes = []string{"["}
		if _, _, err := naming.CleanStringWithError("Film 720p", opts); err == nil {
			t.Fatal("CleanStringWithError(invalid configured rule) returned nil error")
		}
	})
}

// Emby.Naming.Common.NamingOptions.Compile; upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:140-144,782.
func TestInvalidConfiguredStackRuleSurfacesCompilationError(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.VideoFileStackingRules = []naming.FileStackRule{{Token: "[", IsNumerical: true}}
	if _, err := naming.ResolveStacks([]string{"Film part1.mkv", "Film part2.mkv"}, opts); err == nil {
		t.Fatal("ResolveStacks(invalid configured rule) returned nil error")
	}
}

// Emby.Naming.Common.NamingOptions.Compile; upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:320-493,782.
func TestInvalidConfiguredEpisodeExpressionSurfacesCompilationError(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions = []*naming.EpisodeExpression{naming.NewEpisodeExpression("[", false)}
	got, err := naming.ParseEpisodeWithFilters("Show S01E01.mkv", false, opts, naming.EpisodeParseFilters{})
	if err == nil || got != nil {
		t.Fatalf("ParseEpisodeWithFilters(invalid configured expression) = (%#v, %v), want (nil, error)", got, err)
	}
}

// Jellyfin.Naming.Tests.Video.CleanDateTimeTests.CleanDateTimeTest;
// upstream/jellyfin/Emby.Naming/Video/CleanDateTimeParser.cs:38-52.
// CleanDateTimeParser accepts a custom expression only when its Match.Groups
// count is exactly five. The source reads name/year from groups 1/2; accepting
// a merely three-group expression would silently broaden administrator rules.
func TestConfiguredCleanDateTimeRequiresExactlyFiveCaptureGroups(t *testing.T) {
	for _, tt := range []struct {
		name       string
		expression string
		want       naming.CleanDateTimeResult
	}{
		{
			name:       "three capture groups are ignored",
			expression: `^(.+) \(([0-9]{4})\)$`,
			want:       naming.CleanDateTimeResult{Name: "Film (2020)"},
		},
		{
			name:       "five capture groups clean title and year",
			expression: `^(.+) \(([0-9]{4})(())\)$`,
			want:       naming.CleanDateTimeResult{Name: "Film", Year: intp(2020)},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := naming.DefaultOptions()
			opts.CleanDateTimeRegexes = []string{tt.expression}
			got, err := naming.CleanDateTimeWithError("Film (2020)", opts)
			if err != nil || got.Name != tt.want.Name || !equalIntPointer(got.Year, tt.want.Year) {
				t.Fatalf("CleanDateTimeWithError(%q, %q) = (%#v, %v), want (%#v, nil)", "Film (2020)", tt.expression, got, err, tt.want)
			}
		})
	}
}
