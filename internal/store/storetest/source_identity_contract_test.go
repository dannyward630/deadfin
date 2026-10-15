// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestSourceFingerprintRevisionContractSQLite(t *testing.T) {
	runSourceFingerprintRevisionContract(t, openReviewerItemStore)
}

// The optional PostgreSQL seam runs the identical persistence contract when
// DEADFIN_TEST_POSTGRES_DSN is configured; it does not require a service in
// normal development.
func TestSourceFingerprintRevisionContractPostgres(t *testing.T) {
	runSourceFingerprintRevisionContract(t, openPostgresReviewerItemStore)
}

func runSourceFingerprintRevisionContract(t *testing.T, open func(*testing.T) (*store.ItemStore, store.Stores)) {
	t.Helper()

	for offset, change := range []struct {
		name   string
		mutate func(*item.Item)
	}{
		{
			name: "inode",
			mutate: func(source *item.Item) {
				inode := *source.FileInode + 1
				source.FileInode = &inode
			},
		},
		{
			name: "changed at",
			mutate: func(source *item.Item) {
				changed := source.FileChangedAt.Add(time.Microsecond)
				source.FileChangedAt = &changed
			},
		},
	} {
		t.Run("reconcile invalidates derivatives when "+change.name+" changes", func(t *testing.T) {
			items, _ := open(t)
			parent, source, media := sourceFingerprintFixture(t, 18600+offset*10)
			reconcileRevisionSource(t, items, parent, source, media)
			before := derivedSourceSnapshot(t, items, source.ID)
			seedDerivedMetadata(t, items, source.ID, *source.RunTimeTicks)

			change.mutate(&source)
			reconcileRevisionSource(t, items, parent, source, media)

			after := derivedSourceSnapshot(t, items, source.ID)
			assertChangedSourceRevision(t, before.SourceRevision, after.SourceRevision, change.name)
			assertNoDerivedMetadata(t, items, source.ID)
		})
	}

	t.Run("device-only reconciliation retains revision and derivatives", func(t *testing.T) {
		items, _ := open(t)
		parent, source, media := sourceFingerprintFixture(t, 18700)
		reconcileRevisionSource(t, items, parent, source, media)
		before := derivedSourceSnapshot(t, items, source.ID)
		seedDerivedMetadata(t, items, source.ID, *source.RunTimeTicks)

		device := *source.FileDevice + 1
		source.FileDevice = &device
		reconcileRevisionSource(t, items, parent, source, media)

		after := derivedSourceSnapshot(t, items, source.ID)
		if after.SourceRevision != before.SourceRevision {
			t.Fatalf("revision after device-only change = %q, want unchanged %q", after.SourceRevision, before.SourceRevision)
		}
		assertDerivedMetadata(t, items, source.ID)
	})

	t.Run("normalizes changed-at at database microsecond precision before hashing", func(t *testing.T) {
		items, _ := open(t)
		parent, source, media := sourceFingerprintFixture(t, 18800)
		changed := time.Date(2026, time.July, 28, 12, 0, 0, 123_456_789, time.FixedZone("source", -7*60*60))
		source.FileChangedAt = &changed
		reconcileRevisionSource(t, items, parent, source, media)
		first := derivedSourceSnapshot(t, items, source.ID)
		seedDerivedMetadata(t, items, source.ID, *source.RunTimeTicks)

		// The scanner can report finer precision than the database represents.
		// A repeated report must hash the persisted microsecond representation.
		reconcileRevisionSource(t, items, parent, source, media)
		second := derivedSourceSnapshot(t, items, source.ID)
		if second.SourceRevision != first.SourceRevision {
			t.Fatalf("revision after identical nanosecond changed-at refresh = %q, want %q", second.SourceRevision, first.SourceRevision)
		}
		stored, err := items.Get(t.Context(), source.ID)
		if err != nil {
			t.Fatalf("read source after changed-at round trip: %v", err)
		}
		wantChanged := changed.UTC().Truncate(time.Microsecond)
		if stored.FileChangedAt == nil || !stored.FileChangedAt.Equal(wantChanged) {
			t.Fatalf("persisted changed-at = %v, want %s", stored.FileChangedAt, wantChanged)
		}
		assertDerivedMetadata(t, items, source.ID)
	})

	t.Run("media source record preserves persisted fingerprint and canonical revision", func(t *testing.T) {
		items, _ := open(t)
		parent, source, media := sourceFingerprintFixture(t, 18850)
		reconcileRevisionSource(t, items, parent, source, media)
		snapshot := derivedSourceSnapshot(t, items, source.ID)

		record, err := items.MediaSourceRecord(t.Context(), source.ID)
		if err != nil {
			t.Fatalf("read media source record: %v", err)
		}
		if record.FileDevice == nil || *record.FileDevice != *source.FileDevice ||
			record.FileInode == nil || *record.FileInode != *source.FileInode ||
			record.FileChangedAt == nil || !record.FileChangedAt.Equal(*source.FileChangedAt) {
			t.Fatalf("media source record fingerprint = device:%v inode:%v changed-at:%v, want source facts", record.FileDevice, record.FileInode, record.FileChangedAt)
		}
		if record.SourceRevision != snapshot.SourceRevision {
			t.Fatalf("media source record revision = %q, want snapshot revision %q", record.SourceRevision, snapshot.SourceRevision)
		}
	})

	t.Run("derived source returns independent fingerprint facts", func(t *testing.T) {
		items, _ := open(t)
		parent, source, media := sourceFingerprintFixture(t, 18900)
		reconcileRevisionSource(t, items, parent, source, media)
		first := derivedSourceSnapshot(t, items, source.ID)
		if first.FileInode == nil || first.FileChangedAt == nil {
			t.Fatalf("derived source fingerprint = inode:%v changed-at:%v, want both facts", first.FileInode, first.FileChangedAt)
		}

		*first.FileInode = 0
		*first.FileChangedAt = first.FileChangedAt.Add(time.Hour)
		second := derivedSourceSnapshot(t, items, source.ID)
		if second.FileInode == nil || *second.FileInode != *source.FileInode {
			t.Fatalf("derived source inode after caller mutation = %v, want %d", second.FileInode, *source.FileInode)
		}
		if second.FileChangedAt == nil || !second.FileChangedAt.Equal(*source.FileChangedAt) {
			t.Fatalf("derived source changed-at after caller mutation = %v, want %s", second.FileChangedAt, *source.FileChangedAt)
		}
	})
}

func sourceFingerprintFixture(t *testing.T, value int) (item.Item, item.Item, []store.ScannedChildMedia) {
	t.Helper()
	parent, source, media := revisionSourceFixture(t, value)
	device, inode := int64(0x1000), int64(0x2000)
	changed := time.Date(2026, time.July, 28, 12, 0, 0, 654_321_000, time.UTC)
	source.FileDevice = &device
	source.FileInode = &inode
	source.FileChangedAt = &changed
	return parent, source, media
}
