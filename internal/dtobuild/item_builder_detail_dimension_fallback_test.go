// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestBuilderDetailedDimensionsFallBackFromDefaultPrimaryVideoStream
// locks DtoService's detail-only scalar fallback. Persisted item dimensions are
// authoritative; otherwise only the first projected video stream may supply
// dimensions. Direct detail uses DtoOptions() and therefore projects streams
// even when the client did not send Fields.
func TestBuilderDetailedDimensionsFallBackFromDefaultPrimaryVideoStream(t *testing.T) {
	firstVideoWidth, firstVideoHeight := 720, 404
	secondVideoWidth, secondVideoHeight := 1920, 1080
	storedWidth, storedHeight := 3840, 2160

	build := func(value item.Item, detailed bool, fields []string) dto.BaseItemDto {
		t.Helper()
		builder := dtobuild.NewBuilder(nil, projectionEnricher{values: map[dto.UUID]dtobuild.Enrichment{
			value.ID: {
				MediaStreams: []dto.MediaStreamInfo{
					{Index: 0, Type: dto.MediaStreamType("Audio"), Width: builderInt(99), Height: builderInt(88)},
					{Index: 1, Type: dto.MediaStreamType("Video"), Width: &firstVideoWidth, Height: &firstVideoHeight},
					{Index: 2, Type: dto.MediaStreamType("Video"), Width: &secondVideoWidth, Height: &secondVideoHeight},
				},
			},
		}})
		options := store.ItemQueryDTOOptions{Fields: fields}
		var (
			result dto.QueryResult[dto.BaseItemDto]
			err    error
		)
		if detailed {
			result, err = builder.BuildDetailed(t.Context(), []item.Item{value}, options, nil)
		} else {
			result, err = builder.Build(t.Context(), []item.Item{value}, options, nil)
		}
		if err != nil {
			t.Fatalf("build dimensions for %s: %v", value.ID, err)
		}
		if len(result.Items) != 1 {
			t.Fatalf("dimension projection items = %d, want 1", len(result.Items))
		}
		return result.Items[0]
	}

	t.Run("missing scalars use first requested video", func(t *testing.T) {
		value := item.Item{ID: builderUUID(t, 8_310), Kind: item.KindMovie, IsMovie: true, Name: builderString("stream dimensions")}
		projected := build(value, true, []string{"MediaStreams"})
		if projected.Width == nil || *projected.Width != firstVideoWidth || projected.Height == nil || *projected.Height != firstVideoHeight {
			t.Fatalf("detailed dimensions = (%v, %v), want first video %dx%d", projected.Width, projected.Height, firstVideoWidth, firstVideoHeight)
		}
	})

	t.Run("stored scalars win over projected video", func(t *testing.T) {
		value := item.Item{ID: builderUUID(t, 8_311), Kind: item.KindMovie, IsMovie: true, Name: builderString("stored dimensions"), Width: &storedWidth, Height: &storedHeight}
		projected := build(value, true, []string{"MediaStreams"})
		if projected.Width == nil || *projected.Width != storedWidth || projected.Height == nil || *projected.Height != storedHeight {
			t.Fatalf("detailed dimensions = (%v, %v), want stored item scalars %dx%d", projected.Width, projected.Height, storedWidth, storedHeight)
		}
	})

	t.Run("default streams derive dimensions", func(t *testing.T) {
		value := item.Item{ID: builderUUID(t, 8_312), Kind: item.KindMovie, IsMovie: true, Name: builderString("unrequested streams")}
		projected := build(value, true, nil)
		if projected.Width == nil || *projected.Width != firstVideoWidth || projected.Height == nil || *projected.Height != firstVideoHeight {
			t.Fatalf("default detailed dimensions = (%v, %v), want first video %dx%d", projected.Width, projected.Height, firstVideoWidth, firstVideoHeight)
		}
		if got, want := len(projected.MediaStreams), 3; got != want {
			t.Fatalf("default detail MediaStreams = %d, want %d", got, want)
		}
	})

	t.Run("sparse projection never derives dimensions", func(t *testing.T) {
		value := item.Item{ID: builderUUID(t, 8_313), Kind: item.KindMovie, IsMovie: true, Name: builderString("sparse streams")}
		projected := build(value, false, []string{"MediaStreams"})
		if projected.Width != nil || projected.Height != nil {
			t.Fatalf("sparse MediaStreams projection derived dimensions = (%v, %v), want omissions", projected.Width, projected.Height)
		}
	})
}
