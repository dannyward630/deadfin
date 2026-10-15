// SPDX-License-Identifier: GPL-2.0-only

// Package listenbrainz submits music playback reported through Deadfin's P5
// playstate application to ListenBrainz.
//
// Jellyfin 12's in-tree package currently provides artist similarity rather
// than scrobbling; WP-P8-04 explicitly owns the scrobbling integration. The
// wire contract follows ListenBrainz's /1/submit-listens API and keeps network
// failures isolated from playback state.
package listenbrainz
