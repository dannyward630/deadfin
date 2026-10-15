// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestStoreEnricherKeepsVideoAndAudioTaxonomyEntitiesDistinct verifies that a
// shared taxonomy display name does not collapse Genre and MusicGenre identity.
// It also requires the taxonomy lookup to remain one page-level read regardless
// of whether a caller requests movie or audio first.
func TestStoreEnricherKeepsVideoAndAudioTaxonomyEntitiesDistinct(t *testing.T) {
	items, counting := openTaxonomyCountingItemStore(t)
	genreID, musicGenreID := builderUUID(t, 900), builderUUID(t, 901)
	movieID, audioID := builderUUID(t, 902), builderUUID(t, 903)
	sharedName, movieName, audioName := "Shared taxonomy", "Taxonomy movie", "Taxonomy track"
	for _, value := range []item.Item{
		{ID: genreID, Kind: item.KindGenre, Name: &sharedName},
		{ID: musicGenreID, Kind: item.KindMusicGenre, Name: &sharedName},
		{ID: movieID, Kind: item.KindMovie, Name: &movieName, Values: item.ItemValues{Genres: []string{sharedName}}},
		{ID: audioID, Kind: item.KindAudio, Name: &audioName, Values: item.ItemValues{Genres: []string{sharedName}}},
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert taxonomy fixture %s: %v", value.Kind, err)
		}
	}
	movie := builderProjectionLoad(t, items, movieID)
	audio := builderProjectionLoad(t, items, audioID)
	builder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil))

	for _, test := range []struct {
		name   string
		values []item.Item
	}{
		{name: "movie then audio", values: []item.Item{movie, audio}},
		{name: "audio then movie", values: []item.Item{audio, movie}},
	} {
		t.Run(test.name, func(t *testing.T) {
			counting.ResetReaderQueries()
			result, err := builder.Build(t.Context(), test.values, store.ItemQueryDTOOptions{Fields: []string{"Genres"}}, nil)
			if err != nil {
				t.Fatalf("build taxonomy projection: %v", err)
			}

			// One indexed clean-name lookup selects the referenced entities, the
			// named-entity item page consumes its fixed satellite batches, and
			// the video item consumes two subtitle-source batches. The full page
			// therefore remains 12 reads, not a taxonomy lookup per projected item.
			if got, want := counting.ReaderQueries(), int64(12); got != want {
				t.Fatalf("taxonomy page reader query count = %d, want %d bounded page batches", got, want)
			}
			assertTaxonomyGenreItem(t, items, result.Items, movieID, genreID, item.KindGenre)
			assertTaxonomyGenreItem(t, items, result.Items, audioID, musicGenreID, item.KindMusicGenre)
		})
	}
}

func assertTaxonomyGenreItem(t *testing.T, items *store.ItemStore, projected []dto.BaseItemDto, itemID, entityID dto.UUID, entityKind item.Kind) {
	t.Helper()
	for _, value := range projected {
		if value.ID != itemID {
			continue
		}
		if got, want := len(value.GenreItems), 1; got != want {
			t.Fatalf("projected %s GenreItems length = %d, want %d", itemID, got, want)
		}
		genre := value.GenreItems[0]
		if got, want := genre.ID, entityID; got != want {
			t.Errorf("projected %s GenreItems[0].ID = %s, want %s", itemID, got, want)
		}
		entity, err := items.Get(t.Context(), genre.ID)
		if err != nil {
			t.Fatalf("load projected taxonomy entity %s: %v", genre.ID, err)
		}
		if got, want := entity.Kind, entityKind; got != want {
			t.Errorf("projected %s GenreItems[0] resolves to %s, want %s", itemID, got, want)
		}
		return
	}
	t.Fatalf("projection did not contain item %s", itemID)
}

func openTaxonomyCountingItemStore(t *testing.T) (*store.ItemStore, *storetest.CountingSQLiteStores) {
	t.Helper()
	counting := storetest.OpenCountingSQLite(t, "taxonomy-counting.db")
	return store.NewItemStore(counting.Stores()), counting
}
