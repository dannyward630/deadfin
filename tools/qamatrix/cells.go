// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"math"
	"strings"
)

// cell is one row of the Apple TV QA matrix. The matrix is not a cartesian
// product: each cell pins one high-risk combination, and the cells together
// cover every value of every dimension at least once and the risky pairs
// (container x video, video x player, audio x player, subtitle x player).
type cell struct {
	ID    string
	Group string
	Title string
	// Dims names the dimension values the cell pins. Keys: container,
	// video, range, fps, res, audio, subs, ops.
	Dims map[string]string
	// Risk says why the cell exists.
	Risk string
	// Pass says what passing means on the TV.
	Pass string
	// Human is non-empty when the cell needs an operator's eye that no
	// probe can replace.
	Human  string
	Expect map[string]Expect
	// Minutes is the auto-probe play time.
	Minutes int
	// Missing says how to source an item when the library has none.
	Missing string

	// pinned cells hold specific known-bad items. map takes them from
	// -pin or from the local matrix; match is the fallback.
	pinned   bool
	match    func(it item, ms mediaSource) bool
	audio    func(ms mediaSource) int // nil: server default
	subtitle func(ms mediaSource) int // nil: off
}

// Stream helpers. They read the ffprobe-derived MediaStreams only.

func videoOf(ms mediaSource) *mediaStream {
	for i := range ms.MediaStreams {
		if ms.MediaStreams[i].Type == "Video" {
			return &ms.MediaStreams[i]
		}
	}
	return nil
}

func streamsOf(ms mediaSource, typ string) []mediaStream {
	var out []mediaStream
	for _, s := range ms.MediaStreams {
		if s.Type == typ {
			out = append(out, s)
		}
	}
	return out
}

func containerIs(ms mediaSource, names ...string) bool {
	for _, part := range strings.Split(ms.Container, ",") {
		for _, n := range names {
			if strings.EqualFold(part, n) {
				return true
			}
		}
	}
	return false
}

func isMP4(ms mediaSource) bool { return containerIs(ms, "mp4", "m4v", "mov") }

func vid(ms mediaSource, codec string, f func(v mediaStream) bool) bool {
	v := videoOf(ms)
	if v == nil || !strings.EqualFold(v.Codec, codec) {
		return false
	}
	return f == nil || f(*v)
}

func rangeIs(v mediaStream, t string) bool { return v.VideoRangeType == t }

func fpsNear(v mediaStream, want float64) bool { return math.Abs(v.RealFrameRate-want) < 0.01 }

func is1080(v mediaStream) bool { return v.Height > 720 && v.Height <= 1088 }
func is4K(v mediaStream) bool   { return v.Height > 1088 }
func isSD(v mediaStream) bool   { return v.Height > 0 && v.Height <= 576 }

// onlyAudio reports whether the source has exactly one audio track and it
// matches f.
func onlyAudio(ms mediaSource, f func(a mediaStream) bool) bool {
	a := streamsOf(ms, "Audio")
	return len(a) == 1 && f(a[0])
}

func firstAudio(ms mediaSource, f func(a mediaStream) bool) bool {
	a := streamsOf(ms, "Audio")
	return len(a) > 0 && f(a[0])
}

func codec(c string) func(a mediaStream) bool {
	return func(a mediaStream) bool { return strings.EqualFold(a.Codec, c) }
}

func audioCh(c string, ch int) func(a mediaStream) bool {
	return func(a mediaStream) bool { return strings.EqualFold(a.Codec, c) && a.Channels == ch }
}

func noSubs(ms mediaSource) bool { return len(streamsOf(ms, "Subtitle")) == 0 }

func hasSub(ms mediaSource, f func(s mediaStream) bool) bool { return subIndex(ms, f) >= 0 }

func subIndex(ms mediaSource, f func(s mediaStream) bool) int {
	for _, s := range streamsOf(ms, "Subtitle") {
		if f(s) {
			return s.Index
		}
	}
	return -1
}

func subCodec(c string, external, forced bool) func(s mediaStream) bool {
	return func(s mediaStream) bool {
		return strings.EqualFold(s.Codec, c) && s.IsExternal == external && s.IsForced == forced
	}
}

func pickSub(f func(s mediaStream) bool) func(ms mediaSource) int {
	return func(ms mediaSource) int { return subIndex(ms, f) }
}

func hasFonts(ms mediaSource) bool { return len(ms.MediaAttachments) > 0 }

// secondAudio selects the second audio track (an audio-track switch).
func secondAudio(ms mediaSource) int {
	a := streamsOf(ms, "Audio")
	if len(a) < 2 {
		return -1
	}
	return a[1].Index
}

func commentaryIndex(ms mediaSource) int {
	for _, a := range streamsOf(ms, "Audio") {
		if strings.Contains(strings.ToLower(a.Title), "commentary") {
			return a.Index
		}
	}
	return -1
}

func expect(native, vlc, swiftfin Expect) map[string]Expect {
	return map[string]Expect{playerNative: native, playerVLC: vlc, playerSwiftfin: swiftfin}
}

func ex(method, audio, sub string) Expect { return Expect{Method: method, Audio: audio, Subtitle: sub} }

const (
	dp = "DirectPlay"
	rx = "Remux"
	tc = "Transcode"
	cp = "copy"
	at = "transcode"
)

const (
	passStream = "Starts in under 5 s; no audio dropout, A/V drift, stutter or green frames for the full play time; seeks land within 2 s of the target and resume audio and video together."
	passSubs   = passStream + " The selected subtitle shows with correct timing; turning it off removes it."
)

func byID(ids map[string]bool) func(it item, ms mediaSource) bool {
	return func(it item, _ mediaSource) bool { return ids[it.ID] }
}

// cells returns the matrix in report order.
func cells() []cell {
	return []cell{
		// Regression cells.
		{
			ID: "ATV-R01", Group: "Regression", Title: "MKV HEVC Main10 SDR 23.976 AAC 2.0 + ASS with fonts",
			Dims: map[string]string{"container": "MKV", "video": "HEVC 10-bit SDR", "fps": "23.976", "res": "1080p", "audio": "AAC 2.0", "subs": "ASS with fonts", "ops": "10 min play, seeks"},
			Risk: "HLS fMP4 copy-remux relaunched ffmpeg per segment, so AVPlayer dropped audio for about 2 s at a time.",
			Pass: "No audio dropout over 10 min of play and after each seek. ASS styling and fonts render (SharkParty overlay).", Minutes: 10,
			pinned: true, subtitle: pickSub(subCodec("ass", false, false)),
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", func(v mediaStream) bool { return v.BitDepth == 10 && v.VideoRangeType == "SDR" && fpsNear(v, 23.976) }) &&
					onlyAudio(ms, audioCh("aac", 2)) && hasFonts(ms) && hasSub(ms, subCodec("ass", false, false))
			},
			Expect: expect(ex(rx, cp, "External"), ex(dp, cp, "Embed"), ex(tc, cp, "Encode")),
		},
		{
			ID: "ATV-R02", Group: "Regression", Title: "MKV HEVC Main 8-bit 23.976 AAC 2.0, short episode",
			Dims: map[string]string{"container": "MKV", "video": "HEVC 8-bit", "fps": "23.976", "res": "1080p", "audio": "AAC 2.0", "subs": "none", "ops": "full play, seeks"},
			Risk: "Second item that showed the audio dropouts; short episode, so the probe covers it end to end.",
			Pass: passStream, Minutes: 10, pinned: true,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", func(v mediaStream) bool { return v.BitDepth == 8 && fpsNear(v, 23.976) }) &&
					onlyAudio(ms, audioCh("aac", 2)) && !hasSub(ms, func(mediaStream) bool { return true })
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},

		// Container x video codec.
		{
			ID: "ATV-V01", Group: "Video", Title: "MP4 H.264 High 1080p 23.976 AAC 2.0",
			Dims: map[string]string{"container": "MP4", "video": "H.264", "fps": "23.976", "res": "1080p", "audio": "AAC 2.0", "subs": "none"},
			Risk: "Baseline direct play for all three players.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return isMP4(ms) && vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" && is1080(v) && fpsNear(v, 23.976) }) &&
					onlyAudio(ms, audioCh("aac", 2))
			},
			Expect: expect(ex(dp, cp, ""), ex(dp, cp, ""), ex(dp, cp, "")),
		},
		{
			ID: "ATV-V02", Group: "Video", Title: "MKV H.264 High 1080p AC3 5.1 + embedded SRT",
			Dims: map[string]string{"container": "MKV", "video": "H.264", "fps": "23.976", "res": "1080p", "audio": "AC3 5.1", "subs": "embedded SRT"},
			Risk: "The most common library shape: H.264 copy-remux into fMP4 with AC3 copy.", Pass: passSubs, Minutes: 5,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" && is1080(v) && v.BitDepth == 8 }) &&
					onlyAudio(ms, audioCh("ac3", 6)) && hasSub(ms, subCodec("subrip", false, false))
			},
			subtitle: pickSub(subCodec("subrip", false, false)),
			Expect:   expect(ex(rx, cp, "External"), ex(dp, cp, "Embed"), ex(rx, cp, "Hls")),
		},
		{
			ID: "ATV-V03", Group: "Video", Title: "MP4 HEVC Main 8-bit 1080p AAC",
			Dims: map[string]string{"container": "MP4", "video": "HEVC 8-bit", "fps": "23.976", "res": "1080p", "audio": "AAC"},
			Risk: "AVPlayer requires codec tag hvc1/dvh1. Pinned upstream never fills CodecTag (MediaStreamInfo.cs:204 binds codec_tag_string?), so HEVC MP4 remuxes instead of direct play. Parity, not a defect.",
			Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return isMP4(ms) && vid(ms, "hevc", func(v mediaStream) bool { return v.BitDepth == 8 && is1080(v) }) && firstAudio(ms, codec("aac"))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-V04", Group: "Video", Title: "MKV HEVC Main10 SDR 720p 29.97 EAC3 5.1",
			Dims: map[string]string{"container": "MKV", "video": "HEVC 10-bit SDR", "fps": "29.97", "res": "720p", "audio": "EAC3 5.1"},
			Risk: "29.97 copy-remux: segment boundaries on a non-film cadence; EAC3 copy into fMP4.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", func(v mediaStream) bool {
					return v.BitDepth == 10 && rangeIs(v, "SDR") && v.Height == 720 && fpsNear(v, 29.97)
				}) && firstAudio(ms, audioCh("eac3", 6))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-V05", Group: "Video", Title: "MKV HEVC HDR10 4K TrueHD Atmos 7.1",
			Dims: map[string]string{"container": "MKV", "video": "HDR10", "fps": "23.976", "res": "4K", "audio": "TrueHD/Atmos 7.1"},
			Risk: "4K HDR10 copy-remux at high bitrate with an audio transcode from TrueHD; HDR mode switch on the TV.",
			Pass: passStream + " The TV switches to HDR10.", Human: "HDR mode switch; audio is a PCM/AAC/AC3 transcode, not Atmos.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "HDR10") && is4K(v) }) &&
					firstAudio(ms, func(a mediaStream) bool { return a.Codec == "truehd" && a.Channels == 8 })
			},
			Expect: expect(ex(rx, at, ""), ex(rx, at, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-V06", Group: "Video", Title: "MKV HEVC HDR10+ 4K",
			Dims: map[string]string{"container": "MKV", "video": "HDR10+", "res": "4K"},
			Risk: "HDR10+ dynamic metadata survives the copy-remux only if the SEI is copied; the TV shows HDR10+ or plain HDR10.",
			Pass: passStream, Human: "HDR mode on the TV: HDR10+ (or HDR10 on TVs without HDR10+).", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "HDR10Plus") })
			},
			Expect: expect(ex(rx, "", ""), ex(dp, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-V07", Group: "Video", Title: "Dolby Vision profile 5 (DOVI, no HDR10 fallback)",
			Dims: map[string]string{"container": "MKV", "video": "Dolby Vision P5", "res": "4K"},
			Risk: "P5 has no HDR10 base layer: AVPlayer must see a dvh1 sample entry, and VLC (no DOVI in its range list) gets a transcode that needs tone mapping or shows purple/green.",
			Pass: passStream + " Colors are correct (no purple/green cast). The TV shows Dolby Vision on AVPlayer.", Human: "Dolby Vision mode switch and colors.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "DOVI") })
			},
			Expect: expect(ex(rx, "", ""), ex(tc, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-V08", Group: "Video", Title: "Dolby Vision profile 7 (DOVIWithEL)",
			Dims: map[string]string{"container": "MKV", "video": "Dolby Vision P7", "res": "4K"},
			Risk: "Dual-layer UHD BD remux. AVPlayer plays the base layer; the enhancement layer is dropped.",
			Pass: passStream + " The TV shows HDR10 or Dolby Vision.", Human: "HDR/DV mode and colors.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "DOVIWithEL") })
			},
			Expect: expect(ex(rx, "", ""), ex(dp, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-V09", Group: "Video", Title: "Dolby Vision profile 8.1 (DOVIWithHDR10)",
			Dims: map[string]string{"container": "MKV", "video": "Dolby Vision P8", "res": "4K"},
			Risk: "The common streaming DV shape; the remux must keep the RPU and tag dvh1 so AVPlayer engages DV.",
			Pass: passStream + " The TV shows Dolby Vision.", Human: "Dolby Vision mode switch.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "DOVIWithHDR10") })
			},
			Expect: expect(ex(rx, "", ""), ex(dp, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-V10", Group: "Video", Title: "MKV AV1 10-bit + Opus",
			Dims: map[string]string{"container": "MKV", "video": "AV1", "audio": "Opus"},
			Risk: "Apple TV 4K has no AV1 decode: a full video transcode (QSV on live). Opus is copied for VLC only.",
			Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "av1", func(v mediaStream) bool { return v.BitDepth == 10 }) && firstAudio(ms, codec("opus"))
			},
			Expect: expect(ex(tc, at, ""), ex(tc, cp, ""), ex(tc, at, "")),
		},
		{
			ID: "ATV-V11", Group: "Video", Title: "VC-1 (Blu-ray remux) with DTS or AC3",
			Dims: map[string]string{"video": "VC-1"},
			Risk: "Legacy codec: AVPlayer needs a full transcode; VLC plays it directly. With TrueHD first, VLC also transcodes the video, because HLS cannot carry VC-1 (StreamBuilder.cs:31 _supportedHlsVideoCodecs).",
			Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "vc1", nil) && firstAudio(ms, func(a mediaStream) bool { return a.Codec == "dts" || a.Codec == "ac3" })
			},
			Expect: expect(ex(tc, "", ""), ex(dp, "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-V12", Group: "Video", Title: "MPEG-2 video (DVD or broadcast)",
			Dims: map[string]string{"container": "TS/MPEG-PS", "video": "MPEG-2", "res": "SD"},
			Risk: "MPEG-2 in a program or transport stream; AVPlayer needs a transcode, often with deinterlace.", Pass: passStream, Minutes: 3,
			match:   func(_ item, ms mediaSource) bool { return vid(ms, "mpeg2video", nil) },
			Expect:  expect(ex(tc, "", ""), ex(dp, "", ""), ex(tc, "", "")),
			Missing: "ffmpeg -f lavfi -i testsrc2=size=720x480:rate=30000/1001 -f lavfi -i sine=f=440:sample_rate=48000 -t 900 -c:v mpeg2video -b:v 6M -flags +ildct+ilme -c:a ac3 -b:a 192k -f vob sample-mpeg2.vob",
		},
		{
			ID: "ATV-V13", Group: "Video", Title: "MKV H.264 High 10 (Hi10P anime) + FLAC",
			Dims: map[string]string{"container": "MKV", "video": "H.264 10-bit", "audio": "FLAC"},
			Risk: "No Apple decoder for H.264 10-bit, so both players transcode the video; FLAC audio.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High 10" }) && firstAudio(ms, codec("flac"))
			},
			Expect: expect(ex(tc, cp, ""), ex(tc, cp, ""), ex(tc, cp, "")),
		},
		{
			ID: "ATV-V14", Group: "Video", Title: "MPEG-TS H.264 progressive + AAC",
			Dims: map[string]string{"container": "TS/M2TS", "video": "H.264", "audio": "AAC"},
			Risk: "TS source timestamps (non-zero start, PCR wrap) through an fMP4 remux.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "ts", "m2ts", "mpegts") && vid(ms, "h264", func(v mediaStream) bool { return !v.IsInterlaced }) &&
					firstAudio(ms, codec("aac"))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-V15", Group: "Video", Title: "AVI MPEG-4 ASP (XviD) + MP3",
			Dims: map[string]string{"container": "AVI", "video": "MPEG-4 ASP", "audio": "MP3", "res": "SD"},
			Risk: "Legacy AVI: packed B-frames and VFR-like timestamps; HLS cannot carry MPEG-4 ASP, so AVPlayer transcodes.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "avi") && vid(ms, "mpeg4", nil) && firstAudio(ms, codec("mp3"))
			},
			Expect: expect(ex(tc, at, ""), ex(dp, cp, ""), ex(tc, at, "")),
		},
		{
			ID: "ATV-V16", Group: "Video", Title: "HEVC Range Extensions (Rext, 4:2:2/4:4:4)",
			Dims: map[string]string{"video": "HEVC Rext"},
			Risk: "Profile outside main/main 10: every player transcodes.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return v.Profile == "Rext" })
			},
			Expect: expect(ex(tc, "", ""), ex(tc, "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-V17", Group: "Video", Title: "4K HEVC 10-bit SDR",
			Dims: map[string]string{"video": "HEVC 10-bit SDR", "res": "4K"},
			Risk: "4K SDR copy-remux: high segment sizes and no HDR mode switch.", Pass: passStream + " The TV stays in SDR.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return is4K(v) && rangeIs(v, "SDR") && v.BitDepth == 10 })
			},
			Expect: expect(ex(rx, "", ""), ex(dp, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-V18", Group: "Video", Title: "SD H.264 25 fps (PAL)",
			Dims: map[string]string{"video": "H.264", "fps": "25", "res": "SD"},
			Risk: "PAL cadence and SD scaling; display frame-rate match on the TV.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "h264", func(v mediaStream) bool { return isSD(v) && fpsNear(v, 25) && !v.IsInterlaced })
			},
			Expect: expect(ex("", "", ""), ex(dp, "", ""), ex("", "", "")),
		},
		{
			ID: "ATV-V19", Group: "Video", Title: "24.000 fps 1080p",
			Dims: map[string]string{"fps": "24", "res": "1080p"},
			Risk: "Exact 24p against the 23.976 majority; match-frame-rate on the TV.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				v := videoOf(ms)
				return v != nil && fpsNear(*v, 24) && is1080(*v) && (v.Codec == "h264" || v.Codec == "hevc")
			},
			Expect: expect(ex("", "", ""), ex(dp, "", ""), ex("", "", "")),
		},
		{
			ID: "ATV-V20", Group: "Video", Title: "50 fps",
			Dims: map[string]string{"fps": "50"},
			Risk: "PAL broadcast/sport cadence.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				v := videoOf(ms)
				return v != nil && fpsNear(*v, 50)
			},
			Expect:  expect(ex("", "", ""), ex(dp, "", ""), ex("", "", "")),
			Missing: "ffmpeg -f lavfi -i testsrc2=size=1920x1080:rate=50 -f lavfi -i sine=f=440:sample_rate=48000 -t 900 -c:v libx264 -profile:v high -pix_fmt yuv420p -c:a aac -b:a 160k sample-h264-50p.mkv",
		},
		{
			ID: "ATV-V21", Group: "Video", Title: "59.94 fps",
			Dims: map[string]string{"fps": "59.94"},
			Risk: "High frame rate: HEVC is capped at 60 fps in the AVPlayer profile; decode load and frame drops.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				v := videoOf(ms)
				return v != nil && (fpsNear(*v, 59.94) || fpsNear(*v, 60))
			},
			Expect: expect(ex("", "", ""), ex(dp, "", ""), ex("", "", "")),
		},
		{
			ID: "ATV-V22", Group: "Video", Title: "Variable frame rate (RealFrameRate differs from AverageFrameRate)",
			Dims: map[string]string{"fps": "VFR"},
			Risk: "VFR sources break fixed-length segment math and A/V sync in a copy-remux.", Pass: passStream, Minutes: 5,
			match: func(_ item, ms mediaSource) bool {
				v := videoOf(ms)
				return v != nil && !v.IsAnamorphic && v.AverageFrameRate > 1 && math.Abs(v.RealFrameRate-v.AverageFrameRate) > 1
			},
			Expect:  expect(ex("", "", ""), ex(dp, "", ""), ex("", "", "")),
			Missing: "Use a phone screen recording (VFR H.264 in MP4).",
		},
		{
			ID: "ATV-V24", Group: "Video", Title: "Anamorphic SD H.264 (DVD rip)",
			Dims: map[string]string{"video": "H.264 anamorphic", "res": "SD"},
			Risk: "Every tvOS profile rejects anamorphic H.264, so all players transcode to square pixels; the aspect ratio must survive.", Pass: passStream + " The picture is not stretched or squeezed.",
			Human: "Aspect ratio.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "h264", func(v mediaStream) bool { return v.IsAnamorphic && isSD(v) })
			},
			Expect: expect(ex(tc, "", ""), ex(tc, "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-V23", Group: "Video", Title: "Interlaced H.264",
			Dims: map[string]string{"video": "H.264 interlaced"},
			Risk: "AVPlayer profile rejects interlaced H.264: a deinterlacing transcode (QSV deinterlace on live).", Pass: passStream + " No combing.",
			Human: "Bandwidth: the mapped item declares 28.57 Mbit/s in the master playlist, but segments peak at 38.1 Mbit/s, so macOS AVPlayer logs -12318 (upstream parity). On the TV, watch for stalls or quality drops during play and after a seek.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "h264", func(v mediaStream) bool { return v.IsInterlaced })
			},
			Expect: expect(ex(tc, "", ""), ex(dp, "", ""), ex(tc, "", "")),
		},

		// Audio.
		{
			ID: "ATV-A01", Group: "Audio", Title: "DTS 5.1 core",
			Dims: map[string]string{"audio": "DTS 5.1"},
			Risk: "AVPlayer cannot decode DTS: audio transcode in a video-copy remux.", Pass: passStream + " Audio is 5.1 at the receiver.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return onlyAudio(ms, func(a mediaStream) bool { return a.Codec == "dts" && a.Profile == "DTS" && a.Channels == 6 }) &&
					vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" })
			},
			Expect: expect(ex(rx, at, ""), ex(dp, cp, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-A02", Group: "Audio", Title: "DTS-HD MA 7.1",
			Dims: map[string]string{"audio": "DTS-HD MA 7.1"},
			Risk: "8-channel transcode: channel layout and the AAC 7.1 path into AVPlayer.", Pass: passStream + " 7.1 channels map correctly.", Human: "Channel mapping (rear vs side surrounds).", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return firstAudio(ms, func(a mediaStream) bool { return a.Codec == "dts" && a.Profile == "DTS-HD MA" && a.Channels == 8 })
			},
			Expect: expect(ex("", at, ""), ex(dp, cp, ""), ex("", at, "")),
		},
		{
			ID: "ATV-A03", Group: "Audio", Title: "TrueHD Atmos below 4K",
			Dims: map[string]string{"audio": "TrueHD/Atmos"},
			Risk: "TrueHD is in no tvOS profile: every player transcodes audio; Atmos is lost.", Pass: passStream, Human: "Receiver shows multichannel PCM/AC3, not Atmos (expected).", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return firstAudio(ms, func(a mediaStream) bool { return a.Codec == "truehd" && strings.Contains(a.Profile, "Atmos") }) &&
					!vid(ms, "hevc", func(v mediaStream) bool { return is4K(v) })
			},
			Expect: expect(ex("", at, ""), ex("", at, ""), ex("", at, "")),
		},
		{
			ID: "ATV-A04", Group: "Audio", Title: "EAC3 Atmos (DD+ JOC)",
			Dims: map[string]string{"audio": "EAC3/Atmos"},
			Risk: "EAC3 copy must keep the JOC extension so the receiver shows Atmos.", Pass: passStream, Human: "Receiver shows Dolby Atmos.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return firstAudio(ms, func(a mediaStream) bool { return a.Codec == "eac3" && strings.Contains(a.Profile, "Atmos") })
			},
			Expect: expect(ex("", cp, ""), ex(dp, cp, ""), ex("", cp, "")),
		},
		{
			ID: "ATV-A05", Group: "Audio", Title: "FLAC 2.0 in MKV",
			Dims: map[string]string{"audio": "FLAC 2.0"},
			Risk: "FLAC copied into fMP4 HLS: AVPlayer support for fLaC in HLS is thin.", Pass: passStream, Minutes: 5,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && onlyAudio(ms, audioCh("flac", 2)) &&
					vid(ms, "hevc", func(v mediaStream) bool { return v.Profile == "Main 10" || v.Profile == "Main" })
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-A06", Group: "Audio", Title: "Opus 2.0 in MKV",
			Dims: map[string]string{"audio": "Opus 2.0"},
			Risk: "Opus is not in the AVPlayer profile: audio transcode beside a video copy.", Pass: passStream, Minutes: 5,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && onlyAudio(ms, audioCh("opus", 2)) &&
					vid(ms, "hevc", func(v mediaStream) bool { return v.Profile == "Main" || v.Profile == "Main 10" })
			},
			Expect: expect(ex(rx, at, ""), ex(dp, cp, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-A07", Group: "Audio", Title: "Opus 5.1 in MKV",
			Dims: map[string]string{"audio": "Opus 5.1"},
			Risk: "Opus multichannel mapping (Vorbis order) into a 5.1 transcode.", Pass: passStream + " Centre dialogue stays in the centre.", Human: "Channel mapping.", Minutes: 3,
			match:  func(_ item, ms mediaSource) bool { return containerIs(ms, "mkv") && firstAudio(ms, audioCh("opus", 6)) },
			Expect: expect(ex("", at, ""), ex(dp, cp, ""), ex("", at, "")),
		},
		{
			ID: "ATV-A08", Group: "Audio", Title: "MP3 2.0 in MKV",
			Dims: map[string]string{"audio": "MP3"},
			Risk: "MP3 is direct-playable only in MOV for AVPlayer; in MKV it transcodes.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && firstAudio(ms, codec("mp3")) && !vid(ms, "h264", func(v mediaStream) bool { return v.IsAnamorphic })
			},
			Expect:  expect(ex("", at, ""), ex(dp, cp, ""), ex("", at, "")),
			Missing: "ffmpeg -f lavfi -i testsrc2=size=1280x720:rate=24000/1001 -f lavfi -i sine=f=440:sample_rate=44100 -t 900 -c:v libx264 -c:a libmp3lame -b:a 192k sample-mp3.mkv",
		},
		{
			ID: "ATV-A09", Group: "Audio", Title: "AC3 mono",
			Dims: map[string]string{"audio": "AC3 1.0"},
			Risk: "Mono must play from the centre (or both fronts), not one side.", Pass: passStream, Human: "Mono plays centred.", Minutes: 3,
			match:  func(_ item, ms mediaSource) bool { return firstAudio(ms, audioCh("ac3", 1)) },
			Expect: expect(ex("", cp, ""), ex(dp, cp, ""), ex("", cp, "")),
		},
		{
			ID: "ATV-A10", Group: "Audio", Title: "MP4 AAC 5.1",
			Dims: map[string]string{"container": "MP4", "audio": "AAC 5.1"},
			Risk: "Multichannel AAC direct play.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return isMP4(ms) && onlyAudio(ms, audioCh("aac", 6)) && vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" || v.Profile == "Main" })
			},
			Expect: expect(ex(dp, cp, ""), ex(dp, cp, ""), ex(dp, cp, "")),
		},
		{
			ID: "ATV-A11", Group: "Audio", Title: "HE-AAC (SBR, v2 where the library has it)",
			Dims: map[string]string{"audio": "HE-AACv2"},
			Risk: "Parametric stereo copied into fMP4; a wrong ASC plays mono or at half speed.", Pass: passStream + " Audio is stereo at the right pitch.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				a := streamsOf(ms, "Audio")
				return len(a) > 0 && a[0].Codec == "aac" && (a[0].Profile == "HE-AACv2" || a[0].Profile == "HE-AAC")
			},
			Expect: expect(ex("", cp, ""), ex(dp, cp, ""), ex("", cp, "")),
		},
		{
			ID: "ATV-A12", Group: "Audio", Title: "Dual audio: switch to the second track",
			Dims: map[string]string{"audio": "multiple tracks", "ops": "audio-track switch"},
			Risk: "Swiftfin restarts playback with a new AudioStreamIndex; the remux must map the selected track.", Pass: passStream + " The selected language plays.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				a := streamsOf(ms, "Audio")
				return containerIs(ms, "mkv") && len(a) == 2 && a[0].Language != a[1].Language && a[0].Codec == a[1].Codec &&
					(a[0].Codec == "aac" || a[0].Codec == "ac3" || a[0].Codec == "eac3") && vid(ms, "hevc", nil)
			},
			audio:  secondAudio,
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-A13", Group: "Audio", Title: "Commentary track selected",
			Dims: map[string]string{"audio": "commentary"},
			Risk: "A non-default secondary track; mapping by index, not by default flag.", Pass: passStream + " The commentary plays.", Minutes: 3,
			match:  func(_ item, ms mediaSource) bool { return commentaryIndex(ms) >= 0 },
			audio:  commentaryIndex,
			Expect: expect(ex("", "", ""), ex("", "", ""), ex("", "", "")),
		},
		{
			ID: "ATV-A14", Group: "Audio", Title: "PCM s16le in MKV",
			Dims: map[string]string{"audio": "PCM"},
			Risk: "PCM is direct-playable only from MOV for AVPlayer.", Pass: passStream, Minutes: 3,
			match:  func(_ item, ms mediaSource) bool { return containerIs(ms, "mkv") && firstAudio(ms, codec("pcm_s16le")) },
			Expect: expect(ex("", at, ""), ex(dp, cp, ""), ex("", at, "")),
		},

		// Subtitles.
		{
			ID: "ATV-S01", Group: "Subtitles", Title: "External SRT sidecar",
			Dims: map[string]string{"subs": "external SRT"},
			Risk: "Sidecar delivery and the subtitle switch/off path.", Pass: passSubs, Minutes: 3,
			match:    func(_ item, ms mediaSource) bool { return hasSub(ms, subCodec("subrip", true, false)) },
			subtitle: pickSub(subCodec("subrip", true, false)),
			Expect:   expect(ex("", "", "External"), ex("", "", "External"), ex("", "", "")),
		},
		{
			ID: "ATV-S02", Group: "Subtitles", Title: "Embedded forced SRT",
			Dims: map[string]string{"subs": "forced"},
			Risk: "Forced tracks must show without a selection (client) and deliver like any SRT (server).", Pass: passSubs, Human: "Forced subtitles appear without selecting a track.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && hasSub(ms, subCodec("subrip", false, true))
			},
			subtitle: pickSub(subCodec("subrip", false, true)),
			Expect:   expect(ex("", "", "External"), ex("", "", "Embed"), ex("", "", "Hls")),
		},
		{
			ID: "ATV-S03", Group: "Subtitles", Title: "ASS with embedded fonts (styled anime)",
			Dims: map[string]string{"subs": "ASS with fonts"},
			Risk: "SharkParty overlay draws ASS with MKV fonts; stock Swiftfin burns it in (ASS cannot convert to WebVTT upstream).", Pass: passSubs + " Styling, positioning and fonts match the source.",
			Human: "Subtitle styling (fonts, karaoke, signs).", Minutes: 3,
			match: func(it item, ms mediaSource) bool {
				return hasFonts(ms) && hasSub(ms, subCodec("ass", false, false)) && vid(ms, "h264", nil)
			},
			subtitle: pickSub(subCodec("ass", false, false)),
			Expect:   expect(ex("", "", "External"), ex("", "", "Embed"), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-S04", Group: "Subtitles", Title: "PGS (Blu-ray image subtitles)",
			Dims: map[string]string{"subs": "PGS"},
			Risk: "SharkParty gets the .sup as External; stock Swiftfin burns PGS in, a full transcode.", Pass: passSubs, Human: "PGS position and size on 4K. On stock Swiftfin (an iGPU limit): the seek segment takes 6.3-6.7 s and the transcode runs at about 1.5x realtime. Confirm that play recovers after a seek and does not stall.", Minutes: 3,
			match:    func(_ item, ms mediaSource) bool { return hasSub(ms, subCodec("PGSSUB", false, false)) },
			subtitle: pickSub(subCodec("PGSSUB", false, false)),
			Expect:   expect(ex("", "", "External"), ex("", "", "Embed"), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-S05", Group: "Subtitles", Title: "VobSub (DVD image subtitles)",
			Dims: map[string]string{"subs": "VobSub"},
			Risk: "AVPlayer profiles burn DVD subtitles in: a full transcode with overlay.", Pass: passSubs, Minutes: 3,
			match:    func(_ item, ms mediaSource) bool { return hasSub(ms, subCodec("DVDSUB", false, false)) },
			subtitle: pickSub(subCodec("DVDSUB", false, false)),
			Expect:   expect(ex(tc, "", "Encode"), ex("", "", "Embed"), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-S06", Group: "Subtitles", Title: "Forced ASS",
			Dims: map[string]string{"subs": "forced ASS"},
			Risk: "Signs-and-songs tracks are forced ASS; same delivery as ASS.", Pass: passSubs, Minutes: 3,
			match:    func(_ item, ms mediaSource) bool { return hasSub(ms, subCodec("ass", false, true)) },
			subtitle: pickSub(subCodec("ass", false, true)),
			Expect:   expect(ex("", "", "External"), ex("", "", "Embed"), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-S07", Group: "Subtitles", Title: "MP4 mov_text",
			Dims: map[string]string{"container": "MP4", "subs": "mov_text"},
			Risk: "mov_text is in no AVPlayer profile: SharkParty converts it to External SRT; stock Swiftfin has no direct-play text path, so it goes to HLS with WebVTT in the manifest.", Pass: passSubs, Minutes: 3,
			match:    func(_ item, ms mediaSource) bool { return isMP4(ms) && hasSub(ms, subCodec("mov_text", false, false)) },
			subtitle: pickSub(subCodec("mov_text", false, false)),
			Expect:   expect(ex("", "", "External"), ex("", "", "Embed"), ex("", "", "Hls")),
		},
		{
			ID: "ATV-S08", Group: "Subtitles", Title: "Subtitles off on a file with many tracks",
			Dims: map[string]string{"subs": "none (off)", "ops": "subtitle switch and off"},
			Risk: "Off must not burn in or embed anything.", Pass: passStream + " No subtitle shows.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && len(streamsOf(ms, "Subtitle")) >= 5 && vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" }) &&
					firstAudio(ms, codec("aac"))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},

		// Operations that need a series context or the TV itself.
		{
			ID: "ATV-O01", Group: "Operations", Title: "Next-episode autoplay across a remux",
			Dims: map[string]string{"ops": "next-episode autoplay, resume from position"},
			Risk: "End-of-stream on HLS, the next PlaybackInfo, and the resume point of the next episode.", Pass: "The next episode starts by itself; stopping mid-episode and starting again resumes within 10 s of the stop point.",
			Human: "Autoplay countdown and resume.", Minutes: 2,
			match: func(it item, ms mediaSource) bool {
				return it.Type == "Episode" && containerIs(ms, "mkv") && vid(ms, "h264", nil) && onlyAudio(ms, audioCh("aac", 2)) && noSubs(ms)
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-O02", Group: "Operations", Title: "Background and foreground during a transcode",
			Dims: map[string]string{"ops": "background/foreground, pause/resume"},
			Risk: "The server kills an idle HLS job after 60 s; returning from the home screen must restart cleanly.", Pass: "After 2 min on the home screen, the app resumes at the same position.",
			Human: "Background for 2 min, then return.", Minutes: 2,
			match:  func(_ item, ms mediaSource) bool { return vid(ms, "av1", nil) },
			Expect: expect(ex(tc, "", ""), ex(tc, "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-O03", Group: "Operations", Title: "HDR/SDR mode switch between items",
			Dims: map[string]string{"ops": "HDR mode switch"},
			Risk: "Going from an HDR10 item to an SDR item must return the TV to SDR.", Pass: "The TV switches to HDR10 and back to SDR without a blank screen over 5 s.",
			Human: "TV mode switch both ways.", Minutes: 1,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "HDR10") && is4K(v) }) && firstAudio(ms, codec("eac3"))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-O04", Group: "Operations", Title: "Scrub with trickplay on a 4K remux",
			Dims: map[string]string{"ops": "scrub"},
			Risk: "Long scrubs on a remux restart ffmpeg at a far segment; trickplay thumbnails load.", Pass: "Thumbnails show while scrubbing; playback resumes within 3 s of release.",
			Human: "Scrub feel and thumbnails.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return is4K(v) && rangeIs(v, "SDR") }) && firstAudio(ms, codec("eac3"))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
	}
}
