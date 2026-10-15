// SPDX-License-Identifier: GPL-2.0-only

package playback

// Faithful port of MediaBrowser.Model/Dlna/ConditionProcessor.cs @ b04614e.
//
// Semantics that MUST NOT be "fixed" (each is load-bearing for parity):
//   - Unknown (nil) values satisfy a condition iff !IsRequired.
//   - EqualsAny splits Value on '|'; it exists only for int, double, string and
//     VideoRangeType. For bool and timestamp it is an error (upstream throws
//     InvalidOperationException).
//   - String comparisons are case-insensitive; range operators on strings are
//     errors (upstream throws).
//   - Numeric parse failure of Value returns false, not an error
//     (ConditionProcessor.cs:213,230,303,320).
//   - VideoRangeType Unknown is treated as a nil value
//     (ConditionProcessor.cs:346).
//   - HDR10Plus first checks whether the condition is satisfied by HDR10
//     (ConditionProcessor.cs:352-359). Consequence, preserved on purpose: a
//     `NotEquals HDR10Plus` condition IS satisfied by an HDR10Plus stream,
//     because "HDR10 != HDR10Plus" short-circuits to true.
//   - VideoTimestamp uses throwing Enum.Parse upstream (:331): an unparseable
//     Value is an error here, unlike every other property.
//   - VideoFramerate is a float widened to double before comparison
//     (IsVideoConditionSatisfied takes float?, the comparator takes double?).
//     A float32 23.976 widens to 23.975999832153320 and does NOT Equal a
//     parsed 23.976. VideoConditionInput therefore takes *float32 and widens,
//     exactly like C#.
//
// Where upstream throws, these functions return a non-nil error; callers are
// expected to let it surface (upstream's exceptions become HTTP 500s).

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ProfileCondition ports MediaBrowser.Model.Dlna.ProfileCondition.
//
// JSON note: the C# parameterless constructor sets IsRequired = true, so a
// JSON payload that omits IsRequired deserializes to true. UnmarshalJSON below
// reproduces that default. (The 3-arg C# constructor defaults it to false —
// that is a code-construction concern, not a wire concern.)
type ProfileCondition struct {
	Condition  ConditionType     `json:"Condition"`
	Property   ConditionProperty `json:"Property"`
	Value      string            `json:"Value"`
	IsRequired bool              `json:"IsRequired"`
}

func (c *ProfileCondition) UnmarshalJSON(data []byte) error {
	type alias ProfileCondition
	tmp := alias{IsRequired: true} // C# default-ctor value when the field is absent
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*c = ProfileCondition(tmp)
	return nil
}

// VideoConditionInput carries the nullable stream facts consulted by video
// conditions. Field-for-field with IsVideoConditionSatisfied's parameter list
// (ConditionProcessor.cs:38-58); nil pointer == C# null.
type VideoConditionInput struct {
	Width           *int
	Height          *int
	VideoBitDepth   *int
	VideoBitrate    *int
	VideoProfile    *string
	VideoRangeType  *VideoRangeType
	VideoLevel      *float64
	VideoFramerate  *float32 // widened to float64 at comparison time, like C#
	PacketLength    *int
	Timestamp       *TransportStreamTimestamp
	IsAnamorphic    *bool
	IsInterlaced    *bool
	RefFrames       *int
	NumStreams      int // non-nullable upstream
	NumVideoStreams *int
	NumAudioStreams *int
	VideoCodecTag   *string
	IsAvc           *bool
	VideoRotation   *int
}

// IsVideoConditionSatisfied ports ConditionProcessor.IsVideoConditionSatisfied.
// An unhandled property returns true (ConditionProcessor.cs:100-101).
func IsVideoConditionSatisfied(c ProfileCondition, in VideoConditionInput) (bool, error) {
	switch c.Property {
	case PropIsInterlaced:
		return boolCondition(c, in.IsInterlaced)
	case PropIsAnamorphic:
		return boolCondition(c, in.IsAnamorphic)
	case PropIsAvc:
		return boolCondition(c, in.IsAvc)
	case PropVideoFramerate:
		var widened *float64
		if in.VideoFramerate != nil {
			w := float64(*in.VideoFramerate) // C# float→double widening
			widened = &w
		}
		return doubleCondition(c, widened)
	case PropVideoLevel:
		return doubleCondition(c, in.VideoLevel)
	case PropVideoProfile:
		return stringCondition(c, in.VideoProfile)
	case PropVideoRangeType:
		return videoRangeCondition(c, in.VideoRangeType)
	case PropVideoCodecTag:
		return stringCondition(c, in.VideoCodecTag)
	case PropPacketLength:
		return intCondition(c, in.PacketLength)
	case PropVideoBitDepth:
		return intCondition(c, in.VideoBitDepth)
	case PropVideoBitrate:
		return intCondition(c, in.VideoBitrate)
	case PropHeight:
		return intCondition(c, in.Height)
	case PropWidth:
		return intCondition(c, in.Width)
	case PropRefFrames:
		return intCondition(c, in.RefFrames)
	case PropNumStreams:
		return intCondition(c, &in.NumStreams)
	case PropNumAudioStreams:
		return intCondition(c, in.NumAudioStreams)
	case PropNumVideoStreams:
		return intCondition(c, in.NumVideoStreams)
	case PropVideoTimestamp:
		return timestampCondition(c, in.Timestamp)
	case PropVideoRotation:
		return intCondition(c, in.VideoRotation)
	default:
		return true, nil
	}
}

// IsImageConditionSatisfied ports the image variant; unexpected properties are
// errors (upstream throws ArgumentException, ConditionProcessor.cs:121).
func IsImageConditionSatisfied(c ProfileCondition, width, height *int) (bool, error) {
	switch c.Property {
	case PropHeight:
		return intCondition(c, height)
	case PropWidth:
		return intCondition(c, width)
	default:
		return false, fmt.Errorf("unexpected condition on image file: %s", c.Property)
	}
}

// IsAudioConditionSatisfied ports the audio variant; unexpected properties are
// errors (ConditionProcessor.cs:147).
func IsAudioConditionSatisfied(c ProfileCondition, audioChannels, audioBitrate, audioSampleRate, audioBitDepth *int) (bool, error) {
	switch c.Property {
	case PropAudioBitrate:
		return intCondition(c, audioBitrate)
	case PropAudioChannels:
		return intCondition(c, audioChannels)
	case PropAudioSampleRate:
		return intCondition(c, audioSampleRate)
	case PropAudioBitDepth:
		return intCondition(c, audioBitDepth)
	default:
		return false, fmt.Errorf("unexpected condition on audio file: %s", c.Property)
	}
}

// VideoAudioConditionInput mirrors IsVideoAudioConditionSatisfied's parameters.
type VideoAudioConditionInput struct {
	AudioChannels    *int
	AudioBitrate     *int
	AudioSampleRate  *int
	AudioBitDepth    *int
	AudioProfile     *string
	IsSecondaryTrack *bool
}

// IsVideoAudioConditionSatisfied ports the video-audio variant; unexpected
// properties are errors (ConditionProcessor.cs:186).
func IsVideoAudioConditionSatisfied(c ProfileCondition, in VideoAudioConditionInput) (bool, error) {
	switch c.Property {
	case PropAudioProfile:
		return stringCondition(c, in.AudioProfile)
	case PropAudioBitrate:
		return intCondition(c, in.AudioBitrate)
	case PropAudioChannels:
		return intCondition(c, in.AudioChannels)
	case PropIsSecondaryAudio:
		return boolCondition(c, in.IsSecondaryTrack)
	case PropAudioSampleRate:
		return intCondition(c, in.AudioSampleRate)
	case PropAudioBitDepth:
		return intCondition(c, in.AudioBitDepth)
	default:
		return false, fmt.Errorf("unexpected condition on audio file: %s", c.Property)
	}
}

func errUnexpectedConditionType(c ProfileCondition) error {
	return fmt.Errorf("unexpected ProfileConditionType: %s", c.Condition)
}

// parseIntInvariant mirrors int.TryParse with NumberStyles.Integer /
// InvariantCulture: optional surrounding whitespace, optional leading sign.
func parseIntInvariant(s string) (int, bool) {
	s = strings.TrimSpace(s)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return int(n), true
}

// parseDoubleInvariant mirrors double.TryParse with NumberStyles.Float |
// AllowThousands / InvariantCulture: whitespace, sign, decimal point,
// exponent, and ',' group separators.
func parseDoubleInvariant(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, ",") {
		// AllowThousands: ',' is a group separator; it may not appear after
		// the decimal point (C# rejects "1.2,3").
		dot := strings.IndexByte(s, '.')
		if dot >= 0 && strings.Contains(s[dot:], ",") {
			return 0, false
		}
		s = strings.ReplaceAll(s, ",", "")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// parseBoolInvariant mirrors bool.TryParse: "true"/"false" only,
// case-insensitive, surrounding whitespace allowed. (Not Go's ParseBool,
// which also accepts 1/t/T/0/f/F.)
func parseBoolInvariant(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func intCondition(c ProfileCondition, current *int) (bool, error) {
	if current == nil {
		return !c.IsRequired, nil
	}
	if c.Condition == ConditionEqualsAny {
		for part := range strings.SplitSeq(c.Value, "|") {
			if v, ok := parseIntInvariant(part); ok && v == *current {
				return true, nil
			}
		}
		return false, nil
	}
	expected, ok := parseIntInvariant(c.Value)
	if !ok {
		return false, nil
	}
	switch c.Condition {
	case ConditionEquals:
		return *current == expected, nil
	case ConditionGreaterThanEqual:
		return *current >= expected, nil
	case ConditionLessThanEqual:
		return *current <= expected, nil
	case ConditionNotEquals:
		return *current != expected, nil
	default:
		return false, errUnexpectedConditionType(c)
	}
}

func stringCondition(c ProfileCondition, current *string) (bool, error) {
	if current == nil || *current == "" { // string.IsNullOrEmpty
		return !c.IsRequired, nil
	}
	switch c.Condition {
	case ConditionEqualsAny:
		for part := range strings.SplitSeq(c.Value, "|") {
			if strings.EqualFold(part, *current) {
				return true, nil
			}
		}
		return false, nil
	case ConditionEquals:
		return strings.EqualFold(*current, c.Value), nil
	case ConditionNotEquals:
		return !strings.EqualFold(*current, c.Value), nil
	default:
		return false, errUnexpectedConditionType(c)
	}
}

func boolCondition(c ProfileCondition, current *bool) (bool, error) {
	if current == nil {
		return !c.IsRequired, nil
	}
	expected, ok := parseBoolInvariant(c.Value)
	if !ok {
		return false, nil
	}
	switch c.Condition {
	case ConditionEquals:
		return *current == expected, nil
	case ConditionNotEquals:
		return *current != expected, nil
	default:
		return false, errUnexpectedConditionType(c)
	}
}

func doubleCondition(c ProfileCondition, current *float64) (bool, error) {
	if current == nil {
		return !c.IsRequired, nil
	}
	if c.Condition == ConditionEqualsAny {
		for part := range strings.SplitSeq(c.Value, "|") {
			if v, ok := parseDoubleInvariant(part); ok && v == *current {
				return true, nil
			}
		}
		return false, nil
	}
	expected, ok := parseDoubleInvariant(c.Value)
	if !ok {
		return false, nil
	}
	switch c.Condition {
	case ConditionEquals:
		return *current == expected, nil
	case ConditionGreaterThanEqual:
		return *current >= expected, nil
	case ConditionLessThanEqual:
		return *current <= expected, nil
	case ConditionNotEquals:
		return *current != expected, nil
	default:
		return false, errUnexpectedConditionType(c)
	}
}

func timestampCondition(c ProfileCondition, current *TransportStreamTimestamp) (bool, error) {
	if current == nil {
		return !c.IsRequired, nil
	}
	// Upstream uses throwing Enum.Parse here (ConditionProcessor.cs:331) —
	// unlike every other property, an unparseable Value is an error.
	expected, ok := ParseTransportStreamTimestamp(c.Value)
	if !ok {
		return false, fmt.Errorf("invalid TransportStreamTimestamp value %q", c.Value)
	}
	switch c.Condition {
	case ConditionEquals:
		return *current == expected, nil
	case ConditionNotEquals:
		return *current != expected, nil
	default:
		return false, errUnexpectedConditionType(c)
	}
}

func videoRangeCondition(c ProfileCondition, current *VideoRangeType) (bool, error) {
	// Unknown is treated as null (ConditionProcessor.cs:346).
	if current == nil || *current == VideoRangeUnknown {
		return !c.IsRequired, nil
	}
	// HDR10Plus also satisfies every condition that HDR10 satisfies (:352-359).
	// This is checked FIRST, before normal evaluation, and preserves the
	// upstream quirk that `NotEquals HDR10Plus` passes for HDR10Plus streams.
	if *current == VideoRangeHDR10Plus {
		hdr10 := VideoRangeHDR10
		if ok, err := videoRangeCondition(c, &hdr10); err != nil {
			return false, err
		} else if ok {
			return true, nil
		}
	}
	if c.Condition == ConditionEqualsAny {
		for part := range strings.SplitSeq(c.Value, "|") {
			if v, ok := ParseVideoRangeType(part); ok && v == *current {
				return true, nil
			}
		}
		return false, nil
	}
	expected, ok := ParseVideoRangeType(c.Value)
	if !ok {
		return false, nil
	}
	switch c.Condition {
	case ConditionEquals:
		return *current == expected, nil
	case ConditionNotEquals:
		return *current != expected, nil
	default:
		return false, errUnexpectedConditionType(c)
	}
}
