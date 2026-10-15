// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestScannerCanonicalMiniTreeSurvivesStoreReopen exercises the scanner facts
// that a fresh /Items replay reads from ItemStore rather than from a live
// filesystem. The tree deliberately combines the canonical photo, sidecar,
// boxset, disc, and audio conventions in one scan/reopen boundary.
func TestScannerCanonicalMiniTreeSurvivesStoreReopen(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	photos := filepath.Join(root, "photos")
	music := filepath.Join(root, "music")
	for _, path := range []string{movies, photos, music} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("create canonical source %q: %v", path, err)
		}
	}

	subtitleMovie := filepath.Join(movies, "Subtitle Sample (2024)", "Subtitle Sample (2024).mkv")
	writeScannerFile(t, subtitleMovie, "movie")
	writeScannerFile(t, filepath.Join(filepath.Dir(subtitleMovie), "Subtitle Sample (2024).nfo"), `<movie><title>Subtitle Sample</title></movie>`)
	writeScannerFile(t, filepath.Join(filepath.Dir(subtitleMovie), "Subtitle Sample (2024).en.srt"), "1\n00:00:00,000 --> 00:00:01,000\nEnglish\n")

	boxset := filepath.Join(movies, "Synthetic Collection [boxset]")
	boxsetPaths := []string{
		filepath.Join(boxset, "Collection Film One (2020).mkv"),
		filepath.Join(boxset, "Collection Film Three (2022).mkv"),
		filepath.Join(boxset, "Collection Film Two (2021).mkv"),
	}
	for _, path := range boxsetPaths {
		writeScannerFile(t, path, "boxset movie")
	}
	disc := filepath.Join(movies, "Synthetic Disc.bluray.disc")
	writeScannerFile(t, disc, "disc placeholder")

	photoOne := filepath.Join(photos, "orientation-1.jpg")
	photoTwo := filepath.Join(photos, "orientation-2.jpg")
	copyScannerReplayImage(t, filepath.Join("..", "images", "testdata", "stock-corpus", "inputs", "orientation-1.jpg"), photoOne)
	copyScannerReplayImage(t, filepath.Join("..", "images", "testdata", "stock-corpus", "inputs", "orientation-2.jpg"), photoTwo)
	photoModified := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, path := range []string{photoOne, photoTwo} {
		if err := os.Chtimes(path, photoModified, photoModified); err != nil {
			t.Fatalf("set deterministic image timestamp for %q: %v", path, err)
		}
	}

	track := filepath.Join(music, "Goldenrec Generator", "Synthetic Album", "4 - Synthetic Track 4.mp3")
	writeScannerFile(t, track, "audio")

	created := map[string]time.Time{
		photoOne: time.Date(2025, time.January, 2, 3, 4, 6, 0, time.UTC),
		photoTwo: time.Date(2025, time.January, 2, 3, 4, 7, 0, time.UTC),
	}
	database := filepath.Join(t.TempDir(), "items.db")
	stores, err := sqlite.Open(ctx, database)
	if err != nil {
		t.Fatalf("open scanner replay store: %v", err)
	}
	topology, err := library.NewTopology(t.TempDir(), store.NewLibraryStore(stores))
	if err != nil {
		_ = stores.Close()
		t.Fatalf("open scanner replay topology: %v", err)
	}
	for _, source := range []struct {
		name, collection, path string
	}{
		{name: "Movies", collection: "movies", path: movies},
		{name: "Photos", collection: "photos", path: photos},
		{name: "Music", collection: "music", path: music},
	} {
		if _, err := topology.AddVirtualFolder(ctx, source.name, source.collection, library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source.path}}}); err != nil {
			_ = topology.Close()
			_ = stores.Close()
			t.Fatalf("add %s scanner source: %v", source.collection, err)
		}
	}

	items := store.NewItemStore(stores)
	scanner, err := library.NewScanner(topology, items, library.NewDefaultChain(naming.DefaultOptions()), library.ScannerOptions{
		Concurrency: 1,
		ReadDir:     scannerReplayReadDir(created),
		Refresher: providers.NewRegistry(
			providers.NewLocalImageProvider(),
			providers.NewNFOProvider(providers.NFOProviderOptions{Open: func(_ context.Context, path string) (io.ReadCloser, error) { return os.Open(path) }}),
		),
		MediaProbe: scannerReplayProbe{},
	})
	if err != nil {
		_ = topology.Close()
		_ = stores.Close()
		t.Fatalf("create scanner replay scanner: %v", err)
	}
	result, err := scanner.Scan(ctx)
	if err != nil {
		_ = scanner.Close()
		_ = topology.Close()
		_ = stores.Close()
		t.Fatalf("scan canonical mini-tree: %v", err)
	}
	if len(result.Errors) != 0 {
		_ = scanner.Close()
		_ = topology.Close()
		_ = stores.Close()
		t.Fatalf("canonical mini-tree scan errors = %v", result.Errors)
	}
	if err := scanner.Close(); err != nil {
		_ = topology.Close()
		_ = stores.Close()
		t.Fatalf("close scanner replay scanner: %v", err)
	}
	if err := topology.Close(); err != nil {
		_ = stores.Close()
		t.Fatalf("close scanner replay topology: %v", err)
	}
	if err := stores.Close(); err != nil {
		t.Fatalf("close scanner replay store: %v", err)
	}

	reopened, err := sqlite.Open(ctx, database)
	if err != nil {
		t.Fatalf("reopen scanner replay store: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened scanner replay store: %v", err)
		}
	})
	persisted := store.NewItemStore(reopened)

	firstPhoto := scannerItemAtPath(t, persisted, photoOne)
	secondPhoto := scannerItemAtPath(t, persisted, photoTwo)
	for _, photo := range []item.Item{firstPhoto, secondPhoto} {
		if photo.Kind != item.KindPhoto || photo.MediaType == nil || *photo.MediaType != item.MediaTypePhoto {
			t.Fatalf("persisted photo = %#v, want Photo with MediaType Photo", photo)
		}
		if photo.Extra.Photo == nil || photo.Extra.Photo.Orientation == nil {
			t.Fatalf("persisted photo metadata = %#v, want image orientation", photo.Extra.Photo)
		}
		if photo.DateCreated == nil {
			t.Fatalf("persisted photo = %#v, want DateCreated", photo)
		}
	}
	if *firstPhoto.Extra.Photo.Orientation != item.ImageOrientationTopLeft || *secondPhoto.Extra.Photo.Orientation != item.ImageOrientationTopRight {
		t.Fatalf("persisted photo orientations = %s, %s; want TopLeft, TopRight", firstPhoto.Extra.Photo.Orientation.String(), secondPhoto.Extra.Photo.Orientation.String())
	}
	photosByDate, err := persisted.Query(ctx, store.ItemQuery{
		Recursive:              true,
		MediaTypes:             []item.MediaType{item.MediaTypePhoto},
		SortBy:                 []store.ItemSortBy{store.ItemSortByDateCreated},
		SortOrder:              []store.SortOrder{store.SortOrderAscending},
		EnableTotalRecordCount: true,
	})
	if err != nil {
		t.Fatalf("query persisted photos by DateCreated: %v", err)
	}
	if got, want := scannerReplayPaths(photosByDate.Items), []string{photoOne, photoTwo}; !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted photo DateCreated order = %v, want %v", got, want)
	}
	assertScannerReplayImage(t, firstPhoto, photoOne, photoModified)

	subtitle := scannerItemAtPath(t, persisted, subtitleMovie)
	if subtitle.SortName == nil || *subtitle.SortName != "subtitle sample" {
		t.Fatalf("persisted subtitle movie SortName = %q, want subtitle sample", scannerFixtureString(subtitle.SortName))
	}
	streams, err := persisted.Streams(ctx, subtitle.ID)
	if err != nil {
		t.Fatalf("load persisted subtitle streams: %v", err)
	}
	if !scannerReplayHasExternalEnglishSubtitle(streams, filepath.Join(filepath.Dir(subtitleMovie), "Subtitle Sample (2024).en.srt")) {
		t.Fatalf("persisted subtitle streams = %#v, want English external sidecar", streams)
	}

	collection := scannerItemAtPath(t, persisted, boxset)
	if collection.Kind != item.KindBoxSet || len(collection.LinkedChildren) != len(boxsetPaths) {
		t.Fatalf("persisted boxset = %#v, want exact three manual children", collection)
	}
	for index, path := range boxsetPaths {
		child := scannerItemAtPath(t, persisted, path)
		link := collection.LinkedChildren[index]
		if link.ItemID != child.ID || link.Type != item.LinkedChildTypeManual || link.SortOrder == nil || *link.SortOrder != index {
			t.Fatalf("boxset linked child %d = %#v, want Manual link to %s in source order", index, link, child.ID)
		}
	}
	uncollapsed, err := persisted.Query(ctx, store.ItemQuery{Recursive: true, MediaTypes: []item.MediaType{item.MediaTypeVideo}, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query uncollapsed video tree: %v", err)
	}
	if got := scannerReplayPaths(uncollapsed.Items); !scannerReplayContainsAll(got, boxsetPaths) {
		t.Fatalf("uncollapsed video paths = %v, want all physical boxset children %v", got, boxsetPaths)
	}

	placeholder := scannerItemAtPath(t, persisted, disc)
	if placeholder.Kind != item.KindMovie || !placeholder.IsPlaceHolder || placeholder.VideoType == nil || *placeholder.VideoType != item.VideoTypeBluRay {
		t.Fatalf("persisted disc placeholder = %#v, want BluRay placeholder Movie", placeholder)
	}

	audio := scannerItemAtPath(t, persisted, track)
	if audio.Kind != item.KindAudio || audio.RunTimeTicks == nil || *audio.RunTimeTicks != 10_448_980 {
		t.Fatalf("persisted MP3 = %#v, want Audio runtime 10448980 ticks", audio)
	}
}

type scannerReplayProbe struct{}

func (scannerReplayProbe) Probe(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	result := library.MediaProbeResult{}
	if input.Item.Container != nil {
		result.Container = *input.Item.Container
	}
	if input.Item.Kind == item.KindAudio {
		runtime := int64(10_448_980)
		result.RunTimeTicks = &runtime
		result.Streams = []playback.MediaStream{{Index: 0, Type: playback.StreamTypeAudio, Codec: "mp3"}}
	}
	return result, nil
}

func scannerReplayReadDir(created map[string]time.Time) func(string) ([]library.Entry, error) {
	return func(path string) ([]library.Entry, error) {
		entries, err := scannerReadDir(path)
		if err != nil {
			return nil, err
		}
		for index := range entries {
			if value, ok := created[entries[index].Path]; ok {
				entries[index].Created = value
			}
		}
		return entries, nil
	}
}

func copyScannerReplayImage(t *testing.T, source, destination string) {
	t.Helper()
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read scanner image fixture %q: %v", source, err)
	}
	writeScannerFile(t, destination, string(contents))
}

func assertScannerReplayImage(t *testing.T, photo item.Item, path string, modified time.Time) {
	t.Helper()
	if len(photo.ImageInfos) != 1 {
		t.Fatalf("persisted photo image metadata = %#v, want one primary image", photo.ImageInfos)
	}
	image := photo.ImageInfos[0]
	if image.Type != item.ImageTypePrimary || image.Path != path || image.DateModified == nil || !image.DateModified.Equal(modified) {
		t.Fatalf("persisted local image = %#v, want primary %q at %s", image, path, modified)
	}
	if image.BlurHash == nil {
		t.Fatalf("persisted local image = %#v, want stock-compatible BlurHash", image)
	}
	if want := scannerReplayStockBlurHash(); want != "" && *image.BlurHash != want {
		t.Fatalf("persisted local image BlurHash = %q, want stock orientation-1.jpg hash %q", *image.BlurHash, want)
	}
	if got, want := images.CacheTag(image.Path, *image.DateModified), images.CacheTag(path, modified); got != want {
		t.Fatalf("persisted local image cache tag = %q, want %q", got, want)
	}
}

func scannerReplayHasExternalEnglishSubtitle(streams []playback.MediaStream, path string) bool {
	for _, stream := range streams {
		if stream.Type == playback.StreamTypeSubtitle && stream.IsExternal && stream.Language != nil && *stream.Language == "eng" && stream.Path == path {
			return true
		}
	}
	return false
}

func scannerReplayPaths(values []item.Item) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value.Path != nil {
			result = append(result, *value.Path)
		}
	}
	return result
}

func scannerReplayContainsAll(values, expected []string) bool {
	found := make(map[string]struct{}, len(values))
	for _, value := range values {
		found[value] = struct{}{}
	}
	for _, value := range expected {
		if _, ok := found[value]; !ok {
			return false
		}
	}
	return true
}
