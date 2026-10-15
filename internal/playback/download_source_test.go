// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"context"
	"errors"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// Downloads are a separate source-selection contract from static streaming:
// only physical, downloadable item families may produce an attachment source.
func TestPlaybackInfoApplicationDownloadSourceAllowsOnlyDownloadablePhysicalItems(t *testing.T) {
	videoFile := item.VideoTypeVideoFile
	dvd := item.VideoTypeDvd
	bluRay := item.VideoTypeBluRay

	for _, tc := range []struct {
		name    string
		kind    item.Kind
		path    string
		video   *item.VideoType
		allowed bool
	}{
		{name: "file audio", kind: item.KindAudio, path: "/media/song.flac", allowed: true},
		{name: "file audiobook", kind: item.KindAudioBook, path: "/media/book.m4b", allowed: true},
		{name: "file book", kind: item.KindBook, path: "/media/book.epub", allowed: true},
		{name: "file photo", kind: item.KindPhoto, path: "/media/photo.jpg", allowed: true},
		{name: "file movie", kind: item.KindMovie, path: "/media/movie.mp4", video: &videoFile, allowed: true},
		{name: "file episode", kind: item.KindEpisode, path: "/media/episode.mkv", video: &videoFile, allowed: true},
		{name: "file generic video", kind: item.KindVideo, path: "/media/video.mp4", video: &videoFile, allowed: true},
		{name: "folder", kind: item.KindFolder, path: "/media/folder", allowed: false},
		{name: "remote audio source", kind: item.KindAudio, path: "https://example.test/song.flac", allowed: false},
		{name: "DVD video", kind: item.KindMovie, path: "/media/dvd/VIDEO_TS.IFO", video: &dvd, allowed: false},
		{name: "Blu-ray video", kind: item.KindMovie, path: "/media/bluray/index.bdmv", video: &bluRay, allowed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := openPlaybackItemStore(t)
			id := downloadSourceUUID(t, "00000000-0000-0000-0000-0000000008e0")
			stored := item.Item{ID: id, Kind: tc.kind, Path: &tc.path, VideoType: tc.video}
			if err := items.Upsert(t.Context(), stored); err != nil {
				t.Fatalf("seed %s download source: %v", tc.name, err)
			}
			service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{Sources: items})

			source, err := downloadSource(t.Context(), service, dto.UUID{0x8a}, id)
			if tc.allowed {
				if err != nil {
					t.Fatalf("DownloadSource() error = %v", err)
				}
				if source.ID != id.String() {
					t.Errorf("DownloadSource() source ID = %q, want %q", source.ID, id.String())
				}
				return
			}
			if !errors.Is(err, playback.ErrDownloadNotAllowed) {
				t.Fatalf("DownloadSource() error = %v, want ErrDownloadNotAllowed", err)
			}
			if errors.Is(err, playback.ErrItemNotFound) {
				t.Fatalf("DownloadSource() error = %v must remain distinguishable from ErrItemNotFound", err)
			}
		})
	}
}

func TestPlaybackInfoApplicationDownloadSourceHidesMissingAndInaccessibleItems(t *testing.T) {
	userID := dto.UUID{0x8b}
	accessibleID := downloadSourceUUID(t, "00000000-0000-0000-0000-0000000008d0")
	missingID := downloadSourceUUID(t, "00000000-0000-0000-0000-0000000008d1")
	path := "/media/accessible.mp3"

	t.Run("missing item", func(t *testing.T) {
		service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
			Sources: staticSourceGraphReader{errors: map[dto.UUID]error{missingID: playback.ErrItemNotFound}},
		})
		_, err := downloadSource(t.Context(), service, userID, missingID)
		if !errors.Is(err, playback.ErrItemNotFound) {
			t.Fatalf("DownloadSource() error = %v, want ErrItemNotFound", err)
		}
	})

	t.Run("inaccessible item", func(t *testing.T) {
		items := openPlaybackItemStore(t)
		if err := items.Upsert(t.Context(), item.Item{ID: accessibleID, Kind: item.KindAudio, Path: &path}); err != nil {
			t.Fatalf("seed inaccessible download item: %v", err)
		}
		service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
			Sources: items,
			Access:  &staticSourceAccessReader{},
		})
		_, err := downloadSource(t.Context(), service, userID, accessibleID)
		if !errors.Is(err, playback.ErrItemNotFound) {
			t.Fatalf("DownloadSource() error = %v, want ErrItemNotFound", err)
		}
	})
}

// This named interface keeps download-specific source eligibility out of
// DirectFileSourceService, whose static stream operations must remain usable by
// their narrower route contracts.
func downloadSource(ctx context.Context, service playback.DownloadFileSourceService, userID, itemID dto.UUID) (playback.MediaSourceInfo, error) {
	return service.DownloadSource(ctx, userID, itemID)
}

func downloadSourceUUID(t *testing.T, value string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return id
}
