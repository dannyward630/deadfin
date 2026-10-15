// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestBuilderProjectsUserDataAndFolderCountsFromOneBatch defends user-relative
// played/favorite/resume state and folder counts without per-item enrichment
// calls. A page of folders must consume one batch result keyed by item ID.
func TestBuilderProjectsUserDataAndFolderCountsFromOneBatch(t *testing.T) {
	items := openBuilderItemStore(t)
	userID := builderUUID(t, 20)
	firstID, secondID := builderUUID(t, 21), builderUUID(t, 22)
	firstName, secondName := "First", "Second"
	for _, value := range []item.Item{
		{ID: firstID, Kind: item.KindFolder, IsFolder: true, Name: &firstName},
		{ID: secondID, Kind: item.KindFolder, IsFolder: true, Name: &secondName},
	} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert folder fixture: %v", err)
		}
	}
	loaded, err := items.GetBatch(t.Context(), []dto.UUID{firstID, secondID})
	if err != nil {
		t.Fatalf("load folder fixtures: %v", err)
	}
	childFirst, childSecond := 2, 3
	recursiveFirst, recursiveSecond := 7, 11
	enricher := &userCountEnricher{values: map[dto.UUID]dtobuild.Enrichment{
		firstID:  {UserData: &dto.UserItemDataDto{Played: true, PlayCount: 4, IsFavorite: true, PlaybackPositionTicks: 99, Key: firstID.String(), ItemID: firstID}, ChildCount: &childFirst, RecursiveItemCount: &recursiveFirst},
		secondID: {UserData: &dto.UserItemDataDto{Played: false, PlayCount: 0, IsFavorite: false, PlaybackPositionTicks: 0, Key: secondID.String(), ItemID: secondID}, ChildCount: &childSecond, RecursiveItemCount: &recursiveSecond},
	}}
	result, err := dtobuild.NewBuilder(items, enricher).Build(t.Context(), loaded, store.ItemQueryDTOOptions{EnableUserData: true, Fields: []string{"ChildCount", "RecursiveItemCount"}}, &userID)
	if err != nil {
		t.Fatalf("build user/count DTOs: %v", err)
	}
	if got, want := enricher.calls, 1; got != want {
		t.Fatalf("user/count enricher calls = %d, want one batch call", got)
	}
	if got, want := enricher.userID, userID; got != want {
		t.Fatalf("batch user ID = %s, want %s", got, want)
	}
	for index, want := range []struct {
		child, recursive int
		userData         string
	}{
		{2, 7, `{"PlaybackPositionTicks":99,"PlayCount":4,"IsFavorite":true,"Played":true,"Key":"00000000000000000000000000000015","ItemId":"00000000000000000000000000000015"}`},
		{3, 11, `{"PlaybackPositionTicks":0,"PlayCount":0,"IsFavorite":false,"Played":false,"Key":"00000000000000000000000000000016","ItemId":"00000000000000000000000000000016"}`},
	} {
		wire := builderWireObject(t, result.Items[index])
		if got := string(wire["UserData"]); got != want.userData {
			t.Errorf("item %d UserData = %s, want %s", index, got, want.userData)
		}
		if got, expected := string(wire["ChildCount"]), fmt.Sprintf("%d", want.child); got != expected {
			t.Errorf("item %d ChildCount = %s, want %s", index, got, expected)
		}
		if got, expected := string(wire["RecursiveItemCount"]), fmt.Sprintf("%d", want.recursive); got != expected {
			t.Errorf("item %d RecursiveItemCount = %s, want %s", index, got, expected)
		}
	}
}

type userCountEnricher struct {
	calls  int
	userID dto.UUID
	values map[dto.UUID]dtobuild.Enrichment
}

func (e *userCountEnricher) Enrich(_ context.Context, _ []item.Item, _ store.ItemQueryDTOOptions, userID *dto.UUID) (map[dto.UUID]dtobuild.Enrichment, error) {
	e.calls++
	if userID != nil {
		e.userID = *userID
	}
	return e.values, nil
}
