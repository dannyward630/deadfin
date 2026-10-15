// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// TestTranscoderSupport_VideoRangeCapability exercises the
// playbackTranscoderSupport seam through the public ResolvePlaybackInfo API.
// Without an encoder seam no tone mapper is known (D-32), so a source whose
// VideoRangeType is not SDR must not advertise transcoding.
func TestTranscoderSupport_VideoRangeCapability(t *testing.T) {
	// A transcoding profile the builder accepts when direct play fails.
	transcodeProfile := &playback.DeviceProfile{
		ID: "transcode-only",
		TranscodingProfiles: []playback.TranscodingProfile{{
			Type: playback.DlnaVideo, Container: "ts", VideoCodec: "h264", AudioCodec: "aac",
			Protocol: playback.StreamProtocolHTTP, Context: playback.ContextStreaming,
		}},
	}

	for _, tc := range []struct {
		name           string
		videoRangeType playback.VideoRangeType
		wantTranscode  bool
	}{
		{name: "SDR transcodes", videoRangeType: playback.VideoRangeSDR, wantTranscode: true},
		{name: "HDR10 declined", videoRangeType: playback.VideoRangeHDR10, wantTranscode: false},
		{name: "HLG declined", videoRangeType: playback.VideoRangeHLG, wantTranscode: false},
		{name: "DOVI declined", videoRangeType: playback.VideoRangeDOVI, wantTranscode: false},
		{name: "DOVIWithHDR10 declined", videoRangeType: playback.VideoRangeDOVIWithHDR10, wantTranscode: false},
		{name: "DOVIWithEL declined", videoRangeType: playback.VideoRangeDOVIWithEL, wantTranscode: false},
		{name: "HDR10Plus declined", videoRangeType: playback.VideoRangeHDR10Plus, wantTranscode: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := videoRangeSource("range-source", tc.videoRangeType)
			request := playbackInfoRequest(playback.WithDeviceProfile(transcodeProfile))
			got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{source})
			if err != nil {
				t.Fatalf("ResolvePlaybackInfo() error = %v", err)
			}
			if len(got.MediaSources) != 1 {
				t.Fatalf("MediaSources = %d, want 1 resolved source", len(got.MediaSources))
			}
			resolved := got.MediaSources[0]
			if resolved.SupportsTranscoding != tc.wantTranscode {
				t.Errorf("SupportsTranscoding = %t, want %t", resolved.SupportsTranscoding, tc.wantTranscode)
			}
			if tc.wantTranscode {
				if resolved.TranscodingURL == "" {
					t.Error("TranscodingURL is empty, want a non-empty transcoding URL for a transcodable source")
				}
			} else {
				if resolved.TranscodingURL != "" {
					t.Errorf("TranscodingURL = %q, want empty for a non-SDR source the encoder cannot transcode", resolved.TranscodingURL)
				}
			}
		})
	}
}

// TestVideoRange_NonSDRPreservesDirectPlay confirms that a non-SDR source
// under a profile that CAN direct play it still direct-plays, and the
// video-range gate does not interfere with that path.
func TestVideoRange_NonSDRPreservesDirectPlay(t *testing.T) {
	directAndTranscodeProfile := &playback.DeviceProfile{
		ID: "direct-and-transcode",
		DirectPlayProfiles: []playback.DirectPlayProfile{{
			Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "hevc", AudioCodec: "aac",
		}},
		TranscodingProfiles: []playback.TranscodingProfile{{
			Type: playback.DlnaVideo, Container: "ts", VideoCodec: "h264", AudioCodec: "aac",
			Protocol: playback.StreamProtocolHTTP, Context: playback.ContextStreaming,
		}},
	}
	for _, tc := range []struct {
		name           string
		videoRangeType playback.VideoRangeType
	}{
		{name: "SDR", videoRangeType: playback.VideoRangeSDR},
		{name: "HDR10", videoRangeType: playback.VideoRangeHDR10},
		{name: "DOVIWithHDR10", videoRangeType: playback.VideoRangeDOVIWithHDR10},
		{name: "DOVIWithEL", videoRangeType: playback.VideoRangeDOVIWithEL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := videoRangeSource("direct-play-source", tc.videoRangeType)
			source.Container = "mp4"
			source.MediaStreams[0].Codec = "hevc"
			request := playbackInfoRequest(playback.WithDeviceProfile(directAndTranscodeProfile))
			got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{source})
			if err != nil {
				t.Fatalf("ResolvePlaybackInfo() error = %v", err)
			}
			if len(got.MediaSources) != 1 {
				t.Fatalf("MediaSources = %d, want 1 resolved source", len(got.MediaSources))
			}
			resolved := got.MediaSources[0]
			if !resolved.SupportsDirectPlay {
				t.Error("SupportsDirectPlay = false, want true: direct play must remain for a direct-playable source")
			}
			if resolved.TranscodingURL != "" {
				t.Errorf("TranscodingURL = %q, want empty for a direct-played source", resolved.TranscodingURL)
			}
		})
	}
}

// TestVideoRange_NonSDRPreservesDirectStream confirms that a non-SDR source
// forced to direct stream keeps its delivery URL. The video-range gate sets
// SupportsTranscoding false, but the direct-stream URL must remain because
// the selected method is direct stream, not transcode.
func TestVideoRange_NonSDRPreservesDirectStream(t *testing.T) {
	directStreamProfile := &playback.DeviceProfile{
		ID: "direct-stream-only",
		TranscodingProfiles: []playback.TranscodingProfile{{
			Type: playback.DlnaVideo, Container: "ts", VideoCodec: "h264", AudioCodec: "aac",
			Protocol: playback.StreamProtocolHTTP, Context: playback.ContextStreaming,
		}},
	}
	for _, tc := range []struct {
		name           string
		videoRangeType playback.VideoRangeType
	}{
		{name: "HDR10", videoRangeType: playback.VideoRangeHDR10},
		{name: "DOVIWithHDR10", videoRangeType: playback.VideoRangeDOVIWithHDR10},
		{name: "DOVIWithEL", videoRangeType: playback.VideoRangeDOVIWithEL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := videoRangeSource("direct-stream-source", tc.videoRangeType)
			request := playbackInfoRequest(
				playback.WithDeviceProfile(directStreamProfile),
				playback.WithForceDirectStream(true),
			)
			got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{source})
			if err != nil {
				t.Fatalf("ResolvePlaybackInfo() error = %v", err)
			}
			if len(got.MediaSources) != 1 {
				t.Fatalf("MediaSources = %d, want 1 resolved source", len(got.MediaSources))
			}
			resolved := got.MediaSources[0]
			if !resolved.SupportsDirectStream {
				t.Error("SupportsDirectStream = false, want true: direct stream must remain for a forced non-SDR source")
			}
			if resolved.SupportsTranscoding {
				t.Error("SupportsTranscoding = true, want false for a non-SDR source the encoder cannot transcode")
			}
			if resolved.TranscodingURL == "" {
				t.Error("TranscodingURL is empty, want a non-empty direct-stream delivery URL preserved for a direct-streamed source")
			}
		})
	}
}

// TestPlaybackInfo_VideoRangeTranscodingGate checks the three acceptance
// scenarios as a focused table test: non-SDR without direct play declines
// transcoding, SDR without direct play still transcodes, and non-SDR with
// direct play is unaffected.
func TestPlaybackInfo_VideoRangeTranscodingGate(t *testing.T) {
	transcodeProfile := &playback.DeviceProfile{
		ID: "gate-profile",
		TranscodingProfiles: []playback.TranscodingProfile{{
			Type: playback.DlnaVideo, Container: "ts", VideoCodec: "h264", AudioCodec: "aac",
			Protocol: playback.StreamProtocolHTTP, Context: playback.ContextStreaming,
		}},
	}
	directProfile := &playback.DeviceProfile{
		ID: "gate-direct",
		DirectPlayProfiles: []playback.DirectPlayProfile{{
			Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac",
		}},
		TranscodingProfiles: []playback.TranscodingProfile{{
			Type: playback.DlnaVideo, Container: "ts", VideoCodec: "h264", AudioCodec: "aac",
			Protocol: playback.StreamProtocolHTTP, Context: playback.ContextStreaming,
		}},
	}

	t.Run("non-SDR without direct play declines transcoding", func(t *testing.T) {
		source := videoRangeSource("hdr-no-direct", playback.VideoRangeHDR10)
		request := playbackInfoRequest(playback.WithDeviceProfile(transcodeProfile))
		got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{source})
		if err != nil {
			t.Fatalf("ResolvePlaybackInfo() error = %v", err)
		}
		if len(got.MediaSources) != 1 {
			t.Fatalf("MediaSources = %d, want 1", len(got.MediaSources))
		}
		resolved := got.MediaSources[0]
		if resolved.SupportsTranscoding {
			t.Error("SupportsTranscoding = true, want false for a non-SDR source the encoder cannot transcode")
		}
		if resolved.TranscodingURL != "" {
			t.Errorf("TranscodingURL = %q, want empty", resolved.TranscodingURL)
		}
	})

	t.Run("SDR without direct play still transcodes", func(t *testing.T) {
		source := videoRangeSource("sdr-no-direct", playback.VideoRangeSDR)
		request := playbackInfoRequest(playback.WithDeviceProfile(transcodeProfile))
		got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{source})
		if err != nil {
			t.Fatalf("ResolvePlaybackInfo() error = %v", err)
		}
		if len(got.MediaSources) != 1 {
			t.Fatalf("MediaSources = %d, want 1", len(got.MediaSources))
		}
		resolved := got.MediaSources[0]
		if !resolved.SupportsTranscoding {
			t.Error("SupportsTranscoding = false, want true for an SDR source")
		}
		if resolved.TranscodingURL == "" {
			t.Error("TranscodingURL is empty, want a non-empty URL for a transcodable SDR source")
		}
	})

	t.Run("non-SDR with direct play is unaffected", func(t *testing.T) {
		source := videoRangeSource("hdr-direct-play", playback.VideoRangeHDR10)
		source.Container = "mp4"
		source.MediaStreams[0].Codec = "h264"
		request := playbackInfoRequest(playback.WithDeviceProfile(directProfile))
		got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{source})
		if err != nil {
			t.Fatalf("ResolvePlaybackInfo() error = %v", err)
		}
		if len(got.MediaSources) != 1 {
			t.Fatalf("MediaSources = %d, want 1", len(got.MediaSources))
		}
		resolved := got.MediaSources[0]
		if !resolved.SupportsDirectPlay {
			t.Error("SupportsDirectPlay = false, want true: direct play must remain for a direct-playable non-SDR source")
		}
		if resolved.TranscodingURL != "" {
			t.Errorf("TranscodingURL = %q, want empty for a direct-played source", resolved.TranscodingURL)
		}
	})
}

// videoRangeSource builds a MediaSourceInfo whose video stream has the given
// VideoRangeType. The source uses a container and codec that no stock direct
// play profile matches (mkv/hevc), forcing the builder to consider transcoding.
func videoRangeSource(id string, rangeType playback.VideoRangeType) playback.MediaSourceInfo {
	video := playback.MediaStream{
		Index: 0, Type: playback.StreamTypeVideo, Codec: "hevc",
	}
	switch rangeType {
	case playback.VideoRangeHDR10:
		transfer := "smpte2084"
		video.ColorTransfer = &transfer
	case playback.VideoRangeHLG:
		transfer := "arib-std-b67"
		video.ColorTransfer = &transfer
	case playback.VideoRangeDOVI:
		profile, level := 5, 0
		rpu, bl, compat := 1, 1, 0
		video.DvProfile = &profile
		video.DvLevel = &level
		video.RpuPresentFlag = &rpu
		video.BlPresentFlag = &bl
		video.DvBlSignalCompatibilityID = &compat
	case playback.VideoRangeDOVIWithHDR10:
		profile, level := 8, 0
		rpu, bl, compat := 1, 1, 1
		video.DvProfile = &profile
		video.DvLevel = &level
		video.RpuPresentFlag = &rpu
		video.BlPresentFlag = &bl
		video.DvBlSignalCompatibilityID = &compat
	case playback.VideoRangeDOVIWithEL:
		profile, level := 7, 0
		rpu, bl, compat := 1, 1, 0
		video.DvProfile = &profile
		video.DvLevel = &level
		video.RpuPresentFlag = &rpu
		video.BlPresentFlag = &bl
		video.DvBlSignalCompatibilityID = &compat
	case playback.VideoRangeHDR10Plus:
		transfer := "smpte2084"
		plus := true
		video.ColorTransfer = &transfer
		video.Hdr10PlusPresentFlag = &plus
	case playback.VideoRangeSDR:
		// SDR is the default; no color or DoVi fields are set.
	}
	return playback.MediaSourceInfo{
		ID:                   id,
		Protocol:             playback.MediaProtocolFile,
		Container:            "mkv",
		SupportsDirectPlay:   true,
		SupportsDirectStream: true,
		SupportsTranscoding:  true,
		MediaStreams: []playback.MediaStream{
			video,
			{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", IsDefault: true},
		},
	}
}

// TestTranscoderSupport_ToneMappedRangesTranscode covers the encoder seam
// once a tone mapper exists: software tonemapx, or Intel QSV tone mapping
// from encoding.xml. A 10-bit non-SDR source then advertises
// transcoding as pinned stock does; an 8-bit one stays declined.
func TestTranscoderSupport_ToneMappedRangesTranscode(t *testing.T) {
	transcodeProfile := &playback.DeviceProfile{
		ID: "transcode-only",
		TranscodingProfiles: []playback.TranscodingProfile{{
			Type: playback.DlnaVideo, Container: "ts", VideoCodec: "h264", AudioCodec: "aac",
			Protocol: playback.StreamProtocolHTTP, Context: playback.ContextStreaming,
		}},
	}
	software := playback.TranscodeEncoding{Capabilities: encoding.Capabilities{SupportsTonemapX: true}}
	qsv := playback.TranscodeEncoding{
		Capabilities: encoding.Capabilities{
			HWAccels: []string{"opencl", "qsv", "vaapi"},
			Filters:  []string{"overlay_opencl", "scale_opencl", "tonemap_opencl"},
		},
		Options: func() encoding.HardwareOptions {
			return encoding.HardwareOptions{Type: encoding.HardwareAccelerationQSV, EnableTonemapping: true, EnableVppTonemapping: true}
		},
	}
	qsvDisabled := qsv
	qsvDisabled.Options = func() encoding.HardwareOptions {
		return encoding.HardwareOptions{Type: encoding.HardwareAccelerationQSV}
	}

	for _, tc := range []struct {
		name          string
		encoder       playback.TranscodeEncoding
		rangeType     playback.VideoRangeType
		bitDepth      int
		wantTranscode bool
	}{
		{"software HDR10", software, playback.VideoRangeHDR10, 10, true},
		{"software HLG", software, playback.VideoRangeHLG, 10, true},
		{"software DOVIWithHDR10", software, playback.VideoRangeDOVIWithHDR10, 10, true},
		{"software 8-bit HDR10 declined", software, playback.VideoRangeHDR10, 8, false},
		{"QSV DOVI profile 5", qsv, playback.VideoRangeDOVI, 10, true},
		{"QSV DOVIWithEL", qsv, playback.VideoRangeDOVIWithEL, 10, true},
		{"QSV tone mapping disabled declined", qsvDisabled, playback.VideoRangeHDR10, 10, false},
		{"SDR always transcodes", qsvDisabled, playback.VideoRangeSDR, 8, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := videoRangeSource("range-source", tc.rangeType)
			bitDepth := tc.bitDepth
			source.MediaStreams[0].BitDepth = &bitDepth
			request := playbackInfoRequest(playback.WithDeviceProfile(transcodeProfile))
			request.CanTranscodeVideo = tc.encoder.CanTranscodeVideo
			got, err := playback.ResolvePlaybackInfo(request, []playback.MediaSourceInfo{source})
			if err != nil {
				t.Fatalf("ResolvePlaybackInfo() error = %v", err)
			}
			if len(got.MediaSources) != 1 {
				t.Fatalf("MediaSources = %d, want 1", len(got.MediaSources))
			}
			resolved := got.MediaSources[0]
			if resolved.SupportsTranscoding != tc.wantTranscode {
				t.Fatalf("SupportsTranscoding = %t, want %t", resolved.SupportsTranscoding, tc.wantTranscode)
			}
			if (resolved.TranscodingURL != "") != tc.wantTranscode {
				t.Fatalf("TranscodingURL = %q, want present=%t", resolved.TranscodingURL, tc.wantTranscode)
			}
		})
	}
}
