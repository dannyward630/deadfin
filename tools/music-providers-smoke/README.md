# P8 live music-provider smoke

This command is an opt-in operator check. It makes live requests and is not
called by tests or CI.

MusicBrainz plus AudioDb artist/album metadata:

```sh
AUDIODB_API_KEY='...' go run ./tools/music-providers-smoke \
  -mode metadata \
  -artist 'Björk' \
  -album 'Homogenic' \
  -language en
```

Use `-config-dir /path/to/config/plugins` to load the same
`Jellyfin.Plugin.MusicBrainz.xml`, `Jellyfin.Plugin.AudioDb.xml`, and
`Jellyfin.Plugin.ListenBrainz.xml` files as the server. MusicBrainz can be
checked without AudioDb by adding `-skip-audiodb`.

ListenBrainz submission is state-changing and therefore requires an explicit
acknowledgment:

```sh
LISTENBRAINZ_TOKEN='...' go run ./tools/music-providers-smoke \
  -mode scrobble \
  -listen-type playing_now \
  -artist 'Björk' \
  -track 'Jóga' \
  -album 'Homogenic' \
  -confirm-submit
```

Use `-listen-type single` only when you want to add a real listen to the
account. The command reads tokens from provider configuration or environment
variables. It never prints the tokens.
