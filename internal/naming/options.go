// SPDX-License-Identifier: GPL-2.0-only

package naming

import (
	"errors"
	"regexp"
	"strings"
	"sync"
)

// NamingOptions is the ordered, pure rule set used by the Emby.Naming parsers.
// It is a value: DefaultOptions returns independent slice backing arrays so callers
// may customize a resolver without changing another resolver's behavior.
type NamingOptions struct {
	VideoFileExtensions       []string
	StubFileExtensions        []string
	AudioFileExtensions       []string
	SubtitleFileExtensions    []string
	LyricFileExtensions       []string
	VideoFlagDelimiters       []rune
	MediaFlagDelimiters       []rune
	MediaForcedFlags          []string
	MediaDefaultFlags         []string
	MediaHearingImpairedFlags []string
	AlbumStackingPrefixes     []string
	ArtistSubfolders          []string
	StubTypes                 []StubTypeRule
	Format3DRules             []Format3DRule
	VideoExtraRules           []ExtraRule
	VideoFileStackingRules    []FileStackRule
	// The raw upstream patterns remain inspectable for callers that customize
	// a resolver. ResolveAudioBook uses deterministic equivalents where .NET
	// lookbehind cannot be expressed by Go's RE2 engine.
	AudioBookPartsExpressions  []string
	AudioBookNamesExpressions  []string
	EpisodeExpressions         []*EpisodeExpression
	MultipleEpisodeExpressions []*EpisodeExpression
	initialEpisodeExpressions  []episodeExpressionSignature

	// These retain the upstream rule text for inspection/customization. The
	// resolver uses deterministic matchers for rules that rely on .NET lookaround.
	// See upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:148-159.
	CleanDateTimeRegexes []string
	CleanStringRegexes   []string
}

type FileStackRule struct {
	Token       string
	IsNumerical bool
}

// EpisodeExpression is the configurable episode-pattern surface of
// Emby.Naming.Common.EpisodeExpression. Its fields mirror the source object;
// compiled regular expressions are cached privately and rebuilt whenever
// Expression changes, including a direct public-field assignment.
type EpisodeExpression struct {
	Expression                     string
	IsByDate                       bool
	IsOptimistic                   bool
	IsNamed                        bool
	SupportsAbsoluteEpisodeNumbers bool
	DateTimeFormats                []string

	mu                 sync.Mutex
	compiledExpression string
	compiled           *regexp.Regexp
	compileErr         error
}

type episodeExpressionSignature struct {
	expression       string
	byDate           bool
	optimistic       bool
	named            bool
	supportsAbsolute bool
	dateTimeFormats  string
}

func episodeExpressionSignatures(expressions []*EpisodeExpression) []episodeExpressionSignature {
	result := make([]episodeExpressionSignature, len(expressions))
	for index, expression := range expressions {
		if expression == nil {
			continue
		}
		result[index] = episodeExpressionSignature{expression.Expression, expression.IsByDate, expression.IsOptimistic, expression.IsNamed, expression.SupportsAbsoluteEpisodeNumbers, strings.Join(expression.DateTimeFormats, "\x00")}
	}
	return result
}

// NewEpisodeExpression creates an uncompiled expression, matching the source
// object's deferred Regex property construction.
func NewEpisodeExpression(expression string, byDate bool) *EpisodeExpression {
	return &EpisodeExpression{
		Expression:                     expression,
		IsByDate:                       byDate,
		SupportsAbsoluteEpisodeNumbers: true,
		DateTimeFormats:                []string{},
	}
}

// SetExpression replaces a rule. Callers may also assign Expression directly;
// the next match observes that assignment and refreshes the private cache.
func (expression *EpisodeExpression) SetExpression(value string) {
	if expression == nil {
		return
	}
	expression.mu.Lock()
	defer expression.mu.Unlock()
	expression.Expression = value
	expression.compiledExpression = ""
	expression.compiled = nil
	expression.compileErr = nil
}

// MatchString compiles lazily with the source's IgnoreCase behavior. Invalid
// expressions return their compilation error rather than panicking.
func (expression *EpisodeExpression) MatchString(value string) (bool, error) {
	regex, err := expression.regex()
	if err != nil {
		return false, err
	}
	return regex.MatchString(value), nil
}

func (expression *EpisodeExpression) findStringSubmatchIndex(value string) ([]int, *regexp.Regexp, error) {
	regex, err := expression.regex()
	if err != nil {
		return nil, nil, err
	}
	return regex.FindStringSubmatchIndex(value), regex, nil
}

func (expression *EpisodeExpression) regex() (*regexp.Regexp, error) {
	if expression == nil {
		return nil, errNilEpisodeExpression
	}
	expression.mu.Lock()
	defer expression.mu.Unlock()
	if expression.compiled == nil || expression.compiledExpression != expression.Expression {
		expression.compiledExpression = expression.Expression
		expression.compiled, expression.compileErr = regexp.Compile("(?i)" + goEpisodeExpression(expression.Expression))
	}
	return expression.compiled, expression.compileErr
}

// goEpisodeExpression accepts source-compatible .NET named captures while
// preserving Go-native (?P<name>...) captures. Lookarounds remain invalid in
// RE2 rather than being silently given different semantics.
func goEpisodeExpression(value string) string {
	return strings.ReplaceAll(value, "(?<", "(?P<")
}

var errNilEpisodeExpression = errors.New("nil episode expression")

type StubTypeRule struct {
	StubType string
	Token    string
}

type Format3DRule struct {
	Token          string
	PrecedingToken string
}

// ExtraType is a string alias so callers can compare upstream extra values
// without conversion, matching the string-valued transport seam.
type ExtraType = string

const (
	ExtraUnknown         ExtraType = "Unknown"
	ExtraTrailer         ExtraType = "Trailer"
	ExtraThemeVideo      ExtraType = "ThemeVideo"
	ExtraThemeSong       ExtraType = "ThemeSong"
	ExtraBehindTheScenes ExtraType = "BehindTheScenes"
	ExtraDeletedScene    ExtraType = "DeletedScene"
	ExtraInterview       ExtraType = "Interview"
	ExtraScene           ExtraType = "Scene"
	ExtraSample          ExtraType = "Sample"
	ExtraShort           ExtraType = "Short"
	ExtraFeaturette      ExtraType = "Featurette"
	ExtraClip            ExtraType = "Clip"
)

// ExtraRuleKind defines which path component an ExtraRule examines. Its values
// mirror upstream ExtraRuleType and are stable so callers can provide their own
// ordered VideoExtraRules.
type ExtraRuleKind uint8

const (
	ExtraRuleSuffix ExtraRuleKind = iota
	ExtraRuleFilename
	ExtraRuleRegex
	ExtraRuleDirectoryName
)

// Keep the default option table compact while all public customization uses
// the exported names above.
const (
	extraSuffix    = ExtraRuleSuffix
	extraFilename  = ExtraRuleFilename
	extraRegex     = ExtraRuleRegex
	extraDirectory = ExtraRuleDirectoryName
)

type ExtraRule struct {
	Type      ExtraType
	Kind      ExtraRuleKind
	Token     string
	AudioOnly bool
}

// DefaultOptions ports the ordered defaults in
// upstream/jellyfin/Emby.Naming/Common/NamingOptions.cs:24-782.
func DefaultOptions() NamingOptions {
	expressions := defaultEpisodeExpressions()
	return NamingOptions{
		VideoFileExtensions:        []string{".001", ".3g2", ".3gp", ".amv", ".asf", ".asx", ".avi", ".bin", ".bivx", ".divx", ".dv", ".dvr-ms", ".f4v", ".fli", ".flv", ".ifo", ".img", ".iso", ".m2t", ".m2ts", ".m2v", ".m4v", ".mkv", ".mk3d", ".mov", ".mp4", ".mpe", ".mpeg", ".mpg", ".mts", ".mxf", ".nrg", ".nsv", ".nuv", ".ogm", ".ogv", ".pva", ".qt", ".rec", ".rm", ".rmvb", ".strm", ".svq3", ".tp", ".ts", ".ty", ".viv", ".vob", ".vp3", ".webm", ".wmv", ".wtv", ".xvid"},
		StubFileExtensions:         []string{".disc"},
		AudioFileExtensions:        []string{".669", ".3gp", ".aa", ".aac", ".aax", ".ac3", ".act", ".adp", ".adplug", ".adx", ".afc", ".amf", ".aif", ".aifc", ".aiff", ".alac", ".amr", ".ape", ".ast", ".au", ".awb", ".cda", ".cue", ".dmf", ".dsf", ".dsm", ".dsp", ".dts", ".dvf", ".eac3", ".ec3", ".far", ".flac", ".gdm", ".gsm", ".gym", ".hps", ".imf", ".it", ".m15", ".m4a", ".m4b", ".mac", ".med", ".mka", ".mmf", ".mod", ".mogg", ".mp2", ".mp3", ".mpa", ".mpc", ".mpp", ".mp+", ".msv", ".nmf", ".nsf", ".nsv", ".oga", ".ogg", ".okt", ".opus", ".pls", ".ra", ".rf64", ".rm", ".s3m", ".sfx", ".shn", ".sid", ".stm", ".strm", ".ult", ".uni", ".vox", ".wav", ".wma", ".wv", ".xm", ".xsp", ".ymf"},
		SubtitleFileExtensions:     []string{".ass", ".mks", ".sami", ".smi", ".srt", ".ssa", ".sub", ".sup", ".vtt"},
		LyricFileExtensions:        []string{".lrc", ".elrc", ".txt"},
		VideoFlagDelimiters:        []rune{'(', ')', '-', '.', '_', '[', ']'},
		MediaFlagDelimiters:        []rune{'.'},
		MediaForcedFlags:           []string{"foreign", "forced"},
		MediaDefaultFlags:          []string{"default"},
		MediaHearingImpairedFlags:  []string{"cc", "hi", "sdh"},
		AlbumStackingPrefixes:      []string{"cd", "digital media", "disc", "disk", "vol", "volume", "part", "act"},
		ArtistSubfolders:           []string{"albums", "broadcasts", "bootlegs", "compilations", "dj-mixes", "eps", "live", "mixtapes", "others", "remixes", "singles", "soundtracks", "spokenwords", "streets"},
		StubTypes:                  []StubTypeRule{{"dvd", "dvd"}, {"hddvd", "hddvd"}, {"bluray", "bluray"}, {"bluray", "brrip"}, {"bluray", "bd25"}, {"bluray", "bd50"}, {"vhs", "vhs"}, {"tv", "HDTV"}, {"tv", "PDTV"}, {"tv", "DSR"}},
		Format3DRules:              []Format3DRule{{"hsbs", "3d"}, {"sbs", "3d"}, {"htab", "3d"}, {"tab", "3d"}, {"fsbs", ""}, {"hsbs", ""}, {"sbs", ""}, {"ftab", ""}, {"htab", ""}, {"tab", ""}, {"sbs3d", ""}, {"mvc", ""}},
		VideoExtraRules:            extraRules(),
		VideoFileStackingRules:     []FileStackRule{{Token: `^(?<filename>.*?)(?:(?<=[\]\)\}])|[ _.-]+)[\(\[]?(?<parttype>cd|dvd|part|pt|dis[ck])[ _.-]*(?<number>[0-9]+)[\)\]]?(?:\.[^.]+)?$`, IsNumerical: true}, {Token: `^(?<filename>.*?)(?:(?<=[\]\)\}])|[ _.-]+)[\(\[]?(?<parttype>cd|dvd|part|pt|dis[ck])[ _.-]*(?<number>[a-d])[\)\]]?(?:\.[^.]+)?$`, IsNumerical: false}},
		initialEpisodeExpressions:  episodeExpressionSignatures(expressions),
		EpisodeExpressions:         expressions,
		MultipleEpisodeExpressions: defaultMultipleEpisodeExpressions(),
		AudioBookPartsExpressions: []string{
			`ch(?:apter)?[\s_-]?(?<chapter>[0-9]+)`, `p(?:ar)?t[\s_-]?(?<part>[0-9]+)`, `^(?<chapter>[0-9]+)`,
			`(?<!ch(?:apter) )(?<part>[0-9]+)$`, `(?<chapter>[0-9]+)_(?<part>[0-9]+)`, `dis(?:c|k)[\s_-]?(?<chapter>[0-9]+)`,
		},
		AudioBookNamesExpressions: []string{
			`^(?<name>.+?)\s*\(\s*(?<year>[0-9]{4})\s*\)\s*$`, `^\s*(?<name>[^ ].*?)\s*$`,
		},
		CleanDateTimeRegexes: []string{
			`(.+[^_,.()\[\]-])[_.()\[\]-](19[0-9]{2}|20[0-9]{2})(?![0-9]+|\W[0-9]{2}\W[0-9]{2})([ _,.()\[\]-][^0-9]|).*(19[0-9]{2}|20[0-9]{2})*`,
			`(.+[^_,.()\[\]-])[ _.()\[\]-]+(19[0-9]{2}|20[0-9]{2})(?![0-9]+|\W[0-9]{2}\W[0-9]{2})([ _,.()\[\]-][^0-9]|).*(19[0-9]{2}|20[0-9]{2})*`,
		},
		CleanStringRegexes: []string{
			`^\s*(?<cleaned>.+?)[ _,.()\[\]-](3d|sbs|tab|hsbs|htab|mvc|HDR|HDC|UHD|UltraHD|4k|ac3|dts|custom|dc|divx|divx5|dsr|dsrip|dutch|dvd|dvdrip|dvdscr|dvdscreener|screener|dvdivx|cam|fragment|fs|hdtv|hdrip|hdtvrip|internal|limited|multi|subs|ntsc|ogg|ogm|pal|pdtv|proper|repack|rerip|retail|cd[1-9]|r5|bd5|bd|se|svcd|swedish|german|read.nfo|nfofix|unrated|ws|telesync|ts|telecine|tc|brrip|bdrip|480p|480i|576p|576i|720p|720i|1080p|1080i|2160p|hrhd|hrhdtv|hddvd|bluray|blu-ray|x264|x265|h264|h265|xvid|xvidvd|xxx|www.www|AAC|DTS)(?=[ _,.()\[\]-]|$)`,
			`^\s*(?<cleaned>.+?)((\s*\[[^\]]+\]\s*)+)(\.[^\s]+)?$`,
			`^\s*(?<cleaned>.+?)\WE[0-9]+(-|~)E?[0-9]+(\W|$)`,
			`^\s*\[[^\]]+\](?!\.\w+$)\s*(?<cleaned>.+)`,
			`^\s*(?<cleaned>.+?)\s+-\s+[0-9]+\s*$`,
			`^\s*(?<cleaned>.+?)(([-._ ](trailer|sample))|-(scene|clip|behindthescenes|deleted|deletedscene|featurette|short|interview|other|extra))$`,
		},
	}
}

// defaultEpisodeExpressions contains the ordered source forms that are also
// exposed for caller replacement. More permissive legacy forms remain in the
// parser after these canonical first-match forms; source NamingOptions.cs:324-493.
func defaultEpisodeExpressions() []*EpisodeExpression {
	expressions := []*EpisodeExpression{
		{Expression: `.*(\\|\/)(?<seriesname>((?![Ss]([0-9]+)[][ ._-]*[Ee]([0-9]+))[^\\\/])*)?[Ss](?<seasonnumber>[0-9]+)[][ ._-]*[Ee](?<epnumber>[0-9]+)([^\\/]*)$`, IsNamed: true},
		{Expression: `[\._ -]()[Ee][Pp]_?([0-9]+)([^\\/]*)$`},
		{Expression: `[^\\/]*?()\.?[Ee]([0-9]+)\.([^\\/]*)$`},
		{Expression: `(?<year>[0-9]{4})[._ -](?<month>[0-9]{2})[._ -](?<day>[0-9]{2})`, IsByDate: true, DateTimeFormats: []string{"yyyy.MM.dd", "yyyy-MM-dd", "yyyy_MM_dd"}},
		{Expression: `(?<day>[0-9]{2})[._ -](?<month>[0-9]{2})[._ -](?<year>[0-9]{4})`, IsByDate: true, DateTimeFormats: []string{"dd.MM.yyyy", "dd-MM-yyyy", "dd_MM_yyyy"}},
		{Expression: `.*[\\\/]((?<seriesname>[^\\/]+?)\s)?[Ss](?:eason)?\s*(?<seasonnumber>[0-9]+)\s+[Ee](?:pisode)?\s*(?<epnumber>[0-9]+).*$`, IsNamed: true},
		{Expression: `.*[\\\/](?![Ee]pisode)(?![^\\\/]*[Ss][0-9]+[][ ._-]*[Ee][0-9]+)(?<seriesname>[\w\s]+?)\s(?<epnumber>[0-9]{1,4})(-(?<endingepnumber>[0-9]{2,4}))*[^\\\/x]*$`, IsNamed: true},
		{Expression: `[\\\/\._ \[\(-]([0-9]+)x([0-9]+(?:(?:[a-i]|\.[1-9])(?![0-9]))?)([^\\\/]*)$`},
		{Expression: `.*[\\\/]?.*?(\[.*?\])+.*?(?<seriesname>[-\w\s]+?)[\s_]*-[\s_]*(?<epnumber>[0-9]+).*$`, IsNamed: true},
		{Expression: `.*[\\\/](?<seriesname>[^\\\/]+?)[\s_]+-[\s_]+(?<epnumber>[0-9]+)[\s_]*(?:\[.*?\]|\(.*?\))*[\s_]*(?:\.\w+)?$`, IsNamed: true},
		{Expression: `[\\/._ -](?<seriesname>(?![0-9]+[0-9][0-9])([^\\\/_])*)[\\\/._ -](?<seasonnumber>[0-9]+)(?<epnumber>[0-9][0-9](?:(?:[a-i]|\.[1-9])(?![0-9]))?)([._ -][^\\\/]*)$`, IsOptimistic: true, IsNamed: true, SupportsAbsoluteEpisodeNumbers: false},
		{Expression: `[\/._ -]p(?:ar)?t[_. -]()([ivx]+|[0-9]+)([._ -][^\/]*)$`},
		{Expression: `[Ee]pisode (?<epnumber>[0-9]+)(-(?<endingepnumber>[0-9]+))?[^\\\/]*$`, IsNamed: true},
		{Expression: `.*(\\|\/)[sS]?(?<seasonnumber>[0-9]+)[xX](?<epnumber>[0-9]+)[^\\\/]*$`, IsNamed: true},
		{Expression: `.*(\\|\/)[sS](?<seasonnumber>[0-9]+)[x,X]?[eE](?<epnumber>[0-9]+)[^\\\/]*$`, IsNamed: true},
		{Expression: `.*(\\|\/)(?<seriesname>((?![sS]?[0-9]{1,4}[xX][0-9]{1,3})[^\\\/])*)?([sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]+))[^\\\/]*$`, IsNamed: true},
		{Expression: `.*(\\|\/)(?<seriesname>[^\\\/]*)[sS](?<seasonnumber>[0-9]{1,4})[xX\.]?[eE](?<epnumber>[0-9]+)[^\\\/]*$`, IsNamed: true},
		{Expression: `.*[\\\/](?<epnumber>[0-9]+)(-(?<endingepnumber>[0-9]+))*\.\w+$`, IsOptimistic: true, IsNamed: true},
		{Expression: `([0-9]+)-([0-9]+)`},
		{Expression: `.*(\\|\/)(?<epnumber>[0-9]{1,3})(-(?<endingepnumber>[0-9]{2,3}))*\s?-\s?[^\\\/]*$`, IsOptimistic: true, IsNamed: true},
		{Expression: `.*(\\|\/)(?<epnumber>[0-9]{1,3})(-(?<endingepnumber>[0-9]{2,3}))*\.[^\\\/]+$`, IsOptimistic: true, IsNamed: true},
		{Expression: `.*[\\\/][^\\\/]* - (?<epnumber>[0-9]{1,3})(-(?<endingepnumber>[0-9]{2,3}))*[^\\\/]*$`, IsOptimistic: true, IsNamed: true},
		{Expression: `[Ss]eason[\._ ](?<seasonnumber>[0-9]+)[\\\/](?<epnumber>[0-9]{1,3})([^\\\/]*)$`, IsOptimistic: true, IsNamed: true},
		{Expression: `(.*(\\|\/))*(?<seriesname>.+)\/[Ss](eason)?[\. _\-]*(?<seasonnumber>[0-9]+)`, IsNamed: true},
		{Expression: `(.*(\\|\/))*(?<seriesname>.+)[\. _\-]+[sS](eason)?[\. _\-]*(?<seasonnumber>[0-9]+)`, IsNamed: true},
		{Expression: `(?:\[(?:[^\]]+)\]\s*)?(?<seriesname>\[[^\]]+\]|[^[\]]+)\s*\[(?<epnumber>[0-9]+)\]`, IsNamed: true},
	}
	for _, expression := range expressions {
		expression.SupportsAbsoluteEpisodeNumbers = true
		if expression.DateTimeFormats == nil {
			expression.DateTimeFormats = []string{}
		}
	}
	expressions[10].SupportsAbsoluteEpisodeNumbers = false
	expressions[3].DateTimeFormats = append(expressions[3].DateTimeFormats, "yyyy MM dd")
	expressions[4].DateTimeFormats = append(expressions[4].DateTimeFormats, "dd MM yyyy")
	return expressions
}

func defaultMultipleEpisodeExpressions() []*EpisodeExpression {
	patterns := []string{
		`.*(\\|\/)[sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3})((-| - )[0-9]{1,4}[eExX](?<endingepnumber>[0-9]{1,3}))+[^\\/]*$`,
		`.*(\\|\/)[sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3})((-| - )[0-9]{1,4}[xX][eE](?<endingepnumber>[0-9]{1,3}))+[^\\/]*$`,
		`.*(\\|\/)[sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3})((-| - )?[xXeE](?<endingepnumber>[0-9]{1,3}))+[^\\/]*$`,
		`.*(\\|\/)[sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3})(-[xE]?[eE]?(?<endingepnumber>[0-9]{1,3}))+[^\\/]*$`,
		`.*(\\|\/)(?<seriesname>((?![sS]?[0-9]{1,4}[xX][0-9]{1,3})[^\\/])*)?([sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3}))((-| - )[0-9]{1,4}[eExX](?<endingepnumber>[0-9]{1,3}))+[^\\/]*$`,
		`.*(\\|\/)(?<seriesname>((?![sS]?[0-9]{1,4}[xX][0-9]{1,3})[^\\/])*)?([sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3}))((-| - )[0-9]{1,4}[xX][eE](?<endingepnumber>[0-9]{1,3}))+[^\\/]*$`,
		`.*(\\|\/)(?<seriesname>((?![sS]?[0-9]{1,4}[xX][0-9]{1,3})[^\\/])*)?([sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3}))((-| - )?[xXeE](?<endingepnumber>[0-9]{1,3}))+[^\\/]*$`,
		`.*(\\|\/)(?<seriesname>((?![sS]?[0-9]{1,4}[xX][0-9]{1,3})[^\\/])*)?([sS]?(?<seasonnumber>[0-9]{1,4})[xX](?<epnumber>[0-9]{1,3}))(-[xX]?[eE]?(?<endingepnumber>[0-9]{1,3}))+[^\\/]*$`,
		`.*(\\|\/)(?<seriesname>[^\\/]*)[sS](?<seasonnumber>[0-9]{1,4})[xX\.]?[eE](?<epnumber>[0-9]{1,3})((-| - )?[xXeE](?<endingepnumber>[0-9]{1,3}))+[^\\/]*$`,
		`.*(\\|\/)(?<seriesname>[^\\/]*)[sS](?<seasonnumber>[0-9]{1,4})[xX\.]?[eE](?<epnumber>[0-9]{1,3})(-[xX]?[eE]?(?<endingepnumber>[0-9]{1,3}))+[^\\/]*$`,
	}
	result := make([]*EpisodeExpression, len(patterns))
	for index, pattern := range patterns {
		result[index] = &EpisodeExpression{Expression: pattern, IsNamed: true, SupportsAbsoluteEpisodeNumbers: true, DateTimeFormats: []string{}}
	}
	return result
}

func extraRules() []ExtraRule {
	return []ExtraRule{
		{ExtraTrailer, extraDirectory, "trailers", false},
		{ExtraThemeVideo, extraDirectory, "backdrops", false},
		{ExtraThemeSong, extraDirectory, "theme-music", true},
		{ExtraBehindTheScenes, extraDirectory, "behind the scenes", false},
		{ExtraDeletedScene, extraDirectory, "deleted scenes", false},
		{ExtraInterview, extraDirectory, "interviews", false},
		{ExtraScene, extraDirectory, "scenes", false},
		{ExtraSample, extraDirectory, "samples", false},
		{ExtraShort, extraDirectory, "shorts", false},
		{ExtraFeaturette, extraDirectory, "featurettes", false},
		{ExtraUnknown, extraDirectory, "extras", false},
		{ExtraUnknown, extraDirectory, "extra", false},
		{ExtraUnknown, extraDirectory, "other", false},
		{ExtraClip, extraDirectory, "clips", false},
		{ExtraTrailer, extraFilename, "trailer", false},
		{ExtraSample, extraFilename, "sample", false},
		{ExtraThemeSong, extraFilename, "theme", true},
		{ExtraTrailer, extraSuffix, "-trailer", false},
		{ExtraTrailer, extraSuffix, ".trailer", false},
		{ExtraTrailer, extraSuffix, "_trailer", false},
		{ExtraTrailer, extraSuffix, "- trailer", false},
		{ExtraSample, extraSuffix, "-sample", false},
		{ExtraSample, extraSuffix, ".sample", false},
		{ExtraSample, extraSuffix, "_sample", false},
		{ExtraSample, extraSuffix, "- sample", false},
		{ExtraScene, extraSuffix, "-scene", false},
		{ExtraClip, extraSuffix, "-clip", false},
		{ExtraInterview, extraSuffix, "-interview", false},
		{ExtraBehindTheScenes, extraSuffix, "-behindthescenes", false},
		{ExtraDeletedScene, extraSuffix, "-deleted", false},
		{ExtraDeletedScene, extraSuffix, "-deletedscene", false},
		{ExtraFeaturette, extraSuffix, "-featurette", false},
		{ExtraShort, extraSuffix, "-short", false},
		{ExtraUnknown, extraSuffix, "-extra", false},
		{ExtraUnknown, extraSuffix, "-other", false},
	}
}
