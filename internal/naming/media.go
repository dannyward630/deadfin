// SPDX-License-Identifier: GPL-2.0-only

package naming

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/dlclark/regexp2"
	"golang.org/x/text/language"
)

func IsAudioFile(path string, opts NamingOptions) bool {
	return path != "" && inFold(opts.AudioFileExtensions, extension(path))
}

// IsMultiDiscAlbum ports AlbumParser.IsMultiPart (AlbumParser.cs:34-68).
func IsMultiDiscAlbum(path string, opts NamingOptions) bool {
	name := fileName(path)
	if name == "" {
		return false
	}
	name = strings.TrimSpace(albumDelimiter.ReplaceAllString(name, " "))
	for _, prefix := range opts.AlbumStackingPrefixes {
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			continue
		}
		rest := strings.TrimSpace(name[len(prefix):])
		if fields := strings.Fields(rest); len(fields) > 0 && parsePointer(fields[0]) != nil {
			return true
		}
	}
	return false
}

var albumDelimiter = regexp.MustCompile(`[-.()\s]+`)

type AudioBookFileInfo struct {
	Path          string
	Container     string
	ChapterNumber *int
	PartNumber    *int
}

var trailingDigitsPattern = regexp.MustCompile(`([0-9]+)$`)

func ResolveAudioBook(path string, opts NamingOptions) (*AudioBookFileInfo, error) {
	for _, expression := range opts.AudioBookPartsExpressions {
		if _, err := regexp2.Compile(expression, regexp2.IgnoreCase); err != nil {
			return nil, fmt.Errorf("compile audiobook part expression: %w", err)
		}
	}
	return resolveAudioBook(path, opts), nil
}

// ResolveAudioBook ports AudioBookResolver and AudioBookFilePathParser
// (AudioBookResolver.cs:29-54; AudioBookFilePathParser.cs:29-65).
func resolveAudioBook(path string, opts NamingOptions) *AudioBookFileInfo {
	base := strings.TrimSuffix(fileName(path), extension(path))
	if path == "" || base == "" || !IsAudioFile(path, opts) {
		return nil
	}
	result := &AudioBookFileInfo{Path: path, Container: strings.TrimPrefix(extension(path), ".")}
	for _, expression := range opts.AudioBookPartsExpressions {
		if expression == `(?<!ch(?:apter) )(?<part>[0-9]+)$` {
			if result.PartNumber == nil {
				result.PartNumber = trailingAudioBookPart(base)
			}
			continue
		}
		pattern, err := regexp2.Compile(expression, regexp2.IgnoreCase)
		if err != nil {
			continue
		}
		match, err := pattern.FindStringMatch(base)
		if err != nil || match == nil {
			continue
		}
		if result.ChapterNumber == nil {
			if group := match.GroupByName("chapter"); group != nil {
				result.ChapterNumber = parsePointer(group.String())
			}
		}
		if result.PartNumber == nil {
			if group := match.GroupByName("part"); group != nil {
				result.PartNumber = parsePointer(group.String())
			}
		}
	}
	return result
}

// trailingAudioBookPart is the deterministic equivalent of the fourth
// AudioBookPartsExpressions rule. Its exact "chapter " exception is required
// by NamingOptions.cs:751, whose negative lookbehind is unsupported by RE2.
func trailingAudioBookPart(name string) *int {
	match := trailingDigitsPattern.FindStringSubmatchIndex(name)
	if match == nil {
		return nil
	}
	prefix := strings.ToLower(name[:match[0]])
	if strings.HasSuffix(prefix, "chapter ") || strings.HasSuffix(prefix, "ch ") {
		return nil
	}
	return parsePointer(name[match[2]:match[3]])
}

type AudioBookNameResult struct {
	Name string
	Year *int
}

// ParseAudioBookName ports AudioBookNameParser.Parse
// (AudioBookNameParser.cs:24-57). The public result deliberately mirrors the
// upstream parser's title/year surface rather than the file-path resolver.
func ParseAudioBookName(name string, opts NamingOptions) (AudioBookNameResult, error) {
	for _, expression := range opts.AudioBookNamesExpressions {
		if _, err := regexp2.Compile(expression, regexp2.IgnoreCase); err != nil {
			return AudioBookNameResult{}, err
		}
	}
	return parseAudioBookName(name, opts), nil
}

func parseAudioBookName(name string, opts NamingOptions) AudioBookNameResult {
	result := AudioBookNameResult{}
	for _, expression := range opts.AudioBookNamesExpressions {
		pattern, err := regexp2.Compile(expression, regexp2.IgnoreCase)
		if err != nil {
			continue
		}
		match, err := pattern.FindStringMatch(name)
		if err != nil || match == nil {
			continue
		}
		if group := match.GroupByName("name"); group != nil && result.Name == "" {
			result.Name = group.String()
		}
		if group := match.GroupByName("year"); group != nil && result.Year == nil {
			result.Year = parsePointer(group.String())
		}
	}
	if result.Name == "" {
		result.Name = name
	}
	return result
}

// CompareAudioBookFileInfo ports AudioBookFileInfo.CompareTo
// (AudioBookFileInfo.cs:59-82): nil chapter and part values sort before a
// number, followed by an ordinal path comparison.
func CompareAudioBookFileInfo(left, right *AudioBookFileInfo) int {
	if left == right {
		return 0
	}
	if left == nil {
		return -1
	}
	if right == nil {
		return 1
	}
	if comparison := compareOptionalInt(left.ChapterNumber, right.ChapterNumber); comparison != 0 {
		return comparison
	}
	if comparison := compareOptionalInt(left.PartNumber, right.PartNumber); comparison != 0 {
		return comparison
	}
	return compareUTF16Ordinal(left.Path, right.Path)
}

func compareOptionalInt(left, right *int) int {
	if left == nil {
		if right == nil {
			return 0
		}
		return -1
	}
	if right == nil {
		return 1
	}
	if *left < *right {
		return -1
	}
	if *left > *right {
		return 1
	}
	return 0
}

func compareUTF16Ordinal(left, right string) int {
	leftUnits, rightUnits := utf16.Encode([]rune(left)), utf16.Encode([]rune(right))
	for index := 0; index < len(leftUnits) && index < len(rightUnits); index++ {
		if leftUnits[index] < rightUnits[index] {
			return -1
		}
		if leftUnits[index] > rightUnits[index] {
			return 1
		}
	}
	if len(leftUnits) < len(rightUnits) {
		return -1
	}
	if len(leftUnits) > len(rightUnits) {
		return 1
	}
	return 0
}

type AudioBookInfo struct {
	Name              string
	Year              *int
	Files             []AudioBookFileInfo
	Extras            []AudioBookFileInfo
	AlternateVersions []AudioBookFileInfo
}

// ResolveAudioBookList ports AudioBookListResolver.Resolve and preserves the
// source's directory grouping, chapter/part ordering, and stable main-file
// selection (AudioBookListResolver.cs:31-153).
func ResolveAudioBookList(paths []string, opts NamingOptions) ([]AudioBookInfo, error) {
	type stack struct {
		name  string
		files []AudioBookFileInfo
	}
	stacks := make(map[string]*stack)
	order := make([]string, 0, len(paths))
	for _, path := range paths {
		file, err := ResolveAudioBook(path, opts)
		if err != nil {
			return nil, err
		}
		if file == nil {
			continue
		}
		directory := directoryPath(path)
		key := directory
		if directory == "" {
			// StackResolver.ResolveAudioBooks creates one stack per root input,
			// including duplicate paths.
			key = "\x00" + strconv.Itoa(len(order))
		}
		entry := stacks[key]
		if entry == nil {
			name := directoryName(path)
			if directory == "" {
				name = strings.TrimSuffix(fileName(path), extension(path))
			}
			entry = &stack{name: name}
			stacks[key] = entry
			order = append(order, key)
		}
		entry.files = append(entry.files, *file)
	}

	result := make([]AudioBookInfo, 0, len(order))
	for _, directory := range order {
		entry := stacks[directory]
		files := append([]AudioBookFileInfo(nil), entry.files...)
		sort.SliceStable(files, func(left, right int) bool {
			return CompareAudioBookFileInfo(&files[left], &files[right]) < 0
		})
		parsedName, err := ParseAudioBookName(entry.name, opts)
		if err != nil {
			return nil, err
		}
		files, extras, alternates := splitAudioBookFiles(files, parsedName)
		result = append(result, AudioBookInfo{
			Name:              parsedName.Name,
			Year:              parsedName.Year,
			Files:             files,
			Extras:            extras,
			AlternateVersions: alternates,
		})
	}
	return result, nil
}

func splitAudioBookFiles(files []AudioBookFileInfo, name AudioBookNameResult) ([]AudioBookFileInfo, []AudioBookFileInfo, []AudioBookFileInfo) {
	hasNumbered := false
	for _, file := range files {
		if file.ChapterNumber != nil || file.PartNumber != nil {
			hasNumbered = true
			break
		}
	}
	type numberKey struct{ chapter, part int }
	const noNumber = -1
	groups := make(map[numberKey][]int)
	keys := make([]numberKey, 0, len(files))
	for index, file := range files {
		key := numberKey{chapter: noNumber, part: noNumber}
		if file.ChapterNumber != nil {
			key.chapter = *file.ChapterNumber
		}
		if file.PartNumber != nil {
			key.part = *file.PartNumber
		}
		if _, exists := groups[key]; !exists {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], index)
	}
	keep := make([]bool, len(files))
	for index := range keep {
		keep[index] = true
	}
	extras, alternates := make([]AudioBookFileInfo, 0), make([]AudioBookFileInfo, 0)
	dotted := strings.ReplaceAll(name.Name, " ", ".")
	for _, key := range keys {
		indices := groups[key]
		if key.chapter == noNumber && key.part == noNumber {
			if len(indices) <= 1 && !hasNumbered {
				continue
			}
			extraIndices, alternateIndices := make([]int, 0), make([]int, 0)
			for _, index := range indices {
				stem := strings.TrimSuffix(fileName(files[index].Path), extension(files[index].Path))
				if strings.EqualFold(stem, "audiobook") || strings.Contains(strings.ToLower(stem), strings.ToLower(name.Name)) || strings.Contains(strings.ToLower(stem), strings.ToLower(dotted)) {
					alternateIndices = append(alternateIndices, index)
				} else {
					extraIndices = append(extraIndices, index)
				}
			}
			sortAudioBookIndices(extraIndices, files)
			sortAudioBookIndices(alternateIndices, files)
			for _, index := range extraIndices {
				keep[index] = false
				extras = append(extras, files[index])
			}
			if len(alternateIndices) > 0 {
				main, exactTitle := alternateIndices[0], false
				for _, index := range alternateIndices {
					stem := strings.TrimSuffix(fileName(files[index].Path), extension(files[index].Path))
					if strings.EqualFold(stem, name.Name) {
						main, exactTitle = index, true
						break
					}
				}
				if !exactTitle {
					for _, index := range alternateIndices {
						stem := strings.TrimSuffix(fileName(files[index].Path), extension(files[index].Path))
						if strings.EqualFold(stem, "audiobook") {
							main = index
							break
						}
					}
				}
				for _, index := range alternateIndices {
					if index != main {
						keep[index] = false
						alternates = append(alternates, files[index])
					}
				}
			}
			continue
		}
		if len(indices) > 1 {
			sortAudioBookIndices(indices, files)
			for _, index := range indices[1:] {
				keep[index] = false
				alternates = append(alternates, files[index])
			}
		}
	}
	kept := make([]AudioBookFileInfo, 0, len(files))
	for index, file := range files {
		if keep[index] {
			kept = append(kept, file)
		}
	}
	return kept, extras, alternates
}

func sortAudioBookIndices(indices []int, files []AudioBookFileInfo) {
	sort.SliceStable(indices, func(left, right int) bool {
		leftFile, rightFile := files[indices[left]], files[indices[right]]
		if comparison := compareCurrentCulture(leftFile.Container, rightFile.Container); comparison != 0 {
			return comparison < 0
		}
		return compareCurrentCulture(leftFile.Path, rightFile.Path) < 0
	})
}

func compareCurrentCulture(left, right string) int {
	normalize := func(value string) string {
		return strings.NewReplacer("é", "e", "É", "E", "è", "e", "ê", "e", "ë", "e").Replace(value)
	}
	left, right = strings.ToLower(normalize(left)), strings.ToLower(normalize(right))
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func parsePointer(value string) *int {
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed < math.MinInt32 || parsed > math.MaxInt32 {
		return nil
	}
	result := int(parsed)
	return &result
}

type BookResult struct {
	Name        *string
	SeriesName  *string
	Index       *int
	ParentIndex *int
	Year        *int
}

var (
	bookSeriesIndex = regexp.MustCompile(`^(.+?)(?:\s\(([0-9]{4})\))?\s#([0-9]+)(?:\.0)?(?:\s\(of\s[0-9]+\))?(?:\s\(([0-9]{4})\))?$`)
	bookTitleSeries = regexp.MustCompile(`^(.+?)\s\((.+?),\s#([0-9]+)\)(?:\.0)?(?:\s\(([0-9]{4})\))?$`)
	bookNumberTitle = regexp.MustCompile(`^([0-9]+)(?:\.0)?\s-\s(.+?)(?:\s\(([0-9]{4})\))?$`)
	bookYear        = regexp.MustCompile(`^(.*)\(([0-9]{4})\)`)
	bookComic       = regexp.MustCompile(`^(.+?)(?:\sv([0-9]+))?(?:\sc([0-9]+))?$`)
)

func ParseBook(name string) BookResult {
	if match := bookSeriesIndex.FindStringSubmatch(name); match != nil {
		result := BookResult{SeriesName: stringPointer(strings.TrimSpace(match[1]))}
		applyBookComic(&result)
		result.Index = parsePointer(match[3])
		if match[4] != "" {
			result.Year = parsePointer(match[4])
		}
		return result
	}
	if match := bookTitleSeries.FindStringSubmatch(name); match != nil {
		result := BookResult{Name: stringPointer(strings.TrimSpace(match[1])), SeriesName: stringPointer(strings.TrimSpace(match[2]))}
		applyBookComic(&result)
		result.Index = parsePointer(match[3])
		if match[4] != "" {
			result.Year = parsePointer(match[4])
		}
		return result
	}
	if match := bookNumberTitle.FindStringSubmatch(name); match != nil {
		result := BookResult{Name: stringPointer(strings.TrimSpace(match[2]))}
		applyBookComic(&result)
		result.Index = parsePointer(match[1])
		if match[3] != "" {
			result.Year = parsePointer(match[3])
		}
		return result
	}
	result := BookResult{Name: stringPointer(strings.TrimSpace(name))}
	if match := bookYear.FindStringSubmatch(name); match != nil {
		result.Name, result.Year = stringPointer(strings.TrimSpace(match[1])), parsePointer(match[2])
	}
	applyBookComic(&result)
	return result
}

func applyBookComic(result *BookResult) {
	if result.Name == nil {
		return
	}
	if match := bookComic.FindStringSubmatch(strings.TrimSpace(*result.Name)); match != nil {
		if match[2] != "" {
			result.ParentIndex = parsePointer(match[2])
		}
		if match[3] != "" {
			result.Index = parsePointer(match[3])
		}
	}
}

func stringPointer(value string) *string { return &value }

type ExternalFileInfo struct {
	Path              string
	Language          *string
	Title             *string
	IsDefault         bool
	IsForced          bool
	IsHearingImpaired bool
}

// ExternalLanguage and LanguageLookup port ExternalPathParser's localization seam.
type (
	ExternalLanguage struct{ Name, ThreeLetterISO string }
	LanguageLookup   func(token string) (ExternalLanguage, bool)
)

func ParseExternalSubtitle(path, tokens string, opts NamingOptions) *ExternalFileInfo {
	return ParseExternalSubtitleWithLanguageLookup(path, tokens, opts, defaultLanguageLookup)
}

func ParseExternalAudio(path, tokens string, opts NamingOptions) *ExternalFileInfo {
	return ParseExternalAudioWithLanguageLookup(path, tokens, opts, defaultLanguageLookup)
}

func ParseExternalLyric(path, tokens string, opts NamingOptions) *ExternalFileInfo {
	return ParseExternalLyricWithLanguageLookup(path, tokens, opts, defaultLanguageLookup)
}

func ParseExternalSubtitleWithLanguageLookup(path, tokens string, opts NamingOptions, lookup LanguageLookup) *ExternalFileInfo {
	if path == "" || !inFold(opts.SubtitleFileExtensions, extension(path)) {
		return nil
	}
	return parseExternalWithLanguageLookup(path, tokens, opts, lookup)
}

func ParseExternalAudioWithLanguageLookup(path, tokens string, opts NamingOptions, lookup LanguageLookup) *ExternalFileInfo {
	if path == "" || !inFold(opts.AudioFileExtensions, extension(path)) {
		return nil
	}
	return parseExternalWithLanguageLookup(path, tokens, opts, lookup)
}

func ParseExternalLyricWithLanguageLookup(path, tokens string, opts NamingOptions, lookup LanguageLookup) *ExternalFileInfo {
	if path == "" || !inFold(opts.LyricFileExtensions, extension(path)) {
		return nil
	}
	return parseExternalWithLanguageLookup(path, tokens, opts, lookup)
}

// parseExternalWithLanguageLookup ports ExternalPathParser.ParseFile's
// delimiter-by-delimiter pass order (ExternalPathParser.cs:61-128).
func parseExternalWithLanguageLookup(path, tokens string, opts NamingOptions, lookup LanguageLookup) *ExternalFileInfo {
	result := &ExternalFileInfo{Path: path}
	extra := tokens
	for _, delimiter := range opts.MediaFlagDelimiters {
		languageString, titles := extra, make([]string, 0, 2)
		for len(languageString) > 0 {
			index := strings.LastIndex(languageString, string(delimiter))
			if index < 0 {
				break
			}
			current, token := languageString[index:], languageString[index+len(string(delimiter)):]
			consumed := false
			if containsFold(opts.MediaDefaultFlags, token) {
				result.IsDefault = true
				consumed = true
			} else if containsFold(opts.MediaForcedFlags, token) {
				result.IsForced = true
				consumed = true
			} else if language, ok := lookup(token); ok {
				code := language.ThreeLetterISO
				if strings.Contains(language.Name, "-") {
					code = language.Name
				}
				if result.Language == nil {
					result.Language = stringPointer(code)
					consumed = true
				} else if *result.Language == "hin" {
					result.IsHearingImpaired = true
					result.Language = stringPointer(code)
					consumed = true
				} else if inFold(opts.MediaHearingImpairedFlags, token) {
					result.IsHearingImpaired = true
					consumed = true
				} else {
					titles = append([]string{token}, titles...)
				}
			} else if inFold(opts.MediaHearingImpairedFlags, token) {
				result.IsHearingImpaired = true
				consumed = true
			} else {
				titles = append([]string{token}, titles...)
			}
			if consumed {
				extra = removeFoldAll(extra, current)
			}
			languageString = languageString[:index]
		}
		if len(titles) > 0 {
			result.Title = stringPointer(strings.Join(titles, string(delimiter)))
		} else {
			result.Title = nil
		}
	}
	return result
}

func defaultLanguageLookup(token string) (ExternalLanguage, bool) {
	code, ok := isoLanguage(token)
	return ExternalLanguage{ThreeLetterISO: code}, ok
}

func containsFold(values []string, value string) bool {
	for _, candidate := range values {
		if strings.Contains(strings.ToLower(value), strings.ToLower(candidate)) {
			return true
		}
	}
	return false
}

func isoLanguage(value string) (string, bool) {
	// Jellyfin exposes legacy bibliographic aliases for these language infos.
	switch strings.ToLower(value) {
	case "nl", "nld", "dut", "dutch":
		return "nld", true
	case "fr", "fra", "fre", "french":
		return "fre", true
	case "de", "deu", "ger", "german":
		return "ger", true
	case "zh", "zho", "chi", "chinese":
		return "chi", true
	}
	if code, ok := lookupPinnedISOLanguage(value); ok {
		return code, true
	}
	tag, err := language.Parse(value)
	if err != nil {
		return "", false
	}
	base, _ := tag.Base()
	code := base.ISO3()
	if code == "und" {
		return "", false
	}
	return code, true
}
