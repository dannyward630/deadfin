// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/playback"
)

func TestMediaSourceFromRecordPreservesCanonicalFileFactsWithoutChangingWireJSON(t *testing.T) {
	id := playbackTestUUID(t, "cf602d5d-39a7-450e-9400-000000000611")
	path, name, container := "/media/identity.mkv", "identity", "mkv"
	size, device, inode := int64(4_000_000_001), int64(0x1000), int64(0x2000)
	modified := time.Date(2026, time.July, 28, 12, 0, 0, 123_456_000, time.UTC)
	changed := time.Date(2026, time.July, 28, 12, 0, 0, 654_321_000, time.UTC)
	record := playback.MediaSourceRecord{
		ID:             id,
		Path:           &path,
		Name:           &name,
		Container:      &container,
		Size:           &size,
		DateModified:   &modified,
		FileDevice:     &device,
		FileInode:      &inode,
		FileChangedAt:  &changed,
		SourceRevision: "canonical-source-revision",
	}

	source := playback.MediaSourceFromRecord(record)
	if source.SourceRevision != record.SourceRevision {
		t.Fatalf("source revision = %q, want %q", source.SourceRevision, record.SourceRevision)
	}
	if source.FileFacts.Size == nil || *source.FileFacts.Size != size ||
		source.FileFacts.ModifiedAt == nil || !source.FileFacts.ModifiedAt.Equal(modified) ||
		source.FileFacts.Device == nil || *source.FileFacts.Device != device ||
		source.FileFacts.Inode == nil || *source.FileFacts.Inode != inode ||
		source.FileFacts.ChangedAt == nil || !source.FileFacts.ChangedAt.Equal(changed) {
		t.Fatalf("source file facts = %#v, want record facts", source.FileFacts)
	}

	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("marshal media source info: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode media source JSON: %v", err)
	}
	for _, internal := range []string{"FileFacts", "SourceRevision"} {
		if _, exists := wire[internal]; exists {
			t.Fatalf("wire source JSON leaked internal %s: %s", internal, encoded)
		}
	}
	if got := wire["Id"]; string(got) != `"`+id.String()+`"` {
		t.Fatalf("wire Id = %s, want %q", got, id.String())
	}
}

func TestMediaSourceFromRecordHidesRevisionWithoutCompleteIdentity(t *testing.T) {
	id := playbackTestUUID(t, "cf602d5d-39a7-450e-9400-000000000612")
	inode := int64(0x2001)
	changed := time.Date(2026, time.July, 28, 12, 0, 0, 654_321_000, time.UTC)
	const revision = "persisted-reconciliation-hash"

	for _, tc := range []struct {
		name         string
		inode        bool
		changedAt    bool
		wantRevision string
	}{
		{name: "both facts absent"},
		{name: "inode only", inode: true},
		{name: "changed-at only", changedAt: true},
		{name: "complete identity", inode: true, changedAt: true, wantRevision: revision},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := playback.MediaSourceRecord{ID: id, SourceRevision: revision}
			if tc.inode {
				record.FileInode = &inode
			}
			if tc.changedAt {
				record.FileChangedAt = &changed
			}

			source := playback.MediaSourceFromRecord(record)
			if source.SourceRevision != tc.wantRevision {
				t.Fatalf("projected source revision = %q, want %q", source.SourceRevision, tc.wantRevision)
			}
		})
	}
}
