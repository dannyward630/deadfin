// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playback"
)

// Universal playback selects an alternate source with OrdinalIgnoreCase UUID
// matching, while direct-file selection retains the exact source spelling that
// static audio routes historically require.
func TestPlaybackInfoApplicationUniversalAlternateSelectionIsCaseInsensitiveButStaticSourceIsExact(t *testing.T) {
	itemID := universalSelectionUUID(t, "00000000-0000-0000-0000-0000000007a0")
	alternateID := universalSelectionUUID(t, "00000000-0000-0000-0000-0000000007a1")
	service := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources: staticSourceGraphReader{records: map[dto.UUID][]playback.MediaSourceRecord{
			itemID: {
				universalSelectionRecord(itemID, "/media/primary.mp4"),
				universalSelectionRecord(alternateID, "/media/alternate.mp4"),
			},
		}},
		NewPlaySessionID: func() string { return "universal-case-selection" },
	})
	caseVariantAlternateID := strings.ToUpper(alternateID.String())

	t.Run("universal playback resolves alternate UUID without regard to case", func(t *testing.T) {
		request := playback.NewPlaybackInfoRequest()
		request.ItemID = itemID
		request.MediaSourceID = caseVariantAlternateID
		request.DeviceProfile = &playback.DeviceProfile{DirectPlayProfiles: []playback.DirectPlayProfile{{
			Type: playback.DlnaVideo, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac",
		}}}

		got, err := service.PlaybackInfo(t.Context(), request)
		if err != nil {
			t.Fatalf("PlaybackInfo() error = %v", err)
		}
		if len(got.MediaSources) != 1 {
			t.Fatalf("PlaybackInfo() sources = %#v, want exactly the requested alternate", got.MediaSources)
		}
		if got.MediaSources[0].ID != alternateID.String() {
			t.Errorf("PlaybackInfo() selected source = %q, want canonical alternate ID %q", got.MediaSources[0].ID, alternateID.String())
		}
	})

	t.Run("direct static lookup rejects a case-variant alternate UUID", func(t *testing.T) {
		_, err := service.StaticSource(t.Context(), dto.NilUUID, itemID, caseVariantAlternateID)
		if !errors.Is(err, playback.ErrMediaSourceNotFound) {
			t.Fatalf("StaticSource(%q) error = %v, want ErrMediaSourceNotFound", caseVariantAlternateID, err)
		}
	})
}

func universalSelectionRecord(id dto.UUID, path string) playback.MediaSourceRecord {
	container := "mp4"
	return playback.MediaSourceRecord{
		ID:        id,
		Path:      &path,
		Container: &container,
		MediaStreams: []playback.MediaStream{
			{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
			{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", IsDefault: true},
		},
	}
}

func universalSelectionUUID(t *testing.T, value string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return id
}
