// SPDX-License-Identifier: GPL-2.0-only

// Package tmdb fetches movie, series, season, episode, and person metadata and
// artwork from The Movie Database (TMDb) for Deadfin's refresh pipeline.
//
// It ports MediaBrowser.Providers/Plugins/Tmdb (Jellyfin, pinned b04614e). The
// upstream plugin wraps the third-party TMDbLib client; Deadfin models the TMDb
// REST v3 wire directly over the shared [remote.Client] (rate limited, response
// cached) so tests replay recorded cassettes and no live call happens in CI.
//
// Divergence D-11: Deadfin does not embed upstream's shared public TMDb API key.
// The key is supplied via config/env (see [Config]); absent a key the provider
// is inert (it enriches nothing) rather than failing a scan.
package tmdb

import (
	"regexp"
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
)

const (
	// ProviderName is the refresh-pipeline identity; it must match the value
	// libraries write in options.xml MetadataFetchers (TmdbUtils.ProviderName).
	ProviderName = "TheMovieDb"

	// PluginName and PluginID are the stable upstream plugin identity, surfaced
	// so jellyfin-web's TMDb settings page and /Plugins entries stay truthful.
	PluginName = "TMDb"
	PluginID   = "b8715ed1-6c47-4528-9ad3-f72deb539cd4"

	// apiBaseURL is the TMDb REST v3 root (TmdbUtils has no explicit constant;
	// TMDbLib targets this host).
	apiBaseURL = "https://api.themoviedb.org/3"

	// defaultImageBaseURL is TMDb's documented secure image CDN root, used until
	// the live /configuration response supplies a base URL.
	defaultImageBaseURL = "https://image.tmdb.org/t/p/"
)

// nonNameRegex matches runs of characters TMDb search treats as separators.
// Port of TmdbUtils.NonWordRegex (@"[\W_-[·]]+"): every non letter/number is a
// separator except the middle dot, and underscore is explicitly a separator.
var nonNameRegex = regexp.MustCompile(`[^\p{L}\p{N}·]+`)

// cleanName collapses separator runs to single spaces per TmdbUtils.CleanName.
func cleanName(name string) string {
	return nonNameRegex.ReplaceAllString(name, " ")
}

// normalizeLanguage ports TmdbUtils.NormalizeLanguage: TMDb requires the region
// subtag uppercase, drops unsupported Swiss regions to the bare language, and
// maps es-419 to a regional Spanish variant when a country code is known.
func normalizeLanguage(language, countryCode string) string {
	if language == "" {
		return language
	}
	if strings.EqualFold(language, "es-419") && countryCode != "" {
		if strings.EqualFold(countryCode, "AR") {
			return "es-AR"
		}
		return "es-MX"
	}
	parts := strings.Split(language, "-")
	if len(parts) == 2 {
		if strings.EqualFold(parts[1], "CH") {
			return parts[0]
		}
		return parts[0] + "-" + strings.ToUpper(parts[1])
	}
	return language
}

// imageLanguagesParam ports TmdbUtils.GetImageLanguagesParam: the preferred
// language, then untagged images ("null"), then English as a fallback.
func imageLanguagesParam(preferredLanguage, countryCode string) string {
	languages := make([]string, 0, 3)
	if preferredLanguage != "" {
		preferredLanguage = normalizeLanguage(preferredLanguage, countryCode)
		languages = append(languages, preferredLanguage)
	}
	languages = append(languages, "null")
	if !strings.EqualFold(preferredLanguage, "en") {
		languages = append(languages, "en")
	}
	return strings.Join(languages, ",")
}

// adjustImageLanguage ports TmdbUtils.AdjustImageLanguage: prefer the 5-letter
// request language when it extends the image's 2-letter code, and treat TMDb's
// "xx" sentinel (no language) as empty.
func adjustImageLanguage(imageLanguage, requestLanguage string) string {
	if imageLanguage == "" {
		return ""
	}
	if requestLanguage != "" && len(requestLanguage) > 2 && len(imageLanguage) == 2 &&
		strings.HasPrefix(strings.ToLower(requestLanguage), strings.ToLower(imageLanguage)) {
		return requestLanguage
	}
	if strings.EqualFold(imageLanguage, "xx") {
		return ""
	}
	return imageLanguage
}

// buildParentalRating ports TmdbUtils.BuildParentalRating: prefix non-US ratings
// with their country code and rewrite the German DE- prefix to FSK-.
func buildParentalRating(countryCode, ratingValue string) string {
	prefix := ""
	if !strings.EqualFold(countryCode, "US") {
		prefix = countryCode + "-"
	}
	rating := prefix + ratingValue
	return strings.ReplaceAll(rating, "DE-", "FSK-")
}

// mapCrewToPersonType ports TmdbUtils.MapCrewToPersonType. Only Director,
// Producer, and Writer crew are retained by the provider.
func mapCrewToPersonType(department, job string) (item.PersonKind, bool) {
	switch {
	case strings.EqualFold(department, "directing") && strings.EqualFold(job, "director"):
		return item.PersonKindDirector, true
	case strings.EqualFold(department, "production") && strings.EqualFold(job, "producer"):
		return item.PersonKindProducer, true
	case strings.EqualFold(department, "writing") && isWriterJob(job):
		return item.PersonKindWriter, true
	default:
		return item.PersonKindUnknown, false
	}
}

// isWriterJob mirrors TmdbUtils._writerJobs.
func isWriterJob(job string) bool {
	switch strings.ToLower(strings.TrimSpace(job)) {
	case "writer", "screenplay", "novel":
		return true
	default:
		return false
	}
}

// isTrailerVideo ports TmdbUtils.IsTrailerType: a YouTube trailer or teaser.
func isTrailerVideo(site, videoType string) bool {
	return strings.EqualFold(site, "youtube") &&
		(strings.EqualFold(videoType, "trailer") || strings.EqualFold(videoType, "teaser"))
}
