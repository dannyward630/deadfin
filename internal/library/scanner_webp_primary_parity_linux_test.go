// SPDX-License-Identifier: GPL-2.0-only

//go:build linux

package library_test

import (
	"path/filepath"
	"testing"
)

// TestScannerBasenameWebPPrimaryMatchesStockBlurHash pins the actual stock
// response value for both visible shapes. It runs only where the packaged Skia
// bridge is available; the cross-platform scan test covers durable discovery,
// dimensions, timestamps, and DTO projection separately.
func TestScannerBasenameWebPPrimaryMatchesStockBlurHash(t *testing.T) {
	fixture := scanWebPPrimaryFixture(t)
	const want = "W9G+H;?GkC%MR:-p~CVfcUpDr^ovw3#pi$SeWG$Nu4xZmtR*VznQ"
	for _, value := range []struct {
		shape string
		hash  *string
		dto   string
	}{
		{shape: "movie", hash: scannerWebPPrimaryImage(t, fixture.movie, fixture.artwork, fixture.modified).BlurHash, dto: fixture.movieDTO.ImageBlurHashes["Primary"][filepath.Base(fixture.artwork)]},
		{shape: "folder", hash: scannerWebPPrimaryImage(t, fixture.folder, fixture.artwork, fixture.modified).BlurHash, dto: fixture.folderDTO.ImageBlurHashes["Primary"][filepath.Base(fixture.artwork)]},
	} {
		t.Run(value.shape, func(t *testing.T) {
			if value.hash == nil || *value.hash != want {
				t.Fatalf("persisted %s WebP BlurHash = %q, want pinned stock hash %q", value.shape, scannerWebPBlurHash(value.hash), want)
			}
			if value.dto != want {
				t.Fatalf("%s DTO WebP BlurHash = %q, want pinned stock hash %q", value.shape, value.dto, want)
			}
		})
	}
}

func scannerWebPBlurHash(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
