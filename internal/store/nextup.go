// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// NextUpQuery is the dedicated TV next-up request. Its episode selection is
// distinct from a public ItemQuery: it groups direct watch history by series,
// then selects the next episode for each grouped series.
type NextUpQuery struct {
	User                   *User
	UserID                 dto.UUID
	SeriesID               *dto.UUID
	ParentID               *dto.UUID
	StartIndex             *int
	Limit                  *int
	NextUpDateCutoff       *time.Time
	EnableTotalRecordCount bool
	EnableResumable        bool
	EnableRewatching       bool
	IncludeSpecials        bool
	ExcludedTopParentIDs   []dto.UUID
}

// NextUpResult is the post-selection page returned by the batched next-up
// store operation. TotalRecordCount is calculated before paging when enabled.
type NextUpResult struct {
	Items            []item.Item
	TotalRecordCount int
}

type nextUpPlayed struct {
	id         dto.UUID
	lastPlayed *time.Time
}

type nextUpCandidate struct {
	item       item.Item
	lastPlayed time.Time
}

// NextUp implements NextUpService.GetNextUpEpisodesBatch/GetNextUpBatched
// against the normalized store. It deliberately uses direct user-data rows,
// matching Jellyfin's service rather than ItemQuery's shared-key predicates.
// The store reads grouped series keys, played rows, unplayed rows, and resume
// roots in bounded set queries; it never executes one query per series.
func (s *ItemStore) NextUp(ctx context.Context, query NextUpQuery) (result NextUpResult, err error) {
	if query.UserID.IsNil() {
		return NextUpResult{}, fmt.Errorf("next up: user ID is required")
	}
	if query.StartIndex != nil && *query.StartIndex < 0 {
		return NextUpResult{}, fmt.Errorf("next up: start index must not be negative")
	}
	if query.Limit != nil && *query.Limit < 0 {
		return NextUpResult{}, fmt.Errorf("next up: limit must not be negative")
	}

	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return NextUpResult{}, err
	}
	defer rollbackOnReturn(tx, &err)

	keys, err := s.nextUpSeriesKeys(ctx, tx, query)
	if err != nil {
		return NextUpResult{}, err
	}
	if len(keys) == 0 {
		if err := tx.Commit(); err != nil {
			return NextUpResult{}, err
		}
		return NextUpResult{Items: make([]item.Item, 0)}, nil
	}

	played, err := s.nextUpPlayed(ctx, tx, query, keys)
	if err != nil {
		return NextUpResult{}, err
	}
	unplayed, err := s.nextUpUnplayed(ctx, tx, query, keys)
	if err != nil {
		return NextUpResult{}, err
	}
	specialIDs := []dto.UUID(nil)
	specialPlayed := map[dto.UUID]bool(nil)
	specialSeries := map[dto.UUID]string(nil)
	if query.IncludeSpecials {
		specialIDs, specialPlayed, specialSeries, err = s.nextUpSpecials(ctx, tx, query, keys)
		if err != nil {
			return NextUpResult{}, err
		}
	}
	resumed, err := s.nextUpResumedRoots(ctx, tx, query, keys)
	if err != nil {
		return NextUpResult{}, err
	}

	ids := make([]dto.UUID, 0, len(played)+len(unplayed)+len(specialIDs))
	seen := make(map[dto.UUID]struct{}, len(played)+len(unplayed)+len(specialIDs))
	for _, value := range played {
		if _, exists := seen[value.id]; !exists {
			seen[value.id] = struct{}{}
			ids = append(ids, value.id)
		}
	}
	for _, id := range unplayed {
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	for _, id := range specialIDs {
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	// Selection reads only item columns. Satellites load for the returned
	// page, not for every played and unplayed candidate of every series.
	items, err := s.nextUpItemRows(ctx, tx, query, ids)
	if err != nil {
		return NextUpResult{}, err
	}
	byID := make(map[dto.UUID]item.Item, len(items))
	for _, value := range items {
		if nextUpExcludedRoot(value, query.ExcludedTopParentIDs) {
			continue
		}
		byID[value.ID] = value
	}

	playedBySeries := make(map[string][]nextUpPlayed, len(keys))
	playedDate := make(map[dto.UUID]time.Time, len(played))
	for _, value := range played {
		it, found := byID[value.id]
		if !found || it.SeriesPresentationUniqueKey == nil {
			continue
		}
		key := *it.SeriesPresentationUniqueKey
		playedBySeries[key] = append(playedBySeries[key], value)
		if value.lastPlayed == nil {
			continue
		}
		if previous, exists := playedDate[value.id]; !exists || value.lastPlayed.After(previous) {
			playedDate[value.id] = *value.lastPlayed
		}
	}
	unplayedBySeries := make(map[string][]item.Item, len(keys))
	for _, id := range unplayed {
		it, found := byID[id]
		if found && it.SeriesPresentationUniqueKey != nil {
			key := *it.SeriesPresentationUniqueKey
			unplayedBySeries[key] = append(unplayedBySeries[key], it)
		}
	}
	specialsBySeries := make(map[string][]item.Item, len(keys))
	for _, id := range specialIDs {
		if value, found := byID[id]; found {
			specialsBySeries[specialSeries[id]] = append(specialsBySeries[specialSeries[id]], value)
		}
	}

	candidates := make([]nextUpCandidate, 0, len(keys)*2)
	for _, key := range keys {
		lastWatched := latestAiredNextUp(playedBySeries[key], byID)
		next := nextUnplayedAfter(unplayedBySeries[key], lastWatched)
		if query.IncludeSpecials {
			next = nextUpPlacedSpecial(lastWatched, next, specialsBySeries[key], specialPlayed, false)
		}
		if next != nil && (query.EnableResumable || !resumed[nextUpRoot(*next)]) {
			date := time.Time{}
			if lastWatched != nil {
				date = playedDate[lastWatched.ID]
			}
			candidates = append(candidates, nextUpCandidate{item: *next, lastPlayed: date})
		}
		if !query.EnableRewatching {
			continue
		}
		lastByDate := latestDateNextUp(playedBySeries[key], playedDate, byID)
		rewatch := nextPlayedAfter(playedBySeries[key], lastByDate, byID)
		if query.IncludeSpecials {
			rewatch = nextUpPlacedSpecial(lastByDate, rewatch, specialsBySeries[key], specialPlayed, true)
		}
		if rewatch != nil && !resumed[nextUpRoot(*rewatch)] {
			date := time.Time{}
			if lastByDate != nil {
				date = playedDate[lastByDate.ID]
			}
			candidates = append(candidates, nextUpCandidate{item: *rewatch, lastPlayed: date})
		}
	}

	sort.SliceStable(candidates, func(left, right int) bool {
		return candidates[left].lastPlayed.After(candidates[right].lastPlayed)
	})
	if query.EnableTotalRecordCount {
		result.TotalRecordCount = len(candidates)
	}
	start := 0
	if query.StartIndex != nil {
		start = *query.StartIndex
	}
	if start < len(candidates) {
		candidates = candidates[start:]
	} else {
		candidates = nil
	}
	if query.Limit != nil && *query.Limit > 0 && len(candidates) > *query.Limit {
		candidates = candidates[:*query.Limit]
	}
	result.Items = make([]item.Item, 0, len(candidates))
	for _, candidate := range candidates {
		result.Items = append(result.Items, candidate.item)
	}
	if err := s.loadPageSatellites(ctx, tx, result.Items); err != nil {
		return NextUpResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return NextUpResult{}, err
	}
	return result, nil
}

func nextUpExcludedRoot(value item.Item, roots []dto.UUID) bool {
	for _, root := range roots {
		if value.ID == root || (value.ParentID != nil && *value.ParentID == root) || (value.TopParentID != nil && *value.TopParentID == root) {
			return true
		}
	}
	return false
}

func (s *ItemStore) nextUpSeriesKeys(ctx context.Context, tx *sql.Tx, query NextUpQuery) ([]string, error) {
	if query.SeriesID != nil {
		args := []any{s.uuidArg(*query.SeriesID), item.KindSeries.String()}
		visible, args := itemQueryAccessClauseArguments(s.stores.Dialect(), ItemQuery{User: query.User}, "items", args)
		where := "items.id = " + s.ph(1) + " AND items.kind = " + s.ph(2)
		if visible != "" {
			where += " AND " + visible
		}
		row := tx.QueryRowContext(ctx, "SELECT presentation_unique_key FROM items WHERE "+where, args...)
		var key sql.NullString
		if err := row.Scan(&key); errorsIsNoRows(err) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		if !key.Valid || key.String == "" {
			return nil, nil
		}
		return []string{key.String}, nil
	}

	args := []any{s.uuidArg(query.UserID), item.KindEpisode.String()}
	where := []string{"user_data.user_id = " + s.ph(1), "items.kind = " + s.ph(2), "user_data.played = " + s.stores.Dialect().Boolean(true), "items.series_presentation_unique_key IS NOT NULL"}
	if query.ParentID != nil {
		args = append(args, s.uuidArg(*query.ParentID))
		parent := s.ph(len(args))
		where = append(where, "(items.parent_id = "+parent+" OR items.top_parent_id = "+parent+" OR EXISTS (SELECT 1 FROM ancestors AS next_up_parent WHERE next_up_parent.item_id = items.id AND next_up_parent.ancestor_id = "+parent+"))")
	}
	having := ""
	if query.NextUpDateCutoff != nil {
		args = append(args, s.stores.Dialect().TimeValue(query.NextUpDateCutoff.UTC()))
		having = " HAVING MAX(user_data.last_played) >= " + s.ph(len(args))
	}
	visible, args := itemQueryAccessClauseArguments(s.stores.Dialect(), ItemQuery{User: query.User}, "items", args)
	if visible != "" {
		where = append(where, visible)
	}
	sqlText := "SELECT items.series_presentation_unique_key FROM items JOIN user_data ON user_data.item_id = items.id WHERE " + strings.Join(where, " AND ") + " GROUP BY items.series_presentation_unique_key" + having + " ORDER BY CASE WHEN MAX(user_data.last_played) IS NULL THEN 1 ELSE 0 END, MAX(user_data.last_played) DESC, items.series_presentation_unique_key"
	return nextUpStrings(ctx, tx, sqlText, args)
}

func (s *ItemStore) nextUpPlayed(ctx context.Context, tx *sql.Tx, query NextUpQuery, keys []string) ([]nextUpPlayed, error) {
	batchSize, err := s.nextUpKeyBatchSize(query, 2) // user_id, episode kind
	if err != nil {
		return nil, err
	}
	out := make([]nextUpPlayed, 0, len(keys))
	seen := make(map[dto.UUID]struct{}, len(keys))
	for start := 0; start < len(keys); start += batchSize {
		end := min(start+batchSize, len(keys))
		batch, err := s.nextUpPlayedBatch(ctx, tx, query, keys[start:end])
		if err != nil {
			return nil, err
		}
		for _, value := range batch {
			if _, found := seen[value.id]; found {
				continue
			}
			seen[value.id] = struct{}{}
			out = append(out, value)
		}
	}
	return out, nil
}

func (s *ItemStore) nextUpPlayedBatch(ctx context.Context, tx *sql.Tx, query NextUpQuery, keys []string) (out []nextUpPlayed, err error) {
	args := []any{s.uuidArg(query.UserID), item.KindEpisode.String()}
	marks := nextUpStringArguments(s, &args, keys)
	visible, args := itemQueryAccessClauseArguments(s.stores.Dialect(), ItemQuery{User: query.User}, "items", args)
	where := []string{"user_data.user_id = " + s.ph(1), "items.kind = " + s.ph(2), "items.parent_index_number <> 0", "user_data.played = " + s.stores.Dialect().Boolean(true), "items.series_presentation_unique_key IN (" + marks + ")"}
	if visible != "" {
		where = append(where, visible)
	}
	rows, err := tx.QueryContext(ctx, "SELECT items.id, MAX(user_data.last_played) FROM items JOIN user_data ON user_data.item_id = items.id WHERE "+strings.Join(where, " AND ")+" GROUP BY items.id", args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			out = nil
			err = closeErr
		}
	}()
	out = make([]nextUpPlayed, 0)
	for rows.Next() {
		var raw, last any
		if err := rows.Scan(&raw, &last); err != nil {
			return nil, err
		}
		id, err := scanUUID(raw)
		if err != nil {
			return nil, err
		}
		var lastPlayed *time.Time
		if !isNil(last) {
			value, err := s.stores.Dialect().ScanTime(last)
			if err != nil {
				return nil, err
			}
			lastPlayed = &value
		}
		out = append(out, nextUpPlayed{id: id, lastPlayed: lastPlayed})
	}
	return out, rows.Err()
}

func (s *ItemStore) nextUpUnplayed(ctx context.Context, tx *sql.Tx, query NextUpQuery, keys []string) ([]dto.UUID, error) {
	batchSize, err := s.nextUpKeyBatchSize(query, 2) // episode kind, user_id
	if err != nil {
		return nil, err
	}
	out := make([]dto.UUID, 0, len(keys))
	seen := make(map[dto.UUID]struct{}, len(keys))
	for start := 0; start < len(keys); start += batchSize {
		end := min(start+batchSize, len(keys))
		args := []any{item.KindEpisode.String()}
		marks := nextUpStringArguments(s, &args, keys[start:end])
		args = append(args, s.uuidArg(query.UserID))
		user := s.ph(len(args))
		visible, args := itemQueryAccessClauseArguments(s.stores.Dialect(), ItemQuery{User: query.User}, "items", args)
		where := []string{"items.kind = " + s.ph(1), "items.parent_index_number <> 0", "items.is_virtual_item = " + s.stores.Dialect().Boolean(false), "items.series_presentation_unique_key IN (" + marks + ")", "NOT EXISTS (SELECT 1 FROM user_data WHERE user_data.user_id = " + user + " AND user_data.item_id = items.id AND user_data.played = " + s.stores.Dialect().Boolean(true) + ")"}
		if visible != "" {
			where = append(where, visible)
		}
		batch, err := nextUpIDs(ctx, tx, s.stores.Dialect(), "SELECT items.id FROM items WHERE "+strings.Join(where, " AND "), args)
		if err != nil {
			return nil, err
		}
		for _, id := range batch {
			if _, found := seen[id]; found {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *ItemStore) nextUpSpecials(ctx context.Context, tx *sql.Tx, query NextUpQuery, keys []string) (ids []dto.UUID, played map[dto.UUID]bool, series map[dto.UUID]string, err error) {
	batchSize, err := s.nextUpKeyBatchSize(query, 1) // episode kind
	if err != nil {
		return nil, nil, nil, err
	}
	ids = make([]dto.UUID, 0, len(keys))
	played = make(map[dto.UUID]bool)
	series = make(map[dto.UUID]string, len(keys))
	seen := make(map[dto.UUID]struct{}, len(keys))
	for start := 0; start < len(keys); start += batchSize {
		end := min(start+batchSize, len(keys))
		batchIDs, batchSeries, err := s.nextUpSpecialIDs(ctx, tx, query, keys[start:end])
		if err != nil {
			return nil, nil, nil, err
		}
		for _, id := range batchIDs {
			if _, found := seen[id]; found {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
			series[id] = batchSeries[id]
		}
	}
	if len(ids) == 0 {
		return ids, played, series, nil
	}
	playBatchSize, err := nextUpIDBatchSize(1) // user_id
	if err != nil {
		return nil, nil, nil, err
	}
	for start := 0; start < len(ids); start += playBatchSize {
		end := min(start+playBatchSize, len(ids))
		playArgs := []any{s.uuidArg(query.UserID)}
		playMarks := nextUpUUIDArguments(s, &playArgs, ids[start:end])
		playedRows, err := tx.QueryContext(ctx, "SELECT item_id FROM user_data WHERE user_id = "+s.ph(1)+" AND played = "+s.stores.Dialect().Boolean(true)+" AND item_id IN ("+playMarks+")", playArgs...)
		if err != nil {
			return nil, nil, nil, err
		}
		for playedRows.Next() {
			var raw any
			if err := playedRows.Scan(&raw); err != nil {
				return nil, nil, nil, closeRows(playedRows, err)
			}
			id, err := scanUUID(raw)
			if err != nil {
				return nil, nil, nil, closeRows(playedRows, err)
			}
			played[id] = true
		}
		if err := closeRows(playedRows, playedRows.Err()); err != nil {
			return nil, nil, nil, err
		}
	}
	return ids, played, series, nil
}

func (s *ItemStore) nextUpSpecialIDs(ctx context.Context, tx *sql.Tx, query NextUpQuery, keys []string) (ids []dto.UUID, series map[dto.UUID]string, err error) {
	args := []any{item.KindEpisode.String()}
	marks := nextUpStringArguments(s, &args, keys)
	visible, args := itemQueryAccessClauseArguments(s.stores.Dialect(), ItemQuery{User: query.User}, "items", args)
	where := []string{"items.kind = " + s.ph(1), "items.parent_index_number = 0", "items.is_virtual_item = " + s.stores.Dialect().Boolean(false), "items.series_presentation_unique_key IN (" + marks + ")"}
	if visible != "" {
		where = append(where, visible)
	}
	rows, err := tx.QueryContext(ctx, "SELECT items.id, items.series_presentation_unique_key FROM items WHERE "+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			ids = nil
			series = nil
			err = closeErr
		}
	}()
	ids = make([]dto.UUID, 0)
	series = make(map[dto.UUID]string)
	for rows.Next() {
		var raw any
		var key string
		if err := rows.Scan(&raw, &key); err != nil {
			return nil, nil, err
		}
		id, err := scanUUID(raw)
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		series[id] = key
	}
	return ids, series, rows.Err()
}

func (s *ItemStore) nextUpResumedRoots(ctx context.Context, tx *sql.Tx, query NextUpQuery, keys []string) (map[dto.UUID]bool, error) {
	batchSize, err := s.nextUpKeyBatchSize(query, 1) // user_id
	if err != nil {
		return nil, err
	}
	out := make(map[dto.UUID]bool)
	for start := 0; start < len(keys); start += batchSize {
		end := min(start+batchSize, len(keys))
		args := []any{s.uuidArg(query.UserID)}
		marks := nextUpStringArguments(s, &args, keys[start:end])
		visible, args := itemQueryAccessClauseArguments(s.stores.Dialect(), ItemQuery{User: query.User}, "items", args)
		where := []string{"user_data.user_id = " + s.ph(1), "user_data.position_ticks > 0", "items.series_presentation_unique_key IN (" + marks + ")"}
		if visible != "" {
			where = append(where, visible)
		}
		ids, err := nextUpIDs(ctx, tx, s.stores.Dialect(), "SELECT DISTINCT COALESCE(items.primary_version_id, items.id) FROM items JOIN user_data ON user_data.item_id = items.id WHERE "+strings.Join(where, " AND "), args)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			out[id] = true
		}
	}
	return out, nil
}

// nextUpItems loads the visible items of ids, with their satellites, in the
// order of ids.
func (s *ItemStore) nextUpItems(ctx context.Context, tx *sql.Tx, query NextUpQuery, ids []dto.UUID) ([]item.Item, error) {
	out, err := s.nextUpItemRows(ctx, tx, query, ids)
	if err != nil {
		return nil, err
	}
	if err := s.loadPageSatellites(ctx, tx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// nextUpItemRows loads the visible items of ids in the order of ids, without
// satellites.
func (s *ItemStore) nextUpItemRows(ctx context.Context, tx *sql.Tx, query NextUpQuery, ids []dto.UUID) ([]item.Item, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	_, accessArgs := itemQueryAccessClauseArguments(s.stores.Dialect(), ItemQuery{User: query.User}, "items", nil)
	batchSize, err := nextUpIDBatchSize(len(accessArgs))
	if err != nil {
		return nil, err
	}
	orderedIDs := make([]dto.UUID, 0, len(ids))
	seen := make(map[dto.UUID]struct{}, len(ids))
	for _, id := range ids {
		if _, found := seen[id]; found {
			continue
		}
		seen[id] = struct{}{}
		orderedIDs = append(orderedIDs, id)
	}
	byID := make(map[dto.UUID]item.Item, len(orderedIDs))
	for start := 0; start < len(orderedIDs); start += batchSize {
		end := start + batchSize
		if end > len(orderedIDs) {
			end = len(orderedIDs)
		}
		batch, err := s.nextUpItemsBatch(ctx, tx, query, orderedIDs[start:end])
		if err != nil {
			return nil, err
		}
		for _, value := range batch {
			if _, found := byID[value.ID]; !found {
				byID[value.ID] = value
			}
		}
	}
	out := make([]item.Item, 0, len(byID))
	for _, id := range orderedIDs {
		if value, found := byID[id]; found {
			out = append(out, value)
		}
	}
	return out, nil
}

func (s *ItemStore) nextUpItemsBatch(ctx context.Context, tx *sql.Tx, query NextUpQuery, ids []dto.UUID) ([]item.Item, error) {
	args := make([]any, 0, len(ids))
	marks := nextUpUUIDArguments(s, &args, ids)
	visible, args := itemQueryAccessClauseArguments(s.stores.Dialect(), ItemQuery{User: query.User}, "items", args)
	where := "items.id IN (" + marks + ")"
	if visible != "" {
		where += " AND " + visible
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(itemColumns, ", ")+" FROM items WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	out := make([]item.Item, 0, len(ids))
	for rows.Next() {
		value, err := scanItem(rows, s.stores.Dialect())
		if err != nil {
			return nil, closeRows(rows, err)
		}
		out = append(out, value)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	return out, nil
}

func latestAiredNextUp(values []nextUpPlayed, items map[dto.UUID]item.Item) *item.Item {
	var selected *item.Item
	for _, value := range values {
		candidate, found := items[value.id]
		if !found || (selected != nil && !nextUpAfter(candidate, *selected)) {
			continue
		}
		copy := candidate
		selected = &copy
	}
	return selected
}

func latestDateNextUp(values []nextUpPlayed, dates map[dto.UUID]time.Time, items map[dto.UUID]item.Item) *item.Item {
	if len(values) == 0 {
		return nil
	}
	selected := values[0]
	for _, value := range values[1:] {
		if dates[value.id].After(dates[selected.id]) {
			selected = value
		}
	}
	value, found := items[selected.id]
	if !found {
		return nil
	}
	return &value
}

func nextUnplayedAfter(values []item.Item, after *item.Item) *item.Item {
	var selected *item.Item
	for index := range values {
		if after != nil && !nextUpAfter(values[index], *after) {
			continue
		}
		if selected == nil || nextUpAfter(*selected, values[index]) {
			copy := values[index]
			selected = &copy
		}
	}
	return selected
}

func nextPlayedAfter(values []nextUpPlayed, after *item.Item, items map[dto.UUID]item.Item) *item.Item {
	var selected *item.Item
	for _, value := range values {
		candidate, found := items[value.id]
		if !found || (after != nil && !nextUpAfter(candidate, *after)) || (selected != nil && !nextUpAfter(*selected, candidate)) {
			continue
		}
		copy := candidate
		selected = &copy
	}
	return selected
}

func nextUpPlacedSpecial(last, next *item.Item, specials []item.Item, played map[dto.UUID]bool, includePlayed bool) *item.Item {
	values := make([]item.Item, 0, len(specials)+2)
	for _, special := range specials {
		if special.AirsBeforeSeasonNumber != nil || special.AirsAfterSeasonNumber != nil {
			values = append(values, special)
		}
	}
	if last != nil {
		values = append(values, *last)
	}
	if next != nil {
		values = append(values, *next)
	}
	if len(values) == 0 {
		return nil
	}
	sort.SliceStable(values, func(left, right int) bool { return nextUpAiredBefore(values[left], values[right]) })
	start := 0
	if last != nil {
		for index := range values {
			if values[index].ID == last.ID {
				start = index + 1
				break
			}
		}
	}
	for _, value := range values[start:] {
		if includePlayed || !played[value.ID] {
			copy := value
			return &copy
		}
	}
	return nil
}

func nextUpAiredBefore(left, right item.Item) bool {
	leftSpecial, rightSpecial := nextUpNumber(left.ParentIndexNumber) == 0, nextUpNumber(right.ParentIndexNumber) == 0
	if leftSpecial && rightSpecial {
		leftValue, rightValue := nextUpSpecialOrder(left), nextUpSpecialOrder(right)
		if leftValue != rightValue {
			return leftValue < rightValue
		}
		return left.ID.Dashed() < right.ID.Dashed()
	}
	if !leftSpecial && !rightSpecial {
		leftSeason, rightSeason := nextUpNumber(left.ParentIndexNumber), nextUpNumber(right.ParentIndexNumber)
		if leftSeason != rightSeason {
			return leftSeason < rightSeason
		}
		leftEpisode, rightEpisode := nextUpNumber(left.IndexNumber), nextUpNumber(right.IndexNumber)
		if leftEpisode != rightEpisode {
			return leftEpisode < rightEpisode
		}
		return left.ID.Dashed() < right.ID.Dashed()
	}
	if leftSpecial {
		return !nextUpAiredBefore(right, left)
	}
	specialSeason := nextUpNumber(right.AirsAfterSeasonNumber)
	if right.AirsAfterSeasonNumber == nil {
		specialSeason = nextUpNumber(right.AirsBeforeSeasonNumber)
	}
	season := nextUpNumber(left.ParentIndexNumber)
	if season != specialSeason {
		return season < specialSeason
	}
	if right.AirsAfterSeasonNumber != nil {
		return true
	}
	if right.AirsBeforeEpisodeNumber == nil {
		return false
	}
	episode := nextUpNumber(left.IndexNumber)
	if episode == *right.AirsBeforeEpisodeNumber {
		return false
	}
	return episode < *right.AirsBeforeEpisodeNumber
}

func nextUpSpecialOrder(value item.Item) int64 {
	season := value.AirsAfterSeasonNumber
	if season == nil {
		season = value.AirsBeforeSeasonNumber
	}
	order := int64(nextUpNumber(season)) * 1_000_000_000
	if value.AirsAfterSeasonNumber != nil {
		order += 1_000_000
	}
	order += int64(nextUpNumber(value.AirsBeforeEpisodeNumber)) * 1_000
	return order + int64(nextUpNumber(value.IndexNumber))
}

func nextUpAfter(left, right item.Item) bool {
	leftSeason, rightSeason := nextUpNumber(left.ParentIndexNumber), nextUpNumber(right.ParentIndexNumber)
	if leftSeason != rightSeason {
		return leftSeason > rightSeason
	}
	leftEpisode, rightEpisode := nextUpNumber(left.IndexNumber), nextUpNumber(right.IndexNumber)
	return leftEpisode > rightEpisode
}

func nextUpNumber(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

func nextUpRoot(value item.Item) dto.UUID {
	if value.PrimaryVersionID != nil {
		return *value.PrimaryVersionID
	}
	return value.ID
}

func nextUpIDBatchSize(fixedArgs int) (int, error) {
	size := pageSatelliteBatchSize - fixedArgs
	if size <= 0 {
		return 0, fmt.Errorf("next up: %d fixed query arguments exceed the %d-ID batch limit", fixedArgs, pageSatelliteBatchSize)
	}
	return size, nil
}

func (s *ItemStore) nextUpKeyBatchSize(query NextUpQuery, fixedArgs int) (int, error) {
	_, accessArgs := itemQueryAccessClauseArguments(s.stores.Dialect(), ItemQuery{User: query.User}, "items", nil)
	return nextUpIDBatchSize(fixedArgs + len(accessArgs))
}

func nextUpUUIDArguments(s *ItemStore, args *[]any, values []dto.UUID) string {
	marks := make([]string, 0, len(values))
	for _, value := range values {
		*args = append(*args, s.uuidArg(value))
		marks = append(marks, s.ph(len(*args)))
	}
	return strings.Join(marks, ", ")
}

func nextUpStringArguments(s *ItemStore, args *[]any, values []string) string {
	marks := make([]string, 0, len(values))
	for _, value := range values {
		*args = append(*args, value)
		marks = append(marks, s.ph(len(*args)))
	}
	return strings.Join(marks, ", ")
}

func nextUpStrings(ctx context.Context, tx *sql.Tx, sqlText string, args []any) (out []string, err error) {
	rows, err := tx.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			out = nil
			err = closeErr
		}
	}()
	out = make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func nextUpIDs(ctx context.Context, tx *sql.Tx, dialect Dialect, sqlText string, args []any) (out []dto.UUID, err error) {
	rows, err := tx.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			out = nil
			err = closeErr
		}
	}()
	out = make([]dto.UUID, 0)
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := scanUUID(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func errorsIsNoRows(err error) bool { return err == sql.ErrNoRows }
