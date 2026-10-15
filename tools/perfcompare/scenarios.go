// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// scenario is one request that a client makes. The same name on both
// servers means the same request with each server's own ids.
type scenario struct {
	Name   string
	Method string
	Path   string
	Body   []byte
	Image  bool // count bytes only; do not decode
	// Unmapped, when set, says why this server has no id for the scenario.
	// The scenario sends no request and fails with this reason.
	Unmapped string
}

// Field sets copied from jellyfin-web 10.11 and Swiftfin home and library
// screens.
const (
	cardFields   = "PrimaryImageAspectRatio,MediaSourceCount"
	cardImages   = "&ImageTypeLimit=1&EnableImageTypes=Primary,Backdrop,Banner,Thumb"
	browsePage   = 100
	latestLimit  = 16
	nextUpCutoff = 365 * 24 * time.Hour
)

// buildScenarios returns the fixed scenario set for one server. A scenario
// whose ids are missing on this server fails with the reason in t.Unmapped;
// with no reason it is left out, and the report shows it as missing on that
// side.
func buildScenarios(t target, now time.Time) []scenario {
	u := t.UserID
	var out []scenario
	get := func(name, p string) { out = append(out, scenario{Name: name, Method: http.MethodGet, Path: p}) }
	// unmapped adds a failing scenario when one of the entities has a
	// reason.
	unmapped := func(name string, entities ...string) {
		for _, e := range entities {
			if r := t.Unmapped[e]; r != "" {
				out = append(out, scenario{Name: name, Method: http.MethodGet, Unmapped: r})
				return
			}
		}
	}

	get("home/views", "/UserViews?userId="+u)
	get("home/resume", "/UserItems/Resume?userId="+u+"&Limit=12&Fields=PrimaryImageAspectRatio"+
		"&ImageTypeLimit=1&EnableImageTypes=Primary,Backdrop,Thumb&EnableTotalRecordCount=false&MediaTypes=Video")
	cutoff := now.Add(-nextUpCutoff).UTC().Truncate(24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	get("home/nextup", "/Shows/NextUp?userId="+u+"&Limit=24&Fields=PrimaryImageAspectRatio,DateCreated,Path,MediaSourceCount"+
		cardImages+"&EnableTotalRecordCount=false&DisableFirstEpisode=false&NextUpDateCutoff="+url.QueryEscape(cutoff)+
		"&EnableResumable=false&EnableRewatching=false")

	for _, lib := range t.Libraries {
		if lib.ID == "" {
			continue
		}
		key := slug(lib.Key)
		get("latest/"+key, fmt.Sprintf("/Items/Latest?userId=%s&Limit=%d&Fields=PrimaryImageAspectRatio,Path"+
			"&ImageTypeLimit=1&EnableImageTypes=Primary,Backdrop,Thumb&ParentId=%s", u, latestLimit, lib.ID))

		itemType := map[string]string{"movies": "Movie", "tvshows": "Series", "music": "MusicAlbum"}[lib.CollectionType]
		if itemType == "" {
			continue
		}
		browse := fmt.Sprintf("/Items?userId=%s&ParentId=%s&IncludeItemTypes=%s&Recursive=true&Fields=%s%s&Limit=%d",
			u, lib.ID, itemType, cardFields, cardImages, browsePage)
		get("browse/"+key+"/name", browse+"&SortBy=SortName&SortOrder=Ascending&StartIndex=0")
		get("browse/"+key+"/name-page3", browse+"&SortBy=SortName&SortOrder=Ascending&StartIndex="+fmt.Sprint(2*browsePage))
		get("browse/"+key+"/added", browse+"&SortBy=DateCreated,SortName&SortOrder=Descending,Ascending&StartIndex=0")
		if lib.CollectionType != "music" {
			get("browse/"+key+"/rating", browse+"&SortBy=CommunityRating,SortName&SortOrder=Descending,Ascending&StartIndex=0")
			get("browse/"+key+"/unplayed", browse+"&SortBy=SortName&SortOrder=Ascending&Filters=IsUnplayed&StartIndex=0")
		} else {
			get("browse/"+key+"/artist", browse+"&SortBy=AlbumArtist,SortName&SortOrder=Ascending&StartIndex=0")
		}
	}

	detail := func(name, entity string, it baseItem) {
		if it.ID != "" {
			get(name, "/Items/"+it.ID+"?userId="+u)
		} else {
			unmapped(name, entity)
		}
	}
	detail("detail/movie", "movie", t.Movie)
	detail("detail/series", "series", t.Series)
	detail("detail/episode", "episode", t.Episode)
	detail("detail/album", "album", t.Album)

	if t.Series.ID != "" {
		get("show/seasons", "/Shows/"+t.Series.ID+"/Seasons?userId="+u+"&Fields=ItemCounts,PrimaryImageAspectRatio,CanDelete,MediaSourceCount")
	} else {
		unmapped("show/seasons", "series")
	}
	if t.Series.ID != "" && t.Episode.SeasonID != "" {
		get("show/episodes", "/Shows/"+t.Series.ID+"/Episodes?userId="+u+"&seasonId="+t.Episode.SeasonID+
			"&Fields=ItemCounts,PrimaryImageAspectRatio,CanDelete,MediaSourceCount,Overview")
	} else {
		unmapped("show/episodes", "series", "episode")
	}
	if t.Album.ID != "" {
		get("music/album-tracks", "/Items?userId="+u+"&ParentId="+t.Album.ID+
			"&SortBy=ParentIndexNumber,IndexNumber,SortName&Fields=ItemCounts,PrimaryImageAspectRatio,CanDelete,MediaSourceCount")
	} else {
		unmapped("music/album-tracks", "album")
	}

	if t.SearchTerm != "" {
		term := url.QueryEscape(t.SearchTerm)
		get("search/hints", "/Search/Hints?userId="+u+"&searchTerm="+term+"&Limit=24")
		get("search/items-movie", "/Items?userId="+u+"&searchTerm="+term+"&IncludeItemTypes=Movie&Recursive=true&Limit=24"+
			"&Fields=PrimaryImageAspectRatio,CanDelete,MediaSourceCount&ImageTypeLimit=1&EnableTotalRecordCount=false")
		get("search/items-series", "/Items?userId="+u+"&searchTerm="+term+"&IncludeItemTypes=Series&Recursive=true&Limit=24"+
			"&Fields=PrimaryImageAspectRatio,CanDelete,MediaSourceCount&ImageTypeLimit=1&EnableTotalRecordCount=false")
	}

	if t.Movie.ID != "" {
		out = append(out, scenario{Name: "playback/info-direct", Method: http.MethodPost,
			Path: "/Items/" + t.Movie.ID + "/PlaybackInfo?userId=" + u, Body: playbackInfoBody(u, false)})
	} else {
		unmapped("playback/info-direct", "movie")
	}
	if t.TranscodeMovie.ID != "" {
		out = append(out, scenario{Name: "playback/info-transcode", Method: http.MethodPost,
			Path: "/Items/" + t.TranscodeMovie.ID + "/PlaybackInfo?userId=" + u, Body: playbackInfoBody(u, true)})
	} else {
		unmapped("playback/info-transcode", "transcode-movie")
	}
	if tag := t.Movie.ImageTags["Primary"]; t.Movie.ID != "" && tag != "" {
		out = append(out, scenario{Name: "image/primary-card", Method: http.MethodGet, Image: true,
			Path: "/Items/" + t.Movie.ID + "/Images/Primary?fillHeight=396&fillWidth=264&quality=96&tag=" + url.QueryEscape(tag)})
	} else {
		unmapped("image/primary-card", "movie", "image")
	}
	return out
}

// transcodeBitrate is the bitrate cap of the transcode profile. It is low
// enough that every library title needs a transcode.
const transcodeBitrate = 1_500_000

// playbackInfoBody returns a PlaybackInfo request. The direct profile direct
// plays any container and codec. The transcode profile direct plays nothing
// above the bitrate cap and asks for H.264/AAC HLS, as a constrained client
// on a slow link would.
func playbackInfoBody(userID string, transcode bool) []byte {
	profile := map[string]any{
		"Name":                             "perfcompare",
		"MaxStreamingBitrate":              120_000_000,
		"MaxStaticBitrate":                 120_000_000,
		"MusicStreamingTranscodingBitrate": 384_000,
		"DirectPlayProfiles":               []map[string]any{{"Type": "Video"}, {"Type": "Audio"}},
		"TranscodingProfiles": []map[string]any{{
			"Container": "ts", "Type": "Video", "VideoCodec": "h264", "AudioCodec": "aac",
			"Protocol": "hls", "Context": "Streaming", "MaxAudioChannels": "2",
			"MinSegments": 1, "BreakOnNonKeyFrames": true,
		}},
		"SubtitleProfiles": []map[string]any{{"Format": "vtt", "Method": "External"}},
	}
	req := map[string]any{
		"UserId": userID, "StartTimeTicks": 0, "IsPlayback": true, "AutoOpenLiveStream": false,
		"EnableDirectPlay": true, "EnableDirectStream": true, "EnableTranscoding": true,
		"AllowVideoStreamCopy": true, "AllowAudioStreamCopy": true,
		"MaxStreamingBitrate": 120_000_000, "DeviceProfile": profile,
	}
	if transcode {
		profile["MaxStreamingBitrate"] = transcodeBitrate
		profile["DirectPlayProfiles"] = []map[string]any{{"Container": "mp4", "Type": "Video", "VideoCodec": "h264", "AudioCodec": "aac"}}
		req["MaxStreamingBitrate"] = transcodeBitrate
		req["EnableDirectPlay"] = false
		req["EnableDirectStream"] = false
		req["AllowVideoStreamCopy"] = false
	}
	b, _ := json.Marshal(req)
	return b
}

// shape is the correctness signal of one response: the item count, the
// total record count (-1 when absent), and a short signature for responses
// that have no list.
type shape struct {
	Count int    `json:"count"`
	Total int    `json:"total"`
	Sig   string `json:"sig,omitempty"`
}

// shapeOf reads the correctness signal from a JSON body.
func shapeOf(body []byte) (shape, error) {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return shape{}, err
	}
	switch v := raw.(type) {
	case []any:
		return shape{Count: len(v), Total: -1}, nil
	case map[string]any:
		s := shape{Count: 1, Total: -1}
		if t, ok := v["TotalRecordCount"].(float64); ok {
			s.Total = int(t)
		}
		for _, key := range []string{"Items", "SearchHints"} {
			if list, ok := v[key].([]any); ok {
				s.Count = len(list)
				return s, nil
			}
		}
		if srcs, ok := v["MediaSources"].([]any); ok && v["PlaySessionId"] != nil {
			s.Count = len(srcs)
			s.Sig = playbackSig(srcs)
			return s, nil
		}
		if t, ok := v["Type"].(string); ok {
			s.Sig = t
		}
		return s, nil
	default:
		return shape{Count: 1, Total: -1}, nil
	}
}

// playbackSig names the method the first media source offers: direct play,
// direct stream, or transcode.
func playbackSig(srcs []any) string {
	if len(srcs) == 0 {
		return "none"
	}
	src, _ := srcs[0].(map[string]any)
	var parts []string
	if b, _ := src["SupportsDirectPlay"].(bool); b {
		parts = append(parts, "direct-play")
	}
	if b, _ := src["SupportsDirectStream"].(bool); b {
		parts = append(parts, "direct-stream")
	}
	if u, _ := src["TranscodingUrl"].(string); u != "" {
		parts = append(parts, "transcode")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "+")
}

// slug makes a library name safe for a scenario name.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
