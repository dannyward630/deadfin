# Deadfin documentation

## Using Deadfin

- [Install](alpha/install.md): run Deadfin, volumes, settings, the TMDb key,
  the web client, first-run setup, reverse proxy, hardware transcoding.
- [Deployment and security](alpha/deployment-security.md): hardened Docker,
  Podman, Kubernetes, NAS and native Linux setups, backups and upgrades.
- [Import from Jellyfin](alpha/importing-from-jellyfin.md): bring users,
  watch state, playlists, collections and libraries across.
- [Known differences](alpha/known-differences.md): where Deadfin behaves
  differently from Jellyfin, in plain words, and the open gaps.
- [Client compatibility](alpha/client-matrix.md): which Jellyfin clients are
  verified, and how to test the rest.
- [Report a bug](alpha/reporting-bugs.md): what to send when something fails.
- [FAQ](alpha/faq.md): short answers to the first questions.
- [Support](../SUPPORT.md): where to ask questions and report problems.
- [Roadmap](../ROADMAP.md): what comes next.

## How Deadfin works

- [Architecture](01-architecture.md): the package tree and the Jellyfin code
  each package follows, the HTTP stack, the item model, sessions, ffmpeg and
  images.
- [Storage](02-storage.md): the database schema, SQLite and PostgreSQL, and
  the Jellyfin importer.
- [API surface](03-api-surface.md): the Jellyfin API that Deadfin serves,
  including the routes that clients use outside the OpenAPI spec.
- [Parity ledger](05-parity-ledger.md): the generated record of which API
  operations work.
- [Divergences](divergences.md): every intentional difference from Jellyfin,
  with its ID (D-nn), reason and effect.
- [Risk register](06-risks.md): the technical risks of the port and their
  mitigations.
- [Extension seams](07-extension-seams.md): metadata providers and the
  interfaces a plugin system attaches to.
- [Control plane](08-control-plane.md): the design of the Deadfin-native API
  above Jellyfin parity.
- [Swiftfin client defects](swiftfin-client-defects.md): Swiftfin faults that
  also occur against Jellyfin.
- [Apple TV playback matrix](qa/atv-matrix.md): the tvOS playback check and
  its results.
