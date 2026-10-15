// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestScannerPersistsEmbeddedAudioGenresWithoutOverwritingLockedMetadata covers
// scanner-owned ffprobe tags end to end: blank audio metadata accepts embedded
// genres, generated taxonomy is MusicGenre, and pre-existing or locked genres
// retain their higher-precedence metadata.
func TestScannerPersistsEmbeddedAudioGenresWithoutOverwritingLockedMetadata(t *testing.T) {
	resolver := &embeddedGenreResolver{}
	harness := newScannerHarness(t, &resolver.scannerFixtureResolver)
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close default scanner: %v", err)
	}
	harness.chain = library.NewChain(resolver)
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		MediaProbe:  embeddedGenreProbe{},
	})

	paths := map[string]string{
		"embedded.mp3":      scannerPath(t, harness.source, "embedded.mp3"),
		"existing.mp3":      scannerPath(t, harness.source, "existing.mp3"),
		"field-locked.mp3":  scannerPath(t, harness.source, "field-locked.mp3"),
		"global-locked.mp3": scannerPath(t, harness.source, "global-locked.mp3"),
	}
	for _, path := range paths {
		writeScannerFile(t, path, "embedded genre fixture")
	}

	scannerScan(t, harness.scanner, "scan embedded audio genres")

	for _, test := range []struct {
		path       string
		wantGenres []string
		wantLocked bool
		wantFields []item.MetadataField
	}{
		{path: paths["embedded.mp3"], wantGenres: []string{"Ambient", "Synthwave"}},
		{path: paths["existing.mp3"], wantGenres: []string{"Existing Genre"}},
		{path: paths["field-locked.mp3"], wantGenres: []string{"Field Locked Genre"}, wantFields: []item.MetadataField{item.MetadataFieldGenres}},
		{path: paths["global-locked.mp3"], wantGenres: []string{"Global Locked Genre"}, wantLocked: true},
	} {
		got := scannerItemAtPath(t, harness.items, test.path)
		if got.Kind != item.KindAudio {
			t.Errorf("scanned %q Kind = %s, want Audio", filepath.Base(test.path), got.Kind)
		}
		if !reflect.DeepEqual(got.Values.Genres, test.wantGenres) {
			t.Errorf("scanned %q Genres = %#v, want %#v", filepath.Base(test.path), got.Values.Genres, test.wantGenres)
		}
		if got.IsLocked != test.wantLocked {
			t.Errorf("scanned %q IsLocked = %t, want %t", filepath.Base(test.path), got.IsLocked, test.wantLocked)
		}
		if !reflect.DeepEqual(got.LockedFields, test.wantFields) {
			t.Errorf("scanned %q LockedFields = %#v, want %#v", filepath.Base(test.path), got.LockedFields, test.wantFields)
		}
	}

	genres, err := harness.items.Query(t.Context(), store.ItemQuery{
		IncludeItemTypes: []item.Kind{item.KindMusicGenre},
		Recursive:        true,
	})
	if err != nil {
		t.Fatalf("query persisted music genres: %v", err)
	}
	gotNames := make([]string, 0, len(genres.Items))
	for _, genre := range genres.Items {
		if genre.Name == nil {
			t.Errorf("persisted MusicGenre missing Name: %#v", genre)
			continue
		}
		gotNames = append(gotNames, *genre.Name)
	}
	sort.Strings(gotNames)
	wantNames := []string{"Ambient", "Existing Genre", "Field Locked Genre", "Global Locked Genre", "Synthwave"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("persisted MusicGenre names = %#v, want %#v", gotNames, wantNames)
	}
}

type embeddedGenreResolver struct{ scannerFixtureResolver }

func (resolver *embeddedGenreResolver) Resolve(ctx context.Context, input library.ResolveContext) (*item.Item, error) {
	value, err := resolver.scannerFixtureResolver.Resolve(ctx, input)
	if err != nil || input.Entry.IsDir {
		return value, err
	}
	value.Kind = item.KindAudio
	switch filepath.Base(input.Entry.Path) {
	case "existing.mp3":
		value.Values.Genres = []string{"Existing Genre"}
	case "field-locked.mp3":
		value.Values.Genres = []string{"Field Locked Genre"}
		value.LockedFields = []item.MetadataField{item.MetadataFieldGenres}
	case "global-locked.mp3":
		value.Values.Genres = []string{"Global Locked Genre"}
		value.IsLocked = true
	}
	return value, nil
}

type embeddedGenreProbe struct{}

func (embeddedGenreProbe) Probe(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	if input.Item.Kind != item.KindAudio {
		return library.MediaProbeResult{}, nil
	}
	return library.MediaProbeResult{Genres: []string{"Ambient", "Synthwave"}}, nil
}
