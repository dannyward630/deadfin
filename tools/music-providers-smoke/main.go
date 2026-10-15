// SPDX-License-Identifier: GPL-2.0-only

// Command music-providers-smoke performs opt-in live checks of the P8 music
// providers. Tests and CI do not run this command.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers/audiodb"
	"github.com/deadfin-org/deadfin/internal/providers/listenbrainz"
	"github.com/deadfin-org/deadfin/internal/providers/musicbrainz"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
)

const userAgent = "Deadfin-P8-live-smoke/dev (+https://github.com/deadfin-org/deadfin)"

type options struct {
	mode               string
	configDir          string
	artist             string
	album              string
	track              string
	language           string
	skipAudioDb        bool
	listenType         string
	confirmSubmit      bool
	artistMBID         string
	releaseGroupMBID   string
	releaseMBID        string
	recordingMBID      string
	trackMBID          string
	musicBrainzServer  string
	audioDbServer      string
	listenBrainzServer string
}

func main() {
	var opts options
	flag.StringVar(&opts.mode, "mode", "metadata", "smoke mode: metadata or scrobble")
	flag.StringVar(&opts.configDir, "config-dir", "", "optional Deadfin config/plugins directory")
	flag.StringVar(&opts.artist, "artist", "", "artist name")
	flag.StringVar(&opts.album, "album", "", "album/release name (metadata) or release name (scrobble)")
	flag.StringVar(&opts.track, "track", "", "track name for scrobble mode")
	flag.StringVar(&opts.language, "language", "en", "preferred AudioDb metadata language")
	flag.BoolVar(&opts.skipAudioDb, "skip-audiodb", false, "smoke MusicBrainz without requiring AUDIODB_API_KEY")
	flag.StringVar(&opts.listenType, "listen-type", "playing_now", "ListenBrainz type: playing_now or single")
	flag.BoolVar(&opts.confirmSubmit, "confirm-submit", false, "required acknowledgment that scrobble mode changes live ListenBrainz state")
	flag.StringVar(&opts.artistMBID, "artist-mbid", "", "optional MusicBrainz artist UUID")
	flag.StringVar(&opts.releaseGroupMBID, "release-group-mbid", "", "optional MusicBrainz release-group UUID")
	flag.StringVar(&opts.releaseMBID, "release-mbid", "", "optional MusicBrainz release UUID")
	flag.StringVar(&opts.recordingMBID, "recording-mbid", "", "optional MusicBrainz recording UUID")
	flag.StringVar(&opts.trackMBID, "track-mbid", "", "optional MusicBrainz track UUID")
	flag.StringVar(&opts.musicBrainzServer, "musicbrainz-server", "", "override MusicBrainz server")
	flag.StringVar(&opts.audioDbServer, "audiodb-server", "", "override AudioDb server")
	flag.StringVar(&opts.listenBrainzServer, "listenbrainz-server", "", "override ListenBrainz server")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var err error
	switch strings.ToLower(strings.TrimSpace(opts.mode)) {
	case "metadata":
		err = runMetadata(ctx, opts)
	case "scrobble":
		err = runScrobble(ctx, opts)
	default:
		err = fmt.Errorf("unsupported -mode %q (want metadata or scrobble)", opts.mode)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "music-providers-smoke:", err)
		os.Exit(1)
	}
}

func runMetadata(ctx context.Context, opts options) error {
	opts.artist = strings.TrimSpace(opts.artist)
	opts.album = strings.TrimSpace(opts.album)
	if opts.artist == "" {
		return errors.New("-artist is required in metadata mode")
	}
	musicConfig := musicbrainz.LoadConfig(configPath(opts.configDir, "Jellyfin.Plugin.MusicBrainz.xml"))
	if server := strings.TrimSpace(opts.musicBrainzServer); server != "" {
		musicConfig.Server = server
	}
	musicHTTP := remote.NewClient(remote.Config{
		UserAgent:      userAgent,
		MinInterval:    time.Duration(musicConfig.RateLimit * float64(time.Second)),
		Burst:          1,
		CacheEntries:   16,
		RequestTimeout: 30 * time.Second,
	})
	music := musicbrainz.NewProvider(musicbrainz.Options{HTTP: musicHTTP, Config: musicConfig})

	audioConfig := audiodb.LoadConfig(configPath(opts.configDir, "Jellyfin.Plugin.AudioDb.xml"))
	if server := strings.TrimSpace(opts.audioDbServer); server != "" {
		audioConfig.Server = server
	}
	if !opts.skipAudioDb && audioConfig.APIKey == "" {
		return errors.New("AudioDb smoke requires AUDIODB_API_KEY, <ApiKey> provider config, or -skip-audiodb")
	}
	audio := audiodb.NewProvider(audiodb.Options{
		HTTP: remote.NewClient(remote.Config{
			Clock:          clock.NewReal(),
			UserAgent:      userAgent,
			MinInterval:    audiodb.DefaultRequestInterval,
			Burst:          1,
			CacheEntries:   16,
			CacheFallback:  48 * time.Hour,
			RequestTimeout: 30 * time.Second,
		}),
		Config: audioConfig,
	})
	refresh := library.RefreshContext{Options: library.LibraryOptions{
		EnableInternetProviders:   true,
		PreferredMetadataLanguage: strings.TrimSpace(opts.language),
	}}

	artist := item.Item{Kind: item.KindMusicArtist, Name: stringPointer(opts.artist)}
	handled, err := music.Refresh(ctx, refresh, &artist)
	if err != nil {
		return fmt.Errorf("MusicBrainz artist: %w", err)
	}
	if !handled {
		return fmt.Errorf("MusicBrainz returned no artist match for %q", opts.artist)
	}
	if !opts.skipAudioDb {
		if _, err := audio.Refresh(ctx, refresh, &artist); err != nil {
			return fmt.Errorf("AudioDb artist: %w", err)
		}
	}

	result := metadataResult{Artist: summarizeItem(artist)}
	if opts.album != "" {
		album := item.Item{
			Kind: item.KindMusicAlbum,
			Name: stringPointer(opts.album),
			Values: item.ItemValues{
				AlbumArtists: []string{opts.artist},
			},
		}
		if artistID, ok := artist.ProviderIDs.Get("MusicBrainzArtist"); ok {
			album.ProviderIDs.Set("MusicBrainzAlbumArtist", artistID)
		}
		handled, err := music.Refresh(ctx, refresh, &album)
		if err != nil {
			return fmt.Errorf("MusicBrainz album: %w", err)
		}
		if !handled {
			return fmt.Errorf("MusicBrainz returned no album match for %q by %q", opts.album, opts.artist)
		}
		if !opts.skipAudioDb {
			if _, err := audio.Refresh(ctx, refresh, &album); err != nil {
				return fmt.Errorf("AudioDb album: %w", err)
			}
		}
		summary := summarizeItem(album)
		result.Album = &summary
	}
	return writeJSON(result)
}

func runScrobble(ctx context.Context, opts options) error {
	if !opts.confirmSubmit {
		return errors.New("scrobble mode changes live ListenBrainz state; rerun with -confirm-submit")
	}
	opts.artist = strings.TrimSpace(opts.artist)
	opts.track = strings.TrimSpace(opts.track)
	if opts.artist == "" || opts.track == "" {
		return errors.New("-artist and -track are required in scrobble mode")
	}
	listenType := strings.ToLower(strings.TrimSpace(opts.listenType))
	if listenType != "playing_now" && listenType != "single" {
		return fmt.Errorf("invalid -listen-type %q", opts.listenType)
	}
	config := listenbrainz.LoadConfig(configPath(opts.configDir, "Jellyfin.Plugin.ListenBrainz.xml"))
	if server := strings.TrimSpace(opts.listenBrainzServer); server != "" {
		config.Server = server
	}
	if config.Token == "" {
		return errors.New("ListenBrainz smoke requires LISTENBRAINZ_TOKEN or <Token> provider config")
	}
	client := listenbrainz.NewClient(listenbrainz.ClientOptions{
		Doer:      &http.Client{Timeout: 30 * time.Second},
		Config:    config,
		UserAgent: userAgent,
	})
	additional := listenbrainz.AdditionalInfo{
		ReleaseGroupMBID:        strings.TrimSpace(opts.releaseGroupMBID),
		ReleaseMBID:             strings.TrimSpace(opts.releaseMBID),
		RecordingMBID:           strings.TrimSpace(opts.recordingMBID),
		TrackMBID:               strings.TrimSpace(opts.trackMBID),
		MediaPlayer:             "Deadfin P8 live smoke",
		SubmissionClient:        "Deadfin",
		SubmissionClientVersion: "dev",
	}
	if id := strings.TrimSpace(opts.artistMBID); id != "" {
		additional.ArtistMBIDs = []string{id}
	}
	listen := listenbrainz.Listen{TrackMetadata: listenbrainz.TrackMetadata{
		ArtistName:     opts.artist,
		TrackName:      opts.track,
		ReleaseName:    strings.TrimSpace(opts.album),
		AdditionalInfo: additional,
	}}
	if listenType == "single" {
		listenedAt := clock.NewReal().Now().UTC().Unix()
		listen.ListenedAt = &listenedAt
	}
	if err := client.Submit(ctx, listenbrainz.Submission{ListenType: listenType, Payload: []listenbrainz.Listen{listen}}); err != nil {
		return err
	}
	return writeJSON(map[string]any{
		"listenType": listenType,
		"status":     "submitted",
		"server":     config.Server,
	})
}

type metadataResult struct {
	Artist itemSummary  `json:"artist"`
	Album  *itemSummary `json:"album,omitempty"`
}

type itemSummary struct {
	Name           string            `json:"name,omitempty"`
	Album          string            `json:"album,omitempty"`
	Overview       string            `json:"overview,omitempty"`
	ProductionYear *int              `json:"productionYear,omitempty"`
	Genres         []string          `json:"genres,omitempty"`
	AlbumArtists   []string          `json:"albumArtists,omitempty"`
	ProviderIDs    map[string]string `json:"providerIds"`
	Images         []imageSummary    `json:"images,omitempty"`
}

type imageSummary struct {
	Type  string `json:"type"`
	Index int    `json:"index,omitempty"`
	URL   string `json:"url"`
}

func summarizeItem(value item.Item) itemSummary {
	providerIDs := make(map[string]string)
	for _, id := range value.ProviderIDs.Snapshot() {
		providerIDs[id.Name] = id.Value
	}
	images := make([]imageSummary, 0, len(value.ImageInfos))
	for _, image := range value.ImageInfos {
		images = append(images, imageSummary{Type: image.Type.String(), Index: image.Index, URL: image.Path})
	}
	return itemSummary{
		Name:           pointerString(value.Name),
		Album:          pointerString(value.Album),
		Overview:       pointerString(value.Overview),
		ProductionYear: value.ProductionYear,
		Genres:         value.Values.Genres,
		AlbumArtists:   value.Values.AlbumArtists,
		ProviderIDs:    providerIDs,
		Images:         images,
	}
}

func configPath(directory, name string) string {
	if strings.TrimSpace(directory) == "" {
		return ""
	}
	return filepath.Join(directory, name)
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringPointer(value string) *string { return &value }
