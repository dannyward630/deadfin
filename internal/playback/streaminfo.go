// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// StreamOptions is StreamInfo.cs's ordinal-ignore-case option dictionary. In
// addition to lookup, it retains insertion order because ToURL writes options
// in the same order as the upstream Dictionary enumerator.
type StreamOptions struct {
	entries map[string]streamOption
	order   []string
}

type streamOption struct {
	name  string
	value string
}

func NewStreamOptions() StreamOptions {
	return StreamOptions{entries: make(map[string]streamOption)}
}

func (o *StreamOptions) ensure() {
	if o.entries == nil {
		o.entries = make(map[string]streamOption)
	}
}

// Set uses strings.EqualFold rather than Unicode lowercase normalization.
// That preserves OrdinalIgnoreCase identity for distinct dotted-I and ASCII-I
// keys while still accepting ordinary ASCII case variants.
func (o *StreamOptions) Set(name, value string) {
	o.ensure()
	for _, key := range o.order {
		entry := o.entries[key]
		if strings.EqualFold(entry.name, name) {
			entry.value = value
			o.entries[key] = entry
			return
		}
	}
	key := strconv.Itoa(len(o.order))
	o.order = append(o.order, key)
	o.entries[key] = streamOption{name: name, value: value}
}

func (o StreamOptions) get(name string) (string, bool) {
	for _, key := range o.order {
		entry := o.entries[key]
		if strings.EqualFold(entry.name, name) {
			return entry.value, true
		}
	}
	return "", false
}

func (o StreamOptions) Get(name string) string {
	value, _ := o.get(name)
	return value
}

func (o StreamOptions) ordered() []streamOption {
	result := make([]streamOption, 0, len(o.order))
	for _, key := range o.order {
		result = append(result, o.entries[key])
	}
	return result
}

func (o StreamOptions) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, entry := range o.ordered() {
		if i != 0 {
			b.WriteByte(',')
		}
		name, err := json.Marshal(entry.name)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(entry.value)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (o *StreamOptions) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		*o = NewStreamOptions()
		return nil
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return &json.UnmarshalTypeError{Value: "non-object", Type: nil}
	}
	*o = NewStreamOptions()
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return &json.UnmarshalTypeError{Value: "non-string object key", Type: nil}
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		o.Set(name, value)
	}
	_, err = decoder.Token()
	return err
}

// StreamInfo ports MediaBrowser.Model.Dlna.StreamInfo at b04614e. C# nullable
// values are pointers so WhenWritingNull wire omission is retained.
type StreamInfo struct {
	ItemID                              dto.UUID               `json:"ItemId"`
	PlayMethod                          PlayMethod             `json:"PlayMethod"`
	Context                             EncodingContext        `json:"Context"`
	MediaType                           DlnaProfileType        `json:"MediaType"`
	Container                           *string                `json:"Container,omitempty"`
	SubProtocol                         MediaStreamProtocol    `json:"SubProtocol"`
	StartPositionTicks                  int64                  `json:"StartPositionTicks"`
	SegmentLength                       *int                   `json:"SegmentLength,omitempty"`
	MinSegments                         *int                   `json:"MinSegments,omitempty"`
	RequireAvc                          bool                   `json:"RequireAvc"`
	RequireNonAnamorphic                bool                   `json:"RequireNonAnamorphic"`
	CopyTimestamps                      bool                   `json:"CopyTimestamps"`
	EnableMpegtsM2TsMode                bool                   `json:"EnableMpegtsM2TsMode"`
	EnableSubtitlesInManifest           bool                   `json:"EnableSubtitlesInManifest"`
	AudioCodecs                         []string               `json:"AudioCodecs"`
	VideoCodecs                         []string               `json:"VideoCodecs"`
	AudioStreamIndex                    *int                   `json:"AudioStreamIndex,omitempty"`
	SubtitleStreamIndex                 *int                   `json:"SubtitleStreamIndex,omitempty"`
	TranscodingMaxAudioChannels         *int                   `json:"TranscodingMaxAudioChannels,omitempty"`
	GlobalMaxAudioChannels              *int                   `json:"GlobalMaxAudioChannels,omitempty"`
	AudioBitrate                        *int                   `json:"AudioBitrate,omitempty"`
	AudioSampleRate                     *int                   `json:"AudioSampleRate,omitempty"`
	VideoBitrate                        *int                   `json:"VideoBitrate,omitempty"`
	MaxWidth                            *int                   `json:"MaxWidth,omitempty"`
	MaxHeight                           *int                   `json:"MaxHeight,omitempty"`
	MaxFramerate                        *float32               `json:"MaxFramerate,omitempty"`
	DeviceProfile                       *DeviceProfile         `json:"DeviceProfile,omitempty"`
	DeviceProfileID                     *string                `json:"DeviceProfileId,omitempty"`
	DeviceID                            *string                `json:"DeviceId,omitempty"`
	RunTimeTicks                        *int64                 `json:"RunTimeTicks,omitempty"`
	TranscodeSeekInfo                   TranscodeSeekInfo      `json:"TranscodeSeekInfo"`
	EstimateContentLength               bool                   `json:"EstimateContentLength"`
	MediaSource                         *MediaSourceInfo       `json:"MediaSource,omitempty"`
	SubtitleCodecs                      []string               `json:"SubtitleCodecs"`
	SubtitleDeliveryMethod              SubtitleDeliveryMethod `json:"SubtitleDeliveryMethod"`
	SubtitleFormat                      *string                `json:"SubtitleFormat,omitempty"`
	PlaySessionID                       *string                `json:"PlaySessionId,omitempty"`
	TranscodeReasons                    TranscodeReason        `json:"TranscodeReasons"`
	StreamOptions                       StreamOptions          `json:"StreamOptions"`
	EnableAudioVbrEncoding              bool                   `json:"EnableAudioVbrEncoding"`
	AlwaysBurnInSubtitleWhenTranscoding bool                   `json:"AlwaysBurnInSubtitleWhenTranscoding"`
	audioCodecsNull                     bool
	videoCodecsNull                     bool
	subtitleCodecsNull                  bool
}

func NewStreamInfo() StreamInfo {
	return StreamInfo{
		AudioCodecs:    []string{},
		VideoCodecs:    []string{},
		SubtitleCodecs: []string{},
		StreamOptions:  NewStreamOptions(),
	}
}

func (s *StreamInfo) UnmarshalJSON(data []byte) error {
	normalized, err := normalizeStreamInfoNumericStrings(data)
	if err != nil {
		return err
	}
	type raw StreamInfo
	value := NewStreamInfo()
	if err := json.Unmarshal(normalized, (*raw)(&value)); err != nil {
		return err
	}
	// MediaStream.ReferenceFrameRate is computed and its raw JSON value is
	// ignored by MediaStream. Older StreamInfo captures can carry only that
	// derived value, so use it solely when both source frame-rate facts are
	// absent; never override a real computed source rate.
	var captured struct {
		MediaSource *struct {
			MediaStreams []struct {
				ReferenceFrameRate *float32 `json:"ReferenceFrameRate"`
			} `json:"MediaStreams"`
		} `json:"MediaSource"`
	}
	if err := json.Unmarshal(data, &captured); err != nil {
		return err
	}
	if value.MediaSource != nil && captured.MediaSource != nil {
		for i, stream := range captured.MediaSource.MediaStreams {
			if i < len(value.MediaSource.MediaStreams) && value.MediaSource.MediaStreams[i].AverageFrameRate == nil && value.MediaSource.MediaStreams[i].RealFrameRate == nil && stream.ReferenceFrameRate != nil {
				value.MediaSource.MediaStreams[i].AverageFrameRate = stream.ReferenceFrameRate
			}
		}
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawFields); err != nil {
		return err
	}
	value.audioCodecsNull = bytes.Equal(rawFields["AudioCodecs"], []byte("null"))
	value.videoCodecsNull = bytes.Equal(rawFields["VideoCodecs"], []byte("null"))
	value.subtitleCodecsNull = bytes.Equal(rawFields["SubtitleCodecs"], []byte("null"))
	value.StreamOptions.ensure()
	*s = value
	return nil
}

// JsonDefaults uses AllowReadingFromString. Normalize only StreamInfo's scalar
// fields before ordinary Go unmarshaling; nested playback models own theirs.
func normalizeStreamInfoNumericStrings(data []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	normalizeInteger := func(name string, bitSize int) error {
		raw, ok := fields[name]
		if !ok || bytes.Equal(raw, []byte("null")) {
			return nil
		}
		var value string
		if json.Unmarshal(raw, &value) == nil {
			parsed, err := strconv.ParseInt(value, 10, bitSize)
			if err == nil {
				fields[name] = json.RawMessage(strconv.FormatInt(parsed, 10))
			}
			return nil
		}
		var number json.Number
		if err := json.Unmarshal(raw, &number); err != nil {
			return nil
		}
		parsed, err := strconv.ParseInt(number.String(), 10, bitSize)
		if err != nil {
			return fmt.Errorf("invalid %s: %w", name, err)
		}
		fields[name] = json.RawMessage(strconv.FormatInt(parsed, 10))
		return nil
	}
	for _, name := range []string{"StartPositionTicks", "RunTimeTicks"} {
		if err := normalizeInteger(name, 64); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{
		"SegmentLength", "MinSegments", "AudioStreamIndex", "SubtitleStreamIndex",
		"TranscodingMaxAudioChannels", "GlobalMaxAudioChannels", "AudioBitrate",
		"AudioSampleRate", "VideoBitrate", "MaxWidth", "MaxHeight",
	} {
		if err := normalizeInteger(name, 32); err != nil {
			return nil, err
		}
	}
	if raw, ok := fields["MaxFramerate"]; ok {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			if parsed, err := strconv.ParseFloat(value, 32); err == nil {
				fields["MaxFramerate"] = json.RawMessage(strconv.FormatFloat(parsed, 'g', -1, 32))
			}
		}
	}
	return json.Marshal(fields)
}

func (s StreamInfo) MediaSourceID() string {
	if s.MediaSource == nil {
		return ""
	}
	return s.MediaSource.ID
}

func (s StreamInfo) IsDirectStream() bool {
	return (s.MediaSource == nil || s.MediaSource.VideoType == nil || (*s.MediaSource.VideoType != VideoTypeDVD && *s.MediaSource.VideoType != VideoTypeBluRay)) &&
		(s.PlayMethod == PlayMethodDirectStream || s.PlayMethod == PlayMethodDirectPlay)
}

func (s StreamInfo) TargetAudioStream() *MediaStream {
	if s.MediaSource == nil {
		return nil
	}
	return s.MediaSource.DefaultAudioStream(s.AudioStreamIndex)
}

func (s StreamInfo) TargetVideoStream() *MediaStream {
	if s.MediaSource == nil {
		return nil
	}
	return s.MediaSource.VideoStream()
}

func (s StreamInfo) TargetAudioSampleRate() *int {
	if s.AudioSampleRate != nil && !s.IsDirectStream() {
		return s.AudioSampleRate
	}
	if stream := s.TargetAudioStream(); stream != nil {
		return stream.SampleRate
	}
	return nil
}

func (s StreamInfo) TargetAudioBitDepth() *int {
	if s.IsDirectStream() {
		if stream := s.TargetAudioStream(); stream != nil {
			return stream.BitDepth
		}
		return nil
	}
	if codecs := s.TargetAudioCodec(); len(codecs) != 0 && codecs[0] != "" {
		return s.targetAudioBitDepth(codecs[0])
	}
	if stream := s.TargetAudioStream(); stream != nil {
		return stream.BitDepth
	}
	return nil
}

func (s StreamInfo) TargetVideoBitDepth() *int {
	if s.IsDirectStream() {
		if stream := s.TargetVideoStream(); stream != nil {
			return stream.BitDepth
		}
		return nil
	}
	if codecs := s.TargetVideoCodec(); len(codecs) != 0 && codecs[0] != "" {
		return s.GetTargetVideoBitDepth(codecs[0])
	}
	if stream := s.TargetVideoStream(); stream != nil {
		return stream.BitDepth
	}
	return nil
}

func (s StreamInfo) TargetRefFrames() *int {
	if s.IsDirectStream() {
		if stream := s.TargetVideoStream(); stream != nil {
			return stream.RefFrames
		}
		return nil
	}
	if codecs := s.TargetVideoCodec(); len(codecs) != 0 && codecs[0] != "" {
		return s.GetTargetRefFrames(codecs[0])
	}
	if stream := s.TargetVideoStream(); stream != nil {
		return stream.RefFrames
	}
	return nil
}

func (s StreamInfo) TargetFramerate() *float32 {
	if s.MaxFramerate != nil && !s.IsDirectStream() {
		return s.MaxFramerate
	}
	if stream := s.TargetVideoStream(); stream != nil {
		return stream.ReferenceFrameRate()
	}
	return nil
}

func (s StreamInfo) TargetVideoLevel() *float64 {
	if s.IsDirectStream() {
		if stream := s.TargetVideoStream(); stream != nil {
			return stream.Level
		}
		return nil
	}
	if codecs := s.TargetVideoCodec(); len(codecs) != 0 && codecs[0] != "" {
		return s.GetTargetVideoLevel(codecs[0])
	}
	if stream := s.TargetVideoStream(); stream != nil {
		return stream.Level
	}
	return nil
}

func (s StreamInfo) TargetPacketLength() *int {
	if !s.IsDirectStream() {
		return nil
	}
	if stream := s.TargetVideoStream(); stream != nil {
		return stream.PacketLength
	}
	return nil
}

func (s StreamInfo) TargetVideoProfile() *string {
	if s.IsDirectStream() {
		if stream := s.TargetVideoStream(); stream != nil {
			return stream.Profile
		}
		return nil
	}
	if codecs := s.TargetVideoCodec(); len(codecs) != 0 && codecs[0] != "" {
		value, found := s.option(codecs[0], "profile")
		if !found {
			return nil
		}
		return &value
	}
	if stream := s.TargetVideoStream(); stream != nil {
		return stream.Profile
	}
	return nil
}

func (s StreamInfo) TargetVideoRangeType() VideoRangeType {
	if s.IsDirectStream() {
		if stream := s.TargetVideoStream(); stream != nil {
			return stream.VideoRangeType()
		}
		return VideoRangeUnknown
	}
	if codecs := s.TargetVideoCodec(); len(codecs) != 0 && codecs[0] != "" {
		if value, ok := parseVideoRangeTypeOption(s.GetOption(codecs[0], "rangetype")); ok {
			return value
		}
	}
	if stream := s.TargetVideoStream(); stream != nil {
		return stream.VideoRangeType()
	}
	return VideoRangeUnknown
}

// Enum.TryParse accepts comma-delimited enum names even without [Flags], then
// ORs their numeric values. StreamInfo.cs relies on that otherwise surprising
// behavior for profile rangetype options (for example SDR,HDR10,HLG => HLG).
func parseVideoRangeTypeOption(value string) (VideoRangeType, bool) {
	var combined VideoRangeType
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		if numeric, err := strconv.ParseUint(name, 10, 8); err == nil {
			combined |= VideoRangeType(numeric)
			continue
		}
		parsed, ok := ParseVideoRangeType(name)
		if !ok {
			return VideoRangeUnknown, false
		}
		combined |= parsed
	}
	return combined, value != ""
}

func (s StreamInfo) TargetVideoCodecTag() *string {
	if !s.IsDirectStream() {
		return nil
	}
	if stream := s.TargetVideoStream(); stream != nil {
		return stream.CodecTag
	}
	return nil
}

func (s StreamInfo) TargetAudioBitrate() *int {
	if s.AudioBitrate != nil && !s.IsDirectStream() {
		return s.AudioBitrate
	}
	if stream := s.TargetAudioStream(); stream != nil {
		return stream.BitRate
	}
	return nil
}

func (s StreamInfo) targetAudioChannels(codec string) *int {
	defaultValue := s.GlobalMaxAudioChannels
	if defaultValue == nil {
		defaultValue = s.TranscodingMaxAudioChannels
	}
	value := s.GetOption(codec, "audiochannels")
	if value == "" {
		return defaultValue
	}
	parsed := parseOptionInt(value)
	if parsed == nil {
		return defaultValue
	}
	if defaultValue != nil && *parsed > *defaultValue {
		return defaultValue
	}
	return parsed
}

// GetTargetAudioChannels exposes the non-null convenience used by callers.
// TargetAudioChannels retains upstream nullability.
func (s StreamInfo) GetTargetAudioChannels(codec string) int {
	if value := s.targetAudioChannels(codec); value != nil {
		return *value
	}
	return 0
}

func (s StreamInfo) TargetAudioChannels() *int {
	if s.IsDirectStream() {
		if stream := s.TargetAudioStream(); stream != nil {
			return stream.Channels
		}
		return nil
	}
	if codecs := s.TargetAudioCodec(); len(codecs) != 0 && codecs[0] != "" {
		// StreamInfo.cs:530-535 deliberately consults maxrefframes here.
		return s.GetTargetRefFrames(codecs[0])
	}
	if stream := s.TargetAudioStream(); stream != nil {
		return stream.Channels
	}
	return nil
}

func (s StreamInfo) TargetAudioCodec() []string {
	return targetCodec(s.TargetAudioStream(), s.AudioCodecs, s.IsDirectStream())
}

func (s StreamInfo) TargetVideoCodec() []string {
	return targetCodec(s.TargetVideoStream(), s.VideoCodecs, s.IsDirectStream())
}

func targetCodec(stream *MediaStream, codecs []string, direct bool) []string {
	input := ""
	if stream != nil {
		input = stream.Codec
	}
	if direct {
		if input == "" {
			return []string{}
		}
		return []string{input}
	}
	for _, codec := range codecs {
		if strings.EqualFold(codec, input) {
			if codec == "" {
				return []string{}
			}
			return []string{codec}
		}
	}
	return codecs
}

func (s StreamInfo) TargetSize() *int64 {
	if s.IsDirectStream() {
		if s.MediaSource != nil {
			return s.MediaSource.Size
		}
		return nil
	}
	if s.RunTimeTicks == nil {
		return nil
	}
	bitrate := s.TargetTotalBitrate()
	seconds := float64(*s.RunTimeTicks) / 10_000 / 1_000
	value := int64(math.RoundToEven(float64(bitrate) * seconds))
	return &value
}

func (s StreamInfo) TargetVideoBitrate() *int {
	if s.VideoBitrate != nil && !s.IsDirectStream() {
		return s.VideoBitrate
	}
	if stream := s.TargetVideoStream(); stream != nil {
		return stream.BitRate
	}
	return nil
}

func (s StreamInfo) TargetTimestamp() TransportStreamTimestamp {
	defaultValue := TimestampNone
	if s.Container != nil && strings.EqualFold(*s.Container, "m2ts") {
		defaultValue = TimestampValid
	}
	if !s.IsDirectStream() || s.MediaSource == nil {
		return defaultValue
	}
	if s.MediaSource.Timestamp == nil {
		return TimestampNone
	}
	return *s.MediaSource.Timestamp
}

func (s StreamInfo) TargetTotalBitrate() int {
	var value uint32
	if bitrate := s.TargetAudioBitrate(); bitrate != nil {
		value += uint32(int32(*bitrate))
	}
	if bitrate := s.TargetVideoBitrate(); bitrate != nil {
		value += uint32(int32(*bitrate))
	}
	return int(int32(value))
}

func (s StreamInfo) IsTargetAnamorphic() *bool {
	if !s.IsDirectStream() {
		return boolPointer(false)
	}
	if stream := s.TargetVideoStream(); stream != nil {
		return stream.IsAnamorphic
	}
	return nil
}

func (s StreamInfo) IsTargetInterlaced() *bool {
	if s.IsDirectStream() {
		if stream := s.TargetVideoStream(); stream != nil {
			return boolPointer(stream.IsInterlaced)
		}
		return nil
	}
	if codecs := s.TargetVideoCodec(); len(codecs) != 0 && codecs[0] != "" && strings.EqualFold(s.GetOption(codecs[0], "deinterlace"), "true") {
		return boolPointer(false)
	}
	if stream := s.TargetVideoStream(); stream != nil {
		return boolPointer(stream.IsInterlaced)
	}
	return nil
}

func (s StreamInfo) IsTargetAVC() *bool {
	if !s.IsDirectStream() {
		return boolPointer(true)
	}
	if stream := s.TargetVideoStream(); stream != nil {
		return stream.IsAVC
	}
	return nil
}

func (s StreamInfo) TargetWidth() *int  { width, _ := s.targetDimensions(); return width }
func (s StreamInfo) TargetHeight() *int { _, height := s.targetDimensions(); return height }

func (s StreamInfo) targetDimensions() (*int, *int) {
	stream := s.TargetVideoStream()
	if stream == nil || stream.Width == nil || stream.Height == nil {
		return s.MaxWidth, s.MaxHeight
	}
	width, height := *stream.Width, *stream.Height
	if s.MaxHeight != nil && *s.MaxHeight > 0 && *s.MaxHeight < height {
		width = dotnetRound(float64(*s.MaxHeight) / float64(height) * float64(width))
		height = *s.MaxHeight
	}
	if s.MaxWidth != nil && *s.MaxWidth > 0 && *s.MaxWidth < width {
		height = dotnetRound(float64(*s.MaxWidth) / float64(width) * float64(height))
		width = *s.MaxWidth
	}
	return &width, &height
}

func dotnetRound(v float64) int { return int(math.RoundToEven(v)) }

func (s StreamInfo) TargetVideoStreamCount() *int { return s.targetStreamCount(MediaStreamVideo) }
func (s StreamInfo) TargetAudioStreamCount() *int { return s.targetStreamCount(MediaStreamAudio) }
func (s StreamInfo) targetStreamCount(kind MediaStreamType) *int {
	if s.MediaSource == nil {
		return nil
	}
	count := s.MediaSource.StreamCount(kind)
	if count == nil || s.IsDirectStream() || *count <= 1 {
		return count
	}
	value := 1
	return &value
}

func (s *StreamInfo) SetOption(qualifier, name, value string) {
	if qualifier == "" {
		s.SetOptionName(name, value)
		return
	}
	s.SetOptionName(qualifier+"-"+name, value)
}
func (s *StreamInfo) SetOptionName(name, value string) { s.StreamOptions.Set(name, value) }
func (s StreamInfo) GetOption(qualifier, name string) string {
	value, _ := s.option(qualifier, name)
	return value
}
func (s StreamInfo) GetOptionName(name string) string { return s.StreamOptions.Get(name) }
func (s StreamInfo) option(qualifier, name string) (string, bool) {
	value, found := s.StreamOptions.get(qualifier + "-" + name)
	if value == "" {
		if fallback, fallbackFound := s.StreamOptions.get(name); fallbackFound {
			return fallback, true
		}
	}
	return value, found
}

func (s StreamInfo) GetTargetVideoBitDepth(codec string) *int {
	return parseOptionInt(s.GetOption(codec, "videobitdepth"))
}

func (s StreamInfo) targetAudioBitDepth(codec string) *int {
	return parseOptionInt(s.GetOption(codec, "audiobitdepth"))
}

func (s StreamInfo) GetTargetVideoLevel(codec string) *float64 {
	return parseOptionFloat(s.GetOption(codec, "level"))
}

func (s StreamInfo) GetTargetRefFrames(codec string) *int {
	return parseOptionInt(s.GetOption(codec, "maxrefframes"))
}

func parseOptionInt(value string) *int {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil {
		return nil
	}
	result := int(parsed)
	return &result
}

func parseOptionFloat(value string) *float64 {
	parsed, ok := parseDoubleInvariant(value)
	if !ok {
		return nil
	}
	return &parsed
}

func formatFloat32Invariant(value float32) string {
	if math.IsInf(float64(value), 1) {
		return "Infinity"
	}
	if math.IsInf(float64(value), -1) {
		return "-Infinity"
	}
	return strings.ReplaceAll(strconv.FormatFloat(float64(value), 'g', -1, 32), "e", "E")
}

// ToURL ports StreamInfo.ToUrl byte-for-byte: fixed parameter ordering, raw
// values, and option spaces stripped rather than escaped.
func (s StreamInfo) ToURL(baseURL, accessToken, query string) string {
	var b strings.Builder
	if baseURL != "" {
		b.WriteString(strings.TrimRight(baseURL, "/"))
	}
	if s.MediaType == DlnaAudio {
		b.WriteString("/audio/")
	} else {
		b.WriteString("/videos/")
	}
	b.WriteString(s.ItemID.Dashed())
	if s.SubProtocol == StreamProtocolHLS {
		b.WriteString("/master.m3u8")
	} else {
		b.WriteString("/stream")
		if s.Container != nil && *s.Container != "" {
			b.WriteByte('.')
			b.WriteString(*s.Container)
		}
	}
	queryStart := b.Len()
	appendValue := func(name, value string) {
		b.WriteByte('&')
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(value)
	}
	if s.DeviceProfileID != nil && *s.DeviceProfileID != "" {
		appendValue("DeviceProfileId", *s.DeviceProfileID)
	}
	if s.DeviceID != nil && *s.DeviceID != "" {
		appendValue("DeviceId", *s.DeviceID)
	}
	if id := s.MediaSourceID(); id != "" {
		appendValue("MediaSourceId", id)
	}
	if s.IsDirectStream() {
		appendValue("Static", "true")
	}
	if len(s.VideoCodecs) != 0 {
		appendValue("VideoCodec", strings.Join(s.VideoCodecs, ","))
	}
	if len(s.AudioCodecs) != 0 {
		appendValue("AudioCodec", strings.Join(s.AudioCodecs, ","))
	}
	if s.AudioStreamIndex != nil {
		appendValue("AudioStreamIndex", strconv.Itoa(*s.AudioStreamIndex))
	}
	if s.SubtitleStreamIndex != nil && (s.AlwaysBurnInSubtitleWhenTranscoding || s.SubtitleDeliveryMethod != SubtitleExternal) && *s.SubtitleStreamIndex != -1 {
		appendValue("SubtitleStreamIndex", strconv.Itoa(*s.SubtitleStreamIndex))
	}
	if s.VideoBitrate != nil {
		appendValue("VideoBitrate", strconv.Itoa(*s.VideoBitrate))
	}
	if s.AudioBitrate != nil {
		appendValue("AudioBitrate", strconv.Itoa(*s.AudioBitrate))
	}
	if s.AudioSampleRate != nil {
		appendValue("AudioSampleRate", strconv.Itoa(*s.AudioSampleRate))
	}
	if s.MaxFramerate != nil {
		appendValue("MaxFramerate", formatFloat32Invariant(*s.MaxFramerate))
	}
	if s.MaxWidth != nil {
		appendValue("MaxWidth", strconv.Itoa(*s.MaxWidth))
	}
	if s.MaxHeight != nil {
		appendValue("MaxHeight", strconv.Itoa(*s.MaxHeight))
	}
	if s.SubProtocol == StreamProtocolHLS {
		if s.Container != nil && *s.Container != "" {
			appendValue("SegmentContainer", *s.Container)
		}
		if s.SegmentLength != nil {
			appendValue("SegmentLength", strconv.Itoa(*s.SegmentLength))
		}
		if s.MinSegments != nil {
			appendValue("MinSegments", strconv.Itoa(*s.MinSegments))
		}
	} else if s.StartPositionTicks != 0 {
		appendValue("StartTimeTicks", strconv.FormatInt(s.StartPositionTicks, 10))
	}
	if s.PlaySessionID != nil && *s.PlaySessionID != "" {
		appendValue("PlaySessionId", *s.PlaySessionID)
	}
	if accessToken != "" {
		appendValue("ApiKey", url.QueryEscape(accessToken))
	}
	if s.MediaSource != nil && s.MediaSource.LiveStreamID != "" {
		appendValue("LiveStreamId", s.MediaSource.LiveStreamID)
	}
	if !s.IsDirectStream() {
		if s.RequireNonAnamorphic {
			appendValue("RequireNonAnamorphic", "True")
		}
		if s.TranscodingMaxAudioChannels != nil {
			appendValue("TranscodingMaxAudioChannels", strconv.Itoa(*s.TranscodingMaxAudioChannels))
		}
		if s.EnableSubtitlesInManifest {
			appendValue("EnableSubtitlesInManifest", "True")
		}
		if s.EnableMpegtsM2TsMode {
			appendValue("EnableMpegtsM2TsMode", "True")
		}
		if s.EstimateContentLength {
			appendValue("EstimateContentLength", "True")
		}
		if s.TranscodeSeekInfo != SeekAuto {
			appendValue("TranscodeSeekInfo", transcodeSeekInfoName(s.TranscodeSeekInfo))
		}
		if s.CopyTimestamps {
			appendValue("CopyTimestamps", "True")
		}
		appendValue("RequireAvc", strconv.FormatBool(s.RequireAvc))
		appendValue("EnableAudioVbrEncoding", strconv.FormatBool(s.EnableAudioVbrEncoding))
	}
	if s.MediaSource != nil && s.MediaSource.ETag != "" {
		appendValue("Tag", s.MediaSource.ETag)
	}
	if s.SubtitleStreamIndex != nil && s.SubtitleDeliveryMethod != SubtitleExternal {
		appendValue("SubtitleMethod", subtitleDeliveryMethodName(s.SubtitleDeliveryMethod))
	}
	if s.SubtitleStreamIndex != nil && s.SubtitleDeliveryMethod == SubtitleEmbed && len(s.SubtitleCodecs) != 0 {
		appendValue("SubtitleCodec", strings.Join(s.SubtitleCodecs, ","))
	}
	for _, option := range s.StreamOptions.ordered() {
		appendValue(option.name, strings.ReplaceAll(option.value, " ", ""))
	}
	if !s.IsDirectStream() && s.TranscodeReasons != 0 {
		appendValue("TranscodeReasons", transcodeReasonNames(s.TranscodeReasons))
	}
	if query != "" {
		b.WriteString(query)
	}
	result := b.String()
	if len(result) > queryStart {
		result = result[:queryStart] + "?" + result[queryStart+1:]
	}
	return result
}

func boolPointer(value bool) *bool { return &value }
func transcodeSeekInfoName(value TranscodeSeekInfo) string {
	if value == SeekAuto {
		return "Auto"
	}
	if value == SeekBytes {
		return "Bytes"
	}
	if value == SeekTime {
		return "Time"
	}
	return strconv.Itoa(int(value))
}

func subtitleDeliveryMethodName(value SubtitleDeliveryMethod) string {
	switch value {
	case SubtitleEncode:
		return "Encode"
	case SubtitleEmbed:
		return "Embed"
	case SubtitleExternal:
		return "External"
	case SubtitleHLS:
		return "Hls"
	case SubtitleDrop:
		return "Drop"
	default:
		return strconv.Itoa(int(value))
	}
}

func transcodeReasonNames(value TranscodeReason) string {
	ordered := []struct {
		flag TranscodeReason
		name string
	}{
		{ReasonContainerNotSupported, "ContainerNotSupported"}, {ReasonVideoCodecNotSupported, "VideoCodecNotSupported"}, {ReasonAudioCodecNotSupported, "AudioCodecNotSupported"}, {ReasonSubtitleCodecNotSupported, "SubtitleCodecNotSupported"}, {ReasonAudioIsExternal, "AudioIsExternal"}, {ReasonSecondaryAudioNotSupported, "SecondaryAudioNotSupported"}, {ReasonVideoProfileNotSupported, "VideoProfileNotSupported"}, {ReasonVideoLevelNotSupported, "VideoLevelNotSupported"}, {ReasonVideoResolutionNotSupported, "VideoResolutionNotSupported"}, {ReasonVideoBitDepthNotSupported, "VideoBitDepthNotSupported"}, {ReasonVideoFramerateNotSupported, "VideoFramerateNotSupported"}, {ReasonRefFramesNotSupported, "RefFramesNotSupported"}, {ReasonAnamorphicVideoNotSupported, "AnamorphicVideoNotSupported"}, {ReasonInterlacedVideoNotSupported, "InterlacedVideoNotSupported"}, {ReasonAudioChannelsNotSupported, "AudioChannelsNotSupported"}, {ReasonAudioProfileNotSupported, "AudioProfileNotSupported"}, {ReasonAudioSampleRateNotSupported, "AudioSampleRateNotSupported"}, {ReasonAudioBitDepthNotSupported, "AudioBitDepthNotSupported"}, {ReasonContainerBitrateExceedsLimit, "ContainerBitrateExceedsLimit"}, {ReasonVideoBitrateNotSupported, "VideoBitrateNotSupported"}, {ReasonAudioBitrateNotSupported, "AudioBitrateNotSupported"}, {ReasonUnknownVideoStreamInfo, "UnknownVideoStreamInfo"}, {ReasonUnknownAudioStreamInfo, "UnknownAudioStreamInfo"}, {ReasonDirectPlayError, "DirectPlayError"}, {ReasonVideoRangeTypeNotSupported, "VideoRangeTypeNotSupported"}, {ReasonVideoCodecTagNotSupported, "VideoCodecTagNotSupported"}, {ReasonStreamCountExceedsLimit, "StreamCountExceedsLimit"}, {ReasonVideoRotationNotSupported, "VideoRotationNotSupported"},
	}
	values := make([]string, 0, len(ordered))
	for _, item := range ordered {
		if value&item.flag != 0 {
			values = append(values, item.name)
		}
	}
	return strings.Join(values, ",")
}

func (r TranscodeReason) MarshalJSON() ([]byte, error) {
	if r == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(strings.Split(transcodeReasonNames(r), ","))
}

func (r *TranscodeReason) UnmarshalJSON(data []byte) error {
	var names []string
	if err := json.Unmarshal(data, &names); err == nil {
		var value TranscodeReason
		for _, name := range names {
			flag, ok := transcodeReasonFlag(name)
			if !ok {
				return fmt.Errorf("unknown TranscodeReason %q", name)
			}
			value |= flag
		}
		*r = value
		return nil
	}
	var raw uint32
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = TranscodeReason(raw)
	return nil
}

func transcodeReasonFlag(name string) (TranscodeReason, bool) {
	for _, item := range []struct {
		flag TranscodeReason
		name string
	}{
		{ReasonContainerNotSupported, "ContainerNotSupported"}, {ReasonVideoCodecNotSupported, "VideoCodecNotSupported"}, {ReasonAudioCodecNotSupported, "AudioCodecNotSupported"}, {ReasonSubtitleCodecNotSupported, "SubtitleCodecNotSupported"}, {ReasonAudioIsExternal, "AudioIsExternal"}, {ReasonSecondaryAudioNotSupported, "SecondaryAudioNotSupported"}, {ReasonVideoProfileNotSupported, "VideoProfileNotSupported"}, {ReasonVideoLevelNotSupported, "VideoLevelNotSupported"}, {ReasonVideoResolutionNotSupported, "VideoResolutionNotSupported"}, {ReasonVideoBitDepthNotSupported, "VideoBitDepthNotSupported"}, {ReasonVideoFramerateNotSupported, "VideoFramerateNotSupported"}, {ReasonRefFramesNotSupported, "RefFramesNotSupported"}, {ReasonAnamorphicVideoNotSupported, "AnamorphicVideoNotSupported"}, {ReasonInterlacedVideoNotSupported, "InterlacedVideoNotSupported"}, {ReasonAudioChannelsNotSupported, "AudioChannelsNotSupported"}, {ReasonAudioProfileNotSupported, "AudioProfileNotSupported"}, {ReasonAudioSampleRateNotSupported, "AudioSampleRateNotSupported"}, {ReasonAudioBitDepthNotSupported, "AudioBitDepthNotSupported"}, {ReasonContainerBitrateExceedsLimit, "ContainerBitrateExceedsLimit"}, {ReasonVideoBitrateNotSupported, "VideoBitrateNotSupported"}, {ReasonAudioBitrateNotSupported, "AudioBitrateNotSupported"}, {ReasonUnknownVideoStreamInfo, "UnknownVideoStreamInfo"}, {ReasonUnknownAudioStreamInfo, "UnknownAudioStreamInfo"}, {ReasonDirectPlayError, "DirectPlayError"}, {ReasonVideoRangeTypeNotSupported, "VideoRangeTypeNotSupported"}, {ReasonVideoCodecTagNotSupported, "VideoCodecTagNotSupported"}, {ReasonStreamCountExceedsLimit, "StreamCountExceedsLimit"}, {ReasonVideoRotationNotSupported, "VideoRotationNotSupported"},
	} {
		if item.name == name {
			return item.flag, true
		}
	}
	return 0, false
}

func (s StreamInfo) MarshalJSON() (result []byte, err error) {
	defer func() {
		if err != nil {
			return
		}
		if s.audioCodecsNull {
			result = bytes.ReplaceAll(result, []byte(`,"AudioCodecs":null`), nil)
		}
		if s.videoCodecsNull {
			result = bytes.ReplaceAll(result, []byte(`,"VideoCodecs":null`), nil)
		}
		if s.subtitleCodecsNull {
			result = bytes.ReplaceAll(result, []byte(`,"SubtitleCodecs":null`), nil)
		}
	}()
	var mediaSourceID *string
	if s.MediaSource != nil && !s.MediaSource.idNull && (s.MediaSource.ID != "" || s.MediaSource.idSet) {
		mediaSourceID = &s.MediaSource.ID
	}
	return json.Marshal(struct {
		ItemID                              dto.UUID                 `json:"ItemId"`
		PlayMethod                          PlayMethod               `json:"PlayMethod"`
		Context                             EncodingContext          `json:"Context"`
		MediaType                           DlnaProfileType          `json:"MediaType"`
		Container                           *string                  `json:"Container,omitempty"`
		SubProtocol                         MediaStreamProtocol      `json:"SubProtocol"`
		StartPositionTicks                  int64                    `json:"StartPositionTicks"`
		SegmentLength                       *int                     `json:"SegmentLength,omitempty"`
		MinSegments                         *int                     `json:"MinSegments,omitempty"`
		RequireAvc                          bool                     `json:"RequireAvc"`
		RequireNonAnamorphic                bool                     `json:"RequireNonAnamorphic"`
		CopyTimestamps                      bool                     `json:"CopyTimestamps"`
		EnableMpegtsM2TsMode                bool                     `json:"EnableMpegtsM2TsMode"`
		EnableSubtitlesInManifest           bool                     `json:"EnableSubtitlesInManifest"`
		AudioCodecs                         []string                 `json:"AudioCodecs"`
		VideoCodecs                         []string                 `json:"VideoCodecs"`
		AudioStreamIndex                    *int                     `json:"AudioStreamIndex,omitempty"`
		SubtitleStreamIndex                 *int                     `json:"SubtitleStreamIndex,omitempty"`
		TranscodingMaxAudioChannels         *int                     `json:"TranscodingMaxAudioChannels,omitempty"`
		GlobalMaxAudioChannels              *int                     `json:"GlobalMaxAudioChannels,omitempty"`
		AudioBitrate                        *int                     `json:"AudioBitrate,omitempty"`
		AudioSampleRate                     *int                     `json:"AudioSampleRate,omitempty"`
		VideoBitrate                        *int                     `json:"VideoBitrate,omitempty"`
		MaxWidth                            *int                     `json:"MaxWidth,omitempty"`
		MaxHeight                           *int                     `json:"MaxHeight,omitempty"`
		MaxFramerate                        *float32                 `json:"MaxFramerate,omitempty"`
		DeviceProfile                       *DeviceProfile           `json:"DeviceProfile,omitempty"`
		DeviceProfileID                     *string                  `json:"DeviceProfileId,omitempty"`
		DeviceID                            *string                  `json:"DeviceId,omitempty"`
		RunTimeTicks                        *int64                   `json:"RunTimeTicks,omitempty"`
		TranscodeSeekInfo                   TranscodeSeekInfo        `json:"TranscodeSeekInfo"`
		EstimateContentLength               bool                     `json:"EstimateContentLength"`
		MediaSource                         *MediaSourceInfo         `json:"MediaSource,omitempty"`
		SubtitleCodecs                      []string                 `json:"SubtitleCodecs"`
		SubtitleDeliveryMethod              SubtitleDeliveryMethod   `json:"SubtitleDeliveryMethod"`
		SubtitleFormat                      *string                  `json:"SubtitleFormat,omitempty"`
		PlaySessionID                       *string                  `json:"PlaySessionId,omitempty"`
		TranscodeReasons                    TranscodeReason          `json:"TranscodeReasons"`
		StreamOptions                       StreamOptions            `json:"StreamOptions"`
		MediaSourceID                       *string                  `json:"MediaSourceId,omitempty"`
		EnableAudioVbrEncoding              bool                     `json:"EnableAudioVbrEncoding"`
		AlwaysBurnInSubtitleWhenTranscoding bool                     `json:"AlwaysBurnInSubtitleWhenTranscoding"`
		IsDirectStream                      bool                     `json:"IsDirectStream"`
		TargetAudioStream                   *MediaStream             `json:"TargetAudioStream,omitempty"`
		TargetVideoStream                   *MediaStream             `json:"TargetVideoStream,omitempty"`
		TargetAudioSampleRate               *int                     `json:"TargetAudioSampleRate,omitempty"`
		TargetAudioBitDepth                 *int                     `json:"TargetAudioBitDepth,omitempty"`
		TargetVideoBitDepth                 *int                     `json:"TargetVideoBitDepth,omitempty"`
		TargetRefFrames                     *int                     `json:"TargetRefFrames,omitempty"`
		TargetFramerate                     *float32                 `json:"TargetFramerate,omitempty"`
		TargetVideoLevel                    *float64                 `json:"TargetVideoLevel,omitempty"`
		TargetPacketLength                  *int                     `json:"TargetPacketLength,omitempty"`
		TargetVideoProfile                  *string                  `json:"TargetVideoProfile,omitempty"`
		TargetVideoRangeType                VideoRangeType           `json:"TargetVideoRangeType"`
		TargetVideoCodecTag                 *string                  `json:"TargetVideoCodecTag,omitempty"`
		TargetAudioBitrate                  *int                     `json:"TargetAudioBitrate,omitempty"`
		TargetAudioChannels                 *int                     `json:"TargetAudioChannels,omitempty"`
		TargetAudioCodec                    []string                 `json:"TargetAudioCodec"`
		TargetVideoCodec                    []string                 `json:"TargetVideoCodec"`
		TargetSize                          *int64                   `json:"TargetSize,omitempty"`
		TargetVideoBitrate                  *int                     `json:"TargetVideoBitrate,omitempty"`
		TargetTimestamp                     TransportStreamTimestamp `json:"TargetTimestamp"`
		TargetTotalBitrate                  int                      `json:"TargetTotalBitrate"`
		IsTargetAnamorphic                  *bool                    `json:"IsTargetAnamorphic,omitempty"`
		IsTargetInterlaced                  *bool                    `json:"IsTargetInterlaced,omitempty"`
		IsTargetAVC                         *bool                    `json:"IsTargetAVC,omitempty"`
		TargetWidth                         *int                     `json:"TargetWidth,omitempty"`
		TargetHeight                        *int                     `json:"TargetHeight,omitempty"`
		TargetVideoStreamCount              *int                     `json:"TargetVideoStreamCount,omitempty"`
		TargetAudioStreamCount              *int                     `json:"TargetAudioStreamCount,omitempty"`
	}{s.ItemID, s.PlayMethod, s.Context, s.MediaType, s.Container, s.SubProtocol, s.StartPositionTicks, s.SegmentLength, s.MinSegments, s.RequireAvc, s.RequireNonAnamorphic, s.CopyTimestamps, s.EnableMpegtsM2TsMode, s.EnableSubtitlesInManifest, s.AudioCodecs, s.VideoCodecs, s.AudioStreamIndex, s.SubtitleStreamIndex, s.TranscodingMaxAudioChannels, s.GlobalMaxAudioChannels, s.AudioBitrate, s.AudioSampleRate, s.VideoBitrate, s.MaxWidth, s.MaxHeight, s.MaxFramerate, s.DeviceProfile, s.DeviceProfileID, s.DeviceID, s.RunTimeTicks, s.TranscodeSeekInfo, s.EstimateContentLength, s.MediaSource, s.SubtitleCodecs, s.SubtitleDeliveryMethod, s.SubtitleFormat, s.PlaySessionID, s.TranscodeReasons, s.StreamOptions, mediaSourceID, s.EnableAudioVbrEncoding, s.AlwaysBurnInSubtitleWhenTranscoding, s.IsDirectStream(), s.TargetAudioStream(), s.TargetVideoStream(), s.TargetAudioSampleRate(), s.TargetAudioBitDepth(), s.TargetVideoBitDepth(), s.TargetRefFrames(), s.TargetFramerate(), s.TargetVideoLevel(), s.TargetPacketLength(), s.TargetVideoProfile(), s.TargetVideoRangeType(), s.TargetVideoCodecTag(), s.TargetAudioBitrate(), s.TargetAudioChannels(), s.TargetAudioCodec(), s.TargetVideoCodec(), s.TargetSize(), s.TargetVideoBitrate(), s.TargetTimestamp(), s.TargetTotalBitrate(), s.IsTargetAnamorphic(), s.IsTargetInterlaced(), s.IsTargetAVC(), s.TargetWidth(), s.TargetHeight(), s.TargetVideoStreamCount(), s.TargetAudioStreamCount()})
}
