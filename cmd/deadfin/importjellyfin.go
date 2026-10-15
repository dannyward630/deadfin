// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/jfimport"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/playlists"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/jellyfinsource"
)

// importJellyfin runs the one-way Jellyfin import (D3). It reads a copy of
// Jellyfin's jellyfin.db read-only, imports users, device tokens, API keys,
// watch state, playlists, and collections, and matches the watch state to the items Deadfin's scan
// found. With --jellyfin-root it first copies Jellyfin library definitions
// that Deadfin lacks; scan those libraries before importing watch state.
// Stop the server first. The command is safe to rerun.
//
// Usage: deadfin import-jellyfin --jellyfin-db /path/jellyfin.db [--jellyfin-root /path/root/default] [--dry-run]
func importJellyfin(args []string) {
	flags := flag.NewFlagSet("import-jellyfin", flag.ExitOnError)
	jellyfinDB := flags.String("jellyfin-db", "", "path to a copy of Jellyfin's data/jellyfin.db (opened read-only)")
	jellyfinRoot := flags.String("jellyfin-root", "", "optional: Jellyfin's root/default directory; copy library definitions Deadfin lacks")
	dryRun := flags.Bool("dry-run", false, "report what would be imported and write nothing")
	programData := flags.String("program-data", "", programDataUsage)
	databaseURL := flags.String("database-url", "", databaseURLUsage)
	if err := flags.Parse(args); err != nil {
		log.Fatal(err)
	}
	if *jellyfinDB == "" && *jellyfinRoot == "" {
		log.Fatal("usage: deadfin import-jellyfin --jellyfin-db <jellyfin.db> [--jellyfin-root <root/default>] [--dry-run] [--program-data <root>] [--database-url <url>]")
	}
	if err := runImportJellyfin(context.Background(), os.Stdout, resolveProgramData(*programData, os.Getenv), resolveDatabaseURL(*databaseURL, os.Getenv(databaseURLEnv)), *jellyfinDB, *jellyfinRoot, *dryRun); err != nil {
		log.Fatal(err)
	}
}

func runImportJellyfin(ctx context.Context, out io.Writer, programData, databaseURL, jellyfinDB, jellyfinRoot string, dryRun bool) (err error) {
	paths, err := config.NewPaths(programData)
	if err != nil {
		return fmt.Errorf("derive program-data paths: %w", err)
	}
	if jellyfinRoot != "" {
		if err := copyJellyfinLibraries(out, jellyfinRoot, filepath.Join(paths.Root, "root", "default"), dryRun); err != nil {
			return err
		}
	}
	if jellyfinDB == "" {
		return nil
	}
	src, err := jellyfinsource.Open(ctx, jellyfinDB)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := src.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	stores, _, err := openProgramDatabase(ctx, databaseURL, paths.Data)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := stores.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close stores: %w", closeErr)
		}
	}()
	identity, items := store.NewIdentity(stores), store.NewItemStore(stores)
	options := jfimport.Options{DryRun: dryRun}
	if !dryRun {
		// Playlists live under the PlaylistsFolder, which the server creates
		// at startup. Create it here too so an import into a fresh database
		// keeps its playlists.
		if err := items.BootstrapSystemFolders(ctx, paths.Root); err != nil {
			return fmt.Errorf("bootstrap system folders: %w", err)
		}
		var playlistService *playlists.Service
		playlistService, err = playlists.New(playlists.Options{Items: items, Users: identityUsers{identity}, Clock: clock.NewReal(), ProgramData: paths.Root, DataPath: paths.Data})
		if err != nil {
			return fmt.Errorf("open playlists: %w", err)
		}
		options.Playlists = playlistService
		var topology *library.Topology
		topology, err = library.NewTopology(paths.Root, store.NewLibraryStore(stores))
		if err != nil {
			return fmt.Errorf("open topology: %w", err)
		}
		defer func() {
			if closeErr := topology.Close(); err == nil && closeErr != nil {
				err = fmt.Errorf("close topology: %w", closeErr)
			}
		}()
		var scanner *library.Scanner
		scanner, err = library.NewScanner(topology, items, library.NewDefaultChain(naming.DefaultOptions()), library.ScannerOptions{CollectionsPath: filepath.Join(paths.Data, "collections")})
		if err != nil {
			return fmt.Errorf("open scanner: %w", err)
		}
		defer func() {
			if closeErr := scanner.Close(); err == nil && closeErr != nil {
				err = fmt.Errorf("close scanner: %w", closeErr)
			}
		}()
		options.Collections = collectionImporter{scanner: scanner, items: items}
	}
	report, err := jfimport.Run(ctx, src, identity, items, options)
	printImportReport(out, report, dryRun)
	return err
}

func printImportReport(out io.Writer, report jfimport.Report, dryRun bool) {
	if dryRun {
		fmt.Fprintln(out, "dry run: nothing was written")
	}
	fmt.Fprintf(out, "users: %d created with new ids, %d matched by name (keep their id, password, and policy)\n", report.UsersCreated, report.UsersMerged)
	fmt.Fprintf(out, "device tokens: %d imported, %d already present\n", report.DevicesImported, report.DevicesExisting)
	fmt.Fprintf(out, "api keys: %d imported, %d already present\n", report.APIKeysImported, report.APIKeysExisting)
	matched := report.UserDataByKey + report.UserDataByPath
	live := matched + report.UserDataUnmatchedLive
	percent := 0.0
	if live > 0 {
		percent = 100 * float64(matched) / float64(live)
	}
	fmt.Fprintf(out, "watch state: %d matched (%d by key, %d by path) = %.1f%% of rows whose item still exists\n", matched, report.UserDataByKey, report.UserDataByPath, percent)
	fmt.Fprintf(out, "watch state not imported: %d for items that still exist in Jellyfin, %d for items Jellyfin no longer has, %d for unknown users\n", report.UserDataUnmatchedLive, report.UserDataUnmatchedOrphaned, report.UserDataSkippedUser)
	roots := make([]string, 0, len(report.UserDataUnmatchedByRoot))
	for root := range report.UserDataUnmatchedByRoot {
		roots = append(roots, root)
	}
	sort.Slice(roots, func(i, j int) bool {
		return report.UserDataUnmatchedByRoot[roots[i]] > report.UserDataUnmatchedByRoot[roots[j]]
	})
	for _, root := range roots {
		fmt.Fprintf(out, "  unmatched under %s: %d\n", root, report.UserDataUnmatchedByRoot[root])
	}
	fmt.Fprintf(out, "collections: %d imported; members: %d matched, %d not found in Deadfin\n", report.CollectionsImported, report.CollectionEntriesMatched, report.CollectionEntriesUnmatched)
	fmt.Fprintf(out, "playlists: %d imported, %d already present, %d skipped; entries: %d matched, %d not found in Deadfin\n", report.PlaylistsImported, report.PlaylistsExisting, report.PlaylistsSkipped, report.PlaylistEntriesMatched, report.PlaylistEntriesUnmatched)
	for _, warning := range report.Warnings {
		fmt.Fprintf(out, "warning: %s\n", warning)
	}
}

// copyJellyfinLibraries copies each Jellyfin library directory that Deadfin
// lacks. Deadfin reads the same layout: options.xml, a <type>.collection
// marker, and .mblink files naming the media paths. A library whose media
// path does not exist here is skipped.
func copyJellyfinLibraries(out io.Writer, jellyfinRoot, deadfinRoot string, dryRun bool) error {
	entries, err := os.ReadDir(jellyfinRoot)
	if err != nil {
		return fmt.Errorf("read jellyfin libraries: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		target := filepath.Join(deadfinRoot, name)
		if _, err := os.Stat(target); err == nil {
			fmt.Fprintf(out, "library %q: already present\n", name)
			continue
		}
		source := filepath.Join(jellyfinRoot, name)
		files, err := os.ReadDir(source)
		if err != nil {
			return fmt.Errorf("read library %q: %w", name, err)
		}
		var missing []string
		var copyFiles []string
		for _, file := range files {
			// Skip hidden files such as macOS AppleDouble "._movies.mblink",
			// which a copy made on a Mac adds beside every file.
			if file.IsDir() || strings.HasPrefix(file.Name(), ".") {
				continue
			}
			switch ext := filepath.Ext(file.Name()); {
			case ext == ".mblink":
				link, err := os.ReadFile(filepath.Join(source, file.Name()))
				if err != nil {
					return fmt.Errorf("read %s: %w", file.Name(), err)
				}
				mediaPath := strings.TrimSpace(string(link))
				if _, err := os.Stat(mediaPath); err != nil {
					missing = append(missing, mediaPath)
				}
				copyFiles = append(copyFiles, file.Name())
			case ext == ".collection" || file.Name() == "options.xml":
				copyFiles = append(copyFiles, file.Name())
			}
		}
		if len(missing) > 0 {
			fmt.Fprintf(out, "library %q: skipped, media path not available here: %s\n", name, strings.Join(missing, ", "))
			continue
		}
		fmt.Fprintf(out, "library %q: copied (%s)\n", name, strings.Join(copyFiles, ", "))
		if dryRun {
			continue
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("create library %q: %w", name, err)
		}
		for _, file := range copyFiles {
			contents, err := os.ReadFile(filepath.Join(source, file))
			if err != nil {
				return fmt.Errorf("read %s/%s: %w", name, file, err)
			}
			if err := os.WriteFile(filepath.Join(target, file), contents, 0o644); err != nil {
				return fmt.Errorf("write %s/%s: %w", name, file, err)
			}
		}
	}
	return nil
}

// identityUsers adapts the identity store to playlists.Users.
type identityUsers struct{ identity *store.Identity }

func (u identityUsers) Get(ctx context.Context, id dto.UUID) (store.User, error) {
	return u.identity.User(ctx, id)
}

// collectionImporter creates or finds a collection by name and links its
// members.
type collectionImporter struct {
	scanner *library.Scanner
	items   *store.ItemStore
}

func (c collectionImporter) ImportCollection(ctx context.Context, name string, children []dto.UUID) error {
	collection, err := c.scanner.CreateCollection(ctx, name, false)
	if err != nil {
		return err
	}
	if len(children) == 0 {
		return nil
	}
	return c.items.AddToCollection(ctx, collection.ID, children)
}
