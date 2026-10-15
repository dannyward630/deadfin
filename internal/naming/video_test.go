// SPDX-License-Identifier: GPL-2.0-only

// Ported from upstream/jellyfin/tests/Jellyfin.Naming.Tests/Video/*.cs.
// Each table retains the pinned upstream filename and expected observable result.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func intp(value int) *int { return &value }

// Jellyfin.Naming.Tests.Common.NamingOptionsTest.TestNamingOptionsCompile.
func TestDefaultOptionsPopulateCleaningRules(t *testing.T) {
	opts := naming.DefaultOptions()
	if len(opts.CleanDateTimeRegexes) == 0 || len(opts.CleanStringRegexes) == 0 {
		t.Fatal("default naming options must include clean-date and clean-string rules")
	}
}

// Jellyfin.Naming.Tests.Video.CleanDateTimeTests.CleanDateTimeTest.
func TestCleanDateTime(t *testing.T) {
	tests := []struct {
		input string
		name  string
		year  *int
	}{
		{"The Wolf of Wall Street (2013).mkv", "The Wolf of Wall Street", intp(2013)},
		{"The Wolf of Wall Street 2 (2013).mkv", "The Wolf of Wall Street 2", intp(2013)},
		{"300 (2006).mkv", "300", intp(2006)},
		{"curse.of.chucky.2013.stv.unrated.multi.2160p.bluray.x264-rough", "curse.of.chucky", intp(2013)},
		{"/server/Movies/300 (2007)/300 (2006).bluray.disc", "300", intp(2006)},
		{"Arrival.2016.2160p.Blu-Ray.HEVC.mkv", "Arrival", intp(2016)},
		{"American.Psycho.mkv", "American.Psycho.mkv", nil},
		{"[rec].mkv", "[rec].mkv", nil},
		{"St. Vincent (2014)", "St. Vincent", intp(2014)},
		{"Super movie(2009).mp4", "Super movie", intp(2009)},
		{"My Movie (1997) - GreatestReleaseGroup 2019.mp4", "My Movie", intp(1997)},
		{"Maximum Ride - 2016 - WEBDL-1080p - x264 AC3.mkv", "Maximum Ride", intp(2016)},
		{"3.Days.to.Kill.2014.720p.BluRay.x264.YIFY.mkv", "3.Days.to.Kill", intp(2014)},
		{"My Movie 2013.12.09", "My Movie 2013.12.09", nil},
		{"My Movie 2013-12-09 2013", "My Movie 2013-12-09", intp(2013)},
		{"", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := naming.CleanDateTime(tt.input, naming.DefaultOptions())
			if got.Name != tt.name || !reflect.DeepEqual(got.Year, tt.year) {
				t.Fatalf("CleanDateTime(%q) = (%q, %v), want (%q, %v)", tt.input, got.Name, got.Year, tt.name, tt.year)
			}
		})
	}
}

// Jellyfin.Naming.Tests.Video.CleanStringTests.CleanStringTest_NeedsCleaning_Success
// and CleanStringTest_DoesntNeedCleaning_False.
func TestCleanString(t *testing.T) {
	changes := map[string]string{
		"Super movie 480p.mp4":                                          "Super movie",
		"Super movie Multi.mp4":                                         "Super movie",
		"Super movie 480p 2001.mp4":                                     "Super movie",
		"480 Super movie [tmdbid=12345].mp4":                            "480 Super movie",
		"Crouching.Tiger.Hidden.Dragon.4K.UltraHD.HDR.BDrip-HDC.mkv":    "Crouching.Tiger.Hidden.Dragon",
		"[HorribleSubs] Made in Abyss - 13 [720p].mkv":                  "Made in Abyss",
		"[Erai-raws] Jujutsu Kaisen - 03 [720p][Multiple Subtitle].mkv": "Jujutsu Kaisen",
		"[tvN] 연애말고 결혼 E01~E16 END HDTV.H264.720p-WITH":                 "연애말고 결혼",
	}
	for input, want := range changes {
		t.Run(input, func(t *testing.T) {
			got, changed := naming.CleanString(input, naming.DefaultOptions())
			if !changed || got != want {
				t.Fatalf("CleanString(%q) = (%q, %t), want (%q, true)", input, got, changed, want)
			}
		})
	}
	for _, input := range []string{"", "Super movie(2009).mp4", "[rec].mkv", "American.Psycho.mkv", "Run lola run (lola rennt) (2009).mp4", "2026年01月05日00時55分00秒-[新]違国日記【ＡＮｉＭｉＤＮｉＧＨＴ！！！】＃１.mp4"} {
		t.Run("unchanged/"+input, func(t *testing.T) {
			got, changed := naming.CleanString(input, naming.DefaultOptions())
			if changed || got != "" {
				t.Fatalf("CleanString(%q) = (%q, %t), want (empty, false)", input, got, changed)
			}
		})
	}
}

// Jellyfin.Naming.Tests.Video.Format3DTests.TestExpandedFormat3D.
func TestParse3D(t *testing.T) {
	tests := []struct {
		input  string
		is3D   bool
		format string
	}{
		{"Super movie.3d.mp4", false, ""},
		{"Super movie.3d.hsbs.mp4", true, "hsbs"},
		{"Super movie.3d.sbs.mp4", true, "sbs"},
		{"Super movie.3d.htab.mp4", true, "htab"},
		{"Super movie.3d.tab.mp4", true, "tab"},
		{"Super movie.hsbs.mp4", true, "hsbs"},
		{"Super movie.sbs3d.mp4", true, "sbs3d"},
		{"Super movie.3d.mvc.mp4", true, "mvc"},
		{"Super movie [3d].mp4", false, ""},
		{"Super movie [fsbs].mp4", true, "fsbs"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := naming.Parse3D(tt.input, naming.DefaultOptions())
			if got.Is3D != tt.is3D || got.Format != tt.format {
				t.Fatalf("Parse3D(%q) = (%t, %q), want (%t, %q)", tt.input, got.Is3D, got.Format, tt.is3D, tt.format)
			}
		})
	}
}

// Jellyfin.Naming.Tests.Video.StubTests.TestStubs and TestStubName.
func TestResolveStub(t *testing.T) {
	tests := []struct {
		path     string
		ok       bool
		typeName string
	}{
		{"video.mkv", false, ""},
		{"video.disc", true, ""},
		{"video.dvd.disc", true, "dvd"},
		{"video.hddvd.disc", true, "hddvd"},
		{"video.bluray.disc", true, "bluray"},
		{"video.brrip.disc", true, "bluray"},
		{"video.bd25.disc", true, "bluray"},
		{"video.vhs.disc", true, "vhs"},
		{"video.hdtv.disc", true, "tv"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, ok := naming.ResolveStub(tt.path, naming.DefaultOptions())
			if ok != tt.ok || got != tt.typeName {
				t.Fatalf("ResolveStub(%q) = (%q, %t), want (%q, %t)", tt.path, got, ok, tt.typeName, tt.ok)
			}
		})
	}
	video := mustResolveVideo(t, "C:/Users/media/Desktop/Video Test/Movies/Oblivion/Oblivion.dvd.disc", naming.DefaultOptions())
	if video == nil || video.Name != "Oblivion" || !video.IsStub || video.StubType != "dvd" {
		t.Fatalf("ResolveVideo dvd stub = %#v, want named dvd stub", video)
	}
}

// Jellyfin.Naming.Tests.Video.ExtraTests.TestKodiExtras, TestExpandedExtras, and TestSample.
func TestResolveExtra(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"trailer.mp4", "Trailer"},
		{"300-trailer.mp4", "Trailer"},
		{"trailer.mp3", ""},
		{"theme-music/300.mp3", "ThemeSong"},
		{"behind the scenes/300.mp4", "BehindTheScenes"},
		{"sample.mp4", "Sample"},
		{"I Live In A Trailer.mp4", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := naming.ResolveExtraWithError(tt.path, "", naming.DefaultOptions())
			if err != nil {
				t.Fatalf("ResolveExtraWithError(%q) error = %v", tt.path, err)
			}
			if got == nil {
				if tt.want != "" {
					t.Fatalf("ResolveExtra(%q) = nil, want %s", tt.path, tt.want)
				}
				return
			}
			if string(got.ExtraType) != tt.want {
				t.Fatalf("ResolveExtra(%q) = %q, want %q", tt.path, got.ExtraType, tt.want)
			}
		})
	}
}

// Jellyfin.Naming.Tests.Video.VideoResolverTests.ResolveFile_ValidFileName_Success.
func TestResolveVideo(t *testing.T) {
	tests := []struct {
		path, name, container string
		year                  *int
	}{
		{"/server/Movies/300 (2006)/300 (2006).bluray.disc", "300", "disc", intp(2006)},
		{"/server/Movies/Arrival.2016.2160p.Blu-Ray.HEVC.mkv", "Arrival", "mkv", intp(2016)},
		{"/server/Movies/American.Psycho.mkv", "American.Psycho", "mkv", nil},
		{"/server/Movies/Oblivion/Oblivion.3d.hsbs.mkv", "Oblivion", "mkv", nil},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := mustResolveVideo(t, tt.path, naming.DefaultOptions())
			if got == nil || got.Path != tt.path || got.Name != tt.name || got.Container != tt.container || !reflect.DeepEqual(got.Year, tt.year) {
				t.Fatalf("ResolveVideo(%q) = %#v, want path/name/container/year %q/%q/%q/%v", tt.path, got, tt.path, tt.name, tt.container, tt.year)
			}
		})
	}
	if got := mustResolveVideo(t, "", naming.DefaultOptions()); got != nil {
		t.Fatalf("ResolveVideo(empty) = %#v, want nil", got)
	}
}
