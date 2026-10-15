// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/store"
)

const itemQueryStockCorpusMinimumCases = 200

type itemQueryStockCorpus struct {
	Format string                     `json:"format"`
	Cases  []itemQueryStockCorpusCase `json:"cases"`
}

type itemQueryStockCorpusCase struct {
	Name        string                    `json:"name"`
	OperationID string                    `json:"operation_id"`
	Family      string                    `json:"family"`
	Request     itemQueryStockRequest     `json:"request"`
	Expect      itemQueryStockExpectation `json:"expect"`
}

type itemQueryStockRequest struct {
	Method string         `json:"method"`
	Path   string         `json:"path"`
	Params map[string]any `json:"params"`
}

// GetItems accepts exactly the documented query names at ItemsController.GetItems.
// This is intentionally wire-level rather than an ItemQuery field inventory: the
// public controller performs transformations before it constructs ItemQuery.
var itemQueryGetItemsParamNames = func() map[string]struct{} {
	const names = `userId maxOfficialRating hasThemeSong hasThemeVideo hasSubtitles hasSpecialFeature hasTrailer adjacentTo indexNumber parentIndexNumber hasParentalRating isHd is4K locationTypes excludeLocationTypes isMissing isUnaired minCommunityRating minCriticRating minPremiereDate minDateLastSaved minDateLastSavedForUser maxPremiereDate hasOverview hasImdbId hasTmdbId hasTvdbId isMovie isSeries isNews isKids isSports excludeItemIds startIndex limit recursive searchTerm sortOrder parentId fields excludeItemTypes includeItemTypes filters isFavorite mediaTypes imageTypes sortBy isPlayed genres officialRatings tags years enableUserData imageTypeLimit enableImageTypes person personIds personTypes studios artists excludeArtistIds artistIds albumArtistIds contributingArtistIds albums albumIds ids videoTypes minOfficialRating isLocked isPlaceHolder hasOfficialRating collapseBoxSetItems minWidth minHeight maxWidth maxHeight is3D seriesStatus nameStartsWithOrGreater nameStartsWith nameLessThan studioIds genreIds audioLanguages subtitleLanguages enableTotalRecordCount enableImages`
	result := make(map[string]struct{}, len(strings.Fields(names)))
	for _, name := range strings.Fields(names) {
		result[strings.ToLower(name)] = struct{}{}
	}
	return result
}()

type itemQueryStockExpectation struct {
	Status           int      `json:"status"`
	StartIndex       int      `json:"start_index"`
	TotalRecordCount int      `json:"total_record_count"`
	Items            []string `json:"items"`
}

type itemQueryStockProvenance struct {
	Format                  string `json:"format"`
	StockVersion            string `json:"stock_version"`
	StockImageDigest        string `json:"stock_image_digest"`
	UpstreamCommit          string `json:"upstream_commit"`
	UpstreamCommitAuthority string `json:"upstream_commit_authority"`
	RecorderSHA256          string `json:"recorder_sha256"`
	RequestManifestSHA256   string `json:"request_manifest_sha256"`
	SeedSHA256              string `json:"seed_sha256"`
	ProviderPolicySHA256    string `json:"provider_policy_sha256"`
	ExpectedInventorySHA256 string `json:"expected_inventory_sha256"`
	FixtureManifestSHA256   string `json:"fixture_manifest_sha256"`
	FixtureSHA256           string `json:"fixture_sha256"`
}

// TestItemQueryStockCorpusIntegrity is a deliberately strict ratchet. It rejects
// a corpus that cannot exercise the public ItemStore.Query replay below.
func TestItemQueryStockCorpusIntegrity(t *testing.T) {
	corpus, root := loadItemQueryStockCorpus(t)
	if corpus.Format != "goldenrec-itemquery-expectations/v1" {
		t.Fatalf("golden format = %q, want goldenrec-itemquery-expectations/v1", corpus.Format)
	}
	if got := len(corpus.Cases); got < itemQueryStockCorpusMinimumCases {
		t.Fatalf("stock case count = %d, want at least %d", got, itemQueryStockCorpusMinimumCases)
	}

	seenNames := make(map[string]struct{}, len(corpus.Cases))
	seenSorts := make(map[store.ItemSortBy]struct{}, len(store.ItemSortByValues()))
	seenFields := make(map[string]struct{})
	coverage := itemQueryStockCoverage{}
	for _, tc := range corpus.Cases {
		if !kebabCaseName.MatchString(tc.Name) {
			t.Fatalf("stock corpus case name %q must be non-empty kebab-case", tc.Name)
		}
		if _, exists := seenNames[tc.Name]; exists {
			t.Fatalf("duplicate stock case name %q", tc.Name)
		}
		seenNames[tc.Name] = struct{}{}
		if tc.OperationID != "GetItems" || tc.Family != "translator-direct" {
			t.Fatalf("case %q operation/family = %q/%q, want GetItems/translator-direct", tc.Name, tc.OperationID, tc.Family)
		}
		if tc.Request.Method != "GET" || tc.Request.Path != "/Items" {
			t.Fatalf("case %q request = %s %s, want GET /Items", tc.Name, tc.Request.Method, tc.Request.Path)
		}
		if len(tc.Request.Params) == 0 {
			t.Fatalf("case %q has no request parameters", tc.Name)
		}
		if tc.Expect.Status != 200 || tc.Expect.StartIndex < 0 || tc.Expect.TotalRecordCount < len(tc.Expect.Items) {
			t.Fatalf("case %q has invalid stock response status=%d start=%d total=%d items=%d", tc.Name, tc.Expect.Status, tc.Expect.StartIndex, tc.Expect.TotalRecordCount, len(tc.Expect.Items))
		}
		seenItems := make(map[string]struct{}, len(tc.Expect.Items))
		for _, identity := range tc.Expect.Items {
			if !canonicalItemRef.MatchString(identity) {
				t.Errorf("case %q returned non-canonical identity %q", tc.Name, identity)
			}
			if _, exists := seenItems[identity]; exists {
				t.Errorf("case %q returned duplicate identity %q", tc.Name, identity)
			}
			seenItems[identity] = struct{}{}
		}
		validateItemQueryGetItemsParams(t, tc.Name, tc.Request.Params)
		validateItemQueryReferenceParams(t, tc.Name, tc.Request.Params)
		for field := range tc.Request.Params {
			seenFields[field] = struct{}{}
		}
		observeItemQueryPublicFields(seenFields, tc.Request.Params)
		for _, value := range stockStringSlice(t, tc.Name, tc.Request.Params["SortBy"]) {
			sort := store.ItemSortBy(value)
			if !containsItemSortBy(store.ItemSortByValues(), sort) {
				t.Errorf("case %q has unknown ItemSortBy %q", tc.Name, value)
				continue
			}
			if sort == store.ItemSortByRandom {
				t.Errorf("case %q authors nondeterministic ItemSortBy %q; Random belongs only in translator invariants", tc.Name, sort)
				continue
			}
			seenSorts[sort] = struct{}{}
		}
		for _, value := range stockStringSlice(t, tc.Name, tc.Request.Params["SortOrder"]) {
			if value != string(store.SortOrderAscending) && value != string(store.SortOrderDescending) {
				t.Errorf("case %q has unknown SortOrder %q", tc.Name, value)
			}
		}
		validateItemQueryStockValue(t, tc.Name+" request", tc.Request.Params)
		validateItemQueryStockValue(t, tc.Name+" expectation", tc.Expect.Items)
		coverage.observe(tc.Request.Params)
	}
	for _, want := range itemQueryImplementedPublicFields() {
		if _, exists := seenFields[want]; !exists {
			t.Errorf("stock corpus has no case covering implemented ItemQuery field %q", want)
		}
	}
	for _, want := range store.ItemSortByValues() {
		if want == store.ItemSortByRandom {
			continue
		}
		if _, exists := seenSorts[want]; !exists {
			t.Errorf("stock corpus has no case covering deterministic ItemSortBy %q", want)
		}
	}
	coverage.require(t)
	if t.Failed() {
		return
	}
	validateItemQueryStockProvenance(t, root)
}

func loadItemQueryStockCorpus(t *testing.T) (itemQueryStockCorpus, string) {
	t.Helper()
	root := filepath.Join("testdata", "golden")
	var corpus itemQueryStockCorpus
	decodeItemQueryStockJSON(t, filepath.Join(root, "itemquery.json"), &corpus)
	return corpus, root
}

func decodeItemQueryStockJSON(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("decode %s: expected one JSON document, got %v", path, err)
	}
}

func itemQueryImplementedPublicFields() []string {
	fields := make([]string, 0)
	for _, property := range store.ItemQueryProperties() {
		if property.SourceName == "" || property.Classification.Owner != store.ItemQueryPropertyOwnerSQL || !property.Classification.Implemented {
			continue
		}
		if _, public := itemQueryGetItemsParamNames[strings.ToLower(property.SourceName)]; public {
			fields = append(fields, property.SourceName)
		}
	}
	fields = append(fields, "ItemIds", "IsFolder", "IsFavoriteOrLiked", "IsLiked", "IsResumable")
	return fields
}

func observeItemQueryPublicFields(seen map[string]struct{}, params map[string]any) {
	if _, ok := params["Ids"]; ok {
		seen["ItemIds"] = struct{}{}
	}
	if _, ok := params["Recursive"]; ok {
		seen["AncestorIds"] = struct{}{}
		seen["TopParentIds"] = struct{}{}
	}
	filters, _ := params["Filters"].([]any)
	for _, raw := range filters {
		switch raw {
		case "IsFolder", "IsNotFolder":
			seen["IsFolder"] = struct{}{}
		case "IsResumable":
			seen["IsResumable"] = struct{}{}
		case "Likes", "Dislikes":
			seen["IsLiked"] = struct{}{}
		case "IsFavoriteOrLikes":
			seen["IsFavoriteOrLiked"] = struct{}{}
		}
	}
}

func containsItemSortBy(values []store.ItemSortBy, want store.ItemSortBy) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var itemQuerySymbolicReferenceParams = map[string]struct{}{
	"ParentId": {}, "ItemIds": {}, "ExcludeItemIds": {}, "AdjacentTo": {}, "PersonIds": {},
	"StudioIds": {}, "GenreIds": {}, "ChannelIds": {}, "AncestorIds": {}, "LinkedChildAncestorIds": {},
	"TopParentIds": {}, "AlbumIds": {}, "ArtistIds": {}, "ExcludeArtistIds": {}, "AlbumArtistIds": {},
	"BoxSetLibraryFolders": {}, "ContributingArtistIds": {},
}

func validateItemQueryReferenceParams(t *testing.T, caseName string, params map[string]any) {
	t.Helper()
	for key := range itemQuerySymbolicReferenceParams {
		raw, exists := params[key]
		if !exists {
			continue
		}
		validateItemQuerySymbolicReference(t, caseName+"."+key, raw)
	}
}

func validateItemQueryGetItemsParams(t *testing.T, caseName string, params map[string]any) {
	t.Helper()
	for name := range params {
		if _, ok := itemQueryGetItemsParamNames[strings.ToLower(name)]; !ok {
			t.Errorf("case %q has unsupported public GetItems parameter %q", caseName, name)
		}
	}
	if raw, ok := params["ParentId"]; ok {
		validateItemQueryParentReference(t, caseName+".ParentId", raw)
	}
}

func validateItemQueryParentReference(t *testing.T, path string, raw any) {
	t.Helper()
	reference, ok := raw.(map[string]any)
	if !ok {
		t.Errorf("%s = %#v, want symbolic folder/container reference", path, raw)
		return
	}
	identity, ok := reference["$item"].(string)
	if !ok {
		return
	}
	typeName, _, found := strings.Cut(identity, ":")
	if !found || !itemQueryContainerKinds[typeName] {
		t.Errorf("%s = %q, want folder/container symbolic target", path, identity)
	}
}

var itemQueryContainerKinds = map[string]bool{"Folder": true, "BoxSet": true, "CollectionFolder": true, "UserView": true, "Playlist": true, "Series": true, "Season": true, "MusicAlbum": true}

func validateItemQuerySymbolicReference(t *testing.T, path string, raw any) {
	t.Helper()
	switch value := raw.(type) {
	case map[string]any:
		identity, ok := value["$item"].(string)
		if len(value) != 1 || !ok || !canonicalItemRef.MatchString(identity) {
			t.Errorf("%s = %#v, want exactly {$item: Type:/media/...}", path, raw)
		}
	case []any:
		for index, nested := range value {
			validateItemQuerySymbolicReference(t, fmt.Sprintf("%s[%d]", path, index), nested)
		}
	default:
		t.Errorf("%s = %#v, want symbolic item reference", path, raw)
	}
}

func stockStringSlice(t *testing.T, caseName string, raw any) []string {
	t.Helper()
	if raw == nil {
		return nil
	}
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("case %q SortBy is %T, want array", caseName, raw)
	}
	result := make([]string, len(values))
	for i, value := range values {
		name, ok := value.(string)
		if !ok {
			t.Fatalf("case %q SortBy[%d] is %T, want string", caseName, i, value)
		}
		result[i] = name
	}
	return result
}

func validateItemQueryStockProvenance(t *testing.T, root string) {
	t.Helper()
	var provenance itemQueryStockProvenance
	decodeItemQueryStockJSON(t, filepath.Join(root, "provenance.json"), &provenance)
	if provenance.Format != "goldenrec-itemquery-provenance/v1" || provenance.StockVersion != "12.0.0" || provenance.UpstreamCommit == "" || provenance.StockImageDigest == "" || provenance.UpstreamCommitAuthority == "" {
		t.Fatalf("invalid stock provenance: %#v", provenance)
	}
	for name, value := range map[string]string{
		"recorder_sha256": provenance.RecorderSHA256, "request_manifest_sha256": provenance.RequestManifestSHA256,
		"seed_sha256": provenance.SeedSHA256, "provider_policy_sha256": provenance.ProviderPolicySHA256,
		"expected_inventory_sha256": provenance.ExpectedInventorySHA256, "fixture_manifest_sha256": provenance.FixtureManifestSHA256,
		"fixture_sha256": provenance.FixtureSHA256,
	} {
		if !sha256Hex.MatchString(value) {
			t.Errorf("provenance %s = %q, want SHA-256 hex", name, value)
		}
	}
	validateSHA256Sums(t, root)
}

var (
	sha256Hex          = regexp.MustCompile(`^[0-9a-f]{64}$`)
	canonicalItemRef   = regexp.MustCompile(`^[A-Za-z]+:/media(?:/[^\x00]*)?$`)
	runtimeUUID        = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	secretBearingField = regexp.MustCompile(`(?i)(password|secret|token|authorization|cookie|api[_-]?key)`)
	kebabCaseName      = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

func validateSHA256Sums(t *testing.T, root string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "SHA256SUMS"))
	if err != nil {
		t.Fatalf("read item query SHA256SUMS: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("item query SHA256SUMS must end with a newline")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	wantFiles := map[string]bool{"itemquery.json": false, "itemquery-fixtures.json": false, "provenance.json": false}
	seenFiles := make(map[string]struct{}, len(wantFiles))
	for _, line := range lines {
		parts := strings.Split(line, "  ")
		if len(parts) != 2 || !sha256Hex.MatchString(parts[0]) || parts[1] == "" || strings.ContainsAny(parts[1], " \t") {
			t.Fatalf("invalid SHA256SUMS entry %q", line)
		}
		if _, known := wantFiles[parts[1]]; !known {
			t.Fatalf("unexpected item query SHA256SUMS file %q", parts[1])
		}
		if _, duplicate := seenFiles[parts[1]]; duplicate {
			t.Fatalf("duplicate item query SHA256SUMS file %q", parts[1])
		}
		seenFiles[parts[1]] = struct{}{}
		contents, err := os.ReadFile(filepath.Join(root, parts[1]))
		if err != nil {
			t.Fatalf("read checksummed item query artifact %q: %v", parts[1], err)
		}
		actual := sha256.Sum256(contents)
		if got := hex.EncodeToString(actual[:]); got != parts[0] {
			t.Errorf("SHA256SUMS %s = %s, actual %s", parts[1], parts[0], got)
		}
		wantFiles[parts[1]] = true
	}
	for name, found := range wantFiles {
		if !found {
			t.Errorf("SHA256SUMS is missing %s", name)
		}
	}
}

type itemQueryStockCoverage struct {
	falseValue, emptyValue, rangeValue, combined, paging, totalOn, totalOff bool
}

func (c *itemQueryStockCoverage) observe(params map[string]any) {
	if len(params) > 1 {
		c.combined = true
	}
	for _, value := range params {
		switch value := value.(type) {
		case bool:
			c.falseValue = c.falseValue || !value
		case []any:
			c.emptyValue = c.emptyValue || len(value) == 0
		case map[string]any:
			c.emptyValue = c.emptyValue || len(value) == 0
		}
	}
	_, hasMinPremiere := params["MinPremiereDate"]
	_, hasMaxPremiere := params["MaxPremiereDate"]
	_, hasMinCommunity := params["MinCommunityRating"]
	_, hasMaxCommunity := params["MaxCommunityRating"]
	_, hasMinRuntime := params["MinRunTimeTicks"]
	_, hasMaxRuntime := params["MaxRunTimeTicks"]
	c.rangeValue = c.rangeValue || (hasMinPremiere && hasMaxPremiere) || (hasMinCommunity && hasMaxCommunity) || (hasMinRuntime && hasMaxRuntime)
	_, hasStart := params["StartIndex"]
	_, hasLimit := params["Limit"]
	c.paging = c.paging || (hasStart && hasLimit)
	if value, ok := params["EnableTotalRecordCount"].(bool); ok {
		c.totalOn = c.totalOn || value
		c.totalOff = c.totalOff || !value
	}
}

func (c itemQueryStockCoverage) require(t *testing.T) {
	t.Helper()
	for name, covered := range map[string]bool{
		"false predicate": c.falseValue, "empty collection": c.emptyValue, "inclusive range": c.rangeValue,
		"combined predicates": c.combined, "paging": c.paging, "total count enabled": c.totalOn, "total count disabled": c.totalOff,
	} {
		if !covered {
			t.Errorf("stock corpus has no %s case", name)
		}
	}
}

// TestItemQueryStockCorpusSemanticWitnesses prevents a syntactically valid
// request/response projection from passing the corpus ratchet without proving
// source-visible GetItems semantics. The witnesses use captured fixture
// metadata, not the local translator, so they establish a minimal supported
// wire-level oracle before replay ascribes a mismatch to Deadfin.
func TestItemQueryStockCorpusSemanticWitnesses(t *testing.T) {
	corpus, root := loadItemQueryStockCorpus(t)
	var fixture itemQueryStockFixture
	decodeItemQueryStockJSON(t, filepath.Join(root, "itemquery-fixtures.json"), &fixture)
	if fixture.Format != "goldenrec-itemquery-fixtures/v1" || len(fixture.Items) == 0 {
		t.Fatalf("fixture format/items = %q/%d, want goldenrec-itemquery-fixtures/v1 and non-empty", fixture.Format, len(fixture.Items))
	}
	byIdentity := make(map[string]itemQueryStockFixtureItem, len(fixture.Items))
	for _, record := range fixture.Items {
		if _, exists := byIdentity[record.Identity]; exists {
			t.Fatalf("fixture repeats canonical identity %q", record.Identity)
		}
		byIdentity[record.Identity] = record
	}

	witnesses := itemQueryStockWireWitnesses{
		provider: map[string]map[bool]bool{"HasImdbId": {}, "HasTmdbId": {}},
		user:     map[string]map[bool]bool{"IsFavorite": {}, "IsPlayed": {}},
	}
	for index := range corpus.Cases {
		tc := corpus.Cases[index]
		collapseBoxSetItems, _ := tc.Request.Params["CollapseBoxSetItems"].(bool)
		if raw, exists := tc.Request.Params["Ids"]; exists {
			if stockAssertIDsWitness(t, tc, raw) {
				witnesses.ids = true
			}
		}
		if raw, exists := tc.Request.Params["IncludeItemTypes"]; exists {
			witnesses.includeItemTypes = true
			stockAssertItemTypeWitness(t, tc, byIdentity, raw, true, collapseBoxSetItems)
		}
		if raw, exists := tc.Request.Params["ExcludeItemTypes"]; exists {
			witnesses.excludeItemTypes = true
			stockAssertItemTypeWitness(t, tc, byIdentity, raw, false, collapseBoxSetItems)
		}
		if raw, exists := tc.Request.Params["ParentId"]; exists && tc.Request.Params["Ids"] == nil && stockFolderParentWitness(t, tc, byIdentity, raw) {
			witnesses.parent = true
			stockAssertParentWitness(t, tc, byIdentity, raw)
		}
		for _, provider := range []struct {
			parameter string
			provider  string
		}{
			{parameter: "HasImdbId", provider: "Imdb"},
			{parameter: "HasTmdbId", provider: "Tmdb"},
		} {
			if value, exists := tc.Request.Params[provider.parameter].(bool); exists {
				witnesses.provider[provider.parameter][value] = true
				stockAssertProviderBooleanWitness(t, tc, provider.provider, value, byIdentity)
			}
		}
		for _, user := range []struct {
			parameter string
			fixture   string
		}{
			{parameter: "IsFavorite", fixture: "IsFavorite"},
			{parameter: "IsPlayed", fixture: "Played"},
		} {
			if value, exists := tc.Request.Params[user.parameter].(bool); exists {
				witnesses.user[user.parameter][value] = true
				stockAssertUserBooleanWitness(t, tc, user.parameter, user.fixture, value, byIdentity)
			}
		}
	}
	witnesses.require(t)
}

type itemQueryStockWireWitnesses struct {
	ids, includeItemTypes, excludeItemTypes, parent bool
	provider                                        map[string]map[bool]bool
	user                                            map[string]map[bool]bool
}

func (w *itemQueryStockWireWitnesses) require(t *testing.T) {
	t.Helper()
	for name, covered := range map[string]bool{
		"ids":              w.ids,
		"includeItemTypes": w.includeItemTypes,
		"excludeItemTypes": w.excludeItemTypes,
		"parentId":         w.parent,
		"HasImdbId=true":   w.provider["HasImdbId"][true],
		"HasImdbId=false":  w.provider["HasImdbId"][false],
		"HasTmdbId=true":   w.provider["HasTmdbId"][true],
		"HasTmdbId=false":  w.provider["HasTmdbId"][false],
		"IsFavorite=true":  w.user["IsFavorite"][true],
		"IsFavorite=false": w.user["IsFavorite"][false],
		"IsPlayed=true":    w.user["IsPlayed"][true],
		"IsPlayed=false":   w.user["IsPlayed"][false],
	} {
		if !covered {
			t.Errorf("corpus has no supported-wire semantic witness for %s", name)
		}
	}
}

func stockAssertIDsWitness(t *testing.T, tc itemQueryStockCorpusCase, raw any) bool {
	t.Helper()
	want := stockWitnessReferences(t, tc.Name+".Ids", raw)
	if stockIDsOnlyCase(tc.Request.Params) {
		if !reflect.DeepEqual(tc.Expect.Items, want) || tc.Expect.TotalRecordCount != len(want) {
			t.Errorf("case %q Ids-only result = %v/%d, want exact requested identities %v/%d", tc.Name, tc.Expect.Items, tc.Expect.TotalRecordCount, want, len(want))
		}
		return true
	}
	if term, ok := tc.Request.Params["SearchTerm"].(string); ok && strings.TrimSpace(term) != "" {
		got := make(map[string]struct{}, len(tc.Expect.Items))
		for _, identity := range tc.Expect.Items {
			got[identity] = struct{}{}
		}
		for _, identity := range want {
			if _, found := got[identity]; !found {
				t.Errorf("case %q SearchTerm+Ids result omits explicit identity %q; ItemsController unions IDs with search matches", tc.Name, identity)
			}
		}
	}
	return false
}

func stockIDsOnlyCase(params map[string]any) bool {
	for name := range params {
		switch name {
		case "Ids", "Recursive", "Fields", "SortBy", "SortOrder", "EnableTotalRecordCount", "ParentId":
		default:
			return false
		}
	}
	return true
}

func stockAssertItemTypeWitness(t *testing.T, tc itemQueryStockCorpusCase, byIdentity map[string]itemQueryStockFixtureItem, raw any, included, collapseBoxSetItems bool) {
	t.Helper()
	types := stockStringSlice(t, tc.Name, raw)
	allowed := make(map[string]struct{}, len(types))
	for _, kind := range types {
		allowed[kind] = struct{}{}
	}
	for _, identity := range tc.Expect.Items {
		kind, _, ok := strings.Cut(identity, ":")
		if !ok {
			t.Errorf("case %q has malformed identity %q", tc.Name, identity)
			continue
		}
		_, selected := allowed[kind]
		if selected == included {
			continue
		}
		if kind == "BoxSet" && included && collapseBoxSetItems && stockBoxSetCollapseReplacement(identity, allowed, byIdentity) {
			continue
		}
		verb := "excluded"
		if included {
			verb = "included"
		}
		t.Errorf("case %q %s item type result contains %q", tc.Name, verb, identity)
	}
}

func stockBoxSetCollapseReplacement(identity string, allowed map[string]struct{}, byIdentity map[string]itemQueryStockFixtureItem) bool {
	for childIdentity, child := range byIdentity {
		if child.Relations.Parent == nil || child.Relations.Parent.Item != identity {
			continue
		}
		kind, _, ok := strings.Cut(childIdentity, ":")
		if ok {
			if _, selected := allowed[kind]; selected {
				return true
			}
		}
	}
	return false
}

func stockFolderParentWitness(t *testing.T, tc itemQueryStockCorpusCase, byIdentity map[string]itemQueryStockFixtureItem, raw any) bool {
	t.Helper()
	parent := stockWitnessReferences(t, tc.Name+".ParentId", raw)
	if len(parent) != 1 {
		return false
	}
	record, exists := byIdentity[parent[0]]
	return exists && stockRawBool(record.Scalars, "IsFolder")
}

func stockAssertParentWitness(t *testing.T, tc itemQueryStockCorpusCase, byIdentity map[string]itemQueryStockFixtureItem, raw any) {
	t.Helper()
	parent := stockWitnessReferences(t, tc.Name+".ParentId", raw)
	if len(parent) != 1 {
		return
	}
	recursive, _ := tc.Request.Params["Recursive"].(bool)
	for _, identity := range tc.Expect.Items {
		if !stockIsChildOf(byIdentity, identity, parent[0], recursive) {
			t.Errorf("case %q ParentId=%q returned non-child %q", tc.Name, parent[0], identity)
		}
	}
}

func stockIsChildOf(byIdentity map[string]itemQueryStockFixtureItem, identity, parent string, recursive bool) bool {
	for visited := map[string]struct{}{}; ; {
		record, exists := byIdentity[identity]
		if !exists || record.Relations.Parent == nil {
			return false
		}
		identity = record.Relations.Parent.Item
		if identity == parent {
			return true
		}
		if !recursive {
			return false
		}
		if _, repeated := visited[identity]; repeated {
			return false
		}
		visited[identity] = struct{}{}
	}
}

func stockAssertProviderBooleanWitness(t *testing.T, tc itemQueryStockCorpusCase, provider string, want bool, byIdentity map[string]itemQueryStockFixtureItem) {
	t.Helper()
	for _, identity := range tc.Expect.Items {
		record, exists := byIdentity[identity]
		_, got := record.Relations.ProviderIDs[provider]
		if !exists || got != want {
			t.Errorf("case %q provider %s present=%t returned %q", tc.Name, provider, want, identity)
		}
	}
}

func stockAssertUserBooleanWitness(t *testing.T, tc itemQueryStockCorpusCase, parameter, fixtureKey string, want bool, byIdentity map[string]itemQueryStockFixtureItem) {
	t.Helper()
	for _, identity := range tc.Expect.Items {
		record, exists := byIdentity[identity]
		if !exists || stockRawBool(record.UserData.Values, fixtureKey) != want {
			t.Errorf("case %q %s=%t returned %q", tc.Name, parameter, want, identity)
		}
	}
}

func stockWitnessReferences(t *testing.T, path string, raw any) []string {
	t.Helper()
	values := []any{raw}
	if slice, ok := raw.([]any); ok {
		values = slice
	}
	result := make([]string, len(values))
	for index, value := range values {
		object, ok := value.(map[string]any)
		if !ok || len(object) != 1 {
			t.Errorf("%s[%d] = %#v, want exactly {$item: Type:/media/...}", path, index, value)
			continue
		}
		identity, ok := object["$item"].(string)
		if !ok || !canonicalItemRef.MatchString(identity) {
			t.Errorf("%s[%d] = %#v, want canonical symbolic item identity", path, index, value)
			continue
		}
		result[index] = identity
	}
	return result
}

func validateItemQueryStockValue(t *testing.T, path string, value any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		for key, nested := range value {
			if secretBearingField.MatchString(key) {
				t.Errorf("%s contains forbidden secret-bearing field %q", path, key)
			}
			validateItemQueryStockValue(t, path+"."+key, nested)
		}
	case []any:
		for i, nested := range value {
			validateItemQueryStockValue(t, fmt.Sprintf("%s[%d]", path, i), nested)
		}
	case []string:
		for i, nested := range value {
			validateItemQueryStockString(t, fmt.Sprintf("%s[%d]", path, i), nested)
		}
	case string:
		validateItemQueryStockString(t, path, value)
	}
}

func validateItemQueryStockString(t *testing.T, path, value string) {
	t.Helper()
	if runtimeUUID.MatchString(value) {
		t.Errorf("%s contains a runtime UUID", path)
	}
	if strings.Contains(value, "://") || strings.HasPrefix(value, "/Users/") || strings.HasPrefix(value, "/home/") {
		t.Errorf("%s contains a host-specific location %q", path, value)
	}
	if strings.Contains(path, "$item") || strings.Contains(path, "items[") {
		if strings.Contains(value, ":") && !canonicalItemRef.MatchString(value) {
			t.Errorf("%s = %q, want canonical Type:/media symbolic reference", path, value)
		}
	}
}
