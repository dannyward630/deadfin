// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// addLibrary registers a media library (virtual folder) in program data.
// It provides the behavior planned for POST /Library/VirtualFolders.
// Stop the server before you run this command. Restart the server afterward.
// Then trigger a scan. The command is idempotent by library name.
//
// Usage: deadfin add-library --name Movies --type movies --path /media/movies
func addLibrary(args []string) {
	flags := flag.NewFlagSet("add-library", flag.ExitOnError)
	name := flags.String("name", "", "library display name (e.g. Movies)")
	kind := flags.String("type", "", "collection type: movies, tvshows, music, homevideos, books, ...")
	path := flags.String("path", "", "physical media path (e.g. /media/movies)")
	programData := flags.String("program-data", "", programDataUsage)
	databaseURL := flags.String("database-url", "", databaseURLUsage)
	if err := flags.Parse(args); err != nil {
		log.Fatal(err)
	}
	if *name == "" || *kind == "" || *path == "" {
		log.Fatal("usage: deadfin add-library --name <name> --type <collectionType> --path <mediaPath> [--program-data <root>] [--database-url <url>]")
	}
	if err := runAddLibrary(context.Background(), resolveProgramData(*programData, os.Getenv), resolveDatabaseURL(*databaseURL, os.Getenv(databaseURLEnv)), *name, *kind, *path); err != nil {
		log.Fatal(err)
	}
}

func runAddLibrary(ctx context.Context, programData, databaseURL, name, kind, path string) (err error) {
	paths, err := config.NewPaths(programData)
	if err != nil {
		return fmt.Errorf("derive program-data paths: %w", err)
	}
	if err := os.MkdirAll(paths.Root, 0o700); err != nil {
		return fmt.Errorf("create root: %w", err)
	}
	if err := os.MkdirAll(paths.Data, 0o700); err != nil {
		return fmt.Errorf("create data: %w", err)
	}
	stores, _, err := openProgramDatabase(ctx, databaseURL, paths.Data)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := stores.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close stores: %w", closeErr)
		}
	}()
	items := store.NewItemStore(stores)
	if err := items.BootstrapSystemFolders(ctx, paths.Root); err != nil {
		return fmt.Errorf("bootstrap system folders: %w", err)
	}
	topology, err := library.NewTopology(paths.Root, store.NewLibraryStore(stores))
	if err != nil {
		return fmt.Errorf("open topology: %w", err)
	}
	defer func() {
		if closeErr := topology.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close topology: %w", closeErr)
		}
	}()

	existing, err := topology.VirtualFolders(ctx)
	if err != nil {
		return fmt.Errorf("list virtual folders: %w", err)
	}
	for _, folder := range existing {
		if folder.Name == name {
			fmt.Printf("library %q already exists (type %s); nothing to do\n", name, folder.CollectionType)
			return nil
		}
	}

	options := library.DefaultLibraryOptions()
	options.EnableInternetProviders = true
	options.PathInfos = []library.MediaPathInfo{{Path: path}}
	folder, err := topology.AddVirtualFolder(ctx, name, kind, options)
	if err != nil {
		return fmt.Errorf("add virtual folder: %w", err)
	}
	fmt.Printf("added library %q (type %s) -> %s [id %v]\n", folder.Name, folder.CollectionType, path, folder.ID)
	return nil
}
