// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"context"
	"net/netip"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// universalAudioDefaultBitrate is the DeviceProfile default for
// MaxStreamingBitrate, MaxStaticBitrate, and MaxStaticMusicBitrate
// (DeviceProfile.cs:30-45). universalAudioMusicBitrate is the
// MusicStreamingTranscodingBitrate default.
const (
	universalAudioDefaultBitrate = 8_000_000
	universalAudioMusicBitrate   = 128_000
)

// UniversalAudioRequest contains the capabilities that a client sends to
// /Audio/{itemId}/universal (UniversalAudioController.GetUniversalAudioStream).
type UniversalAudioRequest struct {
	Containers               []string
	TranscodingContainer     string
	TranscodingProtocol      *MediaStreamProtocol
	AudioCodec               string
	TranscodingAudioChannels *int
	MaxStreamingBitrate      *int
	MaxAudioChannels         *int
	MaxAudioSampleRate       *int
	MaxAudioBitDepth         *int
}

// UniversalAudioSourceService resolves the source selected by universal audio.
// Its case-insensitive identifier matching is deliberately distinct from the
// exact StaticSource contract used by direct-file endpoints.
type UniversalAudioSourceService interface {
	UniversalAudioSource(context.Context, dto.UUID, dto.UUID, string) (MediaSourceInfo, error)
}

// UniversalAudioDecisionService resolves the source and the delivery that
// universal audio uses: the static file, a progressive transcode, or HLS.
type UniversalAudioDecisionService interface {
	UniversalAudio(context.Context, UniversalAudioQuery) (UniversalAudioDecision, error)
}

// UniversalAudioQuery identifies the item, the user, and the client of one
// universal-audio request.
type UniversalAudioQuery struct {
	UserID        dto.UUID
	ItemID        dto.UUID
	MediaSourceID string
	ClientAddress netip.Addr
	Request       UniversalAudioRequest
}

// UniversalAudioDecision is the outcome of the universal-audio negotiation.
// Static selects the source file. Otherwise Protocol selects a progressive
// (http) or HLS transcode into TranscodingContainer.
type UniversalAudioDecision struct {
	Source               MediaSourceInfo
	PlaySessionID        string
	Static               bool
	Protocol             MediaStreamProtocol
	TranscodingContainer string
	TranscodeReasons     TranscodeReason
}

// TranscodeReasonNames returns the comma-separated TranscodeReason names, as
// in a TranscodingUrl.
func (decision UniversalAudioDecision) TranscodeReasonNames() string {
	return transcodeReasonNames(decision.TranscodeReasons)
}

// DecideUniversalAudio ports the negotiation of
// UniversalAudioController.GetUniversalAudioStream
// (UniversalAudioController.cs:113-160) for one source:
// MediaInfoHelper.SetDeviceSpecificData with direct play, transcoding, and
// stream copy enabled and direct stream disabled, then the static test
// "SupportsDirectStream". maxBitrate is the client limit after the user's
// remote limit (MediaInfoHelper.GetMaxBitrate); nil selects the client limit
// or the profile default.
func DecideUniversalAudio(request UniversalAudioRequest, source MediaSourceInfo, itemID string, maxBitrate *int) (UniversalAudioDecision, error) {
	profile := NewUniversalAudioDeviceProfile(request)
	if maxBitrate == nil {
		maxBitrate = universalAudioMaxBitrate(PlaybackInfoRequest{MaxStreamingBitrate: request.MaxStreamingBitrate, Network: PlaybackNetwork{IsLAN: true}})
	}
	candidate := source
	candidate.TranscodingContainer = request.TranscodingContainer
	if request.TranscodingProtocol != nil {
		candidate.TranscodingSubProtocol = *request.TranscodingProtocol
	}
	options := NewMediaOptions()
	options.ItemID = itemID
	options.Profile = &profile
	options.MediaSources = []MediaSourceInfo{candidate}
	options.MaxAudioChannels = request.MaxAudioChannels
	options.MaxBitrate = maxBitrate
	options.AllowAudioStreamCopy = true
	options.AllowVideoStreamCopy = true
	// SetDeviceSpecificData: "direct-stream http streaming is currently broken".
	options.EnableDirectStream = false
	stream, err := NewStreamBuilder(playbackTranscoderSupport{}).GetOptimalAudioStream(&options)
	if err != nil {
		return UniversalAudioDecision{}, err
	}
	decision := UniversalAudioDecision{Source: candidate, Static: candidate.SupportsDirectStream, Protocol: candidate.TranscodingSubProtocol, TranscodingContainer: candidate.TranscodingContainer}
	if stream == nil || stream.MediaSource == nil {
		// Without a stream, SetDeviceSpecificData leaves the source flags.
		return decision, nil
	}
	resolved := *stream.MediaSource
	decision.Source = resolved
	// With direct stream disabled, SupportsDirectStream means direct play.
	decision.Static = stream.PlayMethod == PlayMethodDirectPlay
	decision.Protocol = resolved.TranscodingSubProtocol
	decision.TranscodingContainer = resolved.TranscodingContainer
	decision.TranscodeReasons = stream.TranscodeReasons
	return decision, nil
}

// NewUniversalAudioDeviceProfile ports UniversalAudioController.GetDeviceProfile
// (UniversalAudioController.cs:269-366): one direct-play profile per
// container entry, one streaming transcoding profile, and one codec profile
// for the audio limits. The bitrates keep the DeviceProfile defaults.
func NewUniversalAudioDeviceProfile(request UniversalAudioRequest) DeviceProfile {
	defaultBitrate, musicBitrate := universalAudioDefaultBitrate, universalAudioMusicBitrate
	profile := DeviceProfile{
		MaxStreamingBitrate:              &defaultBitrate,
		MaxStaticBitrate:                 &defaultBitrate,
		MusicStreamingTranscodingBitrate: &musicBitrate,
		MaxStaticMusicBitrate:            &defaultBitrate,
	}
	for _, requested := range request.Containers {
		parts := strings.Split(requested, "|")
		count := 0
		for _, part := range parts {
			if part != "" {
				parts[count] = part
				count++
			}
		}
		parts = parts[:count]
		if len(parts) == 0 {
			continue
		}
		profile.DirectPlayProfiles = append(profile.DirectPlayProfiles, DirectPlayProfile{
			Type:       DlnaAudio,
			Container:  parts[0],
			AudioCodec: strings.Join(parts[1:], ","),
		})
	}
	transcoding := TranscodingProfile{
		Type:       DlnaAudio,
		Context:    ContextStreaming,
		Container:  firstNonEmpty(request.TranscodingContainer, "mp3"),
		AudioCodec: firstNonEmpty(request.AudioCodec, "mp3"),
		Protocol:   StreamProtocolHTTP,
	}
	if request.TranscodingProtocol != nil {
		transcoding.Protocol = *request.TranscodingProtocol
	}
	if request.TranscodingAudioChannels != nil {
		transcoding.MaxAudioChannels = strconv.Itoa(*request.TranscodingAudioChannels)
	}
	profile.TranscodingProfiles = []TranscodingProfile{transcoding}
	if conditions := universalAudioConditions(request); len(conditions) != 0 {
		profile.CodecProfiles = append(profile.CodecProfiles, CodecProfile{
			Type:       CodecAudio,
			Container:  strings.Join(request.Containers, ","),
			Conditions: conditions,
		})
	}
	return profile
}

func universalAudioConditions(request UniversalAudioRequest) []ProfileCondition {
	limits := []struct {
		property ConditionProperty
		value    *int
	}{
		{PropAudioSampleRate, request.MaxAudioSampleRate},
		{PropAudioBitDepth, request.MaxAudioBitDepth},
		{PropAudioChannels, request.MaxAudioChannels},
	}
	conditions := make([]ProfileCondition, 0, len(limits))
	for _, limit := range limits {
		if limit.value != nil {
			conditions = append(conditions, ProfileCondition{Condition: ConditionLessThanEqual, Property: limit.property, Value: strconv.Itoa(*limit.value)})
		}
	}
	return conditions
}

// UniversalAudio selects the universal-audio source, applies the user's
// remote bitrate limit, mints the play session, and negotiates the delivery
// (UniversalAudioController.cs:96-160).
func (service *PlaybackInfoApplication) UniversalAudio(ctx context.Context, query UniversalAudioQuery) (UniversalAudioDecision, error) {
	source, err := service.UniversalAudioSource(ctx, query.UserID, query.ItemID, query.MediaSourceID)
	if err != nil {
		return UniversalAudioDecision{}, err
	}
	request := PlaybackInfoRequest{MaxStreamingBitrate: query.Request.MaxStreamingBitrate, Network: PlaybackNetwork{IsLAN: true}}
	if service.permissions != nil && !query.UserID.IsNil() {
		request.Permissions, err = service.permissions.PlaybackPermissions(ctx, query.UserID)
		if err != nil {
			return UniversalAudioDecision{}, err
		}
	}
	if service.network != nil {
		request.Network = PlaybackNetwork{IsLAN: !query.ClientAddress.IsValid() || service.network.IsLAN(query.ClientAddress)}
	}
	decision, err := DecideUniversalAudio(query.Request, source, query.ItemID.Dashed(), universalAudioMaxBitrate(request))
	if err != nil {
		return UniversalAudioDecision{}, err
	}
	decision.PlaySessionID, err = service.newSessionID()
	if err != nil {
		return UniversalAudioDecision{}, err
	}
	return decision, nil
}

// universalAudioMaxBitrate ports "maxStreamingBitrate ??
// deviceProfile.MaxStreamingBitrate" (UniversalAudioController.cs:115) and
// MediaInfoHelper.GetMaxBitrate (MediaInfoHelper.cs:504-526): a client limit
// above the profile default stays, and a remote client takes the lower of it
// and the remote limit.
func universalAudioMaxBitrate(request PlaybackInfoRequest) *int {
	limit := universalAudioDefaultBitrate
	if request.MaxStreamingBitrate != nil {
		limit = *request.MaxStreamingBitrate
	}
	if remote := request.Permissions.RemoteBitrateLimit; !request.Network.IsLAN && remote != nil && *remote > 0 && *remote < limit {
		limit = *remote
	}
	return &limit
}
