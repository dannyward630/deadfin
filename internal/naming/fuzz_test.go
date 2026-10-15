// SPDX-License-Identifier: GPL-2.0-only

package naming

import "testing"

// The parsers take file names straight from scanned media trees. Any input
// must return a result or an error, never panic.
func FuzzParseEpisode(f *testing.F) {
	opts := DefaultOptions()
	for _, seed := range []string{
		"/TV/Show/Season 1/Show - S01E02 - Title.mkv",
		"/TV/Show/Season 01/1x02.mp4",
		"/TV/Show/Show.2024.03.15.mkv",
		"/TV/Show/S01E01-E03.mkv",
		"",
	} {
		f.Add(seed, false)
	}
	f.Fuzz(func(t *testing.T, path string, isDirectory bool) {
		_, _ = ParseEpisode(path, isDirectory, opts)
	})
}

func FuzzParseSeason(f *testing.F) {
	for _, seed := range []string{"/TV/Show/Season 2", "/TV/Show/Specials", "/TV/Show/S03", "/TV/Show/2024"} {
		f.Add(seed, "/TV/Show", true, true)
	}
	f.Fuzz(func(t *testing.T, path, parent string, specialAliases, numericFolders bool) {
		_ = ParseSeason(path, parent, specialAliases, numericFolders)
	})
}

func FuzzParseExternalSubtitle(f *testing.F) {
	opts := DefaultOptions()
	for _, seed := range []string{"/Movies/Film (2020)/Film (2020).en.forced.srt", "/m/a.default.sdh.ass", "/m/x.srt"} {
		f.Add(seed, "en")
	}
	f.Fuzz(func(t *testing.T, path, tokens string) {
		_ = ParseExternalSubtitle(path, tokens, opts)
	})
}

// string.Replace with OrdinalIgnoreCase is one non-overlapping pass.
func TestRemoveFoldAll(t *testing.T) {
	for _, tc := range []struct{ value, remove, want string }{
		{"Show Name S01", "show name", " S01"},
		{"aabb", "ab", "ab"},
		{"FORCED.forced", "Forced", "."},
		{"İx", "x", "İ"},
		{".A\xff.AA", ".a", "\xffA"},
		{"abc", "", "abc"},
	} {
		if got := removeFoldAll(tc.value, tc.remove); got != tc.want {
			t.Errorf("removeFoldAll(%q, %q) = %q, want %q", tc.value, tc.remove, got, tc.want)
		}
	}
}
