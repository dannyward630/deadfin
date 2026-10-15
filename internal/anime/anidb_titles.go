// SPDX-License-Identifier: GPL-2.0-only

package anime

import (
	"bufio"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// Title is one title of an anime with its language tag and AniDB title type
// ("main", "official", "syn", "short", "kana", "card").
type Title struct {
	Value    string
	Language string
	Type     string
}

// AniDBAnime is the title set of one AniDB anime.
type AniDBAnime struct {
	AniDB  int
	Titles []Title
}

type anidbWireAnime struct {
	AID    int `xml:"aid,attr"`
	Titles []struct {
		Value    string `xml:",chardata"`
		Language string `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
		Type     string `xml:"type,attr"`
	} `xml:"title"`
}

// ParseAniDBTitles decodes AniDB's anime-titles XML dump and calls visit for
// each anime. The reader may be the gzip file as AniDB serves it or the plain
// XML; a gzip header is detected and unwrapped.
func ParseAniDBTitles(reader io.Reader, visit func(AniDBAnime)) error {
	buffered := bufio.NewReader(reader)
	if magic, err := buffered.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		unzipped, err := gzip.NewReader(buffered)
		if err != nil {
			return fmt.Errorf("anidb titles: open gzip: %w", err)
		}
		defer unzipped.Close()
		reader = unzipped
	} else {
		reader = buffered
	}
	decoder := xml.NewDecoder(reader)
	sawRoot := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("anidb titles: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "animetitles":
			sawRoot = true
		case "anime":
			var wire anidbWireAnime
			if err := decoder.DecodeElement(&wire, &start); err != nil {
				return fmt.Errorf("anidb titles: decode anime: %w", err)
			}
			if wire.AID <= 0 {
				continue
			}
			anime := AniDBAnime{AniDB: wire.AID, Titles: make([]Title, 0, len(wire.Titles))}
			for _, title := range wire.Titles {
				value := strings.TrimSpace(title.Value)
				if value == "" {
					continue
				}
				anime.Titles = append(anime.Titles, Title{Value: value, Language: title.Language, Type: title.Type})
			}
			visit(anime)
		}
	}
	if !sawRoot {
		return fmt.Errorf("anidb titles: document has no animetitles root")
	}
	return nil
}
