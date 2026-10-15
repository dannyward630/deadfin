// SPDX-License-Identifier: GPL-2.0-only

package playback

import "strings"

// ReferenceFrameRate ports MediaStream.ReferenceFrameRate. Broken ffprobe
// average rates at or over 1000 fps must fall back to the real rate.
func (s MediaStream) ReferenceFrameRate() *float32 {
	if s.AverageFrameRate != nil && *s.AverageFrameRate < 1000 {
		return s.AverageFrameRate
	}
	return s.RealFrameRate
}

// VideoRange ports MediaStream.GetVideoColorRange's first return value.
func (s MediaStream) VideoRange() VideoRange {
	rangeValue, _ := s.videoColorRange()
	return rangeValue
}

// VideoRangeType ports MediaStream.GetVideoColorRange's second return value.
func (s MediaStream) VideoRangeType() VideoRangeType {
	_, rangeType := s.videoColorRange()
	return rangeType
}

func (s MediaStream) videoColorRange() (VideoRange, VideoRangeType) {
	if s.Type != StreamTypeVideo {
		return MediaVideoRangeUnknown, VideoRangeUnknown
	}

	dvProfile := derefInt(s.DvProfile)
	dvCompatibility := derefInt(s.DvBlSignalCompatibilityID)
	isDoViProfile := dvProfile == 5 || dvProfile == 7 || dvProfile == 8 || dvProfile == 10
	isDoViFlag := s.DvBlSignalCompatibilityID != nil && derefInt(s.RpuPresentFlag) == 1 && derefInt(s.BlPresentFlag) == 1 &&
		(dvCompatibility == 0 || dvCompatibility == 1 || dvCompatibility == 2 || dvCompatibility == 4 || dvCompatibility == 6)
	if (isDoViProfile && isDoViFlag) || isDoViCodecTag(derefString(s.CodecTag)) {
		videoRange, rangeType := doviRange(dvProfile, dvCompatibility)
		if s.Hdr10PlusPresentFlag != nil && *s.Hdr10PlusPresentFlag {
			switch rangeType {
			case VideoRangeDOVIWithHDR10:
				return MediaVideoRangeHDR, VideoRangeDOVIWithHDR10Plus
			case VideoRangeDOVIWithEL:
				return MediaVideoRangeHDR, VideoRangeDOVIWithELHDR10Plus
			}
		}
		return videoRange, rangeType
	}
	if strings.EqualFold(derefString(s.ColorTransfer), "smpte2084") {
		if s.Hdr10PlusPresentFlag != nil && *s.Hdr10PlusPresentFlag {
			return MediaVideoRangeHDR, VideoRangeHDR10Plus
		}
		return MediaVideoRangeHDR, VideoRangeHDR10
	}
	if strings.EqualFold(derefString(s.ColorTransfer), "arib-std-b67") {
		return MediaVideoRangeHDR, VideoRangeHLG
	}
	return MediaVideoRangeSDR, VideoRangeSDR
}

func doviRange(profile, compatibility int) (VideoRange, VideoRangeType) {
	switch profile {
	case 5:
		return MediaVideoRangeHDR, VideoRangeDOVI
	case 8:
		switch compatibility {
		case 1:
			return MediaVideoRangeHDR, VideoRangeDOVIWithHDR10
		case 4:
			return MediaVideoRangeHDR, VideoRangeDOVIWithHLG
		case 2:
			return MediaVideoRangeSDR, VideoRangeDOVIWithSDR
		default:
			return MediaVideoRangeHDR, VideoRangeDOVIInvalid
		}
	case 7:
		return MediaVideoRangeHDR, VideoRangeDOVIWithEL
	case 10:
		switch compatibility {
		case 0:
			return MediaVideoRangeHDR, VideoRangeDOVI
		case 1:
			return MediaVideoRangeHDR, VideoRangeDOVIWithHDR10
		case 2:
			return MediaVideoRangeSDR, VideoRangeDOVIWithSDR
		case 4:
			return MediaVideoRangeHDR, VideoRangeDOVIWithHLG
		default:
			return MediaVideoRangeHDR, VideoRangeDOVIInvalid
		}
	default:
		return MediaVideoRangeSDR, VideoRangeSDR
	}
}

func isDoViCodecTag(codecTag string) bool {
	return strings.EqualFold(codecTag, "dovi") || strings.EqualFold(codecTag, "dvh1") ||
		strings.EqualFold(codecTag, "dvhe") || strings.EqualFold(codecTag, "dav1")
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// MediaStreamSupportsExternalStream ports MediaSourceManager's retrieval-time
// capability derivation for external, text, PGS, and VobSub subtitles.
func MediaStreamSupportsExternalStream(stream MediaStream) bool {
	return stream.IsExternal || stream.IsTextSubtitleStream() || stream.IsPgsSubtitleStream() || stream.IsVobSubSubtitleStream()
}

// IsTextSubtitleStream ports MediaStream.IsTextSubtitleStream.
func (s MediaStream) IsTextSubtitleStream() bool {
	return s.Type == StreamTypeSubtitle && (s.Codec != "" || s.IsExternal) && IsTextSubtitleFormat(s.Codec)
}

// IsPgsSubtitleStream ports MediaStream.IsPgsSubtitleStream.
func (s MediaStream) IsPgsSubtitleStream() bool {
	return s.Type == StreamTypeSubtitle && (s.Codec != "" || s.IsExternal) && IsPgsSubtitleFormat(s.Codec)
}

// IsVobSubSubtitleStream ports MediaStream.IsVobSubSubtitleStream.
func (s MediaStream) IsVobSubSubtitleStream() bool {
	return s.Type == StreamTypeSubtitle && (s.Codec != "" || s.IsExternal) && IsVobSubSubtitleFormat(s.Codec)
}

// IsExtractableSubtitleStream reports subtitle formats ffmpeg can extract.
func (s MediaStream) IsExtractableSubtitleStream() bool {
	return s.IsTextSubtitleStream() || s.IsPgsSubtitleStream() || s.IsVobSubSubtitleStream()
}

// SupportsSubtitleConversionTo ports MediaStream.SupportsSubtitleConversionTo.
func (s MediaStream) SupportsSubtitleConversionTo(codec string) bool {
	return s.IsTextSubtitleStream() && !strings.EqualFold(s.Codec, "ass") && !strings.EqualFold(s.Codec, "ssa") &&
		!strings.EqualFold(codec, "ass") && !strings.EqualFold(codec, "ssa")
}

// IsTextSubtitleFormat ports MediaStream.IsTextFormat.
func IsTextSubtitleFormat(codec string) bool {
	return strings.Contains(strings.ToLower(codec), "microdvd") || (!strings.Contains(strings.ToLower(codec), "pgs") &&
		!strings.Contains(strings.ToLower(codec), "dvdsub") && !strings.Contains(strings.ToLower(codec), "vobsub") &&
		!strings.Contains(strings.ToLower(codec), "dvbsub") && !strings.EqualFold(codec, "sup") && !strings.EqualFold(codec, "sub"))
}

func IsPgsSubtitleFormat(codec string) bool {
	return strings.Contains(strings.ToLower(codec), "pgs") || strings.EqualFold(codec, "sup")
}

func IsVobSubSubtitleFormat(codec string) bool {
	return strings.Contains(strings.ToLower(codec), "dvdsub") || strings.Contains(strings.ToLower(codec), "vobsub")
}
