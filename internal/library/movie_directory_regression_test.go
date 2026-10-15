// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
)

// TestDefaultMovieResolverResolvesSelfContainedMovieDirectory pins
// MovieResolver.FindMovie's directory branch: a Movies child with exactly one
// resolved video and only media-owned artwork is the movie, not a generic
// folder. The movie retains its media-file Path while taking its name from the
// containing directory.
func TestDefaultMovieResolverResolvesSelfContainedMovieDirectory(t *testing.T) {
	const directory = "/media/movies/Standalone Movie (2023)"
	const media = directory + "/Standalone Movie (2023).mkv"

	parent := item.Item{ID: dto.UUID{0xd7}, Kind: item.KindFolder}
	got, err := library.NewDefaultChain(naming.DefaultOptions()).Resolve(t.Context(), library.ResolveContext{
		Entry:          library.Entry{Path: directory, IsDir: true},
		Parent:         &parent,
		CollectionType: item.CollectionTypeMovies,
		Children: []library.Entry{
			{Path: media, Size: 42},
			{Path: directory + "/movie.nfo", Size: 1},
			{Path: directory + "/Standalone Movie (2023)-poster.jpg", Size: 1},
		},
	})
	if err != nil {
		t.Fatalf("Resolve(self-contained movie directory) error = %v", err)
	}
	if got == nil {
		t.Fatal("Resolve(self-contained movie directory) = nil, want movie")
	}
	if got.Kind != item.KindMovie || got.IsFolder {
		t.Fatalf("Resolve(self-contained movie directory) = %#v, want a non-folder Movie", got)
	}
	if got.Path == nil || *got.Path != media {
		t.Fatalf("resolved movie path = %v, want media path %q", got.Path, media)
	}
	if got.Name == nil || *got.Name != "Standalone Movie (2023)" {
		t.Fatalf("resolved movie name = %v, want containing directory name %q", got.Name, "Standalone Movie (2023)")
	}
	if got.IsInMixedFolder {
		t.Fatalf("resolved movie = %#v, want IsInMixedFolder false", got)
	}
}

// TestDefaultMovieResolverNamesMovieDirectoryWithoutNFO pins FindMovie on the
// natively scanned "Fresh Movies" library. Upstream does not require movie.nfo:
// a directory whose videos form one movie is that movie, named after the
// directory, with the year that the file name carries.
func TestDefaultMovieResolverNamesMovieDirectoryWithoutNFO(t *testing.T) {
	const root = "/media/movies"
	year := func(value int) *int { return &value }
	for _, testCase := range []struct {
		directory string
		files     []string
		year      *int
	}{
		{
			directory: "Apollo 13 (1995)",
			files:     []string{"Apollo.13.1995.2160p.WEB-DL.mkv"},
			year:      year(1995),
		},
		{
			directory: "Battle of Britain (1969)",
			files:     []string{"00000.m2ts"},
		},
		{
			directory: "12 Monkeys (1995)",
			files:     []string{"12.Monkeys.1995.2160p.BluRay.mkv", "12.Monkeys.1995.2160p.BluRay.en.srt"},
			year:      year(1995),
		},
	} {
		t.Run(testCase.directory, func(t *testing.T) {
			directory := root + "/" + testCase.directory
			media := directory + "/" + testCase.files[0]
			children := make([]library.Entry, 0, len(testCase.files))
			for _, name := range testCase.files {
				children = append(children, library.Entry{Path: directory + "/" + name, Size: 42})
			}
			parent := item.Item{ID: dto.UUID{0xd8}, Kind: item.KindFolder}
			got, err := library.NewDefaultChain(naming.DefaultOptions()).Resolve(t.Context(), library.ResolveContext{
				Entry:          library.Entry{Path: directory, IsDir: true},
				Parent:         &parent,
				CollectionType: item.CollectionTypeMovies,
				Children:       children,
				LibraryRoot:    root,
			})
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", directory, err)
			}
			if got == nil || got.Kind != item.KindMovie || got.IsFolder {
				t.Fatalf("Resolve(%q) = %#v, want a non-folder Movie", directory, got)
			}
			if got.Path == nil || *got.Path != media {
				t.Fatalf("movie path = %v, want %q", got.Path, media)
			}
			if got.Name == nil || *got.Name != testCase.directory {
				t.Fatalf("movie name = %v, want directory name %q", got.Name, testCase.directory)
			}
			if (got.ProductionYear == nil) != (testCase.year == nil) || got.ProductionYear != nil && *got.ProductionYear != *testCase.year {
				t.Fatalf("movie year = %v, want %v", got.ProductionYear, testCase.year)
			}
		})
	}
}

// TestDefaultMovieResolverIgnoresSampleVideoInMovieDirectory pins
// MovieResolver.IsIgnoredRegex: ResolveVideos skips a file named with the word
// "sample", so its directory is not a self-contained movie and stays a Folder.
func TestDefaultMovieResolverIgnoresSampleVideoInMovieDirectory(t *testing.T) {
	const directory = "/media/movies/HLG Sample (2024)"
	parent := item.Item{ID: dto.UUID{0xd9}, Kind: item.KindFolder}
	got, err := library.NewDefaultChain(naming.DefaultOptions()).Resolve(t.Context(), library.ResolveContext{
		Entry:          library.Entry{Path: directory, IsDir: true},
		Parent:         &parent,
		CollectionType: item.CollectionTypeMovies,
		Children: []library.Entry{
			{Path: directory + "/HLG Sample (2024).mkv", Size: 42},
			{Path: directory + "/HLG Sample (2024).nfo", Size: 1},
		},
	})
	if err != nil {
		t.Fatalf("Resolve(sample directory) error = %v", err)
	}
	if got == nil || got.Kind != item.KindFolder || !got.IsFolder {
		t.Fatalf("Resolve(sample directory) = %#v, want a generic Folder", got)
	}
}
