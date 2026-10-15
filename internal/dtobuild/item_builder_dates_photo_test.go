// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestBuilderDateCreatedFieldSelectionAndPhotoFolderOmission pins two distinct
// BaseItemDto contracts from DtoService: DateCreated is emitted only when
// requested, while a Photo inherits BaseItem.IsFolder == false and therefore
// omits IsFolder rather than serializing false. Sources: DtoService.cs:961-965;
// BaseItem.cs:787-788; Photo.cs:11-18 at upstream/jellyfin.
func TestBuilderDateCreatedFieldSelectionAndPhotoFolderOmission(t *testing.T) {
	items := openBuilderItemStore(t)
	created := time.Date(2026, time.July, 27, 6, 44, 50, 0, time.UTC)
	photoName, movieName := "orientation-1", "movie-without-created-date"
	photo := item.Item{
		ID:          builderUUID(t, 610),
		Kind:        item.KindPhoto,
		Name:        &photoName,
		DateCreated: &created,
	}
	movieWithoutCreated := item.Item{
		ID:   builderUUID(t, 611),
		Kind: item.KindMovie,
		Name: &movieName,
	}
	for _, value := range []item.Item{photo, movieWithoutCreated} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert date projection fixture %s: %v", value.ID, err)
		}
	}
	loadedPhoto, err := items.Get(t.Context(), photo.ID)
	if err != nil {
		t.Fatalf("load persisted photo: %v", err)
	}
	loadedMovie, err := items.Get(t.Context(), movieWithoutCreated.ID)
	if err != nil {
		t.Fatalf("load persisted movie without DateCreated: %v", err)
	}
	builder := dtobuild.NewBuilder(items, nil)

	t.Run("requested date is exact and photo omits false folder fact", func(t *testing.T) {
		result, err := builder.Build(t.Context(), []item.Item{loadedPhoto}, store.ItemQueryDTOOptions{Fields: []string{"DateCreated"}}, nil)
		if err != nil {
			t.Fatalf("build selected photo: %v", err)
		}
		wire := builderWireObject(t, result.Items[0])
		if got, want := string(wire["DateCreated"]), `"2026-07-27T06:44:50Z"`; got != want {
			t.Errorf("requested Photo DateCreated = %s, want %s", got, want)
		}
		if _, found := wire["IsFolder"]; found {
			t.Errorf("Photo IsFolder = %s, want omitted because upstream Photo inherits false BaseItem.IsFolder", wire["IsFolder"])
		}
	})

	t.Run("unrequested and null dates remain omitted", func(t *testing.T) {
		unrequested, err := builder.Build(t.Context(), []item.Item{loadedPhoto}, store.ItemQueryDTOOptions{}, nil)
		if err != nil {
			t.Fatalf("build unrequested photo: %v", err)
		}
		if _, found := builderWireObject(t, unrequested.Items[0])["DateCreated"]; found {
			t.Error("unrequested DateCreated unexpectedly emitted")
		}
		nullDate, err := builder.Build(t.Context(), []item.Item{loadedMovie}, store.ItemQueryDTOOptions{Fields: []string{"DateCreated"}}, nil)
		if err != nil {
			t.Fatalf("build null DateCreated movie: %v", err)
		}
		if _, found := builderWireObject(t, nullDate.Items[0])["DateCreated"]; found {
			t.Error("nil DateCreated unexpectedly emitted as a JSON null")
		}
	})
}
