// SPDX-License-Identifier: GPL-2.0-only

package handlers

import _ "embed"

// serverVersion is the Deadfin server version, which in-tree plugins inherit
// from the assembly in the pinned Jellyfin 12 source. It matches the version
// reported by SystemInfo (config.DefaultServerIdentity).
const serverVersion = "12.0.0"

//go:embed pluginpages/tmdb/config.html
var tmdbConfigHTML string

//go:embed pluginpages/musicbrainz/config.html
var musicbrainzConfigHTML string

//go:embed pluginpages/audiodb/config.html
var audiodbConfigHTML string

//go:embed pluginpages/listenbrainz/config.html
var listenbrainzConfigHTML string
