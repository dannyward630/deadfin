// SPDX-License-Identifier: GPL-2.0-only

package bind

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestDelimitedEnumsDropsValuesParseRejects(t *testing.T) {
	parse := func(value string) (int, bool) {
		switch value {
		case "one":
			return 1, true
		case "two":
			return 2, true
		}
		return 0, false
	}
	for _, tc := range []struct {
		name   string
		values []string
		want   []int
	}{
		{name: "known", values: []string{"one", "two"}, want: []int{1, 2}},
		{name: "unknown", values: []string{"three"}, want: []int{}},
		{name: "mixed", values: []string{"three", "two", "four", "one"}, want: []int{2, 1}},
		{name: "empty", values: nil, want: []int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DelimitedEnums("p", tc.values, parse); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("DelimitedEnums = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseItemFieldCoversThePinnedVocabulary(t *testing.T) {
	for _, field := range ItemFieldValues() {
		if got, ok := ParseItemField(field); !ok || got != field {
			t.Fatalf("ParseItemField(%q) = %q, %v", field, got, ok)
		}
	}
	if got, ok := ParseItemField("mediasources"); !ok || got != "MediaSources" {
		t.Fatalf("ParseItemField(mediasources) = %q, %v, want MediaSources", got, ok)
	}
	if _, ok := ParseItemField("AlternateMediaSources"); ok {
		t.Fatal("ParseItemField accepted the Emby field AlternateMediaSources")
	}
}

// TestItemsQueryDropsUnknownDelimitedEnumValues pins upstream
// CommaDelimitedCollectionModelBinder.GetParsedResult for every enum list
// that ItemsQuery binds: an unknown value is dropped, never a 400.
func TestItemsQueryDropsUnknownDelimitedEnumValues(t *testing.T) {
	defaults := DefaultItemQueryDTOOptions().ImageTypes
	type site struct {
		param   string
		known   string
		unknown string
		get     func(store.ItemQuery) string
		// want values for known, unknown, mixed, and empty input.
		wantKnown, wantUnknown, wantMixed, wantEmpty string
	}
	sites := []site{
		{param: "IncludeItemTypes", known: "movie", unknown: "Podcast", get: func(q store.ItemQuery) string { return fmt.Sprint(q.IncludeItemTypes) },
			wantKnown: fmt.Sprint([]item.Kind{item.KindMovie}), wantUnknown: "[]", wantMixed: fmt.Sprint([]item.Kind{item.KindMovie}), wantEmpty: "[]"},
		{param: "ExcludeItemTypes", known: "Series", unknown: "Podcast", get: func(q store.ItemQuery) string { return fmt.Sprint(q.ExcludeItemTypes) },
			wantKnown: fmt.Sprint([]item.Kind{item.KindSeries}), wantUnknown: "[]", wantMixed: fmt.Sprint([]item.Kind{item.KindSeries}), wantEmpty: "[]"},
		{param: "MediaTypes", known: "video", unknown: "Hologram", get: func(q store.ItemQuery) string { return fmt.Sprint(q.MediaTypes) },
			wantKnown: fmt.Sprint([]item.MediaType{item.MediaTypeVideo}), wantUnknown: "[]", wantMixed: fmt.Sprint([]item.MediaType{item.MediaTypeVideo}), wantEmpty: "[]"},
		{param: "VideoTypes", known: "bluray", unknown: "Laserdisc", get: func(q store.ItemQuery) string { return fmt.Sprint(q.VideoTypes) },
			wantKnown: fmt.Sprint([]item.VideoType{item.VideoTypeBluRay}), wantUnknown: "[]", wantMixed: fmt.Sprint([]item.VideoType{item.VideoTypeBluRay}), wantEmpty: "[]"},
		{param: "SortBy", known: "datecreated", unknown: "Vibes", get: func(q store.ItemQuery) string { return fmt.Sprint(q.SortBy) },
			wantKnown: "[DateCreated]", wantUnknown: "[]", wantMixed: "[DateCreated]", wantEmpty: "[]"},
		{param: "SortOrder", known: "descending", unknown: "Sideways", get: func(q store.ItemQuery) string { return fmt.Sprint(q.SortOrder) },
			wantKnown: "[Descending]", wantUnknown: "[]", wantMixed: "[Descending]", wantEmpty: "[]"},
		{param: "EnableImageTypes", known: "logo", unknown: "Hologram", get: func(q store.ItemQuery) string { return fmt.Sprint(q.DtoOptions.ImageTypes) },
			wantKnown: fmt.Sprint([]item.ImageType{item.ImageTypeLogo}), wantUnknown: fmt.Sprint(defaults), wantMixed: fmt.Sprint([]item.ImageType{item.ImageTypeLogo}), wantEmpty: fmt.Sprint(defaults)},
		{param: "Fields", known: "overview", unknown: "AlternateMediaSources", get: func(q store.ItemQuery) string { return fmt.Sprint(q.DtoOptions.Fields) },
			wantKnown: "[Overview]", wantUnknown: "[]", wantMixed: "[Overview]", wantEmpty: "[]"},
		{param: "Filters", known: "isfolder", unknown: "IsHologram", get: func(q store.ItemQuery) string { return fmt.Sprint(q.IsFolder != nil && *q.IsFolder) },
			wantKnown: "true", wantUnknown: "false", wantMixed: "true", wantEmpty: "false"},
	}
	for _, s := range sites {
		for _, tc := range []struct{ name, raw, want string }{
			{name: "known", raw: s.known, want: s.wantKnown},
			{name: "unknown", raw: s.unknown, want: s.wantUnknown},
			{name: "mixed", raw: s.unknown + "," + s.known + "|" + s.unknown, want: s.wantMixed},
			{name: "empty", raw: "", want: s.wantEmpty},
		} {
			t.Run(s.param+"/"+tc.name, func(t *testing.T) {
				query, err := ItemsQuery(url.Values{s.param: {tc.raw}})
				if err != nil {
					t.Fatalf("ItemsQuery(%s=%s) error = %v, want nil", s.param, tc.raw, err)
				}
				if got := s.get(query); got != tc.want {
					t.Fatalf("ItemsQuery(%s=%s) = %s, want %s", s.param, tc.raw, got, tc.want)
				}
			})
		}
	}
}
