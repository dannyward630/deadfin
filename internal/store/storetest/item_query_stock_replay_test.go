// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetest "github.com/deadfin-org/deadfin/internal/store/storetest"
)

type itemQueryStockFixture struct {
	Format        string                      `json:"format"`
	ReferenceTime string                      `json:"reference_time"`
	Items         []itemQueryStockFixtureItem `json:"items"`
	QueryBindings []json.RawMessage           `json:"query_bindings"`
}

type itemQueryStockFixtureItem struct {
	Identity  string                        `json:"identity"`
	Scalars   map[string]json.RawMessage    `json:"scalars"`
	Relations itemQueryStockFixtureRelation `json:"relations"`
	Media     itemQueryStockFixtureMedia    `json:"media"`
	UserData  itemQueryStockFixtureUserData `json:"user_data"`
}

type itemQueryStockFixtureRelation struct {
	Parent      *itemQueryStockReference `json:"parent"`
	TopParent   *itemQueryStockReference `json:"top_parent"`
	Series      *itemQueryStockReference `json:"series"`
	Season      *itemQueryStockReference `json:"season"`
	Album       *itemQueryStockReference `json:"album"`
	People      []itemQueryStockPerson   `json:"people"`
	ProviderIDs map[string]string        `json:"provider_ids"`
	Values      itemQueryStockValues     `json:"values"`
}

type (
	itemQueryStockReference struct {
		Item string `json:"$item"`
	}
	itemQueryStockPerson struct{ Name, Type, Role string }
	itemQueryStockValues struct {
		Genres, Studios, Tags, Artists []string
		AlbumArtists                   []struct {
			Name string `json:"Name"`
		} `json:"album_artists"`
		ArtistItems []struct {
			Name string `json:"Name"`
		} `json:"artist_items"`
	}
	itemQueryStockFixtureMedia struct {
		Sources    []json.RawMessage            `json:"sources"`
		Streams    []map[string]json.RawMessage `json:"streams"`
		Chapters   []json.RawMessage            `json:"chapters"`
		ImageTypes []string                     `json:"image_types"`
	}
	itemQueryStockFixtureUserData struct {
		User   string                     `json:"user"`
		Key    *itemQueryStockReference   `json:"key"`
		Values map[string]json.RawMessage `json:"values"`
	}
)

func TestItemStoreQueryStockCorpusSQLite(t *testing.T) {
	runItemQueryStockCorpus(t, func(t *testing.T) store.Stores {
		stores, err := sqlite.Open(t.Context(), ":memory:")
		if err != nil {
			t.Fatalf("open SQLite: %v", err)
		}
		t.Cleanup(func() {
			if err := stores.Close(); err != nil {
				t.Errorf("close SQLite: %v", err)
			}
		})
		return stores
	})
}

func TestItemQueryStockLimitZeroOmitsLimit(t *testing.T) {
	query := stockItemQuery(t, map[string]any{"Limit": float64(0)}, nil, "")
	if query.Limit != nil {
		t.Fatalf("Limit = %d, want nil for public limit=0", *query.Limit)
	}
}

func TestItemStoreQueryStockCorpusPostgres(t *testing.T) {
	runItemQueryStockCorpus(t, storetest.OpenPostgres)
}

func runItemQueryStockCorpus(t *testing.T, open func(*testing.T) store.Stores) {
	t.Helper()
	stores := open(t)
	corpus, root := loadItemQueryStockCorpus(t)
	var fixture itemQueryStockFixture
	decodeItemQueryStockJSON(t, root+"/itemquery-fixtures.json", &fixture)
	if fixture.Format != "goldenrec-itemquery-fixtures/v1" {
		t.Fatalf("fixture format = %q", fixture.Format)
	}
	if fixture.ReferenceTime == "" {
		t.Fatal("fixture reference_time is required")
	}
	ids := seedItemQueryStockFixture(t, store.NewItemStore(stores), fixture)
	items := store.NewItemStore(stores)
	selected := os.Getenv("DEADFIN_STOCK_CASE")
	for _, tc := range corpus.Cases {
		if selected != "" && tc.Name != selected {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			query := stockItemQuery(t, tc.Request.Params, ids, fixture.ReferenceTime)
			if selected != "" {
				translated, err := store.Translate(query, sqlite.Dialect{})
				if err != nil {
					t.Fatalf("Translate: %v", err)
				}
				t.Logf("params=%v\nSQL=%s\nargs=%#v\ncount SQL=%s\ncount args=%#v", tc.Request.Params, translated.SQL, translated.Args, translated.CountSQL, translated.CountArgs)
			}
			got, err := items.Query(t.Context(), query)
			if err != nil {
				t.Fatalf("ItemStore.Query: %v", err)
			}
			want := stockIdentityIDs(t, tc.Expect.Items, ids)
			if actual := stockResultIDs(got.Items); !reflect.DeepEqual(actual, want) {
				t.Fatalf("item identities = %v, want %v", stockIdentityNames(actual, ids), tc.Expect.Items)
			}
			if queryStartIndex(query) != tc.Expect.StartIndex {
				t.Fatalf("request StartIndex = %d, stock start_index = %d", queryStartIndex(query), tc.Expect.StartIndex)
			}
			if got.TotalRecordCount != tc.Expect.TotalRecordCount {
				t.Fatalf("total record count = %d, want %d", got.TotalRecordCount, tc.Expect.TotalRecordCount)
			}
		})
	}
}

func seedItemQueryStockFixture(t *testing.T, items *store.ItemStore, fixture itemQueryStockFixture) map[string]dto.UUID {
	t.Helper()
	ids := make(map[string]dto.UUID, len(fixture.Items))
	byIdentity := make(map[string]itemQueryStockFixtureItem, len(fixture.Items))
	for _, record := range fixture.Items {
		if !canonicalItemRef.MatchString(record.Identity) || record.Scalars == nil {
			t.Fatalf("invalid fixture item %q", record.Identity)
		}
		if _, exists := ids[record.Identity]; exists {
			t.Fatalf("duplicate fixture identity %q", record.Identity)
		}
		ids[record.Identity] = stockFixtureUUID(t, record.Identity)
		byIdentity[record.Identity] = record
	}
	boxSetChildren := make(map[string][]item.LinkedChildRef)
	for _, record := range fixture.Items {
		parent := record.Relations.Parent
		if parent == nil {
			continue
		}
		parentRecord, exists := byIdentity[parent.Item]
		if !exists {
			t.Fatalf("fixture %s references unknown parent %q", record.Identity, parent.Item)
		}
		if stockRawString(t, parentRecord.Scalars, "Type") == "BoxSet" {
			boxSetChildren[parent.Item] = append(boxSetChildren[parent.Item], item.LinkedChildRef{ItemID: ids[record.Identity], Type: item.LinkedChildTypeManual})
		}
	}
	identities := stockFixtureSeedOrder(t, byIdentity)
	for _, identity := range identities {
		record := byIdentity[identity]
		it := stockFixtureItem(t, record, ids)
		it.LinkedChildren = boxSetChildren[identity]
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upsert %s: %v", identity, err)
		}
	}
	for _, identity := range identities {
		record := byIdentity[identity]
		id := ids[identity]
		if len(record.Media.Streams) != 0 {
			if err := items.ReplaceStreams(t.Context(), id, stockFixtureStreams(t, record.Media.Streams)); err != nil {
				t.Fatalf("replace streams for %s: %v", identity, err)
			}
		}
		if record.UserData.User != "" {
			key := stockReferenceID(t, record.UserData.Key, ids)
			if key == nil {
				t.Fatalf("user data key is required for %s", identity)
			}
			if err := items.UpsertUserData(t.Context(), store.UserData{UserID: stockFixtureUUID(t, "user:"+record.UserData.User), CustomKey: key.String(), ItemID: id, Played: stockRawBool(record.UserData.Values, "Played"), IsFavorite: stockRawBool(record.UserData.Values, "IsFavorite"), PlayCount: stockRawIntValue(record.UserData.Values, "PlayCount"), PositionTicks: stockRawInt64Value(record.UserData.Values, "PlaybackPositionTicks")}); err != nil {
				t.Fatalf("upsert user data for %s: %v", identity, err)
			}
		}
	}
	return ids
}

func stockFixtureItem(t *testing.T, record itemQueryStockFixtureItem, ids map[string]dto.UUID) item.Item {
	t.Helper()
	kind, err := item.ParseKind(stockRawString(t, record.Scalars, "Type"))
	if err != nil {
		t.Fatalf("fixture %s Type: %v", record.Identity, err)
	}
	it := item.Item{ID: ids[record.Identity], Kind: kind, Name: stockRawStringPtr(record.Scalars, "Name"), SortName: stockRawStringPtr(record.Scalars, "SortName"), Path: stockRawStringPtr(record.Scalars, "Path"), Container: stockRawStringPtr(record.Scalars, "Container"), OfficialRating: stockRawStringPtr(record.Scalars, "OfficialRating"), Overview: stockRawStringPtr(record.Scalars, "Overview"), ProductionYear: stockRawIntPtr(record.Scalars, "ProductionYear"), IndexNumber: stockRawIntPtr(record.Scalars, "IndexNumber"), ParentIndexNumber: stockRawIntPtr(record.Scalars, "ParentIndexNumber"), Width: stockRawIntPtr(record.Scalars, "Width"), Height: stockRawIntPtr(record.Scalars, "Height"), RunTimeTicks: stockRawInt64Ptr(record.Scalars, "RunTimeTicks"), CommunityRating: stockRawFloat32Ptr(record.Scalars, "CommunityRating"), CriticRating: stockRawFloat32Ptr(record.Scalars, "CriticRating"), DateCreated: stockRawTimePtr(record.Scalars, "DateCreated"), PremiereDate: stockRawTimePtr(record.Scalars, "PremiereDate"), EndDate: stockRawTimePtr(record.Scalars, "EndDate"), DateLastSaved: stockRawTimePtr(record.Scalars, "DateLastSaved"), DateLastMediaAdded: stockRawTimePtr(record.Scalars, "DateLastMediaAdded"), IsFolder: stockRawBool(record.Scalars, "IsFolder"), IsPlaceHolder: stockRawBool(record.Scalars, "IsPlaceHolder"), IsMovie: stockRawBool(record.Scalars, "IsMovie"), IsSeries: stockRawBool(record.Scalars, "IsSeries"), IsVirtualItem: stockRawBool(record.Scalars, "IsVirtualItem")}
	it.TotalBitrate = stockRawIntPtr(record.Scalars, "TotalBitrate")
	it.ParentID = stockReferenceID(t, record.Relations.Parent, ids)
	it.TopParentID = stockReferenceID(t, record.Relations.TopParent, ids)
	it.SeriesID = stockReferenceID(t, record.Relations.Series, ids)
	it.SeasonID = stockReferenceID(t, record.Relations.Season, ids)
	if media := stockRawStringPtr(record.Scalars, "MediaType"); media != nil {
		value := stockMediaType(t, *media)
		it.MediaType = &value
	}
	if video := stockRawStringPtr(record.Scalars, "VideoType"); video != nil {
		value := stockVideoType(t, *video)
		it.VideoType = &value
	}
	for key, value := range record.Relations.ProviderIDs {
		it.ProviderIDs.Set(key, value)
	}
	it.Values = item.ItemValues{Genres: record.Relations.Values.Genres, Studios: record.Relations.Values.Studios, Tags: record.Relations.Values.Tags, Artists: record.Relations.Values.Artists}
	for _, person := range record.Relations.People {
		it.People = append(it.People, item.PersonCredit{PersonID: stockFixtureUUID(t, "person:"+person.Name), Name: person.Name, Role: stockStringPtr(person.Role), Type: stockPersonKind(t, person.Type)})
	}
	return it
}

func stockItemQuery(t *testing.T, params map[string]any, ids map[string]dto.UUID, referenceTime string) store.ItemQuery {
	t.Helper()
	q := store.ItemQuery{EnableTotalRecordCount: true}
	userID := stockFixtureUUID(t, "user:GoldenrecAdmin")
	requiresUser := false
	for key, raw := range params {
		switch key {
		case "Filters":
			stockApplyFilters(t, &q, raw, &requiresUser)
		case "Recursive":
			q.Recursive = stockAnyBool(t, key, raw)
		case "StartIndex":
			q.StartIndex = stockAnyIntPtr(t, key, raw)
		case "Limit":
			if value := stockAnyInt(t, key, raw); value != 0 {
				q.Limit = &value
			}
		case "EnableTotalRecordCount":
			q.EnableTotalRecordCount = stockAnyBool(t, key, raw)
		case "Fields": // Query materialization is outside the ItemStore.Query predicate contract.
		case "Person":
			q.Person = stockAnyString(t, key, raw)
		case "PersonIds":
			q.PersonIDs = stockAnyReferences(t, key, raw, ids)
		case "PersonTypes":
			q.PersonTypes = stockAnyStrings(t, key, raw)
		case "IsPlayed":
			q.IsPlayed = stockAnyBoolPtr(t, key, raw)
			requiresUser = true
		case "IsFavorite":
			q.IsFavorite = stockAnyBoolPtr(t, key, raw)
			requiresUser = true
		case "IsUnaired":
			q.IsUnaired = stockAnyBoolPtr(t, key, raw)
			q.ReferenceTime = stockAnyTimePtr(t, "fixture.reference_time", referenceTime)
		case "CollapseBoxSetItems":
			q.CollapseBoxSetItems = stockAnyBoolPtr(t, key, raw)
		case "HasImdbId":
			q.HasIMDbID = stockAnyBoolPtr(t, key, raw)
		case "HasTmdbId":
			q.HasTMDbID = stockAnyBoolPtr(t, key, raw)
		case "MinDateLastSavedForUser":
			q.MinDateLastSavedForUser = stockAnyTimePtr(t, key, raw)
			requiresUser = true
		case "IncludeItemTypes":
			q.IncludeItemTypes = stockKinds(t, key, raw)
		case "ExcludeItemTypes":
			q.ExcludeItemTypes = stockKinds(t, key, raw)
		case "MediaTypes":
			q.MediaTypes = stockMediaTypes(t, key, raw)
		case "ParentId":
			q.ParentID = stockAnyReference(t, key, raw, ids)
		case "AdjacentTo":
			q.AdjacentTo = stockAnyReference(t, key, raw, ids)
		case "Ids":
			q.ItemIDs = stockAnyReferences(t, key, raw, ids)
		case "ExcludeItemIds":
			q.ExcludeItemIDs = stockAnyReferences(t, key, raw, ids)
		case "SortBy":
			q.SortBy = stockSorts(t, raw)
		case "SortOrder":
			q.SortOrder = stockSortOrders(t, raw)
		case "IsPlaceHolder":
			q.IsPlaceHolder = stockAnyBoolPtr(t, key, raw)
		case "IsMissing":
			q.IsMissing = stockAnyBoolPtr(t, key, raw)
		case "Genres":
			q.Genres = stockAnyStrings(t, key, raw)
		case "Tags":
			q.Tags = stockAnyStrings(t, key, raw)
		case "NameStartsWith":
			q.NameStartsWith = stockAnyString(t, key, raw)
		case "NameStartsWithOrGreater":
			q.NameStartsWithOrGreater = stockAnyString(t, key, raw)
		case "NameLessThan":
			q.NameLessThan = stockAnyString(t, key, raw)
		case "SearchTerm":
			q.SearchTerm = stockAnyString(t, key, raw)
		case "Years":
			q.Years = stockAnyInts(t, key, raw)
		case "VideoTypes":
			q.VideoTypes = stockVideoTypes(t, key, raw)
		case "HasSubtitles":
			q.HasSubtitles = stockAnyBoolPtr(t, key, raw)
		case "SubtitleLanguages":
			q.SubtitleLanguages = stockAnyStrings(t, key, raw)
		case "AudioLanguages":
			q.AudioLanguages = stockAnyStrings(t, key, raw)
		case "MinPremiereDate":
			q.MinPremiereDate = stockAnyTimePtr(t, key, raw)
		case "MaxPremiereDate":
			q.MaxPremiereDate = stockAnyTimePtr(t, key, raw)
		case "MinCommunityRating":
			q.MinCommunityRating = stockAnyFloat32Ptr(t, key, raw)
		case "MaxCommunityRating":
			q.MaxCommunityRating = stockAnyFloat32Ptr(t, key, raw)
		default:
			t.Fatalf("case uses unmapped public ItemQuery parameter %q", key)
		}
	}
	if q.CollapseBoxSetItems != nil && *q.CollapseBoxSetItems && (len(q.ItemIDs) != 0 || q.SearchTerm != "") {
		// ItemsController disables collapse when explicit IDs or search results select the items.
		collapseBoxSetItems := false
		q.CollapseBoxSetItems = &collapseBoxSetItems
	}
	if requiresUser || stockQueryNeedsUser(q) {
		q.UserID = &userID
	}
	return q
}

func stockFixtureUUID(t *testing.T, value string) dto.UUID {
	t.Helper()
	sum := sha256.Sum256([]byte(value))
	id, err := dto.ParseUUID(hex.EncodeToString(sum[:16]))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func stockReferenceID(t *testing.T, ref *itemQueryStockReference, ids map[string]dto.UUID) *dto.UUID {
	if ref == nil {
		return nil
	}
	id, ok := ids[ref.Item]
	if !ok {
		t.Fatalf("unknown fixture relation %q", ref.Item)
	}
	return &id
}

func stockAnyReference(t *testing.T, key string, raw any, ids map[string]dto.UUID) *dto.UUID {
	value, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s must be an item reference", key)
	}
	identity, ok := value["$item"].(string)
	if !ok {
		t.Fatalf("%s invalid item reference", key)
	}
	id, ok := ids[identity]
	if !ok {
		t.Fatalf("%s references unknown item %q", key, identity)
	}
	return &id
}

func stockAnyReferences(t *testing.T, key string, raw any, ids map[string]dto.UUID) []dto.UUID {
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s must be array", key)
	}
	result := make([]dto.UUID, len(values))
	for i := range values {
		result[i] = *stockAnyReference(t, key, values[i], ids)
	}
	return result
}

func stockIdentityIDs(t *testing.T, identities []string, ids map[string]dto.UUID) []dto.UUID {
	result := make([]dto.UUID, len(identities))
	for i, identity := range identities {
		id, ok := ids[identity]
		if !ok {
			t.Fatalf("golden expects unseeded identity %q", identity)
		}
		result[i] = id
	}
	return result
}

func stockResultIDs(items []item.Item) []dto.UUID {
	result := make([]dto.UUID, len(items))
	for i := range items {
		result[i] = items[i].ID
	}
	return result
}

func stockIdentityNames(actual []dto.UUID, ids map[string]dto.UUID) []string {
	reverse := map[dto.UUID]string{}
	for identity, id := range ids {
		reverse[id] = identity
	}
	names := make([]string, len(actual))
	for i, id := range actual {
		names[i] = reverse[id]
	}
	return names
}

func queryStartIndex(q store.ItemQuery) int {
	if q.StartIndex == nil {
		return 0
	}
	return *q.StartIndex
}
func stockStringPtr(value string) *string { return &value }
