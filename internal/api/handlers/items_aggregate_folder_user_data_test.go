// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestItemsRouteRendersAggregateFolderUserDataInCamelCase crosses the public
// /Items DTO path. It pins the fresh-corpus BoxSet failure: three unplayed
// linked children must produce numeric zero PlayedPercentage and count three,
// while an ordinary leaf omits both aggregate-only fields.
func TestItemsRouteRendersAggregateFolderUserDataInCamelCase(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open aggregate HTTP store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close aggregate HTTP store: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	userID, boxSetID, leafID := itemsHandlerUUID(t, 930), itemsHandlerUUID(t, 931), itemsHandlerUUID(t, 932)
	boxSetName, leafName := "Corpus collection", "Corpus child"
	boxSet := item.Item{
		ID: boxSetID, Kind: item.KindBoxSet, IsFolder: true, Name: &boxSetName,
		LinkedChildren: []item.LinkedChildRef{
			{ItemID: leafID, Type: item.LinkedChildTypeManual},
			{ItemID: itemsHandlerUUID(t, 933), Type: item.LinkedChildTypeManual},
			{ItemID: itemsHandlerUUID(t, 934), Type: item.LinkedChildTypeManual},
		},
	}
	leaves := []item.Item{
		{ID: leafID, Kind: item.KindMovie, Name: &leafName},
		{ID: itemsHandlerUUID(t, 933), Kind: item.KindMovie, Name: itemsHandlerString("second child")},
		{ID: itemsHandlerUUID(t, 934), Kind: item.KindMovie, Name: itemsHandlerString("third child")},
	}
	for _, value := range append([]item.Item{boxSet}, leaves...) {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert aggregate HTTP fixture %s: %v", value.ID, err)
		}
	}

	routes := router.New()
	handlers.RegisterItems(routes, items, dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)), clock.NewReal())
	record := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/Items?userId="+userID.Dashed()+"&ids="+boxSetID.Dashed()+","+leafID.Dashed()+"&enableImages=false", nil)
	request.Header.Set("Accept", `application/json; profile="CamelCase"`)
	routes.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET /Items aggregate folder = %d, want 200: %s", record.Code, record.Body.String())
	}

	var payload struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode camel Items response: %v; body=%s", err, record.Body.String())
	}
	rows := make(map[dto.UUID]map[string]json.RawMessage, len(payload.Items))
	for _, row := range payload.Items {
		var id dto.UUID
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatalf("decode response item id %s: %v", row["id"], err)
		}
		rows[id] = itemAggregateHTTPUserData(t, row)
	}
	boxSetData, found := rows[boxSetID]
	if !found {
		t.Fatalf("response omitted BoxSet %s: %s", boxSetID, record.Body.String())
	}
	if got, want := string(boxSetData["unplayedItemCount"]), "3"; got != want {
		t.Errorf("BoxSet unplayedItemCount = %s, want numeric %s", got, want)
	}
	if got, want := string(boxSetData["playedPercentage"]), "0"; got != want {
		t.Errorf("BoxSet playedPercentage = %s, want numeric %s", got, want)
	}
	leafData, found := rows[leafID]
	if !found {
		t.Fatalf("response omitted leaf %s: %s", leafID, record.Body.String())
	}
	for _, field := range []string{"unplayedItemCount", "playedPercentage"} {
		if value, found := leafData[field]; found {
			t.Errorf("leaf userData unexpectedly emitted %s=%s", field, value)
		}
	}
}

func itemAggregateHTTPUserData(t *testing.T, row map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var data map[string]json.RawMessage
	if err := json.Unmarshal(row["userData"], &data); err != nil {
		t.Fatalf("decode item userData %s: %v", row["userData"], err)
	}
	return data
}
