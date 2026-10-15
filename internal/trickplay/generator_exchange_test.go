// SPDX-License-Identifier: GPL-2.0-only

package trickplay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/rootfs"
)

// TestGeneratorExchangeKeepsCanonicalResolutionReadableDuringReplacement
// synchronizes a real reader around the native directory exchange. The reader
// must observe the complete old tile before cutover and the complete new tile
// afterward, never an absent canonical path.
func TestGeneratorExchangeKeepsCanonicalResolutionReadableDuringReplacement(t *testing.T) {
	root := t.TempDir()
	itemID, err := dto.ParseUUID("445566778899aabbccddeeff00112233")
	if err != nil {
		t.Fatalf("ParseUUID(): %v", err)
	}
	width := 318
	resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(width, 3, 2))
	if err := os.MkdirAll(resolution, 0o755); err != nil {
		t.Fatalf("MkdirAll(prior resolution): %v", err)
	}
	if err := os.WriteFile(filepath.Join(resolution, "0.jpg"), []byte("old-tile"), 0o644); err != nil {
		t.Fatalf("WriteFile(prior tile): %v", err)
	}
	store := &exchangeTestStore{}
	generator, err := NewGenerator(GeneratorConfig{
		Root: root, Extractor: exchangeTestExtractor{}, Composer: exchangeTestComposer{}, Store: store, Reader: store, Publication: NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewGenerator(): %v", err)
	}
	nativeExchange := generator.exchange
	cutoverReady := make(chan struct{})
	readerSawOld := make(chan struct{})
	exchangeDone := make(chan struct{})
	generator.exchange = func(from *rootfs.Dir, fromName string, to *rootfs.Dir, toName string) error {
		close(cutoverReady)
		<-readerSawOld
		defer close(exchangeDone)
		return nativeExchange(from, fromName, to, toName)
	}

	videoWidth := 319
	runtimeTicks := int64(30_000_000)
	generation := make(chan error, 1)
	source := openTrickplayTestSource(t)
	go func() {
		_, err := generator.Generate(context.Background(), GenerateRequest{
			ItemID: itemID, Source: source, SourceRevision: "test-source-revision", VideoWidth: &videoWidth,
			RunTimeTicks: &runtimeTicks, RequestedWidth: 321, TileWidth: 3, TileHeight: 2, IntervalMilliseconds: 1_500,
		})
		generation <- err
	}()

	<-cutoverReady
	readerResult := make(chan error, 1)
	go func() {
		contents, err := os.ReadFile(filepath.Join(resolution, "0.jpg"))
		if err != nil {
			readerResult <- fmt.Errorf("read canonical tile before cutover: %w", err)
			return
		}
		if got, want := string(contents), "old-tile"; got != want {
			readerResult <- fmt.Errorf("canonical tile before cutover = %q, want old tile %q", got, want)
			return
		}
		close(readerSawOld)

		for {
			contents, err = os.ReadFile(filepath.Join(resolution, "0.jpg"))
			if err != nil {
				readerResult <- fmt.Errorf("read canonical tile during cutover: %w", err)
				return
			}
			switch got := string(contents); got {
			case "old-tile":
				select {
				case <-exchangeDone:
					contents, err = os.ReadFile(filepath.Join(resolution, "0.jpg"))
					if err != nil {
						readerResult <- fmt.Errorf("read canonical tile after cutover: %w", err)
						return
					}
					if got := string(contents); got != "new-tile" {
						readerResult <- fmt.Errorf("canonical tile after exchange = %q, want new tile", got)
						return
					}
					readerResult <- nil
					return
				default:
					runtime.Gosched()
				}
			case "new-tile":
				readerResult <- nil
				return
			default:
				readerResult <- fmt.Errorf("canonical tile during cutover = %q, want complete old or new tile", got)
				return
			}
		}
	}()

	if err := <-generation; err != nil {
		t.Fatalf("Generate(replacement): %v", err)
	}
	if err := <-readerResult; err != nil {
		t.Fatal(err)
	}
	if got, want := store.calls, 1; got != want {
		t.Fatalf("metadata commits after readable replacement = %d, want %d", got, want)
	}
}

const (
	generatorCrashRootEnvironment  = "DEADFIN_GENERATOR_CRASH_ROOT"
	generatorCrashPhaseEnvironment = "DEADFIN_GENERATOR_CRASH_PHASE"
)

const crashTestNewTile = "new-durable-artifact"

const sameMetadataNewTile = "new-tile"

func TestGeneratorRecoversPostExchangeCrashWithDurableMetadata(t *testing.T) {
	if root := os.Getenv(generatorCrashRootEnvironment); root != "" {
		generatorCrashChild(t, root)
		return
	}

	root := t.TempDir()
	itemID := exchangeTestUUID(t, "5566778899aabbccddeeff0011223344")
	store := newCrashTestStore(root)
	baseline := newCrashTestGenerator(t, root, store, "old-tile", false)
	oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
	if err != nil {
		t.Fatalf("Generate(baseline): %v", err)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestGeneratorRecoversPostExchangeCrashWithDurableMetadata$")
	child.Env = append(os.Environ(), generatorCrashRootEnvironment+"="+root)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("crash child exited %v:\n%s", err, output)
	}

	recovery := newCrashTestGenerator(t, root, store, "ignored", true)
	if _, err := recovery.Generate(t.Context(), exchangeTestRequest(t, itemID)); err == nil {
		t.Fatal("Generate(after crash with forced composition failure) error = nil, want failure after recovery")
	}

	resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(318, 3, 2))
	contents, err := os.ReadFile(filepath.Join(resolution, "0.jpg"))
	if err != nil {
		t.Fatalf("read canonical tile after crash recovery: %v", err)
	}
	if got, want := string(contents), "old-tile"; got != want {
		t.Fatalf("canonical tile after crash recovery = %q, want DB-visible old tile %q", got, want)
	}
	persisted, err := store.ReadTrickplay(t.Context(), itemID, oldMetadata.Width)
	if err != nil || persisted == nil || *persisted != oldMetadata {
		t.Fatalf("durable metadata after crash recovery = (%#v, %v), want unchanged old metadata %#v", persisted, err, oldMetadata)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(artifact root): %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".trickplay-") {
			t.Fatalf("crash recovery left unrecognized staging artifact %q", entry.Name())
		}
	}
}

// TestGeneratorRecoversReplacementAfterPreMetadataCrash protects the window
// after replacement publication is durable but before its metadata CAS. A
// restart must restore the DB-visible old tree, not retain replacement bytes
// under the old generation token.
func TestGeneratorRecoversReplacementAfterPreMetadataCrash(t *testing.T) {
	const crashRootEnvironment = "DEADFIN_GENERATOR_PRE_METADATA_CRASH_ROOT"
	if root := os.Getenv(crashRootEnvironment); root != "" {
		generatorCrashAfterPublicationBeforeMetadataChild(t, root)
		return
	}

	root := t.TempDir()
	itemID := exchangeTestUUID(t, "deaf0010112233445566778899aabbcc")
	baselineStore := newCrashTestStore(root)
	baseline := newCrashTestGenerator(t, root, baselineStore, "old-tile", false)
	oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
	if err != nil {
		t.Fatalf("Generate(baseline): %v", err)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestGeneratorRecoversReplacementAfterPreMetadataCrash$")
	child.Env = append(os.Environ(), crashRootEnvironment+"="+root)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("pre-metadata crash child exited %v:\n%s", err, output)
	}

	// A new store and generator model a real process restart: neither retains
	// the writer's in-memory publication state.
	recoveryStore := newCrashTestStore(root)
	recovery := newCrashTestGenerator(t, root, recoveryStore, "ignored", true)
	if err := recovery.Recover(t.Context()); err != nil {
		t.Fatalf("Recover(after pre-metadata crash): %v", err)
	}

	resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(oldMetadata.Width, oldMetadata.TileWidth, oldMetadata.TileHeight))
	contents, err := os.ReadFile(filepath.Join(resolution, "0.jpg"))
	if err != nil {
		t.Fatalf("read canonical tile after pre-metadata crash recovery: %v", err)
	}
	if got, want := string(contents), "old-tile"; got != want {
		t.Fatalf("canonical tile after pre-metadata crash recovery = %q, want old DB-visible tile %q", got, want)
	}
	persisted, err := recoveryStore.ReadTrickplay(t.Context(), itemID, oldMetadata.Width)
	if err != nil || persisted == nil || *persisted != oldMetadata {
		t.Fatalf("durable metadata after pre-metadata crash recovery = (%#v, %v), want unchanged old metadata %#v", persisted, err, oldMetadata)
	}
}

func generatorCrashAfterPublicationBeforeMetadataChild(t *testing.T, root string) {
	t.Helper()
	itemID := exchangeTestUUID(t, "deaf0010112233445566778899aabbcc")
	store := newCrashTestStore(root)
	generator := newCrashTestGenerator(t, root, store, crashTestNewTile, false)
	resolution := trickplayResolutionName(318, 3, 2)
	canonicalResolution := filepath.Join(exchangeTestCanonicalPath(t, root), itemID.Dashed()[:2], itemID.Dashed(), resolution)
	generator.afterPublicationBeforeMetadata = func() {
		manifest, err := os.ReadFile(filepath.Join(canonicalResolution, generationManifestName))
		if err != nil {
			t.Fatalf("read canonical generation manifest before metadata CAS: %v", err)
		}
		journalPath := filepath.Join(filepath.Dir(canonicalResolution), "."+resolution+".journal")
		raw, err := os.ReadFile(journalPath)
		if err != nil {
			t.Fatalf("read pending publication journal: %v", err)
		}
		journal, err := decodePublicationJournal(filepath.Base(journalPath), raw)
		if err != nil {
			t.Fatalf("decode pending publication journal: %v", err)
		}
		if !journal.Exchanged || journal.Committed || !journal.IdentityAtDestination {
			t.Fatalf("pending replacement journal = %#v, want exchanged, uncommitted destination identity", journal)
		}
		if got, want := strings.TrimSpace(string(manifest)), journal.Token; got != want {
			t.Fatalf("canonical generation manifest before metadata CAS = %q, want pending token %q", got, want)
		}
		persisted, err := store.ReadTrickplay(t.Context(), itemID, 318)
		if err != nil {
			t.Fatalf("read metadata before CAS: %v", err)
		}
		if persisted == nil || persisted.GenerationToken == journal.Token {
			t.Fatalf("metadata before CAS = %#v, want prior generation distinct from pending token %q", persisted, journal.Token)
		}
		os.Exit(0)
	}
	if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); err != nil {
		t.Fatalf("Generate(pre-metadata crash child): %v", err)
	}
	t.Fatal("Generate(pre-metadata crash child) returned without reaching afterPublicationBeforeMetadata")
}

func TestGeneratorExplicitRecoverKeepsPostMetadataCrashPublication(t *testing.T) {
	root := t.TempDir()
	itemID := exchangeTestUUID(t, "5566778899aabbccddeeff0011223344")
	store := newCrashTestStore(root)
	baseline := newCrashTestGenerator(t, root, store, "old-tile", false)
	oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
	if err != nil {
		t.Fatalf("Generate(baseline): %v", err)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestGeneratorRecoversPostExchangeCrashWithDurableMetadata$")
	child.Env = append(os.Environ(), generatorCrashRootEnvironment+"="+root, generatorCrashPhaseEnvironment+"=post-metadata")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("post-metadata crash child exited %v:\n%s", err, output)
	}

	recovery := newCrashTestGenerator(t, root, store, "ignored", true)
	if err := recovery.Recover(t.Context()); err != nil {
		t.Fatalf("Recover(after committed crash): %v", err)
	}
	resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(318, 3, 2))
	contents, err := os.ReadFile(filepath.Join(resolution, "0.jpg"))
	if err != nil {
		t.Fatalf("read canonical tile after explicit recovery: %v", err)
	}
	if got, want := string(contents), crashTestNewTile; got != want {
		t.Fatalf("canonical tile after committed crash recovery = %q, want DB-visible new tile %q", got, want)
	}
	persisted, err := store.ReadTrickplay(t.Context(), itemID, oldMetadata.Width)
	if err != nil || persisted == nil || *persisted == oldMetadata {
		t.Fatalf("durable metadata after committed crash recovery = (%#v, %v), want child-persisted metadata distinct from old %#v", persisted, err, oldMetadata)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(artifact root): %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".trickplay-") {
			t.Fatalf("explicit recovery left unrecognized staging artifact %q", entry.Name())
		}
	}
}

func TestGeneratorRollsBackWhenPostExchangeJournalSyncFails(t *testing.T) {
	root := t.TempDir()
	itemID := exchangeTestUUID(t, "66778899aabbccddeeff001122334455")
	store := newCrashTestStore(root)
	baseline := newCrashTestGenerator(t, root, store, "old-tile", false)
	oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
	if err != nil {
		t.Fatalf("Generate(baseline): %v", err)
	}

	generator := newCrashTestGenerator(t, root, store, crashTestNewTile, false)
	nativeSync := generator.syncDirectory
	syncCalls := 0
	generator.syncDirectory = func(dir *rootfs.Dir) error {
		syncCalls++
		if syncCalls == 3 {
			return fmt.Errorf("synthetic post-exchange journal sync failure")
		}
		return nativeSync(dir)
	}
	if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); err == nil {
		t.Fatal("Generate(post-exchange journal sync failure) error = nil, want rollback error")
	}

	resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(318, 3, 2))
	contents, err := os.ReadFile(filepath.Join(resolution, "0.jpg"))
	if err != nil {
		t.Fatalf("read canonical tile after journal sync failure: %v", err)
	}
	if got, want := string(contents), "old-tile"; got != want {
		t.Fatalf("canonical tile after journal sync failure = %q, want rolled-back old tile %q", got, want)
	}
	persisted, err := store.ReadTrickplay(t.Context(), itemID, oldMetadata.Width)
	if err != nil || persisted == nil || *persisted != oldMetadata {
		t.Fatalf("durable metadata after journal sync failure = (%#v, %v), want unchanged old metadata %#v", persisted, err, oldMetadata)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(artifact root): %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".trickplay-") {
			t.Fatalf("journal sync rollback left unrecognized staging artifact %q", entry.Name())
		}
	}
}

func TestGeneratorRejectsRootShardAndItemSyncFailuresWithoutDurableMismatch(t *testing.T) {
	itemID := exchangeTestUUID(t, "778899aabbccddeeff00112233445566")
	for _, test := range []struct {
		name                      string
		targetFor                 func(root string) string
		retainsPendingPublication bool
	}{
		{name: "root ancestry", targetFor: func(root string) string { return root }},
		{name: "shard ancestry", targetFor: func(root string) string { return filepath.Join(root, itemID.Dashed()[:2]) }},
		{name: "item ancestry", targetFor: func(root string) string { return filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed()) }, retainsPendingPublication: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := newCrashTestStore(root)
			generator := newCrashTestGenerator(t, root, store, crashTestNewTile, false)
			nativeSync := generator.syncDirectory
			target := test.targetFor(exchangeTestCanonicalPath(t, root))
			generator.syncDirectory = func(dir *rootfs.Dir) error {
				if filepath.Clean(dir.Path()) == filepath.Clean(target) {
					return fmt.Errorf("synthetic %s fsync failure", test.name)
				}
				return nativeSync(dir)
			}

			if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); err == nil {
				t.Fatalf("Generate(%s fsync failure) error = nil, want durable failure", test.name)
			}
			resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(318, 3, 2))
			if _, err := os.Stat(resolution); !os.IsNotExist(err) {
				t.Fatalf("canonical resolution after %s fsync failure stat = %v, want absent", test.name, err)
			}
			persisted, err := store.ReadTrickplay(t.Context(), itemID, 318)
			if err != nil || persisted != nil {
				t.Fatalf("durable metadata after %s fsync failure = (%#v, %v), want absent", test.name, persisted, err)
			}
			if test.retainsPendingPublication {
				journalPath := filepath.Join(filepath.Dir(resolution), "."+filepath.Base(resolution)+".journal")
				journal := generatorTerminalReadJournal(t, journalPath)
				if journal.Exchanged || journal.Committed || journal.IdentityAtDestination || journal.RollingBack {
					t.Fatalf("%s fsync failure journal = %#v, want durable pending journal", test.name, journal)
				}
				staged := filepath.Join(root, journal.StagingName, filepath.Base(resolution))
				stagedTree := generatorTerminalTree(t, staged)
				if len(stagedTree) != 2 || stagedTree["0.jpg"] != crashTestNewTile || strings.TrimSpace(stagedTree[generationManifestName]) != journal.Token {
					t.Fatalf("%s fsync failure staged generation = %#v, want pending tile and journal token", test.name, stagedTree)
				}
				recovery := newCrashTestGenerator(t, root, newCrashTestStore(root), "ignored", true)
				if err := recovery.Recover(t.Context()); err != nil {
					t.Fatalf("Recover(after %s fsync failure): %v", test.name, err)
				}
				if _, err := os.Lstat(journalPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s fsync failure journal after recovery Lstat error = %v, want not exist", test.name, err)
				}
				if _, err := os.Lstat(filepath.Join(root, journal.StagingName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s fsync failure staging after recovery Lstat error = %v, want not exist", test.name, err)
				}
				generatorTerminalAssertNoStaging(t, root)
				return
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("ReadDir(artifact root): %v", err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".trickplay-") {
					t.Fatalf("%s fsync failure left unrecognized staging artifact %q", test.name, entry.Name())
				}
			}
		})
	}
}

func TestGeneratorRecoversStaleJournalAcrossExchangeWindows(t *testing.T) {
	itemID := exchangeTestUUID(t, "5566778899aabbccddeeff0011223344")
	for _, test := range []struct {
		name  string
		phase string
	}{
		{name: "exchange completed before exchanged journal rewrite", phase: "exchange-before-journal"},
		{name: "rollback exchange completed before journal removal", phase: "rollback-before-journal-removal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := newCrashTestStore(root)
			baseline := newCrashTestGenerator(t, root, store, "old-tile", false)
			oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
			if err != nil {
				t.Fatalf("Generate(baseline): %v", err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestGeneratorRecoversPostExchangeCrashWithDurableMetadata$")
			child.Env = append(os.Environ(), generatorCrashRootEnvironment+"="+root, generatorCrashPhaseEnvironment+"="+test.phase)
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("%s crash child exited %v:\n%s", test.name, err, output)
			}
			recovery := newCrashTestGenerator(t, root, store, "ignored", true)
			if _, err := recovery.Generate(t.Context(), exchangeTestRequest(t, itemID)); err == nil {
				t.Fatalf("Generate(after %s crash) error = nil, want forced composition failure after recovery", test.name)
			}
			resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(318, 3, 2))
			contents, err := os.ReadFile(filepath.Join(resolution, "0.jpg"))
			if err != nil {
				t.Fatalf("read canonical tile after %s recovery: %v", test.name, err)
			}
			if got, want := string(contents), "old-tile"; got != want {
				t.Fatalf("canonical tile after %s recovery = %q, want old tile %q", test.name, got, want)
			}
			persisted, err := store.ReadTrickplay(t.Context(), itemID, oldMetadata.Width)
			if err != nil || persisted == nil || *persisted != oldMetadata {
				t.Fatalf("durable metadata after %s recovery = (%#v, %v), want old %#v", test.name, persisted, err, oldMetadata)
			}
		})
	}
}

func TestGeneratorReconcilesErrorAfterMetadataWriteUsingDurableRead(t *testing.T) {
	itemID := exchangeTestUUID(t, "8899aabbccddeeff0011223344556677")
	for _, test := range []struct {
		name      string
		configure func(*crashTestStore, Metadata)
		wantTile  string
		evidence  bool
	}{
		{name: "reader confirms exact new metadata", wantTile: crashTestNewTile},
		{
			name: "reader reports exact old metadata", wantTile: "old-tile",
			configure: func(store *crashTestStore, old Metadata) { store.readOverride = &old },
		},
		{
			name: "reader reports absent metadata", wantTile: "old-tile",
			configure: func(store *crashTestStore, _ Metadata) { store.readAbsent = true },
		},
		{
			name: "reader failure retains both recovery candidates", wantTile: crashTestNewTile, evidence: true,
			configure: func(store *crashTestStore, _ Metadata) {
				store.readError = fmt.Errorf("synthetic metadata read failure")
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
			store.returnAfterWrite = fmt.Errorf("synthetic metadata result lost after commit")
			if test.configure != nil {
				test.configure(store, oldMetadata)
			}
			generator := newCrashTestGenerator(t, root, store, crashTestNewTile, false)
			if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); err == nil {
				t.Fatal("Generate(metadata error after write) error = nil, want ambiguous commit error")
			}

			resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(318, 3, 2))
			contents, err := os.ReadFile(filepath.Join(resolution, "0.jpg"))
			if err != nil {
				t.Fatalf("read canonical tile after %s: %v", test.name, err)
			}
			if got := string(contents); got != test.wantTile {
				t.Fatalf("canonical tile after %s = %q, want %q", test.name, got, test.wantTile)
			}
			parentEntries, err := os.ReadDir(filepath.Dir(resolution))
			if err != nil {
				t.Fatalf("ReadDir(resolution parent): %v", err)
			}
			journalFound := false
			for _, entry := range parentEntries {
				if strings.HasSuffix(entry.Name(), ".journal") {
					journalFound = true
				}
			}
			rootEntries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("ReadDir(artifact root): %v", err)
			}
			stagingFound := false
			for _, entry := range rootEntries {
				if strings.HasPrefix(entry.Name(), ".trickplay-") {
					stagingFound = true
				}
			}
			if test.evidence {
				if !journalFound || !stagingFound {
					t.Fatalf("reader failure recovery evidence = journal:%t staging:%t, want both retained", journalFound, stagingFound)
				}
			} else if journalFound || stagingFound {
				t.Fatalf("resolved metadata outcome left recovery evidence = journal:%t staging:%t, want cleanup", journalFound, stagingFound)
			}
		})
	}
}

func TestGeneratorLeavesNoStagingArtifactWhenAllocationCannotBecomeDurable(t *testing.T) {
	itemID := exchangeTestUUID(t, "99aabbccddeeff001122334455667788")
	for _, test := range []struct {
		name      string
		configure func(*Generator, string)
	}{
		{
			name: "staging allocation fails",
			configure: func(generator *Generator, _ string) {
				generator.makeStaging = func(*rootfs.Dir) (string, *rootfs.Dir, error) {
					return "", nil, fmt.Errorf("synthetic staging allocation failure")
				}
			},
		},
		{
			name: "created staging root sync fails",
			configure: func(generator *Generator, root string) {
				nativeSync := generator.syncDirectory
				rootSyncs := 0
				generator.syncDirectory = func(dir *rootfs.Dir) error {
					if filepath.Clean(dir.Path()) == filepath.Clean(root) {
						rootSyncs++
						if rootSyncs == 2 {
							return fmt.Errorf("synthetic created staging root sync failure")
						}
					}
					return nativeSync(dir)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := newCrashTestStore(root)
			generator := newCrashTestGenerator(t, root, store, crashTestNewTile, false)
			test.configure(generator, exchangeTestCanonicalPath(t, root))
			if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); err == nil {
				t.Fatalf("Generate(%s) error = nil, want allocation durability error", test.name)
			}
			persisted, err := store.ReadTrickplay(t.Context(), itemID, 318)
			if err != nil || persisted != nil {
				t.Fatalf("durable metadata after %s = (%#v, %v), want absent", test.name, persisted, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("ReadDir(artifact root): %v", err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".trickplay-") {
					t.Fatalf("%s left orphaned staging directory %q", test.name, entry.Name())
				}
			}
		})
	}
}

func TestGeneratorSurfacesStagingCleanupAndRootSyncFailures(t *testing.T) {
	itemID := exchangeTestUUID(t, "aabbccddeeff00112233445566778899")
	for _, test := range []struct {
		name        string
		configure   func(*Generator, string)
		wantStaging bool
	}{
		{
			name: "staging removal failure is surfaced", wantStaging: true,
			configure: func(generator *Generator, _ string) {
				generator.removeAll = func(*rootfs.Dir, string) error { return fmt.Errorf("synthetic staging removal failure") }
			},
		},
		{
			name: "root sync after staging removal is surfaced",
			configure: func(generator *Generator, root string) {
				nativeSync := generator.syncDirectory
				rootSyncs := 0
				generator.syncDirectory = func(dir *rootfs.Dir) error {
					if filepath.Clean(dir.Path()) == filepath.Clean(root) {
						rootSyncs++
						if rootSyncs == 3 {
							return fmt.Errorf("synthetic cleaned staging root sync failure")
						}
					}
					return nativeSync(dir)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := newCrashTestStore(root)
			generator := newCrashTestGenerator(t, root, store, crashTestNewTile, true)
			test.configure(generator, exchangeTestCanonicalPath(t, root))
			if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); err == nil || !strings.Contains(err.Error(), "cleanup") {
				t.Fatalf("Generate(%s) error = %v, want surfaced cleanup failure", test.name, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("ReadDir(artifact root): %v", err)
			}
			stagingFound := false
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".trickplay-") {
					stagingFound = true
				}
			}
			if stagingFound != test.wantStaging {
				t.Fatalf("staging after %s = %t, want %t", test.name, stagingFound, test.wantStaging)
			}
		})
	}
}

func TestGeneratorPinsPhysicalRootAfterAncestorSymlinkRetarget(t *testing.T) {
	physicalParent := t.TempDir()
	outsideParent := t.TempDir()
	linkParent := t.TempDir()
	ancestor := filepath.Join(linkParent, "mutable-root")
	if err := os.Symlink(physicalParent, ancestor); err != nil {
		t.Fatalf("Symlink(physical root ancestor): %v", err)
	}
	logicalRoot := filepath.Join(ancestor, "trickplay")
	physicalRoot := filepath.Join(physicalParent, "trickplay")
	outsideRoot := filepath.Join(outsideParent, "trickplay")
	store := newCrashTestStore(physicalRoot)
	generator := newCrashTestGenerator(t, logicalRoot, store, crashTestNewTile, false)
	if err := os.MkdirAll(outsideRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(outside redirected root): %v", err)
	}
	outsidePhysicalRoot := exchangeTestCanonicalPath(t, outsideRoot)
	nativeSync := generator.syncDirectory
	generator.syncDirectory = func(dir *rootfs.Dir) error {
		path := dir.Path()
		if filepath.Clean(path) == filepath.Clean(outsidePhysicalRoot) || isDescendant(outsidePhysicalRoot, path) {
			return fmt.Errorf("sync attempted retargeted outside root %q", path)
		}
		return nativeSync(dir)
	}
	nativeStaging := generator.makeStaging
	generator.makeStaging = func(parent *rootfs.Dir) (string, *rootfs.Dir, error) {
		path := parent.Path()
		if filepath.Clean(path) == filepath.Clean(outsidePhysicalRoot) || isDescendant(outsidePhysicalRoot, path) {
			return "", nil, fmt.Errorf("staging attempted retargeted outside root %q", path)
		}
		return nativeStaging(parent)
	}
	nativeRemoveAll := generator.removeAll
	generator.removeAll = func(parent *rootfs.Dir, name string) error {
		path := parent.Path()
		if filepath.Clean(path) == filepath.Clean(outsidePhysicalRoot) || isDescendant(outsidePhysicalRoot, path) {
			return fmt.Errorf("cleanup attempted retargeted outside root %q", path)
		}
		return nativeRemoveAll(parent, name)
	}
	generator.extractor = outsideRootGuardExtractor{outsideRoot: outsidePhysicalRoot}
	if err := os.Remove(ancestor); err != nil {
		t.Fatalf("Remove(original root ancestor symlink): %v", err)
	}
	if err := os.Symlink(outsideParent, ancestor); err != nil {
		t.Fatalf("Symlink(retargeted root ancestor): %v", err)
	}

	itemID := exchangeTestUUID(t, "bbccddeeff00112233445566778899aa")
	metadata, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID))
	if err == nil {
		resolution := filepath.Join(physicalRoot, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(metadata.Width, metadata.TileWidth, metadata.TileHeight))
		contents, readErr := os.ReadFile(filepath.Join(resolution, "0.jpg"))
		if readErr != nil {
			t.Fatalf("read canonical physical-root tile: %v", readErr)
		}
		if got, want := string(contents), crashTestNewTile; got != want {
			t.Fatalf("canonical physical-root tile = %q, want %q", got, want)
		}
	}
	if err := generator.Recover(t.Context()); err != nil {
		t.Fatalf("Recover(retargeted ancestor): %v", err)
	}

	entries, err := os.ReadDir(outsideRoot)
	if err != nil {
		t.Fatalf("ReadDir(outside redirected root): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("retargeted outside root entries = %#v, want no extractor, staging, journal, or cleanup artifacts", entries)
	}
}

func TestGeneratorRollsBackFailedWriteWhenBaselineMetadataEqualsReplacement(t *testing.T) {
	root := t.TempDir()
	itemID := exchangeTestUUID(t, "ccddeeff00112233445566778899aabb")
	store := newCrashTestStore(root)
	baseline := newCrashTestGenerator(t, root, store, "old-tile", false)
	oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
	if err != nil {
		t.Fatalf("Generate(baseline): %v", err)
	}
	if oldMetadata.GenerationToken == "" {
		t.Fatal("baseline generation token = empty, want durable commit identity")
	}
	if len("old-tile") != len(sameMetadataNewTile) {
		t.Fatal("equal-metadata fixture must preserve composed byte size")
	}
	store.replaceError = fmt.Errorf("synthetic metadata write failure before commit")
	replacement := newCrashTestGenerator(t, root, store, sameMetadataNewTile, false)
	if _, err := replacement.Generate(t.Context(), exchangeTestRequest(t, itemID)); err == nil {
		t.Fatal("Generate(replacement metadata write failure) error = nil, want rollback")
	}

	resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(oldMetadata.Width, oldMetadata.TileWidth, oldMetadata.TileHeight))
	contents, err := os.ReadFile(filepath.Join(resolution, "0.jpg"))
	if err != nil {
		t.Fatalf("read canonical tile after failed equal-metadata replacement: %v", err)
	}
	if got, want := string(contents), "old-tile"; got != want {
		t.Fatalf("canonical tile after failed equal-metadata replacement = %q, want exact old tree tile %q", got, want)
	}
	persisted, err := store.ReadTrickplay(t.Context(), itemID, oldMetadata.Width)
	if err != nil || persisted == nil || *persisted != oldMetadata {
		t.Fatalf("persisted metadata after failed equal-metadata replacement = (%#v, %v), want old token-bearing metadata %#v", persisted, err, oldMetadata)
	}
}

func TestGeneratorRollsBackEqualMetadataFailedWriteAfterRestart(t *testing.T) {
	root := t.TempDir()
	itemID := exchangeTestUUID(t, "5566778899aabbccddeeff0011223344")
	store := newCrashTestStore(root)
	baseline := newCrashTestGenerator(t, root, store, "old-tile", false)
	oldMetadata, err := baseline.Generate(t.Context(), exchangeTestRequest(t, itemID))
	if err != nil {
		t.Fatalf("Generate(baseline): %v", err)
	}

	child := exec.Command(os.Args[0], "-test.run=^TestGeneratorRecoversPostExchangeCrashWithDurableMetadata$")
	child.Env = append(os.Environ(), generatorCrashRootEnvironment+"="+root, generatorCrashPhaseEnvironment+"=rollback-equal-metadata")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("equal-metadata rollback crash child exited %v:\n%s", err, output)
	}

	recovery := newCrashTestGenerator(t, root, store, "ignored", true)
	if err := recovery.Recover(t.Context()); err != nil {
		t.Fatalf("Recover(equal-metadata failed write): %v", err)
	}
	resolution := filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(oldMetadata.Width, oldMetadata.TileWidth, oldMetadata.TileHeight))
	contents, err := os.ReadFile(filepath.Join(resolution, "0.jpg"))
	if err != nil {
		t.Fatalf("read canonical tile after equal-metadata restart recovery: %v", err)
	}
	if got, want := string(contents), "old-tile"; got != want {
		t.Fatalf("canonical tile after equal-metadata restart recovery = %q, want old tile %q", got, want)
	}
	persisted, err := store.ReadTrickplay(t.Context(), itemID, oldMetadata.Width)
	if err != nil || persisted == nil || *persisted != oldMetadata {
		t.Fatalf("persisted metadata after equal-metadata restart recovery = (%#v, %v), want old %#v", persisted, err, oldMetadata)
	}
}

type outsideRootGuardExtractor struct {
	outsideRoot string
}

func (extractor outsideRootGuardExtractor) Extract(ctx context.Context, request FrameRequest) (FrameExtraction, error) {
	if filepath.Clean(request.Output.Path()) == filepath.Clean(extractor.outsideRoot) || isDescendant(extractor.outsideRoot, request.Output.Path()) {
		return FrameExtraction{}, fmt.Errorf("extractor attempted retargeted outside root %q", request.Output.Path())
	}
	return exchangeTestExtractor{}.Extract(ctx, request)
}

func generatorCrashChild(t *testing.T, root string) {
	t.Helper()
	itemID := exchangeTestUUID(t, "5566778899aabbccddeeff0011223344")
	store := newCrashTestStore(root)
	generator := newCrashTestGenerator(t, root, store, crashTestNewTile, false)
	switch os.Getenv(generatorCrashPhaseEnvironment) {
	case "":
		generator.afterPublicationBeforeMetadata = func() { os.Exit(0) }
	case "post-metadata":
		generator.afterMetadataBeforeFinalize = func() { os.Exit(0) }
	case "exchange-before-journal":
		generator.afterExchangeBeforeJournalRewrite = func() { os.Exit(0) }
	case "rollback-before-journal-removal":
		store.replaceError = fmt.Errorf("synthetic metadata rejection before commit")
		generator.afterRollbackExchangeBeforeJournalRemoval = func() { os.Exit(0) }
	case "rollback-equal-metadata":
		generator.composer = crashTestComposer{marker: sameMetadataNewTile}
		store.replaceError = fmt.Errorf("synthetic equal-metadata write failure before commit")
		generator.afterRollbackExchangeBeforeJournalRemoval = func() { os.Exit(0) }
	default:
		t.Fatalf("unknown crash phase %q", os.Getenv(generatorCrashPhaseEnvironment))
	}
	if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); err != nil {
		t.Fatalf("Generate(crash child): %v", err)
	}
	t.Fatal("Generate(crash child) returned without reaching the requested crash hook")
}

func newCrashTestGenerator(t *testing.T, root string, store *crashTestStore, marker string, fail bool) *Generator {
	t.Helper()
	generator, err := NewGenerator(GeneratorConfig{
		Root: root, Extractor: exchangeTestExtractor{}, Composer: crashTestComposer{marker: marker, fail: fail}, Store: store, Reader: store, Publication: NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewGenerator(): %v", err)
	}
	return generator
}

func exchangeTestUUID(t *testing.T, text string) dto.UUID {
	t.Helper()
	itemID, err := dto.ParseUUID(text)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", text, err)
	}
	return itemID
}

func exchangeTestCanonicalPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return canonical
}

func exchangeTestRequest(t *testing.T, itemID dto.UUID) GenerateRequest {
	t.Helper()
	videoWidth := 319
	runtimeTicks := int64(30_000_000)
	return GenerateRequest{
		ItemID: itemID, Source: openTrickplayTestSource(t), SourceRevision: "test-source-revision", VideoWidth: &videoWidth,
		RunTimeTicks: &runtimeTicks, RequestedWidth: 321, TileWidth: 3, TileHeight: 2, IntervalMilliseconds: 1_500,
	}
}

type exchangeTestExtractor struct{}

func (exchangeTestExtractor) Extract(ctx context.Context, request FrameRequest) (FrameExtraction, error) {
	if err := ctx.Err(); err != nil {
		return FrameExtraction{}, err
	}
	name := "00000001.jpg"
	file, err := request.Output.CreateExclusive(name, 0o644)
	if err != nil {
		return FrameExtraction{}, err
	}
	if _, err := file.Write([]byte("frame")); err != nil {
		_ = file.Close()
		return FrameExtraction{}, err
	}
	if err := file.Close(); err != nil {
		return FrameExtraction{}, err
	}
	return FrameExtraction{Names: []string{name}, Height: 180}, nil
}

type exchangeTestComposer struct{}

func (exchangeTestComposer) Compose(ctx context.Context, request images.TileRequest) (images.TileResult, error) {
	if err := ctx.Err(); err != nil {
		return images.TileResult{}, err
	}
	contents := []byte("new-tile")
	file, err := request.OutputDir.CreateExclusive(request.OutputName, 0o644)
	if err != nil {
		return images.TileResult{}, err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return images.TileResult{}, err
	}
	if err := file.Close(); err != nil {
		return images.TileResult{}, err
	}
	return images.TileResult{Path: filepath.Join(request.OutputDir.Path(), request.OutputName), ByteSize: int64(len(contents))}, nil
}

type crashTestComposer struct {
	marker string
	fail   bool
}

func (composer crashTestComposer) Compose(ctx context.Context, request images.TileRequest) (images.TileResult, error) {
	if err := ctx.Err(); err != nil {
		return images.TileResult{}, err
	}
	if composer.fail {
		return images.TileResult{}, fmt.Errorf("synthetic composition failure")
	}
	contents := []byte(composer.marker)
	file, err := request.OutputDir.CreateExclusive(request.OutputName, 0o644)
	if err != nil {
		return images.TileResult{}, err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return images.TileResult{}, err
	}
	if err := file.Close(); err != nil {
		return images.TileResult{}, err
	}
	return images.TileResult{Path: filepath.Join(request.OutputDir.Path(), request.OutputName), ByteSize: int64(len(contents))}, nil
}

type exchangeTestStore struct {
	calls int
}

func (store *exchangeTestStore) ReplaceTrickplay(_ context.Context, _ Metadata) error {
	store.calls++
	return nil
}

func (store *exchangeTestStore) ReadTrickplay(_ context.Context, _ dto.UUID, _ int) (*Metadata, error) {
	return nil, nil
}

type crashTestStore struct {
	path             string
	replaceError     error
	returnAfterWrite error
	readOverride     *Metadata
	readAbsent       bool
	readError        error
}

func newCrashTestStore(root string) *crashTestStore {
	return &crashTestStore{path: filepath.Join(root, ".crash-test-metadata.json")}
}

func (store *crashTestStore) ReplaceTrickplay(_ context.Context, metadata Metadata) (err error) {
	if store.replaceError != nil {
		return store.replaceError
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	directory := filepath.Dir(store.path)
	file, err := os.CreateTemp(directory, ".crash-test-metadata-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, file.Close())
		}
	}()
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Rename(temporary, store.path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directoryHandle.Close()) }()
	if err := directoryHandle.Sync(); err != nil {
		return err
	}
	return store.returnAfterWrite
}

func (store *crashTestStore) ReadTrickplay(_ context.Context, itemID dto.UUID, width int) (*Metadata, error) {
	if store.readError != nil {
		return nil, store.readError
	}
	if store.readAbsent {
		return nil, nil
	}
	if store.readOverride != nil {
		metadata := *store.readOverride
		if metadata.ItemID != itemID || metadata.Width != width {
			return nil, nil
		}
		return &metadata, nil
	}
	raw, err := os.ReadFile(store.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var metadata Metadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, err
	}
	if metadata.ItemID != itemID || metadata.Width != width {
		return nil, nil
	}
	return &metadata, nil
}
