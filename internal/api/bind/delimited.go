// SPDX-License-Identifier: GPL-2.0-only

package bind

import (
	"encoding"
	"log/slog"
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
)

// DelimitedEnums parses the elements of a comma- or pipe-delimited enum list
// parameter. It drops each element that parse rejects and logs it at Debug.
// This is upstream CommaDelimitedCollectionModelBinder.GetParsedResult: an
// unknown enum value never fails the request. Scalar enum parameters and JSON
// body enums do not use this function; upstream rejects those.
func DelimitedEnums[T any](parameter string, values []string, parse func(string) (T, bool)) []T {
	out := make([]T, 0, len(values))
	for _, value := range values {
		parsed, ok := parse(value)
		if !ok {
			dropDelimitedValue(parameter, value)
			continue
		}
		out = append(out, parsed)
	}
	return out
}

// ParseText parses one value with the TextUnmarshaler of *T. Use it as the
// parse function of DelimitedEnums for enum types that bind as text.
func ParseText[T any, PT interface {
	*T
	encoding.TextUnmarshaler
}](value string) (T, bool) {
	var parsed T
	if PT(&parsed).UnmarshalText([]byte(value)) != nil {
		var zero T
		return zero, false
	}
	return parsed, true
}

func dropDelimitedValue(parameter, value string) {
	slog.Debug("dropped unknown delimited query value", "parameter", parameter, "value", value)
}

// ItemFields returns the ItemFields values in a fields parameter with
// upstream enum casing. It drops unknown values, such as the Emby field
// AlternateMediaSources.
func ItemFields(parameter string, values []string) []string {
	return DelimitedEnums(parameter, values, ParseItemField)
}

// ParseItemField parses one upstream ItemFields name without regard to case.
func ParseItemField(value string) (string, bool) {
	for _, candidate := range itemFieldValues {
		if strings.EqualFold(value, candidate) {
			return candidate, true
		}
	}
	return "", false
}

// ItemFieldValues returns the complete pinned ItemFields vocabulary in
// upstream enum order.
func ItemFieldValues() []string {
	return append([]string(nil), itemFieldValues[:]...)
}

var itemFieldValues = [...]string{
	"AirTime", "CanDelete", "CanDownload", "ChannelInfo", "Chapters", "Trickplay", "ChildCount", "CumulativeRunTimeTicks", "CustomRating", "DateCreated", "DateLastMediaAdded", "DisplayPreferencesId", "Etag", "ExternalUrls", "Genres", "ItemCounts", "MediaSourceCount", "MediaSources", "OriginalTitle", "Overview", "ParentId", "Path", "People", "PlayAccess", "ProductionLocations", "ProviderIds", "PrimaryImageAspectRatio", "RecursiveItemCount", "Settings", "SeriesStudio", "SortName", "SpecialEpisodeNumbers", "Studios", "Taglines", "Tags", "RemoteTrailers", "MediaStreams", "SeasonUserData", "DateLastRefreshed", "DateLastSaved", "RefreshState", "ChannelImage", "EnableMediaSourceDisplay", "Width", "Height", "ExtraIds", "LocalTrailerCount", "IsHD", "SpecialFeatureCount",
}

// ParseImageType parses one upstream ImageType name without regard to case.
func ParseImageType(value string) (item.ImageType, bool) {
	for _, candidate := range imageTypeValues {
		if strings.EqualFold(value, candidate.String()) {
			return candidate, true
		}
	}
	return 0, false
}

var imageTypeValues = [...]item.ImageType{item.ImageTypePrimary, item.ImageTypeArt, item.ImageTypeBackdrop, item.ImageTypeBanner, item.ImageTypeLogo, item.ImageTypeThumb, item.ImageTypeDisc, item.ImageTypeBox, item.ImageTypeScreenshot, item.ImageTypeMenu, item.ImageTypeChapter, item.ImageTypeBoxRear, item.ImageTypeProfile}

// ParseKind parses one upstream BaseItemKind name without regard to case.
func ParseKind(value string) (item.Kind, bool) {
	kind, err := item.ParseKind(value)
	return kind, err == nil
}
