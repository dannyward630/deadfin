// SPDX-License-Identifier: GPL-2.0-only

package item

import (
	"strconv"
	"strings"
)

// UserDataKeys returns the ordered persistence keys for it.
func UserDataKeys(it Item, in UserDataInputs) []string {
	base := baseUserDataKeys(it)

	switch it.Kind {
	case KindEpisode:
		if in.Series == nil || it.ParentIndexNumber == nil || it.IndexNumber == nil {
			return base
		}
		seriesKeys := UserDataKeys(*in.Series, UserDataInputs{})
		if len(seriesKeys) > 1 {
			seriesKeys = seriesKeys[:len(seriesKeys)-1]
		}
		keys := make([]string, 0, len(seriesKeys)+len(base))
		suffix := formatPadded(*it.ParentIndexNumber, 3) + formatPadded(*it.IndexNumber, 3)
		for _, key := range seriesKeys {
			keys = append(keys, key+suffix)
		}
		return append(keys, base...)
	case KindSeason:
		if in.Series == nil {
			return base
		}
		seriesKeys := UserDataKeys(*in.Series, UserDataInputs{})
		keys := make([]string, 0, len(seriesKeys)+len(base))
		index := 0
		if it.IndexNumber != nil {
			index = *it.IndexNumber
		}
		suffix := formatPadded(index, 3)
		for _, key := range seriesKeys {
			keys = append(keys, key+suffix)
		}
		return append(keys, base...)
	case KindSeries:
		keys := base
		if value, ok := providerID(it, "Imdb"); ok {
			keys = prependKey(keys, value)
		}
		if value, ok := providerID(it, "Tvdb"); ok {
			keys = prependKey(keys, value)
		}
		if value, ok := providerID(it, "Custom"); ok {
			keys = prependKey(keys, value)
		}
		return keys
	case KindVideo, KindMovie, KindMusicVideo, KindTrailer:
		keys := base
		if it.ExtraType != nil {
			if value, ok := providerID(it, "Tmdb"); ok {
				keys = prependKey(keys, extraUserDataKey(value, *it.ExtraType, it.RunTimeTicks))
			}
			if value, ok := providerID(it, "Imdb"); ok {
				keys = prependKey(keys, extraUserDataKey(value, *it.ExtraType, it.RunTimeTicks))
			}
			return keys
		}
		if value, ok := providerID(it, "Imdb"); ok {
			keys = prependKey(keys, value)
		}
		if value, ok := providerID(it, "Tmdb"); ok {
			keys = prependKey(keys, value)
		}
		return keys
	case KindMusicArtist:
		keys := make([]string, 0, len(base)+2)
		if value, ok := providerID(it, "MusicBrainzArtist"); ok {
			keys = append(keys, "Artist-Musicbrainz-"+value)
		}
		keys = append(keys, "Artist-"+removeDiacritics(itemName(it)))
		return append(keys, base...)
	case KindPerson, KindStudio, KindGenre, KindMusicGenre:
		return append([]string{it.Kind.String() + "-" + removeDiacritics(itemName(it))}, base...)
	case KindAudio, KindAudioBook:
		key := audioUserDataKey(it)
		return append([]string{key}, base...)
	case KindMusicAlbum:
		keys := base
		if len(it.Values.AlbumArtists) > 0 && it.Values.AlbumArtists[0] != "" {
			keys = prependKey(keys, it.Values.AlbumArtists[0]+"-"+itemName(it))
		}
		if value, ok := providerID(it, "MusicBrainzAlbum"); ok {
			keys = prependKey(keys, "MusicAlbum-Musicbrainz-"+value)
		}
		if value, ok := providerID(it, "MusicBrainzReleaseGroup"); ok {
			keys = prependKey(keys, "MusicAlbum-MusicBrainzReleaseGroup-"+value)
		}
		return keys
	case KindYear:
		return append([]string{"Year-" + itemName(it)}, base...)
	case KindLiveTvChannel, KindTvChannel:
		if in.DisableLiveTVChannelUserDataName {
			return base
		}
		return prependKey(base, "TvChannel-"+itemName(it))
	case KindLiveTvProgram, KindTvProgram, KindProgram:
		keys := base
		if it.IsSeries {
			if it.EpisodeTitle != nil && *it.EpisodeTitle != "" {
				keys = prependKey(keys, "Program-"+itemName(it)+*it.EpisodeTitle)
			}
			return keys
		}
		if value, ok := providerID(it, "Imdb"); ok {
			keys = prependKey(keys, value)
		}
		if value, ok := providerID(it, "Tmdb"); ok {
			keys = prependKey(keys, value)
		}
		return keys
	default:
		return base
	}
}

// PresentationUniqueKey returns the persisted presentation key or computes it.
func PresentationUniqueKey(it Item, in PresentationInputs) string {
	if it.PresentationUniqueKey != nil {
		return *it.PresentationUniqueKey
	}

	switch it.Kind {
	case KindVideo, KindMovie, KindMusicVideo, KindTrailer, KindEpisode:
		if it.PrimaryVersionID != nil {
			return it.PrimaryVersionID.String()
		}
	case KindSeries:
		if in.SeriesGrouping.EnableAutomaticGrouping {
			keys := UserDataKeys(it, UserDataInputs{})
			if len(keys) > 1 {
				key := keys[0]
				language := ""
				if it.PreferredMetadataLanguage != nil {
					language = *it.PreferredMetadataLanguage
				}
				if language == "" {
					language = in.SeriesGrouping.PreferredMetadataLanguage
				}
				if language != "" {
					key += "-" + language
				}
				for _, folderID := range in.SeriesGrouping.CollectionFolderIDs {
					key += "-" + folderID.String()
				}
				return key
			}
		}
	case KindSeason:
		if it.IndexNumber != nil && in.Series != nil {
			seriesKey := ""
			if in.Series.PresentationUniqueKey != nil {
				seriesKey = *in.Series.PresentationUniqueKey
			}
			return seriesKey + "-" + formatPadded(*it.IndexNumber, 3)
		}
	case KindMusicArtist:
		return "Artist-" + removeDiacritics(itemName(it))
	case KindPerson, KindStudio, KindGenre, KindMusicGenre:
		keys := UserDataKeys(it, UserDataInputs{})
		if len(keys) > 0 {
			return keys[0]
		}
	}

	return it.ID.String()
}

// SeriesPresentationUniqueKey returns a series grouping key when this kind uses one.
func SeriesPresentationUniqueKey(it Item, in PresentationInputs) *string {
	var value *string
	switch it.Kind {
	case KindEpisode, KindSeason:
		if in.Series == nil {
			return nil
		}
		value = in.Series.PresentationUniqueKey
	case KindBook, KindAudioBook:
		value = it.SeriesPresentationUniqueKey
	default:
		return nil
	}
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// ClientTypeName returns the upstream client-visible concrete type name.
func ClientTypeName(it Item) string {
	switch it.Kind {
	case KindPlaylistsFolder, KindManualPlaylistsFolder:
		return "ManualPlaylistsFolder"
	case KindLiveTvChannel, KindTvChannel:
		return "TvChannel"
	case KindLiveTvProgram, KindTvProgram, KindProgram:
		return "Program"
	}
	if IsFolder(it) && it.SourceType == SourceTypeChannel && it.Kind != KindChannel && it.Kind != KindSeason && it.Kind != KindSeries {
		return "ChannelFolderItem"
	}
	return it.Kind.String()
}

func baseUserDataKeys(it Item) []string {
	keys := make([]string, 0, 2)
	if it.SourceType == SourceTypeChannel && it.ExternalID != nil && *it.ExternalID != "" {
		keys = append(keys, *it.ExternalID)
	}
	return append(keys, it.ID.Dashed())
}

func providerID(it Item, name string) (string, bool) {
	value, found := it.ProviderIDs.Get(name)
	return value, found && value != ""
}

func prependKey(keys []string, value string) []string {
	out := make([]string, 1, len(keys)+1)
	out[0] = value
	return append(out, keys...)
}

func extraUserDataKey(providerID string, extraType ExtraType, runtimeTicks *int64) string {
	key := providerID + "-" + strings.ToLower(extraType.String())
	if runtimeTicks != nil {
		key += "-" + strconv.FormatInt(*runtimeTicks, 10)
	}
	return key
}

func audioUserDataKey(it Item) string {
	key := ""
	if it.IndexNumber != nil {
		key = formatPadded(*it.IndexNumber, 4)
	}
	if it.ParentIndexNumber != nil {
		key = formatPadded(*it.ParentIndexNumber, 4) + "-" + key
	}
	key += itemName(it)
	if it.Album != nil && *it.Album != "" {
		key = *it.Album + "-" + key
	}
	if len(it.Values.AlbumArtists) > 0 && it.Values.AlbumArtists[0] != "" {
		key = it.Values.AlbumArtists[0] + "-" + key
	}
	return key
}

func itemName(it Item) string {
	if it.Name == nil {
		return ""
	}
	return *it.Name
}

func formatPadded(value, width int) string {
	digits := strconv.Itoa(value)
	start := 0
	if strings.HasPrefix(digits, "-") {
		start = 1
	}
	padding := width - (len(digits) - start)
	if padding <= 0 {
		return digits
	}

	var out strings.Builder
	out.Grow(len(digits) + padding)
	if start == 1 {
		out.WriteByte('-')
	}
	for range padding {
		out.WriteByte('0')
	}
	out.WriteString(digits[start:])
	return out.String()
}
