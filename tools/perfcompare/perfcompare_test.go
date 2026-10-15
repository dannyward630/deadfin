// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPercentileNearestRank(t *testing.T) {
	sorted := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		p    float64
		want float64
	}{{50, 5}, {95, 10}, {90, 9}, {10, 1}, {100, 10}, {1, 1}}
	for _, c := range cases {
		if got := percentile(sorted, c.p); got != c.want {
			t.Errorf("percentile(%v) = %v, want %v", c.p, got, c.want)
		}
	}
	if got := percentile([]float64{42}, 95); got != 42 {
		t.Errorf("single sample p95 = %v, want 42", got)
	}
}

func TestSummarize(t *testing.T) {
	var samples []time.Duration
	for _, ms := range []int{30, 10, 20, 40, 1000} {
		samples = append(samples, time.Duration(ms)*time.Millisecond)
	}
	got := summarize(samples)
	want := latency{Samples: 5, P50: 30, P95: 1000, Max: 1000, Mean: 220}
	if got != want {
		t.Fatalf("summarize = %+v, want %+v", got, want)
	}
	if (summarize(nil) != latency{}) {
		t.Fatal("summarize(nil) is not zero")
	}
}

func TestRatio(t *testing.T) {
	if got := ratio(30, 10); got != 3 {
		t.Errorf("ratio(30,10) = %v", got)
	}
	if got := ratio(1, 3); got != 0.33 {
		t.Errorf("ratio(1,3) = %v", got)
	}
	if got := ratio(5, 0); got != 0 {
		t.Errorf("ratio with no baseline = %v, want 0", got)
	}
}

func TestMatchItemByPath(t *testing.T) {
	ref := baseItem{ID: "jf1", Name: "Alien", Type: "Movie", Path: "/media/Movies/Alien (1979)/Alien (1979).mkv"}
	cands := []baseItem{
		{ID: "df-other", Name: "Alien", Type: "Movie", Path: "/media/Movies/Alien (2030)/Alien (2030).mkv"},
		{ID: "df1", Name: "Alien", Type: "Movie", Path: "/media/Movies/Alien (1979)//Alien (1979).mkv"},
	}
	got, method, ok := matchItem(ref, cands)
	if !ok || got.ID != "df1" || method != "path" {
		t.Fatalf("exact path: got %q %q %v", got.ID, method, ok)
	}

	// Different mount roots: the last three components decide.
	cands = []baseItem{
		{ID: "df2", Path: "/data/media/Movies/Alien (1979)/Alien (1979).mkv"},
		{ID: "df3", Path: "/data/media/Movies/Aliens (1986)/Aliens (1986).mkv"},
	}
	got, method, ok = matchItem(ref, cands)
	if !ok || got.ID != "df2" || method != "path-suffix" {
		t.Fatalf("suffix path: got %q %q %v", got.ID, method, ok)
	}

	// An ambiguous suffix does not match.
	cands = append(cands, baseItem{ID: "df4", Path: "/other/Movies/Alien (1979)/Alien (1979).mkv"})
	if _, _, ok := matchItem(ref, cands); ok {
		t.Fatal("ambiguous suffix matched")
	}

	// Same name, different file: no match.
	if _, _, ok := matchItem(ref, []baseItem{{ID: "x", Name: "Alien", Type: "Movie", Path: "/media/Movies/Alien/other.mkv"}}); ok {
		t.Fatal("name-only match accepted for an item with a path")
	}

	// No path: unique type and name.
	album := baseItem{ID: "a", Name: "Kid A", Type: "MusicAlbum"}
	got, method, ok = matchItem(album, []baseItem{{ID: "b", Name: "Kid A", Type: "MusicAlbum"}, {ID: "c", Name: "Kid A", Type: "Audio"}})
	if !ok || got.ID != "b" || method != "name" {
		t.Fatalf("name match: got %q %q %v", got.ID, method, ok)
	}
}

func TestMatchView(t *testing.T) {
	views := []baseItem{
		{ID: "1", Name: "Films", CollectionType: "movies"},
		{ID: "2", Name: "Shows", CollectionType: "tvshows"},
		{ID: "3", Name: "animu", CollectionType: "tvshows"},
	}
	if v, m, ok := matchView(baseItem{ID: "x", Name: "animu", CollectionType: "tvshows"}, views); !ok || v.ID != "3" || m != "name" {
		t.Fatalf("by name: %v %v %v", v.ID, m, ok)
	}
	if v, m, ok := matchView(baseItem{ID: "2", Name: "TV Shows", CollectionType: "tvshows"}, views); !ok || v.ID != "2" || m != "same-id" {
		t.Fatalf("by id: %v %v %v", v.ID, m, ok)
	}
	if v, m, ok := matchView(baseItem{Name: "Movies", CollectionType: "movies"}, views); !ok || v.ID != "1" || m != "collection-type" {
		t.Fatalf("by unique type: %v %v %v", v.ID, m, ok)
	}
	if _, _, ok := matchView(baseItem{Name: "anime-shoko", CollectionType: "tvshows"}, views); ok {
		t.Fatal("ambiguous type matched")
	}
}

func TestProbeLibraries(t *testing.T) {
	pairs := []libPair{
		{ref: library{Key: "anime-shoko", CollectionType: "tvshows"}},
		{ref: library{Key: "Films", CollectionType: "movies"}, other: library{ID: "1"}, method: "collection-type"},
		{ref: library{Key: "TV", CollectionType: "tvshows"}, other: library{ID: "2"}, method: "collection-type"},
		{ref: library{Key: "Shows", CollectionType: "tvshows"}, other: library{ID: "3"}, method: "name"},
		{ref: library{Key: "animu", CollectionType: "tvshows"}, other: library{ID: "4"}, method: "same-id"},
	}
	var got []string
	for _, p := range probeLibraries(pairs, "tvshows") {
		got = append(got, p.ref.Key)
	}
	if strings.Join(got, ",") != "Shows,animu,TV,anime-shoko" {
		t.Fatalf("probe order = %v", got)
	}
}

func TestMatchItemByProvider(t *testing.T) {
	ref := baseItem{ID: "j", Name: "Bakemonogatari", Type: "Series", Path: "/media/anime-shoko/Bakemonogatari",
		ProviderIDs: map[string]string{"Tvdb": "102261", "Tmdb": "46195"}}
	cands := []baseItem{
		{ID: "d1", Name: "Bakemonogatari", Type: "Series", Path: "/media/animu/Monogatari", ProviderIDs: map[string]string{"Tvdb": "102261"}},
		{ID: "d2", Name: "Bakemonogatari", Type: "Season", Path: "/media/animu/Monogatari/S1", ProviderIDs: map[string]string{"Tvdb": "102261"}},
		{ID: "d3", Name: "Nisemonogatari", Type: "Series", Path: "/media/animu/Nise", ProviderIDs: map[string]string{"Tmdb": "46195"}},
	}
	got, method, ok := matchItem(ref, cands)
	if !ok || got.ID != "d1" || method != "provider-id" {
		t.Fatalf("provider match: got %q %q %v", got.ID, method, ok)
	}
	// Two candidates with the first provider id the ref has: no match, and
	// a later provider does not break the tie.
	cands[2].ProviderIDs = map[string]string{"Tvdb": "102261"}
	if _, _, ok := matchItem(ref, cands); ok {
		t.Fatal("ambiguous provider id matched")
	}
}

func TestMapEpisodeBySeriesIndex(t *testing.T) {
	one, two := 1, 2
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Shows/ds/Episodes" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(itemList{Items: []baseItem{
			{ID: "e1", Type: "Episode", Path: "/x/S01E01.mkv", ParentIndexNumber: &one, IndexNumber: &one},
			{ID: "e2", Type: "Episode", Path: "/x/S01E02.mkv", ParentIndexNumber: &one, IndexNumber: &two},
		}})
	}))
	defer srv.Close()
	s := newServer("deadfin", srv.URL, "tok", 5*time.Second)
	ref := baseItem{ID: "j", Type: "Episode", Path: "/y/Show - 1x02.mkv", ParentIndexNumber: &one, IndexNumber: &two}
	got, err := mapEpisode(context.Background(), s, "u", "ds", ref)
	if err != nil || got.ID != "e2" {
		t.Fatalf("mapEpisode = %q %v", got.ID, err)
	}
	ref.IndexNumber = &one
	ref.ParentIndexNumber = &two
	if _, err := mapEpisode(context.Background(), s, "u", "ds", ref); err == nil {
		t.Fatal("missing episode matched")
	}
}

func video(id, container, codec, rng string) baseItem {
	return baseItem{ID: id, MediaSources: []mediaSource{{Container: container, MediaStreams: []mediaStream{
		{Type: "Audio", Codec: "aac"}, {Type: "Video", Codec: codec, VideoRange: rng},
	}}}}
}

func TestPickMovies(t *testing.T) {
	items := []baseItem{
		video("hdr", "mkv", "hevc", "HDR"),
		video("mkv264", "mkv", "h264", "SDR"),
		video("mp4", "mov,mp4,m4a,3gp,3g2,mj2", "h264", "SDR"),
		video("hevc", "mkv", "hevc", "SDR"),
	}
	direct, transcode := pickMovies(items)
	if direct.ID != "mp4" {
		t.Errorf("direct = %q, want mp4", direct.ID)
	}
	if transcode.ID != "mkv264" {
		t.Errorf("transcode = %q, want mkv264 (first SDR title that is not the direct one)", transcode.ID)
	}
	direct, transcode = pickMovies([]baseItem{video("only", "mkv", "hevc", "HDR")})
	if direct.ID != "only" || transcode.ID != "only" {
		t.Errorf("fallback = %q %q", direct.ID, transcode.ID)
	}
}

func TestSearchTerm(t *testing.T) {
	cases := map[string]string{
		"The Matrix":       "matrix",
		"Up":               "up",
		"Alien: Covenant":  "alien",
		"2001: A Space...": "2001",
		"Amélie":           "amélie",
	}
	for in, want := range cases {
		if got := searchTerm(in); got != want {
			t.Errorf("searchTerm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShapeOf(t *testing.T) {
	cases := []struct {
		body string
		want shape
	}{
		{`{"Items":[{},{}],"TotalRecordCount":40,"StartIndex":0}`, shape{Count: 2, Total: 40}},
		{`[{},{},{}]`, shape{Count: 3, Total: -1}},
		{`{"SearchHints":[{}],"TotalRecordCount":1}`, shape{Count: 1, Total: 1}},
		{`{"Id":"x","Type":"Movie"}`, shape{Count: 1, Total: -1, Sig: "Movie"}},
		{`{"MediaSources":[{"SupportsDirectPlay":false,"SupportsDirectStream":false,"TranscodingUrl":"/videos/x/master.m3u8"}],"PlaySessionId":"p"}`,
			shape{Count: 1, Total: -1, Sig: "transcode"}},
		{`{"MediaSources":[{"SupportsDirectPlay":true,"SupportsDirectStream":true}],"PlaySessionId":"p"}`,
			shape{Count: 1, Total: -1, Sig: "direct-play+direct-stream"}},
	}
	for _, c := range cases {
		got, err := shapeOf([]byte(c.body))
		if err != nil || got != c.want {
			t.Errorf("shapeOf(%s) = %+v, %v; want %+v", c.body, got, err, c.want)
		}
	}
}

func TestDiff(t *testing.T) {
	side := func(status, count, total int) *Side {
		return &Side{Status: status, Shape: &shape{Count: count, Total: total}}
	}
	if d := diff(side(200, 5, 5), side(200, 5, 5)); d != "" {
		t.Errorf("equal sides: %q", d)
	}
	if d := diff(side(200, 5, 5), side(404, 0, 0)); d != "status 200/404" {
		t.Errorf("status: %q", d)
	}
	if d := diff(side(200, 16, -1), side(200, 3, -1)); d != "count 16/3" {
		t.Errorf("count: %q", d)
	}
	if r := result("x", nil, side(200, 1, 1)); r.Diff != "missing on jellyfin" {
		t.Errorf("missing: %q", r.Diff)
	}
}

func TestBuildScenariosNamesAlign(t *testing.T) {
	mk := func(prefix string) target {
		return target{
			UserID: prefix + "u",
			Libraries: []library{
				{Key: "Movies", ID: prefix + "m", CollectionType: "movies"},
				{Key: "Shows", ID: prefix + "s", CollectionType: "tvshows"},
				{Key: "Music", ID: prefix + "mu", CollectionType: "music"},
			},
			Movie:          baseItem{ID: prefix + "1", ImageTags: map[string]string{"Primary": "t"}},
			TranscodeMovie: baseItem{ID: prefix + "2"},
			Series:         baseItem{ID: prefix + "3"},
			Episode:        baseItem{ID: prefix + "4", SeasonID: prefix + "5"},
			Album:          baseItem{ID: prefix + "6"},
			SearchTerm:     "matrix",
		}
	}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	j, d := buildScenarios(mk("j"), now), buildScenarios(mk("d"), now)
	if len(j) != len(d) {
		t.Fatalf("scenario counts differ: %d %d", len(j), len(d))
	}
	seen := map[string]bool{}
	for i := range j {
		if j[i].Name != d[i].Name {
			t.Fatalf("name %d: %q vs %q", i, j[i].Name, d[i].Name)
		}
		if seen[j[i].Name] {
			t.Fatalf("duplicate scenario %q", j[i].Name)
		}
		seen[j[i].Name] = true
		if strings.Contains(d[i].Path, "ju") || strings.Contains(d[i].Path, "=jm") {
			t.Fatalf("deadfin scenario %q uses a jellyfin id: %s", d[i].Name, d[i].Path)
		}
	}
	for _, want := range []string{"home/views", "home/resume", "home/nextup", "latest/movies", "browse/movies/name-page3",
		"browse/music/artist", "detail/episode", "show/episodes", "search/hints", "playback/info-transcode", "image/primary-card"} {
		if !seen[want] {
			t.Errorf("missing scenario %q", want)
		}
	}

	// A missing id drops the scenario on that side only.
	partial := mk("d")
	partial.Album = baseItem{}
	names, _, dm := alignScenarios(j, buildScenarios(partial, now))
	if _, ok := dm["detail/album"]; ok {
		t.Fatal("detail/album built without an album id")
	}
	if len(names) != len(j) {
		t.Fatalf("aligned names = %d, want %d", len(names), len(j))
	}
}

func TestFirstURIAndStop(t *testing.T) {
	master := []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1500000\nmain.m3u8?DeviceId=d&PlaySessionId=p\n")
	uri, ok := firstURI(master)
	if !ok || uri != "main.m3u8?DeviceId=d&PlaySessionId=p" {
		t.Fatalf("firstURI = %q %v", uri, ok)
	}
	abs, err := resolveRef("http://h/videos/x/master.m3u8?a=1", uri)
	if err != nil || abs != "http://h/videos/x/main.m3u8?DeviceId=d&PlaySessionId=p" {
		t.Fatalf("resolveRef = %q %v", abs, err)
	}

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/Videos/ActiveEncodings" {
			gotQuery = r.URL.RawQuery
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	s := newServer("test", srv.URL, "tok", 5*time.Second)
	if err := stopTranscode(context.Background(), s, "/videos/x/master.m3u8?deviceId=dev&playSessionId=ps1&ApiKey=secret"); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "deviceId=dev&playSessionId=ps1" {
		t.Fatalf("stop query = %q", gotQuery)
	}
}

func TestPathOnlyDropsQuery(t *testing.T) {
	if got := pathOnly("/videos/x/main.m3u8?ApiKey=secret"); got != "/videos/x/main.m3u8" {
		t.Fatalf("pathOnly = %q", got)
	}
}

// TestCompareAgainstFakes runs the whole flow against two fake servers with
// different ids for the same files.
func TestCompareAgainstFakes(t *testing.T) {
	jf := httptest.NewServer(fakeServer("j", 3, true))
	defer jf.Close()
	df := httptest.NewServer(fakeServer("d", 2, false))
	defer df.Close()

	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	run, err := compare(context.Background(),
		newServer("jellyfin", jf.URL, "tok", 5*time.Second),
		newServer("deadfin", df.URL, "tok", 5*time.Second),
		options{iterations: 3, warmup: 1, hlsRuns: 1, now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range run.Mapping {
		// The Jellyfin-only library is unmapped; nothing else is.
		if m.Method == "unmapped" && m.Name != "anime-shoko" {
			t.Errorf("unmapped %s %q: %s", m.Entity, m.Name, m.Mismatch)
		}
		if m.Entity == "series" && (m.Library != "Shows" || m.Name != "Show") {
			t.Errorf("series probe from %q: %+v", m.Library, m)
		}
	}
	for _, w := range run.Warnings {
		if strings.Contains(w, "not found") {
			t.Errorf("warning: %s", w)
		}
	}
	byName := map[string]ScenarioResult{}
	for _, s := range run.Scenarios {
		byName[s.Name] = s
	}
	for _, n := range []string{"detail/series", "detail/episode", "show/seasons", "show/episodes", "image/primary-card"} {
		if got := byName[n]; got.Diff != "" || got.Deadfin == nil || got.Deadfin.Samples != 3 {
			t.Errorf("%s = %+v", n, got)
		}
	}
	if got := byName["latest/movies"].Diff; got != "count 3/2" {
		t.Errorf("latest/movies diff = %q, want count 3/2", got)
	}
	if got := byName["home/views"]; got.Diff != "count 4/3" || got.Jellyfin.Samples != 3 || got.Deadfin.Samples != 3 {
		t.Errorf("home/views = %+v", got)
	}
	if got := byName["hls/first-segment-ttfb"]; got.Jellyfin == nil || got.Jellyfin.Samples != 1 || got.Deadfin.Samples != 1 {
		t.Errorf("hls segment = %+v %+v; warnings %v", got.Jellyfin, got.Deadfin, run.Warnings)
	}
	if !run.Noisy || !strings.Contains(strings.Join(run.NoisyReasons, ";"), "deadfin running at start: Scan Media Library") {
		t.Errorf("noisy = %v %v", run.Noisy, run.NoisyReasons)
	}
	if run.Deadfin.Build == nil || run.Jellyfin.Build != nil {
		t.Errorf("build info: jf %s df %s", run.Jellyfin.Build, run.Deadfin.Build)
	}

	var table bytes.Buffer
	writeTable(&table, run)
	if !strings.Contains(table.String(), "| latest/movies |") || !strings.Contains(table.String(), "NOISY RUN") ||
		!strings.Contains(table.String(), `- probes: movie "The Matrix" (Movies, path)`) ||
		!strings.Contains(table.String(), `series "Show" (Shows, `) {
		t.Errorf("table:\n%s", table.String())
	}

	line, _ := json.Marshal(run)
	runs, err := readRuns(strings.NewReader(string(line) + "\nnot json\n" + string(line) + "\n"))
	if err != nil || len(runs) != 2 {
		t.Fatalf("readRuns = %d, %v", len(runs), err)
	}
	var page bytes.Buffer
	if err := writeHTML(&page, runs, 30); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.String(), "latest/movies") {
		t.Error("html report lacks the scenario rows")
	}
}

// TestUnmappedFailsScenarios checks that a probe item missing on Deadfin
// fails the scenarios that need it, with the reason, and is not left out.
func TestUnmappedFailsScenarios(t *testing.T) {
	jf := httptest.NewServer(fakeServer("j", 1, false))
	defer jf.Close()
	inner := fakeServer("d", 1, false)
	df := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Items" && r.URL.Query().Get("IncludeItemTypes") == "Series" {
			_ = json.NewEncoder(w).Encode(itemList{})
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer df.Close()

	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	run, err := compare(context.Background(),
		newServer("jellyfin", jf.URL, "tok", 5*time.Second),
		newServer("deadfin", df.URL, "tok", 5*time.Second),
		options{iterations: 2, warmup: 0, now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ScenarioResult{}
	for _, s := range run.Scenarios {
		byName[s.Name] = s
	}
	for _, n := range []string{"detail/series", "detail/episode", "show/seasons", "show/episodes"} {
		got := byName[n]
		if got.Diff != "unmapped on deadfin" || got.Deadfin == nil || !strings.Contains(got.Deadfin.Unmapped, `"Show"`) {
			t.Errorf("%s = %+v %+v", n, got, got.Deadfin)
		}
		if got.Jellyfin == nil || got.Jellyfin.Samples != 2 {
			t.Errorf("%s jellyfin side = %+v", n, got.Jellyfin)
		}
	}
	if got := byName["detail/movie"]; got.Diff != "" {
		t.Errorf("detail/movie diff = %q", got.Diff)
	}
	var table bytes.Buffer
	writeTable(&table, run)
	for _, want := range []string{"**unmapped on deadfin**", `- unmapped: series "Show" in "Shows"`, "2 unmapped"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, table.String())
		}
	}
}

// fakeServer serves a tiny library with ids prefixed by p. latest returns
// latestN items, so the two fakes disagree on one row. extra adds a TV
// library before Shows whose series no other server has.
func fakeServer(p string, latestN int, extra bool) http.Handler {
	movie := func(id, file string) map[string]any {
		return map[string]any{"Id": p + id, "Name": "The Matrix", "Type": "Movie", "Path": "/media/Movies/" + file,
			"ImageTags": map[string]string{"Primary": "tag"},
			"MediaSources": []any{map[string]any{"Container": "mp4", "MediaStreams": []any{
				map[string]any{"Type": "Video", "Codec": "h264", "VideoRange": "SDR"}}}}}
	}
	items := []any{movie("m1", "The Matrix (1999)/m.mp4"), movie("m2", "The Matrix Reloaded (2003)/m.mp4")}
	series := map[string]any{"Id": p + "s1", "Name": "Show", "Type": "Series", "Path": "/media/Shows/Show", "ChildCount": 2}
	episode := map[string]any{"Id": p + "e1", "Name": "Pilot", "Type": "Episode", "Path": "/media/Shows/Show/S01/e1.mkv",
		"SeriesId": p + "s1", "SeasonId": p + "se1"}
	alone := map[string]any{"Id": p + "x1", "Name": "Bakemonogatari", "Type": "Series", "Path": "/media/anime-shoko/Bakemonogatari", "ChildCount": 2}
	album := map[string]any{"Id": p + "a1", "Name": "Album", "Type": "MusicAlbum", "Path": "/media/Music/Artist/Album", "ChildCount": 9}
	list := func(xs ...any) map[string]any { return map[string]any{"Items": xs, "TotalRecordCount": len(xs)} }
	byID := map[string]any{p + "m1": items[0], p + "m2": items[1], p + "s1": series, p + "e1": episode, p + "a1": album}

	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("/System/Info/Public", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"Version": "12.0.0", "ServerName": p})
	})
	mux.HandleFunc("/deadfin/v1/build", func(w http.ResponseWriter, r *http.Request) {
		if p == "j" {
			http.NotFound(w, r)
			return
		}
		write(w, map[string]any{"Version": "v0.0.0", "Revision": "abcdef0123"})
	})
	mux.HandleFunc("/ScheduledTasks", func(w http.ResponseWriter, r *http.Request) {
		state := "Idle"
		if p == "d" {
			state = "Running"
		}
		write(w, []any{map[string]any{"Name": "Scan Media Library", "State": state}})
	})
	mux.HandleFunc("/Users/Me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != `MediaBrowser Token="tok"` {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		write(w, map[string]any{"Id": p + "u", "Name": "whiskey"})
	})
	mux.HandleFunc("/UserViews", func(w http.ResponseWriter, r *http.Request) {
		var views []any
		if extra {
			views = append(views, map[string]any{"Id": p + "lx", "Name": "anime-shoko", "CollectionType": "tvshows"})
		}
		write(w, list(append(views,
			map[string]any{"Id": p + "lm", "Name": "Movies", "CollectionType": "movies"},
			map[string]any{"Id": p + "ls", "Name": "Shows", "CollectionType": "tvshows"},
			map[string]any{"Id": p + "lmu", "Name": "Music", "CollectionType": "music"},
		)...))
	})
	mux.HandleFunc("/Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("Ids") != "":
			if it, ok := byID[q.Get("Ids")]; ok {
				write(w, list(it))
			} else {
				write(w, list())
			}
		case q.Get("IncludeItemTypes") == "Movie":
			write(w, list(items...))
		case q.Get("IncludeItemTypes") == "Series" && q.Get("ParentId") == p+"lx":
			write(w, list(alone))
		case q.Get("IncludeItemTypes") == "Series":
			write(w, list(series))
		case q.Get("IncludeItemTypes") == "Episode":
			write(w, list(episode))
		case q.Get("IncludeItemTypes") == "MusicAlbum":
			write(w, list(album))
		default:
			write(w, list())
		}
	})
	mux.HandleFunc("/Items/Latest", func(w http.ResponseWriter, r *http.Request) {
		out := make([]any, latestN)
		for i := range out {
			out[i] = items[0]
		}
		write(w, out)
	})
	mux.HandleFunc("/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		if it, ok := byID[r.PathValue("id")]; ok {
			write(w, it)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/Items/{id}/Images/Primary", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 2048))
	})
	mux.HandleFunc("POST /Items/{id}/PlaybackInfo", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"PlaySessionId": "ps", "MediaSources": []any{map[string]any{
			"SupportsDirectPlay": false, "TranscodingUrl": "/videos/" + r.PathValue("id") + "/master.m3u8?DeviceId=dev&PlaySessionId=ps"}}})
	})
	mux.HandleFunc("/videos/{id}/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nmain.m3u8?PlaySessionId=ps\n"))
	})
	mux.HandleFunc("/videos/{id}/main.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:3,\nhls1/main/0.ts?PlaySessionId=ps\n"))
	})
	mux.HandleFunc("/videos/{id}/hls1/main/0.ts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 4096))
	})
	mux.HandleFunc("DELETE /Videos/ActiveEncodings", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	for _, p := range []string{"/UserItems/Resume", "/Shows/NextUp", "/Search/Hints"} {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) { write(w, list()) })
	}
	mux.HandleFunc("/Shows/{id}/Seasons", func(w http.ResponseWriter, r *http.Request) { write(w, list(map[string]any{"Id": "x"})) })
	mux.HandleFunc("/Shows/{id}/Episodes", func(w http.ResponseWriter, r *http.Request) { write(w, list(episode)) })
	return mux
}
