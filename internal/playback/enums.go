// SPDX-License-Identifier: GPL-2.0-only

// Package playback is the pure playback-negotiation core: a faithful port of
// upstream MediaBrowser.Model/Dlna (ConditionProcessor, ContainerHelper, and —
// in later work packages — StreamBuilder and StreamInfo). No I/O anywhere in
// this package.
package playback

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ConditionType ports MediaBrowser.Model.Dlna.ProfileConditionType.
type ConditionType uint8

const (
	ConditionEquals           ConditionType = 0
	ConditionNotEquals        ConditionType = 1
	ConditionLessThanEqual    ConditionType = 2
	ConditionGreaterThanEqual ConditionType = 3
	ConditionEqualsAny        ConditionType = 4
)

var conditionTypeNames = map[ConditionType]string{
	ConditionEquals:           "Equals",
	ConditionNotEquals:        "NotEquals",
	ConditionLessThanEqual:    "LessThanEqual",
	ConditionGreaterThanEqual: "GreaterThanEqual",
	ConditionEqualsAny:        "EqualsAny",
}

// ConditionProperty ports MediaBrowser.Model.Dlna.ProfileConditionValue.
// Value 15 is unassigned upstream; the gap is preserved.
type ConditionProperty uint8

const (
	PropAudioChannels    ConditionProperty = 0
	PropAudioBitrate     ConditionProperty = 1
	PropAudioProfile     ConditionProperty = 2
	PropWidth            ConditionProperty = 3
	PropHeight           ConditionProperty = 4
	PropHas64BitOffsets  ConditionProperty = 5
	PropPacketLength     ConditionProperty = 6
	PropVideoBitDepth    ConditionProperty = 7
	PropVideoBitrate     ConditionProperty = 8
	PropVideoFramerate   ConditionProperty = 9
	PropVideoLevel       ConditionProperty = 10
	PropVideoProfile     ConditionProperty = 11
	PropVideoTimestamp   ConditionProperty = 12
	PropIsAnamorphic     ConditionProperty = 13
	PropRefFrames        ConditionProperty = 14
	PropNumAudioStreams  ConditionProperty = 16
	PropNumVideoStreams  ConditionProperty = 17
	PropIsSecondaryAudio ConditionProperty = 18
	PropVideoCodecTag    ConditionProperty = 19
	PropIsAvc            ConditionProperty = 20
	PropIsInterlaced     ConditionProperty = 21
	PropAudioSampleRate  ConditionProperty = 22
	PropAudioBitDepth    ConditionProperty = 23
	PropVideoRangeType   ConditionProperty = 24
	PropNumStreams       ConditionProperty = 25
	PropVideoRotation    ConditionProperty = 26
)

var conditionPropertyNames = map[ConditionProperty]string{
	PropAudioChannels:    "AudioChannels",
	PropAudioBitrate:     "AudioBitrate",
	PropAudioProfile:     "AudioProfile",
	PropWidth:            "Width",
	PropHeight:           "Height",
	PropHas64BitOffsets:  "Has64BitOffsets",
	PropPacketLength:     "PacketLength",
	PropVideoBitDepth:    "VideoBitDepth",
	PropVideoBitrate:     "VideoBitrate",
	PropVideoFramerate:   "VideoFramerate",
	PropVideoLevel:       "VideoLevel",
	PropVideoProfile:     "VideoProfile",
	PropVideoTimestamp:   "VideoTimestamp",
	PropIsAnamorphic:     "IsAnamorphic",
	PropRefFrames:        "RefFrames",
	PropNumAudioStreams:  "NumAudioStreams",
	PropNumVideoStreams:  "NumVideoStreams",
	PropIsSecondaryAudio: "IsSecondaryAudio",
	PropVideoCodecTag:    "VideoCodecTag",
	PropIsAvc:            "IsAvc",
	PropIsInterlaced:     "IsInterlaced",
	PropAudioSampleRate:  "AudioSampleRate",
	PropAudioBitDepth:    "AudioBitDepth",
	PropVideoRangeType:   "VideoRangeType",
	PropNumStreams:       "NumStreams",
	PropVideoRotation:    "VideoRotation",
}

// VideoRangeType ports Jellyfin.Data.Enums.VideoRangeType (13 values, in
// declaration order — the numeric values matter for numeric enum parsing).
type VideoRangeType int

const (
	VideoRangeUnknown VideoRangeType = iota
	VideoRangeSDR
	VideoRangeHDR10
	VideoRangeHLG
	VideoRangeDOVI
	VideoRangeDOVIWithHDR10
	VideoRangeDOVIWithHLG
	VideoRangeDOVIWithSDR
	VideoRangeDOVIWithEL
	VideoRangeDOVIWithHDR10Plus
	VideoRangeDOVIWithELHDR10Plus
	VideoRangeDOVIInvalid
	VideoRangeHDR10Plus
)

var videoRangeTypeNames = map[VideoRangeType]string{
	VideoRangeUnknown:             "Unknown",
	VideoRangeSDR:                 "SDR",
	VideoRangeHDR10:               "HDR10",
	VideoRangeHLG:                 "HLG",
	VideoRangeDOVI:                "DOVI",
	VideoRangeDOVIWithHDR10:       "DOVIWithHDR10",
	VideoRangeDOVIWithHLG:         "DOVIWithHLG",
	VideoRangeDOVIWithSDR:         "DOVIWithSDR",
	VideoRangeDOVIWithEL:          "DOVIWithEL",
	VideoRangeDOVIWithHDR10Plus:   "DOVIWithHDR10Plus",
	VideoRangeDOVIWithELHDR10Plus: "DOVIWithELHDR10Plus",
	VideoRangeDOVIInvalid:         "DOVIInvalid",
	VideoRangeHDR10Plus:           "HDR10Plus",
}

// TransportStreamTimestamp ports MediaBrowser.Model.MediaInfo.TransportStreamTimestamp.
type TransportStreamTimestamp int

const (
	TimestampNone TransportStreamTimestamp = iota
	TimestampZero
	TimestampValid
)

var transportStreamTimestampNames = map[TransportStreamTimestamp]string{
	TimestampNone:  "None",
	TimestampZero:  "Zero",
	TimestampValid: "Valid",
}

// enumString renders name maps; unknown values render numerically like .NET.
func enumString[T comparable](names map[T]string, v T, numeric func(T) string) string {
	if s, ok := names[v]; ok {
		return s
	}
	return numeric(v)
}

func (c ConditionType) String() string {
	return enumString(conditionTypeNames, c, func(v ConditionType) string { return strconv.Itoa(int(v)) })
}

func (p ConditionProperty) String() string {
	return enumString(conditionPropertyNames, p, func(v ConditionProperty) string { return strconv.Itoa(int(v)) })
}

func (v VideoRangeType) String() string {
	return enumString(videoRangeTypeNames, v, func(x VideoRangeType) string { return strconv.Itoa(int(x)) })
}

func (t TransportStreamTimestamp) String() string {
	return enumString(transportStreamTimestampNames, t, func(x TransportStreamTimestamp) string { return strconv.Itoa(int(x)) })
}

// parseEnum mirrors C# Enum.TryParse(value, ignoreCase: true): case-insensitive
// name match, plus bare numeric values (which .NET accepts even outside the
// defined range — we reject out-of-range numerics since no caller relies on
// undefined enum members; divergence contained here deliberately).
func parseEnum[T comparable](names map[T]string, s string, fromInt func(int) (T, bool)) (T, bool) {
	var zero T
	s = strings.TrimSpace(s)
	if s == "" {
		return zero, false
	}
	for v, name := range names {
		if strings.EqualFold(name, s) {
			return v, true
		}
	}
	if n, err := strconv.Atoi(s); err == nil {
		return fromInt(n)
	}
	return zero, false
}

// ParseVideoRangeType is the case-insensitive parse used by condition values
// and JSON decoding.
func ParseVideoRangeType(s string) (VideoRangeType, bool) {
	return parseEnum(videoRangeTypeNames, s, func(n int) (VideoRangeType, bool) {
		return VideoRangeType(n), true
	})
}

// ParseTransportStreamTimestamp is the case-insensitive parse; the boolean is
// false where upstream's Enum.Parse would throw.
func ParseTransportStreamTimestamp(s string) (TransportStreamTimestamp, bool) {
	return parseEnum(transportStreamTimestampNames, s, func(n int) (TransportStreamTimestamp, bool) {
		return TransportStreamTimestamp(n), true
	})
}

// ParseConditionType / ParseConditionProperty are used by ProfileCondition
// JSON decoding (spec serializes enums as strings; C# reads them
// case-insensitively and also accepts bare numbers).
func ParseConditionType(s string) (ConditionType, bool) {
	return parseEnum(conditionTypeNames, s, func(n int) (ConditionType, bool) {
		if n < 0 || n > int(ConditionEqualsAny) {
			return ConditionEquals, false
		}
		return ConditionType(n), true
	})
}

func ParseConditionProperty(s string) (ConditionProperty, bool) {
	return parseEnum(conditionPropertyNames, s, func(n int) (ConditionProperty, bool) {
		p := ConditionProperty(n)
		if _, ok := conditionPropertyNames[p]; !ok {
			return 0, false
		}
		return p, true
	})
}

// JSON round-trip: enums as their .NET string names; read accepts any casing
// and bare numbers (matching upstream's JsonStringEnumConverter behavior).

func marshalEnumJSON(s string) ([]byte, error) { return []byte(strconv.Quote(s)), nil }

func unmarshalEnumJSON[T any](data []byte, parse func(string) (T, bool), out *T, what string) error {
	str := strings.Trim(string(data), `"`)
	v, ok := parse(str)
	if !ok {
		return fmt.Errorf("invalid %s value %q", what, str)
	}
	*out = v
	return nil
}

func (c ConditionType) MarshalJSON() ([]byte, error) { return marshalEnumJSON(c.String()) }
func (c *ConditionType) UnmarshalJSON(b []byte) error {
	return unmarshalEnumJSON(b, ParseConditionType, c, "ProfileConditionType")
}

func (p ConditionProperty) MarshalJSON() ([]byte, error) { return marshalEnumJSON(p.String()) }
func (p *ConditionProperty) UnmarshalJSON(b []byte) error {
	return unmarshalEnumJSON(b, ParseConditionProperty, p, "ProfileConditionValue")
}

func (v VideoRangeType) MarshalJSON() ([]byte, error) {
	if name, ok := videoRangeTypeNames[v]; ok {
		return marshalEnumJSON(name)
	}
	return json.Marshal(int(v))
}

func (v *VideoRangeType) UnmarshalJSON(b []byte) error {
	return unmarshalEnumJSON(b, ParseVideoRangeType, v, "VideoRangeType")
}

func (t TransportStreamTimestamp) MarshalJSON() ([]byte, error) {
	if name, ok := transportStreamTimestampNames[t]; ok {
		return marshalEnumJSON(name)
	}
	return json.Marshal(int(t))
}

func (t *TransportStreamTimestamp) UnmarshalJSON(b []byte) error {
	return unmarshalEnumJSON(b, ParseTransportStreamTimestamp, t, "TransportStreamTimestamp")
}
