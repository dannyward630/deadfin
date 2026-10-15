// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

func TestLibraryStoreContractSQLite(t *testing.T) {
	runLibraryStoreContract(t, openLibraryStore)
}

func TestLibraryStoreContractPostgres(t *testing.T) {
	runLibraryStoreContract(t, openPostgresLibraryStore)
}

func runLibraryStoreContract(t *testing.T, open func(*testing.T) *store.LibraryStore) {
	t.Helper()
	t.Run("creates lists reads and removes a virtual-folder registry row", func(t *testing.T) {
		libraries := open(t)
		want := contractLibrary(t, 900, "Cinema", "/var/lib/deadfin/root/default/Cinema", "movies", `<LibraryOptions><EnableEmbeddedTitles>true</EnableEmbeddedTitles><PreferredMetadataLanguage>pt-BR</PreferredMetadataLanguage></LibraryOptions>`)

		created, err := libraries.Create(t.Context(), want)
		if err != nil {
			t.Fatalf("create library: %v", err)
		}
		assertLibrary(t, created, want)

		got, err := libraries.Get(t.Context(), want.ID)
		if err != nil {
			t.Fatalf("get created library: %v", err)
		}
		assertLibrary(t, got, want)

		listed, err := libraries.List(t.Context())
		if err != nil {
			t.Fatalf("list created library: %v", err)
		}
		if len(listed) != 1 {
			t.Fatalf("listed library count = %d, want 1", len(listed))
		}
		assertLibrary(t, listed[0], want)

		if err := libraries.Delete(t.Context(), want.ID); err != nil {
			t.Fatalf("delete library: %v", err)
		}
		listed, err = libraries.List(t.Context())
		if err != nil {
			t.Fatalf("list after delete: %v", err)
		}
		if len(listed) != 0 {
			t.Fatalf("libraries after delete = %#v, want none", listed)
		}
		if _, err := libraries.Get(t.Context(), want.ID); err == nil {
			t.Fatal("get deleted library succeeded")
		}
	})

	t.Run("rejects duplicate virtual-folder names and physical paths", func(t *testing.T) {
		libraries := open(t)
		original := contractLibrary(t, 901, "Films", "/var/lib/deadfin/root/default/Films", "movies", `<LibraryOptions><EnableEmbeddedTitles>true</EnableEmbeddedTitles></LibraryOptions>`)
		if _, err := libraries.Create(t.Context(), original); err != nil {
			t.Fatalf("create original library: %v", err)
		}

		for _, duplicate := range []struct {
			name    string
			library store.Library
		}{
			{
				name:    "same name",
				library: contractLibrary(t, 902, "Films", "/var/lib/deadfin/root/default/Films-2", "movies", `<LibraryOptions><EnableEmbeddedTitles>true</EnableEmbeddedTitles></LibraryOptions>`),
			},
			{
				name:    "same path",
				library: contractLibrary(t, 903, "Films-2", "/var/lib/deadfin/root/default/Films", "movies", `<LibraryOptions><EnableEmbeddedTitles>true</EnableEmbeddedTitles></LibraryOptions>`),
			},
		} {
			t.Run(duplicate.name, func(t *testing.T) {
				if _, err := libraries.Create(t.Context(), duplicate.library); err == nil {
					t.Fatalf("create duplicate library %#v succeeded", duplicate.library)
				}
			})
		}

		listed, err := libraries.List(t.Context())
		if err != nil {
			t.Fatalf("list after duplicate rejections: %v", err)
		}
		if len(listed) != 1 {
			t.Fatalf("libraries after duplicate rejections = %#v, want only original", listed)
		}
		assertLibrary(t, listed[0], original)
	})
}

func openLibraryStore(t *testing.T) *store.LibraryStore {
	t.Helper()
	var stores store.Stores
	storetest.Run(t, func(t *testing.T) store.Stores {
		opened, err := sqlite.Open(t.Context(), ":memory:")
		if err != nil {
			t.Fatalf("open LibraryStore SQLite fixture: %v", err)
		}
		stores = opened
		return opened
	})
	return store.NewLibraryStore(stores)
}

func openPostgresLibraryStore(t *testing.T) *store.LibraryStore {
	t.Helper()
	return store.NewLibraryStore(storetest.OpenPostgres(t))
}

func contractLibrary(t *testing.T, value int, name, path, collectionType, options string) store.Library {
	t.Helper()
	return store.Library{
		ID:             contractUUID(t, value),
		Name:           name,
		Path:           path,
		CollectionType: collectionType,
		Options:        options,
	}
}

func assertLibrary(t *testing.T, got, want store.Library) {
	t.Helper()
	if got.ID != want.ID || got.Name != want.Name || got.Path != want.Path || got.CollectionType != want.CollectionType || got.Options != want.Options {
		t.Fatalf("library = %#v, want %#v", got, want)
	}
}
