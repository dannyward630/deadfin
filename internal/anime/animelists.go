// SPDX-License-Identifier: GPL-2.0-only

package anime

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// AbsoluteSeason marks an Anime-Lists entry whose default TVDB season is "a":
// the TVDB series uses absolute episode numbering for it.
const AbsoluteSeason = -1

// ListEntry is one Anime-Lists mapping: an AniDB anime and where its episodes
// sit in TVDB and TMDb numbering.
type ListEntry struct {
	AniDB int
	Name  string
	// TVDB is the TVDB series id; zero when the entry has none (Anime-Lists
	// uses words such as "movie", "OVA", or "hentai" in place of an id).
	TVDB int
	// TVDBSeason is the default TVDB season of episode 1, or AbsoluteSeason.
	// HasTVDBSeason is false when the list leaves it empty.
	TVDBSeason    int
	HasTVDBSeason bool
	// EpisodeOffset is added to an AniDB episode number to give the TVDB
	// episode number in TVDBSeason.
	EpisodeOffset int
	TMDbTV        int
	TMDbSeason    int
	HasTMDbSeason bool
	TMDbOffset    int
	TMDbMovie     int
	IMDb          []string
}

type animeListWireEntry struct {
	AniDB         string `xml:"anidbid,attr"`
	TVDB          string `xml:"tvdbid,attr"`
	TVDBSeason    string `xml:"defaulttvdbseason,attr"`
	EpisodeOffset string `xml:"episodeoffset,attr"`
	TMDbTV        string `xml:"tmdbtv,attr"`
	TMDbSeason    string `xml:"tmdbseason,attr"`
	TMDbOffset    string `xml:"tmdboffset,attr"`
	TMDbMovie     string `xml:"tmdbid,attr"`
	IMDb          string `xml:"imdbid,attr"`
	Name          string `xml:"name"`
}

// ParseAnimeLists decodes anime-list-master.xml and calls visit for each entry
// that has a valid AniDB id.
func ParseAnimeLists(reader io.Reader, visit func(ListEntry)) error {
	decoder := xml.NewDecoder(reader)
	sawRoot := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("anime-lists: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "anime-list":
			sawRoot = true
		case "anime":
			var wire animeListWireEntry
			if err := decoder.DecodeElement(&wire, &start); err != nil {
				return fmt.Errorf("anime-lists: decode anime: %w", err)
			}
			if entry, ok := wire.entry(); ok {
				visit(entry)
			}
		}
	}
	if !sawRoot {
		return fmt.Errorf("anime-lists: document has no anime-list root")
	}
	return nil
}

func (wire animeListWireEntry) entry() (ListEntry, bool) {
	anidb, ok := positive(wire.AniDB)
	if !ok {
		return ListEntry{}, false
	}
	entry := ListEntry{AniDB: anidb, Name: strings.TrimSpace(wire.Name)}
	entry.TVDB, _ = positive(wire.TVDB)
	switch season := strings.TrimSpace(wire.TVDBSeason); {
	case strings.EqualFold(season, "a"):
		entry.TVDBSeason, entry.HasTVDBSeason = AbsoluteSeason, true
	case season != "":
		if value, err := strconv.Atoi(season); err == nil && value >= 0 {
			entry.TVDBSeason, entry.HasTVDBSeason = value, true
		}
	}
	entry.EpisodeOffset = offset(wire.EpisodeOffset)
	entry.TMDbTV, _ = positive(wire.TMDbTV)
	if season := strings.TrimSpace(wire.TMDbSeason); season != "" {
		if value, err := strconv.Atoi(season); err == nil && value >= 0 {
			entry.TMDbSeason, entry.HasTMDbSeason = value, true
		}
	}
	entry.TMDbOffset = offset(wire.TMDbOffset)
	entry.TMDbMovie, _ = positive(wire.TMDbMovie)
	for _, imdb := range strings.Split(wire.IMDb, ",") {
		if imdb = strings.TrimSpace(imdb); strings.HasPrefix(imdb, "tt") {
			entry.IMDb = append(entry.IMDb, imdb)
		}
	}
	return entry, true
}

func positive(raw string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func offset(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}
