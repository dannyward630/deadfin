// SPDX-License-Identifier: GPL-2.0-only

// Package imageserve resolves an item's stored image to a processed,
// cache-backed artifact ready for HTTP delivery. It is the orchestration seam
// between the pure image pipeline (internal/images) and the HTTP image routes
// (internal/api/handlers): it selects the requested image record, materializes
// a local source — downloading remote provider URLs into the cache on first use
// rather than hot-linking or re-fetching per request — negotiates the output
// format, and drives the processor.
//
// It owns no cgo: the libvips work lives behind the Processor interface, so the
// selection, format-negotiation, and remote-download logic is unit-testable
// without a native runtime.
package imageserve

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/item"
)

var (
	// ErrImageNotFound reports that no stored image matches the requested type
	// and index.
	ErrImageNotFound = errors.New("imageserve: image not found")
	// ErrProcessorUnavailable reports that the service has no image processor
	// (the deployment was constructed without a libvips runtime).
	ErrProcessorUnavailable = errors.New("imageserve: processor unavailable")
	// ErrRemoteUnavailable reports a remote source path with no remote store to
	// download it (the deployment serves local artwork only).
	ErrRemoteUnavailable = errors.New("imageserve: remote image store unavailable")
)

// Processor performs the native resize/encode and atomically publishes a cache
// artifact. *images.Processor satisfies it.
type Processor interface {
	Process(ctx context.Context, options images.ProcessOptions) (images.ProcessedImage, error)
}

// RemoteStore materializes a possibly-remote image path as a local file,
// downloading and caching it on first use. It is consulted only for http(s)
// source paths; local paths never reach it.
type RemoteStore interface {
	Local(ctx context.Context, url string) (path string, modified time.Time, err error)
}

// Service renders stored item images into cache-backed artifacts.
type Service struct {
	processor Processor
	remote    RemoteStore
}

// New constructs a Service. remote can be nil when the deployment serves only
// local artwork. A remote source path then returns ErrRemoteUnavailable.
func New(processor Processor, remote RemoteStore) *Service {
	return &Service{processor: processor, remote: remote}
}

// Request is the resolved, transport-independent image operation.
//
// The overlay fields (PercentPlayed, UnplayedCount, Blur, Background,
// Foreground) are carried so callers can bind them and so future cache identity
// stays stable, but they are not yet applied: overlay rendering (percent-played
// bar, unplayed badge, blur, background/foreground layers) lands with WP-P3-02.
// Until then a request that sets them is served as an unoverlaid resize.
type Request struct {
	// Format is the explicit output format from the `format` parameter; when
	// nil, ClientFormats (Accept-derived) drives negotiation.
	Format        *images.Format
	ClientFormats []images.Format
	Quality       int
	Resize        images.ResizeOptions
	AutoOrient    bool

	PercentPlayed float64
	UnplayedCount *int
	Blur          *int
	Background    string
	Foreground    string
}

// Result describes a rendered artifact ready for HTTP delivery.
type Result struct {
	CachePath   string
	ContentType string
	Dimensions  images.Dimensions
	Tag         string
}

// Select returns the stored image of the given type and index, or
// ErrImageNotFound.
func Select(infos []item.ImageInfo, imageType item.ImageType, index int) (item.ImageInfo, error) {
	for _, info := range infos {
		if info.Type == imageType && info.Index == index {
			return info, nil
		}
	}
	return item.ImageInfo{}, ErrImageNotFound
}

// Tag is the cache-busting identity Deadfin emits for an image and echoes as the
// response ETag. It is the source path's base name, matching the value the DTO
// builder publishes in ImageTags/BackdropImageTags
// (internal/dtobuild/store_enricher.go; divergence D-12). The serving ETag must
// equal it so a client's conditional GET / cache-busting URL lines up with the
// tag it already received in list and detail responses. For a remote provider
// URL the base name is still the identity the client holds, so Tag is always
// computed from the original (possibly remote) path, never the downloaded copy.
func Tag(sourcePath string) string {
	return filepath.Base(sourcePath)
}

// Render materializes info (downloading a remote source into the cache on first
// use), negotiates the output format, and drives the processor. The returned
// Result names the published cache file to serve.
func (s *Service) Render(ctx context.Context, info item.ImageInfo, request Request) (Result, error) {
	if s == nil || s.processor == nil {
		return Result{}, ErrProcessorUnavailable
	}
	if info.Path == "" {
		return Result{}, ErrImageNotFound
	}

	sourcePath := info.Path
	var modified time.Time
	if info.DateModified != nil {
		modified = *info.DateModified
	}
	if isRemote(info.Path) {
		if s.remote == nil {
			return Result{}, ErrRemoteUnavailable
		}
		localPath, remoteModified, err := s.remote.Local(ctx, info.Path)
		if err != nil {
			return Result{}, err
		}
		sourcePath = localPath
		if info.DateModified == nil {
			modified = remoteModified
		}
	}

	processed, err := s.processor.Process(ctx, images.ProcessOptions{
		Source:       images.Source{Path: sourcePath, Modified: modified},
		OutputFormat: selectFormat(sourcePath, request),
		Quality:      request.Quality,
		Resize:       request.Resize,
		AutoOrient:   request.AutoOrient,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{
		CachePath:   processed.Path,
		ContentType: processed.MIMEType,
		Dimensions:  processed.Dimensions,
		Tag:         Tag(info.Path),
	}, nil
}

// selectFormat resolves the output format the way upstream's GetImageInternal
// does: an explicit `format` request constrains the candidate list to that one
// format, otherwise the Accept-derived client list drives selection, with the
// server's WebP/transparent-PNG/JPEG precedence (images.SelectFormat).
func selectFormat(sourcePath string, request Request) images.Format {
	source := formatFromExtension(sourcePath)
	transparent := transparentSource(source)
	if request.Format != nil {
		return images.SelectFormat(source, []images.Format{*request.Format}, transparent)
	}
	return images.SelectFormat(source, request.ClientFormats, transparent)
}

// formatFromExtension classifies a source path by extension for the purpose of
// output-format negotiation only; the processor independently decides whether a
// source needs ffmpeg conversion. An unknown extension is treated as JPEG so
// negotiation still terminates.
func formatFromExtension(path string) images.Format {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return images.FormatJPEG
	case ".png":
		return images.FormatPNG
	case ".webp":
		return images.FormatWebP
	case ".gif":
		return images.FormatGIF
	case ".bmp":
		return images.FormatBMP
	case ".svg":
		return images.FormatSVG
	default:
		return images.FormatJPEG
	}
}

// transparentSource reports whether a source of this format can carry an alpha
// channel, so negotiation can prefer a transparency-preserving output.
func transparentSource(format images.Format) bool {
	switch format {
	case images.FormatPNG, images.FormatWebP, images.FormatGIF:
		return true
	default:
		return false
	}
}

func isRemote(path string) bool {
	return strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
}
