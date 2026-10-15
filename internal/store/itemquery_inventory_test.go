// SPDX-License-Identifier: GPL-2.0-only

package store_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/attest"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

const (
	pinnedInternalItemsQuerySource = "upstream/jellyfin/MediaBrowser.Controller/Entities/InternalItemsQuery.cs"
	pinnedItemSortBySource         = "upstream/jellyfin/Jellyfin.Data/Enums/ItemSortBy.cs"
)

var (
	pinnedAutoProperty = regexp.MustCompile(`^\s*public\s+.+\s+([A-Za-z][A-Za-z0-9_]*)\s+\{\s*get;\s*set;\s*\}`)
	pinnedEnumValue    = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9_]*)\s+=\s*([0-9]+),?$`)
)

type itemQueryInventoryFixture struct {
	QuerySource             string                                    `json:"query_source"`
	QueryPropertyCount      int                                       `json:"query_property_count"`
	QueryExcludedProperties []string                                  `json:"query_excluded_properties"`
	QueryProperties         []string                                  `json:"query_properties"`
	DefaultClassification   itemQueryClassificationFixture            `json:"default_classification"`
	Classifications         map[string]itemQueryClassificationFixture `json:"classifications"`
	DTOOptionsSource        string                                    `json:"dto_options_source"`
	DTOOptionsProperties    []string                                  `json:"dto_options_properties"`
	SortSource              string                                    `json:"sort_source"`
	Sorts                   []itemSortByInventoryFixture              `json:"sorts"`
}

type itemQueryClassificationFixture struct {
	Owner       string `json:"owner"`
	Implemented bool   `json:"implemented"`
}

type itemSortByInventoryFixture struct {
	Name    string `json:"name"`
	Ordinal int    `json:"ordinal"`
}

// TestItemQueryPropertiesMatchPinnedInternalItemsQuery defends the public
// query contract: every input from the pinned upstream model has exactly one
// declared ownership classification and every exported Go request field is
func TestItemQueryPropertiesMatchPinnedInternalItemsQuery(t *testing.T) {
	jellyport := attest.GateForTest(t)
	fixture := loadItemQueryInventoryFixture(t)
	if fixture.QuerySource != pinnedInternalItemsQuerySource {
		t.Fatalf("fixture query source = %q, want %q", fixture.QuerySource, pinnedInternalItemsQuerySource)
	}
	if got, want := fixture.QueryExcludedProperties, []string{"HasFilters", "Parent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pinned InternalItemsQuery exclusions = %v, want %v", got, want)
	}

	source := parsePinnedAutoProperties(t, jellyport, fixture.QuerySource)
	if got, want := len(source), fixture.QueryPropertyCount; got != want {
		t.Fatalf("pinned InternalItemsQuery input property count = %d, want %d", got, want)
	}
	if got, want := sourceNames(source), fixture.QueryProperties; !reflect.DeepEqual(got, want) {
		t.Fatalf("checked-in InternalItemsQuery inventory differs from pinned source\n got: %v\nwant: %v", got, want)
	}

	got := store.ItemQueryProperties()
	gotBySource := make(map[string]store.ItemQueryProperty, len(got))
	gotByGoName := make(map[string]store.ItemQueryProperty, len(got))
	for _, property := range got {
		if property.Classification.Owner == "" || property.SourceRef == "" || (property.SourceName == "" && property.GoName == "") {
			t.Fatalf("ItemQueryProperties contains incomplete record %#v", property)
		}
		if property.SourceName != "" {
			if _, exists := gotBySource[property.SourceName]; exists {
				t.Fatalf("ItemQueryProperties repeats source property %q", property.SourceName)
			}
			gotBySource[property.SourceName] = property
		}
		if property.GoName != "" {
			if _, exists := gotByGoName[property.GoName]; exists {
				t.Fatalf("ItemQueryProperties maps multiple source properties to Go field %q", property.GoName)
			}
			gotByGoName[property.GoName] = property
		}
	}

	for _, sourceProperty := range source {
		property, ok := gotBySource[sourceProperty.name]
		if !ok {
			t.Errorf("ItemQueryProperties is missing pinned source property %q (%s)", sourceProperty.name, sourceProperty.ref)
			continue
		}
		if got, want := property.SourceRef, sourceProperty.ref; got != want {
			t.Errorf("%s source ref = %q, want %q", sourceProperty.name, got, want)
		}
		wantClassification, ok := fixture.Classifications[sourceProperty.name]
		if !ok {
			wantClassification = fixture.DefaultClassification
		}
		if wantClassification.Owner == "" {
			t.Errorf("testdata has no ownership classification for pinned source property %q", sourceProperty.name)
			continue
		}
		if got, want := string(property.Classification.Owner), wantClassification.Owner; got != want {
			t.Errorf("%s owner = %q, want %q", sourceProperty.name, got, want)
		}
		if got, want := property.Classification.Implemented, wantClassification.Implemented; got != want {
			t.Errorf("%s implemented = %t, want %t", sourceProperty.name, got, want)
		}
		if wantClassification.Owner == "dropped" && property.GoName != "" {
			t.Errorf("intentionally dropped %s maps to ItemQuery field %q", sourceProperty.name, property.GoName)
		}
		if wantClassification.Owner != "dropped" && property.GoName == "" {
			t.Errorf("non-dropped %s has no ItemQuery field mapping", sourceProperty.name)
		}
	}
	for sourceName := range gotBySource {
		if !containsString(fixture.QueryProperties, sourceName) {
			t.Errorf("ItemQueryProperties contains unpinned source property %q", sourceName)
		}
	}

	queryType := reflect.TypeFor[store.ItemQuery]()
	for index := range queryType.NumField() {
		field := queryType.Field(index)
		if field.PkgPath != "" {
			continue
		}
		property, ok := gotByGoName[field.Name]
		if !ok {
			t.Errorf("exported ItemQuery field %q (%s) has no ownership classification", field.Name, field.Type)
			continue
		}
		if property.Classification.Owner == store.ItemQueryPropertyOwnerDropped {
			t.Errorf("exported ItemQuery field %q is marked intentionally dropped", field.Name)
		}
	}
	for goName, property := range gotByGoName {
		if _, ok := queryType.FieldByName(goName); !ok {
			t.Errorf("%s maps to absent ItemQuery field %q", property.SourceName, goName)
		}
	}

	assertLegacyItemQueryFieldTypes(t, queryType)
	for goName, want := range map[string]itemQueryClassificationFixture{
		"Studios": {Owner: "sql", Implemented: true}, "MaxCommunityRating": {Owner: "sql", Implemented: true}, "MinRunTimeTicks": {Owner: "sql", Implemented: true}, "MaxRunTimeTicks": {Owner: "sql", Implemented: true}, "ReferenceTime": {Owner: "sql", Implemented: true}, "UserID": {Owner: "sql", Implemented: true}, "DisableLiveTVChannelUserDataName": {Owner: "sql", Implemented: true}, "SortBy": {Owner: "sql", Implemented: true}, "SortOrder": {Owner: "sql", Implemented: true}, "EpisodesOfSeasonID": {Owner: "sql", Implemented: true}, "ExcludeSpecialsFromSeasons": {Owner: "sql", Implemented: true},
	} {
		property, ok := gotByGoName[goName]
		if !ok || property.SourceName != "" || string(property.Classification.Owner) != want.Owner || property.Classification.Implemented != want.Implemented || !strings.HasPrefix(property.SourceRef, "deadfin/internal/store/itemquery.go:") {
			t.Errorf("legacy extension %s classification = %#v (present %t), want SQL implemented with itemquery.go evidence", goName, property, ok)
		}
	}
}

// TestItemQuerySourceTypesContract defends the public query input required by
// scheduled media work: callers can select persisted Library, Channel, or
// LiveTV rows through a SQL-owned, implemented []item.SourceType filter.
func TestItemQuerySourceTypesContract(t *testing.T) {
	var sourceTypes store.ItemQueryProperty
	found := false
	for _, property := range store.ItemQueryProperties() {
		if property.SourceName == "SourceTypes" {
			sourceTypes, found = property, true
			break
		}
	}
	if !found {
		t.Fatal("ItemQueryProperties is missing SourceTypes")
	}
	if got, want := sourceTypes.GoName, "SourceTypes"; got != want {
		t.Errorf("SourceTypes Go field = %q, want %q", got, want)
	}
	if got, want := sourceTypes.Classification.Owner, store.ItemQueryPropertyOwnerSQL; got != want {
		t.Errorf("SourceTypes owner = %q, want %q", got, want)
	}
	if !sourceTypes.Classification.Implemented {
		t.Error("SourceTypes must be classified as implemented SQL support")
	}
	field, ok := reflect.TypeFor[store.ItemQuery]().FieldByName("SourceTypes")
	if !ok {
		t.Error("ItemQuery is missing SourceTypes")
		return
	}
	if got, want := field.Type, reflect.TypeFor[[]item.SourceType](); got != want {
		t.Errorf("ItemQuery.SourceTypes type = %s, want %s", got, want)
	}
}

// TestItemQueryControlOwnership forbids query orchestration and response
// materialization controls from being mislabeled as SQL translation gaps.
func TestItemQueryControlOwnership(t *testing.T) {
	got := make(map[string]store.ItemQueryPropertyClassification)
	for _, property := range store.ItemQueryProperties() {
		if property.SourceName != "" {
			got[property.SourceName] = property.Classification
		}
	}
	if !containsString(loadItemQueryInventoryFixture(t).QueryExcludedProperties, "Parent") {
		t.Error("pinned write-only Parent orchestration accessor must remain excluded from ItemQuery input inventory")
	}
	for sourceName, wantOwner := range map[string]store.ItemQueryPropertyOwner{
		"Recursive":                store.ItemQueryPropertyOwnerService,
		"ForceDirect":              store.ItemQueryPropertyOwnerService,
		"DisplayAlbumFolders":      store.ItemQueryPropertyOwnerService,
		"IncludeItemsByName":       store.ItemQueryPropertyOwnerSQL,
		"EnableGroupByMetadataKey": store.ItemQueryPropertyOwnerService,
		"DtoOptions":               store.ItemQueryPropertyOwnerMaterialization,
		"IncludeExtras":            store.ItemQueryPropertyOwnerMaterialization,
		"SkipDeserialization":      store.ItemQueryPropertyOwnerMaterialization,
	} {
		actual, ok := got[sourceName]
		if !ok || actual.Owner != wantOwner || actual.Implemented {
			t.Errorf("%s classification = %#v (present %t), want owner %q and implemented false", sourceName, actual, ok, wantOwner)
		}
	}
}

// TestItemQueryDTOOptionsMatchesPinnedDtoOptions defends the materialization
// option shape that services consume after ItemQuery has completed SQL
// selection. These fields are not translation controls and must not disappear
func TestItemQueryDTOOptionsMatchesPinnedDtoOptions(t *testing.T) {
	jellyport := attest.GateForTest(t)
	fixture := loadItemQueryInventoryFixture(t)
	source := parsePinnedAutoProperties(t, jellyport, fixture.DTOOptionsSource)
	if got, want := sourceNames(source), fixture.DTOOptionsProperties; !reflect.DeepEqual(got, want) {
		t.Fatalf("checked-in DtoOptions inventory differs from pinned source\n got: %v\nwant: %v", got, want)
	}

	want := []struct {
		name   string
		typeOf reflect.Type
	}{
		{name: "Fields", typeOf: reflect.TypeFor[[]string]()},
		{name: "ImageTypes", typeOf: reflect.TypeFor[[]item.ImageType]()},
		{name: "ImageTypeLimit", typeOf: reflect.TypeFor[int]()},
		{name: "EnableImages", typeOf: reflect.TypeFor[bool]()},
		{name: "AddProgramRecordingInfo", typeOf: reflect.TypeFor[bool]()},
		{name: "EnableUserData", typeOf: reflect.TypeFor[bool]()},
		{name: "AddCurrentProgram", typeOf: reflect.TypeFor[bool]()},
	}
	optionsType := reflect.TypeFor[store.ItemQueryDTOOptions]()
	if got, wantCount := optionsType.NumField(), len(want); got != wantCount {
		t.Fatalf("ItemQueryDTOOptions field count = %d, want %d", got, wantCount)
	}
	for index, expectation := range want {
		field := optionsType.Field(index)
		if field.PkgPath != "" || field.Name != expectation.name || field.Type != expectation.typeOf {
			t.Errorf("ItemQueryDTOOptions field %d = %s %s, want %s %s", index, field.Name, field.Type, expectation.name, expectation.typeOf)
		}
	}
}

func TestItemSortByMatchesPinnedEnum(t *testing.T) {
	jellyport := attest.GateForTest(t)
	fixture := loadItemQueryInventoryFixture(t)
	if fixture.SortSource != pinnedItemSortBySource {
		t.Fatalf("fixture sort source = %q, want %q", fixture.SortSource, pinnedItemSortBySource)
	}
	if got, want := parsePinnedItemSortBy(t, jellyport, fixture.SortSource), fixture.Sorts; !reflect.DeepEqual(got, want) {
		t.Fatalf("checked-in ItemSortBy inventory differs from pinned source\n got: %#v\nwant: %#v", got, want)
	}

	want := []store.ItemSortBy{
		store.ItemSortByDefault,
		store.ItemSortByAiredEpisodeOrder,
		store.ItemSortByAlbum,
		store.ItemSortByAlbumArtist,
		store.ItemSortByArtist,
		store.ItemSortByDateCreated,
		store.ItemSortByOfficialRating,
		store.ItemSortByDatePlayed,
		store.ItemSortByPremiereDate,
		store.ItemSortByStartDate,
		store.ItemSortBySortName,
		store.ItemSortByName,
		store.ItemSortByRandom,
		store.ItemSortByRuntime,
		store.ItemSortByCommunityRating,
		store.ItemSortByProductionYear,
		store.ItemSortByPlayCount,
		store.ItemSortByCriticRating,
		store.ItemSortByIsFolder,
		store.ItemSortByIsUnplayed,
		store.ItemSortByIsPlayed,
		store.ItemSortBySeriesSortName,
		store.ItemSortByVideoBitRate,
		store.ItemSortByAirTime,
		store.ItemSortByStudio,
		store.ItemSortByIsFavoriteOrLiked,
		store.ItemSortByDateLastContentAdded,
		store.ItemSortBySeriesDatePlayed,
		store.ItemSortByParentIndexNumber,
		store.ItemSortByIndexNumber,
	}
	if got := store.ItemSortByValues(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ItemSortByValues = %v, want pinned order %v", got, want)
	}
	for index, sortBy := range want {
		if got, wantName := string(sortBy), fixture.Sorts[index].Name; got != wantName {
			t.Errorf("ItemSortByValues()[%d] string = %q, want %q", index, got, wantName)
		}
		encoded, err := json.Marshal(sortBy)
		if err != nil {
			t.Fatalf("marshal ItemSortByValues()[%d]: %v", index, err)
		}
		if got, wantJSON := string(encoded), `"`+fixture.Sorts[index].Name+`"`; got != wantJSON {
			t.Errorf("ItemSortByValues()[%d] JSON = %s, want %s", index, got, wantJSON)
		}
	}

	var zero store.ItemSortBy
	if zero == store.ItemSortByDefault {
		t.Fatal("zero ItemSortBy must remain an unknown value; the upstream Default identity is explicit")
	}
	if _, err := store.Translate(store.ItemQuery{SortBy: []store.ItemSortBy{zero}}, sqlite.Dialect{}); err == nil || !strings.Contains(err.Error(), "unsupported sort field") {
		t.Fatalf("Translate zero ItemSortBy error = %v, want unsupported sort field", err)
	}
	if _, err := store.Translate(store.ItemQuery{SortBy: []store.ItemSortBy{"not-a-pinned-sort"}}, sqlite.Dialect{}); err == nil || !strings.Contains(err.Error(), "unsupported sort field") {
		t.Fatalf("Translate unknown ItemSortBy error = %v, want unsupported sort field", err)
	}
}

type pinnedProperty struct {
	name string
	ref  string
}

func parsePinnedAutoProperties(t *testing.T, jellyport, sourcePath string) []pinnedProperty {
	t.Helper()
	contents := readPinnedSource(t, jellyport, sourcePath)
	var properties []pinnedProperty
	for line, text := range strings.Split(string(contents), "\n") {
		match := pinnedAutoProperty.FindStringSubmatch(text)
		if match == nil {
			continue
		}
		properties = append(properties, pinnedProperty{name: match[1], ref: sourcePath + ":" + strconvItoa(line+1)})
	}
	return properties
}

func parsePinnedItemSortBy(t *testing.T, jellyport, sourcePath string) []itemSortByInventoryFixture {
	t.Helper()
	contents := readPinnedSource(t, jellyport, sourcePath)
	var values []itemSortByInventoryFixture
	for _, text := range strings.Split(string(contents), "\n") {
		match := pinnedEnumValue.FindStringSubmatch(text)
		if match == nil {
			continue
		}
		ordinal := 0
		for _, digit := range match[2] {
			ordinal = ordinal*10 + int(digit-'0')
		}
		values = append(values, itemSortByInventoryFixture{Name: match[1], Ordinal: ordinal})
	}
	return values
}

func readPinnedSource(t *testing.T, jellyport, sourcePath string) []byte {
	t.Helper()
	contents, err := os.ReadFile(attest.AdjacentPath(jellyport, sourcePath))
	if err != nil {
		t.Fatalf("read pinned source %q: %v", sourcePath, err)
	}
	return contents
}

func loadItemQueryInventoryFixture(t *testing.T) itemQueryInventoryFixture {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", "itemquery_inventory.json"))
	if err != nil {
		t.Fatalf("read ItemQuery inventory: %v", err)
	}
	var fixture itemQueryInventoryFixture
	if err := json.Unmarshal(contents, &fixture); err != nil {
		t.Fatalf("decode ItemQuery inventory: %v", err)
	}
	return fixture
}

func sourceNames(properties []pinnedProperty) []string {
	names := make([]string, len(properties))
	for index, property := range properties {
		names[index] = property.name
	}
	return names
}

func assertLegacyItemQueryFieldTypes(t *testing.T, queryType reflect.Type) {
	t.Helper()
	want := map[string]reflect.Type{
		"ItemIDs": reflect.TypeFor[[]dto.UUID](), "ExcludeItemIDs": reflect.TypeFor[[]dto.UUID](), "ParentID": reflect.TypeFor[*dto.UUID](), "AncestorIDs": reflect.TypeFor[[]dto.UUID](), "TopParentIDs": reflect.TypeFor[[]dto.UUID](),
		"IncludeItemTypes": reflect.TypeFor[[]item.Kind](), "ExcludeItemTypes": reflect.TypeFor[[]item.Kind](), "MediaTypes": reflect.TypeFor[[]item.MediaType](), "Genres": reflect.TypeFor[[]string](), "Studios": reflect.TypeFor[[]string](), "Tags": reflect.TypeFor[[]string](), "ExcludeTags": reflect.TypeFor[[]string](),
		"HasAnyProviderID": reflect.TypeFor[map[string]string](), "HasAnyProviderIDs": reflect.TypeFor[map[string][]string](), "ExcludeProviderIDs": reflect.TypeFor[map[string]string](), "PersonIDs": reflect.TypeFor[[]dto.UUID](), "Person": reflect.TypeFor[string](), "PersonTypes": reflect.TypeFor[[]string](), "Years": reflect.TypeFor[[]int](),
		"MinPremiereDate": reflect.TypeFor[*time.Time](), "MaxPremiereDate": reflect.TypeFor[*time.Time](), "MinCommunityRating": reflect.TypeFor[*float32](), "MaxCommunityRating": reflect.TypeFor[*float32](), "MinRunTimeTicks": reflect.TypeFor[*int64](), "MaxRunTimeTicks": reflect.TypeFor[*int64](), "SearchTerm": reflect.TypeFor[string](),
		"IsFolder": reflect.TypeFor[*bool](), "IsVirtualItem": reflect.TypeFor[*bool](), "IsMissing": reflect.TypeFor[*bool](), "IsUnaired": reflect.TypeFor[*bool](), "ReferenceTime": reflect.TypeFor[*time.Time](), "UserID": reflect.TypeFor[*dto.UUID](), "IsPlayed": reflect.TypeFor[*bool](), "IsResumable": reflect.TypeFor[*bool](), "IsFavorite": reflect.TypeFor[*bool](), "IsFavoriteOrLiked": reflect.TypeFor[*bool](), "IsLiked": reflect.TypeFor[*bool](), "MinDateLastSavedForUser": reflect.TypeFor[*time.Time](),
		"DisableLiveTVChannelUserDataName": reflect.TypeFor[bool](), "SortBy": reflect.TypeFor[[]store.ItemSortBy](), "SortOrder": reflect.TypeFor[[]store.SortOrder](), "StartIndex": reflect.TypeFor[*int](), "Limit": reflect.TypeFor[*int](), "EnableTotalRecordCount": reflect.TypeFor[bool](),
	}
	for name, wantType := range want {
		field, ok := queryType.FieldByName(name)
		if !ok {
			t.Errorf("existing ItemQuery field %q is missing", name)
			continue
		}
		if field.Type != wantType {
			t.Errorf("existing ItemQuery.%s type = %s, want %s", name, field.Type, wantType)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func strconvItoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
