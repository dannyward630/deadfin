// SPDX-License-Identifier: GPL-2.0-only

package bind

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type quotedNumberInner struct {
	Bitrate int64   `json:"Bitrate"`
	Ratio   float32 `json:"Ratio"`
}

type quotedNumberBody struct {
	Index    int                  `json:"Index"`
	Optional *int                 `json:"Optional"`
	Unsigned uint16               `json:"Unsigned"`
	Float    float64              `json:"Float"`
	Name     string               `json:"Name"`
	Flag     bool                 `json:"Flag"`
	Inner    quotedNumberInner    `json:"Inner"`
	InnerPtr *quotedNumberInner   `json:"InnerPtr"`
	List     []int                `json:"List"`
	Inners   []quotedNumberInner  `json:"Inners"`
	Values   map[string]float64   `json:"Values"`
	Nested   map[string][]*uint32 `json:"Nested"`
}

func intPtr(v int) *int          { return &v }
func uint32Ptr(v uint32) *uint32 { return &v }

// TestJSONBodyReadsNumbersFromStrings mirrors upstream's
// JsonNumberHandling.AllowReadingFromString (JsonDefaults.cs:34): a numeric
// member reads a JSON string whose content is a JSON number literal.
func TestJSONBodyReadsNumbersFromStrings(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    quotedNumberBody
		wantErr bool
	}{
		{name: "top-level", body: `{"Index":"2"}`, want: quotedNumberBody{Index: 2}},
		{name: "case-insensitive key", body: `{"index":"2"}`, want: quotedNumberBody{Index: 2}},
		{name: "pointer", body: `{"Optional":"0"}`, want: quotedNumberBody{Optional: intPtr(0)}},
		{name: "unsigned", body: `{"Unsigned":"65535"}`, want: quotedNumberBody{Unsigned: 65535}},
		{name: "nested struct", body: `{"Inner":{"Bitrate":"1000","Ratio":"1.5"}}`, want: quotedNumberBody{Inner: quotedNumberInner{Bitrate: 1000, Ratio: 1.5}}},
		{name: "nested pointer struct", body: `{"innerptr":{"bitrate":"7"}}`, want: quotedNumberBody{InnerPtr: &quotedNumberInner{Bitrate: 7}}},
		{name: "array element", body: `{"List":[1,"2",3]}`, want: quotedNumberBody{List: []int{1, 2, 3}}},
		{name: "struct in array", body: `{"Inners":[{"Bitrate":"5"},{"Bitrate":6}]}`, want: quotedNumberBody{Inners: []quotedNumberInner{{Bitrate: 5}, {Bitrate: 6}}}},
		{name: "map value", body: `{"Values":{"a":"1.25","b":2}}`, want: quotedNumberBody{Values: map[string]float64{"a": 1.25, "b": 2}}},
		{name: "map of slices of pointers", body: `{"Nested":{"k":["4"]}}`, want: quotedNumberBody{Nested: map[string][]*uint32{"k": {uint32Ptr(4)}}}},
		{name: "float", body: `{"Float":"0.5"}`, want: quotedNumberBody{Float: 0.5}},
		{name: "negative", body: `{"Index":"-3","Float":"-0.25"}`, want: quotedNumberBody{Index: -3, Float: -0.25}},
		{name: "exponent into int", body: `{"Float":"1e3","Inner":{"Bitrate":"2E2"}}`, wantErr: true},
		{name: "exponent float", body: `{"Float":"1.5e3"}`, want: quotedNumberBody{Float: 1500}},
		{name: "escaped digits", body: `{"Index":"2"}`, want: quotedNumberBody{Index: 2}},
		{name: "several quoted members", body: `{"Index":"1","Optional":"2","Float":"3","List":["4","5"]}`, want: quotedNumberBody{Index: 1, Optional: intPtr(2), Float: 3, List: []int{4, 5}}},
		{name: "numeric string into string member stays a string", body: `{"Name":"2","Index":"2"}`, want: quotedNumberBody{Name: "2", Index: 2}},
		{name: "unknown members are ignored", body: `{"Other":"x","Index":"9"}`, want: quotedNumberBody{Index: 9}},
		{name: "invalid number", body: `{"Index":"2x"}`, wantErr: true},
		{name: "leading space", body: `{"Index":" 2"}`, wantErr: true},
		{name: "trailing space", body: `{"Index":"2 "}`, wantErr: true},
		{name: "plus sign", body: `{"Index":"+2"}`, wantErr: true},
		{name: "leading zero", body: `{"Index":"02"}`, wantErr: true},
		{name: "empty string", body: `{"Index":""}`, wantErr: true},
		{name: "fraction into int", body: `{"Index":"2.5"}`, wantErr: true},
		{name: "overflow", body: `{"Unsigned":"70000"}`, wantErr: true},
		{name: "bool from string", body: `{"Flag":"true"}`, wantErr: true},
		{name: "bool from string beside quoted number", body: `{"Index":"2","Flag":"true"}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			var got quotedNumberBody
			err := JSONBody(r, &got)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("JSONBody(%s) = nil error, got %+v", tc.body, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("JSONBody(%s): %v", tc.body, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("JSONBody(%s) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

func TestOptionalJSONBodyReadsNumbersFromStrings(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"Index":"4"}`))
	var got quotedNumberBody
	present, err := OptionalJSONBody(r, &got)
	if err != nil || !present || got.Index != 4 {
		t.Fatalf("OptionalJSONBody = %v, %v, %+v", present, err, got)
	}
}
