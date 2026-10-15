// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/jsonnum"
	"github.com/deadfin-org/deadfin/internal/store"
)

// EmbeddedSubtitleOptions controls the allowed embedded subtitle kinds.
type EmbeddedSubtitleOptions string

const (
	AllowEmbeddedSubtitlesAll   EmbeddedSubtitleOptions = "AllowAll"
	AllowEmbeddedSubtitlesText  EmbeddedSubtitleOptions = "AllowText"
	AllowEmbeddedSubtitlesImage EmbeddedSubtitleOptions = "AllowImage"
	AllowEmbeddedSubtitlesNone  EmbeddedSubtitleOptions = "AllowNone"
)

// ErrTopologyClosed reports use of a topology after its root descriptor closes.
var ErrTopologyClosed = errors.New("library topology is closed")

// MediaPathInfo retains the portable per-location configuration. NetworkPath
// exists in deployed options.xml files even though current Jellyfin only needs
// Path for shortcut creation.
type MediaPathInfo struct {
	Path        string `xml:"Path"`
	NetworkPath string `xml:"NetworkPath"`
}

// ImageOption configures image acquisition for one item image type.
type ImageOption struct {
	Type     string `xml:"Type"`
	Limit    int    `xml:"Limit"`
	MinWidth int    `xml:"MinWidth"`
}

// TypeOptions configures metadata and image providers for one media type.
type TypeOptions struct {
	Type                     string        `xml:"Type"`
	MetadataFetchers         []string      `xml:"MetadataFetchers>string"`
	MetadataFetcherOrder     []string      `xml:"MetadataFetcherOrder>string"`
	ImageFetchers            []string      `xml:"ImageFetchers>string"`
	ImageFetcherOrder        []string      `xml:"ImageFetcherOrder>string"`
	ImageOptions             []ImageOption `xml:"ImageOptions>ImageOption"`
	SimilarItemProviders     []string      `xml:"SimilarItemProviders>string"`
	SimilarItemProviderOrder []string      `xml:"SimilarItemProviderOrder>string"`
}

// LibraryOptions is the portable XML configuration stored beside a virtual
// folder. Its shape mirrors the full upstream LibraryOptions XML contract.
type LibraryOptions struct {
	Enabled                                 bool                    `xml:"Enabled"`
	EnablePhotos                            bool                    `xml:"EnablePhotos"`
	EnableRealtimeMonitor                   bool                    `xml:"EnableRealtimeMonitor"`
	EnableLUFSScan                          bool                    `xml:"EnableLUFSScan"`
	EnableChapterImageExtraction            bool                    `xml:"EnableChapterImageExtraction"`
	ExtractChapterImagesDuringLibraryScan   bool                    `xml:"ExtractChapterImagesDuringLibraryScan"`
	EnableTrickplayImageExtraction          bool                    `xml:"EnableTrickplayImageExtraction"`
	ExtractTrickplayImagesDuringLibraryScan bool                    `xml:"ExtractTrickplayImagesDuringLibraryScan"`
	PathInfos                               []MediaPathInfo         `xml:"PathInfos>MediaPathInfo"`
	SaveLocalMetadata                       bool                    `xml:"SaveLocalMetadata"`
	EnableInternetProviders                 bool                    `xml:"EnableInternetProviders"`
	EnableAutomaticSeriesGrouping           bool                    `xml:"EnableAutomaticSeriesGrouping"`
	EnableEmbeddedTitles                    bool                    `xml:"EnableEmbeddedTitles"`
	EnableEmbeddedExtrasTitles              bool                    `xml:"EnableEmbeddedExtrasTitles"`
	EnableEmbeddedEpisodeInfos              bool                    `xml:"EnableEmbeddedEpisodeInfos"`
	AutomaticRefreshIntervalDays            int                     `xml:"AutomaticRefreshIntervalDays"`
	PreferredMetadataLanguage               string                  `xml:"PreferredMetadataLanguage"`
	MetadataCountryCode                     string                  `xml:"MetadataCountryCode"`
	SeasonZeroDisplayName                   string                  `xml:"SeasonZeroDisplayName"`
	MetadataSavers                          []string                `xml:"MetadataSavers>string"`
	DisabledLocalMetadataReaders            []string                `xml:"DisabledLocalMetadataReaders>string"`
	LocalMetadataReaderOrder                []string                `xml:"LocalMetadataReaderOrder>string"`
	DisabledSubtitleFetchers                []string                `xml:"DisabledSubtitleFetchers>string"`
	SubtitleFetcherOrder                    []string                `xml:"SubtitleFetcherOrder>string"`
	DisabledMediaSegmentProviders           []string                `xml:"DisabledMediaSegmentProviders>string"`
	MediaSegmentProviderOrder               []string                `xml:"MediaSegmentProviderOrder>string"`
	SkipSubtitlesIfEmbeddedSubtitlesPresent bool                    `xml:"SkipSubtitlesIfEmbeddedSubtitlesPresent"`
	SkipSubtitlesIfAudioTrackMatches        bool                    `xml:"SkipSubtitlesIfAudioTrackMatches"`
	SubtitleDownloadLanguages               []string                `xml:"SubtitleDownloadLanguages>string"`
	RequirePerfectSubtitleMatch             bool                    `xml:"RequirePerfectSubtitleMatch"`
	SaveSubtitlesWithMedia                  bool                    `xml:"SaveSubtitlesWithMedia"`
	SaveLyricsWithMedia                     bool                    `xml:"SaveLyricsWithMedia"`
	SaveTrickplayWithMedia                  bool                    `xml:"SaveTrickplayWithMedia"`
	DisabledLyricFetchers                   []string                `xml:"DisabledLyricFetchers>string"`
	LyricFetcherOrder                       []string                `xml:"LyricFetcherOrder>string"`
	PreferNonstandardArtistsTag             bool                    `xml:"PreferNonstandardArtistsTag"`
	UseCustomTagDelimiters                  bool                    `xml:"UseCustomTagDelimiters"`
	CustomTagDelimiters                     []string                `xml:"CustomTagDelimiters>string"`
	DelimiterWhitelist                      []string                `xml:"DelimiterWhitelist>string"`
	AutomaticallyAddToCollection            bool                    `xml:"AutomaticallyAddToCollection"`
	AllowEmbeddedSubtitles                  EmbeddedSubtitleOptions `xml:"AllowEmbeddedSubtitles"`
	TypeOptions                             []TypeOptions           `xml:"TypeOptions>TypeOptions"`
}

// UnmarshalJSON starts from DefaultLibraryOptions and then sets only the
// members the document supplies. System.Text.Json runs the upstream
// constructor before it sets the present members, so a client that omits a
// member gets the constructor default, not the Go zero value.
// Member-name matching stays case-insensitive.
func (o *LibraryOptions) UnmarshalJSON(data []byte) error {
	type plain LibraryOptions
	decoded := plain(DefaultLibraryOptions())
	if err := jsonnum.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*o = LibraryOptions(decoded)
	return nil
}

// VirtualFolder joins a registry record to its portable on-disk configuration
// and the media targets represented by its .mblink files. CollectionFolderID is
// the deterministic scanned item identity Jellyfin derives from the folder path
// (LibraryManager.GetNewItemId); it is independent of the registry ID and lets
// management responses and option updates address the scanned CollectionFolder.
type VirtualFolder struct {
	ID                 dto.UUID
	CollectionFolderID dto.UUID
	Name               string
	Path               string
	CollectionType     string
	Options            LibraryOptions
	MediaPaths         []string
}

// Topology owns the portable root/default library folder layout and its typed
// database registry. Its mutation boundary makes a shortcut check-and-write
// operation atomic for callers sharing one topology.
type Topology struct {
	anchor   string
	root     string
	fsroot   *os.Root
	registry *store.LibraryStore
	mu       sync.Mutex
}

var (
	topologySyncDir                      = syncDir
	topologyRootSyncDir                  = func(t *Topology, name string) error { return t.rootSyncDir(name) }
	topologyRootRemoveAll                = func(t *Topology, name string) error { return t.rootRemoveAll(name) }
	topologyBeforeShortcutCreate         = func() {}
	topologyBeforeShortcutWrite          = func() {}
	topologyAfterFolderValidation        = func() {}
	topologyAfterVirtualFolderValidation = func() {}
)

// NewTopology opens (or creates) the portable root/default layout below
// dataRoot and attaches it to registry.
func NewTopology(dataRoot string, registry *store.LibraryStore) (*Topology, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return nil, errors.New("library data root is required")
	}
	if registry == nil {
		return nil, errors.New("library registry is required")
	}
	absoluteRoot, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve library data root: %w", err)
	}
	anchor := filepath.Clean(absoluteRoot)
	root := filepath.Join(anchor, "root", "default")
	if err := ensureDirDurable(root); err != nil {
		return nil, fmt.Errorf("prepare library root: %w", err)
	}
	fsroot, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open library root: %w", err)
	}
	return &Topology{anchor: anchor, root: root, fsroot: fsroot, registry: registry}, nil
}

// Close releases the descriptor anchoring this topology's portable root.
func (t *Topology) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fsroot == nil {
		return nil
	}
	err := t.fsroot.Close()
	t.fsroot = nil
	return err
}

// AddVirtualFolder creates a portable virtual-folder directory and records it
// in the registry. A registry failure removes the directory it created.
func (t *Topology) AddVirtualFolder(ctx context.Context, name, collectionType string, options LibraryOptions) (folder VirtualFolder, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureOpen(); err != nil {
		return VirtualFolder{}, err
	}
	name = strings.TrimSpace(name)
	collectionType = strings.TrimSpace(collectionType)
	if err := validatePathComponent("library name", name); err != nil {
		return VirtualFolder{}, err
	}
	// A null/omitted collectionType is Jellyfin's nullable enum contract: no
	// .collection marker is written, matching stock's CollectionTypeOptions?.
	// An explicit value must canonicalize to a known type.
	if collectionType != "" {
		canonicalType, ok := canonicalCollectionType(collectionType)
		if !ok {
			return VirtualFolder{}, fmt.Errorf("unsupported collection type %q", collectionType)
		}
		collectionType = canonicalType
	}
	for _, pathInfo := range options.PathInfos {
		info, err := os.Stat(pathInfo.Path)
		if err != nil || !info.IsDir() {
			if err != nil {
				return VirtualFolder{}, fmt.Errorf("stat media path: %w", err)
			}
			return VirtualFolder{}, fmt.Errorf("media path is not a directory: %q", pathInfo.Path)
		}
	}

	if err := t.verifyRoot(); err != nil {
		return VirtualFolder{}, err
	}
	topologyAfterVirtualFolderValidation()
	if err := t.verifyRoot(); err != nil {
		return VirtualFolder{}, err
	}
	path := filepath.Join(t.root, name)
	if _, err := t.fsroot.Lstat(name); err == nil {
		return VirtualFolder{}, fmt.Errorf("path already exists: %q", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return VirtualFolder{}, fmt.Errorf("stat path %q: %w", path, err)
	}
	if err := t.fsroot.Mkdir(name, 0o755); err != nil {
		return VirtualFolder{}, fmt.Errorf("create virtual-folder directory: %w", err)
	}
	createdDirectory := true
	defer func() {
		if err == nil || !createdDirectory {
			return
		}
		err = errors.Join(err, t.rootRemoveAll(name))
	}()
	if err := t.rootSyncDir("."); err != nil {
		return VirtualFolder{}, fmt.Errorf("sync virtual-folder parent: %w", err)
	}

	optionsXML, err := xml.Marshal(options)
	if err != nil {
		return VirtualFolder{}, fmt.Errorf("marshal library options: %w", err)
	}
	if collectionType != "" {
		if err := t.rootWriteFile(filepath.Join(path, collectionType+".collection"), nil, 0o600); err != nil {
			return VirtualFolder{}, fmt.Errorf("write collection marker: %w", err)
		}
	}
	if err := t.rootWriteFile(filepath.Join(path, "options.xml"), optionsXML, 0o600); err != nil {
		return VirtualFolder{}, fmt.Errorf("write library options: %w", err)
	}
	for _, pathInfo := range options.PathInfos {
		shortcut, err := t.unusedShortcutPath(path)
		if err != nil {
			return VirtualFolder{}, err
		}
		if err := t.rootWriteFile(shortcut, []byte(pathInfo.Path), 0o600); err != nil {
			return VirtualFolder{}, fmt.Errorf("write media shortcut: %w", err)
		}
	}

	id, err := newUUID()
	if err != nil {
		return VirtualFolder{}, err
	}
	library := store.Library{ID: id, Name: name, Path: path, CollectionType: collectionType, Options: string(optionsXML)}
	if _, err := t.registry.Create(ctx, library); err != nil {
		return VirtualFolder{}, fmt.Errorf("register virtual folder: %w", err)
	}
	createdDirectory = false
	return VirtualFolder{ID: id, CollectionFolderID: collectionFolderItemID(path), Name: name, Path: path, CollectionType: collectionType, Options: options, MediaPaths: mediaPathsFromInfos(options.PathInfos)}, nil
}

// RemoveVirtualFolder unregisters a folder and removes its portable directory.
// If durable filesystem removal fails after unregistering the folder, it
// restores the exact registry record before returning the failure.
func (t *Topology) RemoveVirtualFolder(ctx context.Context, id dto.UUID) (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureOpen(); err != nil {
		return err
	}
	library, err := t.registry.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("get virtual folder: %w", err)
	}
	path, err := t.libraryPath(library.Path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat virtual-folder directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("virtual-folder path is not a directory: %q", path)
	}
	topologyAfterVirtualFolderValidation()
	if _, err := t.libraryPath(library.Path); err != nil {
		return err
	}

	folderName, err := t.rootName(path)
	if err != nil {
		return err
	}
	stagedName := ".removing-" + id.String()
	if _, err := t.fsroot.Lstat(stagedName); err == nil {
		return fmt.Errorf("path already exists: %q", filepath.Join(t.root, stagedName))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat staged virtual-folder directory: %w", err)
	}
	if err := t.fsroot.Rename(folderName, stagedName); err != nil {
		return fmt.Errorf("stage virtual-folder directory: %w", err)
	}
	stagedDirectory := true
	registryDeleted := false
	defer func() {
		if err == nil || !stagedDirectory {
			return
		}
		if restoreErr := t.fsroot.Rename(stagedName, folderName); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore staged virtual-folder directory: %w", restoreErr))
		} else {
			err = errors.Join(err, topologyRootSyncDir(t, "."))
		}
		if !registryDeleted {
			return
		}
		if _, restoreErr := t.registry.Create(context.WithoutCancel(ctx), library); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore virtual-folder registry: %w", restoreErr))
		}
	}()
	if err := topologyRootSyncDir(t, "."); err != nil {
		return fmt.Errorf("sync staged virtual-folder directory: %w", err)
	}
	if err := t.registry.Delete(ctx, id); err != nil {
		return fmt.Errorf("unregister virtual folder: %w", err)
	}
	registryDeleted = true
	if err := topologyRootRemoveAll(t, stagedName); err != nil {
		return fmt.Errorf("remove virtual-folder directory: %w", err)
	}
	stagedDirectory = false
	return nil
}

// AddMediaPath adds an existing media directory as a durable .mblink target.
func (t *Topology) AddMediaPath(ctx context.Context, id dto.UUID, mediaPath string) (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureOpen(); err != nil {
		return err
	}
	mediaPath = filepath.Clean(mediaPath)
	info, err := os.Stat(mediaPath)
	if err != nil {
		return fmt.Errorf("stat media path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("media path is not a directory: %q", mediaPath)
	}
	topologyBeforeShortcutCreate()
	library, err := t.registry.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("get virtual folder: %w", err)
	}
	path, err := t.libraryPath(library.Path)
	if err != nil {
		return err
	}
	if err := ensureDirectory(path); err != nil {
		return err
	}
	topologyAfterFolderValidation()
	if _, err := t.libraryPath(library.Path); err != nil {
		return err
	}
	paths, err := t.readMediaPaths(path)
	if err != nil {
		return err
	}
	for _, existing := range paths {
		if filepath.Clean(existing) == mediaPath {
			return fmt.Errorf("media path is already linked: %q", mediaPath)
		}
	}
	options, originalOptionsXML, err := t.readLibraryOptions(path)
	if err != nil {
		return err
	}
	updatedOptions := options
	updatedOptions.PathInfos = append(append([]MediaPathInfo(nil), options.PathInfos...), MediaPathInfo{Path: mediaPath})
	updatedOptionsXML, err := xml.Marshal(updatedOptions)
	if err != nil {
		return fmt.Errorf("marshal library options: %w", err)
	}
	shortcut, err := t.unusedShortcutPath(path)
	if err != nil {
		return err
	}
	topologyBeforeShortcutWrite()
	if _, err := t.libraryPath(library.Path); err != nil {
		return err
	}
	if err := ensureDirectory(path); err != nil {
		return err
	}
	if err := t.rootWriteFile(shortcut, []byte(mediaPath), 0o600); err != nil {
		return fmt.Errorf("write media shortcut: %w", err)
	}
	shortcutWritten := true
	defer func() {
		if err == nil || !shortcutWritten {
			return
		}
		err = errors.Join(err, t.rootWriteFile(filepath.Join(path, "options.xml"), originalOptionsXML, 0o600), t.rootRemoveFile(shortcut))
	}()
	if err := t.rootWriteFile(filepath.Join(path, "options.xml"), updatedOptionsXML, 0o600); err != nil {
		return fmt.Errorf("write library options: %w", err)
	}
	library.Options = string(updatedOptionsXML)
	if err := t.registry.Update(ctx, library); err != nil {
		return fmt.Errorf("update library registry: %w", err)
	}
	shortcutWritten = false
	return nil
}

// RemoveMediaPath removes the shortcut whose exact file contents equal
// mediaPath, then synchronizes the portable options and registry mirror.
func (t *Topology) RemoveMediaPath(ctx context.Context, id dto.UUID, mediaPath string) (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureOpen(); err != nil {
		return err
	}
	library, err := t.registry.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("get virtual folder: %w", err)
	}
	path, err := t.libraryPath(library.Path)
	if err != nil {
		return err
	}
	topologyAfterFolderValidation()
	if _, err := t.libraryPath(library.Path); err != nil {
		return err
	}
	entries, err := t.rootReadDir(path)
	if err != nil {
		return fmt.Errorf("read virtual-folder directory: %w", err)
	}
	var shortcut string
	var shortcutContents []byte
	for _, entry := range entries {
		if !isShortcut(entry) {
			continue
		}
		candidate := filepath.Join(path, entry.Name())
		contents, readErr := t.rootReadFile(candidate)
		if readErr != nil {
			continue
		}
		if string(contents) == mediaPath {
			shortcut, shortcutContents = candidate, contents
			break
		}
	}
	if shortcut == "" {
		return fmt.Errorf("media path is not linked: %q", mediaPath)
	}
	options, originalOptionsXML, err := t.readLibraryOptions(path)
	if err != nil {
		return err
	}
	updatedOptions := options
	updatedOptions.PathInfos = filterPathInfos(options.PathInfos, mediaPath)
	updatedOptionsXML, err := xml.Marshal(updatedOptions)
	if err != nil {
		return fmt.Errorf("marshal library options: %w", err)
	}
	if err := t.rootRemoveFile(shortcut); err != nil {
		return fmt.Errorf("remove media shortcut: %w", err)
	}
	shortcutRemoved := true
	defer func() {
		if err == nil || !shortcutRemoved {
			return
		}
		err = errors.Join(err, t.rootWriteFile(shortcut, shortcutContents, 0o600), t.rootWriteFile(filepath.Join(path, "options.xml"), originalOptionsXML, 0o600))
	}()
	if err := t.rootWriteFile(filepath.Join(path, "options.xml"), updatedOptionsXML, 0o600); err != nil {
		return fmt.Errorf("write library options: %w", err)
	}
	library.Options = string(updatedOptionsXML)
	if err := t.registry.Update(ctx, library); err != nil {
		return fmt.Errorf("update library registry: %w", err)
	}
	shortcutRemoved = false
	return nil
}

// UpdateLibraryOptions durably replaces a virtual folder's portable options
// and registry mirror. New PathInfos gain matching shortcuts; existing
// shortcuts are retained, matching Jellyfin's update semantics. The id is the
// scanned CollectionFolder item identity (the ItemId the client received from
// GetVirtualFolders), matching stock's GetItemById<CollectionFolder>(request.Id).
func (t *Topology) UpdateLibraryOptions(ctx context.Context, id dto.UUID, options LibraryOptions) (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureOpen(); err != nil {
		return err
	}
	library, err := t.libraryByCollectionFolderID(ctx, id)
	if err != nil {
		return err
	}
	path, err := t.libraryPath(library.Path)
	if err != nil {
		return err
	}
	if err := ensureDirectory(path); err != nil {
		return err
	}
	topologyAfterFolderValidation()
	if _, err := t.libraryPath(library.Path); err != nil {
		return err
	}

	optionsPath := filepath.Join(path, "options.xml")
	originalOptionsXML, readErr := t.rootReadFile(optionsPath)
	optionsExisted := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read library options: %w", readErr)
	}
	updatedOptionsXML, err := xml.Marshal(options)
	if err != nil {
		return fmt.Errorf("marshal library options: %w", err)
	}
	mediaPaths, err := t.readMediaPaths(path)
	if err != nil {
		return err
	}
	linked := make(map[string]struct{}, len(mediaPaths))
	for _, mediaPath := range mediaPaths {
		linked[filepath.Clean(mediaPath)] = struct{}{}
	}

	addedShortcuts := make([]string, 0)
	optionsWritten := false
	defer func() {
		if err == nil {
			return
		}
		var rollbackErr error
		if optionsWritten {
			if optionsExisted {
				rollbackErr = errors.Join(rollbackErr, t.rootWriteFile(optionsPath, originalOptionsXML, 0o600))
			} else {
				rollbackErr = errors.Join(rollbackErr, t.rootRemoveFile(optionsPath))
			}
		}
		for index := len(addedShortcuts) - 1; index >= 0; index-- {
			rollbackErr = errors.Join(rollbackErr, t.rootRemoveFile(addedShortcuts[index]))
		}
		err = errors.Join(err, rollbackErr)
	}()

	for _, pathInfo := range options.PathInfos {
		clean := filepath.Clean(pathInfo.Path)
		if _, exists := linked[clean]; exists {
			continue
		}
		info, statErr := os.Stat(pathInfo.Path)
		if statErr != nil {
			return fmt.Errorf("stat media path: %w", statErr)
		}
		if !info.IsDir() {
			return fmt.Errorf("media path is not a directory: %q", pathInfo.Path)
		}
		shortcut, shortcutErr := t.unusedShortcutPath(path)
		if shortcutErr != nil {
			return shortcutErr
		}
		topologyBeforeShortcutWrite()
		if _, err := t.libraryPath(library.Path); err != nil {
			return err
		}
		if err := ensureDirectory(path); err != nil {
			return err
		}
		if err := t.rootWriteFile(shortcut, []byte(pathInfo.Path), 0o600); err != nil {
			return fmt.Errorf("write media shortcut: %w", err)
		}
		addedShortcuts = append(addedShortcuts, shortcut)
		linked[clean] = struct{}{}
	}
	if _, err := t.libraryPath(library.Path); err != nil {
		return err
	}
	if err := t.rootWriteFile(optionsPath, updatedOptionsXML, 0o600); err != nil {
		return fmt.Errorf("write library options: %w", err)
	}
	optionsWritten = true
	library.Options = string(updatedOptionsXML)
	if err := t.registry.Update(ctx, library); err != nil {
		return fmt.Errorf("update library registry: %w", err)
	}
	return nil
}

// VirtualFolders returns portable filesystem truth and reconciles its registry
// mirror after administrators or another Jellyfin process edit a folder.
func (t *Topology) VirtualFolders(ctx context.Context) ([]VirtualFolder, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureOpen(); err != nil {
		return nil, err
	}
	directories, err := t.portableDirectories()
	if err != nil {
		return nil, err
	}
	libraries, err := t.registry.List(ctx)
	if err != nil {
		return nil, err
	}
	registered := make(map[string]store.Library, len(libraries))
	for _, library := range libraries {
		path, err := t.libraryPath(library.Path)
		if errors.Is(err, os.ErrNotExist) {
			if err := t.registry.Delete(ctx, library.ID); err != nil {
				return nil, fmt.Errorf("prune removed virtual folder: %w", err)
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		registered[path] = library
	}

	folders := make([]VirtualFolder, 0, len(directories))
	for _, directory := range directories {
		library, exists := registered[directory]
		if !exists {
			id, err := newUUID()
			if err != nil {
				return nil, err
			}
			library = store.Library{ID: id, Name: filepath.Base(directory), Path: directory}
		}
		folder, reconciled, err := t.reconcileFolder(directory, library)
		if err != nil {
			return nil, err
		}
		if !exists {
			if _, err := t.registry.Create(ctx, reconciled); err != nil {
				return nil, fmt.Errorf("register discovered virtual folder: %w", err)
			}
		} else if reconciled != library {
			if err := t.registry.Update(ctx, reconciled); err != nil {
				return nil, fmt.Errorf("reconcile library registry: %w", err)
			}
		}
		folders = append(folders, folder)
	}
	return folders, nil
}

func (t *Topology) reconcileFolder(path string, library store.Library) (VirtualFolder, store.Library, error) {
	if _, err := t.libraryPath(path); err != nil {
		return VirtualFolder{}, store.Library{}, err
	}
	topologyAfterFolderValidation()
	if _, err := t.libraryPath(path); err != nil {
		return VirtualFolder{}, store.Library{}, err
	}
	options, optionsXML, err := t.readLibraryOptions(path)
	if err != nil {
		return VirtualFolder{}, store.Library{}, err
	}
	collectionType, err := t.readCollectionType(path)
	if err != nil {
		return VirtualFolder{}, store.Library{}, err
	}
	mediaPaths, err := t.readMediaPaths(path)
	if err != nil {
		return VirtualFolder{}, store.Library{}, err
	}
	if addMissingPathInfos(&options, mediaPaths) {
		updatedOptionsXML, marshalErr := xml.Marshal(options)
		if marshalErr != nil {
			return VirtualFolder{}, store.Library{}, fmt.Errorf("marshal reconciled library options: %w", marshalErr)
		}
		if validLibraryOptionsXML(optionsXML) {
			if err := t.rootWriteFile(filepath.Join(path, "options.xml"), updatedOptionsXML, 0o600); err != nil {
				return VirtualFolder{}, store.Library{}, fmt.Errorf("write reconciled library options: %w", err)
			}
			optionsXML = updatedOptionsXML
		}
	}
	library.Name = filepath.Base(path)
	library.Path = path
	library.CollectionType = collectionType
	library.Options = string(optionsXML)
	return VirtualFolder{ID: library.ID, CollectionFolderID: collectionFolderItemID(path), Name: library.Name, Path: path, CollectionType: collectionType, Options: options, MediaPaths: mediaPaths}, library, nil
}

func (t *Topology) portableDirectories() ([]string, error) {
	if err := t.verifyRoot(); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(t.fsroot.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read library root: %w", err)
	}
	directories := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || validatePathComponent("library name", entry.Name()) != nil {
			continue
		}
		path := filepath.Join(t.root, entry.Name())
		if t.isPortableDirectory(path) {
			directories = append(directories, path)
		}
	}
	sort.Strings(directories)
	return directories, nil
}

func (t *Topology) isPortableDirectory(path string) bool {
	if _, err := t.rootReadFile(filepath.Join(path, "options.xml")); err == nil {
		return true
	}
	typeName, err := t.readCollectionType(path)
	return err == nil && typeName != ""
}

func (t *Topology) libraryPath(path string) (string, error) {
	if err := t.verifyRoot(); err != nil {
		return "", err
	}
	clean := filepath.Clean(path)
	relative, err := filepath.Rel(t.root, clean)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("virtual-folder path escapes library root: %q", path)
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return "", fmt.Errorf("stat virtual-folder path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("virtual-folder path is not a contained directory: %q", path)
	}
	return clean, nil
}

func (t *Topology) verifyRoot() error {
	for _, path := range []string{t.anchor, filepath.Join(t.anchor, "root"), t.root} {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("stat topology root component: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("topology root component is not a directory: %q", path)
		}
	}
	return nil
}

func (t *Topology) unusedShortcutPath(directory string) (string, error) {
	for range 16 {
		id, err := newUUID()
		if err != nil {
			return "", err
		}
		path := filepath.Join(directory, id.String()+".mblink")
		if err := ensurePathAbsent(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("allocate media shortcut name")
}

func validatePathComponent(kind, value string) error {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\\:`) || filepath.Base(value) != value || filepath.IsAbs(value) {
		return fmt.Errorf("invalid %s %q", kind, value)
	}
	return nil
}

func ensurePathAbsent(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return fmt.Errorf("path already exists: %q", path)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("stat path %q: %w", path, err)
}

func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat virtual-folder directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("virtual-folder path is not a directory: %q", path)
	}
	return nil
}

func ensureDirDurable(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	if err := syncTopologyDir(path); err != nil {
		return err
	}
	if err := syncTopologyDir(filepath.Dir(path)); err != nil {
		return err
	}
	return syncTopologyDir(filepath.Dir(filepath.Dir(path)))
}

func (t *Topology) ensureOpen() error {
	if t.fsroot == nil {
		return ErrTopologyClosed
	}
	return nil
}

func (t *Topology) rootName(path string) (string, error) {
	relative, err := filepath.Rel(t.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path escapes descriptor root: %q", path)
	}
	if err := t.ensureOpen(); err != nil {
		return "", err
	}
	return relative, nil
}

func (t *Topology) rootReadFile(path string) ([]byte, error) {
	name, err := t.rootName(path)
	if err != nil {
		return nil, err
	}
	return t.fsroot.ReadFile(name)
}

func (t *Topology) rootReadDir(path string) ([]os.DirEntry, error) {
	name, err := t.rootName(path)
	if err != nil {
		return nil, err
	}
	return fs.ReadDir(t.fsroot.FS(), name)
}

func (t *Topology) readLibraryOptions(directory string) (LibraryOptions, []byte, error) {
	contents, err := t.rootReadFile(filepath.Join(directory, "options.xml"))
	if err != nil {
		return DefaultLibraryOptions(), nil, nil
	}
	var options LibraryOptions
	if err := xml.Unmarshal(contents, &options); err != nil {
		return DefaultLibraryOptions(), contents, nil
	}
	return options, contents, nil
}

func (t *Topology) readCollectionType(directory string) (string, error) {
	entries, err := t.rootReadDir(directory)
	if err != nil {
		return "", fmt.Errorf("read virtual-folder directory: %w", err)
	}
	return collectionTypeFromEntries(entries), nil
}

func (t *Topology) readMediaPaths(directory string) ([]string, error) {
	entries, err := t.rootReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read virtual-folder directory: %w", err)
	}
	seen := make(map[string]struct{})
	paths := make([]string, 0)
	for _, entry := range entries {
		if !isShortcut(entry) {
			continue
		}
		contents, err := t.rootReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			continue
		}
		path := string(contents)
		if _, exists := seen[path]; !exists {
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func collectionTypeFromEntries(entries []os.DirEntry) string {
	markers := make([]string, 0, 1)
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.EqualFold(filepath.Ext(entry.Name()), ".collection") {
			continue
		}
		if marker, ok := canonicalCollectionType(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))); ok {
			markers = append(markers, marker)
		}
	}
	sort.Strings(markers)
	if len(markers) == 0 {
		return ""
	}
	return markers[0]
}

func (t *Topology) rootWriteFile(path string, contents []byte, mode os.FileMode) (err error) {
	name, err := t.rootName(path)
	if err != nil {
		return err
	}
	id, err := newUUID()
	if err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+".tmp-"+id.String())
	file, err := t.fsroot.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, file.Close())
		}
		if err != nil {
			_ = t.fsroot.Remove(temporary)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	if err := t.fsroot.Rename(temporary, name); err != nil {
		return err
	}
	return t.rootSyncDir(filepath.Dir(name))
}

func (t *Topology) rootRemoveFile(path string) error {
	name, err := t.rootName(path)
	if err != nil {
		return err
	}
	if err := t.fsroot.Remove(name); err != nil {
		return err
	}
	return t.rootSyncDir(filepath.Dir(name))
}

func (t *Topology) rootRemoveAll(name string) error {
	if err := t.fsroot.RemoveAll(name); err != nil {
		return err
	}
	return t.rootSyncDir(filepath.Dir(name))
}

func (t *Topology) rootSyncDir(name string) (err error) {
	directory, err := t.fsroot.Open(name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	return directory.Sync()
}

func validLibraryOptionsXML(contents []byte) bool {
	if len(contents) == 0 {
		return false
	}
	var options LibraryOptions
	return xml.Unmarshal(contents, &options) == nil
}

// DefaultLibraryOptions returns the values the upstream LibraryOptions
// constructor sets (MediaBrowser.Model/Configuration/LibraryOptions.cs).
func DefaultLibraryOptions() LibraryOptions {
	return LibraryOptions{
		Enabled:                          true,
		EnablePhotos:                     true,
		EnableRealtimeMonitor:            false,
		PathInfos:                        []MediaPathInfo{},
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
		CustomTagDelimiters:              []string{"/", "|", ";", `\`},
		DelimiterWhitelist:               []string{},
		AllowEmbeddedSubtitles:           AllowEmbeddedSubtitlesAll,
		TypeOptions:                      []TypeOptions{},
	}
}

func canonicalCollectionType(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "movies", "tvshows", "music", "musicvideos", "homevideos", "boxsets", "books", "mixed", "photos":
		return strings.ToLower(strings.TrimSpace(value)), true
	default:
		return "", false
	}
}

// collectionFolderItemID computes the deterministic scanned CollectionFolder
// item identity Jellyfin derives from a virtual folder's portable path
// (LibraryManager.GetNewItemId). It mirrors the resolver's itemID for a
// CollectionFolder: the full portable path lowercased, hashed with the CLR
// type name. It is independent of the registry ID and stable across restarts.
func collectionFolderItemID(path string) dto.UUID {
	return itemID(item.KindCollectionFolder, path, "", false)
}

// libraryByCollectionFolderID resolves the registry record whose scanned
// CollectionFolder identity matches id, matching stock's
// GetItemById<CollectionFolder>(request.Id). It returns store.ErrNotFound
// when no virtual folder's computed identity matches.
func (t *Topology) libraryByCollectionFolderID(ctx context.Context, id dto.UUID) (store.Library, error) {
	libraries, err := t.registry.List(ctx)
	if err != nil {
		return store.Library{}, fmt.Errorf("list libraries: %w", err)
	}
	for _, library := range libraries {
		path, err := t.libraryPath(library.Path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return store.Library{}, err
		}
		if collectionFolderItemID(path) == id {
			return library, nil
		}
	}
	return store.Library{}, fmt.Errorf("%w: collection folder %s", store.ErrNotFound, id)
}

func isShortcut(entry os.DirEntry) bool {
	return !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.EqualFold(filepath.Ext(entry.Name()), ".mblink")
}

func addMissingPathInfos(options *LibraryOptions, paths []string) bool {
	changed := false
	for _, path := range paths {
		found := false
		for _, info := range options.PathInfos {
			if info.Path == path {
				found = true
				break
			}
		}
		if !found {
			options.PathInfos = append(options.PathInfos, MediaPathInfo{Path: path})
			changed = true
		}
	}
	return changed
}

func filterPathInfos(infos []MediaPathInfo, path string) []MediaPathInfo {
	result := make([]MediaPathInfo, 0, len(infos))
	for _, info := range infos {
		if info.Path != path {
			result = append(result, info)
		}
	}
	return result
}

func mediaPathsFromInfos(infos []MediaPathInfo) []string {
	paths := make([]string, 0, len(infos))
	for _, info := range infos {
		paths = append(paths, info.Path)
	}
	sort.Strings(paths)
	return paths
}

func syncTopologyDir(path string) error { return topologySyncDir(path) }

func syncDir(path string) (err error) {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	return directory.Sync()
}

func newUUID() (dto.UUID, error) {
	var id dto.UUID
	if _, err := io.ReadFull(cryptorand.Reader, id[:]); err != nil {
		return dto.UUID{}, fmt.Errorf("generate library UUID: %w", err)
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id, nil
}
