// SPDX-License-Identifier: GPL-2.0-only

package main

import "strings"

// The device profiles below transcribe what the tvOS clients send in
// PlaybackInfo. The source of truth is the SharkParty tree (a Swiftfin
// fork):
//
//   - Shared/Extensions/JellyfinAPI/DeviceProfile.swift (assembly)
//   - Shared/Objects/VideoPlayerType/VideoPlayerType+AVPlayer.swift
//   - Shared/Objects/VideoPlayerType/VideoPlayerType+VLC.swift
//   - Shared/Objects/VideoPlayerType/VideoPlayerType+Shared.swift
//   - Shared/Objects/PlaybackCapabilities.swift (hardware gates)
//
// The SharkParty native subtitle profiles are from branch subtitle-overlay
// (SharkParty PR #8, commit 844ca958). Stock Swiftfin is SharkParty main
// before that commit; its only difference is the AVPlayer subtitle profiles.
//
// The hardware gates are those of an Apple TV 4K (2nd or 3rd generation)
// on an HDR display with the default settings: HEVC, HDR10, HLG and Dolby
// Vision decode are available; AV1 and VP9 are not. forceHDRTranscode,
// forceDVTranscode and forceSubtitleBurnIn are off. The bitrate is the
// fixed lanBitrate instead of the client's "auto" bitrate test, and no
// maximum resolution is set.

const lanBitrate = 120_000_000

type deviceProfile struct {
	Name                             string              `json:"Name,omitempty"`
	MaxStreamingBitrate              int                 `json:"MaxStreamingBitrate,omitempty"`
	MaxStaticBitrate                 int                 `json:"MaxStaticBitrate,omitempty"`
	MusicStreamingTranscodingBitrate int                 `json:"MusicStreamingTranscodingBitrate,omitempty"`
	DirectPlayProfiles               []directPlayProfile `json:"DirectPlayProfiles"`
	TranscodingProfiles              []transcodeProfile  `json:"TranscodingProfiles"`
	CodecProfiles                    []codecProfile      `json:"CodecProfiles"`
	SubtitleProfiles                 []subtitleProfile   `json:"SubtitleProfiles"`
}

type directPlayProfile struct {
	Container  string `json:"Container,omitempty"`
	AudioCodec string `json:"AudioCodec,omitempty"`
	VideoCodec string `json:"VideoCodec,omitempty"`
	Type       string `json:"Type"`
}

type transcodeProfile struct {
	Container                 string `json:"Container,omitempty"`
	Type                      string `json:"Type"`
	VideoCodec                string `json:"VideoCodec,omitempty"`
	AudioCodec                string `json:"AudioCodec,omitempty"`
	Protocol                  string `json:"Protocol"`
	Context                   string `json:"Context"`
	EnableSubtitlesInManifest bool   `json:"EnableSubtitlesInManifest,omitempty"`
	MaxAudioChannels          string `json:"MaxAudioChannels,omitempty"`
	MinSegments               int    `json:"MinSegments,omitempty"`
	BreakOnNonKeyFrames       bool   `json:"BreakOnNonKeyFrames,omitempty"`
}

type codecProfile struct {
	Type       string             `json:"Type"`
	Codec      string             `json:"Codec,omitempty"`
	Conditions []profileCondition `json:"Conditions"`
}

type profileCondition struct {
	Condition  string `json:"Condition"`
	Property   string `json:"Property"`
	Value      string `json:"Value"`
	IsRequired bool   `json:"IsRequired"`
}

type subtitleProfile struct {
	Format string `json:"Format"`
	Method string `json:"Method"`
}

// Player keys. They are the column keys in atv-matrix.json.
const (
	playerNative   = "sharkparty-native"
	playerVLC      = "sharkparty-vlc"
	playerSwiftfin = "swiftfin-native"
)

// players lists the player keys in report order.
func players() []string { return []string{playerNative, playerVLC, playerSwiftfin} }

func playerTitle(key string) string {
	switch key {
	case playerNative:
		return "SharkParty Native (AVPlayer)"
	case playerVLC:
		return "SharkParty VLC (also stock Swiftfin's default player)"
	case playerSwiftfin:
		return "Stock Swiftfin Native (AVPlayer)"
	}
	return key
}

// usesAVPlayer reports whether the player renders through AVPlayer, so that
// the macOS AVFoundation probe is a fair model of it.
func usesAVPlayer(key string) bool { return key == playerNative || key == playerSwiftfin }

func profileFor(key string) deviceProfile {
	p := deviceProfile{
		Name:                             key,
		MaxStreamingBitrate:              lanBitrate,
		MaxStaticBitrate:                 lanBitrate,
		MusicStreamingTranscodingBitrate: lanBitrate,
	}
	switch key {
	case playerNative, playerSwiftfin:
		p.DirectPlayProfiles = avPlayerDirectPlay()
		p.TranscodingProfiles = avPlayerTranscoding()
		p.CodecProfiles = avPlayerCodecProfiles()
		if key == playerNative {
			p.SubtitleProfiles = sharkPartySubtitles()
		} else {
			p.SubtitleProfiles = swiftfinSubtitles()
		}
	case playerVLC:
		p.DirectPlayProfiles = vlcDirectPlay()
		p.TranscodingProfiles = vlcTranscoding()
		p.CodecProfiles = vlcCodecProfiles()
		p.SubtitleProfiles = vlcSubtitles()
	}
	return p
}

func csv(v ...string) string { return strings.Join(v, ",") }

func subs(method string, formats ...string) []subtitleProfile {
	out := make([]subtitleProfile, 0, len(formats))
	for _, f := range formats {
		out = append(out, subtitleProfile{Format: f, Method: method})
	}
	return out
}

func cond(c, prop, value string, required bool) profileCondition {
	return profileCondition{Condition: c, Property: prop, Value: value, IsRequired: required}
}

// VideoPlayerType+AVPlayer.swift _avPlayerDirectPlayProfiles, with
// supportsAV1 and supportsVP9 false and supportsHEVC true.
func avPlayerDirectPlay() []directPlayProfile {
	return []directPlayProfile{
		{Type: "Video", AudioCodec: csv("aac", "ac3", "alac", "eac3", "flac"), VideoCodec: csv("h264", "mpeg4", "hevc"), Container: csv("mp4", "m4v")},
		{Type: "Video", AudioCodec: csv("aac", "ac3", "alac", "eac3", "mp3", "pcm_s16be", "pcm_s16le", "pcm_s24be", "pcm_s24le"), VideoCodec: csv("h264", "mjpeg", "mpeg4", "hevc"), Container: "mov"},
		{Type: "Video", AudioCodec: csv("aac", "amr_nb"), VideoCodec: csv("h264", "mpeg4"), Container: csv("3g2", "3gp")},
		{Type: "Video", AudioCodec: csv("pcm_mulaw", "pcm_s16le"), VideoCodec: "mjpeg", Container: "avi"},
	}
}

func avPlayerTranscoding() []transcodeProfile {
	return []transcodeProfile{{
		Container: "mp4", Type: "Video", Protocol: "hls", Context: "Streaming",
		VideoCodec:                csv("hevc", "h264", "mpeg4"),
		AudioCodec:                csv("aac", "ac3", "alac", "eac3", "flac"),
		EnableSubtitlesInManifest: true, MaxAudioChannels: "8", MinSegments: 2, BreakOnNonKeyFrames: true,
	}}
}

// avPlayerHDRProfiles with supportsHLG, supportsHDR10 and
// supportsDolbyVision true.
const avPlayerRanges = "SDR|DOVIWithSDR|HLG|DOVIWithHLG|HDR10|HDR10Plus|DOVIWithHDR10|DOVIWithHDR10Plus|DOVIWithEL|DOVIWithELHDR10Plus|DOVI"

func h264Base() []profileCondition {
	return []profileCondition{
		cond("NotEquals", "IsAnamorphic", "true", false),
		cond("EqualsAny", "VideoProfile", "high|main|baseline|constrained baseline", false),
		cond("LessThanEqual", "VideoLevel", "80", false),
	}
}

func avPlayerCodecProfiles() []codecProfile {
	h264 := append(h264Base(),
		cond("EqualsAny", "VideoRangeType", "SDR|DOVIWithSDR", false),
		cond("NotEquals", "IsInterlaced", "true", false))
	hevc := []profileCondition{
		cond("NotEquals", "IsAnamorphic", "true", false),
		cond("EqualsAny", "VideoProfile", "main|main 10", false),
		cond("LessThanEqual", "VideoLevel", "175", false),
		cond("NotEquals", "IsInterlaced", "true", false),
		cond("EqualsAny", "VideoRangeType", avPlayerRanges, false),
		cond("EqualsAny", "VideoCodecTag", "hvc1|dvh1", true),
		cond("LessThanEqual", "VideoFramerate", "60", true),
	}
	av1 := []profileCondition{
		cond("NotEquals", "IsAnamorphic", "true", false),
		cond("NotEquals", "IsInterlaced", "true", false),
		cond("EqualsAny", "VideoRangeType", avPlayerRanges, false),
	}
	return []codecProfile{
		{Type: "Video", Codec: "h264", Conditions: h264},
		{Type: "Video", Codec: "hevc", Conditions: hevc},
		{Type: "Video", Codec: "av1", Conditions: av1},
	}
}

// sharkPartySubtitles is _avPlayerSubtitleProfiles on branch
// subtitle-overlay: External before HLS, PGS External instead of Encode.
func sharkPartySubtitles() []subtitleProfile {
	var out []subtitleProfile
	out = append(out, subs("Embed", "cc_dec", "ttml")...)
	out = append(out, subs("External", "subrip", "vtt", "ass", "ssa", "pgssub")...)
	out = append(out, subs("Encode", "dvbsub", "dvdsub", "xsub")...)
	out = append(out, subs("Hls", "vtt")...)
	return out
}

// swiftfinSubtitles is _avPlayerSubtitleProfiles on SharkParty main
// (stock Swiftfin).
func swiftfinSubtitles() []subtitleProfile {
	var out []subtitleProfile
	out = append(out, subs("Embed", "cc_dec", "ttml")...)
	out = append(out, subs("Encode", "dvbsub", "dvdsub", "pgssub", "xsub")...)
	out = append(out, subs("Hls", "vtt")...)
	return out
}

// VideoPlayerType+VLC.swift _vlcDirectPlayProfiles. No container list, so
// any container matches. AV1 is absent because supportsAV1 is false.
func vlcDirectPlay() []directPlayProfile {
	return []directPlayProfile{{
		Type: "Video",
		AudioCodec: csv("aac", "ac3", "alac", "amr_nb", "amr_wb", "dts", "eac3", "flac", "mp1", "mp2", "mp3", "nellymoser", "opus",
			"pcm_alaw", "pcm_bluray", "pcm_dvd", "pcm_mulaw", "pcm_s16be", "pcm_s16le", "pcm_s24be", "pcm_s24le", "pcm_u8",
			"speex", "vorbis", "wavpack", "wmalossless", "wmapro", "wmav1", "wmav2"),
		VideoCodec: csv("dirac", "dv", "ffv1", "flv1", "h261", "h263", "h264", "hevc", "mjpeg", "mpeg1video", "mpeg2video",
			"mpeg4", "msmpeg4v1", "msmpeg4v2", "msmpeg4v3", "prores", "theora", "vc1", "vp8", "vp9", "wmv1", "wmv2", "wmv3"),
	}}
}

func vlcTranscoding() []transcodeProfile {
	audio := csv("aac", "ac3", "alac", "dts", "eac3", "flac", "mp1", "mp2", "mp3", "opus", "vorbis")
	return []transcodeProfile{
		{
			Container: "mp4", Type: "Video", Protocol: "hls", Context: "Streaming", AudioCodec: audio,
			VideoCodec:       csv("h263", "h264", "hevc", "mjpeg", "mpeg1video", "mpeg2video", "mpeg4", "vp9"),
			MaxAudioChannels: "8", MinSegments: 2, BreakOnNonKeyFrames: true,
		},
		{
			Container: "mp4", Type: "Video", Protocol: "hls", Context: "Streaming", AudioCodec: audio,
			VideoCodec: "vc1", MaxAudioChannels: "8", MinSegments: 2, BreakOnNonKeyFrames: true,
		},
	}
}

// vlcHDRProfiles with hdrEnabled true. Note that plain DOVI (profile 5) is
// absent.
const vlcRanges = "SDR|DOVIWithSDR|HLG|HDR10|HDR10Plus|DOVIWithHLG|DOVIWithHDR10|DOVIWithHDR10Plus|DOVIWithEL|DOVIWithELHDR10Plus"

func vlcCodecProfiles() []codecProfile {
	other := func(codec string) codecProfile {
		return codecProfile{Type: "Video", Codec: codec, Conditions: []profileCondition{
			cond("NotEquals", "IsAnamorphic", "true", false),
			cond("NotEquals", "IsInterlaced", "true", false),
			cond("EqualsAny", "VideoRangeType", vlcRanges, true),
		}}
	}
	return []codecProfile{
		{Type: "Video", Codec: "h264", Conditions: append(h264Base(), cond("EqualsAny", "VideoRangeType", "SDR|DOVIWithSDR", true))},
		{Type: "Video", Codec: "hevc", Conditions: []profileCondition{
			cond("NotEquals", "IsAnamorphic", "true", false),
			cond("EqualsAny", "VideoProfile", "main|main 10", false),
			cond("NotEquals", "IsInterlaced", "true", false),
			cond("EqualsAny", "VideoRangeType", vlcRanges, true),
		}},
		other("av1"),
		other("vp9"),
	}
}

func vlcSubtitles() []subtitleProfile {
	var out []subtitleProfile
	out = append(out, subs("Embed", "ass", "cc_dec", "dvbsub", "dvdsub", "libzvbi_teletextdec", "mov_text", "mpl2", "pgssub", "pjs",
		"realtext", "sami", "ssa", "subrip", "subviewer", "subviewer1", "text", "ttml", "vplayer", "vtt", "xsub")...)
	out = append(out, subs("External", "subrip", "ass", "libzvbi_teletextdec", "mpl2", "pjs", "realtext", "sami", "ssa",
		"subviewer", "subviewer1", "text", "ttml", "vplayer")...)
	out = append(out, subs("Encode", "dvbsub", "dvdsub", "pgssub", "vtt", "xsub")...)
	return out
}
