// SPDX-License-Identifier: GPL-2.0-only

package item

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
)

// Sources: BaseItem.GetSortName/ModifySortChunks (BaseItem.cs:928-1027),
// BaseItem SortName precedence (522-556), and ServerConfiguration defaults
// (ServerConfiguration.cs:115-127).
func TestComputeSortNameBaseBehavior(t *testing.T) {
	tests := []struct {
		name string
		item Item
		opts SortOptions
		want string
		ok   bool
	}{
		{"nil name remains null", Item{Kind: KindMovie}, SortOptions{}, "", false},
		{"empty name remains present empty", Item{Kind: KindMovie, Name: sortString("")}, SortOptions{}, "", true},
		{"default article and punctuation rules", Item{Kind: KindMovie, Name: sortString("The Spider-Man: Part 2")}, SortOptions{}, "spiderman: part 0000000002", true},
		{"numeric chunks pad each short digit run only", Item{Kind: KindMovie, Name: sortString("x 1 22 1234567890 12345678901 -3.14")}, SortOptions{}, "x 0000000001 0000000022 1234567890 12345678901 0000000003 0000000014", true},
		{"explicit sort name wins verbatim", Item{Kind: KindMovie, Name: sortString("The Matrix"), SortName: sortString("  Stored.Sort-9  ")}, SortOptions{}, "  Stored.Sort-9  ", true},
		{"explicit empty sort name wins over automatic value", Item{Kind: KindMovie, Name: sortString("The Matrix"), SortName: sortString("")}, SortOptions{}, "", true},
		{"forced sort name is cleaned before subclass override", Item{Kind: KindAudio, Name: sortString("unrelated"), ForcedSortName: sortString("The Spider-Man: Homecoming"), IndexNumber: sortInt(7)}, SortOptions{}, "spiderman: homecoming", true},
		{"empty forced sort name falls through to audio raw override", Item{Kind: KindAudio, Name: sortString("The Track"), ForcedSortName: sortString(""), IndexNumber: sortInt(7)}, SortOptions{}, "0007 - The Track", true},
		{"custom empty lists disable every default rule", Item{Kind: KindMovie, Name: sortString("The Spider-Man")}, SortOptions{RemoveWords: []string{}, RemoveCharacters: []string{}, ReplaceCharacters: []string{}}, "the spider-man", true},
		{"custom replacement does not collapse adjacent spaces", Item{Kind: KindMovie, Name: sortString("x.+2")}, SortOptions{RemoveWords: []string{}, RemoveCharacters: []string{}, ReplaceCharacters: []string{".", "+"}}, "x  0000000002", true},
		{"custom words use ordinal case sensitive matching after invariant lowering", Item{Kind: KindMovie, Name: sortString("The Matrix")}, SortOptions{RemoveWords: []string{"THE"}, RemoveCharacters: []string{}, ReplaceCharacters: []string{}}, "the matrix", true},
		{"person disables alpha cleaning and only trims leading whitespace", Item{Kind: KindPerson, Name: sortString("  The Matrix  ")}, SortOptions{}, "The Matrix  ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ComputeSortName(tt.item, tt.opts)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ComputeSortName() = (%q, %t), want (%q, %t)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// BaseItem.GetSortName calls ToLowerInvariant before ModifySortChunks removes
// diacritics (BaseItem.cs:957-1008). The normalization oracle's
// clean-invariant-casing vector records .NET 10's U+0130 mapping; this sort
// boundary must retain the resulting ASCII uppercase I after diacritic mapping.
func TestComputeSortNameInvariantCasingPrecedesDiacritics(t *testing.T) {
	got, present := ComputeSortName(
		Item{Kind: KindMovie, Name: sortString("\u0130")},
		SortOptions{RemoveWords: []string{}, RemoveCharacters: []string{}, ReplaceCharacters: []string{}},
	)
	if !present || got != "I" {
		t.Fatalf("ComputeSortName(U+0130) = (%q, %t), want (I, true)", got, present)
	}
}

// BaseItem.GetSortName lowers the complete .NET string before it invokes the
// residual transliteration seam (BaseItem.cs:957-1024). In particular,
// ToLowerInvariant must operate on the U+10400/U+10428 surrogate pair as one
// supplementary scalar; the nil adapter preserves that lowered residual.
func TestComputeSortNameSupplementaryInvariantCasingPrecedesAdapter(t *testing.T) {
	const deseretCapitalLongI = "\U00010400"
	const deseretSmallLongI = "\U00010428"

	item := Item{Kind: KindMovie, Name: sortString(deseretCapitalLongI)}
	opts := SortOptions{RemoveWords: []string{}, RemoveCharacters: []string{}, ReplaceCharacters: []string{}}

	t.Run("nil adapter preserves lowered supplementary scalar", func(t *testing.T) {
		got, present := ComputeSortName(item, opts)
		if !present || got != deseretSmallLongI {
			t.Fatalf("ComputeSortName(U+10400) without adapter = (%q, %t), want (U+10428, true)", got, present)
		}
	})

	t.Run("adapter receives lowered supplementary scalar", func(t *testing.T) {
		got, present := ComputeSortName(item, SortOptions{
			RemoveWords:       []string{},
			RemoveCharacters:  []string{},
			ReplaceCharacters: []string{},
			TransliterateNonASCII: func(value string) string {
				if value != deseretSmallLongI {
					t.Fatalf("TransliterateNonASCII received %q, want U+10428 (never U+10400)", value)
				}
				return "deseret-long-i"
			},
		})
		if !present || got != "deseret-long-i" {
			t.Fatalf("ComputeSortName(U+10400) with adapter = (%q, %t), want (deseret-long-i, true)", got, present)
		}
	})
}

// Sources: Audio.CreateSortName (Audio.cs:94-98), Episode.CreateSortName
// (Episode.cs:242-246), Season.CreateSortName (Season.cs:155-158), and
// LiveTvChannel.CreateSortName (LiveTvChannel.cs:108-116).
func TestComputeSortNameKindOverrides(t *testing.T) {
	tests := []struct {
		name string
		item Item
		want string
		ok   bool
	}{
		{"audio parent and track indexes are raw four digit fields", Item{Kind: KindAudio, Name: sortString("The Track"), ParentIndexNumber: sortInt(4), IndexNumber: sortInt(7)}, "0004 - 0007 - The Track", true},
		{"audiobook inherits raw audio sort", Item{Kind: KindAudioBook, Name: sortString("The Book"), IndexNumber: sortInt(1)}, "0001 - The Book", true},
		{"episode has three digit season and four digit episode fields", Item{Kind: KindEpisode, Name: sortString("The Pilot"), ParentIndexNumber: sortInt(2), IndexNumber: sortInt(3)}, "002 - 0003 - The Pilot", true},
		{"season index is raw four digit field", Item{Kind: KindSeason, Name: sortString("The First Season"), IndexNumber: sortInt(2)}, "0002", true},
		{"season without index returns name unchanged", Item{Kind: KindSeason, Name: sortString("The First Season")}, "The First Season", true},
		{"season without index and name remains null", Item{Kind: KindSeason}, "", false},
		{"live TV number formats invariant float", Item{Kind: KindLiveTvChannel, Name: sortString("News"), Extra: ExtraFields{LiveTV: &LiveTVExtra{Number: livetvNumberString("12.5")}}}, "0000000012.50000-News", true},
		{"live TV nonnumeric number is raw", Item{Kind: KindLiveTvChannel, Name: sortString("News"), Extra: ExtraFields{LiveTV: &LiveTVExtra{Number: livetvNumberString("12B")}}}, "12B-News", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ComputeSortName(tt.item, SortOptions{})
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ComputeSortName() = (%q, %t), want (%q, %t)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// Sources: jellyfin/jellyfin@b04614e18d2aeed8d3d3de920c381c6bd5b97bdb,
// MediaBrowser.Controller/Entities/Audio/Audio.cs:94-98;
// MediaBrowser.Controller/Entities/TV/Episode.cs:242-246;
// MediaBrowser.Controller/Entities/TV/Season.cs:155-158; and
// MediaBrowser.Controller/LiveTv/LiveTvChannel.cs:108-116. The pinned
// LiveTvChannel implementation invokes .NET 10 double.TryParse with
// InvariantCulture and formats successful values as 0000000000.00000.
func TestComputeSortNameKindOverrideParityRegressions(t *testing.T) {
	tests := []struct {
		name string
		item Item
		want string
	}{
		{
			name: "audio negative index keeps sign outside four custom digits",
			item: Item{Kind: KindAudio, Name: sortString("Track"), IndexNumber: sortInt(-2)},
			want: "-0002 - Track",
		},
		{
			name: "episode negative season and episode keep their custom widths",
			item: Item{Kind: KindEpisode, Name: sortString("Pilot"), ParentIndexNumber: sortInt(-2), IndexNumber: sortInt(-3)},
			want: "-002 - -0003 - Pilot",
		},
		{
			name: "season negative index keeps sign outside four custom digits",
			item: Item{Kind: KindSeason, Name: sortString("Season"), IndexNumber: sortInt(-2)},
			want: "-0002",
		},
		{
			name: "live TV negative zero suppresses sign after custom rounding",
			item: Item{Kind: KindLiveTvChannel, Name: sortString("Channel"), Extra: ExtraFields{LiveTV: &LiveTVExtra{Number: livetvNumberString("-0")}}},
			want: "0000000000.00000-Channel",
		},
		{
			name: "live TV negative fraction rounding to zero suppresses sign",
			item: Item{Kind: KindLiveTvChannel, Name: sortString("Channel"), Extra: ExtraFields{LiveTV: &LiveTVExtra{Number: livetvNumberString("-0.000001")}}},
			want: "0000000000.00000-Channel",
		},
		{
			name: "live TV negative fraction remaining after rounding retains sign",
			item: Item{Kind: KindLiveTvChannel, Name: sortString("Channel"), Extra: ExtraFields{LiveTV: &LiveTVExtra{Number: livetvNumberString("-0.00001")}}},
			want: "-0000000000.00001-Channel",
		},
		{
			name: "live TV number accepts surrounding invariant whitespace",
			item: Item{Kind: KindLiveTvChannel, Name: sortString("News"), Extra: ExtraFields{LiveTV: &LiveTVExtra{Number: livetvNumberString(" 12.5 ")}}},
			want: "0000000012.50000-News",
		},
		{
			name: "live TV number accepts invariant comma group separator",
			item: Item{Kind: KindLiveTvChannel, Name: sortString("News"), Extra: ExtraFields{LiveTV: &LiveTVExtra{Number: livetvNumberString("1,234.5")}}},
			want: "0000001234.50000-News",
		},
		{
			name: "live TV positive overflow uses invariant infinity spelling",
			item: Item{Kind: KindLiveTvChannel, Name: sortString("News"), Extra: ExtraFields{LiveTV: &LiveTVExtra{Number: livetvNumberString("1e5000")}}},
			want: "Infinity-News",
		},
		{
			name: "live TV explicit positive infinity uses formatted invariant spelling",
			item: Item{Kind: KindLiveTvChannel, Name: sortString("News"), Extra: ExtraFields{LiveTV: &LiveTVExtra{Number: livetvNumberString("+Infinity")}}},
			want: "Infinity-News",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ComputeSortName(tt.item, SortOptions{})
			if got != tt.want || !ok {
				t.Fatalf("ComputeSortName() = (%q, %t), want (%q, true)", got, ok, tt.want)
			}
		})
	}
}

// The explicit adapter is the documented WP-P2-01 bound for ICU4N residual
// transliteration. Diacritics mapping happens before this hook; the nil hook
// does not silently approximate ICU transliteration.
func TestComputeSortNameResidualNonASCIIBound(t *testing.T) {
	item := Item{Kind: KindMovie, Name: sortString("東京")}

	withoutAdapter, ok := ComputeSortName(item, SortOptions{})
	if !ok || withoutAdapter != "東京" {
		t.Fatalf("ComputeSortName() without adapter = (%q, %t), want (東京, true)", withoutAdapter, ok)
	}

	withAdapter, ok := ComputeSortName(item, SortOptions{
		TransliterateNonASCII: func(s string) string {
			if s != "東京" {
				t.Fatalf("adapter received %q, want 東京", s)
			}
			return "tokyo"
		},
	})
	if !ok || withAdapter != "tokyo" {
		t.Fatalf("ComputeSortName() with adapter = (%q, %t), want (tokyo, true)", withAdapter, ok)
	}
}

// OracleTool owns testdata/normalization-oracle.json. This test intentionally
// consumes its source-generated outputs instead of maintaining a second,
// hand-authored sort expectation set. Unsupported ICU residual cases are
// excluded by their explicit oracle flag and covered by the adapter-bound test.
func TestComputeSortNameOracle(t *testing.T) {
	oracle := loadSortOracle(t)
	for _, c := range oracle.Cases {
		if c.Operation != "sort-name" || !c.Supported {
			continue
		}
		var input struct {
			Item    Item        `json:"item"`
			Options SortOptions `json:"options"`
		}
		if err := json.Unmarshal(c.Input, &input); err != nil {
			t.Fatalf("oracle case %q input: %v", c.ID, err)
		}
		var want struct {
			Value   string `json:"value"`
			Present bool   `json:"present"`
		}
		if err := json.Unmarshal(c.Output, &want); err != nil {
			t.Fatalf("oracle case %q output: %v", c.ID, err)
		}
		got, present := ComputeSortName(input.Item, input.Options)
		if got != want.Value || present != want.Present {
			t.Errorf("oracle case %q: ComputeSortName() = (%q, %t), want (%q, %t)", c.ID, got, present, want.Value, want.Present)
		}
	}
}

type sortOracleDocument struct {
	Format     string           `json:"format"`
	Provenance json.RawMessage  `json:"provenance"`
	SHA256     string           `json:"sha256"`
	Cases      []sortOracleCase `json:"cases"`
}

type sortOracleCase struct {
	ID        string          `json:"id"`
	Operation string          `json:"operation"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
	Supported bool            `json:"supported"`
	Source    string          `json:"source"`
}

func loadSortOracle(t *testing.T) sortOracleDocument {
	t.Helper()
	data, err := os.ReadFile("testdata/normalization-oracle.json")
	if err != nil {
		t.Fatalf("read normalization oracle: %v", err)
	}
	// Parse only the signed envelope first; callers must not consume cases from
	// an oracle whose unsigned document digest does not verify.
	var envelope struct {
		Format     string          `json:"format"`
		Provenance json.RawMessage `json:"provenance"`
		SHA256     string          `json:"sha256"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode normalization oracle envelope: %v", err)
	}
	if envelope.Format == "" || len(envelope.Provenance) == 0 || envelope.SHA256 == "" {
		t.Fatal("normalization oracle lacks format, provenance, or SHA-256")
	}
	if _, err := hex.DecodeString(envelope.SHA256); err != nil || len(envelope.SHA256) != sha256.Size*2 {
		t.Fatalf("normalization oracle has invalid SHA-256 %q", envelope.SHA256)
	}
	shaMember := []byte(",\n  \"sha256\": " + strconv.Quote(envelope.SHA256))
	position := bytes.LastIndex(data, shaMember)
	if position < 0 {
		t.Fatal("normalization oracle SHA-256 member is not a terminal document member")
	}
	unsigned := append([]byte(nil), data[:position]...)
	unsigned = append(unsigned, data[position+len(shaMember):]...)
	unsigned = bytes.TrimSuffix(unsigned, []byte("\n"))
	gotSHA := sha256.Sum256(unsigned)
	if actual := hex.EncodeToString(gotSHA[:]); actual != envelope.SHA256 {
		t.Fatalf("normalization oracle SHA-256 = %q, want recomputed %q", envelope.SHA256, actual)
	}

	var doc sortOracleDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode verified normalization oracle: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("normalization oracle has no cases")
	}
	return doc
}

func TestDefaultSortOptions(t *testing.T) {
	got := DefaultSortOptions()
	want := SortOptions{
		RemoveWords:       []string{"the", "a", "an"},
		RemoveCharacters:  []string{",", "&", "-", "{", "}", "'"},
		ReplaceCharacters: []string{".", "+", "%"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultSortOptions() = %#v, want %#v", got, want)
	}
}

func sortString(s string) *string { return &s }
func sortInt(n int) *int          { return &n }
