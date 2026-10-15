// SPDX-License-Identifier: GPL-2.0-only

package bind

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemsQueryMapsPublicFilters independently pins request normalization at
// the API boundary. ItemsQuery must be the only public-query-to-ItemQuery
// mapper: handlers supply route/auth precedence, not a second parameter parser.
func TestItemsQueryMapsPublicFilters(t *testing.T) {
	first, second := itemsBindUUID(t, 1), itemsBindUUID(t, 2)
	parent := itemsBindUUID(t, 3)
	adjacent := itemsBindUUID(t, 4)
	query, err := ItemsQuery(url.Values{
		"ids":                     {first.String() + "," + second.String()},
		"excludeItemIds":          {second.String()},
		"parentId":                {parent.String()},
		"adjacentTo":              {adjacent.String()},
		"includeItemTypes":        {"movie,episode"},
		"excludeItemTypes":        {"boxset"},
		"mediaTypes":              {"video"},
		"genres":                  {"Drama|Comedy"},
		"tags":                    {"featured|children"},
		"years":                   {"2020,2024"},
		"sortBy":                  {"SortName", "DateCreated"},
		"sortOrder":               {"Descending,Ascending"},
		"startIndex":              {"2"},
		"limit":                   {"5"},
		"recursive":               {"true"},
		"isPlayed":                {"false"},
		"minPremiereDate":         {"2024-01-02T03:04:05Z"},
		"minCommunityRating":      {"8.5"},
		"enableTotalRecordCount":  {"false"},
		"enableUserData":          {"true"},
		"enableImages":            {"false"},
		"imageTypeLimit":          {"3"},
		"enableImageTypes":        {"Primary,Backdrop"},
		"audioLanguages":          {"eng,spa"},
		"subtitleLanguages":       {"eng"},
		"nameStartsWith":          {"The"},
		"nameStartsWithOrGreater": {"A"},
		"nameLessThan":            {"Z"},
	})
	if err != nil {
		t.Fatalf("ItemsQuery: %v", err)
	}

	premiere := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	falseValue := false
	if got, want := query.ItemIDs, []dto.UUID{first, second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ItemIDs = %v, want %v", got, want)
	}
	if got, want := query.ExcludeItemIDs, []dto.UUID{second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ExcludeItemIDs = %v, want %v", got, want)
	}
	if query.ParentID == nil || *query.ParentID != parent || query.AdjacentTo == nil || *query.AdjacentTo != adjacent {
		t.Fatalf("parent/adjacent = %v/%v, want %s/%s", query.ParentID, query.AdjacentTo, parent, adjacent)
	}
	if got, want := query.IncludeItemTypes, []item.Kind{item.KindMovie, item.KindEpisode}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IncludeItemTypes = %v, want %v", got, want)
	}
	if got, want := query.ExcludeItemTypes, []item.Kind{item.KindBoxSet}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ExcludeItemTypes = %v, want %v", got, want)
	}
	if got, want := query.MediaTypes, []item.MediaType{item.MediaTypeVideo}; !reflect.DeepEqual(got, want) {
		t.Fatalf("MediaTypes = %v, want %v", got, want)
	}
	if got, want := query.Genres, []string{"Drama", "Comedy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Genres = %v, want pipe-delimited values %v", got, want)
	}
	if got, want := query.Tags, []string{"featured", "children"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Tags = %v, want pipe-delimited values %v", got, want)
	}
	if got, want := query.Years, []int{2020, 2024}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Years = %v, want %v", got, want)
	}
	if got, want := query.SortBy, []store.ItemSortBy{store.ItemSortBySortName, store.ItemSortByDateCreated}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SortBy = %v, want %v", got, want)
	}
	if got, want := query.SortOrder, []store.SortOrder{store.SortOrderDescending, store.SortOrderAscending}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SortOrder = %v, want %v", got, want)
	}
	if query.StartIndex == nil || *query.StartIndex != 2 || query.Limit == nil || *query.Limit != 5 || !query.Recursive {
		t.Fatalf("page/recursive = %#v", query)
	}
	if !reflect.DeepEqual(query.IsPlayed, &falseValue) || query.MinPremiereDate == nil || !query.MinPremiereDate.Equal(premiere) || query.MinCommunityRating == nil || *query.MinCommunityRating != 8.5 {
		t.Fatalf("scalar query mapping = %#v", query)
	}
	if query.EnableTotalRecordCount || !query.DtoOptions.EnableUserData || query.DtoOptions.EnableImages || query.DtoOptions.ImageTypeLimit != 3 {
		t.Fatalf("response controls = %#v", query.DtoOptions)
	}
	if got, want := query.DtoOptions.ImageTypes, []item.ImageType{item.ImageTypePrimary, item.ImageTypeBackdrop}; !reflect.DeepEqual(got, want) {
		t.Fatalf("image types = %v, want %v", got, want)
	}
	if got, want := query.AudioLanguages, []string{"eng", "spa"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AudioLanguages = %v, want %v", got, want)
	}
	if got, want := query.SubtitleLanguages, []string{"eng"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SubtitleLanguages = %v, want %v", got, want)
	}
	if query.NameStartsWith != "The" || query.NameStartsWithOrGreater != "A" || query.NameLessThan != "Z" {
		t.Fatalf("name-range mapping = %#v", query)
	}
}

func TestItemsQueryRequestMatchesItemsQueryURLSemantics(t *testing.T) {
	first, second, third := itemsBindUUID(t, 501), itemsBindUUID(t, 502), itemsBindUUID(t, 503)
	values := url.Values{
		"ids":        {first.String() + "," + second.String(), "", third.String()},
		"genres":     {"Drama|Comedy", "", "Mystery"},
		"fields":     {"MediaSources,ParentId", "People"},
		"limit":      {""},
		"searchTerm": {""},
	}
	request := httptest.NewRequest("GET", "/?"+values.Encode(), nil)

	want, err := ItemsQuery(request.URL.Query())
	if err != nil {
		t.Fatalf("ItemsQuery: %v", err)
	}
	got, err := ItemsQueryRequest(request, clock.NewFake(time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("ItemsQueryRequest: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ItemsQueryRequest result = %#v, want ItemsQuery result %#v", got, want)
	}
}

// The unaired predicate is relative to the request-time cutoff. It must use
// the injected clock rather than a wall-clock read so a request can be
// reproduced and the store receives the precise inclusive date boundary.
func TestItemsQueryRequestUsesInjectedClockAsUnairedDateCutoff(t *testing.T) {
	start := time.Date(2026, time.July, 27, 12, 34, 56, 789, time.FixedZone("UTC+3", 3*60*60))
	fake := clock.NewFake(start)
	request := httptest.NewRequest("GET", "/?IsUnaired=false", nil)

	query, err := ItemsQueryRequest(request, fake)
	if err != nil {
		t.Fatalf("ItemsQueryRequest: %v", err)
	}
	if query.IsUnaired == nil || *query.IsUnaired {
		t.Fatalf("IsUnaired = %v, want explicit false", query.IsUnaired)
	}
	if query.ReferenceTime == nil || !query.ReferenceTime.Equal(start.UTC()) || query.ReferenceTime.Location() != time.UTC {
		t.Fatalf("ReferenceTime = %v, want injected UTC cutoff %s", query.ReferenceTime, start.UTC())
	}

	fake.Advance(2 * time.Hour)
	query, err = ItemsQueryRequest(request, fake)
	if err != nil {
		t.Fatalf("ItemsQueryRequest after clock advance: %v", err)
	}
	if query.ReferenceTime == nil || !query.ReferenceTime.Equal(fake.Now().UTC()) {
		t.Fatalf("ReferenceTime after clock advance = %v, want %s", query.ReferenceTime, fake.Now().UTC())
	}
}

// TestItemsQueryCarriesFieldsAndSearchTerm pins ItemsController.GetItems: its
// comma-delimited model binder accepts both one CSV value and repeated values,
// then copies fields to DtoOptions and SearchTerm to InternalItemsQuery.
// Swiftfin uses the repeated form, including MediaSources and ParentId; see
// https://github.com/jellyfin/Swiftfin/issues/1611.
func TestItemsQueryCarriesFieldsAndSearchTermForCSVAndRepeatedValues(t *testing.T) {
	wantFields := []string{"MediaSources", "ParentId"}
	first, err := ItemsQuery(url.Values{
		"fIeLdS":     {"MediaSources,ParentId"},
		"sEaRcHtErM": {"needle"},
	})
	if err != nil {
		t.Fatalf("ItemsQuery CSV fields: %v", err)
	}
	second, err := ItemsQuery(url.Values{
		"Fields":     {"MediaSources", "ParentId"},
		"SearchTerm": {"needle"},
	})
	if err != nil {
		t.Fatalf("ItemsQuery repeated fields: %v", err)
	}
	for name, query := range map[string]store.ItemQuery{"CSV": first, "repeated": second} {
		if got := query.DtoOptions.Fields; !reflect.DeepEqual(got, wantFields) {
			t.Errorf("%s Fields = %v, want %v", name, got, wantFields)
		}
		if query.SearchTerm != "needle" {
			t.Errorf("%s SearchTerm = %q, want %q", name, query.SearchTerm, "needle")
		}
	}
	if !reflect.DeepEqual(first.DtoOptions.Fields, second.DtoOptions.Fields) {
		t.Errorf("CSV and repeated Fields differ: %v versus %v", first.DtoOptions.Fields, second.DtoOptions.Fields)
	}
}

func TestItemsQueryForwardsPersonFiltersForCSVAndRepeatedValues(t *testing.T) {
	first, second := itemsBindUUID(t, 41), itemsBindUUID(t, 42)
	wantIDs := []dto.UUID{first, second}
	wantTypes := []string{"Actor", "Director"}

	for _, tc := range []struct {
		name   string
		values url.Values
		person string
	}{
		{
			name: "CSV",
			values: url.Values{
				"pErSoN":      {"Ada Lovelace"},
				"pErSoNiDs":   {first.String() + "," + second.String()},
				"pErSoNtYpEs": {"Actor,Director"},
			},
			person: "Ada Lovelace",
		},
		{
			name: "repeated",
			values: url.Values{
				"PeRsOn":      {"Grace Hopper"},
				"PeRsOnIdS":   {first.String(), second.String()},
				"PeRsOnTyPeS": {"Actor", "Director"},
			},
			person: "Grace Hopper",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, err := ItemsQuery(tc.values)
			if err != nil {
				t.Fatalf("ItemsQuery: %v", err)
			}
			if query.Person != tc.person {
				t.Errorf("Person = %q, want %q", query.Person, tc.person)
			}
			if got := query.PersonIDs; !reflect.DeepEqual(got, wantIDs) {
				t.Errorf("PersonIDs = %v, want %v", got, wantIDs)
			}
			if got := query.PersonTypes; !reflect.DeepEqual(got, wantTypes) {
				t.Errorf("PersonTypes = %v, want %v", got, wantTypes)
			}
		})
	}
}

func TestItemsQueryBindsNullableCollapseBoxSetItems(t *testing.T) {
	trueValue, falseValue := true, false
	for _, tc := range []struct {
		name   string
		values url.Values
		want   *bool
	}{
		{name: "absent", values: url.Values{}},
		{name: "true", values: url.Values{"cOlLaPsEbOxSeTiTeMs": {"TrUe"}}, want: &trueValue},
		{name: "false", values: url.Values{"CoLlApSeBoXsEtItEmS": {"fAlSe"}}, want: &falseValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, err := ItemsQuery(tc.values)
			if err != nil {
				t.Fatalf("ItemsQuery: %v", err)
			}
			if !reflect.DeepEqual(query.CollapseBoxSetItems, tc.want) {
				t.Errorf("CollapseBoxSetItems = %v, want %v", query.CollapseBoxSetItems, tc.want)
			}
		})
	}

	if _, err := ItemsQuery(url.Values{"COLLAPSEboxsetITEMS": {"not-a-bool"}}); err == nil {
		t.Fatal("ItemsQuery invalid CollapseBoxSetItems error = nil, want validation failure")
	}
}

// TestItemsQueryDefaultsImagesOnAndHonorsExplicitFalse pins GetItems'
// enableImages = true source default before DtoOptions are constructed.
func TestItemsQueryDefaultsImagesOnAndHonorsExplicitFalse(t *testing.T) {
	implicit, err := ItemsQuery(url.Values{})
	if err != nil {
		t.Fatalf("ItemsQuery implicit images: %v", err)
	}
	if !implicit.DtoOptions.EnableImages {
		t.Fatal("absent EnableImages = false, want source default true")
	}
	explicit, err := ItemsQuery(url.Values{"eNaBlEiMaGeS": {"false"}})
	if err != nil {
		t.Fatalf("ItemsQuery explicit images: %v", err)
	}
	if explicit.DtoOptions.EnableImages {
		t.Fatal("explicit EnableImages=false = true")
	}
}

// TestItemsQueryDefaultsUserDataOnAndHonorsExplicitFalse pins GetItems'
// enableUserData = true source default before DtoOptions are constructed.
func TestItemsQueryDefaultsUserDataOnAndHonorsExplicitFalse(t *testing.T) {
	implicit, err := ItemsQuery(url.Values{})
	if err != nil {
		t.Fatalf("ItemsQuery implicit user data: %v", err)
	}
	if !implicit.DtoOptions.EnableUserData {
		t.Fatal("absent EnableUserData = false, want source default true")
	}

	explicit, err := ItemsQuery(url.Values{"eNaBlEuSeRdAtA": {"false"}})
	if err != nil {
		t.Fatalf("ItemsQuery explicit user data: %v", err)
	}
	if explicit.DtoOptions.EnableUserData {
		t.Fatal("explicit EnableUserData=false = true")
	}

	if _, err := ItemsQuery(url.Values{"ENABLEuserdata": {"not-a-bool"}}); err == nil {
		t.Fatal("ItemsQuery invalid EnableUserData error = nil, want validation failure")
	}
}

func TestItemsQueryDefaultsRejectsAndAcceptsFortyIDs(t *testing.T) {
	ids := make([]string, 40)
	for index := range ids {
		ids[index] = itemsBindUUID(t, index+100).String()
	}
	query, err := ItemsQuery(url.Values{"ids": {strings.Join(ids, ",")}})
	if err != nil {
		t.Fatalf("ItemsQuery forty ids: %v", err)
	}
	if got, want := len(query.ItemIDs), 40; got != want {
		t.Fatalf("ItemIDs length = %d, want %d", got, want)
	}
	if !query.EnableTotalRecordCount {
		t.Fatal("absent enableTotalRecordCount must retain public default true")
	}
	for _, values := range []url.Values{
		{"ids": {"not-a-guid"}},
		{"limit": {"-1"}},
		{"isPlayed": {"not-bool"}},
	} {
		if _, err := ItemsQuery(values); err == nil {
			t.Fatalf("ItemsQuery(%v) error = nil, want request validation failure", values)
		}
	}
}

// TestItemsQueryMapsEveryImplementedStockControlMissingFromTheOriginalBinder
// is derived from the public GetItems manifest used by the stock-corpus
// replay. Each row is a request control that reaches an already-supported
// ItemQuery predicate but was previously discarded at the HTTP boundary.
//
// Keep these as individual request shapes rather than constructing ItemQuery
// directly: case-insensitive names, CSV/repeated arrays, and nullable false
// values are all part of the public controller contract.
func TestItemsQueryMapsEveryImplementedStockControlMissingFromTheOriginalBinder(t *testing.T) {
	trueValue, falseValue := true, false
	date := time.Date(2024, 7, 27, 12, 34, 56, 0, time.UTC)
	tests := []struct {
		name   string
		values url.Values
		check  func(*testing.T, store.ItemQuery)
	}{
		{
			name:   "IsPlaceHolder explicit false",
			values: url.Values{"iSpLaCeHoLdEr": {"false"}},
			check: func(t *testing.T, query store.ItemQuery) {
				if !reflect.DeepEqual(query.IsPlaceHolder, &falseValue) {
					t.Fatalf("IsPlaceHolder = %v, want explicit false", query.IsPlaceHolder)
				}
			},
		},
		{
			name:   "VideoTypes repeated and CSV",
			values: url.Values{"vIdEoTyPeS": {"BluRay,VideoFile", "Iso"}},
			check: func(t *testing.T, query store.ItemQuery) {
				want := []item.VideoType{item.VideoTypeBluRay, item.VideoTypeVideoFile, item.VideoTypeIso}
				if !reflect.DeepEqual(query.VideoTypes, want) {
					t.Fatalf("VideoTypes = %v, want %v", query.VideoTypes, want)
				}
			},
		},
		{
			name:   "HasSubtitles explicit false",
			values: url.Values{"hAsSuBtItLeS": {"false"}},
			check: func(t *testing.T, query store.ItemQuery) {
				if !reflect.DeepEqual(query.HasSubtitles, &falseValue) {
					t.Fatalf("HasSubtitles = %v, want explicit false", query.HasSubtitles)
				}
			},
		},
		{
			name:   "IsMissing explicit false",
			values: url.Values{"iSmIsSiNg": {"false"}},
			check: func(t *testing.T, query store.ItemQuery) {
				if !reflect.DeepEqual(query.IsMissing, &falseValue) {
					t.Fatalf("IsMissing = %v, want explicit false", query.IsMissing)
				}
			},
		},
		{
			name:   "IsUnaired explicit false",
			values: url.Values{"iSuNaIrEd": {"false"}},
			check: func(t *testing.T, query store.ItemQuery) {
				if !reflect.DeepEqual(query.IsUnaired, &falseValue) {
					t.Fatalf("IsUnaired = %v, want explicit false", query.IsUnaired)
				}
			},
		},
		{
			name:   "HasImdbId explicit false",
			values: url.Values{"hAsImDbId": {"false"}},
			check: func(t *testing.T, query store.ItemQuery) {
				if !reflect.DeepEqual(query.HasIMDbID, &falseValue) {
					t.Fatalf("HasIMDbID = %v, want explicit false", query.HasIMDbID)
				}
			},
		},
		{
			name:   "HasTmdbId explicit false",
			values: url.Values{"hAsTmDbId": {"false"}},
			check: func(t *testing.T, query store.ItemQuery) {
				if !reflect.DeepEqual(query.HasTMDbID, &falseValue) {
					t.Fatalf("HasTMDbID = %v, want explicit false", query.HasTMDbID)
				}
			},
		},
		{
			name:   "IsFavorite explicit false",
			values: url.Values{"iSfAvOrItE": {"false"}},
			check: func(t *testing.T, query store.ItemQuery) {
				if !reflect.DeepEqual(query.IsFavorite, &falseValue) {
					t.Fatalf("IsFavorite = %v, want explicit false", query.IsFavorite)
				}
			},
		},
		{
			name:   "MaxPremiereDate",
			values: url.Values{"mAxPrEmIeReDaTe": {date.Format(time.RFC3339)}},
			check: func(t *testing.T, query store.ItemQuery) {
				if query.MaxPremiereDate == nil || !query.MaxPremiereDate.Equal(date) {
					t.Fatalf("MaxPremiereDate = %v, want %s", query.MaxPremiereDate, date)
				}
			},
		},
		{
			name:   "MinDateLastSavedForUser",
			values: url.Values{"mInDaTeLaStSaVeDfOrUsEr": {date.Format(time.RFC3339)}},
			check: func(t *testing.T, query store.ItemQuery) {
				if query.MinDateLastSavedForUser == nil || !query.MinDateLastSavedForUser.Equal(date) {
					t.Fatalf("MinDateLastSavedForUser = %v, want %s", query.MinDateLastSavedForUser, date)
				}
			},
		},
		{name: "Filters IsFolder", values: url.Values{"fIlTeRs": {"IsFolder"}}, check: func(t *testing.T, query store.ItemQuery) {
			if !reflect.DeepEqual(query.IsFolder, &trueValue) {
				t.Fatalf("Filters=IsFolder ItemQuery = %#v", query)
			}
		}},
		{name: "Filters IsNotFolder", values: url.Values{"fIlTeRs": {"IsNotFolder"}}, check: func(t *testing.T, query store.ItemQuery) {
			if !reflect.DeepEqual(query.IsFolder, &falseValue) {
				t.Fatalf("Filters=IsNotFolder ItemQuery = %#v", query)
			}
		}},
		{name: "Filters IsPlayed", values: url.Values{"fIlTeRs": {"IsPlayed"}}, check: func(t *testing.T, query store.ItemQuery) {
			if !reflect.DeepEqual(query.IsPlayed, &trueValue) {
				t.Fatalf("Filters=IsPlayed ItemQuery = %#v", query)
			}
		}},
		{name: "Filters IsUnplayed", values: url.Values{"fIlTeRs": {"IsUnplayed"}}, check: func(t *testing.T, query store.ItemQuery) {
			if !reflect.DeepEqual(query.IsPlayed, &falseValue) {
				t.Fatalf("Filters=IsUnplayed ItemQuery = %#v", query)
			}
		}},
		{name: "Filters IsFavorite", values: url.Values{"fIlTeRs": {"IsFavorite"}}, check: func(t *testing.T, query store.ItemQuery) {
			if !reflect.DeepEqual(query.IsFavorite, &trueValue) {
				t.Fatalf("Filters=IsFavorite ItemQuery = %#v", query)
			}
		}},
		{name: "Filters IsResumable", values: url.Values{"fIlTeRs": {"IsResumable"}}, check: func(t *testing.T, query store.ItemQuery) {
			if !reflect.DeepEqual(query.IsResumable, &trueValue) {
				t.Fatalf("Filters=IsResumable ItemQuery = %#v", query)
			}
		}},
		{name: "Filters Likes", values: url.Values{"fIlTeRs": {"Likes"}}, check: func(t *testing.T, query store.ItemQuery) {
			if !reflect.DeepEqual(query.IsLiked, &trueValue) {
				t.Fatalf("Filters=Likes ItemQuery = %#v", query)
			}
		}},
		{name: "Filters Dislikes", values: url.Values{"fIlTeRs": {"Dislikes"}}, check: func(t *testing.T, query store.ItemQuery) {
			if !reflect.DeepEqual(query.IsLiked, &falseValue) {
				t.Fatalf("Filters=Dislikes ItemQuery = %#v", query)
			}
		}},
		{name: "Filters IsFavoriteOrLikes", values: url.Values{"fIlTeRs": {"IsFavoriteOrLikes"}}, check: func(t *testing.T, query store.ItemQuery) {
			if !reflect.DeepEqual(query.IsFavoriteOrLiked, &trueValue) {
				t.Fatalf("Filters=IsFavoriteOrLikes ItemQuery = %#v", query)
			}
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			query, err := ItemsQuery(tc.values)
			if err != nil {
				t.Fatalf("ItemsQuery(%v): %v", tc.values, err)
			}
			tc.check(t, query)
		})
	}

	for _, name := range []string{"IsPlaceHolder", "HasSubtitles", "IsMissing", "IsUnaired", "HasImdbId", "HasTmdbId", "IsFavorite", "MaxPremiereDate", "MinDateLastSavedForUser"} {
		t.Run(name+" omitted", func(t *testing.T) {
			query, err := ItemsQuery(url.Values{})
			if err != nil {
				t.Fatalf("ItemsQuery: %v", err)
			}
			switch name {
			case "IsPlaceHolder":
				if query.IsPlaceHolder != nil {
					t.Fatalf("omitted %s = %v, want nil", name, query.IsPlaceHolder)
				}
			case "HasSubtitles":
				if query.HasSubtitles != nil {
					t.Fatalf("omitted %s = %v, want nil", name, query.HasSubtitles)
				}
			case "IsMissing":
				if query.IsMissing != nil {
					t.Fatalf("omitted %s = %v, want nil", name, query.IsMissing)
				}
			case "IsUnaired":
				if query.IsUnaired != nil {
					t.Fatalf("omitted %s = %v, want nil", name, query.IsUnaired)
				}
			case "HasImdbId":
				if query.HasIMDbID != nil {
					t.Fatalf("omitted %s = %v, want nil", name, query.HasIMDbID)
				}
			case "HasTmdbId":
				if query.HasTMDbID != nil {
					t.Fatalf("omitted %s = %v, want nil", name, query.HasTMDbID)
				}
			case "IsFavorite":
				if query.IsFavorite != nil {
					t.Fatalf("omitted %s = %v, want nil", name, query.IsFavorite)
				}
			case "MaxPremiereDate":
				if query.MaxPremiereDate != nil {
					t.Fatalf("omitted %s = %v, want nil", name, query.MaxPremiereDate)
				}
			case "MinDateLastSavedForUser":
				if query.MinDateLastSavedForUser != nil {
					t.Fatalf("omitted %s = %v, want nil", name, query.MinDateLastSavedForUser)
				}
			}
		})
	}

	query, err := ItemsQuery(url.Values{"VideoTypes": {""}, "Filters": {""}})
	if err != nil {
		t.Fatalf("ItemsQuery empty arrays: %v", err)
	}
	if len(query.VideoTypes) != 0 || query.IsFolder != nil || query.IsPlayed != nil || query.IsFavorite != nil || query.IsResumable != nil || query.IsLiked != nil || query.IsFavoriteOrLiked != nil {
		t.Fatalf("empty arrays ItemQuery = %#v, want no filter", query)
	}
}

// TestItemsQueryPairsEachSortOrderWithItsSameIndexSortBy guards the public
// repeated-value form as well as CSV: independent direction entries must
// survive to the store in their original index order.
func TestItemsQueryPairsEachSortOrderWithItsSameIndexSortBy(t *testing.T) {
	for _, values := range []url.Values{
		{"sOrTbY": {"SortName,DateCreated"}, "SoRtOrDeR": {"Descending", "Ascending"}},
		{"SortBy": {"SortName", "DateCreated"}, "SortOrder": {"Descending,Ascending"}},
	} {
		query, err := ItemsQuery(values)
		if err != nil {
			t.Fatalf("ItemsQuery(%v): %v", values, err)
		}
		if want := []store.ItemSortBy{store.ItemSortBySortName, store.ItemSortByDateCreated}; !reflect.DeepEqual(query.SortBy, want) {
			t.Fatalf("SortBy = %v, want %v", query.SortBy, want)
		}
		if want := []store.SortOrder{store.SortOrderDescending, store.SortOrderAscending}; !reflect.DeepEqual(query.SortOrder, want) {
			t.Fatalf("SortOrder = %v, want independent directions %v", query.SortOrder, want)
		}
	}
}

// TestItemsPublicParameterManifestClassifiesEveryPublicWireName keeps the
// binder's supported subset anchored to the GetItems public manifest. Names
// absent from ItemQuery are explicit: userId is handler-owned and all other
// names are not represented by the implemented store query. This prevents a
// new binder tag from becoming an unreviewed request semantic.
func TestItemsPublicParameterManifestClassifiesEveryPublicWireName(t *testing.T) {
	const public = `userId maxOfficialRating hasThemeSong hasThemeVideo hasSubtitles hasSpecialFeature hasTrailer adjacentTo indexNumber parentIndexNumber hasParentalRating isHd is4K locationTypes excludeLocationTypes isMissing isUnaired minCommunityRating minCriticRating minPremiereDate minDateLastSaved minDateLastSavedForUser maxPremiereDate hasOverview hasImdbId hasTmdbId hasTvdbId isMovie isSeries isNews isKids isSports excludeItemIds startIndex limit recursive searchTerm sortOrder parentId fields excludeItemTypes includeItemTypes filters isFavorite mediaTypes imageTypes sortBy isPlayed genres officialRatings tags years enableUserData imageTypeLimit enableImageTypes person personIds personTypes studios artists excludeArtistIds artistIds albumArtistIds contributingArtistIds albums albumIds ids videoTypes minOfficialRating isLocked isPlaceHolder hasOfficialRating collapseBoxSetItems minWidth minHeight maxWidth maxHeight is3D seriesStatus nameStartsWithOrGreater nameStartsWith nameLessThan studioIds genreIds audioLanguages subtitleLanguages enableTotalRecordCount enableImages`
	const bound = `adjacentTo albumArtistIds artistIds audioLanguages collapseBoxSetItems contributingArtistIds enableImages enableImageTypes enableTotalRecordCount enableUserData excludeArtistIds excludeItemIds excludeItemTypes fields filters genreIds genres hasImdbId hasSubtitles hasTmdbId ids imageTypeLimit includeItemTypes isFavorite isMissing isPlaceHolder isPlayed isUnaired limit maxPremiereDate mediaTypes minCommunityRating minDateLastSavedForUser minPremiereDate nameLessThan nameStartsWith nameStartsWithOrGreater parentId person personIds personTypes recursive searchTerm sortBy sortOrder startIndex studioIds studios subtitleLanguages tags videoTypes years`

	manifest := make(map[string]struct{})
	for _, name := range strings.Fields(public) {
		key := strings.ToLower(name)
		if _, exists := manifest[key]; exists {
			t.Fatalf("public Items manifest repeats %q", name)
		}
		manifest[key] = struct{}{}
	}
	boundNames := make(map[string]struct{})
	for _, name := range strings.Fields(bound) {
		key := strings.ToLower(name)
		if _, exists := manifest[key]; !exists {
			t.Fatalf("bound Items parameter %q is absent from public manifest", name)
		}
		boundNames[key] = struct{}{}
	}

	typeOfRequest := reflect.TypeOf(itemQueryRequest{})
	boundTags := make(map[string]struct{}, typeOfRequest.NumField())
	for index := range typeOfRequest.NumField() {
		name := typeOfRequest.Field(index).Tag.Get("query")
		if name == "" {
			t.Fatalf("itemQueryRequest field %s has no public query tag", typeOfRequest.Field(index).Name)
		}
		key := strings.ToLower(name)
		if _, public := manifest[key]; !public {
			t.Fatalf("itemQueryRequest tag %q is absent from public Items manifest", name)
		}
		if _, duplicate := boundTags[key]; duplicate {
			t.Fatalf("itemQueryRequest repeats public tag %q", name)
		}
		boundTags[key] = struct{}{}
	}
	if !reflect.DeepEqual(boundTags, boundNames) {
		t.Fatalf("Items binder tags = %v, want manifest-derived implemented controls %v", boundTags, boundNames)
	}
	for name := range manifest {
		_, isBound := boundNames[name]
		if name == "userid" || isBound {
			continue
		}
		if _, accidentallyBound := boundTags[name]; accidentallyBound {
			t.Fatalf("public Items parameter %q is tagged despite having no implemented ItemQuery store control", name)
		}
	}
}

func itemsBindUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing fixture UUID %d: %v", value, err)
	}
	return id
}

// TestItemsQueryBindsNamedEntityFilters covers the genre, studio, and artist
// filters that person, genre, and studio pages send.
func TestItemsQueryBindsNamedEntityFilters(t *testing.T) {
	genre, studio, artist, albumArtist, contributor, excluded := itemsBindUUID(t, 1), itemsBindUUID(t, 2), itemsBindUUID(t, 3), itemsBindUUID(t, 4), itemsBindUUID(t, 5), itemsBindUUID(t, 6)
	query, err := ItemsQuery(url.Values{
		"genreIds":              {genre.String()},
		"StudioIds":             {studio.String()},
		"studios":               {"A24|Pixar"},
		"artistIds":             {artist.String()},
		"albumArtistIds":        {albumArtist.String()},
		"contributingArtistIds": {contributor.String()},
		"excludeArtistIds":      {excluded.String()},
	})
	if err != nil {
		t.Fatalf("ItemsQuery: %v", err)
	}
	for name, pair := range map[string][2][]dto.UUID{
		"GenreIDs":              {query.GenreIDs, {genre}},
		"StudioIDs":             {query.StudioIDs, {studio}},
		"ArtistIDs":             {query.ArtistIDs, {artist}},
		"AlbumArtistIDs":        {query.AlbumArtistIDs, {albumArtist}},
		"ContributingArtistIDs": {query.ContributingArtistIDs, {contributor}},
		"ExcludeArtistIDs":      {query.ExcludeArtistIDs, {excluded}},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s = %v, want %v", name, pair[0], pair[1])
		}
	}
	if !reflect.DeepEqual(query.Studios, []string{"A24", "Pixar"}) {
		t.Errorf("Studios = %#v, want pipe-delimited names", query.Studios)
	}
}
