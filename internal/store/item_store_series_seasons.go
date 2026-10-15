// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// SeriesSeasonEpisode is the season part of one Episode under a Series.
type SeriesSeasonEpisode struct {
	ID                dto.UUID
	ParentIndexNumber *int
	SeasonID          *dto.UUID
	SeasonName        *string
	// InSeasonFolder is true when a Season is an ancestor of the episode.
	// The derived season_id of such an episode is that folder.
	InSeasonFolder bool
}

// SeriesSeasons is the stored season state of one Series: its Season
// children and every Episode in its subtree.
type SeriesSeasons struct {
	Seasons  []item.Item
	Episodes []SeriesSeasonEpisode
}

// SeriesSeasonAssignment links one episode outside a season folder to a
// season, as upstream CreateSeasonsAsync sets Episode.SeasonId and
// Episode.SeasonName.
type SeriesSeasonAssignment struct {
	EpisodeID  dto.UUID
	SeasonID   dto.UUID
	SeasonName *string
}

// SeriesSeasonPlan is one series' virtual-season change: the pathless
// seasons to create, the pathless seasons to delete, and the episode links.
type SeriesSeasonPlan struct {
	Create []item.Item
	Delete []dto.UUID
	Assign []SeriesSeasonAssignment
}

// Empty reports a plan that changes nothing.
func (p SeriesSeasonPlan) Empty() bool {
	return len(p.Create) == 0 && len(p.Delete) == 0 && len(p.Assign) == 0
}

// SeriesSeasons reads the Season children of seriesID and the Episodes in
// its subtree. Seasons carry no satellites.
func (s *ItemStore) SeriesSeasons(ctx context.Context, seriesID dto.UUID) (result SeriesSeasons, err error) {
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return SeriesSeasons{}, err
	}
	defer rollbackOnReturn(tx, &err)
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(itemColumns, ", ")+" FROM items WHERE parent_id = "+s.ph(1)+" AND kind = "+s.ph(2), s.uuidArg(seriesID), item.KindSeason.String())
	if err != nil {
		return SeriesSeasons{}, err
	}
	for rows.Next() {
		value, scanErr := scanItem(rows, s.stores.Dialect())
		if scanErr != nil {
			return SeriesSeasons{}, closeRows(rows, scanErr)
		}
		result.Seasons = append(result.Seasons, value)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return SeriesSeasons{}, err
	}
	query := "SELECT episode.id, episode.parent_index_number, episode.season_id, episode.season_name, " +
		"CASE WHEN EXISTS (SELECT 1 FROM ancestors AS up JOIN items AS folder ON folder.id = up.ancestor_id WHERE up.item_id = episode.id AND folder.kind = " + s.ph(1) + ") THEN 1 ELSE 0 END " +
		"FROM ancestors AS down JOIN items AS episode ON episode.id = down.item_id WHERE down.ancestor_id = " + s.ph(2) + " AND episode.kind = " + s.ph(3)
	rows, err = tx.QueryContext(ctx, query, item.KindSeason.String(), s.uuidArg(seriesID), item.KindEpisode.String())
	if err != nil {
		return SeriesSeasons{}, err
	}
	for rows.Next() {
		var rawID, parentIndex, rawSeason, seasonName, inSeason any
		if err := rows.Scan(&rawID, &parentIndex, &rawSeason, &seasonName, &inSeason); err != nil {
			return SeriesSeasons{}, closeRows(rows, err)
		}
		episode := SeriesSeasonEpisode{ParentIndexNumber: optionalInt(parentIndex), InSeasonFolder: asBool(inSeason)}
		if episode.ID, err = scanUUID(rawID); err != nil {
			return SeriesSeasons{}, closeRows(rows, err)
		}
		if rawSeason != nil {
			seasonID, scanErr := scanUUID(rawSeason)
			if scanErr != nil {
				return SeriesSeasons{}, closeRows(rows, scanErr)
			}
			episode.SeasonID = &seasonID
		}
		if seasonName != nil {
			name := asString(seasonName)
			episode.SeasonName = &name
		}
		result.Episodes = append(result.Episodes, episode)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return SeriesSeasons{}, err
	}
	return result, tx.Commit()
}

// ApplySeriesSeasons writes plan for seriesID in one transaction. It accepts
// only pathless Season children of the series for creation and deletion, and
// only episodes outside a season folder for assignment, so it cannot change
// a season that the folder scan owns.
func (s *ItemStore) ApplySeriesSeasons(ctx context.Context, seriesID dto.UUID, plan SeriesSeasonPlan) (err error) {
	if plan.Empty() {
		return nil
	}
	for _, season := range plan.Create {
		if season.Kind != item.KindSeason || season.Path != nil || season.ParentID == nil || *season.ParentID != seriesID || season.ID.IsNil() {
			return fmt.Errorf("apply series seasons: season %s must be a pathless Season child of %s", season.ID, seriesID)
		}
	}
	tx, err := s.stores.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackOnReturn(tx, &err)
	// The exclusive key on the series excludes every writer inside its
	// subtree, so the episodes and seasons cannot change under this plan.
	scope := derivedLockScope{items: []dto.UUID{seriesID}, parents: []dto.UUID{seriesID}, writes: plan.Create, deleted: plan.Delete}
	for _, season := range plan.Create {
		scope.items = append(scope.items, season.ID)
		scope.replaced = append(scope.replaced, season.ID)
	}
	scope.items = append(scope.items, plan.Delete...)
	if err := s.lockDerivedScope(ctx, tx, func(context.Context, *sql.Tx) (derivedLockScope, error) { return scope, nil }); err != nil {
		return err
	}
	if err := s.requireKind(ctx, tx, seriesID, item.KindSeries, "apply series seasons"); err != nil {
		return err
	}
	if err := s.requirePathlessSeasons(ctx, tx, seriesID, plan.Delete); err != nil {
		return err
	}
	tables := []string{"item_images", "provider_ids", "locked_fields", "trailer_types", "item_value_map", "item_shares", "boxset_library_folders", "people_map", "media_streams", "media_attachments", "chapters", "item_additional_parts", "keyframes", "trickplay_infos", "user_data"}
	if err := s.deleteItemRows(ctx, tx, plan.Delete, tables); err != nil {
		return err
	}
	roots := make([]dto.UUID, 0, len(plan.Create)+len(plan.Assign))
	for _, season := range plan.Create {
		if err := s.validateUpsert(ctx, tx, season); err != nil {
			return err
		}
		if err := s.upsertItem(ctx, tx, season); err != nil {
			return err
		}
		if err := s.replaceSatellites(ctx, tx, season); err != nil {
			return err
		}
		roots = append(roots, season.ID)
	}
	for _, assignment := range plan.Assign {
		var name any
		if assignment.SeasonName != nil {
			name = *assignment.SeasonName
		}
		result, err := tx.ExecContext(ctx, "UPDATE items SET season_id = "+s.ph(1)+", season_name = "+s.ph(2)+" WHERE id = "+s.ph(3)+" AND kind = "+s.ph(4)+
			" AND id IN (SELECT item_id FROM ancestors WHERE ancestor_id = "+s.ph(5)+")"+
			" AND NOT EXISTS (SELECT 1 FROM ancestors AS up JOIN items AS folder ON folder.id = up.ancestor_id WHERE up.item_id = items.id AND folder.kind = "+s.ph(6)+")",
			s.uuidArg(assignment.SeasonID), name, s.uuidArg(assignment.EpisodeID), item.KindEpisode.String(), s.uuidArg(seriesID), item.KindSeason.String())
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil {
			return err
		} else if changed != 1 {
			return fmt.Errorf("apply series seasons: episode %s is not outside a season folder of %s", assignment.EpisodeID, seriesID)
		}
		roots = append(roots, assignment.EpisodeID)
	}
	if err := s.repairDerivedForTx(ctx, tx, roots, plan.Delete); err != nil {
		return err
	}
	return tx.Commit()
}

// requirePathlessSeasons fails unless every id is a pathless Season child of
// seriesID.
func (s *ItemStore) requirePathlessSeasons(ctx context.Context, tx *sql.Tx, seriesID dto.UUID, ids []dto.UUID) error {
	unique := uniqueUUIDs(ids)
	if len(unique) == 0 {
		return nil
	}
	found := 0
	err := s.forUUIDChunks(unique, func(chunk []dto.UUID, marks string, args []any) error {
		args = append(args, s.uuidArg(seriesID), item.KindSeason.String())
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM items WHERE id IN ("+marks+") AND parent_id = "+s.ph(len(args)-1)+" AND kind = "+s.ph(len(args))+" AND path IS NULL", args...).Scan(&count); err != nil {
			return err
		}
		found += count
		return nil
	})
	if err != nil {
		return err
	}
	if found != len(unique) {
		return errors.New("apply series seasons: only pathless Season children of the series can be deleted")
	}
	return nil
}
