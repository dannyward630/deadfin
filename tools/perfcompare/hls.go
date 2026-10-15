// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// hlsResult is one transcode start: PlaybackInfo, the master and variant
// playlists, and the first segment. Segment is time to first byte, which is
// the wait a player sees before the first frame.
type hlsResult struct {
	Info, Master, Variant, Segment time.Duration
	SegmentBytes                   int64
	Stopped                        bool
}

// runHLS starts one transcode of itemID on s, times it, and stops the job.
func runHLS(ctx context.Context, s *server, userID, itemID string) (hlsResult, error) {
	var res hlsResult
	info, err := s.do(ctx, http.MethodPost, "/Items/"+itemID+"/PlaybackInfo?userId="+userID, playbackInfoBody(userID, true), true)
	if err != nil {
		return res, fmt.Errorf("playback info: %w", err)
	}
	if info.Status != http.StatusOK {
		return res, fmt.Errorf("playback info: status %d", info.Status)
	}
	res.Info = info.Total
	var pb struct {
		MediaSources []struct {
			TranscodingURL string `json:"TranscodingUrl"`
		} `json:"MediaSources"`
	}
	if err := json.Unmarshal(info.Body, &pb); err != nil {
		return res, fmt.Errorf("playback info: decode: %w", err)
	}
	if len(pb.MediaSources) == 0 || pb.MediaSources[0].TranscodingURL == "" {
		return res, errors.New("playback info: no TranscodingUrl")
	}
	start := s.resolve(pb.MediaSources[0].TranscodingURL)

	// Stop the job whatever happens next. A fresh context makes sure the
	// stop runs after a timeout or cancel.
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		res.Stopped = stopTranscode(stopCtx, s, start) == nil
	}()

	master, err := s.do(ctx, http.MethodGet, start, nil, true)
	if err != nil {
		return res, fmt.Errorf("master playlist: %w", err)
	}
	if master.Status != http.StatusOK {
		return res, fmt.Errorf("master playlist: status %d", master.Status)
	}
	res.Master = master.TTFB

	media, mediaURL := master, start
	if bytes.Contains(master.Body, []byte("#EXT-X-STREAM-INF")) {
		variant, ok := firstURI(master.Body)
		if !ok {
			return res, errors.New("master playlist: no variant")
		}
		mediaURL, err = resolveRef(start, variant)
		if err != nil {
			return res, err
		}
		media, err = s.do(ctx, http.MethodGet, mediaURL, nil, true)
		if err != nil {
			return res, fmt.Errorf("variant playlist: %w", err)
		}
		if media.Status != http.StatusOK {
			return res, fmt.Errorf("variant playlist: status %d", media.Status)
		}
		res.Variant = media.TTFB
	}
	seg, ok := firstURI(media.Body)
	if !ok {
		return res, errors.New("variant playlist: no segment")
	}
	segURL, err := resolveRef(mediaURL, seg)
	if err != nil {
		return res, err
	}
	segment, err := s.do(ctx, http.MethodGet, segURL, nil, false)
	if err != nil {
		return res, fmt.Errorf("first segment: %w", err)
	}
	if segment.Status != http.StatusOK {
		return res, fmt.Errorf("first segment: status %d", segment.Status)
	}
	res.Segment = segment.TTFB
	res.SegmentBytes = segment.Bytes
	return res, nil
}

// stopTranscode ends the job that the transcoding URL started, with the
// DeviceId and PlaySessionId the URL carries.
func stopTranscode(ctx context.Context, s *server, transcodingURL string) error {
	u, err := url.Parse(transcodingURL)
	if err != nil {
		return err
	}
	q := u.Query()
	device, session := queryFold(q, "DeviceId"), queryFold(q, "PlaySessionId")
	if session == "" {
		return errors.New("no PlaySessionId in TranscodingUrl")
	}
	stop := "/Videos/ActiveEncodings?deviceId=" + url.QueryEscape(device) + "&playSessionId=" + url.QueryEscape(session)
	resp, err := s.do(ctx, http.MethodDelete, stop, nil, false)
	if err != nil {
		return err
	}
	if resp.Status/100 != 2 {
		return fmt.Errorf("stop: status %d", resp.Status)
	}
	return nil
}

// queryFold reads a query value with a case-insensitive key.
func queryFold(q url.Values, key string) string {
	for k, v := range q {
		if strings.EqualFold(k, key) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// firstURI returns the first URI line of an M3U8 playlist.
func firstURI(playlist []byte) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(playlist))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			return line, true
		}
	}
	return "", false
}

// resolveRef resolves a playlist URI against the playlist URL.
func resolveRef(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}
