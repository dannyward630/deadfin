# 01 — Architecture

Go module: the separate `github.com/deadfin-org/deadfin` repository. Jellyport may
hold an ignored `deadfin/` checkout for integrated validation, but the repositories
have independent history and release lifecycles. Nothing is a public library:
everything under `internal/` except the commands under `cmd/`.

## Package tree ↔ C# ancestry

```
cmd/
  deadfin/              main binary: flag parsing, Deps construction, lifecycle   ← Jellyfin.Server/Program.cs, Startup.cs
  deadfin-import/       one-way importer (02-storage.md §Importer)               ← (none; new)
tools/
  goldenrec/            C# harness drivers, traffic capture scripts (dev only)
  difftool/             differential replay comparator (04-validation.md)
internal/
  server/               composition root: Deps struct, wiring, shutdown          ← ApplicationHost.cs (~90 singletons)
  api/router/           chi tree, case-normalizing middleware, route table       ← ASP.NET routing
  api/middleware/       auth, legacy headers, startup-wizard gate, logging       ← Jellyfin.Api/Middleware, Auth/*
  api/bind/             query/body binding (see §JSON)                           ← ASP.NET model binding
  api/render/           dual-casing JSON, stream/image responses                 ← JsonDefaults + output formatters
  api/handlers/         one file-set per controller group                        ← Jellyfin.Api/Controllers (60 files)
  dto/                  hand-written wire structs + enums + UUID/Time scalars    ← MediaBrowser.Model
  dtobuild/             BaseItemDto assembly (fields= projection)                ← Emby.Server.Implementations/Dto/DtoService.cs
  item/                 Item struct, Kind enum, kindDefs, behavior funcs         ← MediaBrowser.Controller/Entities (BaseItem/Folder + 25 subclasses)
  library/              scan, resolvers, topology, validators, visibility        ← Emby.Server.Implementations/Library (LibraryManager.cs 3,906)
  naming/               pure port of Emby.Naming (4,347 loc)                     ← Emby.Naming
  metadata/nfo/         NFO read/write                                           ← MediaBrowser.XbmcMetadata
  metadata/local/       local image/metadata discovery                           ← MediaBrowser.LocalMetadata
  providers/{tmdb,omdb,musicbrainz,audiodb,studioimages,listenbrainz}/           ← MediaBrowser.Providers/Plugins/* (in-tree "plugins")
  store/                interfaces, ItemQuery + translator, Dialect              ← Jellyfin.Server.Implementations/Item (BaseItemRepository ~2,950)
  store/sqlite/         dialect + driver glue (modernc.org/sqlite)               ← Jellyfin.Database.Providers.Sqlite
  store/postgres/       later phase; compiles from day 1                         ← (none; new)
  store/migrate/        goose migrations, per-dialect SQL                        ← EF migrations (final schema only)
  user/                 user manager, policies, PBKDF2 verify                    ← Jellyfin.Server.Implementations/Users
  auth/                 token resolution, 18 policies, quickconnect              ← Jellyfin.Api/Auth, AuthorizationContext.cs, QuickConnectManager.cs
  session/              session registry, dead-reckoning, playstate              ← Emby.Server.Implementations/Session/SessionManager.cs (2,196)
  ws/                   envelope codec, keepalive, feeds, per-conn writer        ← WebSocketManager.cs, SessionWebSocketListener.cs, BasePeriodicWebSocketListener.cs
  events/               typed bus with per-subscription debounce                 ← EntryPoints/LibraryChangedNotifier.cs, UserDataChangeNotifier.cs
  playback/             StreamBuilder, ConditionProcessor, StreamInfo — PURE     ← MediaBrowser.Model/Dlna
  encoding/             pure ffmpeg arg builders                                 ← EncodingHelper.cs (sw subset of 7,987)
  ffmpeg/               Capabilities probe, Prober, TranscodeManager             ← MediaEncoder.cs, TranscodeManager.cs, TranscodingThrottler.cs
  probe/                ProbeResultNormalizer                                    ← ProbeResultNormalizer.cs (1,796)
  hls/                  playlist builders, RFC6381 codec strings, segment state  ← DynamicHlsHelper.cs, HlsCodecStringHelpers.cs, DynamicHlsPlaylistGenerator.cs
  images/               ops + MD5-sharded cache (libvips; cgo confined here)     ← Jellyfin.Drawing.Skia, ImageProcessor.cs
  config/               XML config stores (system/encoding/network/branding/…)   ← Emby.Server.Implementations/AppBase + config factories
  tasks/                scheduler + built-in tasks                               ← Emby.Server.Implementations/ScheduledTasks
  activitylog/          activity log                                             ← Jellyfin.Server.Implementations/Activity
  trickplay/ chapters/ mediasegments/ keyframes/   thin domain pkgs over store   ← respective managers
  discovery/            UDP :7359 responder                                      ← Emby.Server.Implementations/Udp
```

## HTTP stack

**DECISION: router = `go-chi/chi/v5`.**
RATIONALE: verified upstream routes require **mid-segment parameters** —
`Videos/{itemId}/hls1/{playlistId}/{segmentId}.{container}`
(`DynamicHlsController.cs:1087`) and `{itemId}/stream.{container}`
(`VideosController.cs:552`). stdlib `ServeMux` (1.22 patterns) and echo/gin only
support whole-segment params. chi supports `{a}.{b}` and regex-constrained params
(mirror upstream's `ContainerValidationRegexStr`/`LevelValidationRegexStr`,
`EncodingHelper.cs:48`), keeps `net/http` handler signatures (no framework context
type across ~300 handlers), and has a composable middleware chain.
REJECTED: stdlib ServeMux (mid-segment params, per-route method demux boilerplate);
echo/gin (foreign handler signature everywhere, same param limitation);
gorilla/mux (maintenance history, slower tree).

**Case-insensitive routing.** ASP.NET matches paths case-insensitively and clients
exploit it (legacy apiclient builds `Users/...`, SDKs build `/Users/...` with
differing casings across versions). A middleware builds a trie of literal segments
from the registered route table and rewrites only literal segments to canonical
casing before chi dispatch; parameter segments pass through untouched (item IDs and
filenames are never mangled). REJECTED: lowercasing whole paths (corrupts params);
registering all casings (explodes).

**Middleware order** (mirrors upstream `Startup.cs:171-254` where it's observable):
base-URL prefix mapping → forwarded headers → panic recovery/error shaping →
response timing → compression → CORS → **static `/web` (unauthenticated, by
design)** → auth (token resolution populates request identity; anonymous allowed
through) → case-normalizing rewrite → router dispatch (policy check from route
metadata) → IP-based access validation → WS upgrade interception (path-agnostic,
convention `/socket`) → handlers. `/health` and metrics mounted at the end.
The startup-wizard gate replicates upstream's wide-open-until-complete behavior
(`FirstTimeSetupHandler.cs:29`) — documented, not "fixed": clients depend on it
during first run.

## JSON strategy

Verified upstream behavior (`src/Jellyfin.Extensions/Json/JsonDefaults.cs`):
- `DefaultIgnoreCondition = WhenWritingNull` → **nulls are omitted, not emitted**.
- CamelCase profile re-cases **struct properties only** — no `DictionaryKeyPolicy`,
  so map keys (`ProviderIds`, `ImageTags`) keep their original casing.
- GUIDs: N-format lowercase out; dashed/braced/N accepted in.
- `NumberHandling = AllowReadingFromString`; enums as strings, case-insensitive in.

Consequences:
- **DTOs are hand-written** in `internal/dto` (`BaseItemDto` = 155 props, 0
  required, 1,038 nullables in spec — codegen produces unusable all-pointer
  structs; the null-omission fact means we need pointer+`omitzero` only for C#
  nullable/reference fields; see CONVENTIONS §3.2).
- Default encoder = stdlib `encoding/json` with `json:"PascalCase,omitzero"` tags.
- CamelCase profile (`Accept: application/json; profile="CamelCase"` — declared on
  362 operations) via a reflection-walking encoder in `api/render` (~300 loc):
  structs → lowerFirst tag name; maps → keys untouched; delegates everything else.
  REJECTED: token-stream re-caser (cannot tell struct keys from map keys); dual
  codegen marshalers (2× surface for a cold path).
- Scalars: `dto.UUID`, `dto.Time` (UTC ISO-8601 matching upstream converter),
  lenient numeric wrappers only where captures show clients sending strings.
- `api/bind`: one reflection binder driven by struct tags. Accepts CSV **and**
  repeated query params (both clients differ), case-insensitive enum values,
  `text/json` + `application/*+json` body aliases, raw `image/*` bodies for the
  4 upload ops (there is **no multipart anywhere** in this API).

## Item model

**DECISION: one wide `Item` struct + `Kind` enum (37 values) + static capability
table + kind-switch behavior functions. Per-kind service logic lives outside the
entity.**
RATIONALE: upstream's 25-class hierarchy exists to hang ~15 virtual methods and
capability flags off inheritance, and the data already lives in one wide row.
Interface-per-capability fragments every query result into type assertions;
embedding chains reproduce C# fragility without virtual dispatch (an "overridden"
method that doesn't fire is a silent bug Codex WILL write if allowed).
REJECTED: interface-per-capability; struct embedding hierarchy.

```go
// internal/item
type Kind uint8 // 37 values mirroring BaseItemKind; stored as its string name
const ( KindMovie Kind = iota + 1; KindSeries; KindSeason; KindEpisode; KindVideo;
        KindAudio; KindMusicAlbum; KindMusicArtist; KindMusicVideo; KindAudioBook;
        KindBook; KindPhoto; KindPhotoAlbum; KindBoxSet; KindPlaylist; KindPerson;
        KindGenre; KindMusicGenre; KindStudio; KindYear; KindTrailer; KindFolder;
        KindCollectionFolder; KindUserView; KindUserRootFolder; KindAggregateFolder;
        KindPlaylistsFolder; KindChannel; /* + LiveTv kinds reserved for stubs */ )

type Item struct {
    ID       uuid.UUID
    Kind     Kind
    ParentID uuid.UUID
    Path     string
    Name, SortNameOverride, OriginalTitle string
    // union of all persisted queryable fields (~60): dates, index numbers,
    // ratings, RunTimeTicks, SeriesID/SeasonID, ProductionYear, ProviderIDs,
    // ImageInfos, IsVirtual, LockedFields, Album fields, video fields, …
    Extra ExtraFields // rarely-queried per-kind payload; single JSON column
}

type KindDef struct { // pure data — no closures, no services
    IsFolder, SupportsPeople, SupportsAncestors, SupportsUserData,
    SupportsPlayedStatus, IsDisplayOnly bool
    MediaType      MediaType
    ClientTypeName string // only where it differs from the Kind name
}
var kindDefs = map[Kind]KindDef{ /* exhaustiveness-checked by test */ }

// Behavior = free functions, switch on Kind, pure:
func UserDataKeys(it *Item) []string        // ports GetUserDataKeys overrides
func PresentationUniqueKey(it *Item) string // Series/Episode grouping keys
func ComputeSortName(it *Item, o SortOpts) string
```

Kind mapping notes:
- Structural folders (AggregateFolder, UserRootFolder, CollectionFolder, UserView)
  are ordinary rows with folder kinds; `internal/library` owns their construction
  and the filesystem topology (`options.xml`, `*.collection`, `.mblink` — kept
  compatible so a library dir is portable between upstream and Deadfin).
- Person/Genre/Studio/MusicGenre/Year stay items (addressable, have images, appear
  in /Items) flagged `IsDisplayOnly`, excluded from scan-tree logic, maintained by
  post-scan validators.
- Upstream's DB discriminator is the .NET FQN with a **non-bijective** enum map
  (`ItemTypeLookup.cs`: TvChannel/LiveTvChannel collide; 4 kinds unmapped). Deadfin
  stores the BaseItemKind string; FQNs exist only in the importer.
- The BoxSet/Playlist in-memory visibility filter (`Folder.cs:918-929` routes to
  full materialization because shares live in the JSON blob) is **fixed, not
  reproduced**: shares are a table, visibility is SQL (`02-storage.md`). Divergence
  D-03 in `divergences.md`; observable behavior (which items a user sees) is
  identical — only the mechanism differs.

Iron laws for this package: CONVENTIONS §2 (no store-touching methods on Item, no
"is-an-Item" structs, switch on Kind never on type).

## Sessions, WebSocket, events

- Session = in-memory registry keyed `Client + DeviceId` (upstream
  `SessionManager.cs:478`); tokens are DB rows (a Device row *is* a token —
  upstream model kept, clients assume it). Server restart drops sessions, not
  tokens.
- Dead-reckoning: upstream fabricates 1 Hz progress between client reports
  (`SessionInfo.cs:360-437`) and watch-state depends on it. Ported: one goroutine
  per active-playback session on the injected clock, emitting automated progress.
  Watchdogs: 5-min idle check, 1-min inactive check (config-gated), matching
  upstream constants.
- WS: upgrade middleware is path-agnostic (upstream serves any path; clients use
  `/socket`). Envelope `{MessageType, MessageId, Data}`; inbound `Data` for Start
  messages is a CSV string (`"0,1500"` = dueTimeMs,periodMs) — client-supplied
  cadence floors, per-subscription. KeepAlive protocol: ForceKeepAlive sent on
  connect and when idle ∈ (45s, 60s); connection dropped at 60s; sweep every 12s
  (`SessionWebSocketListener.cs`). Client `KeepAlive` messages short-circuit
  (answered, never dispatched).
- Feeds: Sessions / ScheduledTasksInfo / ActivityLogEntry listeners driven by
  `internal/events` subscriptions; Sessions feed filters to the caller's own
  sessions unless admin (upstream behavior).
- Debounces (per-subscription in `internal/events`): LibraryChanged trailing 30s
  (`ServerConfiguration.LibraryUpdateDuration`), UserDataChanged 500ms.

## ffmpeg

- `internal/ffmpeg.Capabilities`: probed once at startup (+ on config change) from
  `-version/-encoders/-decoders/-filters/-hwaccels/-bsfs`; immutable value passed
  into builders. Includes the version-gate table (upstream pins ~20 minimum
  versions, `EncodingHelper.cs:74-90`). jellyfin-ffmpeg is a hard runtime
  dependency (patched build; min libavutil 56.70 etc., multithreaded CLI needs
  ffmpeg ≥ 7.0).
- `internal/encoding`: pure `BuildXxxArgs(job EncodingJob, caps Capabilities)
  ([]string, error)` decomposed like upstream (input / map / video / audio /
  subtitle burn-in / quality / stream-copy predicates). Phase-1 scope = software
  only: x264/x265/libsvtav1, sw filter chain (`GetSwVidFilterChain`,
  `EncodingHelper.cs:3853`), sw `tonemapx`, text + graphical subtitle burn-in,
  DV/HDR10+ metadata-removal bsf logic. All 6 vendor filter chains, 10 hw-device
  arg builders, and 6 hw decoders are excluded (≈25% of the file) — the single
  biggest scope reduction in the project.
- `internal/ffmpeg.TranscodeManager`: one application-owned manager maps canonical
  output paths to jobs under a narrow mutex. An identical output/argv request
  shares the running job; a conflicting request is rejected. Per job,
  `exec.Cmd` runs with `SysProcAttr{Setpgid: true}` and a manager-owned waiter;
  each HTTP request owns only a growing-file reader lease, so client cancellation
  never directly kills a job shared by another request. Progressive jobs receive
  `q` after 10 seconds with zero readers, then the whole process group receives
  `SIGKILL` after the 5-second grace. Startup, reap, partial-output cleanup, and
  cleanup retries remain manager-owned and are joined by context-bounded,
  retry-safe shutdown. Output mutations are descriptor-relative beneath the
  canonical transcode root and reject symlink traversal.
- Progressive output names reproduce `StreamingHelpers.GetOutputFilePath`
  exactly: UTF-16LE MD5 rendered with .NET GUID byte order over
  `mediaPath + "-" + UserAgent + "-" + deviceId + "-" + playSessionId`, followed
  by the lowercase container extension. Startup wipes the owned transcode
  directory before accepting jobs (upstream behavior; single-instance assumption).
  HLS adds segment readiness, restart rules, 60-second activity, and throttle
  state in WP-P6-02/03; those mechanisms are not present in the progressive subset.
- `internal/probe`: ffprobe `-print_format json -show_streams -show_chapters
  -show_format`, hard timeout, size-capped stdout → `Normalize()` pure. Port the
  ~365-line `GetMediaStream` core + format/aspect/framerate helpers first; the
  ~600 lines of music-tag mining are a separate deferred WP.

## Images

**DECISION: libvips via govips; cgo confined to `internal/images`.**
RATIONALE: webp encode quality/speed and full EXIF orientation support; pure-Go
stacks are weak at webp encode. The rest of the binary stays pure Go
(`modernc.org/sqlite` chosen partly for this).
Pixel parity with SkiaSharp is unachievable (different resamplers) — validation is
perceptual from day one: SSIM ≥ 0.98 against stock outputs on the image corpus,
plus exact checks on dimensions, format, cache headers. Blur is upstream-quirky
(fixed 3×3 convolution applied N times, `SkiaEncoder.cs:28`) — approximate
perceptually, note in divergences (D-05).
Operations: resize/fill (Mitchell upscale / linear down, matching upstream's
choices), format conversion (webp/jpg/png/svg out — **no AVIF**, upstream has
none), EXIF orient, blurhash, percent-played bar + unplayed-count badge overlays,
collage/splashscreen, trickplay tile composition. Cache: comma-joined key string in
upstream's fixed field order → MD5 → 1-char shard dir; versioned key for
whole-cache invalidation (`ImageProcessor.cs:254-351`, including upstream's
duplicate-`p=` quirk — keep it, cache keys are server-private but the ETag/tag
interplay is observable).

## Composition root

`internal/server.Deps` is the one struct holding every service; `cmd/deadfin`
builds it in dependency order (config → stores → managers → api). Upstream's
`Lazy<T>` cycle hacks (`ApplicationHost.cs:573-605`) signal real manager cycles
(library ↔ providers ↔ userviews); Deadfin breaks them with narrow interfaces
declared consumer-side (e.g. scan needs `ProbeRunner`, not the whole ffmpeg pkg)
— never with laziness or globals.

## Config

XML stores kept compatible where user-visible/migratable: `system.xml`,
`encoding.xml`, `network.xml`, `branding.xml`, per-library `options.xml`.
`internal/config` = typed stores with load/save + change notification via events.
Env/appsettings keys (`JELLYFIN_*`-style) mapped to flags/env in `cmd/deadfin`.
Paths object mirrors upstream layout (`data/`, `cache/`, `config/`, `log/`,
`metadata/`, `transcodes/`, `trickplay/`, `root/default/` library topology) so the
importer and any future in-place inspection tooling stay simple.

Dashboard saves (`POST /System/Configuration`, `/System/Configuration/{key}`,
`/System/Configuration/Branding`) decode the posted document over the upstream
constructor defaults, write the XML atomically, and publish one
`config.Change`. Readers take a fresh snapshot per request or job, so most
members apply at once. `cmd/deadfin` runs one configuration consumer that
re-probes the hardware device facts after an `encoding.xml` save (D-45) and
logs a restart notice for `network.xml` `BaseUrl` and `AutoDiscovery`, which
the HTTP mount and the discovery responder read once at startup, as upstream
does. `LocalNetworkSubnets` apply to the next request. Deadfin persists but
does not use `CachePath`, `MetadataPath`, `TranscodingTempPath`, the port and
HTTPS members, UPnP, IPv4/IPv6 binding, `KnownProxies`, `RemoteIPFilter`, and
`PublishedServerUriBySubnet`; its paths derive from the data directory and its
listener from `--listen`.
