// SPDX-License-Identifier: GPL-2.0-only

package item

// ProviderID is an immutable provider identifier value returned by ProviderIDs.
type ProviderID struct {
	Name  string
	Value string
}

// ProviderIDs stores provider identifiers with .NET ordinal case-insensitive
// key identity. Entries are private so composite values cannot contain aliases.
type ProviderIDs struct {
	entries []providerIDEntry
}

type providerIDEntry struct {
	name  string
	value string
}

// Set updates the value for name's logical key while preserving the original
// key spelling and insertion position, matching Dictionary's indexer.
func (p *ProviderIDs) Set(name, value string) {
	for index := range p.entries {
		if equalOrdinalIgnoreCase(p.entries[index].name, name) {
			p.cloneEntries(0)
			p.entries[index].value = value
			return
		}
	}

	p.cloneEntries(1)
	p.entries = append(p.entries, providerIDEntry{name: name, value: value})
}

func (p *ProviderIDs) cloneEntries(additionalCapacity int) {
	if len(p.entries) == 0 {
		return
	}

	entries := make([]providerIDEntry, len(p.entries), len(p.entries)+additionalCapacity)
	copy(entries, p.entries)
	p.entries = entries
}

// Get returns the value for name's logical key, including an explicitly empty value.
func (p ProviderIDs) Get(name string) (string, bool) {
	for index := range p.entries {
		entry := &p.entries[index]
		if equalOrdinalIgnoreCase(entry.name, name) {
			return entry.value, true
		}
	}
	return "", false
}

// Snapshot returns a defensive copy of the provider IDs in insertion order.
func (p ProviderIDs) Snapshot() []ProviderID {
	if len(p.entries) == 0 {
		return nil
	}

	entries := make([]ProviderID, len(p.entries))
	for index := range p.entries {
		entries[index] = ProviderID{Name: p.entries[index].name, Value: p.entries[index].value}
	}
	return entries
}
