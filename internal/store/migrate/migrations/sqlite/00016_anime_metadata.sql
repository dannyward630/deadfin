-- +goose Up
-- Anime metadata. These tables are keyed by external ids, not
-- by items: an item reaches its rows through provider_ids. The AniList
-- provider owns all four tables.

-- Every known title of an anime, searchable through item queries. provider
-- and external_id match a provider_ids row (for example AniList/16498).
-- clean_title is item.CleanName(title); fold_title is anime.FoldTitle(title).
CREATE TABLE anime_titles (
    provider TEXT NOT NULL,
    external_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    title TEXT NOT NULL,
    language TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT '',
    clean_title TEXT NOT NULL,
    fold_title TEXT NOT NULL,
    PRIMARY KEY (provider, external_id, ordinal)
);

-- Series-level AniList media facts (the relation graph nodes).
CREATE TABLE anime_media (
    anilist_id INTEGER PRIMARY KEY,
    anidb_id INTEGER,
    mal_id INTEGER,
    format TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    season TEXT NOT NULL DEFAULT '',
    season_year INTEGER,
    episodes INTEGER,
    title TEXT NOT NULL DEFAULT '',
    date_fetched INTEGER NOT NULL
);

-- AniList relation edges (SEQUEL, PREQUEL, SIDE_STORY, PARENT, ...).
CREATE TABLE anime_relations (
    anilist_id INTEGER NOT NULL,
    relation_type TEXT NOT NULL,
    target_anilist_id INTEGER NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_format TEXT NOT NULL DEFAULT '',
    target_title TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (anilist_id, relation_type, target_anilist_id)
);
CREATE INDEX ix_anime_relations_target ON anime_relations(target_anilist_id, anilist_id);

-- The AniDB entries of each season in episode order, for TVDB numbering
-- (numbering 'tvdb', series_id = TVDB series id) and TMDb numbering
-- ('tmdb', series_id = TMDb TV id). A split-cour season has one row per
-- cour; episode_offset places the cour in the season.
CREATE TABLE anime_season_map (
    numbering TEXT NOT NULL,
    series_id INTEGER NOT NULL,
    season_number INTEGER NOT NULL,
    ordinal INTEGER NOT NULL,
    anidb_id INTEGER NOT NULL,
    anilist_id INTEGER,
    mal_id INTEGER,
    episode_offset INTEGER NOT NULL DEFAULT 0,
    name TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (numbering, series_id, season_number, ordinal)
);

-- +goose Down
DROP TABLE anime_season_map;
DROP INDEX ix_anime_relations_target;
DROP TABLE anime_relations;
DROP TABLE anime_media;
DROP TABLE anime_titles;
