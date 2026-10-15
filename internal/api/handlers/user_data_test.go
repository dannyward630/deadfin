// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"encoding/json"
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
)

func TestUserDataRoutesAreRegisteredByItems(t *testing.T) {
	routes := router.New()
	handlers.RegisterItems(routes, nil, nil, clock.NewReal())
	for _, want := range []router.Route{
		{Method: http.MethodPost, Pattern: "/UserItems/{itemId}/UserData", OperationID: "UpdateItemUserData", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodDelete, Pattern: "/UserItems/{itemId}/UserData", OperationID: "SHADOW:DeleteItemUserData", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/UserPlayedItems/{itemId}", OperationID: "MarkPlayedItem", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodDelete, Pattern: "/UserPlayedItems/{itemId}", OperationID: "MarkUnplayedItem", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/UserFavoriteItems/{itemId}", OperationID: "MarkFavoriteItem", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodDelete, Pattern: "/UserFavoriteItems/{itemId}", OperationID: "UnmarkFavoriteItem", Policy: string(auth.PolicyDefaultAuthorization)},
	} {
		got, found := routeByOperation(routes.Routes(), want.OperationID)
		if !found || got != want {
			t.Errorf("route %s = %#v (found=%t), want %#v", want.OperationID, got, found, want)
		}
	}
}

func TestUserDataMutationFamiliesPreserveIndependentState(t *testing.T) {
	routes, items, userID, itemID := userDataRoutes(t)
	request := func(method, path, body string) dto.UserItemDataDto {
		t.Helper()
		record := httptest.NewRecorder()
		req := httptest.NewRequest(method, path+"?userId="+userID.Dashed(), bytes.NewBufferString(body))
		req.Header.Set("X-Item-Access-Actor", userID.String())
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		routes.ServeHTTP(record, req)
		if record.Code != http.StatusOK {
			t.Fatalf("%s %s status = %d; body=%q", method, path, record.Code, record.Body.String())
		}
		var result dto.UserItemDataDto
		if err := json.Unmarshal(record.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return result
	}
	assertState(t, request(http.MethodPost, "/UserItems/"+itemID.Dashed()+"/UserData", `{"PlaybackPositionTicks":555}`), itemID, 555, false, 0, true)
	played := request(http.MethodPost, "/UserPlayedItems/"+itemID.Dashed(), "")
	assertState(t, played, itemID, 0, true, 1, true)
	if played.LastPlayedDate == nil {
		t.Fatal("mark played omitted LastPlayedDate")
	}
	assertState(t, request(http.MethodPost, "/UserItems/"+itemID.Dashed()+"/UserData", `{"PlaybackPositionTicks":777}`), itemID, 777, true, 1, true)
	assertState(t, request(http.MethodDelete, "/UserItems/"+itemID.Dashed()+"/UserData", ""), itemID, 0, true, 1, true)
	assertState(t, request(http.MethodDelete, "/UserFavoriteItems/"+itemID.Dashed(), ""), itemID, 0, true, 1, false)
	assertState(t, request(http.MethodPost, "/UserFavoriteItems/"+itemID.Dashed(), ""), itemID, 0, true, 1, true)
	unplayed := request(http.MethodDelete, "/UserPlayedItems/"+itemID.Dashed(), "")
	assertState(t, unplayed, itemID, 0, false, 0, true)
	if unplayed.LastPlayedDate != nil {
		t.Fatalf("mark unplayed LastPlayedDate = %v", unplayed.LastPlayedDate)
	}
	stored, err := items.GetUserData(t.Context(), userID, itemID.Dashed())
	if err != nil {
		t.Fatalf("read committed user data: %v", err)
	}
	if stored.Played || stored.PlayCount != 0 || stored.PositionTicks != 0 || !stored.IsFavorite || stored.LastPlayed != nil {
		t.Fatalf("committed user data = %#v", stored)
	}
}

func TestUpdateItemUserDataAppliesProvidedFieldsAndPreservesOmittedFields(t *testing.T) {
	routes, items, userID, itemID := userDataRoutes(t)
	update := func(body string) dto.UserItemDataDto {
		t.Helper()
		record := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/UserItems/"+itemID.Dashed()+"/UserData?userId="+userID.Dashed(), bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Item-Access-Actor", userID.String())
		routes.ServeHTTP(record, req)
		if record.Code != http.StatusOK {
			t.Fatalf("POST UserData status = %d; body=%q", record.Code, record.Body.String())
		}
		var result dto.UserItemDataDto
		if err := json.Unmarshal(record.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode UserData response: %v", err)
		}
		return result
	}

	result := update(`{"Rating":8.5,"PlaybackPositionTicks":456,"PlayCount":3,"IsFavorite":false,"Likes":true,"LastPlayedDate":"2026-07-29T10:11:12Z","Played":true}`)
	if result.Rating == nil || *result.Rating != 8.5 || result.Likes == nil || !*result.Likes || result.LastPlayedDate == nil {
		t.Fatalf("updated nullable fields = rating:%v likes:%v last:%v", result.Rating, result.Likes, result.LastPlayedDate)
	}
	assertState(t, result, itemID, 456, true, 3, false)

	// Explicit JSON null preserves the stored value, matching the pinned
	// upstream HasValue contract. The position still applies because it is
	// non-null.
	result = update(`{"PlaybackPositionTicks":789,"Rating":null,"Likes":null,"LastPlayedDate":null}`)
	assertState(t, result, itemID, 789, true, 3, false)
	if result.Rating == nil || *result.Rating != 8.5 || result.Likes == nil || !*result.Likes || result.LastPlayedDate == nil {
		t.Fatalf("explicit null did not preserve stored fields = rating:%v likes:%v last:%v", result.Rating, result.Likes, result.LastPlayedDate)
	}
	result = update(`{}`)
	assertState(t, result, itemID, 789, true, 3, false)

	// Upstream reads numbers from JSON strings (JsonDefaults.cs:34).
	result = update(`{"PlaybackPositionTicks":"790","PlayCount":"3","Rating":"8.5"}`)
	assertState(t, result, itemID, 790, true, 3, false)
	result = update(`{"PlaybackPositionTicks":789}`)

	stored, err := items.GetUserData(t.Context(), userID, itemID.Dashed())
	if err != nil {
		t.Fatalf("read updated user data: %v", err)
	}
	if stored.Rating == nil || *stored.Rating != 8.5 || stored.Likes == nil || *stored.Likes != 1 || stored.LastPlayed == nil || stored.PositionTicks != 789 || !stored.Played || stored.PlayCount != 3 || stored.IsFavorite {
		t.Fatalf("stored updated user data = %#v", stored)
	}
}

func TestMarkPlayedAcceptsOffsetlessDotNetDate(t *testing.T) {
	routes, _, userID, itemID := userDataRoutes(t)
	record := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/UserPlayedItems/"+itemID.Dashed()+"?userId="+userID.Dashed()+"&datePlayed=2026-07-29T10:11:12", nil)
	req.Header.Set("X-Item-Access-Actor", userID.String())
	routes.ServeHTTP(record, req)
	if record.Code != http.StatusOK {
		t.Fatalf("POST played status = %d; body=%q", record.Code, record.Body.String())
	}
	var result dto.UserItemDataDto
	if err := json.Unmarshal(record.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode played response: %v", err)
	}
	want := time.Date(2026, time.July, 29, 10, 11, 12, 0, time.UTC)
	if result.LastPlayedDate == nil || !result.LastPlayedDate.Time.Equal(want) {
		t.Fatalf("LastPlayedDate = %v, want %s", result.LastPlayedDate, want)
	}
}

func TestUpdateItemUserDataRejectsUnsupportedMediaType(t *testing.T) {
	routes, _, userID, itemID := userDataRoutes(t)
	record := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/UserItems/"+itemID.Dashed()+"/UserData?userId="+userID.Dashed(), bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Item-Access-Actor", userID.String())
	routes.ServeHTTP(record, req)
	if record.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("POST UserData status = %d, want 415; body=%q", record.Code, record.Body.String())
	}
}

// TestUpdateItemUserDataExplicitNullPreservesFields pins the upstream
// HasValue contract. Every field in UpdateUserItemDataDto is nullable, and
// UserDataManager.SaveUserData applies a field only when it has a value. An
// explicit JSON null therefore preserves the stored value, while a non-null
// false or zero value still applies.
func TestUpdateItemUserDataExplicitNullPreservesFields(t *testing.T) {
	routes, items, userID, itemID := userDataRoutes(t)
	seed := func(body string) {
		t.Helper()
		record := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/UserItems/"+itemID.Dashed()+"/UserData?userId="+userID.Dashed(), bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Item-Access-Actor", userID.String())
		routes.ServeHTTP(record, req)
		if record.Code != http.StatusOK {
			t.Fatalf("seed POST UserData status = %d; body=%q", record.Code, record.Body.String())
		}
	}
	wantLastPlayed := time.Date(2026, time.July, 28, 8, 0, 0, 0, time.UTC)
	// Seed every supported field with a non-default value.
	seed(`{"Rating":9,"PlaybackPositionTicks":4096,"PlayCount":2,"IsFavorite":true,"Likes":false,"LastPlayedDate":"2026-07-28T08:00:00Z","Played":true}`)

	// Post explicit null for every field. Each stored value must stay.
	record := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/UserItems/"+itemID.Dashed()+"/UserData?userId="+userID.Dashed(), bytes.NewBufferString(`{"Rating":null,"PlaybackPositionTicks":null,"PlayCount":null,"IsFavorite":null,"Likes":null,"LastPlayedDate":null,"Played":null}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Item-Access-Actor", userID.String())
	routes.ServeHTTP(record, req)
	if record.Code != http.StatusOK {
		t.Fatalf("explicit null POST UserData status = %d; body=%q", record.Code, record.Body.String())
	}
	var result dto.UserItemDataDto
	if err := json.Unmarshal(record.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode explicit null response: %v", err)
	}
	// Require exact equality for every field in the response.
	assertState(t, result, itemID, 4096, true, 2, true)
	if result.Rating == nil || *result.Rating != 9 {
		t.Fatalf("explicit null response Rating = %v, want 9", result.Rating)
	}
	if result.Likes == nil || *result.Likes {
		t.Fatalf("explicit null response Likes = %v, want false", result.Likes)
	}
	if result.LastPlayedDate == nil || !result.LastPlayedDate.Time.Equal(wantLastPlayed) {
		t.Fatalf("explicit null response LastPlayedDate = %v, want %s", result.LastPlayedDate, wantLastPlayed)
	}

	// Read the stored row and require exact equality for every counterpart,
	// including LastPlayed.
	stored, err := items.GetUserData(t.Context(), userID, itemID.Dashed())
	if err != nil {
		t.Fatalf("read stored user data after null: %v", err)
	}
	if stored.PositionTicks != 4096 {
		t.Fatalf("stored PositionTicks = %d, want 4096", stored.PositionTicks)
	}
	if stored.PlayCount != 2 {
		t.Fatalf("stored PlayCount = %d, want 2", stored.PlayCount)
	}
	if !stored.Played {
		t.Fatal("stored Played = false, want true")
	}
	if !stored.IsFavorite {
		t.Fatal("stored IsFavorite = false, want true")
	}
	if stored.Rating == nil || *stored.Rating != 9 {
		t.Fatalf("stored Rating = %v, want 9", stored.Rating)
	}
	if stored.Likes == nil || *stored.Likes != 0 {
		t.Fatalf("stored Likes = %v, want 0", stored.Likes)
	}
	if stored.LastPlayed == nil || !stored.LastPlayed.Equal(wantLastPlayed) {
		t.Fatalf("stored LastPlayed = %v, want %s", stored.LastPlayed, wantLastPlayed)
	}

	// Non-null false and zero values still apply. The null fields in this
	// request (Likes, LastPlayedDate) preserve the stored values.
	record = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/UserItems/"+itemID.Dashed()+"/UserData?userId="+userID.Dashed(), bytes.NewBufferString(`{"Rating":0,"PlaybackPositionTicks":0,"PlayCount":0,"IsFavorite":false,"Likes":null,"LastPlayedDate":null,"Played":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Item-Access-Actor", userID.String())
	routes.ServeHTTP(record, req)
	if record.Code != http.StatusOK {
		t.Fatalf("apply zero POST UserData status = %d; body=%q", record.Code, record.Body.String())
	}
	result = dto.UserItemDataDto{}
	if err := json.Unmarshal(record.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode apply zero response: %v", err)
	}
	assertState(t, result, itemID, 0, false, 0, false)
	if result.Rating == nil || *result.Rating != 0 {
		t.Fatalf("zero Rating not applied = %v", result.Rating)
	}
	// The null Likes and LastPlayedDate preserve the stored values.
	if result.Likes == nil || *result.Likes {
		t.Fatalf("null Likes did not preserve stored value = %v, want false", result.Likes)
	}
	if result.LastPlayedDate == nil || !result.LastPlayedDate.Time.Equal(wantLastPlayed) {
		t.Fatalf("null LastPlayedDate did not preserve stored value = %v, want %s", result.LastPlayedDate, wantLastPlayed)
	}

	stored, err = items.GetUserData(t.Context(), userID, itemID.Dashed())
	if err != nil {
		t.Fatalf("read stored user data after zero: %v", err)
	}
	if stored.PositionTicks != 0 || stored.PlayCount != 0 || stored.Played || stored.IsFavorite || stored.Rating == nil || *stored.Rating != 0 {
		t.Fatalf("stored zero values = %#v", stored)
	}
	if stored.Likes == nil || *stored.Likes != 0 {
		t.Fatalf("stored Likes = %v, want 0 (preserved by null)", stored.Likes)
	}
	if stored.LastPlayed == nil || !stored.LastPlayed.Equal(wantLastPlayed) {
		t.Fatalf("stored LastPlayed = %v, want %s (preserved by null)", stored.LastPlayed, wantLastPlayed)
	}
}

// TestUpdateItemUserDataRequiresBody pins the pinned OpenAPI contract that
// marks the request body required. An absent or JSON-null body returns a
// client error, while a present "{}" object is a valid no-op that returns 200.
func TestUpdateItemUserDataRequiresBody(t *testing.T) {
	routes, _, userID, itemID := userDataRoutes(t)
	post := func(body string, contentType string) int {
		t.Helper()
		record := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/UserItems/"+itemID.Dashed()+"/UserData?userId="+userID.Dashed(), bytes.NewBufferString(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("X-Item-Access-Actor", userID.String())
		routes.ServeHTTP(record, req)
		return record.Code
	}
	if code := post("", "application/json"); code != http.StatusBadRequest {
		t.Fatalf("absent body status = %d, want 400", code)
	}
	if code := post(`null`, "application/json"); code != http.StatusBadRequest {
		t.Fatalf("json null body status = %d, want 400", code)
	}
	if code := post(`{}`, "application/json"); code != http.StatusOK {
		t.Fatalf("empty object body status = %d, want 200", code)
	}
}

// TestUserDataPostThenResumeReflectsWrittenPosition proves the integration
// flow: a position written through POST
// /UserItems/{itemId}/UserData is visible on GET /UserItems/Resume. The test
// uses the real item store and the shared RegisterItems wiring, so the
// mutation and the resume query share one persistence layer.
func TestUserDataPostThenResumeReflectsWrittenPosition(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open resume integration store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close resume integration store: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	userID := itemsHandlerUUID(t, 2500)
	itemID := itemsHandlerUUID(t, 2501)
	name := "Resume integration movie"
	if err := items.Upsert(t.Context(), item.Item{ID: itemID, Kind: item.KindMovie, Name: &name, SortName: &name}); err != nil {
		t.Fatal(err)
	}
	user := store.User{ID: userID, Policy: json.RawMessage(`{"EnableAllFolders":true}`)}
	users := itemsAccessUsers{values: map[dto.UUID]store.User{userID: user}}
	authorizer := middleware.NewAuthorizer(itemsAccessResolver{identities: map[string]auth.Identity{userID.String(): {Authenticated: true, UserID: userID}}}, itemsAccessPolicy{}, users)
	routes := router.New(authorizer.Wrap)
	handlers.RegisterItems(routes, items, dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)), clock.NewReal())

	// Write a nonzero playback position through the user-data endpoint.
	writeRecord := httptest.NewRecorder()
	writeReq := httptest.NewRequest(http.MethodPost, "/UserItems/"+itemID.Dashed()+"/UserData?userId="+userID.Dashed(), bytes.NewBufferString(`{"PlaybackPositionTicks":40000000}`))
	writeReq.Header.Set("Content-Type", "application/json")
	writeReq.Header.Set("X-Item-Access-Actor", userID.String())
	routes.ServeHTTP(writeRecord, writeReq)
	if writeRecord.Code != http.StatusOK {
		t.Fatalf("POST UserData status = %d; body=%q", writeRecord.Code, writeRecord.Body.String())
	}

	// GET Resume must report the written position with a count of at least 1.
	resumeRecord := httptest.NewRecorder()
	resumeReq := httptest.NewRequest(http.MethodGet, "/UserItems/Resume?userId="+userID.Dashed()+"&enableImages=false", nil)
	resumeReq.Header.Set("X-Item-Access-Actor", userID.String())
	routes.ServeHTTP(resumeRecord, resumeReq)
	if resumeRecord.Code != http.StatusOK {
		t.Fatalf("GET Resume status = %d; body=%q", resumeRecord.Code, resumeRecord.Body.String())
	}
	var page dto.QueryResult[dto.BaseItemDto]
	if err := json.Unmarshal(resumeRecord.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode Resume response: %v; body=%s", err, resumeRecord.Body.String())
	}
	if page.TotalRecordCount < 1 {
		t.Fatalf("Resume TotalRecordCount = %d, want at least 1; body=%s", page.TotalRecordCount, resumeRecord.Body.String())
	}
	if len(page.Items) < 1 {
		t.Fatalf("Resume Items = %d, want at least 1; body=%s", len(page.Items), resumeRecord.Body.String())
	}
	var found bool
	for _, entry := range page.Items {
		if entry.ID == itemID {
			found = true
			if entry.UserData == nil || entry.UserData.PlaybackPositionTicks != 40000000 {
				t.Fatalf("Resume item UserData = %#v, want position 40000000", entry.UserData)
			}
		}
	}
	if !found {
		t.Fatalf("Resume did not return the written item %s; body=%s", itemID.Dashed(), resumeRecord.Body.String())
	}
}

func TestUserDataMutationRequestErrors(t *testing.T) {
	routes, _, userID, itemID := userDataRoutes(t)
	otherID := itemsHandlerUUID(t, 1203)
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"null field preserves value", http.MethodPost, "/UserItems/" + itemID.Dashed() + "/UserData?userId=" + userID.Dashed(), `{"PlaybackPositionTicks":null}`, http.StatusOK},
		{"malformed user", http.MethodPost, "/UserPlayedItems/" + itemID.Dashed() + "?userId=not-a-guid", "", http.StatusBadRequest},
		{"malformed body", http.MethodPost, "/UserItems/" + itemID.Dashed() + "/UserData?userId=" + userID.Dashed(), `{`, http.StatusBadRequest},
		{"absent body", http.MethodPost, "/UserItems/" + itemID.Dashed() + "/UserData?userId=" + userID.Dashed(), "", http.StatusBadRequest},
		{"json null body", http.MethodPost, "/UserItems/" + itemID.Dashed() + "/UserData?userId=" + userID.Dashed(), `null`, http.StatusBadRequest},
		{"missing item", http.MethodDelete, "/UserFavoriteItems/" + itemsHandlerUUID(t, 1299).Dashed() + "?userId=" + userID.Dashed(), "", http.StatusNotFound},
		{"cross user", http.MethodPost, "/UserPlayedItems/" + itemID.Dashed() + "?userId=" + otherID.Dashed(), "", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			req.Header.Set("X-Item-Access-Actor", userID.String())
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			routes.ServeHTTP(record, req)
			if record.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%q", record.Code, tc.want, record.Body.String())
			}
		})
	}
}

func userDataRoutes(t *testing.T) (*router.Router, *store.ItemStore, dto.UUID, dto.UUID) {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	userID, otherID, itemID := itemsHandlerUUID(t, 1200), itemsHandlerUUID(t, 1203), itemsHandlerUUID(t, 1201)
	name := "User data movie"
	if err := items.Upsert(t.Context(), item.Item{ID: itemID, Kind: item.KindMovie, Name: &name, SortName: &name}); err != nil {
		t.Fatal(err)
	}
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: itemID.Dashed(), ItemID: itemID, PositionTicks: 100, IsFavorite: true}); err != nil {
		t.Fatal(err)
	}
	user := store.User{ID: userID, Policy: json.RawMessage(`{"EnableAllFolders":true}`)}
	other := store.User{ID: otherID, Policy: json.RawMessage(`{"EnableAllFolders":true}`)}
	users := itemsAccessUsers{values: map[dto.UUID]store.User{userID: user, otherID: other}}
	authorizer := middleware.NewAuthorizer(itemsAccessResolver{identities: map[string]auth.Identity{userID.String(): {Authenticated: true, UserID: userID}}}, itemsAccessPolicy{}, users)
	routes := router.New(authorizer.Wrap)
	handlers.RegisterUserData(routes, items, items, users, clock.NewFake(time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)))
	return routes, items, userID, itemID
}

func assertState(t *testing.T, data dto.UserItemDataDto, itemID dto.UUID, position int64, played bool, count int, favorite bool) {
	t.Helper()
	if data.ItemID != itemID || data.Key != itemID.Dashed() || data.PlaybackPositionTicks != position || data.Played != played || data.PlayCount != count || data.IsFavorite != favorite {
		t.Fatalf("UserItemDataDto = %#v, want item=%#v key=%q position=%d played=%t count=%d favorite=%t", data, itemID, itemID.Dashed(), position, played, count, favorite)
	}
}
