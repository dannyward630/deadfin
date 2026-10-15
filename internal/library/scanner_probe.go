// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// MediaProbe is the scanner's narrow media inspection seam. Implementations
// receive the resolved source identity and the filesystem facts gathered for
// that source; they must honor ctx and return normalized source-compatible
// facts without persisting them.
type MediaProbe interface {
	Probe(context.Context, MediaProbeInput) (MediaProbeResult, error)
}

// MediaProbeInput is the immutable scanner context for one playable source.
type MediaProbeInput struct {
	Entry Entry
	Item  item.Item
}

// MediaProbeResult contains the source fields ItemStore persists alongside the
// resolved item plus its stream and attachment satellites.
type MediaProbeResult struct {
	Container         string
	Size              *int64
	RunTimeTicks      *int64
	TotalBitrate      *int
	VideoType         *item.VideoType
	IsoType           *item.IsoType
	Video3DFormat     *item.Video3DFormat
	Name              string
	Album             string
	Artists           []string
	AlbumArtists      []string
	Genres            []string
	Studios           []string
	People            []MediaProbePerson
	ProviderIDs       []MediaProbeProviderID
	Overview          *string
	OfficialRating    *string
	IndexNumber       *int
	ParentIndexNumber *int
	ProductionYear    *int
	PremiereDate      *time.Time
	Streams           []playback.MediaStream
	Attachments       []playback.MediaAttachment
}

// MediaProbePerson is one embedded media credit before the scanner assigns a
// stable Person entity identity.
type MediaProbePerson struct {
	Name string
	Role *string
	Type item.PersonKind
}

// MediaProbeProviderID retains the normalizer's deterministic provider order.
type MediaProbeProviderID struct {
	Name  string
	Value string
}

func (s *Scanner) probeResolvedMedia(ctx context.Context, desired []item.Item, entries map[string]Entry, scanPath string, scanEntries []Entry, directoryChildren map[string][]Entry, options LibraryOptions, existing map[string]item.Item) ([]store.ScannedChildMedia, error) {
	if s.options.MediaProbe == nil || len(desired) == 0 {
		return nil, nil
	}
	payloads := make([]store.ScannedChildMedia, 0)
	for index := range desired {
		candidate := &desired[index]
		if !isPlayableMedia(*candidate) || candidate.Path == nil {
			continue
		}
		path := normalizedEntryPath(*candidate.Path)
		entry, found := entries[path]
		if !found || entry.IsDir {
			continue
		}
		parentEntries := frozenMediaParentEntries(*candidate.Path, scanPath, scanEntries, directoryChildren)
		externalSubtitles := externalSubtitleStreams(*candidate.Path, parentEntries, options)
		if metadataDir := s.internalMetadataDir(candidate.ID); metadataDir != "" {
			externalSubtitles = appendMetadataSubtitleStreams(externalSubtitles, *candidate.Path, metadataDir, s.metadataSubtitleEntries(metadataDir))
		}
		previous, hasPrevious := existing[path]
		if hasPrevious && reusableMediaSource(entry, previous) {
			previousStreams, err := s.items.Streams(ctx, previous.ID)
			if err != nil {
				return nil, err
			}
			if externalSubtitleSetsEqual(previousStreams, externalSubtitles) {
				copyReusableProbeSourceFields(candidate, previous)
				copyProbeMetadata(candidate, previous)
				continue
			}
			attachments, err := s.items.Attachments(ctx, previous.ID)
			if err != nil {
				return nil, err
			}
			copyReusableProbeSourceFields(candidate, previous)
			copyProbeMetadata(candidate, previous)
			payloads = append(payloads, store.ScannedChildMedia{
				ItemID:      candidate.ID,
				Streams:     replaceExternalSubtitleStreams(previousStreams, externalSubtitles),
				Attachments: attachments,
			})
			continue
		}
		result, err := s.options.MediaProbe.Probe(ctx, MediaProbeInput{Entry: entry, Item: *candidate})
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			// A failed refresh retains prior probe facts and leaves stream and
			// attachment satellites untouched.
			if hasPrevious {
				copyProbeFailureSourceFields(candidate, entry, previous)
				copyProbeMetadata(candidate, previous)
			}
			continue
		}
		applyProbeSourceFields(candidate, result)
		applyProbeMetadata(candidate, result)
		result.Streams = hydrateScannedMediaStreams(result.Streams)
		if len(externalSubtitles) != 0 {
			result.Streams = append(externalSubtitles, shiftStreamIndexes(result.Streams, len(externalSubtitles))...)
		}
		payloads = append(payloads, store.ScannedChildMedia{ItemID: candidate.ID, Streams: result.Streams, Attachments: result.Attachments})
	}
	return payloads, nil
}

// externalSubtitleStreams mirrors local-subtitle ingestion: sidecars sharing
// a media stem are source streams, not independently scanned items.
func externalSubtitleStreams(mediaPath string, entries []Entry, options LibraryOptions) []playback.MediaStream {
	return subtitleStreamsIn(mediaPath, filepath.Dir(mediaPath), entries)
}

// appendMetadataSubtitleStreams appends the subtitle files in the item's
// internal metadata directory after the media-folder sidecars, as
// MediaInfoResolver.GetExternalFiles adds the internal metadata path after
// the containing folder. Indexes stay dense from zero.
func appendMetadataSubtitleStreams(sidecars []playback.MediaStream, mediaPath, metadataDir string, entries []Entry) []playback.MediaStream {
	internal := subtitleStreamsIn(mediaPath, metadataDir, entries)
	if len(internal) == 0 {
		return sidecars
	}
	result := append(append(make([]playback.MediaStream, 0, len(sidecars)+len(internal)), sidecars...), internal...)
	for index := range result {
		result[index].Index = index
	}
	return result
}

// subtitleStreamsIn parses the subtitle files of directory whose stem starts
// with the media stem.
func subtitleStreamsIn(mediaPath, directory string, entries []Entry) []playback.MediaStream {
	mediaStem := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	if mediaStem == "" {
		return nil
	}
	result := make([]playback.MediaStream, 0)
	for _, entry := range entries {
		if entry.IsDir || filepath.Dir(entry.Path) != filepath.Clean(directory) {
			continue
		}
		sidecarStem := strings.TrimSuffix(filepath.Base(entry.Path), filepath.Ext(entry.Path))
		if len(sidecarStem) <= len(mediaStem) || !strings.EqualFold(sidecarStem[:len(mediaStem)], mediaStem) {
			continue
		}
		info := naming.ParseExternalSubtitle(entry.Path, sidecarStem[len(mediaStem):], naming.DefaultOptions())
		if info == nil {
			continue
		}
		localizedLanguage := ""
		if info.Language != nil {
			localizedLanguage = naming.LanguageDisplayName(*info.Language)
		}
		result = append(result, playback.MediaStream{
			Codec:                    externalSubtitleCodec(entry.Path),
			Language:                 copyString(info.Language),
			TimeBase:                 "1/1000",
			Type:                     playback.StreamTypeSubtitle,
			Index:                    len(result),
			IsDefault:                info.IsDefault,
			IsForced:                 info.IsForced,
			IsHearingImpaired:        info.IsHearingImpaired,
			IsExternal:               true,
			SupportsExternalStream:   true,
			Path:                     entry.Path,
			LocalizedUndefined:       "Undefined",
			LocalizedDefault:         "Default",
			LocalizedForced:          "Forced",
			LocalizedExternal:        "External",
			LocalizedHearingImpaired: "Hearing Impaired",
			LocalizedLanguage:        localizedLanguage,
		})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Path < result[right].Path })
	for index := range result {
		result[index].Index = index
	}
	return result
}

// internalMetadataDir is the item's internal metadata directory, or empty
// when the scanner has no metadata root.
func (s *Scanner) internalMetadataDir(id dto.UUID) string {
	if s.options.MetadataPath == "" {
		return ""
	}
	return InternalMetadataDir(s.options.MetadataPath, id)
}

// metadataSubtitleEntries lists an internal metadata directory. Most items
// have none, and an unreadable directory contributes no subtitles, as
// upstream skips a missing internal metadata path.
func (s *Scanner) metadataSubtitleEntries(directory string) []Entry {
	entries, err := s.options.ReadDir(directory)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.logger.Warn("read internal metadata directory", "path", directory, "error", err)
		}
		return nil
	}
	return entries
}

// InternalMetadataDir ports BaseItem.GetInternalMetadataPath for library
// items: <metadata root>/library/<first two id characters>/<id>.
func InternalMetadataDir(metadataRoot string, id dto.UUID) string {
	value := id.String()
	return filepath.Join(metadataRoot, "library", value[:2], value)
}

// ReadExternalSubtitleStreams lists a media file's external subtitle
// streams from disk the way a scan does: sidecars in the media folder, then
// subtitle files in metadataDir. An empty metadataDir reads only the media
// folder. A missing directory contributes nothing.
func ReadExternalSubtitleStreams(mediaPath, metadataDir string) ([]playback.MediaStream, error) {
	folderEntries, err := readDirectory(filepath.Dir(mediaPath))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	result := externalSubtitleStreams(mediaPath, folderEntries, LibraryOptions{})
	if metadataDir == "" {
		return result, nil
	}
	metadataEntries, err := readDirectory(metadataDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return appendMetadataSubtitleStreams(result, mediaPath, metadataDir, metadataEntries), nil
}

// ReplaceExternalSubtitleStreams replaces the external subtitle streams of
// a stored stream list. The external streams come first and the other
// streams keep their order with shifted indexes, as a scan stores them.
func ReplaceExternalSubtitleStreams(previous, external []playback.MediaStream) []playback.MediaStream {
	return replaceExternalSubtitleStreams(previous, external)
}

// frozenMediaParentEntries selects the listing already captured for a media
// file's actual parent. Flattened self-contained movie directories resolve a
// child media path while scanning the containing folder, so scanEntries is not
// the sidecar directory in that case.
func frozenMediaParentEntries(mediaPath, scanPath string, scanEntries []Entry, directoryChildren map[string][]Entry) []Entry {
	parentPath := normalizedEntryPath(filepath.Dir(mediaPath))
	if parentPath == normalizedEntryPath(scanPath) {
		return scanEntries
	}
	return directoryChildren[parentPath]
}

func externalSubtitleSetsEqual(previous, expected []playback.MediaStream) bool {
	index := 0
	for _, stream := range previous {
		if !isExternalSubtitleStream(stream) {
			continue
		}
		if index >= len(expected) || !sameExternalSubtitleStream(stream, expected[index]) {
			return false
		}
		index++
	}
	return index == len(expected)
}

func isExternalSubtitleStream(stream playback.MediaStream) bool {
	return stream.Type == playback.StreamTypeSubtitle && stream.IsExternal
}

func sameExternalSubtitleStream(left, right playback.MediaStream) bool {
	return left.Path == right.Path &&
		left.Codec == right.Codec &&
		sameProbeString(left.Language, right.Language) &&
		left.IsDefault == right.IsDefault &&
		left.IsForced == right.IsForced &&
		left.IsHearingImpaired == right.IsHearingImpaired
}

func sameProbeString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func replaceExternalSubtitleStreams(previous, external []playback.MediaStream) []playback.MediaStream {
	retained := make([]playback.MediaStream, 0, len(previous))
	previousExternal := 0
	for _, stream := range previous {
		if isExternalSubtitleStream(stream) {
			previousExternal++
			continue
		}
		retained = append(retained, stream)
	}
	result := append([]playback.MediaStream(nil), external...)
	return append(result, shiftStreamIndexes(retained, len(external)-previousExternal)...)
}

// hydrateScannedMediaStreams ports the repository-time stream enrichment that
// Jellyfin applies after raw ffprobe normalization and before item DTO output.
func hydrateScannedMediaStreams(values []playback.MediaStream) []playback.MediaStream {
	if values == nil {
		return nil
	}
	result := append([]playback.MediaStream(nil), values...)
	for index := range result {
		stream := &result[index]
		if stream.Type != playback.StreamTypeSubtitle {
			continue
		}
		stream.SupportsExternalStream = playback.MediaStreamSupportsExternalStream(*stream)
		stream.LocalizedUndefined = "Undefined"
		stream.LocalizedDefault = "Default"
		stream.LocalizedForced = "Forced"
		stream.LocalizedExternal = "External"
		stream.LocalizedHearingImpaired = "Hearing Impaired"
		if stream.Language != nil {
			stream.LocalizedLanguage = naming.LanguageDisplayName(*stream.Language)
		}
	}
	return result
}

func externalSubtitleCodec(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".srt":
		return "subrip"
	case ".ass":
		return "ass"
	case ".ssa":
		return "ssa"
	case ".vtt":
		return "webvtt"
	default:
		return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	}
}

func shiftStreamIndexes(values []playback.MediaStream, delta int) []playback.MediaStream {
	if len(values) == 0 {
		return nil
	}
	result := append([]playback.MediaStream(nil), values...)
	for index := range result {
		result[index].Index += delta
	}
	return result
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func isPlayableMedia(value item.Item) bool {
	if value.IsFolder || value.Path == nil {
		return false
	}
	definition, found := item.Definition(value.Kind)
	return found && (definition.DefaultMediaType == item.MediaTypeAudio || definition.DefaultMediaType == item.MediaTypeVideo)
}

func reusableMediaSource(entry Entry, previous item.Item) bool {
	if !entry.Identity.Valid || previous.DateModified == nil || entry.Modified.IsZero() || previous.Size == nil || !hasMediaSourceContainer(previous) {
		return false
	}
	if *previous.Size != entry.Size {
		return false
	}
	if !previous.DateModified.Equal(entry.Modified.UTC().Truncate(time.Microsecond)) {
		return false
	}
	return sameFileIdentity(entry, previous)
}

func sameFileIdentity(entry Entry, previous item.Item) bool {
	if !entry.Identity.Valid {
		return false
	}
	if previous.FileInode == nil || previous.FileChangedAt == nil {
		return false
	}
	return uint64(*previous.FileInode) == entry.Identity.Inode && previous.FileChangedAt.Equal(entry.Identity.Changed.UTC().Truncate(time.Microsecond))
}

func applyProbeSourceFields(candidate *item.Item, result MediaProbeResult) {
	container := sourceContainer(result.Container)
	if isOpticalDiscPlaceholder(*candidate) {
		candidate.Container = nil
		candidate.MediaSourceContainer = container
	} else {
		candidate.Container = container
		candidate.MediaSourceContainer = nil
	}
	if result.Size != nil {
		size := *result.Size
		candidate.Size = &size
	}
	candidate.RunTimeTicks = copyInt64(result.RunTimeTicks)
	candidate.TotalBitrate = copyInt(result.TotalBitrate)
	if result.VideoType != nil {
		candidate.VideoType = copyVideoType(result.VideoType)
	}
	if result.IsoType != nil {
		candidate.IsoType = copyIsoType(result.IsoType)
	}
	if result.Video3DFormat != nil {
		candidate.Video3DFormat = copyVideo3DFormat(result.Video3DFormat)
	}
}

func applyProbeMetadata(candidate *item.Item, result MediaProbeResult) {
	if candidate.IsLocked {
		return
	}
	if candidate.IndexNumber == nil {
		candidate.IndexNumber = copyInt(result.IndexNumber)
	}
	if candidate.ParentIndexNumber == nil {
		candidate.ParentIndexNumber = copyInt(result.ParentIndexNumber)
	}
	if candidate.ProductionYear == nil {
		candidate.ProductionYear = copyInt(result.ProductionYear)
	}
	if candidate.PremiereDate == nil {
		candidate.PremiereDate = copyTime(result.PremiereDate)
	}
	if len(candidate.Values.Genres) == 0 && !hasRefreshLockedField(candidate.LockedFields, item.MetadataFieldGenres) {
		candidate.Values.Genres = copyStrings(result.Genres)
	}
	if len(candidate.Values.Studios) == 0 && !hasRefreshLockedField(candidate.LockedFields, item.MetadataFieldStudios) {
		candidate.Values.Studios = copyStrings(result.Studios)
	}
	if candidate.Overview == nil && !hasRefreshLockedField(candidate.LockedFields, item.MetadataFieldOverview) {
		candidate.Overview = copyRefreshString(result.Overview)
	}
	if candidate.OfficialRating == nil && !hasRefreshLockedField(candidate.LockedFields, item.MetadataFieldOfficialRating) {
		candidate.OfficialRating = copyRefreshString(result.OfficialRating)
	}
	for _, providerID := range result.ProviderIDs {
		name := strings.TrimSpace(providerID.Name)
		value := strings.TrimSpace(providerID.Value)
		if name == "" || value == "" {
			continue
		}
		if _, exists := candidate.ProviderIDs.Get(name); !exists {
			candidate.ProviderIDs.Set(name, value)
		}
	}
	if !hasRefreshLockedField(candidate.LockedFields, item.MetadataFieldCast) {
		appendProbePeople(candidate, result.People)
	}
	if candidate.Kind != item.KindAudio {
		return
	}
	if strings.TrimSpace(result.Name) != "" && !hasRefreshLockedField(candidate.LockedFields, item.MetadataFieldName) {
		candidate.Name = copyRefreshString(&result.Name)
		candidate.SortName = nil
	}
	if candidate.Album == nil && strings.TrimSpace(result.Album) != "" {
		candidate.Album = copyRefreshString(&result.Album)
	}
	if hasRefreshLockedField(candidate.LockedFields, item.MetadataFieldCast) {
		return
	}
	if len(candidate.Values.Artists) == 0 {
		candidate.Values.Artists = copyStrings(result.Artists)
	}
	if len(candidate.Values.AlbumArtists) == 0 {
		candidate.Values.AlbumArtists = copyStrings(result.AlbumArtists)
		if len(candidate.Values.AlbumArtists) == 0 {
			candidate.Values.AlbumArtists = copyStrings(result.Artists)
		}
	}
	appendAudioArtistCredits(candidate, candidate.Values.AlbumArtists, item.PersonKindAlbumArtist)
	appendAudioArtistCredits(candidate, candidate.Values.Artists, item.PersonKindArtist)
}

func appendProbePeople(candidate *item.Item, values []MediaProbePerson) {
	for _, value := range values {
		name := strings.TrimSpace(value.Name)
		if name == "" || value.Type == item.PersonKindUnknown || hasProbePerson(*candidate, name, value.Type, value.Role) {
			continue
		}
		person := PersonEntity(name)
		position := len(candidate.People)
		candidate.People = append(candidate.People, item.PersonCredit{
			PersonID:  person.ID,
			Name:      name,
			Role:      copyString(value.Role),
			Type:      value.Type,
			SortOrder: &position,
		})
	}
}

func hasProbePerson(candidate item.Item, name string, kind item.PersonKind, role *string) bool {
	for _, current := range candidate.People {
		if current.Type == kind && strings.EqualFold(strings.TrimSpace(current.Name), name) && sameProbeString(current.Role, role) {
			return true
		}
	}
	return false
}

func appendAudioArtistCredits(candidate *item.Item, names []string, kind item.PersonKind) {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || hasProbePerson(*candidate, name, kind, nil) {
			continue
		}
		person := PersonEntity(name)
		position := len(candidate.People)
		candidate.People = append(candidate.People, item.PersonCredit{PersonID: person.ID, Name: name, Type: kind, SortOrder: &position})
	}
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func copyReusableProbeSourceFields(candidate *item.Item, previous item.Item) {
	copyProbeFacts(candidate, previous)
	copyProbeSourceIdentity(candidate, previous)
}

func copyProbeFailureSourceFields(candidate *item.Item, entry Entry, previous item.Item) {
	copyProbeFacts(candidate, previous)
	if sameFileIdentity(entry, previous) {
		copyProbeSourceIdentity(candidate, previous)
		return
	}
	candidate.FileDevice = nil
	candidate.FileInode = nil
	candidate.FileChangedAt = nil
}

func copyProbeFacts(candidate *item.Item, previous item.Item) {
	if isOpticalDiscPlaceholder(*candidate) {
		candidate.Container = nil
		candidate.MediaSourceContainer = mediaSourceContainer(previous)
	} else {
		candidate.Container = copyString(previous.Container)
		candidate.MediaSourceContainer = nil
	}
	candidate.RunTimeTicks = copyInt64(previous.RunTimeTicks)
	if previous.Size == nil {
		candidate.Size = nil
	} else {
		size := *previous.Size
		candidate.Size = &size
	}
	candidate.TotalBitrate = copyInt(previous.TotalBitrate)
	candidate.VideoType = copyVideoType(previous.VideoType)
	candidate.IsoType = copyIsoType(previous.IsoType)
	candidate.Video3DFormat = copyVideo3DFormat(previous.Video3DFormat)
}

func copyProbeSourceIdentity(candidate *item.Item, previous item.Item) {
	candidate.FileDevice = copyInt64(previous.FileDevice)
	candidate.FileInode = copyInt64(previous.FileInode)
	candidate.FileChangedAt = copyTime(previous.FileChangedAt)
}

func copyProbeMetadata(candidate *item.Item, previous item.Item) {
	result := MediaProbeResult{
		Album:             dereferenceProbeString(previous.Album),
		Artists:           previous.Values.Artists,
		AlbumArtists:      previous.Values.AlbumArtists,
		Genres:            previous.Values.Genres,
		Studios:           previous.Values.Studios,
		Overview:          previous.Overview,
		OfficialRating:    previous.OfficialRating,
		IndexNumber:       previous.IndexNumber,
		ParentIndexNumber: previous.ParentIndexNumber,
		ProductionYear:    previous.ProductionYear,
		PremiereDate:      previous.PremiereDate,
		People:            make([]MediaProbePerson, 0, len(previous.People)),
		ProviderIDs:       make([]MediaProbeProviderID, 0),
	}
	if previous.Name != nil {
		result.Name = *previous.Name
	}
	for _, person := range previous.People {
		result.People = append(result.People, MediaProbePerson{Name: person.Name, Role: person.Role, Type: person.Type})
	}
	for _, providerID := range previous.ProviderIDs.Snapshot() {
		result.ProviderIDs = append(result.ProviderIDs, MediaProbeProviderID{Name: providerID.Name, Value: providerID.Value})
	}
	applyProbeMetadata(candidate, result)
}

func dereferenceProbeString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sourceContainer(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func mediaSourceContainer(value item.Item) *string {
	if value.MediaSourceContainer != nil {
		return copyString(value.MediaSourceContainer)
	}
	return copyString(value.Container)
}

func hasMediaSourceContainer(value item.Item) bool {
	return value.MediaSourceContainer != nil || value.Container != nil
}

func isOpticalDiscPlaceholder(value item.Item) bool {
	return value.IsPlaceHolder && value.VideoType != nil && *value.VideoType == item.VideoTypeBluRay
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyVideoType(value *item.VideoType) *item.VideoType {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyIsoType(value *item.IsoType) *item.IsoType {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyVideo3DFormat(value *item.Video3DFormat) *item.Video3DFormat {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func rehydrateRefreshLocks(candidate *item.Item, previous item.Item) {
	if previous.IsLocked {
		rehydrateGloballyLockedItem(candidate, previous)
		return
	}

	candidate.LockedFields = mergeRefreshLockedFields(previous.LockedFields, candidate.LockedFields)
	if hasRefreshLockedField(previous.LockedFields, item.MetadataFieldName) {
		candidate.Name = copyRefreshString(previous.Name)
		candidate.SortName = copyRefreshString(previous.SortName)
		candidate.ForcedSortName = copyRefreshString(previous.ForcedSortName)
		candidate.OriginalTitle = copyRefreshString(previous.OriginalTitle)
	}
	if hasRefreshLockedField(previous.LockedFields, item.MetadataFieldOverview) {
		candidate.Overview = copyRefreshString(previous.Overview)
		candidate.Tagline = copyRefreshString(previous.Tagline)
	}
	if hasRefreshLockedField(previous.LockedFields, item.MetadataFieldOfficialRating) {
		candidate.OfficialRating = copyRefreshString(previous.OfficialRating)
		candidate.CustomRating = copyRefreshString(previous.CustomRating)
	}
	if hasRefreshLockedField(previous.LockedFields, item.MetadataFieldRuntime) {
		candidate.RunTimeTicks = copyInt64(previous.RunTimeTicks)
	}
	if hasRefreshLockedField(previous.LockedFields, item.MetadataFieldGenres) {
		candidate.Values.Genres = append([]string(nil), previous.Values.Genres...)
	}
	if hasRefreshLockedField(previous.LockedFields, item.MetadataFieldStudios) {
		candidate.Values.Studios = append([]string(nil), previous.Values.Studios...)
	}
	if hasRefreshLockedField(previous.LockedFields, item.MetadataFieldTags) {
		candidate.Values.Tags = append([]string(nil), previous.Values.Tags...)
	}
	if hasRefreshLockedField(previous.LockedFields, item.MetadataFieldProductionLocations) {
		candidate.Values.ProductionLocations = append([]string(nil), previous.Values.ProductionLocations...)
	}
	if hasRefreshLockedField(previous.LockedFields, item.MetadataFieldCast) {
		candidate.People = append([]item.PersonCredit(nil), previous.People...)
	}
}

// rehydrateGloballyLockedItem restores the whole persisted item, then overlays
// the bounded set of facts owned by the scanner. Starting from the complete
// prior item is deliberate: globally locking metadata protects fields beyond
// MetadataField (provider IDs, ratings, dates, indexes, extras, trailers,
// people, and taxonomy values) as providers acquire additional fields.
func rehydrateGloballyLockedItem(candidate *item.Item, previous item.Item) {
	fresh := *candidate
	*candidate = previous
	restoreScannedItemFacts(candidate, fresh)
	candidate.IsLocked = true
	candidate.LockedFields = mergeRefreshLockedFields(previous.LockedFields, fresh.LockedFields)
}

// restoreScannedItemFacts overlays only resolver, local-artwork, filesystem,
// and media-probe facts. All remaining Item fields therefore retain their
// persisted provider metadata without a brittle field-by-field allowlist.
func restoreScannedItemFacts(candidate *item.Item, fresh item.Item) {
	candidate.ID = fresh.ID
	candidate.Kind = fresh.Kind
	candidate.ParentID = copyRefreshUUID(fresh.ParentID)
	candidate.OwnerID = copyRefreshUUID(fresh.OwnerID)
	candidate.TopParentID = copyRefreshUUID(fresh.TopParentID)
	candidate.SeriesID = copyRefreshUUID(fresh.SeriesID)
	candidate.SeasonID = copyRefreshUUID(fresh.SeasonID)
	candidate.Path = copyRefreshString(fresh.Path)
	candidate.IsFolder = fresh.IsFolder
	candidate.IsVirtualItem = fresh.IsVirtualItem
	candidate.IsInMixedFolder = fresh.IsInMixedFolder
	candidate.IsMovie = fresh.IsMovie
	candidate.IsSeries = fresh.IsSeries
	candidate.IsRepeat = fresh.IsRepeat
	candidate.IsPlaceHolder = fresh.IsPlaceHolder
	candidate.SourceType = fresh.SourceType
	candidate.MediaType = copyScannedPointer(fresh.MediaType)

	candidate.DateCreated = copyScannedPointer(fresh.DateCreated)
	candidate.DateModified = copyScannedPointer(fresh.DateModified)
	candidate.FileChangedAt = copyScannedPointer(fresh.FileChangedAt)
	candidate.Size = copyScannedPointer(fresh.Size)
	candidate.FileDevice = copyScannedPointer(fresh.FileDevice)
	candidate.FileInode = copyScannedPointer(fresh.FileInode)
	candidate.Container = copyScannedPointer(fresh.Container)
	candidate.MediaSourceContainer = copyScannedPointer(fresh.MediaSourceContainer)
	candidate.RunTimeTicks = copyScannedPointer(fresh.RunTimeTicks)
	candidate.TotalBitrate = copyScannedPointer(fresh.TotalBitrate)
	candidate.VideoType = copyScannedPointer(fresh.VideoType)
	candidate.IsoType = copyScannedPointer(fresh.IsoType)
	candidate.Video3DFormat = copyScannedPointer(fresh.Video3DFormat)

	// Local artwork is deliberately allowed through the globally locked provider
	// pipeline, while resolver-owned links and stacked source paths must track
	// the current directory topology.
	candidate.ImageInfos = copyScannedImageInfos(fresh.ImageInfos)
	candidate.LinkedChildren = copyScannedLinkedChildren(fresh.LinkedChildren)
	candidate.AdditionalParts = append([]string(nil), fresh.AdditionalParts...)
}

func copyScannedPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyScannedImageInfos(values []item.ImageInfo) []item.ImageInfo {
	if values == nil {
		return nil
	}
	copy := make([]item.ImageInfo, len(values))
	for index, value := range values {
		copy[index] = value
		copy[index].DateModified = copyScannedPointer(value.DateModified)
		copy[index].BlurHash = copyScannedPointer(value.BlurHash)
	}
	return copy
}

func copyScannedLinkedChildren(values []item.LinkedChildRef) []item.LinkedChildRef {
	if values == nil {
		return nil
	}
	copy := make([]item.LinkedChildRef, len(values))
	for index, value := range values {
		copy[index] = value
		copy[index].SortOrder = copyScannedPointer(value.SortOrder)
	}
	return copy
}

func mergeRefreshLockedFields(left, right []item.MetadataField) []item.MetadataField {
	merged := append([]item.MetadataField(nil), left...)
	for _, candidate := range right {
		if !hasRefreshLockedField(merged, candidate) {
			merged = append(merged, candidate)
		}
	}
	return merged
}

func hasRefreshLockedField(values []item.MetadataField, target item.MetadataField) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func canonicalizePeople(values []item.Item) {
	names := make(map[dto.UUID]string)
	for itemIndex := range values {
		for creditIndex := range values[itemIndex].People {
			credit := &values[itemIndex].People[creditIndex]
			if name, found := names[credit.PersonID]; found {
				credit.Name = name
				continue
			}
			names[credit.PersonID] = credit.Name
		}
	}
}
