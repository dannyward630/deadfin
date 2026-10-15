// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestResumeLargeCardinality exercises the authenticated Resume request through
// the real handler and store with a large item and user-data set. It asserts
// HTTP 200, the JSON payload fields, the expected item order, the expected
// TotalRecordCount, and the page boundaries. It must complete in less than 5
// seconds.
func TestResumeLargeCardinality(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open resume handler store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close resume handler store: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	ctx := t.Context()

	userID := itemsHandlerUUID(t, 3500)

	// Insert filler rows through store-owned test support to avoid per-item
	// repair overhead while keeping SQL inside internal/store.
	const fillerCount = 11118
	const userDataFillerCount = 10000
	if err := storetest.SeedResumeLargeCardinality(ctx, stores, userID, fillerCount, userDataFillerCount); err != nil {
		t.Fatalf("seed resume cardinality: %v", err)
	}

	// Test items: direct and alternate versions.
	directID := resumeLargeUUID(t, 10001)
	alternatePrimaryID := resumeLargeUUID(t, 10004)
	alternateFirstID := resumeLargeUUID(t, 10005)
	alternateSecondID := resumeLargeUUID(t, 10006)

	for _, it := range []item.Item{
		{ID: directID, Kind: item.KindMovie, Name: itemsHandlerString("direct"), SortName: itemsHandlerString("same")},
		{ID: alternatePrimaryID, Kind: item.KindMovie, Name: itemsHandlerString("alt primary"), SortName: itemsHandlerString("same")},
		{ID: alternateFirstID, Kind: item.KindMovie, Name: itemsHandlerString("alt first"), SortName: itemsHandlerString("same"), PrimaryVersionID: &alternatePrimaryID},
		{ID: alternateSecondID, Kind: item.KindMovie, Name: itemsHandlerString("alt second"), SortName: itemsHandlerString("same"), PrimaryVersionID: &alternatePrimaryID},
	} {
		if err := items.Upsert(ctx, it); err != nil {
			t.Fatalf("upsert test item %s: %v", it.ID, err)
		}
	}

	// Direct match with the greatest last_played.
	directLast := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	if err := items.UpsertUserData(ctx, store.UserData{UserID: userID, CustomKey: "library/" + directID.String(), ItemID: directID, PositionTicks: 1, LastPlayed: &directLast}); err != nil {
		t.Fatalf("upsert direct user data: %v", err)
	}
	// Alternate versions: first alternate has the greatest last_played.
	altPrimaryLast := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	altFirstLast := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	altSecondLast := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	if err := items.UpsertUserData(ctx, store.UserData{UserID: userID, CustomKey: "library/" + alternatePrimaryID.String(), ItemID: alternatePrimaryID, PositionTicks: 1, LastPlayed: &altPrimaryLast}); err != nil {
		t.Fatalf("upsert alt primary user data: %v", err)
	}
	if err := items.UpsertUserData(ctx, store.UserData{UserID: userID, CustomKey: "library/" + alternateFirstID.String(), ItemID: alternateFirstID, PositionTicks: 1, LastPlayed: &altFirstLast}); err != nil {
		t.Fatalf("upsert alt first user data: %v", err)
	}
	if err := items.UpsertUserData(ctx, store.UserData{UserID: userID, CustomKey: "library/" + alternateSecondID.String(), ItemID: alternateSecondID, PositionTicks: 1, LastPlayed: &altSecondLast}); err != nil {
		t.Fatalf("upsert alt second user data: %v", err)
	}

	// Wire the handler with the real store, builder, and authorizer.
	user := store.User{ID: userID, Policy: json.RawMessage(`{"EnableAllFolders":true}`)}
	users := itemsAccessUsers{values: map[dto.UUID]store.User{userID: user}}
	authorizer := middleware.NewAuthorizer(itemsAccessResolver{identities: map[string]auth.Identity{userID.String(): {Authenticated: true, UserID: userID}}}, itemsAccessPolicy{}, users)
	routes := router.New(authorizer.Wrap)
	handlers.RegisterItems(routes, items, dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)), clock.NewReal())

	// Issue the Resume request and time it.
	const limit = 12
	realClock := clock.NewReal()
	start := realClock.Now()
	record := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/UserItems/Resume?userId="+userID.Dashed()+"&startIndex=0&limit="+fmt.Sprint(limit), nil)
	req.Header.Set("X-Item-Access-Actor", userID.String())
	routes.ServeHTTP(record, req)
	elapsed := realClock.Now().Sub(start)

	if record.Code != http.StatusOK {
		t.Fatalf("GET /UserItems/Resume status = %d; body=%q", record.Code, record.Body.String())
	}

	if elapsed >= 5*time.Second {
		t.Errorf("resume request took %s, exceeds the 5-second limit", elapsed)
	}

	var page struct {
		Items            []dto.BaseItemDto `json:"Items"`
		TotalRecordCount int               `json:"TotalRecordCount"`
		StartIndex       int               `json:"StartIndex"`
	}
	if err := json.Unmarshal(record.Body.Bytes(), &page); err != nil {
		t.Fatalf("unmarshal resume response: %v", err)
	}

	const expectedTotal = userDataFillerCount + 2
	if page.TotalRecordCount != expectedTotal {
		t.Errorf("TotalRecordCount = %d, want %d", page.TotalRecordCount, expectedTotal)
	}
	if page.StartIndex != 0 {
		t.Errorf("StartIndex = %d, want 0", page.StartIndex)
	}
	if len(page.Items) != limit {
		t.Errorf("page item count = %d, want %d (limit)", len(page.Items), limit)
	}
	if len(page.Items) < 2 {
		t.Fatalf("page returned %d items, need at least 2 for assertion", len(page.Items))
	}
	if page.Items[0].ID != directID {
		t.Errorf("page[0] = %s, want %s (direct match, greatest last_played)", page.Items[0].ID, directID)
	}
	if page.Items[1].ID != alternateFirstID {
		t.Errorf("page[1] = %s, want %s (alternate-version winner)", page.Items[1].ID, alternateFirstID)
	}

	t.Logf("resume request completed in %s", elapsed)
}

func resumeLargeUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing resume large UUID %d: %v", value, err)
	}
	return id
}
