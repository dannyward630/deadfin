// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
)

// addMediaFilters appends the media-derived predicates from the pinned
// BaseItemRepository translation. Its helper predicates keep the outer query
// on items, so media rows can never multiply an item result.
func (b *itemQueryFilterBuilder) addMediaFilters(query ItemQuery) {
	b.addResolutionFilters(query)

	b.addAbsentMediaStreamFilter("Audio", query.HasNoAudioTrackWithLanguage, nil)
	internal := false
	b.addAbsentMediaStreamFilter("Subtitle", query.HasNoInternalSubtitleTrackWithLanguage, &internal)
	external := true
	b.addAbsentMediaStreamFilter("Subtitle", query.HasNoExternalSubtitleTrackWithLanguage, &external)
	b.addAbsentMediaStreamFilter("Subtitle", query.HasNoSubtitleTrackWithLanguage, nil)

	if query.HasSubtitles != nil {
		b.addMediaLeafPredicate(*query.HasSubtitles, "media_streams", "media_stream", "media_stream.item_id", func() string {
			return "media_stream.stream_type = " + b.argument("Subtitle")
		})
	}
	b.addMediaStreamLanguageFilter("Subtitle", query.SubtitleLanguages)
	b.addMediaStreamLanguageFilter("Audio", query.AudioLanguages)

	if query.HasChapterImages != nil {
		b.addMediaLeafPredicate(*query.HasChapterImages, "chapters", "chapter", "chapter.item_id", func() string {
			return "chapter.image_path IS NOT NULL"
		})
	}

	if len(query.VideoTypes) != 0 {
		b.addMediaItemPredicate(true, func() string {
			videoTypes := make([]string, 0, len(query.VideoTypes))
			isoTypes := make([]string, 0, len(query.VideoTypes))
			for _, videoType := range query.VideoTypes {
				videoTypes = append(videoTypes, b.argument(enum(videoType)))
				switch videoType {
				case item.VideoTypeDvd:
					isoTypes = append(isoTypes, b.argument(enum(item.IsoTypeDvd)))
				case item.VideoTypeBluRay:
					isoTypes = append(isoTypes, b.argument(enum(item.IsoTypeBluRay)))
				}
			}
			predicate := "media_item.video_type IN (" + strings.Join(videoTypes, ", ") + ")"
			if len(isoTypes) != 0 {
				predicate += " OR media_item.iso_type IN (" + strings.Join(isoTypes, ", ") + ")"
			}
			return "(" + predicate + ")"
		})
	}

	if query.Is3D != nil {
		b.addMediaItemPredicate(*query.Is3D, func() string {
			return "media_item.video_3d_format IS NOT NULL"
		})
	}

	b.addExtraOwnerFilter(query.HasSpecialFeature, func() string {
		return "media_extra.extra_type IS NOT NULL" +
			" AND media_extra.extra_type <> " + b.argument(enum(item.ExtraTypeUnknown)) +
			" AND media_extra.extra_type <> " + b.argument(enum(item.ExtraTypeTrailer)) +
			" AND media_extra.extra_type <> " + b.argument(enum(item.ExtraTypeThemeSong)) +
			" AND media_extra.extra_type <> " + b.argument(enum(item.ExtraTypeThemeVideo))
	})
	b.addExtraOwnerFilter(query.HasTrailer, func() string {
		return "media_extra.extra_type = " + b.argument(enum(item.ExtraTypeTrailer))
	})
	b.addExtraOwnerFilter(query.HasThemeSong, func() string {
		return "media_extra.extra_type = " + b.argument(enum(item.ExtraTypeThemeSong))
	})
	b.addExtraOwnerFilter(query.HasThemeVideo, func() string {
		return "media_extra.extra_type = " + b.argument(enum(item.ExtraTypeThemeVideo))
	})
}

func (b *itemQueryFilterBuilder) addResolutionFilters(query ItemQuery) {
	if query.IsHD == nil && query.Is4K == nil {
		return
	}

	includeSD := query.IsHD != nil && !*query.IsHD
	includeHD := query.IsHD != nil && *query.IsHD
	include4K := query.Is4K != nil && *query.Is4K
	b.addMediaLeafPredicate(true, "items", "media_item", "media_item.id", func() string {
		classes := make([]string, 0, 3)
		if includeSD {
			classes = append(classes, "media_item.width < 1200")
		}
		if includeHD {
			classes = append(classes, "media_item.width >= 1200 AND NOT (media_item.width >= 3800 OR media_item.height >= 2100)")
		}
		if include4K {
			classes = append(classes, "media_item.width >= 3800 OR media_item.height >= 2100")
		}
		if len(classes) == 0 {
			return "1 = 0"
		}
		return "media_item.width > 0 AND (" + strings.Join(classes, " OR ") + ")"
	})
}

func (b *itemQueryFilterBuilder) addAbsentMediaStreamFilter(streamType, language string, external *bool) {
	if strings.TrimSpace(language) == "" {
		return
	}
	b.addMediaLeafPredicate(false, "media_streams", "media_stream", "media_stream.item_id", func() string {
		predicate := "media_stream.stream_type = " + b.argument(streamType) + " AND media_stream.language = " + b.argument(language)
		if external != nil {
			predicate += " AND media_stream.is_external = " + b.argument(mediaBoolArg(b.d, *external))
		}
		return predicate
	})
}

func (b *itemQueryFilterBuilder) addMediaStreamLanguageFilter(streamType string, languages []string) {
	if len(languages) == 0 {
		return
	}
	b.addMediaLeafPredicate(true, "media_streams", "media_stream", "media_stream.item_id", func() string {
		streamPredicate := "media_stream.stream_type = " + b.argument(streamType)
		languagePredicate := "media_stream.language IN (" + b.stringList(languages) + ")"
		for _, language := range languages {
			if language == "und" {
				languagePredicate = "(" + languagePredicate + " OR media_stream.language IS NULL OR media_stream.language = '')"
				break
			}
		}
		return streamPredicate + " AND " + languagePredicate
	})
}

func (b *itemQueryFilterBuilder) addExtraOwnerFilter(include *bool, extraPredicate func() string) {
	if include == nil {
		return
	}
	b.addMediaItemPredicate(*include, func() string {
		return "EXISTS (SELECT 1 FROM items AS media_extra WHERE media_extra.owner_id = media_item.id AND " + extraPredicate() + ")"
	})
}

func (b *itemQueryFilterBuilder) addMediaItemPredicate(include bool, predicate func() string) {
	match := mediaItemOrDescendantMatch(b, predicate)
	if !include {
		match = "NOT " + match
	}
	b.clauses = append(b.clauses, match)
}

func (b *itemQueryFilterBuilder) addMediaLeafPredicate(include bool, table, alias, candidateID string, predicate func() string) {
	match := mediaLeafOrDescendantMatch(b, table, alias, candidateID, predicate)
	if !include {
		match = "NOT " + match
	}
	b.clauses = append(b.clauses, match)
}

// mediaItemOrDescendantMatch mirrors WhereItemOrDescendantMatches: a direct
// item match, a match anywhere in its owned descendant closure, or a direct
// linked-child match all qualify.
func mediaItemOrDescendantMatch(b *itemQueryFilterBuilder, predicate func() string) string {
	return "EXISTS (SELECT 1 FROM items AS media_item WHERE " + predicate() + " AND (" +
		"media_item.id = items.id OR " +
		"EXISTS (SELECT 1 FROM ancestors AS media_ancestor WHERE media_ancestor.ancestor_id = items.id AND media_ancestor.item_id = media_item.id) OR " +
		"EXISTS (SELECT 1 FROM linked_children AS media_linked WHERE media_linked.parent_item_id = items.id AND media_linked.child_item_id = media_item.id)))"
}

// mediaLeafOrDescendantMatch has the leaf/folder split used by the stream,
// chapter-image, and resolution source predicates. Folder-owned media itself
// is intentionally ignored; only owned descendants and linked children count.
func mediaLeafOrDescendantMatch(b *itemQueryFilterBuilder, table, alias, candidateID string, predicate func() string) string {
	return "EXISTS (SELECT 1 FROM " + table + " AS " + alias + " WHERE " + predicate() + " AND ((items.is_folder = " + b.d.Boolean(false) + " AND " + candidateID + " = items.id) OR (items.is_folder = " + b.d.Boolean(true) + " AND (" +
		"EXISTS (SELECT 1 FROM ancestors AS media_ancestor WHERE media_ancestor.ancestor_id = items.id AND media_ancestor.item_id = " + candidateID + ") OR " +
		"EXISTS (SELECT 1 FROM linked_children AS media_linked WHERE media_linked.parent_item_id = items.id AND media_linked.child_item_id = " + candidateID + ")))))"
}

// mediaBoolArg keeps SQLite predicate arguments in its integer representation
// while PostgreSQL receives native booleans. boolArg chooses by the value being
// encoded, which would otherwise leave SQLite false as a Go bool.
func mediaBoolArg(d Dialect, value bool) any {
	if d.Boolean(true) == "1" {
		if value {
			return 1
		}
		return 0
	}
	return value
}
