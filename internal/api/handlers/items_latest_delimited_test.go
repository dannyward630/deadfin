// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestLatestItemsAcceptsTheInfuseRequest reproduces the Infuse 8.5.7 request
// from issue #571. AlternateMediaSources is an Emby field that is not in
// ItemFields. Upstream CommaDelimitedCollectionModelBinder drops it and
// answers 200 with the known fields applied.
func TestLatestItemsAcceptsTheInfuseRequest(t *testing.T) {
	userID, libraryID, movieID := latestUUID(t, 40), latestUUID(t, 41), latestUUID(t, 42)
	reader := &latestReader{latest: []item.Item{{ID: movieID, Kind: item.KindMovie}}}
	rt := latestRouter(reader, &latestBuilder{})
	path := "/Items/Latest?userId=" + userID.Dashed() + "&parentId=" + libraryID.Dashed() +
		"&fields=DateCreated,Etag,Genres,MediaSources,AlternateMediaSources,Overview,ParentId,Path,ProviderIds,SortName,RecursiveItemCount,ChildCount&startIndex=0&limit=20"
	record := httptest.NewRecorder()
	rt.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET Infuse /Items/Latest status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	want := []string{"DateCreated", "Etag", "Genres", "MediaSources", "Overview", "ParentId", "Path", "ProviderIds", "SortName", "RecursiveItemCount", "ChildCount"}
	if got := reader.seen[0].DtoOptions.Fields; !reflect.DeepEqual(got, want) {
		t.Fatalf("latest fields = %v, want %v", got, want)
	}
	if got := reader.seen[0].ParentID; got == nil || *got != libraryID {
		t.Fatalf("latest parent = %v, want %s", got, libraryID)
	}
}

func TestLatestItemsDropsUnknownDelimitedEnumValues(t *testing.T) {
	type site struct {
		param, known, unknown string
		get                   func(store.ItemQuery) string
		// want values for known, unknown, mixed, and empty input.
		wantKnown, wantUnknown, wantMixed, wantEmpty string
	}
	sites := []site{
		{param: "fields", known: "overview", unknown: "AlternateMediaSources", get: func(q store.ItemQuery) string { return fmt.Sprint(q.DtoOptions.Fields) },
			wantKnown: "[Overview]", wantUnknown: "[]", wantMixed: "[Overview]", wantEmpty: fmt.Sprint(latestDefaultItemFields)},
		{param: "includeItemTypes", known: "movie", unknown: "Podcast", get: func(q store.ItemQuery) string { return fmt.Sprint(q.IncludeItemTypes) },
			wantKnown: fmt.Sprint([]item.Kind{item.KindMovie}), wantUnknown: "[]", wantMixed: fmt.Sprint([]item.Kind{item.KindMovie}), wantEmpty: "[]"},
		{param: "enableImageTypes", known: "thumb", unknown: "Hologram", get: func(q store.ItemQuery) string { return fmt.Sprint(q.DtoOptions.ImageTypes) },
			wantKnown: fmt.Sprint([]item.ImageType{item.ImageTypeThumb}), wantUnknown: fmt.Sprint(latestAllImageTypes), wantMixed: fmt.Sprint([]item.ImageType{item.ImageTypeThumb}), wantEmpty: fmt.Sprint(latestAllImageTypes)},
	}
	for _, s := range sites {
		for _, tc := range []struct{ name, raw, want string }{
			{name: "known", raw: s.known, want: s.wantKnown},
			{name: "unknown", raw: s.unknown, want: s.wantUnknown},
			{name: "mixed", raw: s.unknown + "," + s.known, want: s.wantMixed},
			{name: "empty", raw: "", want: s.wantEmpty},
		} {
			t.Run(s.param+"/"+tc.name, func(t *testing.T) {
				reader := &latestReader{}
				record := httptest.NewRecorder()
				latestRouter(reader, &latestBuilder{}).ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Items/Latest?groupItems=false&"+s.param+"="+tc.raw, nil))
				if record.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200; body=%s", record.Code, record.Body.String())
				}
				if got := s.get(reader.seen[0]); got != tc.want {
					t.Fatalf("%s=%s binds %s, want %s", s.param, tc.raw, got, tc.want)
				}
			})
		}
	}
}
