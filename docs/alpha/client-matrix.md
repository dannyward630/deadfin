# Client compatibility

How each Jellyfin client works against Deadfin, and how each result was
found. Every result names its test method, client version, date and limits
under [Evidence](#evidence).

To add a result, test a client with the steps below and open a
"Client compatibility report" issue.

## Legend

| Mark | Meaning |
|---|---|
| **pass (app)** | The real app ran against Deadfin and the step worked. The evidence entry names the device. |
| **fail (app)** | The real app ran and the step failed. The entry names the issue. |
| **QA matrix** | Covered by the Apple TV QA matrix: PlaybackInfo decision, server check and a macOS AVPlayer probe, with no Apple TV run. Per-cell results, open failures included, are in [atv-matrix.md](../qa/atv-matrix.md). |
| **pass (API)** | Only request-level evidence: the client's device profile and URLs, taken from its source or a captured profile, were sent to Deadfin, and the answers were checked. The real app was not run for this cell. |
| **not verified** | No result yet. Steps are under [How to test a client](#how-to-test-a-client). |
| **n/a** | The client or Deadfin does not have the feature. |

## Matrix

| Client | Login and QuickConnect | Browse | Direct play | Transcode | Subtitles | Resume sync |
|---|---|---|---|---|---|---|
| jellyfin-web 12.0 (Chromium) | pass (app): wizard, sign-in [E1]. QuickConnect: pass (API) [E2] | pass (app): home, latest, next up, libraries, search, favorites [E1] | not verified | not verified | not verified | pass (API) [E2] |
| Swiftfin tvOS 1.4.1, native player (AVPlayer) | not verified (E4 used an existing sign-in) | pass (app) [E4] | pass (app): HDR10 HEVC [E4]. Dolby Vision profile 7 remux: fail (app) on rc.5, fix in rc.6, Apple TV retest pending [E5] | fail (app) on rc.5 at 20 Mbit/s, fix in rc.6, Apple TV retest pending [E5]. QA matrix [E3] | QA matrix [E3] | pass (app) [E4] |
| Swiftfin tvOS and iOS, VLC player | not verified | not verified | QA matrix [E3]; pass (API), iOS profile: every test file [E2] | pass (API), iOS native profile: HEVC, AV1, ASS and PGS sources transcode [E2] | pass (API): SRT, ASS, PGS as External [E2] | not verified |
| SharkParty (tvOS) | not verified | not verified | QA matrix [E3] | QA matrix [E3] | QA matrix [E3] | not verified |
| Infuse 8.5.7 for Mac (free tier) | pass (app): password sign-in [E6] | pass (app): libraries. Home rows: fail (app) on rc.3, fixed in rc.4 [E6] | pass (app): direct stream, no transcode [E6] | n/a (Infuse direct-plays) | not verified | not verified |
| Infuse (Apple TV) | not verified | not verified | pass (API), Infuse-shaped profile: every test file [E2] | n/a (Infuse direct-plays) | pass (API): SRT External, ASS and PGS embedded [E2] | not verified |
| Jellyfin Android (phone) | not verified | not verified | not verified | not verified | not verified | not verified |
| Jellyfin Android TV | not verified | not verified | pass (API), ExoPlayer profile: all but AV1 [E2] | pass (API): AV1 transcodes to H.264 [E2] | pass (API): SRT External; ASS and PGS burned in (Encode) [E2] | not verified |
| Jellyfin Media Player | not verified | not verified | not verified | not verified | not verified | not verified |
| Findroid | not verified | not verified | not verified | not verified | not verified | not verified |
| Streamyfin | not verified | not verified | not verified | not verified | not verified | not verified |
| Chromecast (from jellyfin-web or Android) | n/a | n/a | not verified | not verified | not verified | not verified |
| DLNA | n/a | n/a | n/a: Deadfin has no DLNA server | n/a | n/a | n/a |

## Evidence

**E1. Fresh-install pass, jellyfin-web in Chromium.** 2026-10-12. Deadfin
`master` 863451d (between v0.2.31 and v0.2.32), SQLite, default settings,
jellyfin-web 12.0 from `jellyfin/jellyfin:12.0-rc2` (by digest), generated
test media (H.264, HEVC 10-bit, AV1, multi-audio, sidecar SRT, embedded ASS
and PGS, FLAC and MP3). Limits: no TMDb key, so no remote metadata; the
playback columns were not run in the browser.

**E2. Request replay.** 2026-10-12, same server and media as E1.
PlaybackInfo with each client's real device profile (Swiftfin iOS VLC and
native from Swiftfin source, Jellyfin Android TV ExoPlayer, an Infuse-shaped
profile), then a fetch of every returned URL. Also checked this way:

- **Low-bandwidth transcode** (H.264/AAC HLS at 1.5 Mbit/s): every test file
  without a sidecar subtitle transcodes. The first segment of the HEVC 10-bit
  file came back in 0.86 s.
- **Music**: direct play of FLAC and MP3 through `/Audio/{id}/universal`
  works, with album art.

Limits: no app ran. A pass shows that Deadfin gives the answer the app
expects, not that the app plays it.

**E3. Apple TV playback QA matrix.** 2026-10-12 to 2026-10-13, Deadfin
v0.2.32 to v0.2.35, against a real library (about 50 cells: containers,
codecs, HDR, audio, subtitles, seeks). Three player profiles transcribed
from source: SharkParty AVPlayer, SharkParty VLC (also stock Swiftfin's
default player) and stock Swiftfin AVPlayer. For each cell: the PlaybackInfo
decision against the expected decision, a server-side check of every
fetched playlist and segment, and an AVFoundation playback probe on an
Apple silicon Mac (start time, seeks, decode errors). Method, cells and
results: [../qa/atv-matrix.md](../qa/atv-matrix.md). Limits: the probe is
macOS AVFoundation, not tvOS; the operator column (a person watching each
cell on an Apple TV) is open. SharkParty's own tvOS simulator evidence is its
CI install-and-launch check.

**E4. Household use, Swiftfin on Apple TV.** 2026-10-14 to 2026-10-15,
Deadfin v0.3.0-rc.3 to v0.3.0-rc.5, Swiftfin tvOS 1.4.1 with the native player on an
Apple TV 4K, against a real library (movies and series). Browse, play
and resume work in daily use. Guardians of the
Galaxy (HEVC Main 10, HDR10, 2160p) direct-plays. Limits: one household, one
device; the client was already signed in, so QuickConnect and a new sign-in
were not part of this run.

**E5. Swiftfin tvOS AVPlayer failures, #587.** 2026-10-15, Deadfin
v0.3.0-rc.5, Swiftfin tvOS 1.4.1 native player on Apple TV.

- Dolby Vision profile 7 (`DOVIWithEL`) MKV remuxes show "Unable to load
  this item". The init segment carried a profile 7 `dvcC` under an `hvc1`
  master playlist.
- An HEVC HDR10 2160p movie at a 20 Mbit/s limit, which forces an H.264 QSV
  transcode, stops. The master playlist advertised a lower H.264 level than
  the encoder wrote.

PR #588 fixes both (D-65, D-66) and is in v0.3.0-rc.6. On macOS
AVFoundation the fixed streams play. As of 2026-10-15, the Apple TV retest
on rc.6 is pending. The Apple TV check for #523 (AVPlayer -12318 on QSV
transcodes) is part of the same retest.

**E6. Infuse 8.5.7 for Mac.** 2026-10-14, Deadfin v0.3.0-rc.3, Infuse for
Mac (client `Infuse-Direct`, free tier) on an Apple silicon Mac, against a
real library. Password sign-in, library browse and playback of a movie
work; the server reported a direct stream with no transcode. Two defects
found and fixed in v0.3.0-rc.4: Home rows were empty because
`/Items/Latest` returned 400 (#571), and `GET /MediaSegments/{itemId}`
returned 404 (#572). Limits: the free tier blocks TrueHD, AC-3 and lossless
audio tracks, so Infuse picks the first track it can decode; audio
cells need Infuse Pro. Infuse for Mac is not Infuse on Apple TV.
The Infuse column of the QA matrix is #569.

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
