// SPDX-License-Identifier: GPL-2.0-only

package anime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
)

// Source is one downloadable seed dataset.
type Source struct {
	// Name identifies the source in logs.
	Name string
	URL  string
	// File is the cache file name inside the seed directory.
	File string
	// read parses the dataset into builder, or only validates it when
	// builder is nil.
	read func(reader io.Reader, builder *Builder) error
}

// Default seed source URLs. manami publishes weekly releases; the
// "latest/download" path redirects to the newest asset.
const (
	ManamiURL      = "https://github.com/manami-project/anime-offline-database/releases/latest/download/anime-offline-database-minified.json"
	AniDBTitlesURL = "https://anidb.net/api/anime-titles.xml.gz"
	AnimeListsURL  = "https://raw.githubusercontent.com/Anime-Lists/anime-lists/master/anime-list-master.xml"
)

// DefaultSources returns the three seed datasets.
func DefaultSources() []Source {
	return []Source{
		ManamiSource(ManamiURL),
		AniDBTitlesSource(AniDBTitlesURL),
		AnimeListsSource(AnimeListsURL),
	}
}

// ManamiSource is the anime-offline-database at url.
func ManamiSource(url string) Source {
	return Source{Name: "manami", URL: url, File: "anime-offline-database-minified.json", read: func(reader io.Reader, builder *Builder) error {
		return ParseManami(reader, func(entry ManamiEntry) {
			if builder != nil {
				builder.AddManami(entry)
			}
		})
	}}
}

// AniDBTitlesSource is the AniDB title dump at url. AniDB bans clients that
// download it more than once a day; the Fetcher interval enforces that.
func AniDBTitlesSource(url string) Source {
	return Source{Name: "anidb-titles", URL: url, File: "anime-titles.xml.gz", read: func(reader io.Reader, builder *Builder) error {
		return ParseAniDBTitles(reader, func(anime AniDBAnime) {
			if builder != nil {
				builder.AddAniDBTitles(anime)
			}
		})
	}}
}

// AnimeListsSource is the Anime-Lists master mapping at url.
func AnimeListsSource(url string) Source {
	return Source{Name: "anime-lists", URL: url, File: "anime-list-master.xml", read: func(reader io.Reader, builder *Builder) error {
		return ParseAnimeLists(reader, func(entry ListEntry) {
			if builder != nil {
				builder.AddListEntry(entry)
			}
		})
	}}
}

// Doer executes one HTTP request. *http.Client satisfies it.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// FetcherOptions configures a Fetcher.
type FetcherOptions struct {
	Doer  Doer
	Clock clock.Clock
	// Dir is the seed cache directory. It is created when missing.
	Dir string
	// UserAgent identifies Deadfin to the dataset hosts. AniDB requires a
	// descriptive client name.
	UserAgent string
	// Interval is the minimum time between two requests for one source.
	// Zero selects 24 hours, the AniDB limit.
	Interval time.Duration
	// MaxBytes bounds one download. Zero selects 256 MiB.
	MaxBytes int64
	Logger   *slog.Logger
}

// Fetcher downloads seed datasets into a cache directory with conditional
// requests. It makes at most one request per source per Interval, counted
// from the last attempt, so a failing source is not retried sooner.
type Fetcher struct {
	options FetcherOptions
}

// NewFetcher constructs a Fetcher.
func NewFetcher(options FetcherOptions) *Fetcher {
	if options.Clock == nil {
		options.Clock = clock.NewReal()
	}
	if options.Doer == nil {
		options.Doer = &http.Client{Timeout: 10 * time.Minute}
	}
	if options.Interval <= 0 {
		options.Interval = 24 * time.Hour
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = 256 << 20
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Fetcher{options: options}
}

// sourceMeta is the cache sidecar of one source.
type sourceMeta struct {
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"lastModified,omitempty"`
	Attempted    time.Time `json:"attempted"`
	Updated      time.Time `json:"updated,omitzero"`
}

// Fetch refreshes one source. It reports whether the cached file changed.
// It does nothing when the last attempt is younger than the interval.
func (f *Fetcher) Fetch(ctx context.Context, source Source) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := os.MkdirAll(f.options.Dir, 0o755); err != nil {
		return false, fmt.Errorf("anime seed %s: create directory: %w", source.Name, err)
	}
	path := filepath.Join(f.options.Dir, source.File)
	meta := f.readMeta(path)
	now := f.options.Clock.Now()
	if !meta.Attempted.IsZero() && now.Sub(meta.Attempted) < f.options.Interval {
		return false, nil
	}
	_, statErr := os.Stat(path)
	cached := statErr == nil
	meta.Attempted = now
	// Record the attempt before the request, so a crash or a failure cannot
	// cause a second request inside the interval.
	if err := writeMeta(path, meta); err != nil {
		return false, fmt.Errorf("anime seed %s: %w", source.Name, err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return false, fmt.Errorf("anime seed %s: build request: %w", source.Name, err)
	}
	if f.options.UserAgent != "" {
		request.Header.Set("User-Agent", f.options.UserAgent)
	}
	if cached {
		if meta.ETag != "" {
			request.Header.Set("If-None-Match", meta.ETag)
		}
		if meta.LastModified != "" {
			request.Header.Set("If-Modified-Since", meta.LastModified)
		}
	}
	response, err := f.options.Doer.Do(request)
	if err != nil {
		return false, fmt.Errorf("anime seed %s: request: %w", source.Name, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
	}()
	switch {
	case response.StatusCode == http.StatusNotModified && cached:
		f.options.Logger.LogAttrs(ctx, slog.LevelInfo, "anime seed not modified", slog.String("source", source.Name))
		return false, nil
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return false, fmt.Errorf("anime seed %s: upstream returned status %d", source.Name, response.StatusCode)
	}

	temporary, err := os.CreateTemp(f.options.Dir, source.File+".*.tmp")
	if err != nil {
		return false, fmt.Errorf("anime seed %s: create temporary file: %w", source.Name, err)
	}
	defer os.Remove(temporary.Name())
	written, err := io.Copy(temporary, io.LimitReader(response.Body, f.options.MaxBytes+1))
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, fmt.Errorf("anime seed %s: download: %w", source.Name, err)
	}
	if written > f.options.MaxBytes {
		return false, fmt.Errorf("anime seed %s: download exceeds %d bytes", source.Name, f.options.MaxBytes)
	}
	if err := validate(temporary.Name(), source); err != nil {
		return false, fmt.Errorf("anime seed %s: rejected download: %w", source.Name, err)
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return false, fmt.Errorf("anime seed %s: install: %w", source.Name, err)
	}
	meta.ETag = response.Header.Get("ETag")
	meta.LastModified = response.Header.Get("Last-Modified")
	meta.Updated = now
	if err := writeMeta(path, meta); err != nil {
		return true, fmt.Errorf("anime seed %s: %w", source.Name, err)
	}
	f.options.Logger.LogAttrs(ctx, slog.LevelInfo, "anime seed updated", slog.String("source", source.Name), slog.Int64("bytes", written))
	return true, nil
}

func validate(path string, source Source) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return source.read(file, nil)
}

func metaPath(path string) string { return path + ".meta.json" }

func (f *Fetcher) readMeta(path string) sourceMeta {
	var meta sourceMeta
	contents, err := os.ReadFile(metaPath(path))
	if err != nil {
		return meta
	}
	if err := json.Unmarshal(contents, &meta); err != nil {
		return sourceMeta{}
	}
	return meta
}

func writeMeta(path string, meta sourceMeta) error {
	contents, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	temporary := metaPath(path) + ".tmp"
	if err := os.WriteFile(temporary, contents, 0o644); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	if err := os.Rename(temporary, metaPath(path)); err != nil {
		return fmt.Errorf("install metadata: %w", err)
	}
	return nil
}

// Load builds a crosswalk from the cached files in dir. A missing file is
// skipped; Load reports found=false when no source file exists.
func Load(ctx context.Context, dir string, sources []Source) (*Crosswalk, bool, error) {
	var builder Builder
	found := false
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		file, err := os.Open(filepath.Join(dir, source.File))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("anime seed %s: open: %w", source.Name, err)
		}
		err = source.read(file, &builder)
		_ = file.Close()
		if err != nil {
			return nil, false, fmt.Errorf("anime seed %s: %w", source.Name, err)
		}
		found = true
	}
	if !found {
		return nil, false, nil
	}
	return builder.Build(), true, nil
}

// sourceNames lists source names for logs.
func sourceNames(sources []Source) string {
	names := make([]string, len(sources))
	for index, source := range sources {
		names[index] = source.Name
	}
	return strings.Join(names, ",")
}
