// SPDX-License-Identifier: GPL-2.0-only

package dtobuild

import (
	"context"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/path"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestBuilderVirtualPathExpansion makes sure that BaseItemDto.Path receives the
// expanded value when the builder is constructed with a configured expander.
func TestBuilderVirtualPathExpansion(t *testing.T) {
	ctx := context.Background()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)
	expander := path.NewVirtualPathExpander("/srv/data", "/srv/metadata")

	for _, tc := range []struct {
		name, stored, want string
	}{
		{name: "metadata path", stored: "%MetadataPath%/Studio/Case Studio", want: "/srv/metadata/Studio/Case Studio"},
		{name: "appdata path", stored: "%AppDataPath%/library/poster.jpg", want: "/srv/data/library/poster.jpg"},
		{name: "case insensitive", stored: "%METADATAPATH%/People/G/Generator", want: "/srv/metadata/People/G/Generator"},
		{name: "both tokens", stored: "%AppDataPath%/ref/%MetadataPath%/meta", want: "/srv/data/ref//srv/metadata/meta"},
		{name: "no token unchanged", stored: "/media/movies/feature.mkv", want: "/media/movies/feature.mkv"},
		{name: "literal percent unchanged", stored: "/metadata/Caf%C3%A9%20A", want: "/metadata/Caf%C3%A9%20A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := dto.UUID{byte(len(tc.name))}
			n := tc.name
			storedPath := tc.stored
			if err := items.Upsert(ctx, item.Item{ID: id, Kind: item.KindMovie, Name: &n, Path: &storedPath}); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			got, err := items.Get(ctx, id)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			builder := NewBuilder(items, NewStoreEnricher(items, nil, nil), "")
			builder.WithPaths(expander)
			result, err := builder.Build(ctx, []item.Item{got}, store.ItemQueryDTOOptions{Fields: []string{"Path"}}, nil)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if len(result.Items) != 1 {
				t.Fatalf("result items = %d, want 1", len(result.Items))
			}
			if result.Items[0].Path != tc.want {
				t.Fatalf("Path = %q, want %q", result.Items[0].Path, tc.want)
			}
		})
	}
}

// TestStoreEnricherVirtualPathMediaSources makes sure that
// dto.MediaSourceInfo.Path and dto.MediaStreamInfo.Path receive expanded
// values. The test seeds a media-stream path with a raw token and asserts the
// exact configured path in both the source and stream collections.
func TestStoreEnricherVirtualPathMediaSources(t *testing.T) {
	ctx := context.Background()
	stores, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)
	expander := path.NewVirtualPathExpander("/srv/data", "/srv/metadata")

	id := dto.UUID{0x10}
	name := "virtual path movie"
	storedPath := "%MetadataPath%/movies/feature.mkv"
	if err := items.Upsert(ctx, item.Item{
		ID: id, Kind: item.KindMovie, Name: &name, Path: &storedPath,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Seed a media stream with a raw token path so the stream projection
	// exercises the expander.
	streamPath := "%MetadataPath%/subtitles/en.srt"
	if err := items.ReplaceStreams(ctx, id, []playback.MediaStream{
		{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
		{Index: 1, Type: playback.StreamTypeSubtitle, Codec: "srt", Path: streamPath, IsExternal: true},
	}); err != nil {
		t.Fatalf("replace streams: %v", err)
	}

	got, err := items.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	builder := NewBuilder(items, NewStoreEnricher(items, nil, nil), "")
	builder.WithPaths(expander)
	result, err := builder.Build(ctx, []item.Item{got}, store.ItemQueryDTOOptions{Fields: []string{"MediaSources", "MediaStreams"}}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("result items = %d, want 1", len(result.Items))
	}

	// MediaSources must be non-empty with an exact expanded source path.
	if len(result.Items[0].MediaSources) == 0 {
		t.Fatal("MediaSources is empty, want at least one source")
	}
	wantSourcePath := "/srv/metadata/movies/feature.mkv"
	gotSourcePath := ""
	if result.Items[0].MediaSources[0].Path != nil {
		gotSourcePath = *result.Items[0].MediaSources[0].Path
	}
	if gotSourcePath != wantSourcePath {
		t.Fatalf("MediaSourceInfo.Path = %q, want %q", gotSourcePath, wantSourcePath)
	}
	// The source's embedded streams must also have expanded paths.
	if len(result.Items[0].MediaSources[0].MediaStreams) == 0 {
		t.Fatal("MediaSourceInfo.MediaStreams is empty, want at least one stream")
	}
	for _, stream := range result.Items[0].MediaSources[0].MediaStreams {
		if stream.Path == nil {
			continue
		}
		p := *stream.Path
		if p == "" {
			continue
		}
		if containsToken(p, "%MetadataPath%") || containsToken(p, "%AppDataPath%") {
			t.Errorf("source MediaStreamInfo.Path = %q, contains raw token", p)
		}
		// The external subtitle stream must carry the exact expanded path.
		if stream.IsExternal != nil && *stream.IsExternal {
			wantStreamPath := "/srv/metadata/subtitles/en.srt"
			if p != wantStreamPath {
				t.Errorf("external stream Path = %q, want %q", p, wantStreamPath)
			}
		}
	}

	// Top-level MediaStreams must be non-empty with expanded paths.
	if len(result.Items[0].MediaStreams) == 0 {
		t.Fatal("MediaStreams is empty, want at least one stream")
	}
	for _, stream := range result.Items[0].MediaStreams {
		if stream.Path == nil {
			continue
		}
		p := *stream.Path
		if p == "" {
			continue
		}
		if containsToken(p, "%MetadataPath%") || containsToken(p, "%AppDataPath%") {
			t.Errorf("MediaStreamInfo.Path = %q, contains raw token", p)
		}
	}
}

func ptrMediaType(mt item.MediaType) *item.MediaType { return &mt }

func containsToken(s, token string) bool {
	return len(s) >= len(token) && (caseInsensitiveContains(s, token))
}

func caseInsensitiveContains(s, substr string) bool {
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a, b := s[i+j], substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// Reference the playback package to keep the import in use.
var _ = playback.MediaProtocolFile
