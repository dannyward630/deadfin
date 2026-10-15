// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/user"
)

// TestApplicationPlaybackInfoSelectsInheritedOriginalLanguageAudio defends the
// composed PlaybackInfo contract: an item without OriginalLanguage must select
// the audio matching its owning primary version or series, not an earlier
// conflicting IsOriginal stream.
func TestApplicationPlaybackInfoSelectsInheritedOriginalLanguageAudio(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	configureComposedAdministrator(t, app.Router())
	account := composedAdministrator(t, app)
	configuration := json.RawMessage(`{"AudioLanguagePreference":"OriginalLanguage","PlayDefaultAudioTrack":false,"SubtitleMode":"Default"}`)
	if err := app.UserService().UpdateConfiguration(t.Context(), user.Actor{UserID: account.ID, Administrator: true}, account.ID, configuration); err != nil {
		t.Fatalf("configure OriginalLanguage playback user: %v", err)
	}
	token := loginComposedAdministrator(t, app.Router())

	for index, tc := range []struct {
		name       string
		ownerKind  item.Kind
		targetKind item.Kind
		attach     func(*item.Item, dto.UUID)
	}{
		{
			name:       "alternate inherits primary version language",
			ownerKind:  item.KindMovie,
			targetKind: item.KindMovie,
			attach: func(target *item.Item, ownerID dto.UUID) {
				target.PrimaryVersionID = &ownerID
			},
		},
		{
			name:       "episode inherits series language",
			ownerKind:  item.KindSeries,
			targetKind: item.KindEpisode,
			attach: func(target *item.Item, ownerID dto.UUID) {
				target.ParentID = &ownerID
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := store.NewItemStore(app.stores)
			ownerID := composedItemEnrichmentUUID(t, 0xd210+index*2)
			targetID := composedItemEnrichmentUUID(t, 0xd211+index*2)
			ownerName, targetName := "playback language owner", "playback language inheriting target"
			path, container := "/library/playback-inherited-language.mkv", "mkv"
			ownerLanguage, conflictingOriginalLanguage, inheritedLanguage := "fre", "eng", "fre"
			owner := item.Item{ID: ownerID, Kind: tc.ownerKind, Name: &ownerName, OriginalLanguage: &ownerLanguage}
			target := item.Item{ID: targetID, Kind: tc.targetKind, Name: &targetName, Path: &path, Container: &container}
			tc.attach(&target, ownerID)
			if err := items.Upsert(t.Context(), owner); err != nil {
				t.Fatalf("upsert OriginalLanguage owner: %v", err)
			}
			if err := items.Upsert(t.Context(), target); err != nil {
				t.Fatalf("upsert OriginalLanguage inheriting target: %v", err)
			}
			if err := items.ReplaceStreams(t.Context(), targetID, []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", Language: &conflictingOriginalLanguage, IsOriginal: true},
				{Index: 7, Type: playback.StreamTypeAudio, Codec: "aac", Language: &inheritedLanguage, IsDefault: true},
			}); err != nil {
				t.Fatalf("seed OriginalLanguage playback streams: %v", err)
			}

			response := scannedPlaybackInfoRequest(t, app, token, "GET", "/Items/"+targetID.Dashed()+"/PlaybackInfo", nil, "")
			var sources []playback.MediaSourceInfo
			if err := json.Unmarshal(response["MediaSources"], &sources); err != nil {
				t.Fatalf("decode PlaybackInfo media sources: %v; response=%#v", err, response)
			}
			var targetSource *playback.MediaSourceInfo
			for sourceIndex := range sources {
				if sources[sourceIndex].ID == targetID.String() {
					targetSource = &sources[sourceIndex]
					break
				}
			}
			if targetSource == nil {
				t.Fatalf("PlaybackInfo sources do not include requested target %s: %#v", targetID, sources)
			}
			if targetSource.DefaultAudioStreamIndex == nil || *targetSource.DefaultAudioStreamIndex != 7 {
				t.Fatalf("PlaybackInfo target DefaultAudioStreamIndex = %v, want inherited-language French stream 7", inheritedPlaybackInfoIndex(targetSource.DefaultAudioStreamIndex))
			}
		})
	}
}

func inheritedPlaybackInfoIndex(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
