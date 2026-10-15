// SPDX-License-Identifier: GPL-2.0-only

package tmdb

import (
	"context"
	"errors"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
)

// TestSearchQueryCleansSceneReleaseNames pins the search-query cleaning (D-13)
// against live unmatched samples plus noisier movie scene-release forms.
// These are the parsed display Names the local Emby.Naming port produces;
// searchQuery turns each into the clean title TMDb's search expects. The
// display Name itself is never mutated here.
func TestSearchQueryCleansSceneReleaseNames(t *testing.T) {
	for _, testCase := range []struct {
		name string // parsed display Name (input)
		want string // TMDb search query (output)
	}{
		// --- live samples ---
		{"- The Emperors New Groove", "The Emperors New Groove"}, // leading "- " from a stripped [site] prefix
		{"3.10.to.Yuma", "3 10 to Yuma"},                         // dots for spaces
		{"(500) Days of Summer", "500 Days of Summer"},           // leading parenthetical, not a site tag
		{"22 July", "22 July"},                                   // already clean, must survive untouched
		{"11.22.63", "11 22 63"},                                 // dotted series title
		// --- noisier movie scene-release forms (issue "hard case") ---
		{"[ www.UsaBit.com ] - The Emperors New Groove", "The Emperors New Groove"}, // site prefix + leading dash
		{"[YTS.MX] The.Dark.Knight", "The Dark Knight"},                             // bracketed site tag with a TLD
		{"The Movie WEB-DL HEVC", "The Movie"},                                      // modern tags Emby.Naming's list omits
		{"Some.Film.1080p.BluRay.x264-GROUP", "Some Film"},                          // resolution/codec/group tail
		// --- must NOT over-strip real titles ---
		{"Blade Runner 2049", "Blade Runner 2049"}, // a year-like title number is not a token
		{"WALL·E", "WALL·E"},                       // middle dot preserved (parity with cleanName; not item.CleanName)
		{"Amélie", "Amélie"},                       // diacritics preserved for the search query
		// --- degenerate input ---
		{"   ", ""},
		{"", ""},
	} {
		if got := searchQuery(testCase.name); got != testCase.want {
			t.Errorf("searchQuery(%q) = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

func TestParentFolderName(t *testing.T) {
	pathOf := func(value string) *item.Item { return &item.Item{Path: &value} }
	for _, testCase := range []struct {
		label       string
		target      *item.Item
		libraryRoot string
		want        string
	}{
		{"per-title folder", pathOf("/media/Movies/The Matrix (1999)/The Matrix (1999).mkv"), "/media/Movies", "The Matrix (1999)"},
		{"loose file at library root", pathOf("/media/Movies/loose.mkv"), "/media/Movies", ""},
		{"library root with trailing slash", pathOf("/media/Movies/loose.mkv"), "/media/Movies/", ""},
		{"no path", &item.Item{}, "/media/Movies", ""},
		{"nil item", nil, "/media/Movies", ""},
	} {
		if got := parentFolderName(testCase.target, testCase.libraryRoot); got != testCase.want {
			t.Errorf("%s: parentFolderName = %q, want %q", testCase.label, got, testCase.want)
		}
	}
}

func TestFirstMatchIDLadder(t *testing.T) {
	type call struct {
		query string
		year  int
	}

	t.Run("relaxes the year when the exact-year search misses", func(t *testing.T) {
		var calls []call
		id, err := firstMatchID(context.Background(), "The Emperors New Groove", "", 2000, func(_ context.Context, query string, year int) (int, error) {
			calls = append(calls, call{query, year})
			if query == "The Emperors New Groove" && year == 0 {
				return 11688, nil
			}
			return 0, nil
		})
		if err != nil || id != 11688 {
			t.Fatalf("firstMatchID = %d, %v; want 11688, nil", id, err)
		}
		want := []call{{"The Emperors New Groove", 2000}, {"The Emperors New Groove", 0}}
		if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	})

	t.Run("falls back to the parent folder after the name misses", func(t *testing.T) {
		var calls []call
		id, err := firstMatchID(context.Background(), "VIDEO TS", "Inception 2010", 2010, func(_ context.Context, query string, year int) (int, error) {
			calls = append(calls, call{query, year})
			if query == "Inception 2010" && year == 2010 {
				return 27205, nil
			}
			return 0, nil
		})
		if err != nil || id != 27205 {
			t.Fatalf("firstMatchID = %d, %v; want 27205, nil", id, err)
		}
		want := []call{{"VIDEO TS", 2010}, {"VIDEO TS", 0}, {"Inception 2010", 2010}}
		if len(calls) != len(want) {
			t.Fatalf("calls = %v, want first three rungs %v", calls, want)
		}
		for i := range want {
			if calls[i] != want[i] {
				t.Fatalf("calls = %v, want %v", calls, want)
			}
		}
	})

	t.Run("dedupes identical rungs", func(t *testing.T) {
		var calls int
		id, err := firstMatchID(context.Background(), "Same", "Same", 0, func(_ context.Context, _ string, _ int) (int, error) {
			calls++
			return 0, nil
		})
		if err != nil || id != 0 {
			t.Fatalf("firstMatchID = %d, %v; want 0, nil", id, err)
		}
		if calls != 1 {
			t.Fatalf("attempt called %d times, want 1 (identical primary/fallback, no year)", calls)
		}
	})

	t.Run("propagates a search error", func(t *testing.T) {
		boom := errors.New("boom")
		if _, err := firstMatchID(context.Background(), "X", "", 0, func(_ context.Context, _ string, _ int) (int, error) {
			return 0, boom
		}); !errors.Is(err, boom) {
			t.Fatalf("firstMatchID error = %v, want boom", err)
		}
	})
}
