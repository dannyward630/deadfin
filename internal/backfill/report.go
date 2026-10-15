// SPDX-License-Identifier: GPL-2.0-only

package backfill

import (
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
)

// Report is the JSON run report the backfill prints to stdout. It records the
// counts of created, updated, and unchanged units, plus every skip and
// anomaly counter named in the design, plus per-kind skip counts. The pass is
// single-threaded, so the counters do not need synchronization.
type Report struct {
	// StartTime is the backfill run start time.
	StartTime time.Time `json:"start_time"`
	// EndTime is the backfill run end time.
	EndTime time.Time `json:"end_time"`
	// Cancelled is true when the pass stopped early because the context was
	// cancelled.
	Cancelled bool `json:"cancelled,omitempty"`
	// EnumerationError records the error that stopped item enumeration, when
	// one occurred.
	EnumerationError string `json:"enumeration_error,omitempty"`

	// UnitsUpserted is the number of units the pass upserted.
	UnitsUpserted int `json:"units_upserted"`
	// Failed is the number of units that failed and did not recover.
	Failed int `json:"failed"`

	// SkippedUnsupported is the number of items whose kind is not mapped to a
	// native aggregate.
	SkippedUnsupported int `json:"skipped_unsupported"`
	// SkippedVirtual is the number of items that are virtual or placeholder.
	SkippedVirtual int `json:"skipped_virtual"`
	// SkippedOrphanEpisode is the number of Episode items with a nil or
	// dangling SeriesID.
	SkippedOrphanEpisode int `json:"skipped_orphan_episode"`
	// SkippedOrphanAudio is the number of Audio items with a nil or dangling
	// ParentID, or a parent whose kind is not MusicAlbum.
	SkippedOrphanAudio int `json:"skipped_orphan_audio"`
	// IncompleteSourceIdentity is the number of file-bearing items that lack
	// a complete inode and change-time identity. The pass skips these units.
	IncompleteSourceIdentity int `json:"incomplete_source_identity"`
	// IdentityMerge is the number of identity mappings that merged two items
	// into one Work.
	IdentityMerge int `json:"identity_merge"`
	// IdentityCollisionSkipped is the number of cross-kind-group identity
	// collisions that created a fresh Work without the colliding identity.
	IdentityCollisionSkipped int `json:"identity_collision_skipped"`
	// RegroupSkipped is the number of items that changed parent across Works
	// since the last run. The pass updates the Asset facts but does not
	// re-parent the native chain.
	RegroupSkipped int `json:"regroup_skipped"`
	// MultipleProjectionAnomaly is the number of items that had more than one
	// projection row after the target-class filter. The pass retains the
	// unselected rows and selects the lexicographically smallest UUID.
	MultipleProjectionAnomaly int `json:"multiple_projection_anomaly"`
	// StaleRetried is the number of units that retried once after a stale
	// source revision.
	StaleRetried int `json:"stale_retried"`
	// StaleRetryExhausted is the number of units that failed after a second
	// stale source revision.
	StaleRetryExhausted int `json:"stale_retry_exhausted"`

	// SkipByKind records per-kind skip counts for unsupported, virtual, and
	// placeholder items.
	SkipByKind map[string]int `json:"skip_by_kind"`
}

// NewReport constructs an empty Report with the supplied start time.
func NewReport(start time.Time) Report {
	return Report{
		StartTime:  start,
		SkipByKind: make(map[string]int),
	}
}

// IncSkipByKind increments the skip counter for one item kind. The pass is
// single-threaded, so the map does not need synchronization.
func (r *Report) IncSkipByKind(kind string) {
	if r.SkipByKind == nil {
		r.SkipByKind = make(map[string]int)
	}
	r.SkipByKind[kind]++
}

// itemKindString returns the string for an item kind.
func itemKindString(k item.Kind) string { return k.String() }
