// SPDX-License-Identifier: GPL-2.0-only

package naming

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/dlclark/regexp2"
)

// VideoFileInfo is the resolved filename information from
// upstream/jellyfin/Emby.Naming/Video/VideoFileInfo.cs.
type VideoFileInfo struct {
	Path        string
	Container   string
	Name        string
	Year        *int
	ExtraType   ExtraType
	ExtraRule   *ExtraRule
	Format3D    string
	Is3D        bool
	IsStub      bool
	StubType    string
	IsDirectory bool
}

type CleanDateTimeResult struct {
	Name string
	Year *int
}

type Format3DResult struct {
	Is3D   bool
	Format string
}

func fileName(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		return path[index+1:]
	}
	return path
}

func directoryName(path string) string {
	path = strings.TrimRight(strings.ReplaceAll(path, "\\", "/"), "/")
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		return fileName(path[:index])
	}
	return ""
}

func directoryPath(path string) string {
	path = strings.TrimRight(strings.ReplaceAll(path, "\\", "/"), "/")
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		return path[:index]
	}
	return ""
}

func extension(path string) string {
	name := fileName(path)
	index := strings.LastIndexByte(name, '.')
	if index < 0 {
		return ""
	}
	return name[index:]
}

func withoutExtension(path string) string {
	name := fileName(path)
	index := strings.LastIndexByte(name, '.')
	if index <= 0 {
		return name
	}
	return name[:index]
}

func inFold(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}

func ResolveStub(path string, opts NamingOptions) (string, bool) {
	if path == "" || !inFold(opts.StubFileExtensions, extension(path)) {
		return "", false
	}
	token := strings.TrimPrefix(extension(withoutExtension(path)), ".")
	for _, rule := range opts.StubTypes {
		if strings.EqualFold(token, rule.Token) {
			return rule.StubType, true
		}
	}
	return "", true
}

// Parse3D ports token scanning rather than applying a regexp. This deliberately
// keeps the upstream one-token-predecessor behavior; see Format3DParser.cs:20-79.
func Parse3D(path string, opts NamingOptions) Format3DResult {
	delimiters := make(map[rune]struct{}, len(opts.VideoFlagDelimiters)+1)
	for _, delimiter := range opts.VideoFlagDelimiters {
		delimiters[delimiter] = struct{}{}
	}
	delimiters[' '] = struct{}{}
	for _, rule := range opts.Format3DRules {
		foundPrefix := rule.PrecedingToken == ""
		for _, token := range splitTokens(path, delimiters) {
			if !foundPrefix {
				foundPrefix = strings.EqualFold(token, rule.PrecedingToken)
				continue
			}
			if strings.EqualFold(token, rule.Token) || strings.EqualFold(token, rule.Token+"X") {
				return Format3DResult{Is3D: true, Format: rule.Token}
			}
		}
	}
	return Format3DResult{}
}

func splitTokens(value string, delimiters map[rune]struct{}) []string {
	start := 0
	out := make([]string, 0, 8)
	for index, char := range value {
		if _, ok := delimiters[char]; ok {
			out = append(out, value[start:index])
			start = index + len(string(char))
		}
	}
	if start < len(value) && strings.HasSuffix(value, "X") {
		out = append(out, value[start:])
	}
	return out
}

// ResolveExtra returns nil for a non-extra, matching ExtraRuleResolver's empty
// result at the public Go seam. Source: ExtraRuleResolver.cs:23-67.
func ResolveExtraWithError(path, libraryRoot string, opts NamingOptions) (*VideoFileInfo, error) {
	isAudio := inFold(opts.AudioFileExtensions, extension(path))
	isVideo := inFold(opts.VideoFileExtensions, extension(path))
	base := withoutExtension(path)
	trimmed := strings.TrimRightFunc(base, func(char rune) bool { return char >= '0' && char <= '9' })
	dir, fullDir := directoryName(path), directoryPath(path)
	for _, rule := range opts.VideoExtraRules {
		if rule.AudioOnly && !isAudio || !rule.AudioOnly && !isVideo {
			continue
		}
		matched := false
		switch rule.Kind {
		case extraFilename:
			matched = strings.EqualFold(base, rule.Token)
		case extraSuffix:
			matched = strings.HasSuffix(strings.ToLower(trimmed), strings.ToLower(rule.Token))
		case extraRegex:
			// ExtraRuleResolver.cs:43-56 compiles/evaluates only the reached rule.
			pattern, err := regexp2.Compile(rule.Token, regexp2.IgnoreCase)
			if err != nil {
				return nil, err
			}
			matched, err = pattern.MatchString(fileName(path))
			if err != nil {
				return nil, err
			}
		case extraDirectory:
			matched = strings.EqualFold(dir, rule.Token) && !strings.EqualFold(fullDir, libraryRoot)
		}
		if matched {
			return &VideoFileInfo{Path: path, ExtraType: rule.Type, ExtraRule: &rule}, nil
		}
	}
	return nil, nil
}

func CleanDateTimeWithError(name string, opts NamingOptions) (CleanDateTimeResult, error) {
	if defaultCleanDateRules(opts.CleanDateTimeRegexes) {
		return CleanDateTime(name, opts), nil
	}
	if strings.ContainsAny(name, `/\\`) {
		name = withoutExtension(name)
	}
	for _, expression := range opts.CleanDateTimeRegexes {
		pattern, err := regexp2.Compile(expression, regexp2.IgnoreCase)
		if err != nil {
			return CleanDateTimeResult{}, err
		}
		match, err := pattern.FindStringMatch(name)
		if err != nil {
			return CleanDateTimeResult{}, err
		}
		if match == nil {
			continue
		}
		groups := match.Groups()
		if len(groups) != 5 {
			continue
		}
		year := parsePointer(groups[2].String())
		if year == nil {
			continue
		}
		return CleanDateTimeResult{Name: strings.TrimRightFunc(groups[1].String(), unicode.IsSpace), Year: year}, nil
	}
	return CleanDateTimeResult{Name: name}, nil
}

// CleanDateTime is an explicit deterministic equivalent of NamingOptions's two
// .NET lookahead patterns (NamingOptions.cs:148-150), avoiding RE2 incompatibility.
func CleanDateTime(name string, opts NamingOptions) CleanDateTimeResult {
	if len(opts.CleanDateTimeRegexes) == 0 {
		return CleanDateTimeResult{Name: name}
	}
	if !defaultCleanDateRules(opts.CleanDateTimeRegexes) {
		result, _ := CleanDateTimeWithError(name, opts)
		return result
	}
	if strings.ContainsAny(name, `/\\`) {
		name = withoutExtension(name)
	}
	if !defaultCleanDateRules(opts.CleanDateTimeRegexes) {
		for _, configured := range opts.CleanDateTimeRegexes {
			pattern, err := regexp.Compile("(?i)" + goEpisodeExpression(configured))
			if err != nil {
				continue
			}
			match := pattern.FindStringSubmatch(name)
			if len(match) != 5 {
				continue
			}
			year := parsePointer(match[2])
			if year == nil {
				continue
			}
			return CleanDateTimeResult{Name: strings.TrimRightFunc(match[1], unicode.IsSpace), Year: year}
		}
		return CleanDateTimeResult{Name: name}
	}
	result := CleanDateTimeResult{Name: name}
	if name == "" {
		return result
	}
	matches := yearCandidate.FindAllStringIndex(name, -1)
	for pass := range 2 {
		for offset := range len(matches) {
			index := offset
			if pass == 0 {
				index = len(matches) - 1 - offset
			}
			match := matches[index]
			start, end := match[0], match[1]
			if start == 0 || end < len(name) && unicode.IsDigit(rune(name[end])) {
				continue
			}
			strong := isStrongDateDelimiter(rune(name[start-1]))
			if pass == 0 && !strong || pass == 1 && strong || !isDateDelimiter(rune(name[start-1])) {
				continue
			}
			if end+5 <= len(name) && isDateDelimiter(rune(name[end])) && twoDigitsDelimited(name[end:]) {
				continue
			}
			prefix := strings.TrimRightFunc(name[:start], isDateDelimiter)
			if prefix == "" || isDateDelimiter(lastRune(prefix)) {
				continue
			}
			year, _ := strconv.Atoi(name[start:end])
			return CleanDateTimeResult{Name: strings.TrimRightFunc(prefix, unicode.IsSpace), Year: &year}
		}
	}
	return result
}

func defaultCleanDateRules(rules []string) bool {
	defaults := DefaultOptions().CleanDateTimeRegexes
	if len(rules) != len(defaults) {
		return false
	}
	for index := range rules {
		if rules[index] != defaults[index] {
			return false
		}
	}
	return true
}

var yearCandidate = regexp.MustCompile(`(?:19[0-9]{2}|20[0-9]{2})`)

func isDateDelimiter(char rune) bool {
	switch char {
	case '_', '.', '(', ')', '[', ']', '-', ' ':
		return true
	}
	return false
}

func isStrongDateDelimiter(char rune) bool {
	return isDateDelimiter(char) && char != ' '
}

func lastRune(value string) rune {
	for _, char := range value {
		_ = char
	}
	return rune(value[len(value)-1])
}

func twoDigitsDelimited(value string) bool {
	if len(value) < 6 || !isDateDelimiter(rune(value[0])) || !unicode.IsDigit(rune(value[1])) || !unicode.IsDigit(rune(value[2])) || !isDateDelimiter(rune(value[3])) || !unicode.IsDigit(rune(value[4])) || !unicode.IsDigit(rune(value[5])) {
		return false
	}
	return true
}

var (
	cleanFlag           = regexp.MustCompile(`(?i)^\s*(.+?)[ _,.()\[\]-](3d|sbs|tab|hsbs|htab|mvc|hdr|hdc|uhd|ultrahd|4k|ac3|dts|custom|dc|divx|divx5|dsr|dsrip|dutch|dvd|dvdrip|dvdscr|dvdscreener|screener|dvdivx|cam|fragment|fs|hdtv|hdrip|hdtvrip|internal|limited|multi|subs|ntsc|ogg|ogm|pal|pdtv|proper|repack|rerip|retail|cd[1-9]|r5|bd5|bd|se|svcd|swedish|german|read.nfo|nfofix|unrated|ws|telesync|ts|telecine|tc|brrip|bdrip|480p|480i|576p|576i|720p|720i|1080p|1080i|2160p|hrhd|hrhdtv|hddvd|bluray|blu-ray|x264|x265|h264|h265|xvid|xvidvd|xxx|www.www|aac)(?:[ _,.()\[\]-]|$)`)
	cleanBracketTail    = regexp.MustCompile(`^\s*(.+?)((\s*\[[^\]]+\]\s*)+)(\.[^\s]+)?$`)
	cleanLeadingBracket = regexp.MustCompile(`^\s*\[[^\]]+\]\s*(.+)$`)
	cleanEpisodeRange   = regexp.MustCompile(`^\s*(.+?)\WE[0-9]+(?:-|~)E?[0-9]+(?:\W|$)`)
	cleanNumberTail     = regexp.MustCompile(`^\s*(.+?)\s+-\s+[0-9]+\s*$`)
	cleanExtraTail      = regexp.MustCompile(`(?i)^\s*(.+?)(?:[-._ ]trailer|[-._ ]sample|-(?:scene|clip|behindthescenes|deleted|deletedscene|featurette|short|interview|other|extra))$`)
)

func CleanStringWithError(name string, opts NamingOptions) (string, bool, error) {
	if defaultCleanStringRules(opts.CleanStringRegexes) {
		cleaned, ok := CleanString(name, opts)
		return cleaned, ok, nil
	}
	if name == "" || len(opts.CleanStringRegexes) == 0 {
		return "", false, nil
	}
	cleaned := false
	for _, expression := range opts.CleanStringRegexes {
		pattern, err := regexp2.Compile(expression, regexp2.IgnoreCase)
		if err != nil {
			return "", false, err
		}
		match, err := pattern.FindStringMatch(name)
		if err != nil {
			return "", false, err
		}
		if match == nil {
			continue
		}
		group := match.GroupByName("cleaned")
		if group == nil {
			continue
		}
		name, cleaned = strings.TrimSpace(group.String()), true
	}
	if !cleaned {
		return "", false, nil
	}
	return name, true, nil
}

func CleanString(name string, opts NamingOptions) (string, bool) {
	if !defaultCleanStringRules(opts.CleanStringRegexes) {
		cleaned, ok, _ := CleanStringWithError(name, opts)
		return cleaned, ok
	}
	if name == "" || len(opts.CleanStringRegexes) == 0 {
		return "", false
	}
	if !defaultCleanStringRules(opts.CleanStringRegexes) {
		cleaned := false
		for _, configured := range opts.CleanStringRegexes {
			pattern, err := regexp.Compile("(?i)" + goEpisodeExpression(configured))
			if err != nil {
				continue
			}
			match := pattern.FindStringSubmatch(name)
			index := pattern.SubexpIndex("cleaned")
			if match != nil && index >= 0 {
				candidate := strings.TrimSpace(match[index])
				name, cleaned = candidate, true
			}
		}
		if cleaned {
			return name, true
		}
		return "", false
	}
	cleaned := false
	for _, expression := range []*regexp.Regexp{cleanFlag, cleanBracketTail, cleanEpisodeRange, cleanNumberTail, cleanExtraTail} {
		if match := expression.FindStringSubmatch(name); match != nil {
			name, cleaned = strings.TrimSpace(match[1]), true
		}
	}
	if match := cleanLeadingBracket.FindStringSubmatch(name); match != nil && !isExtensionOnly(match[1]) {
		name, cleaned = strings.TrimSpace(match[1]), true
	}
	if !cleaned {
		return "", false
	}
	return name, true
}

func isExtensionOnly(value string) bool {
	if !strings.HasPrefix(value, ".") || len(value) == 1 {
		return false
	}
	for _, char := range value[1:] {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '_' {
			return false
		}
	}
	return true
}

func defaultCleanStringRules(rules []string) bool {
	defaults := DefaultOptions().CleanStringRegexes
	if len(rules) != len(defaults) {
		return false
	}
	for index := range rules {
		if rules[index] != defaults[index] {
			return false
		}
	}
	return true
}

// ResolveVideo ports VideoResolver.ResolveFile.
func ResolveVideoWithError(path string, opts NamingOptions) (*VideoFileInfo, error) {
	return ResolveVideoWithLibraryRootWithError(path, opts, "")
}

func ResolveVideoWithLibraryRootWithError(path string, opts NamingOptions, libraryRoot string) (*VideoFileInfo, error) {
	info := resolveVideoWithLibraryRoot(path, opts, libraryRoot)
	if info == nil {
		return nil, nil
	}
	date, err := CleanDateTimeWithError(withoutExtension(path), opts)
	if err != nil {
		return nil, err
	}
	info.Name, info.Year = date.Name, date.Year
	if cleaned, ok, err := CleanStringWithError(info.Name, opts); err != nil {
		return nil, err
	} else if ok {
		info.Name = cleaned
	}
	if extra, err := ResolveExtraWithError(path, libraryRoot, opts); err != nil {
		return nil, err
	} else if extra != nil {
		info.ExtraType, info.ExtraRule = extra.ExtraType, extra.ExtraRule
	}
	return info, nil
}

func ResolveVideoDirectoryWithError(path string, opts NamingOptions, parseName bool, libraryRoot string) (*VideoFileInfo, error) {
	info := resolveVideoDirectory(path, opts, parseName, libraryRoot)
	if info == nil {
		return nil, nil
	}
	if parseName {
		date, err := CleanDateTimeWithError(withoutExtension(fileName(path)), opts)
		if err != nil {
			return nil, err
		}
		info.Name, info.Year = date.Name, date.Year
		if cleaned, ok, err := CleanStringWithError(info.Name, opts); err != nil {
			return nil, err
		} else if ok {
			info.Name = cleaned
		}
	}
	if extra, err := ResolveExtraWithError(path, libraryRoot, opts); err != nil {
		return nil, err
	} else if extra != nil {
		info.ExtraType, info.ExtraRule = extra.ExtraType, extra.ExtraRule
	}
	return info, nil
}

// ResolveVideoWithLibraryRoot ports VideoResolver.ResolveFile's library-root overload.
func resolveVideoWithLibraryRoot(path string, opts NamingOptions, libraryRoot string) *VideoFileInfo {
	if path == "" {
		return nil
	}
	ext := extension(path)
	stubType, isStub := ResolveStub(path, opts)
	if !inFold(opts.VideoFileExtensions, ext) && !isStub {
		return nil
	}
	name := withoutExtension(path)
	cleanDate := CleanDateTime(name, opts)
	name = cleanDate.Name
	if clean, ok := CleanString(name, opts); ok {
		name = clean
	}
	threeD := Parse3D(path, opts)
	info := &VideoFileInfo{Path: path, Container: strings.TrimPrefix(ext, "."), Name: name, Year: cleanDate.Year, IsStub: isStub, StubType: stubType, Is3D: threeD.Is3D, Format3D: threeD.Format}
	return info
}

// ResolveVideoDirectory ports VideoResolver.ResolveDirectory. Directories are
// accepted regardless of media extension and retain an empty container.
func resolveVideoDirectory(path string, opts NamingOptions, parseName bool, libraryRoot string) *VideoFileInfo {
	if path == "" {
		return nil
	}
	name := withoutExtension(fileName(path))
	var year *int
	if parseName {
		cleanDate := CleanDateTime(name, opts)
		name, year = cleanDate.Name, cleanDate.Year
		if clean, ok := CleanString(name, opts); ok {
			name = clean
		}
	}
	threeD := Parse3D(path, opts)
	info := &VideoFileInfo{Path: path, Name: name, Year: year, IsDirectory: true, Is3D: threeD.Is3D, Format3D: threeD.Format}
	return info
}
