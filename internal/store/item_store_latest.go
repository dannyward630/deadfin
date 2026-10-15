// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// latestRecentAdditionWindow is the window in which episodes of one series
// count as added together (BaseItemRepository.GetLatestTvShowItems).
const latestRecentAdditionWindow = 24 * time.Hour

// LatestItems ports BaseItemRepository.GetLatestItemList for a ParentId whose
// collection type is tvshows, movies, or music. It groups the filtered items
// first and applies query.Limit to the groups:
//
//   - tvshows: the Limit series with the newest episodes. Each series returns
//     its Season, its Series, or its newest Episode, by the episodes added in
//     the 24 hours before its newest one.
//   - movies: the newest item of the Limit newest PresentationUniqueKey groups.
//   - music: the Limit newest MusicAlbums that are ancestors of a matching
//     item.
//
// The result is ordered by the group's newest DateCreated, descending. The
// boolean is false, and no rows are read beyond the parent, when the parent
// has another collection type; the caller then uses Query.
func (s *ItemStore) LatestItems(ctx context.Context, query ItemQuery) (_ []item.Item, grouped bool, err error) {
	if query.ParentID == nil || query.ParentID.IsNil() {
		return nil, false, nil
	}
	tx, err := s.stores.Reader().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer rollbackOnReturn(tx, &err)
	parent, err := s.queryParent(ctx, tx, *query.ParentID)
	if err != nil {
		return nil, false, err
	}
	if parent.collectionType == nil {
		return nil, false, tx.Commit()
	}
	collectionType := *parent.collectionType
	if collectionType != item.CollectionTypeTVShows && collectionType != item.CollectionTypeMovies && collectionType != item.CollectionTypeMusic {
		return nil, false, tx.Commit()
	}
	limit := -1
	if query.Limit != nil {
		limit = *query.Limit
	}
	base := query
	base.Limit, base.StartIndex = nil, nil
	resolved, translated, err := s.translateQuery(ctx, tx, base)
	if err != nil {
		return nil, false, err
	}
	var ids []dto.UUID
	switch {
	case collectionType == item.CollectionTypeTVShows:
		ids, err = s.latestTVShowIDs(ctx, tx, translated, limit)
	case collectionType == item.CollectionTypeMovies && limit > 0 && (&latestWindow{}).appliesTo(resolved):
		ids, err = s.latestWindowMovieIDs(ctx, tx, resolved, limit)
	case collectionType == item.CollectionTypeMovies:
		ids, err = s.latestMovieIDs(ctx, tx, translated, limit)
	case limit > 0 && (&latestWindow{albums: true}).appliesTo(resolved):
		ids, err = s.latestWindowAlbumIDs(ctx, tx, resolved, translated, limit)
	default:
		ids, err = s.latestAlbumIDs(ctx, tx, translated, limit)
	}
	if err != nil {
		return nil, false, err
	}
	values, err := s.latestItemsByID(ctx, tx, ids)
	if err != nil {
		return nil, false, err
	}
	return values, true, tx.Commit()
}

// latestSelect returns "SELECT columns" over the filtered rows that also
// match condition, and its arguments. More arguments may follow it. Without a
// transform it reads the filtered relation directly; otherwise it looks the
// filtered ids up in items.
func latestSelect(translated TranslatedItemQuery, columns, condition string) (string, []any) {
	args := append([]any(nil), translated.CountArgs...)
	if translated.FilteredFrom == "" {
		return "SELECT " + columns + " FROM items WHERE id IN (" + translated.IDSQL + ") AND " + condition, args
	}
	if translated.FilteredWhere == "" {
		return "SELECT " + columns + " FROM " + translated.FilteredFrom + " WHERE " + condition, args
	}
	return "SELECT " + columns + " FROM " + translated.FilteredFrom + translated.FilteredWhere + " AND " + condition, args
}

func (s *ItemStore) latestLimit(sql string, args []any, limit int) (string, []any) {
	if limit < 0 {
		return sql, args
	}
	args = append(args, limit)
	return sql + " LIMIT " + s.ph(len(args)), args
}

func (s *ItemStore) latestTime(raw any) *time.Time {
	return optionalTime(raw, s.stores.Dialect())
}

type latestEpisode struct {
	id, seasonID, seriesID *dto.UUID
	created                *time.Time
}

type latestSeries struct {
	maxDate         *time.Time
	recentEpisodes  int
	seasonIDs       []dto.UUID
	firstSeriesID   *dto.UUID
	newestEpisodeID dto.UUID
	choice          dto.UUID
}

func (s *ItemStore) latestTVShowIDs(ctx context.Context, tx *sql.Tx, translated TranslatedItemQuery, limit int) ([]dto.UUID, error) {
	// One pass over the filtered relation reads the columns the analysis
	// needs. The source runs two queries over its base query, one for the
	// top series and one for their episodes. Here each query re-evaluated
	// the whole filtered relation, so both steps now run in memory on the
	// same rows.
	statement, args := latestSelect(translated, "items.id, items.series_name, items.date_created, items.season_id, items.series_id", "items.series_name IS NOT NULL")
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	bySeries := map[string][]latestEpisode{}
	for rows.Next() {
		var idRaw, createdRaw, seasonRaw, seriesRaw any
		var name string
		if err := rows.Scan(&idRaw, &name, &createdRaw, &seasonRaw, &seriesRaw); err != nil {
			return nil, closeRows(rows, err)
		}
		id, err := scanUUID(idRaw)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		episode := latestEpisode{id: &id, created: s.latestTime(createdRaw)}
		if episode.seasonID, err = latestOptionalUUID(seasonRaw); err != nil {
			return nil, closeRows(rows, err)
		}
		if episode.seriesID, err = latestOptionalUUID(seriesRaw); err != nil {
			return nil, closeRows(rows, err)
		}
		bySeries[name] = append(bySeries[name], episode)
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	names := latestTopSeries(bySeries, limit)
	if len(names) == 0 {
		return nil, nil
	}
	// Episodes older than the oldest series' newest date minus the window
	// cannot be in any series' recent window.
	var cutoff *time.Time
	for _, name := range names {
		if newest := latestNewest(bySeries[name]); newest != nil && (cutoff == nil || newest.Before(*cutoff)) {
			cutoff = newest
		}
	}
	for _, name := range names {
		episodes := bySeries[name][:0:0]
		for _, episode := range bySeries[name] {
			if cutoff == nil || (episode.created != nil && !episode.created.Before(cutoff.Add(-latestRecentAdditionWindow))) {
				episodes = append(episodes, episode)
			}
		}
		sort.SliceStable(episodes, func(left, right int) bool { return latestEpisodeBefore(episodes[left], episodes[right]) })
		bySeries[name] = episodes
	}

	analysis := make([]latestSeries, 0, len(names))
	seasons := map[dto.UUID]bool{}
	seriesIDs := map[dto.UUID]bool{}
	for _, name := range names {
		episodes := bySeries[name]
		if len(episodes) == 0 {
			continue
		}
		newest := time.Time{}
		if episodes[0].created != nil {
			newest = *episodes[0].created
		}
		recentCutoff := newest.Add(-latestRecentAdditionWindow)
		group := latestSeries{maxDate: episodes[0].created, newestEpisodeID: *episodes[0].id}
		seen := map[dto.UUID]bool{}
		for _, episode := range episodes {
			if episode.created == nil || episode.created.Before(recentCutoff) {
				continue
			}
			group.recentEpisodes++
			if episode.seasonID != nil && !seen[*episode.seasonID] {
				seen[*episode.seasonID] = true
				group.seasonIDs = append(group.seasonIDs, *episode.seasonID)
				seasons[*episode.seasonID] = true
			}
			if group.firstSeriesID == nil {
				group.firstSeriesID = episode.seriesID
			}
		}
		if group.firstSeriesID != nil {
			seriesIDs[*group.firstSeriesID] = true
		}
		analysis = append(analysis, group)
	}

	episodeCounts, err := s.latestChildCounts(ctx, tx, "season_id", item.KindEpisode, seasons)
	if err != nil {
		return nil, err
	}
	seasonCounts, err := s.latestChildCounts(ctx, tx, "series_id", item.KindSeason, seriesIDs)
	if err != nil {
		return nil, err
	}
	for index := range analysis {
		group := &analysis[index]
		group.choice = group.newestEpisodeID
		switch {
		case len(group.seasonIDs) == 1:
			season := group.seasonIDs[0]
			seasonsInSeries := 1
			if group.firstSeriesID != nil {
				if count, found := seasonCounts[*group.firstSeriesID]; found {
					seasonsInSeries = count
				}
			}
			multipleOrAll := group.recentEpisodes > 1 || group.recentEpisodes == episodeCounts[season]
			if seasonsInSeries > 1 && multipleOrAll {
				group.choice = season
			} else if multipleOrAll && group.firstSeriesID != nil {
				group.choice = *group.firstSeriesID
			}
		case len(group.seasonIDs) > 1 && group.firstSeriesID != nil:
			group.choice = *group.firstSeriesID
		}
	}
	// Order by the group's newest date, then the chosen entity ID, both
	// descending, as the source orders its final results.
	sortLatestSeries(analysis)
	ids := make([]dto.UUID, len(analysis))
	for index, group := range analysis {
		ids[index] = group.choice
	}
	return ids, nil
}

// latestTopSeries returns the limit series names with the newest episodes,
// ordered by that date, descending, with dates absent last. Equal dates order
// by name bytes, the same on every dialect.
func latestTopSeries(bySeries map[string][]latestEpisode, limit int) []string {
	names := make([]string, 0, len(bySeries))
	newest := make(map[string]*time.Time, len(bySeries))
	for name, episodes := range bySeries {
		names = append(names, name)
		newest[name] = latestNewest(episodes)
	}
	sort.Slice(names, func(left, right int) bool {
		a, b := newest[names[left]], newest[names[right]]
		switch {
		case a == nil && b == nil:
		case a == nil:
			return false
		case b == nil:
			return true
		case !a.Equal(*b):
			return a.After(*b)
		}
		return names[left] < names[right]
	})
	if limit >= 0 && len(names) > limit {
		names = names[:limit]
	}
	return names
}

func latestNewest(episodes []latestEpisode) *time.Time {
	var newest *time.Time
	for _, episode := range episodes {
		if episode.created != nil && (newest == nil || episode.created.After(*newest)) {
			newest = episode.created
		}
	}
	return newest
}

// latestEpisodeBefore orders episodes by DateCreated, descending with absent
// dates last, then by ID, descending.
func latestEpisodeBefore(left, right latestEpisode) bool {
	switch {
	case left.created == nil && right.created == nil:
	case left.created == nil:
		return false
	case right.created == nil:
		return true
	case !left.created.Equal(*right.created):
		return left.created.After(*right.created)
	}
	return compareUUIDBytes(*left.id, *right.id) > 0
}

func sortLatestSeries(values []latestSeries) {
	before := func(left, right latestSeries) bool {
		switch {
		case left.maxDate == nil && right.maxDate == nil:
		case left.maxDate == nil:
			return false
		case right.maxDate == nil:
			return true
		case !left.maxDate.Equal(*right.maxDate):
			return left.maxDate.After(*right.maxDate)
		}
		return compareUUIDBytes(left.choice, right.choice) > 0
	}
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && before(values[cursor], values[cursor-1]); cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}

func compareUUIDBytes(left, right dto.UUID) int {
	for index := range left {
		if left[index] != right[index] {
			if left[index] < right[index] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func latestOptionalUUID(raw any) (*dto.UUID, error) {
	if raw == nil {
		return nil, nil
	}
	id, err := scanUUID(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// latestChildCounts counts items of kind per parent ID in column, over the
// whole library as the source does.
func (s *ItemStore) latestChildCounts(ctx context.Context, tx *sql.Tx, column string, kind item.Kind, parents map[dto.UUID]bool) (map[dto.UUID]int, error) {
	counts := make(map[dto.UUID]int, len(parents))
	if len(parents) == 0 {
		return counts, nil
	}
	args := []any{kind.String()}
	marks := make([]string, 0, len(parents))
	for id := range parents {
		args = append(args, s.uuidArg(id))
		marks = append(marks, s.ph(len(args)))
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+column+", COUNT(*) FROM items WHERE kind = "+s.ph(1)+" AND "+column+" IN ("+strings.Join(marks, ", ")+") GROUP BY "+column, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw any
		var count int
		if err := rows.Scan(&raw, &count); err != nil {
			return nil, closeRows(rows, err)
		}
		id, err := scanUUID(raw)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		counts[id] = count
	}
	return counts, closeRows(rows, rows.Err())
}

func (s *ItemStore) latestMovieIDs(ctx context.Context, tx *sql.Tx, translated TranslatedItemQuery, limit int) ([]dto.UUID, error) {
	d := s.stores.Dialect()
	statement, args := latestSelect(translated, "items.presentation_unique_key", "items.presentation_unique_key IS NOT NULL")
	groupSQL, groupArgs := s.latestLimit(statement+" GROUP BY items.presentation_unique_key ORDER BY MAX(items.date_created) DESC "+d.NullsLast()+", items.presentation_unique_key", args, limit)
	keys, err := nextUpStrings(ctx, tx, groupSQL, groupArgs)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	marks := make([]string, len(keys))
	args = append([]any(nil), translated.CountArgs...)
	for index, key := range keys {
		args = append(args, key)
		marks[index] = s.ph(len(args))
	}
	statement, _ = latestSelect(translated, "items.id, items.presentation_unique_key", "items.presentation_unique_key IN ("+strings.Join(marks, ", ")+")")
	rows, err := tx.QueryContext(ctx, statement+" ORDER BY items.date_created DESC "+d.NullsLast()+", items.id DESC", args...)
	if err != nil {
		return nil, err
	}
	first := make(map[string]dto.UUID, len(keys))
	for rows.Next() {
		var raw any
		var key string
		if err := rows.Scan(&raw, &key); err != nil {
			return nil, closeRows(rows, err)
		}
		if _, found := first[key]; found {
			continue
		}
		id, err := scanUUID(raw)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		first[key] = id
	}
	if err := closeRows(rows, rows.Err()); err != nil {
		return nil, err
	}
	ids := make([]dto.UUID, 0, len(first))
	for _, key := range keys {
		if id, found := first[key]; found {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// latestWindow is one page of a newest-first walk over a top parent's
// candidates. Movie candidates are the items with a PresentationUniqueKey and
// the query's IsFolder value, in the order of the grouped statement: newest
// DateCreated first (NULL last), then PresentationUniqueKey, then the newest
// id. The first row of each key in that order is the group's newest item, and
// the keys first appear in the order of the groups, so the walk can stop at
// the limit. ix_items_top_parent_latest serves that order without a sort.
// Album candidates are a page of the top parent's MusicAlbums, read first;
// the scope is then every item below one of them, with that album's id.
type latestWindow struct {
	parent       any
	offset, size int
	// albums is set for an album walk, and holds the page of album ids.
	albums   bool
	albumIDs []any
}

// appliesTo reports whether query has a top-parent scope that the window can
// narrow. A movie window also needs IsFolder, which selects the index range.
func (w *latestWindow) appliesTo(query ItemQuery) bool {
	if w == nil || !query.Recursive || query.recursiveParent == nil || !query.recursiveParent.topParent {
		return false
	}
	if kind := query.recursiveParent.kind; kind != item.KindCollectionFolder.String() && kind != item.KindUserView.String() {
		return false
	}
	return w.albums || query.IsFolder != nil
}

// subtree is the scope statement of the window page and its arguments, one
// for each "%s".
func (w *latestWindow) subtree(d Dialect, isFolder *bool) (string, []any) {
	if w.albums {
		marks := strings.TrimSuffix(strings.Repeat("%s, ", len(w.albumIDs)), ", ")
		return "SELECT item_id AS scope_item_id, ancestor_id AS scope_album_id FROM ancestors WHERE ancestor_id IN (" + marks + ")", w.albumIDs
	}
	return "SELECT id AS scope_item_id FROM items WHERE top_parent_id = %s AND is_folder = " + d.Boolean(*isFolder) +
		" AND presentation_unique_key IS NOT NULL ORDER BY date_created DESC " + d.NullsLast() + ", presentation_unique_key, id DESC LIMIT " + strconv.Itoa(w.size) + " OFFSET " + strconv.Itoa(w.offset), []any{w.parent}
}

// latestWindowSize is the first page of a window walk. Each later page
// doubles, up to 1,024 candidates, so a walk takes few statements even when
// the filters reject most candidates, and an album page stays well inside
// the bound-parameter limits.
func latestWindowSize(limit int) int {
	return min(max(2*limit, 32), 1024)
}

func nextLatestWindowSize(size int) int {
	return min(2*size, 1024)
}

// latestWindowMovieIDs returns what latestMovieIDs returns. It reads
// newest-first pages of candidates through the filtered relation and stops
// when it has limit keys or no candidates remain.
func (s *ItemStore) latestWindowMovieIDs(ctx context.Context, tx *sql.Tx, query ItemQuery, limit int) ([]dto.UUID, error) {
	d := s.stores.Dialect()
	seen := map[string]bool{}
	var ids []dto.UUID
	for window := (latestWindow{parent: s.uuidArg(*query.ParentID), size: latestWindowSize(limit)}); ; window.offset, window.size = window.offset+window.size, nextLatestWindowSize(window.size) {
		paged := query
		paged.latestWindow = &window
		translated, err := Translate(paged, d)
		if err != nil {
			return nil, err
		}
		statement, args := latestSelect(translated, "items.id, items.presentation_unique_key", "items.presentation_unique_key IS NOT NULL")
		rows, err := tx.QueryContext(ctx, statement+" ORDER BY items.date_created DESC "+d.NullsLast()+", items.presentation_unique_key, items.id DESC", args...)
		if err != nil {
			return nil, err
		}
		for len(ids) < limit && rows.Next() {
			var raw any
			var key string
			if err := rows.Scan(&raw, &key); err != nil {
				return nil, closeRows(rows, err)
			}
			if seen[key] {
				continue
			}
			id, err := scanUUID(raw)
			if err != nil {
				return nil, closeRows(rows, err)
			}
			seen[key] = true
			ids = append(ids, id)
		}
		if err := closeRows(rows, rows.Err()); err != nil {
			return nil, err
		}
		if len(ids) == limit {
			return ids, nil
		}
		more, err := s.latestMovieCandidatesAfter(ctx, tx, query, window.offset+window.size)
		if err != nil || !more {
			return ids, err
		}
	}
}

// latestMovieCandidatesAfter reports whether more than offset movie
// candidates exist.
func (s *ItemStore) latestMovieCandidatesAfter(ctx context.Context, tx *sql.Tx, query ItemQuery, offset int) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM items WHERE top_parent_id = "+s.ph(1)+" AND is_folder = "+s.stores.Dialect().Boolean(*query.IsFolder)+
		" AND presentation_unique_key IS NOT NULL LIMIT 1 OFFSET "+strconv.Itoa(offset), s.uuidArg(*query.ParentID)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// latestWindowAlbumIDs returns what latestAlbumIDs returns: the newest
// MusicAlbums that are ancestors of a filtered item. Every such album has
// the top parent as its top parent. It reads newest-first pages of those
// albums and keeps, in page order, the albums with a filtered item below
// them. The second statement reads the filtered relation, scoped to the
// page, directly: a filtered id set nested in an IN list made PostgreSQL's
// generic plan scan every ancestor row (150 ms a page).
func (s *ItemStore) latestWindowAlbumIDs(ctx context.Context, tx *sql.Tx, query ItemQuery, unwindowed TranslatedItemQuery, limit int) ([]dto.UUID, error) {
	d := s.stores.Dialect()
	var ids []dto.UUID
	for offset, size := 0, latestWindowSize(limit); ; offset, size = offset+size, nextLatestWindowSize(size) {
		pageSQL := "SELECT id FROM items WHERE kind = '" + item.KindMusicAlbum.String() + "' AND top_parent_id = " + s.ph(1) +
			" ORDER BY date_created DESC " + d.NullsLast() + ", id DESC LIMIT " + strconv.Itoa(size) + " OFFSET " + strconv.Itoa(offset)
		page, err := nextUpIDs(ctx, tx, d, pageSQL, []any{s.uuidArg(*query.ParentID)})
		if err != nil || len(page) == 0 {
			return ids, err
		}
		window := latestWindow{albums: true, albumIDs: make([]any, len(page))}
		for index, id := range page {
			window.albumIDs[index] = s.uuidArg(id)
		}
		paged := query
		paged.latestWindow = &window
		translated, err := Translate(paged, d)
		if err != nil {
			return nil, err
		}
		if translated.FilteredFrom == "" {
			// A transform hides the scope's album column.
			return s.latestAlbumIDs(ctx, tx, unwindowed, limit)
		}
		statement, args := latestSelect(translated, "DISTINCT parent_scope.scope_album_id", "1 = 1")
		matched, err := nextUpIDs(ctx, tx, d, statement, args)
		if err != nil {
			return nil, err
		}
		found := make(map[dto.UUID]bool, len(matched))
		for _, id := range matched {
			found[id] = true
		}
		for _, id := range page {
			if found[id] && len(ids) < limit {
				ids = append(ids, id)
			}
		}
		if len(ids) == limit || len(page) < size {
			return ids, nil
		}
	}
}

func (s *ItemStore) latestAlbumIDs(ctx context.Context, tx *sql.Tx, translated TranslatedItemQuery, limit int) ([]dto.UUID, error) {
	d := s.stores.Dialect()
	args := append([]any{item.KindMusicAlbum.String()}, translated.CountArgs...)
	// The album kind is the first argument, so SQLite binds it before the
	// filtered relation's positional arguments.
	ids := "SELECT ancestor_id FROM ancestors WHERE item_id IN (" + translated.IDSQL + ")"
	if d.Placeholder(1) != "?" {
		args = append(append([]any(nil), translated.CountArgs...), item.KindMusicAlbum.String())
		albumSQL, albumArgs := s.latestLimit("SELECT id FROM items WHERE kind = "+s.ph(len(args))+" AND id IN ("+ids+") ORDER BY date_created DESC "+d.NullsLast()+", id DESC", args, limit)
		return nextUpIDs(ctx, tx, d, albumSQL, albumArgs)
	}
	albumSQL, albumArgs := s.latestLimit("SELECT id FROM items WHERE kind = ? AND id IN ("+ids+") ORDER BY date_created DESC "+d.NullsLast()+", id DESC", args, limit)
	return nextUpIDs(ctx, tx, d, albumSQL, albumArgs)
}

// latestItemsByID loads ids with their satellites in the order of ids.
func (s *ItemStore) latestItemsByID(ctx context.Context, tx *sql.Tx, ids []dto.UUID) ([]item.Item, error) {
	return s.nextUpItems(ctx, tx, NextUpQuery{}, ids)
}
