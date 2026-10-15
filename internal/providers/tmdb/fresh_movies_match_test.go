// SPDX-License-Identifier: GPL-2.0-only

package tmdb_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
)

// TestProviderMatchesFreshMovieNamesFromCassette pins the ParseName step of
// TmdbMovieProvider.GetMetadata on names from the natively scanned "Fresh
// Movies" library. LibraryManager.ParseName strips the year from a directory
// name, so "Apollo 13 (1995)" searches as "Apollo 13", and an item without a
// year takes the parsed one. The dotted file name that older scans stored
// still matches. The cassette is a hand-written fixture; its
// search interactions are keyed on the exact query and year, so a match proves
// the query. No live API is used.
func TestProviderMatchesFreshMovieNamesFromCassette(t *testing.T) {
	const root = "/media/movies"
	for _, testCase := range []struct {
		name     string
		path     string
		year     *int
		wantID   string
		wantName string
	}{
		{
			name:     "Apollo 13 (1995)",
			path:     root + "/Apollo 13 (1995)/Apollo.13.1995.2160p.WEB-DL.mkv",
			year:     intPtr(1995),
			wantID:   "568",
			wantName: "Apollo 13",
		},
		{
			name:     "Apollo.13",
			path:     root + "/Apollo 13 (1995)/Apollo.13.1995.2160p.WEB-DL.mkv",
			year:     intPtr(1995),
			wantID:   "568",
			wantName: "Apollo 13",
		},
		{
			name:     "Battle of Britain (1969)",
			path:     root + "/Battle of Britain (1969)/00000.m2ts",
			wantID:   "10511",
			wantName: "Battle of Britain",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			provider := newCassetteProvider(t, "testdata/fresh-movies.json")
			target := item.Item{
				Kind:           item.KindMovie,
				Name:           stringPtr(testCase.name),
				Path:           stringPtr(testCase.path),
				ProductionYear: testCase.year,
			}
			refresh := movieRefreshContext()
			refresh.LibraryRoot = root
			handled, err := provider.Refresh(t.Context(), refresh, &target)
			if err != nil {
				t.Fatalf("refresh %q: %v", testCase.name, err)
			}
			if !handled {
				t.Fatalf("provider did not match %q", testCase.name)
			}
			if id, ok := target.ProviderIDs.Get("Tmdb"); !ok || id != testCase.wantID {
				t.Fatalf("Tmdb id = %q, %t; want %s", id, ok, testCase.wantID)
			}
			if target.Name == nil || *target.Name != testCase.wantName {
				t.Fatalf("Name = %v, want %q", target.Name, testCase.wantName)
			}
			if target.Overview == nil || *target.Overview == "" {
				t.Fatal("Overview is empty, want the TMDb overview")
			}
			hasPoster := false
			for _, image := range target.ImageInfos {
				hasPoster = hasPoster || image.Type == item.ImageTypePrimary
			}
			if !hasPoster {
				t.Fatalf("ImageInfos = %#v, want a TMDb poster", target.ImageInfos)
			}
		})
	}
}
