// SPDX-License-Identifier: GPL-2.0-only

package local_test

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/metadata/local"
)

func TestDiscoverImagesUsesKindPrimaryPrecedenceAndMixedFolderPrefix(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		context local.ImageContext
		files   []string
		want    string
	}{
		{
			name: "movie poster beats generic folder cover",
			context: local.ImageContext{
				Directory: "/movies/Arrival", ItemPath: "/movies/Arrival/Arrival.mkv", Name: "Arrival", Kind: item.KindMovie,
			},
			files: []string{"folder.png", "cover.webp", "poster.jpg"},
			want:  "/movies/Arrival/poster.jpg",
		},
		{
			name: "music album folder beats cover",
			context: local.ImageContext{
				Directory: "/music/Kind of Blue", ItemPath: "/music/Kind of Blue/01.flac", Name: "Kind of Blue", Kind: item.KindMusicAlbum,
			},
			files: []string{"cover.png", "folder.jpg"},
			want:  "/music/Kind of Blue/folder.jpg",
		},
		{
			name: "mixed folder uses item prefixed poster",
			context: local.ImageContext{
				Directory: "/mixed", ItemPath: "/mixed/Arrival.mkv", Name: "Arrival", Kind: item.KindMovie, IsMixedFolder: true,
			},
			files: []string{"poster.png", "Arrival-poster.webp"},
			want:  "/mixed/Arrival-poster.webp",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := imageFixture{}
			fixture.addFiles(testCase.context.Directory, testCase.files...)
			got, err := local.DiscoverImages(t.Context(), fixture.context(testCase.context))
			if err != nil {
				t.Fatalf("discover images: %v", err)
			}
			want := []item.ImageInfo{fixture.image(item.ImageTypePrimary, 0, testCase.want)}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("discovered images = %#v, want %#v", got, want)
			}
		})
	}
}

func TestDiscoverImagesPrefersExactBasenamePrimaryOverConventions(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		context local.ImageContext
		files   []string
		want    string
	}{
		{
			name: "movie exact basename beats extension-ranked poster",
			context: local.ImageContext{
				Directory: "/movies/Arrival", ItemPath: "/movies/Arrival/Arrival.mkv", Name: "Arrival", Kind: item.KindMovie,
			},
			files: []string{"Arrival.png", "poster.jpg"},
			want:  "/movies/Arrival/Arrival.png",
		},
		{
			name: "mixed folder exact basename beats prefixed poster",
			context: local.ImageContext{
				Directory: "/mixed", ItemPath: "/mixed/Arrival.mkv", Name: "Arrival", Kind: item.KindMovie, IsMixedFolder: true,
			},
			files: []string{"Arrival.jpg", "Arrival-poster.png"},
			want:  "/mixed/Arrival.jpg",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := imageFixture{}
			fixture.addFiles(testCase.context.Directory, testCase.files...)
			got, err := local.DiscoverImages(t.Context(), fixture.context(testCase.context))
			if err != nil {
				t.Fatalf("discover images: %v", err)
			}
			want := []item.ImageInfo{fixture.image(item.ImageTypePrimary, 0, testCase.want)}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("exact basename artwork = %#v, want %#v", got, want)
			}
		})
	}
}

func TestDiscoverImagesDiscoversClearAndEpisodeArtwork(t *testing.T) {
	t.Run("clear artwork names", func(t *testing.T) {
		fixture := imageFixture{}
		fixture.addFiles("/movie", "clearlogo.png", "clearart.webp", "discart.jpg", "banner.gif", "landscape.jpeg")
		got, err := local.DiscoverImages(t.Context(), fixture.context(local.ImageContext{
			Directory: "/movie", ItemPath: "/movie/Movie.mkv", Name: "Movie", Kind: item.KindMovie,
		}))
		if err != nil {
			t.Fatalf("discover clear artwork: %v", err)
		}
		want := []item.ImageInfo{
			fixture.image(item.ImageTypeArt, 0, "/movie/clearart.webp"),
			fixture.image(item.ImageTypeBanner, 0, "/movie/banner.gif"),
			fixture.image(item.ImageTypeLogo, 0, "/movie/clearlogo.png"),
			fixture.image(item.ImageTypeThumb, 0, "/movie/landscape.jpeg"),
			fixture.image(item.ImageTypeDisc, 0, "/movie/discart.jpg"),
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("clear artwork = %#v, want %#v", got, want)
		}
	})

	t.Run("episode metadata directory uses basename and thumb", func(t *testing.T) {
		fixture := imageFixture{}
		fixture.addFiles("/metadata/Show", "S01E01.jpg", "S01E01-thumb.png")
		got, err := local.DiscoverImages(t.Context(), fixture.context(local.ImageContext{
			Directory: "/tv/Show/Season 1", ItemPath: "/tv/Show/Season 1/S01E01.mkv", MetadataDirectory: "/metadata/Show", Name: "Episode One", Kind: item.KindEpisode,
		}))
		if err != nil {
			t.Fatalf("discover episode artwork: %v", err)
		}
		want := []item.ImageInfo{
			fixture.image(item.ImageTypePrimary, 0, "/metadata/Show/S01E01.jpg"),
			fixture.image(item.ImageTypeThumb, 0, "/metadata/Show/S01E01-thumb.png"),
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("episode artwork = %#v, want %#v", got, want)
		}
	})
}

func TestDiscoverImagesDiscoversSeasonParentNames(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		seasonName string
		number     int
		file       string
	}{
		{name: "numbered season", seasonName: "Season 01", file: "season01-poster.jpg"},
		{name: "specials season", seasonName: "Season 00", file: "season-specials-poster.png"},
		{name: "upstream name, two-digit marker", seasonName: "Season 1", number: 1, file: "season01-poster.jpg"},
		{name: "nfo title, two-digit marker", seasonName: "Show: Season One", number: 1, file: "season01-poster.jpg"},
		{name: "specials name, specials marker", seasonName: "Specials", number: 0, file: "season-specials-poster.jpg"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := imageFixture{}
			fixture.addFiles("/tv/Show", testCase.file)
			number := testCase.number
			got, err := local.DiscoverImages(t.Context(), fixture.context(local.ImageContext{
				Directory: "/tv/Show/" + testCase.seasonName, ParentDirectory: "/tv/Show", ParentName: "Show", Name: testCase.seasonName, Kind: item.KindSeason,
				SeasonNumber: &number,
			}))
			if err != nil {
				t.Fatalf("discover season artwork: %v", err)
			}
			want := []item.ImageInfo{fixture.image(item.ImageTypePrimary, 0, filepath.Join("/tv/Show", testCase.file))}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("season artwork = %#v, want %#v", got, want)
			}
		})
	}
}

func TestDiscoverImagesUsesSequentialBackdropSearchAndExtraFanart(t *testing.T) {
	fixture := imageFixture{}
	fixture.addFiles("/movie", "fanart.jpg", "fanart-2.png", "fanart-4.webp", "fanart-10.jpg")
	fixture.addFiles("/movie/extrafanart", "extra-a.jpg", "extra-b.png")
	got, err := local.DiscoverImages(t.Context(), fixture.context(local.ImageContext{
		Directory: "/movie", ItemPath: "/movie/Movie.mkv", Name: "Movie", Kind: item.KindMovie,
	}))
	if err != nil {
		t.Fatalf("discover backdrop sequence: %v", err)
	}
	want := []item.ImageInfo{
		fixture.image(item.ImageTypeBackdrop, 0, "/movie/fanart.jpg"),
		fixture.image(item.ImageTypeBackdrop, 1, "/movie/fanart-2.png"),
		fixture.image(item.ImageTypeBackdrop, 2, "/movie/fanart-4.webp"),
		fixture.image(item.ImageTypeBackdrop, 3, "/movie/extrafanart/extra-a.jpg"),
		fixture.image(item.ImageTypeBackdrop, 4, "/movie/extrafanart/extra-b.png"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backdrop sequence = %#v, want %#v", got, want)
	}
	for _, image := range got {
		if image.Path == "/movie/fanart-10.jpg" {
			t.Fatal("backdrop scan continued after three consecutive numbered misses")
		}
	}
}

func TestDiscoverImagesDiscoversExtraFanartIndependentlyOfNamedBackdrops(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		rootFiles  []string
		extraFiles []string
		missingDir bool
		want       []string
	}{
		{
			name:       "extrafanart-only folder consumes every image in stable order",
			extraFiles: []string{"zeta.webp", "alpha.png", "bravo.jpg"},
			want: []string{
				"/movie/extrafanart/alpha.png",
				"/movie/extrafanart/bravo.jpg",
				"/movie/extrafanart/zeta.webp",
			},
		},
		{
			name:       "one named fanart retains one extrafanart image",
			rootFiles:  []string{"fanart.jpg"},
			extraFiles: []string{"extra.jpg"},
			want:       []string{"/movie/fanart.jpg", "/movie/extrafanart/extra.jpg"},
		},
		{
			name:       "one named fanart retains all extrafanart images in stable order",
			rootFiles:  []string{"fanart.jpg"},
			extraFiles: []string{"zeta.webp", "alpha.png", "bravo.jpg"},
			want: []string{
				"/movie/fanart.jpg",
				"/movie/extrafanart/alpha.png",
				"/movie/extrafanart/bravo.jpg",
				"/movie/extrafanart/zeta.webp",
			},
		},
		{
			name:       "missing extrafanart folder leaves named fanart intact",
			rootFiles:  []string{"fanart.jpg"},
			missingDir: true,
			want:       []string{"/movie/fanart.jpg"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := imageFixture{}
			fixture.addFiles("/movie", testCase.rootFiles...)
			fixture.addFiles("/movie/extrafanart", testCase.extraFiles...)
			imageContext := fixture.context(local.ImageContext{
				Directory: "/movie", ItemPath: "/movie/Movie.mkv", Name: "Movie", Kind: item.KindMovie,
			})
			if testCase.missingDir {
				readDir := imageContext.ReadDir
				imageContext.ReadDir = func(ctx context.Context, directory string) ([]local.ImageEntry, error) {
					if directory == "/movie/extrafanart" {
						return nil, fs.ErrNotExist
					}
					return readDir(ctx, directory)
				}
			}

			got, err := local.DiscoverImages(t.Context(), imageContext)
			if err != nil {
				t.Fatalf("discover local extrafanart: %v", err)
			}
			want := make([]item.ImageInfo, 0, len(testCase.want))
			for index, path := range testCase.want {
				want = append(want, fixture.image(item.ImageTypeBackdrop, index, path))
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("extra fanart backdrops = %#v, want %#v", got, want)
			}
		})
	}
}

func TestDiscoverImagesStopsForCanceledContextAndSurfacesEntryErrors(t *testing.T) {
	t.Run("already canceled", func(t *testing.T) {
		fixture := imageFixture{}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := local.DiscoverImages(ctx, fixture.context(local.ImageContext{Directory: "/movie", Kind: item.KindMovie}))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled discovery error = %v, want context.Canceled", err)
		}
		if fixture.reads != 0 {
			t.Fatalf("canceled discovery performed %d directory reads", fixture.reads)
		}
	})

	t.Run("stat failure", func(t *testing.T) {
		fixture := imageFixture{}
		fixture.addFiles("/movie", "poster.png")
		boom := errors.New("stat denied")
		fixture.statErrs = map[string]error{"/movie/poster.png": boom}
		_, err := local.DiscoverImages(t.Context(), fixture.context(local.ImageContext{Directory: "/movie", Kind: item.KindMovie}))
		if !errors.Is(err, boom) {
			t.Fatalf("stat failure = %v, want wrapped %v", err, boom)
		}
	})

	t.Run("directory failure", func(t *testing.T) {
		boom := errors.New("directory denied")
		context := local.ImageContext{
			Directory: "/movie", Kind: item.KindMovie,
			ReadDir: func(context.Context, string) ([]local.ImageEntry, error) { return nil, boom },
			Stat:    func(context.Context, string) (local.ImageStat, error) { return local.ImageStat{}, fs.ErrNotExist },
		}
		_, err := local.DiscoverImages(t.Context(), context)
		if !errors.Is(err, boom) {
			t.Fatalf("directory failure = %v, want wrapped %v", err, boom)
		}
	})
}

func TestDiscoverImagesUsesDeclaredExtensionOrderAndBackdropCap(t *testing.T) {
	t.Run("extension order", func(t *testing.T) {
		fixture := imageFixture{}
		fixture.addFiles("/movie", "poster.svg", "poster.gif", "poster.tbn", "poster.webp", "poster.jpeg", "poster.jpg", "poster.png")
		got, err := local.DiscoverImages(t.Context(), fixture.context(local.ImageContext{Directory: "/movie", ItemPath: "/movie/Movie.mkv", Name: "Movie", Kind: item.KindMovie}))
		if err != nil {
			t.Fatalf("discover extension preference: %v", err)
		}
		want := []item.ImageInfo{fixture.image(item.ImageTypePrimary, 0, "/movie/poster.png")}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("extension preference = %#v, want %#v", got, want)
		}
	})

	t.Run("twenty backdrops maximum", func(t *testing.T) {
		fixture := imageFixture{}
		names := []string{"fanart.jpg"}
		for index := 1; index <= 20; index++ {
			names = append(names, "fanart-"+strconv.Itoa(index)+".jpg")
		}
		fixture.addFiles("/movie", names...)
		got, err := local.DiscoverImages(t.Context(), fixture.context(local.ImageContext{Directory: "/movie", ItemPath: "/movie/Movie.mkv", Name: "Movie", Kind: item.KindMovie}))
		if err != nil {
			t.Fatalf("discover backdrop cap: %v", err)
		}
		backdrops := make([]item.ImageInfo, 0, 20)
		for _, image := range got {
			if image.Type == item.ImageTypeBackdrop {
				backdrops = append(backdrops, image)
			}
		}
		if len(backdrops) != 20 {
			t.Fatalf("backdrop count = %d, want cap 20", len(backdrops))
		}
		for _, image := range backdrops {
			if image.Path == "/movie/fanart-20.jpg" {
				t.Fatal("backdrop sequence exceeded the twenty-image cap")
			}
		}
	})
}
