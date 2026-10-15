// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestPlaybackSelectionReaderInheritsOriginalLanguageFromOwningMedia defends
// the selection input contract: an alternate version inherits its primary
// version's language and an episode inherits its series language when its own
// metadata does not declare one.
func TestPlaybackSelectionReaderInheritsOriginalLanguageFromOwningMedia(t *testing.T) {
	originalPreference := "OriginalLanguage"
	configuration := dto.DefaultUserConfiguration()
	configuration.AudioLanguagePreference = &originalPreference
	configurationJSON, err := json.Marshal(configuration)
	if err != nil {
		t.Fatalf("encode original-language selection configuration: %v", err)
	}

	for _, tc := range []struct {
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
			items, _ := openReviewerItemStore(t)
			userID := contractUUID(t, 86_000)
			ownerID, targetID := contractUUID(t, 86_001), contractUUID(t, 86_002)
			ownerName, targetName := "language owner", "language inheriting target"
			ownerLanguage := "fre"
			owner := item.Item{ID: ownerID, Kind: tc.ownerKind, Name: &ownerName, OriginalLanguage: &ownerLanguage}
			target := item.Item{ID: targetID, Kind: tc.targetKind, Name: &targetName}
			tc.attach(&target, ownerID)
			if err := items.Upsert(t.Context(), owner); err != nil {
				t.Fatalf("upsert original-language owner: %v", err)
			}
			if err := items.Upsert(t.Context(), target); err != nil {
				t.Fatalf("upsert inheriting target: %v", err)
			}

			reader := store.NewPlaybackSelectionReader(items, inheritedLanguageSelectionUsers{user: store.User{ID: userID, Configuration: configurationJSON}})
			selection, found, err := reader.SourceSelection(t.Context(), userID, targetID, []dto.UUID{targetID})
			if err != nil {
				t.Fatalf("read playback selection: %v", err)
			}
			if !found {
				t.Fatal("playback selection was not found for persisted user and target")
			}
			if got, want := selection.OriginalLanguage, "fre"; got != want {
				t.Fatalf("selection OriginalLanguage = %q, want inherited %q", got, want)
			}
		})
	}
}

type inheritedLanguageSelectionUsers struct{ user store.User }

func (users inheritedLanguageSelectionUsers) Get(_ context.Context, _ dto.UUID) (store.User, error) {
	return users.user, nil
}
