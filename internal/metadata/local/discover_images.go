// SPDX-License-Identifier: GPL-2.0-only

package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
)

// ImageEntry is one pre-enumerated directory fact. DiscoverImages never reads
// the host filesystem itself; callers provide a single deterministic listing.
type ImageEntry struct {
	Name string
}

// ImageStat is the portion of a file stat needed to create an ImageInfo.
type ImageStat struct {
	Size     int64
	Modified time.Time
}

// ImageContext supplies the immutable item facts and injected filesystem facts
// used by DiscoverImages.
type ImageContext struct {
	Directory         string
	MetadataDirectory string
	ParentDirectory   string
	ParentName        string
	ItemPath          string
	Name              string
	// SeasonNumber is a season's IndexNumber. It gives the "seasonNN" and
	// "season-specials" prefixes for images in the series folder.
	SeasonNumber  *int
	Kind          item.Kind
	IsMixedFolder bool
	ReadDir       func(context.Context, string) ([]ImageEntry, error)
	Stat          func(context.Context, string) (ImageStat, error)
}

type imageCandidate struct {
	kind      item.ImageType
	index     int
	path      string
	basename  int
	extension int
	modified  time.Time
}

// DiscoverImages deterministically maps local artwork names to ImageInfo.
// Directory and stat facts are injected so scanning never performs a second
// directory traversal or relies on process-global filesystem state.
func DiscoverImages(ctx context.Context, source ImageContext) ([]item.ImageInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source.ReadDir == nil || source.Stat == nil {
		return nil, errors.New("local image discovery requires directory and stat facts")
	}
	directory := imageDirectory(source)
	entries, err := source.ReadDir(ctx, directory)
	if err != nil {
		return nil, err
	}
	candidates := make([]imageCandidate, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		kind, index, basenameRank, ok := classifyImage(source, entry.Name)
		if !ok {
			continue
		}
		extensionRank, ok := imageExtensionRank(filepath.Ext(entry.Name))
		if !ok {
			continue
		}
		path := filepath.Join(directory, entry.Name)
		stat, err := source.Stat(ctx, path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat local image %q: %w", path, err)
		}
		if stat.Size == 0 {
			continue
		}
		candidates = append(candidates, imageCandidate{kind: kind, index: index, path: path, basename: basenameRank, extension: extensionRank, modified: stat.Modified})
	}

	backdrops := selectSequentialBackdrops(candidates)
	if extra, extraErr := source.ReadDir(ctx, filepath.Join(directory, "extrafanart")); extraErr == nil {
		sort.Slice(extra, func(i, j int) bool { return strings.ToLower(extra[i].Name) < strings.ToLower(extra[j].Name) })
		for _, entry := range extra {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if _, ok := imageExtensionRank(filepath.Ext(entry.Name)); !ok {
				continue
			}
			path := filepath.Join(directory, "extrafanart", entry.Name)
			stat, err := source.Stat(ctx, path)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return nil, fmt.Errorf("stat extra fanart %q: %w", path, err)
			}
			if stat.Size > 0 {
				backdrops = append(backdrops, imageCandidate{kind: item.ImageTypeBackdrop, index: len(backdrops), path: path, modified: stat.Modified})
			}
		}
	} else if !errors.Is(extraErr, fs.ErrNotExist) {
		return nil, extraErr
	}
	candidates = append(nonBackdropCandidates(candidates), backdrops...)

	// One named image occupies each kind/index slot. Lower basename and
	// extension ranks win; pathname is the final deterministic tiebreaker.
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.kind != right.kind {
			return left.kind < right.kind
		}
		if left.index != right.index {
			return left.index < right.index
		}
		if left.basename != right.basename {
			return left.basename < right.basename
		}
		if left.extension != right.extension {
			return left.extension < right.extension
		}
		return strings.ToLower(left.path) < strings.ToLower(right.path)
	})

	images := make([]item.ImageInfo, 0, len(candidates))
	for _, candidate := range candidates {
		if len(images) > 0 && images[len(images)-1].Type == candidate.kind && images[len(images)-1].Index == candidate.index {
			continue
		}
		modified := candidate.modified
		images = append(images, item.ImageInfo{Type: candidate.kind, Index: candidate.index, Path: candidate.path, DateModified: &modified})
	}
	return images, nil
}

func imageDirectory(source ImageContext) string {
	if source.Kind == item.KindEpisode && source.MetadataDirectory != "" {
		return source.MetadataDirectory
	}
	if source.Kind == item.KindSeason && source.ParentDirectory != "" {
		return source.ParentDirectory
	}
	return source.Directory
}

func classifyImage(source ImageContext, filename string) (item.ImageType, int, int, bool) {
	base := strings.ToLower(strings.TrimSuffix(filename, filepath.Ext(filename)))
	if base == "" {
		return 0, 0, 0, false
	}
	itemBase := strings.ToLower(strings.TrimSuffix(filepath.Base(source.ItemPath), filepath.Ext(source.ItemPath)))
	if itemBase != "" && itemBase != "." && base == itemBase {
		return item.ImageTypePrimary, 0, -1, true
	}
	if source.IsMixedFolder {
		if base == strings.ToLower(source.Name)+"-poster" {
			return item.ImageTypePrimary, 0, 0, true
		}
		return 0, 0, 0, false
	}
	if source.Kind == item.KindEpisode {
		itemBase := strings.ToLower(strings.TrimSuffix(filepath.Base(source.ItemPath), filepath.Ext(source.ItemPath)))
		if base == itemBase {
			return item.ImageTypePrimary, 0, 0, true
		}
		if base == itemBase+"-thumb" {
			return item.ImageTypeThumb, 0, 0, true
		}
		return 0, 0, 0, false
	}
	if source.Kind == item.KindSeason {
		marker := strings.ToLower(strings.TrimSpace(source.Name))
		if marker == "season 00" {
			marker = "season-specials"
		} else {
			marker = strings.Replace(marker, " ", "", 1)
		}
		if base == marker+"-poster" {
			return item.ImageTypePrimary, 0, 0, true
		}
		// Upstream PopulateSeasonImagesFromSeriesFolder also tries
		// "season" plus the two-digit index, or "season-specials" for 0.
		if source.SeasonNumber != nil {
			number := "-specials"
			if *source.SeasonNumber != 0 {
				number = fmt.Sprintf("%02d", *source.SeasonNumber)
			}
			if base == "season"+number+"-poster" {
				return item.ImageTypePrimary, 0, 1, true
			}
		}
		return 0, 0, 0, false
	}
	if index, ok := indexedBackdrop(base); ok {
		return item.ImageTypeBackdrop, index, 0, true
	}
	switch base {
	case "poster":
		return item.ImageTypePrimary, 0, primaryRank(source.Kind, base), true
	case "folder", "cover":
		return item.ImageTypePrimary, 0, primaryRank(source.Kind, base), true
	case "art", "clearart":
		return item.ImageTypeArt, 0, map[string]int{"art": 0, "clearart": 1}[base], true
	case "banner":
		return item.ImageTypeBanner, 0, 0, true
	case "logo", "clearlogo":
		return item.ImageTypeLogo, 0, map[string]int{"logo": 0, "clearlogo": 1}[base], true
	case "thumb", "landscape":
		return item.ImageTypeThumb, 0, map[string]int{"thumb": 0, "landscape": 1}[base], true
	case "disc", "cdart", "discart":
		return item.ImageTypeDisc, 0, map[string]int{"disc": 0, "cdart": 1, "discart": 2}[base], true
	default:
		return 0, 0, 0, false
	}
}

func primaryRank(kind item.Kind, base string) int {
	if kind == item.KindMusicAlbum {
		return map[string]int{"folder": 0, "cover": 1, "poster": 2}[base]
	}
	return map[string]int{"poster": 0, "folder": 1, "cover": 2}[base]
}

func nonBackdropCandidates(candidates []imageCandidate) []imageCandidate {
	result := make([]imageCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.kind != item.ImageTypeBackdrop {
			result = append(result, candidate)
		}
	}
	return result
}

func selectSequentialBackdrops(candidates []imageCandidate) []imageCandidate {
	backdrops := make([]imageCandidate, 0)
	for _, candidate := range candidates {
		if candidate.kind == item.ImageTypeBackdrop {
			backdrops = append(backdrops, candidate)
		}
	}
	sort.Slice(backdrops, func(i, j int) bool {
		if backdrops[i].index != backdrops[j].index {
			return backdrops[i].index < backdrops[j].index
		}
		if backdrops[i].extension != backdrops[j].extension {
			return backdrops[i].extension < backdrops[j].extension
		}
		return strings.ToLower(backdrops[i].path) < strings.ToLower(backdrops[j].path)
	})
	result := make([]imageCandidate, 0, len(backdrops))
	lastRawIndex := -1
	for _, candidate := range backdrops {
		if len(result) > 0 && candidate.index == lastRawIndex {
			continue
		}
		if candidate.index-lastRawIndex-1 >= 3 || len(result) == 20 {
			break
		}
		rawIndex := candidate.index
		candidate.index = len(result)
		result = append(result, candidate)
		lastRawIndex = rawIndex
	}
	return result
}

func indexedBackdrop(name string) (int, bool) {
	for _, prefix := range []string{"fanart", "backdrop"} {

		if name == prefix {
			return 0, true
		}
		suffix := strings.TrimPrefix(name, prefix)
		if suffix == name {
			continue
		}
		suffix = strings.TrimPrefix(suffix, "-")
		if suffix == "" {
			continue
		}
		value := 0
		for _, digit := range suffix {
			if digit < '0' || digit > '9' {
				value = -1
				break
			}
			value = value*10 + int(digit-'0')
		}
		if value >= 0 {
			return value, true
		}
	}
	return 0, false
}

func imageExtensionRank(extension string) (int, bool) {
	switch strings.ToLower(extension) {
	case ".png":
		return 0, true
	case ".jpg":
		return 1, true
	case ".jpeg":
		return 2, true
	case ".webp":
		return 3, true
	case ".gif":
		return 4, true
	case ".tbn":
		return 5, true
	case ".svg":
		return 6, true
	default:
		return 0, false
	}
}
