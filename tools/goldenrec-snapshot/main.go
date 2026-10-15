// SPDX-License-Identifier: GPL-2.0-only

// goldenrec-snapshot scans a generated Goldenrec fixture tree with Deadfin's
// production scanner components and emits durable item fields for comparison.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

type snapshotItem struct {
	Kind           item.Kind           `json:"kind"`
	Path           string              `json:"path"`
	Name           *string             `json:"name,omitempty"`
	ProductionYear *int                `json:"productionYear,omitempty"`
	Genres         []string            `json:"genres,omitempty"`
	People         []item.PersonCredit `json:"people,omitempty"`
	ProviderIDs    map[string]string   `json:"providerIds,omitempty"`
	ImageInfos     []item.ImageInfo    `json:"imageInfos,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fatalf("%v", err)
	}
}

func run() (err error) {
	fixture := flag.String("fixture-root", "", "generated Goldenrec fixture root")
	data := flag.String("data", "", "temporary Deadfin program-data directory")
	output := flag.String("output", "", "snapshot JSON output")
	flag.Parse()
	if *fixture == "" || *data == "" || *output == "" {
		return fmt.Errorf("-fixture-root, -data, and -output are required")
	}
	ctx := context.Background()
	stores, err := sqlite.Open(ctx, filepath.Join(*data, "deadfin.db"))
	if err != nil {
		return fmt.Errorf("open stores: %w", err)
	}
	defer func() {
		if closeErr := stores.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close stores: %w", closeErr)
		}
	}()
	topology, err := library.NewTopology(*data, store.NewLibraryStore(stores))
	if err != nil {
		return fmt.Errorf("open topology: %w", err)
	}
	defer func() {
		if closeErr := topology.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close topology: %w", closeErr)
		}
	}()
	for _, folder := range []struct{ name, kind, relative string }{
		{"Golden Movies", "movies", "movies"}, {"Golden TV", "tvshows", "tv"}, {"Golden Music", "music", "music"}, {"Golden Photos", "photos", "photos"},
	} {
		path := filepath.Join(*fixture, folder.relative)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if _, err := topology.AddVirtualFolder(ctx, folder.name, folder.kind, library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: path}}}); err != nil {
			return fmt.Errorf("add %s: %w", folder.name, err)
		}
	}
	registry := providers.NewRegistry(
		providers.NewLocalImageProvider(),
		providers.NewNFOProvider(providers.NFOProviderOptions{Open: func(ctx context.Context, path string) (io.ReadCloser, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return os.Open(path)
		}}),
	)
	scanner, err := library.NewScanner(topology, store.NewItemStore(stores), library.NewDefaultChain(naming.DefaultOptions()), library.ScannerOptions{Refresher: registry})
	if err != nil {
		return fmt.Errorf("new scanner: %w", err)
	}
	defer func() {
		if closeErr := scanner.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close scanner: %w", closeErr)
		}
	}()
	scan, err := scanner.Scan(ctx)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	if len(scan.Errors) != 0 {
		return fmt.Errorf("scan completed with errors: %v", scan.Errors)
	}
	result, err := store.NewItemStore(stores).Query(ctx, store.ItemQuery{Recursive: true})
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	out := make([]snapshotItem, 0, len(result.Items))
	for _, value := range result.Items {
		if value.Path == nil {
			continue
		}
		out = append(out, snapshotItem{Kind: value.Kind, Path: *value.Path, Name: value.Name, ProductionYear: value.ProductionYear, Genres: value.Values.Genres, People: value.People, ProviderIDs: snapshotProviderIDs(value.ProviderIDs), ImageInfos: value.ImageInfos})
	}
	file, err := os.Create(*output)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close output: %w", closeErr)
		}
	}()
	if err := json.NewEncoder(file).Encode(out); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func snapshotProviderIDs(ids item.ProviderIDs) map[string]string {
	out := make(map[string]string)
	for _, id := range ids.Snapshot() {
		out[id.Name] = id.Value
	}
	return out
}

func fatalf(format string, values ...any) {
	fmt.Fprintf(os.Stderr, "goldenrec-snapshot: "+format+"\n", values...)
	os.Exit(1)
}
