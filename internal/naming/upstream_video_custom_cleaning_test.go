// SPDX-License-Identifier: GPL-2.0-only

// Configurable rule behavior from Emby.Naming/Video/VideoResolver.cs:144-158.
package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestUpstreamVideoCleaningHonorsExplicitlyClearedRules(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.CleanDateTimeRegexes = nil
	opts.CleanStringRegexes = nil
	date := naming.CleanDateTime("Movie (2024).mkv", opts)
	if date.Name != "Movie (2024).mkv" || date.Year != nil {
		t.Fatalf("CleanDateTime with cleared rules = %#v, want unchanged no-year result", date)
	}
	if got, ok := naming.CleanString("Movie 1080p.mkv", opts); ok || got != "" {
		t.Fatalf("CleanString with cleared rules = (%q, %t), want no clean result", got, ok)
	}
}
