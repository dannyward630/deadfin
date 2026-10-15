// SPDX-License-Identifier: GPL-2.0-only

package encoding

import (
	"fmt"
	"strconv"
	"strings"
)

// AudioJob contains the facts of one progressive audio transcode
// (EncodingHelper.GetProgressiveAudioFullCommandLine). It carries no request,
// filesystem, or process state.
type AudioJob struct {
	InputPath       string
	OutputPath      string
	OutputContainer string

	// OutputAudioCodec is the requested codec name, for example "mp3" or
	// "aac". AudioEncoder maps it to the ffmpeg encoder.
	OutputAudioCodec string
	// OutputAudioBitrate, OutputAudioChannels, and OutputAudioSampleRate
	// are omitted from the command line when zero.
	OutputAudioBitrate    int
	OutputAudioChannels   int
	OutputAudioSampleRate int
	// EnableAudioVbr is EncodingOptions.EnableAudioVbr and
	// StreamState.EnableAudioVbrEncoding together.
	EnableAudioVbr bool

	// StartOffsetTicks is the input seek. RunTimeTicks clamps it as
	// EncodingHelper.GetFastSeekCommandLineParameter does; zero leaves it
	// unclamped.
	StartOffsetTicks int64
	RunTimeTicks     int64
}

// losslessAudioCodecs is EncodingHelper.LosslessAudioCodecs
// (EncodingHelper.cs:151-159).
var losslessAudioCodecs = [...]string{"alac", "ape", "flac", "mlp", "truehd", "wavpack"}

// mp4ContainerNames is EncodingHelper._mp4ContainerNames
// (EncodingHelper.cs:117-125).
var mp4ContainerNames = [...]string{"mp4", "m4a", "m4p", "m4b", "m4r", "m4v"}

// IsLosslessAudioCodec reports whether codec is in
// EncodingHelper.LosslessAudioCodecs. The comparison ignores case.
func IsLosslessAudioCodec(codec string) bool {
	for _, lossless := range losslessAudioCodecs {
		if strings.EqualFold(codec, lossless) {
			return true
		}
	}
	return false
}

// AudioEncoder ports EncodingHelper.GetAudioEncoder (EncodingHelper.cs:762-822):
// it maps a requested audio codec to the ffmpeg encoder name. A codec that
// fails the container validation pattern selects AAC.
func AudioEncoder(codec string, caps Capabilities) string {
	if !validCodecToken(codec) {
		codec = "aac"
	}
	switch strings.ToLower(codec) {
	case "aac":
		if encoder := aacEncoder(caps); encoder != "" {
			return encoder
		}
		return "aac"
	case "mp3":
		return "libmp3lame"
	case "vorbis":
		return "libvorbis"
	case "opus":
		return "libopus"
	case "flac":
		return "flac"
	case "dts":
		return "dca"
	case "alac":
		return "alac"
	}
	return strings.ToLower(codec)
}

// CanEncodeAudio ports MediaEncoder.CanEncodeToAudioCodec
// (MediaEncoder.cs:397-409): opus and mp3 name their external encoders.
func CanEncodeAudio(codec string, caps Capabilities) bool {
	switch strings.ToLower(codec) {
	case "opus":
		codec = "libopus"
	case "mp3":
		codec = "libmp3lame"
	}
	return containsFold(caps.Encoders, codec)
}

// AudioEncoderChannelLimit ports EncodingHelper._audioTranscodeChannelLookup
// (EncodingHelper.cs:132-141): the encoders that cannot write more than a
// fixed channel count. Other encoders default to eight channels.
func AudioEncoderChannelLimit(encoder string) int {
	switch strings.ToLower(encoder) {
	case "libmp3lame":
		return 2
	case "libfdk_aac", "ac3", "eac3", "dca", "mlp", "truehd":
		return 6
	}
	return 8
}

// AudioBitrate ports EncodingHelper.GetAudioBitrateParam
// (EncodingHelper.cs:2807-2855). requested is the request AudioBitRate; nil
// means no limit. inputChannels and outputChannels are zero when unknown.
func AudioBitrate(requested *int, codec string, inputChannels, outputChannels int) int {
	bitrate := int(^uint32(0) >> 1)
	if requested != nil {
		bitrate = *requested
	}
	limit := func(perChannel, surround, unknown int) int {
		switch {
		case inputChannels >= 6 && (outputChannels >= 6 || outputChannels == 0):
			return min(surround, bitrate)
		case inputChannels > 0 && outputChannels > 0:
			return min(outputChannels*perChannel, bitrate)
		case inputChannels > 0:
			return min(inputChannels*perChannel, bitrate)
		}
		return min(unknown, bitrate)
	}
	switch strings.ToLower(codec) {
	case "", "aac", "mp3", "opus", "vorbis", "ac3", "eac3":
		return limit(128000, 640000, 384000)
	case "dts", "dca":
		return limit(136000, 768000, 672000)
	}
	channels := outputChannels
	if channels == 0 {
		channels = inputChannels
	}
	if channels == 0 {
		channels = 2
	}
	return 128000 * channels
}

// audioVbrModeArgs ports EncodingHelper.GetAudioVbrModeParam
// (EncodingHelper.cs:2857-2909). A nil result means the encoder has no VBR
// mode and the caller uses a constant bitrate.
func audioVbrModeArgs(encoder string, bitrate, channels int) []string {
	perChannel := bitrate / max(channels, 1)
	switch strings.ToLower(encoder) {
	case "libfdk_aac":
		mode := "5"
		switch {
		case perChannel < 32000:
			mode = "1"
		case perChannel < 48000:
			mode = "2"
		case perChannel < 64000:
			mode = "3"
		case perChannel < 96000:
			mode = "4"
		}
		return []string{"-vbr:a", mode}
	case "libmp3lame":
		// lame VBR suits only a middle bitrate range; use ABR elsewhere.
		if perChannel < 122500 && perChannel > 48000 {
			quality := "0"
			switch {
			case perChannel < 64000:
				quality = "6"
			case perChannel < 88000:
				quality = "4"
			case perChannel < 112000:
				quality = "2"
			}
			return []string{"-qscale:a", quality}
		}
		return []string{"-abr:a", "1", "-b:a", strconv.Itoa(bitrate)}
	case "aac_at":
		return []string{"-aac_at_mode:a", "2", "-b:a", strconv.Itoa(bitrate)}
	case "libvorbis":
		quality := "8"
		switch {
		case perChannel < 40000:
			quality = "0"
		case perChannel < 56000:
			quality = "2"
		case perChannel < 80000:
			quality = "4"
		case perChannel < 112000:
			quality = "6"
		}
		return []string{"-qscale:a", quality}
	}
	return nil
}

// opusSampleRate ports the Opus sample-rate rounding of
// GetProgressiveAudioFullCommandLine (EncodingHelper.cs:7876-7887).
func opusSampleRate(rate int) int {
	switch {
	case rate <= 8000:
		return 8000
	case rate <= 12000:
		return 12000
	case rate <= 16000:
		return 16000
	case rate <= 24000:
		return 24000
	}
	return 48000
}

// BuildProgressiveAudioArgs ports EncodingHelper.GetProgressiveAudioFullCommandLine
// (EncodingHelper.cs:7836-7911) for a local file input. The returned tokens
// are ready for exec; they are not shell escaped. The ffmpeg input is the
// "file:" operand that internal/ffmpeg replaces with the opened source.
func BuildProgressiveAudioArgs(job AudioJob, caps Capabilities) ([]string, error) {
	encoder := AudioEncoder(job.OutputAudioCodec, caps)
	if err := validateAudioJob(job, encoder, caps); err != nil {
		return nil, err
	}

	args := make([]string, 0, 32)
	if seek := audioSeekTicks(job); seek > 0 {
		args = append(args, "-ss", formatTicks(seek))
	}
	args = append(args, "-i", "file:"+job.InputPath, "-threads", "0", "-vn")
	if job.OutputAudioBitrate > 0 && !IsLosslessAudioCodec(job.OutputAudioCodec) {
		channels := job.OutputAudioChannels
		if channels == 0 {
			channels = 2
		}
		vbr := audioVbrModeArgs(encoder, job.OutputAudioBitrate, channels)
		if job.EnableAudioVbr && vbr != nil {
			args = append(args, vbr...)
		} else {
			args = append(args, "-ab", strconv.Itoa(job.OutputAudioBitrate))
		}
	}
	if job.OutputAudioChannels > 0 {
		args = append(args, "-ac", strconv.Itoa(job.OutputAudioChannels))
	}
	if job.OutputAudioCodec != "" {
		args = append(args, "-acodec", encoder)
	}
	if job.OutputAudioSampleRate > 0 {
		rate := job.OutputAudioSampleRate
		if strings.EqualFold(job.OutputAudioCodec, "opus") {
			rate = opusSampleRate(rate)
		}
		args = append(args, "-ar", strconv.Itoa(rate))
	}
	if isMP4Container(job.OutputContainer) {
		args = append(args, "-movflags", "empty_moov+delay_moov")
	}
	return append(args, "-id3v2_version", "3", "-write_id3v1", "1", "-y", job.OutputPath), nil
}

func validateAudioJob(job AudioJob, encoder string, caps Capabilities) error {
	if !safePath(job.InputPath) || !safePath(job.OutputPath) {
		return fmt.Errorf("encoding paths must be non-empty local paths without control characters or leading dashes")
	}
	if !validOutputContainer(job.OutputContainer) {
		return fmt.Errorf("unsupported progressive audio output container %q", job.OutputContainer)
	}
	if job.OutputAudioCodec == "" {
		return fmt.Errorf("progressive audio requires an output codec")
	}
	if job.OutputAudioBitrate < 0 || job.OutputAudioChannels < 0 || job.OutputAudioSampleRate < 0 {
		return fmt.Errorf("audio output settings must not be negative")
	}
	if job.StartOffsetTicks < 0 || job.RunTimeTicks < 0 {
		return fmt.Errorf("start offset and runtime must not be negative")
	}
	// Upstream writes "-f <format> -ar <AudioBitRate>" for a PCM encoder
	// (EncodingHelper.cs:7867-7871), which passes a bitrate as a sample rate.
	// Deadfin refuses PCM output (D-63).
	if strings.HasPrefix(encoder, "pcm_") {
		return fmt.Errorf("PCM progressive audio output is unsupported")
	}
	if encoder != "copy" && !containsFold(caps.Encoders, encoder) {
		return fmt.Errorf("audio encoder %q is unavailable", encoder)
	}
	return nil
}

// audioSeekTicks ports the clamp of EncodingHelper.GetFastSeekCommandLineParameter
// (EncodingHelper.cs:3046-3072) for an audio request: the seek stays in
// [0, RunTimeTicks - 5 s].
func audioSeekTicks(job AudioJob) int64 {
	seek := job.StartOffsetTicks
	if seek <= 0 {
		return 0
	}
	if job.RunTimeTicks > 0 {
		seek = min(seek, max(job.RunTimeTicks-50_000_000, 0))
	}
	return seek
}

func isMP4Container(container string) bool {
	for _, name := range mp4ContainerNames {
		if strings.EqualFold(container, name) {
			return true
		}
	}
	return false
}

// validOutputContainer accepts a short alphanumeric container. ffmpeg selects
// the muxer from the output file extension, which is this value.
func validOutputContainer(container string) bool {
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

// validCodecToken is EncodingHelper.ContainerValidationRegex
// (^[a-zA-Z0-9\-\._,|]{0,40}$).
func validCodecToken(value string) bool {
	if len(value) > 40 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._,|", r)) {
			return false
		}
	}
	return true
}
