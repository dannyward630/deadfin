// SPDX-License-Identifier: GPL-2.0-only

package itemdelete

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

type fakeItems struct {
	values  map[dto.UUID]item.Item
	hidden  map[dto.UUID]bool
	deleted []dto.UUID
}

func (f *fakeItems) Get(_ context.Context, id dto.UUID) (item.Item, error) {
	value, found := f.values[id]
	if !found {
		return item.Item{}, store.ErrNotFound
	}
	return value, nil
}

func (f *fakeItems) Query(_ context.Context, query store.ItemQuery) (store.ItemQueryResult, error) {
	var result store.ItemQueryResult
	if len(query.ItemIDs) != 0 {
		for _, id := range query.ItemIDs {
			if value, found := f.values[id]; found && !f.hidden[id] {
				result.Items = append(result.Items, value)
			}
		}
		return result, nil
	}
	for _, value := range f.values {
		if f.under(value, *query.ParentID, query.Recursive) {
			result.Items = append(result.Items, value)
		}
	}
	return result, nil
}

func (f *fakeItems) under(value item.Item, parentID dto.UUID, recursive bool) bool {
	for current := value; current.ParentID != nil; current = f.values[*current.ParentID] {
		if *current.ParentID == parentID {
			return true
		}
		if !recursive {
			return false
		}
	}
	return false
}

func (f *fakeItems) Delete(_ context.Context, id dto.UUID) error {
	f.deleted = append(f.deleted, id)
	return nil
}

type fakeUsers map[dto.UUID]store.User

func (f fakeUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	user, found := f[id]
	if !found {
		return store.User{}, store.ErrNotFound
	}
	return user, nil
}

type fakeLibraries []string

func (f fakeLibraries) VirtualFolders(context.Context) ([]library.VirtualFolder, error) {
	return []library.VirtualFolder{{MediaPaths: []string(f)}}, nil
}

type removal struct{ parent, folder, id dto.UUID }

type fakePublisher struct{ removed []removal }

func (f *fakePublisher) PublishItemRemoved(parentID, collectionFolderID, id dto.UUID) {
	f.removed = append(f.removed, removal{parentID, collectionFolderID, id})
}

type deleteFixture struct {
	service   *Service
	items     *fakeItems
	publisher *fakePublisher
	root      string
	metadata  string
	library   item.Item
	admin     dto.UUID
	viewer    dto.UUID
}

func newDeleteFixture(t *testing.T) *deleteFixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "media", "movies")
	mustMkdir(t, root)
	libraryItem := item.Item{ID: testID(1), Kind: item.KindCollectionFolder, IsFolder: true, Path: ptr(filepath.Join(base, "config", "Movies"))}
	items := &fakeItems{values: map[dto.UUID]item.Item{libraryItem.ID: libraryItem}, hidden: map[dto.UUID]bool{}}
	publisher := &fakePublisher{}
	admin, viewer := testID(100), testID(101)
	users := fakeUsers{
		admin:  {ID: admin, Policy: []byte(`{"IsAdministrator":true,"EnableContentDeletion":true}`)},
		viewer: {ID: viewer, Policy: []byte(`{"EnableContentDeletion":false,"EnableContentDeletionFromFolders":[]}`)},
	}
	metadata := filepath.Join(base, "metadata")
	service := New(Options{Items: items, Users: users, Libraries: fakeLibraries{root}, Publisher: publisher, MetadataPath: metadata, TrickplayPath: filepath.Join(base, "trickplay")})
	return &deleteFixture{service: service, items: items, publisher: publisher, root: root, metadata: metadata, library: libraryItem, admin: admin, viewer: viewer}
}

func (f *deleteFixture) add(t *testing.T, value item.Item) item.Item {
	t.Helper()
	if value.ParentID == nil {
		value.ParentID = &f.library.ID
	}
	value.TopParentID = &f.library.ID
	f.items.values[value.ID] = value
	return value
}

func TestDeleteItemRemovesOwnMovieFolder(t *testing.T) {
	f := newDeleteFixture(t)
	folder := filepath.Join(f.root, "The Matrix (1999)")
	media := filepath.Join(folder, "The Matrix (1999).mkv")
	writeFile(t, media)
	writeFile(t, filepath.Join(folder, "poster.jpg"))
	other := filepath.Join(f.root, "Heat (1995)", "Heat (1995).mkv")
	writeFile(t, other)
	movie := f.add(t, item.Item{ID: testID(2), Kind: item.KindMovie, Path: &media})
	f.add(t, item.Item{ID: testID(3), Kind: item.KindMovie, Path: &other})
	metadataDir := filepath.Join(f.metadata, "library", movie.ID.String()[:2], movie.ID.String())
	writeFile(t, filepath.Join(metadataDir, "poster.jpg"))

	if err := f.service.DeleteItem(t.Context(), Actor{UserID: f.admin}, movie.ID); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	assertMissing(t, folder, metadataDir)
	assertPresent(t, other, f.root)
	if len(f.items.deleted) != 1 || f.items.deleted[0] != movie.ID {
		t.Fatalf("deleted rows = %v, want the movie", f.items.deleted)
	}
	if len(f.publisher.removed) != 1 || f.publisher.removed[0] != (removal{f.library.ID, f.library.ID, movie.ID}) {
		t.Fatalf("published = %#v", f.publisher.removed)
	}
}

func TestDeleteItemInLibraryRootKeepsTheRoot(t *testing.T) {
	f := newDeleteFixture(t)
	media := filepath.Join(f.root, "Alien (1979).mkv")
	writeFile(t, media)
	writeFile(t, filepath.Join(f.root, "Alien (1979).en.srt"))
	writeFile(t, filepath.Join(f.root, "Alien (1979).nfo"))
	sibling := filepath.Join(f.root, "Aliens (1986).mkv")
	writeFile(t, sibling)
	movie := f.add(t, item.Item{ID: testID(2), Kind: item.KindMovie, Path: &media})
	f.add(t, item.Item{ID: testID(3), Kind: item.KindMovie, Path: &sibling})

	if err := f.service.DeleteItem(t.Context(), Actor{UserID: f.admin}, movie.ID); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	assertMissing(t, media, filepath.Join(f.root, "Alien (1979).en.srt"), filepath.Join(f.root, "Alien (1979).nfo"))
	assertPresent(t, f.root, sibling)
}

func TestDeleteItemSharedFolderDeletesOnlyTheFiles(t *testing.T) {
	f := newDeleteFixture(t)
	folder := filepath.Join(f.root, "Collection")
	first := filepath.Join(folder, "First.mkv")
	second := filepath.Join(folder, "Second.mkv")
	writeFile(t, first)
	writeFile(t, filepath.Join(folder, "First-poster.jpg"))
	writeFile(t, second)
	writeFile(t, filepath.Join(folder, "Second-poster.jpg"))
	parent := f.add(t, item.Item{ID: testID(4), Kind: item.KindFolder, IsFolder: true, Path: &folder})
	movie := f.add(t, item.Item{ID: testID(2), Kind: item.KindMovie, Path: &first, ParentID: &parent.ID})
	f.add(t, item.Item{ID: testID(3), Kind: item.KindMovie, Path: &second, ParentID: &parent.ID})

	if err := f.service.DeleteItem(t.Context(), Actor{UserID: f.admin}, movie.ID); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	assertMissing(t, first, filepath.Join(folder, "First-poster.jpg"))
	assertPresent(t, second, filepath.Join(folder, "Second-poster.jpg"))
}

func TestDeleteFolderRemovesDescendantMetadata(t *testing.T) {
	f := newDeleteFixture(t)
	seriesPath := filepath.Join(f.root, "Show")
	episodePath := filepath.Join(seriesPath, "Season 1", "Show S01E01.mkv")
	writeFile(t, episodePath)
	series := f.add(t, item.Item{ID: testID(5), Kind: item.KindSeries, IsFolder: true, Path: &seriesPath})
	seasonPath := filepath.Dir(episodePath)
	season := f.add(t, item.Item{ID: testID(6), Kind: item.KindSeason, IsFolder: true, Path: &seasonPath, ParentID: &series.ID})
	episode := f.add(t, item.Item{ID: testID(7), Kind: item.KindEpisode, Path: &episodePath, ParentID: &season.ID})
	episodeMetadata := filepath.Join(f.metadata, "library", episode.ID.String()[:2], episode.ID.String())
	writeFile(t, filepath.Join(episodeMetadata, "x.jpg"))

	if err := f.service.DeleteItem(t.Context(), Actor{UserID: f.admin}, series.ID); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	assertMissing(t, seriesPath, episodeMetadata)
	assertPresent(t, f.root)
}

func TestDeleteItemNeverRemovesALibraryRoot(t *testing.T) {
	f := newDeleteFixture(t)
	root := f.root
	writeFile(t, filepath.Join(root, "Keep.mkv"))
	folder := f.add(t, item.Item{ID: testID(8), Kind: item.KindFolder, IsFolder: true, Path: &root})

	if err := f.service.DeleteItem(t.Context(), Actor{UserID: f.admin}, folder.ID); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	assertPresent(t, filepath.Join(root, "Keep.mkv"))
}

func TestDeleteItemPermissions(t *testing.T) {
	f := newDeleteFixture(t)
	media := filepath.Join(f.root, "Movie", "Movie.mkv")
	writeFile(t, media)
	movie := f.add(t, item.Item{ID: testID(2), Kind: item.KindMovie, Path: &media})
	personPath := filepath.Join(f.metadata, "People", "A")
	person := f.add(t, item.Item{ID: testID(9), Kind: item.KindPerson, Path: &personPath})

	if err := f.service.DeleteItem(t.Context(), Actor{UserID: f.viewer}, movie.ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("viewer delete = %v, want ErrUnauthorized", err)
	}
	if err := f.service.DeleteItem(t.Context(), Actor{UserID: f.admin}, person.ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("person delete = %v, want ErrUnauthorized", err)
	}
	if err := f.service.DeleteItem(t.Context(), Actor{UserID: testID(55)}, movie.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user delete = %v, want ErrNotFound", err)
	}
	f.items.hidden[movie.ID] = true
	if err := f.service.DeleteItem(t.Context(), Actor{UserID: f.admin}, movie.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("hidden delete = %v, want ErrNotFound", err)
	}
	delete(f.items.hidden, movie.ID)
	assertPresent(t, media)
	if len(f.items.deleted) != 0 {
		t.Fatalf("deleted rows = %v, want none", f.items.deleted)
	}

	// EnableContentDeletionFromFolders grants the libraries it names.
	users := f.service.users.(fakeUsers)
	users[f.viewer] = store.User{ID: f.viewer, Policy: []byte(`{"EnableContentDeletionFromFolders":["` + f.library.ID.Dashed() + `"]}`)}
	if err := f.service.DeleteItem(t.Context(), Actor{UserID: f.viewer}, movie.ID); err != nil {
		t.Fatalf("folder-granted delete: %v", err)
	}
	assertMissing(t, media)
}

func TestDeleteItemsAPIKeyAndOrder(t *testing.T) {
	f := newDeleteFixture(t)
	first := filepath.Join(f.root, "A", "A.mkv")
	second := filepath.Join(f.root, "B", "B.mkv")
	writeFile(t, first)
	writeFile(t, second)
	a := f.add(t, item.Item{ID: testID(2), Kind: item.KindMovie, Path: &first})
	b := f.add(t, item.Item{ID: testID(3), Kind: item.KindMovie, Path: &second})

	err := f.service.DeleteItems(t.Context(), Actor{APIKey: true}, []dto.UUID{a.ID, testID(99), b.ID})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteItems = %v, want ErrNotFound for the missing id", err)
	}
	assertMissing(t, first)
	assertPresent(t, second)
	if err := f.service.DeleteItems(t.Context(), Actor{}, []dto.UUID{b.ID}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("DeleteItems without user = %v, want ErrUnauthorized", err)
	}
	if err := f.service.DeleteItems(t.Context(), Actor{APIKey: true, UserID: f.viewer}, []dto.UUID{b.ID}); err != nil {
		t.Fatalf("API key with a user id: %v", err)
	}
	assertMissing(t, second)
}

func testID(value byte) dto.UUID {
	var id dto.UUID
	id[0], id[15] = 0xab, value
	return id
}

func ptr(value string) *string { return &value }

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertMissing(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists (err %v)", path, err)
		}
	}
}

func assertPresent(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s missing: %v", path, err)
		}
	}
}
