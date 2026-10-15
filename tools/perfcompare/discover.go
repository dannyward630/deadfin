// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode"
)

// baseItem is the subset of BaseItemDto that discovery reads.
type baseItem struct {
	ID                string            `json:"Id"`
	Name              string            `json:"Name"`
	Type              string            `json:"Type"`
	Path              string            `json:"Path"`
	CollectionType    string            `json:"CollectionType"`
	SeriesID          string            `json:"SeriesId"`
	SeasonID          string            `json:"SeasonId"`
	IndexNumber       *int              `json:"IndexNumber"`
	ParentIndexNumber *int              `json:"ParentIndexNumber"`
	ChildCount        *int              `json:"ChildCount"`
	ImageTags         map[string]string `json:"ImageTags"`
	ProviderIDs       map[string]string `json:"ProviderIds"`
	MediaSources      []mediaSource     `json:"MediaSources"`
}

type mediaSource struct {
	Container    string        `json:"Container"`
	MediaStreams []mediaStream `json:"MediaStreams"`
}

type mediaStream struct {
	Type       string `json:"Type"`
	Codec      string `json:"Codec"`
	VideoRange string `json:"VideoRange"`
}

type itemList struct {
	Items            []baseItem `json:"Items"`
	TotalRecordCount int        `json:"TotalRecordCount"`
}

// library is one user view. Key is the reference server's view name, so
// that scenario names line up across servers.
type library struct {
	Key            string `json:"key"`
	ID             string `json:"id"`
	CollectionType string `json:"collectionType"`
}

// target holds the ids that scenarios use on one server.
type target struct {
	// Unmapped names, per probe entity (movie, transcode-movie, series,
	// episode, album, image), why this server has no id for an entity that
	// the reference server has. The scenarios that need the entity fail
	// with this reason; they are not left out.
	Unmapped       map[string]string
	UserID         string
	Libraries      []library
	Movie          baseItem // direct-play candidate
	TranscodeMovie baseItem // SDR title for the transcode scenarios
	Series         baseItem
	Episode        baseItem
	Album          baseItem
	SearchTerm     string
}

// mapEntry records how one reference entity was found on the other server.
type mapEntry struct {
	Entity   string `json:"entity"`
	Name     string `json:"name"`
	Library  string `json:"library,omitempty"`
	RefID    string `json:"jellyfinId"`
	OtherID  string `json:"deadfinId,omitempty"`
	Method   string `json:"method"` // same-id, path, path-suffix, provider-id, series-index, name, collection-type, unmapped
	Mismatch string `json:"error,omitempty"`
}

const listFields = "Path,MediaSources,ChildCount,ProviderIds"

// probeTries bounds how many candidates of one kind discovery tries to map
// before it reports the entity as unmapped.
const probeTries = 5

// discover picks representative items on ref (Jellyfin) and finds the same
// items on other (Deadfin). It takes probe items only from libraries that
// both servers have, so that a library only one server imported cannot
// supply a probe item. An item that it cannot find on other is recorded in
// other's Unmapped, and the scenarios that need it fail. It reads only.
func discover(ctx context.Context, ref, other *server, userName string) (target, target, []mapEntry, []string, error) {
	var rt, ot target
	ot.Unmapped = map[string]string{}
	var mapping []mapEntry
	var warnings []string

	var err error
	rt.UserID, userName, err = resolveUser(ctx, ref, userName)
	if err != nil {
		return rt, ot, nil, nil, err
	}
	ot.UserID, _, err = resolveUser(ctx, other, userName)
	if err != nil {
		return rt, ot, nil, nil, err
	}

	var refViews, otherViews itemList
	if err := ref.getJSON(ctx, "/UserViews?userId="+rt.UserID, &refViews); err != nil {
		return rt, ot, nil, nil, err
	}
	if err := other.getJSON(ctx, "/UserViews?userId="+ot.UserID, &otherViews); err != nil {
		return rt, ot, nil, nil, err
	}
	var pairs []libPair
	for _, v := range refViews.Items {
		rl := library{Key: v.Name, ID: v.ID, CollectionType: v.CollectionType}
		rt.Libraries = append(rt.Libraries, rl)
		m, method, ok := matchView(v, otherViews.Items)
		entry := mapEntry{Entity: "library", Name: v.Name, RefID: v.ID, Method: "unmapped"}
		p := libPair{ref: rl}
		if ok {
			p.other = library{Key: v.Name, ID: m.ID, CollectionType: m.CollectionType}
			p.method = method
			ot.Libraries = append(ot.Libraries, p.other)
			entry.OtherID, entry.Method = m.ID, method
		}
		pairs = append(pairs, p)
		mapping = append(mapping, entry)
	}

	record := func(entity string, it baseItem, lib string, found baseItem, method, reason string) {
		entry := mapEntry{Entity: entity, Name: it.Name, Library: lib, RefID: it.ID, Method: "unmapped"}
		if reason == "" {
			entry.OtherID, entry.Method = found.ID, method
		} else {
			entry.Mismatch = reason
			ot.Unmapped[entity] = fmt.Sprintf("%s %q in %q: %s", entity, it.Name, lib, reason)
			warnings = append(warnings, fmt.Sprintf("%s %q in library %q: not found on %s: %s", entity, it.Name, lib, other.label, reason))
		}
		mapping = append(mapping, entry)
	}
	// find maps one item of a shared library, or says why it cannot.
	find := func(p libPair, it baseItem) (baseItem, string, string) {
		if p.other.ID == "" {
			return baseItem{}, "", fmt.Sprintf("library %q is not on %s", p.ref.Key, other.label)
		}
		found, method, err := mapItem(ctx, other, ot.UserID, it)
		if err != nil {
			return baseItem{}, "", err.Error()
		}
		return found, method, ""
	}

	// Movies: the first shared library whose direct-play pick maps.
	if libs := probeLibraries(pairs, "movies"); len(libs) == 0 {
		warnings = append(warnings, "no movies library on "+ref.label)
	} else {
		type movieProbe struct {
			lib                 libPair
			direct, transcode   baseItem
			oDirect, oTranscode baseItem
			dMethod, tMethod    string
			dReason, tReason    string
		}
		var probes []movieProbe
		for _, lib := range libs {
			movies, err := listItems(ctx, ref, rt.UserID, lib.ref.ID, "Movie", 300)
			if err != nil {
				return rt, ot, nil, nil, err
			}
			pr := movieProbe{lib: lib}
			pr.direct, pr.transcode = pickMovies(movies)
			if pr.direct.ID == "" {
				continue
			}
			pr.oDirect, pr.dMethod, pr.dReason = find(lib, pr.direct)
			pr.oTranscode, pr.tMethod, pr.tReason = pr.oDirect, pr.dMethod, pr.dReason
			if pr.transcode.ID != pr.direct.ID {
				pr.oTranscode, pr.tMethod, pr.tReason = find(lib, pr.transcode)
			}
			probes = append(probes, pr)
			if pr.dReason == "" && pr.tReason == "" {
				break
			}
		}
		if len(probes) > 0 {
			pr := probes[0]
			for _, p := range probes {
				if p.dReason == "" {
					pr = p
					if p.tReason == "" {
						break
					}
				}
			}
			rt.Movie, rt.TranscodeMovie = pr.direct, pr.transcode
			ot.Movie, ot.TranscodeMovie = pr.oDirect, pr.oTranscode
			record("movie", pr.direct, pr.lib.ref.Key, pr.oDirect, pr.dMethod, pr.dReason)
			if pr.transcode.ID != pr.direct.ID {
				record("transcode-movie", pr.transcode, pr.lib.ref.Key, pr.oTranscode, pr.tMethod, pr.tReason)
			} else if pr.dReason != "" {
				ot.Unmapped["transcode-movie"] = ot.Unmapped["movie"]
			}
			rt.SearchTerm = searchTerm(rt.Movie.Name)
			ot.SearchTerm = rt.SearchTerm
			if tag := rt.Movie.ImageTags["Primary"]; tag != "" && ot.Movie.ID != "" && ot.Movie.ImageTags["Primary"] == "" {
				reason := fmt.Sprintf("movie %q has a Primary image on %s but no Primary image tag on %s", rt.Movie.Name, ref.label, other.label)
				ot.Unmapped["image"] = reason
				warnings = append(warnings, reason)
			}
		}
	}

	// Series and episode: the first series of a shared library whose series
	// and first episode both map. Each library offers up to probeTries
	// series with two or more seasons.
	if libs := probeLibraries(pairs, "tvshows"); len(libs) == 0 {
		warnings = append(warnings, "no tvshows library on "+ref.label)
	} else {
		type showProbe struct {
			lib               libPair
			series, episode   baseItem
			oSeries, oEpisode baseItem
			sMethod, eMethod  string
			sReason, eReason  string
		}
		var probes []showProbe
	libraries:
		for _, lib := range libs {
			series, err := listItems(ctx, ref, rt.UserID, lib.ref.ID, "Series", 100)
			if err != nil {
				return rt, ot, nil, nil, err
			}
			for _, s := range withChildren(series, 2, probeTries) {
				pr := showProbe{lib: lib, series: s}
				var eps itemList
				q := "/Shows/" + s.ID + "/Episodes?userId=" + rt.UserID + "&Limit=1&Fields=" + listFields
				if err := ref.getJSON(ctx, q, &eps); err != nil {
					return rt, ot, nil, nil, err
				}
				if len(eps.Items) > 0 {
					pr.episode = eps.Items[0]
				}
				pr.oSeries, pr.sMethod, pr.sReason = find(lib, s)
				switch {
				case pr.episode.ID == "":
					pr.eReason = "no episode on " + ref.label
				case pr.sReason != "":
					pr.eReason = fmt.Sprintf("its series %q not found", s.Name)
				default:
					pr.oEpisode, pr.eMethod, pr.eReason = find(lib, pr.episode)
					if pr.eReason != "" {
						if got, err := mapEpisode(ctx, other, ot.UserID, pr.oSeries.ID, pr.episode); err == nil {
							pr.oEpisode, pr.eMethod, pr.eReason = got, "series-index", ""
						}
					}
				}
				probes = append(probes, pr)
				if pr.sReason == "" && pr.eReason == "" {
					break libraries
				}
				if lib.other.ID == "" {
					continue libraries
				}
			}
		}
		if len(probes) > 0 {
			pr := probes[len(probes)-1]
			if pr.sReason != "" || pr.eReason != "" {
				// No series mapped with its episode. Keep the first whose
				// series mapped, else the first tried.
				pr = probes[0]
				for _, p := range probes {
					if p.sReason == "" {
						pr = p
						break
					}
				}
				pr.sReason = attempts(len(probes), pr.sReason)
				pr.eReason = attempts(len(probes), pr.eReason)
			}
			rt.Series, rt.Episode = pr.series, pr.episode
			ot.Series, ot.Episode = pr.oSeries, pr.oEpisode
			record("series", pr.series, pr.lib.ref.Key, pr.oSeries, pr.sMethod, pr.sReason)
			if pr.episode.ID != "" {
				record("episode", pr.episode, pr.lib.ref.Key, pr.oEpisode, pr.eMethod, pr.eReason)
			}
		}
	}

	// Album: the first album of a shared music library that maps.
	if libs := probeLibraries(pairs, "music"); len(libs) == 0 {
		warnings = append(warnings, "no music library on "+ref.label)
	} else {
		var first, firstLib, firstReason = baseItem{}, "", ""
		tries := 0
	albums:
		for _, lib := range libs {
			list, err := listItems(ctx, ref, rt.UserID, lib.ref.ID, "MusicAlbum", 100)
			if err != nil {
				return rt, ot, nil, nil, err
			}
			for _, a := range withChildren(list, 1, probeTries) {
				tries++
				found, method, reason := find(lib, a)
				if reason == "" {
					rt.Album, ot.Album = a, found
					record("album", a, lib.ref.Key, found, method, "")
					break albums
				}
				if first.ID == "" {
					first, firstLib, firstReason = a, lib.ref.Key, reason
				}
				if lib.other.ID == "" {
					continue albums
				}
			}
		}
		if rt.Album.ID == "" && first.ID != "" {
			rt.Album = first
			record("album", first, firstLib, baseItem{}, "", attempts(tries, firstReason))
		}
	}

	return rt, ot, mapping, warnings, nil
}

// libPair is one reference library and its match on the other server.
// other.ID is empty when the other server has no match.
type libPair struct {
	ref, other library
	method     string
}

// probeLibraries returns the libraries of one collection type to take
// probe items from. Libraries that both servers have come first: those
// matched by id or name, then those matched by collection type alone.
// Libraries only the reference has come last, so that the probe still
// reports an unmapped item when no library is shared.
func probeLibraries(pairs []libPair, collectionType string) []libPair {
	var named, typed, alone []libPair
	for _, p := range pairs {
		if p.ref.CollectionType != collectionType {
			continue
		}
		switch p.method {
		case "same-id", "name":
			named = append(named, p)
		case "":
			alone = append(alone, p)
		default:
			typed = append(typed, p)
		}
	}
	return append(append(named, typed...), alone...)
}

// listItems lists items of one type in a library, sorted by name.
func listItems(ctx context.Context, s *server, userID, parentID, itemType string, limit int) ([]baseItem, error) {
	var out itemList
	q := "/Items?userId=" + userID + "&ParentId=" + parentID + "&IncludeItemTypes=" + itemType +
		"&Recursive=true&SortBy=SortName&SortOrder=Ascending&Limit=" + fmt.Sprint(limit) + "&Fields=" + listFields
	if err := s.getJSON(ctx, q, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// attempts adds the number of tried candidates to a reason.
func attempts(n int, reason string) string {
	if reason == "" || n <= 1 {
		return reason
	}
	return fmt.Sprintf("%s (first of %d candidates tried; none mapped)", reason, n)
}

// mapEpisode finds ref among the episodes of the other server's series by
// path, else by provider id, else by season and episode number.
func mapEpisode(ctx context.Context, s *server, userID, seriesID string, ref baseItem) (baseItem, error) {
	var eps itemList
	q := "/Shows/" + seriesID + "/Episodes?userId=" + userID + "&Fields=" + listFields
	if err := s.getJSON(ctx, q, &eps); err != nil {
		return baseItem{}, err
	}
	if got, _, ok := matchItem(ref, eps.Items); ok {
		return got, nil
	}
	if ref.IndexNumber == nil || ref.ParentIndexNumber == nil {
		return baseItem{}, errors.New("no episode with the same path and no index to match")
	}
	var hit []baseItem
	for _, e := range eps.Items {
		if e.IndexNumber != nil && e.ParentIndexNumber != nil &&
			*e.IndexNumber == *ref.IndexNumber && *e.ParentIndexNumber == *ref.ParentIndexNumber {
			hit = append(hit, e)
		}
	}
	if len(hit) != 1 {
		return baseItem{}, fmt.Errorf("%d episodes with S%02dE%02d", len(hit), *ref.ParentIndexNumber, *ref.IndexNumber)
	}
	return hit[0], nil
}

// resolveUser returns the id and name of the token's user. When the token
// is an API key, /Users/Me fails and the user is found by name, or the
// first administrator is used.
func resolveUser(ctx context.Context, s *server, name string) (string, string, error) {
	var me struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	}
	if err := s.getJSON(ctx, "/Users/Me", &me); err == nil && me.ID != "" {
		if name == "" || strings.EqualFold(name, me.Name) {
			return me.ID, me.Name, nil
		}
	}
	var users []struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Policy struct {
			IsAdministrator bool `json:"IsAdministrator"`
		} `json:"Policy"`
	}
	if err := s.getJSON(ctx, "/Users", &users); err != nil {
		return "", "", fmt.Errorf("resolve user: %w", err)
	}
	for _, u := range users {
		if name != "" && strings.EqualFold(u.Name, name) {
			return u.ID, u.Name, nil
		}
	}
	if name == "" {
		for _, u := range users {
			if u.Policy.IsAdministrator {
				return u.ID, u.Name, nil
			}
		}
	}
	return "", "", fmt.Errorf("%s: user %q not found", s.label, name)
}

// mapItem finds ref on s. It tries the same id first, then a name search
// that it narrows by path.
func mapItem(ctx context.Context, s *server, userID string, ref baseItem) (baseItem, string, error) {
	var byID itemList
	q := "/Items?userId=" + userID + "&Ids=" + ref.ID + "&Fields=" + listFields
	if err := s.getJSON(ctx, q, &byID); err == nil && len(byID.Items) == 1 {
		got := byID.Items[0]
		if ref.Path == "" || cleanPath(got.Path) == cleanPath(ref.Path) {
			return got, "same-id", nil
		}
	}
	for _, param := range []string{"SearchTerm", "NameStartsWith"} {
		var found itemList
		q := "/Items?userId=" + userID + "&Recursive=true&IncludeItemTypes=" + ref.Type +
			"&" + param + "=" + url.QueryEscape(ref.Name) + "&Limit=50&Fields=" + listFields
		if err := s.getJSON(ctx, q, &found); err != nil {
			continue
		}
		if got, method, ok := matchItem(ref, found.Items); ok {
			return got, method, nil
		}
	}
	return baseItem{}, "", errors.New("no candidate with the same path or provider id")
}

// matchProvider picks the only candidate of the same type that shares a
// provider id with ref. The first of Tvdb, Tmdb and Imdb that ref has
// decides.
func matchProvider(ref baseItem, cands []baseItem) (baseItem, string, bool) {
	for _, key := range []string{"Tvdb", "Tmdb", "Imdb"} {
		want := ref.ProviderIDs[key]
		if want == "" {
			continue
		}
		var hit []baseItem
		for _, c := range cands {
			if c.Type == ref.Type && strings.EqualFold(c.ProviderIDs[key], want) {
				hit = append(hit, c)
			}
		}
		if len(hit) == 1 {
			return hit[0], "provider-id", true
		}
		return baseItem{}, "", false
	}
	return baseItem{}, "", false
}

// matchItem picks the candidate that is the same file or folder as ref.
// An exact path match wins. Otherwise a unique match on the last three path
// components covers servers that mount the library at different roots.
// Items without a path match on type and name when unique.
func matchItem(ref baseItem, cands []baseItem) (baseItem, string, bool) {
	if ref.Path != "" {
		want := cleanPath(ref.Path)
		for _, c := range cands {
			if c.Path != "" && cleanPath(c.Path) == want {
				return c, "path", true
			}
		}
		var hit []baseItem
		for _, c := range cands {
			if c.Path != "" && pathTail(c.Path, 3) == pathTail(ref.Path, 3) {
				hit = append(hit, c)
			}
		}
		if len(hit) == 1 {
			return hit[0], "path-suffix", true
		}
		return matchProvider(ref, cands)
	}
	var hit []baseItem
	for _, c := range cands {
		if c.Type == ref.Type && c.Name == ref.Name {
			hit = append(hit, c)
		}
	}
	if len(hit) == 1 {
		return hit[0], "name", true
	}
	return baseItem{}, "", false
}

func cleanPath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	return path.Clean(p)
}

// pathTail returns the last n components of p.
func pathTail(p string, n int) string {
	parts := strings.Split(strings.Trim(cleanPath(p), "/"), "/")
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	return strings.Join(parts, "/")
}

// matchView finds the view with the same id or name, or else the only view
// with the same collection type. The method says which.
func matchView(ref baseItem, views []baseItem) (baseItem, string, bool) {
	for _, v := range views {
		if v.ID == ref.ID {
			return v, "same-id", true
		}
	}
	for _, v := range views {
		if v.Name == ref.Name {
			return v, "name", true
		}
	}
	var hit []baseItem
	for _, v := range views {
		if ref.CollectionType != "" && v.CollectionType == ref.CollectionType {
			hit = append(hit, v)
		}
	}
	if len(hit) == 1 {
		return hit[0], "collection-type", true
	}
	return baseItem{}, "", false
}

// videoOf returns the first video stream of the first media source.
func videoOf(it baseItem) (container string, v mediaStream, ok bool) {
	if len(it.MediaSources) == 0 {
		return "", mediaStream{}, false
	}
	src := it.MediaSources[0]
	for _, s := range src.MediaStreams {
		if s.Type == "Video" {
			return strings.ToLower(src.Container), s, true
		}
	}
	return strings.ToLower(src.Container), mediaStream{}, false
}

// pickMovies chooses a direct-play movie and a transcode movie from a list
// sorted by name. The direct-play choice prefers SDR H.264 in an MP4-family
// container, which every client plays directly. The transcode choice is a
// different SDR H.264 or HEVC title: the transcode scenario forces a
// transcode with a low bitrate cap, and Deadfin refuses non-SDR transcodes
// (D-32), so an HDR title would measure a refusal, not a transcode.
func pickMovies(items []baseItem) (direct, transcode baseItem) {
	sdr := func(it baseItem, codecs ...string) bool {
		_, v, ok := videoOf(it)
		if !ok || (v.VideoRange != "" && v.VideoRange != "SDR") {
			return false
		}
		for _, c := range codecs {
			if strings.EqualFold(v.Codec, c) {
				return true
			}
		}
		return false
	}
	mp4 := func(it baseItem) bool {
		c, _, _ := videoOf(it)
		for _, part := range strings.Split(c, ",") {
			switch part {
			case "mp4", "m4v", "mov":
				return true
			}
		}
		return false
	}
	tiers := []func(baseItem) bool{
		func(it baseItem) bool { return sdr(it, "h264") && mp4(it) },
		func(it baseItem) bool { return sdr(it, "h264") },
		func(it baseItem) bool { return sdr(it, "h264", "hevc") },
		func(it baseItem) bool { return true },
	}
	for _, ok := range tiers {
		if direct.ID != "" {
			break
		}
		for _, it := range items {
			if ok(it) {
				direct = it
				break
			}
		}
	}
	for _, it := range items {
		if it.ID != direct.ID && sdr(it, "h264", "hevc") {
			return direct, it
		}
	}
	return direct, direct
}

// withChildren returns up to n items, those with at least min children
// first, each group in list order.
func withChildren(items []baseItem, min, n int) []baseItem {
	var full, rest []baseItem
	for _, it := range items {
		if it.ChildCount != nil && *it.ChildCount >= min {
			full = append(full, it)
		} else {
			rest = append(rest, it)
		}
	}
	out := append(full, rest...)
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// searchTerm returns the first word of name with four or more letters, in
// lower case, or the first four runes of name.
func searchTerm(name string) string {
	for _, w := range strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 4 {
			return strings.ToLower(w)
		}
	}
	r := []rune(strings.TrimSpace(name))
	if len(r) > 4 {
		r = r[:4]
	}
	return strings.ToLower(string(r))
}
