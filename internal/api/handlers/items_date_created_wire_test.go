// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestItemsFieldsDateCreatedWiresExactRequestedUTCValue ensures /Items forwards
// Fields=DateCreated to the builder and serializes the selected persisted UTC
// timestamp in both public casing profiles. Source: ItemsController.cs:121 and
// DtoService.cs:961-965 at upstream/jellyfin.
func TestItemsFieldsDateCreatedWiresExactRequestedUTCValue(t *testing.T) {
	id := itemsHandlerUUID(t, 620)
	created := time.Date(2026, time.July, 27, 6, 44, 50, 0, time.UTC)
	reader := &itemsHandlerReader{result: store.ItemQueryResult{Items: []item.Item{{ID: id, Kind: item.KindMovie}}, TotalRecordCount: 1}}
	builder := &itemsHandlerBuilder{items: []dto.BaseItemDto{{ID: id, Name: "created movie", Type: dto.Kind("Movie"), DateCreated: &created}}}
	rt := router.New()
	handlers.RegisterItems(rt, reader, builder, clock.NewReal())

	for _, profile := range []struct {
		name, accept, itemsKey, dateKey string
	}{
		{name: "Pascal", accept: "application/json", itemsKey: "Items", dateKey: "DateCreated"},
		{name: "Camel", accept: `application/json; profile="CamelCase"`, itemsKey: "items", dateKey: "dateCreated"},
	} {
		t.Run(profile.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/Items?Recursive=true&Fields=DateCreated", nil)
			request.Header.Set("Accept", profile.accept)
			rt.ServeHTTP(record, request)
			if record.Code != http.StatusOK {
				t.Fatalf("GET /Items status = %d, want 200; body=%s", record.Code, record.Body.String())
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(record.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode %s response: %v; body=%s", profile.name, err, record.Body.String())
			}
			var rows []map[string]json.RawMessage
			if err := json.Unmarshal(envelope[profile.itemsKey], &rows); err != nil {
				t.Fatalf("decode %s Items array: %v; body=%s", profile.name, err, record.Body.String())
			}
			if len(rows) != 1 {
				t.Fatalf("%s item count = %d, want one", profile.name, len(rows))
			}
			if got, want := string(rows[0][profile.dateKey]), `"2026-07-27T06:44:50Z"`; got != want {
				t.Errorf("%s DateCreated = %s, want %s", profile.name, got, want)
			}
		})
	}
	if builder.calls != 2 {
		t.Fatalf("builder calls = %d, want one per profile", builder.calls)
	}
	for _, options := range builder.options {
		if len(options.Fields) != 1 || options.Fields[0] != "DateCreated" {
			t.Fatalf("builder Fields = %#v, want []string{DateCreated}", options.Fields)
		}
	}
}
