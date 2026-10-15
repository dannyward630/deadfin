// SPDX-License-Identifier: GPL-2.0-only

package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/attest"
	"github.com/deadfin-org/deadfin/internal/dto"
)

func TestCamelCasePolicy(t *testing.T) {
	// Mirrors System.Text.Json JsonNamingPolicy.CamelCase (FixCasing).
	cases := map[string]string{
		"Name":               "name",
		"ProviderIds":        "providerIds",
		"ID":                 "id",
		"URLValue":           "urlValue",
		"IsHD":               "isHD",
		"RunTimeTicks":       "runTimeTicks",
		"already":            "already",
		"A":                  "a",
		"ABTest":             "abTest",
		"DateLastMediaAdded": "dateLastMediaAdded",
	}
	for in, want := range cases {
		if got := camelCase(in); got != want {
			t.Errorf("camelCase(%q) = %q, want %q", in, got, want)
		}
	}
}

type sampleInner struct {
	SeasonName string `json:"SeasonName,omitzero"`
}

type sampleDTO struct {
	Name            string            `json:"Name"`
	RunTimeTicks    *int64            `json:"RunTimeTicks,omitzero"`
	ProviderIds     map[string]string `json:"ProviderIds,omitzero"`
	ImageTags       map[string]string `json:"ImageTags,omitzero"`
	Genres          []string          `json:"Genres,omitzero"`
	ID              dto.UUID          `json:"Id"`
	Premiere        dto.Time          `json:"PremiereDate,omitzero"`
	Inner           sampleInner       `json:"UserData,omitzero"`
	Skipped         string            `json:"-"`
	IsFolder        bool              `json:"IsFolder"`
	CommunityRating *float32          `json:"CommunityRating,omitzero"`
}

// THE LOCK TEST: map keys are never re-cased by the CamelCase profile.
// ProviderIds keys like "Tmdb" and ImageTags keys like "Primary" must survive
// byte-for-byte. Do not "fix" this — upstream has no DictionaryKeyPolicy.
func TestCamelMarshal_MapKeysUntouched(t *testing.T) {
	id, _ := dto.ParseUUID("8a1b4a2f9dbe4f379f6b2b1c3d4e5f60")
	ticks := int64(12345)
	v := sampleDTO{
		Name:         "The Thing",
		RunTimeTicks: &ticks,
		ProviderIds:  map[string]string{"Tmdb": "603", "Imdb": "tt0084787"},
		ImageTags:    map[string]string{"Primary": "abc123"},
		Genres:       []string{"Horror"},
		ID:           id,
		IsFolder:     false,
	}
	out, err := MarshalCamel(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"The Thing","runTimeTicks":12345,` +
		`"providerIds":{"Imdb":"tt0084787","Tmdb":"603"},` +
		`"imageTags":{"Primary":"abc123"},"genres":["Horror"],` +
		`"id":"8a1b4a2f9dbe4f379f6b2b1c3d4e5f60","isFolder":false}`
	if string(out) != want {
		t.Errorf("got:  %s\nwant: %s", out, want)
	}
}

func TestCamelMarshal_OmissionAndNesting(t *testing.T) {
	v := sampleDTO{Name: "x", Inner: sampleInner{SeasonName: "S1"}}
	out, err := MarshalCamel(v)
	if err != nil {
		t.Fatal(err)
	}
	// Id carries no omitzero (C# non-nullable Guid => always emitted), so the
	// zero GUID appears — that's the DTO mapping rule working as intended.
	want := `{"name":"x","id":"00000000000000000000000000000000",` +
		`"userData":{"seasonName":"S1"},"isFolder":false}`
	if string(out) != want {
		t.Errorf("got:  %s\nwant: %s", out, want)
	}

	// Valid JSON always.
	var chk any
	if err := json.Unmarshal(out, &chk); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
}

func TestNegotiateCasing_StockFormatterSelection(t *testing.T) {
	// Recorded from the pinned stock formatter probe through difftool. ASP.NET
	// selects the highest-quality matching formatter, retaining header order for
	// ties. A zero-quality candidate remains selectable when it is the only one.
	cases := []struct {
		name   string
		accept string
		want   Casing
	}{
		{name: "absent Accept defaults Pascal", want: CasingPascal},
		{name: "unprofiled JSON defaults Pascal", accept: `application/json`, want: CasingPascal},
		{name: "CamelCase profile", accept: `application/json; profile="CamelCase"`, want: CasingCamel},
		{name: "media type and profile are case insensitive", accept: `Application/JSON; PROFILE=camelcase`, want: CasingCamel},
		{name: "first equal quality bare JSON wins", accept: `application/json, application/json; profile=CamelCase`, want: CasingPascal},
		{name: "first equal quality profile wins", accept: `application/json; profile=CamelCase, application/json`, want: CasingCamel},
		{name: "higher quality bare JSON wins", accept: `application/json; profile=CamelCase; q=0.1, application/json; q=1`, want: CasingPascal},
		{name: "higher quality CamelCase wins", accept: `application/json; q=0.1, application/json; profile=CamelCase; q=1`, want: CasingCamel},
		{name: "zero quality only CamelCase candidate still wins", accept: `application/json; profile=CamelCase; q=0`, want: CasingCamel},
		{name: "equal quality retains first entry", accept: `application/json; profile=CamelCase; q=0.5, application/json; q=0.5`, want: CasingCamel},
		{name: "application wildcard selects CamelCase", accept: `application/*; profile=CamelCase`, want: CasingCamel},
		{name: "global wildcard selects CamelCase", accept: `*/*; profile=CamelCase`, want: CasingCamel},
		{name: "structured JSON suffix does not select", accept: `application/*+json; profile=CamelCase`, want: CasingPascal},
		{name: "non JSON wildcard does not select", accept: `text/*; profile=CamelCase`, want: CasingPascal},
		{name: "unknown parameter before quality prevents profile match", accept: `application/json; charset=utf-8; profile=CamelCase`, want: CasingPascal},
		{name: "profile after quality is an accept extension", accept: `application/json; q=1; profile=CamelCase`, want: CasingPascal},
		{name: "malformed profile parameter", accept: `application/json; profile`, want: CasingPascal},
		{name: "duplicate profiles do not match formatter", accept: `application/json; profile=CamelCase; profile=PascalCase`, want: CasingPascal},
		{name: "PascalCase profile", accept: `application/json; profile=PascalCase`, want: CasingPascal},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NegotiateCasing(tc.accept); got != tc.want {
				t.Fatalf("NegotiateCasing(%q) = %v, want %v", tc.accept, got, tc.want)
			}
		})
	}
}

func TestNegotiateCasing_StockInfinityQualities(t *testing.T) {
	cases := []struct {
		name   string
		accept string
		want   Casing
	}{
		{
			name:   "Inf is rejected",
			accept: `application/json; profile=CamelCase; q=Inf, application/json; profile=PascalCase; q=.5`,
			want:   CasingPascal,
		},
		{
			name:   "Infinity selects CamelCase",
			accept: `application/json; profile=CamelCase; q=Infinity, application/json; profile=PascalCase; q=.5`,
			want:   CasingCamel,
		},
		{
			name:   "negative Infinity is rejected",
			accept: `application/json; profile=CamelCase; q=-Infinity, application/json; profile=PascalCase; q=.5`,
			want:   CasingPascal,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NegotiateCasing(tc.accept); got != tc.want {
				t.Fatalf("NegotiateCasing(%q) = %v, want %v", tc.accept, got, tc.want)
			}
		})
	}
}

func TestJSON_NegotiatesStockCasingWithoutRecasingMapKeys(t *testing.T) {
	type payload struct {
		ServerName  string            `json:"ServerName"`
		ProviderIDs map[string]string `json:"ProviderIds"`
	}

	v := payload{ServerName: "Deadfin", ProviderIDs: map[string]string{"Tmdb": "603"}}
	cases := []struct {
		name        string
		accept      string
		contentType string
		want        string
	}{
		{
			name:        "absent Accept writes PascalCase",
			contentType: "application/json; charset=utf-8",
			want:        `{"ServerName":"Deadfin","ProviderIds":{"Tmdb":"603"}}`,
		},
		{
			name:        "CamelCase profile writes CamelCase",
			accept:      `application/json; profile=CamelCase`,
			contentType: `application/json; profile="CamelCase"; charset=utf-8`,
			want:        `{"serverName":"Deadfin","providerIds":{"Tmdb":"603"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.accept != "" {
				r.Header.Set("Accept", tc.accept)
			}
			w := httptest.NewRecorder()
			if err := JSON(w, r, http.StatusCreated, v); err != nil {
				t.Fatalf("JSON() error: %v", err)
			}
			if w.Code != http.StatusCreated {
				t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
			}
			if got := w.Header().Get("Content-Type"); got != tc.contentType {
				t.Errorf("Content-Type = %q, want %q", got, tc.contentType)
			}
			if got := w.Body.String(); got != tc.want {
				t.Errorf("body = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestJSON_StockCasingProbe(t *testing.T) {
	jellyport := attest.GateForTest(t)
	const fixtureName = "stock-casing-negotiation.probe.json"
	fixturePath := attest.AdjacentPath(jellyport, filepath.Join("corpora", "captured", fixtureName))
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read stock casing probe: %v", err)
	}

	manifestPath := attest.AdjacentPath(jellyport, filepath.Join("corpora", "captured", "stock-casing-negotiation.probe.SHA256SUMS"))
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read stock casing probe manifest: %v", err)
	}
	manifestFields := strings.Fields(string(manifest))
	if len(manifestFields) != 2 || manifestFields[1] != fixtureName {
		t.Fatalf("invalid stock casing probe manifest %q", manifest)
	}
	digest := sha256.Sum256(fixture)
	if got := hex.EncodeToString(digest[:]); got != manifestFields[0] {
		t.Fatalf("stock casing probe SHA-256 = %s, manifest has %s", got, manifestFields[0])
	}

	var probe struct {
		Format     string `json:"format"`
		Provenance struct {
			CredentialFree bool   `json:"credential_free"`
			RequestMethod  string `json:"request_method"`
			Endpoint       string `json:"endpoint"`
			StockOCIImage  string `json:"stock_oci_image"`
			APISource      struct {
				Repository string `json:"repository"`
				Commit     string `json:"commit"`
			} `json:"api_source"`
		} `json:"provenance"`
		Cases []struct {
			ID      string `json:"id"`
			Request struct {
				Headers map[string][]string `json:"headers"`
			} `json:"request"`
			Observed struct {
				Status           int      `json:"status"`
				Casing           string   `json:"casing"`
				RequiredJSONKeys []string `json:"required_json_keys"`
				AbsentJSONKeys   []string `json:"absent_json_keys"`
			} `json:"observed"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(fixture, &probe); err != nil {
		t.Fatalf("decode stock casing probe: %v", err)
	}
	if probe.Format != "deadfin.stock-casing-probe/v1" || !probe.Provenance.CredentialFree || probe.Provenance.RequestMethod != http.MethodGet || probe.Provenance.Endpoint != "/System/Info/Public" {
		t.Fatalf("invalid stock casing probe provenance: %+v", probe.Provenance)
	}
	if probe.Provenance.StockOCIImage != "jellyfin/jellyfin@sha256:2d1a69be5aa2695d0c2df90ebeb76bbcc6efc0a798cb09bfa65906680e1d785a" || probe.Provenance.APISource.Repository != "jellyfin/jellyfin" || probe.Provenance.APISource.Commit != "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb" {
		t.Fatalf("stock casing probe is not bound to the pinned source: %+v", probe.Provenance)
	}
	if got, want := len(probe.Cases), 31; got != want {
		t.Fatalf("stock casing probe case count = %d, want %d", got, want)
	}

	type publicInfo struct {
		Version string `json:"Version"`
		ID      string `json:"Id"`
	}
	for _, tc := range probe.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			acceptValues := tc.Request.Headers["Accept"]
			if len(acceptValues) == 0 {
				t.Fatal("probe case has no Accept header values")
			}
			if tc.Observed.Status != http.StatusOK || (tc.Observed.Casing != "CamelCase" && tc.Observed.Casing != "PascalCase") || len(tc.Observed.RequiredJSONKeys) == 0 || len(tc.Observed.AbsentJSONKeys) == 0 {
				t.Fatalf("invalid observed result: %+v", tc.Observed)
			}

			r := httptest.NewRequest(probe.Provenance.RequestMethod, probe.Provenance.Endpoint, nil)
			for _, value := range acceptValues {
				r.Header.Add("Accept", value)
			}
			w := httptest.NewRecorder()
			if err := JSON(w, r, tc.Observed.Status, publicInfo{Version: "12.0.0", ID: "stock-id"}); err != nil {
				t.Fatalf("JSON() error: %v", err)
			}
			if w.Code != tc.Observed.Status {
				t.Errorf("status = %d, want %d", w.Code, tc.Observed.Status)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode rendered body: %v", err)
			}
			for _, key := range tc.Observed.RequiredJSONKeys {
				if _, ok := body[key]; !ok {
					t.Errorf("rendered body missing stock-proven %s key %q: %s", tc.Observed.Casing, key, w.Body.String())
				}
			}
			for _, key := range tc.Observed.AbsentJSONKeys {
				if _, ok := body[key]; ok {
					t.Errorf("rendered body unexpectedly contains opposite-casing key %q: %s", key, w.Body.String())
				}
			}
		})
	}
}
