// SPDX-License-Identifier: GPL-2.0-only

package itemdelete

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// delete ports LibraryManager.DeleteItem with DeleteFileLocation set. It
// removes the internal metadata of the item and its descendants, then the
// media paths, then the rows. The first media path must go; a failure on a
// later path is ignored, as upstream does.
func (s *Service) delete(ctx context.Context, target item.Item) error {
	var descendants []item.Item
	if target.IsFolder {
		result, err := s.items.Query(ctx, store.ItemQuery{ParentID: &target.ID, Recursive: true, IncludeOwnedItems: true})
		if err != nil {
			return err
		}
		descendants = result.Items
	}
	s.logger.Info("removing item", "kind", target.Kind.String(), "name", stringValue(target.Name), "path", stringValue(target.Path), "id", target.ID.String())

	for _, directory := range s.internalPaths(target, descendants) {
		if err := os.RemoveAll(directory); err != nil {
			s.logger.Error("delete metadata path", "path", directory, "error", err)
		}
	}
	if isFileProtocol(target.Path) {
		roots, err := s.mediaRoots(ctx)
		if err != nil {
			return err
		}
		layout, err := s.folderLayout(ctx, target, roots)
		if err != nil {
			return err
		}
		for index, path := range deletePaths(target, layout) {
			if holdsRoot(filepath.Clean(path.name), roots) {
				s.logger.Warn("delete item path: path holds a library media path", "path", path.name, "id", target.ID.String())
				continue
			}
			if err := removePath(path); err != nil {
				if index == 0 {
					return err
				}
				s.logger.Warn("delete item path", "path", path.name, "error", err)
			}
		}
	}

	if err := s.items.Delete(ctx, target.ID); err != nil {
		return err
	}
	if s.publisher != nil {
		parentID := dto.NilUUID
		if target.OwnerID != nil && !target.OwnerID.IsNil() {
			parentID = *target.OwnerID
		} else if target.ParentID != nil {
			parentID = *target.ParentID
		}
		collectionFolderID := dto.NilUUID
		if target.TopParentID != nil {
			collectionFolderID = *target.TopParentID
		}
		s.publisher.PublishItemRemoved(parentID, collectionFolderID, target.ID)
	}
	return nil
}

// internalPaths ports LibraryManager.GetMetadataPaths: the internal metadata
// directory of each item, and the trickplay directory of each video.
func (s *Service) internalPaths(target item.Item, descendants []item.Item) []string {
	var paths []string
	for _, value := range append([]item.Item{target}, descendants...) {
		if s.metadataPath != "" {
			id := value.ID.String()
			paths = append(paths, filepath.Join(s.metadataPath, "library", id[:2], id))
		}
		if s.trickplayPath != "" && isVideo(value.Kind) {
			id := value.ID.Dashed()
			paths = append(paths, filepath.Join(s.trickplayPath, id[:2], id))
		}
	}
	return paths
}

type deletePath struct {
	name      string
	directory bool
}

// folderLayout tells deletePaths how the item sits on disk.
type folderLayout struct {
	// ownsFolder reports a video alone in its containing folder.
	ownsFolder bool
	// mixed reports a video or a stored mixed-folder item that shares its
	// folder with other items.
	mixed bool
}

// folderLayout decides Video.IsInMixedFolder from the store, because the
// scanner does not record it: a video owns its containing folder only when
// no other item (except its own alternates and extras) has a path in that
// folder, and the folder is strictly below a library media path. The second
// rule keeps a delete from removing a library root or a folder above one.
func (s *Service) folderLayout(ctx context.Context, target item.Item, roots []string) (folderLayout, error) {
	if !isVideo(target.Kind) {
		return folderLayout{mixed: target.IsInMixedFolder}, nil
	}
	folder := filepath.Clean(containingFolderPath(target))
	inside := !holdsRoot(folder, roots) && insideRoot(folder, roots)
	shared := !inside
	if inside && target.ParentID != nil {
		siblings, err := s.items.Query(ctx, store.ItemQuery{ParentID: target.ParentID, IncludeOwnedItems: true})
		if err != nil {
			return folderLayout{}, err
		}
		for _, sibling := range siblings.Items {
			if sibling.ID == target.ID || sibling.Path == nil || isOwnedBy(sibling, target.ID) {
				continue
			}
			if pathWithin(filepath.Clean(*sibling.Path), folder) {
				shared = true
				break
			}
		}
	}
	if target.Kind == item.KindEpisode {
		return folderLayout{mixed: target.IsInMixedFolder || shared}, nil
	}
	return folderLayout{ownsFolder: !target.IsInMixedFolder && !shared, mixed: target.IsInMixedFolder || shared}, nil
}

// mediaRoots returns the media paths of every library.
func (s *Service) mediaRoots(ctx context.Context) ([]string, error) {
	folders, err := s.libraries.VirtualFolders(ctx)
	if err != nil {
		return nil, err
	}
	var roots []string
	for _, virtual := range folders {
		for _, root := range virtual.MediaPaths {
			roots = append(roots, filepath.Clean(root))
		}
	}
	return roots, nil
}

// holdsRoot reports whether path is a library media path or a folder above
// one. A delete never removes such a path.
func holdsRoot(path string, roots []string) bool {
	for _, root := range roots {
		if pathWithin(root, path) {
			return true
		}
	}
	return false
}

func insideRoot(path string, roots []string) bool {
	for _, root := range roots {
		if pathWithin(path, root) {
			return true
		}
	}
	return false
}

// pathWithin reports whether path is root or below it.
func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func isOwnedBy(value item.Item, id dto.UUID) bool {
	return (value.OwnerID != nil && *value.OwnerID == id) || (value.PrimaryVersionID != nil && *value.PrimaryVersionID == id)
}

// deletePaths ports BaseItem.GetDeletePaths and its Video and Episode
// overrides. A video that owns its containing folder loses the whole folder.
func deletePaths(target item.Item, layout folderLayout) []deletePath {
	path := *target.Path
	if layout.ownsFolder {
		return []deletePath{{name: containingFolderPath(target), directory: true}}
	}
	first := deletePath{name: path, directory: target.IsFolder}
	if target.Kind != item.KindEpisode {
		// FileSystem.GetFileSystemInfo reports what is on disk.
		if info, err := os.Stat(path); err == nil {
			first.directory = info.IsDir()
		}
	}
	if !layout.mixed {
		return []deletePath{first}
	}
	return append([]deletePath{first}, localMetadataFiles(target)...)
}

// containingFolderPath ports Video.ContainingFolderPath.
func containingFolderPath(target item.Item) string {
	path := *target.Path
	if !target.IsPlaceHolder && target.VideoType != nil && (*target.VideoType == item.VideoTypeBluRay || *target.VideoType == item.VideoTypeDvd) {
		return path
	}
	if target.IsFolder {
		return path
	}
	return filepath.Dir(path)
}

// localMetadataFiles ports BaseItem.GetLocalMetadataFilesToDelete: in a
// mixed folder, the sidecar files whose names start with the media file name.
func localMetadataFiles(target item.Item) []deletePath {
	if target.IsFolder {
		return nil
	}
	path := *target.Path
	stem := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil
	}
	var files []deletePath
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isLocalMetadataExtension(strings.ToLower(filepath.Ext(name))) {
			continue
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))), stem) {
			files = append(files, deletePath{name: filepath.Join(filepath.Dir(path), name)})
		}
	}
	return files
}

// removePath ports LibraryManager.DeleteItemPath. A path that is already gone
// is not an error.
func removePath(path deletePath) error {
	var err error
	if path.directory {
		if _, statErr := os.Stat(path.name); errors.Is(statErr, fs.ErrNotExist) {
			return nil
		}
		err = os.RemoveAll(path.name)
	} else {
		err = os.Remove(path.name)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// isLocalMetadataExtension ports BaseItem._supportedExtensions: the image and
// sidecar extensions a mixed-folder delete also removes.
func isLocalMetadataExtension(extension string) bool {
	switch extension {
	case ".png", ".jpg", ".jpeg", ".webp", ".tbn", ".gif", ".svg",
		".nfo", ".xml", ".srt", ".vtt", ".sub", ".sup", ".idx", ".txt",
		".edl", ".bif", ".smi", ".ttml", ".lrc", ".elrc":
		return true
	}
	return false
}

func isVideo(kind item.Kind) bool {
	switch kind {
	case item.KindEpisode, item.KindMovie, item.KindMusicVideo, item.KindRecording, item.KindTrailer, item.KindVideo:
		return true
	}
	return false
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
