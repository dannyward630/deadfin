# 07 — Extension Seams

Decision D6 (settled): **spec the extension architecture; port no plugins; write no
loader this sprint.** This document is the design artifact that decision requires:
the interfaces a future plugin system attaches to, shaped so adding a loader later
is additive, not a refactor.

## Three things upstream calls "plugins" (do not conflate)

1. **In-tree providers** (TMDb, OMDb, MusicBrainz, AudioDb, StudioImages,
   ListenBrainz): compiled into the server, wear the plugin shape only for
   dashboard settings pages. **Core functions are ordinary Go packages
   in P8** (`internal/providers/*`). Without TMDb there is no movie/TV metadata at
   all.
2. **External .NET plugins** (`PluginManager.cs` + `AssemblyLoadContext`): a Go
   server cannot load these — an incompatibility, not a porting task. Measured
   cost of dropping them: **all subtitle download** (zero in-tree
   `ISubtitleProvider`), lyrics download, Intro Skipper, Trakt, LDAP/SSO, Kodi
   sync.
3. **Client-side web plugins** (`jellyfin-web/src/plugins/`): browser modules, no
   server involvement. Ignore.

## Provider interfaces (Go)

Consumer-side, narrow, context-first. These are the seams `internal/library`'s
refresh pipeline and the P8 providers implement. A future loader can expose the
same seams to out-of-process plugins.

```go
// internal/providers/seam.go  (names final; signatures the contract)

type ItemLookup struct {            // ← ItemLookupInfo
    Kind item.Kind; Name string; Year *int
    ProviderIDs map[string]string   // "Tmdb", "Imdb", "Tvdb", ...
    Language, Country string
    // per-kind extras via typed sub-structs (EpisodeLookup, AlbumLookup, …)
}

type MetadataResult struct { Item *item.Item; People []Person; HasMetadata bool }

type MetadataProvider interface {    // ← IRemoteMetadataProvider<T>
    Name() string
    Supports(kind item.Kind) bool
    Search(ctx context.Context, l ItemLookup) ([]RemoteSearchResult, error)
    Fetch(ctx context.Context, l ItemLookup) (MetadataResult, error)
}

type ImageProvider interface {       // ← IRemoteImageProvider
    Name() string
    Supports(it *item.Item) bool
    ImageTypes(it *item.Item) []dto.ImageType
    Images(ctx context.Context, it *item.Item) ([]RemoteImageInfo, error)
}

type SubtitleProvider interface {    // ← ISubtitleProvider (NO implementations this sprint)
    Name() string
    Formats() []string
    Search(ctx context.Context, req SubtitleSearchRequest) ([]RemoteSubtitleInfo, error)
    Get(ctx context.Context, id string) (SubtitleResponse, error)
}

type LyricProvider interface { /* ← ILyricProvider; same shape; none in-tree */ }

type MediaSegmentProvider interface { /* ← IMediaSegmentProvider (intro/credits); none in-tree */ }
```

Registration: explicit, in the composition root — `providers.Registry` built in
`cmd/deadfin` with ordered lists per interface (upstream ordering semantics:
per-library provider enable/order lives in library `options.xml`, honored by the
refresh pipeline). **No global registry, no init() self-registration** (CONVENTIONS
1.1) — that discipline is exactly what makes a loader additive later: a loader is
just another way to append to the Registry before serving.

## Configuration + dashboard pages

- Provider config: one typed struct per provider, persisted like upstream plugin
  configs (`config/plugins/<name>.xml` — importer-friendly), exposed via
  `GET/POST /Plugins/{pluginId}/Configuration` using the provider's stable
  upstream plugin GUID (so jellyfin-web's existing pages work unmodified).
- `GET /web/ConfigurationPages` lists the in-tree providers' pages;
  `GET /web/ConfigurationPage?name=X` serves their `config.html` (embedded assets,
  `text/html`) — upstream embeds these in the provider assemblies; Deadfin embeds
  them in the provider packages (`embed.FS`). This is why these two endpoints stay
  in scope despite "no plugins".
- `GET /Plugins` (P9) reports the in-tree providers as installed plugins with
  their upstream GUIDs/versions — the dashboard then renders something truthful
  rather than an empty page. (If this proves confusing in practice, flip to empty
  list — one-line change; record in annotations.)

## Future loader — what the seams are shaped for

**DECISION: shape for out-of-process plugins (subprocess + gRPC/stdio), not WASM,
not compile-time-only.**
RATIONALE: every interface above is already context-first, request/response, and
serialization-friendly (no `*sql.DB`, no callbacks, no streaming state except
`Get` bodies — modeled as size-limited byte payloads). That is precisely the shape
that survives a process boundary (hashicorp/go-plugin-style). WASM constrains
plugin authors' language/network access severely today; compile-time registration
(build-tag providers) is already free with the Registry and needs no design.
CONSTRAINT this imposes now (cheap): provider interfaces must not grow methods
taking live server objects — lookups and results are plain data. The exhaustive
check: every parameter/return type in the seam file must be marshalable; a CI
lint asserts it.

What a loader phase adds (out of scope now, sized for planning): plugin
manifest + discovery dir, subprocess lifecycle supervision, per-plugin config
schema registration, the 17 Plugin-tag endpoints backed for real, versioned wire
contract for the seam types. Nothing in that list touches the seam signatures —
that is the additive-not-refactor test.

## Honest cost statement (mirrors 00 §Deferred)

Until a loader exists: no subtitle/lyric downloading (embedded + sidecar files work
— local path through the subtitle encoder), no Intro Skipper (MediaSegments API
serves only imported/empty data), no Trakt scrobbling (ListenBrainz IS ported —
it's in-tree), no LDAP/SSO, no Kodi sync.
