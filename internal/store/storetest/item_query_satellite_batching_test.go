// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/migrate"
	storesqlite "github.com/deadfin-org/deadfin/internal/store/sqlite"
	moderncsqlite "modernc.org/sqlite"
)

// TestItemStoreQuerySatelliteEquivalenceSQLite proves that a page preserves
// every normalized Item relation while it is assembled as a page rather than
// as individual Get calls.
func TestItemStoreQuerySatelliteEquivalenceSQLite(t *testing.T) {
	runItemStoreQuerySatelliteEquivalence(t, openItemStore)
}

// TestItemStoreQuerySatelliteEquivalencePostgres exercises the identical
// public ItemStore contract when DEADFIN_TEST_POSTGRES_DSN is configured.
func TestItemStoreQuerySatelliteEquivalencePostgres(t *testing.T) {
	runItemStoreQuerySatelliteEquivalence(t, openPostgresItemStore)
}

func runItemStoreQuerySatelliteEquivalence(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	items := open(t)
	fixture := seedQuerySatelliteFixture(t, items)

	page, err := items.Query(t.Context(), store.ItemQuery{
		ItemIDs:                []dto.UUID{fixture.third.ID, fixture.first.ID, fixture.first.ID, fixture.second.ID},
		SortBy:                 []store.ItemSortBy{store.ItemSortBySortName},
		SortOrder:              []store.SortOrder{store.SortOrderAscending},
		EnableTotalRecordCount: true,
	})
	if err != nil {
		t.Fatalf("querying satellite page: %v", err)
	}
	if got, want := queryIDs(page.Items), []dto.UUID{fixture.first.ID, fixture.second.ID, fixture.third.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("satellite page IDs = %v, want stable deduplicated order %v", got, want)
	}
	if page.TotalRecordCount != len(fixture.items) {
		t.Fatalf("satellite page total = %d, want %d deduplicated rows", page.TotalRecordCount, len(fixture.items))
	}
	for index, want := range fixture.items {
		assertQuerySatelliteItemFacts(t, page.Items[index], want)
		assertQuerySatelliteMediaFacts(t, items, want.ID, fixture.media[want.ID])
	}
}

// TestItemStoreQuerySatelliteBatchingSQLite counts real reader SQL calls. A
// page has one item query and one batched query for each of the eight Item
// relation families. The number is therefore invariant for one and three
// returned items, while a zero-row page does not load relations at all.
func TestItemStoreQuerySatelliteBatchingSQLite(t *testing.T) {
	stores := openSatelliteQueryCountingSQLite(t)
	items := store.NewItemStore(stores)
	fixture := seedQuerySatelliteFixture(t, items)

	query := func(ids []dto.UUID, startIndex *int) store.ItemQuery {
		return store.ItemQuery{
			ItemIDs:    ids,
			SortBy:     []store.ItemSortBy{store.ItemSortBySortName},
			SortOrder:  []store.SortOrder{store.SortOrderAscending},
			StartIndex: startIndex,
		}
	}
	for _, tc := range []struct {
		name  string
		query store.ItemQuery
		want  int64
	}{
		{name: "one item", query: query([]dto.UUID{fixture.first.ID}, nil), want: 9},
		{name: "three items", query: query([]dto.UUID{fixture.third.ID, fixture.first.ID, fixture.first.ID, fixture.second.ID}, nil), want: 9},
		{name: "empty page", query: query([]dto.UUID{fixture.first.ID, fixture.second.ID, fixture.third.ID}, querySatelliteInt(3)), want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			satelliteReaderQueries.Store(0)
			page, err := items.Query(t.Context(), tc.query)
			if err != nil {
				t.Fatalf("querying satellite batch page: %v", err)
			}
			if tc.want == 1 && len(page.Items) != 0 {
				t.Fatalf("empty page items = %#v, want none", page.Items)
			}
			if got := satelliteReaderQueries.Load(); got != tc.want {
				t.Fatalf("reader query count = %d, want %d: page SQL plus one query per satellite relation, independent of page length", got, tc.want)
			}
		})
	}
}

// TestItemStoreQuerySatelliteBatchingSQLiteChunksLargePages protects the
// SQLite variable limit for relation batches. A 901-item page must split each
// relation lookup into a 900-ID batch and a one-ID batch rather than issuing a
// single oversized IN clause.
func TestItemStoreQuerySatelliteBatchingSQLiteChunksLargePages(t *testing.T) {
	stores := openSatelliteQueryCountingSQLite(t)
	items := store.NewItemStore(stores)
	parentID := querySatelliteUUID(t, 92_000)
	if err := items.Upsert(t.Context(), item.Item{ID: parentID, Kind: item.KindFolder, Name: querySatelliteString("batch parent")}); err != nil {
		t.Fatalf("upserting large-page parent: %v", err)
	}
	const childCount = 901
	children := make([]item.Item, childCount)
	for index := range children {
		children[index] = item.Item{
			ID:       querySatelliteUUID(t, 92_001+index),
			Kind:     item.KindMovie,
			ParentID: &parentID,
			Name:     querySatelliteString(fmt.Sprintf("batch child %04d", index)),
		}
	}
	if _, err := items.ReconcileChildren(t.Context(), parentID, children); err != nil {
		t.Fatalf("seeding 901-item page: %v", err)
	}

	satelliteReaderQueries.Store(0)
	page, err := items.Query(t.Context(), store.ItemQuery{ParentID: &parentID, Limit: querySatelliteInt(childCount)})
	if err != nil {
		t.Fatalf("querying 901-item page: %v", err)
	}
	if got := len(page.Items); got != childCount {
		t.Fatalf("large page item count = %d, want %d", got, childCount)
	}
	// The ParentID page also reads the parent's CollectionFolder marker in
	// the same reader transaction: one constant statement.
	if got, want := satelliteReaderQueries.Load(), int64(18); got != want {
		t.Fatalf("large-page reader query count = %d, want %d: parent marker, page SQL, and two bounded relation batches", got, want)
	}
}

// TestItemStoreDTOProjectionsAggregateUserDataBatchesSQLite keeps folder UserData
// aggregation page-scoped: selecting more folders must not add reader statements.
// The DTO builder's aggregate-value tests cover the returned fields; this test
// protects the independent no-N+1 store contract.
func TestItemStoreDTOProjectionsAggregateUserDataBatchesSQLite(t *testing.T) {
	stores := openSatelliteQueryCountingSQLite(t)
	items := store.NewItemStore(stores)
	userID := querySatelliteUUID(t, 93_000)
	first := item.Item{ID: querySatelliteUUID(t, 93_001), Kind: item.KindFolder, Name: querySatelliteString("aggregate first"), IsFolder: true}
	second := item.Item{ID: querySatelliteUUID(t, 93_002), Kind: item.KindBoxSet, Name: querySatelliteString("aggregate second"), IsFolder: true}
	for _, value := range []item.Item{first, second} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert aggregate projection folder %s: %v", value.ID, err)
		}
	}

	var statements int64
	for _, ids := range [][]dto.UUID{{first.ID}, {first.ID, second.ID}} {
		satelliteReaderQueries.Store(0)
		if _, err := items.DTOProjections(t.Context(), ids, &userID, false, true, false, false, ids); err != nil {
			t.Fatalf("project aggregate UserData for %d folders: %v", len(ids), err)
		}
		got := satelliteReaderQueries.Load()
		if got == 0 {
			t.Fatal("aggregate UserData projection issued no reader statement")
		}
		if statements == 0 {
			statements = got
			continue
		}
		if got != statements {
			t.Fatalf("aggregate UserData reader statements for %d folders = %d, want one-folder count %d; folders must share page-scoped aggregate queries", len(ids), got, statements)
		}
	}

	// A page with no aggregate folders, such as UserViews, reads only its
	// user-data rows. Jellyfin computes no played counts for library views.
	satelliteReaderQueries.Store(0)
	projection, err := items.DTOProjections(t.Context(), []dto.UUID{first.ID, second.ID}, &userID, false, true, false, false, nil)
	if err != nil {
		t.Fatalf("project UserData without aggregate folders: %v", err)
	}
	if got := satelliteReaderQueries.Load(); got != statements-1 {
		t.Fatalf("reader statements without aggregate folders = %d, want %d: the folder aggregate must not run", got, statements-1)
	}
	if len(projection.FolderUserDataCounts) != 0 {
		t.Fatalf("folder aggregates without aggregate folders = %#v, want none", projection.FolderUserDataCounts)
	}
}

type querySatelliteFixture struct {
	first, second, third item.Item
	items                []item.Item
	media                map[dto.UUID]querySatelliteMedia
}

type querySatelliteMedia struct {
	streams     []playback.MediaStream
	attachments []playback.MediaAttachment
	chapters    []store.Chapter
}

func seedQuerySatelliteFixture(t *testing.T, items *store.ItemStore) querySatelliteFixture {
	t.Helper()
	linkedID := querySatelliteUUID(t, 91_000)
	if err := items.Upsert(t.Context(), item.Item{ID: linkedID, Kind: item.KindFolder, Name: querySatelliteString("linked target")}); err != nil {
		t.Fatalf("upserting linked target: %v", err)
	}
	makeItem := func(value int, name, sortName string) item.Item {
		order := value
		role := fmt.Sprintf("role-%d", value)
		blurHash := fmt.Sprintf("blur-%d", value)
		it := item.Item{
			ID:       querySatelliteUUID(t, value),
			Kind:     item.KindMovie,
			Name:     querySatelliteString(name),
			SortName: querySatelliteString(sortName),
			ImageInfos: []item.ImageInfo{
				{Type: item.ImageTypeBackdrop, Index: 1, Path: fmt.Sprintf("%d-backdrop.jpg", value), Width: value + 100, BlurHash: &blurHash},
				{Type: item.ImageTypePrimary, Index: 0, Path: fmt.Sprintf("%d-primary.jpg", value), Width: value + 200},
			},
			LockedFields: []item.MetadataField{item.MetadataFieldCast, item.MetadataFieldGenres},
			Values: item.ItemValues{
				Genres:              []string{fmt.Sprintf("genre-%d-b", value), fmt.Sprintf("genre-%d-a", value)},
				Tags:                []string{fmt.Sprintf("tag-%d", value)},
				Artists:             []string{fmt.Sprintf("artist-%d", value)},
				ProductionLocations: []string{fmt.Sprintf("country-%d", value)},
			},
			People: []item.PersonCredit{
				{PersonID: querySatelliteUUID(t, value+1_000), Name: fmt.Sprintf("Person %d B", value), Role: &role, Type: item.PersonKindActor, SortOrder: &order},
				{PersonID: querySatelliteUUID(t, value+2_000), Name: fmt.Sprintf("Person %d A", value), Type: item.PersonKindDirector},
			},
			TrailerTypes:    []item.TrailerType{item.TrailerTypeArchive, item.TrailerTypeLocalTrailer},
			LinkedChildren:  []item.LinkedChildRef{{ItemID: linkedID, Type: item.LinkedChildTypeManual, SortOrder: &order}},
			AdditionalParts: []string{fmt.Sprintf("%d-part-1.mkv", value), fmt.Sprintf("%d-part-2.mkv", value)},
		}
		it.ProviderIDs.Set("Imdb", fmt.Sprintf("tt-%d", value))
		it.ProviderIDs.Set("Tmdb", fmt.Sprintf("tmdb-%d", value))
		return it
	}
	fixture := querySatelliteFixture{
		first:  makeItem(91_001, "first", "a first"),
		second: makeItem(91_002, "second", "b second"),
		third:  makeItem(91_003, "third", "c third"),
		media:  make(map[dto.UUID]querySatelliteMedia, 3),
	}
	fixture.items = []item.Item{fixture.first, fixture.second, fixture.third}
	for _, it := range fixture.items {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting satellite fixture %s: %v", it.ID, err)
		}
		name := fmt.Sprintf("chapter-%s", *it.Name)
		media := querySatelliteMedia{
			streams:     []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264", Language: querySatelliteString("en")}},
			attachments: []playback.MediaAttachment{{Index: 0, FileName: fmt.Sprintf("%s-cover.jpg", *it.Name), Codec: "mjpeg", MimeType: "image/jpeg"}},
			chapters:    []store.Chapter{{Index: 0, StartPositionTicks: int64(it.ID[15]), Name: &name, Extra: []byte(`{"marker":"intro"}`)}},
		}
		if err := items.ReplaceStreams(t.Context(), it.ID, media.streams); err != nil {
			t.Fatalf("seeding streams for %s: %v", it.ID, err)
		}
		if err := items.ReplaceAttachments(t.Context(), it.ID, media.attachments); err != nil {
			t.Fatalf("seeding attachments for %s: %v", it.ID, err)
		}
		if err := items.ReplaceChapters(t.Context(), it.ID, media.chapters); err != nil {
			t.Fatalf("seeding chapters for %s: %v", it.ID, err)
		}
		fixture.media[it.ID] = media
	}
	return fixture
}

func assertQuerySatelliteItemFacts(t *testing.T, got, want item.Item) {
	t.Helper()
	if got.ID != want.ID || got.Name == nil || want.Name == nil || *got.Name != *want.Name || got.SortName == nil || want.SortName == nil || *got.SortName != *want.SortName {
		t.Fatalf("item scalar facts = %#v, want identity/name/sort facts from %#v", got, want)
	}
	for _, tc := range []struct {
		name string
		got  any
		want any
	}{
		{name: "provider IDs", got: got.ProviderIDs.Snapshot(), want: want.ProviderIDs.Snapshot()},
		{name: "images", got: got.ImageInfos, want: want.ImageInfos},
		{name: "locked fields", got: got.LockedFields, want: want.LockedFields},
		{name: "values", got: got.Values, want: want.Values},
		{name: "people", got: got.People, want: want.People},
		{name: "trailer types", got: got.TrailerTypes, want: want.TrailerTypes},
		{name: "linked children", got: got.LinkedChildren, want: want.LinkedChildren},
		{name: "additional parts", got: got.AdditionalParts, want: want.AdditionalParts},
	} {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Fatalf("item %s %s = %#v, want %#v", want.ID, tc.name, tc.got, tc.want)
		}
	}
}

func assertQuerySatelliteMediaFacts(t *testing.T, items *store.ItemStore, id dto.UUID, want querySatelliteMedia) {
	t.Helper()
	streams, err := items.Streams(t.Context(), id)
	if err != nil || !reflect.DeepEqual(streams, want.streams) {
		t.Fatalf("streams for %s = %#v, %v; want %#v, nil", id, streams, err, want.streams)
	}
	attachments, err := items.Attachments(t.Context(), id)
	if err != nil || !reflect.DeepEqual(attachments, want.attachments) {
		t.Fatalf("attachments for %s = %#v, %v; want %#v, nil", id, attachments, err, want.attachments)
	}
	chapters, err := items.Chapters(t.Context(), id)
	if err != nil || !querySatelliteChaptersEqual(chapters, want.chapters) {
		t.Fatalf("chapters for %s = %#v, %v; want %#v, nil", id, chapters, err, want.chapters)
	}
}

func querySatelliteChaptersEqual(got, want []store.Chapter) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index].Index != want[index].Index ||
			got[index].StartPositionTicks != want[index].StartPositionTicks ||
			!reflect.DeepEqual(got[index].Name, want[index].Name) ||
			!reflect.DeepEqual(got[index].ImagePath, want[index].ImagePath) ||
			!querySatelliteJSONEqual(got[index].Extra, want[index].Extra) {
			return false
		}
	}
	return true
}

func querySatelliteJSONEqual(got, want json.RawMessage) bool {
	if len(got) == 0 || len(want) == 0 {
		return len(got) == len(want)
	}
	var gotValue, wantValue any
	return json.Unmarshal(got, &gotValue) == nil && json.Unmarshal(want, &wantValue) == nil && reflect.DeepEqual(gotValue, wantValue)
}

func querySatelliteUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing satellite fixture UUID %d: %v", value, err)
	}
	return id
}

func querySatelliteString(value string) *string { return &value }

func querySatelliteInt(value int) *int { return &value }

type satelliteQueryCountingStores struct {
	writer, reader *sql.DB
}

func (s satelliteQueryCountingStores) Writer() *sql.DB { return s.writer }
func (s satelliteQueryCountingStores) Reader() *sql.DB { return s.reader }
func (satelliteQueryCountingStores) Dialect() store.Dialect {
	return storesqlite.Dialect{}
}

func (s satelliteQueryCountingStores) Close() error {
	return s.reader.Close()
}

var (
	satelliteReaderQueries               atomic.Int64
	registerSatelliteQueryCountingDriver sync.Once
)

func openSatelliteQueryCountingSQLite(t *testing.T) store.Stores {
	t.Helper()
	registerSatelliteQueryCountingDriver.Do(func() {
		sql.Register("storetest-satellite-query-counting-sqlite", satelliteQueryCountingDriver{driver: &moderncsqlite.Driver{}})
	})
	path := filepath.Join(t.TempDir(), "satellite-query-count.db")
	writer, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open SQLite satellite fixture writer: %v", err)
	}
	reader, err := sql.Open("storetest-satellite-query-counting-sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_pragma=query_only(ON)")
	if err != nil {
		_ = writer.Close()
		t.Fatalf("open counting SQLite satellite fixture reader: %v", err)
	}
	stores := satelliteQueryCountingStores{writer: writer, reader: reader}
	if err := migrate.Up(t.Context(), writer, migrate.SQLite); err != nil {
		_ = stores.Close()
		_ = writer.Close()
		t.Fatalf("migrate counting SQLite satellite fixture: %v", err)
	}
	if err := store.BackfillMissingItemUserDataKeys(t.Context(), stores); err != nil {
		_ = stores.Close()
		_ = writer.Close()
		t.Fatalf("backfill counting SQLite satellite fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close counting SQLite satellite fixture reader: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Errorf("close counting SQLite satellite fixture writer: %v", err)
		}
	})
	return stores
}

type satelliteQueryCountingDriver struct{ driver driver.Driver }

func (d satelliteQueryCountingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.driver.Open(name)
	if err != nil {
		return nil, err
	}
	return satelliteQueryCountingConn{Conn: conn}, nil
}

type satelliteQueryCountingConn struct{ driver.Conn }

func (c satelliteQueryCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	satelliteReaderQueries.Add(1)
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, args)
}

func (c satelliteQueryCountingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	return beginner.BeginTx(ctx, opts)
}
