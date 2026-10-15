// SPDX-License-Identifier: GPL-2.0-only

// Source-led configurable TV parser regressions from Emby.Naming/TV/EpisodePathParser.cs:53-124,201-232 and SeriesPathParser.cs:21,46.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestConfiguredEpisodeExpressionsNilDisablesParsing(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions = nil
	got, err := naming.ParseEpisodeWithFilters("Show.S01E02.mkv", false, opts, naming.EpisodeParseFilters{FillExtendedInfo: true})
	if err != nil || got != nil {
		t.Fatalf("nil EpisodeExpressions result = %#v, %v; want nil without error", got, err)
	}
}

func TestConfiguredNamedPrimaryPreservesSeriesWithoutExtendedInfo(t *testing.T) {
	expression := naming.NewEpisodeExpression(`^(?<seriesname>__Show__)-(?<seasonnumber>[0-9]+)-(?<epnumber>[0-9]+)$`, false)
	expression.IsNamed = true
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions = []*naming.EpisodeExpression{expression}
	got, err := naming.ParseEpisodeWithFilters("__Show__-1-2", false, opts, naming.EpisodeParseFilters{FillExtendedInfo: false})
	if err != nil || got == nil || got.SeriesName != "__Show__" || !equalIntPointer(got.SeasonNumber, intp(1)) || !equalIntPointer(got.EpisodeNumber, intp(2)) {
		t.Fatalf("named primary without extended info = %#v, %v; want __Show__ S1E2 without error", got, err)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:79-92,201-232: when a positional
// primary lacks a series name, named EpisodeExpressions are prepended as
// enrichment candidates before named MultipleEpisodeExpressions.
func TestNamedEpisodeExpressionsEnrichUnnamedPositionalPrimary(t *testing.T) {
	primary := naming.NewEpisodeExpression(`^Show-([0-9]+)-([0-9]+)$`, false)
	primary.IsNamed = false
	named := naming.NewEpisodeExpression(`^(?<seriesname>Show)-(?<seasonnumber>[0-9]+)-(?<epnumber>[0-9]+)$`, false)
	named.IsNamed = true
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions = []*naming.EpisodeExpression{primary, named}
	got, err := naming.ParseEpisodeWithFilters("Show-1-2", false, opts, naming.EpisodeParseFilters{FillExtendedInfo: true})
	if err != nil || got == nil || got.SeriesName != "Show" || !equalIntPointer(got.SeasonNumber, intp(1)) || !equalIntPointer(got.EpisodeNumber, intp(2)) {
		t.Fatalf("named EpisodeExpression enrichment = %#v, %v; want Show S1E2 without error", got, err)
	}
}

func TestConfiguredDateExpressionsUseInvariantFormats(t *testing.T) {
	cases := []struct {
		name, input string
		expression  *naming.EpisodeExpression
	}{
		{"numeric", "1/2/2020", func() *naming.EpisodeExpression {
			expression := naming.NewEpisodeExpression(`^([0-9]{1,2}/[0-9]{1,2}/[0-9]{4})$`, true)
			expression.DateTimeFormats = []string{"M/d/yyyy"}
			return expression
		}()},
		{"month name", "January 2, 2020", naming.NewEpisodeExpression(`^([A-Za-z]+ [0-9]{1,2}, [0-9]{4})$`, true)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := naming.DefaultOptions()
			opts.EpisodeExpressions = []*naming.EpisodeExpression{c.expression}
			got, err := naming.ParseEpisodeWithFilters(c.input, false, opts, naming.EpisodeParseFilters{FillExtendedInfo: true})
			if err != nil || got == nil || !got.ByDate || !equalIntPointer(got.Year, intp(2020)) || !equalIntPointer(got.Month, intp(1)) || !equalIntPointer(got.Day, intp(2)) {
				t.Fatalf("invariant date %q = %#v, %v; want 2020-01-02 without error", c.input, got, err)
			}
		})
	}
}

func TestConfiguredMultipleExpressionsValidateLazily(t *testing.T) {
	primary := naming.NewEpisodeExpression(`^Show-([0-9]+)-([0-9]+)$`, false)
	valid := naming.NewEpisodeExpression(`^(?<seriesname>Show)-(?<seasonnumber>[0-9]+)-(?<epnumber>[0-9]+)$`, false)
	valid.IsNamed = true
	invalid := naming.NewEpisodeExpression(`[`, false)
	invalid.IsNamed = true
	base := naming.DefaultOptions()
	base.EpisodeExpressions = []*naming.EpisodeExpression{primary}

	invalidFirst := base
	invalidFirst.MultipleEpisodeExpressions = []*naming.EpisodeExpression{invalid}
	if got, err := naming.ParseEpisodeWithFilters("Show-1-2", false, invalidFirst, naming.EpisodeParseFilters{FillExtendedInfo: true}); err == nil || got != nil {
		t.Fatalf("reached invalid MultipleEpisodeExpression = %#v, %v; want nil and error", got, err)
	}

	unnamedInvalid := naming.NewEpisodeExpression(`[`, false)
	unnamedFirst := base
	unnamedFirst.MultipleEpisodeExpressions = []*naming.EpisodeExpression{unnamedInvalid, valid}
	if got, err := naming.ParseEpisodeWithFilters("Show-1-2", false, unnamedFirst, naming.EpisodeParseFilters{FillExtendedInfo: true}); err != nil || got == nil || got.SeriesName != "Show" || !equalIntPointer(got.SeasonNumber, intp(1)) || !equalIntPointer(got.EpisodeNumber, intp(2)) {
		t.Fatalf("unnamed malformed MultipleEpisodeExpression = (%#v, %v), want Show S1E2 without error", got, err)
	}

	seriesOnlyThenBroken := base
	seriesOnlyThenBroken.MultipleEpisodeExpressions = []*naming.EpisodeExpression{valid, invalid}
	if got, err := naming.ParseEpisodeWithFilters("Show-1-2", false, seriesOnlyThenBroken, naming.EpisodeParseFilters{FillExtendedInfo: true}); err == nil || got != nil {
		t.Fatalf("series-only named MultipleEpisodeExpression must reach later malformed rule = (%#v, %v), want (nil, error)", got, err)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:199-232: a named custom Multiple
// expression fills the ending omitted by the default primary expression.
func TestNamedCustomMultipleExpressionFillsMissingEnding(t *testing.T) {
	multiple := naming.NewEpisodeExpression(`EP(?<epnumber>[0-9]+)-EP(?<endingepnumber>[0-9]+)`, false)
	multiple.IsNamed = true
	opts := naming.DefaultOptions()
	opts.MultipleEpisodeExpressions = []*naming.EpisodeExpression{multiple}
	got, err := naming.ParseEpisodeWithFilters("EP01-EP02.mkv", false, opts, naming.EpisodeParseFilters{FillExtendedInfo: true})
	if err != nil || got == nil || !equalIntPointer(got.EpisodeNumber, intp(1)) || !equalIntPointer(got.EndingEpisodeNumber, intp(2)) {
		t.Fatalf("named custom MultipleEpisodeExpression = (%#v, %v), want E1-E2 without error", got, err)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:225-236: a named Multiple expression
// that supplies only series/episode cannot stop dispatch; a later named rule
// must still fill the missing ending episode.
func TestNamedMultipleExpressionsContinueUntilEndingIsFilled(t *testing.T) {
	primary := naming.NewEpisodeExpression(`^Show-([0-9]+)-([0-9]+)-[0-9]+$`, false)
	primary.IsNamed = false
	seriesOnly := naming.NewEpisodeExpression(`^(?<seriesname>Show)-(?<seasonnumber>[0-9]+)-(?<epnumber>[0-9]+)-[0-9]+$`, false)
	seriesOnly.IsNamed = true
	ending := naming.NewEpisodeExpression(`^Show-[0-9]+-(?<epnumber>[0-9]+)-(?<endingepnumber>[0-9]+)$`, false)
	ending.IsNamed = true
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions = []*naming.EpisodeExpression{primary}
	opts.MultipleEpisodeExpressions = []*naming.EpisodeExpression{seriesOnly, ending}
	got, err := naming.ParseEpisodeWithFilters("Show-1-2-3", false, opts, naming.EpisodeParseFilters{FillExtendedInfo: true})
	if err != nil || got == nil || got.SeriesName != "Show" || !equalIntPointer(got.SeasonNumber, intp(1)) || !equalIntPointer(got.EpisodeNumber, intp(2)) || !equalIntPointer(got.EndingEpisodeNumber, intp(3)) {
		t.Fatalf("ordered named MultipleEpisodeExpressions = (%#v, %v), want Show S1E2-E3 without error", got, err)
	}
}

func TestConfiguredEpisodeExpressionsSupportDotNetLookahead(t *testing.T) {
	lookahead := naming.NewEpisodeExpression(`^(?<seriesname>Show)(?=\.S(?<seasonnumber>01)E(?<epnumber>02)\.mkv$).*`, false)
	lookahead.IsNamed = true
	opts := naming.DefaultOptions()
	opts.EpisodeExpressions = []*naming.EpisodeExpression{lookahead}
	got, err := naming.ParseEpisodeWithFilters("Show.S01E02.mkv", false, opts, naming.EpisodeParseFilters{FillExtendedInfo: true})
	if err != nil || got == nil || got.SeriesName != "Show" || !equalIntPointer(got.SeasonNumber, intp(1)) || !equalIntPointer(got.EpisodeNumber, intp(2)) {
		t.Fatalf("lookahead expression = %#v, %v; want Show S1E2 without error", got, err)
	}
}

func TestConfiguredExpressionsReceiveRawBackslashPaths(t *testing.T) {
	opts := naming.DefaultOptions()
	pathExpression := naming.NewEpisodeExpression(`^C:\\(?<seriesname>Show)-(?<seasonnumber>[0-9]+)-(?<epnumber>[0-9]+)\.mkv$`, false)
	pathExpression.IsNamed = true
	opts.EpisodeExpressions = []*naming.EpisodeExpression{pathExpression}
	got, err := naming.ParseEpisodeWithFilters(`C:\Show-1-2.mkv`, false, opts, naming.EpisodeParseFilters{FillExtendedInfo: true})
	if err != nil || got == nil || got.SeriesName != "Show" || !equalIntPointer(got.SeasonNumber, intp(1)) || !equalIntPointer(got.EpisodeNumber, intp(2)) {
		t.Fatalf("raw backslash episode path = %#v, %v; want Show S1E2 without error", got, err)
	}

	seriesPath := naming.NewEpisodeExpression(`^C:\\(?<seriesname>Show)-S(?<seasonnumber>[0-9]+)$`, false)
	seriesPath.IsNamed = true
	opts.EpisodeExpressions = []*naming.EpisodeExpression{seriesPath}
	seriesResult, err := naming.ParseSeriesPath(`C:\Show-S1`, opts)
	if err != nil {
		t.Fatalf("raw backslash series path error = %v", err)
	}
	if !seriesResult.Success || seriesResult.SeriesName != "Show" {
		t.Fatalf("raw backslash series path = %#v, want Show", seriesResult)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:53-92: default procedural rules obey
// SupportsAbsoluteNumbers and IsNamed filtering, and FillExtendedInfo affects
// only secondary multiple-expression enrichment.
func TestDefaultEpisodeFiltersGateProceduralRulesAndSecondaryEnding(t *testing.T) {
	falseValue := false
	if got, err := naming.ParseEpisodeWithFilters("/Show/Show.S01E02.mkv", false, naming.DefaultOptions(), naming.EpisodeParseFilters{SupportsAbsoluteNumbers: &falseValue}); err != nil || got != nil {
		t.Fatalf("default absolute=false S01E02 = %#v, %v; want nil without error", got, err)
	}
	compact, err := naming.ParseEpisodeWithFilters("/server/anything_102.mp4", false, naming.DefaultOptions(), naming.EpisodeParseFilters{SupportsAbsoluteNumbers: &falseValue})
	if err != nil || compact == nil || !equalIntPointer(compact.SeasonNumber, intp(1)) || !equalIntPointer(compact.EpisodeNumber, intp(2)) {
		t.Fatalf("default absolute=false compact rule = %#v, %v; want S1E2 without error", compact, err)
	}
	trueValue := true
	if got, err := naming.ParseEpisodeWithFilters("/Show/Foo_ep_02.avi", false, naming.DefaultOptions(), naming.EpisodeParseFilters{IsNamed: &trueValue}); err != nil || got != nil {
		t.Fatalf("default named=true Foo_ep_02 = %#v, %v; want nil without error", got, err)
	}
	got, err := naming.ParseEpisodeWithFilters("Show.S01E01-E02.mkv", false, naming.DefaultOptions(), naming.EpisodeParseFilters{FillExtendedInfo: false})
	if err != nil || got == nil || !equalIntPointer(got.SeasonNumber, intp(1)) || !equalIntPointer(got.EpisodeNumber, intp(1)) || got.EndingEpisodeNumber != nil {
		t.Fatalf("FillExtendedInfo=false secondary ending = %#v, %v; want S1E1 without ending", got, err)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:172-183: a bare dash-number pair is a
// title separator, never a multi-episode range.
func TestSimpleDashPairDoesNotCreateAnEndingEpisode(t *testing.T) {
	got := mustParseUpstreamEpisode(t, "1-2", false, naming.DefaultOptions())
	if got == nil || !equalIntPointer(got.SeasonNumber, intp(1)) || !equalIntPointer(got.EpisodeNumber, intp(2)) || got.EndingEpisodeNumber != nil {
		t.Fatalf("ParseEpisode(1-2) = %#v; want S1E2 without ending", got)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:53-124 and
// Emby.Naming/Video/ExtraRuleResolver.cs:43-56: invalid caller-provided rules
// reached through public TV parser/resolver seams surface an error.
func TestTVPublicParserAndResolverPropagateInvalidConfiguredRuleErrors(t *testing.T) {
	invalidEpisode := naming.DefaultOptions()
	invalidEpisode.EpisodeExpressions = []*naming.EpisodeExpression{naming.NewEpisodeExpression(`[`, false)}
	if got, err := naming.ParseEpisode("Show.S01E01.mkv", false, invalidEpisode); err == nil || got != nil {
		t.Fatalf("ParseEpisode invalid EpisodeExpression = (%#v, %v), want (nil, error)", got, err)
	}
	if got, err := naming.ResolveEpisode("Show.S01E01.mkv", false, invalidEpisode, false); err == nil || got != nil {
		t.Fatalf("ResolveEpisode invalid EpisodeExpression = (%#v, %v), want (nil, error)", got, err)
	}

	invalidExtra := naming.DefaultOptions()
	invalidExtra.VideoExtraRules = []naming.ExtraRule{{Type: naming.ExtraUnknown, Kind: naming.ExtraRuleRegex, Token: `[`}}
	if got, err := naming.ResolveEpisode("Show.S01E01.mkv", false, invalidExtra, false); err == nil || got != nil {
		t.Fatalf("ResolveEpisode invalid ExtraRule = (%#v, %v), want (nil, error)", got, err)
	}
}

// Emby.Naming/TV/SeriesPathParser.cs:21,46: a reached malformed named
// EpisodeExpression propagates through both public series APIs, while a valid
// .NET lookahead remains eligible to parse.
func TestSeriesPublicAPIsPropagateInvalidNamedExpressionErrors(t *testing.T) {
	invalid := naming.DefaultOptions()
	broken := naming.NewEpisodeExpression(`[`, false)
	broken.IsNamed = true
	invalid.EpisodeExpressions = []*naming.EpisodeExpression{broken}
	if got, err := naming.ParseSeriesPath("Show.S01", invalid); err == nil || got.Success || got.SeriesName != "" {
		t.Fatalf("ParseSeriesPath invalid named expression = (%#v, %v), want (zero, error)", got, err)
	}
	if got, err := naming.ResolveSeries("Show.S01", invalid); err == nil || got.Name != "" || got.Path != "" {
		t.Fatalf("ResolveSeries invalid named expression = (%#v, %v), want (zero, error)", got, err)
	}

	valid := naming.DefaultOptions()
	lookahead := naming.NewEpisodeExpression(`^(?<seriesname>Show)(?=\.S(?<seasonnumber>01)$).*`, false)
	lookahead.IsNamed = true
	valid.EpisodeExpressions = []*naming.EpisodeExpression{lookahead}
	if got, err := naming.ParseSeriesPath("Show.S01", valid); err != nil || !got.Success || got.SeriesName != "Show" {
		t.Fatalf("ParseSeriesPath configured lookahead = (%#v, %v), want Show without error", got, err)
	}
}

// Emby.Naming/TV/EpisodePathParser.cs:79-92,201-232: reached configured
// MultipleEpisodeExpressions use .NET-compatible matching to enrich primary
// metadata; malformed rules error only when their ordered turn is reached.
func TestMultipleEpisodeExpressionsSupportDotNetLookaheadAndLazyErrors(t *testing.T) {
	primary := naming.NewEpisodeExpression(`^Show-([0-9]+)-([0-9]+)$`, false)
	primary.IsNamed = false
	lookahead := naming.NewEpisodeExpression(`^(?<seriesname>Show)(?=-(?<seasonnumber>[0-9]+)-(?<epnumber>[0-9]+)$).*`, false)
	lookahead.IsNamed = true
	broken := naming.NewEpisodeExpression(`[`, false)
	broken.IsNamed = true
	base := naming.DefaultOptions()
	base.EpisodeExpressions = []*naming.EpisodeExpression{primary}

	validFirst := base
	validFirst.MultipleEpisodeExpressions = []*naming.EpisodeExpression{lookahead, broken}
	if got, err := naming.ParseEpisodeWithFilters("Show-1-2", false, validFirst, naming.EpisodeParseFilters{FillExtendedInfo: true}); err == nil || got != nil {
		t.Fatalf("series-only lookahead MultipleEpisodeExpression must reach later malformed rule = (%#v, %v), want (nil, error)", got, err)
	}

	brokenFirst := base
	brokenFirst.MultipleEpisodeExpressions = []*naming.EpisodeExpression{broken, lookahead}
	if got, err := naming.ParseEpisodeWithFilters("Show-1-2", false, brokenFirst, naming.EpisodeParseFilters{FillExtendedInfo: true}); err == nil || got != nil {
		t.Fatalf("reached malformed MultipleEpisodeExpression = (%#v, %v), want (nil, error)", got, err)
	}
}
