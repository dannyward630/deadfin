// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"context"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// testExpander is a minimal VirtualPathExpander for playback tests.
type testExpander struct {
	data     string
	metadata string
}

func (e testExpander) Expand(value string) string {
	if value == "" {
		return ""
	}
	result := value
	result = replaceTestToken(result, "%AppDataPath%", e.data)
	result = replaceTestToken(result, "%MetadataPath%", e.metadata)
	return result
}

func replaceTestToken(input, token, replacement string) string {
	if token == "" || replacement == "" {
		return input
	}
	for i := 0; i <= len(input)-len(token); i++ {
		match := true
		for j := 0; j < len(token); j++ {
			a, b := input[i+j], token[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return input[:i] + replacement + replaceTestToken(input[i+len(token):], token, replacement)
		}
	}
	return input
}

// TestPlaybackVirtualPathSourceExpansion makes sure that PlaybackInfo expands
// virtual path tokens in MediaSourceInfo.Path before it returns the response.
func TestPlaybackVirtualPathSourceExpansion(t *testing.T) {
	ctx := context.Background()
	itemID := dto.UUID{0x30}
	storedPath := "%MetadataPath%/movies/feature.mkv"
	record := MediaSourceRecord{
		ID:        itemID,
		Path:      &storedPath,
		Container: strPtr("mkv"),
	}
	reader := &stubGraphReader{records: []MediaSourceRecord{record}}
	service := NewPlaybackInfoService(PlaybackInfoServiceConfig{
		Sources: reader,
		Paths:   testExpander{data: "/srv/data", metadata: "/srv/metadata"},
	})

	resp, err := service.PlaybackInfo(ctx, PlaybackInfoRequest{ItemID: itemID, AssemblyOnly: true})
	if err != nil {
		t.Fatalf("PlaybackInfo: %v", err)
	}
	if len(resp.MediaSources) != 1 {
		t.Fatalf("media sources = %d, want 1", len(resp.MediaSources))
	}
	got := resp.MediaSources[0].Path
	want := "/srv/metadata/movies/feature.mkv"
	if got != want {
		t.Fatalf("MediaSourceInfo.Path = %q, want %q", got, want)
	}
}

// TestPlaybackVirtualPathStaticSourceExpansion makes sure that StaticSource
// returns expanded paths.
func TestPlaybackVirtualPathStaticSourceExpansion(t *testing.T) {
	ctx := context.Background()
	itemID := dto.UUID{0x31}
	storedPath := "%AppDataPath%/library/feature.mkv"
	record := MediaSourceRecord{
		ID:        itemID,
		Path:      &storedPath,
		Container: strPtr("mkv"),
	}
	reader := &stubGraphReader{records: []MediaSourceRecord{record}}
	service := NewPlaybackInfoService(PlaybackInfoServiceConfig{
		Sources: reader,
		Paths:   testExpander{data: "/srv/data", metadata: "/srv/metadata"},
	})

	source, err := service.StaticSource(ctx, dto.NilUUID, itemID, "")
	if err != nil {
		t.Fatalf("StaticSource: %v", err)
	}
	want := "/srv/data/library/feature.mkv"
	if source.Path != want {
		t.Fatalf("StaticSource path = %q, want %q", source.Path, want)
	}
}

// TestPlaybackVirtualPathNoExpanderUnchanged makes sure that a nil expander
// leaves paths unchanged.
func TestPlaybackVirtualPathNoExpanderUnchanged(t *testing.T) {
	ctx := context.Background()
	itemID := dto.UUID{0x32}
	storedPath := "%MetadataPath%/movies/feature.mkv"
	record := MediaSourceRecord{
		ID:        itemID,
		Path:      &storedPath,
		Container: strPtr("mkv"),
	}
	reader := &stubGraphReader{records: []MediaSourceRecord{record}}
	service := NewPlaybackInfoService(PlaybackInfoServiceConfig{
		Sources: reader,
	})

	resp, err := service.PlaybackInfo(ctx, PlaybackInfoRequest{ItemID: itemID, AssemblyOnly: true})
	if err != nil {
		t.Fatalf("PlaybackInfo: %v", err)
	}
	if len(resp.MediaSources) != 1 {
		t.Fatalf("media sources = %d, want 1", len(resp.MediaSources))
	}
	if resp.MediaSources[0].Path != storedPath {
		t.Fatalf("MediaSourceInfo.Path = %q, want unchanged %q", resp.MediaSources[0].Path, storedPath)
	}
}

type stubGraphReader struct {
	records []MediaSourceRecord
}

func (r *stubGraphReader) MediaSourceRecords(_ context.Context, _ dto.UUID) ([]MediaSourceRecord, error) {
	return r.records, nil
}

func strPtr(s string) *string { return &s }
