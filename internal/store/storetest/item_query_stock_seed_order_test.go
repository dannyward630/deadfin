// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"sort"
	"testing"
)

func stockFixtureSeedOrder(t *testing.T, records map[string]itemQueryStockFixtureItem) []string {
	t.Helper()
	pending := make([]string, 0, len(records))
	for identity := range records {
		pending = append(pending, identity)
	}
	sort.Strings(pending)
	visiting, visited := map[string]bool{}, map[string]bool{}
	result := make([]string, 0, len(records))
	var visit func(string)
	visit = func(identity string) {
		if visited[identity] {
			return
		}
		if visiting[identity] {
			t.Fatalf("cyclic fixture parent relation at %q", identity)
		}
		record, ok := records[identity]
		if !ok {
			t.Fatalf("unknown fixture relation %q", identity)
		}
		visiting[identity] = true
		if record.Relations.Parent != nil {
			visit(record.Relations.Parent.Item)
		}
		visiting[identity] = false
		visited[identity] = true
		result = append(result, identity)
	}
	for _, identity := range pending {
		visit(identity)
	}
	return result
}
