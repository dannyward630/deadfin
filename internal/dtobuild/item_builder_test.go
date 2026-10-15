// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestBuilderBuildsCoreItemAndBatchesEnrichment defends the public builder
// boundary: persisted item facts become a DTO, and supplemental facts are
// requested once for the whole page rather than once per item.
func TestBuilderBuildsCoreItemAndBatchesEnrichment(t *testing.T) {
	items := openBuilderItemStore(t)
	id := builderUUID(t, 1)
	name := "Aster"
	stored := item.Item{ID: id, Kind: item.KindMovie, Name: &name}
	if err := items.Upsert(t.Context(), stored); err != nil {
		t.Fatalf("upsert builder fixture: %v", err)
	}
	loaded, err := items.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("load builder fixture: %v", err)
	}

	enricher := &countingEnricher{}
	builder := dtobuild.NewBuilder(items, enricher)
	result, err := builder.Build(t.Context(), []item.Item{loaded}, store.ItemQueryDTOOptions{}, nil)
	if err != nil {
		t.Fatalf("build core item DTO: %v", err)
	}
	if got, want := enricher.calls, 1; got != want {
		t.Fatalf("batch enricher calls = %d, want one page-level call", got)
	}
	if got, want := len(result.Items), 1; got != want {
		t.Fatalf("built item count = %d, want %d", got, want)
	}
	wire := builderWireObject(t, result.Items[0])
	if got, want := string(wire["Id"]), fmt.Sprintf(`"%s"`, id.String()); got != want {
		t.Errorf("DTO Id = %s, want %s", got, want)
	}
	if got, want := string(wire["Name"]), `"Aster"`; got != want {
		t.Errorf("DTO Name = %s, want %s", got, want)
	}
	if got, want := string(wire["Type"]), `"Movie"`; got != want {
		t.Errorf("DTO Type = %s, want %s", got, want)
	}
}

type countingEnricher struct{ calls int }

func (e *countingEnricher) Enrich(_ context.Context, values []item.Item, _ store.ItemQueryDTOOptions, _ *dto.UUID) (map[dto.UUID]dtobuild.Enrichment, error) {
	e.calls++
	return make(map[dto.UUID]dtobuild.Enrichment, len(values)), nil
}

func openBuilderItemStore(t *testing.T) *store.ItemStore {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open builder SQLite store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close builder SQLite store: %v", err)
		}
	})
	return store.NewItemStore(stores)
}

func builderUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parse builder UUID: %v", err)
	}
	return id
}

func builderWireObject(t *testing.T, value any) map[string]json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal builder DTO: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode builder DTO: %v", err)
	}
	return wire
}

// TestBuilderRendersMixedCollectionFolderTypeAsNull proves a mixed library
// renders upstream's null CollectionType, not "unknown".
func TestBuilderRendersMixedCollectionFolderTypeAsNull(t *testing.T) {
	items := openBuilderItemStore(t)
	mixed, movies := item.CollectionTypeUnknown, item.CollectionTypeMovies
	values := []item.Item{
		{ID: builderUUID(t, 1), Kind: item.KindCollectionFolder, IsFolder: true, Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: &mixed}}},
		{ID: builderUUID(t, 2), Kind: item.KindCollectionFolder, IsFolder: true, Extra: item.ExtraFields{CollectionFolder: &item.CollectionFolderExtra{CollectionType: &movies}}},
	}
	result, err := dtobuild.NewBuilder(items, nil).Build(t.Context(), values, store.ItemQueryDTOOptions{}, nil)
	if err != nil {
		t.Fatalf("build collection folder DTOs: %v", err)
	}
	if got, ok := builderWireObject(t, result.Items[0])["CollectionType"]; ok && string(got) != "null" {
		t.Fatalf("mixed CollectionType = %s, want null", got)
	}
	if got := builderWireObject(t, result.Items[1])["CollectionType"]; string(got) != `"movies"` {
		t.Fatalf("movies CollectionType = %s, want movies", got)
	}
}
