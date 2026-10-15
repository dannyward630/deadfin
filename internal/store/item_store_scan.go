// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

type rowScanner interface{ Scan(...any) error }

func scanItem(row rowScanner, d Dialect) (item.Item, error) {
	values := make([]any, len(itemColumns))
	dest := make([]any, len(values))
	for i := range values {
		dest[i] = &values[i]
	}
	if err := row.Scan(dest...); err != nil {
		return item.Item{}, err
	}
	get := func(name string) any {
		for i, c := range itemColumns {
			if c == name {
				return values[i]
			}
		}
		return nil
	}
	id, err := scanUUID(get("id"))
	if err != nil {
		return item.Item{}, err
	}
	kind, err := item.ParseKind(asString(get("kind")))
	if err != nil {
		return item.Item{}, err
	}
	it := item.Item{ID: id, Kind: kind, PlaylistOwnerUserID: dto.NilUUID, SourceType: item.SourceType(asUint8(get("source_type"))), IsFolder: asBool(get("is_folder")), IsVirtualItem: asBool(get("is_virtual_item")), IsInMixedFolder: asBool(get("is_in_mixed_folder")), IsLocked: asBool(get("is_locked")), IsMovie: asBool(get("is_movie")), IsSeries: asBool(get("is_series")), IsRepeat: asBool(get("is_repeat")), IsPlaceHolder: asBool(get("is_placeholder")), OpenAccess: asBool(get("open_access"))}
	for _, pair := range []struct {
		name string
		dst  **dto.UUID
	}{{"parent_id", &it.ParentID}, {"top_parent_id", &it.TopParentID}, {"series_id", &it.SeriesID}, {"season_id", &it.SeasonID}, {"channel_id", &it.ChannelID}, {"primary_version_id", &it.PrimaryVersionID}, {"owner_id", &it.OwnerID}} {
		if !isNil(pairRaw(get, pair.name)) {
			v, e := scanUUID(get(pair.name))
			if e != nil {
				return item.Item{}, e
			}
			*pair.dst = &v
		}
	}
	if !isNil(get("playlist_owner_user_id")) {
		u, e := scanUUID(get("playlist_owner_user_id"))
		if e != nil {
			return item.Item{}, e
		}
		it.PlaylistOwnerUserID = u
	}
	assignStringPointers(&it, get)
	if err := assignTimes(&it, get, d); err != nil {
		return item.Item{}, err
	}
	assignNumbers(&it, get)
	assignEnums(&it, get)
	if raw := get("extra"); !isNil(raw) {
		if err := json.Unmarshal([]byte(asString(raw)), &it.Extra); err != nil {
			return item.Item{}, fmt.Errorf("decode item extra: %w", err)
		}
	}
	return it, nil
}
func pairRaw(get func(string) any, name string) any { return get(name) }
func assignStringPointers(it *item.Item, get func(string) any) {
	for _, p := range []struct {
		name string
		dst  **string
	}{{"path", &it.Path}, {"name", &it.Name}, {"sort_name", &it.SortName}, {"clean_name", &it.CleanName}, {"forced_sort_name", &it.ForcedSortName}, {"original_title", &it.OriginalTitle}, {"original_language", &it.OriginalLanguage}, {"overview", &it.Overview}, {"tagline", &it.Tagline}, {"official_rating", &it.OfficialRating}, {"custom_rating", &it.CustomRating}, {"presentation_unique_key", &it.PresentationUniqueKey}, {"series_presentation_unique_key", &it.SeriesPresentationUniqueKey}, {"album", &it.Album}, {"external_id", &it.ExternalID}, {"external_series_id", &it.ExternalSeriesID}, {"external_service_id", &it.ExternalServiceID}, {"preferred_metadata_language", &it.PreferredMetadataLanguage}, {"preferred_metadata_country_code", &it.PreferredMetadataCountryCode}, {"series_name", &it.SeriesName}, {"season_name", &it.SeasonName}, {"episode_title", &it.EpisodeTitle}, {"show_id", &it.ShowID}, {"container", &it.Container}, {"media_source_container", &it.MediaSourceContainer}} {
		if !isNil(get(p.name)) {
			v := asString(get(p.name))
			*p.dst = &v
		}
	}
}

func assignTimes(it *item.Item, get func(string) any, d Dialect) error {
	for _, p := range []struct {
		name string
		dst  **time.Time
	}{{"start_date", &it.StartDate}, {"end_date", &it.EndDate}, {"premiere_date", &it.PremiereDate}, {"date_created", &it.DateCreated}, {"date_modified", &it.DateModified}, {"file_changed_at", &it.FileChangedAt}, {"date_last_refreshed", &it.DateLastRefreshed}, {"date_last_saved", &it.DateLastSaved}, {"date_last_media_added", &it.DateLastMediaAdded}} {
		if !isNil(get(p.name)) {
			v, e := d.ScanTime(get(p.name))
			if e != nil {
				return e
			}
			*p.dst = &v
		}
	}
	return nil
}

func assignNumbers(it *item.Item, get func(string) any) {
	for _, p := range []struct {
		name string
		dst  **int
	}{{"production_year", &it.ProductionYear}, {"index_number", &it.IndexNumber}, {"parent_index_number", &it.ParentIndexNumber}, {"width", &it.Width}, {"height", &it.Height}, {"total_bitrate", &it.TotalBitrate}, {"inherited_parental_rating_value", &it.InheritedParentalRatingValue}, {"inherited_parental_rating_subvalue", &it.InheritedParentalRatingSubValue}, {"airs_before_season_number", &it.AirsBeforeSeasonNumber}, {"airs_after_season_number", &it.AirsAfterSeasonNumber}, {"airs_before_episode_number", &it.AirsBeforeEpisodeNumber}} {
		if !isNil(get(p.name)) {
			v := asInt(get(p.name))
			*p.dst = &v
		}
	}
	for _, p := range []struct {
		name string
		dst  **int64
	}{{"run_time_ticks", &it.RunTimeTicks}, {"size", &it.Size}, {"file_device", &it.FileDevice}, {"file_inode", &it.FileInode}} {
		if !isNil(get(p.name)) {
			v := asInt64(get(p.name))
			*p.dst = &v
		}
	}
	for _, p := range []struct {
		name string
		dst  **float32
	}{{"community_rating", &it.CommunityRating}, {"critic_rating", &it.CriticRating}, {"lufs", &it.LUFS}, {"normalization_gain", &it.NormalizationGain}} {
		if !isNil(get(p.name)) {
			v := asFloat32(get(p.name))
			*p.dst = &v
		}
	}
}

func assignEnums(it *item.Item, get func(string) any) {
	if !isNil(get("unrated_type")) {
		v := item.UnratedItem(asUint8(get("unrated_type")))
		it.UnratedType = &v
	}
	if !isNil(get("media_type")) {
		v := item.MediaType(asUint8(get("media_type")))
		it.MediaType = &v
	}
	if !isNil(get("extra_type")) {
		v := item.ExtraType(asUint8(get("extra_type")))
		it.ExtraType = &v
	}
	if !isNil(get("video_type")) {
		v := item.VideoType(asUint8(get("video_type")))
		it.VideoType = &v
	}
	if !isNil(get("iso_type")) {
		v := item.IsoType(asUint8(get("iso_type")))
		it.IsoType = &v
	}
	if !isNil(get("video_3d_format")) {
		v := item.Video3DFormat(asUint8(get("video_3d_format")))
		it.Video3DFormat = &v
	}
	if !isNil(get("series_status")) {
		v := item.SeriesStatus(asUint8(get("series_status")))
		it.SeriesStatus = &v
	}
	if !isNil(get("program_audio")) {
		v := item.ProgramAudio(asUint8(get("program_audio")))
		it.ProgramAudio = &v
	}
}

func (s *ItemStore) repairDerivedTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM ancestors"); err != nil {
		return err
	}
	closure := `WITH RECURSIVE closure(item_id, ancestor_id) AS (
		SELECT id, parent_id FROM items WHERE parent_id IS NOT NULL
		UNION ALL
		SELECT closure.item_id, items.parent_id FROM closure JOIN items ON closure.ancestor_id = items.id WHERE items.parent_id IS NOT NULL
	) INSERT INTO ancestors(item_id, ancestor_id) SELECT item_id, ancestor_id FROM closure`
	if _, err := tx.ExecContext(ctx, closure); err != nil {
		return err
	}

	nodes := make(map[dto.UUID]derivedNode)
	if err := s.scanDerivedNodes(ctx, tx, "SELECT id, kind, parent_id, name, clean_name, top_parent_id, series_id, season_id FROM items", nil, nodes); err != nil {
		return err
	}
	ids := make([]dto.UUID, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return bytes.Compare(ids[left][:], ids[right][:]) < 0 })
	if err := s.repairScopedColumns(ctx, tx, ids, nodes); err != nil {
		return err
	}
	if err := s.repairCleanSatellites(ctx, tx); err != nil {
		return err
	}
	return s.rebuildUserDataKeys(ctx, tx)
}

func scanUUID(raw any) (dto.UUID, error) {
	switch v := raw.(type) {
	case []byte:
		if len(v) == 16 {
			var u dto.UUID
			copy(u[:], v)
			return u, nil
		}
		return dto.ParseUUID(string(v))
	case string:
		return dto.ParseUUID(v)
	default:
		return dto.UUID{}, fmt.Errorf("scan UUID %T", raw)
	}
}
func isNil(v any) bool { return v == nil }
func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func asInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case []byte:
		n, _ := strconv.ParseInt(string(x), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	default:
		return 0
	}
}

// asInt and asUint8 read an out-of-range column value as 0, the same
// result as an unparseable value, instead of a wrapped integer.
func asInt(v any) int {
	n := asInt64(v)
	if n < math.MinInt || n > math.MaxInt {
		return 0
	}
	return int(n)
}

func asUint8(v any) uint8 {
	n := asInt64(v)
	if n < 0 || n > math.MaxUint8 {
		return 0
	}
	return uint8(n)
}

func asFloat32(v any) float32 {
	switch x := v.(type) {
	case float64:
		return float32(x)
	case float32:
		return x
	case []byte:
		n, _ := strconv.ParseFloat(string(x), 32)
		return float32(n)
	case string:
		n, _ := strconv.ParseFloat(x, 32)
		return float32(n)
	default:
		return 0
	}
}

func asBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	case int:
		return x != 0
	case []byte:
		return string(x) == "1" || string(x) == "true"
	case string:
		return x == "1" || x == "true"
	default:
		return false
	}
}

func optionalInt(v any) *int {
	if isNil(v) {
		return nil
	}
	n := asInt(v)
	return &n
}

func optionalFloat32(v any) *float32 {
	if isNil(v) {
		return nil
	}
	n := asFloat32(v)
	return &n
}

func optionalTime(v any, d Dialect) *time.Time {
	if isNil(v) {
		return nil
	}
	n, e := d.ScanTime(v)
	if e != nil {
		return nil
	}
	return &n
}
