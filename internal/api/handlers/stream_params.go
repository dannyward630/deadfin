// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/playback"
)

var (
	streamParamsCodecName = regexp.MustCompile(`^[a-zA-Z0-9\-._,|]{0,40}$`)
	streamParamsLevel     = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)
)

// StreamParams is the subset of StreamingRequestDto and VideoRequestDto that
// the legacy, positional Params query value can replace. Pointers preserve the
// distinction between an omitted query value and an explicitly supplied one.
type StreamParams struct {
	DeviceID                    *string
	MediaSourceID               *string
	Static                      *bool
	VideoCodec                  *string
	AudioCodec                  *string
	AudioStreamIndex            *int
	SubtitleStreamIndex         *int
	VideoBitrate                *int
	AudioBitrate                *int
	MaxAudioChannels            *int
	MaxFramerate                *float32
	MaxWidth                    *int
	MaxHeight                   *int
	StartTimeTicks              *int64
	Level                       *string
	MaxRefFrames                *int
	MaxVideoBitDepth            *int
	Profile                     *string
	PlaySessionID               *string
	LiveStreamID                *string
	CopyTimestamps              *bool
	SubtitleMethod              *playback.SubtitleDeliveryMethod
	TranscodingMaxAudioChannels *int
	EnableSubtitlesInManifest   *bool
	Tag                         *string
	RequireAVC                  *bool
	SubtitleCodec               *string
	RequireNonAnamorphic        *bool
	Deinterlace                 *bool
	TranscodeReasons            *string
}

// DecodeStreamParams ports StreamingHelpers.ParseParams. It applies nonblank
// packed slots over existing explicitly bound fields, preserving the source's
// positional compatibility behavior and ignoring slots introduced after 33.
func DecodeStreamParams(packed string, video bool, existing StreamParams) (StreamParams, error) {
	params := existing

	for index, value := range strings.Split(packed, ";") {
		if strings.TrimSpace(value) == "" {
			continue
		}

		switch index {
		case 0, 19, 21, 23:
			// Obsolete DeviceProfileId, CABAC, api_key, and duplicate ItemId.
		case 1:
			params.DeviceID = stringPointer(value)
		case 2:
			params.MediaSourceID = stringPointer(value)
		case 3:
			params.Static = boolPointer(strings.EqualFold(value, "true"))
		case 4:
			if video && validCodecName(value) {
				params.VideoCodec = stringPointer(value)
			}
		case 5:
			if validCodecName(value) {
				params.AudioCodec = stringPointer(value)
			}
		case 6:
			if video {
				parsed, err := parseInt(value)
				if err != nil {
					return params, err
				}
				params.AudioStreamIndex = intPointer(parsed)
			}
		case 7:
			if video {
				parsed, err := parseInt(value)
				if err != nil {
					return params, err
				}
				params.SubtitleStreamIndex = intPointer(parsed)
			}
		case 8:
			if video {
				parsed, err := parseInt(value)
				if err != nil {
					return params, err
				}
				params.VideoBitrate = intPointer(parsed)
			}
		case 9:
			parsed, err := parseInt(value)
			if err != nil {
				return params, err
			}
			params.AudioBitrate = intPointer(parsed)
		case 10:
			parsed, err := parseInt(value)
			if err != nil {
				return params, err
			}
			params.MaxAudioChannels = intPointer(parsed)
		case 11:
			if video {
				parsed, err := parseFloat32(value)
				if err != nil {
					return params, err
				}
				params.MaxFramerate = float32Pointer(parsed)
			}
		case 12:
			if video {
				parsed, err := parseInt(value)
				if err != nil {
					return params, err
				}
				params.MaxWidth = intPointer(parsed)
			}
		case 13:
			if video {
				parsed, err := parseInt(value)
				if err != nil {
					return params, err
				}
				params.MaxHeight = intPointer(parsed)
			}
		case 14:
			parsed, err := parseInt64(value)
			if err != nil {
				return params, err
			}
			params.StartTimeTicks = int64Pointer(parsed)
		case 15:
			if video && streamParamsLevel.MatchString(value) {
				params.Level = stringPointer(value)
			}
		case 16:
			if video {
				parsed, err := parseInt(value)
				if err != nil {
					return params, err
				}
				params.MaxRefFrames = intPointer(parsed)
			}
		case 17:
			if video {
				parsed, err := parseInt(value)
				if err != nil {
					return params, err
				}
				params.MaxVideoBitDepth = intPointer(parsed)
			}
		case 18:
			if video && validCodecName(value) {
				params.Profile = stringPointer(value)
			}
		case 20:
			params.PlaySessionID = stringPointer(value)
		case 22:
			params.LiveStreamID = stringPointer(value)
		case 24:
			if video {
				params.CopyTimestamps = boolPointer(strings.EqualFold(value, "true"))
			}
		case 25:
			if video {
				if method, ok := parseSubtitleDeliveryMethod(value); ok {
					params.SubtitleMethod = &method
				}
			}
		case 26:
			parsed, err := parseInt(value)
			if err != nil {
				return params, err
			}
			params.TranscodingMaxAudioChannels = intPointer(parsed)
		case 27:
			if video {
				params.EnableSubtitlesInManifest = boolPointer(strings.EqualFold(value, "true"))
			}
		case 28:
			params.Tag = stringPointer(value)
		case 29:
			if video {
				params.RequireAVC = boolPointer(strings.EqualFold(value, "true"))
			}
		case 30:
			if validCodecName(value) {
				params.SubtitleCodec = stringPointer(value)
			}
		case 31:
			if video {
				params.RequireNonAnamorphic = boolPointer(strings.EqualFold(value, "true"))
			}
		case 32:
			if video {
				params.Deinterlace = boolPointer(strings.EqualFold(value, "true"))
			}
		case 33:
			params.TranscodeReasons = stringPointer(value)
		}
	}

	return params, nil
}

func validCodecName(value string) bool {
	return streamParamsCodecName.MatchString(value)
}

func parseInt(value string) (int, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	return int(parsed), err
}

func parseInt64(value string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
}

func parseFloat32(value string) (float32, error) {
	trimmed := strings.TrimSpace(value)
	switch {
	case strings.EqualFold(trimmed, "nan"):
		return float32(math.NaN()), nil
	case strings.EqualFold(trimmed, "infinity"), strings.EqualFold(trimmed, "+infinity"):
		return float32(math.Inf(1)), nil
	case strings.EqualFold(trimmed, "-infinity"):
		return float32(math.Inf(-1)), nil
	}

	parsed, err := strconv.ParseFloat(trimmed, 32)
	if err != nil && math.IsInf(parsed, 0) {
		// Current .NET returns infinity rather than throwing for finite values
		// outside Single's range.
		return float32(parsed), nil
	}
	return float32(parsed), err
}

// Enum.TryParse without its ignoreCase argument compares named constants with
// ordinal casing, while still accepting any numeric value representable by the
// underlying enum type.
func parseSubtitleDeliveryMethod(value string) (playback.SubtitleDeliveryMethod, bool) {
	trimmed := strings.TrimSpace(value)
	switch trimmed {
	case "Encode":
		return playback.SubtitleEncode, true
	case "Embed":
		return playback.SubtitleEmbed, true
	case "External":
		return playback.SubtitleExternal, true
	case "Hls":
		return playback.SubtitleHLS, true
	case "Drop":
		return playback.SubtitleDrop, true
	}

	parsed, err := strconv.ParseInt(trimmed, 10, 32)
	if err != nil {
		return 0, false
	}
	return playback.SubtitleDeliveryMethod(parsed), true
}

func stringPointer(value string) *string { return &value }

func boolPointer(value bool) *bool { return &value }

func intPointer(value int) *int { return &value }

func int64Pointer(value int64) *int64 { return &value }

func float32Pointer(value float32) *float32 { return &value }
