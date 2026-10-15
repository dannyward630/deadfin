// SPDX-License-Identifier: GPL-2.0-only

//go:build cgo

package images

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/davidbyttow/govips/v2/vips"
)

var (
	// ErrRuntimeClosed reports use of a Runtime after Close has completed.
	ErrRuntimeClosed = errors.New("image runtime is closed")
	// ErrUnsupportedOutputFormat reports an output format this processor cannot encode.
	ErrUnsupportedOutputFormat = errors.New("unsupported image output format")
	// ErrUnsupportedSourceFormat reports a source that needs a SourceConverter.
	ErrUnsupportedSourceFormat = errors.New("unsupported image source format")
	// ErrRuntimeInUse reports an attempt to create a second owner for the
	// process-wide libvips runtime.
	ErrRuntimeInUse = errors.New("image runtime is already in use")
)

// runtimeLifecycle is the minimal ownership adapter required because govips
// starts one process-wide libvips instance and cannot restart it after Shutdown.
var runtimeLifecycle struct {
	mu       sync.Mutex
	active   bool
	shutdown bool
}

// Runtime owns the process-wide libvips lifecycle. It must be closed only after
// all processors created from it are no longer in use. libvips cannot be
// restarted after shutdown, so a closed Runtime is terminal.
type Runtime struct {
	state *runtimeState
}

type runtimeState struct {
	mu     sync.RWMutex
	closed bool
}

// OpenRuntime starts libvips for use by a Processor. The caller owns the
// returned Runtime and must close it exactly once after processing is complete.
func OpenRuntime(ctx context.Context) (runtime *Runtime, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	runtimeLifecycle.mu.Lock()
	defer runtimeLifecycle.mu.Unlock()
	if runtimeLifecycle.active {
		return nil, ErrRuntimeInUse
	}
	if runtimeLifecycle.shutdown {
		return nil, ErrRuntimeClosed
	}

	// govips reports initialization failures as panics. Keep that implementation
	// detail behind this package's error-returning boundary.
	defer func() {
		if recovered := recover(); recovered != nil {
			runtime = nil
			err = fmt.Errorf("start libvips: %v", recovered)
		}
	}()
	vips.Startup(nil)
	runtimeLifecycle.active = true
	return &Runtime{state: &runtimeState{}}, nil
}

// Close waits for in-flight processing to finish and then shuts down libvips.
// It is safe to call more than once.
func (runtime *Runtime) Close() error {
	if runtime == nil || runtime.state == nil {
		return nil
	}

	runtime.state.mu.Lock()
	defer runtime.state.mu.Unlock()
	if runtime.state.closed {
		return nil
	}
	vips.Shutdown()
	runtime.state.closed = true

	runtimeLifecycle.mu.Lock()
	runtimeLifecycle.active = false
	runtimeLifecycle.shutdown = true
	runtimeLifecycle.mu.Unlock()
	return nil
}

func (runtime *Runtime) begin() error {
	if runtime == nil || runtime.state == nil {
		return ErrRuntimeClosed
	}
	runtime.state.mu.RLock()
	if runtime.state.closed {
		runtime.state.mu.RUnlock()
		return ErrRuntimeClosed
	}
	return nil
}

func (runtime *Runtime) end() {
	runtime.state.mu.RUnlock()
}

// ProcessorConfig controls the cache location and maximum simultaneous image
// operations for one Processor. SourceConverter is required only for source
// extensions that libvips does not process natively.
type ProcessorConfig struct {
	CacheRoot       string
	MaxConcurrent   int
	SourceConverter SourceConverter
}

// Processor transforms source images and atomically publishes their cache files.
type Processor struct {
	runtime         *Runtime
	cacheRoot       string
	limit           chan struct{}
	sourceConverter SourceConverter
}

// Source identifies an image and the modification time used in cache identity.
type Source struct {
	Path     string
	Modified time.Time
}

// ProcessOptions contains every caller-supplied image operation. Cache identity
// is derived internally so it cannot disagree with the processing request.
type ProcessOptions struct {
	Source       Source
	OutputFormat Format
	Quality      int
	Resize       ResizeOptions
	AutoOrient   bool
}

// ProcessedImage describes the published cache artifact.
type ProcessedImage struct {
	Path       string
	MIMEType   string
	Dimensions Dimensions
}

// NewProcessor constructs a processor bound to runtime.
func NewProcessor(runtime *Runtime, config ProcessorConfig) (*Processor, error) {
	if err := runtime.begin(); err != nil {
		return nil, err
	}
	runtime.end()
	if config.CacheRoot == "" {
		return nil, errors.New("image cache root is required")
	}
	if config.MaxConcurrent <= 0 {
		return nil, errors.New("image processor maximum concurrency must be positive")
	}

	return &Processor{
		runtime:         runtime,
		cacheRoot:       config.CacheRoot,
		limit:           make(chan struct{}, config.MaxConcurrent),
		sourceConverter: config.SourceConverter,
	}, nil
}

// Process decodes, optionally auto-orients, resizes, encodes, and atomically
// publishes an image. Cancellation is observed before native work and before
// publication; libvips itself has no context-aware encoder API.
func (processor *Processor) Process(ctx context.Context, options ProcessOptions) (ProcessedImage, error) {
	if err := ctx.Err(); err != nil {
		return ProcessedImage{}, err
	}
	if processor == nil || processor.runtime == nil {
		return ProcessedImage{}, ErrRuntimeClosed
	}
	if !supportsOutput(options.OutputFormat) {
		return ProcessedImage{}, fmt.Errorf("%w: %s", ErrUnsupportedOutputFormat, options.OutputFormat)
	}

	cachePath := CachePath(processor.cacheRoot, CacheKey(CacheOptions{
		SourcePath: options.Source.Path,
		Quality:    options.Quality,
		Modified:   options.Source.Modified,
		Format:     options.OutputFormat,
		Resize:     options.Resize,
	}), options.OutputFormat)
	result := ProcessedImage{
		Path:     cachePath,
		MIMEType: mimeType(options.OutputFormat),
	}

	if _, err := os.Stat(cachePath); err == nil {
		select {
		case processor.limit <- struct{}{}:
			defer func() { <-processor.limit }()
		case <-ctx.Done():
			return ProcessedImage{}, ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return ProcessedImage{}, err
		}
		if err := processor.runtime.begin(); err != nil {
			return ProcessedImage{}, err
		}
		defer processor.runtime.end()
		cachedImage, err := vips.NewImageFromFile(cachePath)
		if err != nil {
			return ProcessedImage{}, fmt.Errorf("decode image cache %q: %w", cachePath, err)
		}
		result.Dimensions = Dimensions{Width: cachedImage.Width(), Height: cachedImage.Height()}
		cachedImage.Close()
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ProcessedImage{}, fmt.Errorf("stat image cache %q: %w", cachePath, err)
	}

	select {
	case processor.limit <- struct{}{}:
		defer func() { <-processor.limit }()
	case <-ctx.Done():
		return ProcessedImage{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return ProcessedImage{}, err
	}
	if err := processor.runtime.begin(); err != nil {
		return ProcessedImage{}, err
	}
	defer processor.runtime.end()
	sourcePath, cleanup, err := processor.convertSource(ctx, options.Source.Path)
	if err != nil {
		return ProcessedImage{}, err
	}
	defer cleanup()

	imageRef, err := vips.NewImageFromFile(sourcePath)
	if err != nil {
		return ProcessedImage{}, fmt.Errorf("decode image %q: %w", options.Source.Path, err)
	}
	defer imageRef.Close()

	if options.AutoOrient {
		if err := imageRef.AutoRotate(); err != nil {
			return ProcessedImage{}, fmt.Errorf("auto-orient image %q: %w", options.Source.Path, err)
		}
	}

	sourceDimensions := Dimensions{Width: imageRef.Width(), Height: imageRef.Height()}
	targetDimensions := ResolveDimensions(sourceDimensions, options.Resize)
	if targetDimensions.Width <= 0 || targetDimensions.Height <= 0 {
		return ProcessedImage{}, fmt.Errorf("invalid resolved image dimensions %dx%d", targetDimensions.Width, targetDimensions.Height)
	}
	result.Dimensions = targetDimensions
	if shouldPublishOriginal(options, sourcePath, sourceDimensions, targetDimensions) {
		return result, processor.publishSource(ctx, sourcePath, cachePath)
	}

	if options.OutputFormat == FormatSVG {
		if sourceFormat(sourcePath) != FormatSVG || options.AutoOrient || sourceDimensions != targetDimensions {
			return ProcessedImage{}, fmt.Errorf("%w: SVG transforms", ErrUnsupportedOutputFormat)
		}
		return result, processor.publishSource(ctx, sourcePath, cachePath)
	}

	if err := ctx.Err(); err != nil {
		return ProcessedImage{}, err
	}

	encoded, err := encodeSkiaEquivalent(sourcePath, options.AutoOrient, options.OutputFormat, options.Quality, targetDimensions)
	if err != nil {
		return ProcessedImage{}, err
	}
	if err := processor.publish(ctx, cachePath, encoded); err != nil {
		return ProcessedImage{}, err
	}
	return result, nil
}

func supportsOutput(format Format) bool {
	switch format {
	case FormatJPEG, FormatPNG, FormatWebP, FormatSVG:
		return true
	default:
		return false
	}
}

func shouldPublishOriginal(options ProcessOptions, sourcePath string, source, target Dimensions) bool {
	return !options.AutoOrient &&
		options.Quality >= 90 &&
		source == target &&
		sourcePath == options.Source.Path &&
		sourceFormat(sourcePath) == options.OutputFormat
}

func (processor *Processor) publishSource(ctx context.Context, sourcePath, destination string) (err error) {
	input, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open source image %q: %w", sourcePath, err)
	}
	defer func() {
		err = errors.Join(err, input.Close())
	}()
	return processor.publishFrom(ctx, destination, input)
}

func (processor *Processor) publish(ctx context.Context, destination string, data []byte) error {
	return processor.publishFrom(ctx, destination, bytes.NewReader(data))
}

func (processor *Processor) publishFrom(ctx context.Context, destination string, source io.Reader) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create image cache directory %q: %w", directory, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(directory, ".deadfin-image-*")
	if err != nil {
		return fmt.Errorf("create temporary image cache file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set temporary image cache permissions: %w", err)
	}
	if _, err := io.Copy(temporary, source); err != nil {
		return fmt.Errorf("write temporary image cache file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary image cache file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary image cache file: %w", err)
	}
	temporary = nil
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("publish image cache file: %w", err)
	}
	return nil
}

// convertSource returns a path that libvips can decode natively. The temporary
// conversion is owned by this Process call and therefore remains bounded by the
// processor concurrency slot.
func (processor *Processor) convertSource(ctx context.Context, sourcePath string) (string, func(), error) {
	if sourceFormat(sourcePath) != "" {
		return sourcePath, func() {}, nil
	}
	if processor.sourceConverter == nil {
		return "", nil, fmt.Errorf("%w: %q", ErrUnsupportedSourceFormat, sourcePath)
	}
	directory, err := os.MkdirTemp("", "deadfin-image-")
	if err != nil {
		return "", nil, fmt.Errorf("create image conversion directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	convertedPath := filepath.Join(directory, "converted.png")
	if err := processor.sourceConverter.Convert(ctx, sourcePath, convertedPath); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("convert image %q: %w", sourcePath, err)
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return "", nil, err
	}
	return convertedPath, cleanup, nil
}

func sourceFormat(path string) Format {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".svg":
		return FormatSVG
	case ".jpg", ".jpeg":
		return FormatJPEG
	case ".png":
		return FormatPNG
	case ".webp":
		return FormatWebP
	case ".gif":
		return FormatGIF
	case ".bmp":
		return FormatBMP
	default:
		return ""
	}
}

func mimeType(format Format) string {
	switch format {
	case FormatJPEG:
		return "image/jpeg"
	case FormatPNG:
		return "image/png"
	case FormatWebP:
		return "image/webp"
	case FormatSVG:
		return "image/svg+xml"
	default:
		return ""
	}
}
