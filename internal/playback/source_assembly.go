// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// SourceFileFacts is the persisted file snapshot used internally to validate a
// media source before descriptor-based processing. It is intentionally absent
// from the wire DTO.
type SourceFileFacts struct {
	Size       *int64
	ModifiedAt *time.Time
	Inode      *int64
	ChangedAt  *time.Time
	Device     *int64
}

// MediaSourceRecord is the single-snapshot persistence projection needed to
// construct a file-backed media source. Store adapters implement
// MediaSourceReader without playback depending on their concrete types.
type MediaSourceRecord struct {
	ID               dto.UUID
	Path             *string
	Name             *string
	Container        *string
	Size             *int64
	RunTimeTicks     *int64
	TotalBitrate     *int
	DateModified     *time.Time
	FileDevice       *int64
	FileInode        *int64
	FileChangedAt    *time.Time
	SourceRevision   string
	Type             MediaSourceType
	VideoType        *VideoType
	IsoType          *IsoType
	Video3DFormat    *Video3DFormat
	MediaStreams     []MediaStream
	MediaAttachments []MediaAttachment
	Downloadable     bool
}

// MediaSourceReader reads all source facts from one consistent persistence
// snapshot. It intentionally avoids a dependency on the store package.
type MediaSourceReader interface {
	MediaSourceRecord(context.Context, dto.UUID) (MediaSourceRecord, error)
}

// MediaSourceGraphReader loads every candidate source for an item in one
// consistent snapshot.
type MediaSourceGraphReader interface {
	MediaSourceRecords(context.Context, dto.UUID) ([]MediaSourceRecord, error)
}

// AssembleMediaSource builds the default local-file source for an item and its
// persisted stream satellites.
func AssembleMediaSource(ctx context.Context, reader MediaSourceReader, id dto.UUID) (MediaSourceInfo, error) {
	record, err := reader.MediaSourceRecord(ctx, id)
	if err != nil {
		return MediaSourceInfo{}, err
	}
	return MediaSourceFromRecord(record), nil
}

// AssembleMediaSources returns the queried source first, followed by its
// deduplicated alternate-version graph in client-visible preference order.
func AssembleMediaSources(ctx context.Context, reader MediaSourceGraphReader, requestedID dto.UUID) ([]MediaSourceInfo, error) {
	records, err := assembleMediaSourceRecords(ctx, reader, requestedID)
	if err != nil {
		return nil, err
	}
	return MediaSourcesFromRecords(records), nil
}

// assembleMediaSourceRecords builds the ordered, deduplicated source graph
// shared by raw PlaybackInfo and the normal media-source projection.
func assembleMediaSourceRecords(ctx context.Context, reader MediaSourceGraphReader, requestedID dto.UUID) ([]MediaSourceRecord, error) {
	records, err := reader.MediaSourceRecords(ctx, requestedID)
	if err != nil {
		return nil, err
	}
	seen := make(map[dto.UUID]struct{}, len(records))
	unique := make([]MediaSourceRecord, 0, len(records))
	for _, record := range records {
		if _, exists := seen[record.ID]; exists {
			continue
		}
		seen[record.ID] = struct{}{}
		unique = append(unique, record)
	}
	sort.SliceStable(unique, func(i, j int) bool {
		if unique[i].ID == requestedID || unique[j].ID == requestedID {
			return unique[i].ID == requestedID
		}
		if fileRank(unique[i]) != fileRank(unique[j]) {
			return fileRank(unique[i]) < fileRank(unique[j])
		}
		if threeDRank(unique[i]) != threeDRank(unique[j]) {
			return threeDRank(unique[i]) < threeDRank(unique[j])
		}
		return sourceWidth(unique[i]) > sourceWidth(unique[j])
	})
	return unique, nil
}

// MediaSourceFromRecord projects a persisted source snapshot onto the
// client-visible MediaSourceInfo shape shared by playback and item DTO paths.
func MediaSourceFromRecord(record MediaSourceRecord) MediaSourceInfo {
	return mediaSourceFromRecord(record, "", true)
}

// MediaSourcesFromRecords builds a coherent source graph from a batch-loaded
// record set. The shared file-name prefix is intentionally computed before
// projection so every version gets the same concise source label.
func MediaSourcesFromRecords(records []MediaSourceRecord) []MediaSourceInfo {
	if records == nil {
		return nil
	}
	prefix := commonSourceNamePrefix(records)
	sources := make([]MediaSourceInfo, len(records))
	for index, record := range records {
		sources[index] = mediaSourceFromRecord(record, prefix, true)
	}
	return sources
}

// rawMediaSourcesFromRecords preserves the persisted source snapshot returned
// by a PlaybackInfo request without a negotiated device profile. Its source
// labels and stream selection facts must not inherit the richer /Items
// media-source projection.
func rawMediaSourcesFromRecords(records []MediaSourceRecord) []MediaSourceInfo {
	sources := make([]MediaSourceInfo, len(records))
	for index, record := range records {
		sources[index] = mediaSourceFromRecord(record, "", false)
		if record.Name != nil {
			sources[index].Name = *record.Name
		}
	}
	return sources
}

func mediaSourceFromRecord(record MediaSourceRecord, commonPrefix string, includeSubtitleDefaults bool) MediaSourceInfo {
	source := NewMediaSourceInfo()
	source.Protocol = MediaProtocolFile
	source.ID = record.ID.String()
	source.Type = record.Type
	source.Size = record.Size
	source.RunTimeTicks = record.RunTimeTicks
	source.Bitrate = record.TotalBitrate
	source.VideoType = record.VideoType
	source.IsoType = record.IsoType
	source.Video3DFormat = record.Video3DFormat
	source.FileFacts = SourceFileFacts{
		Size:       record.Size,
		ModifiedAt: record.DateModified,
		Inode:      record.FileInode,
		ChangedAt:  record.FileChangedAt,
		Device:     record.FileDevice,
	}
	if record.FileInode != nil && record.FileChangedAt != nil && record.SourceRevision != "" {
		source.SourceRevision = record.SourceRevision
	}
	if record.MediaStreams != nil {
		source.MediaStreams = append(make([]MediaStream, 0, len(record.MediaStreams)), record.MediaStreams...)
	}
	if record.MediaAttachments != nil {
		source.MediaAttachments = record.MediaAttachments
	}
	if record.Path == nil || *record.Path == "" {
		source.Type = MediaSourcePlaceholder
	} else {
		source.Path = *record.Path
		source.Name = mediaSourceName(record, commonPrefix)
		if record.DateModified != nil {
			source.ETag = mediaSourceETag(*record.DateModified)
		}
	}
	if source.Name == "" && record.Name != nil {
		source.Name = *record.Name
	}
	if record.Container != nil {
		source.Container = *record.Container
	}
	setDefaultAudioStreamIndex(&source)
	if includeSubtitleDefaults {
		setDefaultSubtitleStreamIndices(&source)
	}
	return source
}

// mediaSourceName ports BaseItem.GetMediaSourceName: alternate versions expose
// only their differentiating leaf suffix, while optical-disc sources carry the
// stock type suffix.
func mediaSourceName(record MediaSourceRecord, commonPrefix string) string {
	name := ""
	if record.Path != nil && *record.Path != "" {
		name = strings.TrimSpace(strings.TrimSuffix(filepath.Base(*record.Path), filepath.Ext(*record.Path)))
		if name == "." {
			name = ""
		}
		if commonPrefix != "" && len(name) > len(commonPrefix) && strings.EqualFold(name[:len(commonPrefix)], commonPrefix) {
			name = strings.TrimLeft(name[len(commonPrefix):], " -_.")
		}
	}
	if name == "" && record.Name != nil {
		name = *record.Name
	}
	if record.VideoType != nil && *record.VideoType == VideoTypeBluRay {
		name = strings.TrimSpace(name)
		if name != "" {
			name += "/Bluray"
		}
	}
	return name
}

func commonSourceNamePrefix(records []MediaSourceRecord) string {
	names := make([]string, 0, len(records))
	for _, record := range records {
		if record.Path == nil || *record.Path == "" {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(*record.Path), filepath.Ext(*record.Path))
		if name != "" && name != "." {
			names = append(names, name)
		}
	}
	if len(names) < 2 {
		return ""
	}
	prefix := names[0]
	for _, name := range names[1:] {
		limit := len(prefix)
		if len(name) < limit {
			limit = len(name)
		}
		matched := 0
		for matched < limit && strings.EqualFold(prefix[matched:matched+1], name[matched:matched+1]) {
			matched++
		}
		prefix = prefix[:matched]
		if prefix == "" {
			return ""
		}
	}
	cut := strings.LastIndexAny(prefix, "-_.")
	if cut < 0 {
		cut = strings.LastIndex(prefix, " ")
	}
	if cut < 0 {
		return ""
	}
	return prefix[:cut+1]
}

// Raw and rich sources retain the persisted default-audio selection.

func setDefaultAudioStreamIndex(source *MediaSourceInfo) {
	for _, stream := range source.MediaStreams {
		if stream.Type != StreamTypeAudio {
			continue
		}
		if source.DefaultAudioStreamIndex == nil || stream.IsDefault {
			index := stream.Index
			source.DefaultAudioStreamIndex = &index
			if stream.IsDefault {
				source.DefaultAudioIndexSource = AudioIndexDefault
				break
			}
		}
	}
}

// ApplyStaticSubtitleScore applies Jellyfin's item-source subtitle policy to
// one eligible stream. Callers may use a transient copy when projecting a
// non-source DTO, keeping score calculation independent of stored rows.
func ApplyStaticSubtitleScore(stream *MediaStream) bool {
	if stream.Type != StreamTypeSubtitle || (!stream.IsExternal && !stream.IsDefault && !stream.IsForced) {
		return false
	}
	stream.SupportsExternalStream = MediaStreamSupportsExternalStream(*stream)
	score := defaultSubtitleScore(*stream)
	stream.Score = &score
	return true
}

// Item-source projection applies Jellyfin's static subtitle policy.
func setDefaultSubtitleStreamIndices(source *MediaSourceInfo) {
	best := -1
	for index := range source.MediaStreams {
		stream := &source.MediaStreams[index]
		if !ApplyStaticSubtitleScore(stream) {
			continue
		}
		if best == -1 || defaultSubtitlePreferred(*stream, source.MediaStreams[best]) {
			best = index
		}
	}
	if best >= 0 {
		index := source.MediaStreams[best].Index
		source.DefaultSubtitleStreamIndex = &index
	}
}

func defaultSubtitleScore(stream MediaStream) int {
	score := 1
	if stream.IsForced {
		score = score*10 + 2
	} else {
		score = score*10 + 1
	}
	if stream.IsDefault {
		score = score*10 + 2
	} else {
		score = score*10 + 1
	}
	if stream.SupportsExternalStream {
		score = score*10 + 2
	} else {
		score = score*10 + 1
	}
	if stream.IsTextSubtitleStream() {
		score = score*10 + 2
	} else {
		score = score*10 + 1
	}
	if stream.IsExternal {
		return score*10 + 2
	}
	return score*10 + 1
}

func defaultSubtitlePreferred(left, right MediaStream) bool {
	if left.IsExternal != right.IsExternal {
		return left.IsExternal
	}
	if left.IsDefault != right.IsDefault {
		return left.IsDefault
	}
	if left.IsForced != right.IsForced {
		return !left.IsForced
	}
	return left.Index < right.Index
}

func fileRank(record MediaSourceRecord) int {
	if record.VideoType == nil || *record.VideoType == VideoTypeVideo {
		return 0
	}
	return 1
}

func threeDRank(record MediaSourceRecord) int {
	if record.Video3DFormat == nil {
		return 0
	}
	return 1
}

func sourceWidth(record MediaSourceRecord) int {
	for _, stream := range record.MediaStreams {
		if stream.Type == StreamTypeVideo && stream.Width != nil {
			return *stream.Width
		}
	}
	return 0
}

func mediaSourceETag(modified time.Time) string {
	// DateTime.Ticks is the upstream cache token input: 100ns ticks since year 1.
	ticks := modified.UTC().UnixNano()/100 + 621355968000000000
	digest := md5.Sum([]byte(strconv.FormatInt(ticks, 10)))
	return hex.EncodeToString(digest[:])
}

// expandMediaSourcePaths replaces virtual path placeholders in a media source
// and its streams. The source path and every stream path are expanded so both
// API responses and filesystem operations receive configured physical paths.
// EncoderPath and remote-search paths remain unchanged because they do not
// carry persisted item paths.
func expandMediaSourcePaths(source *MediaSourceInfo, expander VirtualPathExpander) {
	if expander == nil {
		return
	}
	source.Path = expander.Expand(source.Path)
	for index := range source.MediaStreams {
		stream := &source.MediaStreams[index]
		stream.Path = expander.Expand(stream.Path)
	}
}
