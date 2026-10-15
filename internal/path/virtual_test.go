// SPDX-License-Identifier: GPL-2.0-only

package path

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestVirtualPathExpanderMetadataPath(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	got := expander.Expand("%MetadataPath%/Studio/Case Studio")
	want := "/srv/metadata/Studio/Case Studio"
	if got != want {
		t.Fatalf("Expand(%q) = %q, want %q", "%MetadataPath%/Studio/Case Studio", got, want)
	}
}

func TestVirtualPathExpanderAppDataPath(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	got := expander.Expand("%AppDataPath%/library/poster.jpg")
	want := "/srv/data/library/poster.jpg"
	if got != want {
		t.Fatalf("Expand(%q) = %q, want %q", "%AppDataPath%/library/poster.jpg", got, want)
	}
}

func TestVirtualPathExpanderCaseInsensitive(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "lowercase metadata", input: "%metadatapath%/People/G/Generator", want: "/srv/metadata/People/G/Generator"},
		{name: "uppercase metadata", input: "%METADATAPATH%/People/G/Generator", want: "/srv/metadata/People/G/Generator"},
		{name: "mixed metadata", input: "%MeTaDaTaPaTh%/People/G/Generator", want: "/srv/metadata/People/G/Generator"},
		{name: "lowercase appdata", input: "%appdatapath%/library/poster.jpg", want: "/srv/data/library/poster.jpg"},
		{name: "uppercase appdata", input: "%APPDATAPATH%/library/poster.jpg", want: "/srv/data/library/poster.jpg"},
		{name: "mixed appdata", input: "%aPpDaTaPaTh%/library/poster.jpg", want: "/srv/data/library/poster.jpg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := expander.Expand(tc.input)
			if got != tc.want {
				t.Fatalf("Expand(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestVirtualPathExpanderBothTokensInOrder(t *testing.T) {
	// The data root contains %MetadataPath% so the order of replacement
	// changes the result. If %MetadataPath% expanded first, the data root
	// would not absorb the nested token and the result would leave it
	// unexpanded. Because %AppDataPath% expands first, the nested
	// %MetadataPath% in the expanded data root is replaced in the second
	// pass.
	expander := NewVirtualPathExpander("%MetadataPath%/data", "/srv/metadata")
	got := expander.Expand("%AppDataPath%/ref/meta")
	want := "/srv/metadata/data/ref/meta"
	if got != want {
		t.Fatalf("Expand order-dependent = %q, want %q", got, want)
	}
}

func TestVirtualPathExpanderNoTokenUnchanged(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "plain path", input: "/media/movies/feature.mkv"},
		{name: "empty path", input: ""},
		{name: "url path", input: "https://example.com/image.jpg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := expander.Expand(tc.input)
			if got != tc.input {
				t.Fatalf("Expand(%q) = %q, want unchanged", tc.input, got)
			}
		})
	}
}

func TestVirtualPathExpanderLiteralPercentPreserved(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "url-encoded path", input: "/metadata/Caf%C3%A9%20A", want: "/metadata/Caf%C3%A9%20A"},
		{name: "lone percent", input: "/metadata/bad%2G/lone%/truncated%2", want: "/metadata/bad%2G/lone%/truncated%2"},
		{name: "percent not a token", input: "/data/100%done/file.mkv", want: "/data/100%done/file.mkv"},
		{name: "partial token prefix", input: "/data/%Metadata/file.mkv", want: "/data/%Metadata/file.mkv"},
		{name: "partial token suffix", input: "/data/MetadataPath%/file.mkv", want: "/data/MetadataPath%/file.mkv"},
		{name: "token with extra percent", input: "/data/%%MetadataPath%%/file.mkv", want: "/data/%/srv/metadata%/file.mkv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := expander.Expand(tc.input)
			if got != tc.want {
				t.Fatalf("Expand(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestVirtualPathExpanderMultipleOccurrences(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	got := expander.Expand("%MetadataPath%/a/%MetadataPath%/b")
	want := "/srv/metadata/a//srv/metadata/b"
	if got != want {
		t.Fatalf("Expand multiple = %q, want %q", got, want)
	}
}

func TestVirtualPathExpanderPtrNil(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	got := expander.ExpandPtr(nil)
	if got != nil {
		t.Fatalf("ExpandPtr(nil) = %v, want nil", got)
	}
}

func TestVirtualPathExpanderPtrEmpty(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	empty := ""
	got := expander.ExpandPtr(&empty)
	if got == nil || *got != "" {
		t.Fatalf("ExpandPtr(empty) = %v, want pointer to empty string", got)
	}
}

func TestVirtualPathExpanderPtrExpanded(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	value := "%MetadataPath%/Studio/Case"
	got := expander.ExpandPtr(&value)
	if got == nil {
		t.Fatal("ExpandPtr returned nil for non-nil input")
	}
	want := "/srv/metadata/Studio/Case"
	if *got != want {
		t.Fatalf("ExpandPtr = %q, want %q", *got, want)
	}
}

func TestVirtualPathExpanderByteForByteUnchanged(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	input := "/metadata/Música/Café déjà-vu.mkv"
	got := expander.Expand(input)
	if got != input {
		t.Fatalf("Expand(%q) = %q, want byte-for-byte unchanged", input, got)
	}
}

func TestVirtualPathExpanderZeroValue(t *testing.T) {
	var expander VirtualPathExpander
	input := "%MetadataPath%/Studio/Case"
	got := expander.Expand(input)
	if got != input {
		t.Fatalf("zero-value Expand(%q) = %q, want %q (no expansion with empty roots)", input, got, input)
	}
}

func TestVirtualPathExpanderEmptyDataOnlyMetadata(t *testing.T) {
	expander := NewVirtualPathExpander("", "/srv/metadata")
	got := expander.Expand("%AppDataPath%/library/%MetadataPath%/meta")
	want := "%AppDataPath%/library//srv/metadata/meta"
	if got != want {
		t.Fatalf("Expand with empty data = %q, want %q", got, want)
	}
}

func TestVirtualPathExpanderAllOccurrencesReplaceIgnoreCase(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	got := expander.Expand(strings.Repeat("%METADATAPATH%/", 3) + "tail")
	want := strings.Repeat("/srv/metadata/", 3) + "tail"
	if got != want {
		t.Fatalf("Expand all occurrences = %q, want %q", got, want)
	}
}

// TestVirtualPathExpanderUnicodePrefix makes sure that case folding cannot
// misalign a byte index when a Unicode character changes byte length in the
// lowercased copy. The Kelvin sign K (U+212A, three bytes) lowercases to k
// (one byte). This test makes sure that the prefix survives and the token
// expands correctly.
func TestVirtualPathExpanderUnicodePrefix(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	got := expander.Expand("K/%MetadataPath%/x")
	want := "K//srv/metadata/x"
	if !utf8.ValidString(got) {
		t.Fatalf("Expand produced invalid UTF-8: %q", got)
	}
	if got != want {
		t.Fatalf("Expand(K/%%MetadataPath%%/x) = %q, want %q", got, want)
	}
}

// TestVirtualPathExpanderInvalidUTF8Preserved makes sure that a token-free
// input with invalid UTF-8 bytes is returned byte-for-byte unchanged. The
// scan must not replace invalid bytes with the replacement character.
func TestVirtualPathExpanderInvalidUTF8Preserved(t *testing.T) {
	expander := NewVirtualPathExpander("/srv/data", "/srv/metadata")
	input := string([]byte{0x01, 0xff, 0xfe, 0xfd, 0x02, 'h', 'e', 'l', 'l', 'o'})
	got := expander.Expand(input)
	if got != input {
		t.Fatalf("Expand invalid UTF-8 = %q (bytes %v), want %q (bytes %v)", got, []byte(got), input, []byte(input))
	}
}
