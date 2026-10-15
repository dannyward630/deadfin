// SPDX-License-Identifier: GPL-2.0-only

// Package native holds the Deadfin-native control-plane aggregate types. The
// six aggregates (Work, Edition, Release, Asset, Inventory, and Request) form
// a separate aggregate family from the Jellyfin compatibility item.Item. The
// projection table in the store maps native UUIDs to compatibility item IDs.
//
// The native graph is a derived layer until a separate ratified decision makes
// it canonical (docs/08-control-plane.md:35-39).
package native
