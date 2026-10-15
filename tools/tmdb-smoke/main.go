// SPDX-License-Identifier: GPL-2.0-only

// Command tmdb-smoke exercises the TMDb provider against the live API.
// It reads the key from TMDB_API_KEY. CI does not run this command because it
// makes real network calls. Run it to make sure that the key works.
// Use -record to capture fresh cassettes for provider tests.
//
// Usage:
//
//	TMDB_API_KEY=xxxx go run ./tools/tmdb-smoke -name "The Matrix" -year 1999
//	TMDB_API_KEY=xxxx go run ./tools/tmdb-smoke -name "The Matrix" -year 1999 \
//	    -record internal/providers/tmdb/testdata/the-matrix.json
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
	"github.com/deadfin-org/deadfin/internal/providers/tmdb"
)

func main() {
	name := flag.String("name", "The Matrix", "movie title to look up")
	year := flag.Int("year", 0, "release year hint (0 for none)")
	language := flag.String("language", "en", "preferred metadata language")
	country := flag.String("country", "US", "metadata country code")
	record := flag.String("record", "", "path to write a recorded cassette (optional)")
	flag.Parse()

	key := strings.TrimSpace(os.Getenv("TMDB_API_KEY"))
	if key == "" {
		fmt.Fprintln(os.Stderr, "TMDB_API_KEY is required for the live smoke run")
		os.Exit(2)
	}

	var recorder *remotetest.Recorder
	var doer remote.Doer
	if *record != "" {
		recorder = remotetest.NewRecorder(http.DefaultTransport)
		doer = recorder
	}
	config := tmdb.DefaultConfig()
	config.APIKey = key
	provider := tmdb.NewProvider(tmdb.Options{
		HTTP: remote.NewClient(remote.Config{
			Doer:           doer,
			UserAgent:      "Deadfin-smoke/dev",
			RequestTimeout: 30 * time.Second,
			CacheEntries:   64,
			CacheFallback:  time.Hour,
		}),
		Config: config,
	})

	target := item.Item{Kind: item.KindMovie, Name: name}
	if *year > 0 {
		target.ProductionYear = year
	}
	refresh := library.RefreshContext{Options: library.LibraryOptions{
		EnableInternetProviders:   true,
		PreferredMetadataLanguage: *language,
		MetadataCountryCode:       *country,
	}}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	handled, err := provider.Refresh(ctx, refresh, &target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "refresh failed: %v\n", err)
		os.Exit(1)
	}
	if !handled {
		fmt.Println("no TMDb match found")
		return
	}

	printField("Name", target.Name)
	printField("OriginalTitle", target.OriginalTitle)
	printField("Overview", target.Overview)
	printField("Tagline", target.Tagline)
	printField("OfficialRating", target.OfficialRating)
	if target.ProductionYear != nil {
		fmt.Printf("%-16s %d\n", "ProductionYear", *target.ProductionYear)
	}
	if target.CommunityRating != nil {
		fmt.Printf("%-16s %.1f\n", "CommunityRating", *target.CommunityRating)
	}
	fmt.Printf("%-16s %v\n", "Genres", target.Values.Genres)
	fmt.Printf("%-16s %v\n", "Studios", target.Values.Studios)
	fmt.Printf("%-16s %d credited\n", "People", len(target.People))
	for _, image := range target.ImageInfos {
		fmt.Printf("%-16s %s %s\n", "Image", image.Type, image.Path)
	}

	if recorder != nil {
		if err := recorder.Save(*record); err != nil {
			fmt.Fprintf(os.Stderr, "save cassette: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nrecorded cassette to %s\n", *record)
	}
}

func printField(label string, value *string) {
	if value != nil && strings.TrimSpace(*value) != "" {
		fmt.Printf("%-16s %s\n", label, *value)
	}
}
