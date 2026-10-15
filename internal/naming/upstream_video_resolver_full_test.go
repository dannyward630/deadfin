// SPDX-License-Identifier: GPL-2.0-only

// Exhaustive port of active upstream/jellyfin/tests/Jellyfin.Naming.Tests/Video/VideoResolverTests.cs provider rows.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

// Jellyfin.Naming.Tests.Video.VideoResolverTests.ResolveFile_ValidFileName_Success
// (16 active MemberData rows) and ResolveFile_EmptyPath.
func TestUpstreamVideoResolverValidFilesFull(t *testing.T) {
	for _, tt := range []struct {
		path, container, name string
		extra                 naming.ExtraType
		format, stubType      string
		year                  *int
		is3D, isStub          bool
	}{
		{"/server/Movies/7 Psychos.mkv/7 Psychos.mkv", "mkv", "7 Psychos", "", "", "", nil, false, false},
		{"/server/Movies/3 days to kill (2005)/3 days to kill (2005).mkv", "mkv", "3 days to kill", "", "", "", intp(2005), false, false},
		{"/server/Movies/American Psycho/American.Psycho.mkv", "mkv", "American.Psycho", "", "", "", nil, false, false},
		{"/server/Movies/brave (2007)/brave (2006).3d.sbs.mkv", "mkv", "brave", "", "sbs", "", intp(2006), true, false},
		{"/server/Movies/300 (2007)/300 (2006).3d1.sbas.mkv", "mkv", "300", "", "", "", intp(2006), false, false},
		{"/server/Movies/300 (2007)/300 (2006).3d.sbs.mkv", "mkv", "300", "", "sbs", "", intp(2006), true, false},
		{"/server/Movies/brave (2007)/brave (2006)-trailer.bluray.disc", "disc", "brave", "", "", "bluray", intp(2006), false, true},
		{"/server/Movies/300 (2007)/300 (2006)-trailer.bluray.disc", "disc", "300", "", "", "bluray", intp(2006), false, true},
		{"/server/Movies/Brave (2007)/Brave (2006).bluray.disc", "disc", "Brave", "", "", "bluray", intp(2006), false, true},
		{"/server/Movies/300 (2007)/300 (2006).bluray.disc", "disc", "300", "", "", "bluray", intp(2006), false, true},
		{"/server/Movies/300 (2007)/300 (2006)-trailer.mkv", "mkv", "300", naming.ExtraTrailer, "", "", intp(2006), false, false},
		{"/server/Movies/Brave (2007)/Brave (2006)-trailer.mkv", "mkv", "Brave", naming.ExtraTrailer, "", "", intp(2006), false, false},
		{"/server/Movies/300 (2007)/300 (2006).mkv", "mkv", "300", "", "", "", intp(2006), false, false},
		{"/server/Movies/Bad Boys (1995)/Bad Boys (1995).mkv", "mkv", "Bad Boys", "", "", "", intp(1995), false, false},
		{"/server/Movies/Brave (2007)/Brave (2006).mkv", "mkv", "Brave", "", "", "", intp(2006), false, false},
		{"/server/Movies/Rain Man 1988 REMASTERED 1080p BluRay x264 AAC - JEFF/Rain Man 1988 REMASTERED 1080p BluRay x264 AAC - JEFF.mp4", "mp4", "Rain Man", "", "", "", intp(1988), false, false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			got := mustResolveVideo(t, tt.path, naming.DefaultOptions())
			if got == nil {
				t.Fatalf("ResolveVideo(%q) = nil", tt.path)
			}
			if got.Path != tt.path || got.Container != tt.container || got.Name != tt.name || !reflect.DeepEqual(got.Year, tt.year) || got.ExtraType != tt.extra || got.Format3D != tt.format || got.Is3D != tt.is3D || got.IsStub != tt.isStub || got.StubType != tt.stubType || got.IsDirectory {
				t.Fatalf("ResolveVideo(%q) = %#v, want all VideoFileInfo fields path/container/name/year/extra/3D/stub/directory", tt.path, got)
			}
		})
	}
	if got := mustResolveVideo(t, "", naming.DefaultOptions()); got != nil {
		t.Fatalf("ResolveVideo(empty) = %#v, want nil", got)
	}
}
