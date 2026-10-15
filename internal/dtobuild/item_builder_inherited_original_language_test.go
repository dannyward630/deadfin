// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestStoreEnricherSelectsInheritedOriginalLanguageAudio defends the /Items
// MediaSources contract: OriginalLanguage preference must use the language
// inherited from a primary version or series, rather than choosing the first
// IsOriginal stream on an item with no language of its own.
func TestStoreEnricherSelectsInheritedOriginalLanguageAudio(t *testing.T) {
	originalPreference := "OriginalLanguage"
	configuration := dto.DefaultUserConfiguration()
	configuration.AudioLanguagePreference = &originalPreference
	configuration.PlayDefaultAudioTrack = false
	configurationJSON, err := json.Marshal(configuration)
	if err != nil {
		t.Fatalf("encode original-language user configuration: %v", err)
	}

	for _, tc := range []struct {
		name       string
		ownerKind  item.Kind
		targetKind item.Kind
		attach     func(*item.Item, dto.UUID)
	}{
		{
			name:       "alternate uses primary language instead of conflicting original stream",
			ownerKind:  item.KindMovie,
			targetKind: item.KindMovie,
			attach: func(target *item.Item, ownerID dto.UUID) {
				target.PrimaryVersionID = &ownerID
			},
		},
		{
			name:       "episode uses series language instead of conflicting original stream",
			ownerKind:  item.KindSeries,
			targetKind: item.KindEpisode,
			attach: func(target *item.Item, ownerID dto.UUID) {
				target.ParentID = &ownerID
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := openBuilderItemStore(t)
			userID, ownerID, targetID := builderUUID(t, 86_100), builderUUID(t, 86_101), builderUUID(t, 86_102)
			ownerName, targetName := "language owner", "language inheriting target"
			path, container := "/library/inherited-language.mkv", "mkv"
			ownerLanguage, conflictingOriginalLanguage, inheritedLanguage := "fre", "eng", "fre"
			owner := item.Item{ID: ownerID, Kind: tc.ownerKind, Name: &ownerName, OriginalLanguage: &ownerLanguage}
			target := item.Item{ID: targetID, Kind: tc.targetKind, Name: &targetName, Path: &path, Container: &container}
			tc.attach(&target, ownerID)
			if err := items.Upsert(t.Context(), owner); err != nil {
				t.Fatalf("upsert original-language owner: %v", err)
			}
			if err := items.Upsert(t.Context(), target); err != nil {
				t.Fatalf("upsert language-inheriting target: %v", err)
			}
			if err := items.ReplaceStreams(t.Context(), targetID, []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", Language: &conflictingOriginalLanguage, IsOriginal: true},
				{Index: 7, Type: playback.StreamTypeAudio, Codec: "aac", Language: &inheritedLanguage, IsDefault: true},
			}); err != nil {
				t.Fatalf("seed target media streams: %v", err)
			}
			loaded, err := items.Get(t.Context(), targetID)
			if err != nil {
				t.Fatalf("load language-inheriting target: %v", err)
			}

			builder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, inheritedLanguageEnricherUsers{user: store.User{ID: userID, Configuration: configurationJSON}}, nil))
			result, err := builder.Build(t.Context(), []item.Item{loaded}, store.ItemQueryDTOOptions{Fields: []string{"MediaSources"}}, &userID)
			if err != nil {
				t.Fatalf("build /Items MediaSources projection: %v", err)
			}
			if got, want := len(result.Items), 1; got != want {
				t.Fatalf("projected item count = %d, want %d", got, want)
			}
			if got, want := len(result.Items[0].MediaSources), 1; got != want {
				t.Fatalf("projected media source count = %d, want %d", got, want)
			}
			source := result.Items[0].MediaSources[0]
			if source.DefaultAudioStreamIndex == nil || *source.DefaultAudioStreamIndex != 7 {
				t.Fatalf("projected DefaultAudioStreamIndex = %v, want inherited-language French stream 7", inheritedLanguageIndex(source.DefaultAudioStreamIndex))
			}
		})
	}
}

type inheritedLanguageEnricherUsers struct{ user store.User }

func (users inheritedLanguageEnricherUsers) Get(_ context.Context, _ dto.UUID) (store.User, error) {
	return users.user, nil
}

func inheritedLanguageIndex(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
