// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestItemStoreQuerySQLiteLiveTVChannelNameKeyFlag defends the orchestration
// boundary for same-name channel siblings. Disabling the channel-name key must
// remove only that lossy correlation, while a stable channel ExternalID key
// remains eligible for shared user-data state.
func TestItemStoreQuerySQLiteLiveTVChannelNameKeyFlag(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open live channel user-key fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close live channel user-key fixture: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	userID := liveChannelUserKeyUUID(t, 950)
	played := true

	nameSource := item.Item{ID: liveChannelUserKeyUUID(t, 951), Kind: item.KindLiveTvChannel, Name: liveChannelUserKeyString("Same Channel")}
	nameCandidate := item.Item{ID: liveChannelUserKeyUUID(t, 952), Kind: item.KindLiveTvChannel, Name: liveChannelUserKeyString("Same Channel")}
	externalSource := item.Item{ID: liveChannelUserKeyUUID(t, 953), Kind: item.KindTvChannel, Name: liveChannelUserKeyString("Different Channel"), SourceType: item.SourceTypeChannel, ExternalID: liveChannelUserKeyString("stable-channel")}
	externalCandidate := item.Item{ID: liveChannelUserKeyUUID(t, 954), Kind: item.KindTvChannel, Name: liveChannelUserKeyString("Another Channel"), SourceType: item.SourceTypeChannel, ExternalID: liveChannelUserKeyString("stable-channel")}
	for _, it := range []item.Item{nameSource, nameCandidate, externalSource, externalCandidate} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upsert live channel %s: %v", it.ID, err)
		}
	}
	nameKey := liveChannelSharedUserKey(t, nameSource, nameCandidate, item.UserDataInputs{})
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: nameKey, ItemID: nameSource.ID, Played: true}); err != nil {
		t.Fatalf("upsert same-name channel user data: %v", err)
	}
	externalKey := liveChannelSharedUserKey(t, externalSource, externalCandidate, item.UserDataInputs{DisableLiveTVChannelUserDataName: true})
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: externalKey, ItemID: externalSource.ID, Played: true}); err != nil {
		t.Fatalf("upsert external channel user data: %v", err)
	}

	cases := []struct {
		name  string
		query store.ItemQuery
		want  []dto.UUID
		total int
	}{
		{
			name:  "name-derived sibling remains matched when explicitly enabled",
			query: store.ItemQuery{ItemIDs: []dto.UUID{nameCandidate.ID}, UserID: &userID, IsPlayed: &played, DisableLiveTVChannelUserDataName: false, EnableTotalRecordCount: true},
			want:  []dto.UUID{nameCandidate.ID},
			total: 1,
		},
		{
			name:  "name-derived sibling is excluded when disabled",
			query: store.ItemQuery{ItemIDs: []dto.UUID{nameCandidate.ID}, UserID: &userID, IsPlayed: &played, DisableLiveTVChannelUserDataName: true, EnableTotalRecordCount: true},
			want:  []dto.UUID{},
			total: 0,
		},
		{
			name:  "stable external ID remains matched when names are disabled",
			query: store.ItemQuery{ItemIDs: []dto.UUID{externalCandidate.ID}, UserID: &userID, IsPlayed: &played, DisableLiveTVChannelUserDataName: true, EnableTotalRecordCount: true},
			want:  []dto.UUID{externalCandidate.ID},
			total: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := items.Query(t.Context(), tc.query)
			if err != nil {
				t.Fatalf("query live channel user data: %v", err)
			}
			if got := liveChannelUserKeyIDs(result.Items); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("live channel IDs = %v, want %v", got, tc.want)
			}
			if result.TotalRecordCount != tc.total {
				t.Fatalf("live channel count = %d, want %d", result.TotalRecordCount, tc.total)
			}
		})
	}
}

func liveChannelSharedUserKey(t *testing.T, source, candidate item.Item, inputs item.UserDataInputs) string {
	t.Helper()
	candidateKeys := make(map[string]struct{})
	for _, key := range item.UserDataKeys(candidate, inputs) {
		candidateKeys[key] = struct{}{}
	}
	for _, key := range item.UserDataKeys(source, inputs) {
		if _, ok := candidateKeys[key]; ok {
			return key
		}
	}
	t.Fatalf("source keys %q and candidate keys %q have no shared identity", item.UserDataKeys(source, inputs), item.UserDataKeys(candidate, inputs))
	return ""
}

func liveChannelUserKeyUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	return legacyUserKeyUUID(t, value)
}

func liveChannelUserKeyString(value string) *string { return &value }

func liveChannelUserKeyIDs(items []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(items))
	for index, it := range items {
		ids[index] = it.ID
	}
	return ids
}
