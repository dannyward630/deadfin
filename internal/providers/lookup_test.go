// SPDX-License-Identifier: GPL-2.0-only

package providers_test

import (
	"context"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// fakeSearcher is a minimal Searcher for merge testing.
type fakeSearcher struct {
	name     string
	kind     item.Kind
	results  []providers.RemoteSearchResult
	supports bool
}

func (f fakeSearcher) Name() string                    { return f.name }
func (f fakeSearcher) SearchSupports(k item.Kind) bool { return f.supports }
func (f fakeSearcher) Search(_ context.Context, _ providers.Lookup) ([]providers.RemoteSearchResult, error) {
	return f.results, nil
}

// TestMergeSearchResultsDedupesByProviderID verifies two searchers with an
// overlapping provider ID produce one merged result with SearchProviderName
// from the first provider (ProviderManager.cs:967-988).
func TestMergeSearchResultsDedupesByProviderID(t *testing.T) {
	first := []providers.RemoteSearchResult{
		{Name: strPtr("Movie A"), ProviderIDs: map[string]string{"Tmdb": "111"}, SearchProviderName: "TheMovieDb"},
	}
	second := []providers.RemoteSearchResult{
		{Name: strPtr("Movie A"), ProviderIDs: map[string]string{"Tmdb": "111", "Imdb": "tt111"}, SearchProviderName: "IMDb"},
	}
	merged := providers.MergeSearchResults([][]providers.RemoteSearchResult{first, second})
	if len(merged) != 1 {
		t.Fatalf("merged results = %d, want 1 (dedupe by matching provider ID Tmdb:111)", len(merged))
	}
	if merged[0].SearchProviderName != "TheMovieDb" {
		t.Errorf("SearchProviderName = %q, want %q (first provider)", merged[0].SearchProviderName, "TheMovieDb")
	}
	// The missing provider ID from the second result should be merged in.
	if merged[0].ProviderIDs["Imdb"] != "tt111" {
		t.Errorf("Imdb ID not merged: %v", merged[0].ProviderIDs)
	}
	if merged[0].ProviderIDs["Tmdb"] != "111" {
		t.Errorf("Tmdb ID lost: %v", merged[0].ProviderIDs)
	}
}

// TestMergeSearchResultsPreservesDistinctIDs verifies results with no
// overlapping provider IDs stay separate.
func TestMergeSearchResultsPreservesDistinctIDs(t *testing.T) {
	results := []providers.RemoteSearchResult{
		{Name: strPtr("A"), ProviderIDs: map[string]string{"Tmdb": "1"}, SearchProviderName: "TheMovieDb"},
		{Name: strPtr("B"), ProviderIDs: map[string]string{"Tmdb": "2"}, SearchProviderName: "TheMovieDb"},
	}
	merged := providers.MergeSearchResults([][]providers.RemoteSearchResult{results})
	if len(merged) != 2 {
		t.Fatalf("merged = %d, want 2 (no dedupe)", len(merged))
	}
}

// TestFilteredSearchersEnabledFilter verifies the enabled filter drops a
// searcher whose name is not in the MetadataFetchers list when TypeOptions
// exist for the kind (BaseItemManager.cs:26-47).
func TestFilteredSearchersEnabledFilter(t *testing.T) {
	tmdb := fakeSearcher{name: "TheMovieDb", kind: item.KindMovie, supports: true}
	omdb := fakeSearcher{name: "OMDb", kind: item.KindMovie, supports: true}
	// Enabled list includes only TheMovieDb.
	filtered := providers.FilteredSearchers([]providers.Searcher{tmdb, omdb}, item.KindMovie, nil, []string{"TheMovieDb"}, false)
	if len(filtered) != 1 {
		t.Fatalf("filtered = %d, want 1 (only enabled)", len(filtered))
	}
	if filtered[0].Name() != "TheMovieDb" {
		t.Errorf("filtered[0] = %q, want TheMovieDb", filtered[0].Name())
	}
}

// TestFilteredSearchersIncludeDisabledBypassesFilter verifies
// IncludeDisabledProviders bypasses the enabled filter
// (ProviderManager.cs:571-574).
func TestFilteredSearchersIncludeDisabledBypassesFilter(t *testing.T) {
	tmdb := fakeSearcher{name: "TheMovieDb", kind: item.KindMovie, supports: true}
	omdb := fakeSearcher{name: "OMDb", kind: item.KindMovie, supports: true}
	filtered := providers.FilteredSearchers([]providers.Searcher{tmdb, omdb}, item.KindMovie, nil, []string{"TheMovieDb"}, true)
	if len(filtered) != 2 {
		t.Fatalf("filtered = %d, want 2 (includeDisabled bypasses filter)", len(filtered))
	}
}

// TestFilteredSearchersNilEnabledAllowsAll verifies enabled == nil (no
// TypeOptions for the kind) allows all searchers (BaseItemManager.cs:46-47).
func TestFilteredSearchersNilEnabledAllowsAll(t *testing.T) {
	tmdb := fakeSearcher{name: "TheMovieDb", kind: item.KindMovie, supports: true}
	omdb := fakeSearcher{name: "OMDb", kind: item.KindMovie, supports: true}
	filtered := providers.FilteredSearchers([]providers.Searcher{tmdb, omdb}, item.KindMovie, nil, nil, false)
	if len(filtered) != 2 {
		t.Fatalf("filtered = %d, want 2 (nil enabled = all enabled)", len(filtered))
	}
}

// TestFilteredSearchersEmptyEnabledBlocksAll verifies enabled == []string{}
// (TypeOptions exist with explicit empty MetadataFetchers) blocks all
// searchers (BaseItemManager.cs:40-46).
func TestFilteredSearchersEmptyEnabledBlocksAll(t *testing.T) {
	tmdb := fakeSearcher{name: "TheMovieDb", kind: item.KindMovie, supports: true}
	omdb := fakeSearcher{name: "OMDb", kind: item.KindMovie, supports: true}
	filtered := providers.FilteredSearchers([]providers.Searcher{tmdb, omdb}, item.KindMovie, nil, []string{}, false)
	if len(filtered) != 0 {
		t.Fatalf("filtered = %d, want 0 (empty enabled = none enabled)", len(filtered))
	}
}

// TestFilteredSearchersEmptyOrderPreservesRegistration verifies that when
// MetadataFetcherOrder is absent (nil), searchers keep registration order
// (ProviderManager.cs:597-607).
func TestFilteredSearchersEmptyOrderPreservesRegistration(t *testing.T) {
	tmdb := fakeSearcher{name: "TheMovieDb", kind: item.KindMovie, supports: true}
	omdb := fakeSearcher{name: "OMDb", kind: item.KindMovie, supports: true}
	filtered := providers.FilteredSearchers([]providers.Searcher{tmdb, omdb}, item.KindMovie, nil, []string{"TheMovieDb", "OMDb"}, false)
	if len(filtered) != 2 {
		t.Fatalf("filtered = %d, want 2", len(filtered))
	}
	if filtered[0].Name() != "TheMovieDb" {
		t.Errorf("filtered[0] = %q, want TheMovieDb (registration order)", filtered[0].Name())
	}
	if filtered[1].Name() != "OMDb" {
		t.Errorf("filtered[1] = %q, want OMDb (registration order)", filtered[1].Name())
	}
}

// TestFilteredSearchersOrderOverridesRegistration verifies MetadataFetcherOrder
// reorders searchers (ProviderManager.cs:597-607).
func TestFilteredSearchersOrderOverridesRegistration(t *testing.T) {
	tmdb := fakeSearcher{name: "TheMovieDb", kind: item.KindMovie, supports: true}
	omdb := fakeSearcher{name: "OMDb", kind: item.KindMovie, supports: true}
	filtered := providers.FilteredSearchers([]providers.Searcher{tmdb, omdb}, item.KindMovie, []string{"OMDb", "TheMovieDb"}, nil, false)
	if len(filtered) != 2 {
		t.Fatalf("filtered = %d, want 2", len(filtered))
	}
	if filtered[0].Name() != "OMDb" {
		t.Errorf("filtered[0] = %q, want OMDb (order override)", filtered[0].Name())
	}
	if filtered[1].Name() != "TheMovieDb" {
		t.Errorf("filtered[1] = %q, want TheMovieDb (order override)", filtered[1].Name())
	}
}

// TestMergeSearchResultsDeterministicMultipleIDs verifies that when a result
// has multiple provider IDs, the dedup first-match key selection is
// deterministic (sorted provider keys, not Go map iteration).
func TestMergeSearchResultsDeterministicMultipleIDs(t *testing.T) {
	first := []providers.RemoteSearchResult{
		{Name: strPtr("Movie"), ProviderIDs: map[string]string{"Tmdb": "111", "Imdb": "tt111"}, SearchProviderName: "TheMovieDb"},
	}
	second := []providers.RemoteSearchResult{
		{Name: strPtr("Movie"), ProviderIDs: map[string]string{"Imdb": "tt111"}, SearchProviderName: "IMDb"},
	}
	for i := 0; i < 100; i++ {
		m := providers.MergeSearchResults([][]providers.RemoteSearchResult{first, second})
		if len(m) != 1 {
			t.Fatalf("iteration %d: merged = %d, want 1", i, len(m))
		}
		if m[0].SearchProviderName != "TheMovieDb" {
			t.Fatalf("iteration %d: SearchProviderName = %q, want TheMovieDb", i, m[0].SearchProviderName)
		}
	}
}

// TestMergeSearchResultsCaseInsensitiveProviderIDs verifies provider IDs are
// matched case-insensitively.
func TestMergeSearchResultsCaseInsensitiveProviderIDs(t *testing.T) {
	first := []providers.RemoteSearchResult{
		{Name: strPtr("Movie"), ProviderIDs: map[string]string{"Tmdb": "111"}, SearchProviderName: "TheMovieDb"},
	}
	second := []providers.RemoteSearchResult{
		{Name: strPtr("Movie"), ProviderIDs: map[string]string{"tmdb": "111"}, SearchProviderName: "IMDb"},
	}
	merged := providers.MergeSearchResults([][]providers.RemoteSearchResult{first, second})
	if len(merged) != 1 {
		t.Fatalf("merged = %d, want 1 (case-insensitive Tmdb==tmdb)", len(merged))
	}
	if merged[0].SearchProviderName != "TheMovieDb" {
		t.Errorf("SearchProviderName = %q, want TheMovieDb (first)", merged[0].SearchProviderName)
	}
}

// TestMergeSearchResultsFirstMatchAndCaseFold verifies that a bridge candidate
// matching multiple existing results always merges into the earliest
// (lowest-indexed) existing result, not the first key that happens to sort
// first. It also verifies that case-variant provider keys produce one
// canonical entry with no duplicate case-variant keys.
func TestMergeSearchResultsFirstMatchAndCaseFold(t *testing.T) {
	// Result 0 has provider ID key "Z", result 1 has key "A".
	// A bridge candidate matching result 0 via "Z" and result 1 via "A"
	// must merge into result 0 (the earliest existing result), not
	// result 1 (which key "A" would select if we broke on first sorted key).
	for i := 0; i < 100; i++ {
		// Fresh maps each iteration — MergeSearchResults mutates the
		// merged result's ProviderIDs map in place.
		provider0 := []providers.RemoteSearchResult{
			{Name: strPtr("Result 0"), ProviderIDs: map[string]string{"Z": "z0"}, SearchProviderName: "P0"},
		}
		provider1 := []providers.RemoteSearchResult{
			{Name: strPtr("Result 1"), ProviderIDs: map[string]string{"A": "a1"}, SearchProviderName: "P1"},
		}
		bridge := []providers.RemoteSearchResult{
			{Name: strPtr("Bridge"), ProviderIDs: map[string]string{"A": "a1", "Z": "z0"}, SearchProviderName: "P2"},
		}
		merged := providers.MergeSearchResults([][]providers.RemoteSearchResult{provider0, provider1, bridge})
		if len(merged) != 2 {
			t.Fatalf("iteration %d: merged = %d, want 2 (bridge merged into earliest)", i, len(merged))
		}
		// The bridge must merge into result 0 (earliest), so result 0
		// carries both keys and SearchProviderName stays "P0".
		if merged[0].SearchProviderName != "P0" {
			t.Fatalf("iteration %d: merged[0].SearchProviderName = %q, want P0 (earliest existing)", i, merged[0].SearchProviderName)
		}
		if merged[0].ProviderIDs["A"] != "a1" {
			t.Errorf("iteration %d: bridge key A not merged into result 0: %v", i, merged[0].ProviderIDs)
		}
		if merged[0].ProviderIDs["Z"] != "z0" {
			t.Errorf("iteration %d: bridge key Z lost from result 0: %v", i, merged[0].ProviderIDs)
		}
	}

	// Case-variant keys must produce one canonical entry, not duplicates.
	first := []providers.RemoteSearchResult{
		{Name: strPtr("Movie"), ProviderIDs: map[string]string{"Tmdb": "111"}, SearchProviderName: "TheMovieDb"},
	}
	second := []providers.RemoteSearchResult{
		{Name: strPtr("Movie"), ProviderIDs: map[string]string{"tmdb": "111", "Imdb": "tt999"}, SearchProviderName: "IMDb"},
	}
	merged := providers.MergeSearchResults([][]providers.RemoteSearchResult{first, second})
	if len(merged) != 1 {
		t.Fatalf("case-fold merge: merged = %d, want 1", len(merged))
	}
	// No duplicate case-variant keys: only "Tmdb" (the original casing)
	// should remain, not both "Tmdb" and "tmdb".
	if merged[0].ProviderIDs["Tmdb"] != "111" {
		t.Errorf("canonical Tmdb key missing or wrong: %v", merged[0].ProviderIDs)
	}
	if _, has := merged[0].ProviderIDs["tmdb"]; has {
		t.Errorf("duplicate lowercase tmdb key remains alongside Tmdb: %v", merged[0].ProviderIDs)
	}
	if merged[0].ProviderIDs["Imdb"] != "tt999" {
		t.Errorf("Imdb ID not merged: %v", merged[0].ProviderIDs)
	}
}

// TestSortRemoteImagesRoundsMidpointsToEven verifies that SortRemoteImages
// rounds CommunityRating to one decimal with midpoint-to-even, matching
// upstream's Math.Round(value, 1) (EnumerableExtensions.cs:53). It uses two
// equal-language images whose ratings are 8.25 and 8.20. Midpoint-to-even
// rounds both to 8.2, so the rating does not break the tie. The image with
// the higher VoteCount (8.20) must sort first.
func TestSortRemoteImagesRoundsMidpointsToEven(t *testing.T) {
	images := []providers.RemoteImageInfo{
		{URL: "https://cdn.example.com/eight25.jpg", CommunityRating: 8.25, VoteCount: 50, Language: "en"},
		{URL: "https://cdn.example.com/eight20.jpg", CommunityRating: 8.20, VoteCount: 200, Language: "en"},
	}

	providers.SortRemoteImages(images, "en")

	if len(images) != 2 {
		t.Fatalf("images = %d, want 2", len(images))
	}
	// Midpoint-to-even makes both rounded ratings 8.2, so the vote count
	// selects the first image: the 8.20 image has the higher vote count.
	if images[0].CommunityRating != 8.20 {
		t.Errorf("first image rating = %v, want 8.20 (higher vote wins on a midpoint tie)", images[0].CommunityRating)
	}
	if images[0].VoteCount != 200 {
		t.Errorf("first image vote = %v, want 200", images[0].VoteCount)
	}
	if images[1].CommunityRating != 8.25 {
		t.Errorf("second image rating = %v, want 8.25", images[1].CommunityRating)
	}
	if images[1].VoteCount != 50 {
		t.Errorf("second image vote = %v, want 50", images[1].VoteCount)
	}
}
