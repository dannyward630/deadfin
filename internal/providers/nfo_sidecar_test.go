// SPDX-License-Identifier: GPL-2.0-only

package providers_test

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// Jellyfin's movie-folder convention names the sidecar movie.nfo rather than
// deriving it from the media file name.
func TestNFOProviderDiscoversMovieFolderConvention(t *testing.T) {
	const mediaPath = "/media/films/The Matrix (1999).mkv"
	const sidecarPath = "/media/films/movie.nfo"

	var opened []string
	provider := providers.NewNFOProvider(providers.NFOProviderOptions{
		Open: func(_ context.Context, path string) (io.ReadCloser, error) {
			opened = append(opened, path)
			return io.NopCloser(strings.NewReader(`<movie><title>The Matrix</title><year>1999</year><genre>Science Fiction</genre></movie>`)), nil
		},
	})
	refresh := library.RefreshContext{
		Directory: library.Entry{Path: "/media/films", IsDir: true},
		Entries: []library.Entry{
			{Path: mediaPath},
			{Path: sidecarPath},
		},
	}
	target := item.Item{Kind: item.KindMovie, Path: adapterString(mediaPath), Name: adapterString("resolver title")}

	handled, err := provider.Refresh(t.Context(), refresh, &target)
	if err != nil {
		t.Fatalf("refresh NFO metadata: %v", err)
	}
	if !handled {
		t.Fatal("NFO provider did not claim a movie with its movie.nfo folder sidecar")
	}
	if want := []string{sidecarPath}; !reflect.DeepEqual(opened, want) {
		t.Fatalf("NFO provider opened %v, want %v", opened, want)
	}
	if target.Name == nil || *target.Name != "The Matrix" || target.ProductionYear == nil || *target.ProductionYear != 1999 || !reflect.DeepEqual(target.Values.Genres, []string{"Science Fiction"}) {
		t.Fatalf("NFO merged item = %#v, want movie.nfo title, year, and genre", target)
	}
}

func TestNFOProviderDiscoversDirectoryConventions(t *testing.T) {
	for _, tt := range []struct {
		name        string
		kind        item.Kind
		directory   string
		sidecarName string
		title       string
		document    string
	}{
		{
			name:        "series uses tvshow.nfo",
			kind:        item.KindSeries,
			directory:   "/media/tv/Example Show",
			sidecarName: "tvshow.nfo",
			title:       "Example Show",
			document:    `<tvshow><title>Example Show</title></tvshow>`,
		},
		{
			name:        "season uses season.nfo",
			kind:        item.KindSeason,
			directory:   "/media/tv/Example Show/Season 01",
			sidecarName: "season.nfo",
			title:       "Example Show: Season 1",
			document:    `<season><title>Example Show: Season 1</title></season>`,
		},
		{
			name:        "artist uses artist.nfo",
			kind:        item.KindMusicArtist,
			directory:   "/media/music/Example Artist",
			sidecarName: "artist.nfo",
			title:       "Example Artist",
			document:    `<artist><title>Example Artist</title></artist>`,
		},
		{
			name:        "album uses album.nfo",
			kind:        item.KindMusicAlbum,
			directory:   "/media/music/Example Artist/Example Album",
			sidecarName: "album.nfo",
			title:       "Example Album",
			document:    `<album><title>Example Album</title></album>`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sidecarPath := tt.directory + "/" + tt.sidecarName
			var opened []string
			provider := providers.NewNFOProvider(providers.NFOProviderOptions{
				Open: func(_ context.Context, path string) (io.ReadCloser, error) {
					opened = append(opened, path)
					return io.NopCloser(strings.NewReader(tt.document)), nil
				},
			})
			refresh := library.RefreshContext{
				Directory: library.Entry{Path: tt.directory, IsDir: true},
				Entries:   []library.Entry{{Path: sidecarPath}},
			}
			target := item.Item{
				Kind:     tt.kind,
				Path:     adapterString(tt.directory),
				Name:     adapterString("resolver title"),
				IsFolder: true,
			}

			handled, err := provider.Refresh(t.Context(), refresh, &target)
			if err != nil {
				t.Fatalf("refresh NFO metadata: %v", err)
			}
			if !handled {
				t.Fatalf("NFO provider did not claim directory %q with %q", tt.directory, tt.sidecarName)
			}
			if want := []string{sidecarPath}; !reflect.DeepEqual(opened, want) {
				t.Fatalf("NFO provider opened %v, want %v", opened, want)
			}
			if target.Name == nil || *target.Name != tt.title {
				t.Fatalf("NFO merged title = %v, want %q", target.Name, tt.title)
			}
		})
	}
}

func TestNFOProviderPrioritizesMovieSidecars(t *testing.T) {
	const (
		directoryPath = "/media/films"
		mediaPath     = directoryPath + "/The Matrix (1999).mkv"
		movieNFOPath  = directoryPath + "/movie.nfo"
		stemNFOPath   = directoryPath + "/The Matrix (1999).nfo"
	)

	for _, tt := range []struct {
		name          string
		isMixedFolder bool
		wantOpen      string
		wantTitle     string
	}{
		{
			name:      "non-mixed movies prefer movie.nfo",
			wantOpen:  movieNFOPath,
			wantTitle: "Folder-sidecar title",
		},
		{
			name:          "mixed movies use stem-derived sidecar",
			isMixedFolder: true,
			wantOpen:      stemNFOPath,
			wantTitle:     "Stem-sidecar title",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var opened []string
			provider := providers.NewNFOProvider(providers.NFOProviderOptions{
				Open: func(_ context.Context, path string) (io.ReadCloser, error) {
					opened = append(opened, path)
					switch path {
					case movieNFOPath:
						return io.NopCloser(strings.NewReader(`<movie><title>Folder-sidecar title</title></movie>`)), nil
					case stemNFOPath:
						return io.NopCloser(strings.NewReader(`<movie><title>Stem-sidecar title</title></movie>`)), nil
					default:
						t.Fatalf("NFO provider opened unexpected path %q", path)
						return nil, nil
					}
				},
			})
			refresh := library.RefreshContext{
				Directory: library.Entry{Path: directoryPath, IsDir: true},
				Entries: []library.Entry{
					{Path: mediaPath},
					{Path: movieNFOPath},
					{Path: stemNFOPath},
				},
			}
			target := item.Item{
				Kind:            item.KindMovie,
				Path:            adapterString(mediaPath),
				IsInMixedFolder: tt.isMixedFolder,
			}

			handled, err := provider.Refresh(t.Context(), refresh, &target)
			if err != nil {
				t.Fatalf("refresh NFO metadata: %v", err)
			}
			if !handled {
				t.Fatal("NFO provider did not claim movie with both movie.nfo and stem sidecars")
			}
			if want := []string{tt.wantOpen}; !reflect.DeepEqual(opened, want) {
				t.Fatalf("NFO provider opened %v, want exactly %v", opened, want)
			}
			if target.Name == nil || *target.Name != tt.wantTitle {
				t.Fatalf("NFO merged title = %v, want %q", target.Name, tt.wantTitle)
			}
		})
	}
}
