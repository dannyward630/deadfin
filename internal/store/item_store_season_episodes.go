// SPDX-License-Identifier: GPL-2.0-only

package store

import "github.com/deadfin-org/deadfin/internal/item"

// seasonEpisodeMatch is the SQL predicate that episode (an items alias) is an
// episode of season (an items alias). It ports Series.GetSeasonEpisodes and
// Series.FilterEpisodesBySeason (jellyfin@b04614e Series.cs). The candidates
// are the episodes of the season's series. An episode matches when:
//
//  1. its season number is the season's IndexNumber. With specials within
//     seasons and a season other than 0, the number is AiredSeasonNumber
//     (AirsAfterSeasonNumber ?? AirsBeforeSeasonNumber ?? ParentIndexNumber,
//     Episode.cs); else it is ParentIndexNumber;
//  2. both numbers are null, the season is virtual (no path), and the
//     episode has no season or a virtual one;
//  3. its season has the season's presentation key: it is the season, or a
//     season of the same series with the same IndexNumber
//     (Season.CreatePresentationUniqueKey).
//
// Without specials within seasons upstream reads only the episodes under an
// ancestor with the season's presentation key (Episode.GetAncestorIds adds
// SeasonId), so only rule 3 can select an episode.
//
// The predicate has no arguments. The series_id join reads ix_items_series.
func seasonEpisodeMatch(season, episode string, includeSpecials bool) string {
	sameKey := episode + ".season_id = " + season + ".id OR (" + season + ".index_number IS NOT NULL AND EXISTS (SELECT 1 FROM items AS episode_season WHERE episode_season.id = " + episode + ".season_id AND episode_season.kind = '" + item.KindSeason.String() + "' AND episode_season.series_id = " + season + ".series_id AND episode_season.index_number = " + season + ".index_number))"
	match := sameKey
	if includeSpecials {
		aired := "CASE WHEN " + season + ".index_number <> 0 THEN COALESCE(" + episode + ".airs_after_season_number, " + episode + ".airs_before_season_number, " + episode + ".parent_index_number) ELSE " + episode + ".parent_index_number END"
		number := season + ".index_number IS NOT NULL AND " + aired + " = " + season + ".index_number"
		virtual := season + ".index_number IS NULL AND " + season + ".path IS NULL AND " + episode + ".parent_index_number IS NULL AND (" + episode + ".season_id IS NULL OR EXISTS (SELECT 1 FROM items AS episode_season WHERE episode_season.id = " + episode + ".season_id AND episode_season.path IS NULL))"
		match = "(" + number + ") OR (" + virtual + ") OR " + sameKey
	}
	return episode + ".kind = '" + item.KindEpisode.String() + "' AND " + episode + ".series_id = " + season + ".series_id AND (" + match + ")"
}
