// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/keyframes"
	"github.com/deadfin-org/deadfin/internal/store"
	"go.uber.org/goleak"
)

// TestP6ApplicationCloseCancelsAndReapsLazyKeyframesBeforeStoresClose proves
// ffprobe is owned by the application, rather than the request that happened
// to start it. In particular, stores must never close while the worker can
// still call ReplaceKeyframes.
func TestP6ApplicationCloseCancelsAndReapsLazyKeyframesBeforeStoresClose(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	app := newP6AdapterApplication(t)
	items := store.NewItemStore(app.stores)
	itemID := p6AdapterUUID(t, 0x6d1)
	fixture := p6WriteSourceFixture(t, "application-owned-keyframes.mkv", "application-owned keyframe source")
	p6UpsertSourceVideo(t, items, itemID, fixture, nil, nil, nil)

	started := filepath.Join(t.TempDir(), "ffprobe-started")
	extractor := p6KeyframeExtractor(t, fmt.Sprintf("touch %q\nwhile :; do sleep 0.01; done\n", started))
	app.keyframes = newLazyKeyframeSource(items, extractor)
	result := make(chan p6KeyframeResult, 1)
	go func() {
		data, found, err := app.keyframes.Keyframes(context.Background(), itemID, itemID.Dashed())
		result <- p6KeyframeResult{data: data, found: found, err: err}
	}()
	p6WaitForFile(t, started, "application-owned ffprobe did not start")
	p6WaitForKeyframeExtraction(t, app.keyframes, itemID)

	trackedStores := &applicationCloseTrackingStores{Stores: app.stores, closed: make(chan struct{})}
	app.stores = trackedStores
	if err := app.Close(t.Context()); err != nil {
		t.Fatalf("close application with blocked keyframe extraction: %v", err)
	}
	select {
	case <-trackedStores.closed:
	default:
		t.Fatal("application Close did not close stores after joining keyframe worker")
	}
	p6WaitForNoKeyframeExtraction(t, app.keyframes, itemID)
	if got := p6ReceiveKeyframeResult(t, result); !errors.Is(got.err, context.Canceled) || got.found {
		t.Fatalf("keyframe waiter after application shutdown = %#v, want canceled without a durable result", got)
	}

	data, found, err := app.keyframes.Keyframes(t.Context(), itemID, itemID.Dashed())
	if err == nil || found || !reflect.DeepEqual(data, keyframes.Data{}) {
		t.Fatalf("Keyframes after application Close = (%#v, %t, %v), want safe closed failure without data", data, found, err)
	}
}

// TestP6ScheduledTrickplayCandidateKeepsFrameRateInAtomicSourceSnapshot
// protects the ABA case for scheduled generation. Source revision hashes are
// intentionally content-derived, so A -> B -> A can make the final revision
// equal the first. Candidate facts therefore cannot read frame rate through a
// second MediaSourceRecord snapshot after the source projection was captured.
func TestP6ScheduledTrickplayCandidateKeepsFrameRateInAtomicSourceSnapshot(t *testing.T) {
	harness := newP6RevisionGenerationHarness(t)
	before, err := harness.items.DerivedSourceSnapshot(t.Context(), harness.itemID)
	if err != nil {
		t.Fatalf("snapshot source A before ABA mutation: %v", err)
	}
	if before.ReferenceFrameRate == nil || *before.ReferenceFrameRate != harness.sourceA.rate {
		t.Fatalf("source-A snapshot frame rate = %v, want %v", before.ReferenceFrameRate, harness.sourceA.rate)
	}

	// The old candidate path returned from DerivedSourceSnapshot here, then
	// read MediaSourceRecord separately. Reconciliation can expose B in that
	// second read and restore revision-compatible A before publication.
	harness.reconcile(t, harness.sourceB)
	harness.reconcile(t, harness.sourceA)
	after, err := harness.items.DerivedSourceSnapshot(t.Context(), harness.itemID)
	if err != nil {
		t.Fatalf("snapshot restored source A after ABA mutation: %v", err)
	}
	if after.SourceRevision != before.SourceRevision {
		t.Fatalf("restored source-A revision = %q, want revision-compatible original %q", after.SourceRevision, before.SourceRevision)
	}
	if after.ReferenceFrameRate == nil || *after.ReferenceFrameRate != *before.ReferenceFrameRate {
		t.Fatalf("restored source-A snapshot frame rate = %v, want %v", after.ReferenceFrameRate, before.ReferenceFrameRate)
	}

	candidate := harness.nextCandidate(t)
	if candidate.SourceRevision != after.SourceRevision || candidate.SourcePath != harness.sourceA.path {
		t.Fatalf("candidate source facts after ABA = %#v, want restored source-A snapshot", candidate)
	}
	if candidate.ReferenceFrameRate == nil || *candidate.ReferenceFrameRate != *after.ReferenceFrameRate {
		t.Fatalf("candidate frame rate after ABA = %v, want atomic restored source-A rate %v", candidate.ReferenceFrameRate, after.ReferenceFrameRate)
	}
}
