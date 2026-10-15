# Provenance

Deadfin is a Go port of the Jellyfin media server. This file records where
the code comes from, which upstream revision it follows, and why it uses
the GNU General Public License, version 2 only (`GPL-2.0-only`).

Deadfin is not affiliated with or endorsed by the Jellyfin project.
"Jellyfin" and "Emby" are trademarks of their respective owners. Deadfin
uses the names only to identify the software it is compatible with and
derived from.

## Lineage

```
Emby (MediaBrowser) ──fork 2018──▶ Jellyfin ──Go port──▶ Deadfin
```

1. **Emby.** The original server was named MediaBrowser and was later
   renamed Emby. It was released under the GPL.
2. **Jellyfin.** Jellyfin forked the last GPL release of Emby in 2018. The
   Jellyfin tree still carries the original names: the `MediaBrowser.*` and
   `Emby.*` projects (for example `MediaBrowser.Controller`,
   `MediaBrowser.Model`, `Emby.Naming`, `Emby.Server.Implementations`).
3. **Deadfin.** Deadfin reimplements the Jellyfin server in Go. It serves the
   Jellyfin HTTP and WebSocket API so that unmodified Jellyfin clients
   (jellyfin-web, Swiftfin, and others) work against it.

## A source-informed port

Deadfin is a source-informed port written with AI coding agents. The agents
read the upstream C# source and translated its behavior into Go; the
maintainer directed and reviewed the work. Some parts follow the upstream
structure closely (for example the playback decision engine, the ffmpeg
argument builder, and file-name parsing). Other parts are new designs that
match the upstream wire behavior. Deadfin treats the whole work as a
derivative of Jellyfin and licenses it to match.

The behavior target is one pinned upstream commit:

| Item | Value |
| --- | --- |
| Upstream repository | <https://github.com/jellyfin/jellyfin> |
| Pinned commit | `b04614e18d2aeed8d3d3de920c381c6bd5b97bdb` (see `upstream/PIN`) |
| API version served | Jellyfin 12.0.0 |

Inputs derived from upstream (the OpenAPI document under `ledger/inputs/`,
fixtures recorded from the C# code, and the request corpora under
`corpora/`) are distributed under the same license as the rest of Deadfin.

Every intentional difference from upstream behavior is recorded in
`docs/divergences.md`.

## Why GPL-2.0-only

A port that translates GPL source is a derivative work, so Deadfin uses a
license compatible with Jellyfin's.

Jellyfin's own license metadata is not consistent:

- The root `LICENSE` at the pinned commit is the GNU GPL version 2 text,
  with no "or any later version" grant. The GitHub license badge in the
  upstream README also says GPL 2.0.
- 8 of the 42 `.csproj` files at the pinned commit declare
  `GPL-3.0-only`:
  `Emby.Naming`, `MediaBrowser.Common`, `MediaBrowser.Controller`,
  `MediaBrowser.Model`, `Jellyfin.Data`,
  `Jellyfin.Database.Implementations`, `Jellyfin.Extensions`, and
  `Jellyfin.MediaEncoding.Keyframes`. Deadfin derives from several of
  these directly.

GPL-2.0-only and GPL-3.0-only code cannot be combined into one
distributable program. Jellyfin ships as one program. If the `GPL-3.0-only`
tags governed, Jellyfin itself could not be distributed. So the tags cannot
be authoritative, and the governing license is the version 2 root
`LICENSE`.

Deadfin chooses version 2 only because it is correct under every reading:

- If upstream is GPL-2.0-only, a derivative must be GPL-2.0-only.
- If upstream is GPL-2.0-or-later, a derivative may choose GPL-2.0-only.

GPL-3.0 is correct only under the "or later" reading, which the upstream
root license does not grant.

## Third-party components

The source tree and the container image include third-party components
under their own licenses:

| Component | License | Where |
| --- | --- | --- |
| SkiaSharp native library 3.119.4 | MIT, plus bundled notices | `third_party/skia-sharp/3.119.4/` |
| jellyfin-ffmpeg 8.1.1-1 (container image only) | GPL-3.0-or-later build of FFmpeg | `third_party/deadfin-image/NOTICE` |
| libvips (container image only) | LGPL-2.1-or-later | `third_party/deadfin-image/NOTICE` |
| Go module dependencies | see each module's `LICENSE` | `go.mod` |

Deadfin runs ffmpeg and ffprobe as separate processes.

## Records

- `docs/divergences.md` — every intentional difference from upstream.
