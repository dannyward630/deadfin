// SPDX-License-Identifier: GPL-2.0-only

package tmdb

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/naming"
)

// Divergence D-13: the TMDb title search deviates from upstream's single
// `TmdbUtils.CleanName(ParseName(name).Name)` call. Upstream collapses
// non-word runs to spaces once (see [cleanName]) and searches, leaving edge
// whitespace and residual scene-release cruft (a leading "- " after a stripped
// "[site]" prefix, dotted separators, and modern tags Emby.Naming's CleanStrings
// list omits — WEB-DL/HEVC/REMUX). [searchQuery] additionally trims the query
// and strips a leading site prefix and a trailing release-token tail, and
// [firstMatchID] runs a small fallback ladder (name+year, name, parent-folder
// +year, parent-folder). The display Name/year are never touched — only the
// transient search query changes.

// leadingSitePrefix matches a leading "[www.site.com]" / "(site.tld)" release
// tag — a bracketed group whose interior carries a URL scheme or a dotted TLD —
// together with any separators that trail it. It deliberately does not match a
// numeric parenthetical like "(500)", which is part of a real title.
var leadingSitePrefix = regexp.MustCompile(`(?i)^\s*[\[(][^\])]*(?:www\.|https?://|\.(?:com|net|org|info|to|me|mx|tv|cc|se|it|eu|co|xyz))[^\])]*[\])][\s._-]*`)

// releaseTokenTail matches the first standalone quality/source/codec/audio token
// in a name — the point at which the title ends and release cruft begins. Only
// techy tokens that essentially never occur as real title words are listed;
// ambiguous edition words (proper, repack, remastered, limited, internal) are
// intentionally omitted to avoid truncating genuine titles. Emby.Naming's
// CleanStrings already removes most of these when a separator+year split does
// not; this is the query-only net for the residual (notably year-less) cases.
var releaseTokenTail = regexp.MustCompile(`(?i)(?:^|[ ._\-\[(])(?:720p|1080p|2160p|480p|576p|4k|8k|uhd|x264|x265|h264|h265|hevc|xvid|divx|avc|bluray|blu-ray|bdrip|brrip|dvdrip|dvdscr|webrip|web-?dl|hdtv|hdrip|pdtv|hdcam|remux|aac|ac3|dts|truehd)(?:[ ._\-)\]]|$)`)

// searchQuery derives a TMDb search query from a locally parsed display name
// (D-13). It strips a leading site prefix and a trailing release-token tail,
// applies upstream's [cleanName] separator collapse, then trims. If stripping
// empties the string it falls back to the faithful CleanName of the original,
// so a title made only of separator-like tokens still yields a query.
func searchQuery(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	stripped := leadingSitePrefix.ReplaceAllString(trimmed, "")
	if cut := releaseTokenTail.FindStringIndex(stripped); cut != nil && cut[0] > 0 {
		stripped = stripped[:cut[0]]
	}
	if query := strings.TrimSpace(cleanName(stripped)); query != "" {
		return query
	}
	return strings.TrimSpace(cleanName(trimmed))
}

// parentFolderName returns the base name of the item's containing directory when
// it is a plausible per-title folder — present, not the library root, not a
// filesystem root — for use as a secondary TMDb search source (D-13). It returns
// "" when no such folder is available.
func parentFolderName(target *item.Item, libraryRoot string) string {
	if target == nil || target.Path == nil {
		return ""
	}
	path := strings.TrimSpace(*target.Path)
	if path == "" {
		return ""
	}
	dir := filepath.Dir(path)
	if dir == "" || dir == "." || dir == string(filepath.Separator) {
		return ""
	}
	if libraryRoot != "" && filepath.Clean(dir) == filepath.Clean(libraryRoot) {
		return ""
	}
	base := filepath.Base(dir)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

// searchTerms returns the primary and fallback search queries and the search
// year for target. It ports the ParseName step of TmdbMovieProvider and
// TmdbSeriesProvider GetMetadata: LibraryManager.ParseName strips the year
// from the name ("Apollo 13 (1995)" searches as "Apollo 13", 1995), and the
// search year is info.Year ?? parsedName.Year. The parent-folder fallback
// (D-13) is parsed the same way.
func (provider *Provider) searchTerms(target *item.Item, libraryRoot string) (primary, fallback string, year int) {
	name := ""
	if target.Name != nil {
		name = *target.Name
	}
	parsedName, parsedYear := parseName(name, provider.naming)
	parsedFolder, folderYear := parseName(parentFolderName(target, libraryRoot), provider.naming)
	switch {
	case target.ProductionYear != nil:
		year = *target.ProductionYear
	case parsedYear != 0:
		year = parsedYear
	default:
		year = folderYear
	}
	return searchQuery(parsedName), searchQuery(parsedFolder), year
}

// parseName ports LibraryManager.ParseName: VideoResolver.CleanDateTime, then
// VideoResolver.TryCleanString on the name it returns.
func parseName(name string, options naming.NamingOptions) (string, int) {
	if strings.TrimSpace(name) == "" {
		return "", 0
	}
	result := naming.CleanDateTime(name, options)
	cleaned := result.Name
	if value, ok := naming.CleanString(cleaned, options); ok {
		cleaned = value
	}
	year := 0
	if result.Year != nil {
		year = *result.Year
	}
	return cleaned, year
}

// firstMatchID walks the TMDb search ladder (D-13) and returns the first
// candidate id, or 0 on a clean miss (never an error-path change). attempt runs
// one search and returns its top result id (0 when the search is empty). primary
// is the cleaned display name; fallback is the cleaned parent-folder name (may
// be ""). Each distinct (query, year) rung is tried once, in order:
// name+year, name (year relaxed), parent+year, parent (year relaxed).
func firstMatchID(ctx context.Context, primary, fallback string, year int, attempt func(context.Context, string, int) (int, error)) (int, error) {
	type rung struct {
		query string
		year  int
	}
	rungs := make([]rung, 0, 4)
	seen := make(map[rung]bool, 4)
	add := func(query string, y int) {
		query = strings.TrimSpace(query)
		if query == "" {
			return
		}
		key := rung{query: query, year: y}
		if seen[key] {
			return
		}
		seen[key] = true
		rungs = append(rungs, key)
	}
	add(primary, year)
	if year > 0 {
		add(primary, 0) // year-window relaxation for regional-release off-by-one
	}
	add(fallback, year)
	if year > 0 {
		add(fallback, 0)
	}
	for _, current := range rungs {
		id, err := attempt(ctx, current.query, current.year)
		if err != nil {
			return 0, err
		}
		if id > 0 {
			return id, nil
		}
	}
	return 0, nil
}
