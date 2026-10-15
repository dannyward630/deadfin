// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestNormalizerEquatesOnlyStockVolatileUserViewChildCounts pins the narrow
// DtoService volatility allowance: any paired JSON integer in [1,9] is a
// comparable UserViews sentinel, while every other representation remains an
// observable difference.
func TestNormalizerEquatesOnlyStockVolatileUserViewChildCounts(t *testing.T) {
	normalize := func(t *testing.T, body string) normalizedResponse {
		t.Helper()
		result, err := newNormalizer([]Rule{{Name: "user-view-ids", Action: "user_view_ids"}}).normalize(Response{Status: 200, Body: []byte(body)}, nil)
		if err != nil {
			t.Fatalf("normalize UserViews response: %v", err)
		}
		return result
	}

	for leftOffset := range 9 {
		leftCount := leftOffset + 1
		for rightOffset := range 9 {
			right := rightOffset + 1
			t.Run(fmt.Sprintf("paired integers %d and %d", leftCount, right), func(t *testing.T) {
				left := normalize(t, userViewChildCountBody("Id", "Items", "stock-view", fmt.Sprintf(`"ChildCount":%d`, leftCount)))
				other := normalize(t, userViewChildCountBody("Id", "Items", "deadfin-view", fmt.Sprintf(`"ChildCount":%d`, right)))
				if equal, reason := equalNormalized(left, other, 0); !equal {
					t.Fatalf("paired stock ChildCount values must compare equal: %s", reason)
				}
				view := left.Body.(map[string]any)["Items"].([]any)[0].(map[string]any)
				if got, want := view["ChildCount"], userViewChildCountToken; got != want {
					t.Fatalf("normalized valid ChildCount = %#v, want %q", got, want)
				}
			})
		}
	}

	t.Run("Camel direct UserViews row", func(t *testing.T) {
		left := normalize(t, userViewChildCountBody("id", "items", "stock-view", `"childCount":2`))
		right := normalize(t, userViewChildCountBody("id", "items", "deadfin-view", `"childCount":8`))
		if equal, reason := equalNormalized(left, right, 0); !equal {
			t.Fatalf("Camel paired stock ChildCount values must compare equal: %s", reason)
		}
	})

	for _, test := range []struct {
		name          string
		leftField     string
		rightField    string
		leftPreserved any
	}{
		{name: "zero", leftField: `"ChildCount":0`, rightField: `"ChildCount":1`, leftPreserved: json.Number("0")},
		{name: "above upper bound", leftField: `"ChildCount":10`, rightField: `"ChildCount":1`, leftPreserved: json.Number("10")},
		{name: "fraction", leftField: `"ChildCount":1.5`, rightField: `"ChildCount":1`, leftPreserved: json.Number("1.5")},
		{name: "string", leftField: `"ChildCount":"1"`, rightField: `"ChildCount":1`, leftPreserved: "1"},
		{name: "missing", leftField: "", rightField: `"ChildCount":1`, leftPreserved: nil},
		{name: "one-sided", leftField: `"ChildCount":1`, rightField: "", leftPreserved: userViewChildCountToken},
	} {
		t.Run(test.name, func(t *testing.T) {
			left := normalize(t, userViewChildCountBody("Id", "Items", "stock-view", test.leftField))
			right := normalize(t, userViewChildCountBody("Id", "Items", "deadfin-view", test.rightField))
			if equal, reason := equalNormalized(left, right, 0); equal {
				t.Fatalf("non-stock ChildCount boundary compared equal: %s", reason)
			}
			view := left.Body.(map[string]any)["Items"].([]any)[0].(map[string]any)
			got, found := view["ChildCount"]
			if test.leftField == "" {
				if found {
					t.Fatalf("missing ChildCount was synthesized as %#v", got)
				}
				return
			}
			if got != test.leftPreserved {
				t.Fatalf("ChildCount %s normalized to %#v, want exact %#v", test.leftField, got, test.leftPreserved)
			}
		})
	}
}

func userViewChildCountBody(idKey, itemsKey, id, childCountField string) string {
	childCount := ""
	if childCountField != "" {
		childCount = "," + childCountField
	}
	return fmt.Sprintf(`{"%s":[{"%s":%q,"Name":"Golden Movies","CollectionType":"movies"%s}]}`, itemsKey, idKey, id, childCount)
}
