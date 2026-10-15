// SPDX-License-Identifier: GPL-2.0-only

package listenbrainz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxResponseBytes = 1 << 20

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type ClientOptions struct {
	Doer      Doer
	Config    Config
	UserAgent string
}

// Client submits ListenBrainz wire documents. It owns no goroutines.
type Client struct {
	doer      Doer
	server    string
	token     string
	userAgent string
}

func NewClient(options ClientOptions) *Client {
	config := normalizeConfig(options.Config)
	return &Client{
		doer:      options.Doer,
		server:    config.Server,
		token:     config.Token,
		userAgent: strings.TrimSpace(options.UserAgent),
	}
}

func (client *Client) Enabled() bool {
	return client != nil && client.doer != nil && client.token != ""
}

type Submission struct {
	ListenType string   `json:"listen_type"`
	Payload    []Listen `json:"payload"`
}

type Listen struct {
	ListenedAt    *int64        `json:"listened_at,omitempty"`
	TrackMetadata TrackMetadata `json:"track_metadata"`
}

type TrackMetadata struct {
	ArtistName     string         `json:"artist_name"`
	TrackName      string         `json:"track_name"`
	ReleaseName    string         `json:"release_name,omitempty"`
	AdditionalInfo AdditionalInfo `json:"additional_info,omitempty"`
}

type AdditionalInfo struct {
	ArtistMBIDs             []string `json:"artist_mbids,omitempty"`
	ReleaseGroupMBID        string   `json:"release_group_mbid,omitempty"`
	ReleaseMBID             string   `json:"release_mbid,omitempty"`
	RecordingMBID           string   `json:"recording_mbid,omitempty"`
	TrackMBID               string   `json:"track_mbid,omitempty"`
	TrackNumber             string   `json:"tracknumber,omitempty"`
	MediaPlayer             string   `json:"media_player,omitempty"`
	SubmissionClient        string   `json:"submission_client,omitempty"`
	SubmissionClientVersion string   `json:"submission_client_version,omitempty"`
	DurationMS              int64    `json:"duration_ms,omitempty"`
	DurationPlayed          int64    `json:"duration_played,omitempty"`
}

func (client *Client) Submit(ctx context.Context, submission Submission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !client.Enabled() {
		return errors.New("listenbrainz: client is not configured")
	}
	if submission.ListenType != "single" && submission.ListenType != "playing_now" {
		return fmt.Errorf("listenbrainz: invalid listen type %q", submission.ListenType)
	}
	if len(submission.Payload) != 1 {
		return fmt.Errorf("listenbrainz: %s payload has %d listens, want 1", submission.ListenType, len(submission.Payload))
	}
	if submission.Payload[0].TrackMetadata.ArtistName == "" || submission.Payload[0].TrackMetadata.TrackName == "" {
		return errors.New("listenbrainz: artist_name and track_name are required")
	}
	body, err := json.Marshal(submission)
	if err != nil {
		return fmt.Errorf("listenbrainz: encode submission: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.server+"/1/submit-listens", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("listenbrainz: build request: %w", err)
	}
	request.Header.Set("Authorization", "Token "+client.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if client.userAgent != "" {
		request.Header.Set("User-Agent", client.userAgent)
	}
	response, err := client.doer.Do(request)
	if err != nil {
		return fmt.Errorf("listenbrainz: submit: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("listenbrainz: read response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(responseBody))
		if len(message) > 256 {
			message = message[:256]
		}
		if message == "" {
			return fmt.Errorf("listenbrainz: submit returned status %d", response.StatusCode)
		}
		return fmt.Errorf("listenbrainz: submit returned status %d: %s", response.StatusCode, message)
	}
	var acknowledgment struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(responseBody, &acknowledgment); err != nil {
		return fmt.Errorf("listenbrainz: decode submission response: %w", err)
	}
	if acknowledgment.Status != "ok" {
		return fmt.Errorf("listenbrainz: submission response status %q, want ok", acknowledgment.Status)
	}
	return nil
}
