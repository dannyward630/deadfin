// SPDX-License-Identifier: GPL-2.0-only

//go:build linux

package library_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"golang.org/x/sys/unix"
)

// TestLinuxScannerProjectsFilesystemCreationTimesAndPagesByDateCreated covers
// the packaged Linux filesystem path end to end: filesystem creation fact, the
// real scanner, persisted items, and a public Fields=DateCreated response. The
// independent filesystem observation mirrors Jellyfin's documented fallback to
// the earlier inode status-change or modification time when STATX_BTIME is not
// exposed by a bind-mounted filesystem.
func TestLinuxScannerProjectsFilesystemCreationTimesAndPagesByDateCreated(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}

	photos := filepath.Join(t.TempDir(), "photos")
	if err := os.Mkdir(photos, 0o755); err != nil {
		t.Fatalf("create photo source: %v", err)
	}
	if _, err := harness.topology.AddVirtualFolder(t.Context(), "Creation Photos", "photos", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: photos}}}); err != nil {
		t.Fatalf("add photo source: %v", err)
	}

	fixtures := linuxDateCreatedFixtures(t, harness.source, photos)
	harness.chain = library.NewDefaultChain(naming.DefaultOptions())
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{Concurrency: 1})
	scannerScan(t, harness.scanner, "scan Linux creation-time fixtures")

	routes := router.New()
	handlers.RegisterItems(routes, harness.items, dtobuild.NewBuilder(harness.items, dtobuild.NewStoreEnricher(harness.items, nil, nil)), clock.NewReal())

	all := linuxDateCreatedItemsPage(t, routes, 0, len(fixtures))
	if all.TotalRecordCount != len(fixtures) {
		t.Fatalf("DateCreated total record count = %d, want %d media fixtures", all.TotalRecordCount, len(fixtures))
	}
	linuxAssertDateCreatedRows(t, all.Items, fixtures)

	expected := append([]linuxDateCreatedFixture(nil), fixtures...)
	sort.Slice(expected, func(left, right int) bool {
		return expected[left].created.After(expected[right].created)
	})
	linuxAssertDateCreatedOrder(t, all.Items, expected)

	page := linuxDateCreatedItemsPage(t, routes, 1, 2)
	if page.StartIndex != 1 {
		t.Fatalf("DateCreated page StartIndex = %d, want 1", page.StartIndex)
	}
	if len(page.Items) != 2 {
		t.Fatalf("DateCreated page size = %d, want 2", len(page.Items))
	}
	linuxAssertDateCreatedOrder(t, page.Items, expected[1:3])
}

type linuxDateCreatedFixture struct {
	path    string
	kind    item.Kind
	created time.Time
}

func linuxDateCreatedFixtures(t *testing.T, movies, photos string) []linuxDateCreatedFixture {
	t.Helper()
	fixtures := []linuxDateCreatedFixture{
		{path: filepath.Join(movies, "Creation Movie One (2024).mkv"), kind: item.KindMovie},
		{path: filepath.Join(movies, "Creation Movie Two (2024).mkv"), kind: item.KindMovie},
		{path: filepath.Join(photos, "creation-photo-one.jpg"), kind: item.KindPhoto},
		{path: filepath.Join(photos, "creation-photo-two.jpg"), kind: item.KindPhoto},
	}

	// Linux birth-time precision is a filesystem property. Recreate only this
	// tiny fixture until its observed values remain distinct after SQLite's
	// persisted timestamp precision, instead of deriving an order from sleeps or
	// from a wall-clock literal.
	for range 8 {
		for _, fixture := range fixtures {
			if err := os.Remove(fixture.path); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove creation-time fixture %q: %v", fixture.path, err)
			}
			writeScannerFile(t, fixture.path, "creation-time fixture")
		}
		for index := range fixtures {
			fixtures[index].created = linuxFilesystemCreationTime(t, fixtures[index].path).Truncate(time.Microsecond)
		}
		if linuxDateCreatedTimesDistinct(fixtures) {
			return fixtures
		}
	}
	t.Skip("filesystem birth-time precision cannot establish a deterministic DateCreated page order")
	return nil
}

func linuxFilesystemCreationTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat creation-time fixture %q: %v", path, err)
	}
	var stat unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &stat); err == nil {
		if stat.Mask&unix.STATX_BTIME != 0 {
			return time.Unix(stat.Btime.Sec, int64(stat.Btime.Nsec)).UTC()
		}
		return linuxEarlierFilesystemTime(
			time.Unix(stat.Ctime.Sec, int64(stat.Ctime.Nsec)).UTC(),
			time.Unix(stat.Mtime.Sec, int64(stat.Mtime.Nsec)).UTC(),
		)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return linuxEarlierFilesystemTime(time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec).UTC(), info.ModTime().UTC())
	}
	return info.ModTime().UTC()
}

func linuxEarlierFilesystemTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func linuxDateCreatedTimesDistinct(fixtures []linuxDateCreatedFixture) bool {
	seen := make(map[time.Time]struct{}, len(fixtures))
	for _, fixture := range fixtures {
		if _, duplicate := seen[fixture.created]; duplicate {
			return false
		}
		seen[fixture.created] = struct{}{}
	}
	return true
}

type linuxDateCreatedItemsResponse struct {
	Items            []linuxDateCreatedItem `json:"Items"`
	TotalRecordCount int                    `json:"TotalRecordCount"`
	StartIndex       int                    `json:"StartIndex"`
}

type linuxDateCreatedItem struct {
	Path        string     `json:"Path"`
	Type        item.Kind  `json:"Type"`
	DateCreated *time.Time `json:"DateCreated"`
}

func linuxDateCreatedItemsPage(t *testing.T, routes *router.Router, startIndex, limit int) linuxDateCreatedItemsResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/Items?IncludeItemTypes=Movie,Photo&Recursive=true&Fields=DateCreated,Path&EnableTotalRecordCount=true&SortBy=DateCreated&SortOrder=Descending&StartIndex="+strconv.Itoa(startIndex)+"&Limit="+strconv.Itoa(limit), nil)
	record := httptest.NewRecorder()
	routes.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET /Items DateCreated page status = %d, want 200; body=%s", record.Code, record.Body.String())
	}
	var response linuxDateCreatedItemsResponse
	if err := json.Unmarshal(record.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode DateCreated page: %v; body=%s", err, record.Body.String())
	}
	return response
}

func linuxAssertDateCreatedRows(t *testing.T, rows []linuxDateCreatedItem, fixtures []linuxDateCreatedFixture) {
	t.Helper()
	if len(rows) != len(fixtures) {
		t.Fatalf("DateCreated rows = %d, want %d", len(rows), len(fixtures))
	}
	expected := make(map[string]linuxDateCreatedFixture, len(fixtures))
	for _, fixture := range fixtures {
		expected[fixture.path] = fixture
	}
	for _, row := range rows {
		fixture, found := expected[row.Path]
		if !found {
			t.Fatalf("DateCreated row path %q is not a fixture", row.Path)
		}
		if row.Type != fixture.kind {
			t.Errorf("DateCreated row %q Type = %q, want %q", row.Path, row.Type, fixture.kind)
		}
		if row.DateCreated == nil {
			t.Errorf("DateCreated row %q omitted requested filesystem creation time", row.Path)
			continue
		}
		if !row.DateCreated.Equal(fixture.created) {
			t.Errorf("DateCreated row %q = %s, want filesystem birth time %s", row.Path, row.DateCreated.UTC(), fixture.created)
		}
		delete(expected, row.Path)
	}
	if len(expected) != 0 {
		t.Errorf("DateCreated response omitted fixtures: %#v", expected)
	}
}

func linuxAssertDateCreatedOrder(t *testing.T, rows []linuxDateCreatedItem, expected []linuxDateCreatedFixture) {
	t.Helper()
	if len(rows) != len(expected) {
		t.Fatalf("DateCreated ordered rows = %d, want %d", len(rows), len(expected))
	}
	for index, fixture := range expected {
		if rows[index].Path != fixture.path {
			t.Errorf("DateCreated descending row %d path = %q, want %q", index, rows[index].Path, fixture.path)
		}
		if rows[index].DateCreated == nil || !rows[index].DateCreated.Equal(fixture.created) {
			t.Errorf("DateCreated descending row %d = %v, want filesystem birth time %s", index, rows[index].DateCreated, fixture.created)
		}
	}
}
