// SPDX-License-Identifier: GPL-2.0-only

//go:build linux

package providers

import (
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
)

// The landscape fixture is byte-identical to the canonical Standalone Movie
// primary poster. Its hash is pinned from the stock item replay.
func TestEnrichLocalImageMetadataMatchesStockPosterBlurHash(t *testing.T) {
	poster := filepath.Join("..", "images", "testdata", "stock-corpus", "inputs", "landscape.jpg")
	target := &item.Item{ImageInfos: []item.ImageInfo{{Path: poster}}}
	if err := enrichLocalImageMetadata(t.Context(), target); err != nil {
		t.Fatalf("enrich canonical poster: %v", err)
	}
	if len(target.ImageInfos) != 1 || target.ImageInfos[0].BlurHash == nil {
		t.Fatalf("canonical poster image metadata = %#v, want a BlurHash", target.ImageInfos)
	}
	if got, want := *target.ImageInfos[0].BlurHash, "W8G+RE%NbF%MbZx]?coXn#k9oIoebMSQbFS2bZOA?dbTnzbXn~k9"; got != want {
		t.Fatalf("canonical poster BlurHash = %q, want pinned stock hash %q", got, want)
	}
}
