// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/postgres"
)

// TestScopedDerivedRepairEquivalenceSQLite and its PostgreSQL twin run
// randomized sequences of scanner reconciles, adoptions, kind changes,
// reparents, deletes, upserts, and metadata edits on a fixture library. Every
// write repairs only its affected subtree. After each write the test snapshots
// the derived state (closure rows, top/series/season IDs, clean names, clean
// satellites, user-data keys), runs the global RepairDerived on the same data,
// and requires an identical snapshot.
func TestScopedDerivedRepairEquivalenceSQLite(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			items, stores := openReviewerItemStore(t)
			runScopedDerivedEquivalence(t, items, stores, seed, 120)
		})
	}
}

func TestScopedDerivedRepairEquivalencePostgres(t *testing.T) {
	for _, seed := range []uint64{1, 2} {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			items, stores := openPostgresReviewerItemStore(t)
			runScopedDerivedEquivalence(t, items, stores, seed, 80)
		})
	}
}

type derivedFixture struct {
	t      *testing.T
	ctx    context.Context
	items  *store.ItemStore
	stores store.Stores
	rng    *rand.Rand
	next   int
	agg    dto.UUID
}

func runScopedDerivedEquivalence(t *testing.T, items *store.ItemStore, stores store.Stores, seed uint64, steps int) {
	t.Helper()
	f := &derivedFixture{t: t, ctx: t.Context(), items: items, stores: stores, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), next: 700000}
	f.seed()
	f.assertEquivalent("seed fixture")
	operations := []struct {
		name   string
		weight int
		run    func() string
	}{
		{"reconcile", 8, f.reconcileRandomFolder},
		{"upsert", 3, f.upsertRandom},
		{"move", 2, f.moveRandom},
		{"delete", 1, f.deleteRandom},
		{"edit", 2, f.editRandom},
	}
	total := 0
	for _, operation := range operations {
		total += operation.weight
	}
	for step := range steps {
		pick := f.rng.IntN(total)
		for _, operation := range operations {
			if pick < operation.weight {
				description := operation.run()
				f.assertEquivalent(fmt.Sprintf("step %d %s: %s", step, operation.name, description))
				break
			}
			pick -= operation.weight
		}
	}
}

func (f *derivedFixture) id() dto.UUID {
	f.next++
	return contractUUID(f.t, f.next)
}

func (f *derivedFixture) personID(n int) dto.UUID { return contractUUID(f.t, 900000+n) }

func (f *derivedFixture) seed() {
	f.agg = f.id()
	f.upsert(item.Item{ID: f.agg, Kind: item.KindAggregateFolder, Name: stringPtr("root"), IsFolder: true})
	movies := f.child(f.agg, item.KindCollectionFolder, "Movies")
	tv := f.child(f.agg, item.KindCollectionFolder, "Shows")
	music := f.child(f.agg, item.KindCollectionFolder, "Music")
	for range 3 {
		folder := f.child(movies, item.KindFolder, "Movie folder")
		movie := f.newItem(folder, item.KindMovie)
		f.upsert(movie)
		trailer := f.newItem(movie.ID, item.KindVideo)
		extraType := item.ExtraTypeTrailer
		trailer.ExtraType = &extraType
		f.upsert(trailer)
	}
	for range 2 {
		series := f.newItem(tv, item.KindSeries)
		f.upsert(series)
		for seasonIndex := 1; seasonIndex <= 2; seasonIndex++ {
			season := f.newItem(series.ID, item.KindSeason)
			season.IndexNumber = intPtr(seasonIndex)
			f.upsert(season)
			for episodeIndex := 1; episodeIndex <= 3; episodeIndex++ {
				episode := f.newItem(season.ID, item.KindEpisode)
				episode.ParentIndexNumber = intPtr(seasonIndex)
				episode.IndexNumber = intPtr(episodeIndex)
				f.upsert(episode)
			}
		}
	}
	artist := f.newItem(music, item.KindMusicArtist)
	f.upsert(artist)
	album := f.newItem(artist.ID, item.KindMusicAlbum)
	f.upsert(album)
	for track := 1; track <= 3; track++ {
		audio := f.newItem(album.ID, item.KindAudio)
		audio.IndexNumber = intPtr(track)
		f.upsert(audio)
	}
	nested := f.child(f.child(movies, item.KindFolder, "Nested"), item.KindFolder, "Deeper")
	f.upsert(f.newItem(nested, item.KindMovie))
	f.upsert(item.Item{ID: f.id(), Kind: item.KindGenre, Name: stringPtr("Drama")})
}

func (f *derivedFixture) child(parent dto.UUID, kind item.Kind, name string) dto.UUID {
	value := item.Item{ID: f.id(), Kind: kind, Name: stringPtr(fmt.Sprintf("%s %d", name, f.next)), ParentID: &parent, IsFolder: true}
	f.upsert(value)
	return value.ID
}

var derivedFixtureNames = []string{"The Thing", "Amélie", "  Spaced   Out ", "Ça Va", "Ünïcödé", "Plain", "BBC: One"}

// newItem returns a fresh item of kind under parent with random metadata,
// provider IDs, genres, album artists, and person credits.
func (f *derivedFixture) newItem(parent dto.UUID, kind item.Kind) item.Item {
	value := item.Item{ID: f.id(), Kind: kind, ParentID: &parent}
	if parent.IsNil() {
		value.ParentID = nil
	}
	switch kind {
	case item.KindFolder, item.KindCollectionFolder, item.KindSeries, item.KindSeason, item.KindMusicArtist, item.KindMusicAlbum:
		value.IsFolder = true
	}
	f.mutateMetadata(&value)
	return value
}

func (f *derivedFixture) mutateMetadata(value *item.Item) {
	name := derivedFixtureNames[f.rng.IntN(len(derivedFixtureNames))] + fmt.Sprintf(" %d", f.rng.IntN(5))
	value.Name = &name
	var providers item.ProviderIDs
	for _, provider := range []string{"Tmdb", "Imdb", "Tvdb", "MusicBrainzArtist", "MusicBrainzAlbum", "Custom"} {
		if f.rng.IntN(2) == 0 {
			providers.Set(provider, fmt.Sprintf("%s-%d", strings.ToLower(provider), f.rng.IntN(4)))
		}
	}
	value.ProviderIDs = providers
	value.Values = item.ItemValues{}
	for _, genre := range []string{"Drama", "Comédie", "Sci-Fi", "Horror"} {
		if f.rng.IntN(3) == 0 {
			value.Values.Genres = append(value.Values.Genres, genre)
		}
	}
	if f.rng.IntN(3) == 0 {
		value.Values.AlbumArtists = []string{fmt.Sprintf("Artist %d", f.rng.IntN(3))}
	}
	if f.rng.IntN(4) == 0 {
		value.Values.Tags = []string{fmt.Sprintf("tag %d", f.next+f.rng.IntN(1000))}
	}
	if f.rng.IntN(3) == 0 {
		album := fmt.Sprintf("Album %d", f.rng.IntN(3))
		value.Album = &album
	}
	value.People = nil
	used := make(map[int]bool)
	for range f.rng.IntN(4) {
		n := f.rng.IntN(12)
		if used[n] {
			continue
		}
		used[n] = true
		value.People = append(value.People, item.PersonCredit{PersonID: f.personID(n), Name: fmt.Sprintf("Persón %d", n), Type: item.PersonKindActor, SortOrder: intPtr(len(value.People))})
	}
}

func (f *derivedFixture) upsert(value item.Item) {
	f.t.Helper()
	if err := f.items.Upsert(f.ctx, value); err != nil {
		f.t.Fatalf("upsert %s %s: %v", value.Kind, value.ID, err)
	}
}

type derivedFixtureRow struct {
	id       dto.UUID
	kind     string
	parentID *dto.UUID
}

func (f *derivedFixture) rows() []derivedFixtureRow {
	f.t.Helper()
	result, err := f.stores.Writer().QueryContext(f.ctx, "SELECT id, kind, parent_id FROM items ORDER BY id")
	if err != nil {
		f.t.Fatalf("list items: %v", err)
	}
	defer func() { _ = result.Close() }()
	var rows []derivedFixtureRow
	for result.Next() {
		var rawID, rawParent any
		var row derivedFixtureRow
		if err := result.Scan(&rawID, &row.kind, &rawParent); err != nil {
			f.t.Fatalf("scan item: %v", err)
		}
		row.id = fixtureUUID(f.t, rawID)
		if rawParent != nil {
			parent := fixtureUUID(f.t, rawParent)
			row.parentID = &parent
		}
		rows = append(rows, row)
	}
	if err := result.Err(); err != nil {
		f.t.Fatalf("list items: %v", err)
	}
	return rows
}

func (f *derivedFixture) folders() []derivedFixtureRow {
	var out []derivedFixtureRow
	for _, row := range f.rows() {
		switch row.kind {
		case "AggregateFolder", "CollectionFolder", "Folder", "Series", "Season", "MusicArtist", "MusicAlbum", "Movie":
			out = append(out, row)
		}
	}
	return out
}

// selfAndAncestors returns id and every ancestor of id.
func (f *derivedFixture) selfAndAncestors(id dto.UUID) map[dto.UUID]bool {
	f.t.Helper()
	ancestors, err := f.items.Ancestors(f.ctx, id)
	if err != nil {
		f.t.Fatalf("ancestors %s: %v", id, err)
	}
	out := map[dto.UUID]bool{id: true}
	for _, ancestor := range ancestors {
		out[ancestor] = true
	}
	return out
}

func (f *derivedFixture) randomKind() item.Kind {
	kinds := []item.Kind{item.KindFolder, item.KindMovie, item.KindVideo, item.KindSeries, item.KindSeason, item.KindEpisode, item.KindMusicAlbum, item.KindAudio, item.KindMusicArtist, item.KindTrailer}
	return kinds[f.rng.IntN(len(kinds))]
}

func (f *derivedFixture) reconcileRandomFolder() string {
	f.t.Helper()
	folders := f.folders()
	parent := folders[f.rng.IntN(len(folders))].id
	current, err := f.items.ReconciliationChildren(f.ctx, parent)
	if err != nil {
		f.t.Fatalf("reconciliation children %s: %v", parent, err)
	}
	var desired []item.Item
	var actions []string
	for _, child := range current {
		switch roll := f.rng.IntN(10); {
		case roll < 2:
			actions = append(actions, "drop")
			continue
		case roll < 5:
			f.mutateMetadata(&child)
			actions = append(actions, "modify")
		case roll < 6:
			child.Kind = f.randomKind()
			actions = append(actions, "kind")
		}
		if f.rng.IntN(4) == 0 && child.Kind == item.KindEpisode {
			child.ParentIndexNumber = intPtr(f.rng.IntN(3))
			child.IndexNumber = intPtr(f.rng.IntN(9))
		}
		desired = append(desired, child)
	}
	for range f.rng.IntN(3) {
		desired = append(desired, f.newItem(parent, f.randomKind()))
		actions = append(actions, "add")
	}
	if f.rng.IntN(4) == 0 {
		blocked := f.selfAndAncestors(parent)
		var candidates []dto.UUID
		for _, row := range f.rows() {
			if !blocked[row.id] && row.kind != "AggregateFolder" && (row.parentID == nil || *row.parentID != parent) && row.kind != "Person" && row.kind != "Genre" {
				candidates = append(candidates, row.id)
			}
		}
		if len(candidates) != 0 {
			adopted, err := f.items.Get(f.ctx, candidates[f.rng.IntN(len(candidates))])
			if err != nil {
				f.t.Fatalf("get adoption candidate: %v", err)
			}
			adopted.ParentID = &parent
			desired = append(desired, adopted)
			actions = append(actions, "adopt")
		}
	}
	persons := make(map[dto.UUID]string)
	for _, child := range desired {
		for _, credit := range child.People {
			persons[credit.PersonID] = credit.Name
		}
	}
	var virtuals []item.Item
	for id, name := range persons {
		if f.rng.IntN(2) == 0 {
			virtuals = append(virtuals, item.Item{ID: id, Kind: item.KindPerson, Name: stringPtr(name)})
		}
	}
	if _, err := f.items.ReconcileScannedPlan(f.ctx, parent, desired, nil, virtuals); err != nil {
		f.t.Fatalf("reconcile %s with %v: %v", parent, actions, err)
	}
	return fmt.Sprintf("parent %s %v", parent, actions)
}

func (f *derivedFixture) upsertRandom() string {
	f.t.Helper()
	if f.rng.IntN(5) == 0 {
		value := f.newItem(dto.NilUUID, f.randomKind())
		f.upsert(value)
		return "new root " + value.ID.String()
	}
	rows := f.rows()
	target := rows[f.rng.IntN(len(rows))]
	if target.kind == "AggregateFolder" {
		return "skip aggregate folder"
	}
	value, err := f.items.Get(f.ctx, target.id)
	if err != nil {
		f.t.Fatalf("get upsert target: %v", err)
	}
	f.mutateMetadata(&value)
	description := "metadata " + target.id.String()
	if f.rng.IntN(3) == 0 {
		folders := f.folders()
		parent := folders[f.rng.IntN(len(folders))].id
		if parent != target.id {
			if ancestors := f.selfAndAncestors(parent); !ancestors[target.id] {
				value.ParentID = &parent
				description += " reparent " + parent.String()
			}
		}
	}
	if f.rng.IntN(4) == 0 {
		value.Kind = f.randomKind()
		description += " kind " + value.Kind.String()
	}
	f.upsert(value)
	return description
}

func (f *derivedFixture) moveRandom() string {
	f.t.Helper()
	rows := f.rows()
	target := rows[f.rng.IntN(len(rows))]
	if target.kind == "AggregateFolder" {
		return "skip aggregate folder"
	}
	if f.rng.IntN(6) == 0 {
		if err := f.items.MoveSubtree(f.ctx, target.id, nil); err != nil {
			f.t.Fatalf("move %s to root: %v", target.id, err)
		}
		return "to root " + target.id.String()
	}
	folders := f.folders()
	parent := folders[f.rng.IntN(len(folders))].id
	if f.selfAndAncestors(parent)[target.id] {
		return "skip cycle"
	}
	if err := f.items.MoveSubtree(f.ctx, target.id, &parent); err != nil {
		f.t.Fatalf("move %s under %s: %v", target.id, parent, err)
	}
	return fmt.Sprintf("%s under %s", target.id, parent)
}

func (f *derivedFixture) deleteRandom() string {
	f.t.Helper()
	rows := f.rows()
	target := rows[f.rng.IntN(len(rows))]
	if target.kind == "AggregateFolder" || target.kind == "CollectionFolder" {
		return "skip library folder"
	}
	if err := f.items.Delete(f.ctx, target.id); err != nil {
		f.t.Fatalf("delete %s: %v", target.id, err)
	}
	return target.id.String()
}

func (f *derivedFixture) editRandom() string {
	f.t.Helper()
	rows := f.rows()
	target := rows[f.rng.IntN(len(rows))]
	value, err := f.items.Get(f.ctx, target.id)
	if err != nil {
		f.t.Fatalf("get edit target: %v", err)
	}
	f.mutateMetadata(&value)
	var entities []item.Item
	if f.rng.IntN(3) == 0 {
		entities = append(entities, item.Item{ID: f.id(), Kind: item.KindStudio, Name: stringPtr(fmt.Sprintf("Studio %d", f.next))})
	}
	if err := f.items.EditItems(f.ctx, []item.Item{value}, entities); err != nil {
		f.t.Fatalf("edit %s: %v", target.id, err)
	}
	return target.id.String()
}

func (f *derivedFixture) assertEquivalent(step string) {
	f.t.Helper()
	scoped := f.derivedSnapshot()
	if err := f.items.RepairDerived(f.ctx); err != nil {
		f.t.Fatalf("%s: global repair: %v", step, err)
	}
	global := f.derivedSnapshot()
	for _, table := range []string{"ancestors", "items", "item_user_data_keys", "item_values", "people"} {
		if reflect.DeepEqual(scoped[table], global[table]) {
			continue
		}
		f.t.Fatalf("%s: scoped %s differs from the global repair:\nonly scoped: %v\nonly global: %v", step, table, setDifference(scoped[table], global[table]), setDifference(global[table], scoped[table]))
	}
	var orphans int
	if err := f.stores.Writer().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM item_values WHERE NOT EXISTS (SELECT 1 FROM item_value_map WHERE item_value_map.value_id = item_values.id)").Scan(&orphans); err != nil {
		f.t.Fatalf("%s: count orphan values: %v", step, err)
	}
	if orphans != 0 {
		f.t.Fatalf("%s: %d orphan item_values rows", step, orphans)
	}
}

func setDifference(left, right []string) []string {
	var out []string
	for _, value := range left {
		if !slices.Contains(right, value) {
			out = append(out, value)
		}
	}
	return out
}

// derivedSnapshot reads every derived encoding as sorted text rows.
func (f *derivedFixture) derivedSnapshot() map[string][]string {
	f.t.Helper()
	queries := map[string]string{
		"ancestors":           "SELECT item_id, ancestor_id FROM ancestors",
		"items":               "SELECT id, clean_name, top_parent_id, series_id, season_id FROM items",
		"item_user_data_keys": "SELECT item_id, custom_key, is_live_tv_channel_name FROM item_user_data_keys",
		"item_values":         "SELECT id, kind, value, clean_value FROM item_values",
		"people":              "SELECT id, name, clean_name FROM people",
	}
	snapshot := make(map[string][]string, len(queries))
	for table, query := range queries {
		rows, err := f.stores.Writer().QueryContext(f.ctx, query)
		if err != nil {
			f.t.Fatalf("snapshot %s: %v", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			f.t.Fatalf("snapshot %s columns: %v", table, err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for index := range values {
				dest[index] = &values[index]
			}
			if err := rows.Scan(dest...); err != nil {
				f.t.Fatalf("snapshot %s scan: %v", table, err)
			}
			parts := make([]string, len(values))
			for index, value := range values {
				parts[index] = snapshotText(columns[index], value)
			}
			snapshot[table] = append(snapshot[table], strings.Join(parts, " | "))
		}
		if err := rows.Close(); err != nil {
			f.t.Fatalf("snapshot %s close: %v", table, err)
		}
		sort.Strings(snapshot[table])
	}
	return snapshot
}

func snapshotText(column string, value any) string {
	if value == nil {
		return "<nil>"
	}
	if column == "id" || strings.HasSuffix(column, "_id") {
		switch raw := value.(type) {
		case []byte:
			if len(raw) == 16 {
				var id dto.UUID
				copy(id[:], raw)
				return id.String()
			}
		case [16]byte:
			return dto.UUID(raw).String()
		case string:
			if id, err := dto.ParseUUID(raw); err == nil {
				return id.String()
			}
		}
	}
	switch raw := value.(type) {
	case []byte:
		return string(raw)
	case bool:
		if raw {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprint(raw)
	}
}

func fixtureUUID(t *testing.T, raw any) dto.UUID {
	t.Helper()
	switch value := raw.(type) {
	case []byte:
		if len(value) == 16 {
			var id dto.UUID
			copy(id[:], value)
			return id
		}
		id, err := dto.ParseUUID(string(value))
		if err != nil {
			t.Fatalf("parse UUID %q: %v", value, err)
		}
		return id
	case [16]byte:
		return dto.UUID(value)
	case string:
		id, err := dto.ParseUUID(value)
		if err != nil {
			t.Fatalf("parse UUID %q: %v", value, err)
		}
		return id
	default:
		t.Fatalf("scan UUID %T", raw)
		return dto.NilUUID
	}
}

// TestScopedDerivedRepairConcurrentPostgres runs scoped writers at the same
// time on PostgreSQL, where they no longer share one global lock. One store
// has one writer connection, so each worker uses its own store on the same
// schema, as separate server processes would. Each group of workers owns two
// libraries and one show folder: a mover moves the show folder between the
// two libraries (which changes the top parent of every descendant), a
// rewriter reconciles the show folder's children (renames and kind changes
// that change the derived series and season of every grandchild), and a leaf
// worker reconciles the grandchildren. All workers credit the same people and
// genres. Every operation must succeed (no deadlock), and the final derived
// state must equal a global repair.
func TestScopedDerivedRepairConcurrentPostgres(t *testing.T) {
	const groups = 3
	pool := openPostgresStorePool(t, 1+3*groups)
	items, stores := store.NewItemStore(pool[0]), pool[0]
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	f := &derivedFixture{t: t, ctx: ctx, items: items, stores: stores, rng: rand.New(rand.NewPCG(7, 11)), next: 700000}
	f.agg = f.id()
	f.upsert(item.Item{ID: f.agg, Kind: item.KindAggregateFolder, Name: stringPtr("root"), IsFolder: true})
	type group struct {
		libraries [2]dto.UUID
		show      dto.UUID
	}
	fixtures := make([]group, groups)
	for index := range fixtures {
		fixtures[index].libraries = [2]dto.UUID{f.child(f.agg, item.KindCollectionFolder, "Library"), f.child(f.agg, item.KindCollectionFolder, "Library")}
		fixtures[index].show = f.child(fixtures[index].libraries[0], item.KindFolder, "Show")
		for range 3 {
			folder := f.child(fixtures[index].show, item.KindFolder, "Folder")
			for range 3 {
				f.upsert(f.newItem(folder, item.KindEpisode))
			}
		}
	}
	f.assertEquivalent("concurrent fixture")

	worker := func(index, role int) *derivedFixture {
		opened := pool[1+3*index+role]
		return &derivedFixture{t: t, ctx: ctx, items: store.NewItemStore(opened), stores: opened, rng: rand.New(rand.NewPCG(uint64(index), uint64(role))), next: 1000000 + (3*index+role)*20000}
	}
	var operations []func(context.Context) error
	for index, fixture := range fixtures {
		mover, rewriter, leaves := worker(index, 0), worker(index, 1), worker(index, 2)
		operations = append(
			operations,
			func(ctx context.Context) error {
				return mover.concurrentMoves(ctx, fixture.show, fixture.libraries, 20)
			},
			func(ctx context.Context) error { return rewriter.concurrentLibraryRewrites(ctx, fixture.show, 10) },
			func(ctx context.Context) error { return leaves.concurrentLeafReconciles(ctx, fixture.show, 20) },
		)
	}
	runConcurrentItemStoreOperations(t, ctx, operations)
	f.assertEquivalent("after concurrent writes")
}

// concurrentMoves moves show between the two libraries.
func (f *derivedFixture) concurrentMoves(ctx context.Context, show dto.UUID, libraries [2]dto.UUID, rounds int) error {
	for round := range rounds {
		target := libraries[(round+1)%2]
		if err := f.items.MoveSubtree(ctx, show, &target); err != nil {
			return fmt.Errorf("move %s round %d: %w", show, round, err)
		}
	}
	return nil
}

// concurrentLibraryRewrites reconciles library with every current child kept,
// each renamed, re-credited, and given a random folder-like kind.
func (f *derivedFixture) concurrentLibraryRewrites(ctx context.Context, library dto.UUID, rounds int) error {
	kinds := []item.Kind{item.KindFolder, item.KindSeries, item.KindSeason, item.KindMusicAlbum}
	for round := range rounds {
		current, err := f.items.ReconciliationChildren(ctx, library)
		if err != nil {
			return fmt.Errorf("library %s round %d children: %w", library, round, err)
		}
		for index := range current {
			f.mutateMetadata(&current[index])
			current[index].Kind = kinds[f.rng.IntN(len(kinds))]
			current[index].IsFolder = true
		}
		if _, err := f.items.ReconcileScannedPlan(ctx, library, current, nil, f.personVirtuals(current)); err != nil {
			return fmt.Errorf("library %s round %d reconcile: %w", library, round, err)
		}
	}
	return nil
}

// concurrentLeafReconciles reconciles the children of random folders under
// library: drops, modifies, and adds leaf items.
func (f *derivedFixture) concurrentLeafReconciles(ctx context.Context, library dto.UUID, rounds int) error {
	kinds := []item.Kind{item.KindEpisode, item.KindMovie, item.KindAudio, item.KindVideo}
	for round := range rounds {
		folders, err := f.items.ReconciliationChildren(ctx, library)
		if err != nil {
			return fmt.Errorf("library %s round %d folders: %w", library, round, err)
		}
		folder := folders[f.rng.IntN(len(folders))].ID
		current, err := f.items.ReconciliationChildren(ctx, folder)
		if err != nil {
			return fmt.Errorf("folder %s round %d children: %w", folder, round, err)
		}
		var desired []item.Item
		for _, child := range current {
			if f.rng.IntN(4) == 0 {
				continue
			}
			f.mutateMetadata(&child)
			child.ParentIndexNumber = intPtr(f.rng.IntN(3))
			child.IndexNumber = intPtr(f.rng.IntN(9))
			desired = append(desired, child)
		}
		for range 1 + f.rng.IntN(2) {
			leaf := f.newItem(folder, kinds[f.rng.IntN(len(kinds))])
			leaf.ParentIndexNumber = intPtr(1)
			leaf.IndexNumber = intPtr(f.rng.IntN(9))
			desired = append(desired, leaf)
		}
		if _, err := f.items.ReconcileScannedPlan(ctx, folder, desired, nil, f.personVirtuals(desired)); err != nil {
			return fmt.Errorf("folder %s round %d reconcile: %w", folder, round, err)
		}
	}
	return nil
}

func (f *derivedFixture) personVirtuals(values []item.Item) []item.Item {
	persons := make(map[dto.UUID]string)
	for _, value := range values {
		for _, credit := range value.People {
			persons[credit.PersonID] = credit.Name
		}
	}
	virtuals := make([]item.Item, 0, len(persons))
	for id, name := range persons {
		virtuals = append(virtuals, item.Item{ID: id, Kind: item.KindPerson, Name: stringPtr(name)})
	}
	return virtuals
}

// openPostgresStorePool opens count stores on one fresh PostgreSQL schema.
func openPostgresStorePool(t *testing.T, count int) []store.Stores {
	t.Helper()
	dsn := os.Getenv("DEADFIN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DEADFIN_TEST_POSTGRES_DSN is not set")
	}
	schema := reconciliationPostgresSchema(t)
	pool := make([]store.Stores, 0, count)
	t.Cleanup(func() {
		for _, opened := range pool {
			if err := opened.Close(); err != nil {
				t.Errorf("close pooled PostgreSQL store: %v", err)
			}
		}
		admin, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Errorf("open PostgreSQL schema administrator: %v", err)
			return
		}
		defer func() { _ = admin.Close() }()
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Errorf("drop PostgreSQL schema %q: %v", schema, err)
		}
	})
	for range count {
		opened, err := postgres.Open(t.Context(), postgres.Config{DSN: dsn, Schema: schema})
		if err != nil {
			t.Fatalf("open pooled PostgreSQL store: %v", err)
		}
		pool = append(pool, opened)
	}
	return pool
}
