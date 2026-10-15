// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestScannerInvalidatesDerivedMetadataWhenAtomicReplacementPreservesSizeAndMtime
// covers the scanner-to-derived-cache boundary for an atomic in-place source
// replacement. A filesystem publisher can replace a playable path with different
// bytes while preserving its byte length and mtime, so a rescan must not reuse
// the persisted probe result on those two facts alone.
func TestScannerInvalidatesDerivedMetadataWhenAtomicReplacementPreservesSizeAndMtime(t *testing.T) {
	harness, probe := newScannerSourceIdentityHarness(t)

	media := scannerPath(t, harness.source, "atomic-source-identity.mkv")
	original := []byte("probed-source-v1")
	replacement := []byte("probed-source-v2")
	if len(replacement) != len(original) {
		t.Fatalf("replacement length = %d, want original length %d", len(replacement), len(original))
	}
	writeScannerFile(t, media, string(original))
	modified := time.Date(2026, time.July, 28, 12, 34, 56, 123_456_000, time.UTC)
	if err := os.Chtimes(media, modified, modified); err != nil {
		t.Fatalf("set original media mtime: %v", err)
	}
	beforeFile := scannerSourceIdentityStat(t, media, "stat original media")
	if got := beforeFile.ModTime().UTC().Truncate(time.Microsecond); !got.Equal(modified) {
		t.Fatalf("original media mtime = %s, want persisted microsecond value %s", got, modified)
	}

	scannerScan(t, harness.scanner, "scan original playable media")
	firstItem := scannerItemAtPath(t, harness.items, media)
	first := scannerSourceIdentitySnapshot(t, harness.items, firstItem.ID, "read original derived source snapshot")
	if first.SourceRevision == "" {
		t.Fatal("original derived source revision is empty")
	}
	scannerSourceIdentityAssertPersisted(t, firstItem, "initial scan")
	scannerSourceIdentityAssertSnapshot(t, first, "initial scan")
	if calls := probe.Calls(); calls != 1 {
		t.Fatalf("original MediaProbe calls = %d, want 1", calls)
	}
	scannerSourceIdentitySeedDerivedMetadata(t, harness.items, first)

	replacementPath := filepath.Join(filepath.Dir(media), ".atomic-source-identity-replacement.mkv")
	writeScannerFile(t, replacementPath, string(replacement))
	if err := os.Chtimes(replacementPath, modified, modified); err != nil {
		t.Fatalf("set replacement media mtime: %v", err)
	}
	if err := os.Rename(replacementPath, media); err != nil {
		t.Fatalf("atomically replace media: %v", err)
	}
	afterFile := scannerSourceIdentityStat(t, media, "stat atomically replaced media")
	if afterFile.Size() != beforeFile.Size() {
		t.Fatalf("atomic replacement size = %d, want original size %d", afterFile.Size(), beforeFile.Size())
	}
	if got, want := afterFile.ModTime().UTC().Truncate(time.Microsecond), beforeFile.ModTime().UTC().Truncate(time.Microsecond); !got.Equal(want) {
		t.Fatalf("atomic replacement mtime = %s, want original persisted microsecond mtime %s", got, want)
	}
	contents, err := os.ReadFile(media)
	if err != nil {
		t.Fatalf("read atomically replaced media: %v", err)
	}
	if !bytes.Equal(contents, replacement) {
		t.Fatalf("atomic replacement bytes = %q, want %q", contents, replacement)
	}

	scannerScan(t, harness.scanner, "rescan atomically replaced same-size same-mtime media")
	if calls := probe.Calls(); calls != 2 {
		t.Errorf("MediaProbe calls after atomic same-size same-mtime replacement = %d, want 2", calls)
	}
	second := scannerSourceIdentitySnapshot(t, harness.items, firstItem.ID, "read replacement derived source snapshot")
	secondItem := scannerItemAtPath(t, harness.items, media)
	scannerSourceIdentityAssertPersisted(t, secondItem, "replacement scan")
	scannerSourceIdentityAssertSnapshot(t, second, "replacement scan")
	if second.FileInode == nil || first.FileInode == nil || *second.FileInode == *first.FileInode {
		t.Errorf("file inode after atomic replacement = %v, want a new inode distinct from %v", second.FileInode, first.FileInode)
	}
	if second.SourceRevision == first.SourceRevision {
		t.Errorf("derived source revision after atomic same-size same-mtime replacement = %q, want a changed opaque revision", second.SourceRevision)
	}
	if _, err := harness.items.Keyframes(t.Context(), firstItem.ID); !store.IsNotFound(err) {
		t.Errorf("keyframes after atomic source replacement error = %v, want not found after invalidation", err)
	}
	if got, err := harness.items.Trickplays(t.Context(), firstItem.ID); err != nil || got != nil {
		t.Errorf("trickplays after atomic source replacement = %#v, %v; want no rows after invalidation", got, err)
	}
}

// TestScannerForcesReprobeForLegacySourceWithoutFileIdentity ensures a legacy
// database row is not accepted as proof that the source bytes remain current.
// The next scan must re-probe and backfill the scan-time file facts.
func TestScannerForcesReprobeForLegacySourceWithoutFileIdentity(t *testing.T) {
	harness, probe := newScannerSourceIdentityHarness(t)
	media := scannerPath(t, harness.source, "legacy-source-identity.mkv")
	modified := time.Date(2026, time.July, 28, 13, 34, 56, 123_456_000, time.UTC)
	scannerSourceIdentityWrite(t, media, []byte("probed-source-v1"), modified)

	scannerScan(t, harness.scanner, "scan source before legacy identity removal")
	seeded := scannerItemAtPath(t, harness.items, media)
	scannerSourceIdentityAssertPersisted(t, seeded, "initial scan")
	seeded.FileDevice = nil
	seeded.FileInode = nil
	seeded.FileChangedAt = nil
	if err := harness.items.Upsert(t.Context(), seeded); err != nil {
		t.Fatalf("persist legacy source without file identity: %v", err)
	}

	scannerScan(t, harness.scanner, "rescan legacy source without file identity")
	if calls := probe.Calls(); calls != 2 {
		t.Errorf("MediaProbe calls after legacy source rescan = %d, want 2", calls)
	}
	scannerSourceIdentityAssertPersisted(t, scannerItemAtPath(t, harness.items, media), "legacy backfill rescan")
}

// TestScannerReprobesWhenCurrentObservationLacksIdentity proves an identityless
// observation cannot authorize reuse of a previous probe, even when the prior
// scan persisted complete facts for unchanged bytes.
func TestScannerReprobesWhenCurrentObservationLacksIdentity(t *testing.T) {
	harness, probe := newScannerSourceIdentityHarness(t)
	media := scannerPath(t, harness.source, "identityless-current-observation.mkv")
	modified := time.Date(2026, time.July, 28, 13, 44, 56, 123_456_000, time.UTC)
	scannerSourceIdentityWrite(t, media, []byte("probed-source-v1"), modified)

	scannerScan(t, harness.scanner, "scan complete-identity source")
	item := scannerItemAtPath(t, harness.items, media)
	if source := scannerSourceIdentitySnapshot(t, harness.items, item.ID, "read complete-identity source snapshot"); source.SourceRevision == "" {
		t.Fatal("complete-identity source revision is empty")
	}
	if calls := probe.Calls(); calls != 1 {
		t.Fatalf("MediaProbe calls after complete-identity scan = %d, want 1", calls)
	}

	scannerSourceIdentityInstallScanner(t, harness, probe, func(path string) ([]library.Entry, error) {
		entries, err := scannerReadDir(path)
		if err != nil {
			return nil, err
		}
		for index := range entries {
			if entries[index].Path == media {
				entries[index].Identity = library.FileIdentity{}
			}
		}
		return entries, nil
	})

	for scan := 2; scan <= 3; scan++ {
		scannerScan(t, harness.scanner, "scan identityless current observation")
		if calls := probe.Calls(); calls != scan {
			t.Errorf("MediaProbe calls after identityless scan %d = %d, want %d", scan-1, calls, scan)
		}
		source := scannerSourceIdentitySnapshot(t, harness.items, item.ID, "read identityless source snapshot")
		if source.SourceRevision != "" {
			t.Errorf("identityless current source revision after scan %d = %q, want empty", scan-1, source.SourceRevision)
		}
	}
}

// TestScannerReusesProbeForUnchangedFileIdentity proves the new identity gate
// remains selective: an exact re-observation of the same source must retain
// the reusable probe path rather than re-probe every scan.
func TestScannerReusesProbeForUnchangedFileIdentity(t *testing.T) {
	harness, probe := newScannerSourceIdentityHarness(t)
	media := scannerPath(t, harness.source, "unchanged-source-identity.mkv")
	modified := time.Date(2026, time.July, 28, 14, 34, 56, 123_456_000, time.UTC)
	scannerSourceIdentityWrite(t, media, []byte("probed-source-v1"), modified)

	scannerScan(t, harness.scanner, "scan exact-identity source")
	first := scannerItemAtPath(t, harness.items, media)
	scannerSourceIdentityAssertPersisted(t, first, "initial exact-identity scan")
	scannerScan(t, harness.scanner, "rescan exact-identity source")
	if calls := probe.Calls(); calls != 1 {
		t.Errorf("MediaProbe calls after exact-identity rescan = %d, want 1", calls)
	}
	second := scannerItemAtPath(t, harness.items, media)
	scannerSourceIdentityAssertPersisted(t, second, "exact-identity rescan")
	if *second.FileInode != *first.FileInode || !second.FileChangedAt.Equal(*first.FileChangedAt) {
		t.Errorf("persisted file identity after exact rescan = (inode %d, changed %s), want unchanged (inode %d, changed %s)", *second.FileInode, *second.FileChangedAt, *first.FileInode, *first.FileChangedAt)
	}
}

// TestScannerRefreshesFileIdentityThroughGloballyLockedMetadata verifies the
// global metadata lock boundary does not preserve an obsolete scanner-owned
// fingerprint when a publisher atomically replaces the file.
func TestScannerRefreshesFileIdentityThroughGloballyLockedMetadata(t *testing.T) {
	harness, probe := newScannerSourceIdentityHarness(t)
	media := scannerPath(t, harness.source, "locked-source-identity.mkv")
	modified := time.Date(2026, time.July, 28, 15, 34, 56, 123_456_000, time.UTC)
	scannerSourceIdentityWrite(t, media, []byte("probed-source-v1"), modified)

	scannerScan(t, harness.scanner, "scan globally locked source")
	before := scannerItemAtPath(t, harness.items, media)
	scannerSourceIdentityAssertPersisted(t, before, "initial globally locked scan")
	lockedName := "Locked source metadata"
	before.IsLocked = true
	before.Name = &lockedName
	if err := harness.items.Upsert(t.Context(), before); err != nil {
		t.Fatalf("mark source globally locked: %v", err)
	}

	replacementPath := filepath.Join(filepath.Dir(media), ".locked-source-identity-replacement.mkv")
	scannerSourceIdentityWrite(t, replacementPath, []byte("probed-source-v2"), modified)
	if err := os.Rename(replacementPath, media); err != nil {
		t.Fatalf("atomically replace globally locked source: %v", err)
	}
	scannerScan(t, harness.scanner, "rescan atomically replaced globally locked source")
	if calls := probe.Calls(); calls != 2 {
		t.Errorf("MediaProbe calls after globally locked atomic replacement = %d, want 2", calls)
	}
	after := scannerItemAtPath(t, harness.items, media)
	if !after.IsLocked || after.Name == nil || *after.Name != lockedName {
		t.Errorf("globally locked metadata after source replacement = %#v, want locked name %q", after, lockedName)
	}
	scannerSourceIdentityAssertPersisted(t, after, "globally locked replacement scan")
	if *after.FileInode == *before.FileInode {
		t.Errorf("globally locked source inode after atomic replacement = %d, want a new inode distinct from %d", *after.FileInode, *before.FileInode)
	}
}

// TestScannerProbeFailureRevokesReplacedSourceAuthority proves that a failed
// probe for B cannot leave A's complete identity behind. In particular, an A
// worker that holds both its open descriptor and revision cannot publish
// derived metadata after B has replaced the path.
func TestScannerProbeFailureRevokesReplacedSourceAuthority(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	probe := &scannerProbeFailureRecovery{}
	scannerSourceIdentityInstallScanner(t, harness, probe, nil)

	media := scannerPath(t, harness.source, "probe-failure-identity.mkv")
	firstContents := []byte("source-generation-A")
	secondContents := []byte("source-generation-B")
	if len(secondContents) != len(firstContents) {
		t.Fatalf("replacement length = %d, want original length %d", len(secondContents), len(firstContents))
	}
	modified := time.Date(2026, time.July, 28, 16, 34, 56, 123_456_000, time.UTC)
	scannerSourceIdentityWrite(t, media, firstContents, modified)

	scannerScan(t, harness.scanner, "scan source A")
	firstItem := scannerItemAtPath(t, harness.items, media)
	first := scannerSourceIdentitySnapshot(t, harness.items, firstItem.ID, "read source A snapshot")
	scannerSourceIdentityAssertPersisted(t, firstItem, "source A scan")
	if first.SourceRevision == "" {
		t.Fatal("source A revision is empty")
	}
	if calls := probe.Calls(); calls != 1 {
		t.Fatalf("source A MediaProbe calls = %d, want 1", calls)
	}
	firstStreams, err := harness.items.Streams(t.Context(), firstItem.ID)
	if err != nil {
		t.Fatalf("read source A streams: %v", err)
	}
	firstAttachments, err := harness.items.Attachments(t.Context(), firstItem.ID)
	if err != nil {
		t.Fatalf("read source A attachments: %v", err)
	}
	if len(firstStreams) != 1 || firstStreams[0].Codec != "h264" || len(firstAttachments) != 1 || firstAttachments[0].FileName != "source-A.jpg" {
		t.Fatalf("source A satellites = streams %#v, attachments %#v; want h264 stream and source-A.jpg attachment", firstStreams, firstAttachments)
	}
	scannerSourceIdentitySeedDerivedMetadata(t, harness.items, first)
	oldSource, err := sourcefile.Open(media, sourcefile.Expected{})
	if err != nil {
		t.Fatalf("open source A for derived worker: %v", err)
	}
	firstFingerprint := oldSource.Fingerprint()
	t.Cleanup(func() {
		if err := oldSource.Close(); err != nil {
			t.Errorf("close source A worker descriptor: %v", err)
		}
	})

	replacementPath := filepath.Join(filepath.Dir(media), ".probe-failure-identity-replacement.mkv")
	scannerSourceIdentityWrite(t, replacementPath, secondContents, modified)
	if err := os.Rename(replacementPath, media); err != nil {
		t.Fatalf("atomically replace source A with B: %v", err)
	}
	replacementSource, err := sourcefile.Open(media, sourcefile.Expected{})
	if err != nil {
		t.Fatalf("open source B after atomic replacement: %v", err)
	}
	replacementFingerprint := replacementSource.Fingerprint()
	if err := replacementSource.Close(); err != nil {
		t.Fatalf("close source B replacement descriptor: %v", err)
	}
	if replacementFingerprint.Size != firstFingerprint.Size || !replacementFingerprint.MTime.Equal(firstFingerprint.MTime) || replacementFingerprint.Ino == firstFingerprint.Ino || replacementFingerprint.CTime.Equal(firstFingerprint.CTime) {
		t.Fatalf("atomic source B fingerprint = %#v, want same size+mtime and different inode+ctime from source A %#v", replacementFingerprint, firstFingerprint)
	}
	if err := oldSource.VerifyPathStillCurrent(); !errors.Is(err, sourcefile.ErrStaleSource) {
		t.Fatalf("source A worker path validation after replacement = %v, want stale source", err)
	}

	probe.SetFailure(true)
	scannerScan(t, harness.scanner, "scan source B with failing probe")
	failed := scannerItemAtPath(t, harness.items, media)
	failedSource := scannerSourceIdentitySnapshot(t, harness.items, failed.ID, "read failed source B snapshot")
	if failed.FileDevice != nil || failed.FileInode != nil || failed.FileChangedAt != nil || failedSource.SourceRevision != "" {
		t.Fatalf("failed source B authority = device %v, inode %v, changed %v, revision %q; want no complete identity or revision", failed.FileDevice, failed.FileInode, failed.FileChangedAt, failedSource.SourceRevision)
	}
	if failed.Size == nil || *failed.Size != *firstItem.Size || failed.RunTimeTicks == nil || *failed.RunTimeTicks != *firstItem.RunTimeTicks || !reflect.DeepEqual(failed.Container, firstItem.Container) {
		t.Fatalf("failed source B probe facts = %#v, want preserved source A facts %#v", failed, firstItem)
	}
	failedStreams, err := harness.items.Streams(t.Context(), failed.ID)
	if err != nil {
		t.Fatalf("read failed source B streams: %v", err)
	}
	failedAttachments, err := harness.items.Attachments(t.Context(), failed.ID)
	if err != nil {
		t.Fatalf("read failed source B attachments: %v", err)
	}
	if !reflect.DeepEqual(failedStreams, firstStreams) || !reflect.DeepEqual(failedAttachments, firstAttachments) {
		t.Fatalf("failed source B satellites = streams %#v, attachments %#v; want preserved source A satellites streams %#v, attachments %#v", failedStreams, failedAttachments, firstStreams, firstAttachments)
	}
	if _, err := harness.items.Keyframes(t.Context(), failed.ID); !store.IsNotFound(err) {
		t.Fatalf("keyframes after failed source B reconciliation error = %v, want invalidated", err)
	}
	if trickplays, err := harness.items.Trickplays(t.Context(), failed.ID); err != nil || trickplays != nil {
		t.Fatalf("trickplays after failed source B reconciliation = %#v, %v; want invalidated", trickplays, err)
	}
	if err := harness.items.ReplaceKeyframes(t.Context(), store.KeyframeInfo{ItemID: first.ItemID, SourceRevision: first.SourceRevision, TotalDurationTicks: *first.RunTimeTicks, KeyframeTicks: []int64{0, 45_000_000}}); !errors.Is(err, store.ErrStaleSourceRevision) {
		t.Fatalf("source A worker replace keyframes error = %v, want stale source revision", err)
	}
	if err := harness.items.ReplaceTrickplay(t.Context(), store.TrickplayInfo{ItemID: first.ItemID, SourceRevision: first.SourceRevision, Width: 320, Height: 180, TileWidth: 160, TileHeight: 90, ThumbnailCount: 4, IntervalMS: 2_500, Bandwidth: 84_000, GenerationToken: "stale-source-A-worker"}); !errors.Is(err, store.ErrStaleSourceRevision) {
		t.Fatalf("source A worker replace trickplay error = %v, want stale source revision", err)
	}
	if _, err := harness.items.Keyframes(t.Context(), failed.ID); !store.IsNotFound(err) {
		t.Fatalf("keyframes after rejected source A worker write error = %v, want no row", err)
	}
	if trickplays, err := harness.items.Trickplays(t.Context(), failed.ID); err != nil || trickplays != nil {
		t.Fatalf("trickplays after rejected source A worker write = %#v, %v; want no rows", trickplays, err)
	}
	if calls := probe.Calls(); calls != 2 {
		t.Fatalf("failed source B MediaProbe calls = %d, want 2", calls)
	}

	probe.SetFailure(false)
	scannerScan(t, harness.scanner, "reprobe source B after transient failure")
	recovered := scannerItemAtPath(t, harness.items, media)
	recoveredSource := scannerSourceIdentitySnapshot(t, harness.items, recovered.ID, "read recovered source B snapshot")
	scannerSourceIdentityAssertPersisted(t, recovered, "recovered source B scan")
	if recoveredSource.SourceRevision == "" || recoveredSource.SourceRevision == first.SourceRevision {
		t.Fatalf("recovered source B revision = %q, want a nonempty revision distinct from source A %q", recoveredSource.SourceRevision, first.SourceRevision)
	}
	if calls := probe.Calls(); calls != 3 {
		t.Fatalf("recovered source B MediaProbe calls = %d, want 3", calls)
	}
	recoveredStreams, err := harness.items.Streams(t.Context(), recovered.ID)
	if err != nil {
		t.Fatalf("read recovered source B streams: %v", err)
	}
	recoveredAttachments, err := harness.items.Attachments(t.Context(), recovered.ID)
	if err != nil {
		t.Fatalf("read recovered source B attachments: %v", err)
	}
	if len(recoveredStreams) != 1 || recoveredStreams[0].Codec != "hevc" || len(recoveredAttachments) != 1 || recoveredAttachments[0].FileName != "source-B.jpg" {
		t.Fatalf("recovered source B satellites = streams %#v, attachments %#v; want hevc stream and source-B.jpg attachment", recoveredStreams, recoveredAttachments)
	}
}

// TestScannerRetainsSourceAuthorityWhenUnchangedGenerationSkipsFailingProbe
// keeps the selective compatibility control: a forced probe failure must not
// revoke authority when exact inode and change-time identity already authorizes
// reuse and therefore avoids probing altogether.
func TestScannerRetainsSourceAuthorityWhenUnchangedGenerationSkipsFailingProbe(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	probe := &scannerProbeFailureRecovery{}
	scannerSourceIdentityInstallScanner(t, harness, probe, nil)

	media := scannerPath(t, harness.source, "unchanged-probe-failure-identity.mkv")
	writeScannerFile(t, media, "source-generation-A")
	scannerScan(t, harness.scanner, "scan unchanged-control source")
	initial := scannerItemAtPath(t, harness.items, media)
	initialSource := scannerSourceIdentitySnapshot(t, harness.items, initial.ID, "read unchanged-control source snapshot")
	scannerSourceIdentityAssertPersisted(t, initial, "unchanged-control initial scan")
	if initialSource.SourceRevision == "" {
		t.Fatal("unchanged-control initial source revision is empty")
	}
	scannerSourceIdentitySeedDerivedMetadata(t, harness.items, initialSource)

	probe.SetFailure(true)
	scannerScan(t, harness.scanner, "rescan exact unchanged source while probe fails")
	if calls := probe.Calls(); calls != 1 {
		t.Fatalf("unchanged-control MediaProbe calls = %d, want 1 because exact identity reuses source A facts", calls)
	}
	after := scannerItemAtPath(t, harness.items, media)
	afterSource := scannerSourceIdentitySnapshot(t, harness.items, after.ID, "read unchanged-control source after failed probe")
	scannerSourceIdentityAssertPersisted(t, after, "unchanged-control failed-probe rescan")
	if afterSource.SourceRevision != initialSource.SourceRevision {
		t.Fatalf("unchanged-control source revision after skipped failing probe = %q, want %q", afterSource.SourceRevision, initialSource.SourceRevision)
	}
	if _, err := harness.items.Keyframes(t.Context(), after.ID); err != nil {
		t.Fatalf("unchanged-control keyframes after skipped failing probe: %v", err)
	}
	if trickplays, err := harness.items.Trickplays(t.Context(), after.ID); err != nil || len(trickplays) != 1 {
		t.Fatalf("unchanged-control trickplays after skipped failing probe = %#v, %v; want retained rendition", trickplays, err)
	}
}

// TestScannerRetainsObservedIdentityAcrossRepeatedProbeFailure keeps scan-time
// identity stable when ffprobe has not yet produced reusable media facts. The
// scanner must retry the probe, but an unchanged inode and change time are not
// a mutation and must not be cleared.
func TestScannerRetainsObservedIdentityAcrossRepeatedProbeFailure(t *testing.T) {
	harness, probe := newScannerSourceIdentityHarness(t)

	media := scannerPath(t, harness.source, "repeated-probe-failure.mkv")
	writeScannerFile(t, media, "not a recognized probe fixture")
	scannerScan(t, harness.scanner, "scan source with failing probe")
	initial := scannerItemAtPath(t, harness.items, media)
	scannerSourceIdentityAssertPersisted(t, initial, "initial failed probe")
	initialSource := scannerSourceIdentitySnapshot(t, harness.items, initial.ID, "read initial failed-probe source")
	if initialSource.SourceRevision == "" {
		t.Fatal("initial failed-probe source revision is empty")
	}

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("rescan unchanged source with failing probe: %v", err)
	}
	if result.Inserted != 0 || result.Updated != 0 || result.Deleted != 0 || len(result.Errors) != 0 {
		t.Fatalf("unchanged failed-probe rescan = %#v, want exact no-op", result)
	}
	if calls := probe.Calls(); calls != 2 {
		t.Fatalf("failed MediaProbe calls = %d, want one retry per scan", calls)
	}
	after := scannerItemAtPath(t, harness.items, media)
	scannerSourceIdentityAssertPersisted(t, after, "unchanged failed-probe rescan")
	afterSource := scannerSourceIdentitySnapshot(t, harness.items, after.ID, "read unchanged failed-probe source")
	if afterSource.SourceRevision != initialSource.SourceRevision {
		t.Fatalf("unchanged failed-probe source revision = %q, want %q", afterSource.SourceRevision, initialSource.SourceRevision)
	}
	if *after.FileDevice != *initial.FileDevice || *after.FileInode != *initial.FileInode || !after.FileChangedAt.Equal(*initial.FileChangedAt) {
		t.Fatalf("unchanged failed-probe identity = (%d, %d, %s), want (%d, %d, %s)", *after.FileDevice, *after.FileInode, after.FileChangedAt, *initial.FileDevice, *initial.FileInode, initial.FileChangedAt)
	}
}

func newScannerSourceIdentityHarness(t *testing.T) (*scannerHarness, *scannerSourceIdentityProbe) {
	t.Helper()
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	probe := &scannerSourceIdentityProbe{}
	scannerSourceIdentityInstallScanner(t, harness, probe, nil)
	return harness, probe
}

func scannerSourceIdentityInstallScanner(t *testing.T, harness *scannerHarness, probe library.MediaProbe, readDir func(string) ([]library.Entry, error)) {
	t.Helper()
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		MediaProbe:  probe,
		ReadDir:     readDir,
	})
}

func scannerSourceIdentityWrite(t *testing.T, path string, contents []byte, modified time.Time) {
	t.Helper()
	writeScannerFile(t, path, string(contents))
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("set media mtime for %q: %v", path, err)
	}
}

func scannerSourceIdentityAssertPersisted(t *testing.T, scanned item.Item, operation string) {
	t.Helper()
	if scanned.FileDevice == nil || scanned.FileInode == nil || scanned.FileChangedAt == nil {
		t.Fatalf("%s persisted file identity = (device %v, inode %v, changed %v), want all scan-time facts", operation, scanned.FileDevice, scanned.FileInode, scanned.FileChangedAt)
	}
}

func scannerSourceIdentityAssertSnapshot(t *testing.T, source store.DerivedSource, operation string) {
	t.Helper()
	if source.FileInode == nil || source.FileChangedAt == nil {
		t.Fatalf("%s derived source file identity = (inode %v, changed %v), want current scan-time facts", operation, source.FileInode, source.FileChangedAt)
	}
}

type scannerSourceIdentityProbe struct {
	mu    sync.Mutex
	calls int
}

func (probe *scannerSourceIdentityProbe) Probe(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	contents, err := os.ReadFile(input.Entry.Path)
	if err != nil {
		return library.MediaProbeResult{}, err
	}

	probe.mu.Lock()
	probe.calls++
	probe.mu.Unlock()

	var codec string
	switch {
	case bytes.Equal(contents, []byte("probed-source-v1")):
		codec = "h264"
	case bytes.Equal(contents, []byte("probed-source-v2")):
		codec = "hevc"
	default:
		return library.MediaProbeResult{}, os.ErrInvalid
	}
	size := input.Entry.Size
	runtime := int64(90_000_000)
	return library.MediaProbeResult{
		Container:    "mkv",
		Size:         &size,
		RunTimeTicks: &runtime,
		Streams: []playback.MediaStream{{
			Index: 0,
			Type:  playback.StreamTypeVideo,
			Codec: codec,
		}},
	}, nil
}

func (probe *scannerSourceIdentityProbe) Calls() int {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return probe.calls
}

type scannerProbeFailureRecovery struct {
	mu      sync.Mutex
	calls   int
	failing bool
}

func (probe *scannerProbeFailureRecovery) Probe(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	probe.mu.Lock()
	probe.calls++
	failing := probe.failing
	probe.mu.Unlock()
	if failing {
		return library.MediaProbeResult{}, errors.New("ffprobe temporarily unavailable")
	}
	contents, err := os.ReadFile(input.Entry.Path)
	if err != nil {
		return library.MediaProbeResult{}, err
	}
	codec, attachment := "", ""
	switch {
	case bytes.Equal(contents, []byte("source-generation-A")):
		codec, attachment = "h264", "source-A.jpg"
	case bytes.Equal(contents, []byte("source-generation-B")):
		codec, attachment = "hevc", "source-B.jpg"
	default:
		return library.MediaProbeResult{}, os.ErrInvalid
	}
	size := input.Entry.Size
	runtime := int64(90_000_000)
	return library.MediaProbeResult{
		Container: "mkv", Size: &size, RunTimeTicks: &runtime,
		Streams:     []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: codec}},
		Attachments: []playback.MediaAttachment{{Index: 0, FileName: attachment}},
	}, nil
}

func (probe *scannerProbeFailureRecovery) SetFailure(failing bool) {
	probe.mu.Lock()
	probe.failing = failing
	probe.mu.Unlock()
}

func (probe *scannerProbeFailureRecovery) Calls() int {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return probe.calls
}

func scannerSourceIdentityStat(t *testing.T, path, operation string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	return info
}

func scannerSourceIdentitySnapshot(t *testing.T, items *store.ItemStore, itemID dto.UUID, operation string) store.DerivedSource {
	t.Helper()
	snapshot, err := items.DerivedSourceSnapshot(t.Context(), itemID)
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	return snapshot
}

func scannerSourceIdentitySeedDerivedMetadata(t *testing.T, items *store.ItemStore, source store.DerivedSource) {
	t.Helper()
	if source.RunTimeTicks == nil {
		t.Fatal("derived source runtime is absent")
	}
	keyframes := store.KeyframeInfo{
		ItemID:             source.ItemID,
		SourceRevision:     source.SourceRevision,
		TotalDurationTicks: *source.RunTimeTicks,
		KeyframeTicks:      []int64{0, 45_000_000},
	}
	if err := items.ReplaceKeyframes(t.Context(), keyframes); err != nil {
		t.Fatalf("seed current-source keyframes: %v", err)
	}
	trickplay := store.TrickplayInfo{
		ItemID:          source.ItemID,
		SourceRevision:  source.SourceRevision,
		Width:           320,
		Height:          180,
		TileWidth:       160,
		TileHeight:      90,
		ThumbnailCount:  4,
		IntervalMS:      2_500,
		Bandwidth:       84_000,
		GenerationToken: "scanner-source-identity-regression",
	}
	if err := items.ReplaceTrickplay(t.Context(), trickplay); err != nil {
		t.Fatalf("seed current-source trickplay: %v", err)
	}
}
