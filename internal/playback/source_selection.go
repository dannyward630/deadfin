// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"context"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// LanguageNormalizer canonicalizes a persisted ISO language alias without
// exposing the localization corpus to playback selection.
type LanguageNormalizer interface {
	NormalizeLanguage(string) string
}

// SourceUserData contains the persisted selection and resume facts consumed by
// media-source selection.
type SourceUserData struct {
	AudioStreamIndex    *int
	SubtitleStreamIndex *int
	PositionTicks       int64
	LastPlayed          *time.Time
}

// SourceMediaKind identifies the media behavior applied by source selection.
type SourceMediaKind uint8

const (
	SourceMediaOther SourceMediaKind = iota
	SourceMediaVideo
	SourceMediaAudio
)

// SourceSelection supplies all user and item facts needed to select streams
// without coupling playback to persistence or item models.
type SourceSelection struct {
	Configuration    dto.UserConfiguration
	MediaKind        SourceMediaKind
	OriginalLanguage string
	PrimaryItem      bool
	ItemUserData     *SourceUserData
	VersionUserData  map[string]SourceUserData
	// SeriesSelection is the user's most recent stream choice on an episode of
	// the same series. It is nil for items outside a series.
	SeriesSelection *SeriesStreamSelection
}

// SeriesStreamSelection carries the stream indexes saved for the user's most
// recently played episode of a series, with that episode's streams. Indexes are
// per file, so selection matches the streams they name against each new
// episode by language, title, flags, and codec (divergence D-36).
type SeriesStreamSelection struct {
	Streams             []MediaStream
	AudioStreamIndex    *int
	SubtitleStreamIndex *int
}

// SourceSelectionReader resolves user selection facts for a playback source
// graph. A false result means that no user selection is available.
type SourceSelectionReader interface {
	SourceSelection(context.Context, dto.UUID, dto.UUID, []dto.UUID) (SourceSelection, bool, error)
}

// ApplyUserSourceSelection applies user stream preferences and persisted source
// selections in place, returning sources in their selected playback order.
func ApplyUserSourceSelection(sources []MediaSourceInfo, selection SourceSelection, languages LanguageNormalizer) []MediaSourceInfo {
	switch selection.MediaKind {
	case SourceMediaVideo:
		audioPreferences := parseLanguagePreferences(selection.Configuration.AudioLanguagePreference, languages)
		preferOriginalAudio := audioLanguagePreferenceIsOriginal(selection.Configuration.AudioLanguagePreference)
		if preferOriginalAudio {
			audioPreferences = parseLanguagePreferences(&selection.OriginalLanguage, languages)
		}
		subtitlePreferences := parseLanguagePreferences(selection.Configuration.SubtitleLanguagePreference, languages)
		for index := range sources {
			var remembered *SourceUserData
			if data, found := selection.VersionUserData[sources[index].ID]; found {
				remembered = &data
			} else if selection.ItemUserData != nil {
				remembered = selection.ItemUserData
			}
			selectSourceStreams(&sources[index], remembered, selection.SeriesSelection, audioPreferences, subtitlePreferences, selection.Configuration, preferOriginalAudio, languages)
		}
		return reorderResumableAlternate(sources, selection)
	case SourceMediaAudio:
		ApplyFirstAudioStreamDefaults(sources)
	}
	return sources
}

// ApplyFirstAudioStreamDefaults selects each source's first audio stream and
// clears its subtitle default.
func ApplyFirstAudioStreamDefaults(sources []MediaSourceInfo) {
	for sourceIndex := range sources {
		source := &sources[sourceIndex]
		source.DefaultSubtitleStreamIndex = nil
		for _, stream := range source.MediaStreams {
			if stream.Type == StreamTypeAudio {
				index := stream.Index
				source.DefaultAudioStreamIndex = &index
				break
			}
		}
	}
}

func reorderResumableAlternate(sources []MediaSourceInfo, selection SourceSelection) []MediaSourceInfo {
	if !selection.PrimaryItem || len(sources) < 2 {
		return sources
	}

	best, found := 0, false
	var bestLastPlayed time.Time
	for index := range sources {
		data, exists := selection.VersionUserData[sources[index].ID]
		if !exists || data.PositionTicks <= 0 {
			continue
		}
		lastPlayed := time.Time{}
		if data.LastPlayed != nil {
			lastPlayed = *data.LastPlayed
		}
		if !found || lastPlayed.After(bestLastPlayed) {
			best, bestLastPlayed, found = index, lastPlayed, true
		}
	}
	if !found || best == 0 {
		return sources
	}

	selected := sources[best]
	copy(sources[1:best+1], sources[:best])
	sources[0] = selected
	return sources
}

// selectSourceStreams ports MediaSourceManager.SetDefaultAudioStreamIndex and
// SetDefaultSubtitleStreamIndex for one source. A remembered audio choice
// replaces the preference choice before the subtitle default is chosen, so
// SubtitleMode Smart compares against the audio stream that will play.
// AudioIndexDefault provenance is set only when the selected stream carries
// IsDefault, so a source whose audio streams all lack IsDefault does not
// constrain the candidate set to an empty set and the profile can select a
// supported codec from the remaining candidates.
func selectSourceStreams(source *MediaSourceInfo, remembered *SourceUserData, series *SeriesStreamSelection, audioPreferences, subtitlePreferences []string, configuration dto.UserConfiguration, preferOriginalAudio bool, normalizer LanguageNormalizer) {
	source.DefaultAudioIndexSource = AudioIndexNone
	if audioIndex, found := preferredAudioStreamIndex(source.MediaStreams, audioPreferences, configuration.PlayDefaultAudioTrack, preferOriginalAudio, normalizer); found {
		value := audioIndex
		source.DefaultAudioStreamIndex = &value
		if len(audioPreferences) > 0 {
			source.DefaultAudioIndexSource |= AudioIndexLanguage
		}
		if configuration.PlayDefaultAudioTrack && audioStreamIsDefault(source.MediaStreams, &value) {
			source.DefaultAudioIndexSource |= AudioIndexDefault
		}
	} else {
		source.DefaultAudioStreamIndex = nil
	}
	if audioIndex, found := rememberedAudioStreamIndex(source.MediaStreams, remembered, series, configuration, normalizer); found {
		source.DefaultAudioStreamIndex = &audioIndex
		source.DefaultAudioIndexSource = AudioIndexUser
	}
	source.DefaultSubtitleStreamIndex = defaultSubtitleStreamIndex(source.MediaStreams, subtitlePreferences, configuration.SubtitleMode, source.DefaultAudioStreamIndex, normalizer)
	setSubtitleStreamScores(source.MediaStreams, subtitlePreferences, configuration.SubtitleMode, source.DefaultAudioStreamIndex, normalizer)
	if subtitleIndex, found := rememberedSubtitleStreamIndex(source.MediaStreams, remembered, series, configuration, normalizer); found {
		source.DefaultSubtitleStreamIndex = &subtitleIndex
	}
}

// parseLanguagePreferences treats no preference as the selector's wildcard.
// Preferences are comma-separated; empty entries are ignored without copying
// the retained string segments.
func parseLanguagePreferences(value *string, normalizer LanguageNormalizer) []string {
	if value == nil {
		return nil
	}
	remaining := *value
	var preferences []string
	for {
		separator := strings.IndexByte(remaining, ',')
		part := remaining
		if separator >= 0 {
			part = remaining[:separator]
		}
		if part = strings.TrimSpace(part); part != "" {
			preferences = append(preferences, normalizeLanguage(normalizer, part))
		}
		if separator < 0 {
			return preferences
		}
		remaining = remaining[separator+1:]
	}
}

func normalizeLanguage(normalizer LanguageNormalizer, value string) string {
	if normalizer == nil {
		return value
	}
	return normalizer.NormalizeLanguage(value)
}

func audioLanguagePreferenceIsOriginal(preference *string) bool {
	return preference != nil && strings.EqualFold(strings.TrimSpace(*preference), "OriginalLanguage")
}

func defaultAudioStreamIndex(streams []MediaStream, preferences []string, preferDefaultTrack bool, normalizer LanguageNormalizer) (int, bool) {
	bestScore, bestIndex, foundBest := 0, 0, false
	defaultScore, defaultIndex, foundDefault := 0, 0, false
	for _, stream := range streams {
		if stream.Type != StreamTypeAudio {
			continue
		}
		score := mediaStreamScore(stream, preferences, normalizer)
		if !foundBest || score > bestScore {
			bestScore, bestIndex, foundBest = score, stream.Index, true
		}
		if stream.IsDefault && (!foundDefault || score > defaultScore) {
			defaultScore, defaultIndex, foundDefault = score, stream.Index, true
		}
	}
	if preferDefaultTrack && foundDefault {
		return defaultIndex, true
	}
	return bestIndex, foundBest
}

func preferredAudioStreamIndex(streams []MediaStream, preferences []string, preferDefaultTrack, preferOriginal bool, normalizer LanguageNormalizer) (int, bool) {
	if preferOriginal && !preferDefaultTrack {
		if index, found := originalAudioStreamIndex(streams, preferences, normalizer); found {
			return index, true
		}
	}
	return defaultAudioStreamIndex(streams, preferences, preferDefaultTrack, normalizer)
}

func originalAudioStreamIndex(streams []MediaStream, preferences []string, normalizer LanguageNormalizer) (int, bool) {
	for _, stream := range streams {
		if stream.Type == StreamTypeAudio && stream.IsOriginal && matchesPreferredLanguage(streamLanguage(stream), preferences, normalizer) {
			return stream.Index, true
		}
	}
	return 0, false
}

type subtitleSelection uint8

const (
	subtitleSelectionDefault subtitleSelection = iota
	subtitleSelectionSmart
	subtitleSelectionAlways
)

func defaultSubtitleStreamIndex(streams []MediaStream, preferences []string, mode dto.SubtitleMode, audioIndex *int, normalizer LanguageNormalizer) *int {
	if mode == dto.SubtitleModeNone {
		return nil
	}
	var selected *MediaStream
	switch mode {
	case dto.SubtitleModeDefault:
		selected = bestSubtitleStream(streams, preferences, subtitleSelectionDefault, normalizer)
	case dto.SubtitleModeSmart:
		if !containsPreferredLanguage(audioStreamLanguage(streams, audioIndex), preferences, normalizer) {
			selected = bestSubtitleStream(streams, preferences, subtitleSelectionSmart, normalizer)
		} else {
			selected = bestForcedSubtitleStream(streams, preferences, normalizer)
		}
	case dto.SubtitleModeAlways:
		selected = bestSubtitleStream(streams, preferences, subtitleSelectionAlways, normalizer)
		if selected == nil {
			selected = bestForcedSubtitleStream(streams, preferences, normalizer)
		}
	case dto.SubtitleModeOnlyForced:
		selected = bestForcedSubtitleStream(streams, preferences, normalizer)
	}
	if selected == nil {
		return nil
	}
	value := selected.Index
	return &value
}

func setSubtitleStreamScores(streams []MediaStream, preferences []string, mode dto.SubtitleMode, audioIndex *int, normalizer LanguageNormalizer) {
	if mode == dto.SubtitleModeNone {
		return
	}

	selection := subtitleSelectionDefault
	forcedOnly := false
	switch mode {
	case dto.SubtitleModeSmart:
		if !containsPreferredLanguage(audioStreamLanguage(streams, audioIndex), preferences, normalizer) {
			selection = subtitleSelectionSmart
		} else {
			forcedOnly = true
		}
	case dto.SubtitleModeAlways:
		selection = subtitleSelectionAlways
	case dto.SubtitleModeOnlyForced:
		forcedOnly = true
	}

	for index := range streams {
		stream := &streams[index]
		if stream.Type != StreamTypeSubtitle {
			continue
		}
		if forcedOnly {
			language := streamLanguage(*stream)
			if !stream.IsForced || (!matchesPreferredLanguage(language, preferences, normalizer) && !undefinedLanguage(language)) {
				continue
			}
		} else if !eligibleSubtitleStream(*stream, preferences, selection, normalizer) {
			continue
		}
		score := mediaStreamScore(*stream, preferences, normalizer)
		stream.Score = &score
	}
}

func bestSubtitleStream(streams []MediaStream, preferences []string, selection subtitleSelection, normalizer LanguageNormalizer) *MediaStream {
	best, bestScore := -1, 0
	for index := range streams {
		stream := &streams[index]
		if stream.Type != StreamTypeSubtitle || !eligibleSubtitleStream(*stream, preferences, selection, normalizer) {
			continue
		}
		score := subtitleStreamScore(*stream, preferences, normalizer)
		if best < 0 || score > bestScore {
			best, bestScore = index, score
		}
	}
	if best < 0 {
		return nil
	}
	return &streams[best]
}

func eligibleSubtitleStream(stream MediaStream, preferences []string, selection subtitleSelection, normalizer LanguageNormalizer) bool {
	switch selection {
	case subtitleSelectionDefault:
		return stream.IsExternal || stream.IsDefault || stream.IsForced
	case subtitleSelectionSmart:
		return matchesPreferredLanguage(streamLanguage(stream), preferences, normalizer)
	case subtitleSelectionAlways:
		return !stream.IsForced && matchesPreferredLanguage(streamLanguage(stream), preferences, normalizer)
	default:
		return false
	}
}

func bestForcedSubtitleStream(streams []MediaStream, preferences []string, normalizer LanguageNormalizer) *MediaStream {
	best, bestScore := -1, 0
	for index := range streams {
		stream := &streams[index]
		language := streamLanguage(*stream)
		matches := matchesPreferredLanguage(language, preferences, normalizer)
		undefined := undefinedLanguage(language)
		if stream.Type != StreamTypeSubtitle || !stream.IsForced || (!matches && !undefined) {
			continue
		}
		score := subtitleStreamScore(*stream, preferences, normalizer)
		if matches {
			score += 1 << 8
		}
		if undefined {
			score += 1 << 7
		}
		if best < 0 || score > bestScore {
			best, bestScore = index, score
		}
	}
	if best < 0 {
		return nil
	}
	return &streams[best]
}

func mediaStreamScore(stream MediaStream, preferences []string, normalizer LanguageNormalizer) int {
	preference := languagePreferenceIndex(streamLanguage(stream), preferences, normalizer)
	score := 1
	if preference >= 0 {
		score = 101 - preference
	}
	score = extendMediaStreamScore(score, stream.IsForced)
	score = extendMediaStreamScore(score, stream.IsDefault)
	score = extendMediaStreamScore(score, stream.SupportsExternalStream)
	score = extendMediaStreamScore(score, stream.IsTextSubtitleStream())
	return extendMediaStreamScore(score, stream.IsExternal)
}

func extendMediaStreamScore(score int, flag bool) int {
	if flag {
		return score*10 + 2
	}
	return score*10 + 1
}

func subtitleStreamScore(stream MediaStream, preferences []string, normalizer LanguageNormalizer) int {
	language := streamLanguage(stream)
	score := 0
	if stream.IsExternal {
		score += 1 << 5
	}
	if stream.IsDefault {
		score += 1 << 4
	}
	if !stream.IsForced && matchesPreferredLanguage(language, preferences, normalizer) {
		score += 1 << 3
	}
	if stream.IsForced && matchesPreferredLanguage(language, preferences, normalizer) {
		score += 1 << 2
	}
	if stream.IsForced && undefinedLanguage(language) {
		score += 1 << 1
	}
	if stream.IsForced {
		score++
	}
	return score
}

func audioStreamLanguage(streams []MediaStream, index *int) string {
	if index == nil {
		return ""
	}
	for _, stream := range streams {
		if stream.Type == StreamTypeAudio && stream.Index == *index {
			return streamLanguage(stream)
		}
	}
	return ""
}

// audioStreamIsDefault reports whether the audio stream at index carries the
// IsDefault flag. A false result means the index was a fallback selection, not
// an actual source default, and must not set default-only provenance.
func audioStreamIsDefault(streams []MediaStream, index *int) bool {
	if index == nil {
		return false
	}
	for _, stream := range streams {
		if stream.Type == StreamTypeAudio && stream.Index == *index {
			return stream.IsDefault
		}
	}
	return false
}

func streamLanguage(stream MediaStream) string {
	if stream.Language == nil {
		return ""
	}
	return *stream.Language
}

func languagePreferenceIndex(language string, preferences []string, normalizer LanguageNormalizer) int {
	language = normalizeLanguage(normalizer, language)
	for index, preference := range preferences {
		if strings.EqualFold(language, preference) {
			return index
		}
	}
	return -1
}

func containsPreferredLanguage(language string, preferences []string, normalizer LanguageNormalizer) bool {
	return languagePreferenceIndex(language, preferences, normalizer) >= 0
}

func matchesPreferredLanguage(language string, preferences []string, normalizer LanguageNormalizer) bool {
	return len(preferences) == 0 || containsPreferredLanguage(language, preferences, normalizer)
}

func undefinedLanguage(language string) bool {
	return language == "" || strings.EqualFold(language, "und") || strings.EqualFold(language, "unknown") || strings.EqualFold(language, "undetermined") || strings.EqualFold(language, "mul") || strings.EqualFold(language, "zxx")
}

// rememberedAudioStreamIndex returns the item's saved audio index when it is
// still valid, else the series choice matched against these streams.
func rememberedAudioStreamIndex(streams []MediaStream, data *SourceUserData, series *SeriesStreamSelection, configuration dto.UserConfiguration, normalizer LanguageNormalizer) (int, bool) {
	if !configuration.RememberAudioSelections {
		return 0, false
	}
	if data != nil && data.AudioStreamIndex != nil && sourceHasStream(streams, StreamTypeAudio, *data.AudioStreamIndex) {
		return *data.AudioStreamIndex, true
	}
	if series == nil || series.AudioStreamIndex == nil {
		return 0, false
	}
	previous, found := streamAt(series.Streams, StreamTypeAudio, *series.AudioStreamIndex)
	if !found {
		return 0, false
	}
	return matchingSeriesStream(streams, previous, normalizer)
}

// rememberedSubtitleStreamIndex returns the item's saved subtitle index when it
// is still valid, else the series choice matched against these streams. -1 is
// an explicit choice of no subtitles.
func rememberedSubtitleStreamIndex(streams []MediaStream, data *SourceUserData, series *SeriesStreamSelection, configuration dto.UserConfiguration, normalizer LanguageNormalizer) (int, bool) {
	if !configuration.RememberSubtitleSelections || configuration.SubtitleMode == dto.SubtitleModeNone {
		return 0, false
	}
	if data != nil && data.SubtitleStreamIndex != nil && (*data.SubtitleStreamIndex == -1 || sourceHasStream(streams, StreamTypeSubtitle, *data.SubtitleStreamIndex)) {
		return *data.SubtitleStreamIndex, true
	}
	if series == nil || series.SubtitleStreamIndex == nil {
		return 0, false
	}
	if *series.SubtitleStreamIndex == -1 {
		return -1, true
	}
	previous, found := streamAt(series.Streams, StreamTypeSubtitle, *series.SubtitleStreamIndex)
	if !found {
		return 0, false
	}
	return matchingSeriesStream(streams, previous, normalizer)
}

// matchingSeriesStream finds the stream that best matches a stream chosen on
// another episode. The language must match. Among streams of that language the
// title outranks the forced flag, which outranks the codec, the channel count
// or external flag, and the default flag. Equal ranks keep stream order. No
// match leaves the preference choice in place.
func matchingSeriesStream(streams []MediaStream, previous MediaStream, normalizer LanguageNormalizer) (int, bool) {
	language := streamLanguage(previous)
	bestScore, bestIndex, found := 0, 0, false
	for _, stream := range streams {
		if stream.Type != previous.Type || !sameStreamLanguage(streamLanguage(stream), language, normalizer) {
			continue
		}
		score := 0
		if strings.EqualFold(strings.TrimSpace(optionalStreamString(stream.Title)), strings.TrimSpace(optionalStreamString(previous.Title))) {
			score += 1 << 4
		}
		if stream.IsForced == previous.IsForced {
			score += 1 << 3
		}
		if strings.EqualFold(stream.Codec, previous.Codec) {
			score += 1 << 2
		}
		if stream.Type == StreamTypeAudio && optionalStreamInt(stream.Channels) == optionalStreamInt(previous.Channels) {
			score += 1 << 1
		}
		if stream.Type == StreamTypeSubtitle && stream.IsExternal == previous.IsExternal {
			score += 1 << 1
		}
		if stream.IsDefault == previous.IsDefault {
			score++
		}
		if !found || score > bestScore {
			bestScore, bestIndex, found = score, stream.Index, true
		}
	}
	return bestIndex, found
}

func sameStreamLanguage(left, right string, normalizer LanguageNormalizer) bool {
	if undefinedLanguage(left) || undefinedLanguage(right) {
		return undefinedLanguage(left) && undefinedLanguage(right)
	}
	return strings.EqualFold(normalizeLanguage(normalizer, left), normalizeLanguage(normalizer, right))
}

func streamAt(streams []MediaStream, streamType StreamType, streamIndex int) (MediaStream, bool) {
	for _, stream := range streams {
		if stream.Type == streamType && stream.Index == streamIndex {
			return stream, true
		}
	}
	return MediaStream{}, false
}

func optionalStreamString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalStreamInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func sourceHasStream(streams []MediaStream, streamType StreamType, streamIndex int) bool {
	for _, stream := range streams {
		if stream.Type == streamType && stream.Index == streamIndex {
			return true
		}
	}
	return false
}
