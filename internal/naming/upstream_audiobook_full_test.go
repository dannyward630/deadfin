// SPDX-License-Identifier: GPL-2.0-only

// Ported exhaustively from pinned upstream Jellyfin.Naming.Tests/AudioBook/*.cs.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func resolveAudioBook(t *testing.T, path string, opts naming.NamingOptions) *naming.AudioBookFileInfo {
	t.Helper()
	got, err := naming.ResolveAudioBook(path, opts)
	if err != nil {
		t.Fatalf("ResolveAudioBook(%q) error = %v", path, err)
	}
	return got
}

func resolveAudioBookList(t *testing.T, paths []string, opts naming.NamingOptions) []naming.AudioBookInfo {
	t.Helper()
	got, err := naming.ResolveAudioBookList(paths, opts)
	if err != nil {
		t.Fatalf("ResolveAudioBookList(%#v) error = %v", paths, err)
	}
	return got
}

func parseAudioBookName(t *testing.T, value string, opts naming.NamingOptions) naming.AudioBookNameResult {
	t.Helper()
	got, err := naming.ParseAudioBookName(value, opts)
	if err != nil {
		t.Fatalf("ParseAudioBookName(%q) error = %v", value, err)
	}
	return got
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookFileInfoTests.CompareTo_Same_Success.
func TestUpstreamAudioBookFileInfoCompareSame(t *testing.T) {
	info := &naming.AudioBookFileInfo{}
	if got := naming.CompareAudioBookFileInfo(info, info); got != 0 {
		t.Fatalf("CompareAudioBookFileInfo(info, info) = %d, want 0", got)
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookFileInfoTests.CompareTo_Null_Success.
func TestUpstreamAudioBookFileInfoCompareNil(t *testing.T) {
	if got := naming.CompareAudioBookFileInfo(&naming.AudioBookFileInfo{}, nil); got != 1 {
		t.Fatalf("CompareAudioBookFileInfo(info, nil) = %d, want 1", got)
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookFileInfoTests.CompareTo_Empty_Success.
func TestUpstreamAudioBookFileInfoCompareEmpty(t *testing.T) {
	if got := naming.CompareAudioBookFileInfo(&naming.AudioBookFileInfo{}, &naming.AudioBookFileInfo{}); got != 0 {
		t.Fatalf("CompareAudioBookFileInfo(empty, empty) = %d, want 0", got)
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookResolverTests.Resolve_ValidFileName_Success.
// All 3 active MemberData rows are retained.
func TestUpstreamAudioBookResolverValidFileNames(t *testing.T) {
	tests := []struct {
		path, container string
		chapter, part   *int
	}{
		{"/server/AudioBooks/Larry Potter/Larry Potter.mp3", "mp3", nil, nil},
		{"/server/AudioBooks/Berry Potter/Chapter 1 .ogg", "ogg", mediaInt(1), nil},
		{"/server/AudioBooks/Nerry Potter/Part 3 - Chapter 2.mp3", "mp3", mediaInt(2), mediaInt(3)},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := resolveAudioBook(t, tt.path, naming.DefaultOptions())
			if got == nil || got.Path != tt.path || got.Container != tt.container || !reflect.DeepEqual(got.ChapterNumber, tt.chapter) || !reflect.DeepEqual(got.PartNumber, tt.part) {
				t.Fatalf("ResolveAudioBook(%q) = %#v, want path/container/chapter/part %q/%q/%v/%v", tt.path, got, tt.path, tt.container, tt.chapter, tt.part)
			}
		})
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookResolverTests.Resolve_InvalidExtension.
func TestUpstreamAudioBookResolverRejectsInvalidExtension(t *testing.T) {
	if got := resolveAudioBook(t, "/server/AudioBooks/Larry Potter/Larry Potter.mp9", naming.DefaultOptions()); got != nil {
		t.Fatalf("ResolveAudioBook(invalid extension) = %#v, want nil", got)
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookResolverTests.Resolve_EmptyFileName.
func TestUpstreamAudioBookResolverRejectsEmptyName(t *testing.T) {
	if got := resolveAudioBook(t, "", naming.DefaultOptions()); got != nil {
		t.Fatalf("ResolveAudioBook(empty path) = %#v, want nil", got)
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookListResolverTests.TestStackAndExtras.
func TestUpstreamAudioBookListStackAndExtras(t *testing.T) {
	got := resolveAudioBookList(t, []string{
		"Harry Potter and the Deathly Hallows/Part 1.mp3",
		"Harry Potter and the Deathly Hallows/Part 2.mp3",
		"Harry Potter and the Deathly Hallows/Extra.mp3",
		"Batman/Chapter 1.mp3",
		"Batman/Chapter 2.mp3",
		"Batman/Chapter 3.mp3",
		"Badman/audiobook.mp3",
		"Badman/extra.mp3",
		"Superman (2020)/Part 1.mp3",
		"Superman (2020)/extra.mp3",
		"Ready Player One (2020)/audiobook.mp3",
		"Ready Player One (2020)/extra.mp3",
		".mp3",
	}, naming.DefaultOptions())
	if len(got) != 5 {
		t.Fatalf("ResolveAudioBookList() returned %d items, want 5: %#v", len(got), got)
	}
	for index, want := range []struct {
		name          string
		files, extras int
	}{
		{"Harry Potter and the Deathly Hallows", 2, 1},
		{"Batman", 3, 0},
		{"Badman", 1, 1},
		{"Superman", 1, 1},
		{"Ready Player One", 1, 1},
	} {
		if got[index].Name != want.name || len(got[index].Files) != want.files || len(got[index].Extras) != want.extras {
			t.Fatalf("ResolveAudioBookList()[%d] = %#v, want name/files/extras %q/%d/%d", index, got[index], want.name, want.files, want.extras)
		}
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookListResolverTests.TestAlternativeVersions.
func TestUpstreamAudioBookListAlternativeVersions(t *testing.T) {
	got := resolveAudioBookList(t, []string{
		"Harry Potter and the Deathly Hallows/Chapter 1.ogg",
		"Harry Potter and the Deathly Hallows/Chapter 1.mp3",
		"Deadpool.mp3",
		"Deadpool [HQ].mp3",
		"Superman/audiobook.mp3",
		"Superman/Superman.mp3",
		"Superman/Superman [HQ].mp3",
		"Superman/extra.mp3",
		"Batman/ Chapter 1 .mp3",
		"Batman/Chapter 1[loss-less].mp3",
	}, naming.DefaultOptions())
	if len(got) != 5 {
		t.Fatalf("ResolveAudioBookList() returned %d items, want 5: %#v", len(got), got)
	}
	for index, want := range []int{1, 0, 0, 2, 1} {
		if actual := len(got[index].AlternateVersions); actual != want {
			t.Fatalf("ResolveAudioBookList()[%d] has %d alternate versions, want %d: %#v", index, actual, want, got[index])
		}
	}
	paths := make(map[string]struct{}, len(got[3].AlternateVersions))
	for _, alternate := range got[3].AlternateVersions {
		paths[alternate.Path] = struct{}{}
	}
	for _, path := range []string{"Superman/audiobook.mp3", "Superman/Superman [HQ].mp3"} {
		if _, exists := paths[path]; !exists {
			t.Fatalf("Superman alternate versions = %#v, want path %q", got[3].AlternateVersions, path)
		}
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookListResolverTests.TestNameYearExtraction.
func TestUpstreamAudioBookListExtractsNameAndYear(t *testing.T) {
	tests := []struct {
		path, name string
		year       *int
	}{
		{"Harry Potter and the Deathly Hallows (2007)/Chapter 1.ogg", "Harry Potter and the Deathly Hallows", mediaInt(2007)},
		{"Batman (2020).ogg", "Batman", mediaInt(2020)},
		{"Batman( 2021 ).mp3", "Batman", mediaInt(2021)},
		{"Batman(*2021*).mp3", "Batman(*2021*)", nil},
		{"Batman.mp3", "Batman", nil},
		{" + Batman . .mp3", "+ Batman .", nil},
		{" .mp3", " ", nil},
	}
	paths := make([]string, len(tests))
	for index := range tests {
		paths[index] = tests[index].path
	}
	got := resolveAudioBookList(t, paths, naming.DefaultOptions())
	if len(got) != len(tests) {
		t.Fatalf("ResolveAudioBookList() returned %d items, want %d: %#v", len(got), len(tests), got)
	}
	for index, want := range tests {
		if got[index].Name != want.name || !reflect.DeepEqual(got[index].Year, want.year) {
			t.Fatalf("ResolveAudioBookList()[%d] = %#v, want name/year %q/%v", index, got[index], want.name, want.year)
		}
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookListResolverTests.TestWithMetadata.
func TestUpstreamAudioBookListIgnoresMetadata(t *testing.T) {
	got := resolveAudioBookList(t, []string{
		"Harry Potter and the Deathly Hallows/Chapter 1.ogg",
		"Harry Potter and the Deathly Hallows/Harry Potter and the Deathly Hallows.nfo",
	}, naming.DefaultOptions())
	if len(got) != 1 {
		t.Fatalf("ResolveAudioBookList(with metadata) returned %d items, want 1: %#v", len(got), got)
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookListResolverTests.TestWithExtra.
func TestUpstreamAudioBookListSeparatesExtra(t *testing.T) {
	got := resolveAudioBookList(t, []string{
		"Harry Potter and the Deathly Hallows/Chapter 1.mp3",
		"Harry Potter and the Deathly Hallows/Harry Potter and the Deathly Hallows trailer.mp3",
	}, naming.DefaultOptions())
	if len(got) != 1 {
		t.Fatalf("ResolveAudioBookList(with extra) returned %d items, want 1: %#v", len(got), got)
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookListResolverTests.TestWithoutFolder.
func TestUpstreamAudioBookListRetainsRootExtra(t *testing.T) {
	got := resolveAudioBookList(t, []string{"Harry Potter and the Deathly Hallows trailer.mp3"}, naming.DefaultOptions())
	if len(got) != 1 {
		t.Fatalf("ResolveAudioBookList(without folder) returned %d items, want 1: %#v", len(got), got)
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookListResolverTests.TestEmpty.
func TestUpstreamAudioBookListEmpty(t *testing.T) {
	if got := resolveAudioBookList(t, nil, naming.DefaultOptions()); len(got) != 0 {
		t.Fatalf("ResolveAudioBookList(empty) = %#v, want no items", got)
	}
}

// Emby.Naming.AudioBook.AudioBookFilePathParser.Parse honors the configured
// AudioBookPartsExpressions in order and only fills each capture once.
func TestSourcePinnedAudioBookParserHonorsConfiguredPartExpressions(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioBookPartsExpressions = []string{`^P(?<part>[0-9]+)$`}
	got := resolveAudioBook(t, "P7.mp3", opts)
	if got == nil || got.ChapterNumber != nil || !reflect.DeepEqual(got.PartNumber, mediaInt(7)) {
		t.Fatalf("ResolveAudioBook(custom part expression) = %#v, want nil chapter and part 7", got)
	}
}

// Emby.Naming.AudioBook.AudioBookFilePathParser.Parse applies default
// expressions in source order. For 1_2_3 the leading chapter rule wins first,
// and the trailing-part rule then supplies the final 3.
func TestSourcePinnedAudioBookParserPreservesExpressionOrder(t *testing.T) {
	got := resolveAudioBook(t, "1_2_3.mp3", naming.DefaultOptions())
	if got == nil || !reflect.DeepEqual(got.ChapterNumber, mediaInt(1)) || !reflect.DeepEqual(got.PartNumber, mediaInt(3)) {
		t.Fatalf("ResolveAudioBook(expression order) = %#v, want chapter 1 and part 3", got)
	}
}

// Emby.Naming.AudioBook.AudioBookFilePathParser.Parse treats ch 12 as a
// chapter and does not reinterpret its trailing digits as an unnamed part.
func TestSourcePinnedAudioBookParserDoesNotDuplicateChapterAsPart(t *testing.T) {
	got := resolveAudioBook(t, "ch 12.mp3", naming.DefaultOptions())
	if got == nil || !reflect.DeepEqual(got.ChapterNumber, mediaInt(12)) || got.PartNumber != nil {
		t.Fatalf("ResolveAudioBook(chapter-only) = %#v, want chapter 12 and nil part", got)
	}
}

// Emby.Naming.AudioBook.AudioBookFilePathParser.Parse uses Int32.TryParse;
// capture values above Int32.MaxValue must remain unset.
func TestSourcePinnedAudioBookParserRejectsInt32Overflow(t *testing.T) {
	got := resolveAudioBook(t, "Chapter 2147483648.mp3", naming.DefaultOptions())
	if got == nil || got.ChapterNumber != nil || got.PartNumber != nil {
		t.Fatalf("ResolveAudioBook(Int32-overflow capture) = %#v, want nil chapter and part", got)
	}
}

// Emby.Naming.AudioBook.AudioBookNameParser.Parse iterates caller-provided
// AudioBookNamesExpressions, and list resolution uses that result for name/year.
func TestSourcePinnedAudioBookListHonorsConfiguredNameExpressions(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioBookNamesExpressions = []string{`^(?<name>.+?)--(?<year>[0-9]{4})$`}
	got := resolveAudioBookList(t, []string{"Custom Title--2001/Chapter 1.mp3"}, opts)
	if len(got) != 1 || got[0].Name != "Custom Title" || !reflect.DeepEqual(got[0].Year, mediaInt(2001)) {
		t.Fatalf("ResolveAudioBookList(custom name expression) = %#v, want Custom Title (2001)", got)
	}
}

// Emby.Naming.AudioBook.AudioBookFileInfo.CompareTo delegates path comparison
// to StringComparison.Ordinal, whose UTF-16 code-unit ordering differs from
// Go's UTF-8 byte ordering for supplementary-plane characters.
func TestSourcePinnedAudioBookFileInfoUsesUTF16OrdinalPathOrder(t *testing.T) {
	left := &naming.AudioBookFileInfo{Path: "\U00010000"}
	right := &naming.AudioBookFileInfo{Path: "\uE000"}
	if got := naming.CompareAudioBookFileInfo(left, right); got >= 0 {
		t.Fatalf("CompareAudioBookFileInfo(U+10000, U+E000) = %d, want UTF-16 ordinal negative", got)
	}
}

// Emby.Naming.AudioBook.AudioBookListResolver.FindExtraAndAlternativeFiles
// removes alternate objects by reference. Duplicate equal file values retain
// one main file and exactly one alternate, rather than deleting both by value.
func TestSourcePinnedAudioBookListPreservesDuplicateFileIdentity(t *testing.T) {
	paths := []string{"Novel/Chapter 1.mp3", "Novel/Chapter 1.mp3"}
	got := resolveAudioBookList(t, paths, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].Files) != 1 || len(got[0].AlternateVersions) != 1 {
		t.Fatalf("ResolveAudioBookList(duplicate file) = %#v, want one main file and one alternate", got)
	}
}

// Emby.Naming.AudioBook.AudioBookNameParser.Parse evaluates every configured
// expression, preserving a name from one match and a year from a later match.
func TestSourcePinnedAudioBookListCombinesConfiguredNameExpressions(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioBookNamesExpressions = []string{
		`^(?<name>Novel) 2001$`,
		`^Novel (?<year>[0-9]{4})$`,
	}
	got := resolveAudioBookList(t, []string{"Novel 2001/Chapter 1.mp3"}, opts)
	if len(got) != 1 || got[0].Name != "Novel" || !reflect.DeepEqual(got[0].Year, mediaInt(2001)) {
		t.Fatalf("ResolveAudioBookList(split name/year expressions) = %#v, want Novel (2001)", got)
	}
}

// Emby.Naming.AudioBook.AudioBookNameParser.Parse stores a successful named
// capture verbatim; only the no-match fallback retains its original input.
func TestSourcePinnedAudioBookNameParserPreservesCapturedWhitespace(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioBookNamesExpressions = []string{`^(?<name> .+ )$`}
	got := parseAudioBookName(t, " Title ", opts)
	if got.Name != " Title " || got.Year != nil {
		t.Fatalf("ParseAudioBookName(captured whitespace) = %#v, want verbatim name and nil year", got)
	}
}

// Emby.Naming.AudioBook.AudioBookListResolver.FindExtraAndAlternativeFiles
// sorts a duplicate chapter/part group by Container and then Path before it
// keeps the first file and emits the remaining alternate versions.
func TestSourcePinnedAudioBookListOrdersNumberedAlternatesByContainerThenPath(t *testing.T) {
	got := resolveAudioBookList(t, []string{
		"Novel/A Chapter 1.ogg",
		"Novel/Z Chapter 1.mp3",
	}, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].Files) != 1 || len(got[0].AlternateVersions) != 1 || got[0].Files[0].Path != "Novel/Z Chapter 1.mp3" || got[0].AlternateVersions[0].Path != "Novel/A Chapter 1.ogg" {
		t.Fatalf("ResolveAudioBookList(numbered duplicate) = %#v, want mp3 main and ogg alternate by container/path order", got)
	}
}

// Emby.Naming.AudioBook.AudioBookListResolver.FindExtraAndAlternativeFiles
// sorts unnumbered alternate candidates by Container then Path before fallback
// main-file selection, just as it does numbered duplicate groups.
func TestSourcePinnedAudioBookListOrdersUnnamedAlternatesByContainerThenPath(t *testing.T) {
	got := resolveAudioBookList(t, []string{
		"Book/A Book.ogg",
		"Book/Z Book.mp3",
	}, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].Files) != 1 || len(got[0].AlternateVersions) != 1 || got[0].Files[0].Path != "Book/Z Book.mp3" || got[0].AlternateVersions[0].Path != "Book/A Book.ogg" {
		t.Fatalf("ResolveAudioBookList(unnumbered alternates) = %#v, want mp3 main and ogg alternate by container/path order", got)
	}
}

// Emby.Naming.AudioBook.AudioBookListResolver.FindExtraAndAlternativeFiles
// exposes unnumbered extras in the same Container/Path order used upstream.
func TestSourcePinnedAudioBookListOrdersExtrasByContainerThenPath(t *testing.T) {
	got := resolveAudioBookList(t, []string{
		"Book/A Extra.ogg",
		"Book/Z Extra.mp3",
	}, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].Extras) != 2 || got[0].Extras[0].Path != "Book/Z Extra.mp3" || got[0].Extras[1].Path != "Book/A Extra.ogg" {
		t.Fatalf("ResolveAudioBookList(extras order) = %#v, want mp3 then ogg by container/path order", got)
	}
}

// Emby.Naming.Video.StackResolver.ResolveAudioBooks emits one root-file stack
// per input entry, even when root paths are identical; list resolution must
// preserve that input identity rather than using a path-keyed root map.
func TestSourcePinnedAudioBookListPreservesDuplicateRootFileIdentity(t *testing.T) {
	got := resolveAudioBookList(t, []string{"Book.mp3", "Book.mp3"}, naming.DefaultOptions())
	if len(got) != 2 || len(got[0].Files) != 1 || len(got[1].Files) != 1 || got[0].Files[0].Path != "Book.mp3" || got[1].Files[0].Path != "Book.mp3" {
		t.Fatalf("ResolveAudioBookList(duplicate root file) = %#v, want two independent root audiobook entries", got)
	}
}

// Emby.Naming.AudioBook.AudioBookFilePathParser.Parse evaluates configured
// .NET regex syntax, including named captures and lookahead assertions.
func TestSourcePinnedAudioBookParserSupportsDotNetLookahead(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioBookPartsExpressions = []string{`(?<chapter>[0-9]+)(?=x)`}
	got, err := naming.ResolveAudioBook("12X.mp3", opts)
	if err != nil || got == nil || !reflect.DeepEqual(got.ChapterNumber, mediaInt(12)) || got.PartNumber != nil {
		t.Fatalf("ResolveAudioBook(.NET lookahead) = (%#v, %v), want chapter 12 and nil part without error", got, err)
	}
}

// Emby.Naming.AudioBook.AudioBookFilePathParser.Parse lets invalid configured
// .NET expressions surface an error instead of silently skipping an RE2 failure.
func TestSourcePinnedAudioBookParserReportsInvalidExpression(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioBookPartsExpressions = []string{`[`}
	if _, err := naming.ResolveAudioBook("12.mp3", opts); err == nil {
		t.Fatal("ResolveAudioBook(invalid expression) returned nil error")
	}
}

// Emby.Naming.AudioBook.AudioBookListResolver.Resolve must propagate invalid
// configured part expressions instead of resolving arbitrary fallback groups.
func TestSourcePinnedAudioBookListReportsInvalidPartExpression(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioBookPartsExpressions = []string{`[`}
	if _, err := naming.ResolveAudioBookList([]string{"12.mp3"}, opts); err == nil {
		t.Fatal("ResolveAudioBookList(invalid part expression) returned nil error")
	}
}

// Emby.Naming.AudioBook.AudioBookListResolver.Resolve must propagate invalid
// configured name expressions before it creates a title fallback.
func TestSourcePinnedAudioBookListReportsInvalidNameExpression(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioBookNamesExpressions = []string{`[`}
	if _, err := naming.ResolveAudioBookList([]string{"Book/Chapter 1.mp3"}, opts); err == nil {
		t.Fatal("ResolveAudioBookList(invalid name expression) returned nil error")
	}
}

// Emby.Naming.AudioBook.AudioBookListResolver.FindExtraAndAlternativeFiles
// uses the current-culture string comparer for Container/Path ordering, which
// is distinct from the UTF-16 ordinal comparer used by AudioBookFileInfo.CompareTo.
func TestSourcePinnedAudioBookListUsesCurrentCultureForAlternateOrder(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioFileExtensions = append(opts.AudioFileExtensions, ".é", ".z")
	got := resolveAudioBookList(t, []string{"Book/A Book.é", "Book/Z Book.z"}, opts)
	if len(got) != 1 || len(got[0].Files) != 1 || len(got[0].AlternateVersions) != 1 || got[0].Files[0].Path != "Book/A Book.é" || got[0].AlternateVersions[0].Path != "Book/Z Book.z" {
		t.Fatalf("ResolveAudioBookList(culture alternate order) = %#v, want é-container primary before z", got)
	}
}

// Emby.Naming.AudioBook.AudioBookListResolver.FindExtraAndAlternativeFiles
// applies the same current-culture Container/Path order to extras.
func TestSourcePinnedAudioBookListUsesCurrentCultureForExtraOrder(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioFileExtensions = append(opts.AudioFileExtensions, ".é", ".z")
	got := resolveAudioBookList(t, []string{"Book/A Extra.é", "Book/Z Extra.z"}, opts)
	if len(got) != 1 || len(got[0].Extras) != 2 || got[0].Extras[0].Path != "Book/A Extra.é" || got[0].Extras[1].Path != "Book/Z Extra.z" {
		t.Fatalf("ResolveAudioBookList(culture extra order) = %#v, want é-container before z", got)
	}
}

// Emby.Naming.AudioBook.AudioBookListResolver.FindMainAudioBookFile prefers
// an audiobook stem over the sorted fallback candidate when no exact title exists.
func TestSourcePinnedAudioBookListPrefersAudiobookStemOverHQFallback(t *testing.T) {
	got := resolveAudioBookList(t, []string{"Book/audiobook.mp3", "Book/Book [HQ].mp3"}, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].Files) != 1 || len(got[0].AlternateVersions) != 1 || got[0].Files[0].Path != "Book/audiobook.mp3" || got[0].AlternateVersions[0].Path != "Book/Book [HQ].mp3" {
		t.Fatalf("ResolveAudioBookList(audiobook fallback) = %#v, want audiobook primary and HQ alternate", got)
	}
}

// Emby.Naming.AudioBook.AudioBookNameParser.Parse must surface invalid
// configured expressions rather than silently retaining a fallback name.
func TestSourcePinnedAudioBookNameParserReportsInvalidExpression(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioBookNamesExpressions = []string{`[`}
	if _, err := naming.ParseAudioBookName("Book", opts); err == nil {
		t.Fatal("ParseAudioBookName(invalid expression) returned nil error")
	}
}

// Emby.Naming.AudioBook.AudioBookListResolver.FindExtraAndAlternativeFiles
// applies current-culture ordering to Path after equal Container values.
func TestSourcePinnedAudioBookListUsesCurrentCultureForExtraPathOrder(t *testing.T) {
	got := resolveAudioBookList(t, []string{"Book/a Extra.mp3", "Book/B Extra.mp3"}, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].Extras) != 2 || got[0].Extras[0].Path != "Book/a Extra.mp3" || got[0].Extras[1].Path != "Book/B Extra.mp3" {
		t.Fatalf("ResolveAudioBookList(culture extra path order) = %#v, want a Extra before B Extra", got)
	}
}

// Emby.Naming.AudioBook.AudioBookListResolver.FindMainAudioBookFile
// (AudioBookListResolver.cs:139-145) selects the audiobook stem only after
// no exact title stem exists among the alternate candidates.
func TestSourcePinnedAudioBookListFallsBackToAudiobookAfterMissingExactTitle(t *testing.T) {
	got := resolveAudioBookList(t, []string{"A/A special.mp3", "A/audiobook.mp3"}, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].Files) != 1 || len(got[0].AlternateVersions) != 1 || got[0].Files[0].Path != "A/audiobook.mp3" || got[0].AlternateVersions[0].Path != "A/A special.mp3" {
		t.Fatalf("ResolveAudioBookList(missing exact title) = %#v, want audiobook primary and A special alternate", got)
	}
}

// Emby.Naming.AudioBook.AudioBookListResolver.FindMainAudioBookFile
// (AudioBookListResolver.cs:139-145) gives an exact title stem precedence
// over the audiobook fallback, even when it is not the sorted first candidate.
func TestSourcePinnedAudioBookListPrefersExactTitleBeforeAudiobookFallback(t *testing.T) {
	got := resolveAudioBookList(t, []string{"A/A.mp3", "A/audiobook.mp3"}, naming.DefaultOptions())
	if len(got) != 1 || len(got[0].Files) != 1 || len(got[0].AlternateVersions) != 1 || got[0].Files[0].Path != "A/A.mp3" || got[0].AlternateVersions[0].Path != "A/audiobook.mp3" {
		t.Fatalf("ResolveAudioBookList(exact title precedence) = %#v, want A primary and audiobook alternate", got)
	}
}

// Emby.Naming.AudioBook.AudioBookFilePathParser.Parse
// (AudioBookFilePathParser.cs:29-64) evaluates every configured .NET regex
// with IgnoreCase, so valid named-capture lookaheads must not be special-cased.
func TestSourcePinnedAudioBookParserSupportsArbitraryDotNetLookahead(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioBookPartsExpressions = []string{`(?<chapter>[0-9]+)(?=X$)`}
	got, err := naming.ResolveAudioBook("12X.mp3", opts)
	if err != nil || got == nil || !reflect.DeepEqual(got.ChapterNumber, mediaInt(12)) || got.PartNumber != nil {
		t.Fatalf("ResolveAudioBook(arbitrary .NET lookahead) = (%#v, %v), want chapter 12 and nil part without error", got, err)
	}
}

// Emby.Naming.AudioBook.AudioBookNameParser.Parse
// (AudioBookNameParser.cs:28-64) evaluates configured .NET expressions for
// both the direct parser and list-derived directory title.
func TestSourcePinnedAudioBookNameParserSupportsDotNetLookaheadDirectAndList(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioBookNamesExpressions = []string{`^(?<name>.+?)(?=--[0-9]{4}$)--(?<year>[0-9]{4})$`}
	direct, err := naming.ParseAudioBookName("Novel--2001", opts)
	if err != nil || direct.Name != "Novel" || !reflect.DeepEqual(direct.Year, mediaInt(2001)) {
		t.Fatalf("ParseAudioBookName(.NET lookahead) = (%#v, %v), want Novel (2001) without error", direct, err)
	}
	list, err := naming.ResolveAudioBookList([]string{"Novel--2001/Chapter 1.mp3"}, opts)
	if err != nil || len(list) != 1 || list[0].Name != "Novel" || !reflect.DeepEqual(list[0].Year, mediaInt(2001)) {
		t.Fatalf("ResolveAudioBookList(.NET name lookahead) = (%#v, %v), want one Novel (2001) without error", list, err)
	}
}

// Emby.Naming.AudioBook.AudioBookNameParser.Parse
// (AudioBookNameParser.cs:31-57) evaluates every configured expression, so a
// malformed expression reached after a successful match remains actionable.
func TestSourcePinnedAudioBookNameParserReportsReachedMalformedExpressionDirectAndList(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.AudioBookNamesExpressions = []string{`^(?<name>.+)$`, `[`}
	if _, err := naming.ParseAudioBookName("Novel", opts); err == nil {
		t.Fatal("ParseAudioBookName(reached malformed expression) returned nil error")
	}
	if _, err := naming.ResolveAudioBookList([]string{"Novel/Chapter 1.mp3"}, opts); err == nil {
		t.Fatal("ResolveAudioBookList(reached malformed name expression) returned nil error")
	}
}
