// SPDX-License-Identifier: GPL-2.0-only

package imageserve_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/imageserve"
	"github.com/deadfin-org/deadfin/internal/item"
)

type fakeProcessor struct {
	got    images.ProcessOptions
	result images.ProcessedImage
	err    error
}

func (f *fakeProcessor) Process(_ context.Context, options images.ProcessOptions) (images.ProcessedImage, error) {
	f.got = options
	if f.err != nil {
		return images.ProcessedImage{}, f.err
	}
	return f.result, nil
}

type fakeRemote struct {
	path     string
	modified time.Time
	err      error
	calls    int
	lastURL  string
}

func (f *fakeRemote) Local(_ context.Context, url string) (string, time.Time, error) {
	f.calls++
	f.lastURL = url
	return f.path, f.modified, f.err
}

func modifiedAt(t *testing.T) *time.Time {
	t.Helper()
	when := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	return &when
}

func TestSelectMatchesTypeAndIndex(t *testing.T) {
	infos := []item.ImageInfo{
		{Type: item.ImageTypePrimary, Index: 0, Path: "primary.jpg"},
		{Type: item.ImageTypeBackdrop, Index: 0, Path: "backdrop-0.jpg"},
		{Type: item.ImageTypeBackdrop, Index: 1, Path: "backdrop-1.jpg"},
	}

	got, err := imageserve.Select(infos, item.ImageTypeBackdrop, 1)
	if err != nil {
		t.Fatalf("choose backdrop index 1: %v", err)
	}
	if got.Path != "backdrop-1.jpg" {
		t.Errorf("selected path = %q, want backdrop-1.jpg", got.Path)
	}

	if _, err := imageserve.Select(infos, item.ImageTypeLogo, 0); !errors.Is(err, imageserve.ErrImageNotFound) {
		t.Errorf("choose missing type error = %v, want ErrImageNotFound", err)
	}
	if _, err := imageserve.Select(infos, item.ImageTypeBackdrop, 2); !errors.Is(err, imageserve.ErrImageNotFound) {
		t.Errorf("choose missing index error = %v, want ErrImageNotFound", err)
	}
}

func TestTagIsSourceBaseName(t *testing.T) {
	if got := imageserve.Tag("/media/movie/poster.jpg"); got != "poster.jpg" {
		t.Errorf("Tag(local) = %q, want poster.jpg", got)
	}
	if got := imageserve.Tag("https://image.tmdb.org/t/p/original/abc123.jpg"); got != "abc123.jpg" {
		t.Errorf("Tag(remote) = %q, want abc123.jpg", got)
	}
}

func TestRenderLocalDrivesProcessorWithSourceIdentity(t *testing.T) {
	processor := &fakeProcessor{result: images.ProcessedImage{Path: "/cache/out.png", MIMEType: "image/png", Dimensions: images.Dimensions{Width: 320, Height: 180}}}
	service := imageserve.New(processor, nil)

	pngFormat := images.FormatPNG
	width := 320
	modified := modifiedAt(t)
	info := item.ImageInfo{Type: item.ImageTypePrimary, Path: "/media/movie/poster.jpg", DateModified: modified}

	result, err := service.Render(context.Background(), info, imageserve.Request{
		Format:  &pngFormat,
		Quality: 90,
		Resize:  images.ResizeOptions{Width: &width},
	})
	if err != nil {
		t.Fatalf("Render local: %v", err)
	}

	if processor.got.Source.Path != "/media/movie/poster.jpg" {
		t.Errorf("processor source path = %q, want the local file path", processor.got.Source.Path)
	}
	if !processor.got.Source.Modified.Equal(*modified) {
		t.Errorf("processor modified = %v, want %v", processor.got.Source.Modified, *modified)
	}
	if !processor.got.AutoOrient {
		// The service passes the caller's AutoOrient through unchanged.
		t.Log("AutoOrient false (request did not set it)")
	}
	if processor.got.OutputFormat != images.FormatPNG {
		t.Errorf("output format = %q, want Png (explicit format honored over jpeg source)", processor.got.OutputFormat)
	}
	if result.ContentType != "image/png" || result.Tag != "poster.jpg" {
		t.Errorf("result = %+v, want content-type image/png and tag poster.jpg", result)
	}
}

func TestRenderRemoteDownloadsAndTagsOriginalURL(t *testing.T) {
	processor := &fakeProcessor{result: images.ProcessedImage{Path: "/cache/out.jpg", MIMEType: "image/jpeg"}}
	remoteModified := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	remote := &fakeRemote{path: "/var/cache/deadfin/remote/a/abc123.jpg", modified: remoteModified}
	service := imageserve.New(processor, remote)

	const url = "https://image.tmdb.org/t/p/original/abc123.jpg"
	info := item.ImageInfo{Type: item.ImageTypePrimary, Path: url}

	result, err := service.Render(context.Background(), info, imageserve.Request{Quality: 90})
	if err != nil {
		t.Fatalf("Render remote: %v", err)
	}
	if remote.calls != 1 || remote.lastURL != url {
		t.Errorf("remote fetch calls = %d url = %q, want one call for %q", remote.calls, remote.lastURL, url)
	}
	if processor.got.Source.Path != remote.path {
		t.Errorf("processor source = %q, want downloaded local copy %q", processor.got.Source.Path, remote.path)
	}
	if !processor.got.Source.Modified.Equal(remoteModified) {
		t.Errorf("processor modified = %v, want downloaded file mtime %v", processor.got.Source.Modified, remoteModified)
	}
	if result.Tag != "abc123.jpg" {
		t.Errorf("tag = %q, want the original URL base name abc123.jpg", result.Tag)
	}
}

func TestRenderRemoteHonorsStoredDateModified(t *testing.T) {
	processor := &fakeProcessor{result: images.ProcessedImage{Path: "/cache/out.jpg", MIMEType: "image/jpeg"}}
	remote := &fakeRemote{path: "/cache/remote/x.jpg", modified: time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)}
	service := imageserve.New(processor, remote)

	stored := modifiedAt(t)
	info := item.ImageInfo{Type: item.ImageTypePrimary, Path: "https://cdn.example/x.jpg", DateModified: stored}
	if _, err := service.Render(context.Background(), info, imageserve.Request{Quality: 90}); err != nil {
		t.Fatalf("Render remote with stored modified: %v", err)
	}
	if !processor.got.Source.Modified.Equal(*stored) {
		t.Errorf("processor modified = %v, want stored DateModified %v", processor.got.Source.Modified, *stored)
	}
}

func TestRenderRemoteWithoutStoreFails(t *testing.T) {
	service := imageserve.New(&fakeProcessor{}, nil)
	info := item.ImageInfo{Type: item.ImageTypePrimary, Path: "https://cdn.example/x.jpg"}
	if _, err := service.Render(context.Background(), info, imageserve.Request{}); !errors.Is(err, imageserve.ErrRemoteUnavailable) {
		t.Errorf("Render remote without store = %v, want ErrRemoteUnavailable", err)
	}
}

func TestRenderWithoutProcessorFails(t *testing.T) {
	service := imageserve.New(nil, nil)
	info := item.ImageInfo{Type: item.ImageTypePrimary, Path: "poster.jpg"}
	if _, err := service.Render(context.Background(), info, imageserve.Request{}); !errors.Is(err, imageserve.ErrProcessorUnavailable) {
		t.Errorf("Render without processor = %v, want ErrProcessorUnavailable", err)
	}
}

func TestRenderNegotiatesFormatFromClientList(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		formats []images.Format
		want    images.Format
	}{
		{name: "webp preferred", source: "poster.jpg", formats: []images.Format{images.FormatWebP, images.FormatJPEG}, want: images.FormatWebP},
		{name: "transparent source keeps png", source: "logo.png", formats: []images.Format{images.FormatJPEG, images.FormatPNG}, want: images.FormatPNG},
		{name: "opaque falls to first supported", source: "poster.jpg", formats: []images.Format{images.FormatJPEG}, want: images.FormatJPEG},
		{name: "empty falls back to jpeg", source: "poster.jpg", formats: nil, want: images.FormatJPEG},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			processor := &fakeProcessor{result: images.ProcessedImage{Path: "/c/out", MIMEType: "x"}}
			service := imageserve.New(processor, nil)
			info := item.ImageInfo{Type: item.ImageTypePrimary, Path: test.source}
			if _, err := service.Render(context.Background(), info, imageserve.Request{ClientFormats: test.formats}); err != nil {
				t.Fatalf("Render: %v", err)
			}
			if processor.got.OutputFormat != test.want {
				t.Errorf("output format = %q, want %q", processor.got.OutputFormat, test.want)
			}
		})
	}
}
