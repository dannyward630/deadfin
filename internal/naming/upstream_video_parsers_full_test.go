// SPDX-License-Identifier: GPL-2.0-only

// Exhaustive port of active upstream/jellyfin/tests/Jellyfin.Naming.Tests/Video/{CleanDateTimeTests,CleanStringTests,Format3DTests,StubTests,ExtraTests}.cs cases.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

// Jellyfin.Naming.Tests.Video.CleanDateTimeTests.CleanDateTimeTest (43 active InlineData rows).
func TestUpstreamVideoCleanDateTimeFull(t *testing.T) {
	for _, tt := range []struct {
		input, name string
		year        *int
	}{
		{"The Wolf of Wall Street (2013).mkv", "The Wolf of Wall Street", intp(2013)},
		{"The Wolf of Wall Street 2 (2013).mkv", "The Wolf of Wall Street 2", intp(2013)},
		{"The Wolf of Wall Street - 2 (2013).mkv", "The Wolf of Wall Street - 2", intp(2013)},
		{"The Wolf of Wall Street 2001 (2013).mkv", "The Wolf of Wall Street 2001", intp(2013)},
		{"300 (2006).mkv", "300", intp(2006)},
		{"d:/movies/300 (2006).mkv", "300", intp(2006)},
		{"300 2 (2006).mkv", "300 2", intp(2006)},
		{"300 - 2 (2006).mkv", "300 - 2", intp(2006)},
		{"300 2001 (2006).mkv", "300 2001", intp(2006)},
		{"curse.of.chucky.2013.stv.unrated.multi.1080p.bluray.x264-rough", "curse.of.chucky", intp(2013)},
		{"curse.of.chucky.2013.stv.unrated.multi.2160p.bluray.x264-rough", "curse.of.chucky", intp(2013)},
		{"/server/Movies/300 (2007)/300 (2006).bluray.disc", "300", intp(2006)},
		{"Arrival.2016.2160p.Blu-Ray.HEVC.mkv", "Arrival", intp(2016)},
		{"The Wolf of Wall Street (2013)", "The Wolf of Wall Street", intp(2013)},
		{"The Wolf of Wall Street 2 (2013)", "The Wolf of Wall Street 2", intp(2013)},
		{"The Wolf of Wall Street - 2 (2013)", "The Wolf of Wall Street - 2", intp(2013)},
		{"The Wolf of Wall Street 2001 (2013)", "The Wolf of Wall Street 2001", intp(2013)},
		{"300 (2006)", "300", intp(2006)},
		{"d:/movies/300 (2006)", "300", intp(2006)},
		{"300 2 (2006)", "300 2", intp(2006)},
		{"300 - 2 (2006)", "300 - 2", intp(2006)},
		{"300 2001 (2006)", "300 2001", intp(2006)},
		{"/server/Movies/300 (2007)/300 (2006)", "300", intp(2006)},
		{"/server/Movies/300 (2007)/300 (2006).mkv", "300", intp(2006)},
		{"American.Psycho.mkv", "American.Psycho.mkv", nil},
		{"American Psycho.mkv", "American Psycho.mkv", nil},
		{"[rec].mkv", "[rec].mkv", nil},
		{"St. Vincent (2014)", "St. Vincent", intp(2014)},
		{"Super movie(2009).mp4", "Super movie", intp(2009)},
		{"Drug War 2013.mp4", "Drug War", intp(2013)},
		{"My Movie (1997) - GreatestReleaseGroup 2019.mp4", "My Movie", intp(1997)},
		{"First Man 2018 1080p.mkv", "First Man", intp(2018)},
		{"First Man (2018) 1080p.mkv", "First Man", intp(2018)},
		{"Maximum Ride - 2016 - WEBDL-1080p - x264 AC3.mkv", "Maximum Ride", intp(2016)},
		{"3.Days.to.Kill.2014.720p.BluRay.x264.YIFY.mkv", "3.Days.to.Kill", intp(2014)},
		{"3 days to kill (2005).mkv", "3 days to kill", intp(2005)},
		{"Rain Man 1988 REMASTERED 1080p BluRay x264 AAC - Ozlem.mp4", "Rain Man", intp(1988)},
		{"My Movie 2013.12.09", "My Movie 2013.12.09", nil},
		{"My Movie 2013-12-09", "My Movie 2013-12-09", nil},
		{"My Movie 20131209", "My Movie 20131209", nil},
		{"My Movie 2013-12-09 2013", "My Movie 2013-12-09", intp(2013)},
		{"", "", nil},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got := naming.CleanDateTime(tt.input, naming.DefaultOptions())
			if got.Name != tt.name || !reflect.DeepEqual(got.Year, tt.year) {
				t.Fatalf("CleanDateTime(%q) = %#v, want name/year %q/%v", tt.input, got, tt.name, tt.year)
			}
		})
	}
}

// Jellyfin.Naming.Tests.Video.CleanStringTests.CleanStringTest_NeedsCleaning_Success (20 active InlineData rows)
// and CleanStringTest_DoesntNeedCleaning_False (8 active InlineData rows).
func TestUpstreamVideoCleanStringFull(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"Super movie 480p.mp4", "Super movie"},
		{"Super movie Multi.mp4", "Super movie"},
		{"Super movie 480p 2001.mp4", "Super movie"},
		{"Super movie [480p].mp4", "Super movie"},
		{"480 Super movie [tmdbid=12345].mp4", "480 Super movie"},
		{"Crouching.Tiger.Hidden.Dragon.4k.mkv", "Crouching.Tiger.Hidden.Dragon"},
		{"Crouching.Tiger.Hidden.Dragon.UltraHD.mkv", "Crouching.Tiger.Hidden.Dragon"},
		{"Crouching.Tiger.Hidden.Dragon.UHD.mkv", "Crouching.Tiger.Hidden.Dragon"},
		{"Crouching.Tiger.Hidden.Dragon.HDR.mkv", "Crouching.Tiger.Hidden.Dragon"},
		{"Crouching.Tiger.Hidden.Dragon.HDC.mkv", "Crouching.Tiger.Hidden.Dragon"},
		{"Crouching.Tiger.Hidden.Dragon-HDC.mkv", "Crouching.Tiger.Hidden.Dragon"},
		{"Crouching.Tiger.Hidden.Dragon.BDrip.mkv", "Crouching.Tiger.Hidden.Dragon"},
		{"Crouching.Tiger.Hidden.Dragon.BDrip-HDC.mkv", "Crouching.Tiger.Hidden.Dragon"},
		{"Crouching.Tiger.Hidden.Dragon.4K.UltraHD.HDR.BDrip-HDC.mkv", "Crouching.Tiger.Hidden.Dragon"},
		{"[HorribleSubs] Made in Abyss - 13 [720p].mkv", "Made in Abyss"},
		{"[Tsundere] Kore wa Zombie Desu ka of the Dead [BDRip h264 1920x1080 FLAC]", "Kore wa Zombie Desu ka of the Dead"},
		{"[Erai-raws] Jujutsu Kaisen - 03 [720p][Multiple Subtitle].mkv", "Jujutsu Kaisen"},
		{"[OCN] 애타는 로맨스 720p-NEXT", "애타는 로맨스"},
		{"[tvN] 혼술남녀.E01-E16.720p-NEXT", "혼술남녀"},
		{"[tvN] 연애말고 결혼 E01~E16 END HDTV.H264.720p-WITH", "연애말고 결혼"},
		{"2026年01月10日23時00分00秒-[新]TRIGUN　STARGAZE[字].mp4", "2026年01月10日23時00分00秒-[新]TRIGUN　STARGAZE"},
	} {
		t.Run("cleans/"+tt.input, func(t *testing.T) {
			got, ok := naming.CleanString(tt.input, naming.DefaultOptions())
			if !ok || got != tt.want {
				t.Fatalf("CleanString(%q) = (%q, %t), want (%q, true)", tt.input, got, ok, tt.want)
			}
		})
	}
	for _, input := range []string{"", "Super movie(2009).mp4", "[rec].mkv", "American.Psycho.mkv", "American Psycho.mkv", "Run lola run (lola rennt) (2009).mp4", "2026年01月05日00時55分00秒-[新]違国日記【ＡＮｉＭｉＤＮｉＧＨＴ！！！】＃１.mp4"} {
		t.Run("unchanged/"+input, func(t *testing.T) {
			if got, ok := naming.CleanString(input, naming.DefaultOptions()); ok || got != "" {
				t.Fatalf("CleanString(%q) = (%q, %t), want (empty, false)", input, got, ok)
			}
		})
	}
}

// Jellyfin.Naming.Tests.Video.Format3DTests.TestKodiFormat3D, Test3DName, and TestExpandedFormat3D (23 active calls).
func TestUpstreamVideoFormat3DFull(t *testing.T) {
	for _, tt := range []struct {
		input, format string
		is3D          bool
	}{
		{"Super movie.3d.mp4", "", false},
		{"Super movie.3d.hsbs.mp4", "hsbs", true},
		{"Super movie.3d.sbs.mp4", "sbs", true},
		{"Super movie.3d.htab.mp4", "htab", true},
		{"Super movie.3d.tab.mp4", "tab", true},
		{"Super movie 3d hsbs.mp4", "hsbs", true},
		{"Super movie.3d.mp4", "", false},
		{"Super movie.3d.hsbs.mp4", "hsbs", true},
		{"Super movie.3d.sbs.mp4", "sbs", true},
		{"Super movie.3d.htab.mp4", "htab", true},
		{"Super movie.3d.tab.mp4", "tab", true},
		{"Super movie.hsbs.mp4", "hsbs", true},
		{"Super movie.sbs.mp4", "sbs", true},
		{"Super movie.htab.mp4", "htab", true},
		{"Super movie.tab.mp4", "tab", true},
		{"Super movie.sbs3d.mp4", "sbs3d", true},
		{"Super movie.3d.mvc.mp4", "mvc", true},
		{"Super movie [3d].mp4", "", false},
		{"Super movie [hsbs].mp4", "hsbs", true},
		{"Super movie [fsbs].mp4", "fsbs", true},
		{"Super movie [ftab].mp4", "ftab", true},
		{"Super movie [htab].mp4", "htab", true},
		{"Super movie [sbs3d].mp4", "sbs3d", true},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got := naming.Parse3D(tt.input, naming.DefaultOptions())
			if got.Is3D != tt.is3D || got.Format != tt.format {
				t.Fatalf("Parse3D(%q) = %#v, want (%t, %q)", tt.input, got, tt.is3D, tt.format)
			}
		})
	}
	video := mustResolveVideo(t, "C:/Users/media/Desktop/Video Test/Movies/Oblivion/Oblivion.3d.hsbs.mkv", naming.DefaultOptions())
	if video == nil || video.Name != "Oblivion" || video.Format3D != "hsbs" {
		t.Fatalf("ResolveVideo 3D name = %#v, want Oblivion/hsbs", video)
	}
}

// Jellyfin.Naming.Tests.Video.StubTests.TestStubs and TestStubName (14 active calls).
func TestUpstreamVideoStubsFull(t *testing.T) {
	for _, tt := range []struct {
		path, kind string
		stub       bool
	}{
		{"video.mkv", "", false}, {"video.disc", "", true}, {"video.dvd.disc", "dvd", true}, {"video.hddvd.disc", "hddvd", true}, {"video.bluray.disc", "bluray", true}, {"video.brrip.disc", "bluray", true}, {"video.bd25.disc", "bluray", true}, {"video.bd50.disc", "bluray", true}, {"video.vhs.disc", "vhs", true}, {"video.hdtv.disc", "tv", true}, {"video.pdtv.disc", "tv", true}, {"video.dsr.disc", "tv", true}, {"", "", false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			kind, ok := naming.ResolveStub(tt.path, naming.DefaultOptions())
			if ok != tt.stub || kind != tt.kind {
				t.Fatalf("ResolveStub(%q) = (%q, %t), want (%q, %t)", tt.path, kind, ok, tt.kind, tt.stub)
			}
		})
	}
	video := mustResolveVideo(t, "C:/Users/media/Desktop/Video Test/Movies/Oblivion/Oblivion.dvd.disc", naming.DefaultOptions())
	if video == nil || video.Name != "Oblivion" {
		t.Fatalf("ResolveVideo stub = %#v, want named Oblivion", video)
	}
}

// Jellyfin.Naming.Tests.Video.ExtraTests.TestKodiExtras, TestExpandedExtras, TestDirectoriesAudioExtras,
// TestDirectoriesVideoExtras, TestNonExtraDirectories, TestTopLevelDirectoriesWithAudioExtraNames,
// TestTopLevelDirectoriesWithVideoExtraNames, TestSample, and TestSuffixPartOfTitle.
func TestUpstreamVideoExtrasFull(t *testing.T) {
	type extraCase struct {
		path, root string
		want       naming.ExtraType
	}
	cases := []extraCase{
		{"trailer.mp4", "", naming.ExtraTrailer},
		{"300-trailer.mp4", "", naming.ExtraTrailer},
		{"300.trailer.mp4", "", naming.ExtraTrailer},
		{"300_trailer.mp4", "", naming.ExtraTrailer},
		{"300 - trailer.mp4", "", naming.ExtraTrailer},
		{"theme.mp3", "", naming.ExtraThemeSong},
		{"trailer.mp4", "", naming.ExtraTrailer},
		{"trailer.mp3", "", ""},
		{"300-trailer.mp4", "", naming.ExtraTrailer},
		{"stuff trailerthings.mkv", "", ""},
		{"theme.mp3", "", naming.ExtraThemeSong},
		{"theme.mkv", "", ""},
		{"300-scene.mp4", "", naming.ExtraScene},
		{"300-scene2.mp4", "", naming.ExtraScene},
		{"300-clip.mp4", "", naming.ExtraClip},
		{"300-deleted.mp4", "", naming.ExtraDeletedScene},
		{"300-deletedscene.mp4", "", naming.ExtraDeletedScene},
		{"300-interview.mp4", "", naming.ExtraInterview},
		{"300-behindthescenes.mp4", "", naming.ExtraBehindTheScenes},
		{"300-featurette.mp4", "", naming.ExtraFeaturette},
		{"300-short.mp4", "", naming.ExtraShort},
		{"300-extra.mp4", "", naming.ExtraUnknown},
		{"300-other.mp4", "", naming.ExtraUnknown},
		{"sample.mp4", "", naming.ExtraSample},
		{"300-sample.mp4", "", naming.ExtraSample},
		{"300.sample.mp4", "", naming.ExtraSample},
		{"300_sample.mp4", "", naming.ExtraSample},
		{"300 - sample.mp4", "", naming.ExtraSample},
		{"I Live In A Trailer.mp4", "", ""},
		{"The DNA Sample.mp4", "", ""},
	}
	for _, tt := range cases {
		assertUpstreamExtra(t, tt.path, tt.root, tt.want)
	}
	for _, tt := range []struct {
		kind  naming.ExtraType
		dir   string
		audio bool
	}{{naming.ExtraThemeSong, "theme-music", true}, {naming.ExtraBehindTheScenes, "behind the scenes", false}, {naming.ExtraDeletedScene, "deleted scenes", false}, {naming.ExtraInterview, "interviews", false}, {naming.ExtraScene, "scenes", false}, {naming.ExtraSample, "samples", false}, {naming.ExtraShort, "shorts", false}, {naming.ExtraTrailer, "trailers", false}, {naming.ExtraFeaturette, "featurettes", false}, {naming.ExtraClip, "clips", false}, {naming.ExtraThemeVideo, "backdrops", false}, {naming.ExtraUnknown, "extra", false}, {naming.ExtraUnknown, "extras", false}, {naming.ExtraUnknown, "other", false}} {
		ext := ".mp4"
		nested := "something.mkv"
		absolute := "whoknows.mp4"
		if tt.audio {
			ext = ".mp3"
			nested = "something.mp3"
			absolute = "whoknows.mp3"
		}
		assertUpstreamExtra(t, tt.dir+"/300"+ext, "", tt.kind)
		assertUpstreamExtra(t, "300/"+tt.dir+"/"+nested, "", tt.kind)
		assertUpstreamExtra(t, "/data/something/Movies/300/"+tt.dir+"/"+absolute, "", tt.kind)
	}
	for _, dir := range []string{"gibberish", "not a scene", "The Big Short"} {
		for _, path := range []string{dir + "/300.mp4", "300/" + dir + "/something.mkv", "/data/something/Movies/300/" + dir + "/whoknows.mp4", "/data/something/Movies/" + dir + "/" + dir + ".mp4"} {
			assertUpstreamExtra(t, path, "", "")
		}
	}
	for _, tt := range []struct {
		kind  naming.ExtraType
		dir   string
		audio bool
	}{{naming.ExtraThemeSong, "theme-music", true}, {naming.ExtraTrailer, "trailers", false}, {naming.ExtraThemeVideo, "backdrops", false}, {naming.ExtraBehindTheScenes, "behind the scenes", false}, {naming.ExtraDeletedScene, "deleted scenes", false}, {naming.ExtraInterview, "interviews", false}, {naming.ExtraScene, "scenes", false}, {naming.ExtraSample, "samples", false}, {naming.ExtraShort, "shorts", false}, {naming.ExtraFeaturette, "featurettes", false}, {naming.ExtraUnknown, "extras", false}, {naming.ExtraUnknown, "extra", false}, {naming.ExtraUnknown, "other", false}, {naming.ExtraClip, "clips", false}} {
		root := "/data/something/" + tt.dir
		ext := ".mp4"
		nested := "something.mkv"
		if tt.audio {
			ext = ".mp3"
			nested = "something.mp3"
		}
		assertUpstreamExtra(t, root+"/300"+ext, root, "")
		assertUpstreamExtra(t, root+"/300/"+tt.dir+"/"+nested, root, tt.kind)
	}
}

func assertUpstreamExtra(t *testing.T, path, root string, want naming.ExtraType) {
	t.Helper()
	got, err := naming.ResolveExtraWithError(path, root, naming.DefaultOptions())
	if err != nil {
		t.Fatalf("ResolveExtraWithError(%q, %q) error = %v", path, root, err)
	}
	if want == "" {
		if got != nil {
			t.Fatalf("ResolveExtraWithError(%q, %q) = %#v, want nil", path, root, got)
		}
		return
	}
	if got == nil || got.ExtraType != want {
		t.Fatalf("ResolveExtraWithError(%q, %q) = %#v, want type %q", path, root, got, want)
	}
}
