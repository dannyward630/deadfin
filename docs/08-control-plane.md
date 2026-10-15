# Deadfin-native media control plane

This document records the ratified architecture for the Deadfin-native media
control plane. It is the design record for a native aggregate family, a
durable event log, a lifecycle engine, a backend-neutral adapter framework, a
versioned public API, a backfill path, playback diagnostics, invariants, and
issue decomposition.

The architecture is ratified. Each decision row in this document carries
`Status: ratified`, and the recorded design is the basis for the follow-up
issues.

This file is the architecture record for the native control plane. The
decision IDs D9 to D15 match the decision rows of the project overview.


All claims about current behavior cite `file:line`. All references to
`deadfin/` paths point at the Deadfin Go server repository.

## Native model

### Decision D9 — Separate native aggregate family

Status: ratified

`item.Item` is the persisted compatibility representation of one Jellyfin media
item (`deadfin/internal/item/item.go:9-11`). The conventions prohibit a second
Item subtype or embedding chain.

The control plane introduces a separate aggregate family. The aggregates are
`Work`, `Edition`, `Release`, `Asset`, `Inventory`, and `Request`. The code
does not embed `item.Item` in any native aggregate. The code does not put the
native graph in `Item.Extra`.

A projection table maps the native graph to compatibility items. During
migration, the compatibility item remains canonical. The native graph is an
idempotent derived layer. The target state makes the native graph canonical and
maintains Jellyfin items as projections. That source-of-truth switch requires a
separate ratified decision before implementation.

Rejected alternative: extend `item.Item` with native fields. This option
violates the convention that prohibits a second Item-like struct
(D9 above). It also couples the native model to the
compatibility schema, so every native change risks the parity surface.

### Stable native identifiers

Each native aggregate carries a server-assigned UUID. The UUID is stable across
re-imports. The projection table records the mapping from native UUID to
compatibility `item.Item.ID` (`deadfin/internal/item/item.go:13`).

### Aggregate boundaries

| Aggregate | Boundary | Owner of facts |
|---|---|---|
| Work | One movie, one series, one album, or one collection | Catalog identity and provider IDs |
| Edition | One release variant of a Work, for example theatrical cut or extended cut | Edition-level metadata and provider IDs |
| Release | One physical or digital source of one Edition | Release identity and acquisition state |
| Asset | One media file of one Release | File identity, inode facts, and source revision |
| Inventory | One server-owned copy of one Asset | Physical bytes, hardlink facts, and seed obligations |
| Request | One user request for one Work or Edition | Request state, approval state, and fulfillment link |

### Catalog states

A Work has these catalog states: `Unidentified`, `Identified`, `Matched`, and
`Locked`. An `Identified` Work has provider IDs but no confirmed match. A
`Matched` Work has a confirmed provider match. A `Locked` Work ignores
automatic re-match and accepts only manual edits.

### Inventory states

An Inventory has these inventory states: `Absent`, `Downloading`, `Present`,
`Seeding`, and `Evicted`. See the Lifecycle section for the legal transitions.

### Identity mappings

The native model records identity mappings for these external catalogs: TMDB,
IMDb, TVDB, AniDB, AniList, and MAL. Each mapping stores the external ID, the
catalog name, and the last-verified time. A mapping with a stale last-verified
time is a candidate for re-match, not a deletion.

### Asset identity reuse

The asset model reuses the source identity and revision work. The code already
records inode facts and source revisions
(`deadfin/internal/sourcefile/sourcefile.go:240-258`,
`deadfin/internal/store/item_store.go:163-174`). The Asset aggregate stores the
device and inode pair from `sourcefile.Source.Identity`
(`deadfin/internal/sourcefile/sourcefile.go:240-246`). The source revision
check from `item_store.go:163-174` detects a changed source file before the
projection updates.

## Events and actions

### Decision D10 — Store-backed event log and transactional outbox

Status: ratified

The existing bus provides typed, non-blocking notifications
(`deadfin/internal/events/bus.go:1,20-29`). It closes a slow subscriber
instead of blocking a publisher (`deadfin/internal/events/bus.go:73-89`).

The control plane keeps `events.Bus` as the process-local notification
transport. A follow-up issue adds a store-backed event log and a transactional
outbox.

Rejected alternative: use `events.Bus` as the durable event log. The bus is
non-durable. A process restart loses every in-flight event. The bus is a
transport, not a source of truth.

### Event log semantics

The record selects these semantics:

- The store assigns one increasing sequence under the single-writer database
  contract of the store.
- Each command has a caller-supplied idempotency key. A duplicate key returns
  the prior result and emits no new event.
- Each event has an immutable event ID, aggregate ID, event type, schema
  version, actor, time, and payload.
- A state change, its event, and its outbox row commit in one transaction.
- The outbox publishes committed notifications to `events.Bus` in sequence
  order.
- Durable consumers replay from a stored sequence checkpoint. They do not use
  the bus as durable storage.
- Authorization, deletion, override, and lifecycle events remain in the audit
  log.

### Event retention

The record selects this retention rule: keep command and mutation events for
the life of the aggregate. Keep a notification event until every applicable
durable consumer confirms receipt of that event sequence number.

The store can delete a notification event after every applicable durable
consumer confirms receipt. The command log stays for the life of the
aggregate. The store can reconstruct a deleted notification event from the
command log later.

If the system adds a new durable consumer after the deletion, the command
log replays and regenerates the notification event. The new consumer replays
that reconstructed event from its stored checkpoint. The possible
reconstruction order is: confirm receipt, delete the event, add the new
consumer, reconstruct the event, and replay the event.

Rejected alternative: delete a notification event after one consumer confirms
receipt. This option loses the event for other applicable consumers that have
not confirmed receipt. A new consumer added after the deletion cannot replay
the event.

Rejected alternative: time-based retention for all event classes. This option
deletes the audit trail for authorization and deletion events. INV-1 and INV-3
depend on the audit trail.

Rejected alternative: keep every event class forever. This option grows the
database without bound. The command log reconstructs notification events, so
permanent notification storage is redundant after every consumer confirms
receipt.

### Commands and event types

Favorite, watched, promote, pin, request, approve, and deletion confirmation
are different commands. They emit different event types. Each command maps to
exactly one event type. The event type name encodes the aggregate and the
action, for example `request.submitted` and `inventory.evicted`.

### User-data durable event

The Jellyfin user-data path publishes a notification only after a committed
mutation (`deadfin/internal/playstate/service.go:141-145,417-419`). The
follow-up issue for the event log must also append the matching durable event
in the same transaction as the user-data mutation. The publish call at
`service.go:417-419` stays as the bus notification. The new durable event is an
addition, not a replacement.

## Lifecycle

### Decision D11 — Single scheduler, lifecycle states, and two-phase eviction

Status: ratified

The control plane defines the legal transitions for these lifecycle states:
`Discovered`, `Downloading`, `Fresh`, `Promoted`, `Seed-bound`, `Evictable`,
and `Archived`.

Rejected alternative: a second scheduler for lifecycle evaluation. The
existing task scheduler owns registration, workers, persistence, and shutdown
(`deadfin/internal/tasks/scheduler.go:81-104,129-145`). A second scheduler
duplicates the shutdown path and the persistence logic. The control plane uses
one scheduler.

### Legal transitions

| From | To | Actor | Preconditions |
|---|---|---|---|
| Discovered | Downloading | Acquisition adapter | A Request exists and an acquisition job started |
| Downloading | Fresh | Acquisition adapter | The download completed and the file is present |
| Fresh | Promoted | Scanner | The file passed probe and the projection updated |
| Promoted | Seed-bound | Lifecycle evaluator | Tracker reports an unmet seed obligation |
| Promoted | Evictable | Lifecycle evaluator | No seed obligation and the age threshold elapsed |
| Seed-bound | Evictable | Admin override | The override is an explicit admin action recorded in the event log |
| Evictable | Archived | Authorized user | A dry-run plan exists and an authorized user confirmed it |
| Archived | Discovered | Admin override | The override is an explicit admin action recorded in the event log |

### Scheduler reuse

The existing task scheduler provides the `Runnable` seam with a context
(`deadfin/internal/tasks/scheduler.go:16-20`). The lifecycle evaluator is one
`Runnable` registered with the existing scheduler. The evaluator runs on a
periodic trigger. All future SQL lives in `internal/store`, with both database
variants (SQLite and PostgreSQL).

### Schema migration number

The schema issue must claim the first free migration number at branch start.
The latest committed migration is `00012_item_source_file_identity.sql` in
both `deadfin/internal/store/migrate/migrations/sqlite/00012_item_source_file_identity.sql:1-9`
and `deadfin/internal/store/migrate/migrations/postgres/00012_item_source_file_identity.sql:1-9`.
The schema issue claims `00013`. The stale `00011` example from the issue body
is wrong. The event-log issue, the lifecycle issue, the API issue, and the
backfill issue reference the schema issue number in their `## Depends` section.

### Two-phase eviction

The eviction plan has two phases. The first phase persists a dry-run plan. The
dry-run plan records the hardlink-aware physical bytes and the seed obligations
for each candidate. The second phase requires an authorized confirmation
before any unlink operation. Both phases emit durable events before the
destructive syscall.

## Adapters

### Decision D12 — Context-first adapter seams with composition-root registration

Status: ratified

The control plane defines these context-first seams:

- `RequestBackend`, with Seerr as the first adapter.
- `AcquisitionBackend`, for Radarr and Sonarr.
- `TorrentClient`, for qBittorrent.
- `SubtitleBackend`, for Bazarr.

Rejected alternative: register adapters with `init()` functions. The provider
law requires composition-root registration and prohibits `init()` registration
(`docs/07-extension-seams.md:67-72`). An `init()` adapter hides the dependency
graph and blocks a future loader phase.

### Serialization contract

Every seam value must support serialization across a future process boundary
(`docs/07-extension-seams.md:92-103`). The seams use plain request and response
data. No seam method takes a live server object. The adapter issue must add a
CI lint that asserts every parameter and return type is marshalable, after the
seam file exists.

### Registration

The composition root in `cmd/deadfin` registers each adapter explicitly. The
adapter configuration lives in per-adapter typed structs, persisted like the
existing provider configuration (`docs/07-extension-seams.md:76-79`).

### Credential safety

No Seerr-specific field enters the native schema. The adapter errors and
client payloads redact credentials. See INV-1 for the invariant.

## Parity boundary

### Decision D13 — Native API under /deadfin/v1/ with route-registered authorization

Status: ratified

The control plane reserves `/deadfin/v1/` for the native API. The server mounts
its router below the configured base URL
(`deadfin/cmd/deadfin/main.go:150-154`). The native router mounts below the same
base URL.

Rejected alternative: add native routes to the Jellyfin router without a
prefix. This option changes the parity surface. The parity contract contains
the OpenAPI surface and hidden routes of the pinned upstream. A native
route on the Jellyfin router can collide with an upstream route and break the
differential corpus.

### Parity preservation

The control plane changes no Jellyfin route, response, or differential corpus.
The OpenAPI artifacts belong to the later API issue. The native API has its
own OpenAPI description.

### Route authorization

Each native route carries authorization metadata. The existing handlers place
policy metadata beside route registration
(`deadfin/internal/api/handlers/auth.go:26-41`). The composition root wraps the
router with the authorizer (`deadfin/cmd/deadfin/application.go:508-510`). The
native routes use the same `router.Route` pattern with a `Policy` field.

### Native API contract

The record defines these parts of the native API:

- Versioning: the path prefix `/deadfin/v1/` carries the major version. A
  breaking change increments the major version and adds a new prefix.
- Error shapes: every error response has a code, a message, and an optional
  detail field. The code is a stable string, not an HTTP status.
- Pagination: list endpoints use cursor pagination. The cursor is an opaque
  token that encodes the last sequence value.
- Idempotency headers: every mutating endpoint accepts an
  `Idempotency-Key` header. The store rejects a duplicate key and returns the
  prior result.
- Request states: a Request has these states: `Submitted`, `PendingApproval`,
  `Approved`, `Fulfilled`, `Rejected`, and `Cancelled`.
- Permission metadata: each route declares the required policy. Quotas,
  approval rules, and the admin PIN are server-side policy data. See INV-2.

## Playback diagnostics

### Decision D14 — Native decision-explanation object under /deadfin/v1/

Status: ratified

The control plane defines a native decision-explanation object. The object
covers source selection, stream selection, remux, and transcode decisions. Each
decision carries machine-readable reasons and a user-readable summary.

Rejected alternative: add explanation fields to the Jellyfin `PlaybackInfo`
contract. This option changes the parity surface.
The differential corpus pins the `PlaybackInfo` shape, so any extra field risks
a corpus mismatch.

### Exposure

The decision-explanation object is exposed only through `/deadfin/v1/`. The
Jellyfin `PlaybackInfo` contract is unchanged. The diagnostics issue depends
on the API issue for the route registration.

## Invariants

Every follow-up issue must quote each invariant that governs its work. The
issue bodies include the invariant text verbatim.

- **INV-1** Upstream credentials (Seerr, arr, qBittorrent, Bazarr, trackers)
  exist server-side only, never serialized into any client-facing payload;
  adapters redact credentials from propagated errors (precedent D-19,
  `divergences.md:24`).
- **INV-2** Every native mutating action is authorized per-user via
  route-registration policy; quotas, approval rules, and admin PIN are
  server-side policy data.
- **INV-3** No automated path unlinks media. Eviction/cleanup is two-phase: a
  persisted dry-run plan (hardlink-aware physical bytes, seed obligations
  evaluated) confirmed by an authorized user, then execution; both phases emit
  durable events **before** the destructive syscall.
- **INV-4** Unmet tracker seed-time/ratio obligations make an asset
  non-deletable by any automated policy; override is an explicit admin action
  recorded in the event log.
- **INV-5** Native-model backfill is idempotent and re-runnable; any failure
  leaves the Jellyfin-visible library and its data unchanged (one-way importer
  precedent, D3).

## Decomposition

The control plane decomposes into eight follow-up issues. Seven server issues
are filed in `deadfin/deadfin` with titles that start with `Control plane:`. One
client issue is filed in `deadfin/deadfin-web`.

### Server issues

1. `Control plane: native schema, stores, and item projection` — schema issue,
   filed first. The other issues reference its number in their dependencies.
2. `Control plane: durable event log, outbox, and typed actions` — depends on
   the schema issue.
3. `Control plane: lifecycle engine and eviction planner` — depends on the
   event-log issue. Quotes INV-3 and INV-4.
4. `Control plane: adapter framework and Seerr backend` — depends on the
   event-log issue for progress events. Quotes INV-1 and INV-2.
5. `Control plane: native catalog and request API` — depends on the schema
   issue and the adapter issue. Quotes INV-1 and INV-2.
6. `Control plane: native model backfill` — depends on the schema issue. Quotes
   INV-5.
7. `Control plane: playback decision diagnostics` — depends on the API issue.

### Client issue

8. `Control plane: TV catalog and request experience` — filed in
   `deadfin/deadfin-web`. Depends on the API issue and the diagnostics issue.
   The client consumes only the native API.

### Dependency graph

```
schema(1) ──┬─> event-log(2) ──┬─> lifecycle(3)
            │                  ├─> adapter(4) ──┐
            ├─> backfill(6)     │                ├─> API(5) ──> diagnostics(7)
            └──────────────────┘                └────────────┘
                                                  API(5) + diagnostics(7) ──> client(8)
```
