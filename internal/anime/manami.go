// SPDX-License-Identifier: GPL-2.0-only

package anime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ManamiEntry is one anime from the manami anime-offline-database. Only the
// fields the crosswalk uses are decoded.
type ManamiEntry struct {
	AniDB    int
	AniList  int
	MAL      int
	Kitsu    int
	Title    string
	Type     string
	Episodes int
	Season   string
	Year     int
	Synonyms []string
	// RelatedAniList holds the AniList ids of the related-anime links. manami
	// does not type its links; the AniList provider supplies relation types.
	RelatedAniList []int
}

type manamiWireEntry struct {
	Sources     []string `json:"sources"`
	Title       string   `json:"title"`
	Type        string   `json:"type"`
	Episodes    int      `json:"episodes"`
	AnimeSeason struct {
		Season string `json:"season"`
		Year   int    `json:"year"`
	} `json:"animeSeason"`
	Synonyms     []string `json:"synonyms"`
	RelatedAnime []string `json:"relatedAnime"`
}

// ParseManami decodes an anime-offline-database JSON document (full or
// minified) and calls visit for each entry. It streams the data array, so the
// whole document is never held in memory at once.
func ParseManami(reader io.Reader, visit func(ManamiEntry)) error {
	decoder := json.NewDecoder(reader)
	if err := expectDelim(decoder, '{'); err != nil {
		return fmt.Errorf("manami: %w", err)
	}
	foundData := false
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("manami: read key: %w", err)
		}
		key, _ := token.(string)
		if key != "data" {
			var skip json.RawMessage
			if err := decoder.Decode(&skip); err != nil {
				return fmt.Errorf("manami: skip %q: %w", key, err)
			}
			continue
		}
		foundData = true
		if err := expectDelim(decoder, '['); err != nil {
			return fmt.Errorf("manami: data: %w", err)
		}
		for decoder.More() {
			var wire manamiWireEntry
			if err := decoder.Decode(&wire); err != nil {
				return fmt.Errorf("manami: decode entry: %w", err)
			}
			visit(wire.entry())
		}
		if err := expectDelim(decoder, ']'); err != nil {
			return fmt.Errorf("manami: data: %w", err)
		}
	}
	if !foundData {
		return errors.New("manami: document has no data array")
	}
	return nil
}

func (wire manamiWireEntry) entry() ManamiEntry {
	entry := ManamiEntry{
		Title:    strings.TrimSpace(wire.Title),
		Type:     strings.ToUpper(strings.TrimSpace(wire.Type)),
		Episodes: wire.Episodes,
		Season:   strings.ToUpper(strings.TrimSpace(wire.AnimeSeason.Season)),
		Year:     wire.AnimeSeason.Year,
		Synonyms: wire.Synonyms,
	}
	for _, source := range wire.Sources {
		site, id, ok := sourceID(source)
		if !ok {
			continue
		}
		switch site {
		case siteAniDB:
			entry.AniDB = id
		case siteAniList:
			entry.AniList = id
		case siteMAL:
			entry.MAL = id
		case siteKitsu:
			entry.Kitsu = id
		}
	}
	for _, related := range wire.RelatedAnime {
		if site, id, ok := sourceID(related); ok && site == siteAniList {
			entry.RelatedAniList = append(entry.RelatedAniList, id)
		}
	}
	return entry
}

type site uint8

const (
	siteAniDB site = iota + 1
	siteAniList
	siteMAL
	siteKitsu
)

// sourceID parses a manami source URL into its site and numeric id.
func sourceID(raw string) (site, int, bool) {
	prefixes := [...]struct {
		prefix string
		site   site
	}{
		{"https://anidb.net/anime/", siteAniDB},
		{"https://anilist.co/anime/", siteAniList},
		{"https://myanimelist.net/anime/", siteMAL},
		{"https://kitsu.app/anime/", siteKitsu},
		{"https://kitsu.io/anime/", siteKitsu},
	}
	for _, candidate := range prefixes {
		rest, found := strings.CutPrefix(raw, candidate.prefix)
		if !found {
			continue
		}
		id, err := strconv.Atoi(rest)
		if err != nil || id <= 0 {
			return 0, 0, false
		}
		return candidate.site, id, true
	}
	return 0, 0, false
}

func expectDelim(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); !ok || delim != want {
		return fmt.Errorf("expected %q, found %v", want, token)
	}
	return nil
}
