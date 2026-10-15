// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

func TestLibraryOptionsXMLRoundTrip(t *testing.T) {
	want := library.LibraryOptions{
		Enabled:                               false,
		EnablePhotos:                          false,
		EnableRealtimeMonitor:                 false,
		EnableChapterImageExtraction:          true,
		ExtractChapterImagesDuringLibraryScan: true,
		PathInfos: []library.MediaPathInfo{
			{Path: "/media/films", NetworkPath: "smb://nas/films"},
		},
		SaveLocalMetadata:                       true,
		EnableInternetProviders:                 true,
		EnableAutomaticSeriesGrouping:           false,
		EnableEmbeddedTitles:                    true,
		EnableEmbeddedEpisodeInfos:              true,
		AutomaticRefreshIntervalDays:            17,
		PreferredMetadataLanguage:               "pt-BR",
		MetadataCountryCode:                     "BR",
		SeasonZeroDisplayName:                   "Especiais",
		MetadataSavers:                          []string{"NfoSaver"},
		DisabledLocalMetadataReaders:            []string{"NfoMetadata", "XmlMetadata"},
		LocalMetadataReaderOrder:                []string{"XmlMetadata", "NfoMetadata"},
		DisabledSubtitleFetchers:                []string{"OpenSubtitles"},
		SubtitleFetcherOrder:                    []string{"LocalSubtitles", "OpenSubtitles"},
		SkipSubtitlesIfEmbeddedSubtitlesPresent: true,
		SkipSubtitlesIfAudioTrackMatches:        false,
		SubtitleDownloadLanguages:               []string{"pt", "en"},
		RequirePerfectSubtitleMatch:             false,
		SaveSubtitlesWithMedia:                  false,
		AutomaticallyAddToCollection:            true,
		AllowEmbeddedSubtitles:                  library.AllowEmbeddedSubtitlesNone,
		TypeOptions: []library.TypeOptions{
			{
				Type:                 "Movie",
				MetadataFetchers:     []string{"LocalMetadata", "TheMovieDb"},
				MetadataFetcherOrder: []string{"TheMovieDb", "LocalMetadata"},
				ImageFetchers:        []string{"TheMovieDb"},
				ImageFetcherOrder:    []string{"TheMovieDb"},
			},
		},
	}

	encoded, err := xml.Marshal(want)
	if err != nil {
		t.Fatalf("marshal library options: %v", err)
	}

	var got library.LibraryOptions
	if err := xml.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal library options: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LibraryOptions XML round-trip = %#v, want %#v", got, want)
	}
}

func TestTopologyCreatesPortableFolderAndReopensRegistry(t *testing.T) {
	topology, registry, dataRoot := openTopology(t)
	mediaPath := existingMediaPath(t, "movies")
	options := library.LibraryOptions{
		EnableEmbeddedTitles:         true,
		AutomaticRefreshIntervalDays: 17,
		PreferredMetadataLanguage:    "pt-BR",
		MetadataCountryCode:          "BR",
		DisabledLocalMetadataReaders: []string{
			"NfoMetadata",
		},
	}

	created, err := topology.AddVirtualFolder(t.Context(), "Cinema", "movies", options)
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	wantPath := filepath.Join(dataRoot, "root", "default", "Cinema")
	if created.Path != wantPath {
		t.Fatalf("created virtual-folder path = %q, want %q", created.Path, wantPath)
	}

	marker, err := os.ReadFile(filepath.Join(wantPath, "movies.collection"))
	if err != nil {
		t.Fatalf("read collection marker: %v", err)
	}
	if len(marker) != 0 {
		t.Fatalf("collection marker = %q, want empty marker", marker)
	}

	optionsXML, err := os.ReadFile(filepath.Join(wantPath, "options.xml"))
	if err != nil {
		t.Fatalf("read options XML: %v", err)
	}
	var optionsFromDisk library.LibraryOptions
	if err := xml.Unmarshal(optionsXML, &optionsFromDisk); err != nil {
		t.Fatalf("unmarshal options XML: %v", err)
	}
	if !reflect.DeepEqual(optionsFromDisk, options) {
		t.Fatalf("options XML = %#v, want %#v", optionsFromDisk, options)
	}
	registered, err := registry.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("get created registry row: %v", err)
	}
	if registered.Options != string(optionsXML) {
		t.Fatalf("registry options = %q, want exact options.xml text %q", registered.Options, optionsXML)
	}

	if err := topology.AddMediaPath(t.Context(), created.ID, mediaPath); err != nil {
		t.Fatalf("add media path: %v", err)
	}
	shortcuts, err := filepath.Glob(filepath.Join(wantPath, "*.mblink"))
	if err != nil {
		t.Fatalf("glob media shortcuts: %v", err)
	}
	if len(shortcuts) != 1 {
		t.Fatalf("media shortcut count = %d, want 1", len(shortcuts))
	}
	shortcut, err := os.ReadFile(shortcuts[0])
	if err != nil {
		t.Fatalf("read media shortcut: %v", err)
	}
	if string(shortcut) != mediaPath {
		t.Fatalf("media shortcut text = %q, want exact target path %q", shortcut, mediaPath)
	}
	wantReopenedOptions := options
	wantReopenedOptions.PathInfos = append([]library.MediaPathInfo(nil), options.PathInfos...)
	wantReopenedOptions.PathInfos = append(wantReopenedOptions.PathInfos, library.MediaPathInfo{Path: mediaPath})

	reopened, err := library.NewTopology(dataRoot, registry)
	if err != nil {
		t.Fatalf("reopen topology: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened topology: %v", err)
		}
	})
	folders, err := reopened.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list reopened virtual folders: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("reopened virtual-folder count = %d, want 1", len(folders))
	}
	got := folders[0]
	if got.ID != created.ID || got.Name != "Cinema" || got.CollectionType != "movies" || !reflect.DeepEqual(got.Options, wantReopenedOptions) || !reflect.DeepEqual(got.MediaPaths, []string{mediaPath}) {
		t.Fatalf("reopened virtual folder = %#v, want ID %s, name Cinema, type movies, options %#v, paths %#v", got, created.ID, wantReopenedOptions, []string{mediaPath})
	}

	if err := reopened.RemoveMediaPath(t.Context(), created.ID, mediaPath); err != nil {
		t.Fatalf("remove media path: %v", err)
	}
	if err := reopened.RemoveVirtualFolder(t.Context(), created.ID); err != nil {
		t.Fatalf("remove virtual folder: %v", err)
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatalf("removed virtual-folder directory stat error = %v, want not exist", err)
	}
	folders, err = reopened.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list removed virtual folders: %v", err)
	}
	if len(folders) != 0 {
		t.Fatalf("virtual folders after removal = %#v, want none", folders)
	}
	libraries, err := registry.List(t.Context())
	if err != nil {
		t.Fatalf("list registry after removal: %v", err)
	}
	if len(libraries) != 0 {
		t.Fatalf("registry after removal = %#v, want no rows", libraries)
	}
}

func TestTopologyUpdateLibraryOptionsSynchronizesPortableState(t *testing.T) {
	topology, registry, _ := openTopology(t)
	firstPath := existingMediaPath(t, "existing")
	secondPath := existingMediaPath(t, "added-by-options")
	created, err := topology.AddVirtualFolder(t.Context(), "Updated", "movies", library.LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	if err := topology.AddMediaPath(t.Context(), created.ID, firstPath); err != nil {
		t.Fatalf("add existing media path: %v", err)
	}
	if err := os.Remove(firstPath); err != nil {
		t.Fatalf("take existing linked media path offline: %v", err)
	}
	want := library.LibraryOptions{
		Enabled:                   true,
		PreferredMetadataLanguage: "fr",
		PathInfos: []library.MediaPathInfo{
			{Path: firstPath},
			{Path: secondPath},
		},
	}
	if err := topology.UpdateLibraryOptions(t.Context(), created.CollectionFolderID, want); err != nil {
		t.Fatalf("update library options: %v", err)
	}

	optionsXML, err := os.ReadFile(filepath.Join(created.Path, "options.xml"))
	if err != nil {
		t.Fatalf("read updated options XML: %v", err)
	}
	var fromDisk library.LibraryOptions
	if err := xml.Unmarshal(optionsXML, &fromDisk); err != nil {
		t.Fatalf("unmarshal updated options XML: %v", err)
	}
	if !reflect.DeepEqual(fromDisk, want) {
		t.Fatalf("updated options XML = %#v, want %#v", fromDisk, want)
	}
	registered, err := registry.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("get updated registry row: %v", err)
	}
	if registered.Options != string(optionsXML) {
		t.Fatalf("registry options = %q, want exact options.xml text %q", registered.Options, optionsXML)
	}
	folders, err := topology.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list updated virtual folders: %v", err)
	}
	if len(folders) != 1 || !reflect.DeepEqual(folders[0].Options, want) || !reflect.DeepEqual(sortedPaths(folders[0].MediaPaths), sortedPaths([]string{firstPath, secondPath})) {
		t.Fatalf("updated virtual folders = %#v, want options %#v and both paths", folders, want)
	}
}

func TestTopologyNullableCollectionTypeCreatesNoMarkerAndExposesCollectionFolderID(t *testing.T) {
	topology, _, _ := openTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Untyped", "", library.LibraryOptions{Enabled: true})
	if err != nil {
		t.Fatalf("create virtual folder with null collection type: %v", err)
	}
	if created.CollectionType != "" {
		t.Fatalf("collection type = %q, want empty for null/omitted input", created.CollectionType)
	}
	entries, err := os.ReadDir(created.Path)
	if err != nil {
		t.Fatalf("read virtual folder directory: %v", err)
	}
	for _, entry := range entries {
		if strings.EqualFold(filepath.Ext(entry.Name()), ".collection") {
			t.Fatalf("null collection type created .collection marker %q", entry.Name())
		}
	}
	if created.CollectionFolderID == (dto.UUID{}) {
		t.Fatalf("CollectionFolderID is zero, want deterministic scanned identity")
	}
	folders, err := topology.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list virtual folders: %v", err)
	}
	if len(folders) != 1 || folders[0].CollectionFolderID != created.CollectionFolderID {
		t.Fatalf("VirtualFolders CollectionFolderID = %#v, want %#v", folders, created.CollectionFolderID)
	}
}

func TestTopologyTreatsShortcutFilesAsMediaPathTruth(t *testing.T) {
	topology, registry, dataRoot := openTopology(t)
	firstPath := existingMediaPath(t, "first")
	secondPath := existingMediaPath(t, "second")
	created, err := topology.AddVirtualFolder(t.Context(), "Concerts", "music", library.LibraryOptions{
		EnableEmbeddedTitles:      true,
		PreferredMetadataLanguage: "de-DE",
		MetadataCountryCode:       "DE",
	})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	if err := topology.AddMediaPath(t.Context(), created.ID, firstPath); err != nil {
		t.Fatalf("add first media path: %v", err)
	}

	shortcuts, err := filepath.Glob(filepath.Join(created.Path, "*.mblink"))
	if err != nil {
		t.Fatalf("glob first media shortcut: %v", err)
	}
	if len(shortcuts) != 1 {
		t.Fatalf("first media shortcut count = %d, want 1", len(shortcuts))
	}
	if err := os.Remove(shortcuts[0]); err != nil {
		t.Fatalf("remove first media shortcut: %v", err)
	}
	if err := os.WriteFile(filepath.Join(created.Path, "manual.mblink"), []byte(secondPath), 0o600); err != nil {
		t.Fatalf("write externally managed media shortcut: %v", err)
	}

	reopened, err := library.NewTopology(dataRoot, registry)
	if err != nil {
		t.Fatalf("reopen topology after external shortcut edit: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened topology: %v", err)
		}
	})
	folders, err := reopened.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list virtual folders after external shortcut edit: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("virtual-folder count after external shortcut edit = %d, want 1", len(folders))
	}
	if !reflect.DeepEqual(sortedPaths(folders[0].MediaPaths), []string{secondPath}) {
		t.Fatalf("media paths after external shortcut edit = %#v, want %#v", folders[0].MediaPaths, []string{secondPath})
	}
}

func TestTopologyRejectsTraversalNamesAndMissingMediaPaths(t *testing.T) {
	topology, _, dataRoot := openTopology(t)

	for _, name := range []string{"../outside", `..\outside`, "nested/name", "nested\\name"} {
		t.Run(name, func(t *testing.T) {
			if _, err := topology.AddVirtualFolder(t.Context(), name, "movies", library.LibraryOptions{
				EnableEmbeddedTitles:      true,
				PreferredMetadataLanguage: "es-ES",
				MetadataCountryCode:       "ES",
			}); err == nil {
				t.Fatalf("create virtual folder %q succeeded, want traversal-safe rejection", name)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "root", "outside")); !os.IsNotExist(err) {
		t.Fatalf("traversal target stat error = %v, want not exist", err)
	}

	created, err := topology.AddVirtualFolder(t.Context(), "Pictures", "homevideos", library.LibraryOptions{
		EnableEmbeddedTitles:      true,
		PreferredMetadataLanguage: "es-ES",
		MetadataCountryCode:       "ES",
	})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	missingPath := filepath.Join(t.TempDir(), "does-not-exist")
	if err := topology.AddMediaPath(t.Context(), created.ID, missingPath); err == nil {
		t.Fatalf("add missing media path %q succeeded", missingPath)
	}
	shortcuts, err := filepath.Glob(filepath.Join(created.Path, "*.mblink"))
	if err != nil {
		t.Fatalf("glob shortcuts after rejected path: %v", err)
	}
	if len(shortcuts) != 0 {
		t.Fatalf("shortcuts after rejected path = %#v, want none", shortcuts)
	}
}

func TestTopologyRereadsExternalOptionsAndCollectionMarker(t *testing.T) {
	topology, _, _ := openTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{
		Enabled:                      false,
		EnableEmbeddedTitles:         true,
		PreferredMetadataLanguage:    "en-US",
		MetadataCountryCode:          "US",
		DisabledSubtitleFetchers:     []string{"OldSubtitleProvider"},
		SubtitleFetcherOrder:         []string{"OldSubtitleProvider"},
		DisabledLocalMetadataReaders: []string{"OldMetadataReader"},
	})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	wantOptions := library.LibraryOptions{
		Enabled:                      true,
		EnablePhotos:                 false,
		EnableRealtimeMonitor:        false,
		EnableEmbeddedTitles:         true,
		PreferredMetadataLanguage:    "ja-JP",
		MetadataCountryCode:          "JP",
		DisabledSubtitleFetchers:     []string{"RemoteSubtitles"},
		SubtitleFetcherOrder:         []string{"LocalSubtitles", "RemoteSubtitles"},
		DisabledLocalMetadataReaders: []string{"NfoMetadata"},
		TypeOptions: []library.TypeOptions{{
			Type:                 "Series",
			MetadataFetchers:     []string{"LocalMetadata", "TheTvDb"},
			MetadataFetcherOrder: []string{"TheTvDb", "LocalMetadata"},
		}},
	}
	optionsXML, err := xml.Marshal(wantOptions)
	if err != nil {
		t.Fatalf("marshal externally edited options: %v", err)
	}
	if err := os.WriteFile(filepath.Join(created.Path, "options.xml"), optionsXML, 0o600); err != nil {
		t.Fatalf("write externally edited options: %v", err)
	}
	if err := os.Remove(filepath.Join(created.Path, "movies.collection")); err != nil {
		t.Fatalf("remove original collection marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(created.Path, "tvshows.collection"), nil, 0o600); err != nil {
		t.Fatalf("write externally edited collection marker: %v", err)
	}

	folders, err := topology.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list externally edited folder: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("externally edited virtual-folder count = %d, want 1", len(folders))
	}
	if folders[0].CollectionType != "tvshows" || !reflect.DeepEqual(folders[0].Options, wantOptions) {
		t.Fatalf("externally edited virtual folder = %#v, want type tvshows and options %#v", folders[0], wantOptions)
	}
}

func TestTopologyDoesNotReadThroughReplacedFolderSymlink(t *testing.T) {
	topology, _, _ := openTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	outside := t.TempDir()
	if err := os.RemoveAll(created.Path); err != nil {
		t.Fatalf("remove registered folder before replacement: %v", err)
	}
	if err := os.Symlink(outside, created.Path); err != nil {
		t.Fatalf("replace registered folder with symlink: %v", err)
	}
	if _, err := topology.VirtualFolders(t.Context()); err == nil {
		t.Fatal("list symlink-replaced folder succeeded, want root-escape rejection")
	}
}

func TestTopologyDoesNotWriteThroughReplacedFolderSymlink(t *testing.T) {
	topology, _, _ := openTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	outside := t.TempDir()
	if err := os.RemoveAll(created.Path); err != nil {
		t.Fatalf("remove registered folder before replacement: %v", err)
	}
	if err := os.Symlink(outside, created.Path); err != nil {
		t.Fatalf("replace registered folder with symlink: %v", err)
	}
	if err := topology.AddMediaPath(t.Context(), created.ID, existingMediaPath(t, "outside-target")); err == nil {
		t.Fatal("add media path through symlink-replaced folder succeeded, want root-escape rejection")
	}
	shortcuts, err := filepath.Glob(filepath.Join(outside, "*.mblink"))
	if err != nil {
		t.Fatalf("glob outside shortcuts: %v", err)
	}
	if len(shortcuts) != 0 {
		t.Fatalf("shortcuts written outside topology root = %#v", shortcuts)
	}
}

func TestTopologyCanonicalizesRelativeDataRootBeforeRegistryWrite(t *testing.T) {
	_, registry, dataRoot := openTopology(t)
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dataRoot); err != nil {
		t.Fatalf("change to relative data root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	relativeTopology, err := library.NewTopology(".", registry)
	if err != nil {
		t.Fatalf("open relative topology root: %v", err)
	}
	t.Cleanup(func() {
		if err := relativeTopology.Close(); err != nil {
			t.Errorf("close relative topology: %v", err)
		}
	})
	if _, err := relativeTopology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{Enabled: false}); err != nil {
		t.Fatalf("create from relative root: %v", err)
	}
	absoluteRoot, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve absolute data root: %v", err)
	}
	absTopology, err := library.NewTopology(absoluteRoot, registry)
	if err != nil {
		t.Fatalf("open absolute topology root: %v", err)
	}
	t.Cleanup(func() {
		if err := absTopology.Close(); err != nil {
			t.Errorf("close absolute topology: %v", err)
		}
	})
	folders, err := absTopology.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list relative-root folder through absolute topology: %v", err)
	}
	if len(folders) != 1 || folders[0].Name != "Movies" {
		t.Fatalf("folders after absolute reopen = %#v, want Movies", folders)
	}
}

func TestTopologySkipsBrokenShortcutDuringFilesystemReconciliation(t *testing.T) {
	topology, _, _ := openTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	mediaPath := existingMediaPath(t, "movies")
	if err := os.WriteFile(filepath.Join(created.Path, "valid.mblink"), []byte(mediaPath), 0o600); err != nil {
		t.Fatalf("write valid shortcut: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-target"), filepath.Join(created.Path, "broken.mblink")); err != nil {
		t.Fatalf("write broken shortcut: %v", err)
	}
	folders, err := topology.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list folder with broken shortcut: %v", err)
	}
	if len(folders) != 1 || !reflect.DeepEqual(folders[0].MediaPaths, []string{mediaPath}) {
		t.Fatalf("media paths with broken shortcut = %#v, want %#v", folders, []string{mediaPath})
	}
}

func TestTopologyRecognizesCaseInsensitiveShortcutExtension(t *testing.T) {
	topology, _, _ := openTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	mediaPath := existingMediaPath(t, "movies")
	if err := os.WriteFile(filepath.Join(created.Path, "external.MBLINK"), []byte(mediaPath), 0o600); err != nil {
		t.Fatalf("write uppercase shortcut: %v", err)
	}
	folders, err := topology.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list folder with uppercase shortcut: %v", err)
	}
	if len(folders) != 1 || !reflect.DeepEqual(folders[0].MediaPaths, []string{mediaPath}) {
		t.Fatalf("media paths with uppercase shortcut = %#v, want %#v", folders, []string{mediaPath})
	}
}

func TestTopologyNormalizesFolderNamesAndRejectsWhitespaceOnlyNames(t *testing.T) {
	topology, _, dataRoot := openTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "  Cinema  ", "movies", library.LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create whitespace-padded folder: %v", err)
	}
	if created.Name != "Cinema" || created.Path != filepath.Join(dataRoot, "root", "default", "Cinema") {
		t.Fatalf("normalized virtual folder = %#v, want name Cinema below canonical root", created)
	}
	if _, err := topology.AddVirtualFolder(t.Context(), " \t ", "movies", library.LibraryOptions{Enabled: false}); err == nil {
		t.Fatal("create whitespace-only virtual folder succeeded")
	}
}

func TestTopologySynchronizesPathInfosWithShortcutsWithoutDroppingPayload(t *testing.T) {
	topology, registry, _ := openTopology(t)
	preservedPath := existingMediaPath(t, "preserved")
	addedPath := existingMediaPath(t, "added")
	preservedInfo := library.MediaPathInfo{Path: preservedPath, NetworkPath: "smb://nas/preserved"}
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{
		Enabled:   false,
		PathInfos: []library.MediaPathInfo{preservedInfo},
	})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(created.Path, "preserved.mblink"), []byte(preservedPath), 0o600); err != nil {
		t.Fatalf("write preserved shortcut: %v", err)
	}
	if err := topology.AddMediaPath(t.Context(), created.ID, addedPath); err != nil {
		t.Fatalf("add second media path: %v", err)
	}
	assertOptionsPathInfos(t, created.Path, registry, created.ID, []library.MediaPathInfo{
		preservedInfo,
		{Path: addedPath},
	})
	if err := topology.RemoveMediaPath(t.Context(), created.ID, addedPath); err != nil {
		t.Fatalf("remove second media path: %v", err)
	}
	assertOptionsPathInfos(t, created.Path, registry, created.ID, []library.MediaPathInfo{preservedInfo})
}

func assertOptionsPathInfos(t *testing.T, path string, registry *store.LibraryStore, id dto.UUID, want []library.MediaPathInfo) {
	t.Helper()
	optionsXML, err := os.ReadFile(filepath.Join(path, "options.xml"))
	if err != nil {
		t.Fatalf("read options XML: %v", err)
	}
	var got library.LibraryOptions
	if err := xml.Unmarshal(optionsXML, &got); err != nil {
		t.Fatalf("unmarshal options XML: %v", err)
	}
	if !reflect.DeepEqual(got.PathInfos, want) {
		t.Fatalf("options PathInfos = %#v, want %#v", got.PathInfos, want)
	}
	registered, err := registry.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("get registry row: %v", err)
	}
	if registered.Options != string(optionsXML) {
		t.Fatalf("registry options = %q, want current exact options.xml %q", registered.Options, optionsXML)
	}
}

func TestTopologyDiscoversPortableFolderNotYetInRegistry(t *testing.T) {
	topology, registry, dataRoot := openTopology(t)
	path := filepath.Join(dataRoot, "root", "default", "ImportedMusic")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("create unregistered folder: %v", err)
	}
	options := library.LibraryOptions{Enabled: false, PreferredMetadataLanguage: "fi-FI", MetadataCountryCode: "FI"}
	optionsXML, err := xml.Marshal(options)
	if err != nil {
		t.Fatalf("marshal imported options: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "music.collection"), nil, 0o600); err != nil {
		t.Fatalf("write imported collection marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "options.xml"), optionsXML, 0o600); err != nil {
		t.Fatalf("write imported options: %v", err)
	}

	folders, err := topology.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("discover unregistered portable folder: %v", err)
	}
	if len(folders) != 1 || folders[0].Name != "ImportedMusic" || folders[0].CollectionType != "music" || !reflect.DeepEqual(folders[0].Options, options) {
		t.Fatalf("discovered folders = %#v, want ImportedMusic music with %#v", folders, options)
	}
	registered, err := registry.List(t.Context())
	if err != nil {
		t.Fatalf("list reconciled registry: %v", err)
	}
	if len(registered) != 1 || registered[0].Name != "ImportedMusic" || registered[0].Path != path || registered[0].CollectionType != "music" || registered[0].Options != string(optionsXML) {
		t.Fatalf("reconciled registry = %#v, want imported portable folder", registered)
	}
}

func TestTopologyPrunesExternallyDeletedRegisteredFolderWithoutHidingOthers(t *testing.T) {
	topology, registry, _ := openTopology(t)
	kept, err := topology.AddVirtualFolder(t.Context(), "Kept", "music", library.LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create kept folder: %v", err)
	}
	removed, err := topology.AddVirtualFolder(t.Context(), "Removed", "movies", library.LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create removed folder: %v", err)
	}
	if err := os.RemoveAll(removed.Path); err != nil {
		t.Fatalf("externally delete registered folder: %v", err)
	}

	folders, err := topology.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list after external folder deletion: %v", err)
	}
	if len(folders) != 1 || folders[0].ID != kept.ID || folders[0].Name != "Kept" {
		t.Fatalf("folders after external deletion = %#v, want only kept folder", folders)
	}
	registered, err := registry.List(t.Context())
	if err != nil {
		t.Fatalf("list registry after external deletion: %v", err)
	}
	if len(registered) != 1 || registered[0].ID != kept.ID {
		t.Fatalf("registry after external deletion = %#v, want only kept row", registered)
	}
}

func TestTopologyRejectsAncestorSymlinkEscapeBeforeWritingShortcut(t *testing.T) {
	topology, _, dataRoot := openTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	outsideDefault := filepath.Join(t.TempDir(), "default")
	insideDefault := filepath.Join(dataRoot, "root", "default")
	if err := os.Rename(insideDefault, outsideDefault); err != nil {
		t.Fatalf("move topology ancestor outside root: %v", err)
	}
	if err := os.Symlink(outsideDefault, insideDefault); err != nil {
		t.Fatalf("replace topology ancestor with symlink: %v", err)
	}
	if err := topology.AddMediaPath(t.Context(), created.ID, existingMediaPath(t, "movies")); err == nil {
		t.Fatal("add media path through ancestor symlink succeeded, want root-escape rejection")
	}
	shortcuts, err := filepath.Glob(filepath.Join(outsideDefault, "Movies", "*.mblink"))
	if err != nil {
		t.Fatalf("glob outside shortcuts: %v", err)
	}
	if len(shortcuts) != 0 {
		t.Fatalf("ancestor symlink wrote shortcuts outside topology root = %#v", shortcuts)
	}
}

func TestTopologyUsesFullDefaultOptionsForMissingOrMalformedFiles(t *testing.T) {
	const malformedOptionsXML = "<LibraryOptions><Enabled>not-a-bool</Enabled>"
	for _, mutation := range []struct {
		name  string
		apply func(*testing.T, string) string
	}{
		{
			name: "missing options XML",
			apply: func(t *testing.T, path string) string {
				t.Helper()
				if err := os.Remove(filepath.Join(path, "options.xml")); err != nil {
					t.Fatalf("remove options XML: %v", err)
				}
				return ""
			},
		},
		{
			name: "malformed options XML",
			apply: func(t *testing.T, path string) string {
				t.Helper()
				if err := os.WriteFile(filepath.Join(path, "options.xml"), []byte(malformedOptionsXML), 0o600); err != nil {
					t.Fatalf("write malformed options XML: %v", err)
				}
				return malformedOptionsXML
			},
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			topology, registry, _ := openTopology(t)
			fallback, err := topology.AddVirtualFolder(t.Context(), "Fallback", "movies", library.LibraryOptions{Enabled: false})
			if err != nil {
				t.Fatalf("create fallback folder: %v", err)
			}
			healthyOptions := library.LibraryOptions{Enabled: false, PreferredMetadataLanguage: "ko-KR", MetadataCountryCode: "KR"}
			healthy, err := topology.AddVirtualFolder(t.Context(), "Healthy", "music", healthyOptions)
			if err != nil {
				t.Fatalf("create healthy folder: %v", err)
			}
			expectedOptionsXML := mutation.apply(t, fallback.Path)

			folders, err := topology.VirtualFolders(t.Context())
			if err != nil {
				t.Fatalf("list folders with %s: %v", mutation.name, err)
			}
			byName := make(map[string]library.VirtualFolder, len(folders))
			for _, folder := range folders {
				byName[folder.Name] = folder
			}
			if got, ok := byName["Fallback"]; !ok || !reflect.DeepEqual(got.Options, fullDefaultLibraryOptions()) {
				t.Fatalf("fallback options = %#v, want full upstream default %#v", got.Options, fullDefaultLibraryOptions())
			}
			if got, ok := byName["Healthy"]; !ok || got.ID != healthy.ID || !reflect.DeepEqual(got.Options, healthyOptions) {
				t.Fatalf("healthy folder after fallback %s = %#v, want original options %#v", mutation.name, got, healthyOptions)
			}
			optionsPath := filepath.Join(fallback.Path, "options.xml")
			if expectedOptionsXML == "" {
				if _, err := os.Stat(optionsPath); !os.IsNotExist(err) {
					t.Fatalf("missing options XML stat error = %v, want not exist", err)
				}
			} else {
				actualOptionsXML, err := os.ReadFile(optionsPath)
				if err != nil {
					t.Fatalf("read malformed options XML after listing: %v", err)
				}
				if string(actualOptionsXML) != expectedOptionsXML {
					t.Fatalf("malformed options XML after listing = %q, want unchanged %q", actualOptionsXML, expectedOptionsXML)
				}
			}
			registered, err := registry.Get(t.Context(), fallback.ID)
			if err != nil {
				t.Fatalf("get fallback registry row: %v", err)
			}
			if registered.Options != expectedOptionsXML {
				t.Fatalf("fallback registry options = %q, want exact fallback mirror %q", registered.Options, expectedOptionsXML)
			}
		})
	}
}

func TestTopologyIgnoresInvalidCollectionMarkerBesideValidMarker(t *testing.T) {
	topology, _, _ := openTopology(t)
	created, err := topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create virtual folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(created.Path, "aaa.collection"), nil, 0o600); err != nil {
		t.Fatalf("write invalid marker: %v", err)
	}
	folders, err := topology.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list folder with invalid marker: %v", err)
	}
	if len(folders) != 1 || folders[0].CollectionType != "movies" {
		t.Fatalf("collection type with invalid marker = %#v, want movies", folders)
	}
}

func TestTopologyCanonicalizesAndValidatesCollectionTypes(t *testing.T) {
	for _, collectionType := range []string{"movies", "tvshows", "music", "musicvideos", "homevideos", "boxsets", "books", "mixed", "photos"} {
		t.Run(collectionType, func(t *testing.T) {
			topology, _, _ := openTopology(t)
			created, err := topology.AddVirtualFolder(t.Context(), "Library", strings.ToUpper(collectionType), library.LibraryOptions{Enabled: false})
			if err != nil {
				t.Fatalf("create %s library: %v", collectionType, err)
			}
			if created.CollectionType != collectionType {
				t.Fatalf("created collection type = %q, want canonical %q", created.CollectionType, collectionType)
			}
			if _, err := os.Stat(filepath.Join(created.Path, collectionType+".collection")); err != nil {
				t.Fatalf("canonical collection marker: %v", err)
			}
		})
	}
	t.Run("rejects unrecognized type", func(t *testing.T) {
		topology, _, _ := openTopology(t)
		if _, err := topology.AddVirtualFolder(t.Context(), "Unsupported", "not-a-library-type", library.LibraryOptions{Enabled: false}); err == nil {
			t.Fatal("create unrecognized library type succeeded")
		}
	})
}

func fullDefaultLibraryOptions() library.LibraryOptions {
	return library.LibraryOptions{
		Enabled:                          true,
		EnablePhotos:                     true,
		EnableRealtimeMonitor:            false,
		PathInfos:                        []library.MediaPathInfo{},
		EnableAutomaticSeriesGrouping:    true,
		SeasonZeroDisplayName:            "Specials",
		MetadataSavers:                   []string{},
		DisabledLocalMetadataReaders:     []string{},
		LocalMetadataReaderOrder:         []string{},
		DisabledSubtitleFetchers:         []string{},
		SubtitleFetcherOrder:             []string{},
		DisabledMediaSegmentProviders:    []string{},
		MediaSegmentProviderOrder:        []string{},
		SkipSubtitlesIfAudioTrackMatches: true,
		SubtitleDownloadLanguages:        []string{},
		RequirePerfectSubtitleMatch:      true,
		SaveSubtitlesWithMedia:           true,
		DisabledLyricFetchers:            []string{},
		LyricFetcherOrder:                []string{},
		CustomTagDelimiters:              []string{"/", "|", ";", "\\"},
		DelimiterWhitelist:               []string{},
		AllowEmbeddedSubtitles:           library.AllowEmbeddedSubtitlesAll,
		TypeOptions:                      []library.TypeOptions{},
	}
}

func TestTopologyCloseRejectsEveryPublicMethodWithoutTouchingPaths(t *testing.T) {
	topology, _, _ := openTopology(t)
	mediaPath := existingMediaPath(t, "closed-media")
	if err := topology.Close(); err != nil {
		t.Fatalf("close topology: %v", err)
	}
	if err := topology.Close(); err != nil {
		t.Fatalf("repeat close topology: %v", err)
	}

	for _, call := range []struct {
		name   string
		invoke func() error
	}{
		{
			name: "AddVirtualFolder",
			invoke: func() error {
				_, err := topology.AddVirtualFolder(t.Context(), "Closed", "movies", library.LibraryOptions{Enabled: false})
				return err
			},
		},
		{
			name: "RemoveVirtualFolder",
			invoke: func() error {
				return topology.RemoveVirtualFolder(t.Context(), dto.UUID{})
			},
		},
		{
			name: "AddMediaPath",
			invoke: func() error {
				return topology.AddMediaPath(t.Context(), dto.UUID{}, mediaPath)
			},
		},
		{
			name: "RemoveMediaPath",
			invoke: func() error {
				return topology.RemoveMediaPath(t.Context(), dto.UUID{}, mediaPath)
			},
		},
		{
			name: "UpdateLibraryOptions",
			invoke: func() error {
				return topology.UpdateLibraryOptions(t.Context(), dto.UUID{}, library.LibraryOptions{})
			},
		},
		{
			name: "VirtualFolders",
			invoke: func() error {
				_, err := topology.VirtualFolders(t.Context())
				return err
			},
		},
	} {
		t.Run(call.name, func(t *testing.T) {
			if err := call.invoke(); err != library.ErrTopologyClosed {
				t.Fatalf("%s after close error = %v, want stable ErrTopologyClosed", call.name, err)
			}
		})
	}
}

func TestTopologyKeepsMalformedOptionsBytesWhileReconcilingShortcutInMemory(t *testing.T) {
	topology, registry, _ := openTopology(t)
	folder, err := topology.AddVirtualFolder(t.Context(), "Fallback", "movies", library.LibraryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("create fallback folder: %v", err)
	}
	const malformedOptionsXML = "<LibraryOptions><Enabled>not-a-bool</Enabled>"
	if err := os.WriteFile(filepath.Join(folder.Path, "options.xml"), []byte(malformedOptionsXML), 0o600); err != nil {
		t.Fatalf("write malformed options XML: %v", err)
	}
	mediaPath := existingMediaPath(t, "existing")
	if err := os.WriteFile(filepath.Join(folder.Path, "existing.mblink"), []byte(mediaPath), 0o600); err != nil {
		t.Fatalf("write existing shortcut: %v", err)
	}

	folders, err := topology.VirtualFolders(t.Context())
	if err != nil {
		t.Fatalf("list malformed folder with shortcut: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("malformed folder count = %d, want 1", len(folders))
	}
	wantOptions := fullDefaultLibraryOptions()
	wantOptions.PathInfos = []library.MediaPathInfo{{Path: mediaPath}}
	if !reflect.DeepEqual(folders[0].Options, wantOptions) || !reflect.DeepEqual(folders[0].MediaPaths, []string{mediaPath}) {
		t.Fatalf("malformed folder = %#v, want defaults with PathInfos %#v and media path %q", folders[0], wantOptions, mediaPath)
	}
	optionsXML, err := os.ReadFile(filepath.Join(folder.Path, "options.xml"))
	if err != nil {
		t.Fatalf("read malformed options XML after listing: %v", err)
	}
	if string(optionsXML) != malformedOptionsXML {
		t.Fatalf("malformed options XML after shortcut reconciliation = %q, want unchanged %q", optionsXML, malformedOptionsXML)
	}
	registered, err := registry.Get(t.Context(), folder.ID)
	if err != nil {
		t.Fatalf("get malformed fallback registry row: %v", err)
	}
	if registered.Options != malformedOptionsXML {
		t.Fatalf("malformed fallback registry options = %q, want exact malformed XML %q", registered.Options, malformedOptionsXML)
	}
}

func openTopology(t *testing.T) (*library.Topology, *store.LibraryStore, string) {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite stores: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close SQLite stores: %v", err)
		}
	})
	registry := store.NewLibraryStore(stores)
	dataRoot := t.TempDir()
	topology, err := library.NewTopology(dataRoot, registry)
	if err != nil {
		t.Fatalf("open topology: %v", err)
	}
	t.Cleanup(func() {
		if err := topology.Close(); err != nil {
			t.Errorf("close topology: %v", err)
		}
	})
	return topology, registry, dataRoot
}

func existingMediaPath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("create media path %q: %v", path, err)
	}
	return path
}

func sortedPaths(paths []string) []string {
	result := append([]string(nil), paths...)
	sort.Strings(result)
	return result
}
