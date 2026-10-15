# Changelog

All notable changes to Deadfin are recorded in this file. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and Deadfin uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The release workflow copies the section of a tag into its GitHub Release. A
`vX.Y.Z` tag needs a `## [X.Y.Z]` section. A `vX.Y.Z-rc.N` tag uses its own
section, then the `X.Y.Z` section, then `Unreleased`.

Container images: `ghcr.io/deadfin-org/deadfin:<tag>`. `:alpha` follows the
newest release or release candidate. `:latest` follows the newest release.

## [Unreleased]

v0.3.0 is the first public alpha of Deadfin.

### Highlights

- **Audio and subtitle choice carries to the next episode** (D-36). When you
  pick a language or a subtitle track in one episode, the next episode of the
  same series starts with the matching tracks. Subtitles off also carries.
- **Anime search finds every title** (D-48). Search matches the English,
  romaji, native and alias titles from AniList and AniDB, with a kana/romaji
  fold. "Shingeki" and "進撃" both find "Attack on Titan".
- **PostgreSQL support.** SQLite stays the default. Set
  `DEADFIN_DATABASE_URL` (or `--database-url`) to a `postgres://` URL to run
  on PostgreSQL. See [docs/alpha/install.md](docs/alpha/install.md#database).
- **Security fixes** from the release-candidate review: sign-out revokes the
  token, video attachments are scoped to the user, the image downloader
  refuses special-purpose address ranges, and the toolchain is Go 1.26.9.

### Added

- `CHANGELOG.md`. Each GitHub Release body comes from its section here.
- Release images carry a build provenance attestation and an SPDX SBOM for
  each architecture. The SBOMs are attached to the GitHub Release.
- `:alpha` and `:latest` image tags.

### Changed

- The quick start and the install guide put TMDb API key setup first.
- `DEADFIN_DATA_DIR` sets the data root. `JELLYFIN_DATA_DIR` stays as the
  fallback, and the image sets `DEADFIN_DATA_DIR=/var/lib/deadfin`. Deadfin
  logs a warning for each set `JELLYFIN_CONFIG_DIR`, `JELLYFIN_CACHE_DIR`,
  `JELLYFIN_LOG_DIR` or `JELLYFIN_WEB_DIR`.

## [0.3.0-rc.3] - 2026-10-14

Third release candidate for v0.3.0 and the first public cut. The server code
is the same as 0.3.0-rc.2. This tag is the root of the public history at
github.com/deadfin-org/deadfin.

### Added

- Release images on `ghcr.io/deadfin-org/deadfin` for linux/amd64 and
  linux/arm64, built by GitHub Actions, with a GitHub Release for each tag.

## [0.3.0-rc.2] - 2026-10-14

Second release candidate for v0.3.0. It adds the fixes from the
release-candidate security review.

### Security

- `POST /Sessions/Logout` revokes the caller's access token and ends the
  device sessions of that token.
- Video attachments (fonts and other embedded files) are scoped to the user
  of the token. Active content types download as attachments and do not
  render in the browser.
- The remote-image downloader refuses CGNAT (100.64.0.0/10) and the other
  IANA special-purpose address ranges.
- The toolchain is Go 1.26.9, with updated `golang.org/x/image`,
  `golang.org/x/net`, `golang.org/x/text` and `go-chi/chi`. govulncheck
  reports no findings on the binary.

### Added

- The deployment and security guide:
  [docs/alpha/deployment-security.md](docs/alpha/deployment-security.md).

## [0.3.0-rc.1] - 2026-10-14

First release candidate for v0.3.0, the community alpha.

### Added

- PostgreSQL as a database backend, selected with `DEADFIN_DATABASE_URL`.
  SQLite stays the default.
- Release-candidate tags `vX.Y.Z-rc.N` publish images on the `rc` channel.
- The public README, SECURITY, CONTRIBUTING, and issue templates.
- The GPL-2.0 license, PROVENANCE.md, REUSE metadata and SPDX headers. The
  image carries the license notices and a source offer.

### Changed

- The Go module path is `github.com/deadfin-org/deadfin`.

### Fixed

- The SQLite query for folder played counts uses a faster plan on large
  libraries.

[Unreleased]: https://github.com/deadfin-org/deadfin/compare/v0.3.0-rc.3...HEAD
[0.3.0-rc.3]: https://github.com/deadfin-org/deadfin/releases/tag/v0.3.0-rc.3
