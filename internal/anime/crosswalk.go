// SPDX-License-Identifier: GPL-2.0-only

package anime

import (
	"cmp"
	"slices"
	"strings"
)

// Anime is one joined crosswalk record. Zero ids are unknown.
type Anime struct {
	AniDB    int
	AniList  int
	MAL      int
	Kitsu    int
	Title    string
	Type     string
	Episodes int
	Season   string
	Year     int
	// RelatedAniList holds manami's untyped related-anime links.
	RelatedAniList []int
}

// Segment is one AniDB entry inside a TVDB or TMDb season. A split-cour
// season has several segments; EpisodeOffset orders them.
type Segment struct {
	AniDB         int
	AniList       int
	MAL           int
	EpisodeOffset int
	Name          string
}

// Crosswalk is an immutable join of the three seed datasets. It is safe for
// concurrent use.
type Crosswalk struct {
	anime     []Anime
	byAniDB   map[int]int32
	byAniList map[int]int32
	byMAL     map[int]int32
	titles    map[int][]Title // AniDB titles by AniDB id
	lists     []ListEntry
	byTVDB    map[int][]int32
	byTMDbTV  map[int][]int32
	listByID  map[int]int32
	index     map[string][]titleRef
}

// titleRank orders title quality for identification. Lower is better.
type titleRank uint8

const (
	rankMain titleRank = iota
	rankOfficial
	rankSynonym
	// rankShort covers abbreviations such as "SnK". They are searchable but
	// too ambiguous to identify a series.
	rankShort
)

// maxIdentifyRank is the worst title rank title identification accepts.
const maxIdentifyRank = rankSynonym

type titleRef struct {
	anime int32
	rank  titleRank
}

// Builder accumulates seed data. Add the datasets in any order, then call
// Build once.
type Builder struct {
	manami []ManamiEntry
	anidb  []AniDBAnime
	lists  []ListEntry
}

// AddManami records one manami entry. Synonyms are indexed and then dropped.
func (b *Builder) AddManami(entry ManamiEntry) { b.manami = append(b.manami, entry) }

// AddAniDBTitles records the titles of one AniDB anime.
func (b *Builder) AddAniDBTitles(anime AniDBAnime) { b.anidb = append(b.anidb, anime) }

// AddListEntry records one Anime-Lists mapping.
func (b *Builder) AddListEntry(entry ListEntry) { b.lists = append(b.lists, entry) }

// Build joins the recorded data into a Crosswalk. The builder must not be
// used afterwards.
func (b *Builder) Build() *Crosswalk {
	cw := &Crosswalk{
		anime:     make([]Anime, 0, len(b.manami)),
		byAniDB:   make(map[int]int32, len(b.manami)),
		byAniList: make(map[int]int32, len(b.manami)),
		byMAL:     make(map[int]int32, len(b.manami)),
		titles:    make(map[int][]Title, len(b.anidb)),
		lists:     b.lists,
		byTVDB:    make(map[int][]int32),
		byTMDbTV:  make(map[int][]int32),
		listByID:  make(map[int]int32, len(b.lists)),
		index:     make(map[string][]titleRef, len(b.manami)*4),
	}
	for _, entry := range b.manami {
		// An entry known only to sites without a crosswalk id (for example
		// anime-planet) cannot be identified to anything useful.
		if entry.AniDB <= 0 && entry.AniList <= 0 && entry.MAL <= 0 {
			continue
		}
		position := int32(len(cw.anime))
		cw.anime = append(cw.anime, Anime{
			AniDB: entry.AniDB, AniList: entry.AniList, MAL: entry.MAL, Kitsu: entry.Kitsu,
			Title: entry.Title, Type: entry.Type, Episodes: entry.Episodes,
			Season: entry.Season, Year: entry.Year, RelatedAniList: entry.RelatedAniList,
		})
		claim(cw.byAniDB, entry.AniDB, position)
		claim(cw.byAniList, entry.AniList, position)
		claim(cw.byMAL, entry.MAL, position)
		cw.addTitle(entry.Title, position, rankMain)
		for _, synonym := range entry.Synonyms {
			cw.addTitle(synonym, position, rankSynonym)
		}
	}
	for _, anime := range b.anidb {
		position, found := cw.byAniDB[anime.AniDB]
		if !found {
			position = int32(len(cw.anime))
			cw.anime = append(cw.anime, Anime{AniDB: anime.AniDB, Title: mainTitle(anime.Titles)})
			cw.byAniDB[anime.AniDB] = position
		}
		cw.titles[anime.AniDB] = anime.Titles
		for _, title := range anime.Titles {
			cw.addTitle(title.Value, position, anidbRank(title))
		}
	}
	for position, entry := range cw.lists {
		claim(cw.listByID, entry.AniDB, int32(position))
		if entry.TVDB > 0 {
			cw.byTVDB[entry.TVDB] = append(cw.byTVDB[entry.TVDB], int32(position))
		}
		if entry.TMDbTV > 0 {
			cw.byTMDbTV[entry.TMDbTV] = append(cw.byTMDbTV[entry.TMDbTV], int32(position))
		}
	}
	*b = Builder{}
	return cw
}

// claim maps id to position unless the id is zero or already mapped. manami
// lists a few ids under more than one entry; the first entry wins.
func claim(index map[int]int32, id int, position int32) {
	if id <= 0 {
		return
	}
	if _, taken := index[id]; !taken {
		index[id] = position
	}
}

func (cw *Crosswalk) addTitle(title string, position int32, rank titleRank) {
	key := FoldTitle(title)
	if key == "" {
		return
	}
	refs := cw.index[key]
	for index := range refs {
		if refs[index].anime == position {
			refs[index].rank = min(refs[index].rank, rank)
			return
		}
	}
	cw.index[key] = append(refs, titleRef{anime: position, rank: rank})
}

func anidbRank(title Title) titleRank {
	switch title.Type {
	case "main":
		return rankMain
	case "official":
		return rankOfficial
	case "short":
		return rankShort
	default:
		return rankSynonym
	}
}

func mainTitle(titles []Title) string {
	for _, title := range titles {
		if title.Type == "main" {
			return title.Value
		}
	}
	if len(titles) != 0 {
		return titles[0].Value
	}
	return ""
}

// Stats reports dataset sizes for logs.
type Stats struct {
	Anime, AniDBTitled, ListEntries, TitleKeys int
}

// Stats returns the crosswalk's dataset sizes.
func (cw *Crosswalk) Stats() Stats {
	if cw == nil {
		return Stats{}
	}
	return Stats{Anime: len(cw.anime), AniDBTitled: len(cw.titles), ListEntries: len(cw.lists), TitleKeys: len(cw.index)}
}

// ByAniDB returns the anime with an AniDB id.
func (cw *Crosswalk) ByAniDB(id int) (Anime, bool) { return cw.lookup(cw.byAniDB, id) }

// ByAniList returns the anime with an AniList id.
func (cw *Crosswalk) ByAniList(id int) (Anime, bool) { return cw.lookup(cw.byAniList, id) }

// ByMAL returns the anime with a MyAnimeList id.
func (cw *Crosswalk) ByMAL(id int) (Anime, bool) { return cw.lookup(cw.byMAL, id) }

func (cw *Crosswalk) lookup(index map[int]int32, id int) (Anime, bool) {
	if cw == nil || id <= 0 {
		return Anime{}, false
	}
	position, found := index[id]
	if !found {
		return Anime{}, false
	}
	return cw.anime[position], true
}

// AniDBTitles returns every AniDB title of an anime, in dump order.
func (cw *Crosswalk) AniDBTitles(anidb int) []Title {
	if cw == nil {
		return nil
	}
	return slices.Clone(cw.titles[anidb])
}

// TVDBEntries returns the Anime-Lists entries of a TVDB series.
func (cw *Crosswalk) TVDBEntries(tvdb int) []ListEntry {
	if cw == nil {
		return nil
	}
	return cw.entries(cw.byTVDB[tvdb])
}

// TMDbEntries returns the Anime-Lists entries of a TMDb TV series.
func (cw *Crosswalk) TMDbEntries(tmdb int) []ListEntry {
	if cw == nil {
		return nil
	}
	return cw.entries(cw.byTMDbTV[tmdb])
}

func (cw *Crosswalk) entries(positions []int32) []ListEntry {
	out := make([]ListEntry, len(positions))
	for index, position := range positions {
		out[index] = cw.lists[position]
	}
	return out
}

// ListEntry returns the Anime-Lists entry of an AniDB anime.
func (cw *Crosswalk) ListEntry(anidb int) (ListEntry, bool) {
	if cw == nil {
		return ListEntry{}, false
	}
	position, found := cw.listByID[anidb]
	if !found {
		return ListEntry{}, false
	}
	return cw.lists[position], true
}

// SeasonSegments returns the AniDB entries whose episodes start in a TVDB
// season, ordered by episode offset. Season 0 holds specials and movies. A
// series that Anime-Lists maps with absolute numbering has no per-season
// segments; it returns nil.
func (cw *Crosswalk) SeasonSegments(tvdb, season int) []Segment {
	if cw == nil || tvdb <= 0 || season < 0 {
		return nil
	}
	var segments []Segment
	for _, position := range cw.byTVDB[tvdb] {
		entry := cw.lists[position]
		if !entry.HasTVDBSeason || entry.TVDBSeason != season {
			continue
		}
		segments = append(segments, cw.segment(entry, entry.EpisodeOffset))
	}
	sortSegments(segments)
	return segments
}

// TMDbSeasonSegments is SeasonSegments for TMDb TV numbering.
func (cw *Crosswalk) TMDbSeasonSegments(tmdb, season int) []Segment {
	if cw == nil || tmdb <= 0 || season < 0 {
		return nil
	}
	var segments []Segment
	for _, position := range cw.byTMDbTV[tmdb] {
		entry := cw.lists[position]
		if !entry.HasTMDbSeason || entry.TMDbSeason != season {
			continue
		}
		segments = append(segments, cw.segment(entry, entry.TMDbOffset))
	}
	sortSegments(segments)
	return segments
}

func (cw *Crosswalk) segment(entry ListEntry, episodeOffset int) Segment {
	segment := Segment{AniDB: entry.AniDB, EpisodeOffset: episodeOffset, Name: entry.Name}
	if anime, found := cw.ByAniDB(entry.AniDB); found {
		segment.AniList, segment.MAL = anime.AniList, anime.MAL
		if segment.Name == "" {
			segment.Name = anime.Title
		}
	}
	return segment
}

func sortSegments(segments []Segment) {
	slices.SortFunc(segments, func(left, right Segment) int {
		return cmp.Or(cmp.Compare(left.EpisodeOffset, right.EpisodeOffset), cmp.Compare(left.AniDB, right.AniDB))
	})
}

// anchor selects the Anime-Lists entry that represents a whole TVDB or TMDb
// series: the entry at episode 1 of season 1, else the earliest absolute
// entry, else the earliest regular season, else the first special.
func anchor(entries []ListEntry, season func(ListEntry) (int, bool), episodeOffset func(ListEntry) int) (ListEntry, bool) {
	best, found := ListEntry{}, false
	bestKey := [3]int{}
	for _, entry := range entries {
		value, ok := season(entry)
		if !ok {
			continue
		}
		var class int
		switch {
		case value == 1:
			class = 0
		case value == AbsoluteSeason:
			class = 1
		case value > 1:
			class = 2
		default:
			class = 3
		}
		key := [3]int{class, value, episodeOffset(entry)}
		if !found || slices.Compare(key[:], bestKey[:]) < 0 || slices.Compare(key[:], bestKey[:]) == 0 && entry.AniDB < best.AniDB {
			best, bestKey, found = entry, key, true
		}
	}
	return best, found
}

func tvdbSeason(entry ListEntry) (int, bool) { return entry.TVDBSeason, entry.HasTVDBSeason }
func tmdbSeason(entry ListEntry) (int, bool) { return entry.TMDbSeason, entry.HasTMDbSeason }
func tvdbOffset(entry ListEntry) int         { return entry.EpisodeOffset }
func tmdbOffset(entry ListEntry) int         { return entry.TMDbOffset }

// typeRank orders anime formats for series identification.
func typeRank(kind string) int {
	switch strings.ToUpper(kind) {
	case "TV":
		return 0
	case "ONA":
		return 1
	case "OVA":
		return 2
	case "SPECIAL":
		return 3
	case "MOVIE":
		return 4
	default:
		return 5
	}
}
