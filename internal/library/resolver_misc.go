// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/naming"
)

func resolveSpecialFolder(_ context.Context, input ResolveContext) (*item.Item, error) {
	if input.IsPhysicalLibraryRoot {
		if !input.Entry.IsDir {
			return nil, nil
		}
		resolved := initializeContext(input, item.KindFolder)
		return &resolved, nil
	}
	if !input.Entry.IsDir || !input.IsVirtualFolder && !input.PhysicalRoot {
		return nil, nil
	}
	kind := item.KindAggregateFolder
	if input.IsVirtualFolder {
		kind = item.KindCollectionFolder
	}
	resolved := initializeContext(input, kind)
	if kind == item.KindCollectionFolder {
		collectionType := input.CollectionType
		resolved.Extra.CollectionFolder = &item.CollectionFolderExtra{CollectionType: &collectionType}
	}
	return &resolved, nil
}

func resolvePhotoAlbum(options naming.NamingOptions) func(context.Context, ResolveContext) (*item.Item, error) {
	return func(_ context.Context, input ResolveContext) (*item.Item, error) {
		if !input.Entry.IsDir || !photoCollection(input) || !hasPhotoChild(input.Children, options) {
			return nil, nil
		}
		resolved := initializeContext(input, item.KindPhotoAlbum)
		return &resolved, nil
	}
}

func resolvePhoto(options naming.NamingOptions) func(context.Context, ResolveContext) (*item.Item, error) {
	return func(_ context.Context, input ResolveContext) (*item.Item, error) {
		if input.Entry.IsDir || !photoCollection(input) || !isPhoto(input.Entry.Path) || photoOwnedByVideo(input, options) {
			return nil, nil
		}
		resolved := initializeContext(input, item.KindPhoto)
		return &resolved, nil
	}
}

func resolvePlaylist(_ context.Context, input ResolveContext) (*item.Item, error) {
	if !playlistEntry(input) {
		return nil, nil
	}
	resolved := initializeContext(input, item.KindPlaylist)
	setName(&resolved, markerName(*resolved.Name, "[playlist]"))
	return &resolved, nil
}

func resolveBook(_ context.Context, input ResolveContext) (*item.Item, error) {
	if !collectionIs(input, item.CollectionTypeBooks) {
		return nil, nil
	}
	entry := input.Entry
	if entry.IsDir {
		supported := make([]Entry, 0, len(input.Children))
		for _, child := range input.Children {
			if !child.IsDir && isBook(child.Path) {
				supported = append(supported, child)
			}
		}
		if len(supported) != 1 {
			return nil, nil
		}
		entry = supported[0]
	}
	if !isBook(entry.Path) {
		return nil, nil
	}
	parseName := strings.TrimSuffix(filepath.Base(entry.Path), filepath.Ext(entry.Path))
	if input.Entry.IsDir {
		parseName = filepath.Base(input.Entry.Path)
	}
	parsed := naming.ParseBook(parseName)
	bookInput := input
	bookInput.Entry = entry
	resolved := initializeContext(bookInput, item.KindBook)
	if parsed.Name != nil {
		setName(&resolved, *parsed.Name)
	}
	if parsed.SeriesName != nil {
		seriesName := *parsed.SeriesName
		resolved.SeriesName = &seriesName
	} else {
		seriesName := filepath.Base(filepath.Dir(entry.Path))
		resolved.SeriesName = &seriesName
	}
	resolved.ProductionYear = parsed.Year
	resolved.IndexNumber = parsed.Index
	resolved.ParentIndexNumber = parsed.ParentIndex
	return &resolved, nil
}

func resolveBoxSet(options naming.NamingOptions) func(context.Context, ResolveContext) (*item.Item, error) {
	return func(_ context.Context, input ResolveContext) (*item.Item, error) {
		if !input.Entry.IsDir || !strings.Contains(strings.ToLower(filepath.Base(input.Entry.Path)), "[boxset]") && !hasChildNamed(input.Children, "collection.xml") {
			return nil, nil
		}
		resolved := initializeContext(input, item.KindBoxSet)
		name := markerName(filepath.Base(input.Entry.Path), "[boxset]")
		setName(&resolved, name)
		displayOrder := "PremiereDate"
		resolved.Extra.BoxSet = &item.BoxSetExtra{DisplayOrder: &displayOrder}
		for _, child := range input.Children {
			if child.IsDir {
				continue
			}
			video, err := naming.ResolveVideoWithError(child.Path, options)
			if err != nil {
				return nil, err
			}
			if video == nil || video.ExtraType != "" {
				continue
			}
			if video.Year != nil && (resolved.ProductionYear == nil || *video.Year < *resolved.ProductionYear) {
				year := *video.Year
				resolved.ProductionYear = &year
			}
			order := len(resolved.LinkedChildren)
			resolved.LinkedChildren = append(resolved.LinkedChildren, item.LinkedChildRef{
				ItemID: itemID(item.KindMovie, child.Path, input.ProgramDataRoot, input.CaseSensitiveIDs),
				Type:   item.LinkedChildTypeManual, SortOrder: &order,
			})
		}
		return &resolved, nil
	}
}

func resolveFolder(_ context.Context, input ResolveContext) (*item.Item, error) {
	if !input.Entry.IsDir {
		return nil, nil
	}
	resolved := initializeContext(input, item.KindFolder)
	return &resolved, nil
}

func isPhoto(path string) bool {
	if !isOneOf(fileExtension(path), ".bmp", ".gif", ".heic", ".jpeg", ".jpg", ".png", ".tif", ".tiff", ".webp") {
		return false
	}
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	for _, prefix := range []string{"folder", "thumb", "landscape", "fanart", "backdrop", "poster", "cover", "logo", "default"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

func isOneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func photoCollection(input ResolveContext) bool {
	return collectionIs(input, item.CollectionTypePhotos) || collectionIs(input, item.CollectionTypeHomeVideos)
}

func hasPhotoChild(children []Entry, options naming.NamingOptions) bool {
	for _, child := range children {
		if child.IsDir || !isPhoto(child.Path) || photoChildOwnedByVideo(child, children, options) {
			continue
		}
		return true
	}
	return false
}

func playlistEntry(input ResolveContext) bool {
	if input.Entry.IsDir {
		return hasChildNamed(input.Children, "playlist.xml") || strings.Contains(strings.ToLower(filepath.Base(input.Entry.Path)), "[playlist]")
	}
	return (input.CollectionType == item.CollectionTypeUnknown || collectionIs(input, item.CollectionTypeMusic)) && isOneOf(fileExtension(input.Entry.Path), ".m3u", ".m3u8", ".pls", ".wpl", ".zpl")
}

func hasChildNamed(children []Entry, name string) bool {
	for _, child := range children {
		if strings.EqualFold(filepath.Base(child.Path), name) {
			return true
		}
	}
	return false
}

func isBook(path string) bool {
	return isOneOf(fileExtension(path), ".epub", ".pdf", ".mobi", ".azw", ".azw3", ".cbz", ".cbr", ".cb7", ".cbt")
}

func photoOwnedByVideo(input ResolveContext, options naming.NamingOptions) bool {
	name := strings.TrimSuffix(filepath.Base(input.Entry.Path), filepath.Ext(input.Entry.Path))
	for _, child := range input.Children {
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(strings.TrimSuffix(filepath.Base(child.Path), filepath.Ext(child.Path)))) && videoExtension(child.Path, options) {
			return true
		}
	}
	return false
}

func photoChildOwnedByVideo(photo Entry, children []Entry, options naming.NamingOptions) bool {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(photo.Path), filepath.Ext(photo.Path)))
	for _, child := range children {
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(child.Path), filepath.Ext(child.Path)))
		if strings.HasPrefix(name, base) && videoExtension(child.Path, options) {
			return true
		}
	}
	return false
}

func videoExtension(path string, options naming.NamingOptions) bool {
	for _, extension := range options.VideoFileExtensions {
		if strings.EqualFold(fileExtension(path), extension) {
			return true
		}
	}
	return false
}

func markerName(name, marker string) string {
	position := strings.Index(strings.ToLower(name), marker)
	if position < 0 {
		return strings.TrimSpace(name)
	}
	return strings.TrimSpace(name[:position] + name[position+len(marker):])
}
