// SPDX-License-Identifier: GPL-2.0-only

package trickplay

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// TestGeneratorCompletesPinnedGenerationAfterRootPathSwap proves that work
// already admitted by Generate remains descriptor-rooted. A root replacement
// after the extractor starts must neither divert publication nor make an
// otherwise valid generation fail.
func TestGeneratorCompletesPinnedGenerationAfterRootPathSwap(t *testing.T) {
	root := filepath.Join(t.TempDir(), "trickplay")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("Mkdir(root): %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	store := &exchangeTestStore{}
	generator, err := NewGenerator(GeneratorConfig{
		Root:      root,
		Extractor: generatorRootedBlockingExtractor{started: started, release: release},
		Composer:  exchangeTestComposer{},
		Store:     store, Reader: store, Publication: NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewGenerator(): %v", err)
	}
	t.Cleanup(func() {
		if err := generator.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})

	itemID := exchangeTestUUID(t, "deaf1000112233445566778899aabbcc")
	generated := make(chan struct {
		metadata Metadata
		err      error
	}, 1)
	go func() {
		metadata, generateErr := generator.Generate(context.Background(), exchangeTestRequest(t, itemID))
		generated <- struct {
			metadata Metadata
			err      error
		}{metadata: metadata, err: generateErr}
	}()
	<-started

	pinnedRoot := generatorTerminalReplaceRootPathname(t, root)
	close(release)
	result := <-generated
	if result.err != nil {
		t.Fatalf("Generate(after root pathname replacement): %v", result.err)
	}
	if result.metadata.GenerationToken == "" {
		t.Fatal("Generate(after root pathname replacement) token = empty")
	}
	if got, want := store.calls, 1; got != want {
		t.Fatalf("metadata commits in pinned root = %d, want %d", got, want)
	}

	resolution := filepath.Join(pinnedRoot, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(318, 3, 2))
	if got, want := generatorTerminalTree(t, resolution), map[string]string{"0.jpg": "new-tile"}; !mapsEqual(got, want) {
		t.Fatalf("published pinned-root resolution = %#v, want %#v", got, want)
	}
	if got, want := generatorTerminalDirectoryNames(t, root), []string{}; !stringSlicesEqual(got, want) {
		t.Fatalf("replacement root entries = %#v, want no generator artifacts %#v", got, want)
	}
}

// TestGeneratorWritesValidatedComponentJournalV2 observes the durable pending
// journal at the publication crash boundary. It must encode only rooted
// single-component coordinates, never pathname-bearing legacy fields.
func TestGeneratorWritesValidatedComponentJournalV2(t *testing.T) {
	root := t.TempDir()
	itemID := exchangeTestUUID(t, "deaf1003112233445566778899aabbcc")
	store := &exchangeTestStore{}
	generator, err := NewGenerator(GeneratorConfig{
		Root: root, Extractor: exchangeTestExtractor{}, Composer: exchangeTestComposer{}, Store: store, Reader: store, Publication: NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewGenerator(): %v", err)
	}
	t.Cleanup(func() { _ = generator.Close() })

	journalPath := generatorRootedJournalPath(t, root, itemID)
	var journal map[string]any
	generator.afterPublicationBeforeMetadata = func() {
		raw, readErr := os.ReadFile(journalPath)
		if readErr != nil {
			t.Fatalf("ReadFile(pending journal): %v", readErr)
		}
		if decodeErr := json.Unmarshal(raw, &journal); decodeErr != nil {
			t.Fatalf("Unmarshal(pending journal): %v", decodeErr)
		}
	}
	if _, err := generator.Generate(t.Context(), exchangeTestRequest(t, itemID)); err != nil {
		t.Fatalf("Generate(): %v", err)
	}

	if got, want := journal["Version"], float64(2); got != want {
		t.Fatalf("journal Version = %#v, want %#v", got, want)
	}
	for key, want := range map[string]string{
		"Shard": itemID.Dashed()[:2], "Item": itemID.Dashed(), "Resolution": trickplayResolutionName(318, 3, 2),
	} {
		if got, ok := journal[key].(string); !ok || got != want {
			t.Fatalf("journal %s = %#v, want validated component %q", key, journal[key], want)
		}
	}
	stagingName, ok := journal["StagingName"].(string)
	if !ok || !strings.HasPrefix(stagingName, ".trickplay-") || strings.Contains(stagingName, string(filepath.Separator)) {
		t.Fatalf("journal StagingName = %#v, want one staging-name component", journal["StagingName"])
	}
	for _, legacyKey := range []string{"Destination", "Staged"} {
		if _, found := journal[legacyKey]; found {
			t.Fatalf("journal unexpectedly stores legacy pathname field %q: %#v", legacyKey, journal[legacyKey])
		}
	}
}

// TestGeneratorRecoverAfterRootPathSwapFailsClosedWithoutTouchingReplacement
// protects recovery from sweeping a replacement directory after its original
// root pin has been renamed away.
func TestGeneratorRecoverAfterRootPathSwapFailsClosedWithoutTouchingReplacement(t *testing.T) {
	root := filepath.Join(t.TempDir(), "trickplay")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("Mkdir(root): %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, ".trickplay-abandoned"), 0o700); err != nil {
		t.Fatalf("Mkdir(original abandoned staging): %v", err)
	}
	store := &exchangeTestStore{}
	generator, err := NewGenerator(GeneratorConfig{
		Root: root, Extractor: exchangeTestExtractor{}, Composer: exchangeTestComposer{}, Store: store, Reader: store, Publication: NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewGenerator(): %v", err)
	}
	t.Cleanup(func() {
		if err := generator.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})

	pinnedRoot := generatorTerminalReplaceRootPathname(t, root)
	replacementStaging := filepath.Join(root, ".trickplay-replacement-must-survive")
	if err := os.Mkdir(replacementStaging, 0o700); err != nil {
		t.Fatalf("Mkdir(replacement staging): %v", err)
	}
	if err := os.WriteFile(filepath.Join(replacementStaging, "sentinel"), []byte("replacement"), 0o600); err != nil {
		t.Fatalf("WriteFile(replacement sentinel): %v", err)
	}

	err = generator.Recover(t.Context())
	if err == nil {
		t.Fatal("Recover(after root pathname replacement) error = nil, want freshness rejection")
	}
	if !strings.Contains(err.Error(), "root identity changed") {
		t.Fatalf("Recover(after root pathname replacement) error = %q, want root identity freshness rejection", err)
	}
	if got, want := generatorTerminalTree(t, root), map[string]string{".trickplay-replacement-must-survive/sentinel": "replacement"}; !mapsEqual(got, want) {
		t.Fatalf("replacement root after failed recovery = %#v, want untouched %#v", got, want)
	}
	if got, want := generatorTerminalDirectoryNames(t, pinnedRoot), []string{".trickplay-abandoned"}; !stringSlicesEqual(got, want) {
		t.Fatalf("pinned root after freshness rejection = %#v, want recovery did not sweep %#v", got, want)
	}
}

func TestGeneratorRecoverRejectsLegacyJournalWithTargetedError(t *testing.T) {
	root := t.TempDir()
	itemID := exchangeTestUUID(t, "deaf1001112233445566778899aabbcc")
	journalPath := generatorRootedJournalPath(t, root, itemID)
	legacy := map[string]any{
		"Destination": filepath.Join(root, itemID.Dashed()[:2], itemID.Dashed(), trickplayResolutionName(318, 3, 2)),
		"Staged":      filepath.Join(root, ".trickplay-legacy", trickplayResolutionName(318, 3, 2)),
		"Token":       "legacy-token",
	}
	generatorRootedWriteJournal(t, journalPath, legacy)

	store := &exchangeTestStore{}
	generator, err := NewGenerator(GeneratorConfig{
		Root: root, Extractor: exchangeTestExtractor{}, Composer: exchangeTestComposer{}, Store: store, Reader: store, Publication: NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewGenerator(): %v", err)
	}
	t.Cleanup(func() { _ = generator.Close() })

	err = generator.Recover(t.Context())
	if err == nil {
		t.Fatal("Recover(legacy journal) error = nil, want targeted migration rejection")
	}
	for _, want := range []string{filepath.Base(journalPath), "legacy"} {
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
			t.Fatalf("Recover(legacy journal) error = %q, want journal-specific legacy guidance containing %q", err, want)
		}
	}
}

func TestGeneratorRecoverRejectsJournalV2InvalidComponentWithTargetedError(t *testing.T) {
	root := t.TempDir()
	itemID := exchangeTestUUID(t, "deaf1002112233445566778899aabbcc")
	journalPath := generatorRootedJournalPath(t, root, itemID)
	invalid := map[string]any{
		"Version":     2,
		"Shard":       "not-the-item-shard",
		"Item":        itemID.Dashed(),
		"Resolution":  trickplayResolutionName(318, 3, 2),
		"StagingName": ".trickplay-staging",
		"Token":       "v2-token",
		"Metadata": Metadata{
			ItemID: itemID, Width: 318, Height: 180, TileWidth: 3, TileHeight: 2, Interval: 1_500,
		},
	}
	generatorRootedWriteJournal(t, journalPath, invalid)

	store := &exchangeTestStore{}
	generator, err := NewGenerator(GeneratorConfig{
		Root: root, Extractor: exchangeTestExtractor{}, Composer: exchangeTestComposer{}, Store: store, Reader: store, Publication: NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewGenerator(): %v", err)
	}
	t.Cleanup(func() { _ = generator.Close() })

	err = generator.Recover(t.Context())
	if err == nil {
		t.Fatal("Recover(invalid v2 journal) error = nil, want component rejection")
	}
	for _, want := range []string{filepath.Base(journalPath), "Shard"} {
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
			t.Fatalf("Recover(invalid v2 journal) error = %q, want journal-specific component guidance containing %q", err, want)
		}
	}
}

type generatorRootedBlockingExtractor struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (extractor generatorRootedBlockingExtractor) Extract(ctx context.Context, request FrameRequest) (FrameExtraction, error) {
	close(extractor.started)
	select {
	case <-extractor.release:
	case <-ctx.Done():
		return FrameExtraction{}, ctx.Err()
	}
	return exchangeTestExtractor{}.Extract(ctx, request)
}

func generatorRootedJournalPath(t *testing.T, root string, itemID dto.UUID) string {
	t.Helper()
	item := itemID.Dashed()
	parent := filepath.Join(root, item[:2], item)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("MkdirAll(journal parent): %v", err)
	}
	return filepath.Join(parent, "."+trickplayResolutionName(318, 3, 2)+".journal")
}

func generatorRootedWriteJournal(t *testing.T, path string, journal map[string]any) {
	t.Helper()
	raw, err := json.Marshal(journal)
	if err != nil {
		t.Fatalf("Marshal(journal): %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile(journal): %v", err)
	}
}

func mapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for name, value := range left {
		if right[name] != value {
			return false
		}
	}
	return true
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
