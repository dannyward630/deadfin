// SPDX-License-Identifier: GPL-2.0-only

package dto_test

import (
	"encoding/json"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// TestNewQueryResultPublishesNonNullItems defends the list-response invariant:
// an empty item result remains an array on the wire, never null.
func TestNewQueryResultPublishesNonNullItems(t *testing.T) {
	result := dto.NewQueryResult[dto.BaseItemDto]()
	if result.Items == nil {
		t.Fatal("new item query result has nil Items; clients require an empty array")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal item query result: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode item query result: %v", err)
	}
	if got, ok := wire["Items"]; !ok || string(got) != "[]" {
		t.Fatalf("Items wire value = %s (present %t), want []", got, ok)
	}
}
