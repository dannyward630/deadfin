// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"context"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestBuilderSelectsImagesByEnableTypeAndLimit defends image projection:
// enabled callers receive only allowed image types, the per-type limit bounds
// repeated backdrops, and tags/blur hashes retain their client map keys.
func TestBuilderSelectsImagesByEnableTypeAndLimit(t *testing.T) {
	items := openBuilderItemStore(t)
	id := builderUUID(t, 40)
	name := "Artwork"
	if err := items.Upsert(t.Context(), item.Item{ID: id, Kind: item.KindMovie, Name: &name}); err != nil {
		t.Fatalf("upsert artwork fixture: %v", err)
	}
	loaded, err := items.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("load artwork fixture: %v", err)
	}
	result, err := dtobuild.NewBuilder(items, imageEnricher{values: map[dto.UUID]dtobuild.Enrichment{id: {
		ImageTags:          map[string]string{"Primary": "poster", "Logo": "logo"},
		BackdropImageTags:  []string{"wide-a", "wide-b"},
		ImageBlurHashes:    map[string]map[string]string{"Primary": {"poster": "blur-poster"}, "Backdrop": {"wide-a": "blur-a", "wide-b": "blur-b"}, "Logo": {"logo": "blur-logo"}},
		InheritedImageTags: map[string]string{"Art": "parent-art"},
	}}}).Build(t.Context(), []item.Item{loaded}, store.ItemQueryDTOOptions{EnableImages: true, ImageTypes: []item.ImageType{item.ImageTypePrimary, item.ImageTypeBackdrop, item.ImageTypeArt}, ImageTypeLimit: 1}, nil)
	if err != nil {
		t.Fatalf("build image DTO: %v", err)
	}
	wire := builderWireObject(t, result.Items[0])
	if got, want := string(wire["ImageTags"]), `{"Art":"parent-art","Primary":"poster"}`; got != want {
		t.Errorf("ImageTags = %s, want allowed local/inherited tags %s", got, want)
	}
	if got, want := string(wire["BackdropImageTags"]), `["wide-a"]`; got != want {
		t.Errorf("BackdropImageTags = %s, want first allowed tag only %s", got, want)
	}
	if got, want := string(wire["ImageBlurHashes"]), `{"Backdrop":{"wide-a":"blur-a"},"Primary":{"poster":"blur-poster"}}`; got != want {
		t.Errorf("ImageBlurHashes = %s, want hashes only for selected image tags %s", got, want)
	}
}

// TestBuilderProjectsPrimaryImageAspectRatio defends the public item DTO
// contract: only requested (or detailed-default) positive primary image
// dimensions produce the image's width-to-height aspect ratio.
func TestBuilderProjectsPrimaryImageAspectRatio(t *testing.T) {
	primaryAspectRatio := 16.0 / 9.0
	tests := []struct {
		name     string
		images   []item.ImageInfo
		fields   []string
		detailed bool
		want     *float64
	}{
		{
			name:   "requested positive primary image uses image dimensions",
			images: []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "primary.jpg", Width: 320, Height: 180}},
			fields: []string{"PrimaryImageAspectRatio"},
			want:   &primaryAspectRatio,
		},
		{
			name:     "detailed defaults include positive primary image",
			images:   []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "primary.jpg", Width: 320, Height: 180}},
			detailed: true,
			want:     &primaryAspectRatio,
		},
		{
			name:   "sparse Build omits unrequested primary image aspect ratio",
			images: []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "primary.jpg", Width: 320, Height: 180}},
		},
		{
			name:   "requested item with no images omits primary image aspect ratio",
			fields: []string{"PrimaryImageAspectRatio"},
		},
		{
			name:   "requested primary image with zero width omits aspect ratio",
			images: []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "zero-width.jpg", Width: 0, Height: 180}},
			fields: []string{"PrimaryImageAspectRatio"},
		},
		{
			name:   "requested primary image with zero height omits aspect ratio",
			images: []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "zero-height.jpg", Width: 320, Height: 0}},
			fields: []string{"PrimaryImageAspectRatio"},
		},
		{
			name:   "requested primary image with negative width omits aspect ratio",
			images: []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "negative-width.jpg", Width: -320, Height: 180}},
			fields: []string{"PrimaryImageAspectRatio"},
		},
		{
			name:   "requested non-primary image does not supply aspect ratio",
			images: []item.ImageInfo{{Type: item.ImageTypeBackdrop, Index: 0, Path: "backdrop.jpg", Width: 320, Height: 180}},
			fields: []string{"PrimaryImageAspectRatio"},
		},
		{
			name:   "requested invalid primary index zero ignores later valid primary index",
			images: []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "invalid-primary.jpg", Width: 0, Height: 180}, {Type: item.ImageTypePrimary, Index: 1, Path: "later-primary.jpg", Width: 320, Height: 180}},
			fields: []string{"PrimaryImageAspectRatio"},
		},
		{
			name:   "requested missing primary index zero ignores later valid primary index",
			images: []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 1, Path: "later-primary.jpg", Width: 320, Height: 180}},
			fields: []string{"PrimaryImageAspectRatio"},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			items := openBuilderItemStore(t)
			id := builderUUID(t, 4_100+index)
			name := "Primary image aspect ratio"
			if err := items.Upsert(t.Context(), item.Item{
				ID: id, Kind: item.KindMovie, IsMovie: true, Name: &name,
				Width: builderInt(640), Height: builderInt(480), ImageInfos: test.images,
			}); err != nil {
				t.Fatalf("upsert %s fixture: %v", test.name, err)
			}
			value, err := items.Get(t.Context(), id)
			if err != nil {
				t.Fatalf("load %s fixture: %v", test.name, err)
			}

			builder := dtobuild.NewBuilder(items, nil)
			options := store.ItemQueryDTOOptions{Fields: test.fields}
			var result dto.QueryResult[dto.BaseItemDto]
			if test.detailed {
				result, err = builder.BuildDetailed(t.Context(), []item.Item{value}, options, nil)
			} else {
				result, err = builder.Build(t.Context(), []item.Item{value}, options, nil)
			}
			if err != nil {
				t.Fatalf("build %s DTO: %v", test.name, err)
			}
			if len(result.Items) != 1 {
				t.Fatalf("built %s item count = %d, want 1", test.name, len(result.Items))
			}

			projected := result.Items[0]
			wire := builderWireObject(t, projected)
			if test.want == nil {
				if projected.PrimaryImageAspectRatio != nil {
					t.Errorf("PrimaryImageAspectRatio = %v, want omission", *projected.PrimaryImageAspectRatio)
				}
				if _, found := wire["PrimaryImageAspectRatio"]; found {
					t.Error("PrimaryImageAspectRatio unexpectedly emitted")
				}
				return
			}
			if projected.PrimaryImageAspectRatio == nil {
				t.Fatal("PrimaryImageAspectRatio omitted, want primary image aspect ratio")
			}
			if got := *projected.PrimaryImageAspectRatio; got != *test.want {
				t.Errorf("PrimaryImageAspectRatio = %v, want %v", got, *test.want)
			}
			if _, found := wire["PrimaryImageAspectRatio"]; !found {
				t.Error("PrimaryImageAspectRatio omitted from wire DTO")
			}
		})
	}
}

type imageEnricher struct {
	values map[dto.UUID]dtobuild.Enrichment
}

func (e imageEnricher) Enrich(_ context.Context, _ []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (map[dto.UUID]dtobuild.Enrichment, error) {
	return e.values, nil
}
