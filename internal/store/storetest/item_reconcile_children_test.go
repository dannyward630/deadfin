// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
)

func TestItemStoreReconcileChildrenSQLite(t *testing.T) {
	runItemStoreReconcileChildren(t, openItemStore)
}

func TestItemStoreReconcileChildrenPostgres(t *testing.T) {
	runItemStoreReconcileChildren(t, openPostgresItemStore)
}

func TestItemStoreReconcileChildrenPostgresSerializesCompetingPlans(t *testing.T) {
	first, second := openConcurrentReconciliationPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	parent := hierarchyItem(t, 11800, item.KindFolder, nil)
	if err := first.Upsert(ctx, parent); err != nil {
		t.Fatalf("upsert concurrent reconciliation parent: %v", err)
	}
	firstPlan := []item.Item{
		reconciliationChild(t, 11801, item.KindMovie, parent.ID, "first plan one"),
		reconciliationChild(t, 11802, item.KindMovie, parent.ID, "first plan two"),
	}
	secondPlan := []item.Item{
		reconciliationChild(t, 11803, item.KindMovie, parent.ID, "second plan one"),
		reconciliationChild(t, 11804, item.KindMovie, parent.ID, "second plan two"),
	}
	type outcome struct {
		name   string
		result store.ReconcileChildrenResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var group sync.WaitGroup
	for _, operation := range []struct {
		name    string
		items   *store.ItemStore
		desired []item.Item
	}{
		{name: "first", items: first, desired: firstPlan},
		{name: "second", items: second, desired: secondPlan},
	} {
		group.Add(1)
		go func(operation struct {
			name    string
			items   *store.ItemStore
			desired []item.Item
		},
		) {
			defer group.Done()
			<-start
			result, err := operation.items.ReconcileChildren(ctx, parent.ID, operation.desired)
			outcomes <- outcome{name: operation.name, result: result, err: err}
		}(operation)
	}
	close(start)
	group.Wait()
	close(outcomes)

	byName := make(map[string]outcome, 2)
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("concurrent %s reconciliation: %v", outcome.name, outcome.err)
		}
		byName[outcome.name] = outcome
	}
	firstIDs := reconciliationIDs(firstPlan)
	secondIDs := reconciliationIDs(secondPlan)
	for name, want := range map[string][]dto.UUID{"first": firstIDs, "second": secondIDs} {
		got := byName[name].result
		if !sameUUIDSet(got.Inserted, want) || len(got.Updated) != 0 {
			t.Fatalf("concurrent %s reconciliation result = %#v, want its two inserted children and no updates", name, got)
		}
	}
	firstResult, secondResult := byName["first"].result, byName["second"].result
	if (!sameUUIDSet(firstResult.Deleted, secondIDs) || len(secondResult.Deleted) != 0) &&
		(!sameUUIDSet(secondResult.Deleted, firstIDs) || len(firstResult.Deleted) != 0) {
		t.Fatalf("concurrent reconciliation deletion results = first %#v second %#v, want one serial replacement of the other", firstResult, secondResult)
	}
	stored, err := first.Query(ctx, store.ItemQuery{ParentID: &parent.ID})
	if err != nil {
		t.Fatalf("query concurrent reconciliation result: %v", err)
	}
	storedIDs := make([]dto.UUID, 0, len(stored.Items))
	for _, it := range stored.Items {
		storedIDs = append(storedIDs, it.ID)
	}
	if !sameUUIDSet(storedIDs, firstIDs) && !sameUUIDSet(storedIDs, secondIDs) {
		t.Fatalf("concurrent reconciliation retained merged or partial IDs %v, want one complete plan %v or %v", storedIDs, firstIDs, secondIDs)
	}
}

func TestItemStoreReconcileChildrenPostgresCompletesSeparateParents(t *testing.T) {
	first, second := openConcurrentReconciliationPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	firstParent := hierarchyItem(t, 11810, item.KindFolder, nil)
	secondParent := hierarchyItem(t, 11811, item.KindFolder, nil)
	for _, parent := range []item.Item{firstParent, secondParent} {
		if err := first.Upsert(ctx, parent); err != nil {
			t.Fatalf("upsert separate-parent reconciliation fixture %s: %v", parent.ID, err)
		}
	}
	firstChild := reconciliationChild(t, 11812, item.KindMovie, firstParent.ID, "first parent child")
	secondChild := reconciliationChild(t, 11813, item.KindMovie, secondParent.ID, "second parent child")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for _, operation := range []struct {
		items   *store.ItemStore
		parent  dto.UUID
		desired item.Item
	}{
		{items: first, parent: firstParent.ID, desired: firstChild},
		{items: second, parent: secondParent.ID, desired: secondChild},
	} {
		group.Add(1)
		go func(operation struct {
			items   *store.ItemStore
			parent  dto.UUID
			desired item.Item
		},
		) {
			defer group.Done()
			<-start
			_, err := operation.items.ReconcileChildren(ctx, operation.parent, []item.Item{operation.desired})
			errs <- err
		}(operation)
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent separate-parent reconciliation: %v", err)
		}
	}
	assertReconciledItem(t, first, firstChild)
	assertReconciledItem(t, first, secondChild)
}

func runItemStoreReconcileChildren(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()

	t.Run("inserts complete children and recognizes an unchanged scan", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11000, item.KindFolder, nil)
		if err := items.Upsert(t.Context(), parent); err != nil {
			t.Fatalf("upsert reconciliation parent: %v", err)
		}

		first := reconciliationChild(t, 11002, item.KindMovie, parent.ID, "first complete child")
		first.Path = stringPtr("/library/first.mkv")
		first.ProviderIDs = providerIDs("Tmdb", "101", "Imdb", "tt101")
		first.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "first.jpg", Width: 1200}}
		first.LockedFields = []item.MetadataField{item.MetadataFieldName}
		first.Values = item.ItemValues{Genres: []string{"Drama"}, Tags: []string{"first-tag"}, Artists: []string{"First Artist"}}
		first.People = []item.PersonCredit{{PersonID: contractUUID(t, 11020), Name: "First Person", Type: item.PersonKindActor}}
		second := reconciliationChild(t, 11001, item.KindMovie, parent.ID, "second complete child")
		second.ProviderIDs = providerIDs("Tmdb", "102")
		second.ImageInfos = []item.ImageInfo{{Type: item.ImageTypeBackdrop, Path: "second.jpg", Width: 1920}}
		second.Values = item.ItemValues{Genres: []string{"Comedy"}, Tags: []string{"second-tag"}, Artists: []string{"Second Artist"}}

		desired := []item.Item{first, second}
		result, err := items.ReconcileChildren(t.Context(), parent.ID, desired)
		if err != nil {
			t.Fatalf("insert complete children: %v", err)
		}
		assertReconcileResult(t, result, []dto.UUID{first.ID, second.ID}, nil, nil)
		assertReconciledItem(t, items, first)
		assertReconciledItem(t, items, second)

		unchanged, err := items.ReconcileChildren(t.Context(), parent.ID, desired)
		if err != nil {
			t.Fatalf("reconcile unchanged children: %v", err)
		}
		assertReconcileResult(t, unchanged, nil, nil, nil)
	})

	t.Run("updates scalar and satellite facts as one child mutation", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11100, item.KindFolder, nil)
		if err := items.Upsert(t.Context(), parent); err != nil {
			t.Fatalf("upsert reconciliation parent: %v", err)
		}
		initial := reconciliationChild(t, 11101, item.KindMovie, parent.ID, "original child")
		initial.ProviderIDs = providerIDs("Tmdb", "old")
		initial.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "old.jpg", Width: 640}}
		initial.LockedFields = []item.MetadataField{item.MetadataFieldName}
		initial.Values = item.ItemValues{Genres: []string{"Drama"}, Tags: []string{"old-tag"}, Artists: []string{"Old Artist"}}
		initial.People = []item.PersonCredit{{PersonID: contractUUID(t, 11120), Name: "Old Person", Type: item.PersonKindActor}}
		if _, err := items.ReconcileChildren(t.Context(), parent.ID, []item.Item{initial}); err != nil {
			t.Fatalf("insert initial child: %v", err)
		}

		replacement := reconciliationChild(t, 11101, item.KindMovie, parent.ID, "replacement child")
		replacement.ProviderIDs = providerIDs("Tmdb", "new")
		replacement.ImageInfos = []item.ImageInfo{{Type: item.ImageTypeBackdrop, Path: "new.jpg", Width: 1280}}
		replacement.LockedFields = []item.MetadataField{item.MetadataFieldOverview}
		replacement.Values = item.ItemValues{Genres: []string{"Comedy"}, Tags: []string{"new-tag"}, Artists: []string{"New Artist"}}
		replacement.People = []item.PersonCredit{{PersonID: contractUUID(t, 11121), Name: "New Person", Type: item.PersonKindDirector}}
		result, err := items.ReconcileChildren(t.Context(), parent.ID, []item.Item{replacement})
		if err != nil {
			t.Fatalf("update complete child: %v", err)
		}
		assertReconcileResult(t, result, nil, []dto.UUID{replacement.ID}, nil)
		assertReconciledItem(t, items, replacement)
	})

	t.Run("deletes removed direct roots with their complete subtrees", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11200, item.KindFolder, nil)
		retained := reconciliationChild(t, 11201, item.KindMovie, parent.ID, "retained child")
		stale := reconciliationChild(t, 11203, item.KindFolder, parent.ID, "stale folder")
		descendant := reconciliationChild(t, 11204, item.KindMovie, stale.ID, "stale descendant")
		descendant.ProviderIDs = providerIDs("Tmdb", "stale")
		userID := contractUUID(t, 11205)
		for _, it := range []item.Item{parent, retained, stale, descendant} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("seed stale subtree item %s: %v", it.ID, err)
			}
		}
		data := store.UserData{UserID: userID, CustomKey: "stale-subtree", ItemID: descendant.ID, Played: true}
		if err := items.UpsertUserData(t.Context(), data); err != nil {
			t.Fatalf("seed stale subtree user data: %v", err)
		}

		result, err := items.ReconcileChildren(t.Context(), parent.ID, []item.Item{retained})
		if err != nil {
			t.Fatalf("delete stale direct child: %v", err)
		}
		assertReconcileResult(t, result, nil, nil, []dto.UUID{stale.ID})
		if _, err := items.Get(t.Context(), stale.ID); err == nil {
			t.Fatal("removed direct root remained readable")
		}
		if _, err := items.Get(t.Context(), descendant.ID); err == nil {
			t.Fatal("removed descendant remained readable")
		}
		if _, err := items.GetUserData(t.Context(), userID, data.CustomKey); err == nil {
			t.Fatal("removed descendant user data remained readable")
		}
		assertReconciledItem(t, items, retained)
	})

	t.Run("returns mutation identities deterministically", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11300, item.KindFolder, nil)
		updateFirst := reconciliationChild(t, 11301, item.KindMovie, parent.ID, "update first old")
		updateSecond := reconciliationChild(t, 11302, item.KindMovie, parent.ID, "update second old")
		staleHigh := reconciliationChild(t, 11305, item.KindMovie, parent.ID, "stale high")
		staleLow := reconciliationChild(t, 11304, item.KindMovie, parent.ID, "stale low")
		for _, it := range []item.Item{parent, updateFirst, updateSecond, staleHigh, staleLow} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("seed deterministic result item %s: %v", it.ID, err)
			}
		}
		updatedSecond := reconciliationChild(t, 11302, item.KindMovie, parent.ID, "update second new")
		insertSecond := reconciliationChild(t, 11307, item.KindMovie, parent.ID, "insert second")
		updatedFirst := reconciliationChild(t, 11301, item.KindMovie, parent.ID, "update first new")
		insertFirst := reconciliationChild(t, 11306, item.KindMovie, parent.ID, "insert first")
		result, err := items.ReconcileChildren(t.Context(), parent.ID, []item.Item{updatedSecond, insertSecond, updatedFirst, insertFirst})
		if err != nil {
			t.Fatalf("reconcile deterministic mutations: %v", err)
		}
		assertReconcileResult(t, result, []dto.UUID{insertSecond.ID, insertFirst.ID}, []dto.UUID{updatedSecond.ID, updatedFirst.ID}, []dto.UUID{staleLow.ID, staleHigh.ID})
	})

	t.Run("kind changes remove satellites owned only by the former kind", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11350, item.KindFolder, nil)
		playlist := reconciliationChild(t, 11351, item.KindPlaylist, parent.ID, "playlist before folder")
		playlist.PlaylistOwnerUserID = contractUUID(t, 11352)
		movie := reconciliationChild(t, 11353, item.KindMovie, parent.ID, "movie before folder")
		boxset := reconciliationChild(t, 11354, item.KindBoxSet, parent.ID, "boxset before folder")
		for _, it := range []item.Item{parent, playlist, movie, boxset} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("seed kind transition item %s: %v", it.ID, err)
			}
		}
		if err := items.ReplaceShares(t.Context(), playlist.ID, []store.ItemShare{{UserID: contractUUID(t, 11355), CanEdit: true}}); err != nil {
			t.Fatalf("seed playlist share: %v", err)
		}
		if err := items.ReplaceStreams(t.Context(), movie.ID, []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"}}); err != nil {
			t.Fatalf("seed movie stream: %v", err)
		}
		if err := items.ReplaceAttachments(t.Context(), movie.ID, []playback.MediaAttachment{{Index: 0, FileName: "cover.jpg", Codec: "mjpeg"}}); err != nil {
			t.Fatalf("seed movie attachment: %v", err)
		}
		if err := items.ReplaceChapters(t.Context(), movie.ID, []store.Chapter{{Index: 0, StartPositionTicks: 1, Extra: []byte(`{"marker":"old"}`)}}); err != nil {
			t.Fatalf("seed movie chapter: %v", err)
		}
		if err := items.ReplaceBoxSetLibraryFolders(t.Context(), boxset.ID, []dto.UUID{contractUUID(t, 11356)}); err != nil {
			t.Fatalf("seed boxset library folder: %v", err)
		}

		playlistFolder := reconciliationChild(t, 11351, item.KindFolder, parent.ID, "playlist now folder")
		movieFolder := reconciliationChild(t, 11353, item.KindFolder, parent.ID, "movie now folder")
		boxsetFolder := reconciliationChild(t, 11354, item.KindFolder, parent.ID, "boxset now folder")
		result, err := items.ReconcileChildren(t.Context(), parent.ID, []item.Item{playlistFolder, movieFolder, boxsetFolder})
		if err != nil {
			t.Fatalf("reconcile kind transitions: %v", err)
		}
		assertReconcileResult(t, result, nil, []dto.UUID{playlist.ID, movie.ID, boxset.ID}, nil)
		if shares, err := items.Shares(t.Context(), playlist.ID); err != nil || len(shares) != 0 {
			t.Fatalf("folder retained playlist shares = %#v, %v; want none", shares, err)
		}
		if streams, err := items.Streams(t.Context(), movie.ID); err != nil || len(streams) != 0 {
			t.Fatalf("folder retained movie streams = %#v, %v; want none", streams, err)
		}
		if attachments, err := items.Attachments(t.Context(), movie.ID); err != nil || len(attachments) != 0 {
			t.Fatalf("folder retained movie attachments = %#v, %v; want none", attachments, err)
		}
		if chapters, err := items.Chapters(t.Context(), movie.ID); err != nil || len(chapters) != 0 {
			t.Fatalf("folder retained movie chapters = %#v, %v; want none", chapters, err)
		}
		if folders, err := items.BoxSetLibraryFolders(t.Context(), boxset.ID); err != nil || len(folders) != 0 {
			t.Fatalf("folder retained boxset library folders = %#v, %v; want none", folders, err)
		}
	})

	t.Run("normalizes SQLite microsecond timestamps before no-op comparison", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11360, item.KindFolder, nil)
		if err := items.Upsert(t.Context(), parent); err != nil {
			t.Fatalf("upsert timestamp parent: %v", err)
		}
		itemModified := time.Date(2026, 7, 26, 12, 0, 0, 123456789, time.FixedZone("offset", 2*60*60))
		imageModified := time.Date(2026, 7, 26, 12, 0, 1, 987654321, time.FixedZone("offset", -3*60*60))
		child := reconciliationChild(t, 11361, item.KindMovie, parent.ID, "sub-microsecond child")
		child.DateModified = &itemModified
		child.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "timestamp.jpg", DateModified: &imageModified}}
		if _, err := items.ReconcileChildren(t.Context(), parent.ID, []item.Item{child}); err != nil {
			t.Fatalf("insert timestamp child: %v", err)
		}
		result, err := items.ReconcileChildren(t.Context(), parent.ID, []item.Item{child})
		if err != nil {
			t.Fatalf("reconcile timestamp-identical child: %v", err)
		}
		assertReconcileResult(t, result, nil, nil, nil)
	})

	t.Run("rejects invalid satellite enums before changing the sibling set", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11370, item.KindFolder, nil)
		baseline := reconciliationChild(t, 11371, item.KindMovie, parent.ID, "enum baseline")
		baseline.ProviderIDs = providerIDs("Tmdb", "enum-baseline")
		unrelated := reconciliationChild(t, 11372, item.KindMovie, parent.ID, "enum unrelated")
		linkedTarget := hierarchyItem(t, 11373, item.KindMovie, nil)
		for _, it := range []item.Item{parent, baseline, unrelated, linkedTarget} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("seed enum validation item %s: %v", it.ID, err)
			}
		}
		for _, test := range []struct {
			name string
			edit func(*item.Item)
		}{
			{name: "image type", edit: func(candidate *item.Item) {
				candidate.ImageInfos = []item.ImageInfo{{Type: item.ImageType(255), Path: "invalid.jpg"}}
			}},
			{name: "metadata field", edit: func(candidate *item.Item) { candidate.LockedFields = []item.MetadataField{item.MetadataField(255)} }},
			{name: "trailer type", edit: func(candidate *item.Item) { candidate.TrailerTypes = []item.TrailerType{item.TrailerType(255)} }},
			{name: "person kind", edit: func(candidate *item.Item) {
				candidate.People = []item.PersonCredit{{PersonID: contractUUID(t, 11374), Name: "invalid person", Type: item.PersonKind(255)}}
			}},
			{name: "linked child type", edit: func(candidate *item.Item) {
				candidate.LinkedChildren = []item.LinkedChildRef{{ItemID: linkedTarget.ID, Type: item.LinkedChildType(255)}}
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				candidate := baseline
				candidate.Name = stringPtr("enum candidate")
				test.edit(&candidate)
				if _, err := items.ReconcileChildren(t.Context(), parent.ID, []item.Item{candidate, unrelated}); err == nil {
					t.Fatal("reconcile accepted an invalid satellite enum")
				}
				assertReconciledItem(t, items, baseline)
				assertReconciledItem(t, items, unrelated)
			})
		}
	})

	t.Run("rejects duplicate satellite keys before changing the sibling set", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11380, item.KindFolder, nil)
		baseline := reconciliationChild(t, 11381, item.KindMovie, parent.ID, "duplicate baseline")
		baseline.ProviderIDs = providerIDs("Tmdb", "duplicate-baseline")
		unrelated := reconciliationChild(t, 11382, item.KindMovie, parent.ID, "duplicate unrelated")
		linkedTarget := hierarchyItem(t, 11383, item.KindMovie, nil)
		for _, it := range []item.Item{parent, baseline, unrelated, linkedTarget} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("seed duplicate validation item %s: %v", it.ID, err)
			}
		}
		for _, test := range []struct {
			name string
			edit func(*item.Item)
		}{
			{name: "image type and index", edit: func(candidate *item.Item) {
				candidate.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "one.jpg"}, {Type: item.ImageTypePrimary, Index: 0, Path: "two.jpg"}}
			}},
			{name: "metadata field", edit: func(candidate *item.Item) {
				candidate.LockedFields = []item.MetadataField{item.MetadataFieldName, item.MetadataFieldName}
			}},
			{name: "trailer type", edit: func(candidate *item.Item) {
				candidate.TrailerTypes = []item.TrailerType{item.TrailerTypeLocalTrailer, item.TrailerTypeLocalTrailer}
			}},
			{name: "person identity and type", edit: func(candidate *item.Item) {
				candidate.People = []item.PersonCredit{{PersonID: contractUUID(t, 11384), Name: "duplicate person", Type: item.PersonKindActor}, {PersonID: contractUUID(t, 11384), Name: "duplicate person", Type: item.PersonKindActor}}
			}},
			{name: "linked child identity", edit: func(candidate *item.Item) {
				candidate.LinkedChildren = []item.LinkedChildRef{{ItemID: linkedTarget.ID, Type: item.LinkedChildTypeManual}, {ItemID: linkedTarget.ID, Type: item.LinkedChildTypeManual}}
			}},
			{name: "item value", edit: func(candidate *item.Item) { candidate.Values = item.ItemValues{Genres: []string{"Drama", "Drama"}} }},
		} {
			t.Run(test.name, func(t *testing.T) {
				candidate := baseline
				candidate.Name = stringPtr("duplicate candidate")
				test.edit(&candidate)
				if _, err := items.ReconcileChildren(t.Context(), parent.ID, []item.Item{candidate, unrelated}); err == nil {
					t.Fatal("reconcile accepted duplicate satellite keys")
				}
				assertReconciledItem(t, items, baseline)
				assertReconciledItem(t, items, unrelated)
			})
		}
	})

	t.Run("rejects ambiguous or foreign desired children without changing the sibling set", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11400, item.KindFolder, nil)
		foreignParent := hierarchyItem(t, 11401, item.KindFolder, nil)
		baseline := reconciliationChild(t, 11402, item.KindMovie, parent.ID, "baseline child")
		baseline.ProviderIDs = providerIDs("Tmdb", "baseline")
		for _, it := range []item.Item{parent, foreignParent, baseline} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("seed validation item %s: %v", it.ID, err)
			}
		}
		valid := reconciliationChild(t, 11403, item.KindMovie, parent.ID, "valid desired child")
		duplicate := valid
		duplicate.Name = stringPtr("duplicate desired child")
		noParent := valid
		noParent.ParentID = nil
		foreign := valid
		foreign.ParentID = &foreignParent.ID
		noID := valid
		noID.ID = dto.UUID{}
		for _, test := range []struct {
			name    string
			desired []item.Item
		}{
			{name: "duplicate identifier", desired: []item.Item{valid, duplicate}},
			{name: "nil parent is not a root fallback", desired: []item.Item{noParent}},
			{name: "foreign parent", desired: []item.Item{foreign}},
			{name: "nil identifier", desired: []item.Item{noID}},
		} {
			t.Run(test.name, func(t *testing.T) {
				if _, err := items.ReconcileChildren(t.Context(), parent.ID, test.desired); err == nil {
					t.Fatal("reconcile accepted malformed desired children")
				}
				assertReconciledItem(t, items, baseline)
				if _, err := items.Get(t.Context(), valid.ID); err == nil {
					t.Fatal("invalid reconciliation inserted a new direct child")
				}
			})
		}
	})

	t.Run("rolls back the full sibling set when one desired child is malformed", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11500, item.KindFolder, nil)
		first := reconciliationChild(t, 11501, item.KindMovie, parent.ID, "first original")
		first.ProviderIDs = providerIDs("Tmdb", "first-original")
		second := reconciliationChild(t, 11502, item.KindMovie, parent.ID, "second original")
		second.ProviderIDs = providerIDs("Tmdb", "second-original")
		for _, it := range []item.Item{parent, first, second} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("seed rollback item %s: %v", it.ID, err)
			}
		}
		replacement := reconciliationChild(t, 11501, item.KindMovie, parent.ID, "first replacement")
		replacement.ProviderIDs = providerIDs("Tmdb", "first-replacement")
		malformed := reconciliationChild(t, 11503, item.KindMovie, parent.ID, "malformed sibling")
		malformed.ID = dto.UUID{}
		if _, err := items.ReconcileChildren(t.Context(), parent.ID, []item.Item{replacement, malformed}); err == nil {
			t.Fatal("reconcile accepted a malformed sibling")
		}
		assertReconciledItem(t, items, first)
		assertReconciledItem(t, items, second)
	})

	t.Run("cancellation leaves the complete direct-child set intact", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11600, item.KindFolder, nil)
		child := reconciliationChild(t, 11601, item.KindMovie, parent.ID, "cancellation child")
		for _, it := range []item.Item{parent, child} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("seed cancellation item %s: %v", it.ID, err)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := items.ReconcileChildren(ctx, parent.ID, nil); err == nil {
			t.Fatal("reconcile succeeded with a canceled context")
		}
		assertReconciledItem(t, items, child)
	})

	t.Run("repairs ancestors and user-data keys after a child update", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11700, item.KindFolder, nil)
		source := hierarchyItem(t, 11701, item.KindMovie, nil)
		source.ProviderIDs = providerIDs("Tmdb", "shared-old")
		target := reconciliationChild(t, 11702, item.KindMovie, parent.ID, "target old")
		target.ProviderIDs = providerIDs("Tmdb", "shared-old")
		for _, it := range []item.Item{parent, source} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("seed derived repair item %s: %v", it.ID, err)
			}
		}
		if _, err := items.ReconcileChildren(t.Context(), parent.ID, []item.Item{target}); err != nil {
			t.Fatalf("insert user-data-key target: %v", err)
		}
		key := reconciliationSharedUserDataKey(t, source, target)
		userID := contractUUID(t, 11703)
		if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: key, ItemID: source.ID, Played: true}); err != nil {
			t.Fatalf("upsert shared user data: %v", err)
		}
		assertReconciliationPlayedQuery(t, items, target.ID, userID, []dto.UUID{target.ID})
		assertAncestorSet(t, items, target.ID, parent.ID)
		storedTarget, err := items.Get(t.Context(), target.ID)
		if err != nil {
			t.Fatalf("get derived target: %v", err)
		}
		assertUUIDPointer(t, "target top parent", storedTarget.TopParentID, parent.ID)

		updated := reconciliationChild(t, 11702, item.KindMovie, parent.ID, "target new")
		updated.ProviderIDs = providerIDs("Tmdb", "shared-new")
		result, err := items.ReconcileChildren(t.Context(), parent.ID, []item.Item{updated})
		if err != nil {
			t.Fatalf("update user-data-key target: %v", err)
		}
		assertReconcileResult(t, result, nil, []dto.UUID{target.ID}, nil)
		assertReconciliationPlayedQuery(t, items, target.ID, userID, nil)
		assertAncestorSet(t, items, target.ID, parent.ID)
	})
}

func reconciliationChild(t *testing.T, value int, kind item.Kind, parentID dto.UUID, name string) item.Item {
	t.Helper()
	child := hierarchyItem(t, value, kind, &parentID)
	child.Name = stringPtr(name)
	return child
}

func assertReconcileResult(t *testing.T, got store.ReconcileChildrenResult, inserted, updated, deleted []dto.UUID) {
	t.Helper()
	if !reflect.DeepEqual(got.Inserted, inserted) || !reflect.DeepEqual(got.Updated, updated) || !reflect.DeepEqual(got.Deleted, deleted) {
		t.Fatalf("reconciliation result = %#v, want inserted %v updated %v deleted %v", got, inserted, updated, deleted)
	}
}

func assertReconciledItem(t *testing.T, items *store.ItemStore, want item.Item) {
	t.Helper()
	got, err := items.Get(t.Context(), want.ID)
	if err != nil {
		t.Fatalf("get reconciled item %s: %v", want.ID, err)
	}
	if got.ParentID == nil || want.ParentID == nil || *got.ParentID != *want.ParentID || got.Kind != want.Kind || !reflect.DeepEqual(got.Name, want.Name) || !reflect.DeepEqual(got.Path, want.Path) || !sameReconciliationProviderIDs(got.ProviderIDs, want.ProviderIDs) || !reflect.DeepEqual(got.ImageInfos, want.ImageInfos) || !reflect.DeepEqual(got.LockedFields, want.LockedFields) || !reflect.DeepEqual(got.Values, want.Values) || !reflect.DeepEqual(got.People, want.People) {
		t.Fatalf("reconciled item %s = %#v, want scalar and satellites %#v", want.ID, got, want)
	}
}

func sameReconciliationProviderIDs(got, want item.ProviderIDs) bool {
	if len(got.Snapshot()) != len(want.Snapshot()) {
		return false
	}
	for _, entry := range want.Snapshot() {
		value, ok := got.Get(entry.Name)
		if !ok || value != entry.Value {
			return false
		}
	}
	return true
}

func reconciliationSharedUserDataKey(t *testing.T, source, target item.Item) string {
	t.Helper()
	targetKeys := make(map[string]struct{})
	for _, key := range item.UserDataKeys(target, item.UserDataInputs{}) {
		targetKeys[key] = struct{}{}
	}
	for _, key := range item.UserDataKeys(source, item.UserDataInputs{}) {
		if _, ok := targetKeys[key]; ok {
			return key
		}
	}
	t.Fatalf("source user-data keys %q and target keys %q have no shared key", item.UserDataKeys(source, item.UserDataInputs{}), item.UserDataKeys(target, item.UserDataInputs{}))
	return ""
}

func assertReconciliationPlayedQuery(t *testing.T, items *store.ItemStore, itemID, userID dto.UUID, want []dto.UUID) {
	t.Helper()
	played := true
	result, err := items.Query(t.Context(), store.ItemQuery{ItemIDs: []dto.UUID{itemID}, UserID: &userID, IsPlayed: &played, EnableTotalRecordCount: true})
	if err != nil {
		t.Fatalf("query played reconciliation item %s: %v", itemID, err)
	}
	var got []dto.UUID
	for _, it := range result.Items {
		got = append(got, it.ID)
	}
	if !sameUUIDSet(got, want) || result.TotalRecordCount != len(want) {
		t.Fatalf("played reconciliation IDs = %v (total %d), want %v (total %d)", got, result.TotalRecordCount, want, len(want))
	}
}

func openConcurrentReconciliationPostgres(t *testing.T) (*store.ItemStore, *store.ItemStore) {
	t.Helper()
	dsn := os.Getenv("DEADFIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DEADFIN_TEST_POSTGRES_DSN is not set")
	}
	schema := reconciliationPostgresSchema(t)
	firstStores, err := postgres.Open(t.Context(), postgres.Config{DSN: dsn, Schema: schema})
	if err != nil {
		t.Fatalf("open first concurrent PostgreSQL store: %v", err)
	}
	secondStores, err := postgres.Open(t.Context(), postgres.Config{DSN: dsn, Schema: schema})
	if err != nil {
		_ = firstStores.Close()
		t.Fatalf("open second concurrent PostgreSQL store: %v", err)
	}
	t.Cleanup(func() {
		if err := secondStores.Close(); err != nil {
			t.Errorf("close second concurrent PostgreSQL store: %v", err)
		}
		if err := firstStores.Close(); err != nil {
			t.Errorf("close first concurrent PostgreSQL store: %v", err)
		}
		admin, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Errorf("open concurrent PostgreSQL schema administrator: %v", err)
			return
		}
		defer func() {
			if err := admin.Close(); err != nil {
				t.Errorf("close concurrent PostgreSQL schema administrator: %v", err)
			}
		}()
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Errorf("drop concurrent PostgreSQL schema %q: %v", schema, err)
		}
	})
	return store.NewItemStore(firstStores), store.NewItemStore(secondStores)
}

func reconciliationPostgresSchema(t *testing.T) string {
	t.Helper()
	var suffix [8]byte
	if _, err := cryptorand.Read(suffix[:]); err != nil {
		t.Fatalf("generate concurrent PostgreSQL schema suffix: %v", err)
	}
	return "reconciletest_" + hex.EncodeToString(suffix[:])
}

func reconciliationIDs(items []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(items))
	for index, it := range items {
		ids[index] = it.ID
	}
	return ids
}

// TestReconcilePreserveUnplannedChildrenSQLite and its PostgreSQL counterpart
// prove the PreserveUnplannedChildren flag preserves every existing
// unplanned direct child while ordinary reconcile without the flag deletes
// them. This is the store-level control for the enrichment-only remote-backfill
// contract: the flag is the seam reconcileBackfill uses, and the ordinary
// default must stay unchanged.
func TestReconcilePreserveUnplannedChildrenSQLite(t *testing.T) {
	runItemStoreReconcilePreserveUnplannedChildren(t, openItemStore)
}

func TestReconcilePreserveUnplannedChildrenPostgres(t *testing.T) {
	runItemStoreReconcilePreserveUnplannedChildren(t, openPostgresItemStore)
}

func runItemStoreReconcilePreserveUnplannedChildren(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()

	t.Run("flag preserves unplanned children while inserting absent items", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11800, item.KindFolder, nil)
		if err := items.Upsert(t.Context(), parent); err != nil {
			t.Fatalf("upsert parent: %v", err)
		}
		retained := reconciliationChild(t, 11801, item.KindMovie, parent.ID, "retained child")
		stale := reconciliationChild(t, 11802, item.KindMovie, parent.ID, "stale child")
		for _, it := range []item.Item{retained, stale} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("seed child %s: %v", it.ID, err)
			}
		}
		// Reconcile with only the retained child in the desired set, but set
		// PreserveUnplannedChildren. The stale child must survive.
		result, err := items.ReconcileScannedPlanAtWithOptions(t.Context(), parent.ID, []item.Item{retained}, nil, nil, time.Time{}, store.ReconcileOptions{PreserveUnplannedChildren: true})
		if err != nil {
			t.Fatalf("reconcile with preserve flag: %v", err)
		}
		assertReconcileResult(t, result, nil, nil, nil)
		assertReconciledItem(t, items, retained)
		if _, err := items.Get(t.Context(), stale.ID); err != nil {
			t.Fatalf("stale child deleted under PreserveUnplannedChildren: %v", err)
		}
	})

	t.Run("ordinary reconcile without the flag deletes unplanned children", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 11900, item.KindFolder, nil)
		if err := items.Upsert(t.Context(), parent); err != nil {
			t.Fatalf("upsert parent: %v", err)
		}
		retained := reconciliationChild(t, 11901, item.KindMovie, parent.ID, "retained child")
		stale := reconciliationChild(t, 11902, item.KindMovie, parent.ID, "stale child")
		for _, it := range []item.Item{retained, stale} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("seed child %s: %v", it.ID, err)
			}
		}
		// Ordinary reconcile without the flag must delete the stale child.
		result, err := items.ReconcileScannedPlanAtWithOptions(t.Context(), parent.ID, []item.Item{retained}, nil, nil, time.Time{}, store.ReconcileOptions{})
		if err != nil {
			t.Fatalf("ordinary reconcile: %v", err)
		}
		assertReconcileResult(t, result, nil, nil, []dto.UUID{stale.ID})
		assertReconciledItem(t, items, retained)
		if _, err := items.Get(t.Context(), stale.ID); err == nil {
			t.Fatalf("stale child survived an ordinary reconcile without the flag")
		}
	})

	t.Run("flag preserves unplanned children with empty desired set", func(t *testing.T) {
		items := open(t)
		parent := hierarchyItem(t, 12000, item.KindFolder, nil)
		if err := items.Upsert(t.Context(), parent); err != nil {
			t.Fatalf("upsert parent: %v", err)
		}
		stale := reconciliationChild(t, 12001, item.KindMovie, parent.ID, "stale child")
		if err := items.Upsert(t.Context(), stale); err != nil {
			t.Fatalf("seed child %s: %v", stale.ID, err)
		}
		// Reconcile with an empty desired set and the preserve flag. The stale
		// child must survive.
		result, err := items.ReconcileScannedPlanAtWithOptions(t.Context(), parent.ID, nil, nil, nil, time.Time{}, store.ReconcileOptions{PreserveUnplannedChildren: true})
		if err != nil {
			t.Fatalf("reconcile with preserve flag and empty desired: %v", err)
		}
		assertReconcileResult(t, result, nil, nil, nil)
		if _, err := items.Get(t.Context(), stale.ID); err != nil {
			t.Fatalf("stale child deleted under PreserveUnplannedChildren with empty desired: %v", err)
		}
	})
}
