// SPDX-License-Identifier: GPL-2.0-only

package anime

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"
)

// Method names how a series was identified.
type Method string

const (
	MethodAniList Method = "anilist"
	MethodAniDB   Method = "anidb"
	MethodMAL     Method = "mal"
	MethodTVDB    Method = "tvdb"
	MethodTMDb    Method = "tmdb"
	MethodTitle   Method = "title"
)

// Query carries what is already known about a series. Zero ids are unknown.
type Query struct {
	AniList int
	AniDB   int
	MAL     int
	TVDB    int
	TMDb    int
	// Names are candidate titles in preference order (the item name, then
	// the folder name). A trailing "(2013)" year is parsed and removed.
	Names []string
	// Year is the premiere year when known; it breaks title ties.
	Year int
	// AllowTitle permits the title fallback. Callers set it only where the
	// library is known to hold anime, because a title match is weaker
	// evidence than an id.
	AllowTitle bool
}

// Identity is the result of identification.
type Identity struct {
	Anime  Anime
	Method Method
	// TVDB and TMDb are the series ids the per-season mapping uses: the
	// query's ids, or the Anime-Lists ids of the identified anime.
	TVDB int
	TMDb int
	// WrongTVDB and WrongTMDb are query ids that Identify replaced: the id maps
	// to no anime in Anime-Lists, and Anime-Lists lists the identified anime
	// with a different id. Such an id is usually a same-name match of another
	// show ("Hundred" labelled "Cain's Hundred"). Zero means no replacement.
	WrongTVDB int
	WrongTMDb int
}

// Identify maps a series to its anime entry. Ids are tried before titles:
// AniList, AniDB, and MyAnimeList ids resolve directly; TVDB and TMDb ids
// resolve through Anime-Lists to the entry at season 1 episode 1; then, when
// AllowTitle is set, the folded names are matched against the title index.
func (cw *Crosswalk) Identify(query Query) (Identity, bool) {
	if cw == nil {
		return Identity{}, false
	}
	identity, found := cw.identifyByID(query)
	if !found && query.AllowTitle {
		identity, found = cw.identifyByTitle(query)
	}
	if !found {
		return Identity{}, false
	}
	identity.TVDB, identity.TMDb = query.TVDB, query.TMDb
	if entry, listed := cw.ListEntry(identity.Anime.AniDB); listed {
		// The anime ids win over a series id that maps to no anime. An id
		// that Anime-Lists does not contradict stays: the list is incomplete
		// for new series.
		if identity.TVDB > 0 && entry.TVDB > 0 && entry.TVDB != identity.TVDB && len(cw.TVDBEntries(identity.TVDB)) == 0 {
			identity.WrongTVDB, identity.TVDB = identity.TVDB, 0
		}
		if identity.TMDb > 0 && entry.TMDbTV > 0 && entry.TMDbTV != identity.TMDb && len(cw.TMDbEntries(identity.TMDb)) == 0 {
			identity.WrongTMDb, identity.TMDb = identity.TMDb, 0
		}
		if identity.TVDB == 0 {
			identity.TVDB = entry.TVDB
		}
		if identity.TMDb == 0 {
			identity.TMDb = entry.TMDbTV
		}
	}
	return identity, true
}

func (cw *Crosswalk) identifyByID(query Query) (Identity, bool) {
	if anime, found := cw.ByAniList(query.AniList); found {
		return Identity{Anime: anime, Method: MethodAniList}, true
	}
	if anime, found := cw.anidbAnime(query.AniDB); found {
		return Identity{Anime: anime, Method: MethodAniDB}, true
	}
	if anime, found := cw.ByMAL(query.MAL); found {
		return Identity{Anime: anime, Method: MethodMAL}, true
	}
	if query.TVDB > 0 {
		if entry, found := anchor(cw.TVDBEntries(query.TVDB), tvdbSeason, tvdbOffset); found {
			if anime, found := cw.anidbAnime(entry.AniDB); found {
				return Identity{Anime: anime, Method: MethodTVDB}, true
			}
		}
	}
	if query.TMDb > 0 {
		if entry, found := anchor(cw.entries(cw.byTMDbTV[query.TMDb]), tmdbSeason, tmdbOffset); found {
			if anime, found := cw.anidbAnime(entry.AniDB); found {
				return Identity{Anime: anime, Method: MethodTMDb}, true
			}
		}
	}
	return Identity{}, false
}

// anidbAnime returns the anime with an AniDB id, or a bare record when only
// Anime-Lists knows the id.
func (cw *Crosswalk) anidbAnime(id int) (Anime, bool) {
	if anime, found := cw.ByAniDB(id); found {
		return anime, true
	}
	if entry, found := cw.ListEntry(id); found {
		return Anime{AniDB: id, Title: entry.Name}, true
	}
	return Anime{}, false
}

var trailingYear = regexp.MustCompile(`^(.*?)\s*[\(\[]((?:19|20)\d\d)[\)\]]\s*$`)

// splitYear removes a trailing "(2013)" or "[2013]" from a name.
func splitYear(name string) (string, int) {
	match := trailingYear.FindStringSubmatch(strings.TrimSpace(name))
	if match == nil {
		return strings.TrimSpace(name), 0
	}
	year, _ := strconv.Atoi(match[2])
	return match[1], year
}

type candidate struct {
	position int32
	rank     titleRank
}

func (cw *Crosswalk) identifyByTitle(query Query) (Identity, bool) {
	for _, raw := range query.Names {
		name, year := splitYear(raw)
		if year == 0 {
			year = query.Year
		}
		key := FoldTitle(name)
		if key == "" {
			continue
		}
		var best candidate
		found := false
		short := isShortLatin(key)
		for _, ref := range cw.index[key] {
			// A short Latin synonym is usually an abbreviation ("SnK").
			if ref.rank > maxIdentifyRank || short && ref.rank != rankMain {
				continue
			}
			current := candidate{position: ref.anime, rank: ref.rank}
			if !found || cw.betterTitleMatch(current, best, year) {
				best, found = current, true
			}
		}
		if found {
			return Identity{Anime: cw.anime[best.position], Method: MethodTitle}, true
		}
	}
	return Identity{}, false
}

// betterTitleMatch orders title candidates: a known year that matches beats
// one that does not, then title rank, then format (TV first), then the
// earliest year, then the lowest AniDB and AniList ids for determinism.
func (cw *Crosswalk) betterTitleMatch(left, right candidate, year int) bool {
	a, b := cw.anime[left.position], cw.anime[right.position]
	return cmp.Or(
		cmp.Compare(yearDistance(a.Year, year), yearDistance(b.Year, year)),
		cmp.Compare(left.rank, right.rank),
		cmp.Compare(typeRank(a.Type), typeRank(b.Type)),
		cmp.Compare(orLarge(a.Year), orLarge(b.Year)),
		cmp.Compare(orLarge(a.AniDB), orLarge(b.AniDB)),
		cmp.Compare(orLarge(a.AniList), orLarge(b.AniList)),
	) < 0
}

// yearDistance buckets how far a candidate's year is from the wanted year.
// Without a wanted year every candidate ties.
func yearDistance(have, want int) int {
	switch {
	case want == 0:
		return 0
	case have == 0:
		return 2
	case have == want:
		return 0
	case have-want <= 1 && want-have <= 1:
		return 1
	default:
		return 3
	}
}

func orLarge(value int) int {
	if value <= 0 {
		return 1 << 30
	}
	return value
}

// isShortLatin reports a folded key of at most three ASCII characters.
func isShortLatin(key string) bool {
	if len(key) > 3 {
		return false
	}
	for index := 0; index < len(key); index++ {
		if key[index] >= 0x80 {
			return false
		}
	}
	return true
}
