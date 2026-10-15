// SPDX-License-Identifier: GPL-2.0-only

// Package anime builds Deadfin's anime identity layer from offline seed data.
//
// Three community datasets feed it, and none of them needs an API request
// per item:
//
//   - manami anime-offline-database: one entry per anime with its AniList,
//     MyAnimeList, AniDB, and Kitsu ids, type, episode count, season, year,
//     synonyms, and untyped related-anime links.
//   - AniDB anime-titles.xml.gz: every AniDB title by anime id and language.
//     AniDB permits at most one download per day.
//   - Anime-Lists anime-list-master.xml: AniDB to TVDB and TMDb mapping with
//     the default TVDB season and the episode offset of each AniDB entry.
//
// [Fetcher] downloads the datasets with conditional requests, at most once
// per day each. [Build] joins them into a [Crosswalk]: an id crosswalk and a
// folded title index. [Crosswalk.Identify] maps a series to its AniDB,
// AniList, and MyAnimeList ids, and [Crosswalk.SeasonSegments] maps each TVDB
// season to its AniDB entries in episode order (a split-cour season has more
// than one).
//
// [Service] owns the current crosswalk and its refresh. It is constructed in
// the composition root; the package has no package-level state.
package anime
