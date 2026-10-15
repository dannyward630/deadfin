// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/localization"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/user"
)

// TestStoreEnricherAppliesPersistedUserSelectionsOnlyToSourcesContainingThem
// defends the /Items/{id} detail contract: a persisted stream index overrides
// a source's derived default only when that source exposes a MediaStream of the
// required type at that index. Disabled subtitles (-1) are valid for every
// source, and absent, unmatched, or another user's selections leave defaults
// intact.
func TestStoreEnricherAppliesPersistedUserSelectionsOnlyToSourcesContainingThem(t *testing.T) {
	tests := []struct {
		name         string
		seedUserData func(t *testing.T, fixture builderSourceSelectionFixture)
		want         func(fixture builderSourceSelectionFixture) map[string]builderSourceSelection
	}{
		{
			name: "matching audio applies only to sources containing it while disabled subtitles apply to all sources",
			seedUserData: func(t *testing.T, fixture builderSourceSelectionFixture) {
				t.Helper()
				selectionAudio, selectionSubtitle := 7, -1
				fixture.upsertUserData(t, fixture.userID, fixture.matchingKey, &selectionAudio, &selectionSubtitle)
			},
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceSelection {
				return fixture.expectedSelections(map[string]builderSourceSelection{
					fixture.primarySourceID:   {audio: 7, subtitle: -1},
					fixture.alternateSourceID: {audio: 11, subtitle: -1},
				})
			},
		},
		{
			name: "matching subtitle applies only to sources containing it without changing audio",
			seedUserData: func(t *testing.T, fixture builderSourceSelectionFixture) {
				t.Helper()
				selectionSubtitle := 8
				fixture.upsertUserData(t, fixture.userID, fixture.matchingKey, nil, &selectionSubtitle)
			},
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceSelection {
				return fixture.expectedSelections(map[string]builderSourceSelection{
					fixture.primarySourceID: {audio: 1, subtitle: 8},
				})
			},
		},
		{
			name: "matching user data with nil selections preserves source defaults",
			seedUserData: func(t *testing.T, fixture builderSourceSelectionFixture) {
				t.Helper()
				fixture.upsertUserData(t, fixture.userID, fixture.matchingKey, nil, nil)
			},
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceSelection {
				return fixture.expectedSelections(nil)
			},
		},
		{
			name: "unmatched user data preserves source defaults",
			seedUserData: func(t *testing.T, fixture builderSourceSelectionFixture) {
				t.Helper()
				selectionAudio, selectionSubtitle := 7, -1
				fixture.upsertUserData(t, fixture.userID, "unmatched-source-selection-key", &selectionAudio, &selectionSubtitle)
			},
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceSelection {
				return fixture.expectedSelections(nil)
			},
		},
		{
			name: "other user selections preserve source defaults",
			seedUserData: func(t *testing.T, fixture builderSourceSelectionFixture) {
				t.Helper()
				selectionAudio, selectionSubtitle := 7, -1
				fixture.upsertUserData(t, builderUUID(t, 233), fixture.matchingKey, &selectionAudio, &selectionSubtitle)
			},
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceSelection {
				return fixture.expectedSelections(nil)
			},
		},
		{
			name: "no persisted user data preserves source defaults",
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceSelection {
				return fixture.expectedSelections(nil)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBuilderSourceSelectionFixture(t)
			if test.seedUserData != nil {
				test.seedUserData(t, fixture)
			}

			users := builderSourceSelectionDefaultUserReader(t, fixture.userID)

			result, err := dtobuild.NewBuilder(fixture.items, dtobuild.NewStoreEnricher(fixture.items, users, nil)).BuildDetailed(
				t.Context(),
				[]item.Item{fixture.item},
				store.ItemQueryDTOOptions{Fields: []string{"MediaSources"}, EnableUserData: true},
				&fixture.userID,
			)
			if err != nil {
				t.Fatalf("build detailed MediaSources projection: %v", err)
			}
			if got, want := len(result.Items), 1; got != want {
				t.Fatalf("detailed DTO count = %d, want %d", got, want)
			}

			wantSelections := test.want(fixture)
			sources := result.Items[0].MediaSources
			if got, want := len(sources), len(wantSelections); got != want {
				t.Fatalf("projected MediaSources = %d, want %d source graph entries", got, want)
			}
			for _, source := range sources {
				if source.ID == nil {
					t.Fatalf("projected media source missing ID: %#v", source)
				}
				want, found := wantSelections[*source.ID]
				if !found {
					t.Fatalf("projected unexpected media source %q", *source.ID)
				}
				assertBuilderSourceSelections(t, *source.ID, source, want)
			}
		})
	}
}

// TestStoreEnricherGatesPersistedSelectionsByUserPreference defends the
// /Items/{id} MediaSources contract: saved per-user choices are independent of
// EnableUserData, but each choice is gated by its explicit persisted preference.
// Audio and subtitles are evaluated independently; a None subtitle mode omits
// every source's subtitle default, including a remembered -1 sentinel.
func TestStoreEnricherGatesPersistedSelectionsByUserPreference(t *testing.T) {
	tests := []struct {
		name           string
		configuration  []byte
		enableUserData bool
		audio          *int
		subtitle       *int
		omitSubtitles  bool
		want           func(builderSourceSelectionFixture) map[string]builderSourceSelection
	}{
		{
			name:          "saved audio and disabled subtitle apply without EnableUserData",
			configuration: []byte(`{"RememberAudioSelections":true,"RememberSubtitleSelections":true,"SubtitleMode":"Default"}`),
			audio:         builderSourceSelectionPointer(7),
			subtitle:      builderSourceSelectionPointer(-1),
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceSelection {
				return fixture.expectedSelections(map[string]builderSourceSelection{
					fixture.primarySourceID:   {audio: 7, subtitle: -1},
					fixture.alternateSourceID: {audio: 11, subtitle: -1},
				})
			},
		},
		{
			name:           "audio selection is ignored when audio remembering is disabled while eligible subtitle applies to its source",
			configuration:  []byte(`{"RememberAudioSelections":false,"RememberSubtitleSelections":true,"SubtitleMode":"Default"}`),
			enableUserData: true,
			audio:          builderSourceSelectionPointer(7),
			subtitle:       builderSourceSelectionPointer(8),
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceSelection {
				return fixture.expectedSelections(map[string]builderSourceSelection{
					fixture.primarySourceID: {audio: 1, subtitle: 8},
				})
			},
		},
		{
			name:           "subtitle selection is ignored when subtitle remembering is disabled while eligible audio applies to its source",
			configuration:  []byte(`{"RememberAudioSelections":true,"RememberSubtitleSelections":false,"SubtitleMode":"Default"}`),
			enableUserData: true,
			audio:          builderSourceSelectionPointer(7),
			subtitle:       builderSourceSelectionPointer(8),
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceSelection {
				return fixture.expectedSelections(map[string]builderSourceSelection{
					fixture.primarySourceID: {audio: 7, subtitle: 2},
				})
			},
		},
		{
			name:           "None subtitle mode omits every subtitle default while remembered audio applies",
			configuration:  []byte(`{"RememberAudioSelections":true,"RememberSubtitleSelections":true,"SubtitleMode":"None"}`),
			enableUserData: true,
			audio:          builderSourceSelectionPointer(7),
			subtitle:       builderSourceSelectionPointer(-1),
			omitSubtitles:  true,
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceSelection {
				return fixture.expectedSelections(map[string]builderSourceSelection{
					fixture.primarySourceID: {audio: 7},
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBuilderSourceSelectionFixture(t)
			fixture.upsertUserData(t, fixture.userID, fixture.matchingKey, test.audio, test.subtitle)
			users := aggregateUserReader{users: map[dto.UUID]store.User{
				fixture.userID: {ID: fixture.userID, Configuration: test.configuration},
			}}

			result, err := dtobuild.NewBuilder(fixture.items, dtobuild.NewStoreEnricher(fixture.items, users, nil)).BuildDetailed(
				t.Context(),
				[]item.Item{fixture.item},
				store.ItemQueryDTOOptions{Fields: []string{"MediaSources"}, EnableUserData: test.enableUserData},
				&fixture.userID,
			)
			if err != nil {
				t.Fatalf("build detailed MediaSources projection: %v", err)
			}
			if got, want := len(result.Items), 1; got != want {
				t.Fatalf("detailed DTO count = %d, want %d", got, want)
			}

			wantSelections := test.want(fixture)
			if got, want := len(result.Items[0].MediaSources), len(wantSelections); got != want {
				t.Fatalf("projected MediaSources = %d, want %d source graph entries", got, want)
			}
			for _, source := range result.Items[0].MediaSources {
				if source.ID == nil {
					t.Fatalf("projected media source missing ID: %#v", source)
				}
				want, found := wantSelections[*source.ID]
				if !found {
					t.Fatalf("projected unexpected media source %q", *source.ID)
				}
				if test.omitSubtitles {
					assertBuilderSourceFallbackSelection(t, *source.ID, source, builderSourceFallbackSelection{audio: builderSourceSelectionPointer(want.audio)})
				} else {
					assertBuilderSourceSelections(t, *source.ID, source, want)
				}
			}
		})
	}
}

// TestStoreEnricherAppliesFallbackPreferencesWithoutUserData defends the
// /Items/{id} MediaSources contract: persisted user fallback preferences apply
// to every source even before that user has a user-data row. The UserData DTO
// option independently controls only the UserData projection.
func TestStoreEnricherAppliesFallbackPreferencesWithoutUserData(t *testing.T) {
	tests := []struct {
		name           string
		configuration  []byte
		enableUserData bool
		want           func(builderSourceSelectionFixture) map[string]builderSourceFallbackSelection
	}{
		{
			name:           "None subtitle mode omits static subtitle defaults while projecting user data",
			configuration:  []byte(`{"SubtitleMode":"None"}`),
			enableUserData: true,
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceFallbackSelection {
				return map[string]builderSourceFallbackSelection{
					fixture.primarySourceID:   {audio: builderSourceSelectionPointer(1)},
					fixture.alternateSourceID: {audio: builderSourceSelectionPointer(11)},
				}
			},
		},
		{
			name:           "English preference overrides French static defaults without projecting user data",
			configuration:  []byte(`{"AudioLanguagePreference":"eng","PlayDefaultAudioTrack":false,"SubtitleMode":"Default"}`),
			enableUserData: false,
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceFallbackSelection {
				return map[string]builderSourceFallbackSelection{
					fixture.primarySourceID:   {audio: builderSourceSelectionPointer(7), subtitle: builderSourceSelectionPointer(2)},
					fixture.alternateSourceID: {audio: builderSourceSelectionPointer(13), subtitle: builderSourceSelectionPointer(12)},
				}
			},
		},
		{
			name:           "two-letter English preference selects three-letter English streams without projecting user data",
			configuration:  []byte(`{"AudioLanguagePreference":"en","PlayDefaultAudioTrack":false,"SubtitleMode":"Default"}`),
			enableUserData: false,
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceFallbackSelection {
				return map[string]builderSourceFallbackSelection{
					fixture.primarySourceID:   {audio: builderSourceSelectionPointer(7), subtitle: builderSourceSelectionPointer(2)},
					fixture.alternateSourceID: {audio: builderSourceSelectionPointer(13), subtitle: builderSourceSelectionPointer(12)},
				}
			},
		},
		{
			name:           "OriginalLanguage preference selects original streams when item original language is empty",
			configuration:  []byte(`{"AudioLanguagePreference":"OriginalLanguage","PlayDefaultAudioTrack":false,"SubtitleMode":"Default"}`),
			enableUserData: false,
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceFallbackSelection {
				return map[string]builderSourceFallbackSelection{
					fixture.primarySourceID:   {audio: builderSourceSelectionPointer(7), subtitle: builderSourceSelectionPointer(2)},
					fixture.alternateSourceID: {audio: builderSourceSelectionPointer(13), subtitle: builderSourceSelectionPointer(12)},
				}
			},
		},
		{
			name:           "playing default audio preserves French static defaults over English preference",
			configuration:  []byte(`{"AudioLanguagePreference":"eng","PlayDefaultAudioTrack":true,"SubtitleMode":"Default"}`),
			enableUserData: true,
			want: func(fixture builderSourceSelectionFixture) map[string]builderSourceFallbackSelection {
				return map[string]builderSourceFallbackSelection{
					fixture.primarySourceID:   {audio: builderSourceSelectionPointer(1), subtitle: builderSourceSelectionPointer(2)},
					fixture.alternateSourceID: {audio: builderSourceSelectionPointer(11), subtitle: builderSourceSelectionPointer(12)},
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, users, languages := newBuilderSourceSelectionFallbackFixture(t, test.configuration)
			result, err := dtobuild.NewBuilder(fixture.items, dtobuild.NewStoreEnricher(fixture.items, users, languages)).BuildDetailed(
				t.Context(),
				[]item.Item{fixture.item},
				store.ItemQueryDTOOptions{Fields: []string{"MediaSources"}, EnableUserData: test.enableUserData},
				&fixture.userID,
			)
			if err != nil {
				t.Fatalf("build detailed MediaSources projection: %v", err)
			}
			if got, want := len(result.Items), 1; got != want {
				t.Fatalf("detailed DTO count = %d, want %d", got, want)
			}
			if test.enableUserData {
				wantUserData := &dto.UserItemDataDto{ItemID: fixture.item.ID, Key: fixture.matchingKey}
				if got := result.Items[0].UserData; !reflect.DeepEqual(got, wantUserData) {
					t.Fatalf("absent-row UserData = %#v, want %#v", got, wantUserData)
				}
			} else if got := result.Items[0].UserData; got != nil {
				t.Fatalf("UserData without EnableUserData = %#v, want nil", got)
			}

			wantSelections := test.want(fixture)
			sources := result.Items[0].MediaSources
			if got, want := len(sources), len(wantSelections); got != want {
				t.Fatalf("projected MediaSources = %d, want %d source graph entries", got, want)
			}
			for _, source := range sources {
				if source.ID == nil {
					t.Fatalf("projected media source missing ID: %#v", source)
				}
				want, found := wantSelections[*source.ID]
				if !found {
					t.Fatalf("projected unexpected media source %q", *source.ID)
				}
				assertBuilderSourceFallbackSelection(t, *source.ID, source, want)
			}
		})
	}
}

// TestStoreEnricherKeepsFirstAudioStreamForAudioItems defends the /Items/{id}
// audio-source contract: unlike video sources, an audio item always reports
// its first audio stream, without a subtitle selection, even when video-only
// fallback preferences or remembered stream history point at a later stream.
func TestStoreEnricherKeepsFirstAudioStreamForAudioItems(t *testing.T) {
	tests := []struct {
		name          string
		configuration []byte
		seedUserData  func(t *testing.T, fixture builderAudioSourceSelectionFixture)
	}{
		{
			name:          "later language preference does not replace first audio stream",
			configuration: []byte(`{"AudioLanguagePreference":"eng","PlayDefaultAudioTrack":false,"SubtitleMode":"Default"}`),
		},
		{
			name:          "later remembered selection does not replace first audio stream",
			configuration: []byte(`{"RememberAudioSelections":true,"RememberSubtitleSelections":true,"PlayDefaultAudioTrack":true,"SubtitleMode":"Default"}`),
			seedUserData: func(t *testing.T, fixture builderAudioSourceSelectionFixture) {
				t.Helper()
				selection := 7
				fixture.upsertUserData(t, &selection)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, users, languages := newBuilderAudioSourceSelectionFixture(t, test.configuration)
			if test.seedUserData != nil {
				test.seedUserData(t, fixture)
			}
			result, err := dtobuild.NewBuilder(fixture.items, dtobuild.NewStoreEnricher(fixture.items, users, languages)).BuildDetailed(
				t.Context(),
				[]item.Item{fixture.item},
				store.ItemQueryDTOOptions{Fields: []string{"MediaSources"}},
				&fixture.userID,
			)
			if err != nil {
				t.Fatalf("build detailed audio MediaSources projection: %v", err)
			}
			if got, want := len(result.Items), 1; got != want {
				t.Fatalf("detailed audio DTO count = %d, want %d", got, want)
			}
			sources := result.Items[0].MediaSources
			if got, want := len(sources), 1; got != want {
				t.Fatalf("audio MediaSources = %d, want %d", got, want)
			}
			source := sources[0]
			if source.ID == nil || *source.ID != fixture.sourceID {
				t.Fatalf("audio source ID = %v, want %q", source.ID, fixture.sourceID)
			}
			if source.DefaultAudioStreamIndex == nil || *source.DefaultAudioStreamIndex != 1 {
				t.Errorf("audio source DefaultAudioStreamIndex = %v, want 1", builderSourceSelectionIndex(source.DefaultAudioStreamIndex))
			}
			if source.DefaultSubtitleStreamIndex != nil {
				t.Errorf("audio source DefaultSubtitleStreamIndex = %v, want nil", builderSourceSelectionIndex(source.DefaultSubtitleStreamIndex))
			}
		})
	}
}

func builderSourceSelectionPointer(value int) *int { return &value }

func builderSourceSelectionDefaultUserReader(t *testing.T, userID dto.UUID) dtobuild.StoreUserReader {
	t.Helper()
	configuration := dto.DefaultUserConfiguration()
	configuration.RememberAudioSelections = true
	configuration.RememberSubtitleSelections = true
	configuration.SubtitleMode = dto.SubtitleModeDefault
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatalf("encode source selection user configuration: %v", err)
	}
	return aggregateUserReader{users: map[dto.UUID]store.User{
		userID: {ID: userID, Configuration: encoded},
	}}
}

type builderSourceSelectionFixture struct {
	items             *store.ItemStore
	item              item.Item
	userID            dto.UUID
	matchingKey       string
	primarySourceID   string
	alternateSourceID string
	defaults          map[string]builderSourceSelection
}

type builderSourceSelection struct {
	audio    int
	subtitle int
}

func newBuilderSourceSelectionFixture(t *testing.T) builderSourceSelectionFixture {
	t.Helper()
	return newBuilderSourceSelectionFixtureForItems(t, openBuilderItemStore(t))
}

func newBuilderSourceSelectionFixtureForItems(t *testing.T, items *store.ItemStore) builderSourceSelectionFixture {
	t.Helper()
	primaryID, alternateID, userID := builderUUID(t, 230), builderUUID(t, 231), builderUUID(t, 232)
	primaryName, alternateName := "Selection feature", "Selection feature alternate"
	primaryPath, alternatePath, container := "/library/selection-feature.mkv", "/library/selection-feature-alternate.mkv", "mkv"
	alternateOrder := 1
	primary := item.Item{
		ID:               primaryID,
		Kind:             item.KindMovie,
		IsMovie:          true,
		Name:             &primaryName,
		OriginalLanguage: builderString(""),
		Path:             &primaryPath,
		Container:        &container,
		LinkedChildren: []item.LinkedChildRef{{
			ItemID:    alternateID,
			Type:      item.LinkedChildTypeLocalAlternateVersion,
			SortOrder: &alternateOrder,
		}},
	}
	alternate := item.Item{ID: alternateID, Kind: item.KindMovie, IsMovie: true, Name: &alternateName, Path: &alternatePath, Container: &container}
	for _, source := range []item.Item{alternate, primary} {
		if err := items.Upsert(t.Context(), source); err != nil {
			t.Fatalf("upsert source selection fixture %s: %v", source.ID, err)
		}
	}
	for _, source := range []struct {
		id      dto.UUID
		streams []playback.MediaStream
	}{
		{
			id: primaryID,
			streams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
				{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", Language: builderString("fre"), IsDefault: true},
				{Index: 7, Type: playback.StreamTypeAudio, Codec: "aac", Language: builderString("eng"), IsOriginal: true},
				{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "subrip", IsDefault: true},
				{Index: 8, Type: playback.StreamTypeSubtitle, Codec: "subrip"},
			},
		},
		{
			id: alternateID,
			streams: []playback.MediaStream{
				{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
				{Index: 11, Type: playback.StreamTypeAudio, Codec: "aac", Language: builderString("fre"), IsDefault: true},
				{Index: 13, Type: playback.StreamTypeAudio, Codec: "aac", Language: builderString("eng"), IsOriginal: true},
				{Index: 12, Type: playback.StreamTypeSubtitle, Codec: "subrip", IsDefault: true},
			},
		},
	} {
		if err := items.ReplaceStreams(t.Context(), source.id, source.streams); err != nil {
			t.Fatalf("seed source selection streams for %s: %v", source.id, err)
		}
	}
	loaded, err := items.Get(t.Context(), primaryID)
	if err != nil {
		t.Fatalf("load source selection fixture: %v", err)
	}
	keys := item.UserDataKeys(loaded, item.UserDataInputs{})
	if len(keys) == 0 {
		t.Fatal("source selection fixture has no user-data keys")
	}
	return builderSourceSelectionFixture{
		items:             items,
		item:              loaded,
		userID:            userID,
		matchingKey:       keys[0],
		primarySourceID:   primaryID.String(),
		alternateSourceID: alternateID.String(),
		defaults: map[string]builderSourceSelection{
			primaryID.String():   {audio: 1, subtitle: 2},
			alternateID.String(): {audio: 11, subtitle: 12},
		},
	}
}

func (fixture builderSourceSelectionFixture) expectedSelections(overrides map[string]builderSourceSelection) map[string]builderSourceSelection {
	selections := make(map[string]builderSourceSelection, len(fixture.defaults))
	for sourceID, selection := range fixture.defaults {
		selections[sourceID] = selection
	}
	for sourceID, selection := range overrides {
		selections[sourceID] = selection
	}
	return selections
}

func (fixture builderSourceSelectionFixture) upsertUserData(t *testing.T, userID dto.UUID, key string, audio, subtitle *int) {
	t.Helper()
	if err := fixture.items.UpsertUserData(t.Context(), store.UserData{
		UserID:              userID,
		CustomKey:           key,
		ItemID:              fixture.item.ID,
		AudioStreamIndex:    audio,
		SubtitleStreamIndex: subtitle,
	}); err != nil {
		t.Fatalf("upsert source selection user data: %v", err)
	}
}

func newBuilderSourceSelectionFallbackFixture(t *testing.T, configuration []byte) (builderSourceSelectionFixture, dtobuild.StoreUserReader, playback.LanguageNormalizer) {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open source selection fallback SQLite store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close source selection fallback SQLite store: %v", err)
		}
	})
	fixture := newBuilderSourceSelectionFixtureForItems(t, store.NewItemStore(stores))
	identity := store.NewIdentity(stores)
	if _, err := identity.CreateUser(t.Context(), store.User{
		ID:             fixture.userID,
		Name:           "source selection fallback user",
		NormalizedName: "source selection fallback user",
		Configuration:  configuration,
	}); err != nil {
		t.Fatalf("create source selection fallback user: %v", err)
	}
	languages, err := localization.NewService()
	if err != nil {
		t.Fatalf("create source selection fallback language normalizer: %v", err)
	}
	return fixture, user.New(identity, clock.NewFake(time.Unix(0, 0).UTC()), bytes.NewReader(nil)), languages
}

type builderAudioSourceSelectionFixture struct {
	items    *store.ItemStore
	item     item.Item
	userID   dto.UUID
	key      string
	sourceID string
}

func newBuilderAudioSourceSelectionFixture(t *testing.T, configuration []byte) (builderAudioSourceSelectionFixture, dtobuild.StoreUserReader, playback.LanguageNormalizer) {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open audio source selection SQLite store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close audio source selection SQLite store: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	itemID, userID := builderUUID(t, 240), builderUUID(t, 241)
	name, path, container := "Selection track", "/library/selection-track.flac", "flac"
	stored := item.Item{ID: itemID, Kind: item.KindAudio, Name: &name, Path: &path, Container: &container}
	if err := items.Upsert(t.Context(), stored); err != nil {
		t.Fatalf("upsert audio source selection fixture: %v", err)
	}
	if err := items.ReplaceStreams(t.Context(), itemID, []playback.MediaStream{
		{Index: 1, Type: playback.StreamTypeAudio, Codec: "flac", Language: builderString("fre")},
		{Index: 7, Type: playback.StreamTypeAudio, Codec: "flac", Language: builderString("eng"), IsDefault: true},
	}); err != nil {
		t.Fatalf("seed audio source selection streams: %v", err)
	}
	loaded, err := items.Get(t.Context(), itemID)
	if err != nil {
		t.Fatalf("load audio source selection fixture: %v", err)
	}
	keys := item.UserDataKeys(loaded, item.UserDataInputs{})
	if len(keys) == 0 {
		t.Fatal("audio source selection fixture has no user-data keys")
	}
	identity := store.NewIdentity(stores)
	if _, err := identity.CreateUser(t.Context(), store.User{
		ID:             userID,
		Name:           "audio source selection user",
		NormalizedName: "audio source selection user",
		Configuration:  configuration,
	}); err != nil {
		t.Fatalf("create audio source selection user: %v", err)
	}
	languages, err := localization.NewService()
	if err != nil {
		t.Fatalf("create audio source selection language normalizer: %v", err)
	}
	return builderAudioSourceSelectionFixture{items: items, item: loaded, userID: userID, key: keys[0], sourceID: itemID.String()}, user.New(identity, clock.NewFake(time.Unix(0, 0).UTC()), bytes.NewReader(nil)), languages
}

func (fixture builderAudioSourceSelectionFixture) upsertUserData(t *testing.T, audio *int) {
	t.Helper()
	if err := fixture.items.UpsertUserData(t.Context(), store.UserData{
		UserID:           fixture.userID,
		CustomKey:        fixture.key,
		ItemID:           fixture.item.ID,
		AudioStreamIndex: audio,
	}); err != nil {
		t.Fatalf("upsert audio source selection user data: %v", err)
	}
}

func assertBuilderSourceSelections(t *testing.T, sourceID string, source dto.MediaSourceInfo, want builderSourceSelection) {
	t.Helper()
	if source.DefaultAudioStreamIndex == nil || *source.DefaultAudioStreamIndex != want.audio {
		t.Errorf("source %s DefaultAudioStreamIndex = %v, want %d", sourceID, builderSourceSelectionIndex(source.DefaultAudioStreamIndex), want.audio)
	}
	if source.DefaultSubtitleStreamIndex == nil || *source.DefaultSubtitleStreamIndex != want.subtitle {
		t.Errorf("source %s DefaultSubtitleStreamIndex = %v, want %d", sourceID, builderSourceSelectionIndex(source.DefaultSubtitleStreamIndex), want.subtitle)
	}
}

type builderSourceFallbackSelection struct {
	audio    *int
	subtitle *int
}

func assertBuilderSourceFallbackSelection(t *testing.T, sourceID string, source dto.MediaSourceInfo, want builderSourceFallbackSelection) {
	t.Helper()
	if !reflect.DeepEqual(source.DefaultAudioStreamIndex, want.audio) {
		t.Errorf("source %s DefaultAudioStreamIndex = %v, want %v", sourceID, builderSourceSelectionIndex(source.DefaultAudioStreamIndex), builderSourceSelectionIndex(want.audio))
	}
	if !reflect.DeepEqual(source.DefaultSubtitleStreamIndex, want.subtitle) {
		t.Errorf("source %s DefaultSubtitleStreamIndex = %v, want %v", sourceID, builderSourceSelectionIndex(source.DefaultSubtitleStreamIndex), builderSourceSelectionIndex(want.subtitle))
	}
}

func builderSourceSelectionIndex(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
