// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

const (
	hlsPlaylistMIME = "application/x-mpegURL"
	hlsTSMIME       = "video/mp2t"
	hlsFMP4MIME     = "video/mp4"
)

// RegisterHLSStreams registers Jellyfin's dynamic HLS routes alongside the
// legacy artifact endpoints that older clients still request.
func RegisterHLSStreams(rt *router.Router, streamer playback.HLSVideoStreamer, logger *slog.Logger) {
	h := hlsStreamHandler{streamer: streamer, logger: logger}

	for _, route := range []struct {
		method, pattern, operationID string
		handler                      http.HandlerFunc
		anonymous                    bool
	}{
		{http.MethodGet, "/Videos/{itemId}/live.m3u8", "SHADOW:GetLiveHlsStream", h.live, false},
		{http.MethodGet, "/Videos/{itemId}/master.m3u8", "SHADOW:GetMasterHlsVideoPlaylist", h.master, false},
		{http.MethodHead, "/Videos/{itemId}/master.m3u8", "SHADOW:HeadMasterHlsVideoPlaylist", h.master, false},
		{http.MethodGet, "/Audio/{itemId}/master.m3u8", "SHADOW:GetMasterHlsAudioPlaylist", h.master, false},
		{http.MethodHead, "/Audio/{itemId}/master.m3u8", "SHADOW:HeadMasterHlsAudioPlaylist", h.master, false},
		{http.MethodGet, "/Videos/{itemId}/main.m3u8", "SHADOW:GetVariantHlsVideoPlaylist", h.main, false},
		{http.MethodGet, "/Audio/{itemId}/main.m3u8", "SHADOW:GetVariantHlsAudioPlaylist", h.main, false},
		{http.MethodGet, "/Videos/{itemId}/hls1/{playlistId}/{segmentId}.{container}", "SHADOW:GetHlsVideoSegment", h.segment, false},
		{http.MethodGet, "/Audio/{itemId}/hls1/{playlistId}/{segmentId}.{container}", "SHADOW:GetHlsAudioSegment", h.segment, false},
		{http.MethodGet, "/Audio/{itemId}/hls/{segmentId}/stream.mp3", "SHADOW:GetHlsAudioSegmentLegacyMp3", h.legacyAudioMP3, true},
		{http.MethodGet, "/Audio/{itemId}/hls/{segmentId}/stream.aac", "SHADOW:GetHlsAudioSegmentLegacyAac", h.legacyAudioAAC, true},
		{http.MethodGet, "/Videos/{itemId}/hls/{playlistId}/stream.m3u8", "SHADOW:GetHlsPlaylistLegacy", h.legacyPlaylist, false},
		{http.MethodGet, "/Videos/{itemId}/hls/{playlistId}/{segmentId}.{segmentContainer}", "SHADOW:GetHlsVideoSegmentLegacy", h.legacySegment, true},
		{http.MethodDelete, "/Videos/ActiveEncodings", "SHADOW:StopEncodingProcess", h.stop, false},
	} {
		meta := router.Route{OperationID: route.operationID, Anonymous: route.anonymous}
		if !route.anonymous {
			meta.Policy = string(auth.PolicyDefaultAuthorization)
		}
		rt.Handle(route.method, route.pattern, meta, route.handler)
	}
}

type hlsStreamHandler struct {
	streamer playback.HLSVideoStreamer
	logger   *slog.Logger
}

type hlsStreamQuery struct {
	MediaSourceID                       *string  `query:"mediaSourceId"`
	DeviceID                            *string  `query:"deviceId"`
	PlaySessionID                       *string  `query:"playSessionId"`
	APIKey                              *string  `query:"api_key"`
	APIKeyLegacy                        *string  `query:"apiKey"`
	Params                              *string  `query:"params"`
	Static                              *bool    `query:"static"`
	VideoCodec                          *string  `query:"videoCodec"`
	AudioCodec                          *string  `query:"audioCodec"`
	VideoStreamIndex                    *int     `query:"videoStreamIndex"`
	AudioStreamIndex                    *int     `query:"audioStreamIndex"`
	SubtitleStreamIndex                 *int     `query:"subtitleStreamIndex"`
	SubtitleMethod                      *string  `query:"subtitleMethod"`
	StartTimeTicks                      *int64   `query:"startTimeTicks"`
	EnableAutoStreamCopy                *bool    `query:"enableAutoStreamCopy"`
	AllowVideoStreamCopy                *bool    `query:"allowVideoStreamCopy"`
	AllowAudioStreamCopy                *bool    `query:"allowAudioStreamCopy"`
	AlwaysBurnInSubtitleWhenTranscoding *bool    `query:"alwaysBurnInSubtitleWhenTranscoding"`
	RequireAvc                          *bool    `query:"requireAvc"`
	RequireNonAnamorphic                *bool    `query:"requireNonAnamorphic"`
	DeInterlace                         *bool    `query:"deInterlace"`
	Width                               *int     `query:"width"`
	Height                              *int     `query:"height"`
	MaxWidth                            *int     `query:"maxWidth"`
	MaxHeight                           *int     `query:"maxHeight"`
	Framerate                           *float32 `query:"framerate"`
	MaxFramerate                        *float32 `query:"maxFramerate"`
	VideoBitrate                        *int     `query:"videoBitRate"`
	AudioBitrate                        *int     `query:"audioBitRate"`
	Profile                             *string  `query:"profile"`
	Level                               *string  `query:"level"`
	MaxRefFrames                        *int     `query:"maxRefFrames"`
	MaxVideoBitDepth                    *int     `query:"maxVideoBitDepth"`
	Rotation                            *int     `query:"rotation"`
	MaxAudioChannels                    *int     `query:"maxAudioChannels"`
	AudioSampleRate                     *int     `query:"audioSampleRate"`
	MaxAudioBitDepth                    *int     `query:"maxAudioBitDepth"`
	AudioChannels                       *int     `query:"audioChannels"`
	TranscodingMaxAudioChannels         *int     `query:"transcodingMaxAudioChannels"`
	CopyTimestamps                      *bool    `query:"copyTimestamps"`
	H264Profile                         *string  `query:"h264-profile"`
	H264Level                           *string  `query:"h264-level"`
	H264MaxRefFrames                    *int     `query:"h264-maxrefframes"`
	H264VideoBitDepth                   *int     `query:"h264-videobitdepth"`
	H264Rotation                        *int     `query:"h264-rotation"`
	H264DeInterlace                     *bool    `query:"h264-deinterlace"`
	H264RangeType                       *string  `query:"h264-rangetype"`
	AC3AudioChannels                    *int     `query:"ac3-audiochannels"`
	AC3AudioBitDepth                    *int     `query:"ac3-audiobitdepth"`
	SegmentContainer                    *string  `query:"segmentContainer"`
	SegmentLength                       *int     `query:"segmentLength"`
	MinSegments                         *int     `query:"minSegments"`
	RuntimeTicks                        *int64   `query:"runtimeTicks"`
	ActualSegmentLengthTicks            *int64   `query:"actualSegmentLengthTicks"`
	Tag                                 *string  `query:"tag"`
	LiveStreamID                        *string  `query:"liveStreamId"`
	EnableSubtitlesInManifest           *bool    `query:"enableSubtitlesInManifest"`
	SubtitleCodec                       *string  `query:"subtitleCodec"`
	TranscodeReasons                    *string  `query:"transcodeReasons"`
	EnableTrickplay                     *bool    `query:"enableTrickplay"`
}

type hlsStopQuery struct {
	DeviceID      *string `query:"deviceId"`
	PlaySessionID *string `query:"playSessionId"`
}

func (h hlsStreamHandler) master(w http.ResponseWriter, r *http.Request) {
	request, ok := h.request(w, r, hlsRouteVideo(r), true)
	if !ok {
		return
	}
	request.Head = r.Method == http.MethodHead
	if hlsRouteVideo(r) {
		enableTrickplay, bound := hlsMasterTrickplay(w, r)
		if !bound {
			return
		}
		request.EnableTrickplay = enableTrickplay
	}
	h.dispatch(w, r, request, hlsPlaylistMIME, true, h.streamer.Master)
}

func (h hlsStreamHandler) main(w http.ResponseWriter, r *http.Request) {
	request, ok := h.request(w, r, hlsRouteVideo(r), false)
	if !ok {
		return
	}
	request.EndpointPrefix = "hls1/main/"
	h.dispatch(w, r, request, hlsPlaylistMIME, false, h.streamer.Main)
}

func (h hlsStreamHandler) live(w http.ResponseWriter, r *http.Request) {
	request, ok := h.request(w, r, true, false)
	if !ok {
		return
	}
	request.Live = true
	enableSubtitles, bound := hlsLiveSubtitleManifest(w, r)
	if !bound {
		return
	}
	request.EnableSubtitlesInManifest = enableSubtitles
	h.dispatch(w, r, request, hlsPlaylistMIME, false, h.streamer.Live)
}

func (h hlsStreamHandler) segment(w http.ResponseWriter, r *http.Request) {
	request, ok := h.request(w, r, hlsRouteVideo(r), false)
	if !ok {
		return
	}
	routeContainer := strings.ToLower(chi.URLParam(r, "container"))
	if !validHLSHandlerToken(chi.URLParam(r, "playlistId"), false) || routeContainer != "ts" && routeContainer != "fmp4" && routeContainer != "mp4" {
		badRequest(w)
		return
	}
	container := routeContainer
	if container == "mp4" {
		container = "fmp4"
	}
	request.SegmentContainer = container
	request.ProgressiveRequest.Container = container
	index, err := strconv.Atoi(chi.URLParam(r, "segmentId"))
	if err != nil || !validHLSHandlerInt(index) || index < -1 || index == -1 && routeContainer != "mp4" {
		badRequest(w)
		return
	}
	request.SegmentIndex = index
	if !h.segmentTimingPresent(w, r) {
		return
	}
	request.ActualSegmentLengthTicksSet = hlsLifecycleSegmentTimingSafe(request.RuntimeTicks, request.ActualSegmentLengthTicks)
	h.dispatch(w, r, request, hlsSegmentMIME(request.SegmentContainer), false, h.streamer.Segment)
}

func (h hlsStreamHandler) legacyAudioMP3(w http.ResponseWriter, r *http.Request) {
	h.legacyAudio(w, r, "mp3", "audio/mpeg")
}

func (h hlsStreamHandler) legacyAudioAAC(w http.ResponseWriter, r *http.Request) {
	h.legacyAudio(w, r, "aac", "audio/aac")
}

func (h hlsStreamHandler) legacyAudio(w http.ResponseWriter, r *http.Request, container, contentType string) {
	if !h.legacyItemID(w, r) || !validHLSHandlerToken(chi.URLParam(r, "segmentId"), false) {
		badRequest(w)
		return
	}
	response, err := h.streamer.LegacySegment(r.Context(), "", chi.URLParam(r, "segmentId"), container)
	if err != nil {
		hlsLegacyError(w, err)
		return
	}
	h.writeResponse(w, r, response, contentType, false)
}

func (h hlsStreamHandler) legacyPlaylist(w http.ResponseWriter, r *http.Request) {
	playlistID := chi.URLParam(r, "playlistId")
	if !h.legacyItemID(w, r) || !validHLSHandlerToken(playlistID, false) {
		badRequest(w)
		return
	}
	response, err := h.streamer.LegacyPlaylist(r.Context(), playlistID)
	if err != nil {
		hlsLegacyError(w, err)
		return
	}
	h.writeResponse(w, r, response, hlsPlaylistMIME, false)
}

func (h hlsStreamHandler) legacySegment(w http.ResponseWriter, r *http.Request) {
	playlistID, segmentID := chi.URLParam(r, "playlistId"), chi.URLParam(r, "segmentId")
	container := strings.ToLower(chi.URLParam(r, "segmentContainer"))
	if !h.legacyItemID(w, r) || !validHLSHandlerToken(playlistID, true) || !validHLSHandlerToken(segmentID, false) || !validHLSLegacyHandlerContainer(container) {
		badRequest(w)
		return
	}
	response, err := h.streamer.LegacySegment(r.Context(), playlistID, segmentID, container)
	if err != nil {
		hlsLegacyError(w, err)
		return
	}
	h.writeResponse(w, r, response, hlsLegacyMIME(container), false)
}

func (h hlsStreamHandler) stop(w http.ResponseWriter, r *http.Request) {
	var query hlsStopQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		badRequest(w)
		return
	}
	deviceID, playSessionID := stringValue(query.DeviceID), stringValue(query.PlaySessionID)
	if deviceID == "" || playSessionID == "" {
		badRequest(w)
		return
	}
	if err := h.streamer.Stop(r.Context(), deviceID, playSessionID); err != nil {
		progressiveVideoError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h hlsStreamHandler) request(w http.ResponseWriter, r *http.Request, video, requireMediaSource bool) (playback.HLSRequest, bool) {
	itemID, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil {
		badRequest(w)
		return playback.HLSRequest{}, false
	}
	identity, authenticated := middleware.IdentityFromContext(r.Context())
	if authenticated && identity.Authenticated {
		r.URL.RawQuery = hlsCanonicalAuthenticatedQuery(r.URL.RawQuery, identity.Token)
	}
	var query hlsStreamQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		badRequest(w)
		return playback.HLSRequest{}, false
	}
	if query.Params == nil {
		query.Params = packedParamsFromRawQuery(r.URL.RawQuery)
	}
	if requireMediaSource && query.MediaSourceID == nil {
		badRequest(w)
		return playback.HLSRequest{}, false
	}
	if !validHLSQueryIntegers(query) {
		badRequest(w)
		return playback.HLSRequest{}, false
	}
	if query.Level != nil && !progressiveLevelSyntax.MatchString(*query.Level) {
		badRequest(w)
		return playback.HLSRequest{}, false
	}
	if !validCodecName(stringValue(query.VideoCodec)) || !validCodecName(stringValue(query.AudioCodec)) {
		badRequest(w)
		return playback.HLSRequest{}, false
	}
	var subtitleMethod *playback.SubtitleDeliveryMethod
	if query.SubtitleMethod == nil {
		method := playback.SubtitleExternal
		subtitleMethod = &method
	} else {
		var subtitleOK bool
		subtitleMethod, subtitleOK = progressiveSubtitleMethod(w, query.SubtitleMethod)
		if !subtitleOK {
			return playback.HLSRequest{}, false
		}
	}
	params := hlsExplicitStreamParams(query, subtitleMethod)
	videoIndex := query.VideoStreamIndex
	if query.Params != nil {
		params, err = DecodeStreamParams(*query.Params, video, params)
		if err != nil {
			badRequest(w)
			return playback.HLSRequest{}, false
		}
		videoParams, decodeErr := DecodeStreamParams(*query.Params, video, StreamParams{AudioStreamIndex: query.VideoStreamIndex})
		if decodeErr != nil {
			badRequest(w)
			return playback.HLSRequest{}, false
		}
		videoIndex = videoParams.AudioStreamIndex
	}
	container := strings.ToLower(strings.TrimSpace(stringValue(query.SegmentContainer)))
	if container == "mp4" {
		container = "fmp4"
	}
	if container != "" && container != "ts" && container != "fmp4" {
		badRequest(w)
		return playback.HLSRequest{}, false
	}
	if query.SegmentLength != nil && *query.SegmentLength < 0 || query.MinSegments != nil && *query.MinSegments < 0 || !validProgressiveIndices(videoIndex, params) {
		badRequest(w)
		return playback.HLSRequest{}, false
	}

	apiKey := stringValue(query.APIKey)
	if apiKey == "" {
		apiKey = stringValue(query.APIKeyLegacy)
	}
	userID := dto.NilUUID
	if authenticated && identity.Authenticated {
		userID = identity.UserID
		apiKey = identity.Token
	}
	videoCodec, audioCodec := stringValue(params.VideoCodec), stringValue(params.AudioCodec)
	return playback.HLSRequest{
		ProgressiveRequest: playback.ProgressiveRequest{
			UserID:                      userID,
			ItemID:                      itemID,
			MediaSourceID:               stringValue(params.MediaSourceID),
			Container:                   container,
			DeviceID:                    stringValue(params.DeviceID),
			PlaySessionID:               stringValue(params.PlaySessionID),
			UserAgent:                   r.UserAgent(),
			VideoCodecs:                 progressiveCodecList(videoCodec),
			AudioCodecs:                 progressiveCodecList(audioCodec),
			VideoCodec:                  videoCodec,
			AudioCodec:                  audioCodec,
			VideoStreamIndex:            videoIndex,
			AudioStreamIndex:            params.AudioStreamIndex,
			SubtitleStreamIndex:         params.SubtitleStreamIndex,
			SubtitleDeliveryMethod:      params.SubtitleMethod,
			StartTimeTicks:              int64Value(params.StartTimeTicks),
			AllowVideoStreamCopy:        boolDefault(query.AllowVideoStreamCopy, true),
			AllowAudioStreamCopy:        boolDefault(query.AllowAudioStreamCopy, true),
			EnableAutoStreamCopy:        boolDefault(query.EnableAutoStreamCopy, true),
			RequireAvc:                  boolValue(params.RequireAVC),
			RequireNonAnamorphic:        boolValue(params.RequireNonAnamorphic),
			DeInterlace:                 boolValue(params.Deinterlace),
			Width:                       query.Width,
			Height:                      query.Height,
			MaxWidth:                    params.MaxWidth,
			MaxHeight:                   params.MaxHeight,
			MaxFramerate:                params.MaxFramerate,
			Framerate:                   query.Framerate,
			VideoBitrate:                params.VideoBitrate,
			Profile:                     params.Profile,
			Level:                       params.Level,
			MaxRefFrames:                params.MaxRefFrames,
			MaxVideoBitDepth:            params.MaxVideoBitDepth,
			Rotation:                    query.Rotation,
			AudioBitrate:                params.AudioBitrate,
			MaxAudioChannels:            params.MaxAudioChannels,
			AudioChannels:               query.AudioChannels,
			TranscodingMaxAudioChannels: params.TranscodingMaxAudioChannels,
			AudioSampleRate:             query.AudioSampleRate,
			MaxAudioBitDepth:            query.MaxAudioBitDepth,
			StreamOptions:               hlsStreamOptions(r, query),
			CopyTimestamps:              boolValue(params.CopyTimestamps),
			Head:                        r.Method == http.MethodHead,
		},
		SegmentContainer:                    container,
		SegmentLengthSeconds:                intValue(query.SegmentLength),
		SegmentLengthSet:                    query.SegmentLength != nil,
		MinSegments:                         hlsMinSegments(query.MinSegments, query.SegmentLength),
		MinSegmentsSet:                      query.MinSegments != nil,
		RuntimeTicks:                        int64Value(query.RuntimeTicks),
		ActualSegmentLengthTicks:            int64Value(query.ActualSegmentLengthTicks),
		ActualSegmentLengthTicksSet:         query.ActualSegmentLengthTicks != nil,
		RawQuery:                            r.URL.RawQuery,
		APIKey:                              apiKey,
		Tag:                                 stringValue(params.Tag),
		LiveStreamID:                        stringValue(params.LiveStreamID),
		EnableSubtitlesInManifest:           boolValue(params.EnableSubtitlesInManifest),
		AlwaysBurnInSubtitleWhenTranscoding: boolValue(query.AlwaysBurnInSubtitleWhenTranscoding),
		SubtitleCodec:                       stringValue(params.SubtitleCodec),
		TranscodeReasons:                    stringValue(params.TranscodeReasons),
		Static:                              boolValue(params.Static),
		AudioOnly:                           !video,
		EnableTrickplay:                     boolValue(query.EnableTrickplay),
	}, true
}

// hlsCanonicalAuthenticatedQuery retains every non-authentication query part
// byte-for-byte, removes all decoded API-key aliases, and appends the resolved
// credential in the casing used by stock generated HLS URIs.
func hlsCanonicalAuthenticatedQuery(rawQuery, token string) string {
	var query strings.Builder
	query.Grow(len(rawQuery) + len(token) + len("ApiKey="))

	kept := 0
	if rawQuery != "" {
		for remaining := rawQuery; ; {
			part, rest, more := strings.Cut(remaining, "&")
			if !hlsAPIKeyQueryPart(part) {
				if kept > 0 {
					query.WriteByte('&')
				}
				query.WriteString(part)
				kept++
			}
			if !more {
				break
			}
			remaining = rest
		}
	}
	if kept > 0 {
		query.WriteByte('&')
	}
	query.WriteString("ApiKey=")
	query.WriteString(url.QueryEscape(token))
	return query.String()
}

func hlsAPIKeyQueryPart(part string) bool {
	rawName, _, _ := strings.Cut(part, "=")
	name, err := url.QueryUnescape(rawName)
	return err == nil && (strings.EqualFold(name, "ApiKey") || strings.EqualFold(name, "api_key"))
}

func hlsExplicitStreamParams(query hlsStreamQuery, subtitleMethod *playback.SubtitleDeliveryMethod) StreamParams {
	return StreamParams{
		DeviceID:                    query.DeviceID,
		MediaSourceID:               query.MediaSourceID,
		Static:                      query.Static,
		VideoCodec:                  query.VideoCodec,
		AudioCodec:                  query.AudioCodec,
		AudioStreamIndex:            query.AudioStreamIndex,
		SubtitleStreamIndex:         query.SubtitleStreamIndex,
		VideoBitrate:                query.VideoBitrate,
		AudioBitrate:                query.AudioBitrate,
		MaxAudioChannels:            query.MaxAudioChannels,
		MaxFramerate:                firstFloat32(query.MaxFramerate, query.Framerate),
		MaxWidth:                    query.MaxWidth,
		MaxHeight:                   query.MaxHeight,
		StartTimeTicks:              query.StartTimeTicks,
		Level:                       query.Level,
		MaxRefFrames:                query.MaxRefFrames,
		MaxVideoBitDepth:            query.MaxVideoBitDepth,
		Profile:                     query.Profile,
		PlaySessionID:               query.PlaySessionID,
		LiveStreamID:                query.LiveStreamID,
		CopyTimestamps:              query.CopyTimestamps,
		SubtitleMethod:              subtitleMethod,
		TranscodingMaxAudioChannels: query.TranscodingMaxAudioChannels,
		EnableSubtitlesInManifest:   query.EnableSubtitlesInManifest,
		Tag:                         query.Tag,
		RequireAVC:                  query.RequireAvc,
		SubtitleCodec:               query.SubtitleCodec,
		RequireNonAnamorphic:        query.RequireNonAnamorphic,
		Deinterlace:                 query.DeInterlace,
		TranscodeReasons:            query.TranscodeReasons,
	}
}

func validProgressiveIndices(videoIndex *int, params StreamParams) bool {
	return int64Value(params.StartTimeTicks) >= 0 && validHLSHandlerIndex(videoIndex) && validHLSHandlerIndex(params.AudioStreamIndex) && validHLSHandlerIndex(params.SubtitleStreamIndex)
}

func validHLSHandlerIndex(value *int) bool { return value == nil || *value >= -1 }

const (
	hlsHandlerMinInt32 = -1 << 31
	hlsHandlerMaxInt32 = 1<<31 - 1
)

func validHLSHandlerInt(value int) bool {
	return value >= hlsHandlerMinInt32 && value <= hlsHandlerMaxInt32
}

func validHLSQueryIntegers(query hlsStreamQuery) bool {
	for _, value := range []*int{
		query.VideoStreamIndex, query.AudioStreamIndex, query.SubtitleStreamIndex,
		query.Width, query.Height, query.MaxWidth, query.MaxHeight,
		query.VideoBitrate, query.AudioBitrate,
		query.MaxRefFrames, query.MaxVideoBitDepth, query.Rotation,
		query.MaxAudioChannels, query.AudioSampleRate, query.MaxAudioBitDepth,
		query.AudioChannels, query.TranscodingMaxAudioChannels,
		query.H264MaxRefFrames, query.H264VideoBitDepth, query.H264Rotation,
		query.AC3AudioChannels, query.AC3AudioBitDepth,
		query.SegmentLength, query.MinSegments,
	} {
		if value != nil && !validHLSHandlerInt(*value) {
			return false
		}
	}
	return true
}

const hlsTickDuration = 100 * time.Nanosecond

func hlsLifecycleSegmentTimingSafe(runtimeTicks, actualSegmentLengthTicks int64) bool {
	if actualSegmentLengthTicks <= 0 || runtimeTicks < 0 || runtimeTicks > math.MaxInt64-actualSegmentLengthTicks {
		return false
	}
	endTicks := runtimeTicks + actualSegmentLengthTicks
	return endTicks >= 0 && endTicks <= math.MaxInt64/int64(hlsTickDuration)
}

func (h hlsStreamHandler) segmentTimingPresent(w http.ResponseWriter, r *http.Request) bool {
	var query struct {
		RuntimeTicks             *int64 `query:"runtimeTicks"`
		ActualSegmentLengthTicks *int64 `query:"actualSegmentLengthTicks"`
	}
	if err := bind.QueryRequest(r, &query); err != nil || query.RuntimeTicks == nil || query.ActualSegmentLengthTicks == nil {
		badRequest(w)
		return false
	}
	return true
}

func hlsMasterTrickplay(w http.ResponseWriter, r *http.Request) (bool, bool) {
	var query struct {
		EnableTrickplay *bool `query:"enableTrickplay"`
	}
	if err := bind.QueryRequest(r, &query); err != nil {
		badRequest(w)
		return false, false
	}
	return boolDefault(query.EnableTrickplay, true), true
}

func hlsLiveSubtitleManifest(w http.ResponseWriter, r *http.Request) (bool, bool) {
	var query struct {
		Params                    *string `query:"params"`
		EnableSubtitlesInManifest *bool   `query:"enableSubtitlesInManifest"`
	}
	if err := bind.QueryRequest(r, &query); err != nil {
		badRequest(w)
		return false, false
	}
	if query.Params == nil {
		query.Params = packedParamsFromRawQuery(r.URL.RawQuery)
	}
	params := StreamParams{EnableSubtitlesInManifest: query.EnableSubtitlesInManifest}
	if query.Params != nil {
		var err error
		params, err = DecodeStreamParams(*query.Params, true, params)
		if err != nil {
			badRequest(w)
			return false, false
		}
	}
	return boolDefault(params.EnableSubtitlesInManifest, true), true
}

func (h hlsStreamHandler) dispatch(w http.ResponseWriter, r *http.Request, request playback.HLSRequest, contentType string, expires bool, call func(context.Context, playback.HLSRequest) (playback.HLSResponse, error)) {
	response, err := call(r.Context(), request)
	if errors.Is(err, sourcefile.ErrStaleSource) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	if errors.Is(err, playback.ErrProgressiveUnsupported) {
		h.logger.Error("HLS service cannot fulfill request",
			"error", err.Error(),
			"item_id", request.ItemID.Dashed(),
			"video_stream_index", streamIndexValue(request.VideoStreamIndex),
			"audio_stream_index", streamIndexValue(request.AudioStreamIndex),
			"subtitle_stream_index", streamIndexValue(request.SubtitleStreamIndex),
		)
	}
	if err != nil {
		if hlsInternalFailure(err) {
			h.logInternalFailure(r, request, err)
		}
		progressiveVideoError(w, err)
		return
	}
	if expires {
		w.Header().Set("Expires", "0")
	}
	h.writeResponse(w, r, response, contentType, request.Head)
}

// hlsInternalFailure reports whether progressiveVideoError answers err with
// HTTP 500.
func hlsInternalFailure(err error) bool {
	return !errors.Is(err, playback.ErrMediaSourceNotFound) &&
		!errors.Is(err, playback.ErrItemNotFound) &&
		!errors.Is(err, playback.ErrProgressiveInvalid) &&
		!errors.Is(err, playback.ErrProgressiveUnsupported)
}

// logInternalFailure records every HLS 500 at ERROR level, so that a failed
// init, segment or playlist request is visible in the pod log. When the client
// has already gone, the failure is its cancellation and is not an error.
func (h hlsStreamHandler) logInternalFailure(r *http.Request, request playback.HLSRequest, err error) {
	level := slog.LevelError
	if r.Context().Err() != nil {
		level = slog.LevelDebug
	}
	h.logger.Log(r.Context(), level, "HLS request failed",
		"error", err.Error(),
		"path", r.URL.Path,
		"item_id", request.ItemID.Dashed(),
		"play_session_id", request.PlaySessionID,
		"device_id", request.DeviceID,
		"segment_index", request.SegmentIndex,
	)
}

// streamIndexValue renders a requested stream index as an integer, or nil when
// the request omits the index.
func streamIndexValue(index *int) any {
	if index == nil {
		return nil
	}
	return *index
}

func (h hlsStreamHandler) writeResponse(w http.ResponseWriter, r *http.Request, response playback.HLSResponse, fallbackContentType string, head bool) {
	if response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	contentType := response.ContentType
	if contentType == "" {
		contentType = fallbackContentType
	}
	w.Header().Set("Content-Type", contentType)
	if head {
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

func hlsLegacyError(w http.ResponseWriter, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	progressiveVideoError(w, err)
}

func (h hlsStreamHandler) legacyItemID(w http.ResponseWriter, r *http.Request) bool {
	return chi.URLParam(r, "itemId") != ""
}

func hlsRouteVideo(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/Videos/") }

func validHLSHandlerToken(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func validHLSLegacyHandlerContainer(container string) bool {
	switch container {
	case "ts", "mp4", "fmp4", "mp3", "aac":
		return true
	default:
		return false
	}
}

func hlsSegmentMIME(container string) string {
	if container == "fmp4" {
		return hlsFMP4MIME
	}
	return hlsTSMIME
}

func hlsLegacyMIME(container string) string {
	switch container {
	case "ts":
		return hlsTSMIME
	case "mp4", "fmp4":
		return hlsFMP4MIME
	case "mp3":
		return "audio/mpeg"
	default:
		return "audio/aac"
	}
}

func hlsStreamOptions(r *http.Request, query hlsStreamQuery) playback.StreamOptions {
	var options playback.StreamOptions
	for _, option := range bind.LowercaseQueryOptions(r) {
		options.Set(option.Name, option.Value)
	}

	setProgressiveStringOption(&options, "h264-profile", query.H264Profile)
	setProgressiveStringOption(&options, "h264-level", query.H264Level)
	setProgressiveIntOption(&options, "h264-maxrefframes", query.H264MaxRefFrames)
	setProgressiveIntOption(&options, "h264-videobitdepth", query.H264VideoBitDepth)
	setProgressiveIntOption(&options, "h264-rotation", query.H264Rotation)
	setProgressiveBoolOption(&options, "h264-deinterlace", query.H264DeInterlace)
	setProgressiveStringOption(&options, "h264-rangetype", query.H264RangeType)
	setProgressiveIntOption(&options, "ac3-audiochannels", query.AC3AudioChannels)
	setProgressiveIntOption(&options, "ac3-audiobitdepth", query.AC3AudioBitDepth)
	return options
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func hlsMinSegments(value, segmentLength *int) int {
	if value != nil {
		return *value
	}
	if segmentLength != nil && *segmentLength >= 10 {
		return 2
	}
	return 3
}
