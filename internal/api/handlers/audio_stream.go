// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// progressiveAudioStreamQuery binds the AudioController parameters that a
// progressive audio transcode uses (AudioController.cs:92-150).
type progressiveAudioStreamQuery struct {
	Container                   *string `query:"container"`
	Params                      *string `query:"params"`
	MediaSourceID               *string `query:"mediaSourceId"`
	DeviceID                    *string `query:"deviceId"`
	PlaySessionID               *string `query:"playSessionId"`
	LiveStreamID                *string `query:"liveStreamId"`
	AudioCodec                  *string `query:"audioCodec"`
	AudioStreamIndex            *int    `query:"audioStreamIndex"`
	StartTimeTicks              *int64  `query:"startTimeTicks"`
	AudioBitrate                *int    `query:"audioBitRate"`
	AudioSampleRate             *int    `query:"audioSampleRate"`
	MaxAudioChannels            *int    `query:"maxAudioChannels"`
	AudioChannels               *int    `query:"audioChannels"`
	TranscodingMaxAudioChannels *int    `query:"transcodingMaxAudioChannels"`
	MaxAudioBitDepth            *int    `query:"maxAudioBitDepth"`
	EnableAudioVbrEncoding      *bool   `query:"enableAudioVbrEncoding"`
}

// universalAudioTranscodeQuery binds the transcoding parameters of
// UniversalAudioController.GetUniversalAudioStream
// (UniversalAudioController.cs:72-90) that the direct branch does not use.
type universalAudioTranscodeQuery struct {
	DeviceID                 *string `query:"deviceId"`
	AudioCodec               *string `query:"audioCodec"`
	TranscodingAudioChannels *int    `query:"transcodingAudioChannels"`
	AudioBitrate             *int    `query:"audioBitRate"`
	StartTimeTicks           *int64  `query:"startTimeTicks"`
	TranscodingContainer     *string `query:"transcodingContainer"`
	TranscodingProtocol      *string `query:"transcodingProtocol"`
	EnableAudioVbrEncoding   *bool   `query:"enableAudioVbrEncoding"`
}

// progressiveAudio serves /Audio/{itemId}/stream without static: one
// progressive ffmpeg transcode (AudioHelper.GetAudioStream).
func (h directFileStreamHandler) progressiveAudio(w http.ResponseWriter, r *http.Request) {
	itemID, ok := directFileItemID(w, r)
	if !ok {
		return
	}
	if h.progressiveAudioStreamer == nil {
		http.Error(w, http.StatusText(http.StatusNotImplemented), http.StatusNotImplemented)
		return
	}
	var query progressiveAudioStreamQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		badRequest(w)
		return
	}
	if query.Params == nil {
		query.Params = packedParamsFromRawQuery(r.URL.RawQuery)
	}
	params := StreamParams{
		DeviceID:                    query.DeviceID,
		MediaSourceID:               query.MediaSourceID,
		AudioCodec:                  query.AudioCodec,
		AudioStreamIndex:            query.AudioStreamIndex,
		AudioBitrate:                query.AudioBitrate,
		MaxAudioChannels:            query.MaxAudioChannels,
		StartTimeTicks:              query.StartTimeTicks,
		PlaySessionID:               query.PlaySessionID,
		LiveStreamID:                query.LiveStreamID,
		TranscodingMaxAudioChannels: query.TranscodingMaxAudioChannels,
	}
	if query.Params != nil {
		var err error
		params, err = DecodeStreamParams(*query.Params, false, params)
		if err != nil {
			badRequest(w)
			return
		}
	}
	if !directContainerSyntax.MatchString(stringValue(params.AudioCodec)) {
		badRequest(w)
		return
	}
	if stringValue(params.LiveStreamID) != "" {
		http.Error(w, http.StatusText(http.StatusNotImplemented), http.StatusNotImplemented)
		return
	}
	container := chi.URLParam(r, "container")
	if container == "" {
		container = stringValue(query.Container)
	}
	if !directContainerSyntax.MatchString(container) {
		badRequest(w)
		return
	}
	h.writeProgressiveAudio(w, r, playback.ProgressiveAudioRequest{
		UserID:                      streamUserID(r),
		ItemID:                      itemID,
		MediaSourceID:               stringValue(params.MediaSourceID),
		Container:                   container,
		DeviceID:                    stringValue(params.DeviceID),
		PlaySessionID:               stringValue(params.PlaySessionID),
		UserAgent:                   r.UserAgent(),
		AudioCodec:                  stringValue(params.AudioCodec),
		AudioStreamIndex:            params.AudioStreamIndex,
		StartTimeTicks:              int64Value(params.StartTimeTicks),
		AudioBitrate:                params.AudioBitrate,
		AudioSampleRate:             query.AudioSampleRate,
		MaxAudioChannels:            params.MaxAudioChannels,
		AudioChannels:               query.AudioChannels,
		TranscodingMaxAudioChannels: params.TranscodingMaxAudioChannels,
		MaxAudioBitDepth:            query.MaxAudioBitDepth,
		StreamOptions:               audioStreamOptions(r),
		EnableAudioVbrEncoding:      boolDefault(query.EnableAudioVbrEncoding, true),
		Head:                        r.Method == http.MethodHead,
	})
}

// writeProgressiveAudio opens the transcode and copies its growing output
// (FileStreamResponseHelpers.GetTranscodedFile).
func (h directFileStreamHandler) writeProgressiveAudio(w http.ResponseWriter, r *http.Request, request playback.ProgressiveAudioRequest) {
	response, err := h.progressiveAudioStreamer.OpenProgressiveAudio(r.Context(), request)
	if err != nil {
		progressiveVideoError(w, err)
		return
	}
	if response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Content-Type", response.ContentType)
	if request.Head {
		w.WriteHeader(http.StatusOK)
		return
	}
	if response.Body == nil {
		internalError(w)
		return
	}
	_ = http.NewResponseController(w).Flush()
	_, _ = io.Copy(w, response.Body)
}

// audioStreamOptions ports StreamingHelpers.ParseStreamOptions: every query
// key that starts with a lowercase letter.
func audioStreamOptions(r *http.Request) playback.StreamOptions {
	var options playback.StreamOptions
	for _, option := range bind.LowercaseQueryOptions(r) {
		options.Set(option.Name, option.Value)
	}
	return options
}

// universalAudioTranscode runs the universal-audio negotiation and serves its
// result: the source file, a progressive transcode, or the HLS master
// playlist (UniversalAudioController.cs:113-266).
func (h directFileStreamHandler) universalAudioTranscode(w http.ResponseWriter, r *http.Request, userID, itemID dto.UUID, query universalAudioStreamQuery) {
	var transcode universalAudioTranscodeQuery
	if err := bind.QueryRequest(r, &transcode); err != nil {
		badRequest(w)
		return
	}
	audioCodec, transcodingContainer := stringValue(transcode.AudioCodec), stringValue(transcode.TranscodingContainer)
	if !directContainerSyntax.MatchString(audioCodec) || !directContainerSyntax.MatchString(transcodingContainer) {
		badRequest(w)
		return
	}
	request := playback.UniversalAudioRequest{
		Containers:               query.Container,
		TranscodingContainer:     transcodingContainer,
		AudioCodec:               audioCodec,
		TranscodingAudioChannels: transcode.TranscodingAudioChannels,
		MaxStreamingBitrate:      query.MaxStreamingBitrate,
		MaxAudioChannels:         query.MaxAudioChannels,
		MaxAudioSampleRate:       query.MaxAudioSampleRate,
		MaxAudioBitDepth:         query.MaxAudioBitDepth,
	}
	if transcode.TranscodingProtocol != nil {
		protocol, ok := universalAudioProtocol(*transcode.TranscodingProtocol)
		if !ok {
			badRequest(w)
			return
		}
		request.TranscodingProtocol = &protocol
	}
	decision, err := h.universal.UniversalAudio(r.Context(), playback.UniversalAudioQuery{
		UserID:        userID,
		ItemID:        itemID,
		MediaSourceID: stringValue(query.MediaSourceID),
		ClientAddress: middleware.ClientAddressFromContext(r.Context()),
		Request:       request,
	})
	if err != nil {
		h.sourceError(w, err)
		return
	}
	if decision.Static {
		h.serveSource(w, r, decision.Source, "", "")
		return
	}
	audioBitrate := transcode.AudioBitrate
	if audioBitrate == nil {
		audioBitrate = query.MaxStreamingBitrate
	}
	if decision.Protocol == playback.StreamProtocolHLS {
		h.universalAudioHLS(w, r, decision, request, transcode, audioBitrate)
		return
	}
	if h.progressiveAudioStreamer == nil {
		http.Error(w, http.StatusText(http.StatusNotImplemented), http.StatusNotImplemented)
		return
	}
	h.writeProgressiveAudio(w, r, playback.ProgressiveAudioRequest{
		UserID:                 userID,
		ItemID:                 itemID,
		MediaSourceID:          decision.Source.ID,
		Container:              decision.TranscodingContainer,
		DeviceID:               stringValue(transcode.DeviceID),
		PlaySessionID:          decision.PlaySessionID,
		UserAgent:              r.UserAgent(),
		AudioCodec:             audioCodec,
		StartTimeTicks:         int64Value(transcode.StartTimeTicks),
		AudioBitrate:           audioBitrate,
		AudioSampleRate:        query.MaxAudioSampleRate,
		MaxAudioChannels:       query.MaxAudioChannels,
		AudioChannels:          query.MaxAudioChannels,
		MaxAudioBitDepth:       query.MaxAudioBitDepth,
		EnableAudioVbrEncoding: boolDefault(transcode.EnableAudioVbrEncoding, true),
		Head:                   r.Method == http.MethodHead,
	})
}

// universalAudioHLS serves the HLS master playlist of the audio transcode
// (UniversalAudioController.cs:146-201, DynamicHlsHelper.GetMasterHlsPlaylist).
// It dispatches to the /Audio/{itemId}/master.m3u8 handler with the
// HlsAudioRequestDto values as the query. The playlist names main.m3u8
// relatively, which resolves to /Audio/{itemId}/main.m3u8.
func (h directFileStreamHandler) universalAudioHLS(w http.ResponseWriter, r *http.Request, decision playback.UniversalAudioDecision, request playback.UniversalAudioRequest, transcode universalAudioTranscodeQuery, audioBitrate *int) {
	if h.hls == nil {
		http.Error(w, http.StatusText(http.StatusNotImplemented), http.StatusNotImplemented)
		return
	}
	// HLS segments are mpegts or fmp4; another value falls back to ts.
	segmentContainer := "ts"
	if universalHLSContainer(request.TranscodingContainer) {
		segmentContainer = request.TranscodingContainer
	}
	if universalHLSContainer(decision.TranscodingContainer) {
		segmentContainer = decision.TranscodingContainer
	}
	audioCodec := request.AudioCodec
	if decision.TranscodeReasons == playback.ReasonContainerNotSupported {
		// Upstream requests "copy". The HLS service copies a source codec
		// that the request lists.
		if audio := decision.Source.DefaultAudioStream(nil); audio != nil {
			audioCodec = audio.Codec
		}
	}
	values := url.Values{}
	setQueryValue(values, "DeviceId", stringValue(transcode.DeviceID))
	setQueryValue(values, "MediaSourceId", decision.Source.ID)
	setQueryValue(values, "PlaySessionId", decision.PlaySessionID)
	setQueryValue(values, "SegmentContainer", segmentContainer)
	setQueryValue(values, "AudioCodec", audioCodec)
	values.Set("EnableAutoStreamCopy", "true")
	values.Set("AllowAudioStreamCopy", "true")
	values.Set("AllowVideoStreamCopy", "true")
	setQueryInt(values, "AudioSampleRate", request.MaxAudioSampleRate)
	setQueryInt(values, "MaxAudioChannels", request.MaxAudioChannels)
	setQueryInt(values, "MaxAudioBitDepth", request.MaxAudioBitDepth)
	setQueryInt(values, "AudioBitRate", audioBitrate)
	if transcode.StartTimeTicks != nil {
		values.Set("StartTimeTicks", strconv.FormatInt(*transcode.StartTimeTicks, 10))
	}
	values.Set("SubtitleMethod", "Hls")
	values.Set("RequireAvc", "false")
	values.Set("DeInterlace", "false")
	values.Set("RequireNonAnamorphic", "false")
	values.Set("EnableAudioVbrEncoding", strconv.FormatBool(boolDefault(transcode.EnableAudioVbrEncoding, true)))
	if decision.TranscodeReasons != 0 {
		values.Set("TranscodeReasons", decision.TranscodeReasonNames())
	}
	master := r.Clone(r.Context())
	master.URL.RawQuery = values.Encode()
	hlsStreamHandler{streamer: h.hls, logger: slog.Default()}.master(w, master)
}

func universalAudioProtocol(value string) (playback.MediaStreamProtocol, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "http":
		return playback.StreamProtocolHTTP, true
	case "hls":
		return playback.StreamProtocolHLS, true
	}
	return 0, false
}

func universalHLSContainer(container string) bool {
	return strings.EqualFold(container, "ts") || strings.EqualFold(container, "mp4")
}

func setQueryValue(values url.Values, name, value string) {
	if value != "" {
		values.Set(name, value)
	}
}

func setQueryInt(values url.Values, name string, value *int) {
	if value != nil {
		values.Set(name, strconv.Itoa(*value))
	}
}
