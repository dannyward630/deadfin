// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// System-folder bootstrap is create-if-missing: startup must not replace
// metadata that a later metadata/image refresh has attached to either root.
func TestApplicationBootstrapPreservesEnrichedSystemRootsAcrossRestart(t *testing.T) {
	programData := filepath.Join(t.TempDir(), "program-data")

	app := openPreservationBootstrapApplication(t, programData)
	items := store.NewItemStore(app.stores)
	before := enrichedBootstrapRoots(t, items)
	closePreservationBootstrapApplication(t, app, t.Context())

	app = openPreservationBootstrapApplication(t, programData)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()
		closePreservationBootstrapApplication(t, app, ctx)
	})
	items = store.NewItemStore(app.stores)
	after := bootstrapRootsByKind(t, items)

	for kind, want := range before {
		got, ok := after[kind]
		if !ok {
			t.Errorf("restarted bootstrap omitted %s root", kind)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("restarted bootstrap overwrote enriched %s root\n got: %#v\nwant: %#v", kind, got, want)
		}
	}
}

func openPreservationBootstrapApplication(t *testing.T, programData string) *application {
	t.Helper()

	app, err := newApplication(t.Context(), programData)
	if err != nil {
		t.Fatalf("start application for bootstrap preservation: %v", err)
	}
	return app
}

func closePreservationBootstrapApplication(t *testing.T, app *application, ctx context.Context) {
	t.Helper()

	if err := app.Close(ctx); err != nil {
		t.Fatalf("close application for bootstrap preservation restart: %v", err)
	}
}

func enrichedBootstrapRoots(t *testing.T, items *store.ItemStore) map[item.Kind]item.Item {
	t.Helper()

	roots := bootstrapRootsByKind(t, items)
	for kind, root := range roots {
		enriched := enrichBootstrapRoot(t, root, kind)
		if err := items.Upsert(t.Context(), enriched); err != nil {
			t.Fatalf("enrich %s system root: %v", kind, err)
		}
		persisted, err := items.Get(t.Context(), enriched.ID)
		if err != nil {
			t.Fatalf("get enriched %s system root: %v", kind, err)
		}
		roots[kind] = persisted
	}
	return roots
}

func bootstrapRootsByKind(t *testing.T, items *store.ItemStore) map[item.Kind]item.Item {
	t.Helper()

	roots := make(map[item.Kind]item.Item, 2)
	for _, kind := range []item.Kind{item.KindUserRootFolder, item.KindPlaylistsFolder} {
		result, err := items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{kind}})
		if err != nil {
			t.Fatalf("query bootstrapped %s roots: %v", kind, err)
		}
		if len(result.Items) != 1 {
			t.Fatalf("bootstrapped %s root count = %d, want exactly one", kind, len(result.Items))
		}
		root, err := items.Get(t.Context(), result.Items[0].ID)
		if err != nil {
			t.Fatalf("get bootstrapped %s root: %v", kind, err)
		}
		roots[kind] = root
	}
	return roots
}

func enrichBootstrapRoot(t *testing.T, root item.Item, kind item.Kind) item.Item {
	t.Helper()

	created := time.Date(2021, time.Month(kind)+1, 2, 3, 4, 5, 678900000, time.FixedZone("root-preservation", 90*60))
	imageModified := created.Add(37 * time.Minute)
	path := filepath.Join("/metadata-refresh", kind.String(), "source")
	role := "enriched " + kind.String() + " person"
	sortOrder := int(kind) + 11
	root.DateCreated = &created
	root.Path = &path
	root.ProviderIDs.Set("Tmdb", "bootstrap-"+kind.String())
	root.ProviderIDs.Set("CustomProvider", "survives-restart")
	root.ImageInfos = []item.ImageInfo{
		{Type: item.ImageTypePrimary, Path: filepath.Join("/images", kind.String(), "primary.jpg"), DateModified: &imageModified, Width: 1200, Height: 1800, BlurHash: rootBootstrapString("root-primary-blur")},
		{Type: item.ImageTypeBackdrop, Index: 1, Path: filepath.Join("/images", kind.String(), "backdrop.jpg"), DateModified: &created, Width: 1920, Height: 1080},
	}
	root.LockedFields = []item.MetadataField{item.MetadataFieldName, item.MetadataFieldOverview}
	root.Values = item.ItemValues{
		Genres: []string{"Root Genre", kind.String()}, Tags: []string{"root-tag", "restart-preservation"}, Artists: []string{"Root Artist"}, Studios: []string{"Root Studio"}, ProductionLocations: []string{"Root Location"},
	}
	root.People = []item.PersonCredit{{PersonID: rootBootstrapPersonID(kind), Name: "Root " + kind.String() + " Person", Role: &role, Type: item.PersonKindDirector, SortOrder: &sortOrder}}
	return root
}

func rootBootstrapString(value string) *string { return &value }

func rootBootstrapPersonID(kind item.Kind) dto.UUID {
	return dto.UUID{0x52, 0x6f, 0x6f, 0x74, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, byte(kind)}
}
