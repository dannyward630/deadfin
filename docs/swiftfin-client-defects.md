# Swiftfin client defects

Known Swiftfin faults observed against stock Jellyfin 12.0-rc2 during the first
T3 stock baseline run (2026-08-14 and 2026-08-16). These are client faults, not
stock or Deadfin faults. They are recorded so future runs do not rediscover them.

Server: `jellyfin/jellyfin@sha256:2d1a69be5aa2695d0c2df90ebeb76bbcc6efc0a798cb09bfa65906680e1d785a`
(tag `12.0-rc2`). Client: Swiftfin tvOS 1.6 on physical Apple TV, DeviceId
`tvOS_849A74D8-16A1-43C0-813C-084BCFCBF89B`. Captures (on the gate host):
`t3-stock-20260815T001941Z.sanitized.jsonl` (S2-S8, S11-S13), `t3-realmedia-oom.sanitized.jsonl`
(S15), `t3-s1-s9-s10-20260816.sanitized.jsonl` (S1 QuickConnect, S9, S10).

## C-1 QuickConnect duplicate-user

When QuickConnect is authorized for an account that the client already holds
credentials for, Swiftfin discards the freshly issued token and continues to
send the stale one.

Stock completed the flow correctly. `/QuickConnect/Initiate`, 11
`/QuickConnect/Connect` polls, and `POST /Users/AuthenticateWithQuickConnect`
all returned 200 with a valid token. Swiftfin hit its duplicate-user
condition, discarded the new token, and kept sending the stale one. Every
subsequent call returned 401 with `Failure message: Invalid token`, and
`/socket` returned 403.

The app reached an unrecoverable error screen whose back button exits to the
tvOS home screen. A force-quit was required.

A clean retry with a second account (a different UserId) passed. Stock's
QuickConnect implementation works end to end. The defect is specific to the
duplicate-user path.

## C-2 Stale-token cascade

Swiftfin presents a saved token for a server that no longer exists and
produces an unrecoverable 401/403 cascade with no re-authentication prompt.

During the baseline run, 29 unauthorized responses before the QuickConnect
sign-in carried UserIds that do not exist on the server. They were dead
identities from a previous instance whose config volume was dropped. Every
call returned 401, and `/socket` returned 403.

The recovery path is the same end state as C-1: an unrecoverable cascade. Two
independent causes produce the same failure, which makes the trigger
incidental and the recovery path the actual problem. It took deleting the
saved server entirely — not signing out — to clear it.

## C-3 Photo libraries unsupported

The library grid query is hardcoded to
`includeItemTypes=BoxSet&Movie&MusicVideo&Series&Video`. Photo is absent, so a
photo library renders "No Items".

Server proof: that filter returns 0 items against the Photos library. A
Photo filter returns 8.

Separately, `/Items/Latest` carries no type filter, so the same 8 photos do
appear in the home row. There they render blank, because for photo items the
client requests only `Images/Thumb` and `Images/Backdrop`, never
`Images/Primary`. Photos have Primary only.

## C-4 No music row

The client issues `/Items/Latest` only for non-music libraries. A music library
does not get a Latest row on the home screen.

## C-5 DirectPlay silent hang

Given `SupportsDirectPlay: true` with `Protocol: File` and no `TranscodingUrl`,
Swiftfin made zero media requests while posting 16 progress reports claiming
to play. It tried to open a server-side filesystem path it cannot reach.

This is a silent hang with false progress, not a visible error. It is worse
than the other defects because the user sees playback indicators with no
video and no error message.

## C-6 Images/Chapter 404

`GET /Items/{id}/Images/Chapter` returned 404 on stock with image providers
disabled. This is a stock/client observation, not a Deadfin divergence.
