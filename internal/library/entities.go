// SPDX-License-Identifier: GPL-2.0-only

package library

import "github.com/deadfin-org/deadfin/internal/item"

// ReferencedEntities returns the Person, Genre, MusicGenre, and Studio items
// that values reference, with the same identities the scanner writes. A
// metadata edit persists them so a new credit or taxonomy name resolves to an
// entity page, as the scanner does for scanned names.
func ReferencedEntities(collection item.CollectionType, values []item.Item) []item.Item {
	return append(nfoPeople(values), taxonomyEntities(collection, values)...)
}
