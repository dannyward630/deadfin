// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"fmt"
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
)

func translateItemQueryOrderWithArgs(query ItemQuery, d Dialect, argumentOffset int) (string, []any, error) {
	orders, mode, err := itemQueryOrders(query)
	if err != nil {
		return "", nil, err
	}
	b := itemQueryOrderBuilder{d: d, argumentOffset: argumentOffset, mode: mode}
	if query.SearchTerm == "" {
		for _, order := range orders {
			if order.SortBy != ItemSortBySeriesDatePlayed {
				continue
			}
			expression, err := b.seriesDatePlayedExpression(query)
			if err != nil {
				return "", nil, err
			}
			return expression + " " + directionSQL(order.SortOrder) + " " + b.nullOrdering(order.SortOrder) + ", " + b.text("sort_name") + " ASC " + b.nullOrdering(SortOrderAscending) + ", id ASC", b.args, nil
		}
	}
	parts := make([]string, 0, len(orders)+5)
	if query.linkedOrderParentID != nil {
		parent := b.argument(uuidArgDialect(d, *query.linkedOrderParentID))
		parts = append(parts, "(SELECT linked_order.sort_order FROM linked_children AS linked_order WHERE linked_order.parent_item_id = "+parent+" AND linked_order.child_item_id = items.id) ASC "+b.d.NullsLast())
	}
	hasSearch := query.SearchTerm != ""
	if hasSearch {
		parts = append(parts, b.searchRelevance(query.SearchTerm)+" ASC")
	}
	firstSort := ItemSortByDefault
	firstDirection := SortOrderAscending
	hasExplicitSort := false
	hasNameSort := false
	hasRandomSort := false
	for _, order := range orders {
		if order.SortBy == ItemSortByDefault {
			continue
		}
		if !hasExplicitSort {
			firstSort = order.SortBy
			firstDirection = order.SortOrder
			hasExplicitSort = true
		}
		switch order.SortBy {
		case ItemSortBySortName, ItemSortByName, ItemSortByAiredEpisodeOrder, ItemSortByAirTime:
			hasNameSort = true
		}
		expression, err := b.expression(order.SortBy, query)
		if err != nil {
			return "", nil, err
		}
		if order.SortBy == ItemSortByRandom {
			hasRandomSort = true
			parts = append(parts, expression)
			continue
		}
		parts = append(parts, expression+" "+directionSQL(order.SortOrder)+" "+b.nullOrdering(order.SortOrder))
	}
	if !hasSearch && !hasExplicitSort {
		parts = append(parts, b.text("sort_name")+" ASC "+b.nullOrdering(SortOrderAscending))
	}
	if firstSort == ItemSortBySortName {
		parts = append(parts, b.text("name")+" "+directionSQL(firstDirection)+" "+b.nullOrdering(firstDirection))
	}
	if !hasSearch && hasExplicitSort && !hasNameSort && !hasRandomSort {
		parts = append(parts, b.text("sort_name")+" ASC "+b.nullOrdering(SortOrderAscending))
	}
	parts = append(parts, "id ASC")
	return strings.Join(parts, ", "), b.args, nil
}

type itemQueryOrderMode uint8

const (
	itemQueryOrderModeCanonical itemQueryOrderMode = iota
	itemQueryOrderModeLegacy
)

func itemQueryOrders(query ItemQuery) ([]ItemOrder, itemQueryOrderMode, error) {
	if len(query.OrderBy) != 0 {
		orders := append([]ItemOrder(nil), query.OrderBy...)
		for _, order := range orders {
			if order.SortOrder != SortOrderAscending && order.SortOrder != SortOrderDescending {
				return nil, itemQueryOrderModeCanonical, fmt.Errorf("translate item query: unsupported sort order %q", order.SortOrder)
			}
		}
		return orders, itemQueryOrderModeCanonical, nil
	}
	if len(query.SortBy) == 0 {
		// RequestHelpers.GetOrderBy returns an empty OrderBy collection when
		// SortBy is empty, regardless of a supplied SortOrder. Swiftfin relies
		// on this with SortOrder=Ascending in its first-series-episode fallback.
		return []ItemOrder{{SortBy: ItemSortBySortName, SortOrder: SortOrderAscending}}, itemQueryOrderModeLegacy, nil
	}
	if len(query.SortOrder) > len(query.SortBy) {
		return nil, itemQueryOrderModeLegacy, fmt.Errorf("translate item query: received %d sort orders for %d sort fields", len(query.SortOrder), len(query.SortBy))
	}
	orders := make([]ItemOrder, len(query.SortBy))
	direction := SortOrderAscending
	if len(query.SortOrder) != 0 {
		direction = query.SortOrder[0]
	}
	if direction != SortOrderAscending && direction != SortOrderDescending {
		return nil, itemQueryOrderModeLegacy, fmt.Errorf("translate item query: unsupported sort order %q", direction)
	}
	for i, sort := range query.SortBy {
		if i < len(query.SortOrder) {
			direction = query.SortOrder[i]
		}
		if direction != SortOrderAscending && direction != SortOrderDescending {
			return nil, itemQueryOrderModeLegacy, fmt.Errorf("translate item query: unsupported sort order %q", direction)
		}
		orders[i] = ItemOrder{SortBy: sort, SortOrder: direction}
	}
	return orders, itemQueryOrderModeLegacy, nil
}

type itemQueryOrderBuilder struct {
	d              Dialect
	argumentOffset int
	args           []any
	mode           itemQueryOrderMode
}

func (b *itemQueryOrderBuilder) argument(value any) string {
	b.args = append(b.args, value)
	return b.d.Placeholder(b.argumentOffset + len(b.args))
}

// nullOrdering makes the captured SQLite source behavior independent of the
// target database: ascending nullable values precede concrete values, while
// descending nullable values follow them.
func (b *itemQueryOrderBuilder) nullOrdering(order SortOrder) string {
	if order == SortOrderDescending {
		return b.d.NullsLast()
	}
	return "NULLS FIRST"
}

func (b *itemQueryOrderBuilder) text(expression string) string {
	return b.d.TextCollation(expression)
}

// Source: jellyfin/jellyfin@b04614e18d2aeed8d3d3de920c381c6bd5b97bdb.
// Canonical OrderBy follows Item/OrderMapper.cs:30-78 and
// BaseItemRepository.QueryBuilding.cs:273-339. Legacy SortBy follows the
// public /Items controller path and retains its distinct comparer fallbacks.
func (b *itemQueryOrderBuilder) expression(sort ItemSortBy, query ItemQuery) (string, error) {
	switch sort {
	case ItemSortByDefault, ItemSortBySortName:
		return b.text("sort_name"), nil
	case ItemSortByAiredEpisodeOrder:
		return b.text("sort_name"), nil
	case ItemSortByAlbum:
		return b.text("album"), nil
	case ItemSortByAlbumArtist:
		return b.itemValueExpression("album_artist"), nil
	case ItemSortByArtist:
		return b.itemValueExpression("artist"), nil
	case ItemSortByDateCreated:
		return "date_created", nil
	case ItemSortByOfficialRating:
		return "inherited_parental_rating_value", nil
	case ItemSortByDatePlayed:
		return b.datePlayedExpression(query)
	case ItemSortByPremiereDate:
		return b.premiereDateExpression(), nil
	case ItemSortByStartDate:
		return "start_date", nil
	case ItemSortByName:
		return b.text("sort_name"), nil
	case ItemSortByRandom:
		return b.d.Random(), nil
	case ItemSortByRuntime:
		if b.mode == itemQueryOrderModeLegacy {
			return "COALESCE(run_time_ticks, 0)", nil
		}
		return "run_time_ticks", nil
	case ItemSortByCommunityRating:
		return "community_rating", nil
	case ItemSortByProductionYear:
		if b.mode == itemQueryOrderModeLegacy {
			return b.productionYearExpression(), nil
		}
		return "production_year", nil
	case ItemSortByPlayCount:
		return b.userDataExpression(query, "play_count", "0")
	case ItemSortByCriticRating:
		return "critic_rating", nil
	case ItemSortByIsFolder:
		return "is_folder", nil
	case ItemSortByIsUnplayed:
		expression, err := b.userDataExpression(query, "played", b.d.Boolean(false))
		if err != nil {
			return "", err
		}
		return "NOT " + expression, nil
	case ItemSortByIsPlayed:
		return b.userDataExpression(query, "played", b.d.Boolean(false))
	case ItemSortBySeriesSortName:
		return b.text("series_name"), nil
	case ItemSortByVideoBitRate:
		return "total_bitrate", nil
	case ItemSortByAirTime:
		return b.text("sort_name"), nil
	case ItemSortByStudio:
		return b.itemValueExpression("studio"), nil
	case ItemSortByIsFavoriteOrLiked:
		return b.userDataExpression(query, "is_favorite", b.d.Boolean(false))
	case ItemSortByDateLastContentAdded:
		if b.mode == itemQueryOrderModeLegacy {
			return b.dateLastContentAddedExpression(), nil
		}
		return "date_last_media_added", nil
	case ItemSortBySeriesDatePlayed:
		return b.seriesDatePlayedExpression(query)
	case ItemSortByParentIndexNumber:
		return "parent_index_number", nil
	case ItemSortByIndexNumber:
		return "index_number", nil
	default:
		return "", fmt.Errorf("translate item query: unsupported sort field %q", sort)
	}
}

func (b *itemQueryOrderBuilder) itemValueExpression(kind string) string {
	kindPlaceholder := b.argument(kind)
	expression := "(SELECT value.clean_value FROM item_value_map AS value_map JOIN item_values AS value ON value.id = value_map.value_id WHERE value_map.item_id = items.id AND value.kind = " + kindPlaceholder + " ORDER BY " + b.text("value.clean_value") + " ASC LIMIT 1)"
	return b.text(expression)
}

func (b *itemQueryOrderBuilder) datePlayedExpression(query ItemQuery) (string, error) {
	// When the Resume progress CTEs are present, use the bounded relation
	// instead of the unbounded correlated lookup. This avoids a per-item
	// scan of all user_data rows for the user.
	if query.IsResumable != nil && *query.IsResumable && query.UserID != nil && !query.UserID.IsNil() && !itemQueryIncludesKind(query.IncludeItemTypes, item.KindSeries) {
		return "(SELECT last_played FROM resume_progress_ids WHERE resume_progress_ids.item_id = items.id)", nil
	}
	// One indexed arm per way a user row reaches the item: stored item id,
	// a custom key the item carries, or a row of one of its alternate
	// versions. A single correlated scan with an OR across these read every
	// user row for every item (19s for Episodes by DatePlayed on ~9k
	// episodes). MAX ignores the duplicates UNION ALL keeps.
	// Arguments are added in text order because SQLite binds "?" by position.
	direct := "SELECT played_data.last_played FROM user_data AS played_data WHERE " + b.optionalUserCondition(query, "played_data") + " AND played_data.item_id = items.id"
	byKey := "SELECT played_data.last_played FROM item_user_data_keys AS item_key CROSS JOIN user_data AS played_data WHERE item_key.item_id = items.id" + b.userDataKeyFilter(query) + " AND " + b.optionalUserCondition(query, "played_data") + " AND played_data.custom_key = item_key.custom_key"
	byVersion := "SELECT played_data.last_played FROM items AS played_item CROSS JOIN user_data AS played_data WHERE played_item.primary_version_id = items.id AND " + b.optionalUserCondition(query, "played_data") + " AND played_data.item_id = played_item.id"
	return "(SELECT MAX(played_dates.last_played) FROM (" + direct + " UNION ALL " + byKey + " UNION ALL " + byVersion + ") AS played_dates)", nil
}

func (b *itemQueryOrderBuilder) seriesDatePlayedExpression(query ItemQuery) (string, error) {
	condition := b.optionalUserCondition(query, "series_data")
	return "(SELECT MAX(series_data.last_played) FROM user_data AS series_data JOIN items AS played_item ON played_item.id = series_data.item_id WHERE " + condition + " AND " + b.userDataCandidateClause(query, "series_data") + " AND series_data.played = " + b.d.Boolean(true) + " AND played_item.series_presentation_unique_key = items.presentation_unique_key)", nil
}

func (b *itemQueryOrderBuilder) userDataExpression(query ItemQuery, column, fallback string) (string, error) {
	// The first matching row by custom key, read through two indexed arms
	// (stored item id, carried custom key) instead of one correlated scan
	// with an OR that read every user row for every item.
	directCondition, err := b.userCondition(query, "user_sort")
	if err != nil {
		return "", err
	}
	direct := "SELECT user_sort.custom_key, user_sort." + column + " AS value FROM user_data AS user_sort WHERE " + directCondition + " AND user_sort.item_id = items.id"
	keyCondition, err := b.userCondition(query, "user_sort")
	if err != nil {
		return "", err
	}
	byKey := "SELECT user_sort.custom_key, user_sort." + column + " AS value FROM item_user_data_keys AS item_key CROSS JOIN user_data AS user_sort WHERE item_key.item_id = items.id" + b.userDataKeyFilter(query) + " AND " + keyCondition + " AND user_sort.custom_key = item_key.custom_key"
	expression := "(SELECT user_rows.value FROM (" + direct + " UNION ALL " + byKey + ") AS user_rows ORDER BY " + b.text("user_rows.custom_key") + " ASC LIMIT 1)"
	return "COALESCE(" + expression + ", " + fallback + ")", nil
}

func (b *itemQueryOrderBuilder) optionalUserCondition(query ItemQuery, alias string) string {
	if query.UserID == nil || query.UserID.IsNil() {
		return "1 = 1"
	}
	return alias + ".user_id = " + b.argument(uuidArgDialect(b.d, *query.UserID))
}

func (b *itemQueryOrderBuilder) userDataCandidateClause(query ItemQuery, alias string) string {
	keyFilter := ""
	if query.DisableLiveTVChannelUserDataName {
		keyFilter = " AND item_key.is_live_tv_channel_name = " + b.d.Boolean(false)
	}
	return "(" + alias + ".item_id = items.id OR EXISTS (SELECT 1 FROM item_user_data_keys AS item_key WHERE item_key.item_id = items.id AND item_key.custom_key = " + alias + ".custom_key" + keyFilter + "))"
}

func (b *itemQueryOrderBuilder) userDataKeyFilter(query ItemQuery) string {
	if query.DisableLiveTVChannelUserDataName {
		return " AND item_key.is_live_tv_channel_name = " + b.d.Boolean(false)
	}
	return ""
}

func (b *itemQueryOrderBuilder) premiereDateExpression() string {
	if b.d.Placeholder(1) == "?" {
		fallback := "CASE WHEN production_year BETWEEN 1 AND 9999 THEN strftime('%s', printf('%04d-01-01', production_year)) * 1000000 END"
		if b.mode == itemQueryOrderModeLegacy {
			fallback = "COALESCE(" + fallback + ", -62135596800000000)"
		}
		return "COALESCE(premiere_date, " + fallback + ")"
	}
	fallback := "CASE WHEN production_year BETWEEN 1 AND 9999 THEN make_timestamptz(production_year, 1, 1, 0, 0, 0, 'UTC') END"
	if b.mode == itemQueryOrderModeLegacy {
		fallback = "COALESCE(" + fallback + ", make_timestamptz(1, 1, 1, 0, 0, 0, 'UTC'))"
	}
	return "COALESCE(premiere_date, " + fallback + ")"
}

func (b *itemQueryOrderBuilder) productionYearExpression() string {
	if b.d.Placeholder(1) == "?" {
		return "COALESCE(production_year, CAST(strftime('%Y', premiere_date) AS INTEGER), 0)"
	}
	return "COALESCE(production_year, EXTRACT(YEAR FROM premiere_date)::INTEGER, 0)"
}

func (b *itemQueryOrderBuilder) dateLastContentAddedExpression() string {
	if b.d.Placeholder(1) == "?" {
		return "CASE WHEN is_folder THEN COALESCE(date_last_media_added, -62135596800000000) ELSE -62135596800000000 END"
	}
	return "CASE WHEN is_folder THEN COALESCE(date_last_media_added, make_timestamptz(1, 1, 1, 0, 0, 0, 'UTC')) ELSE make_timestamptz(1, 1, 1, 0, 0, 0, 'UTC') END"
}

func (b *itemQueryOrderBuilder) userCondition(query ItemQuery, alias string) (string, error) {
	if query.UserID == nil || query.UserID.IsNil() {
		return "", fmt.Errorf("translate item query: user-relative sort requires a user ID")
	}
	return alias + ".user_id = " + b.argument(uuidArgDialect(b.d, *query.UserID)), nil
}

func (b *itemQueryOrderBuilder) searchRelevance(term string) string {
	clean := item.CleanName(term)
	original := strings.ToLower(term)
	exactClean := b.argument(clean)
	exactOriginal := b.argument(original)
	prefixCleanWord := b.argument(clean + " %")
	prefixOriginalWord := b.argument(original + " %")
	prefixClean := b.argument(clean + "%")
	prefixOriginal := b.argument(original + "%")
	return "CASE WHEN clean_name = " + exactClean + " OR LOWER(original_title) = " + exactOriginal + " THEN 0 WHEN " + b.d.SearchClause("clean_name", prefixCleanWord) + " OR " + b.d.SearchClause("original_title", prefixOriginalWord) + " THEN 1 WHEN " + b.d.SearchClause("clean_name", prefixClean) + " OR " + b.d.SearchClause("original_title", prefixOriginal) + " THEN 2 ELSE 3 END"
}
