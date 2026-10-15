// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/naming"
)

func resolveMovie(options naming.NamingOptions) func(context.Context, ResolveContext) (*item.Item, error) {
	return func(ctx context.Context, input ResolveContext) (*item.Item, error) {
		if !movieCollection(input) {
			return nil, nil
		}
		// MovieResolver's null CollectionType branches leave anything under a
		// series to the TV resolvers.
		if input.CollectionType == item.CollectionTypeUnknown && seriesParent(input) != nil {
			return nil, nil
		}
		if input.Entry.IsDir {
			return resolveMovieDirectory(ctx, options, input)
		}
		info, err := naming.ResolveVideoWithError(input.Entry.Path, options)
		if err != nil || info == nil || info.ExtraType != "" {
			return nil, err
		}
		kind := item.KindVideo
		if collectionIs(input, item.CollectionTypeMovies) {
			kind = item.KindMovie
		}
		if collectionIs(input, item.CollectionTypeMusicVideos) {
			kind = item.KindMusicVideo
		}
		resolved := initializeContext(input, kind)
		setName(&resolved, info.Name)
		resolved.ProductionYear = info.Year
		placeholder := isBluRayPlaceholder(input.Entry.Path)
		setVideoContainer(&resolved, info.Container, placeholder)
		if placeholder {
			videoType := item.VideoTypeBluRay
			resolved.VideoType = &videoType
			resolved.IsPlaceHolder = true
		}
		return &resolved, nil
	}
}

// resolveMovieDirectory ports MovieResolver.FindMovie's self-contained folder
// branch. A single resolved movie with no child directory belongs at the parent
// as the media item, not as a generic intermediate Folder. Upstream does not
// require movie.nfo: "Apollo 13 (1995)/Apollo.13.1995.2160p.WEB-DL.mkv" is the
// movie "Apollo 13 (1995)". Deadfin has no owned-extras pass, so a directory
// with any subdirectory, extras folders included, stays a Folder. A mixed
// library (null CollectionType) takes the same branch when the folder has a
// parent; ResolveVideos then also refuses a folder with tvshow.nfo or
// season.nfo.
func resolveMovieDirectory(ctx context.Context, options naming.NamingOptions, input ResolveContext) (*item.Item, error) {
	if !collectionIs(input, item.CollectionTypeMovies) && (input.CollectionType != item.CollectionTypeUnknown || input.Parent == nil) {
		return nil, nil
	}
	for _, entry := range input.Children {
		if entry.IsDir {
			return nil, nil
		}
	}
	resolved, err := resolveMovies(options)(ctx, input)
	if err != nil || len(resolved) != 1 {
		return nil, err
	}
	value := resolved[0]
	if value == nil {
		return nil, nil
	}
	value.IsInMixedFolder = false
	setName(value, filepath.Base(input.Entry.Path))
	return value, nil
}

func resolveMovies(options naming.NamingOptions) func(context.Context, ResolveContext) ([]*item.Item, error) {
	resolveCandidates := resolveMovieCandidates(options)
	return func(ctx context.Context, input ResolveContext) ([]*item.Item, error) {
		candidates, err := resolveCandidates(ctx, input)
		if err != nil {
			return nil, err
		}
		result := make([]*item.Item, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.LocalAlternateVersion != nil && candidate.LocalAlternateVersion.Index != 0 {
				continue
			}
			result = append(result, candidate.Item)
		}
		return result, nil
	}
}

// ignoredMultiVideoName ports MovieResolver.IsIgnoredRegex. ResolveVideos skips
// a file whose name contains the word "sample", so neither the folder-level
// group nor FindMovie's self-contained directory branch sees it. The file then
// resolves alone, as a Movie inside a generic Folder.
var ignoredMultiVideoName = regexp.MustCompile(`(?i)\bsample\b`)

// resolveMovieCandidates translates naming's already-decided alternate groups
// into scanner candidates. The resolver owns both group membership and primary
// selection; callers must not infer either fact from item presentation fields.
func resolveMovieCandidates(options naming.NamingOptions) func(context.Context, ResolveContext) ([]ResolvedCandidate, error) {
	return func(_ context.Context, input ResolveContext) ([]ResolvedCandidate, error) {
		if (!movieCollection(input) && !collectionIs(input, item.CollectionTypeTVShows)) || len(input.Children) == 0 {
			return nil, nil
		}
		mixed := input.CollectionType == item.CollectionTypeUnknown
		if mixed && (seriesParent(input) != nil || hasChildNamed(input.Children, "tvshow.nfo") || hasChildNamed(input.Children, "season.nfo")) {
			return nil, nil
		}
		paths := make([]string, 0, len(input.Children))
		entries := make(map[string]Entry, len(input.Children))
		for _, entry := range input.Children {
			if entry.IsDir || ignoredMultiVideoName.MatchString(filepath.Base(entry.Path)) {
				continue
			}
			paths = append(paths, entry.Path)
			entries[entry.Path] = entry
		}
		// ResolveMultipleInternal groups a mixed folder's movies without
		// multi-version support; FindMovie, for a movie folder, keeps it.
		supportMultiVersion := !mixed || !resolvesFolderChildren(input)
		infos, err := naming.ResolveVideoListMultiVersion(paths, options, input.CollectionType.String(), supportMultiVersion)
		if err != nil {
			return nil, err
		}
		result := make([]ResolvedCandidate, 0, len(infos))
		for _, info := range infos {
			primary, err := resolveMovieInfo(input, entries, info, options)
			if err != nil {
				return nil, err
			}
			if primary == nil {
				continue
			}
			if len(info.AlternateVersions) == 0 {
				result = append(result, ResolvedCandidate{Item: primary})
				continue
			}
			primaryID := primary.ID
			result = append(result, ResolvedCandidate{Item: primary, LocalAlternateVersion: &LocalAlternateVersion{PrimaryID: primaryID}})
			for alternateIndex, alternateInfo := range info.AlternateVersions {
				alternate, err := resolveMovieInfo(input, entries, alternateInfo, options)
				if err != nil {
					return nil, err
				}
				if alternate == nil {
					continue
				}
				result = append(result, ResolvedCandidate{Item: alternate, LocalAlternateVersion: &LocalAlternateVersion{PrimaryID: primaryID, Index: alternateIndex + 1}})
			}
		}
		return result, nil
	}
}

func resolveMovieInfo(input ResolveContext, entries map[string]Entry, info naming.VideoInfo, options naming.NamingOptions) (*item.Item, error) {
	if len(info.Files) == 0 || info.ExtraType != "" {
		return nil, nil
	}
	entry, ok := entries[info.Files[0].Path]
	if !ok {
		return nil, nil
	}
	itemContext := input
	itemContext.Entry = entry
	resolved := initializeContext(itemContext, multiVideoKind(input))
	setName(&resolved, info.Name)
	resolved.ProductionYear = info.Year
	if collectionIs(input, item.CollectionTypeTVShows) {
		episode, err := naming.ResolveEpisode(entry.Path, false, options, true)
		if err != nil {
			return nil, err
		}
		if episode != nil {
			if name := episodeTitle(entry.Path); name != "" {
				setName(&resolved, name)
			}
			resolved.IndexNumber = episode.EpisodeNumber
			resolved.ParentIndexNumber = episode.SeasonNumber
			resolved.ProductionYear = episode.Year
		}
	}
	placeholder := isBluRayPlaceholder(entry.Path)
	setVideoContainer(&resolved, info.Files[0].Container, placeholder)
	if placeholder {
		videoType := item.VideoTypeBluRay
		resolved.VideoType = &videoType
		resolved.IsPlaceHolder = true
	}
	if len(info.Files) > 1 {
		resolved.AdditionalParts = make([]string, len(info.Files)-1)
		for index := range info.Files[1:] {
			resolved.AdditionalParts[index] = info.Files[index+1].Path
		}
	}
	return &resolved, nil
}

// resolvesFolderChildren reports a folder-level group resolution: the scanner
// passes the folder's own item as Parent. A movie-folder resolution passes the
// containing folder instead.
func resolvesFolderChildren(input ResolveContext) bool {
	return input.Parent != nil && input.Parent.Path != nil && normalizedEntryPath(*input.Parent.Path) == normalizedEntryPath(input.Entry.Path)
}

func multiVideoKind(input ResolveContext) item.Kind {
	// ResolveMultipleInternal resolves a mixed folder's videos as movies, but
	// owned items (no parent) as plain videos.
	if collectionIs(input, item.CollectionTypeMovies) || input.CollectionType == item.CollectionTypeUnknown && input.Parent != nil {
		return item.KindMovie
	}
	if collectionIs(input, item.CollectionTypeMusicVideos) {
		return item.KindMusicVideo
	}
	if collectionIs(input, item.CollectionTypeTVShows) {
		return item.KindEpisode
	}
	return item.KindVideo
}

func resolveExtra(options naming.NamingOptions) func(context.Context, ResolveContext) (*item.Item, error) {
	return func(_ context.Context, input ResolveContext) (*item.Item, error) {
		if input.Entry.IsDir {
			return nil, nil
		}
		info, err := naming.ResolveExtraWithError(input.Entry.Path, input.LibraryRoot, options)
		if err != nil || info == nil || info.ExtraType == "" {
			return nil, err
		}
		kind := item.KindVideo
		if info.ExtraType == naming.ExtraTrailer {
			kind = item.KindTrailer
		} else if naming.IsAudioFile(input.Entry.Path, options) {
			kind = item.KindAudio
		}
		resolved := initializeContext(input, kind)
		setName(&resolved, info.Name)
		resolved.ExtraType = extraType(info.ExtraType)
		if info.Container != "" {
			container := info.Container
			resolved.Container = &container
		}
		return &resolved, nil
	}
}

func extraType(value string) *item.ExtraType {
	var result item.ExtraType
	switch value {
	case naming.ExtraClip:
		result = item.ExtraTypeClip
	case naming.ExtraTrailer:
		result = item.ExtraTypeTrailer
	case naming.ExtraBehindTheScenes:
		result = item.ExtraTypeBehindTheScenes
	case naming.ExtraDeletedScene:
		result = item.ExtraTypeDeletedScene
	case naming.ExtraInterview:
		result = item.ExtraTypeInterview
	case naming.ExtraScene:
		result = item.ExtraTypeScene
	case naming.ExtraSample:
		result = item.ExtraTypeSample
	case naming.ExtraThemeSong:
		result = item.ExtraTypeThemeSong
	case naming.ExtraThemeVideo:
		result = item.ExtraTypeThemeVideo
	case naming.ExtraFeaturette:
		result = item.ExtraTypeFeaturette
	case naming.ExtraShort:
		result = item.ExtraTypeShort
	default:
		return nil
	}
	return &result
}

func setName(resolved *item.Item, value string) {
	if strings.TrimSpace(value) != "" {
		resolved.Name = &value
	}
}

func setVideoContainer(resolved *item.Item, value string, opticalPlaceholder bool) {
	if value == "" {
		return
	}
	container := value
	if opticalPlaceholder {
		resolved.MediaSourceContainer = &container
		return
	}
	resolved.Container = &container
}

func fileExtension(path string) string { return strings.ToLower(filepath.Ext(path)) }

func isBluRayPlaceholder(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasSuffix(base, ".disc") && strings.Contains(base, ".bluray.")
}

func movieCollection(input ResolveContext) bool {
	return input.CollectionType == item.CollectionTypeUnknown || collectionIs(input, item.CollectionTypeMovies) || collectionIs(input, item.CollectionTypePhotos) || collectionIs(input, item.CollectionTypeHomeVideos) || collectionIs(input, item.CollectionTypeMusicVideos)
}
