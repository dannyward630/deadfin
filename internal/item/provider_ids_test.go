// SPDX-License-Identifier: GPL-2.0-only

package item

import (
	"reflect"
	"testing"
)

// Source: upstream/jellyfin/MediaBrowser.Controller/Entities/BaseItem.cs:99-110
// constructs ProviderIds with StringComparer.OrdinalIgnoreCase. The public Go
// value must therefore prevent callers from constructing casing aliases.
func TestProviderIDsEnforcesOrdinalIgnoreCaseIdentity(t *testing.T) {
	providerType := reflect.TypeOf(ProviderIDs{})
	if providerType.Kind() != reflect.Struct {
		t.Fatalf("ProviderIDs public representation = %s, want an opaque struct rather than a map-like collection", providerType.Kind())
	}
	for index := range providerType.NumField() {
		if field := providerType.Field(index); field.IsExported() {
			t.Fatalf("ProviderIDs must not expose mutable field %q that can admit case-equivalent duplicate keys", field.Name)
		}
	}

	var providers ProviderIDs
	if value, found := providers.Get("imdb"); found || value != "" {
		t.Fatalf("zero ProviderIDs.Get(imdb) = (%q, %t), want (\"\", false)", value, found)
	}

	providers.Set("Imdb", "a")
	if value, found := providers.Get("iMdB"); !found || value != "a" {
		t.Fatalf("ProviderIDs.Get(iMdB) after Set(Imdb, a) = (%q, %t), want (a, true)", value, found)
	}

	providers.Set("IMDB", "b")
	if value, found := providers.Get("imdb"); !found || value != "b" {
		t.Fatalf("ProviderIDs.Set(IMDB, b) must replace the sole Imdb identity; Get(imdb) = (%q, %t), want (b, true)", value, found)
	}
}

// Source: jellyfin/jellyfin@b04614e18d2aeed8d3d3de920c381c6bd5b97bdb,
// MediaBrowser.Controller/Entities/BaseItem.cs:99-110. Dictionary assignment
// with StringComparer.OrdinalIgnoreCase updates the existing entry's value but
// retains the original key object and therefore its spelling.
func TestProviderIDsComparerEqualUpdateRetainsOriginalKeySpelling(t *testing.T) {
	var providers ProviderIDs
	providers.Set("PluginX", "first")
	providers.Set("PLUGINX", "replacement")

	want := []ProviderID{{Name: "PluginX", Value: "replacement"}}
	if got := providers.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot() after case-equivalent update = %#v, want %#v", got, want)
	}
}

// Source: upstream/jellyfin/MediaBrowser.Controller/Entities/BaseItem.cs:99-110
// uses StringComparer.OrdinalIgnoreCase, whose Unicode ordinal casing treats
// Greek sigma forms and supplementary Deseret case pairs as one dictionary key.
func TestProviderIDsEnforcesUnicodeOrdinalIgnoreCaseIdentity(t *testing.T) {
	cases := []struct {
		name string
		keys []string
	}{
		{name: "all Greek sigma forms", keys: []string{"Σ", "σ", "ς"}},
		{name: "Deseret supplementary case pair", keys: []string{"𐐀", "𐐨"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var providers ProviderIDs
			providers.Set(tc.keys[0], "first")
			for _, key := range tc.keys[1:] {
				providers.Set(key, "replacement")
			}

			for _, key := range tc.keys {
				if got, found := providers.Get(key); !found || got != "replacement" {
					t.Fatalf("Get(%q) after case-equivalent replacements = (%q, %t), want (replacement, true)", key, got, found)
				}
			}
			if got := providers.Snapshot(); len(got) != 1 {
				t.Fatalf("Snapshot() after case-equivalent replacement = %#v, want one logical provider", got)
			}
		})
	}
}

// Source: .NET 10 StringComparer.OrdinalIgnoreCase. Ordinal comparison folds
// Greek sigma and Deseret case pairs, but must not apply compatibility folding:
// ASCII S is distinct from U+017F LONG S and ASCII k from U+212A KELVIN SIGN.
// ProviderIDs is the public consumer of equalOrdinalIgnoreCase, so its snapshot
// shows whether the comparer has merged or retained logical dictionary keys.
func TestProviderIDsMatchesDotNetOrdinalIgnoreCaseIdentityBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		first  string
		second string
		want   []ProviderID
	}{
		{
			name:   "Greek sigma forms share one identity",
			first:  "Σ",
			second: "ς",
			want:   []ProviderID{{Name: "Σ", Value: "second"}},
		},
		{
			name:   "Deseret supplementary case pair shares one identity",
			first:  "𐐀",
			second: "𐐨",
			want:   []ProviderID{{Name: "𐐀", Value: "second"}},
		},
		{
			name:   "long s is distinct from ASCII S",
			first:  "ſ",
			second: "S",
			want: []ProviderID{
				{Name: "ſ", Value: "first"},
				{Name: "S", Value: "second"},
			},
		},
		{
			name:   "Kelvin sign is distinct from ASCII k",
			first:  "K",
			second: "k",
			want: []ProviderID{
				{Name: "K", Value: "first"},
				{Name: "k", Value: "second"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var providers ProviderIDs
			providers.Set(tc.first, "first")
			providers.Set(tc.second, "second")

			if got := providers.Snapshot(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ProviderIDs.Set(%q, first); Set(%q, second); Snapshot() = %#v, want %#v", tc.first, tc.second, got, tc.want)
			}
		})
	}
}

// Snapshot returns the externally serializable provider dictionary in insertion
// order while retaining ProviderIDs' sole ownership of its internal entries.
func TestProviderIDsSnapshotPreservesEntriesAndOwnership(t *testing.T) {
	var zero ProviderIDs
	if got := zero.Snapshot(); got != nil {
		t.Fatalf("zero ProviderIDs.Snapshot() = %#v, want nil", got)
	}

	var providers ProviderIDs
	providers.Set("custom", "custom-id")
	providers.Set("TMDB", "movie-id")
	providers.Set("MusicBrainz", "artist-id")
	want := []ProviderID{
		{Name: "custom", Value: "custom-id"},
		{Name: "TMDB", Value: "movie-id"},
		{Name: "MusicBrainz", Value: "artist-id"},
	}
	if got := providers.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ProviderIDs.Snapshot() = %#v, want %#v", got, want)
	}

	snapshot := providers.Snapshot()
	snapshot[0] = ProviderID{Name: "mutated", Value: "mutated"}
	if got := providers.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating returned snapshot changed ProviderIDs: got %#v, want %#v", got, want)
	}
}

// ProviderIDs is a value object: copying it must sever mutable ownership so a
// caller may derive a provider set without rewriting the source item.
func TestProviderIDsValueCopyOwnsIndependentEntries(t *testing.T) {
	var original ProviderIDs
	original.Set("Imdb", "original-imdb")
	original.Set("TMDB", "original-tmdb")

	copied := original
	copied.Set("IMDB", "copied-imdb")
	copied.Set("TVDB", "copied-tvdb")

	wantOriginal := []ProviderID{
		{Name: "Imdb", Value: "original-imdb"},
		{Name: "TMDB", Value: "original-tmdb"},
	}
	if got := original.Snapshot(); !reflect.DeepEqual(got, wantOriginal) {
		t.Fatalf("original.Snapshot() after copied mutations = %#v, want %#v", got, wantOriginal)
	}
	if got, found := original.Get("imdb"); !found || got != "original-imdb" {
		t.Fatalf("original.Get(imdb) after copied replacement = (%q, %t), want (original-imdb, true)", got, found)
	}
	if got, found := original.Get("tvdb"); found || got != "" {
		t.Fatalf("original.Get(tvdb) after copied append = (%q, %t), want (\"\", false)", got, found)
	}

	wantCopied := []ProviderID{
		{Name: "Imdb", Value: "copied-imdb"},
		{Name: "TMDB", Value: "original-tmdb"},
		{Name: "TVDB", Value: "copied-tvdb"},
	}
	if got := copied.Snapshot(); !reflect.DeepEqual(got, wantCopied) {
		t.Fatalf("copied.Snapshot() = %#v, want ordered entries %#v", got, wantCopied)
	}
}
