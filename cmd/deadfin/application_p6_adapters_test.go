// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/keyframes"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/tasks"
	"github.com/deadfin-org/deadfin/internal/trickplay"
	"go.uber.org/goleak"
)

// TestP6LazyKeyframeSourceUsesPersistedSelectedSource verifies that selected
// media-source IDs own the durable keyframe cache. A cache hit must not invoke
// ffprobe and must preserve the stored duration and seek positions exactly.
func TestP6LazyKeyframeSourceUsesPersistedSelectedSource(t *testing.T) {
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	itemID := p6AdapterUUID(t, 0x601)
	sourceID := p6AdapterUUID(t, 0x602)
	p6UpsertVideo(t, items, itemID, "/media/logical.mkv", nil, nil, nil)
	selected := p6WriteSourceFixture(t, "selected.mkv", "selected keyframe source")
	p6UpsertSourceVideo(t, items, sourceID, selected, nil, nil, nil)
	want := keyframes.Data{TotalDurationTicks: 42_500_000, KeyframeTicks: []int64{0, 10_000_000, 30_000_000}}
	snapshot, err := items.DerivedSourceSnapshot(t.Context(), sourceID)
	if err != nil {
		t.Fatalf("snapshot selected source revision: %v", err)
	}
	if err := items.ReplaceKeyframes(t.Context(), store.KeyframeInfo{ItemID: sourceID, SourceRevision: snapshot.SourceRevision, TotalDurationTicks: want.TotalDurationTicks, KeyframeTicks: want.KeyframeTicks}); err != nil {
		t.Fatalf("persist selected-source keyframes: %v", err)
	}

	extractor := p6KeyframeExtractor(t, "exit 19\n")
	got, found, err := newLazyKeyframeSource(items, extractor).Keyframes(t.Context(), itemID, sourceID.Dashed())
	if err != nil {
		t.Fatalf("read persisted selected-source keyframes: %v", err)
	}
	if !found {
		t.Fatal("persisted selected-source keyframes were reported missing")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected-source keyframes = %#v, want %#v", got, want)
	}
}

// TestP6LazyKeyframeSourceTreatsIncompleteFingerprintAsCacheMiss proves old
// scan rows cannot authorize a source open, an ffprobe invocation, or a
// keyframe replacement. Normal and concurrent requests retain the stale-cache
// policy (empty data, not found, no error) until a scan backfills both facts.
func TestP6LazyKeyframeSourceTreatsIncompleteFingerprintAsCacheMiss(t *testing.T) {
	for index, tc := range []struct {
		name   string
		mutate func(*item.Item)
	}{
		{name: "neither", mutate: func(value *item.Item) { value.FileInode, value.FileChangedAt = nil, nil }},
		{name: "inode only", mutate: func(value *item.Item) { value.FileChangedAt = nil }},
		{name: "changed-at only", mutate: func(value *item.Item) { value.FileInode = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newP6AdapterApplication(t)
			items := store.NewItemStore(app.stores)
			itemID := p6AdapterUUID(t, uint64(0x603+index))
			fixture := p6WriteSourceFixture(t, "legacy-keyframe-source.mkv", "legacy keyframe source")
			p6UpsertSourceVideo(t, items, itemID, fixture, nil, nil, nil)
			legacy, err := items.Get(t.Context(), itemID)
			if err != nil {
				t.Fatalf("read %s legacy source: %v", tc.name, err)
			}
			invalidPath := "\x00must-not-open"
			legacy.Path = &invalidPath
			tc.mutate(&legacy)
			if err := items.Upsert(t.Context(), legacy); err != nil {
				t.Fatalf("persist %s incomplete fingerprint: %v", tc.name, err)
			}

			invoked := filepath.Join(t.TempDir(), "ffprobe-invoked")
			source := newLazyKeyframeSource(items, p6KeyframeExtractor(t, fmt.Sprintf("touch %q\nprintf 'packet,0,K_\\nformat,1\\n'\n", invoked)))
			assertMiss := func(result p6KeyframeResult, request string) {
				t.Helper()
				if result.err != nil || result.found || !reflect.DeepEqual(result.data, keyframes.Data{}) {
					t.Fatalf("%s incomplete %s keyframes = %#v, want empty, false, nil", request, tc.name, result)
				}
			}

			data, found, err := source.Keyframes(t.Context(), itemID, itemID.Dashed())
			assertMiss(p6KeyframeResult{data: data, found: found, err: err}, "normal")

			start := make(chan struct{})
			results := make(chan p6KeyframeResult, 2)
			for range 2 {
				go func() {
					<-start
					data, found, err := source.Keyframes(context.Background(), itemID, itemID.Dashed())
					results <- p6KeyframeResult{data: data, found: found, err: err}
				}()
			}
			close(start)
			assertMiss(p6ReceiveKeyframeResult(t, results), "concurrent first")
			assertMiss(p6ReceiveKeyframeResult(t, results), "concurrent second")
			if _, err := os.Stat(invoked); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("incomplete %s fingerprint invoked ffprobe: stat error = %v, want not exist", tc.name, err)
			}
			if _, err := items.Keyframes(t.Context(), itemID); !store.IsNotFound(err) {
				t.Fatalf("incomplete %s fingerprint durable keyframes error = %v, want not found", tc.name, err)
			}
		})
	}
}

// TestP6LazyKeyframeSourceCoalescesSelectedSourceExtractionAndPersistsResult
// verifies concurrent HLS reads for one selected media source run one ffprobe
// process and make that exact extraction durable for later readers.
func TestP6LazyKeyframeSourceCoalescesSelectedSourceExtractionAndPersistsResult(t *testing.T) {
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	logicalID := p6AdapterUUID(t, 0x611)
	sourceID := p6AdapterUUID(t, 0x612)
	p6UpsertVideo(t, items, logicalID, "/media/logical.mkv", nil, nil, nil)
	selected := p6WriteSourceFixture(t, "selected.mkv", "coalesced keyframe source")
	p6UpsertSourceVideo(t, items, sourceID, selected, nil, nil, nil)

	countPath, releasePath := filepath.Join(t.TempDir(), "ffprobe-count"), filepath.Join(t.TempDir(), "release")
	extractor := p6KeyframeExtractor(t, fmt.Sprintf("printf . >> %q\nwhile [ ! -e %q ]; do sleep 0.01; done\nprintf 'packet,0,K_\\npacket,1,K_\\nformat,2.5\\n'\n", countPath, releasePath))
	source := newLazyKeyframeSource(items, extractor)

	first := make(chan p6KeyframeResult, 1)
	go func() {
		data, found, err := source.Keyframes(context.Background(), logicalID, sourceID.Dashed())
		first <- p6KeyframeResult{data: data, found: found, err: err}
	}()
	p6WaitForKeyframeExtraction(t, source, sourceID)
	second := make(chan p6KeyframeResult, 1)
	go func() {
		data, found, err := source.Keyframes(context.Background(), logicalID, sourceID.Dashed())
		second <- p6KeyframeResult{data: data, found: found, err: err}
	}()
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatalf("release controlled ffprobe: %v", err)
	}

	want := keyframes.Data{TotalDurationTicks: 25_000_000, KeyframeTicks: []int64{0, 10_000_000}}
	for _, result := range []p6KeyframeResult{p6ReceiveKeyframeResult(t, first), p6ReceiveKeyframeResult(t, second)} {
		if result.err != nil || !result.found || !reflect.DeepEqual(result.data, want) {
			t.Fatalf("concurrent keyframe read = %#v, want found persisted extraction %#v", result, want)
		}
	}
	count, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatalf("read ffprobe invocation count: %v", err)
	}
	if got := string(count); got != "." {
		t.Fatalf("selected-source ffprobe invocations = %q, want one coalesced extraction", got)
	}
	stored, err := items.Keyframes(t.Context(), sourceID)
	if err != nil {
		t.Fatalf("read persisted coalesced keyframes: %v", err)
	}
	if got := (keyframes.Data{TotalDurationTicks: stored.TotalDurationTicks, KeyframeTicks: stored.KeyframeTicks}); !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted keyframes = %#v, want %#v", got, want)
	}
}

// TestP6LazyKeyframeSourceDiscardsExtractionAfterSourceRevisionChange verifies
// that an ffprobe result belongs to the source revision snapshotted before it
// ran. A scan that replaces that source must invalidate existing trickplay
// metadata and prevent the late result from becoming a cache hit or durable
// keyframe record for the replacement source.
func TestP6LazyKeyframeSourceDiscardsExtractionAfterSourceRevisionChange(t *testing.T) {
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	library := p6AddTrickplayLibrary(t, app, "keyframe-revision", true)
	itemID := p6AdapterUUID(t, 0x617)
	sourceA := p6WriteSourceFixture(t, "keyframe-revision-a.mkv", "source A bytes")
	sourceB := p6WriteSourceFixture(t, "keyframe-revision-b.mkv", "source B replacement bytes")
	runtimeA, runtimeB := int64(25_000_000), int64(40_000_000)
	p6UpsertSourceVideo(t, items, itemID, sourceA, &library.CollectionFolderID, nil, &runtimeA)
	snapshot, err := items.DerivedSourceSnapshot(t.Context(), itemID)
	if err != nil {
		t.Fatalf("snapshot source-A revision for trickplay seed: %v", err)
	}
	if err := items.ReplaceTrickplay(t.Context(), store.TrickplayInfo{
		ItemID: itemID, SourceRevision: snapshot.SourceRevision, Width: 640, Height: 360, TileWidth: 160, TileHeight: 90,
		ThumbnailCount: 2, IntervalMS: 2_500, Bandwidth: 84_000, GenerationToken: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatalf("seed source-A trickplay metadata: %v", err)
	}

	started, releasePath := filepath.Join(t.TempDir(), "ffprobe-started"), filepath.Join(t.TempDir(), "ffprobe-release")
	extractor := p6KeyframeExtractor(t, fmt.Sprintf("touch %q\nwhile [ ! -e %q ]; do sleep 0.01; done\nprintf 'packet,0,K_\\npacket,1,K_\\nformat,2.5\\n'\n", started, releasePath))
	source := newLazyKeyframeSource(items, extractor)
	result := make(chan p6KeyframeResult, 1)
	go func() {
		data, found, err := source.Keyframes(context.Background(), itemID, itemID.Dashed())
		result <- p6KeyframeResult{data: data, found: found, err: err}
	}()
	t.Cleanup(func() {
		if err := os.WriteFile(releasePath, nil, 0o600); err != nil && !errors.Is(err, os.ErrExist) {
			t.Errorf("release blocked ffprobe during cleanup: %v", err)
		}
	})
	p6WaitForFile(t, started, "ffprobe did not start after source-A snapshot")

	changed, err := items.Get(t.Context(), itemID)
	if err != nil {
		t.Fatalf("read source-A item before reconcile: %v", err)
	}
	changed.Path, changed.Size, changed.DateModified, changed.RunTimeTicks = &sourceB.path, &sourceB.size, &sourceB.modified, &runtimeB
	if _, err := items.ReconcileScannedPlan(t.Context(), library.CollectionFolderID, []item.Item{changed}, nil, nil); err != nil {
		t.Fatalf("reconcile replacement source B: %v", err)
	}
	if got, err := items.Trickplays(t.Context(), itemID); err != nil || got != nil {
		t.Fatalf("trickplay after source-B reconcile = %#v, %v; want invalidated empty metadata", got, err)
	}
	if _, err := items.Keyframes(t.Context(), itemID); !store.IsNotFound(err) {
		t.Fatalf("keyframes immediately after source-B reconcile error = %v, want not found", err)
	}
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatalf("release blocked source-A ffprobe: %v", err)
	}

	got := p6ReceiveKeyframeResult(t, result)
	if _, err := items.Keyframes(t.Context(), itemID); !store.IsNotFound(err) {
		t.Fatalf("keyframes after late source-A extraction error = %v, want not found", err)
	}
	if got.found || !reflect.DeepEqual(got.data, keyframes.Data{}) {
		t.Fatalf("late source-A keyframe result = %#v, want no stale data after source-B reconcile", got)
	}
}

// TestP6LazyKeyframeSourceDiscardsPathnameReplacementAfterGatedProbe proves
// the descriptor retained for ffprobe cannot make a pathname replacement
// durable. Filesystem staleness uses the ordinary empty cache-miss outcome so
// concurrent HLS waiters neither observe a failed request nor stale data.
func TestP6LazyKeyframeSourceDiscardsPathnameReplacementAfterGatedProbe(t *testing.T) {
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	itemID := p6AdapterUUID(t, 0x619)
	fixture := p6WriteSourceFixture(t, "replaced-during-keyframes.mkv", "initial pinned source bytes")
	p6UpsertSourceVideo(t, items, itemID, fixture, nil, nil, nil)

	started, release := filepath.Join(t.TempDir(), "ffprobe-started"), filepath.Join(t.TempDir(), "ffprobe-release")
	source := newLazyKeyframeSource(items, p6KeyframeExtractor(t, fmt.Sprintf("touch %q\nwhile [ ! -e %q ]; do sleep 0.01; done\nprintf 'packet,0,K_\\nformat,2\\n'\n", started, release)))
	result := make(chan p6KeyframeResult, 1)
	go func() {
		data, found, err := source.Keyframes(context.Background(), itemID, itemID.Dashed())
		result <- p6KeyframeResult{data: data, found: found, err: err}
	}()
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	p6WaitForFile(t, started, "gated ffprobe did not start before pathname replacement")
	p6ReplaceSourceFixture(t, fixture.path, "replacement pathname bytes")
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release ffprobe after pathname replacement: %v", err)
	}

	p6AssertDiscardedStaleKeyframes(t, items, itemID, p6ReceiveKeyframeResult(t, result))
}

// TestP6LazyKeyframeSourceDiscardsInPlaceMutationAfterGatedProbe proves
// source validation detects writes to the already-pinned inode, including a
// same-size rewrite with its original mtime restored.
func TestP6LazyKeyframeSourceDiscardsInPlaceMutationAfterGatedProbe(t *testing.T) {
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	itemID := p6AdapterUUID(t, 0x61a)
	fixture := p6WriteSourceFixture(t, "mutated-during-keyframes.mkv", "initial source bytes")
	p6UpsertSourceVideo(t, items, itemID, fixture, nil, nil, nil)

	started, release := filepath.Join(t.TempDir(), "ffprobe-started"), filepath.Join(t.TempDir(), "ffprobe-release")
	source := newLazyKeyframeSource(items, p6KeyframeExtractor(t, fmt.Sprintf("touch %q\nwhile [ ! -e %q ]; do sleep 0.01; done\nprintf 'packet,0,K_\\nformat,2\\n'\n", started, release)))
	result := make(chan p6KeyframeResult, 1)
	go func() {
		data, found, err := source.Keyframes(context.Background(), itemID, itemID.Dashed())
		result <- p6KeyframeResult{data: data, found: found, err: err}
	}()
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	p6WaitForFile(t, started, "gated ffprobe did not start before in-place mutation")
	p6MutateSourceFixtureInPlace(t, fixture, "mutated source bytes")
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release ffprobe after in-place mutation: %v", err)
	}

	p6AssertDiscardedStaleKeyframes(t, items, itemID, p6ReceiveKeyframeResult(t, result))
}

// TestP6LazyKeyframeSourceCommitsExtractionWhenSourceRevisionIsUnchanged is
// the control for the stale-result regression: a no-op scan after an extractor
// snapshots the source must not discard a result that still matches it.
func TestP6LazyKeyframeSourceCommitsExtractionWhenSourceRevisionIsUnchanged(t *testing.T) {
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	library := p6AddTrickplayLibrary(t, app, "keyframe-revision-control", true)
	itemID := p6AdapterUUID(t, 0x618)
	runtime := int64(25_000_000)
	fixture := p6WriteSourceFixture(t, "keyframe-revision-control.mkv", "unchanged keyframe source")
	p6UpsertSourceVideo(t, items, itemID, fixture, &library.CollectionFolderID, nil, &runtime)

	started, releasePath := filepath.Join(t.TempDir(), "ffprobe-started"), filepath.Join(t.TempDir(), "ffprobe-release")
	extractor := p6KeyframeExtractor(t, fmt.Sprintf("touch %q\nwhile [ ! -e %q ]; do sleep 0.01; done\nprintf 'packet,0,K_\\npacket,1,K_\\nformat,2.5\\n'\n", started, releasePath))
	source := newLazyKeyframeSource(items, extractor)
	result := make(chan p6KeyframeResult, 1)
	go func() {
		data, found, err := source.Keyframes(context.Background(), itemID, itemID.Dashed())
		result <- p6KeyframeResult{data: data, found: found, err: err}
	}()
	t.Cleanup(func() {
		if err := os.WriteFile(releasePath, nil, 0o600); err != nil && !errors.Is(err, os.ErrExist) {
			t.Errorf("release blocked ffprobe during cleanup: %v", err)
		}
	})
	p6WaitForFile(t, started, "ffprobe did not start after unchanged source snapshot")

	unchanged, err := items.Get(t.Context(), itemID)
	if err != nil {
		t.Fatalf("read unchanged source before reconcile: %v", err)
	}
	if _, err := items.ReconcileScannedPlan(t.Context(), library.CollectionFolderID, []item.Item{unchanged}, nil, nil); err != nil {
		t.Fatalf("reconcile unchanged source: %v", err)
	}
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatalf("release unchanged-source ffprobe: %v", err)
	}

	want := keyframes.Data{TotalDurationTicks: 25_000_000, KeyframeTicks: []int64{0, 10_000_000}}
	if got := p6ReceiveKeyframeResult(t, result); got.err != nil || !got.found || !reflect.DeepEqual(got.data, want) {
		t.Fatalf("unchanged-source extraction = %#v, want persisted result %#v", got, want)
	}
	stored, err := items.Keyframes(t.Context(), itemID)
	if err != nil {
		t.Fatalf("read persisted unchanged-source keyframes: %v", err)
	}
	if got := (keyframes.Data{TotalDurationTicks: stored.TotalDurationTicks, KeyframeTicks: stored.KeyframeTicks}); !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted unchanged-source keyframes = %#v, want %#v", got, want)
	}
}

// TestP6LazyKeyframeSourceRetainsExtractionForLiveWaiters verifies caller
// cancellation is local to that caller: the shared probe remains alive until
// its final waiter leaves, allowing a surviving HLS request to persist data.
func TestP6LazyKeyframeSourceRetainsExtractionForLiveWaiters(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	logicalID, sourceID := p6AdapterUUID(t, 0x613), p6AdapterUUID(t, 0x614)
	p6UpsertVideo(t, items, logicalID, "/media/logical.mkv", nil, nil, nil)
	selected := p6WriteSourceFixture(t, "selected.mkv", "surviving keyframe source")
	p6UpsertSourceVideo(t, items, sourceID, selected, nil, nil, nil)
	started, release := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "release")
	extractor := p6KeyframeExtractor(t, fmt.Sprintf("touch %q\nwhile [ ! -e %q ]; do sleep 0.01; done\nprintf 'packet,0,K_\\nformat,2\\n'\n", started, release))
	source := newLazyKeyframeSource(items, extractor)
	firstCtx, cancelFirst := context.WithCancel(t.Context())
	defer cancelFirst()
	first := make(chan p6KeyframeResult, 1)
	go func() {
		data, found, err := source.Keyframes(firstCtx, logicalID, sourceID.Dashed())
		first <- p6KeyframeResult{data: data, found: found, err: err}
	}()
	p6WaitForFile(t, started, "shared ffprobe did not start")
	second := make(chan p6KeyframeResult, 1)
	go func() {
		data, found, err := source.Keyframes(context.Background(), logicalID, sourceID.Dashed())
		second <- p6KeyframeResult{data: data, found: found, err: err}
	}()
	p6WaitForKeyframeWaiters(t, source, sourceID, 2)
	cancelFirst()
	if got := p6ReceiveKeyframeResult(t, first); !errors.Is(got.err, context.Canceled) || got.found {
		t.Fatalf("canceled first waiter = %#v, want context.Canceled without data", got)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release probe for surviving waiter: %v", err)
	}
	want := keyframes.Data{TotalDurationTicks: 20_000_000, KeyframeTicks: []int64{0}}
	if got := p6ReceiveKeyframeResult(t, second); got.err != nil || !got.found || !reflect.DeepEqual(got.data, want) {
		t.Fatalf("surviving waiter = %#v, want persisted extraction %#v", got, want)
	}
	stored, err := items.Keyframes(t.Context(), sourceID)
	if err != nil {
		t.Fatalf("read surviving waiter persistence: %v", err)
	}
	if got := (keyframes.Data{TotalDurationTicks: stored.TotalDurationTicks, KeyframeTicks: stored.KeyframeTicks}); !reflect.DeepEqual(got, want) {
		t.Fatalf("surviving waiter persistence = %#v, want %#v", got, want)
	}
}

// TestP6LazyKeyframeSourceFinishesAndPersistsAfterLastWaiterLeaves verifies
// an extraction outlives every caller: a retry finds the persisted result and
// does not start again from zero.
func TestP6LazyKeyframeSourceFinishesAndPersistsAfterLastWaiterLeaves(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	itemID := p6AdapterUUID(t, 0x615)
	fixture := p6WriteSourceFixture(t, "abandoned.mkv", "abandoned keyframe source")
	p6UpsertSourceVideo(t, items, itemID, fixture, nil, nil, nil)
	started, release := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "release")
	countPath := filepath.Join(t.TempDir(), "count")
	extractor := p6KeyframeExtractor(t, fmt.Sprintf("printf x >> %q\ntouch %q\nwhile [ ! -e %q ]; do sleep 0.01; done\nprintf 'packet,0,K_\\npacket,4,K_\\nformat,6\\n'\n", countPath, started, release))
	source := newLazyKeyframeSource(items, extractor)
	t.Cleanup(func() {
		if err := source.Close(context.Background()); err != nil {
			t.Errorf("close keyframe source: %v", err)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan p6KeyframeResult, 1)
	go func() {
		data, found, err := source.Keyframes(ctx, itemID, itemID.Dashed())
		result <- p6KeyframeResult{data: data, found: found, err: err}
	}()
	p6WaitForFile(t, started, "abandoned extraction did not start")
	cancel()
	if got := p6ReceiveKeyframeResult(t, result); !errors.Is(got.err, context.Canceled) || got.found {
		t.Fatalf("last canceled waiter = %#v, want context.Canceled without data", got)
	}
	p6WaitForKeyframeWaiters(t, source, itemID, 0)
	source.mu.Lock()
	call := source.inFlight[itemID]
	source.mu.Unlock()
	if call == nil || call.ctx.Err() != nil {
		t.Fatalf("extraction after last waiter left = %#v, want a live in-flight extraction", call)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release abandoned extraction: %v", err)
	}
	p6WaitForNoKeyframeExtraction(t, source, itemID)
	want := keyframes.Data{TotalDurationTicks: 60_000_000, KeyframeTicks: []int64{0, 40_000_000}}
	got, found, err := source.Keyframes(t.Context(), itemID, itemID.Dashed())
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("retry after abandoned extraction = %#v, %t, %v; want persisted %#v", got, found, err, want)
	}
	if count, err := os.ReadFile(countPath); err != nil || string(count) != "x" {
		t.Fatalf("extraction invocations = %q, %v; want exactly one", count, err)
	}
}

// TestP6LazyKeyframeSourceCloseCancelsAndReapsAbandonedExtraction verifies the
// owner's shutdown path: Close cancels an extraction no caller waits for and
// returns only after its process is reaped and its worker has stopped.
func TestP6LazyKeyframeSourceCloseCancelsAndReapsAbandonedExtraction(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	itemID := p6AdapterUUID(t, 0x616)
	fixture := p6WriteSourceFixture(t, "closed.mkv", "closed keyframe source")
	p6UpsertSourceVideo(t, items, itemID, fixture, nil, nil, nil)
	started, pidPath := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "pid")
	extractor := p6KeyframeExtractor(t, fmt.Sprintf("printf '%%s' \"$$\" > %q\ntouch %q\nwhile :; do sleep 0.01; done\n", pidPath, started))
	source := newLazyKeyframeSource(items, extractor)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan p6KeyframeResult, 1)
	go func() {
		data, found, err := source.Keyframes(ctx, itemID, itemID.Dashed())
		result <- p6KeyframeResult{data: data, found: found, err: err}
	}()
	p6WaitForFile(t, started, "abandoned extraction did not start")
	cancel()
	p6ReceiveKeyframeResult(t, result)
	closeCtx, cancelClose := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelClose()
	if err := source.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	p6WaitForNoKeyframeExtraction(t, source, itemID)
	rawPID, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read abandoned extraction PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatalf("parse abandoned extraction PID %q: %v", rawPID, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("abandoned extraction PID %d remains live after Close: %v", pid, err)
	}
	if _, found, err := source.Keyframes(t.Context(), itemID, itemID.Dashed()); !errors.Is(err, errLazyKeyframeSourceClosed) || found {
		t.Fatalf("Keyframes() after Close = %t, %v; want errLazyKeyframeSourceClosed", found, err)
	}
}

// TestP6LazyKeyframeSourceReadsMatroskaCuesAndTreatsMissingCuesAsMiss covers
// the production extractor: a generated MKV with Cues persists its keyframes,
// one without Cues is a miss that a playlist answers with equal segments.
func TestP6LazyKeyframeSourceReadsMatroskaCuesAndTreatsMissingCuesAsMiss(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	source := newLazyKeyframeSource(items, keyframes.MatroskaExtractor{})
	t.Cleanup(func() {
		if err := source.Close(context.Background()); err != nil {
			t.Errorf("close keyframe source: %v", err)
		}
	})
	generate := func(name string, seekable bool) p6SourceFixture {
		path := filepath.Join(t.TempDir(), name)
		args := []string{"-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-t", "6",
			"-c:v", "libx264", "-preset", "ultrafast", "-g", "25", "-f", "matroska"}
		command := exec.CommandContext(t.Context(), ffmpegPath, append(args, path)...)
		if !seekable {
			output, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			command = exec.CommandContext(t.Context(), ffmpegPath, append(args, "pipe:1")...)
			command.Stdout = output
		}
		if err := command.Run(); err != nil {
			t.Fatalf("generate %s: %v", name, err)
		}
		return p6StatSourceFixture(t, path)
	}

	withCues, withoutCues := p6AdapterUUID(t, 0x617), p6AdapterUUID(t, 0x618)
	p6UpsertSourceVideo(t, items, withCues, generate("cues.mkv", true), nil, nil, nil)
	p6UpsertSourceVideo(t, items, withoutCues, generate("nocues.mkv", false), nil, nil, nil)

	got, found, err := source.Keyframes(t.Context(), withCues, withCues.Dashed())
	want := []int64{0, 10_000_000, 20_000_000, 30_000_000, 40_000_000, 50_000_000}
	if err != nil || !found || !reflect.DeepEqual(got.KeyframeTicks, want) {
		t.Fatalf("Keyframes(with Cues) = %#v, %t, %v; want keyframes %v", got, found, err, want)
	}
	if stored, err := items.Keyframes(t.Context(), withCues); err != nil || !reflect.DeepEqual(stored.KeyframeTicks, want) {
		t.Fatalf("persisted Cues keyframes = %#v, %v; want %v", stored, err, want)
	}
	if got, found, err := source.Keyframes(t.Context(), withoutCues, withoutCues.Dashed()); err == nil || found {
		t.Fatalf("Keyframes(without Cues) = %#v, %t, %v; want an extraction miss with error", got, found, err)
	}
	if _, err := items.Keyframes(t.Context(), withoutCues); !store.IsNotFound(err) {
		t.Fatalf("persisted keyframes without Cues error = %v, want not found", err)
	}
}

// TestP6LazyKeyframeSourceSurfacesReadExtractionAndPersistenceFailures verifies
// the adapter never turns a real store, ffprobe, or durable-write failure into
// a successful empty cache response.
func TestP6LazyKeyframeSourceSurfacesReadExtractionAndPersistenceFailures(t *testing.T) {
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	itemID := p6AdapterUUID(t, 0x621)
	fixture := p6WriteSourceFixture(t, "input.mkv", "keyframe failure source")
	p6UpsertSourceVideo(t, items, itemID, fixture, nil, nil, nil)

	t.Run("canceled cache read is returned without extraction", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		data, found, err := newLazyKeyframeSource(items, p6KeyframeExtractor(t, "exit 19\n")).Keyframes(ctx, itemID, itemID.Dashed())
		if !errors.Is(err, context.Canceled) || found || !reflect.DeepEqual(data, keyframes.Data{}) {
			t.Fatalf("canceled keyframe cache read = %#v, %t, %v; want empty, false, context.Canceled", data, found, err)
		}
	})

	t.Run("ffprobe failure is returned", func(t *testing.T) {
		data, found, err := newLazyKeyframeSource(items, p6KeyframeExtractor(t, "echo failed >&2\nexit 19\n")).Keyframes(t.Context(), itemID, itemID.Dashed())
		if err == nil || found || !reflect.DeepEqual(data, keyframes.Data{}) {
			t.Fatalf("failed extraction = %#v, %t, %v; want empty, false, error", data, found, err)
		}
	})

	t.Run("source deletion before durable replacement is discarded", func(t *testing.T) {
		deleteID := p6AdapterUUID(t, 0x622)
		deleted := p6WriteSourceFixture(t, "deleted-before-persist.mkv", "deleted keyframe source")
		p6UpsertSourceVideo(t, items, deleteID, deleted, nil, nil, nil)
		started, release := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "release")
		extractor := p6KeyframeExtractor(t, fmt.Sprintf("touch %q\nwhile [ ! -e %q ]; do sleep 0.01; done\nprintf 'packet,0,K_\\nformat,2\\n'\n", started, release))
		source := newLazyKeyframeSource(items, extractor)
		result := make(chan p6KeyframeResult, 1)
		go func() {
			data, found, err := source.Keyframes(context.Background(), deleteID, deleteID.Dashed())
			result <- p6KeyframeResult{data: data, found: found, err: err}
		}()
		p6WaitForKeyframeExtraction(t, source, deleteID)
		p6WaitForFile(t, started, "ffprobe did not start after source lookup")
		if err := items.Delete(t.Context(), deleteID); err != nil {
			t.Fatalf("delete source before durable replacement: %v", err)
		}
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Fatalf("release ffprobe after delete: %v", err)
		}
		got := p6ReceiveKeyframeResult(t, result)
		if got.err != nil || got.found || !reflect.DeepEqual(got.data, keyframes.Data{}) {
			t.Fatalf("source deletion before durable replacement = %#v, want empty, false, nil", got)
		}
	})
}

// TestP6TrickplayAdaptersProjectStoredMetadataAndPlaybackAccess verifies the
// application adapters retain every persisted rendition fact for the reader,
// advertise the HLS projection in width order, and expose normal-user and
// administrator playback checks through the store-backed policy surface.
func TestP6TrickplayAdaptersProjectStoredMetadataAndPlaybackAccess(t *testing.T) {
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	itemID := p6AdapterUUID(t, 0x631)
	p6UpsertVideo(t, items, itemID, "/media/trickplay.mkv", nil, nil, nil)
	metadata := []trickplay.Metadata{
		{ItemID: itemID, Width: 640, Height: 360, TileWidth: 10, TileHeight: 8, ThumbnailCount: 23, Interval: 5_000, Bandwidth: 71_000, GenerationToken: strings.Repeat("6", 64)},
		{ItemID: itemID, Width: 320, Height: 180, TileWidth: 8, TileHeight: 6, ThumbnailCount: 19, Interval: 10_000, Bandwidth: 36_000, GenerationToken: strings.Repeat("3", 64)},
	}
	snapshot, err := items.DerivedSourceSnapshot(t.Context(), itemID)
	if err != nil {
		t.Fatalf("snapshot trickplay source revision: %v", err)
	}
	adapter := trickplayStoreAdapter{items: items}
	for index := range metadata {
		metadata[index].SourceRevision = snapshot.SourceRevision
		if err := adapter.ReplaceTrickplay(t.Context(), metadata[index]); err != nil {
			t.Fatalf("persist trickplay %d: %v", metadata[index].Width, err)
		}
	}
	for index := range metadata {
		metadata[index].SourceRevision = ""
	}
	got, err := adapter.Trickplay(t.Context(), itemID, 640)
	if err != nil {
		t.Fatalf("read full trickplay metadata: %v", err)
	}
	if !reflect.DeepEqual(got, metadata[0]) {
		t.Fatalf("reader metadata = %#v, want %#v", got, metadata[0])
	}
	recovered, err := adapter.ReadTrickplay(t.Context(), itemID, 640)
	if err != nil {
		t.Fatalf("recover full persisted trickplay metadata: %v", err)
	}
	if recovered == nil || !reflect.DeepEqual(*recovered, metadata[0]) {
		t.Fatalf("recovered persisted metadata = %#v, want %#v", recovered, metadata[0])
	}
	missing, err := adapter.ReadTrickplay(t.Context(), itemID, 480)
	if err != nil || missing != nil {
		t.Fatalf("recover missing persisted metadata = %#v, %v; want nil, nil", missing, err)
	}
	playlist, err := adapter.TrickplayMetadata(t.Context(), itemID)
	if err != nil {
		t.Fatalf("read HLS trickplay projection: %v", err)
	}
	wantPlaylist := []playback.HLSTrickplayMetadata{{Width: 320, Height: 180, Bandwidth: 36_000}, {Width: 640, Height: 360, Bandwidth: 71_000}}
	if !reflect.DeepEqual(playlist, wantPlaylist) {
		t.Fatalf("HLS trickplay projection = %#v, want %#v", playlist, wantPlaylist)
	}

	user, err := app.UserService().Create(t.Context(), "p6-trickplay-member", "password")
	if err != nil {
		t.Fatalf("create playback member: %v", err)
	}
	visibility := trickplayVisibilityAdapter{access: store.NewPlaybackItemAccessReader(items, app.UserService())}
	if visible, err := visibility.Visible(t.Context(), user.ID, itemID, false); err != nil || !visible {
		t.Fatalf("member playback visibility = %t, %v; want true, nil", visible, err)
	}
	if err := app.UserService().UpdatePolicy(t.Context(), user.ID, []byte(`{"EnableAllFolders":false,"EnabledFolders":[]}`)); err != nil {
		t.Fatalf("restrict member playback folders: %v", err)
	}
	if visible, err := visibility.Visible(t.Context(), user.ID, itemID, false); err != nil || visible {
		t.Fatalf("restricted member playback visibility = %t, %v; want false, nil", visible, err)
	}
	if visible, err := visibility.Visible(context.Background(), dto.NilUUID, itemID, true); err != nil || !visible {
		t.Fatalf("administrator playback visibility = %t, %v; want true, nil without user lookup", visible, err)
	}
}

// TestP6TrickplayTaskGeneratorPersistsConvertedGenerationMetadata verifies the
// scheduled-task adapter carries candidate identity, source media facts, and
// generation geometry through the real generator into its durable rendition.
func TestP6TrickplayTaskGeneratorPersistsConvertedGenerationMetadata(t *testing.T) {
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	itemID := p6AdapterUUID(t, 0x635)
	fixture := p6WriteSourceFixture(t, "generator-input.mkv", "generator source")
	p6UpsertSourceVideo(t, items, itemID, fixture, nil, nil, nil)
	runtime, videoWidth := int64(100_000_000), 319
	snapshot, err := items.DerivedSourceSnapshot(t.Context(), itemID)
	if err != nil {
		t.Fatalf("snapshot generator source revision: %v", err)
	}
	generator, err := trickplay.NewGenerator(trickplay.GeneratorConfig{
		Root:        filepath.Join(t.TempDir(), "trickplay"),
		Extractor:   p6TaskGeneratorFrameExtractor{sourcePath: fixture.path, width: 318, interval: 10_000},
		Composer:    p6TaskGeneratorTileComposer{cellWidth: 318, cellHeight: 180, columns: 2, rows: 1, quality: 73},
		Store:       trickplayStoreAdapter{items: items},
		Reader:      trickplayStoreAdapter{items: items},
		Publication: trickplay.NewPublicationCoordinator(),
	})
	if err != nil {
		t.Fatalf("create adapter generator: %v", err)
	}
	adapter := trickplayTaskGenerator{generator: generator}
	if err := adapter.Generate(t.Context(), tasks.Generation{
		Candidate:      tasks.Candidate{ID: itemID, SourcePath: fixture.path, SourceRevision: snapshot.SourceRevision, SourceSize: snapshot.Size, SourceModified: snapshot.DateModified, SourceInode: snapshot.FileInode, SourceChangedAt: snapshot.FileChangedAt, VideoWidth: &videoWidth, RunTimeTicks: &runtime},
		SourceRevision: snapshot.SourceRevision,
		RequestedWidth: 320, TileWidth: 2, TileHeight: 1, IntervalMilliseconds: 10_000, Quality: 73,
	}); err != nil {
		t.Fatalf("generate converted scheduled rendition: %v", err)
	}
	got, err := trickplayStoreAdapter{items: items}.Trickplay(t.Context(), itemID, 318)
	if err != nil {
		t.Fatalf("read generated rendition: %v", err)
	}
	if len(got.GenerationToken) != 64 {
		t.Fatalf("persisted generation token length = %d, want 64", len(got.GenerationToken))
	}
	want := trickplay.Metadata{ItemID: itemID, Width: 318, Height: 180, TileWidth: 2, TileHeight: 1, ThumbnailCount: 1, Interval: 10_000, Bandwidth: 40, GenerationToken: got.GenerationToken}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted generated metadata = %#v, want %#v", got, want)
	}
}

func TestP6TrickplayTaskSourcePreservesVideoReferenceFrameRates(t *testing.T) {
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	libraryID := p6AddTrickplayLibrary(t, app, "frame-rate", true).CollectionFolderID
	ownerID := p6AdapterUUID(t, 0x64a1)
	width := 1_920
	runtime := int64(100_000_000)
	source := trickplayTaskSource{items: items, options: tasks.Options{IntervalMilliseconds: 10_000, Widths: []int{320}}}
	libraries := map[dto.UUID]library.LibraryOptions{libraryID: {EnableTrickplayImageExtraction: true}}

	for index, tc := range []struct {
		name string
		rate float32
	}{
		{name: "23.976fps", rate: 23.976},
		{name: "24fps", rate: 24},
		{name: "60fps", rate: 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := p6AdapterUUID(t, uint64(0x64b+index))
			path := fmt.Sprintf("/media/frame-rate-%s.mkv", tc.name)
			p6UpsertTaskVideo(t, items, id, "P6 frame-rate "+tc.name, path, &libraryID, &width, &runtime, p6VideoMediaType(), item.SourceTypeLibrary, false, false, &ownerID)
			if err := items.ReplaceStreams(t.Context(), id, []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, AverageFrameRate: &tc.rate}}); err != nil {
				t.Fatalf("persist %s source stream: %v", tc.name, err)
			}
			snapshot, err := items.DerivedSourceSnapshot(t.Context(), id)
			if err != nil {
				t.Fatalf("snapshot %s source revision: %v", tc.name, err)
			}

			candidate, err := source.candidate(t.Context(), item.Item{ID: id, Path: &path, Width: &width, RunTimeTicks: &runtime, TopParentID: &libraryID}, libraries)
			if err != nil {
				t.Fatalf("map %s candidate: %v", tc.name, err)
			}
			if candidate.ReferenceFrameRate == nil {
				t.Fatalf("candidate %s reference frame rate = nil, want %v", tc.name, tc.rate)
			}
			if got, want := *candidate.ReferenceFrameRate, tc.rate; got != want {
				t.Fatalf("candidate %s reference frame rate = %v, want %v", tc.name, got, want)
			}
			if candidate.SourceRevision != snapshot.SourceRevision {
				t.Fatalf("candidate %s source revision = %q, want %q", tc.name, candidate.SourceRevision, snapshot.SourceRevision)
			}
		})
	}
}

// TestP6TrickplayTaskSourceLeavesIncompleteFingerprintCandidatesInert proves
// scheduled snapshots do not reuse a rendition that was persisted against a
// legacy or partial source identity. A later scan must backfill both facts
// before that candidate becomes eligible.
func TestP6TrickplayTaskSourceLeavesIncompleteFingerprintCandidatesInert(t *testing.T) {
	for index, tc := range []struct {
		name   string
		mutate func(*item.Item)
	}{
		{name: "neither", mutate: func(value *item.Item) { value.FileInode, value.FileChangedAt = nil, nil }},
		{name: "inode only", mutate: func(value *item.Item) { value.FileChangedAt = nil }},
		{name: "changed-at only", mutate: func(value *item.Item) { value.FileInode = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newP6AdapterApplication(t)
			items := store.NewItemStore(app.stores)
			library := p6AddTrickplayLibrary(t, app, "incomplete-snapshot-"+tc.name, true)
			itemID := p6AdapterUUID(t, uint64(0x651+index))
			width, runtime := 640, int64(100_000_000)
			fixture := p6WriteSourceFixture(t, "incomplete-snapshot.mkv", "scheduled legacy source")
			p6UpsertSourceVideo(t, items, itemID, fixture, &library.CollectionFolderID, &width, &runtime)
			complete, err := items.DerivedSourceSnapshot(t.Context(), itemID)
			if err != nil {
				t.Fatalf("snapshot complete %s scheduled source: %v", tc.name, err)
			}
			if err := items.ReplaceTrickplay(t.Context(), store.TrickplayInfo{ItemID: itemID, SourceRevision: complete.SourceRevision, Width: 320, Height: 180, TileWidth: 10, TileHeight: 8, ThumbnailCount: 1, IntervalMS: 10_000, Bandwidth: 1, GenerationToken: strings.Repeat("a", 64)}); err != nil {
				t.Fatalf("seed %s legacy rendition: %v", tc.name, err)
			}
			legacy, err := items.Get(t.Context(), itemID)
			if err != nil {
				t.Fatalf("read %s scheduled source: %v", tc.name, err)
			}
			tc.mutate(&legacy)
			if err := items.Upsert(t.Context(), legacy); err != nil {
				t.Fatalf("persist %s scheduled fingerprint: %v", tc.name, err)
			}

			snapshot, err := (trickplayTaskSource{items: items, topology: app.topology, options: tasks.Options{Widths: []int{320}, IntervalMilliseconds: 10_000}}).Snapshot(t.Context())
			if err != nil {
				t.Fatalf("snapshot %s scheduled candidate: %v", tc.name, err)
			}
			if got, want := snapshot.Count(), 1; got != want {
				t.Fatalf("%s scheduled candidate count = %d, want %d", tc.name, got, want)
			}
			page, err := snapshot.Page(t.Context(), 0, 1)
			if err != nil || len(page) != 1 {
				t.Fatalf("%s scheduled candidate page = %#v, %v; want one candidate", tc.name, page, err)
			}
			candidate := page[0]
			if candidate.Generate || len(candidate.ExistingWidths) != 0 {
				t.Fatalf("%s incomplete scheduled candidate = %#v, want non-generating without stale rendition widths", tc.name, candidate)
			}
		})
	}
}

// TestP6TrickplayTaskSourceSnapshotsEligibleLibraryVideos verifies scheduled
// generation takes a stable, deterministic snapshot of only concrete library
// videos and preserves library policy, runtime eligibility, and the persisted
// capped/even effective rendition width consumed by tasks.
func TestP6TrickplayTaskSourceSnapshotsEligibleLibraryVideos(t *testing.T) {
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	enabled := p6AddTrickplayLibrary(t, app, "enabled", true)
	disabled := p6AddTrickplayLibrary(t, app, "disabled", false)
	runtime := int64(10_000 * 10_000)
	shortRuntime := runtime - 1
	smallWidth, wideWidth := 319, 640
	ownerID := p6AdapterUUID(t, 0x641)
	eligibleID := p6AdapterUUID(t, 0x642)
	disabledID := p6AdapterUUID(t, 0x643)
	shortID := p6AdapterUUID(t, 0x644)
	for _, value := range []struct {
		id        dto.UUID
		name      string
		path      string
		topParent *dto.UUID
		width     *int
		runtime   *int64
		mediaType *item.MediaType
		source    item.SourceType
		folder    bool
		virtual   bool
	}{
		{eligibleID, "A eligible small video", "/media/eligible.mkv", &enabled.CollectionFolderID, &smallWidth, &runtime, p6VideoMediaType(), item.SourceTypeLibrary, false, false},
		{disabledID, "B disabled-library video", "/media/disabled.mkv", &disabled.CollectionFolderID, &wideWidth, &runtime, p6VideoMediaType(), item.SourceTypeLibrary, false, false},
		{shortID, "C too-short video", "/media/short.mkv", &enabled.CollectionFolderID, &wideWidth, &shortRuntime, p6VideoMediaType(), item.SourceTypeLibrary, false, false},
		{p6AdapterUUID(t, 0x645), "D audio", "/media/audio.mka", &enabled.CollectionFolderID, &wideWidth, &runtime, p6AudioMediaType(), item.SourceTypeLibrary, false, false},
		{p6AdapterUUID(t, 0x646), "E virtual video", "/media/virtual.mkv", &enabled.CollectionFolderID, &wideWidth, &runtime, p6VideoMediaType(), item.SourceTypeLibrary, false, true},
		{p6AdapterUUID(t, 0x647), "F folder video", "/media/folder", &enabled.CollectionFolderID, &wideWidth, &runtime, p6VideoMediaType(), item.SourceTypeLibrary, true, false},
		{p6AdapterUUID(t, 0x648), "G live video", "/media/live.mkv", &enabled.CollectionFolderID, &wideWidth, &runtime, p6VideoMediaType(), item.SourceTypeLiveTV, false, false},
	} {
		p6UpsertTaskVideo(t, items, value.id, value.name, value.path, value.topParent, value.width, value.runtime, value.mediaType, value.source, value.folder, value.virtual, &ownerID)
	}
	eligibleSource, err := items.DerivedSourceSnapshot(t.Context(), eligibleID)
	if err != nil {
		t.Fatalf("snapshot eligible source revision: %v", err)
	}
	if err := items.ReplaceTrickplay(t.Context(), store.TrickplayInfo{ItemID: eligibleID, SourceRevision: eligibleSource.SourceRevision, Width: 318, Height: 180, TileWidth: 10, TileHeight: 8, ThumbnailCount: 1, IntervalMS: 10_000, Bandwidth: 1, GenerationToken: strings.Repeat("e", 64)}); err != nil {
		t.Fatalf("persist effective-width rendition: %v", err)
	}

	source := trickplayTaskSource{items: items, topology: app.topology, options: tasks.Options{IntervalMilliseconds: 10_000, Widths: []int{320, 640}}}
	snapshot, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("snapshot scheduled trickplay candidates: %v", err)
	}
	if got, want := snapshot.Count(), 3; got != want {
		t.Fatalf("candidate count = %d, want only concrete library videos %d", got, want)
	}
	firstPage, err := snapshot.Page(t.Context(), 0, 2)
	if err != nil {
		t.Fatalf("first candidate page: %v", err)
	}
	secondPage, err := snapshot.Page(t.Context(), 2, 2)
	if err != nil {
		t.Fatalf("second candidate page: %v", err)
	}
	got := append(append([]tasks.Candidate(nil), firstPage...), secondPage...)
	if gotIDs := []dto.UUID{got[0].ID, got[1].ID, got[2].ID}; !reflect.DeepEqual(gotIDs, []dto.UUID{eligibleID, disabledID, shortID}) {
		t.Fatalf("deterministic candidate order = %v, want eligible/disabled/short", gotIDs)
	}
	if !got[0].Generate || !reflect.DeepEqual(got[0].ExistingWidths, []int{318}) || got[0].VideoWidth == nil || *got[0].VideoWidth != smallWidth {
		t.Fatalf("eligible small-video candidate = %#v, want generation with the shared persisted effective width 318", got[0])
	}
	if got[1].Generate || got[2].Generate {
		t.Fatalf("library policy/runtime-ineligible candidates generated = %#v / %#v, want disabled", got[1], got[2])
	}
	if page, err := snapshot.Page(t.Context(), -1, 1); err != nil || page != nil {
		t.Fatalf("negative page = %#v, %v; want nil, nil", page, err)
	}
	if page, err := snapshot.Page(t.Context(), 0, 0); err != nil || len(page) != 0 {
		t.Fatalf("empty page = %#v, %v; want empty, nil", page, err)
	}
}

type p6KeyframeResult struct {
	data  keyframes.Data
	found bool
	err   error
}

type p6TaskGeneratorFrameExtractor struct {
	sourcePath string
	width      int
	interval   int64
}

func (extractor p6TaskGeneratorFrameExtractor) Extract(ctx context.Context, request trickplay.FrameRequest) (trickplay.FrameExtraction, error) {
	if err := ctx.Err(); err != nil {
		return trickplay.FrameExtraction{}, err
	}
	if request.Source == nil || request.Source.Name() != extractor.sourcePath || request.Width != extractor.width || request.IntervalMilliseconds != extractor.interval {
		return trickplay.FrameExtraction{}, fmt.Errorf("unexpected converted frame request: %#v", request)
	}
	frame, err := request.Output.CreateExclusive("00000001.jpg", 0o600)
	if err != nil {
		return trickplay.FrameExtraction{}, err
	}
	if _, err := frame.Write([]byte("frame")); err != nil {
		_ = frame.Close()
		return trickplay.FrameExtraction{}, err
	}
	if err := frame.Close(); err != nil {
		return trickplay.FrameExtraction{}, err
	}
	return trickplay.FrameExtraction{Names: []string{"00000001.jpg"}, Height: 180}, nil
}

type p6TaskGeneratorTileComposer struct {
	cellWidth, cellHeight int
	columns, rows         int
	quality               int
}

func (composer p6TaskGeneratorTileComposer) Compose(ctx context.Context, request images.TileRequest) (images.TileResult, error) {
	if err := ctx.Err(); err != nil {
		return images.TileResult{}, err
	}
	if request.CellWidth != composer.cellWidth || request.CellHeight != composer.cellHeight || request.Columns != composer.columns || request.Rows != composer.rows || request.JPEGQuality != composer.quality || len(request.SourceNames) != 1 {
		return images.TileResult{}, fmt.Errorf("unexpected converted tile request: %#v", request)
	}
	encoded := make([]byte, 100)
	output, err := request.OutputDir.CreateExclusive(request.OutputName, 0o600)
	if err != nil {
		return images.TileResult{}, err
	}
	if _, err := output.Write(encoded); err != nil {
		_ = output.Close()
		return images.TileResult{}, err
	}
	if err := output.Close(); err != nil {
		return images.TileResult{}, err
	}
	return images.TileResult{Path: request.OutputName, ByteSize: int64(len(encoded))}, nil
}

func newP6AdapterApplication(t *testing.T) *application {
	t.Helper()
	app, err := newApplication(t.Context(), filepath.Join(t.TempDir(), "program-data"))
	if err != nil {
		t.Fatalf("create adapter application: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("close adapter application: %v", err)
		}
	})
	return app
}

func p6AdapterUUID(t *testing.T, value uint64) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("00000000-0000-0000-0000-%012x", value))
	if err != nil {
		t.Fatalf("parse deterministic test UUID: %v", err)
	}
	return id
}

type p6SourceFixture struct {
	path      string
	size      int64
	modified  time.Time
	inode     int64
	changedAt time.Time
}

func p6WriteSourceFixture(t *testing.T, name, contents string) p6SourceFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write source fixture %q: %v", path, err)
	}
	return p6StatSourceFixture(t, path)
}

func p6ReplaceSourceFixture(t *testing.T, path, contents string) p6SourceFixture {
	t.Helper()
	replacement := filepath.Join(filepath.Dir(path), ".replacement-"+filepath.Base(path))
	if err := os.WriteFile(replacement, []byte(contents), 0o600); err != nil {
		t.Fatalf("write replacement source fixture %q: %v", replacement, err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("replace source fixture %q: %v", path, err)
	}
	return p6StatSourceFixture(t, path)
}

func p6StatSourceFixture(t *testing.T, path string) p6SourceFixture {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat source fixture %q: %v", path, err)
	}
	stat := reflect.ValueOf(info.Sys())
	if stat.Kind() != reflect.Pointer || stat.IsNil() {
		t.Fatalf("source fixture stat %q = %T, want pointer stat", path, info.Sys())
	}
	fields := stat.Elem()
	inode := fields.FieldByName("Ino")
	changed := fields.FieldByName("Ctim")
	if !changed.IsValid() {
		changed = fields.FieldByName("Ctimespec")
	}
	if !inode.IsValid() || !changed.IsValid() {
		t.Fatalf("source fixture stat %q lacks inode/changed-at fields", path)
	}
	seconds, nanoseconds := changed.FieldByName("Sec"), changed.FieldByName("Nsec")
	if !seconds.IsValid() || !nanoseconds.IsValid() {
		t.Fatalf("source fixture changed-at stat %q lacks seconds/nanoseconds", path)
	}
	return p6SourceFixture{path: path, size: info.Size(), modified: info.ModTime(), inode: p6StatInteger(t, inode), changedAt: time.Unix(p6StatInteger(t, seconds), p6StatInteger(t, nanoseconds)).UTC()}
}

func p6StatInteger(t *testing.T, value reflect.Value) int64 {
	t.Helper()
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(value.Uint())
	default:
		t.Fatalf("source fixture stat value kind = %s, want an integer", value.Kind())
		return 0
	}
}

func p6MutateSourceFixtureInPlace(t *testing.T, source p6SourceFixture, contents string) {
	t.Helper()
	if int64(len(contents)) != source.size {
		t.Fatalf("in-place mutation length = %d, want fixture size %d", len(contents), source.size)
	}
	if err := os.WriteFile(source.path, []byte(contents), 0o600); err != nil {
		t.Fatalf("mutate source fixture %q: %v", source.path, err)
	}
	if err := os.Chtimes(source.path, source.modified, source.modified); err != nil {
		t.Fatalf("restore source fixture mtime %q: %v", source.path, err)
	}
}

func p6AssertDiscardedStaleKeyframes(t *testing.T, items *store.ItemStore, itemID dto.UUID, got p6KeyframeResult) {
	t.Helper()
	if got.err != nil || got.found || !reflect.DeepEqual(got.data, keyframes.Data{}) {
		t.Fatalf("stale filesystem keyframes = %#v, want empty, false, nil", got)
	}
	if _, err := items.Keyframes(t.Context(), itemID); !store.IsNotFound(err) {
		t.Fatalf("stale filesystem keyframes durable row error = %v, want not found", err)
	}
}

func p6UpsertSourceVideo(t *testing.T, items *store.ItemStore, id dto.UUID, source p6SourceFixture, topParent *dto.UUID, width *int, runtime *int64) {
	t.Helper()
	name, mediaType := "P6 video "+id.String(), item.MediaTypeVideo
	container := "mkv"
	if err := items.Upsert(t.Context(), item.Item{ID: id, Kind: item.KindMovie, IsMovie: true, Name: &name, Path: &source.path, Container: &container, ParentID: topParent, TopParentID: topParent, Width: width, RunTimeTicks: runtime, Size: &source.size, DateModified: &source.modified, FileInode: &source.inode, FileChangedAt: &source.changedAt, MediaType: &mediaType, SourceType: item.SourceTypeLibrary}); err != nil {
		t.Fatalf("upsert P6 source candidate %s: %v", id, err)
	}
}

func p6UpsertVideo(t *testing.T, items *store.ItemStore, id dto.UUID, path string, topParent *dto.UUID, width *int, runtime *int64) {
	t.Helper()
	name, mediaType := "P6 video "+id.String(), item.MediaTypeVideo
	p6UpsertTaskVideo(t, items, id, name, path, topParent, width, runtime, &mediaType, item.SourceTypeLibrary, false, false, nil)
}

func p6UpsertTaskVideo(t *testing.T, items *store.ItemStore, id dto.UUID, name, path string, topParent *dto.UUID, width *int, runtime *int64, mediaType *item.MediaType, source item.SourceType, folder, virtual bool, owner *dto.UUID) {
	t.Helper()
	container := "mkv"
	inode := int64(id[15]) + 1
	changedAt := time.Date(2026, time.July, 28, 12, 0, int(id[14]), 0, time.UTC)
	if err := items.Upsert(t.Context(), item.Item{ID: id, Kind: item.KindMovie, IsMovie: true, Name: &name, Path: &path, Container: &container, ParentID: topParent, TopParentID: topParent, Width: width, RunTimeTicks: runtime, FileInode: &inode, FileChangedAt: &changedAt, MediaType: mediaType, SourceType: source, IsFolder: folder, IsVirtualItem: virtual, OwnerID: owner}); err != nil {
		t.Fatalf("upsert P6 candidate %s: %v", id, err)
	}
}

func p6AddTrickplayLibrary(t *testing.T, app *application, name string, enabled bool) library.VirtualFolder {
	t.Helper()
	root := t.TempDir()
	folder, err := app.topology.AddVirtualFolder(t.Context(), name, "movies", library.LibraryOptions{EnableTrickplayImageExtraction: enabled, PathInfos: []library.MediaPathInfo{{Path: root}}})
	if err != nil {
		t.Fatalf("add %s library: %v", name, err)
	}
	items := store.NewItemStore(app.stores)
	collectionName := "P6 " + name + " library"
	if err := items.Upsert(t.Context(), item.Item{ID: folder.CollectionFolderID, Kind: item.KindCollectionFolder, IsFolder: true, Name: &collectionName}); err != nil {
		t.Fatalf("persist %s library root: %v", name, err)
	}
	return folder
}

func p6VideoMediaType() *item.MediaType {
	value := item.MediaTypeVideo
	return &value
}

func p6AudioMediaType() *item.MediaType {
	value := item.MediaTypeAudio
	return &value
}

func p6KeyframeExtractor(t *testing.T, body string) *keyframes.Extractor {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatalf("write controlled ffprobe: %v", err)
	}
	extractor, err := keyframes.NewExtractor(path, 5*time.Second, 4096)
	if err != nil {
		t.Fatalf("construct controlled ffprobe: %v", err)
	}
	return extractor
}

func p6WaitForKeyframeExtraction(t *testing.T, source *lazyKeyframeSource, id dto.UUID) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		source.mu.Lock()
		_, running := source.inFlight[id]
		source.mu.Unlock()
		if running {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("keyframe extraction did not enter the coalescing registry")
		case <-time.After(time.Millisecond):
		}
	}
}

func p6ReceiveKeyframeResult(t *testing.T, results <-chan p6KeyframeResult) p6KeyframeResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-t.Context().Done():
		t.Fatalf("keyframe caller did not complete: %v", t.Context().Err())
		return p6KeyframeResult{}
	}
}

func p6WaitForFile(t *testing.T, path, failure string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal(failure)
		case <-time.After(time.Millisecond):
		}
	}
}

func p6WaitForNoKeyframeExtraction(t *testing.T, source *lazyKeyframeSource, id dto.UUID) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		source.mu.Lock()
		_, running := source.inFlight[id]
		source.mu.Unlock()
		if !running {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("canceled keyframe extraction remained in flight")
		case <-time.After(time.Millisecond):
		}
	}
}

func p6WaitForKeyframeWaiters(t *testing.T, source *lazyKeyframeSource, id dto.UUID, want int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		source.mu.Lock()
		call := source.inFlight[id]
		waiters := 0
		if call != nil {
			waiters = call.waiters
		}
		source.mu.Unlock()
		if waiters == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("keyframe waiters = %d, want %d", waiters, want)
		case <-time.After(time.Millisecond):
		}
	}
}
