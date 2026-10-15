// SPDX-License-Identifier: GPL-2.0-only

package jellyfinsource_test

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store/jellyfinsource"
)

const (
	containerOwner  = "4E703B0E-6E78-4EA6-9C48-43A2654C5CB1"
	containerFriend = "5F703B0E-6E78-4EA6-9C48-43A2654C5CB2"
	containerList   = "CCCCCCCC-0000-0000-0000-000000000001"
)

// containerFixture builds a jellyfin.db with one playlist and three movies.
// With linkedTable, the entries live in the LinkedChildren table; otherwise
// they live in the playlist's Data JSON as Jellyfin 10.11 writes them.
func containerFixture(t *testing.T, linkedTable bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jellyfin.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	data := `{"OwnerUserId":"4e703b0e6e784ea69c4843a2654c5cb1","OpenAccess":true,` +
		`"Shares":[{"UserId":"5f703b0e6e784ea69c4843a2654c5cb2","CanEdit":true}],"PlaylistMediaType":"Video",` +
		`"LinkedChildren":[{"Path":"/media/b.mkv","ItemId":"aaaaaaaa000000000000000000000002","Type":0},` +
		`{"Path":"/media/stale-a.mkv","LibraryItemId":"aaaaaaaa000000000000000000000001","Type":"Manual"},` +
		`{"Path":"/media/shortcut.mkv","Type":1},` +
		`{"Path":"/media/c.mkv"},` +
		`{"ItemId":"aaaaaaaa0000000000000000000000ff"}]}`
	statements := []string{
		`CREATE TABLE BaseItems (Id TEXT PRIMARY KEY, Type TEXT, Name TEXT, Path TEXT, Data TEXT, DateCreated TEXT)`,
		`INSERT INTO BaseItems VALUES ('AAAAAAAA-0000-0000-0000-000000000001','MediaBrowser.Controller.Entities.Movies.Movie','A','/media/a.mkv',NULL,NULL),
			('AAAAAAAA-0000-0000-0000-000000000002','MediaBrowser.Controller.Entities.Movies.Movie','B','/media/b.mkv',NULL,NULL),
			('AAAAAAAA-0000-0000-0000-000000000003','MediaBrowser.Controller.Entities.Movies.Movie','C','/media/c.mkv',NULL,NULL),
			('` + containerList + `','MediaBrowser.Controller.Playlists.Playlist','Road trip','/config/data/playlists/Road trip','` + data + `','2025-03-04 05:06:07')`,
	}
	if linkedTable {
		statements = append(statements,
			`CREATE TABLE LinkedChildren (ParentId TEXT, ChildId TEXT, ChildType INTEGER, SortOrder INTEGER)`,
			`INSERT INTO LinkedChildren VALUES ('`+containerList+`','AAAAAAAA-0000-0000-0000-000000000003',0,2),
				('`+containerList+`','AAAAAAAA-0000-0000-0000-000000000001',0,0),
				('`+containerList+`','AAAAAAAA-0000-0000-0000-000000000002',1,1),
				('`+containerList+`','AAAAAAAA-0000-0000-0000-0000000000FF',0,3)`)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture: %v\n%s", err, statement)
		}
	}
	return path
}

func TestSourceReadsPlaylists(t *testing.T) {
	for _, test := range []struct {
		name        string
		linkedTable bool
		children    []string
		unresolved  int
	}{
		// ItemId wins, then LibraryItemId, then Path; Shortcut entries drop.
		{name: "data json", children: []string{"/media/b.mkv", "/media/a.mkv", "/media/c.mkv"}, unresolved: 1},
		// Manual rows in SortOrder; the missing child has no path.
		{name: "linked children table", linkedTable: true, children: []string{"/media/a.mkv", "/media/c.mkv"}, unresolved: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			src, err := jellyfinsource.Open(t.Context(), containerFixture(t, test.linkedTable))
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			values, err := src.Containers(t.Context(), jellyfinsource.PlaylistType)
			if err != nil {
				t.Fatal(err)
			}
			if len(values) != 1 {
				t.Fatalf("containers = %+v", values)
			}
			got := values[0]
			owner, _ := dto.ParseUUID(containerOwner)
			friend, _ := dto.ParseUUID(containerFriend)
			if got.Name != "Road trip" || got.OwnerUserID != owner || !got.OpenAccess || got.MediaType != "Video" {
				t.Fatalf("container = %+v", got)
			}
			if !reflect.DeepEqual(got.Shares, []jellyfinsource.Share{{UserID: friend, CanEdit: true}}) {
				t.Fatalf("shares = %+v", got.Shares)
			}
			if got.DateCreated == nil || got.DateCreated.Year() != 2025 {
				t.Fatalf("date created = %v", got.DateCreated)
			}
			if !reflect.DeepEqual(got.ChildPaths, test.children) || got.UnresolvedChildren != test.unresolved {
				t.Fatalf("children = %v (unresolved %d), want %v (unresolved %d)", got.ChildPaths, got.UnresolvedChildren, test.children, test.unresolved)
			}
		})
	}
}
