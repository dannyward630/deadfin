// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/probe"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

func main() {
	if err := run(); err != nil {
		fatalf("%v", err)
	}
}

func run() error {
	state := flag.String("state", "", "Deadfin program-data root")
	flag.Parse()
	if *state == "" {
		return fmt.Errorf("-state is required")
	}
	return bootstrap(context.Background(), *state)
}

type bootstrapOptions struct {
	mediaRoot  string
	mediaProbe func(context.Context) (library.MediaProbe, error)
}

type bootstrapLibrary struct {
	name string
	kind string
	path string
}

func bootstrap(ctx context.Context, state string) error {
	return bootstrapWithOptions(ctx, state, bootstrapOptions{
		mediaRoot:  "/media",
		mediaProbe: newMediaProbe,
	})
}

func bootstrapWithOptions(ctx context.Context, state string, options bootstrapOptions) (err error) {
	if options.mediaRoot == "" {
		return fmt.Errorf("media root is required")
	}
	if options.mediaProbe == nil {
		options.mediaProbe = newMediaProbe
	}
	paths, err := config.NewPaths(state)
	if err != nil {
		return fmt.Errorf("derive program-data paths: %w", err)
	}
	if err := os.MkdirAll(paths.Root, 0o700); err != nil {
		return fmt.Errorf("create state: %w", err)
	}
	data := paths.Data
	if err := os.MkdirAll(data, 0o700); err != nil {
		return fmt.Errorf("create data: %w", err)
	}
	stores, err := sqlite.Open(ctx, filepath.Join(data, "deadfin.db"))
	if err != nil {
		return fmt.Errorf("open stores: %w", err)
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
	libraries := []bootstrapLibrary{
		{name: "Golden Movies", kind: "movies", path: filepath.Join(options.mediaRoot, "movies")},
		{name: "Golden TV", kind: "tvshows", path: filepath.Join(options.mediaRoot, "tv")},
		{name: "Golden Music", kind: "music", path: filepath.Join(options.mediaRoot, "music")},
		{name: "Golden Photos", kind: "homevideos", path: filepath.Join(options.mediaRoot, "photos")},
	}
	existing, err := topology.VirtualFolders(ctx)
	if err != nil {
		return fmt.Errorf("list virtual folders: %w", err)
	}
	existingByName := make(map[string]library.VirtualFolder, len(existing))
	for _, folder := range existing {
		existingByName[folder.Name] = folder
	}
	for _, folder := range libraries {
		if current, found := existingByName[folder.name]; found {
			if current.CollectionType != folder.kind || len(current.MediaPaths) != 1 || current.MediaPaths[0] != folder.path {
				return fmt.Errorf("existing %s does not match bootstrap configuration", folder.name)
			}
			continue
		}
		if _, err := topology.AddVirtualFolder(ctx, folder.name, folder.kind, library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: folder.path}}}); err != nil {
			return fmt.Errorf("add %s: %w", folder.name, err)
		}
	}
	mediaProbe, err := options.mediaProbe(ctx)
	if err != nil {
		return fmt.Errorf("open media probe: %w", err)
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
	scanner, err := library.NewScanner(topology, store.NewItemStore(stores), library.NewDefaultChain(naming.DefaultOptions()), library.ScannerOptions{Refresher: registry, MediaProbe: mediaProbe})
	if err != nil {
		return fmt.Errorf("new scanner: %w", err)
	}
	defer func() {
		if closeErr := scanner.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close scanner: %w", closeErr)
		}
	}()
	result, err := scanner.Scan(ctx)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	if len(result.Errors) != 0 {
		return fmt.Errorf("scan errors: %v", result.Errors)
	}
	changes := events.NewBus[config.Change](clock.NewReal())
	defer func() {
		if closeErr := changes.Close(context.Background()); err == nil && closeErr != nil {
			err = fmt.Errorf("close configuration changes: %w", closeErr)
		}
	}()
	manager, err := config.Open(ctx, paths, changes)
	if err != nil {
		return fmt.Errorf("open configuration: %w", err)
	}
	if err := manager.UpdateSystem(ctx, func(configuration *config.ServerConfiguration) error {
		configuration.IsStartupWizardCompleted = true
		return nil
	}); err != nil {
		return fmt.Errorf("complete startup wizard: %w", err)
	}
	return nil
}

type mediaProbe struct{ prober ffmpeg.Prober }

func newMediaProbe(ctx context.Context) (library.MediaProbe, error) {
	prober, err := ffmpeg.NewProber(ctx, "ffprobe")
	if err != nil {
		return nil, err
	}
	return mediaProbe{prober: prober}, nil
}

func (p mediaProbe) Probe(ctx context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	source, err := sourcefile.Open(input.Entry.Path, entryExpected(input.Entry))
	if err != nil {
		return library.MediaProbeResult{}, fmt.Errorf("pin scan source: %w", err)
	}
	defer func() { _ = source.Close() }()

	raw, err := p.prober.ProbeSourceAndValidate(ctx, source)
	if err != nil {
		return library.MediaProbeResult{}, err
	}
	definition, found := item.Definition(input.Item.Kind)
	info, err := probe.Normalize(ctx, raw, probe.Options{IsAudio: found && definition.DefaultMediaType == item.MediaTypeAudio, Path: input.Entry.Path, Protocol: "File", VideoType: item.VideoTypeVideoFile.String()})
	if err != nil {
		return library.MediaProbeResult{}, err
	}
	result := library.MediaProbeResult{Container: info.Container, Size: info.Size, RunTimeTicks: info.RunTimeTicks, TotalBitrate: info.Bitrate, Artists: info.Artists, AlbumArtists: info.AlbumArtists, IndexNumber: info.IndexNumber, ParentIndexNumber: info.ParentIndexNumber, ProductionYear: info.ProductionYear, PremiereDate: info.PremiereDate, Streams: info.MediaStreams, Attachments: info.MediaAttachments}
	if info.Name != nil {
		result.Name = *info.Name
	}
	if info.Album != nil {
		result.Album = *info.Album
	}
	if info.VideoType != nil {
		value := item.VideoType(*info.VideoType)
		result.VideoType = &value
	}
	if info.IsoType != nil {
		value := item.IsoType(*info.IsoType)
		result.IsoType = &value
	}
	if info.Video3DFormat != nil {
		value := item.Video3DFormat(*info.Video3DFormat)
		result.Video3DFormat = &value
	}
	return result, nil
}

func entryExpected(entry library.Entry) sourcefile.Expected {
	size := entry.Size
	expected := sourcefile.Expected{Size: &size}
	if !entry.Modified.IsZero() {
		modified := entry.Modified
		expected.ModifiedAt = &modified
	}
	if entry.Identity.Valid {
		inode := int64(entry.Identity.Inode)
		changed := entry.Identity.Changed
		expected.Inode, expected.ChangedAt = &inode, &changed
	}
	return expected
}

func fatalf(format string, values ...any) {
	fmt.Fprintf(os.Stderr, "finalreplaybootstrap: "+format+"\n", values...)
	os.Exit(1)
}
