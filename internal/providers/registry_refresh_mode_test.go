// SPDX-License-Identifier: GPL-2.0-only

package providers_test

import (
	"context"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// TestRegistryRemoteStageHonorsRefreshMode locks the refresh-mode gate: the
// remote-metadata stage runs on an item that already carries provider metadata
// only when the refresh intent forces it (a full/replace refresh), mirroring
// MetadataService's ExecuteRemoteProviders call site. A plain Default refresh
// still runs it, so a normal library scan keeps fetching; None/ValidationOnly
// suppress it. The obsolete EnableInternetProviders flag does not gate the
// stage: upstream marks it obsolete and never reads it, and the web client
// never sends it.
func TestRegistryRemoteStageHonorsRefreshMode(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		internet   bool
		mode       library.RefreshMode
		replaceAll bool
		wantRemote bool
	}{
		{name: "none skips remote", internet: true, mode: library.RefreshModeNone, wantRemote: false},
		{name: "validation only skips remote", internet: true, mode: library.RefreshModeValidationOnly, wantRemote: false},
		{name: "default runs remote", internet: true, mode: library.RefreshModeDefault, wantRemote: true},
		{name: "full refresh runs remote on an already-scanned item", internet: true, mode: library.RefreshModeFullRefresh, wantRemote: true},
		{name: "replace-all metadata forces remote under none", internet: true, mode: library.RefreshModeNone, replaceAll: true, wantRemote: true},
		{name: "full refresh ignores the obsolete internet providers flag", internet: false, mode: library.RefreshModeFullRefresh, wantRemote: true},
		{name: "default ignores the obsolete internet providers flag", internet: false, mode: library.RefreshModeDefault, wantRemote: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			remoteCalls := 0
			registry := providers.NewRegistry(
				providerFunc{name: "nfo", stage: providers.ProviderStageLocalMetadata, refresh: func(context.Context, library.RefreshContext, *item.Item) (bool, error) {
					return true, nil
				}},
				providerFunc{name: "remote", stage: providers.ProviderStageRemoteMetadata, refresh: func(_ context.Context, _ library.RefreshContext, target *item.Item) (bool, error) {
					remoteCalls++
					target.ProviderIDs.Set("Tmdb", "603")
					return true, nil
				}},
			)

			// An already-scanned movie: it resolves and carries persisted metadata.
			target := item.Item{Kind: item.KindMovie, Name: providerString("The Matrix")}
			target.ProviderIDs.Set("Imdb", "tt0133093")

			refresh := library.RefreshContext{
				Mode:               testCase.mode,
				ReplaceAllMetadata: testCase.replaceAll,
				Options:            library.LibraryOptions{EnableInternetProviders: testCase.internet},
			}
			if err := registry.Refresh(t.Context(), refresh, &target); err != nil {
				t.Fatalf("registry refresh: %v", err)
			}

			if wantCalls := boolToInt(testCase.wantRemote); remoteCalls != wantCalls {
				t.Fatalf("remote stage calls = %d, want %d", remoteCalls, wantCalls)
			}
			if _, hasTmdb := target.ProviderIDs.Get("Tmdb"); hasTmdb != testCase.wantRemote {
				t.Fatalf("backfilled Tmdb id = %t, want %t", hasTmdb, testCase.wantRemote)
			}
		})
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
