# Client compatibility

How each Jellyfin client works against Deadfin. The results come from a
fresh install with SQLite and default settings, jellyfin-web 12.0, and
generated test media (H.264, HEVC 10-bit, AV1, multi-audio, sidecar SRT,
embedded ASS and PGS, FLAC and MP3).

To add a result, test a client with the steps below and open a
"Client compatibility report" issue.

## Legend

| Mark | Meaning |
|---|---|
| **pass (UI)** | Verified with the real client. |
| **pass (API)** | Verified with the client's requests sent by hand: the same device profile and URLs that the client builds, from its source or a captured profile. The real app is not run. |
| **not verified** | No result yet. Steps are under [How to test a client](#how-to-test-a-client). |
| **n/a** | The client or Deadfin does not have the feature. |

## Matrix

| Client | Login and QuickConnect | Browse | Direct play | Transcode | Subtitles | Resume sync |
|---|---|---|---|---|---|---|
| jellyfin-web 12.0 (Chromium) | pass (UI): wizard, sign-in. QuickConnect: pass (API) | pass (UI): home, latest, next up, libraries, search, favorites | not verified | not verified | not verified | pass (API) |
| Swiftfin iOS (App Store) | pass (API): password and QuickConnect flows | pass (API) | pass (API), VLC player: every test file | pass (API), native player: HEVC, AV1, ASS and PGS sources transcode | pass (API): SRT, ASS, PGS as External | not verified |
| Swiftfin tvOS (App Store) | not verified | not verified | not verified (same profile as iOS) | not verified | not verified | not verified |
| Jellyfin Android (phone) | not verified | not verified | not verified | not verified | not verified | not verified |
| Jellyfin Android TV | not verified | not verified | pass (API), ExoPlayer profile: all but AV1 | pass (API): AV1 transcodes to H.264 | pass (API): SRT External; ASS and PGS burned in (Encode) | not verified |
| Infuse | not verified | not verified | pass (API), Infuse-shaped profile: every test file | n/a (Infuse direct-plays) | pass (API): SRT External, ASS and PGS embedded | not verified |
| Jellyfin Media Player | not verified | not verified | not verified | not verified | not verified | not verified |
| Findroid | not verified | not verified | not verified | not verified | not verified | not verified |
| Streamyfin | not verified | not verified | not verified | not verified | not verified | not verified |
| Chromecast (from jellyfin-web or Android) | n/a | n/a | not verified | not verified | not verified | not verified |
| DLNA | n/a | n/a | n/a: Deadfin has no DLNA server | n/a | n/a | n/a |

Also verified with the API:

- **Low-bandwidth transcode** (H.264/AAC HLS at 1.5 Mbit/s): every test file
  without a sidecar subtitle transcodes. The first segment of the HEVC 10-bit
  file came back in 0.86 s.
- **Music**: direct play of FLAC and MP3 through `/Audio/{id}/universal`
  works, with album art.

## How to test a client

Use one test server with your own media. Use a
movie with an external `.srt`, a movie with two audio tracks, an HEVC 10-bit
file, and a series with at least three episodes. For each client, record pass
or fail per column, the client version, and the server log for each
failure (`docker logs deadfin`).

### Every client: the six columns

1. **Login and QuickConnect.** Add the server by URL. Sign in with a password.
   Sign out. Choose QuickConnect in the client, then in jellyfin-web go to
   Profile → Quick Connect and enter the code. The client must sign in.
2. **Browse.** Open Home: Continue Watching, Next Up, Latest. Open each
   library. Search for a movie title. Open a series, a season, an episode.
3. **Direct play.** Play the H.264 movie. In Dashboard → Activity or the
   playback info overlay, the method must be Direct Play. Seek forward 30 s.
4. **Transcode.** Set the client's quality to the lowest bitrate (for example
   720p 1 Mbit/s). Play the HEVC file. The dashboard must show Transcoding.
   Seek forward.
5. **Subtitles.** Play the movie with the external `.srt`. Turn on English
   subtitles, then change to another track, then off. Change the audio
   track. Repeat once with a low bitrate (transcode).
6. **Resume sync.** Play an episode for 2 minutes and stop. On a second
   client (or jellyfin-web), the episode must be in Continue Watching at the
   same position. Resume it there. Play to the end; Next Up must show the
   next episode.

### Client notes

- **Swiftfin iOS and tvOS**: test both players (Settings → Video Player →
  Native and VLC). The native player transcodes MKV, so also run step 5 with an
  MKV file and a sidecar SRT. On tvOS, also test QuickConnect
  for a user that is already signed in (known Swiftfin defect C-1 in
  [../swiftfin-client-defects.md](../swiftfin-client-defects.md)).
- **Jellyfin Android and Android TV**: test ExoPlayer (default) and, on the
  phone, the web player. Play the AV1 file: Android TV must transcode it.
- **Infuse**: add the server as Jellyfin. Infuse syncs state on its own
  schedule: in step 6, pull to refresh before you check.
- **Jellyfin Media Player**: it uses jellyfin-web from the server.
- **Findroid, Streamyfin**: test downloads too (offline play, then sync the
  played state when back online).
- **Chromecast**: cast from jellyfin-web and from Jellyfin Android. The cast
  receiver fetches the stream itself; record whether it plays and whether
  the position syncs back.
- **DLNA**: not available in Deadfin. Record only whether a DLNA client finds
  anything (it must not).
