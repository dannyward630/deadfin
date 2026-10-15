// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// addValueFilters appends the ItemQuery predicates backed by normalized item
// values and relation satellites. Every relation is correlated to items so the
// outer query continues to produce one row per item.
func (b *itemQueryFilterBuilder) addValueFilters(query ItemQuery) {
	b.addReferencedValueFilter("genre", query.GenreIDs, false)
	b.addReferencedValueFilter("studio", query.StudioIDs, false)
	b.addReferencedValuesFilter([]string{"artist", "album_artist"}, query.ArtistIDs, false)
	b.addReferencedValueFilter("album_artist", query.AlbumArtistIDs, false)
	b.addContributingArtistFilter(query.ContributingArtistIDs)
	b.addReferencedValuesFilter([]string{"artist", "album_artist"}, query.ExcludeArtistIDs, true)

	if len(query.AlbumIDs) != 0 {
		b.clauses = append(b.clauses, "parent_id IN ("+b.uuidList(query.AlbumIDs)+")")
	}

	b.addInheritedTagFilters(query)
	b.addImageTypesFilter(query)
	b.addTrailerTypesFilter(query)
	b.addSeriesStatusesFilter(query)
	b.addBoxSetLibraryFoldersFilter(query)
	b.addLinkedChildAncestorFilter(query)
	b.addPresentationRelationshipFilters(query)
	b.addOfficialRatingFilters(query)
}

// addReferencedValueFilter matches values whose normalized name belongs to one
// of the requested item IDs. Jellyfin models genres, studios, and artists as
// items but stores their relation to media as text-valued metadata.
func (b *itemQueryFilterBuilder) addReferencedValueFilter(kind string, referenceIDs []dto.UUID, exclude bool) {
	b.addReferencedValuesFilter([]string{kind}, referenceIDs, exclude)
}

func (b *itemQueryFilterBuilder) addReferencedValuesFilter(kinds []string, referenceIDs []dto.UUID, exclude bool) {
	if len(kinds) == 0 || len(referenceIDs) == 0 {
		return
	}

	kindClause := ""
	if len(kinds) == 1 {
		kindClause = "iv.kind = " + b.argument(kinds[0])
	} else {
		kindClause = "iv.kind IN (" + b.stringList(kinds) + ")"
	}
	prefix := "EXISTS"
	if exclude {
		prefix = "NOT EXISTS"
	}
	b.clauses = append(b.clauses, prefix+" (SELECT 1 FROM item_value_map AS ivm JOIN item_values AS iv ON iv.id = ivm.value_id WHERE ivm.item_id = items.id AND "+kindClause+" AND EXISTS (SELECT 1 FROM items AS reference WHERE reference.id IN ("+b.uuidList(referenceIDs)+") AND reference.clean_name = iv.clean_value))")
}

func (b *itemQueryFilterBuilder) addContributingArtistFilter(referenceIDs []dto.UUID) {
	if len(referenceIDs) == 0 {
		return
	}

	artists := "EXISTS (SELECT 1 FROM item_value_map AS ivm JOIN item_values AS iv ON iv.id = ivm.value_id WHERE ivm.item_id = items.id AND iv.kind = " + b.argument("artist") + " AND EXISTS (SELECT 1 FROM items AS reference WHERE reference.id IN (" + b.uuidList(referenceIDs) + ") AND reference.clean_name = iv.clean_value))"
	albumArtists := "NOT EXISTS (SELECT 1 FROM item_value_map AS ivm JOIN item_values AS iv ON iv.id = ivm.value_id WHERE ivm.item_id = items.id AND iv.kind = " + b.argument("album_artist") + " AND EXISTS (SELECT 1 FROM items AS reference WHERE reference.id IN (" + b.uuidList(referenceIDs) + ") AND reference.clean_name = iv.clean_value))"
	b.clauses = append(b.clauses, artists+" AND "+albumArtists)
}

func (b *itemQueryFilterBuilder) addInheritedTagFilters(query ItemQuery) {
	if len(query.ExcludeInheritedTags) != 0 {
		b.clauses = append(b.clauses, "NOT ("+b.inheritedTagMatch(query.ExcludeInheritedTags)+")")
	}
	if len(query.IncludeInheritedTags) != 0 {
		match := b.inheritedTagMatch(query.IncludeInheritedTags)
		b.clauses = append(b.clauses, "("+match+" OR kind = "+b.argument(item.KindPerson.String())+")")
	}
}

// inheritedTagMatch follows the pinned repository query through direct tags,
// the series, the maintained ancestor closure, and the top parent.
func (b *itemQueryFilterBuilder) inheritedTagMatch(tags []string) string {
	clean := make([]string, len(tags))
	for i, tag := range tags {
		clean[i] = item.CleanName(tag)
	}
	matches := func(itemID, kind string) string {
		return "EXISTS (SELECT 1 FROM item_value_map AS tag_map JOIN item_values AS tag_value ON tag_value.id = tag_map.value_id WHERE tag_map.item_id = " + itemID + " AND tag_value.kind = " + b.argument(kind) + " AND tag_value.clean_value IN (" + b.stringList(clean) + "))"
	}
	return "(" + strings.Join([]string{
		matches("items.id", "tag"),
		matches("items.series_id", "tag"),
		"EXISTS (SELECT 1 FROM ancestors AS ancestor WHERE ancestor.item_id = items.id AND " + matches("ancestor.ancestor_id", "tag") + ")",
		matches("items.top_parent_id", "tag"),
	}, " OR ") + ")"
}

func (b *itemQueryFilterBuilder) addImageTypesFilter(query ItemQuery) {
	if len(query.ImageTypes) == 0 {
		return
	}
	types := make([]string, len(query.ImageTypes))
	for i, imageType := range query.ImageTypes {
		types[i] = imageType.String()
	}
	b.clauses = append(b.clauses, "EXISTS (SELECT 1 FROM item_images AS image WHERE image.item_id = items.id AND image.type IN ("+b.stringList(types)+"))")
}

func (b *itemQueryFilterBuilder) addTrailerTypesFilter(query ItemQuery) {
	if len(query.TrailerTypes) == 0 {
		return
	}
	types := make([]string, len(query.TrailerTypes))
	for i, trailerType := range query.TrailerTypes {
		types[i] = trailerType.String()
	}
	b.clauses = append(b.clauses, "EXISTS (SELECT 1 FROM trailer_types AS trailer WHERE trailer.item_id = items.id AND trailer.type IN ("+b.stringList(types)+"))")
}

func (b *itemQueryFilterBuilder) addSeriesStatusesFilter(query ItemQuery) {
	if len(query.SeriesStatuses) == 0 {
		return
	}
	statuses := make([]string, len(query.SeriesStatuses))
	for i, status := range query.SeriesStatuses {
		statuses[i] = enum(status)
	}
	b.clauses = append(b.clauses, "series_status IN ("+b.stringList(statuses)+")")
}

func (b *itemQueryFilterBuilder) addBoxSetLibraryFoldersFilter(query ItemQuery) {
	if len(query.BoxSetLibraryFolders) == 0 {
		return
	}
	b.clauses = append(b.clauses, "EXISTS (SELECT 1 FROM boxset_library_folders AS boxset_folder WHERE boxset_folder.item_id = items.id AND boxset_folder.library_folder_id IN ("+b.uuidList(query.BoxSetLibraryFolders)+"))")
}

func (b *itemQueryFilterBuilder) addLinkedChildAncestorFilter(query ItemQuery) {
	if len(query.LinkedChildAncestorIDs) == 0 {
		return
	}
	b.clauses = append(b.clauses, "EXISTS (SELECT 1 FROM linked_children AS linked_child JOIN ancestors AS child_ancestor ON child_ancestor.item_id = linked_child.child_item_id WHERE linked_child.parent_item_id = items.id AND child_ancestor.ancestor_id IN ("+b.uuidList(query.LinkedChildAncestorIDs)+"))")
}

func (b *itemQueryFilterBuilder) addPresentationRelationshipFilters(query ItemQuery) {
	if strings.TrimSpace(query.AncestorWithPresentationUniqueKey) != "" {
		b.clauses = append(b.clauses, "EXISTS (SELECT 1 FROM items AS parent WHERE parent.id = items.parent_id AND parent.presentation_unique_key = "+b.argument(query.AncestorWithPresentationUniqueKey)+")")
	}
	if strings.TrimSpace(query.SeriesPresentationUniqueKey) != "" {
		b.clauses = append(b.clauses, "series_presentation_unique_key = "+b.argument(query.SeriesPresentationUniqueKey))
	}
}

func (b *itemQueryFilterBuilder) addOfficialRatingFilters(query ItemQuery) {
	if len(query.OfficialRatings) != 0 {
		b.clauses = append(b.clauses, b.itemOrRelatedMatch(func(alias string) string {
			return alias + ".official_rating IN (" + b.stringList(query.OfficialRatings) + ")"
		}))
	}
	if query.HasOfficialRating != nil {
		match := b.itemOrRelatedMatch(func(alias string) string {
			return alias + ".official_rating IS NOT NULL AND " + alias + ".official_rating <> ''"
		})
		if *query.HasOfficialRating {
			b.clauses = append(b.clauses, match)
		} else {
			b.clauses = append(b.clauses, "NOT ("+match+")")
		}
	}
}

// itemOrRelatedMatch preserves folder-aware predicates for direct items,
// descendants represented by the maintained closure, and linked children.
func (b *itemQueryFilterBuilder) itemOrRelatedMatch(predicate func(alias string) string) string {
	direct := predicate("items")
	descendant := predicate("descendant")
	linked := predicate("linked_item")
	return "(" + direct + " OR EXISTS (SELECT 1 FROM ancestors AS descendant_ancestor JOIN items AS descendant ON descendant.id = descendant_ancestor.item_id WHERE descendant_ancestor.ancestor_id = items.id AND " + descendant + ") OR EXISTS (SELECT 1 FROM linked_children AS linked_child JOIN items AS linked_item ON linked_item.id = linked_child.child_item_id WHERE linked_child.parent_item_id = items.id AND " + linked + "))"
}
