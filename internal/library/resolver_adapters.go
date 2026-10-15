// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/naming"
)

type adapter struct {
	priority ResolverPriority
	resolve  func(context.Context, ResolveContext) (*item.Item, error)
}

func (r adapter) Priority() ResolverPriority { return r.priority }
func (r adapter) Resolve(ctx context.Context, input ResolveContext) (*item.Item, error) {
	if r.resolve == nil {
		return nil, nil
	}
	return r.resolve(ctx, input)
}

type multiAdapter struct {
	adapter
	resolveMany       func(context.Context, ResolveContext) ([]*item.Item, error)
	resolveCandidates func(context.Context, ResolveContext) ([]ResolvedCandidate, error)
}

func (r multiAdapter) ResolveMany(ctx context.Context, input ResolveContext) ([]*item.Item, error) {
	if r.resolveMany == nil {
		return nil, nil
	}
	return r.resolveMany(ctx, input)
}

func (r multiAdapter) ResolveCandidates(ctx context.Context, input ResolveContext) ([]ResolvedCandidate, error) {
	if r.resolveCandidates == nil {
		return nil, nil
	}
	return r.resolveCandidates(ctx, input)
}

// NewDefaultChain installs the source-ordered, pure naming adapters. It has no
// filesystem, store, probe, metadata, or refresh dependency.
func NewDefaultChain(options naming.NamingOptions) *Chain {
	return NewChain(defaultResolvers(snapshotNamingOptions(options))...)
}

func defaultResolvers(options naming.NamingOptions) []Resolver {
	return []Resolver{
		adapter{priority: ResolverPriorityFirst, resolve: resolveSpecialFolder},
		adapter{priority: ResolverPrioritySecond, resolve: resolveSeries(options)},
		adapter{priority: ResolverPrioritySecond, resolve: resolveMusicArtist(options)},
		adapter{priority: ResolverPrioritySecond, resolve: resolvePhotoAlbum(options)},
		adapter{priority: ResolverPriorityThird, resolve: resolveMusicAlbum(options)},
		multiAdapter{adapter: adapter{priority: ResolverPriorityFourth, resolve: resolveMovie(options)}, resolveMany: resolveMovies(options), resolveCandidates: resolveMovieCandidates(options)},
		multiAdapter{adapter: adapter{priority: ResolverPriorityFifth, resolve: resolveAudio(options)}, resolveMany: resolveAudioMany(options)},
		adapter{priority: ResolverPriorityFirst, resolve: resolveSeason(options)},
		adapter{priority: ResolverPriorityFirst, resolve: resolveEpisode(options)},
		adapter{priority: ResolverPriorityFirst, resolve: resolvePhoto(options)},
		adapter{priority: ResolverPriorityFirst, resolve: resolvePlaylist},
		adapter{priority: ResolverPriorityFirst, resolve: resolveBook},
		adapter{priority: ResolverPriorityFirst, resolve: resolveBoxSet(options)},
		adapter{priority: ResolverPriorityFirst, resolve: resolveExtra(options)},
		adapter{priority: ResolverPriorityLast, resolve: resolveFolder},
	}
}

func snapshotNamingOptions(options naming.NamingOptions) naming.NamingOptions {
	result := options
	result.VideoFileExtensions = copyResolverSlice(options.VideoFileExtensions)
	result.StubFileExtensions = copyResolverSlice(options.StubFileExtensions)
	result.AudioFileExtensions = copyResolverSlice(options.AudioFileExtensions)
	result.SubtitleFileExtensions = copyResolverSlice(options.SubtitleFileExtensions)
	result.LyricFileExtensions = copyResolverSlice(options.LyricFileExtensions)
	result.VideoFlagDelimiters = copyResolverSlice(options.VideoFlagDelimiters)
	result.MediaFlagDelimiters = copyResolverSlice(options.MediaFlagDelimiters)
	result.MediaForcedFlags = copyResolverSlice(options.MediaForcedFlags)
	result.MediaDefaultFlags = copyResolverSlice(options.MediaDefaultFlags)
	result.MediaHearingImpairedFlags = copyResolverSlice(options.MediaHearingImpairedFlags)
	result.AlbumStackingPrefixes = copyResolverSlice(options.AlbumStackingPrefixes)
	result.ArtistSubfolders = copyResolverSlice(options.ArtistSubfolders)
	result.StubTypes = copyResolverSlice(options.StubTypes)
	result.Format3DRules = copyResolverSlice(options.Format3DRules)
	result.VideoExtraRules = copyResolverSlice(options.VideoExtraRules)
	result.VideoFileStackingRules = copyResolverSlice(options.VideoFileStackingRules)
	result.AudioBookPartsExpressions = copyResolverSlice(options.AudioBookPartsExpressions)
	result.AudioBookNamesExpressions = copyResolverSlice(options.AudioBookNamesExpressions)
	result.CleanDateTimeRegexes = copyResolverSlice(options.CleanDateTimeRegexes)
	result.CleanStringRegexes = copyResolverSlice(options.CleanStringRegexes)
	result.EpisodeExpressions = copyEpisodeExpressions(options.EpisodeExpressions)
	result.MultipleEpisodeExpressions = copyEpisodeExpressions(options.MultipleEpisodeExpressions)
	return result
}

func copyResolverSlice[T any](values []T) []T { return append([]T(nil), values...) }

func copyEpisodeExpressions(values []*naming.EpisodeExpression) []*naming.EpisodeExpression {
	result := make([]*naming.EpisodeExpression, len(values))
	for index, value := range values {
		if value == nil {
			continue
		}
		copy := naming.NewEpisodeExpression(value.Expression, value.IsByDate)
		copy.IsOptimistic = value.IsOptimistic
		copy.IsNamed = value.IsNamed
		copy.SupportsAbsoluteEpisodeNumbers = value.SupportsAbsoluteEpisodeNumbers
		copy.DateTimeFormats = copyResolverSlice(value.DateTimeFormats)
		result[index] = copy
	}
	return result
}
