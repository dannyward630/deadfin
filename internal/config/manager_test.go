// SPDX-License-Identifier: GPL-2.0-only

package config_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/events"
)

// Source: upstream/jellyfin/MediaBrowser.Model/Configuration/
// BaseApplicationConfiguration.cs:16-19 and ServerConfiguration.cs:20-291.
// Every default matches the upstream constructor and field initializers.
func TestServerConfigurationFreshValuesMatchUpstreamCompatibilityDefaults(t *testing.T) {
	got := config.DefaultServerConfiguration()
	activityLogRetention := 30
	want := config.ServerConfiguration{
		LogFileRetentionDays:               3,
		EnableNormalizedItemByNameIds:      true,
		QuickConnectAvailable:              true,
		EnableCaseSensitiveItemIds:         true,
		DisableLiveTvChannelUserDataName:   true,
		PreferredMetadataLanguage:          "en",
		MetadataCountryCode:                "US",
		SortReplaceCharacters:              []string{".", "+", "%"},
		SortRemoveCharacters:               []string{",", "&", "-", "{", "}", "'"},
		SortRemoveWords:                    []string{"the", "a", "an"},
		MinResumePct:                       5,
		MaxResumePct:                       90,
		MinResumeDurationSeconds:           300,
		MinAudiobookResume:                 5,
		MaxAudiobookResume:                 5,
		LibraryMonitorDelay:                60,
		LibraryUpdateDuration:              30,
		CacheSize:                          runtime.NumCPU() * 100,
		ImageSavingConvention:              config.ImageSavingConventionLegacy,
		MetadataOptions:                    config.DefaultServerConfiguration().MetadataOptions,
		SkipDeserializationForBasicTypes:   true,
		UICulture:                          "en-US",
		DisplaySpecialsWithinSeasons:       true,
		EnableExternalContentInSuggestions: true,
		EnableSlowResponseWarning:          true,
		SlowResponseThresholdMs:            500,
		CorsHosts:                          []string{"*"},
		ActivityLogRetentionDays:           &activityLogRetention,
		AllowClientLogUpload:               true,
		ChapterImageResolution:             config.ImageResolutionMatchSource,
		CastReceiverApplications: []config.CastReceiverApplication{
			{ID: "F007D354", Name: "Stable"},
			{ID: "6F511C87", Name: "Unstable"},
		},
		ContentTypes:       []config.NameValuePair{},
		CodecsUsed:         []string{},
		PluginRepositories: []config.RepositoryInfo{},
		PathSubstitutions:  []config.PathSubstitution{},
		TrickplayOptions:   config.DefaultTrickplayOptions(),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultServerConfiguration() = %#v, want %#v", got, want)
	}
}

// Source: upstream/jellyfin/MediaBrowser.Model/Configuration/ServerConfiguration.cs.
// The ordered list is persisted in system.xml and is exposed by SystemInfo.
func TestDefaultServerConfigurationUsesOrderedUpstreamCastReceiverApplications(t *testing.T) {
	got := config.DefaultServerConfiguration().CastReceiverApplications
	want := []config.CastReceiverApplication{
		{ID: "F007D354", Name: "Stable"},
		{ID: "6F511C87", Name: "Unstable"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultServerConfiguration().CastReceiverApplications = %#v, want ordered upstream values %#v", got, want)
	}
}

func TestManagerCreatesMissingSystemConfigurationAndRestoresItOnReopen(t *testing.T) {
	paths := mustPaths(t)
	changes := newChangeBus(t)

	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("Open(missing system.xml): %v", err)
	}
	if got, want := manager.System(), config.DefaultServerConfiguration(); !reflect.DeepEqual(got, want) {
		t.Fatalf("System() after opening missing system.xml = %#v, want %#v", got, want)
	}

	if _, err := os.Stat(filepath.Join(paths.Config, "system.xml")); err != nil {
		t.Fatalf("missing system.xml was not created: %v", err)
	}
	if _, err := config.Open(t.Context(), paths, changes); err != nil {
		t.Fatalf("Open after creating system.xml: %v", err)
	}
}

func TestManagerLoadsSourceXMLAndPersistsEverySupportedSystemField(t *testing.T) {
	paths := mustPaths(t)
	if err := os.MkdirAll(paths.Config, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	const source = `<?xml version="1.0" encoding="utf-8"?>
<ServerConfiguration>
  <IsStartupWizardCompleted>true</IsStartupWizardCompleted>
  <ServerName>Imported Jellyfin</ServerName>
  <UICulture>fr-FR</UICulture>
  <MetadataCountryCode>CA</MetadataCountryCode>
  <PreferredMetadataLanguage>fr</PreferredMetadataLanguage>
  <LibraryUpdateDuration>17</LibraryUpdateDuration>
  <MinResumePct>17</MinResumePct>
  <MaxResumePct>83</MaxResumePct>
  <MinResumeDurationSeconds>765</MinResumeDurationSeconds>
  <MinAudiobookResume>9</MinAudiobookResume>
  <MaxAudiobookResume>3</MaxAudiobookResume>
  <InactiveSessionThreshold>9</InactiveSessionThreshold>
  <EnableLegacyAuthorization>true</EnableLegacyAuthorization>
  <QuickConnectAvailable>false</QuickConnectAvailable>
  <RemoteClientBitrateLimit>34000000</RemoteClientBitrateLimit>
  <DisplaySpecialsWithinSeasons>true</DisplaySpecialsWithinSeasons>
  <CastReceiverApplications>
    <CastReceiverApplication>
      <Id>F007D354</Id>
      <Name>Stable</Name>
    </CastReceiverApplication>
    <CastReceiverApplication>
      <Id>6F511C87</Id>
      <Name>Unstable</Name>
    </CastReceiverApplication>
  </CastReceiverApplications>
</ServerConfiguration>`
	if err := os.WriteFile(filepath.Join(paths.Config, "system.xml"), []byte(source), 0o600); err != nil {
		t.Fatalf("write source system.xml: %v", err)
	}

	changes := newChangeBus(t)
	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("Open(source system.xml): %v", err)
	}
	want := config.DefaultServerConfiguration()
	want.IsStartupWizardCompleted = true
	want.ServerName = "Imported Jellyfin"
	want.UICulture = "fr-FR"
	want.MetadataCountryCode = "CA"
	want.PreferredMetadataLanguage = "fr"
	want.LibraryUpdateDuration = 17
	want.MinResumePct = 17
	want.MaxResumePct = 83
	want.MinResumeDurationSeconds = 765
	want.MinAudiobookResume = 9
	want.MaxAudiobookResume = 3
	want.InactiveSessionThreshold = 9
	want.EnableLegacyAuthorization = true
	want.QuickConnectAvailable = false
	want.RemoteClientBitrateLimit = 34000000
	want.DisplaySpecialsWithinSeasons = true
	want.CastReceiverApplications = []config.CastReceiverApplication{
		{ID: "F007D354", Name: "Stable"},
		{ID: "6F511C87", Name: "Unstable"},
	}
	if got := manager.System(); !reflect.DeepEqual(got, want) {
		t.Fatalf("System() loaded from source XML = %#v, want %#v", got, want)
	}

	want.ServerName = "Saved Jellyfin"
	want.UICulture = "de-DE"
	want.MetadataCountryCode = "DE"
	want.PreferredMetadataLanguage = "de"
	want.LibraryUpdateDuration = 31
	want.MinResumePct = 0
	want.MaxResumePct = 0
	want.MinResumeDurationSeconds = 0
	want.MinAudiobookResume = 0
	want.MaxAudiobookResume = 0
	want.InactiveSessionThreshold = 4
	want.EnableLegacyAuthorization = false
	want.QuickConnectAvailable = true
	want.RemoteClientBitrateLimit = 54000000
	want.DisplaySpecialsWithinSeasons = false
	want.IsStartupWizardCompleted = false
	if err := manager.UpdateSystem(t.Context(), func(system *config.ServerConfiguration) error {
		*system = want
		return nil
	}); err != nil {
		t.Fatalf("UpdateSystem: %v", err)
	}
	assertXMLDocumentIdentity(t, filepath.Join(paths.Config, "system.xml"), "ServerConfiguration", []string{
		"LogFileRetentionDays", "IsStartupWizardCompleted",
		"EnableMetrics", "EnableNormalizedItemByNameIds", "IsPortAuthorized",
		"QuickConnectAvailable", "EnableCaseSensitiveItemIds",
		"DisableLiveTvChannelUserDataName", "MetadataPath",
		"PreferredMetadataLanguage", "MetadataCountryCode",
		"SortReplaceCharacters", "SortRemoveCharacters", "SortRemoveWords",
		"MinResumePct", "MaxResumePct", "MinResumeDurationSeconds",
		"MinAudiobookResume", "MaxAudiobookResume", "InactiveSessionThreshold",
		"LibraryMonitorDelay", "LibraryUpdateDuration", "CacheSize",
		"ImageSavingConvention", "MetadataOptions",
		"SkipDeserializationForBasicTypes", "ServerName", "UICulture",
		"SaveMetadataHidden", "ContentTypes", "RemoteClientBitrateLimit",
		"EnableFolderView", "EnableGroupingMoviesIntoCollections",
		"EnableGroupingShowsIntoCollections", "DisplaySpecialsWithinSeasons",
		"CodecsUsed", "PluginRepositories", "EnableExternalContentInSuggestions",
		"ImageExtractionTimeoutMs", "PathSubstitutions",
		"EnableSlowResponseWarning", "SlowResponseThresholdMs", "CorsHosts",
		"ActivityLogRetentionDays", "LibraryScanFanoutConcurrency",
		"LibraryMetadataRefreshConcurrency", "AllowClientLogUpload",
		"DummyChapterDuration", "ChapterImageResolution",
		"ParallelImageEncodingLimit", "CastReceiverApplications",
		"TrickplayOptions", "EnableLegacyAuthorization",
	})

	reopened, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("reopen saved system.xml: %v", err)
	}
	if got := reopened.System(); !reflect.DeepEqual(got, want) {
		t.Fatalf("System() after reopen = %#v, want %#v", got, want)
	}
}

func TestManagerCorruptSystemXMLFallsBackAndBacksUpOriginalBytes(t *testing.T) {
	paths := mustPaths(t)
	if err := os.MkdirAll(paths.Config, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	const corrupt = `<ServerConfiguration><ServerName>truncated`
	systemPath := filepath.Join(paths.Config, "system.xml")
	if err := os.WriteFile(systemPath, []byte(corrupt), 0o600); err != nil {
		t.Fatalf("write corrupt system.xml: %v", err)
	}

	changes := newChangeBus(t)
	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("Open(corrupt system.xml): %v", err)
	}
	if got, want := manager.System(), config.DefaultServerConfiguration(); !reflect.DeepEqual(got, want) {
		t.Fatalf("System() after corrupt XML = %#v, want fallback %#v", got, want)
	}
	backup, err := os.ReadFile(systemPath + ".bak")
	if err != nil {
		t.Fatalf("read corrupt-system backup: %v", err)
	}
	if got := string(backup); got != corrupt {
		t.Fatalf("corrupt-system backup = %q, want original bytes %q", got, corrupt)
	}

	reopened, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("Open(canonical fallback system.xml): %v", err)
	}
	if got, want := reopened.System(), config.DefaultServerConfiguration(); !reflect.DeepEqual(got, want) {
		t.Fatalf("System() after corrupt fallback rewrite = %#v, want %#v", got, want)
	}
}

func TestManagerFailedDurableSavePreservesLiveSystemAndEmitsNoChange(t *testing.T) {
	paths := mustPaths(t)
	changes := newChangeBus(t)
	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	subscription := changes.Subscribe(events.SubscriptionOptions{Buffer: 1})
	before := manager.System()

	if err := os.Remove(filepath.Join(paths.Config, "system.xml")); err != nil {
		t.Fatalf("remove system.xml before blocking config directory: %v", err)
	}
	if err := os.Remove(paths.Config); err != nil {
		t.Fatalf("remove config directory before blocking writes: %v", err)
	}
	if err := os.WriteFile(paths.Config, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("replace config directory with file: %v", err)
	}

	err = manager.UpdateSystem(t.Context(), func(system *config.ServerConfiguration) error {
		system.ServerName = "must-not-publish"
		return nil
	})
	if err == nil {
		t.Fatal("UpdateSystem with a non-directory config parent = nil, want durable-write error")
	}
	if got := manager.System(); !reflect.DeepEqual(got, before) {
		t.Fatalf("System() after failed durable save = %#v, want unchanged %#v", got, before)
	}
	mustNotReceiveChange(t, subscription.Events())
}

func TestManagerSerializesConcurrentUpdatesAndPublishesOneChangePerSave(t *testing.T) {
	paths := mustPaths(t)
	changes := newChangeBus(t)
	manager, err := config.Open(t.Context(), paths, changes)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const workers = 16
	subscription := changes.Subscribe(events.SubscriptionOptions{Buffer: workers})
	before := manager.System().LibraryUpdateDuration
	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(workers)
	done.Add(workers)
	errs := make(chan error, workers)
	for range workers {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			errs <- manager.UpdateSystem(t.Context(), func(system *config.ServerConfiguration) error {
				system.LibraryUpdateDuration++
				return nil
			})
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent UpdateSystem: %v", err)
		}
	}

	if got, want := manager.System().LibraryUpdateDuration, before+workers; got != want {
		t.Fatalf("LibraryUpdateDuration after %d concurrent updates = %d, want %d", workers, got, want)
	}
	for range workers {
		if got := receiveChange(t, subscription.Events()); got != (config.Change{File: "system.xml"}) {
			t.Fatalf("configuration change = %#v, want Change{File: %q}", got, "system.xml")
		}
	}
	mustNotReceiveChange(t, subscription.Events())
}

func mustPaths(t *testing.T) config.Paths {
	t.Helper()
	paths, err := config.NewPaths(filepath.Join(t.TempDir(), "program-data"))
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	return paths
}

func newChangeBus(t *testing.T) *events.Bus[config.Change] {
	t.Helper()
	bus := events.NewBus[config.Change](clock.NewFake(time.Unix(0, 0).UTC()))
	t.Cleanup(func() {
		if err := bus.Close(context.Background()); err != nil {
			t.Errorf("close changes bus: %v", err)
		}
	})
	return bus
}

func receiveChange(t *testing.T, changes <-chan config.Change) config.Change {
	t.Helper()
	select {
	case change := <-changes:
		return change
	default:
		t.Fatal("configuration change was not published")
		return config.Change{}
	}
}

func mustNotReceiveChange(t *testing.T, changes <-chan config.Change) {
	t.Helper()
	select {
	case change := <-changes:
		t.Fatalf("unexpected configuration change %#v", change)
	default:
	}
}
