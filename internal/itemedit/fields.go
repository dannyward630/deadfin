// SPDX-License-Identifier: GPL-2.0-only

package itemedit

import (
	"fmt"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
)

// parsedRequest holds the enum values of an update body. Upstream binds them
// through System.Text.Json, which rejects an unknown name.
type parsedRequest struct {
	lockedFields  []item.MetadataField
	video3DFormat *item.Video3DFormat
	seriesStatus  *item.SeriesStatus
	airDays       []item.DayOfWeek
	peopleKinds   []item.PersonKind
}

func parseRequest(request dto.ItemUpdateRequest) (parsedRequest, error) {
	var parsed parsedRequest
	if request.LockedFields != nil {
		parsed.lockedFields = make([]item.MetadataField, 0, len(request.LockedFields))
		for _, name := range request.LockedFields {
			value, ok := parseEnum[item.MetadataField](name)
			if !ok {
				return parsedRequest{}, fmt.Errorf("%w: LockedFields %q", ErrInvalidRequest, name)
			}
			parsed.lockedFields = append(parsed.lockedFields, value)
		}
	}
	if request.Video3DFormat != nil {
		value, ok := parseEnum[item.Video3DFormat](*request.Video3DFormat)
		if !ok {
			return parsedRequest{}, fmt.Errorf("%w: Video3DFormat %q", ErrInvalidRequest, *request.Video3DFormat)
		}
		parsed.video3DFormat = &value
	}
	// GetSeriesStatus: an empty status is null; otherwise Enum.Parse with
	// ignoreCase (ItemUpdateController.cs:490-498).
	if request.Status != nil && *request.Status != "" {
		value, ok := parseEnum[item.SeriesStatus](*request.Status)
		if !ok {
			return parsedRequest{}, fmt.Errorf("%w: Status %q", ErrInvalidRequest, *request.Status)
		}
		parsed.seriesStatus = &value
	}
	if request.AirDays != nil {
		parsed.airDays = make([]item.DayOfWeek, 0, len(request.AirDays))
		for _, name := range request.AirDays {
			value, ok := parseEnum[item.DayOfWeek](name)
			if !ok {
				return parsedRequest{}, fmt.Errorf("%w: AirDays %q", ErrInvalidRequest, name)
			}
			parsed.airDays = append(parsed.airDays, value)
		}
	}
	parsed.peopleKinds = make([]item.PersonKind, len(request.People))
	for index, person := range request.People {
		value, ok := parseEnum[item.PersonKind](person.Type)
		if !ok {
			return parsedRequest{}, fmt.Errorf("%w: person Type %q", ErrInvalidRequest, person.Type)
		}
		parsed.peopleKinds[index] = value
	}
	return parsed, nil
}

// parseEnum matches a case-insensitive enum name against the type's String
// values.
func parseEnum[T interface {
	~uint8
	String() string
}](name string) (T, bool) {
	name = strings.TrimSpace(name)
	for value := 0; value <= 255; value++ {
		candidate := T(value)
		if text := candidate.String(); text != "" && strings.EqualFold(text, name) {
			return candidate, true
		}
	}
	var zero T
	return zero, false
}

// applyFields ports ItemUpdateController.UpdateItem(request, item)
// (ItemUpdateController.cs:229-488) for the edited item itself. The child
// cascade runs separately before it.
func applyFields(target *item.Item, request dto.ItemUpdateRequest, parsed parsedRequest) {
	target.Name = cloneString(request.Name)
	target.ForcedSortName = cloneString(request.ForcedSortName)
	target.OriginalTitle = blankToNil(request.OriginalTitle)
	target.OriginalLanguage = blankToNil(request.OriginalLanguage)
	target.CriticRating = cloneFloat(request.CriticRating)
	target.CommunityRating = cloneFloat(request.CommunityRating)
	target.IndexNumber = cloneInt(request.IndexNumber)
	target.ParentIndexNumber = cloneInt(request.ParentIndexNumber)
	target.Overview = cloneString(request.Overview)

	if request.Genres != nil {
		target.Values.Genres = distinctFold(request.Genres)
	}
	if target.Kind == item.KindEpisode {
		target.AirsAfterSeasonNumber = cloneInt(request.AirsAfterSeasonNumber)
		target.AirsBeforeEpisodeNumber = cloneInt(request.AirsBeforeEpisodeNumber)
		target.AirsBeforeSeasonNumber = cloneInt(request.AirsBeforeSeasonNumber)
	}
	if request.Height != nil && (target.Kind == item.KindLiveTvChannel || target.Kind == item.KindTvChannel) {
		target.Height = cloneInt(request.Height)
	}
	if request.Taglines != nil {
		if len(request.Taglines) == 0 {
			target.Tagline = nil
		} else {
			target.Tagline = cloneString(&request.Taglines[0])
		}
	}
	if request.Studios != nil {
		target.Values.Studios = distinctFold(names(request.Studios, false))
	}
	if request.DateCreated != nil {
		target.DateCreated = request.DateCreated.StdPtr()
	}
	if request.SeriesName != nil && hasSeries(target.Kind) {
		target.SeriesName = cloneString(request.SeriesName)
	}
	target.EndDate = request.EndDate.StdPtr()
	target.PremiereDate = request.PremiereDate.StdPtr()
	target.ProductionYear = cloneInt(request.ProductionYear)
	target.OfficialRating = blankToNil(request.OfficialRating)
	target.CustomRating = cloneString(request.CustomRating)
	if request.Tags != nil {
		target.Values.Tags = distinctFold(trimAll(request.Tags))
	}
	if request.ProductionLocations != nil {
		target.Values.ProductionLocations = distinctFold(request.ProductionLocations)
	}
	target.PreferredMetadataCountryCode = cloneString(request.PreferredMetadataCountryCode)
	target.PreferredMetadataLanguage = cloneString(request.PreferredMetadataLanguage)

	switch target.Kind {
	case item.KindSeries:
		if target.Extra.Series == nil {
			target.Extra.Series = &item.SeriesExtra{}
		}
		target.Extra.Series.DisplayOrder = cloneString(request.DisplayOrder)
	case item.KindBoxSet:
		if target.Extra.BoxSet == nil {
			target.Extra.BoxSet = &item.BoxSetExtra{}
		}
		target.Extra.BoxSet.DisplayOrder = cloneString(request.DisplayOrder)
	}
	if isVideo(target.Kind) {
		if target.Extra.Video == nil {
			target.Extra.Video = &item.VideoExtra{}
		}
		target.Extra.Video.AspectRatio = cloneString(request.AspectRatio)
	}

	target.IsLocked = request.LockData != nil && *request.LockData
	if request.LockedFields != nil {
		target.LockedFields = append([]item.MetadataField{}, parsed.lockedFields...)
	}
	// Runtime for media comes from ffprobe; only a series accepts an edit.
	if target.Kind == item.KindSeries {
		target.RunTimeTicks = cloneInt64(request.RunTimeTicks)
	}
	if request.ProviderIDs != nil {
		var ids item.ProviderIDs
		for _, pair := range request.ProviderIDs {
			if pair.Value == "" {
				continue
			}
			ids.Set(pair.Name, pair.Value)
		}
		target.ProviderIDs = ids
	}
	if isVideo(target.Kind) {
		target.Video3DFormat = parsed.video3DFormat
	}
	if request.AlbumArtists != nil && hasAlbumArtists(target.Kind) {
		target.Values.AlbumArtists = distinctFold(names(request.AlbumArtists, true))
	}
	if request.ArtistItems != nil && hasArtists(target.Kind) {
		target.Values.Artists = distinctFold(names(request.ArtistItems, true))
	}

	switch target.Kind {
	case item.KindAudio, item.KindAudioBook, item.KindMusicVideo:
		target.Album = cloneString(request.Album)
	case item.KindSeries:
		target.SeriesStatus = parsed.seriesStatus
		if request.AirDays != nil {
			target.Extra.Series.AirDays = append([]item.DayOfWeek{}, parsed.airDays...)
			target.Extra.Series.AirTime = cloneString(request.AirTime)
		}
	}
}

// editedPeople ports PeopleRepository.UpdatePeople: names and roles are
// trimmed, a repeated name and type keeps its first credit, and list order
// becomes the sort order (PeopleRepository.cs:98-160).
func editedPeople(people []dto.ItemUpdatePerson, kinds []item.PersonKind) []item.PersonCredit {
	credits := make([]item.PersonCredit, 0, len(people))
	seen := make(map[string]struct{}, len(people))
	for index, person := range people {
		name := strings.TrimSpace(person.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name) + "-" + kinds[index].String()
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		role := ""
		if person.Role != nil {
			role = strings.TrimSpace(*person.Role)
		}
		order := len(credits)
		credits = append(credits, item.PersonCredit{
			PersonID:  library.PersonEntity(name).ID,
			Name:      name,
			Role:      &role,
			Type:      kinds[index],
			SortOrder: &order,
		})
	}
	return credits
}

// refreshSortName ports BaseItem.OnMetadataChanged: the stored sort name is
// recomputed from the edited name and forced sort name.
func refreshSortName(target *item.Item) {
	target.SortName = nil
	if value, present := item.ComputeSortName(*target, item.DefaultSortOptions()); present {
		target.SortName = &value
	}
}

func hasSeries(kind item.Kind) bool {
	switch kind {
	case item.KindEpisode, item.KindSeason, item.KindBook, item.KindAudioBook:
		return true
	}
	return false
}

func isVideo(kind item.Kind) bool {
	switch kind {
	case item.KindVideo, item.KindMovie, item.KindEpisode, item.KindMusicVideo, item.KindTrailer:
		return true
	}
	return false
}

func hasAlbumArtists(kind item.Kind) bool {
	switch kind {
	case item.KindAudio, item.KindAudioBook, item.KindMusicAlbum:
		return true
	}
	return false
}

func hasArtists(kind item.Kind) bool {
	switch kind {
	case item.KindAudio, item.KindAudioBook, item.KindMusicAlbum, item.KindMusicVideo:
		return true
	}
	return false
}

func hasLockedField(fields []item.MetadataField, field item.MetadataField) bool {
	for _, value := range fields {
		if value == field {
			return true
		}
	}
	return false
}

func names(values []dto.ItemUpdateName, trim bool) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.Name
		if trim {
			result[index] = strings.TrimSpace(value.Name)
		}
	}
	return result
}

func trimAll(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	return result
}

// distinctFold ports Distinct(StringComparer.OrdinalIgnoreCase): the first
// spelling of each case-insensitive value is kept, in order.
func distinctFold(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

// exceptOrdinal ports Enumerable.Except with the default ordinal comparer:
// distinct values of left that are not in right.
func exceptOrdinal(left, right []string) []string {
	exclude := make(map[string]struct{}, len(right))
	for _, value := range right {
		exclude[value] = struct{}{}
	}
	result := make([]string, 0, len(left))
	for _, value := range left {
		if _, found := exclude[value]; found {
			continue
		}
		exclude[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func blankToNil(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return cloneString(value)
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFloat(value *float32) *float32 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
