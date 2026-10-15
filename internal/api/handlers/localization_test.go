// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/localization"
)

func TestLocalizationRoutesDeclareStockOperationsAndDefaultSetupPolicy(t *testing.T) {
	service := newLocalizationService(t)
	rt, manager, _ := newManagedRouter(t)
	handlers.RegisterLocalization(rt, service, manager)

	for _, want := range []router.Route{
		{Method: http.MethodGet, Pattern: "/Localization/Countries", OperationID: "GetCountries", Policy: string(auth.PolicyFirstTimeSetupOrDefault)},
		{Method: http.MethodGet, Pattern: "/Localization/Cultures", OperationID: "GetCultures", Policy: string(auth.PolicyFirstTimeSetupOrDefault)},
		{Method: http.MethodGet, Pattern: "/Localization/ParentalRatings", OperationID: "GetParentalRatings", Policy: string(auth.PolicyFirstTimeSetupOrDefault)},
		{Method: http.MethodGet, Pattern: "/Localization/Options", OperationID: "GetLocalizationOptions", Policy: string(auth.PolicyFirstTimeSetupOrDefault)},
	} {
		t.Run(want.OperationID, func(t *testing.T) {
			got, found := routeByOperation(rt.Routes(), want.OperationID)
			if !found {
				t.Fatalf("operation %q is not registered", want.OperationID)
			}
			if got.Method != want.Method || got.Pattern != want.Pattern || got.Policy != want.Policy || got.Anonymous {
				t.Errorf("route %q = %#v, want method=%q pattern=%q policy=%q and non-anonymous", want.OperationID, got, want.Method, want.Pattern, want.Policy)
			}
		})
	}
}

func TestLocalizationEndpointsRenderPinnedPayloadsWithNegotiatedCasing(t *testing.T) {
	service := newLocalizationService(t)
	rt, manager, _ := newManagedRouter(t)
	seedSystem(t, manager, config.ServerConfiguration{MetadataCountryCode: "DE"})
	handlers.RegisterLocalization(rt, service, manager)

	for _, tc := range []struct {
		name       string
		path       string
		accept     string
		count      int
		first      map[string]any
		wrongField string
	}{
		{
			name: "countries Pascal profile and normalized route", path: "/lOcAlIzAtIoN/cOuNtRiEs", count: 140,
			first: map[string]any{"Name": "AF", "DisplayName": "Afghanistan", "TwoLetterISORegionName": "AF", "ThreeLetterISORegionName": "AFG"}, wrongField: "name",
		},
		{
			name: "cultures Camel profile and normalized route", path: "/LOCALIZATION/cUlTuReS", accept: `application/json; profile="CamelCase"`, count: 494,
			first: map[string]any{"name": "Abkhazian", "displayName": "Abkhazian", "twoLetterISOLanguageName": "ab", "threeLetterISOLanguageName": "abk", "threeLetterISOLanguageNames": []any{"abk"}}, wrongField: "DisplayName",
		},
		{
			name: "options Camel profile and normalized route", path: "/LoCaLiZaTiOn/oPtIoNs", accept: `application/json; profile="CamelCase"`, count: 105,
			first: map[string]any{"name": "Abkhazian", "value": "ab"}, wrongField: "Name",
		},
		{
			name: "ratings Pascal profile uses configured country", path: "/localization/PARENTALRATINGS", count: 24,
			first: map[string]any{"Name": "Unrated"}, wrongField: "name",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodGet, tc.path, "", "", tc.accept)
			assertStatus(t, rec, http.StatusOK)
			wantType := "application/json; charset=utf-8"
			if tc.accept != "" {
				wantType = `application/json; profile="CamelCase"; charset=utf-8`
			}
			if got := rec.Header().Get("Content-Type"); got != wantType {
				t.Errorf("%s Content-Type = %q, want %q", tc.path, got, wantType)
			}
			values := decodeJSONArray(t, rec.Body.Bytes())
			if got := len(values); got != tc.count {
				t.Fatalf("GET %s array count = %d, want %d", tc.path, got, tc.count)
			}
			assertJSONMap(t, values[0], tc.first)
			if _, exists := values[0][tc.wrongField]; exists {
				t.Errorf("GET %s unexpectedly rendered %q in the wrong casing: %s", tc.path, tc.wrongField, rec.Body.String())
			}
		})
	}
}

func TestLocalizationCulturesDeduplicateDisplayNames(t *testing.T) {
	service := newLocalizationService(t)
	rt, manager, _ := newManagedRouter(t)
	handlers.RegisterLocalization(rt, service, manager)

	rec := request(rt, http.MethodGet, "/Localization/Cultures", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	cultures := decodeJSONArray(t, rec.Body.Bytes())
	if got, want := len(cultures), 494; got != want {
		t.Fatalf("GET Cultures count = %d, want %d distinct display names", got, want)
	}
	seen := make(map[string]struct{}, len(cultures))
	for _, culture := range cultures {
		display, _ := culture["DisplayName"].(string)
		key := strings.ToLower(display)
		if _, duplicate := seen[key]; duplicate {
			t.Errorf("GET Cultures retained duplicate display name %q", display)
		}
		seen[key] = struct{}{}
	}
	for _, want := range []struct {
		name, display string
	}{
		{name: "zh-cn", display: "Chinese (Simplified)"},
		{name: "zh-tw", display: "Chinese (Traditional)"},
	} {
		count := 0
		for _, culture := range cultures {
			if culture["DisplayName"] == want.display {
				count++
				if culture["Name"] != want.name {
					t.Errorf("first source culture retained for %q has Name=%q, want %q", want.display, culture["Name"], want.name)
				}
			}
		}
		if count != 1 {
			t.Errorf("GET Cultures display %q count = %d, want one after case-insensitive distinct", want.display, count)
		}
	}
}

func TestLocalizationParentalRatingScorePreservesPinnedNestedWireNames(t *testing.T) {
	service := newLocalizationService(t)
	rt, manager, _ := newManagedRouter(t)
	seedSystem(t, manager, config.ServerConfiguration{MetadataCountryCode: "DE"})
	handlers.RegisterLocalization(rt, service, manager)

	for _, tc := range []struct {
		name, accept, key string
		unrated, scored   string
	}{
		{
			name: "Pascal outer DTO fields", key: "Name",
			unrated: `{"Name":"Unrated"}`,
			scored:  `{"Name":"FSK-12","Value":12,"RatingScore":{"score":12}}`,
		},
		{
			name: "Camel outer DTO fields", accept: `application/json; profile="CamelCase"`, key: "name",
			unrated: `{"name":"Unrated"}`,
			scored:  `{"name":"FSK-12","value":12,"ratingScore":{"score":12}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(rt, http.MethodGet, "/Localization/ParentalRatings", "", "", tc.accept)
			assertStatus(t, rec, http.StatusOK)
			if got := string(rawJSONRatingByName(t, rec.Body.Bytes(), tc.key, "Unrated")); got != tc.unrated {
				t.Errorf("Unrated raw JSON = %s, want stock omission shape %s", got, tc.unrated)
			}
			if got := string(rawJSONRatingByName(t, rec.Body.Bytes(), tc.key, "FSK-12")); got != tc.scored {
				t.Errorf("FSK-12 raw JSON = %s, want stock omission shape %s", got, tc.scored)
			}
		})
	}
}

func TestLocalizationParentalRatingsUsesConfiguredCountryAndRejectsUnsupportedCountryQuery(t *testing.T) {
	service := newLocalizationService(t)
	rt, manager, _ := newManagedRouter(t)
	seedSystem(t, manager, config.ServerConfiguration{MetadataCountryCode: "DE"})
	handlers.RegisterLocalization(rt, service, manager)

	// The pinned OpenAPI operation has no country-code query parameter. The
	// configured country selects the set; an unrecognized query cannot switch it.
	rec := request(rt, http.MethodGet, "/LoCaLiZaTiOn/PaReNtAlRaTiNgS?countryCode=US", "", "", `application/json; profile="CamelCase"`)
	assertStatus(t, rec, http.StatusOK)
	ratings := decodeJSONArray(t, rec.Body.Bytes())
	if got, want := len(ratings), 24; got != want {
		t.Fatalf("configured DE ratings count = %d, want %d; query must not select US", got, want)
	}
	if !containsJSONNamed(ratings, "name", "FSK-12") || containsJSONNamed(ratings, "name", "TV-MA") {
		t.Errorf("configured DE response did not retain FSK system while ignoring countryCode query: %s", rec.Body.String())
	}
	fsk, found := jsonNamed(ratings, "name", "FSK-12")
	if !found {
		t.Fatal("FSK-12 missing")
	}
	assertJSONMap(t, fsk, map[string]any{"name": "FSK-12", "value": 12, "ratingScore": map[string]any{"score": 12}})
	if _, exists := fsk["RatingScore"]; exists {
		t.Errorf("Camel ratings response retained Pascal nested field: %s", rec.Body.String())
	}

	seedSystem(t, manager, config.ServerConfiguration{MetadataCountryCode: "ZZ"})
	rec = request(rt, http.MethodGet, "/Localization/ParentalRatings", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	ratings = decodeJSONArray(t, rec.Body.Bytes())
	if got, want := len(ratings), 8; got != want {
		t.Fatalf("unknown configured country ratings count = %d, want mandatory-only %d", got, want)
	}
	for index, name := range []string{"Unrated", "Approved", "10", "13", "14", "21", "XXX", "Banned"} {
		if got := ratings[index]["Name"]; got != name {
			t.Errorf("unknown country ratings[%d] Name = %#v, want %q", index, got, name)
		}
	}
}

func TestWizardLocalizationGermanRatingsPreserveNullsValuesAndSourceTieOrder(t *testing.T) {
	service := newLocalizationService(t)
	rt, manager, _ := newManagedRouter(t)
	seedSystem(t, manager, config.ServerConfiguration{MetadataCountryCode: "DE"})
	handlers.RegisterLocalization(rt, service, manager)

	rec := request(rt, http.MethodGet, "/Localization/ParentalRatings", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	var ratings []json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &ratings); err != nil {
		t.Fatalf("decode German ratings: %v", err)
	}
	if got, want := string(ratings[0]), `{"Name":"Unrated"}`; got != want {
		t.Errorf("Unrated wire shape = %s, want Value and RatingScore omitted", got)
	}
	for index, want := range []string{"0", "FSK 0", "FSK-0", "Educational", "Infoprogramm", "6", "FSK 6", "FSK-6", "10", "12", "FSK 12", "FSK-12"} {
		var rating map[string]json.RawMessage
		if err := json.Unmarshal(ratings[index+1], &rating); err != nil {
			t.Fatalf("decode ratings[%d]: %v", index+1, err)
		}
		if got := string(rating["Name"]); got != `"`+want+`"` {
			t.Errorf("ratings[%d] Name = %s, want %q", index+1, got, want)
		}
		if string(rating["Value"]) == "null" || string(rating["RatingScore"]) == "null" {
			t.Errorf("rated entry %q omitted its source-derived value or score: %s", want, ratings[index+1])
		}
	}
	if got := string(rawJSONRatingByName(t, rec.Body.Bytes(), "Name", "10")); got != `{"Name":"10","Value":10,"RatingScore":{"score":10}}` {
		t.Errorf("mandatory 10 rating = %s, want omitted nested subScore", got)
	}
}

func TestWizardLocalizationOptionsRetainPinnedValuesAndOrdering(t *testing.T) {
	service := newLocalizationService(t)
	rt, manager, _ := newManagedRouter(t)
	handlers.RegisterLocalization(rt, service, manager)
	rec := request(rt, http.MethodGet, "/Localization/Options", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	options := decodeJSONArray(t, rec.Body.Bytes())
	if got, want := len(options), 105; got != want {
		t.Errorf("Options count = %d, want stock fixture count %d with Melayu/ms retained after Malti", got, want)
	}
	for _, tc := range []struct {
		index       int
		name, value string
	}{
		{0, "Abkhazian", "ab"},
		{1, "Afrikaans", "af"},
		{2, "azərbaycan", "az"},
		{3, "bosanski", "bs"},
		{12, "English", "en-US"},
		{16, "español (Latinoamérica)", "es_419"},
		{39, "Malti", "mt"},
		{40, "Melayu", "ms"},
		{51, "pr", "pr"},
		{104, "한국어", "ko"},
	} {
		if got := options[tc.index]; len(got) != 2 || got["Name"] != tc.name || got["Value"] != tc.value {
			t.Errorf("Options[%d] = %#v, want exact two-field %#v", tc.index, got, map[string]string{"Name": tc.name, "Value": tc.value})
		}
	}
	if containsJSONNamed(options, "Name", "Bahasa Melayu") {
		t.Errorf("Options unexpectedly contains excluded stock entry Bahasa Melayu/ms: %s", rec.Body.String())
	}
}

func newLocalizationService(t *testing.T) *localization.Service {
	t.Helper()
	service, err := localization.NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

func routeByOperation(routes []router.Route, operation string) (router.Route, bool) {
	for _, route := range routes {
		if route.OperationID == operation {
			return route, true
		}
	}
	return router.Route{}, false
}

func decodeJSONArray(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var values []map[string]any
	if err := json.Unmarshal(body, &values); err != nil {
		t.Fatalf("response is not a JSON array: %v; body=%q", err, body)
	}
	return values
}

func rawJSONRatingByName(t *testing.T, body []byte, nameKey, name string) json.RawMessage {
	t.Helper()
	var entries []json.RawMessage
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("response is not a JSON rating array: %v; body=%q", err, body)
	}
	for _, entry := range entries {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry, &fields); err != nil {
			t.Fatalf("rating entry is not a JSON object: %v; body=%s", err, entry)
		}
		var actual string
		if field, exists := fields[nameKey]; exists && json.Unmarshal(field, &actual) == nil && actual == name {
			return entry
		}
	}
	t.Fatalf("rating %q with name field %q missing from %s", name, nameKey, body)
	return nil
}

func assertJSONMap(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	for key, expected := range want {
		actual, exists := got[key]
		if !exists {
			t.Errorf("JSON object missing %q: %#v", key, got)
			continue
		}
		wantJSON, err := json.Marshal(expected)
		if err != nil {
			t.Fatalf("marshal expected %q: %v", key, err)
		}
		gotJSON, err := json.Marshal(actual)
		if err != nil {
			t.Fatalf("marshal actual %q: %v", key, err)
		}
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("JSON field %q = %s, want %s", key, gotJSON, wantJSON)
		}
	}
}

func containsJSONNamed(values []map[string]any, key, name string) bool {
	_, found := jsonNamed(values, key, name)
	return found
}

func jsonNamed(values []map[string]any, key, name string) (map[string]any, bool) {
	for _, value := range values {
		if value[key] == name {
			return value, true
		}
	}
	return nil, false
}
