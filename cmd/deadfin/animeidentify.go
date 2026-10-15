// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/anime"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// runAnimeIdentify reports how many series of a library the offline anime
// crosswalk identifies. It reads the series from a database and changes
// nothing in the library; run it on a copy, because opening the database
// applies pending migrations.
//
// Usage: deadfin anime-identify (--db <copy.db> | --database-url <url>) --seeds <dir> [--library /media/animu] [--fetch]
//
// --fetch downloads the seed datasets into --seeds first (at most once a day
// per source, like the scheduled task).
func runAnimeIdentify(args []string) {
	flags := flag.NewFlagSet("anime-identify", flag.ExitOnError)
	dbPath := flags.String("db", "", "SQLite database to read (use a copy)")
	databaseURL := flags.String("database-url", "", "PostgreSQL database URL to read instead of --db (use a copy)")
	seeds := flags.String("seeds", "", "anime seed directory")
	libraryPath := flags.String("library", "", "only series below this path")
	fetch := flags.Bool("fetch", false, "download the seed datasets first")
	if err := flags.Parse(args); err != nil {
		log.Fatal(err)
	}
	if (*dbPath == "") == (*databaseURL == "") || *seeds == "" {
		log.Fatal("anime-identify: --seeds and one of --db or --database-url are required")
	}
	selected := database{sqlitePath: *dbPath}
	if *databaseURL != "" {
		parsed, err := parseDatabase(*databaseURL, "")
		if err != nil {
			log.Fatal(err)
		}
		selected = parsed
	}
	report, err := animeIdentify(context.Background(), selected, *seeds, *libraryPath, *fetch)
	if err != nil {
		log.Fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(report); err != nil {
		log.Fatal(err)
	}
}

type animeIdentifyReport struct {
	Series     int                   `json:"series"`
	Identified int                   `json:"identified"`
	ByMethod   map[string]int        `json:"byMethod"`
	Seasons    animeSeasonReport     `json:"seasons"`
	Misses     []animeIdentifyResult `json:"misses"`
	Matches    []animeIdentifyResult `json:"matches"`
}

type animeSeasonReport struct {
	Seasons   int `json:"seasons"`
	Mapped    int `json:"mapped"`
	SplitCour int `json:"splitCour"`
}

type animeIdentifyResult struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	TVDB    string `json:"tvdb,omitempty"`
	TMDb    string `json:"tmdb,omitempty"`
	Method  string `json:"method,omitempty"`
	AniList int    `json:"anilist,omitempty"`
	AniDB   int    `json:"anidb,omitempty"`
	Title   string `json:"title,omitempty"`
}

func animeIdentify(ctx context.Context, source database, seeds, libraryPath string, fetch bool) (*animeIdentifyReport, error) {
	if fetch {
		service := anime.NewService(anime.ServiceOptions{
			Fetcher: anime.NewFetcher(anime.FetcherOptions{Dir: seeds, UserAgent: "Deadfin/dev (+https://github.com/deadfin-org/deadfin)"}),
			Dir:     seeds,
		})
		if err := service.Refresh(ctx, nil); err != nil {
			return nil, fmt.Errorf("fetch seeds: %w", err)
		}
	}
	clk := clock.NewReal()
	started := clk.Now()
	crosswalk, found, err := anime.Load(ctx, seeds, anime.DefaultSources())
	if err != nil {
		return nil, fmt.Errorf("load seeds: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("load seeds: no seed files in %s", seeds)
	}
	log.Printf("anime-identify: crosswalk %+v loaded in %s", crosswalk.Stats(), clk.Now().Sub(started).Round(time.Millisecond))

	stores, err := source.open(ctx)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", source, err)
	}
	defer stores.Close()
	items := store.NewItemStore(stores)
	series, err := items.Query(ctx, store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindSeries}, Recursive: true})
	if err != nil {
		return nil, fmt.Errorf("query series: %w", err)
	}
	seasons, err := items.Query(ctx, store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindSeason}, Recursive: true})
	if err != nil {
		return nil, fmt.Errorf("query seasons: %w", err)
	}

	report := &animeIdentifyReport{ByMethod: map[string]int{}}
	seriesTVDB := make(map[string]int)
	for _, value := range series.Items {
		itemPath := stringOf(value.Path)
		if libraryPath != "" && !strings.HasPrefix(itemPath, strings.TrimRight(libraryPath, "/")+"/") {
			continue
		}
		report.Series++
		query := anime.Query{AllowTitle: true}
		query.AniList = intProvider(value.ProviderIDs, "AniList")
		query.AniDB = intProvider(value.ProviderIDs, "AniDB")
		query.MAL = intProvider(value.ProviderIDs, "MyAnimeList")
		query.TVDB = intProvider(value.ProviderIDs, "Tvdb")
		query.TMDb = intProvider(value.ProviderIDs, "Tmdb")
		query.Names = nonEmpty(stringOf(value.Name), stringOf(value.OriginalTitle), path.Base(itemPath))
		if value.ProductionYear != nil {
			query.Year = *value.ProductionYear
		}
		tvdb, _ := value.ProviderIDs.Get("Tvdb")
		tmdb, _ := value.ProviderIDs.Get("Tmdb")
		result := animeIdentifyResult{Name: stringOf(value.Name), Path: itemPath, TVDB: tvdb, TMDb: tmdb}
		identity, identified := crosswalk.Identify(query)
		if !identified {
			report.Misses = append(report.Misses, result)
			continue
		}
		report.Identified++
		report.ByMethod[string(identity.Method)]++
		result.Method, result.AniList, result.AniDB, result.Title = string(identity.Method), identity.Anime.AniList, identity.Anime.AniDB, identity.Anime.Title
		report.Matches = append(report.Matches, result)
		seriesTVDB[value.ID.String()] = identity.TVDB
	}
	for _, season := range seasons.Items {
		if season.SeriesID == nil || season.IndexNumber == nil {
			continue
		}
		tvdb, ok := seriesTVDB[season.SeriesID.String()]
		if !ok {
			continue
		}
		report.Seasons.Seasons++
		segments := crosswalk.SeasonSegments(tvdb, *season.IndexNumber)
		if len(segments) != 0 {
			report.Seasons.Mapped++
		}
		if len(segments) > 1 {
			report.Seasons.SplitCour++
		}
	}
	sort.Slice(report.Misses, func(i, j int) bool { return report.Misses[i].Name < report.Misses[j].Name })
	sort.Slice(report.Matches, func(i, j int) bool { return report.Matches[i].Name < report.Matches[j].Name })
	return report, nil
}

func stringOf(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func intProvider(ids item.ProviderIDs, key string) int {
	value, ok := ids.Get(key)
	if !ok {
		return 0
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}
