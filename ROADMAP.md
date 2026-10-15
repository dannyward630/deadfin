# Roadmap

This page lists the work that comes next, in rough order. Plans change with
alpha feedback. To ask for something, open a feature request or start a
[discussion](https://github.com/deadfin-org/deadfin/discussions).

## Now: v0.3.0, the first alpha

v0.3.0 is the first public alpha. It is the release that runs a household's
media day to day in place of Jellyfin, with Jellyfin kept as the fallback.
Release candidates are on the [tags page](https://github.com/deadfin-org/deadfin/tags).
The work until v0.3.0 is fixes for what alpha testers and daily use find.

## Next: v0.4.0

- **Device management.** List the devices that are signed in to an account,
  and sign one out. This also fixes the dashboard's Devices page.
- **Security hardening.** Tighter checks on session updates and on access to
  media attachments.
- **Playback decisions per client.** The server ranks the ways a client can
  play a file and picks the best one for that client.
- **Playback diagnostics.** A clear answer to "why did this transcode?" for
  each playback.
- **Request and catalog API.** A Deadfin-native API under `/deadfin/v1/`,
  with an OpenAPI description, for requests, the catalog and events.
- **Seerr integration.** Requests from Seerr flow through the native API.
- **Media lifecycle.** Rules that find watched or stale media and clean it
  up safely, with awareness of hard links on NFS.
- **Anime.** Franchise collections built from AniList relations, and dual
  Japanese and English subtitles with furigana.
- **arm64 NAS hosts.** Run and measure Deadfin on an arm64 NAS.

## Planned

### Live TV

Live TV arrives in phases:

1. M3U tuners and XMLTV guides: channels and programs in the guide. A local
   HDHomeRun works through its M3U lineup.
2. Xtream tuners: channels and guide from an Xtream-compatible source.
3. Live playback through a shared stream broker. Viewers of the same channel
   share one upstream stream.
4. Logical channels with more than one source, and failover between them.
5. A native HDHomeRun tuner type and a Live TV guide in the web client.

### SyncPlay

Group watching with SyncPlay, as in Jellyfin. The work starts with a design
spike.

### Transcoding

- **Distributed transcoding.** Transcode on GPUs in other machines. A
  Deadfin server sends transcode jobs to remote worker processes, with
  fallback to local transcoding.
- **NVENC fallback.** Transcode in software when NVENC sessions or GPU
  memory run out.

### Jellyfin parity

- OMDb and studio-image metadata providers.
- Remote image search and item lookup that match Jellyfin's listings and
  pagination.
- Anime episode metadata from TVDB and AniDB.
