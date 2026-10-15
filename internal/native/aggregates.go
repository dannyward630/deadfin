// SPDX-License-Identifier: GPL-2.0-only

package native

import (
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// CatalogState records the identification state of a Work.
type CatalogState string

const (
	// CatalogUnidentified is the state of a Work with no provider IDs and no
	// confirmed match.
	CatalogUnidentified CatalogState = "Unidentified"
	// CatalogIdentified is the state of a Work with provider IDs but no
	// confirmed match.
	CatalogIdentified CatalogState = "Identified"
	// CatalogMatched is the state of a Work with a confirmed provider match.
	CatalogMatched CatalogState = "Matched"
	// CatalogLocked is the state of a Work that ignores automatic re-match
	// and accepts only manual edits.
	CatalogLocked CatalogState = "Locked"
)

// Catalog is the named external catalog for one identity mapping.
type Catalog string

const (
	CatalogTMDB    Catalog = "TMDB"
	CatalogIMDb    Catalog = "IMDb"
	CatalogTVDB    Catalog = "TVDB"
	CatalogAniDB   Catalog = "AniDB"
	CatalogAniList Catalog = "AniList"
	CatalogMAL     Catalog = "MAL"
)

// IdentityMapping is one external identity for a Work or Edition. Each row
// records the catalog, the external ID, and the last-verified time
// (docs/08-control-plane.md:76-80).
type IdentityMapping struct {
	Catalog      Catalog
	ExternalID   string
	LastVerified time.Time
}

// Work is one movie, one series, one album, or one collection. The Work owns
// catalog identity and provider IDs (docs/08-control-plane.md:56).
type Work struct {
	ID           dto.UUID
	State        CatalogState
	Title        string
	ProviderIDs  []IdentityMapping
	DateCreated  time.Time
	DateModified time.Time
}

// Edition is one release variant of a Work. The Edition owns edition-level
// metadata and provider IDs (docs/08-control-plane.md:57).
type Edition struct {
	ID           dto.UUID
	WorkID       dto.UUID
	Title        string
	ProviderIDs  []IdentityMapping
	DateCreated  time.Time
	DateModified time.Time
}

// AcquisitionState records the acquisition state of a Release.
type AcquisitionState string

const (
	// AcquisitionAcquired means the release source is present on the server.
	AcquisitionAcquired AcquisitionState = "Acquired"
	// AcquisitionMissing means the release source is not present on the server.
	AcquisitionMissing AcquisitionState = "Missing"
)

// Release is one physical or digital source of one Edition. The Release owns
// release identity and acquisition state (docs/08-control-plane.md:58).
type Release struct {
	ID               dto.UUID
	EditionID        dto.UUID
	Title            string
	AcquisitionState AcquisitionState
	DateCreated      time.Time
	DateModified     time.Time
}

// Asset is one media file of one Release. The Asset owns file identity, inode
// facts, and source revision (docs/08-control-plane.md:59).
type Asset struct {
	ID             dto.UUID
	ReleaseID      dto.UUID
	Path           string
	Device         uint64
	Inode          uint64
	SourceRevision string
	DateCreated    time.Time
	DateModified   time.Time
}

// InventoryState records the physical presence state of one Inventory.
type InventoryState string

const (
	// InventoryAbsent means the inventory copy is not on the server.
	InventoryAbsent InventoryState = "Absent"
	// InventoryDownloading means the inventory copy is downloading.
	InventoryDownloading InventoryState = "Downloading"
	// InventoryPresent means the inventory copy is present on the server.
	InventoryPresent InventoryState = "Present"
	// InventorySeeding means the inventory copy is present and has an
	// unmet seed obligation.
	InventorySeeding InventoryState = "Seeding"
	// InventoryEvicted means the inventory copy was evicted.
	InventoryEvicted InventoryState = "Evicted"
)

// Inventory is one server-owned copy of one Asset. The Inventory owns physical
// bytes, hardlink facts, and seed obligations (docs/08-control-plane.md:60).
type Inventory struct {
	ID                dto.UUID
	AssetID           dto.UUID
	State             InventoryState
	PhysicalBytes     int64
	Hardlinked        bool
	HardlinkTarget    string
	SeedRatio         float64
	SeedTime          int64
	SeedObligationMet bool
	DateCreated       time.Time
	DateModified      time.Time
}

// RequestState records the lifecycle state of a Request.
type RequestState string

const (
	// RequestSubmitted means the request was submitted but not yet reviewed.
	RequestSubmitted RequestState = "Submitted"
	// RequestPendingApproval means the request is under review.
	RequestPendingApproval RequestState = "PendingApproval"
	// RequestApproved means the request was approved.
	RequestApproved RequestState = "Approved"
	// RequestFulfilled means the request was fulfilled.
	RequestFulfilled RequestState = "Fulfilled"
	// RequestRejected means the request was rejected.
	RequestRejected RequestState = "Rejected"
	// RequestCancelled means the request was cancelled.
	RequestCancelled RequestState = "Cancelled"
)

// ApprovalState records the approval disposition of a Request.
type ApprovalState string

const (
	// ApprovalPending means the request has not been reviewed for approval.
	ApprovalPending ApprovalState = "Pending"
	// ApprovalGranted means an authorized user approved the request.
	ApprovalGranted ApprovalState = "Granted"
	// ApprovalDenied means an authorized user denied the request.
	ApprovalDenied ApprovalState = "Denied"
)

// Request is one user request for one Work or one Edition. The Request owns
// request state, approval state, and the fulfillment link
// (docs/08-control-plane.md:61).
type Request struct {
	ID            dto.UUID
	WorkID        *dto.UUID
	EditionID     *dto.UUID
	State         RequestState
	ApprovalState ApprovalState
	FulfillmentID *dto.UUID
	DateCreated   time.Time
	DateModified  time.Time
}
