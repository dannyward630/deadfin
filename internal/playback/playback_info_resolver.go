// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// PlaybackInfoOption applies one pure PlaybackInfo request setting.
type PlaybackInfoOption func(*PlaybackInfoRequest)

// NewPlaybackInfoRequest returns the normal HTTP-playback defaults.
func NewPlaybackInfoRequest() PlaybackInfoRequest {
	return PlaybackInfoRequest{
		MediaType:            DlnaVideo,
		EnableDirectPlay:     true,
		EnableDirectStream:   true,
		EnableTranscoding:    true,
		AllowVideoStreamCopy: true,
		AllowAudioStreamCopy: true,
		DeviceProfile:        &DeviceProfile{},
		Network:              PlaybackNetwork{IsLAN: true},
	}
}

func WithMediaSourceID(value string) PlaybackInfoOption {
	return func(request *PlaybackInfoRequest) { request.MediaSourceID = value }
}

func WithSession(value PlaybackSession) PlaybackInfoOption {
	return func(request *PlaybackInfoRequest) { request.Session = value }
}

func WithDeviceProfile(value *DeviceProfile) PlaybackInfoOption {
	return func(request *PlaybackInfoRequest) { request.DeviceProfile = value }
}

func WithForceDirectStream(value bool) PlaybackInfoOption {
	return func(request *PlaybackInfoRequest) { request.ForceDirectStream = value }
}

func WithPermissions(value PlaybackPermissions) PlaybackInfoOption {
	return func(request *PlaybackInfoRequest) { request.Permissions = value }
}

func WithNetwork(value PlaybackNetwork) PlaybackInfoOption {
	return func(request *PlaybackInfoRequest) { request.Network = value }
}

func WithMaxStreamingBitrate(value *int) PlaybackInfoOption {
	return func(request *PlaybackInfoRequest) { request.MaxStreamingBitrate = value }
}

func WithSubtitleStreamIndex(value *int) PlaybackInfoOption {
	return func(request *PlaybackInfoRequest) { request.SubtitleStreamIndex = value }
}

// WithNetwork returns a request with the supplied client-locality context.
func (request PlaybackInfoRequest) WithNetwork(value PlaybackNetwork) PlaybackInfoRequest {
	request.Network = value
	return request
}

// ResolvePlaybackInfo applies the selected device profile and caller policy to
// already-assembled sources. It does not perform source lookup, session minting,
// persistence, or transcoder I/O.
func ResolvePlaybackInfo(request PlaybackInfoRequest, sources []MediaSourceInfo) (PlaybackInfoResponse, error) {
	itemID := request.ItemID.Dashed()
	profile := request.DeviceProfile
	if profile == nil {
		profile = &DeviceProfile{}
	}
	candidates := clonePlaybackSources(sources, request.MediaSourceID)
	if len(candidates) == 0 {
		return noCompatiblePlaybackInfo(), nil
	}

	resolved := make([]resolvedPlaybackSource, 0, len(candidates))
	for index := range candidates {
		source, method, ok, err := resolvePlaybackSource(request, itemID, profile, candidates[index])
		if err != nil {
			return PlaybackInfoResponse{}, err
		}
		if ok {
			resolved = append(resolved, resolvedPlaybackSource{source: source, playMethod: method})
		}
	}
	if len(resolved) == 0 {
		return noCompatiblePlaybackInfo(), nil
	}

	bitrate := playbackBitrate(request, profile)
	sort.SliceStable(resolved, func(left, right int) bool {
		return playbackSourceLess(resolved[left], resolved[right], bitrate, itemID)
	})
	response := PlaybackInfoResponse{
		MediaSources:  make([]MediaSourceInfo, len(resolved)),
		PlaySessionID: request.Session.ID,
	}
	for index := range resolved {
		response.MediaSources[index] = resolved[index].source
	}
	return response, nil
}

type resolvedPlaybackSource struct {
	source     MediaSourceInfo
	playMethod PlayMethod
}

func playbackSourceLess(left, right resolvedPlaybackSource, bitrate *int, itemID string) bool {
	leftPreferred := strings.EqualFold(left.source.ID, itemID)
	rightPreferred := strings.EqualFold(right.source.ID, itemID)
	if leftPreferred != rightPreferred {
		return leftPreferred
	}
	leftFileDirect := left.playMethod == PlayMethodDirectPlay && left.source.Protocol == MediaProtocolFile
	rightFileDirect := right.playMethod == PlayMethodDirectPlay && right.source.Protocol == MediaProtocolFile
	if leftFileDirect != rightFileDirect {
		return leftFileDirect
	}
	leftDirect := left.playMethod == PlayMethodDirectPlay || left.playMethod == PlayMethodDirectStream
	rightDirect := right.playMethod == PlayMethodDirectPlay || right.playMethod == PlayMethodDirectStream
	if leftDirect != rightDirect {
		return leftDirect
	}
	leftFile, rightFile := left.source.Protocol == MediaProtocolFile, right.source.Protocol == MediaProtocolFile
	if leftFile != rightFile {
		return leftFile
	}
	return playbackBitrateRank(left.source.Bitrate, bitrate) < playbackBitrateRank(right.source.Bitrate, bitrate)
}

func playbackBitrateRank(value, limit *int) int {
	if limit == nil || value == nil {
		return 1
	}
	if *value <= *limit {
		return 0
	}
	return 2
}

func resolvePlaybackSource(request PlaybackInfoRequest, itemID string, profile *DeviceProfile, source MediaSourceInfo) (MediaSourceInfo, PlayMethod, bool, error) {
	forceRemote := source.IsRemote && request.Permissions.ForceRemoteSourceTranscoding
	support := playbackTranscoderSupport{canTranscodeVideo: request.CanTranscodeVideo, canExtractSubtitles: request.CanExtractSubtitles}
	canTranscode := request.EnableTranscoding && source.SupportsTranscoding && len(profile.TranscodingProfiles) != 0 && playbackTranscodingAllowed(request.Permissions, request.MediaType)
	if video := source.VideoStream(); video != nil && !support.CanTranscodeVideo(*video) {
		canTranscode = false
	}
	if !canTranscode {
		source.SupportsTranscoding = false
	}

	options := NewMediaOptions()
	options.ItemID = itemID
	options.MediaSourceID = request.MediaSourceID
	options.MediaSources = []MediaSourceInfo{source}
	options.Profile = profile
	options.Context = ContextStreaming
	if request.MediaSourceID != "" && strings.EqualFold(request.MediaSourceID, source.ID) {
		options.AudioStreamIndex = request.AudioStreamIndex
		options.SubtitleStreamIndex = request.SubtitleStreamIndex
	}
	options.MaxAudioChannels = request.MaxAudioChannels
	options.MaxBitrate = playbackBitrate(request, profile)
	options.AlwaysBurnInSubtitleWhenTranscoding = request.AlwaysBurnInSubtitleWhenTranscoding
	options.DeviceID = request.Session.DeviceID
	if options.DeviceID == "" {
		options.DeviceID = request.DeviceID
	}
	options.AllowAudioStreamCopy = request.AllowAudioStreamCopy && !forceRemote
	options.AllowVideoStreamCopy = request.AllowVideoStreamCopy && !forceRemote
	options.EnableDirectPlay = request.EnableDirectPlay && source.SupportsDirectPlay && !forceRemote
	options.EnableDirectStream = request.EnableDirectStream && source.SupportsDirectStream && request.ForceDirectStream && request.Permissions.EnableRemuxing && !forceRemote
	options.ForceDirectStream = options.EnableDirectStream
	builder := NewStreamBuilder(support)
	var stream *StreamInfo
	var err error
	if request.MediaType == DlnaAudio {
		stream, err = builder.GetOptimalAudioStream(&options)
	} else {
		stream, err = builder.GetOptimalVideoStream(&options)
	}
	if err != nil {
		return MediaSourceInfo{}, PlayMethodTranscode, false, err
	}
	if stream == nil || stream.Container == nil || stream.MediaSource == nil {
		return MediaSourceInfo{}, PlayMethodTranscode, false, nil
	}

	resolved := *stream.MediaSource
	resolved.SupportsDirectPlay = stream.PlayMethod == PlayMethodDirectPlay
	// A direct-play profile remains streamable. The stock endpoint reports both
	// capabilities for that selected source; only a transcode clears them.
	resolved.SupportsDirectStream = stream.PlayMethod != PlayMethodTranscode
	resolved.SupportsTranscoding = canTranscode
	playSessionID := request.Session.ID
	stream.PlaySessionID = &playSessionID
	if stream.PlayMethod != PlayMethodDirectPlay {
		if request.StartTimeTicks != nil {
			stream.StartPositionTicks = *request.StartTimeTicks
		}
		query := ""
		if forceRemote {
			query = "&allowVideoStreamCopy=false&allowAudioStreamCopy=false"
		} else {
			if !request.AllowVideoStreamCopy {
				query += "&allowVideoStreamCopy=false"
			}
			if !request.AllowAudioStreamCopy {
				query += "&allowAudioStreamCopy=false"
			}
		}
		if stream.AlwaysBurnInSubtitleWhenTranscoding {
			query += "&alwaysBurnInSubtitleWhenTranscoding=true"
		}
		resolved.TranscodingURL = stream.ToURL("", request.Session.AccessToken, query)
	}
	if !canTranscode && stream.PlayMethod == PlayMethodTranscode {
		resolved.TranscodingURL = ""
	}
	if stream.AudioStreamIndex != nil {
		value := *stream.AudioStreamIndex
		resolved.DefaultAudioStreamIndex = &value
	}
	if stream.SubtitleStreamIndex != nil {
		value := *stream.SubtitleStreamIndex
		resolved.DefaultSubtitleStreamIndex = &value
	}
	setPlaybackDeliveryURLs(&resolved, itemID, stream, profile, support, request.Session.AccessToken)
	return resolved, stream.PlayMethod, true, nil
}

func noCompatiblePlaybackInfo() PlaybackInfoResponse {
	return PlaybackInfoResponse{MediaSources: []MediaSourceInfo{}, ErrorCode: PlaybackInfoNoCompatibleStream}
}

func playbackTranscodingAllowed(permissions PlaybackPermissions, mediaType DlnaProfileType) bool {
	if mediaType == DlnaAudio {
		return permissions.EnableAudioTranscoding || permissions.EnableRemuxing
	}
	return permissions.EnableVideoTranscoding || permissions.EnableAudioTranscoding || permissions.EnableRemuxing
}

func playbackBitrate(request PlaybackInfoRequest, profile *DeviceProfile) *int {
	limit := profile.MaxStreamingBitrate
	if request.MaxStreamingBitrate != nil && (limit == nil || *request.MaxStreamingBitrate < *limit) {
		limit = request.MaxStreamingBitrate
	}
	if !request.Network.IsLAN && request.Permissions.RemoteBitrateLimit != nil && (limit == nil || *request.Permissions.RemoteBitrateLimit < *limit) {
		limit = request.Permissions.RemoteBitrateLimit
	}
	return limit
}

// setPlaybackDeliveryURLs ports the attachment loop of
// MediaInfoHelper.SetDeviceSpecificData and SetDeviceSpecificSubtitleInfo:
// every subtitle stream gets the delivery method of its profile, and every
// External stream gets its URL (StreamInfo.GetSubtitleStreamInfo).
func setPlaybackDeliveryURLs(source *MediaSourceInfo, itemID string, stream *StreamInfo, profile *DeviceProfile, support playbackTranscoderSupport, accessToken string) {
	for index := range source.MediaAttachments {
		source.MediaAttachments[index].DeliveryURL = "/Videos/" + itemID + "/" + source.ID + "/Attachments/" + integerString(source.MediaAttachments[index].Index)
	}
	// HLS keeps timestamps, so it takes the full subtitle stream.
	startPositionTicks := int64(0)
	if stream.SubProtocol != StreamProtocolHLS && stream.PlayMethod == PlayMethodTranscode && !stream.CopyTimestamps {
		startPositionTicks = stream.StartPositionTicks
	}
	for index := range source.MediaStreams {
		subtitle := &source.MediaStreams[index]
		if subtitle.Type != StreamTypeSubtitle {
			continue
		}
		matched := GetSubtitleProfile(source, subtitle, profile.SubtitleProfiles, stream.PlayMethod, support, *stream.Container, stream.SubProtocol)
		method := matched.Method
		subtitle.DeliveryMethod = &method
		if method != SubtitleExternal {
			continue
		}
		external := false
		if subtitle.IsExternal && subtitle.SupportsExternalStream && strings.EqualFold(subtitle.Codec, matched.Format) && isHTTPURL(subtitle.Path) {
			external = true
			subtitle.DeliveryURL = subtitle.Path
		} else {
			subtitle.DeliveryURL = "/Videos/" + itemID + "/" + source.ID + "/Subtitles/" + integerString(subtitle.Index) + "/" + strconv.FormatInt(startPositionTicks, 10) + "/Stream." + matched.Format
			if accessToken != "" {
				subtitle.DeliveryURL += "?ApiKey=" + url.QueryEscape(accessToken)
			}
		}
		subtitle.IsExternalURL = &external
	}
}

func isHTTPURL(value string) bool {
	return strings.HasPrefix(strings.ToLower(value), "http://") || strings.HasPrefix(strings.ToLower(value), "https://")
}

func clonePlaybackSources(sources []MediaSourceInfo, requestedID string) []MediaSourceInfo {
	result := make([]MediaSourceInfo, 0, len(sources))
	for index := range sources {
		if requestedID != "" && !strings.EqualFold(sources[index].ID, requestedID) {
			continue
		}
		copy := sources[index]
		if sources[index].MediaStreams != nil {
			copy.MediaStreams = append(make([]MediaStream, 0, len(sources[index].MediaStreams)), sources[index].MediaStreams...)
		}
		if sources[index].MediaAttachments != nil {
			copy.MediaAttachments = append(make([]MediaAttachment, 0, len(sources[index].MediaAttachments)), sources[index].MediaAttachments...)
		}
		if sources[index].Formats != nil {
			copy.Formats = append(make([]string, 0, len(sources[index].Formats)), sources[index].Formats...)
		}
		if sources[index].RequiredHTTPHeaders != nil {
			copy.RequiredHTTPHeaders = make(map[string]string, len(sources[index].RequiredHTTPHeaders))
			for key, value := range sources[index].RequiredHTTPHeaders {
				copy.RequiredHTTPHeaders[key] = value
			}
		}
		result = append(result, copy)
	}
	return result
}

type playbackTranscoderSupport struct {
	canTranscodeVideo   func(MediaStream) bool
	canExtractSubtitles func(string) bool
}

func (playbackTranscoderSupport) CanEncodeToAudioCodec(string) bool    { return true }
func (playbackTranscoderSupport) CanEncodeToSubtitleCodec(string) bool { return false }

// CanExtractSubtitles reports EncodingOptions.EnableSubtitleExtraction
// (MediaEncoder.CanExtractSubtitles). Without the seam it reports false.
func (support playbackTranscoderSupport) CanExtractSubtitles(codec string) bool {
	return support.canExtractSubtitles != nil && support.canExtractSubtitles(codec)
}

// CanTranscodeVideo reports whether the encoder can transcode the video
// stream. Without an encoder seam only SDR sources transcode (D-32).
func (support playbackTranscoderSupport) CanTranscodeVideo(video MediaStream) bool {
	if support.canTranscodeVideo != nil {
		return support.canTranscodeVideo(video)
	}
	return video.VideoRangeType() == VideoRangeSDR
}

func integerString(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	unsigned := uint(value)
	if negative {
		unsigned = uint(-value)
	}
	var buffer [20]byte
	end := len(buffer)
	for unsigned > 0 {
		end--
		buffer[end] = byte(unsigned%10) + '0'
		unsigned /= 10
	}
	if negative {
		end--
		buffer[end] = '-'
	}
	return string(buffer[end:])
}
