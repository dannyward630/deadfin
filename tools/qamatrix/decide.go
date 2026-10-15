// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Wire types: the subset of BaseItemDto and PlaybackInfoResponse that the
// tool reads.

type item struct {
	ID                string        `json:"Id"`
	Name              string        `json:"Name"`
	Type              string        `json:"Type"`
	SeriesName        string        `json:"SeriesName"`
	IndexNumber       *int          `json:"IndexNumber"`
	ParentIndexNumber *int          `json:"ParentIndexNumber"`
	RunTimeTicks      int64         `json:"RunTimeTicks"`
	Path              string        `json:"Path"`
	MediaSources      []mediaSource `json:"MediaSources"`
}

type mediaSource struct {
	ID                   string            `json:"Id"`
	Container            string            `json:"Container"`
	Path                 string            `json:"Path"`
	ETag                 string            `json:"ETag"`
	Bitrate              int64             `json:"Bitrate"`
	RunTimeTicks         int64             `json:"RunTimeTicks"`
	SupportsDirectPlay   bool              `json:"SupportsDirectPlay"`
	SupportsDirectStream bool              `json:"SupportsDirectStream"`
	SupportsTranscoding  bool              `json:"SupportsTranscoding"`
	TranscodingURL       string            `json:"TranscodingUrl"`
	TranscodingProtocol  string            `json:"TranscodingSubProtocol"`
	TranscodingContainer string            `json:"TranscodingContainer"`
	MediaStreams         []mediaStream     `json:"MediaStreams"`
	MediaAttachments     []json.RawMessage `json:"MediaAttachments"`
}

type mediaStream struct {
	Type             string  `json:"Type"`
	Index            int     `json:"Index"`
	Codec            string  `json:"Codec"`
	CodecTag         string  `json:"CodecTag"`
	Profile          string  `json:"Profile"`
	BitDepth         int     `json:"BitDepth"`
	VideoRange       string  `json:"VideoRange"`
	VideoRangeType   string  `json:"VideoRangeType"`
	DvProfile        int     `json:"DvProfile"`
	RealFrameRate    float64 `json:"RealFrameRate"`
	AverageFrameRate float64 `json:"AverageFrameRate"`
	Width            int     `json:"Width"`
	Height           int     `json:"Height"`
	IsInterlaced     bool    `json:"IsInterlaced"`
	IsAnamorphic     bool    `json:"IsAnamorphic"`
	Channels         int     `json:"Channels"`
	Language         string  `json:"Language"`
	Title            string  `json:"Title"`
	IsDefault        bool    `json:"IsDefault"`
	IsForced         bool    `json:"IsForced"`
	IsExternal       bool    `json:"IsExternal"`
	DeliveryMethod   string  `json:"DeliveryMethod"`
	DeliveryURL      string  `json:"DeliveryUrl"`
	SampleRate       int     `json:"SampleRate"`
	Level            float64 `json:"Level"`
	DisplayTitle     string  `json:"DisplayTitle"`
}

type playbackInfoResponse struct {
	MediaSources  []mediaSource `json:"MediaSources"`
	PlaySessionID string        `json:"PlaySessionId"`
	ErrorCode     string        `json:"ErrorCode"`
}

// Decision is the server's PlaybackInfo outcome for one item, one player,
// and one stream selection, reduced to what a tester compares.
type Decision struct {
	// Method is DirectPlay, Remux (HLS with video copy) or Transcode
	// (HLS with video encode). DirectStream is a progressive remux.
	Method string `json:"method"`
	// Audio is copy or transcode for the selected audio track.
	Audio            string   `json:"audio,omitempty"`
	AudioCodec       string   `json:"audioCodec,omitempty"`
	VideoCodecs      string   `json:"videoCodecs,omitempty"`
	AudioCodecs      string   `json:"audioCodecs,omitempty"`
	Subtitle         string   `json:"subtitle,omitempty"`
	SubtitleURL      string   `json:"subtitleUrl,omitempty"`
	TranscodeReasons []string `json:"transcodeReasons,omitempty"`
	// StreamURL is the URL the client plays, with credentials removed.
	StreamURL     string `json:"streamUrl,omitempty"`
	PlaySessionID string `json:"playSessionId,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Expect is the expected decision for one player. Empty fields are not
// compared.
type Expect struct {
	Method   string `json:"method"`
	Audio    string `json:"audio,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
}

func (e Expect) String() string {
	parts := []string{e.Method}
	if e.Audio != "" {
		parts = append(parts, "audio "+e.Audio)
	}
	if e.Subtitle != "" {
		parts = append(parts, "subs "+e.Subtitle)
	}
	return strings.Join(parts, ", ")
}

func (d Decision) String() string {
	if d.Error != "" {
		return "error: " + d.Error
	}
	return Expect{Method: d.Method, Audio: d.Audio, Subtitle: d.Subtitle}.String()
}

// mismatch returns why d does not meet e, or "".
func (e Expect) mismatch(d Decision) string {
	if d.Error != "" {
		return d.Error
	}
	var out []string
	if e.Method != "" && e.Method != d.Method {
		out = append(out, fmt.Sprintf("method %s, want %s", d.Method, e.Method))
	}
	if e.Audio != "" && e.Audio != d.Audio {
		out = append(out, fmt.Sprintf("audio %s, want %s", d.Audio, e.Audio))
	}
	if e.Subtitle != "" && e.Subtitle != d.Subtitle {
		out = append(out, fmt.Sprintf("subtitle %s, want %s", d.Subtitle, e.Subtitle))
	}
	return strings.Join(out, "; ")
}

// remuxReasons are the transcode reasons that a video stream copy can
// satisfy: StreamBuilder's DirectStreamReasons less the audio reasons.
var remuxReasons = map[string]bool{
	"ContainerNotSupported":     true,
	"VideoCodecTagNotSupported": true,
}

var audioReasons = map[string]bool{
	"AudioCodecNotSupported":      true,
	"AudioIsExternal":             true,
	"SecondaryAudioNotSupported":  true,
	"AudioChannelsNotSupported":   true,
	"AudioProfileNotSupported":    true,
	"AudioSampleRateNotSupported": true,
	"AudioBitDepthNotSupported":   true,
	"AudioBitrateNotSupported":    true,
	"UnknownAudioStreamInfo":      true,
}

type selection struct {
	MediaSourceID string
	AudioIndex    int // -1: server default
	SubtitleIndex int // -1: off
}

// playbackInfo posts PlaybackInfo as the tvOS client does
// (MediaPlayerItem+Build.swift) and classifies the answer.
func playbackInfo(ctx context.Context, c *client, userID, player string, it item, sel selection) Decision {
	body := map[string]any{
		"DeviceProfile":       profileFor(player),
		"UserId":              userID,
		"MaxStreamingBitrate": lanBitrate,
		"AutoOpenLiveStream":  true,
		"MediaSourceId":       sel.MediaSourceID,
		"SubtitleStreamIndex": sel.SubtitleIndex,
	}
	if sel.AudioIndex >= 0 {
		body["AudioStreamIndex"] = sel.AudioIndex
	}
	ref := "/Items/" + it.ID + "/PlaybackInfo?userId=" + url.QueryEscape(userID)
	raw, _, err := c.do(ctx, http.MethodPost, ref, deviceID(player), body, nil)
	if err != nil {
		return Decision{Error: err.Error()}
	}
	var resp playbackInfoResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Decision{Error: "decode PlaybackInfo: " + err.Error()}
	}
	if resp.ErrorCode != "" {
		return Decision{Error: "ErrorCode " + resp.ErrorCode}
	}
	return classify(c, it, sel, resp)
}

func deviceID(player string) string { return "qamatrix-" + player }

// freshDecision posts PlaybackInfo again for a mapped item, so each run
// streams under its own PlaySessionId. Two runs that reuse the stored
// PlaySessionId are one play session with two clients: they share one
// playlist family, and each run's stop ends it under the other.
func freshDecision(ctx context.Context, c *client, userID, player string, it ItemOut) Decision {
	sel := selection{MediaSourceID: it.MediaSourceID, AudioIndex: it.AudioIndex, SubtitleIndex: it.SubtitleIndex}
	return playbackInfo(ctx, c, userID, player, item{ID: it.ID}, sel)
}

func classify(c *client, it item, sel selection, resp playbackInfoResponse) Decision {
	if len(resp.MediaSources) == 0 {
		return Decision{Error: "no MediaSources"}
	}
	ms := resp.MediaSources[0]
	for _, s := range resp.MediaSources {
		if s.ID == sel.MediaSourceID {
			ms = s
		}
	}
	d := Decision{PlaySessionID: resp.PlaySessionID}

	audio := pickAudio(ms, sel.AudioIndex)
	if audio != nil {
		d.AudioCodec = audio.Codec
	}
	if sel.SubtitleIndex >= 0 {
		for _, s := range ms.MediaStreams {
			if s.Type == "Subtitle" && s.Index == sel.SubtitleIndex {
				d.Subtitle = s.DeliveryMethod
				if s.DeliveryURL != "" {
					d.SubtitleURL = stripSecrets(s.DeliveryURL)
				}
			}
		}
	}

	switch {
	case ms.TranscodingURL != "":
		u, err := url.Parse(ms.TranscodingURL)
		if err != nil {
			d.Error = c.redact(err.Error())
			return d
		}
		q := u.Query()
		d.VideoCodecs = q.Get("VideoCodec")
		d.AudioCodecs = q.Get("AudioCodec")
		if r := q.Get("TranscodeReasons"); r != "" {
			d.TranscodeReasons = strings.Split(r, ",")
		}
		d.StreamURL = stripSecrets(ms.TranscodingURL)
		videoCopy, audioCopy := true, true
		for _, r := range d.TranscodeReasons {
			switch {
			case audioReasons[r]:
				audioCopy = false
			case r == "SubtitleCodecNotSupported":
				// Only a burn-in needs the video encoded; HLS WebVTT
				// rides beside a video copy.
				if q.Get("SubtitleMethod") == "Encode" {
					videoCopy = false
				}
			case !remuxReasons[r]:
				videoCopy = false
			}
		}
		// The server copies audio only when the source codec is one the
		// transcoding profile lists.
		if audio != nil && !containsFold(strings.Split(d.AudioCodecs, ","), audio.Codec) {
			audioCopy = false
		}
		switch {
		case !videoCopy:
			d.Method = "Transcode"
		case strings.EqualFold(ms.TranscodingProtocol, "hls"):
			d.Method = "Remux"
		default:
			d.Method = "DirectStream"
		}
		d.Audio = map[bool]string{true: "copy", false: "transcode"}[audioCopy]
	case ms.SupportsDirectPlay:
		d.Method = "DirectPlay"
		d.Audio = "copy"
		v := url.Values{}
		v.Set("static", "true")
		v.Set("mediaSourceId", ms.ID)
		v.Set("playSessionId", resp.PlaySessionID)
		if ms.ETag != "" {
			v.Set("tag", ms.ETag)
		}
		d.StreamURL = "/Videos/" + it.ID + "/stream?" + v.Encode()
	default:
		d.Error = "no direct play and no TranscodingUrl"
	}
	return d
}

func pickAudio(ms mediaSource, index int) *mediaStream {
	var first *mediaStream
	for i := range ms.MediaStreams {
		s := &ms.MediaStreams[i]
		if s.Type != "Audio" {
			continue
		}
		if index >= 0 && s.Index == index {
			return s
		}
		if first == nil {
			first = s
		}
		if index < 0 && s.IsDefault {
			return s
		}
	}
	return first
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), v) {
			return true
		}
	}
	return false
}
