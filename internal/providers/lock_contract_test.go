// SPDX-License-Identifier: GPL-2.0-only

package providers_test

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// Source: Jellyfin v10.10.7, MediaBrowser.Providers/Manager/MetadataService.cs:
// RefreshWithProviders (lines 673-788) skips metadata when IsLocked and skips
// remote metadata after a local LockData result; MergeBaseItemData (lines
// 936-1132) protects each matching MetadataField while allowing unprotected
// fields to merge. Local images run before this metadata path.
func TestRegistryLockedItemRunsOnlyIndependentLocalImages(t *testing.T) {
	var calls []string
	registry := providers.NewRegistry(
		lockProvider{name: "image", stage: providers.ProviderStageLocalImages, refresh: func(target *item.Item) {
			calls = append(calls, "image")
			target.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/media/poster.jpg"}}
		}},
		lockProvider{name: "local", stage: providers.ProviderStageLocalMetadata, refresh: func(target *item.Item) {
			calls = append(calls, "local")
			target.Name = lockString("must not persist")
		}},
		lockProvider{name: "remote", stage: providers.ProviderStageRemoteMetadata, refresh: func(target *item.Item) {
			calls = append(calls, "remote")
			target.Name = lockString("must not persist")
		}},
	)
	target := item.Item{IsLocked: true, Name: lockString("locked title")}
	if err := registry.Refresh(t.Context(), library.RefreshContext{Options: library.LibraryOptions{EnableInternetProviders: true}}, &target); err != nil {
		t.Fatalf("refresh locked item: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"image"}) || target.Name == nil || *target.Name != "locked title" || !reflect.DeepEqual(target.ImageInfos, []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/media/poster.jpg"}}) {
		t.Fatalf("locked registry result calls=%v item=%#v, want local image only and unchanged metadata", calls, target)
	}
}

func TestNFOProviderProtectsOnlyLockedMetadataFields(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		field item.MetadataField
	}{
		{name: "name", field: item.MetadataFieldName},
		{name: "overview", field: item.MetadataFieldOverview},
		{name: "runtime", field: item.MetadataFieldRuntime},
		{name: "official rating", field: item.MetadataFieldOfficialRating},
		{name: "genres", field: item.MetadataFieldGenres},
		{name: "studios", field: item.MetadataFieldStudios},
		{name: "tags", field: item.MetadataFieldTags},
		{name: "people", field: item.MetadataFieldCast},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			provider := lockNFOProvider(`<movie><title>NFO title</title><plot>NFO overview</plot><runtime>120</runtime><mpaa>PG-13</mpaa><genre>NFO genre</genre><studio>NFO studio</studio><tag>NFO tag</tag><actor><name>NFO actor</name></actor><year>2024</year></movie>`)
			target := lockMetadataFixture()
			target.LockedFields = []item.MetadataField{testCase.field}
			handled, err := provider.Refresh(t.Context(), lockRefreshContext(), &target)
			if err != nil || !handled {
				t.Fatalf("refresh locked %s field: handled=%t err=%v", testCase.name, handled, err)
			}
			assertLockedFieldPreserved(t, target, testCase.field)
			assertUnlockedNFOFieldsApplied(t, target, testCase.field)
		})
	}
}

func TestRegistryHonorsNFOResultLocksBeforeRemoteMetadata(t *testing.T) {
	t.Run("LockData stops later metadata but preserves local images", func(t *testing.T) {
		remoteCalled := false
		registry := providers.NewRegistry(
			providers.NewLocalImageProvider(),
			lockNFOProvider(`<movie><title>NFO locked title</title><genre>NFO genre</genre><lockdata>true</lockdata><lockedfields>Name|Genres</lockedfields></movie>`),
			lockProvider{name: "remote", stage: providers.ProviderStageRemoteMetadata, refresh: func(target *item.Item) {
				remoteCalled = true
				target.Name = lockString("remote title")
				target.Values.Genres = []string{"remote genre"}
			}},
		)
		target := lockCandidate()
		if err := registry.Refresh(t.Context(), lockRefreshContext(), &target); err != nil {
			t.Fatalf("refresh NFO LockData: %v", err)
		}
		if remoteCalled || !target.IsLocked || !reflect.DeepEqual(target.LockedFields, []item.MetadataField{item.MetadataFieldName, item.MetadataFieldGenres}) || target.Name == nil || *target.Name != "NFO locked title" || !reflect.DeepEqual(target.Values.Genres, []string{"NFO genre"}) || !lockHasImage(target.ImageInfos, "/media/poster.jpg") {
			t.Fatalf("NFO LockData result = %#v remoteCalled=%t, want locked NFO metadata, no remote mutation, and local artwork", target, remoteCalled)
		}
	})

	t.Run("LockedFields shield mapped fields while later metadata enriches unlocked fields", func(t *testing.T) {
		remoteCalled := false
		registry := providers.NewRegistry(
			lockNFOProvider(`<movie><title>NFO locked-fields title</title><genre>NFO genre</genre><lockedfields>Name|Genres</lockedfields></movie>`),
			lockProvider{name: "remote", stage: providers.ProviderStageRemoteMetadata, refresh: func(target *item.Item) {
				remoteCalled = true
				target.Name = lockString("remote title")
				target.Values.Genres = []string{"remote genre"}
				target.Overview = lockString("remote overview")
			}},
		)
		target := lockCandidate()
		if err := registry.Refresh(t.Context(), lockRefreshContext(), &target); err != nil {
			t.Fatalf("refresh NFO LockedFields: %v", err)
		}
		if !remoteCalled || target.Name == nil || *target.Name != "NFO locked-fields title" || !reflect.DeepEqual(target.Values.Genres, []string{"NFO genre"}) || target.Overview == nil || *target.Overview != "remote overview" {
			t.Fatalf("NFO LockedFields result = %#v remoteCalled=%t, want locked Name/Genres and remote unlocked Overview", target, remoteCalled)
		}
	})

	t.Run("NFO locks union existing fields before later provider selection", func(t *testing.T) {
		remoteCalled := false
		registry := providers.NewRegistry(
			lockNFOProvider(`<movie><title>must retain existing title</title><lockdata>true</lockdata><lockedfields>Genres</lockedfields></movie>`),
			lockProvider{name: "remote", stage: providers.ProviderStageRemoteMetadata, refresh: func(*item.Item) { remoteCalled = true }},
		)
		target := lockCandidate()
		target.LockedFields = []item.MetadataField{item.MetadataFieldName}
		if err := registry.Refresh(t.Context(), lockRefreshContext(), &target); err != nil {
			t.Fatalf("refresh NFO lock union: %v", err)
		}
		if remoteCalled || !target.IsLocked || target.Name == nil || *target.Name != "resolver title" || !reflect.DeepEqual(target.LockedFields, []item.MetadataField{item.MetadataFieldName, item.MetadataFieldGenres}) {
			t.Fatalf("NFO lock union result = %#v remoteCalled=%t, want existing Name plus new Genres lock and no remote metadata", target, remoteCalled)
		}
	})
}

func lockNFOProvider(document string) providers.Provider {
	return providers.NewNFOProvider(providers.NFOProviderOptions{
		Open: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(document)), nil
		},
	})
}

func lockRefreshContext() library.RefreshContext {
	return library.RefreshContext{
		Directory: library.Entry{Path: "/media", IsDir: true},
		Entries: []library.Entry{
			{Path: "/media/movie.mkv", Size: 1},
			{Path: "/media/movie.nfo", Size: 1},
			{Path: "/media/poster.jpg", Size: 1},
		},
		Mode:    library.RefreshModeDefault,
		Options: library.LibraryOptions{EnableInternetProviders: true},
	}
}

func lockCandidate() item.Item {
	return item.Item{Kind: item.KindMovie, Path: lockString("/media/movie.mkv"), Name: lockString("resolver title")}
}

func lockMetadataFixture() item.Item {
	runtime := int64(1)
	return item.Item{
		Kind:           item.KindMovie,
		Path:           lockString("/media/movie.mkv"),
		Name:           lockString("existing title"),
		Overview:       lockString("existing overview"),
		RunTimeTicks:   &runtime,
		OfficialRating: lockString("existing rating"),
		ProductionYear: lockInt(2000),
		Values:         item.ItemValues{Genres: []string{"existing genre"}, Studios: []string{"existing studio"}, Tags: []string{"existing tag"}},
		People:         []item.PersonCredit{{Name: "existing person", Type: item.PersonKindActor}},
	}
}

func assertLockedFieldPreserved(t *testing.T, target item.Item, field item.MetadataField) {
	t.Helper()
	switch field {
	case item.MetadataFieldName:
		if target.Name == nil || *target.Name != "existing title" {
			t.Fatalf("locked Name = %#v, want existing title", target.Name)
		}
	case item.MetadataFieldOverview:
		if target.Overview == nil || *target.Overview != "existing overview" {
			t.Fatalf("locked Overview = %#v, want existing overview", target.Overview)
		}
	case item.MetadataFieldRuntime:
		if target.RunTimeTicks == nil || *target.RunTimeTicks != 1 {
			t.Fatalf("locked Runtime = %#v, want 1", target.RunTimeTicks)
		}
	case item.MetadataFieldOfficialRating:
		if target.OfficialRating == nil || *target.OfficialRating != "existing rating" {
			t.Fatalf("locked OfficialRating = %#v, want existing rating", target.OfficialRating)
		}
	case item.MetadataFieldGenres:
		if !reflect.DeepEqual(target.Values.Genres, []string{"existing genre"}) {
			t.Fatalf("locked Genres = %#v, want existing", target.Values.Genres)
		}
	case item.MetadataFieldStudios:
		if !reflect.DeepEqual(target.Values.Studios, []string{"existing studio"}) {
			t.Fatalf("locked Studios = %#v, want existing", target.Values.Studios)
		}
	case item.MetadataFieldTags:
		if !reflect.DeepEqual(target.Values.Tags, []string{"existing tag"}) {
			t.Fatalf("locked Tags = %#v, want existing", target.Values.Tags)
		}
	case item.MetadataFieldCast:
		if len(target.People) != 1 || target.People[0].Name != "existing person" {
			t.Fatalf("locked People = %#v, want existing", target.People)
		}
	}
}

func assertUnlockedNFOFieldsApplied(t *testing.T, target item.Item, locked item.MetadataField) {
	t.Helper()
	if locked != item.MetadataFieldName && (target.Name == nil || *target.Name != "NFO title") {
		t.Fatalf("unlocked Name = %#v, want NFO title", target.Name)
	}
	if locked != item.MetadataFieldOverview && (target.Overview == nil || *target.Overview != "NFO overview") {
		t.Fatalf("unlocked Overview = %#v, want NFO overview", target.Overview)
	}
	if locked != item.MetadataFieldRuntime && (target.RunTimeTicks == nil || *target.RunTimeTicks != 72_000_000_000) {
		t.Fatalf("unlocked Runtime = %#v, want 120 minutes", target.RunTimeTicks)
	}
	if locked != item.MetadataFieldOfficialRating && (target.OfficialRating == nil || *target.OfficialRating != "PG-13") {
		t.Fatalf("unlocked OfficialRating = %#v, want PG-13", target.OfficialRating)
	}
	if locked != item.MetadataFieldGenres && !reflect.DeepEqual(target.Values.Genres, []string{"NFO genre"}) {
		t.Fatalf("unlocked Genres = %#v, want NFO", target.Values.Genres)
	}
	if locked != item.MetadataFieldStudios && !reflect.DeepEqual(target.Values.Studios, []string{"NFO studio"}) {
		t.Fatalf("unlocked Studios = %#v, want NFO", target.Values.Studios)
	}
	if locked != item.MetadataFieldTags && !reflect.DeepEqual(target.Values.Tags, []string{"NFO tag"}) {
		t.Fatalf("unlocked Tags = %#v, want NFO", target.Values.Tags)
	}
	if locked != item.MetadataFieldCast && (len(target.People) != 1 || target.People[0].Name != "NFO actor") {
		t.Fatalf("unlocked People = %#v, want NFO actor", target.People)
	}
	if target.ProductionYear == nil || *target.ProductionYear != 2024 {
		t.Fatalf("unlocked ProductionYear = %#v, want 2024", target.ProductionYear)
	}
}

func lockHasImage(images []item.ImageInfo, path string) bool {
	for _, image := range images {
		if image.Type == item.ImageTypePrimary && image.Path == path {
			return true
		}
	}
	return false
}

func lockString(value string) *string { return &value }
func lockInt(value int) *int          { return &value }

type lockProvider struct {
	name    string
	stage   providers.ProviderStage
	refresh func(*item.Item)
}

func (provider lockProvider) Name() string                   { return provider.name }
func (provider lockProvider) Stage() providers.ProviderStage { return provider.stage }
func (provider lockProvider) Refresh(_ context.Context, _ library.RefreshContext, target *item.Item) (bool, error) {
	provider.refresh(target)
	return true, nil
}
