// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"github.com/deadfin-org/deadfin/internal/item"
)

// materializeScannedSourceFacts commits resolver-owned classification facts
// independently of downstream query and projection defaults. Persisting the
// concrete type keeps scanner output stable across a store reopen.
func materializeScannedSourceFacts(values []item.Item, _ map[string]Entry, _ LibraryOptions) {
	for index := range values {
		value := &values[index]
		if value.MediaType != nil {
			continue
		}
		definition, found := item.Definition(value.Kind)
		if !found || definition.DefaultMediaType == item.MediaTypeUnknown {
			continue
		}
		mediaType := definition.DefaultMediaType
		value.MediaType = &mediaType
	}
}
