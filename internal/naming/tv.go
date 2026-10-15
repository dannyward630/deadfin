// SPDX-License-Identifier: GPL-2.0-only

package naming

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

type EpisodeInfo struct {
	Path                string
	Container           string
	IsStub              bool
	StubType            string
	Format3D            string
	Is3D                bool
	SeriesName          string
	SeasonNumber        *int
	EpisodeNumber       *int
	EndingEpisodeNumber *int
	ByDate              bool
	Year                *int
	Month               *int
	Day                 *int
}

type SeasonResult struct {
	SeasonNumber   *int
	IsSeasonFolder bool
	Success        bool
}

type SeriesInfo struct {
	Path string
	Name string
	Year *int
}

type SeriesPathResult struct {
	SeriesName string
	Success    bool
}

type SeriesStatus string

const (
	SeriesStatusEnded      SeriesStatus = "Ended"
	SeriesStatusContinuing SeriesStatus = "Continuing"
	SeriesStatusUnreleased SeriesStatus = "Unreleased"
)

// TryParseSeriesStatus ports TvParserHelpers.TryParseSeriesStatus.
func TryParseSeriesStatus(status string) (SeriesStatus, bool) {
	switch strings.ToLower(status) {
	case "ended", "cancelled", "canceled":
		return SeriesStatusEnded, true
	case "continuing", "returning", "returning series", "pilot":
		return SeriesStatusContinuing, true
	case "unreleased":
		return SeriesStatusUnreleased, true
	default:
		return "", false
	}
}

var (
	episodeNamed          = regexp.MustCompile(`(?i)(?:^|/)(.*?)(?:[ ._-]+)s(?:eason)?\s*([0-9]+)[ ._-]*e(?:pisode)?\s*([0-9]+)`)
	episodeEP             = regexp.MustCompile(`(?i)[._ -]ep_?([0-9]+)(?:[^/]*)$`)
	episodeEPRange        = regexp.MustCompile(`(?i)^ep([0-9]+)-ep([0-9]+)`)
	episodeE              = regexp.MustCompile(`(?i)[^/]*?\.?e([0-9]+)\.(?:[^/]*)$`)
	episodeX              = regexp.MustCompile(`(?i)(?:^|/)(?:(.*?)[ ._-]+)?(?:s)?([0-9]{1,4})x[e]?([0-9]{1,3})`)
	episodeXBare          = regexp.MustCompile(`(?i)(?:^|/)(?:s)?([0-9]{1,4})x[e]?([0-9]{1,3})`)
	episodeS              = regexp.MustCompile(`(?i)(?:^|/)(?:(.*?)[ ._-]+)?s([0-9]{1,4})[ ._-]*e([0-9]{1,3})`)
	episodeCompactS       = regexp.MustCompile(`(?i)(?:^|/)([^/]*?)_s([0-9]{1,4})e([0-9]{1,3})`)
	episodeCompact        = regexp.MustCompile(`(?i)(?:^|/)([^/._-]+)_([0-9]+)([0-9]{2})(?:\.[^/]*)?$`)
	episodeDateYMD        = regexp.MustCompile(`([0-9]{4})[._ -]([0-9]{2})[._ -]([0-9]{2})`)
	episodeDateDMY        = regexp.MustCompile(`([0-9]{2})[._ -]([0-9]{2})[._ -]([0-9]{4})`)
	episodeSimpleRange    = regexp.MustCompile(`(?:^|/)([0-9]+)-([0-9]+)(?:\s*-|\s+|\.)`)
	episodeNamedNumber    = regexp.MustCompile(`(?i)episode\s*-?\s*([0-9]{1,4})`)
	episodeLeadingNumber  = regexp.MustCompile(`(?:^|/)([0-9]{1,4})(?:[.\s-]|$)`)
	episodeDashNumber     = regexp.MustCompile(`(?i)(?:^|/)(?:\[[^\]]+\]\s*)?([^/]+?)\s-\s*([0-9]{1,4})(?:\s|\.|\[|-|$)`)
	episodeBracketNumber  = regexp.MustCompile(`\[([0-9]{1,4})\]`)
	episodeTailNumber     = regexp.MustCompile(`(?:^|/)[^/]*\s([0-9]{1,4})(?:\s|\.|\[|$)`)
	seriesSPath           = regexp.MustCompile(`(?i)^(.*?)[._ -]+s(?:eason)?[._ -]*([0-9]+)(?:$|[._ -])`)
	seasonPrefixCandidate = regexp.MustCompile(`(?i)s([0-9]{1,4})`)
	seasonPrePattern      = regexp.MustCompile(`(?i)^\s*([0-9]+)(?:st|nd|rd|th|\.)*\s*(?:seasong|season|sæson|saison|staffel|series|stagione|säsong|seizoen|sezonul|sezona|sezon|sezóna|série|séria|serie|seria|temporada|kausi|시즌|シーズン|сезон)\s*(.*)$`)
	seasonPostPrefix      = regexp.MustCompile(`(?i)^\s*(?:seasong|season|sæson|saison|staffel|series|stagione|säsong|seizoen|sezonul|sezona|sezon|sezóna|série|séria|serie|seria|temporada|kausi|시즌|シーズン|сезон)\s*`)
	seasonKeyword         = regexp.MustCompile(`(?i)seasong|season|sæson|saison|staffel|series|stagione|säsong|seizoen|sezonul|sezona|sezon|sezóna|série|séria|serie|seria|temporada|kausi|시즌|シーズン|сезон`)
)

type EpisodeParseFilters struct {
	IsNamed                 *bool
	IsOptimistic            *bool
	SupportsAbsoluteNumbers *bool
	FillExtendedInfo        bool
}

// ParseEpisode is an ordered explicit port of the most permissive NamingOptions
// episode rules. The full .NET tempered lookaheads are represented by parsing the
// filename boundary first; source rules: NamingOptions.cs:324-493 and
// EpisodePathParser.cs:35-240.
func ParseEpisode(path string, isDirectory bool, opts NamingOptions) (*EpisodeInfo, error) {
	return ParseEpisodeWithFilters(path, isDirectory, opts, EpisodeParseFilters{FillExtendedInfo: true})
}

func ParseEpisodeWithFilters(path string, isDirectory bool, opts NamingOptions, filters EpisodeParseFilters) (*EpisodeInfo, error) {
	if len(opts.EpisodeExpressions) == 0 {
		return nil, nil
	}
	usesDefaultRules := sourceDefaultEpisodeExpressions(opts.EpisodeExpressions)
	if filters.IsNamed != nil || filters.IsOptimistic != nil {
		filtered := make([]*EpisodeExpression, 0, len(opts.EpisodeExpressions))
		for _, expression := range opts.EpisodeExpressions {
			if expression == nil || filters.IsNamed != nil && expression.IsNamed != *filters.IsNamed || filters.IsOptimistic != nil && expression.IsOptimistic != *filters.IsOptimistic {
				continue
			}
			filtered = append(filtered, expression)
		}
		opts.EpisodeExpressions = filtered
	}
	if !usesDefaultRules {
		for _, expression := range opts.EpisodeExpressions {
			if expression == nil {
				continue
			}
			match, _, err := expression.findStringSubmatchIndex(path)
			if err != nil {
				if _, dotNetErr := regexp2.Compile(expression.Expression, regexp2.IgnoreCase); dotNetErr != nil {
					return nil, err
				}
				continue
			}
			if match != nil {
				break
			}
		}
	}
	if usesDefaultRules && filters.SupportsAbsoluteNumbers != nil && !*filters.SupportsAbsoluteNumbers && !episodeCompact.MatchString(path) {
		return nil, nil
	}
	result := parseEpisode(path, isDirectory, opts, filters.SupportsAbsoluteNumbers, filters.FillExtendedInfo)
	if result == nil && usesDefaultRules && filters.IsNamed != nil && *filters.IsNamed && episodeCompact.MatchString(path) {
		defaults := DefaultOptions()
		result = parseEpisode(path, isDirectory, defaults, filters.SupportsAbsoluteNumbers, filters.FillExtendedInfo)
	}
	if result != nil && (result.SeriesName == "" || !defaultMultipleEpisodeRules(opts.MultipleEpisodeExpressions)) {
		expressions := make([]*EpisodeExpression, 0, len(opts.MultipleEpisodeExpressions)+len(opts.EpisodeExpressions))
		if result.SeriesName == "" && !usesDefaultRules {
			for _, expression := range opts.EpisodeExpressions {
				if expression != nil && expression.IsNamed {
					expressions = append(expressions, expression)
				}
			}
		}
		if !defaultMultipleEpisodeRules(opts.MultipleEpisodeExpressions) {
			for _, expression := range opts.MultipleEpisodeExpressions {
				if expression != nil && expression.IsNamed {
					expressions = append(expressions, expression)
				}
			}
		}
		for _, expression := range expressions {
			// EpisodePathParser.cs:201-239 evaluates only named rules in source order.
			pattern, err := regexp2.Compile(expression.Expression, regexp2.IgnoreCase)
			if err != nil {
				return nil, err
			}
			match, err := pattern.FindStringMatch(path)
			if err != nil {
				return nil, err
			}
			if match == nil {
				continue
			}
			if result.SeriesName == "" {
				if group := match.GroupByName("seriesname"); group != nil && group.String() != "" {
					result.SeriesName = group.String()
				}
			}
			if result.EndingEpisodeNumber == nil && result.EpisodeNumber != nil {
				if group := match.GroupByName("endingepnumber"); group != nil {
					result.EndingEpisodeNumber = parsePointer(group.String())
				}
			}
			if result.SeriesName != "" && (result.EpisodeNumber == nil || result.EndingEpisodeNumber != nil) {
				break
			}
		}
	}
	return result, nil
}

func parseEpisode(path string, isDirectory bool, opts NamingOptions, allowAbsoluteNumbers *bool, fillExtendedInfo bool) *EpisodeInfo {
	if path == "" {
		return nil
	}
	parsePath := path
	if sourceDefaultEpisodeExpressions(opts.EpisodeExpressions) {
		parsePath = strings.ReplaceAll(path, "\\", "/")
	}
	if isDirectory {
		parsePath += ".mp4"
	}
	if match := regexp.MustCompile(`^([0-9]{1,4})-([0-9]{1,3})$`).FindStringSubmatch(parsePath); match != nil {
		return withEpisode("", parsePointer(match[1]), parsePointer(match[2]), nil)
	}
	// A non-empty collection is the caller's complete ordered configuration,
	usingDefaults := sourceDefaultEpisodeExpressions(opts.EpisodeExpressions)
	// array. The procedural rules below are the internal equivalent of the
	// source defaults when callers use DefaultOptions.
	if len(opts.EpisodeExpressions) > 0 && !usingDefaults {
		for _, expression := range opts.EpisodeExpressions {
			result := parseEpisodeExpression(parsePath, expression, opts)
			if result == nil || !allowsAbsoluteEpisode(result, expression, allowAbsoluteNumbers) {
				continue
			}
			return result
		}
		return nil
	}
	if match := episodeEPRange.FindStringSubmatch(parsePath); match != nil {
		return withEpisode("", nil, parsePointer(match[1]), parsePointer(match[2]))
	}
	if match := episodeNamed.FindStringSubmatch(parsePath); match != nil {
		return withEpisode(cleanSeries(match[1]), parsePointer(match[2]), parsePointer(match[3]), endingEpisodeWithOptions(parsePath, match[2], match[3], opts, fillExtendedInfo))
	}
	if match := episodeEP.FindStringSubmatch(parsePath); match != nil {
		return withEpisode("", nil, parsePointer(match[1]), nil)
	}
	if match := episodeE.FindStringSubmatch(parsePath); match != nil {
		return withEpisode("", nil, parsePointer(match[1]), nil)
	}
	if match := episodeS.FindStringSubmatch(parsePath); match != nil {
		return withEpisode(cleanSeries(match[1]), parsePointer(match[2]), parsePointer(match[3]), endingEpisodeWithOptions(parsePath, match[2], match[3], opts, fillExtendedInfo))
	}
	if match := episodeCompactS.FindStringSubmatch(parsePath); match != nil {
		return withEpisode(cleanSeries(match[1]), parsePointer(match[2]), parsePointer(match[3]), endingEpisodeWithOptions(parsePath, match[2], match[3], opts, fillExtendedInfo))
	}
	if match := episodeXBare.FindStringSubmatch(parsePath); match != nil {
		return withEpisode("", parsePointer(match[1]), parsePointer(match[2]), endingEpisodeWithOptions(parsePath, match[1], match[2], opts, fillExtendedInfo))
	}
	if match := episodeX.FindStringSubmatch(parsePath); match != nil {
		return withEpisode(cleanSeries(match[1]), parsePointer(match[2]), parsePointer(match[3]), endingEpisodeWithOptions(parsePath, match[2], match[3], opts, fillExtendedInfo))
	}
	if result := parseDateEpisode(parsePath); result != nil {
		return result
	}
	if match := episodeCompact.FindStringSubmatch(parsePath); match != nil {
		if allowAbsoluteNumbers != nil && *allowAbsoluteNumbers {
			return nil
		}
		return withEpisode(cleanSeries(match[1]), parsePointer(match[2]), parsePointer(match[3]), nil)
	}
	if match := episodeSimpleRange.FindStringSubmatch(parsePath); match != nil {
		ending := parsePointer(match[2])
		if strings.TrimSpace(parsePath) == match[0] {
			ending = nil
		}
		return withEpisode("", parsePointer(match[1]), parsePointer(match[2]), ending)
	}
	return parseFallbackEpisode(parsePath)
}

// ResolveEpisode ports EpisodeResolver.Resolve's file acceptance and media
// metadata handoff. The existing ParseEpisode seam remains parser-only.
func ResolveEpisode(path string, isDirectory bool, opts NamingOptions, allowAbsoluteNumbers bool) (*EpisodeInfo, error) {
	if path == "" {
		return nil, nil
	}
	var media *VideoFileInfo
	var err error
	if isDirectory {
		media, err = ResolveVideoDirectoryWithError(path, opts, true, "")
	} else {
		media, err = ResolveVideoWithError(path, opts)
	}
	if err != nil {
		return nil, err
	}
	if media == nil {
		return nil, nil
	}
	var result *EpisodeInfo
	if sourceDefaultEpisodeExpressions(opts.EpisodeExpressions) {
		result, err = ParseEpisode(path, isDirectory, opts)
	} else {
		result, err = ParseEpisodeWithFilters(path, isDirectory, opts, EpisodeParseFilters{SupportsAbsoluteNumbers: &allowAbsoluteNumbers, FillExtendedInfo: true})
	}
	if err != nil {
		return nil, err
	}
	if result == nil {
		if !media.IsStub {
			return nil, nil
		}
		result = &EpisodeInfo{}
	}
	result.Path, result.Container, result.IsStub, result.StubType, result.Format3D, result.Is3D = path, media.Container, media.IsStub, media.StubType, media.Format3D, media.Is3D
	return result, nil
}

func parseEpisodeExpression(path string, expression *EpisodeExpression, opts NamingOptions) *EpisodeInfo {
	if expression == nil {
		return nil
	}
	name := path
	if expression.IsByDate {
		// EpisodePathParser normalizes WMC's underscore-delimited timestamps
		// before applying an expression flagged as date-based.
		name = strings.ReplaceAll(name, "_", "-")
	}
	match, regex, err := expression.findStringSubmatchIndex(name)
	if err != nil {
		return parseDotNetEpisodeExpression(name, expression)
	}
	if match == nil || (!expression.IsByDate && len(match) < 6) {
		return nil
	}
	if expression.IsByDate {
		return parseExpressionDate(name, match, expression)
	}

	if expression.IsNamed {
		series, _, _ := episodeCapture(name, match, regex, "seriesname")
		season, _, _ := episodeCapture(name, match, regex, "seasonnumber")
		episode, _, _ := episodeCapture(name, match, regex, "epnumber")
		ending, endingEnd, endingFound := episodeCapture(name, match, regex, "endingepnumber")
		endingNumber := parsePointer(ending)
		if !endingFound || endingEnd < len(name) && strings.ContainsRune("0123456789iIpP", rune(name[endingEnd])) {
			endingNumber = nil
		}
		if endingNumber == nil && season != "" && episode != "" {
			endingNumber = endingEpisodeWithOptions(name, season, episode, opts, true)
		}
		return withEpisode(series, parsePointer(season), parsePointer(episode), endingNumber)
	}

	return withEpisode("", matchPointer(name, match, 1), matchPointer(name, match, 2), nil)
}

func parseDotNetEpisodeExpression(path string, expression *EpisodeExpression) *EpisodeInfo {
	pattern, err := regexp2.Compile(expression.Expression, regexp2.IgnoreCase)
	if err != nil {
		return nil
	}
	match, err := pattern.FindStringMatch(path)
	if err != nil || match == nil {
		return nil
	}
	if expression.IsNamed {
		return withEpisode(dotNetGroup(match, "seriesname"), parsePointer(dotNetGroup(match, "seasonnumber")), parsePointer(dotNetGroup(match, "epnumber")), parsePointer(dotNetGroup(match, "endingepnumber")))
	}
	groups := match.Groups()
	if len(groups) < 3 {
		return nil
	}
	return withEpisode("", parsePointer(groups[1].String()), parsePointer(groups[2].String()), nil)
}

func dotNetGroup(match *regexp2.Match, name string) string {
	group := match.GroupByName(name)
	if group == nil {
		return ""
	}
	return group.String()
}

func episodeCapture(value string, match []int, regex *regexp.Regexp, names ...string) (string, int, bool) {
	for _, name := range names {
		group := regex.SubexpIndex(name)
		index := group * 2
		if group >= 0 && index+1 < len(match) && match[index] >= 0 {
			return value[match[index]:match[index+1]], match[index+1], true
		}
	}
	return "", 0, false
}

func matchPointer(value string, match []int, group int) *int {
	index := group * 2
	if index+1 >= len(match) || match[index] < 0 {
		return nil
	}
	return parsePointer(value[match[index]:match[index+1]])
}

func parseExpressionDate(value string, match []int, expression *EpisodeExpression) *EpisodeInfo {
	result := &EpisodeInfo{ByDate: true}
	dateValue := value[match[0]:match[1]]
	if len(match) >= 4 && match[2] >= 0 {
		dateValue = value[match[2]:match[3]]
	}
	layouts := dateTimeLayouts(expression.DateTimeFormats)
	for _, layout := range layouts {
		date, err := time.Parse(layout, dateValue)
		if err != nil {
			continue
		}
		year, month, day := date.Date()
		result.Year, result.Month, result.Day = &year, intPointer(int(month)), &day
		break
	}
	return result
}

func dateTimeLayouts(formats []string) []string {
	if len(formats) == 0 {
		return []string{
			"2006-01-02 15:04:05", "2006-01-02", "02-01-2006", "2006.01.02", "02.01.2006", "2006 01 02", "02 01 2006", "January 2, 2006",
		}
	}
	layouts := make([]string, 0, len(formats))
	for _, format := range formats {
		layouts = append(layouts, strings.NewReplacer("yyyy", "2006", "MM", "01", "dd", "02", "M", "1", "d", "2", "HH", "15", "mm", "04", "ss", "05").Replace(format))
	}
	return layouts
}

func allowsAbsoluteEpisode(_ *EpisodeInfo, expression *EpisodeExpression, allowAbsoluteNumbers *bool) bool {
	return allowAbsoluteNumbers == nil || expression.SupportsAbsoluteEpisodeNumbers == *allowAbsoluteNumbers
}

func withEpisode(series string, season, episode, ending *int) *EpisodeInfo {
	if episode == nil || invalidSeason(season) {
		return nil
	}
	return &EpisodeInfo{SeriesName: series, SeasonNumber: season, EpisodeNumber: episode, EndingEpisodeNumber: ending}
}

func invalidSeason(value *int) bool {
	return value != nil && (*value >= 200 && *value < 1928 || *value > 2500)
}

// parseDateEpisode explicitly preserves the two date expressions' observable
// semantics, including success for an invalid calendar date. This is the
// documented behavior of EpisodePathParser.Parse (EpisodePathParser.cs:81-111)
// for NamingOptions.cs:332-350; it avoids replacing .NET date parsing with a
// less precise permissive regular expression.
func parseDateEpisode(path string) *EpisodeInfo {
	if match := episodeDateYMD.FindStringSubmatchIndex(path); match != nil {
		return dateEpisode(path, match, false)
	}
	if match := episodeDateDMY.FindStringSubmatchIndex(path); match != nil {
		return dateEpisode(path, match, true)
	}
	return nil
}

func dateEpisode(path string, match []int, dayFirst bool) *EpisodeInfo {
	result := &EpisodeInfo{ByDate: true, SeriesName: cleanSeries(path[:match[0]])}
	yearStart, yearEnd := match[2], match[3]
	monthStart, monthEnd := match[4], match[5]
	dayStart, dayEnd := match[6], match[7]
	if dayFirst {
		yearStart, yearEnd = match[6], match[7]
		dayStart, dayEnd = match[2], match[3]
	}
	date, err := time.Parse("2006-01-02", path[yearStart:yearEnd]+"-"+path[monthStart:monthEnd]+"-"+path[dayStart:dayEnd])
	if err != nil {
		return result
	}
	year, month, day := date.Date()
	result.Year, result.Month, result.Day = &year, intPointer(int(month)), &day
	return result
}

func parseFallbackEpisode(path string) *EpisodeInfo {
	name := fileName(path)
	for _, pattern := range []*regexp.Regexp{episodeNamedNumber, episodeLeadingNumber} {
		if match := pattern.FindStringSubmatch(name); match != nil {
			return withEpisode("", nil, parsePointer(match[1]), nil)
		}
	}
	if match := episodeDashNumber.FindStringSubmatch(name); match != nil {
		return withEpisode(cleanAnimeSeries(match[1]), nil, parsePointer(match[2]), nil)
	}
	for _, pattern := range []*regexp.Regexp{episodeBracketNumber, episodeTailNumber} {
		if match := pattern.FindStringSubmatch(name); match != nil {
			return withEpisode("", nil, parsePointer(match[1]), nil)
		}
	}
	return nil
}

func cleanAnimeSeries(value string) string {
	value = cleanSeries(value)
	for strings.HasPrefix(value, "[") {
		end := strings.Index(value, "]")
		if end < 0 {
			break
		}
		value = strings.TrimSpace(value[end+1:])
	}
	return strings.TrimSpace(value)
}

func intPointer(value int) *int { return &value }

func cleanSeries(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "_.- ")
	value = strings.TrimSuffix(value, "/")
	if index := strings.LastIndexByte(value, '/'); index >= 0 {
		value = value[index+1:]
	}
	return strings.TrimSpace(value)
}

func endingEpisodeWithOptions(path, season, episode string, opts NamingOptions, fillExtendedInfo bool) *int {
	if !fillExtendedInfo {
		return nil
	}
	if len(opts.MultipleEpisodeExpressions) == 0 {
		return nil
	}
	if defaultMultipleEpisodeRules(opts.MultipleEpisodeExpressions) {
		return endingEpisode(path, season, episode)
	}
	for _, expression := range opts.MultipleEpisodeExpressions {
		if result := parseEpisodeExpression(path, expression, NamingOptions{}); result != nil && result.EndingEpisodeNumber != nil {
			return result.EndingEpisodeNumber
		}
	}
	return nil
}

func endingEpisode(path, season, episode string) *int {
	initial := regexp.MustCompile(`(?i)(?:s` + season + `[ ._-]*e|` + season + `x)` + episode).FindStringIndex(path)
	if initial == nil {
		return nil
	}
	remainder := path[initial[1]:]
	// A single bare dash-number is a title separator, not a multiple-episode
	// continuation. MultipleEpisodeExpressions require another marker; source:
	// NamingOptions.cs:767-776.
	if bare := regexp.MustCompile(`^\s*-\s*[0-9]{1,3}(?:\s|$)`).FindStringIndex(remainder); bare != nil && !strings.Contains(remainder[bare[1]:], "-") {
		return nil
	}
	// The upstream multi-episode expressions permit only immediately adjacent
	// dash/x/e continuations. This rejects title text such as "The 6-10".
	continuation := regexp.MustCompile(`(?i)^(?:\s*-\s*(?:[0-9]{1,4}x[e]?|[xe][xe]?)?|[-]?[xe])([0-9]{1,3})`)
	var ending *int
	for len(remainder) > 0 {
		match := continuation.FindStringSubmatchIndex(remainder)
		if match == nil {
			break
		}
		ending = parsePointer(remainder[match[2]:match[3]])
		remainder = remainder[match[1]:]
	}
	if ending == nil {
		// Direct equivalent for chained S01E23-E24-E26 form; the configured
		// source multi-expression captures the final E marker.
		matches := regexp.MustCompile(`(?i)-e([0-9]{1,3})`).FindAllStringSubmatch(remainder, -1)
		if len(matches) > 0 {
			ending = parsePointer(matches[len(matches)-1][1])
		}
	}
	if ending == nil {
		return nil
	}
	if len(remainder) > 0 && strings.ContainsRune("0123456789iIpP", rune(remainder[0])) {
		return nil
	}
	return ending
}

// ParseSeason ports SeasonPathParser's supported keyword/alias behavior
// (SeasonPathParser.cs:12-137). The prefix and keyword handlers below perform
// its lookahead checks directly because Go's RE2 does not support lookarounds.
func ParseSeason(path, parent string, specialAliases, numericFolders bool) SeasonResult {
	original := fileName(path)
	if season := prefixedSeason(original); season != nil {
		return seasonResult(season)
	}

	name := removeSeasonPunctuation(original)
	if parent != "" {
		name = removeFoldAll(name, removeSeasonPunctuation(fileName(parent)))
	}
	lower := strings.ToLower(name)
	if specialAliases && (lower == "specials" || lower == "extras") {
		return seasonResult(intPointer(0))
	}
	if numericFolders && allDigits(name) {
		if season := parseInt32Pointer(name); season != nil {
			return seasonResult(season)
		}
	}
	// A mixed library (no alias or numeric support) also needs a season
	// keyword in the folder name itself, not only in the cleaned name.
	mixedLibrary := !numericFolders && !specialAliases
	if match := seasonPrePattern.FindStringSubmatch(name); match != nil {
		if mixedLibrary && !seasonKeyword.MatchString(original) {
			return SeasonResult{}
		}
		if season := parseInt32Pointer(match[1]); season != nil {
			return seasonResult(season)
		}
	}
	if match := seasonPostPrefix.FindStringIndex(name); match != nil {
		if mixedLibrary && !seasonKeyword.MatchString(original) {
			return SeasonResult{}
		}
		if season := postfixedSeason(name[match[1]:]); season != nil {
			return seasonResult(season)
		}
	}
	return SeasonResult{}
}

func seasonResult(number *int) SeasonResult {
	return SeasonResult{SeasonNumber: number, IsSeasonFolder: true, Success: true}
}

// prefixedSeason is the deterministic equivalent of SeasonPrefix's `(?!...)`
// lookahead in SeasonPathParser.cs:21. It intentionally allows the prefix
// anywhere in the name, matching upstream's unanchored expression.
func prefixedSeason(name string) *int {
	for _, match := range seasonPrefixCandidate.FindAllStringSubmatchIndex(name, -1) {
		numberEnd := match[3]
		if numberEnd < len(name) {
			next := name[numberEnd]
			if (next >= '0' && next <= '9') || next == 'e' || next == 'E' {
				continue
			}
			if !strings.ContainsRune("._-[] \t\r\n", rune(next)) {
				continue
			}
		}
		if season := parseInt32Pointer(name[match[2]:match[3]]); season != nil {
			return season
		}
	}
	return nil
}

// postfixedSeason matches the lazy season-number capture and its two
// lookaheads in ProcessPost (SeasonPathParser.cs:26): stop before a resolution
// suffix or non-digit, but reject an immediately following E<number> marker.
func postfixedSeason(value string) *int {
	if value == "" || value[0] < '0' || value[0] > '9' {
		return nil
	}
	for end := 1; end <= len(value) && value[end-1] >= '0' && value[end-1] <= '9'; end++ {
		remainder := value[end:]
		if startsEpisodeMarker(remainder) {
			return nil
		}
		if remainder == "" || remainder[0] < '0' || remainder[0] > '9' || isResolutionTail(remainder) {
			return parseInt32Pointer(value[:end])
		}
	}
	return nil
}

func startsEpisodeMarker(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 2 && (value[0] == 'e' || value[0] == 'E') && value[1] >= '0' && value[1] <= '9'
}

func isResolutionTail(value string) bool {
	for digits := 3; digits <= 4; digits++ {
		if len(value) < digits+1 || (value[digits] != 'p' && value[digits] != 'P') {
			continue
		}
		if allDigits(value[:digits]) {
			return true
		}
	}
	return false
}

func removeSeasonPunctuation(value string) string {
	return strings.Map(func(char rune) rune {
		if strings.ContainsRune(" ._-[]", char) {
			return -1
		}
		return char
	}, value)
}

func removeFoldAll(value, remove string) string {
	if remove == "" {
		return value
	}
	lowerValue, lowerRemove := strings.ToLower(value), strings.ToLower(remove)
	for index := strings.Index(lowerValue, lowerRemove); index >= 0; index = strings.Index(lowerValue, lowerRemove) {
		value = value[:index] + value[index+len(remove):]
		lowerValue = lowerValue[:index] + lowerValue[index+len(lowerRemove):]
	}
	return value
}

func parseInt32Pointer(value string) *int {
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return nil
	}
	result := int(parsed)
	return &result
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// ResolveSeries ports SeriesResolver.Resolve (SeriesResolver.cs:33-70).
func ResolveSeries(path string, opts NamingOptions) (SeriesInfo, error) {
	name := fileName(path)
	if regexp.MustCompile(`(?i)^season\s+[0-9]+$`).MatchString(strings.TrimSpace(name)) {
		name = fileName(directoryPath(path))
	}
	if match := regexp.MustCompile(`^(.+?)\s*\(([0-9]{4})\)`).FindStringSubmatch(name); match != nil {
		return SeriesInfo{Path: path, Name: strings.TrimSpace(match[1]), Year: parsePointer(match[2])}, nil
	}
	parsed, err := ParseSeriesPath(path, opts)
	if err != nil {
		return SeriesInfo{}, err
	}
	if parsed.Success {
		name = parsed.SeriesName
	}
	if match := regexp.MustCompile(`(?i)^(.+?)[ ._-]+s[0-9]{1,4}[ ._-]*e[0-9]{1,3}`).FindStringSubmatch(name); match != nil {
		name = match[1]
	}
	return SeriesInfo{Path: path, Name: normalizeSeries(name)}, nil
}

func normalizeSeries(value string) string {
	parts := strings.FieldsFunc(value, func(char rune) bool { return char == '.' || char == '_' })
	if len(parts) < 2 {
		return strings.TrimSpace(value)
	}
	firstSingle := len(parts)
	for index := len(parts) - 1; index >= 0 && len([]rune(parts[index])) == 1; index-- {
		firstSingle = index
	}
	if firstSingle == 0 {
		return strings.TrimSpace(value)
	}
	prefix := strings.Join(parts[:firstSingle], " ")
	suffix := strings.Join(parts[firstSingle:], ".")
	if prefix == "" {
		return suffix
	}
	if suffix == "" {
		return prefix
	}
	return prefix + " " + suffix
}

func ParseSeriesPath(path string, opts NamingOptions) (SeriesPathResult, error) {
	normalizedPath := path
	if sourceDefaultEpisodeExpressions(opts.EpisodeExpressions) {
		normalizedPath = strings.ReplaceAll(path, "\\", "/")
	}
	for _, expression := range opts.EpisodeExpressions {
		if expression == nil || !expression.IsNamed {
			continue
		}
		// SeriesPathParser.cs accesses each reached named Regex; use regexp2 for
		// both RE2-compatible and lookaround expressions so order stays uniform.
		pattern, err := regexp2.Compile(expression.Expression, regexp2.IgnoreCase)
		if err != nil {
			return SeriesPathResult{}, err
		}
		match, err := pattern.FindStringMatch(normalizedPath)
		if err != nil {
			return SeriesPathResult{}, err
		}
		if match == nil {
			continue
		}
		series, season := match.GroupByName("seriesname"), match.GroupByName("seasonnumber")
		if series != nil && season != nil && series.String() != "" && season.String() != "" {
			return SeriesPathResult{SeriesName: strings.Trim(series.String(), " _.-"), Success: true}, nil
		}
	}
	if !sourceDefaultEpisodeExpressions(opts.EpisodeExpressions) {
		return SeriesPathResult{}, nil
	}
	name := fileName(path)
	if match := regexp.MustCompile(`(?i)^(.+?)[ ._-]+s(?:eason)?[ ._-]*[0-9]{1,4}[ ._-]*e[0-9]+`).FindStringSubmatch(name); match != nil {
		return SeriesPathResult{SeriesName: strings.Trim(match[1], " _.-"), Success: true}, nil
	}
	if regexp.MustCompile(`(?i)^(?:season\s+|s)[0-9]+$`).MatchString(name) {
		parent := fileName(directoryPath(path))
		if parent != "" {
			return SeriesPathResult{SeriesName: parent, Success: true}, nil
		}
	}
	if match := seriesSPath.FindStringSubmatch(name); match != nil && match[1] != "" {
		return SeriesPathResult{SeriesName: strings.Trim(match[1], " _.-"), Success: true}, nil
	}
	return SeriesPathResult{}, nil
}

func sameEpisodeExpressions(expressions []*EpisodeExpression, signatures []episodeExpressionSignature) bool {
	if len(expressions) != len(signatures) {
		return false
	}
	for index, expression := range expressions {
		if expression == nil {
			if signatures[index] != (episodeExpressionSignature{}) {
				return false
			}
			continue
		}
		current := episodeExpressionSignature{expression.Expression, expression.IsByDate, expression.IsOptimistic, expression.IsNamed, expression.SupportsAbsoluteEpisodeNumbers, strings.Join(expression.DateTimeFormats, "\x00")}
		if current != signatures[index] {
			return false
		}
	}
	return true
}

func sourceDefaultEpisodeExpressions(expressions []*EpisodeExpression) bool {
	defaults := defaultEpisodeExpressions()
	signatures := make([]episodeExpressionSignature, len(defaults))
	for index, expression := range defaults {
		signatures[index] = episodeExpressionSignature{expression.Expression, expression.IsByDate, expression.IsOptimistic, expression.IsNamed, expression.SupportsAbsoluteEpisodeNumbers, strings.Join(expression.DateTimeFormats, "\x00")}
	}
	return sameEpisodeExpressions(expressions, signatures)
}

func defaultMultipleEpisodeRules(expressions []*EpisodeExpression) bool {
	defaults := defaultMultipleEpisodeExpressions()
	if len(expressions) != len(defaults) {
		return false
	}
	for index := range expressions {
		if expressions[index] == nil || expressions[index].Expression != defaults[index].Expression || expressions[index].IsNamed != defaults[index].IsNamed || expressions[index].SupportsAbsoluteEpisodeNumbers != defaults[index].SupportsAbsoluteEpisodeNumbers {
			return false
		}
	}
	return true
}
