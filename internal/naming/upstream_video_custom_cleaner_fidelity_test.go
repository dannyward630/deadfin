// SPDX-License-Identifier: GPL-2.0-only

// Configured cleaner arrays are executed in order by upstream CleanStringParser.cs:27-48 and CleanDateTimeParser.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoCleanDateTimeHonorsCustomRuleContent(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.CleanDateTimeRegexes = []string{`^(.*)ZZ(2024)()()$`}
	got := naming.CleanDateTime("TitleZZ2024", opts)
	if got.Name != "Title" || !reflect.DeepEqual(got.Year, intp(2024)) {
		t.Fatalf("CleanDateTime custom rule = %#v, want Title/2024", got)
	}
	got = naming.CleanDateTime("Movie (2024)", opts)
	if got.Name != "Movie (2024)" || got.Year != nil {
		t.Fatalf("CleanDateTime nonmatching custom rule = %#v, want unchanged/no-year", got)
	}
}

func TestUpstreamVideoCleanStringHonorsMiddleRuleMutation(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.CleanStringRegexes[1] = `^(?<cleaned>.+?)XX$`
	if got, ok := naming.CleanString("TitleXX", opts); !ok || got != "Title" {
		t.Fatalf("CleanString middle custom rule = (%q, %t), want (Title, true)", got, ok)
	}
}

// Jellyfin.Naming.Tests.Video.CleanStringTests.CleanStringTest_DoesntNeedCleaning_False [InlineData(null)].
// Go's public string seam has no nullable value; this is deliberately a distinct source-row execution
// of the same observable empty result, separate from the active [InlineData("")] row.
func TestUpstreamVideoCleanStringNullInputRow(t *testing.T) {
	if got, ok := naming.CleanString("", naming.DefaultOptions()); ok || got != "" {
		t.Fatalf("CleanString(mapped C# null) = (%q, %t), want (empty, false)", got, ok)
	}
}
