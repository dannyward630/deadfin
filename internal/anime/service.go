// SPDX-License-Identifier: GPL-2.0-only

package anime

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
)

// ServiceOptions configures a Service.
type ServiceOptions struct {
	Fetcher *Fetcher
	// Dir is the seed cache directory; it must match the Fetcher's.
	Dir     string
	Sources []Source
	Logger  *slog.Logger
}

// Service owns the current crosswalk. Readers get the last built crosswalk
// without blocking; the first reader loads it from the cache directory when
// no refresh has run yet. It starts no goroutines: Refresh runs on the
// caller's goroutine (the scheduled task).
type Service struct {
	fetcher *Fetcher
	dir     string
	sources []Source
	logger  *slog.Logger

	load    sync.Mutex
	loaded  atomic.Bool
	current atomic.Pointer[Crosswalk]
}

// NewService constructs a Service.
func NewService(options ServiceOptions) *Service {
	sources := options.Sources
	if sources == nil {
		sources = DefaultSources()
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{fetcher: options.Fetcher, dir: options.Dir, sources: sources, logger: logger}
}

// Crosswalk returns the current crosswalk. It returns nil with no error when
// no seed data has been downloaded yet.
func (s *Service) Crosswalk(ctx context.Context) (*Crosswalk, error) {
	if s == nil {
		return nil, nil
	}
	if s.loaded.Load() {
		return s.current.Load(), nil
	}
	s.load.Lock()
	defer s.load.Unlock()
	if s.loaded.Load() {
		return s.current.Load(), nil
	}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	return s.current.Load(), nil
}

// Refresh downloads stale sources and rebuilds the crosswalk when a file
// changed or none is loaded. A failed source does not stop the others; the
// errors are joined. progress receives values in [0, 100].
func (s *Service) Refresh(ctx context.Context, progress func(float64)) error {
	if progress == nil {
		progress = func(float64) {}
	}
	var failures []error
	changed := false
	for index, source := range s.sources {
		updated, err := s.fetcher.Fetch(ctx, source)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			s.logger.LogAttrs(ctx, slog.LevelWarn, "anime seed download failed", slog.String("source", source.Name), slog.String("error", err.Error()))
			failures = append(failures, err)
		}
		changed = changed || updated
		progress(float64(index+1) / float64(len(s.sources)+1) * 100)
	}
	s.load.Lock()
	defer s.load.Unlock()
	if changed || !s.loaded.Load() {
		if err := s.reload(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	progress(100)
	return errors.Join(failures...)
}

// reload builds the crosswalk from disk. Callers hold s.load.
func (s *Service) reload(ctx context.Context) error {
	crosswalk, found, err := Load(ctx, s.dir, s.sources)
	if err != nil {
		return err
	}
	if found {
		s.current.Store(crosswalk)
		stats := crosswalk.Stats()
		s.logger.LogAttrs(ctx, slog.LevelInfo, "anime crosswalk loaded",
			slog.String("sources", sourceNames(s.sources)),
			slog.Int("anime", stats.Anime),
			slog.Int("anidbTitled", stats.AniDBTitled),
			slog.Int("listEntries", stats.ListEntries),
			slog.Int("titleKeys", stats.TitleKeys))
	}
	s.loaded.Store(true)
	return nil
}
