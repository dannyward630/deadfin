// SPDX-License-Identifier: GPL-2.0-only

// Package musicbrainz enriches music artists and albums with MusicBrainz
// identities and release metadata.
//
// The request and mapping behavior ports Jellyfin's pinned
// MediaBrowser.Providers/Plugins/MusicBrainz providers. HTTP is routed through
// the shared remote transport so tests can replay cassettes without contacting
// the live service.
package musicbrainz
