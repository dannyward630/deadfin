// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"context"
	"fmt"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/encoding"
)

// ProgressiveAudioStreamer is the handler-facing progressive audio seam
// (AudioHelper.GetAudioStream without static).
type ProgressiveAudioStreamer interface {
	OpenProgressiveAudio(context.Context, ProgressiveAudioRequest) (ProgressiveResponse, error)
}

// ProgressiveAudioRequest contains the StreamingRequestDto facts that a
// progressive audio transcode uses.
type ProgressiveAudioRequest struct {
	UserID        dto.UUID
	ItemID        dto.UUID
	MediaSourceID string
	// Container is the output container from the route extension or the
	// Container query value. Empty infers it from the audio codec.
	Container string

	DeviceID      string
	PlaySessionID string
	UserAgent     string

	// AudioCodec is the comma-separated list of acceptable codecs.
	AudioCodec       string
	AudioStreamIndex *int
	StartTimeTicks   int64

	AudioBitrate                *int
	AudioSampleRate             *int
	MaxAudioChannels            *int
	AudioChannels               *int
	TranscodingMaxAudioChannels *int
	MaxAudioBitDepth            *int
	StreamOptions               StreamOptions
	EnableAudioVbrEncoding      bool
	Head                        bool
}

// OpenProgressiveAudio ports the transcode branch of AudioHelper.GetAudioStream
// (AudioHelper.cs:134-145) with StreamingHelpers.GetStreamingState
// (StreamingHelpers.cs:58-195) for a local audio source.
func (service *ProgressiveService) OpenProgressiveAudio(ctx context.Context, request ProgressiveAudioRequest) (ProgressiveResponse, error) {
	if err := validateProgressiveAudioRequest(request); err != nil {
		return ProgressiveResponse{}, err
	}
	if service == nil || service.sources == nil || service.transcodeDir == "" {
		return ProgressiveResponse{}, fmt.Errorf("%w: incomplete progressive service configuration", ErrProgressiveBackend)
	}
	source, err := service.sources.StaticSource(ctx, request.UserID, request.ItemID, request.MediaSourceID)
	if err != nil {
		return ProgressiveResponse{}, err
	}
	if err := validateHLSSource(source, HLSRequest{}); err != nil {
		return ProgressiveResponse{}, err
	}
	audio, err := hlsSelectedAudioStream(source, ProgressiveRequest{AudioStreamIndex: request.AudioStreamIndex})
	if err != nil {
		return ProgressiveResponse{}, err
	}
	if audio == nil {
		return ProgressiveResponse{}, fmt.Errorf("%w: source has no audio stream", ErrProgressiveUnsupported)
	}
	if !hasCanonicalSourceAuthority(source) {
		return ProgressiveResponse{}, fmt.Errorf("%w: media source has incomplete canonical file authority", ErrProgressiveUnsupported)
	}

	caps := service.currentCapabilities()
	job := progressiveAudioJob(source, audio, request, caps)
	job.EnableAudioVbr = request.EnableAudioVbrEncoding && service.audioVbr != nil && service.audioVbr()
	extension := "." + job.OutputContainer
	identity := job
	identity.OutputPath = progressiveRenditionIdentityOutputPath
	identityArgs, err := encoding.BuildProgressiveAudioArgs(identity, caps)
	if err != nil {
		return ProgressiveResponse{}, fmt.Errorf("%w: build progressive audio identity: %v", ErrProgressiveUnsupported, err)
	}
	job.OutputPath = progressiveOutputPath(service.transcodeDir, request.UserAgent, request.DeviceID, request.PlaySessionID, source.SourceRevision, identityArgs, extension)
	args, err := encoding.BuildProgressiveAudioArgs(job, caps)
	if err != nil {
		return ProgressiveResponse{}, fmt.Errorf("%w: build progressive audio job: %v", ErrProgressiveUnsupported, err)
	}

	response := ProgressiveResponse{ContentType: progressiveAudioContentType(job.OutputContainer), OutputPath: job.OutputPath}
	if request.Head {
		return response, nil
	}
	return service.openProcess(ctx, response, source, args, request.PlaySessionID, request.DeviceID)
}

func validateProgressiveAudioRequest(request ProgressiveAudioRequest) error {
	if request.ItemID == dto.NilUUID {
		return fmt.Errorf("%w: item ID is required", ErrProgressiveInvalid)
	}
	if request.StartTimeTicks < 0 {
		return fmt.Errorf("%w: start ticks must not be negative", ErrProgressiveInvalid)
	}
	if request.AudioStreamIndex != nil && *request.AudioStreamIndex < -1 {
		return fmt.Errorf("%w: invalid audio stream index", ErrProgressiveInvalid)
	}
	if request.Container != "" && !progressiveAudioContainerSyntax(request.Container) {
		return fmt.Errorf("%w: output container %q", ErrProgressiveInvalid, request.Container)
	}
	return nil
}

// progressiveAudioJob ports the audio output decisions of
// StreamingHelpers.GetStreamingState: the first encodable requested codec,
// the output container, channels, bitrate, and sample rate.
func progressiveAudioJob(source MediaSourceInfo, audio *MediaStream, request ProgressiveAudioRequest, caps encoding.Capabilities) encoding.AudioJob {
	codec := progressiveAudioCodec(request, caps)
	container := progressiveAudioContainer(request.Container, codec, source.Container)
	channels := progressiveAudioOutputChannels(request, audio, codec, caps)
	job := encoding.AudioJob{
		InputPath:           source.Path,
		OutputContainer:     container,
		OutputAudioCodec:    codec,
		OutputAudioChannels: channels,
		StartOffsetTicks:    request.StartTimeTicks,
		RunTimeTicks:        int64PointerValue(source.RunTimeTicks),
	}
	if encoding.IsLosslessAudioCodec(codec) {
		job.OutputAudioBitrate = intPointerValue(audio.BitRate)
	} else {
		job.OutputAudioBitrate = encoding.AudioBitrate(request.AudioBitrate, codec, intPointerValue(audio.Channels), channels)
	}
	// EncodingJobInfo.OutputAudioSampleRate (EncodingJobInfo.cs:204-225).
	if strings.EqualFold(codec, "copy") {
		job.OutputAudioSampleRate = intPointerValue(audio.SampleRate)
	} else if request.AudioSampleRate != nil {
		job.OutputAudioSampleRate = *request.AudioSampleRate
	}
	return job
}

// progressiveAudioCodec ports StreamingHelpers.cs:54-57,78-83: an empty
// AudioCodec is inferred from the route container, then the first requested
// codec that the encoder supports is chosen, else the first codec.
func progressiveAudioCodec(request ProgressiveAudioRequest, caps encoding.Capabilities) string {
	var codecs []string
	for codec := range strings.SplitSeq(request.AudioCodec, ",") {
		if codec = strings.TrimSpace(codec); codec != "" {
			codecs = append(codecs, codec)
		}
	}
	if len(codecs) == 0 {
		return inferAudioCodec(request.Container)
	}
	for _, codec := range codecs {
		if encoding.CanEncodeAudio(codec, caps) {
			return codec
		}
	}
	return codecs[0]
}

// inferAudioCodec ports EncodingHelper.InferAudioCodec (EncodingHelper.cs:683-700).
// Upstream reads the text after the first dot of the request path; a route
// without an extension then yields a value that GetAudioEncoder replaces with
// AAC, which is the result here too.
func inferAudioCodec(container string) string {
	container = strings.ToLower(strings.TrimSpace(container))
	switch container {
	case "":
		return "aac"
	case "ogg", "oga", "ogv", "webm", "webma":
		return "opus"
	case "m4a", "m4b", "mp4", "mov", "mkv", "mka":
		return "aac"
	case "ts", "avi", "flv", "f4v", "swf":
		return "mp3"
	}
	return container
}

// progressiveAudioContainer ports StreamingHelpers.GetOutputFileExtension
// and GetContainerFileExtension (StreamingHelpers.cs:302-371,613-626) for an
// audio request without a route extension.
func progressiveAudioContainer(requested, codec, sourceContainer string) string {
	container := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(requested), "."))
	if container == "" {
		switch strings.ToLower(codec) {
		case "aac":
			container = "aac"
		case "mp3":
			container = "mp3"
		case "vorbis":
			container = "ogg"
		case "wma":
			container = "wma"
		default:
			container, _, _ = strings.Cut(strings.ToLower(sourceContainer), ",")
			container = strings.TrimSpace(container)
		}
	}
	switch container {
	case "mpegts":
		return "ts"
	case "matroska":
		return "mkv"
	}
	return container
}

// progressiveAudioOutputChannels ports EncodingHelper.GetNumAudioChannelsParam
// (EncodingHelper.cs:2953-3020) for a progressive job.
func progressiveAudioOutputChannels(request ProgressiveAudioRequest, audio *MediaStream, codec string, caps encoding.Capabilities) int {
	result := hlsRequestedAudioChannels(ProgressiveRequest{
		MaxAudioChannels:            request.MaxAudioChannels,
		AudioChannels:               request.AudioChannels,
		TranscodingMaxAudioChannels: request.TranscodingMaxAudioChannels,
		StreamOptions:               request.StreamOptions,
	}, codec)
	if input := intPointerValue(audio.Channels); input > 0 && (result == nil || input < *result) {
		result = &input
	}
	if !strings.EqualFold(codec, "copy") {
		limit := encoding.AudioEncoderChannelLimit(encoding.AudioEncoder(codec, caps))
		if result == nil || limit < *result {
			result = &limit
		}
		if request.TranscodingMaxAudioChannels != nil && *request.TranscodingMaxAudioChannels < *result {
			result = request.TranscodingMaxAudioChannels
		}
	}
	return intPointerValue(result)
}

func progressiveAudioContainerSyntax(container string) bool {
	container = strings.TrimPrefix(container, ".")
	if container == "" || len(container) > 16 {
		return false
	}
	for _, r := range container {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// progressiveAudioContentType ports MimeTypes.GetMimeType for the output file
// (EncodingJobInfo.GetMimeType, FileStreamResponseHelpers.cs:137).
func progressiveAudioContentType(container string) string {
	switch strings.ToLower(container) {
	case "mp3":
		return "audio/mpeg"
	case "aac":
		return "audio/aac"
	case "m4a", "m4b":
		return "audio/mp4"
	case "mp4":
		return "video/mp4"
	case "ogg", "oga", "opus":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "wav":
		return "audio/wav"
	case "webma":
		return "audio/webm"
	case "webm":
		return "video/webm"
	case "mka":
		return "audio/x-matroska"
	case "ts":
		return "video/mp2t"
	case "ac3":
		return "audio/ac3"
	case "wma":
		return "audio/x-ms-wma"
	}
	return "application/octet-stream"
}
