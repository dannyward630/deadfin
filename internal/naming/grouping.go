// SPDX-License-Identifier: GPL-2.0-only

package naming

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dlclark/regexp2"
)

type VideoInfo struct {
	Name              string
	Year              *int
	Files             []VideoFileInfo
	AlternateVersions []VideoInfo
	ExtraType         ExtraType
}

type FileStack struct {
	Name             string
	Files            []string
	IsDirectoryStack bool
}

func (stack FileStack) ContainsFile(path string, isDirectory bool) bool {
	if stack.IsDirectoryStack != isDirectory {
		return false
	}
	for _, file := range stack.Files {
		if strings.EqualFold(file, path) {
			return true
		}
	}
	return false
}

func ResolveStacks(paths []string, opts NamingOptions) ([]FileStack, error) {
	files := make([]VideoFileInfo, len(paths))
	for index, path := range paths {
		files[index].Path = path
	}
	return ResolveVideoInfoStacks(files, opts)
}

// ResolveVideoInfoStacks ports StackResolver.Resolve for resolved video
// metadata. Directory entries are stack candidates regardless of extension;
// files remain subject to the upstream video/stub extension filter.
func ResolveVideoInfoStacks(files []VideoFileInfo, opts NamingOptions) ([]FileStack, error) {
	if !defaultStackRules(opts.VideoFileStackingRules) {
		for _, rule := range opts.VideoFileStackingRules {
			if _, err := regexp2.Compile(rule.Token, regexp2.IgnoreCase); err != nil {
				return nil, err
			}
		}
	}
	sorted := append([]VideoFileInfo(nil), files...)
	sort.SliceStable(sorted, func(left, right int) bool {
		return sorted[left].Path < sorted[right].Path
	})
	type stackParts struct {
		name, partType string
		numeric        bool
		directory      bool
		parts          map[string]string
		order          []string
	}
	stacks := make(map[string]*stackParts)
	keys := make([]string, 0, len(sorted))
	for _, file := range sorted {
		if !file.IsDirectory && !inFold(opts.VideoFileExtensions, extension(file.Path)) && !inFold(opts.StubFileExtensions, extension(file.Path)) {
			continue
		}
		name, partType, part, numeric, ok := parseStack(fileName(file.Path), opts)
		if !ok {
			continue
		}
		// StackResolver uses an ordinal outer dictionary and a case-insensitive
		// per-stack part dictionary (StackResolver.cs:77-120).
		stack := stacks[name]
		if stack == nil {
			stack = &stackParts{
				name:      name,
				partType:  partType,
				numeric:   numeric,
				directory: file.IsDirectory,
				parts:     make(map[string]string),
			}
			stacks[name] = stack
			keys = append(keys, name)
		}
		if stack.directory != file.IsDirectory || !strings.EqualFold(stack.partType, partType) || stack.numeric != numeric {
			continue
		}
		if _, exists := stack.parts[strings.ToLower(part)]; exists {
			if alternateName, alternateType, alternatePart, _, found := parseStackAvoidingParts(fileName(file.Path), opts, stack.parts); found && alternateName == name && strings.EqualFold(alternateType, stack.partType) {
				part = alternatePart
			}
		}
		partKey := strings.ToLower(part)
		if _, exists := stack.parts[partKey]; exists {
			continue
		}
		stack.parts[partKey] = file.Path
		stack.order = append(stack.order, partKey)
	}
	result := make([]FileStack, 0, len(keys))
	for _, key := range keys {
		stack := stacks[key]
		if len(stack.parts) < 2 {
			continue
		}
		// Inputs were ordinal-sorted before discovery; StackResolver enumerates its
		// insertion-ordered part dictionary without re-sorting numeric labels.
		paths := make([]string, len(stack.order))
		for index, part := range stack.order {
			paths[index] = stack.parts[part]
		}
		result = append(result, FileStack{Name: stack.name, Files: paths, IsDirectoryStack: stack.directory})
	}
	return result, nil
}

// parseStack explicitly replaces NamingOptions.cs:142's lookbehind: the marker
// must be preceded by a separator or by ], ), or }.
func parseStack(name string, opts NamingOptions) (stackName, partType, part string, numeric, ok bool) {
	for _, rule := range opts.VideoFileStackingRules {
		filename, candidateType, candidate, found := parseStackRule(name, rule)
		if found {
			return filename, candidateType, candidate, rule.IsNumerical, true
		}
	}
	if !defaultStackRules(opts.VideoFileStackingRules) {
		return "", "", "", false, false
	}
	stem := withoutExtension(name)
	lower := strings.ToLower(stem)
	markers := []string{"disc", "disk", "part", "dvd", "cd", "pt"}
	for _, marker := range markers {
		for index := strings.LastIndex(lower, marker); index >= 0; index = strings.LastIndex(lower[:index], marker) {
			boundary := index - 1
			if boundary >= 0 && (lower[boundary] == '(' || lower[boundary] == '[') {
				boundary--
			}
			if boundary < 0 || !stackBoundary(rune(lower[boundary])) {
				continue
			}
			rest := lower[index+len(marker):]
			trimmed := strings.TrimLeft(rest, " _.-([{")
			if trimmed == "" {
				continue
			}
			candidate := trimmed[:1]
			if candidate[0] >= '0' && candidate[0] <= '9' {
				end := 0
				for end < len(trimmed) && trimmed[end] >= '0' && trimmed[end] <= '9' {
					end++
				}
				candidate = trimmed[:end]
				if strings.Trim(trimmed[end:], ")]}") != "" {
					continue
				}
				return strings.TrimRight(stem[:index], " _.-([{"), marker, candidate, true, true
			}
			if candidate[0] >= 'a' && candidate[0] <= 'd' && strings.Trim(trimmed[1:], ")]}") == "" {
				return strings.TrimRight(stem[:index], " _.-([{"), marker, candidate, false, true
			}
		}
	}
	return "", "", "", false, false
}

func parseStackAvoidingParts(name string, opts NamingOptions, parts map[string]string) (stackName, partType, part string, numeric, ok bool) {
	for _, rule := range opts.VideoFileStackingRules {
		filename, candidateType, candidate, found := parseStackRule(name, rule)
		if found && parts[strings.ToLower(candidate)] == "" {
			return filename, candidateType, candidate, rule.IsNumerical, true
		}
	}
	return "", "", "", false, false
}

// FileStackRule.cs executes configured expressions using .NET Regex; regexp2
// retains lookbehind support while RE2 remains the allocation-light fast path.
func parseStackRule(name string, rule FileStackRule) (string, string, string, bool) {
	if pattern, err := regexp.Compile("(?i)" + goEpisodeExpression(rule.Token)); err == nil {
		match := pattern.FindStringSubmatch(name)
		filenameIndex, partTypeIndex, numberIndex := pattern.SubexpIndex("filename"), pattern.SubexpIndex("parttype"), pattern.SubexpIndex("number")
		if match != nil && filenameIndex >= 0 && numberIndex >= 0 && match[filenameIndex] != "" && match[numberIndex] != "" {
			partType := "unknown"
			if partTypeIndex >= 0 {
				partType = match[partTypeIndex]
			}
			return match[filenameIndex], partType, match[numberIndex], true
		}
		return "", "", "", false
	}
	pattern, err := regexp2.Compile(rule.Token, regexp2.IgnoreCase)
	if err != nil {
		return "", "", "", false
	}
	match, err := pattern.FindStringMatch(name)
	if err != nil || match == nil {
		return "", "", "", false
	}
	filename, number := match.GroupByName("filename").String(), match.GroupByName("number").String()
	if filename == "" || number == "" {
		return "", "", "", false
	}
	partType := "unknown"
	if group := match.GroupByName("parttype"); group != nil && group.String() != "" {
		partType = group.String()
	}
	return filename, partType, number, true
}

func defaultStackRules(rules []FileStackRule) bool {
	defaults := DefaultOptions().VideoFileStackingRules
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

func stackBoundary(value rune) bool {
	switch value {
	case ' ', '_', '.', '-', ']', ')', '}':
		return true
	}
	return false
}

// ResolveVideoList resolves file paths before grouping them. Call
// ResolveVideoInfoList when directory/file metadata is already available.
func ResolveVideoList(paths []string, opts NamingOptions) ([]VideoInfo, error) {
	videos, err := resolveVideoPaths(paths, opts)
	if err != nil {
		return nil, err
	}
	return ResolveVideoInfoList(videos, opts, "")
}

func ResolveVideoListForCollection(paths []string, opts NamingOptions, collection string) ([]VideoInfo, error) {
	return ResolveVideoListMultiVersion(paths, opts, collection, true)
}

// ResolveVideoListMultiVersion ports VideoListResolver.Resolve with its
// supportMultiVersion argument. Without it, alternate versions stay separate
// videos.
func ResolveVideoListMultiVersion(paths []string, opts NamingOptions, collection string, supportMultiVersion bool) ([]VideoInfo, error) {
	videos, err := resolveVideoPaths(paths, opts)
	if err != nil {
		return nil, err
	}
	return resolveVideoInfoList(videos, opts, collection, supportMultiVersion)
}

func resolveVideoPaths(paths []string, opts NamingOptions) ([]VideoFileInfo, error) {
	videos := make([]VideoFileInfo, 0, len(paths))
	for _, path := range paths {
		info, err := ResolveVideoWithError(path, opts)
		if err != nil {
			return nil, err
		}
		if info != nil {
			videos = append(videos, *info)
		}
	}
	return videos, nil
}

// ResolveVideoInfoList ports VideoListResolver.Resolve for already-resolved
// video metadata. It preserves supplied directory flags, excludes extras from
// stack discovery, then appends extras after multi-version grouping.
func ResolveVideoInfoList(videos []VideoFileInfo, opts NamingOptions, collectionType string) ([]VideoInfo, error) {
	return resolveVideoInfoList(videos, opts, collectionType, true)
}

func resolveVideoInfoList(videos []VideoFileInfo, opts NamingOptions, collectionType string, supportMultiVersion bool) ([]VideoInfo, error) {
	if _, err := ResolveExtraWithError("", "", opts); err != nil {
		return nil, err
	}
	nonExtras := make([]VideoFileInfo, 0, len(videos))
	for _, video := range videos {
		if video.ExtraType == "" {
			nonExtras = append(nonExtras, video)
		}
	}
	stacks, err := ResolveVideoInfoStacks(nonExtras, opts)
	if err != nil {
		return nil, err
	}
	list := make([]VideoInfo, 0, len(videos))
	for _, stack := range stacks {
		cleanedStackName := CleanDateTime(stack.Name, opts)
		info := VideoInfo{Name: cleanedStackName.Name, Year: cleanedStackName.Year}
		for _, path := range stack.Files {
			var resolved *VideoFileInfo
			var resolveErr error
			if stack.IsDirectoryStack {
				resolved, resolveErr = ResolveVideoDirectoryWithError(path, opts, true, "")
			} else {
				resolved, resolveErr = ResolveVideoWithError(path, opts)
			}
			if resolveErr != nil {
				return nil, resolveErr
			}
			if resolved != nil {
				info.Files = append(info.Files, *resolved)
			}
		}
		if len(info.Files) != 0 {
			info.Year = info.Files[0].Year
			list = append(list, info)
		}
	}

	extras := make([]VideoInfo, 0)
	for _, video := range videos {
		if videoIsStacked(video, stacks) {
			continue
		}
		info := VideoInfo{Name: video.Name, Year: video.Year, Files: []VideoFileInfo{video}, ExtraType: video.ExtraType}
		if video.ExtraType == "" {
			list = append(list, info)
		} else {
			extras = append(extras, info)
		}
	}
	if !supportMultiVersion {
		return append(list, extras...), nil
	}
	if strings.EqualFold(collectionType, "tvshows") {
		var err error
		list, err = groupEpisodeAlternateVersions(list, opts)
		if err != nil {
			return nil, err
		}
	} else {
		list = groupAlternateVersions(list, opts)
	}
	return append(list, extras...), nil
}

func videoIsStacked(video VideoFileInfo, stacks []FileStack) bool {
	for _, stack := range stacks {
		if stack.IsDirectoryStack != video.IsDirectory {
			continue
		}
		for _, path := range stack.Files {
			if strings.EqualFold(path, video.Path) {
				return true
			}
		}
	}
	return false
}

func groupEpisodeAlternateVersions(videos []VideoInfo, opts NamingOptions) ([]VideoInfo, error) {
	if len(videos) < 2 {
		return videos, nil
	}
	groups := make(map[string][]VideoInfo)
	order := make([]string, 0, len(videos))
	result := make([]VideoInfo, 0, len(videos))
	for _, video := range videos {
		if len(video.Files) == 0 {
			result = append(result, video)
			continue
		}
		episode, err := ParseEpisode(video.Files[0].Path, false, opts)
		if err != nil {
			return nil, err
		}
		key := ""
		if episode != nil && episode.ByDate && episode.Year != nil && episode.Month != nil && episode.Day != nil {
			key = "D" + strconv.Itoa(*episode.Year) + strconv.Itoa(*episode.Month + 100)[1:] + strconv.Itoa(*episode.Day + 100)[1:]
		} else if episode != nil && episode.EpisodeNumber != nil {
			season := 0
			if episode.SeasonNumber != nil {
				season = *episode.SeasonNumber
			}
			key = "S" + strconv.Itoa(season) + "E" + strconv.Itoa(*episode.EpisodeNumber)
		}
		if key == "" {
			result = append(result, video)
			continue
		}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], video)
	}
	for _, key := range order {
		group := groups[key]
		if len(group) == 1 {
			result = append(result, group[0])
			continue
		}
		result = append(result, organizeAlternateVersions(group))
	}
	return result, nil
}

func groupAlternateVersions(videos []VideoInfo, opts NamingOptions) []VideoInfo {
	if len(videos) < 2 || len(videos[0].Files) == 0 {
		return videos
	}
	folder := directoryName(videos[0].Files[0].Path)
	if len(folder) <= 1 {
		return videos
	}
	for _, video := range videos {
		if len(video.Files) == 0 || !sameYear(video.Year, videos[0].Year) || !eligibleVersion(folder, videoFileStem(video.Files[0]), opts) {
			return videos
		}
	}
	primary := -1
	for index, video := range videos {
		if videoFileStem(video.Files[0]) == folder {
			primary = index
		}
	}
	if primary >= 0 {
		result := organizeAlternateVersionsWithPrimary(videos, primary)
		result.Name = folder
		return []VideoInfo{result}
	}
	return []VideoInfo{organizeAlternateVersions(videos)}
}

func organizeAlternateVersions(videos []VideoInfo) VideoInfo {
	return organizeAlternateVersionsWithPrimary(videos, -1)
}

func organizeAlternateVersionsWithPrimary(videos []VideoInfo, primaryOverride int) VideoInfo {
	ordered := append([]VideoInfo(nil), videos...)
	sort.SliceStable(ordered, func(left, right int) bool {
		leftName, rightName := strings.ToLower(videoFileStem(ordered[left].Files[0])), strings.ToLower(videoFileStem(ordered[right].Files[0]))
		leftResolution, rightResolution := resolutionDigits(leftName), resolutionDigits(rightName)
		if leftResolution != "" && rightResolution == "" {
			return true
		}
		if leftResolution == "" && rightResolution != "" {
			return false
		}
		if leftResolution != "" && leftResolution != rightResolution {
			return compareDecimalStrings(leftResolution, rightResolution) > 0
		}
		return alternateLess(leftName, rightName)
	})
	primary := primaryOverride
	if primaryOverride >= 0 {
		primary = -1
		primaryPath := videos[primaryOverride].Files[0].Path
		for index, video := range ordered {
			if video.Files[0].Path == primaryPath && video.Files[0].IsDirectory == videos[primaryOverride].Files[0].IsDirectory {
				primary = index
				break
			}
		}
	}
	if primary < 0 {
		for index, video := range ordered {
			if len(video.Files) > 1 {
				primary = index
				break
			}
		}
	}
	if primary < 0 {
		primary = 0
	}
	result := ordered[primary]
	result.AlternateVersions = make([]VideoInfo, 0, len(ordered)-1)
	for index, video := range ordered {
		if index != primary {
			result.AlternateVersions = append(result.AlternateVersions, video)
		}
	}
	return result
}

func videoFileStem(video VideoFileInfo) string {
	if video.IsDirectory {
		return fileName(video.Path)
	}
	return withoutExtension(video.Path)
}

func alternateLess(left, right string) bool {
	for left != "" && right != "" {
		if left[0] >= '0' && left[0] <= '9' && right[0] >= '0' && right[0] <= '9' {
			leftEnd, rightEnd := 0, 0
			for leftEnd < len(left) && left[leftEnd] >= '0' && left[leftEnd] <= '9' {
				leftEnd++
			}
			for rightEnd < len(right) && right[rightEnd] >= '0' && right[rightEnd] <= '9' {
				rightEnd++
			}
			if comparison := compareDecimalStrings(strings.TrimLeft(left[:leftEnd], "0"), strings.TrimLeft(right[:rightEnd], "0")); comparison != 0 {
				return comparison < 0
			}
			left, right = left[leftEnd:], right[rightEnd:]
			continue
		}
		if left[0] != right[0] {
			return left[0] < right[0]
		}
		left, right = left[1:], right[1:]
	}
	return left == "" && right != ""
}

// resolutionDigits ports VideoListResolver's [0-9]{2}[0-9]+[ip] matcher
// and retains arbitrary precision for NumericOrdering.
func resolutionDigits(name string) string {
	for start := 0; start < len(name); {
		if name[start] < '0' || name[start] > '9' {
			start++
			continue
		}
		end := start
		for end < len(name) && name[end] >= '0' && name[end] <= '9' {
			end++
		}
		if end-start >= 3 && end < len(name) && (name[end] == 'i' || name[end] == 'p') {
			return strings.TrimLeft(name[start:end], "0")
		}
		start = end
	}
	return ""
}

func compareDecimalStrings(left, right string) int {
	if left == "" {
		left = "0"
	}
	if right == "" {
		right = "0"
	}
	if len(left) != len(right) {
		if len(left) < len(right) {
			return -1
		}
		return 1
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func sameYear(left, right *int) bool {
	leftYear, rightYear := -1, -1
	if left != nil {
		leftYear = *left
	}
	if right != nil {
		rightYear = *right
	}
	return leftYear == rightYear
}

func eligibleVersion(folder, filename string, opts NamingOptions) bool {
	if !strings.HasPrefix(strings.ToLower(filename), strings.ToLower(folder)) {
		return false
	}
	remainder := strings.TrimSpace(filename[len(folder):])
	if cleaned, ok := CleanString(remainder, opts); ok {
		remainder = strings.TrimSpace(cleaned)
	}
	return remainder == "" || strings.HasPrefix(remainder, "-") || strings.HasPrefix(remainder, "_") || strings.HasPrefix(remainder, ".") || (strings.HasPrefix(remainder, "[") && strings.Contains(remainder, "]"))
}
