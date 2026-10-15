// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/deadfin-org/deadfin/internal/anime"
	"github.com/deadfin-org/deadfin/internal/item"
)

// AnimeTitle is one stored title of an anime.
type AnimeTitle struct {
	Title    string
	Language string
	// Kind is the title type: "english", "romaji", "native", "synonym", or
	// an AniDB title type such as "official".
	Kind string
}

// AnimeTitleSet is the full title list of one external anime id. Provider and
// ExternalID equal a provider_ids row, so item search reaches the titles
// through the item's provider ids.
type AnimeTitleSet struct {
	Provider   string
	ExternalID string
	Titles     []AnimeTitle
}

// AnimeRelation is one AniList relation edge.
type AnimeRelation struct {
	RelationType    string
	TargetAniListID int
	TargetType      string
	TargetFormat    string
	TargetTitle     string
}

// AnimeMedia is the series-level AniList record of one anime.
type AnimeMedia struct {
	AniListID   int
	AniDBID     int
	MALID       int
	Format      string
	Status      string
	Season      string
	SeasonYear  int
	Episodes    int
	Title       string
	DateFetched time.Time
	Relations   []AnimeRelation
	// TitleSets replace the stored titles of each listed external id.
	TitleSets []AnimeTitleSet
}

// Season-map numberings.
const (
	AnimeNumberingTVDB = "tvdb"
	AnimeNumberingTMDb = "tmdb"
)

// AnimeSegment is one AniDB entry of a TVDB or TMDb season.
type AnimeSegment struct {
	AniDBID       int
	AniListID     int
	MALID         int
	EpisodeOffset int
	Name          string
}

// AnimeStore persists anime metadata keyed by external ids:
// titles for search, AniList media and relations, and the TVDB and TMDb season maps.
type AnimeStore struct{ stores Stores }

// NewAnimeStore constructs the anime metadata adapter.
func NewAnimeStore(stores Stores) *AnimeStore { return &AnimeStore{stores: stores} }

func (s *AnimeStore) p(index int) string { return s.stores.Dialect().Placeholder(index) }

func (s *AnimeStore) placeholders(count int) string {
	result := s.p(1)
	for index := 2; index <= count; index++ {
		result += ", " + s.p(index)
	}
	return result
}

// SaveMedia replaces one AniList media record, its relation edges, and the
// titles of each listed title set, in one transaction.
func (s *AnimeStore) SaveMedia(ctx context.Context, media AnimeMedia) (err error) {
	if media.AniListID <= 0 {
		return fmt.Errorf("save anime media: AniList id is required")
	}
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	d := s.stores.Dialect()
	if _, err := tx.ExecContext(ctx, "DELETE FROM anime_media WHERE anilist_id = "+s.p(1), media.AniListID); err != nil {
		return fmt.Errorf("save anime media: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO anime_media (anilist_id, anidb_id, mal_id, format, status, season, season_year, episodes, title, date_fetched) VALUES ("+s.placeholders(10)+")",
		media.AniListID, nullPositive(media.AniDBID), nullPositive(media.MALID), media.Format, media.Status, media.Season,
		nullPositive(media.SeasonYear), nullPositive(media.Episodes), media.Title, d.TimeValue(media.DateFetched.UTC()),
	); err != nil {
		return fmt.Errorf("save anime media: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM anime_relations WHERE anilist_id = "+s.p(1), media.AniListID); err != nil {
		return fmt.Errorf("save anime relations: %w", err)
	}
	seen := make(map[[2]string]bool, len(media.Relations))
	for _, relation := range media.Relations {
		key := [2]string{relation.RelationType, strconv.Itoa(relation.TargetAniListID)}
		if relation.TargetAniListID <= 0 || relation.RelationType == "" || seen[key] {
			continue
		}
		seen[key] = true
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO anime_relations (anilist_id, relation_type, target_anilist_id, target_type, target_format, target_title) VALUES ("+s.placeholders(6)+")",
			media.AniListID, relation.RelationType, relation.TargetAniListID, relation.TargetType, relation.TargetFormat, relation.TargetTitle,
		); err != nil {
			return fmt.Errorf("save anime relations: %w", err)
		}
	}
	for _, set := range media.TitleSets {
		if err := s.replaceTitlesTx(ctx, tx, set); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *AnimeStore) replaceTitlesTx(ctx context.Context, tx *sql.Tx, set AnimeTitleSet) error {
	if set.Provider == "" || set.ExternalID == "" {
		return fmt.Errorf("save anime titles: provider and external id are required")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM anime_titles WHERE provider = "+s.p(1)+" AND external_id = "+s.p(2), set.Provider, set.ExternalID); err != nil {
		return fmt.Errorf("save anime titles: %w", err)
	}
	seen := make(map[string]bool, len(set.Titles))
	ordinal := 0
	for _, title := range set.Titles {
		if title.Title == "" || seen[title.Title] {
			continue
		}
		seen[title.Title] = true
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO anime_titles (provider, external_id, ordinal, title, language, kind, clean_title, fold_title) VALUES ("+s.placeholders(8)+")",
			set.Provider, set.ExternalID, ordinal, title.Title, title.Language, title.Kind, item.CleanName(title.Title), anime.FoldTitle(title.Title),
		); err != nil {
			return fmt.Errorf("save anime titles: %w", err)
		}
		ordinal++
	}
	return nil
}

// ReplaceSeasonMap replaces the stored season map of one series in one
// numbering (AnimeNumberingTVDB with a TVDB id, AnimeNumberingTMDb with a
// TMDb TV id).
func (s *AnimeStore) ReplaceSeasonMap(ctx context.Context, numbering string, seriesID int, seasons map[int][]AnimeSegment) (err error) {
	if seriesID <= 0 || numbering == "" {
		return fmt.Errorf("replace anime season map: numbering and series id are required")
	}
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	if _, err := tx.ExecContext(ctx, "DELETE FROM anime_season_map WHERE numbering = "+s.p(1)+" AND series_id = "+s.p(2), numbering, seriesID); err != nil {
		return fmt.Errorf("replace anime season map: %w", err)
	}
	for season, segments := range seasons {
		for ordinal, segment := range segments {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO anime_season_map (numbering, series_id, season_number, ordinal, anidb_id, anilist_id, mal_id, episode_offset, name) VALUES ("+s.placeholders(9)+")",
				numbering, seriesID, season, ordinal, segment.AniDBID, nullPositive(segment.AniListID), nullPositive(segment.MALID), segment.EpisodeOffset, segment.Name,
			); err != nil {
				return fmt.Errorf("replace anime season map: %w", err)
			}
		}
	}
	return tx.Commit()
}

// SeasonMap returns the stored season map of one series in one numbering.
func (s *AnimeStore) SeasonMap(ctx context.Context, numbering string, seriesID int) (result map[int][]AnimeSegment, err error) {
	rows, err := s.stores.Reader().QueryContext(ctx,
		"SELECT season_number, anidb_id, anilist_id, mal_id, episode_offset, name FROM anime_season_map WHERE numbering = "+s.p(1)+" AND series_id = "+s.p(2)+" ORDER BY season_number ASC, ordinal ASC",
		numbering, seriesID)
	if err != nil {
		return nil, err
	}
	defer func() { err = closeRows(rows, err) }()
	result = make(map[int][]AnimeSegment)
	for rows.Next() {
		var season int
		var segment AnimeSegment
		var anilist, mal sql.NullInt64
		if err := rows.Scan(&season, &segment.AniDBID, &anilist, &mal, &segment.EpisodeOffset, &segment.Name); err != nil {
			return nil, err
		}
		segment.AniListID, segment.MALID = int(anilist.Int64), int(mal.Int64)
		result[season] = append(result[season], segment)
	}
	return result, rows.Err()
}

// Media returns one stored AniList media record with its relations.
func (s *AnimeStore) Media(ctx context.Context, anilistID int) (media AnimeMedia, err error) {
	d := s.stores.Dialect()
	var anidb, mal, seasonYear, episodes sql.NullInt64
	var fetched any
	if err := s.stores.Reader().QueryRowContext(ctx,
		"SELECT anilist_id, anidb_id, mal_id, format, status, season, season_year, episodes, title, date_fetched FROM anime_media WHERE anilist_id = "+s.p(1),
		anilistID,
	).Scan(&media.AniListID, &anidb, &mal, &media.Format, &media.Status, &media.Season, &seasonYear, &episodes, &media.Title, &fetched); err != nil {
		return AnimeMedia{}, err
	}
	media.AniDBID, media.MALID = int(anidb.Int64), int(mal.Int64)
	media.SeasonYear, media.Episodes = int(seasonYear.Int64), int(episodes.Int64)
	if media.DateFetched, err = d.ScanTime(fetched); err != nil {
		return AnimeMedia{}, err
	}
	rows, err := s.stores.Reader().QueryContext(ctx,
		"SELECT relation_type, target_anilist_id, target_type, target_format, target_title FROM anime_relations WHERE anilist_id = "+s.p(1)+" ORDER BY relation_type ASC, target_anilist_id ASC",
		anilistID)
	if err != nil {
		return AnimeMedia{}, err
	}
	defer func() { err = closeRows(rows, err) }()
	for rows.Next() {
		var relation AnimeRelation
		if err := rows.Scan(&relation.RelationType, &relation.TargetAniListID, &relation.TargetType, &relation.TargetFormat, &relation.TargetTitle); err != nil {
			return AnimeMedia{}, err
		}
		media.Relations = append(media.Relations, relation)
	}
	return media, rows.Err()
}

// Titles returns the stored titles of one external id in stored order.
func (s *AnimeStore) Titles(ctx context.Context, provider, externalID string) (titles []AnimeTitle, err error) {
	rows, err := s.stores.Reader().QueryContext(ctx,
		"SELECT title, language, kind FROM anime_titles WHERE provider = "+s.p(1)+" AND external_id = "+s.p(2)+" ORDER BY ordinal ASC",
		provider, externalID)
	if err != nil {
		return nil, err
	}
	defer func() { err = closeRows(rows, err) }()
	for rows.Next() {
		var title AnimeTitle
		if err := rows.Scan(&title.Title, &title.Language, &title.Kind); err != nil {
			return nil, err
		}
		titles = append(titles, title)
	}
	return titles, rows.Err()
}

func nullPositive(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}
