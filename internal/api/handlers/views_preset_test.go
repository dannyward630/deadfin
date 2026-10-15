// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
)

// TestUserViewsQueryDropsUnknownPresetViews pins upstream
// CommaDelimitedCollectionModelBinder for CollectionType?[] presetViews.
func TestUserViewsQueryDropsUnknownPresetViews(t *testing.T) {
	userID := latestUUID(t, 50)
	for _, tc := range []struct{ name, raw, want string }{
		{name: "known", raw: "movies,TVSHOWS", want: fmt.Sprint([]item.CollectionType{item.CollectionTypeMovies, item.CollectionTypeTVShows})},
		{name: "unknown", raw: "podcasts", want: "[]"},
		{name: "mixed", raw: "podcasts,music", want: fmt.Sprint([]item.CollectionType{item.CollectionTypeMusic})},
		{name: "empty", raw: "", want: "[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			query, ok := userViewsQuery(record, httptest.NewRequest(http.MethodGet, "/UserViews?presetViews="+tc.raw, nil), userID)
			if !ok {
				t.Fatalf("userViewsQuery(presetViews=%s) status = %d, want success", tc.raw, record.Code)
			}
			if got := fmt.Sprint(query.PresetViews); got != tc.want {
				t.Fatalf("presetViews=%s binds %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}
