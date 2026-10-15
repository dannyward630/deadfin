// SPDX-License-Identifier: GPL-2.0-only

package displaypreferences_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/displaypreferences"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestResolveIDMatchesUTF16LEMD5DotNetGUIDAndPreservesGUIDs(t *testing.T) {
	parsed, err := dto.ParseUUID("8a1b4a2f-9dbe-4f37-9f6b-2b1c3d4e5f60")
	if err != nil {
		t.Fatalf("ParseUUID fixture: %v", err)
	}

	for _, tt := range []struct {
		name  string
		input string
		want  dto.UUID
	}{
		{
			name:  "legacy usersettings key hashes through UTF-16LE MD5 and .NET GUID ordering",
			input: "usersettings",
			want:  mustUUID(t, "3ce5b65d-e116-d731-65d1-efc4a30ec35c"),
		},
		{
			name:  "non-BMP input preserves UTF-16 surrogate pairs",
			input: "Hello 🚀",
			want:  mustUUID(t, "0e732764-6feb-6592-edcb-8f7832357e58"),
		},
		{
			name:  "real UUID is not rehashed",
			input: "8a1b4a2f-9dbe-4f37-9f6b-2b1c3d4e5f60",
			want:  parsed,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := displaypreferences.ResolveID(tt.input)
			if err != nil {
				t.Fatalf("ResolveID(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("ResolveID(%q) = %s, want %s", tt.input, got.Dashed(), tt.want.Dashed())
			}
		})
	}
}

func TestServiceGetMissingPreferenceSynthesizesExactFreshStockDocument(t *testing.T) {
	service := newService(t, newMemoryStore())
	userID := mustUUID(t, "11111111-1111-1111-1111-111111111111")
	const routeID, client = "usersettings", "Web"
	itemID := mustResolvedID(t, routeID)

	got, err := service.Get(t.Context(), userID, routeID, client)
	if err != nil {
		t.Fatalf("Get missing preference: %v", err)
	}
	want := freshPreferences(itemID.Dashed(), client)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get missing preference = %#v, want exact fresh stock document %#v", got, want)
	}
}

func TestServiceGetPreservesDirectPersistedPartialOpaqueDocument(t *testing.T) {
	memory := newMemoryStore()
	service := newService(t, memory)
	userID := mustUUID(t, "13131313-1313-1313-1313-131313131313")
	const routeID, client = "usersettings", "Web"
	itemID := mustResolvedID(t, routeID)

	memory.preferences[preferenceKey{userID: userID, itemID: itemID, client: client}] = store.DisplayPreference{
		UserID: userID,
		ItemID: itemID,
		Client: client,
		Data:   []byte(`{"Id":"persisted-item-id","Client":"persisted-client","ViewType":"PluginView","RememberSorting":true,"ShowSidebar":true,"CustomPrefs":{"third-party-layout":"custom-layout","third-party-null":null}}`),
	}

	got, err := service.Get(t.Context(), userID, routeID, client)
	if err != nil {
		t.Fatalf("Get direct persisted partial preference: %v", err)
	}
	want := dto.DisplayPreferences{
		ID:              stringPtr(itemID.Dashed()),
		ViewType:        stringPtr("PluginView"),
		CustomPrefs:     map[string]*string{"third-party-layout": stringPtr("custom-layout"), "third-party-null": nil},
		RememberSorting: true,
		ShowSidebar:     true,
		Client:          stringPtr(client),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get direct persisted partial preference = %#v, want exact opaque persisted document with route identity %#v", got, want)
	}
}

func TestServicePersistedPreferenceOverridesFreshDefaultsAndSurvivesServiceReopen(t *testing.T) {
	memory := newMemoryStore()
	userID := mustUUID(t, "12121212-1212-1212-1212-121212121212")
	const routeID, client = "usersettings", "Web"
	itemID := mustResolvedID(t, routeID)
	service := newService(t, memory)

	persisted := freshPreferences(itemID.Dashed(), client)
	persisted.SortBy = stringPtr("DateCreated")
	persisted.RememberSorting = true
	persisted.CustomPrefs = map[string]*string{
		"chromecastVersion":          stringPtr("NIGHTLY"),
		"skipForwardLength":          stringPtr("00030000"),
		"skipBackLength":             stringPtr("010000"),
		"enableNextVideoInfoOverlay": stringPtr("true"),
		"tvhome":                     stringPtr(""),
		"dashboardTheme":             nil,
		"userCasing":                 stringPtr("MiXeD Value"),
	}
	if err := service.Put(t.Context(), userID, routeID, client, persisted); err != nil {
		t.Fatalf("Put explicit user preference: %v", err)
	}

	reopened := newService(t, memory)
	got, err := reopened.Get(t.Context(), userID, routeID, client)
	if err != nil {
		t.Fatalf("Get persisted preference after service reopen: %v", err)
	}
	if !reflect.DeepEqual(got, persisted) {
		t.Fatalf("persisted preference after reopen = %#v, want exact user document %#v", got, persisted)
	}
}

func TestServicePropagatesStoreReadFailuresOtherThanNotFound(t *testing.T) {
	storeFailure := errors.New("display preference store unavailable")
	memory := newMemoryStore()
	memory.err = storeFailure
	service := newService(t, memory)

	_, err := service.Get(t.Context(), mustUUID(t, "99999999-9999-9999-9999-999999999999"), "usersettings", "Web")
	if !errors.Is(err, storeFailure) {
		t.Fatalf("Get store error = %v, want propagated %v", err, storeFailure)
	}
}

func TestServicePersistsFullOpaqueDocumentWithRouteIdentityAndExactCustomPreferences(t *testing.T) {
	memory := newMemoryStore()
	service := newService(t, memory)
	userID := mustUUID(t, "22222222-2222-2222-2222-222222222222")
	const routeID, client = "usersettings", "Android TV"
	itemID := mustResolvedID(t, routeID)

	input := dto.DisplayPreferences{
		ID:                 stringPtr("untrusted-item-id"),
		ViewType:           stringPtr("Movies"),
		SortBy:             stringPtr("DateCreated"),
		IndexBy:            stringPtr("ProductionYear"),
		RememberIndexing:   true,
		PrimaryImageHeight: 1080,
		PrimaryImageWidth:  1920,
		CustomPrefs: map[string]*string{
			"chromecastVersion":          stringPtr("NIGHTLY"),
			"skipForwardLength":          stringPtr("00030000"),
			"skipBackLength":             stringPtr("010000"),
			"enableNextVideoInfoOverlay": stringPtr("true"),
			"dashboardTheme":             stringPtr("custom dark"),
			"third-party-key":            stringPtr("  preserve exactly  "),
			"nullable-third-party-key":   nil,
		},
		ScrollDirection: dto.DisplayPreferencesScrollDirectionVertical,
		ShowBackdrop:    false,
		RememberSorting: true,
		SortOrder:       dto.DisplayPreferencesSortOrderDescending,
		ShowSidebar:     true,
		Client:          stringPtr("untrusted-client"),
	}

	if err := service.Put(t.Context(), userID, routeID, client, input); err != nil {
		t.Fatalf("Put opaque preference document: %v", err)
	}
	got, err := service.Get(t.Context(), userID, routeID, client)
	if err != nil {
		t.Fatalf("Get persisted preference: %v", err)
	}
	want := input
	want.ID = stringPtr(itemID.Dashed())
	want.Client = stringPtr(client)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get after Put = %#v, want exact persisted full document %#v", got, want)
	}
}

func TestServicePersistsReservedCustomPreferenceValuesWithoutCoercion(t *testing.T) {
	service := newService(t, newMemoryStore())
	userID := mustUUID(t, "33333333-3333-3333-3333-333333333333")
	const routeID, client = "usersettings", "Web"
	preference := freshPreferences(mustResolvedID(t, routeID).Dashed(), client)
	preference.CustomPrefs = map[string]*string{
		"chromecastVersion":          stringPtr("NIGHTLY"),
		"skipForwardLength":          stringPtr("not-a-millisecond-count"),
		"skipBackLength":             nil,
		"enableNextVideoInfoOverlay": stringPtr("true"),
		"tvhome":                     stringPtr(""),
		"dashboardTheme":             nil,
	}
	if err := service.Put(t.Context(), userID, routeID, client, preference); err != nil {
		t.Fatalf("Put custom preference values: %v", err)
	}
	got, err := service.Get(t.Context(), userID, routeID, client)
	if err != nil {
		t.Fatalf("Get exact custom preference values: %v", err)
	}
	if !reflect.DeepEqual(got, preference) {
		t.Fatalf("custom preference values = %#v, want exact saved document %#v", got, preference)
	}
}

func TestServiceIsolatesPreferencesByUserAndClient(t *testing.T) {
	service := newService(t, newMemoryStore())
	const routeID = "usersettings"
	firstUser := mustUUID(t, "44444444-4444-4444-4444-444444444444")
	secondUser := mustUUID(t, "55555555-5555-5555-5555-555555555555")

	first := stockPreferences(mustResolvedID(t, routeID).Dashed(), "Web")
	first.SortBy = stringPtr("DatePlayed")
	first.CustomPrefs["scope"] = stringPtr("first-user-web")
	if err := service.Put(t.Context(), firstUser, routeID, "Web", first); err != nil {
		t.Fatalf("Put first user web preference: %v", err)
	}
	second := stockPreferences(mustResolvedID(t, routeID).Dashed(), "Android")
	second.SortBy = stringPtr("CommunityRating")
	second.CustomPrefs["scope"] = stringPtr("second-user-android")
	if err := service.Put(t.Context(), secondUser, routeID, "Android", second); err != nil {
		t.Fatalf("Put second user android preference: %v", err)
	}

	gotFirst, err := service.Get(t.Context(), firstUser, routeID, "Web")
	if err != nil {
		t.Fatalf("Get first user web preference: %v", err)
	}
	if !reflect.DeepEqual(gotFirst, first) {
		t.Fatalf("first user web preference = %#v, want %#v", gotFirst, first)
	}
	gotSecond, err := service.Get(t.Context(), secondUser, routeID, "Android")
	if err != nil {
		t.Fatalf("Get second user android preference: %v", err)
	}
	if !reflect.DeepEqual(gotSecond, second) {
		t.Fatalf("second user android preference = %#v, want %#v", gotSecond, second)
	}
	missing, err := service.Get(t.Context(), firstUser, routeID, "Android")
	if err != nil {
		t.Fatalf("Get isolated user/client preference: %v", err)
	}
	if want := freshPreferences(mustResolvedID(t, routeID).Dashed(), "Android"); !reflect.DeepEqual(missing, want) {
		t.Fatalf("isolated user/client preference = %#v, want fresh %#v", missing, want)
	}
}

func TestServiceReturnsDefensiveCustomPreferenceMaps(t *testing.T) {
	service := newService(t, newMemoryStore())
	userID := mustUUID(t, "66666666-6666-6666-6666-666666666666")
	const routeID, client = "usersettings", "Web"
	preference := stockPreferences(mustResolvedID(t, routeID).Dashed(), client)
	preference.CustomPrefs["third-party-key"] = stringPtr("saved")
	if err := service.Put(t.Context(), userID, routeID, client, preference); err != nil {
		t.Fatalf("Put preference: %v", err)
	}

	first, err := service.Get(t.Context(), userID, routeID, client)
	if err != nil {
		t.Fatalf("Get first copy: %v", err)
	}
	first.CustomPrefs["third-party-key"] = stringPtr("caller mutation")
	second, err := service.Get(t.Context(), userID, routeID, client)
	if err != nil {
		t.Fatalf("Get second copy: %v", err)
	}
	if got := stringValue(second.CustomPrefs["third-party-key"]); got != "saved" {
		t.Fatalf("Get after caller map mutation has third-party-key %q, want persisted value %q", got, "saved")
	}
}

func TestServiceConcurrentWritesLeaveOneCompleteDocument(t *testing.T) {
	service := newService(t, newMemoryStore())
	userID := mustUUID(t, "77777777-7777-7777-7777-777777777777")
	const routeID, client = "usersettings", "Web"
	itemID := mustResolvedID(t, routeID)
	candidates := []dto.DisplayPreferences{
		completePreference(itemID.Dashed(), client, "DateCreated", "first"),
		completePreference(itemID.Dashed(), client, "CommunityRating", "second"),
	}

	start := make(chan struct{})
	results := make(chan error, len(candidates))
	var ready, done sync.WaitGroup
	ready.Add(len(candidates))
	done.Add(len(candidates))
	for _, candidate := range candidates {
		go func(preference dto.DisplayPreferences) {
			defer done.Done()
			ready.Done()
			<-start
			results <- service.Put(t.Context(), userID, routeID, client, preference)
		}(candidate)
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent Put: %v", err)
		}
	}

	got, err := service.Get(t.Context(), userID, routeID, client)
	if err != nil {
		t.Fatalf("Get after concurrent Put: %v", err)
	}
	if !reflect.DeepEqual(got, candidates[0]) && !reflect.DeepEqual(got, candidates[1]) {
		t.Fatalf("concurrent last-write document = %#v, want one complete candidate %#v or %#v", got, candidates[0], candidates[1])
	}
}

func TestServiceRejectsMissingStoreIDOrClientWithTypedValidationErrors(t *testing.T) {
	if _, err := displaypreferences.New(nil); err == nil {
		t.Fatal("New(nil) = nil error, want typed validation error")
	} else {
		assertValidationError(t, err)
	}

	service := newService(t, newMemoryStore())
	userID := mustUUID(t, "88888888-8888-8888-8888-888888888888")
	for _, tt := range []struct {
		name   string
		id     string
		client string
	}{
		{name: "empty id", id: "", client: "Web"},
		{name: "empty client", id: "usersettings", client: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.Get(t.Context(), userID, tt.id, tt.client)
			if err == nil {
				t.Fatal("Get = nil error, want typed validation error")
			}
			assertValidationError(t, err)
		})
	}
}

// Reserved-looking custom preference keys are user-owned persisted values. A
// later client write must neither rewrite their spelling/value nor materialize
// missing keys from the fresh-document defaults.
func TestServicePreservesCustomPreferenceValuesAndKeyCasing(t *testing.T) {
	const routeID, client = "usersettings", "Web"
	itemID := mustResolvedID(t, routeID)
	for _, tt := range []struct {
		name  string
		input map[string]*string
	}{
		{
			name: "empty values and nulls remain distinguishable",
			input: map[string]*string{
				"chromecastVersion":          stringPtr(""),
				"skipForwardLength":          stringPtr(""),
				"skipBackLength":             nil,
				"enableNextVideoInfoOverlay": stringPtr(""),
				"dashboardTheme":             nil,
				"tvhome":                     stringPtr(""),
				"third-party":                stringPtr("  unchanged  "),
			},
		},
		{
			name: "reserved-looking keys retain source casing and literal values",
			input: map[string]*string{
				"HomeSection0":      stringPtr("Resume"),
				"Landing-not-guid":  stringPtr("not-a-view-type"),
				"ChromecastVersion": stringPtr("ordinary exact-case-sensitive custom value"),
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := newService(t, newMemoryStore())
			preference := freshPreferences(itemID.Dashed(), client)
			preference.CustomPrefs = cloneCustomPrefs(tt.input)
			if err := service.Put(t.Context(), mustUUID(t, "abababab-abab-abab-abab-abababababab"), routeID, client, preference); err != nil {
				t.Fatalf("Put custom preference values: %v", err)
			}
			got, err := service.Get(t.Context(), mustUUID(t, "abababab-abab-abab-abab-abababababab"), routeID, client)
			if err != nil {
				t.Fatalf("Get custom preference values: %v", err)
			}
			if !reflect.DeepEqual(got.CustomPrefs, tt.input) {
				t.Fatalf("custom preferences = %#v, want exact persisted values %#v", got.CustomPrefs, tt.input)
			}
		})
	}
}

type memoryStore struct {
	mu          sync.Mutex
	preferences map[preferenceKey]store.DisplayPreference
	err         error
}

type preferenceKey struct {
	userID dto.UUID
	itemID dto.UUID
	client string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{preferences: make(map[preferenceKey]store.DisplayPreference)}
}

func (s *memoryStore) GetDisplayPreference(_ context.Context, userID, itemID dto.UUID, client string) (store.DisplayPreference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return store.DisplayPreference{}, s.err
	}
	preference, ok := s.preferences[preferenceKey{userID: userID, itemID: itemID, client: client}]
	if !ok {
		return store.DisplayPreference{}, store.ErrNotFound
	}
	preference.Data = append([]byte(nil), preference.Data...)
	return preference, nil
}

func (s *memoryStore) PutDisplayPreference(_ context.Context, preference store.DisplayPreference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	preference.Data = append([]byte(nil), preference.Data...)
	s.preferences[preferenceKey{userID: preference.UserID, itemID: preference.ItemID, client: preference.Client}] = preference
	return nil
}

func newService(t *testing.T, preferenceStore displaypreferences.Store) *displaypreferences.Service {
	t.Helper()
	service, err := displaypreferences.New(preferenceStore)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return service
}

func freshPreferences(id, client string) dto.DisplayPreferences {
	return dto.DisplayPreferences{
		ID:                 stringPtr(id),
		ViewType:           nil,
		SortBy:             stringPtr("SortName"),
		IndexBy:            nil,
		RememberIndexing:   false,
		PrimaryImageHeight: 250,
		PrimaryImageWidth:  250,
		CustomPrefs: map[string]*string{
			"chromecastVersion":          stringPtr("stable"),
			"skipForwardLength":          stringPtr("30000"),
			"skipBackLength":             stringPtr("10000"),
			"enableNextVideoInfoOverlay": stringPtr("False"),
			"tvhome":                     nil,
			"dashboardTheme":             nil,
		},
		ScrollDirection: dto.DisplayPreferencesScrollDirectionHorizontal,
		ShowBackdrop:    true,
		RememberSorting: false,
		SortOrder:       dto.DisplayPreferencesSortOrderAscending,
		ShowSidebar:     false,
		Client:          stringPtr(client),
	}
}

func stockPreferences(id, client string) dto.DisplayPreferences {
	return dto.DisplayPreferences{
		ID:                 stringPtr(id),
		ViewType:           nil,
		SortBy:             stringPtr("DateCreated"),
		IndexBy:            nil,
		RememberIndexing:   false,
		PrimaryImageHeight: 250,
		PrimaryImageWidth:  250,
		CustomPrefs: map[string]*string{
			"chromecastVersion":          stringPtr("stable"),
			"skipForwardLength":          stringPtr("15000"),
			"skipBackLength":             stringPtr("15000"),
			"enableNextVideoInfoOverlay": stringPtr("True"),
			"tvhome":                     stringPtr(""),
			"dashboardTheme":             stringPtr(""),
		},
		ScrollDirection: dto.DisplayPreferencesScrollDirectionHorizontal,
		ShowBackdrop:    true,
		RememberSorting: true,
		SortOrder:       dto.DisplayPreferencesSortOrderAscending,
		ShowSidebar:     false,
		Client:          stringPtr(client),
	}
}

func completePreference(id, client, sortBy, marker string) dto.DisplayPreferences {
	preference := stockPreferences(id, client)
	preference.SortBy = stringPtr(sortBy)
	preference.ViewType = stringPtr("Movies")
	preference.IndexBy = stringPtr("ProductionYear")
	preference.RememberIndexing = true
	preference.PrimaryImageHeight = 1080
	preference.PrimaryImageWidth = 1920
	preference.ScrollDirection = dto.DisplayPreferencesScrollDirectionVertical
	preference.ShowBackdrop = false
	preference.RememberSorting = true
	preference.SortOrder = dto.DisplayPreferencesSortOrderDescending
	preference.ShowSidebar = true
	preference.CustomPrefs["winner"] = stringPtr(marker)
	return preference
}

func assertValidationError(t *testing.T, err error) {
	t.Helper()
	var validation *displaypreferences.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T %v, want *ValidationError", err, err)
	}
}

func mustUUID(t *testing.T, value string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(value)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", value, err)
	}
	return id
}

func mustResolvedID(t *testing.T, value string) dto.UUID {
	t.Helper()
	id, err := displaypreferences.ResolveID(value)
	if err != nil {
		t.Fatalf("ResolveID(%q): %v", value, err)
	}
	return id
}

func stringPtr(value string) *string { return &value }

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func cloneCustomPrefs(source map[string]*string) map[string]*string {
	cloned := make(map[string]*string, len(source))
	for key, value := range source {
		if value != nil {
			cloned[key] = stringPtr(*value)
			continue
		}
		cloned[key] = nil
	}
	return cloned
}
