// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
)

// TestScannerRefreshTargetsScopeToLibrary is the regression for a recursive
// POST /Items/{libraryId}/Refresh that refreshed every library. Upstream
// ProviderManager.RefreshItem refreshes only the target item's subtree, so a
// refresh of one library must leave the other library unrefreshed.
func TestScannerRefreshTargetsScopeToLibrary(t *testing.T) {
	resolver := &scannerFixtureResolver{}
	harness := newRefreshStateHarness(t, resolver)
	alphaMovie := scannerPath(t, harness.alphaSourcePath, "alpha.mkv")
	writeScannerFile(t, alphaMovie, "alpha")
	bravoMovie := scannerPath(t, harness.bravoSourcePath, "bravo.mkv")
	writeScannerFile(t, bravoMovie, "bravo")
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}

	var mu sync.Mutex
	var refreshed []string
	refresher := scannerRefreshFunc(func(_ context.Context, refresh library.RefreshContext, candidate *item.Item) error {
		if candidate.Kind != item.KindMovie || refresh.Mode < library.RefreshModeFullRefresh {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		refreshed = append(refreshed, *candidate.Path)
		return nil
	})
	harness.scanner = newScanner(t, harness.topology, harness.items, library.NewChain(resolver), library.ScannerOptions{
		Concurrency: 1,
		Refresher:   refresher,
	})
	if _, err := harness.scanner.ScanWithOptions(t.Context(), library.RefreshOptions{
		Metadata: library.RefreshModeValidationOnly,
		Image:    library.RefreshModeValidationOnly,
	}); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	bravoItem := scannerItemAtPath(t, harness.items, bravoMovie)

	for _, tc := range []struct {
		name    string
		targets []library.RefreshTarget
		want    []string
	}{
		{name: "library id", targets: []library.RefreshTarget{{ItemID: harness.alphaFolderID}}, want: []string{alphaMovie}},
		{name: "item path", targets: []library.RefreshTarget{{ItemID: bravoItem.ID, Path: bravoMovie}}, want: []string{bravoMovie}},
		{name: "both libraries", targets: []library.RefreshTarget{{ItemID: harness.alphaFolderID}, {ItemID: harness.bravoFolderID}}, want: []string{alphaMovie, bravoMovie}},
		{name: "unscoped", want: []string{alphaMovie, bravoMovie}},
		{name: "target in no library", targets: []library.RefreshTarget{{ItemID: dto.UUID{1}}}, want: []string{alphaMovie, bravoMovie}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			refreshed = nil
			mu.Unlock()
			result, err := harness.scanner.ScanWithOptions(t.Context(), library.RefreshOptions{
				Metadata: library.RefreshModeFullRefresh,
				Image:    library.RefreshModeFullRefresh,
				Targets:  tc.targets,
			})
			if err != nil {
				t.Fatalf("scoped refresh: %v", err)
			}
			if len(result.Errors) != 0 {
				t.Fatalf("scoped refresh errors = %v", result.Errors)
			}
			mu.Lock()
			got := append([]string(nil), refreshed...)
			mu.Unlock()
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("refreshed movies = %v, want %v", got, tc.want)
			}
		})
	}
}
