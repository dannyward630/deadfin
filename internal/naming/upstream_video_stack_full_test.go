// SPDX-License-Identifier: GPL-2.0-only

// Port of active file-only cases in upstream/jellyfin/tests/Jellyfin.Naming.Tests/Video/StackTests.cs.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

type upstreamStackExpectation struct {
	name  string
	files []string
}

// Jellyfin.Naming.Tests.Video.StackTests: TestSimpleStack through TestNamesWithoutParts,
// excluding only the three directory/mixed-metadata methods pending the directory resolver seam.
func TestUpstreamVideoStacksFull(t *testing.T) {
	cases := []struct {
		method string
		paths  []string
		want   []upstreamStackExpectation
	}{
		{"TestSimpleStack", []string{"Bad Boys (2006) part1.mkv", "Bad Boys (2006) part2.mkv", "Bad Boys (2006) part3.mkv", "Bad Boys (2006) part4.mkv", "Bad Boys (2006)-trailer.mkv"}, []upstreamStackExpectation{{"Bad Boys (2006)", []string{"Bad Boys (2006) part1.mkv", "Bad Boys (2006) part2.mkv", "Bad Boys (2006) part3.mkv", "Bad Boys (2006) part4.mkv"}}}},
		{"TestFalsePositives", []string{"Bad Boys (2006).mkv", "Bad Boys (2007).mkv"}, nil},
		{"TestFalsePositives2", []string{"Bad Boys 2006.mkv", "Bad Boys 2007.mkv"}, nil},
		{"TestFalsePositives3", []string{"300 (2006).mkv", "300 (2007).mkv"}, nil},
		{"TestFalsePositives4", []string{"300 2006.mkv", "300 2007.mkv"}, nil},
		{"TestFalsePositives5", []string{"Star Trek 1 - The motion picture.mkv", "Star Trek 2- The wrath of khan.mkv"}, nil},
		{"TestFalsePositives6", []string{"Red Riding in the Year of Our Lord 1983 (2009).mkv", "Red Riding in the Year of Our Lord 1980 (2009).mkv", "Red Riding in the Year of Our Lord 1974 (2009).mkv"}, nil},
		{"TestStackName", []string{"d:/movies/300 2006 part1.mkv", "d:/movies/300 2006 part2.mkv"}, []upstreamStackExpectation{{"300 2006", []string{"d:/movies/300 2006 part1.mkv", "d:/movies/300 2006 part2.mkv"}}}},
		{"ResolveFiles_GivenPartInMiddleOfName_ReturnsNoStack", []string{"Bad Boys (2006).part1.stv.unrated.multi.1080p.bluray.x264-rough.mkv", "Bad Boys (2006).part2.stv.unrated.multi.1080p.bluray.x264-rough.mkv", "Bad Boys (2006).part3.stv.unrated.multi.1080p.bluray.x264-rough.mkv", "Bad Boys (2006).part4.stv.unrated.multi.1080p.bluray.x264-rough.mkv", "Bad Boys (2006)-trailer.mkv"}, nil},
		{"ResolveFiles_FileNamesWithMissingPartType_ReturnsNoStack", []string{"Bad Boys (2006).mkv", "Bad Boys (2006) 1.mkv", "Bad Boys (2006) 2.mkv", "Bad Boys (2006) 3.mkv", "Bad Boys (2006)-trailer.mkv"}, nil},
		{"TestSimpleStackWithNumericName", []string{"300 (2006) part1.mkv", "300 (2006) part2.mkv", "300 (2006) part3.mkv", "300 (2006) part4.mkv", "300 (2006)-trailer.mkv"}, []upstreamStackExpectation{{"300 (2006)", []string{"300 (2006) part1.mkv", "300 (2006) part2.mkv", "300 (2006) part3.mkv", "300 (2006) part4.mkv"}}}},
		{"TestMixedExpressionsNotAllowed", []string{"Bad Boys (2006) part1.mkv", "Bad Boys (2006) part2.mkv", "Bad Boys (2006) part3.mkv", "Bad Boys (2006) parta.mkv", "Bad Boys (2006)-trailer.mkv"}, []upstreamStackExpectation{{"Bad Boys (2006)", []string{"Bad Boys (2006) part1.mkv", "Bad Boys (2006) part2.mkv", "Bad Boys (2006) part3.mkv"}}}},
		{"TestDualStacks", []string{"Bad Boys (2006) part1.mkv", "Bad Boys (2006) part2.mkv", "Bad Boys (2006) part3.mkv", "Bad Boys (2006) part4.mkv", "Bad Boys (2006)-trailer.mkv", "300 (2006) part1.mkv", "300 (2006) part2.mkv", "300 (2006) part3.mkv", "300 (2006)-trailer.mkv"}, []upstreamStackExpectation{{"300 (2006)", []string{"300 (2006) part1.mkv", "300 (2006) part2.mkv", "300 (2006) part3.mkv"}}, {"Bad Boys (2006)", []string{"Bad Boys (2006) part1.mkv", "Bad Boys (2006) part2.mkv", "Bad Boys (2006) part3.mkv", "Bad Boys (2006) part4.mkv"}}}},
		{"TestMissingParttype", []string{"300a.mkv", "300b.mkv", "300c.mkv", "300-trailer.mkv"}, nil},
		{"TestFailSequence", []string{"300 part1.mkv", "300 part2.mkv", "Avatar", "Avengers part1.mkv", "Avengers part2.mkv", "Avengers part3.mkv"}, []upstreamStackExpectation{{"300", []string{"300 part1.mkv", "300 part2.mkv"}}, {"Avengers", []string{"Avengers part1.mkv", "Avengers part2.mkv", "Avengers part3.mkv"}}}},
		{"TestMixedExpressions", []string{"Bad Boys (2006) part1.mkv", "Bad Boys (2006) part2.mkv", "Bad Boys (2006) part3.mkv", "Bad Boys (2006) part4.mkv", "Bad Boys (2006)-trailer.mkv", "300 (2006) parta.mkv", "300 (2006) partb.mkv", "300 (2006) partc.mkv", "300 (2006) partd.mkv", "300 (2006)-trailer.mkv", "300a.mkv", "300b.mkv", "300c.mkv", "300-trailer.mkv"}, []upstreamStackExpectation{{"300 (2006)", []string{"300 (2006) parta.mkv", "300 (2006) partb.mkv", "300 (2006) partc.mkv", "300 (2006) partd.mkv"}}, {"Bad Boys (2006)", []string{"Bad Boys (2006) part1.mkv", "Bad Boys (2006) part2.mkv", "Bad Boys (2006) part3.mkv", "Bad Boys (2006) part4.mkv"}}}},
		{"TestAlphaLimitOfFour", []string{"300 (2006) parta.mkv", "300 (2006) partb.mkv", "300 (2006) partc.mkv", "300 (2006) partd.mkv", "300 (2006) parte.mkv", "300 (2006) partf.mkv", "300 (2006) partg.mkv", "300 (2006)-trailer.mkv"}, []upstreamStackExpectation{{"300 (2006)", []string{"300 (2006) parta.mkv", "300 (2006) partb.mkv", "300 (2006) partc.mkv", "300 (2006) partd.mkv"}}}},
		{"TestNamesWithoutParts", []string{"Harry Potter and the Deathly Hallows.mkv", "Harry Potter and the Deathly Hallows 1.mkv", "Harry Potter and the Deathly Hallows 2.mkv", "Harry Potter and the Deathly Hallows 3.mkv", "Harry Potter and the Deathly Hallows 4.mkv"}, nil},
		{"TestNumbersAppearingBeforePartNumber", []string{"Neverland (2011)[720p][PG][Voted 6.5][Family-Fantasy]part1.mkv", "Neverland (2011)[720p][PG][Voted 6.5][Family-Fantasy]part2.mkv"}, []upstreamStackExpectation{{"Neverland (2011)[720p][PG][Voted 6.5][Family-Fantasy]", []string{"Neverland (2011)[720p][PG][Voted 6.5][Family-Fantasy]part1.mkv", "Neverland (2011)[720p][PG][Voted 6.5][Family-Fantasy]part2.mkv"}}}},
	}
	for _, tt := range cases {
		t.Run(tt.method, func(t *testing.T) {
			got := mustResolveStacks(t, tt.paths, naming.DefaultOptions())
			if len(got) != len(tt.want) {
				t.Fatalf("ResolveStacks(%s) returned %#v, want %d stacks", tt.method, got, len(tt.want))
			}
			for i, want := range tt.want {
				if got[i].Name != want.name || !reflect.DeepEqual(got[i].Files, want.files) {
					t.Fatalf("ResolveStacks(%s)[%d] = %#v, want %#v", tt.method, i, got[i], want)
				}
			}
		})
	}
}
