// SPDX-License-Identifier: GPL-2.0-only

package trickplay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/rootfs"
)

const (
	generatorTerminalInitialRollbackCrashRootEnvironment  = "DEADFIN_GENERATOR_TERMINAL_INITIAL_ROLLBACK_CRASH_ROOT"
	generatorTerminalRecoveryRollbackCrashRootEnvironment = "DEADFIN_GENERATOR_TERMINAL_RECOVERY_ROLLBACK_CRASH_ROOT"
	generatorTerminalRollbackCrashRootEnvironment         = "DEADFIN_GENERATOR_TERMINAL_ROLLBACK_CRASH_ROOT"
)

// TestGeneratorCloseReleasesThePinnedRootDescriptor verifies that an opened
// generator owns a live descriptor for its root until its idempotent Close.
func TestGeneratorCloseReleasesThePinnedRootDescriptor(t *testing.T) {
	root := t.TempDir()
	store := &exchangeTestStore{}
	generator, err := NewGenerator(GeneratorConfig{
		Root: root, Extractor: exchangeTestExtractor{}, Composer: exchangeTestComposer{}, Store: store, Reader: store, Publication: NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewGenerator(): %v", err)
	}
	if _, err := generator.root.Stat(); err != nil {
		t.Fatalf("pinned root descriptor before Close: %v", err)
	}
	if err := generator.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if err := generator.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
	if _, err := generator.root.Stat(); err == nil {
		t.Fatal("pinned root descriptor remained usable after Close")
	}
}

// TestGeneratorCloseWaitsForActiveGenerateAndRejectsLaterOperations verifies
// that Close cannot release the root descriptor underneath a running Generate,
// and that the closed generator has no later filesystem surface.
func TestGeneratorCloseWaitsForActiveGenerateAndRejectsLaterOperations(t *testing.T) {
	root := t.TempDir()
	extractionStarted := make(chan struct{})
	releaseExtraction := make(chan struct{})
	store := &exchangeTestStore{}
	generator, err := NewGenerator(GeneratorConfig{
		Root: root, Extractor: generatorTerminalCloseExtractor{started: extractionStarted, release: releaseExtraction}, Composer: exchangeTestComposer{}, Store: store, Reader: store, Publication: NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewGenerator(): %v", err)
	}
	itemID := exchangeTestUUID(t, "deaf0002112233445566778899aabbcc")
	generation := make(chan error, 1)
	go func() {
		_, err := generator.Generate(t.Context(), generatorTerminalRequest(t, itemID))
		generation <- err
	}()
	<-extractionStarted

	closed := make(chan error, 1)
	go func() { closed <- generator.Close() }()
	generatorTerminalWaitForCloseLease(t)
	select {
	case err := <-closed:
		t.Fatalf("Close() returned %v while active Generate still held the lifecycle lease", err)
	default:
	}
	close(releaseExtraction)
	if err := <-generation; err != nil {
		t.Fatalf("Generate(active during Close): %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("Close(after active Generate): %v", err)
	}

	before := generatorTerminalTree(t, root)
	if _, err := generator.Generate(t.Context(), generatorTerminalRequest(t, itemID)); !errors.Is(err, ErrGeneratorClosed) {
		t.Errorf("Generate() after Close error = %v, want ErrGeneratorClosed", err)
	}
	if err := generator.Recover(t.Context()); !errors.Is(err, ErrGeneratorClosed) {
		t.Errorf("Recover() after Close error = %v, want ErrGeneratorClosed", err)
	}
	if got := generatorTerminalTree(t, root); !reflect.DeepEqual(got, before) {
		t.Fatalf("root tree after closed Generate and Recover = %#v, want unchanged %#v", got, before)
	}
}

// TestGeneratorRejectsQueuedGenerateAfterRootPathnameIdentityReplacement proves
// that a caller which passed root validation before waiting for serialization
// cannot publish through a subsequently replaced root pathname.
func TestGeneratorRejectsQueuedGenerateAfterRootPathnameIdentityReplacement(t *testing.T) {
	root := t.TempDir()
	store := &exchangeTestStore{}
	generator, err := NewGenerator(GeneratorConfig{
		Root: root, Extractor: exchangeTestExtractor{}, Composer: exchangeTestComposer{}, Store: store, Reader: store, Publication: NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewGenerator(): %v", err)
	}

	// Hold the real serialization lease. The gateable context stops the queued
	// call while Generate evaluates Context.Done for its lease-acquisition
	// select: after initial root validation and before it can acquire the lease.
	generator.oneAtATime <- struct{}{}
	leaseHeld := true
	t.Cleanup(func() {
		if leaseHeld {
			<-generator.oneAtATime
		}
	})
	passedValidation := make(chan struct{})
	releaseQueuedCall := make(chan struct{})
	queuedContext := &generatorTerminalPostValidationContext{
		Context: t.Context(), entered: passedValidation, release: releaseQueuedCall,
	}
	itemID := exchangeTestUUID(t, "deaf0000112233445566778899aabbcc")
	result := make(chan error, 1)
	go func() {
		_, err := generator.Generate(queuedContext, exchangeTestRequest(t, itemID))
		result <- err
	}()
	<-passedValidation

	originalRoot := generatorTerminalReplaceRootPathname(t, root)
	close(releaseQueuedCall)
	<-generator.oneAtATime
	leaseHeld = false

	if err := <-result; err == nil {
		t.Error("Generate(queued after root replacement) error = nil, want root identity rejection")
	}
	if got, want := generatorTerminalDirectoryNames(t, root), []string{}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement root artifacts = %#v, want no artifacts outside original physical root %#v", got, want)
	}
	if got, want := generatorTerminalDirectoryNames(t, originalRoot), []string{}; !reflect.DeepEqual(got, want) {
		t.Fatalf("original physical root artifacts = %#v, want queued call never began publication %#v", got, want)
	}
}

// TestGeneratorRejectsQueuedRecoverAfterRootPathnameIdentityReplacement proves
// recovery rechecks the original root identity after waiting for publication
// serialization, before it scans or mutates the current pathname.
func TestGeneratorRejectsQueuedRecoverAfterRootPathnameIdentityReplacement(t *testing.T) {
	root := t.TempDir()
	publication := NewPublicationCoordinator()
	store := &exchangeTestStore{}
	generator, err := NewGenerator(GeneratorConfig{
		Root: root, Extractor: exchangeTestExtractor{}, Composer: exchangeTestComposer{}, Store: store, Reader: store, Publication: publication,
	})
	if err != nil {
		t.Fatalf("NewGenerator(): %v", err)
	}

	publication.Lock()
	leaseHeld := true
	t.Cleanup(func() {
		if leaseHeld {
			publication.Unlock()
		}
	})
	result := make(chan error, 1)
	go func() { result <- generator.Recover(t.Context()) }()
	generatorTerminalWaitForQueuedRecover(t)

	originalRoot := generatorTerminalReplaceRootPathname(t, root)
	publication.Unlock()
	leaseHeld = false

	if err := <-result; err == nil {
		t.Error("Recover(queued after root replacement) error = nil, want root identity rejection")
	}
	if got, want := generatorTerminalDirectoryNames(t, root), []string{}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement root artifacts = %#v, want recovery never scanned or mutated outside original physical root %#v", got, want)
	}
	if got, want := generatorTerminalDirectoryNames(t, originalRoot), []string{}; !reflect.DeepEqual(got, want) {
		t.Fatalf("original physical root artifacts = %#v, want no recovery artifacts %#v", got, want)
	}
}

// TestGeneratorRecoversRollbackCrashBeforeStagedManifestRewrite proves that a
// crash after the old tree has been exchanged back, but before the staged new
// tree receives its identity manifest, still resolves to the old committed
// generation on restart.
func TestGeneratorRecoversRollbackCrashBeforeStagedManifestRewrite(t *testing.T) {
	if root := os.Getenv(generatorTerminalRollbackCrashRootEnvironment); root != "" {
		generatorTerminalRollbackCrashChild(t, root)
		return
	}

	root := t.TempDir()
	itemID := exchangeTestUUID(t, "deaf0001112233445566778899aabbcc")
	baselineStore := newCrashTestStore(root)
	baseline := newCrashTestGenerator(t, root, baselineStore, "old-tile", false)
	oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
	if err != nil {
		t.Fatalf("Generate(baseline): %v", err)
	}
	if oldMetadata.GenerationToken == "" {
		t.Fatal("baseline GenerationToken = empty, want committed generation identity")
	}

	child := exec.Command(os.Args[0], "-test.run=^TestGeneratorRecoversRollbackCrashBeforeStagedManifestRewrite$")
	child.Env = append(os.Environ(), generatorTerminalRollbackCrashRootEnvironment+"="+root)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("rollback-before-manifest crash child exited %v:\n%s", err, output)
	}

	// A fresh store and generator model process restart rather than retaining
	// any in-memory publication state from the failed writer.
	recoveryStore := newCrashTestStore(root)
	recovery := newCrashTestGenerator(t, root, recoveryStore, "ignored", true)
	if err := recovery.Recover(t.Context()); err != nil {
		t.Fatalf("Recover(after rollback-before-manifest crash): %v", err)
	}

	resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(oldMetadata.Width, oldMetadata.TileWidth, oldMetadata.TileHeight))
	if got, want := generatorTerminalTree(t, resolution), map[string]string{"0.jpg": "old-tile"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical tree after rollback-before-manifest crash recovery = %#v, want old committed tree only %#v", got, want)
	}
	persisted, err := recoveryStore.ReadTrickplay(t.Context(), itemID, oldMetadata.Width)
	if err != nil || persisted == nil || *persisted != oldMetadata {
		t.Fatalf("durable metadata after rollback-before-manifest crash recovery = (%#v, %v), want old committed metadata %#v", persisted, err, oldMetadata)
	}
}

func generatorTerminalRollbackCrashChild(t *testing.T, root string) {
	t.Helper()
	itemID := exchangeTestUUID(t, "deaf0001112233445566778899aabbcc")
	store := newCrashTestStore(root)
	store.replaceError = errors.New("synthetic metadata rejection before commit")
	generator := newCrashTestGenerator(t, root, store, crashTestNewTile, false)
	nativeExchange := generator.exchange
	exchanges := 0
	generator.exchange = func(from *rootfs.Dir, fromName string, to *rootfs.Dir, toName string) error {
		if err := nativeExchange(from, fromName, to, toName); err != nil {
			return err
		}
		exchanges++
		if exchanges == 2 {
			// The first exchange publishes the new tree. The second restores the
			// old tree during rollback, so exiting here is precisely before
			// rollbackPublication rewrites the staged generation manifest.
			os.Exit(0)
		}
		return nil
	}
	if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); err != nil {
		t.Fatalf("Generate(rollback crash child): %v", err)
	}
	t.Fatal("Generate(rollback crash child) returned without reaching rollback exchange")
}

// TestGeneratorRecoversInitialRollbackCrashAfterDurableJournal proves that a
// fresh recovery removes a never-committed initial publication when the crash
// lands after its durable rollback journal but before destination removal.
func TestGeneratorRecoversInitialRollbackCrashAfterDurableJournal(t *testing.T) {
	if root := os.Getenv(generatorTerminalInitialRollbackCrashRootEnvironment); root != "" {
		generatorTerminalInitialRollbackCrashChild(t, root)
		return
	}

	root := t.TempDir()
	itemID := exchangeTestUUID(t, "deaf0003112233445566778899aabbcc")
	child := exec.Command(os.Args[0], "-test.run=^TestGeneratorRecoversInitialRollbackCrashAfterDurableJournal$")
	child.Env = append(os.Environ(), generatorTerminalInitialRollbackCrashRootEnvironment+"="+root)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("initial rollback crash child exited %v:\n%s", err, output)
	}

	store := newCrashTestStore(root)
	recovery := newCrashTestGenerator(t, root, store, "ignored", true)
	if err := recovery.Recover(t.Context()); err != nil {
		t.Fatalf("Recover(after initial rollback crash): %v", err)
	}

	resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(318, 3, 2))
	if _, err := os.Lstat(resolution); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical resolution after initial rollback crash recovery Lstat error = %v, want not exist", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(resolution), "."+filepath.Base(resolution)+".journal")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal after initial rollback crash recovery Lstat error = %v, want not exist", err)
	}
	if persisted, err := store.ReadTrickplay(t.Context(), itemID, 318); err != nil || persisted != nil {
		t.Fatalf("durable metadata after initial rollback crash recovery = (%#v, %v), want absent", persisted, err)
	}
	if got := generatorTerminalTree(t, root); len(got) != 0 {
		t.Fatalf("artifact root after initial rollback crash recovery = %#v, want no destination JPEGs, journals, or staging", got)
	}
}

func generatorTerminalInitialRollbackCrashChild(t *testing.T, root string) {
	t.Helper()
	itemID := exchangeTestUUID(t, "deaf0003112233445566778899aabbcc")
	store := newCrashTestStore(root)
	store.replaceError = errors.New("synthetic initial metadata rejection before commit")
	generator := newCrashTestGenerator(t, root, store, crashTestNewTile, false)
	generator.afterRollbackJournalBeforeExchange = func() {
		resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(318, 3, 2))
		journalPath := filepath.Join(filepath.Dir(resolution), "."+filepath.Base(resolution)+".journal")
		raw, err := os.ReadFile(journalPath)
		if err != nil {
			t.Fatalf("ReadFile(durable rollback journal): %v", err)
		}
		var journal publicationJournal
		if err := json.Unmarshal(raw, &journal); err != nil {
			t.Fatalf("Unmarshal(durable rollback journal): %v", err)
		}
		if !journal.RollingBack || journal.Exchanged || !journal.IdentityAtDestination {
			t.Fatalf("durable initial rollback journal = %#v, want RollingBack initial destination identity", journal)
		}
		item, openErr := generator.openItemDir(itemID)
		if openErr != nil {
			t.Fatalf("open initial publication parent: %v", openErr)
		}
		removeErr := generator.withResolutionDir(item, trickplayResolutionName(318, 3, 2), generator.removeGenerationManifest)
		closeErr := item.Close()
		if removeErr != nil || closeErr != nil {
			t.Fatalf("remove initial generation manifest before crash: %v", errors.Join(removeErr, closeErr))
		}
		os.Exit(0)
	}
	if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); err != nil {
		t.Fatalf("Generate(initial rollback crash child): %v", err)
	}
	t.Fatal("Generate(initial rollback crash child) returned without reaching durable rollback journal")
}

// TestGeneratorRetainsRollbackJournalUntilEveryMutatedDirectoryIsSynced
// proves a failed rollback never unlinks its recovery record until both sides
// of the exchanged resolution have reached durable storage.
func TestGeneratorRetainsRollbackJournalUntilEveryMutatedDirectoryIsSynced(t *testing.T) {
	itemID := exchangeTestUUID(t, "deaf0004112233445566778899aabbcc")
	for _, test := range []struct {
		name        string
		isTargetDir func(root, itemParent, path string) bool
	}{
		{
			name: "item parent",
			isTargetDir: func(_ string, itemParent, path string) bool {
				return filepath.Clean(path) == filepath.Clean(itemParent)
			},
		},
		{
			name: "staging parent",
			isTargetDir: func(root, _ string, path string) bool {
				return filepath.Clean(filepath.Dir(path)) == filepath.Clean(root) && strings.HasPrefix(filepath.Base(path), ".trickplay-")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := newCrashTestStore(root)
			baseline := newCrashTestGenerator(t, root, store, "old-tile", false)
			oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
			if err != nil {
				t.Fatalf("Generate(baseline): %v", err)
			}

			store.replaceError = errors.New("synthetic metadata rejection before commit")
			generator := newCrashTestGenerator(t, root, store, crashTestNewTile, false)
			nativeSync := generator.syncDirectory
			rollbackMutated := false
			syncFailures := 0
			injected := errors.New("synthetic rollback directory sync failure")
			itemParent := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed())
			canonicalRoot := exchangeTestCanonicalPath(t, root)
			canonicalItemParent := filepath.Join(canonicalRoot, itemID.Dashed()[:2], itemID.Dashed())
			generator.afterRollbackExchangeBeforeJournalRemoval = func() { rollbackMutated = true }
			generator.syncDirectory = func(dir *rootfs.Dir) error {
				if rollbackMutated && test.isTargetDir(canonicalRoot, canonicalItemParent, dir.Path()) {
					syncFailures++
					return injected
				}
				return nativeSync(dir)
			}
			if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); !errors.Is(err, injected) {
				t.Fatalf("Generate(%s rollback sync failure) error = %v, want injected sync failure", test.name, err)
			}
			if syncFailures != 1 {
				t.Fatalf("%s rollback sync failures = %d, want 1 before journal unlink", test.name, syncFailures)
			}

			resolution := trickplayResolutionName(oldMetadata.Width, oldMetadata.TileWidth, oldMetadata.TileHeight)
			journalPath := filepath.Join(itemParent, "."+resolution+".journal")
			if _, err := os.Lstat(journalPath); err != nil {
				t.Fatalf("rollback journal after %s sync failure Lstat: %v, want retained", test.name, err)
			}
			generatorTerminalAssertOldPublication(t, root, store, itemID, oldMetadata)

			recovery := newCrashTestGenerator(t, root, store, "ignored", true)
			if err := recovery.Recover(t.Context()); err != nil {
				t.Fatalf("Recover(after %s rollback sync failure): %v", test.name, err)
			}
			if _, err := os.Lstat(journalPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("journal after recovery from %s rollback sync failure Lstat error = %v, want not exist", test.name, err)
			}
			generatorTerminalAssertOldPublication(t, root, store, itemID, oldMetadata)
			generatorTerminalAssertNoStaging(t, root)
		})
	}
}

// TestGeneratorRecoveryRetainsJournalWhenStagingRemovalRootSyncFails proves
// recovery does not discard the only crash record after removing a staging
// parent until that root-directory removal is durable and retryable.
func TestGeneratorRecoveryRetainsJournalWhenStagingRemovalRootSyncFails(t *testing.T) {
	if root := os.Getenv(generatorTerminalRecoveryRollbackCrashRootEnvironment); root != "" {
		generatorTerminalRecoveryRollbackCrashChild(t, root)
		return
	}

	root := t.TempDir()
	itemID := exchangeTestUUID(t, "deaf0005112233445566778899aabbcc")
	store := newCrashTestStore(root)
	baseline := newCrashTestGenerator(t, root, store, "old-tile", false)
	oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
	if err != nil {
		t.Fatalf("Generate(baseline): %v", err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestGeneratorRecoveryRetainsJournalWhenStagingRemovalRootSyncFails$")
	child.Env = append(os.Environ(), generatorTerminalRecoveryRollbackCrashRootEnvironment+"="+root)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("recovery rollback crash child exited %v:\n%s", err, output)
	}

	resolution := trickplayResolutionName(oldMetadata.Width, oldMetadata.TileWidth, oldMetadata.TileHeight)
	journalPath := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), "."+resolution+".journal")
	injected := errors.New("synthetic recovery staging-parent root sync failure")
	canonicalRoot := exchangeTestCanonicalPath(t, root)
	recovery := newCrashTestGenerator(t, root, store, "ignored", true)
	nativeSync := recovery.syncDirectory
	syncFailures := 0
	recovery.syncDirectory = func(dir *rootfs.Dir) error {
		if filepath.Clean(dir.Path()) == filepath.Clean(canonicalRoot) {
			syncFailures++
			return injected
		}
		return nativeSync(dir)
	}
	if err := recovery.Recover(t.Context()); !errors.Is(err, injected) {
		t.Fatalf("Recover(staging-parent root sync failure) error = %v, want injected sync failure", err)
	}
	if syncFailures != 1 {
		t.Fatalf("recovery root sync failures = %d, want 1 before journal unlink", syncFailures)
	}
	if _, err := os.Lstat(journalPath); err != nil {
		t.Fatalf("recovery journal after root sync failure Lstat: %v, want retained", err)
	}
	generatorTerminalAssertOldPublication(t, root, store, itemID, oldMetadata)

	retry := newCrashTestGenerator(t, root, store, "ignored", true)
	if err := retry.Recover(t.Context()); err != nil {
		t.Fatalf("Recover(retry after root sync failure): %v", err)
	}
	if _, err := os.Lstat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal after retry recovery Lstat error = %v, want not exist", err)
	}
	generatorTerminalAssertOldPublication(t, root, store, itemID, oldMetadata)
	generatorTerminalAssertNoStaging(t, root)
}

// TestGeneratorRecoverySyncsExchangedParentsBeforeCleanup proves that recovery
// persists both sides of every restoration exchange before it can discard the
// fallback generation. A failed parent sync must retain the journal and both
// generation trees; a later recovery then cleans up in durable order.
func TestGeneratorRecoverySyncsExchangedParentsBeforeCleanup(t *testing.T) {
	itemID := exchangeTestUUID(t, "5566778899aabbccddeeff0011223344")
	for _, restoration := range []struct {
		name                string
		phase               string
		prepare             func(t *testing.T, generator *Generator, itemID dto.UUID, journal publicationJournal)
		wantCanonicalTile   string
		wantStagingTile     string
		wantRecoverySyncs   func(root, resolution string) []string
		assertFinalMetadata func(t *testing.T, store *crashTestStore, itemID dto.UUID, old Metadata)
	}{
		{
			name:              "rollback restoration",
			phase:             "exchange-before-journal",
			wantCanonicalTile: "old-tile",
			wantStagingTile:   crashTestNewTile,
			wantRecoverySyncs: func(root, _ string) []string {
				return []string{root, filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed())}
			},
			assertFinalMetadata: generatorTerminalAssertOldMetadata,
		},
		{
			name:  "committed-new restoration",
			phase: "post-metadata",
			prepare: func(t *testing.T, generator *Generator, itemID dto.UUID, journal publicationJournal) {
				generatorTerminalExchangeRecoveryTrees(t, generator, itemID, journal.StagingName, journal.Resolution)
			},
			wantCanonicalTile: crashTestNewTile,
			wantStagingTile:   "old-tile",
			wantRecoverySyncs: func(root, resolution string) []string {
				return []string{resolution, root, filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed())}
			},
			assertFinalMetadata: generatorTerminalAssertNewMetadata,
		},
	} {
		for _, failedParent := range []struct {
			name  string
			index int
		}{
			{name: "canonical parent", index: 0},
			{name: "staging parent", index: 1},
		} {
			t.Run(restoration.name+"/"+failedParent.name, func(t *testing.T) {
				root := t.TempDir()
				store := newCrashTestStore(root)
				baseline := newCrashTestGenerator(t, root, store, "old-tile", false)
				oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
				if err != nil {
					t.Fatalf("Generate(baseline): %v", err)
				}

				child := exec.Command(os.Args[0], "-test.run=^TestGeneratorRecoversPostExchangeCrashWithDurableMetadata$")
				child.Env = append(os.Environ(), generatorCrashRootEnvironment+"="+root, generatorCrashPhaseEnvironment+"="+restoration.phase)
				if output, err := child.CombinedOutput(); err != nil {
					t.Fatalf("%s crash child exited %v:\n%s", restoration.name, err, output)
				}

				resolutionName := trickplayResolutionName(oldMetadata.Width, oldMetadata.TileWidth, oldMetadata.TileHeight)
				itemParent := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed())
				journalPath := filepath.Join(itemParent, "."+resolutionName+".journal")
				journal := generatorTerminalReadJournal(t, journalPath)
				setup := newCrashTestGenerator(t, root, store, "ignored", true)
				if restoration.prepare != nil {
					restoration.prepare(t, setup, itemID, journal)
				}

				recovery := newCrashTestGenerator(t, root, store, "ignored", true)
				nativeSync := recovery.syncDirectory
				var syncCalls []string
				injected := errors.New("synthetic recovery exchanged-parent sync failure")
				recovery.syncDirectory = func(dir *rootfs.Dir) error {
					syncCalls = append(syncCalls, generatorTerminalSyncPath(dir.Path()))
					if len(syncCalls) == failedParent.index+1 {
						return injected
					}
					return nativeSync(dir)
				}
				if err := recovery.Recover(t.Context()); !errors.Is(err, injected) {
					t.Fatalf("Recover(after %s exchange, %s sync failure) error = %v, want injected error", restoration.name, failedParent.name, err)
				}
				wantExchangeSyncs := []string{generatorTerminalSyncPath(itemParent), generatorTerminalSyncPath(filepath.Join(root, journal.StagingName))}
				if !reflect.DeepEqual(syncCalls, wantExchangeSyncs) {
					t.Fatalf("sync calls through failed post-exchange parent persistence = %#v, want %#v before any cleanup", syncCalls, wantExchangeSyncs)
				}
				generatorTerminalAssertRecoveryCandidates(t, root, itemID, journal, restoration.wantCanonicalTile, restoration.wantStagingTile)

				fresh := newCrashTestGenerator(t, root, store, "ignored", true)
				var freshSyncCalls []string
				freshNativeSync := fresh.syncDirectory
				fresh.syncDirectory = func(dir *rootfs.Dir) error {
					freshSyncCalls = append(freshSyncCalls, generatorTerminalSyncPath(dir.Path()))
					return freshNativeSync(dir)
				}
				if err := fresh.Recover(t.Context()); err != nil {
					t.Fatalf("Recover(after %s exchange %s sync failure): %v", restoration.name, failedParent.name, err)
				}
				resolutionPath := filepath.Join(itemParent, resolutionName)
				wantRecoverySyncs := restoration.wantRecoverySyncs(generatorTerminalSyncPath(root), generatorTerminalSyncPath(resolutionPath))
				if !reflect.DeepEqual(freshSyncCalls, wantRecoverySyncs) {
					t.Fatalf("successful recovery sync order = %#v, want %#v", freshSyncCalls, wantRecoverySyncs)
				}
				if _, err := os.Lstat(journalPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("journal after successful fresh recovery Lstat error = %v, want not exist", err)
				}
				if _, err := os.Lstat(filepath.Join(root, journal.StagingName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("staging after successful fresh recovery Lstat error = %v, want not exist", err)
				}
				if got := generatorTerminalTree(t, resolutionPath)["0.jpg"]; got != restoration.wantCanonicalTile {
					t.Fatalf("canonical tile after successful fresh recovery = %q, want %q", got, restoration.wantCanonicalTile)
				}
				restoration.assertFinalMetadata(t, store, itemID, oldMetadata)

				freshSyncCalls = nil
				if err := fresh.Recover(t.Context()); err != nil {
					t.Fatalf("second Recover(after %s exchange %s sync failure): %v", restoration.name, failedParent.name, err)
				}
				if len(freshSyncCalls) != 0 {
					t.Fatalf("second recovery sync calls = %#v, want no work after cleanup", freshSyncCalls)
				}
			})
		}
	}
}

func generatorTerminalExchangeRecoveryTrees(t *testing.T, generator *Generator, itemID dto.UUID, stagingName, resolution string) {
	t.Helper()
	parent, err := generator.openItemDir(itemID)
	if err != nil {
		t.Fatalf("open recovery item parent: %v", err)
	}
	defer func() {
		if err := parent.Close(); err != nil {
			t.Fatalf("close recovery item parent: %v", err)
		}
	}()
	staging, err := generator.root.OpenDir(stagingName)
	if err != nil {
		t.Fatalf("open recovery staging parent: %v", err)
	}
	defer func() {
		if err := staging.Close(); err != nil {
			t.Fatalf("close recovery staging parent: %v", err)
		}
	}()
	if err := generator.exchange(parent, resolution, staging, resolution); err != nil {
		t.Fatalf("prepare committed recovery exchange: %v", err)
	}
}

func generatorTerminalAssertRecoveryCandidates(t *testing.T, root string, itemID dto.UUID, journal publicationJournal, wantCanonicalTile, wantStagingTile string) {
	t.Helper()
	resolution := journal.Resolution
	canonical := generatorTerminalTree(t, filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), resolution))
	staged := generatorTerminalTree(t, filepath.Join(root, journal.StagingName, resolution))
	if got := canonical["0.jpg"]; got != wantCanonicalTile {
		t.Fatalf("recoverable canonical tile = %q, want %q", got, wantCanonicalTile)
	}
	if got := staged["0.jpg"]; got != wantStagingTile {
		t.Fatalf("recoverable staged tile = %q, want %q", got, wantStagingTile)
	}
	newTree := staged
	if wantCanonicalTile == crashTestNewTile {
		newTree = canonical
	}
	if got := strings.TrimSpace(newTree[generationManifestName]); got != journal.Token {
		t.Fatalf("recoverable new generation manifest = %q, want journal token %q: canonical=%#v staged=%#v", got, journal.Token, canonical, staged)
	}
	if _, err := os.Lstat(filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), "."+resolution+".journal")); err != nil {
		t.Fatalf("journal after failed exchanged-parent sync Lstat: %v, want retained", err)
	}
}

func generatorTerminalSyncPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

func generatorTerminalAssertOldMetadata(t *testing.T, store *crashTestStore, itemID dto.UUID, old Metadata) {
	t.Helper()
	persisted, err := store.ReadTrickplay(t.Context(), itemID, old.Width)
	if err != nil || persisted == nil || *persisted != old {
		t.Fatalf("metadata after rollback restoration = (%#v, %v), want old %#v", persisted, err, old)
	}
}

func generatorTerminalAssertNewMetadata(t *testing.T, store *crashTestStore, itemID dto.UUID, old Metadata) {
	t.Helper()
	persisted, err := store.ReadTrickplay(t.Context(), itemID, old.Width)
	if err != nil || persisted == nil || persisted.GenerationToken == old.GenerationToken {
		t.Fatalf("metadata after committed restoration = (%#v, %v), want persisted replacement distinct from %#v", persisted, err, old)
	}
}

func generatorTerminalRecoveryRollbackCrashChild(t *testing.T, root string) {
	t.Helper()
	itemID := exchangeTestUUID(t, "deaf0005112233445566778899aabbcc")
	store := newCrashTestStore(root)
	store.replaceError = errors.New("synthetic metadata rejection before commit")
	generator := newCrashTestGenerator(t, root, store, crashTestNewTile, false)
	generator.afterRollbackExchangeBeforeJournalRemoval = func() { os.Exit(0) }
	if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); err != nil {
		t.Fatalf("Generate(recovery rollback crash child): %v", err)
	}
	t.Fatal("Generate(recovery rollback crash child) returned without reaching rollback exchange")
}

func generatorTerminalAssertOldPublication(t *testing.T, root string, store *crashTestStore, itemID dto.UUID, oldMetadata Metadata) {
	t.Helper()
	resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(oldMetadata.Width, oldMetadata.TileWidth, oldMetadata.TileHeight))
	if got, want := generatorTerminalTree(t, resolution), map[string]string{"0.jpg": "old-tile"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical tree = %#v, want old committed tree %#v", got, want)
	}
	persisted, err := store.ReadTrickplay(t.Context(), itemID, oldMetadata.Width)
	if err != nil || persisted == nil || *persisted != oldMetadata {
		t.Fatalf("durable metadata = (%#v, %v), want old committed metadata %#v", persisted, err, oldMetadata)
	}
}

func generatorTerminalAssertNoStaging(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(trickplay root): %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".trickplay-") {
			t.Fatalf("trickplay root retained staging entry %q", entry.Name())
		}
	}
}

type generatorTerminalPostValidationContext struct {
	context.Context
	entered chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (context *generatorTerminalPostValidationContext) Done() <-chan struct{} {
	context.once.Do(func() {
		close(context.entered)
		<-context.release
	})
	return context.Context.Done()
}

func generatorTerminalWaitForQueuedRecover(t *testing.T) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	stack := make([]byte, 1<<20)
	for {
		size := runtime.Stack(stack, true)
		waiting := stack[:size]
		if bytes.Contains(waiting, []byte("sync.runtime_SemacquireMutex")) && bytes.Contains(waiting, []byte("(*PublicationCoordinator).Lock")) && bytes.Contains(waiting, []byte("(*Generator).Recover")) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("Recover() did not reach the held publication lease")
		case <-poll.C:
		}
	}
}

func generatorTerminalWaitForCloseLease(t *testing.T) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	stack := make([]byte, 1<<20)
	for {
		size := runtime.Stack(stack, true)
		waiting := stack[:size]
		if bytes.Contains(waiting, []byte("sync.(*RWMutex).Lock")) && bytes.Contains(waiting, []byte("(*Generator).Close")) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("Close() did not wait for the active generator lifecycle lease")
		case <-poll.C:
		}
	}
}

func generatorTerminalRequest(t *testing.T, itemID dto.UUID) GenerateRequest {
	t.Helper()
	request := exchangeTestRequest(t, itemID)
	request.SourceRevision = "terminal-close-source-revision"
	return request
}

type generatorTerminalCloseExtractor struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (extractor generatorTerminalCloseExtractor) Extract(ctx context.Context, request FrameRequest) (FrameExtraction, error) {
	close(extractor.started)
	select {
	case <-extractor.release:
	case <-ctx.Done():
		return FrameExtraction{}, ctx.Err()
	}
	return exchangeTestExtractor{}.Extract(ctx, request)
}

func generatorTerminalReplaceRootPathname(t *testing.T, root string) string {
	t.Helper()
	originalRoot := root + ".original-physical-root"
	if err := os.Rename(root, originalRoot); err != nil {
		t.Fatalf("Rename(original root): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(originalRoot) })
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("Mkdir(replacement root): %v", err)
	}
	originalInfo, err := os.Lstat(originalRoot)
	if err != nil {
		t.Fatalf("Lstat(original physical root): %v", err)
	}
	replacementInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatalf("Lstat(replacement root): %v", err)
	}
	if originalInfo.Mode()&os.ModeSymlink != 0 || replacementInfo.Mode()&os.ModeSymlink != 0 || !originalInfo.IsDir() || !replacementInfo.IsDir() || os.SameFile(originalInfo, replacementInfo) {
		t.Fatal("root replacement fixture did not create distinct ordinary directory identities")
	}
	return originalRoot
}

func generatorTerminalDirectoryNames(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", directory, err)
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Name())
	}
	return result
}

func generatorTerminalTree(t *testing.T, directory string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == directory || entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		result[relative] = string(contents)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%q): %v", directory, err)
	}
	return result
}

// TestGeneratorRecoversRollbackIntentAfterPreRestoreSyncFailure proves a
// durable rollback intent preserves both sides of an exchanged replacement
// when the required pre-restore directory sync fails. A new process must use
// that journal to restore the old DB-visible generation exactly once.
func TestGeneratorRecoversRollbackIntentAfterPreRestoreSyncFailure(t *testing.T) {
	const childEnvironment = "DEADFIN_GENERATOR_PRE_RESTORE_SYNC_FAILURE_ROOT"
	if root := os.Getenv(childEnvironment); root != "" {
		generatorTerminalPreRestoreSyncFailureChild(t, root)
		return
	}

	root := t.TempDir()
	itemID := exchangeTestUUID(t, "deaf0006112233445566778899aabbcc")
	baselineStore := newCrashTestStore(root)
	baseline := newCrashTestGenerator(t, root, baselineStore, "old-tile", false)
	oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
	if err != nil {
		t.Fatalf("Generate(baseline): %v", err)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestGeneratorRecoversRollbackIntentAfterPreRestoreSyncFailure$")
	child.Env = append(os.Environ(), childEnvironment+"="+root)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("pre-restore-sync child exited %v:\n%s", err, output)
	}

	resolution := trickplayResolutionName(oldMetadata.Width, oldMetadata.TileWidth, oldMetadata.TileHeight)
	itemParent := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed())
	journalPath := filepath.Join(itemParent, "."+resolution+".journal")
	journal := generatorTerminalReadJournal(t, journalPath)
	if !journal.Exchanged || journal.Committed || !journal.IdentityAtDestination || !journal.RollingBack {
		t.Fatalf("journal after pre-restore sync failure = %#v, want exchanged uncommitted rollback intent with destination identity", journal)
	}
	if journal.Token == "" || journal.Metadata.GenerationToken != journal.Token || journal.Token == oldMetadata.GenerationToken {
		t.Fatalf("rollback journal generation identity = (%q, %#v), want pending token distinct from old metadata %#v", journal.Token, journal.Metadata, oldMetadata)
	}
	canonical := filepath.Join(itemParent, resolution)
	if got, want := generatorTerminalTree(t, canonical), map[string]string{"0.jpg": crashTestNewTile, generationManifestName: journal.Token + "\n"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical tree after pre-restore sync failure = %#v, want pending replacement with marker %#v", got, want)
	}
	staged := filepath.Join(root, journal.StagingName, resolution)
	if got, want := generatorTerminalTree(t, staged), map[string]string{"0.jpg": "old-tile"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("staged old tree after pre-restore sync failure = %#v, want old committed generation without pending marker %#v", got, want)
	}
	if got, want := generatorTerminalDirectoryNames(t, itemParent), []string{"." + resolution + ".journal", resolution}; !reflect.DeepEqual(got, want) {
		t.Fatalf("item parent after pre-restore sync failure = %#v, want canonical replacement and rollback journal %#v", got, want)
	}

	recoveryStore := newCrashTestStore(root)
	recovery := newCrashTestGenerator(t, root, recoveryStore, "ignored", true)
	if err := recovery.Recover(t.Context()); err != nil {
		t.Fatalf("Recover(after pre-restore sync failure): %v", err)
	}
	generatorTerminalAssertOldPublication(t, root, recoveryStore, itemID, oldMetadata)
	if _, err := os.Lstat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback journal after recovery Lstat error = %v, want not exist", err)
	}
	if _, err := os.Lstat(filepath.Join(root, journal.StagingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback staging after recovery Lstat error = %v, want not exist", err)
	}
	if got, want := generatorTerminalDirectoryNames(t, itemParent), []string{resolution}; !reflect.DeepEqual(got, want) {
		t.Fatalf("item parent after recovery = %#v, want restored canonical only %#v", got, want)
	}
	if err := recovery.Recover(t.Context()); err != nil {
		t.Fatalf("second Recover(after pre-restore sync failure): %v", err)
	}
	generatorTerminalAssertOldPublication(t, root, recoveryStore, itemID, oldMetadata)
	generatorTerminalAssertNoStaging(t, root)
}

func generatorTerminalPreRestoreSyncFailureChild(t *testing.T, root string) {
	t.Helper()
	itemID := exchangeTestUUID(t, "deaf0006112233445566778899aabbcc")
	store := newCrashTestStore(root)
	store.replaceError = errors.New("synthetic metadata rejection before commit")
	generator := newCrashTestGenerator(t, root, store, crashTestNewTile, false)
	resolution := trickplayResolutionName(318, 3, 2)
	itemParent := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed())
	journalPath := filepath.Join(itemParent, "."+resolution+".journal")
	canonicalItemParent := filepath.Join(exchangeTestCanonicalPath(t, root), itemID.Dashed()[:2], itemID.Dashed())
	nativeSync := generator.syncDirectory
	injected := errors.New("synthetic pre-restore publication-parent sync failure")
	syncFailures := 0
	generator.syncDirectory = func(directory *rootfs.Dir) error {
		if filepath.Clean(directory.Path()) == filepath.Clean(canonicalItemParent) {
			if raw, err := os.ReadFile(journalPath); err == nil {
				journal, decodeErr := decodePublicationJournal(filepath.Base(journalPath), raw)
				if decodeErr != nil {
					t.Fatalf("decode rollback journal before injected sync failure: %v", decodeErr)
				}
				if journal.RollingBack {
					syncFailures++
					return injected
				}
			}
		}
		return nativeSync(directory)
	}
	if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); err == nil || !strings.Contains(err.Error(), injected.Error()) {
		t.Fatalf("Generate(pre-restore sync failure) error = %v, want injected sync failure", err)
	}
	if got, want := syncFailures, 1; got != want {
		t.Fatalf("pre-restore sync failures = %d, want %d", got, want)
	}
}

// TestGeneratorRecoversPendingJournalAfterPreMutationSyncFailure proves a
// pending journal never outlives its staged generation when the sync that made
// that journal durable fails before a replacement mutates the canonical tree.
func TestGeneratorRecoversPendingJournalAfterPreMutationSyncFailure(t *testing.T) {
	const childEnvironment = "DEADFIN_GENERATOR_PRE_MUTATION_SYNC_FAILURE_ROOT"
	if root := os.Getenv(childEnvironment); root != "" {
		generatorTerminalPreMutationSyncFailureChild(t, root)
		return
	}

	root := t.TempDir()
	itemID := exchangeTestUUID(t, "deaf0007112233445566778899aabbcc")
	baselineStore := newCrashTestStore(root)
	baseline := newCrashTestGenerator(t, root, baselineStore, "old-tile", false)
	oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
	if err != nil {
		t.Fatalf("Generate(baseline): %v", err)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestGeneratorRecoversPendingJournalAfterPreMutationSyncFailure$")
	child.Env = append(os.Environ(), childEnvironment+"="+root)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("pre-mutation-sync child exited %v:\n%s", err, output)
	}

	resolution := trickplayResolutionName(oldMetadata.Width, oldMetadata.TileWidth, oldMetadata.TileHeight)
	itemParent := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed())
	journalPath := filepath.Join(itemParent, "."+resolution+".journal")
	journal := generatorTerminalReadJournal(t, journalPath)
	if journal.Exchanged || journal.Committed || journal.IdentityAtDestination || journal.RollingBack {
		t.Fatalf("journal after pre-mutation sync failure = %#v, want durable pending non-rollback journal", journal)
	}
	if journal.Token == "" || journal.Metadata.GenerationToken != journal.Token || journal.Token == oldMetadata.GenerationToken {
		t.Fatalf("pending journal generation identity = (%q, %#v), want pending token distinct from old metadata %#v", journal.Token, journal.Metadata, oldMetadata)
	}
	canonical := filepath.Join(itemParent, resolution)
	if got, want := generatorTerminalTree(t, canonical), map[string]string{"0.jpg": "old-tile"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical tree after pre-mutation sync failure = %#v, want unchanged old generation %#v", got, want)
	}
	staged := filepath.Join(root, journal.StagingName, resolution)
	if got, want := generatorTerminalTree(t, staged), map[string]string{"0.jpg": crashTestNewTile, generationManifestName: journal.Token + "\n"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("staged pending tree after pre-mutation sync failure = %#v, want pending generation with marker %#v", got, want)
	}
	if got, want := generatorTerminalDirectoryNames(t, itemParent), []string{"." + resolution + ".journal", resolution}; !reflect.DeepEqual(got, want) {
		t.Fatalf("item parent after pre-mutation sync failure = %#v, want old canonical and pending journal %#v", got, want)
	}

	recoveryStore := newCrashTestStore(root)
	recovery := newCrashTestGenerator(t, root, recoveryStore, "ignored", true)
	if err := recovery.Recover(t.Context()); err != nil {
		t.Fatalf("Recover(after pre-mutation sync failure): %v", err)
	}
	generatorTerminalAssertOldPublication(t, root, recoveryStore, itemID, oldMetadata)
	if _, err := os.Lstat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending journal after recovery Lstat error = %v, want not exist", err)
	}
	if _, err := os.Lstat(filepath.Join(root, journal.StagingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending staging after recovery Lstat error = %v, want not exist", err)
	}
	if got, want := generatorTerminalDirectoryNames(t, itemParent), []string{resolution}; !reflect.DeepEqual(got, want) {
		t.Fatalf("item parent after recovery = %#v, want unchanged canonical only %#v", got, want)
	}
	if err := recovery.Recover(t.Context()); err != nil {
		t.Fatalf("second Recover(after pre-mutation sync failure): %v", err)
	}
	generatorTerminalAssertOldPublication(t, root, recoveryStore, itemID, oldMetadata)
	generatorTerminalAssertNoStaging(t, root)
}

func generatorTerminalPreMutationSyncFailureChild(t *testing.T, root string) {
	t.Helper()
	itemID := exchangeTestUUID(t, "deaf0007112233445566778899aabbcc")
	store := newCrashTestStore(root)
	generator := newCrashTestGenerator(t, root, store, crashTestNewTile, false)
	resolution := trickplayResolutionName(318, 3, 2)
	itemParent := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed())
	journalPath := filepath.Join(itemParent, "."+resolution+".journal")
	canonicalItemParent := filepath.Join(exchangeTestCanonicalPath(t, root), itemID.Dashed()[:2], itemID.Dashed())
	nativeSync := generator.syncDirectory
	injected := errors.New("synthetic pre-mutation publication-parent sync failure")
	syncFailures := 0
	generator.syncDirectory = func(directory *rootfs.Dir) error {
		if filepath.Clean(directory.Path()) == filepath.Clean(canonicalItemParent) {
			if raw, err := os.ReadFile(journalPath); err == nil {
				journal, decodeErr := decodePublicationJournal(filepath.Base(journalPath), raw)
				if decodeErr != nil {
					t.Fatalf("decode pending journal before injected sync failure: %v", decodeErr)
				}
				if !journal.Exchanged && !journal.RollingBack && !journal.IdentityAtDestination {
					syncFailures++
					return injected
				}
			}
		}
		return nativeSync(directory)
	}
	if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); !errors.Is(err, injected) {
		t.Fatalf("Generate(pre-mutation sync failure) error = %v, want injected sync failure", err)
	}
	if got, want := syncFailures, 1; got != want {
		t.Fatalf("pre-mutation sync failures = %d, want %d", got, want)
	}
}

func generatorTerminalReadJournal(t *testing.T, path string) publicationJournal {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(journal %q): %v", path, err)
	}
	journal, err := decodePublicationJournal(filepath.Base(path), raw)
	if err != nil {
		t.Fatalf("decode journal %q: %v", path, err)
	}
	return journal
}
