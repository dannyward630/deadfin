// SPDX-License-Identifier: GPL-2.0-only

// Ported exhaustively from pinned upstream Jellyfin.Naming.Tests Book, Music, and ExternalFiles resolver rows.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func mediaInt(value int) *int          { return &value }
func mediaString(value string) *string { return &value }

// Jellyfin.Naming.Tests.Book.BookResolverTests.Resolve_Books.
// All 16 active InlineData rows are retained.
func TestUpstreamBookResolverResolveBooks(t *testing.T) {
	tests := []struct {
		input        string
		name, series *string
		index, year  *int
	}{
		{"Sherlock Holmes (1887) #1 (of 4) (1887)", nil, mediaString("Sherlock Holmes"), mediaInt(1), mediaInt(1887)},
		{"Sherlock Holmes #2", nil, mediaString("Sherlock Holmes"), mediaInt(2), nil},
		{"Sherlock Holmes (1887) #1", nil, mediaString("Sherlock Holmes"), mediaInt(1), nil},
		{"Sherlock Holmes #2 (1890)", nil, mediaString("Sherlock Holmes"), mediaInt(2), mediaInt(1890)},
		{"A Study in Scarlet (Sherlock Holmes, #1) (1887)", mediaString("A Study in Scarlet"), mediaString("Sherlock Holmes"), mediaInt(1), mediaInt(1887)},
		{"The Adventures of Sherlock Holmes (Sherlock Holmes, #5)", mediaString("The Adventures of Sherlock Holmes"), mediaString("Sherlock Holmes"), mediaInt(5), nil},
		{"The Sign of the Four (1890)", mediaString("The Sign of the Four"), nil, nil, mediaInt(1890)},
		{"The Valley of Fear (1915)", mediaString("The Valley of Fear"), nil, nil, mediaInt(1915)},
		{"2 - The Sign of the Four (1890)", mediaString("The Sign of the Four"), nil, mediaInt(2), mediaInt(1890)},
		{"4 - The Valley of Fear", mediaString("The Valley of Fear"), nil, mediaInt(4), nil},
		{"A Study in Scarlet", mediaString("A Study in Scarlet"), nil, nil, nil},
		{"The Adventures of Sherlock Holmes", mediaString("The Adventures of Sherlock Holmes"), nil, nil, nil},
		{"00 - Dracula's Guest (1914)", mediaString("Dracula's Guest"), nil, mediaInt(0), mediaInt(1914)},
		{"01 - Dracula (1897)", mediaString("Dracula"), nil, mediaInt(1), mediaInt(1897)},
		{"2.0 - Twenty Thousand Leagues Under the Sea", mediaString("Twenty Thousand Leagues Under the Sea"), nil, mediaInt(2), nil},
		{"2.1 - The Blockade Runners", mediaString("2.1 - The Blockade Runners"), nil, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := naming.ParseBook(tt.input)
			if !reflect.DeepEqual(got.Name, tt.name) || !reflect.DeepEqual(got.SeriesName, tt.series) || !reflect.DeepEqual(got.Index, tt.index) || !reflect.DeepEqual(got.Year, tt.year) {
				t.Fatalf("ParseBook(%q) = %#v, want name/series/index/year %v/%v/%v/%v", tt.input, got, tt.name, tt.series, tt.index, tt.year)
			}
		})
	}
}

// Jellyfin.Naming.Tests.Book.BookResolverTests.Resolve_Comics.
// All 3 active InlineData rows are retained.
func TestUpstreamBookResolverResolveComics(t *testing.T) {
	tests := []struct {
		input                 string
		name, series          *string
		chapter, volume, year *int
	}{
		{"Captain Marvel Adventures v01 (1941)", mediaString("Captain Marvel Adventures v01"), nil, nil, mediaInt(1), mediaInt(1941)},
		{"Captain Marvel Adventures c120", mediaString("Captain Marvel Adventures c120"), nil, mediaInt(120), nil, nil},
		{"Captain Marvel Adventures v01 c120", mediaString("Captain Marvel Adventures v01 c120"), nil, mediaInt(120), mediaInt(1), nil},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := naming.ParseBook(tt.input)
			if !reflect.DeepEqual(got.Name, tt.name) || !reflect.DeepEqual(got.SeriesName, tt.series) || !reflect.DeepEqual(got.Index, tt.chapter) || !reflect.DeepEqual(got.ParentIndex, tt.volume) || !reflect.DeepEqual(got.Year, tt.year) {
				t.Fatalf("ParseBook(%q) = %#v, want name/series/chapter/volume/year %v/%v/%v/%v/%v", tt.input, got, tt.name, tt.series, tt.chapter, tt.volume, tt.year)
			}
		})
	}
}

// Jellyfin.Naming.Tests.Music.MultiDiscAlbumTests.AlbumParser_MultidiscPath_Identifies.
// All 31 active InlineData rows are retained.
func TestUpstreamMultiDiscAlbum(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"", false},
		{"C:/", false},
		{"/home/", false},
		{"blah blah", false},
		{"D:/music/weezer/03 Pinkerton", false},
		{"D:/music/michael jackson/Bad (2012 Remaster)", false},
		{"cd1", true},
		{"disc18", true},
		{"disk10", true},
		{"vol7", true},
		{"volume1", true},
		{"cd 1", true},
		{"disc 1", true},
		{"disk 1", true},
		{"disk", false},
		{"disk ·", false},
		{"disk a", false},
		{"disk volume", false},
		{"disc disc", false},
		{"disk disc 6", false},
		{"cd  - 1", true},
		{"disc- 1", true},
		{"disk - 1", true},
		{"Disc 01 (Hugo Wolf · 24 Lieder)", true},
		{"Disc 04 (Encores and Folk Songs)", true},
		{"Disc04 (Encores and Folk Songs)", true},
		{"Disc 04(Encores and Folk Songs)", true},
		{"Disc04(Encores and Folk Songs)", true},
		{"D:/Video/MBTestLibrary/VideoTest/music/.38 special/anth/Disc 2", true},
		{"[1985] Opportunities (Let's make lots of money) (1985)", false},
		{"Blah 04(Encores and Folk Songs)", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := naming.IsMultiDiscAlbum(tt.path, naming.DefaultOptions()); got != tt.want {
				t.Fatalf("IsMultiDiscAlbum(%q) = %t, want %t", tt.path, got, tt.want)
			}
		})
	}
}

// Jellyfin.Naming.Tests.ExternalFiles.ExternalPathParserTests.ParseFile_AudioExtensionsNotMatched_ReturnsNull.
// All 6 active InlineData rows are retained.
func TestUpstreamExternalAudioRejectsNonAudioExtensions(t *testing.T) {
	for _, path := range []string{"", "MyVideo.ass", "MyVideo.mks", "MyVideo.sami", "MyVideo.srt", "MyVideo.m4v"} {
		t.Run(path, func(t *testing.T) {
			if got := naming.ParseExternalAudio(path, "", naming.DefaultOptions()); got != nil {
				t.Fatalf("ParseExternalAudio(%q) = %#v, want nil", path, got)
			}
		})
	}
}

// Jellyfin.Naming.Tests.ExternalFiles.ExternalPathParserTests.ParseFile_AudioExtensionsMatched_ReturnsPath.
// All 6 active InlineData rows are retained.
func TestUpstreamExternalAudioAcceptsAudioExtensions(t *testing.T) {
	for _, path := range []string{"MyVideo.aa", "MyVideo.aac", "MyVideo.flac", "MyVideo.m4a", "MyVideo.mka", "MyVideo.mp3"} {
		t.Run(path, func(t *testing.T) {
			got := naming.ParseExternalAudio(path, "", naming.DefaultOptions())
			if got == nil || got.Path != path {
				t.Fatalf("ParseExternalAudio(%q) = %#v, want path %q", path, got, path)
			}
		})
	}
}

// Jellyfin.Naming.Tests.ExternalFiles.ExternalPathParserTests.ParseFile_SubtitleExtensionsNotMatched_ReturnsNull.
// All 6 active InlineData rows are retained.
func TestUpstreamExternalSubtitleRejectsNonSubtitleExtensions(t *testing.T) {
	for _, path := range []string{"", "MyVideo.aa", "MyVideo.aac", "MyVideo.flac", "MyVideo.mka", "MyVideo.m4v"} {
		t.Run(path, func(t *testing.T) {
			if got := naming.ParseExternalSubtitle(path, "", naming.DefaultOptions()); got != nil {
				t.Fatalf("ParseExternalSubtitle(%q) = %#v, want nil", path, got)
			}
		})
	}
}

// Jellyfin.Naming.Tests.ExternalFiles.ExternalPathParserTests.ParseFile_SubtitleExtensionsMatched_ReturnsPath.
// All 5 active InlineData rows are retained.
func TestUpstreamExternalSubtitleAcceptsSubtitleExtensions(t *testing.T) {
	for _, path := range []string{"MyVideo.ass", "MyVideo.mks", "MyVideo.sami", "MyVideo.srt", "MyVideo.vtt"} {
		t.Run(path, func(t *testing.T) {
			got := naming.ParseExternalSubtitle(path, "", naming.DefaultOptions())
			if got == nil || got.Path != path {
				t.Fatalf("ParseExternalSubtitle(%q) = %#v, want path %q", path, got, path)
			}
		})
	}
}

// Jellyfin.Naming.Tests.ExternalFiles.ExternalPathParserTests.ParseFile_ExtraTokens_ParseToValues.
// All 22 active InlineData rows are retained, including right-to-left language/title/flag precedence.
func TestUpstreamExternalTokenParsing(t *testing.T) {
	tests := []struct {
		tokens                 string
		title, language        *string
		defaulted, forced, sdh bool
	}{
		{"", nil, nil, false, false, false},
		{".default", nil, nil, true, false, false},
		{".forced", nil, nil, false, true, false},
		{".foreign", nil, nil, false, true, false},
		{".default.forced", nil, nil, true, true, false},
		{".forced.default", nil, nil, true, true, false},
		{".DEFAULT.FORCED", nil, nil, true, true, false},
		{".en", nil, mediaString("eng"), false, false, false},
		{".EN", nil, mediaString("eng"), false, false, false},
		{".hi", nil, mediaString("hin"), false, false, false},
		{".fr.en", mediaString("fr"), mediaString("eng"), false, false, false},
		{".en.fr", mediaString("en"), mediaString("fre"), false, false, false},
		{".title.en.fr", mediaString("title.en"), mediaString("fre"), false, false, false},
		{".Title Goes Here", mediaString("Title Goes Here"), nil, false, false, false},
		{".Title.with.Separator", mediaString("Title.with.Separator"), nil, false, false, false},
		{".title.en.default.forced", mediaString("title"), mediaString("eng"), true, true, false},
		{".forced.default.en.title", mediaString("title"), mediaString("eng"), true, true, false},
		{".sdh.en.title", mediaString("title"), mediaString("eng"), false, false, true},
		{".en.cc.title", mediaString("title"), mediaString("eng"), false, false, true},
		{".hi.en.title", mediaString("title"), mediaString("eng"), false, false, true},
		{".en.hi.title", mediaString("title"), mediaString("eng"), false, false, true},
		{".Subs for Chinese Audio.eng", mediaString("Subs for Chinese Audio"), mediaString("eng"), false, false, false},
	}
	for _, tt := range tests {
		path := "My.Video" + tt.tokens + ".srt"
		t.Run(path, func(t *testing.T) {
			got := naming.ParseExternalSubtitle(path, tt.tokens, naming.DefaultOptions())
			if got == nil || !reflect.DeepEqual(got.Title, tt.title) || !reflect.DeepEqual(got.Language, tt.language) || got.IsDefault != tt.defaulted || got.IsForced != tt.forced || got.IsHearingImpaired != tt.sdh {
				t.Fatalf("ParseExternalSubtitle(%q, %q) = %#v, want title/language/default/forced/sdh %v/%v/%t/%t/%t", path, tt.tokens, got, tt.title, tt.language, tt.defaulted, tt.forced, tt.sdh)
			}
		})
	}
}

// Emby.Naming.Book.BookFileNameParser.Parse: every captured name is trimmed
// before it is returned, including the final whole-string fallback match.
func TestSourcePinnedBookParserTrimsCapturedName(t *testing.T) {
	got := naming.ParseBook("  A Title  ")
	if !reflect.DeepEqual(got.Name, mediaString("A Title")) {
		t.Fatalf("ParseBook() name = %v, want trimmed %q", got.Name, "A Title")
	}
}

// Emby.Naming.Book.BookFileNameParser.Parse uses Int32.TryParse for numeric
// capture groups; values outside Int32 range remain unset.
func TestSourcePinnedBookParserRejectsInt32Overflow(t *testing.T) {
	got := naming.ParseBook("2147483648 - Title")
	if !reflect.DeepEqual(got.Name, mediaString("Title")) || got.Index != nil {
		t.Fatalf("ParseBook(Int32-overflow index) = %#v, want title with nil index", got)
	}
}

// Emby.Naming.ExternalFiles.ExternalPathParser.ParseFile iterates every
// configured MediaFlagDelimiter, rather than treating only dots as tokens.
func TestSourcePinnedExternalParserHonorsCustomDelimiter(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.MediaFlagDelimiters = []rune{'-'}
	got := naming.ParseExternalSubtitle("Movie-en-forced.srt", "-en-forced", opts)
	if got == nil || !reflect.DeepEqual(got.Language, mediaString("eng")) || !got.IsForced || got.Title != nil {
		t.Fatalf("ParseExternalSubtitle(custom delimiter) = %#v, want English forced sidecar without title", got)
	}
}

// Emby.Naming.ExternalFiles.ExternalPathParser.ParseFile recognizes default
// and forced flags by case-insensitive substring containment, not exact token equality.
func TestSourcePinnedExternalParserRecognizesFlagSubstrings(t *testing.T) {
	opts := naming.DefaultOptions()
	for _, tt := range []struct {
		tokens            string
		defaulted, forced bool
	}{
		{".mydefaultflag", true, false},
		{".unforced", false, true},
	} {
		t.Run(tt.tokens, func(t *testing.T) {
			got := naming.ParseExternalSubtitle("Movie"+tt.tokens+".srt", tt.tokens, opts)
			if got == nil || got.IsDefault != tt.defaulted || got.IsForced != tt.forced {
				t.Fatalf("ParseExternalSubtitle(%q) = %#v, want default/forced %t/%t", tt.tokens, got, tt.defaulted, tt.forced)
			}
		})
	}
}

// Emby.Naming.ExternalFiles.ExternalPathParser.ParseFile selects only the
// profile's extension set: audio and lyric sidecars are distinct APIs.
func TestSourcePinnedExternalParserSeparatesAudioAndLyricExtensions(t *testing.T) {
	opts := naming.DefaultOptions()
	if got := naming.ParseExternalAudio("Movie.lrc", "", opts); got != nil {
		t.Fatalf("ParseExternalAudio(.lrc) = %#v, want nil", got)
	}
	if got := naming.ParseExternalLyric("Movie.mp3", "", opts); got != nil {
		t.Fatalf("ParseExternalLyric(.mp3) = %#v, want nil", got)
	}
	if got := naming.ParseExternalAudio("Movie.mp3", "", opts); got == nil {
		t.Fatal("ParseExternalAudio(.mp3) = nil, want audio sidecar")
	}
	if got := naming.ParseExternalLyric("Movie.lrc", "", opts); got == nil {
		t.Fatal("ParseExternalLyric(.lrc) = nil, want lyric sidecar")
	}
}

// Emby.Naming.Book.BookFileNameParser.Parse always runs ComicRegex over the
// captured title before applying outer series/index fields from the same match.
func TestSourcePinnedBookParserCombinesComicAndSeriesCaptures(t *testing.T) {
	got := naming.ParseBook("Hero v02 c3 (Saga, #4)")
	if !reflect.DeepEqual(got.Name, mediaString("Hero v02 c3")) || !reflect.DeepEqual(got.SeriesName, mediaString("Saga")) || !reflect.DeepEqual(got.ParentIndex, mediaInt(2)) || !reflect.DeepEqual(got.Index, mediaInt(4)) {
		t.Fatalf("ParseBook(comic series) = %#v, want Hero v02 c3 / Saga / volume 2 / index 4", got)
	}
}

// Emby.Naming.Book.BookFileNameParser.Parse returns a null Name when a
// series/index pattern has no name capture; null is not an empty string.
func TestSourcePinnedBookParserPreservesNullName(t *testing.T) {
	got := naming.ParseBook("Sherlock Holmes #2")
	if got.Name != nil || !reflect.DeepEqual(got.SeriesName, mediaString("Sherlock Holmes")) || !reflect.DeepEqual(got.Index, mediaInt(2)) {
		t.Fatalf("ParseBook(series index) = %#v, want nil Name and Sherlock Holmes index 2", got)
	}
}

// Emby.Naming.ExternalFiles.ExternalPathParser.ParseFile leaves Language and
// Title null when the sidecar carries no extra tokens.
func TestSourcePinnedExternalParserPreservesNullTextFields(t *testing.T) {
	got := naming.ParseExternalSubtitle("x.srt", "", naming.DefaultOptions())
	if got == nil || got.Language != nil || got.Title != nil {
		t.Fatalf("ParseExternalSubtitle(no tokens) = %#v, want nil language and title", got)
	}
}

// Emby.Naming.Audio.AlbumParser.IsMultiPart uses Int32.TryParse for the
// stacking number, so an arm64-native integer overflow is not a multipart disc.
func TestSourcePinnedMultiDiscAlbumRejectsInt32Overflow(t *testing.T) {
	if got := naming.IsMultiDiscAlbum("cd2147483648", naming.DefaultOptions()); got {
		t.Fatal("IsMultiDiscAlbum(Int32-overflow disc) = true, want false")
	}
}

// Emby.Naming.ExternalFiles.ExternalPathParser.ParseFile completes each
// configured delimiter pass in order. A dot pass consumes .en-forced as one
// forced slice before the later dash delimiter is considered.
func TestSourcePinnedExternalParserHonorsDelimiterPassOrder(t *testing.T) {
	opts := naming.DefaultOptions()
	opts.MediaFlagDelimiters = []rune{'.', '-'}
	got := naming.ParseExternalSubtitle("Movie.en-forced.srt", ".en-forced", opts)
	if got == nil || !got.IsForced || got.Language != nil || got.Title != nil {
		t.Fatalf("ParseExternalSubtitle(delimiter pass order) = %#v, want forced with nil language/title", got)
	}
}

// Emby.Naming.ExternalFiles.ExternalPathParser.ParseFile delegates language
// resolution to ILocalizationManager.FindLanguageInfo (lines 95-112), using a
// culture's three-letter code unless its Name is an explicit culture tag.
func TestSourcePinnedExternalParserUsesInjectedLanguageLookup(t *testing.T) {
	lookup := func(token string) (naming.ExternalLanguage, bool) {
		switch token {
		case "pt":
			return naming.ExternalLanguage{Name: "Portuguese", ThreeLetterISO: "por"}, true
		case "zh-Hans":
			return naming.ExternalLanguage{Name: "zh-Hans", ThreeLetterISO: "zho"}, true
		case "cc":
			// cc is normally an SDH marker, but an injected culture lookup must
			// win because upstream calls FindLanguageInfo before flag handling.
			return naming.ExternalLanguage{Name: "Custom Captions", ThreeLetterISO: "cap"}, true
		default:
			return naming.ExternalLanguage{}, false
		}
	}
	for _, tt := range []struct {
		tokens string
		want   *string
		sdh    bool
	}{
		{".pt", mediaString("por"), false},
		{".zh-Hans", mediaString("zh-Hans"), false},
		{".cc", mediaString("cap"), false},
	} {
		t.Run(tt.tokens, func(t *testing.T) {
			got := naming.ParseExternalSubtitleWithLanguageLookup("Movie"+tt.tokens+".srt", tt.tokens, naming.DefaultOptions(), lookup)
			if got == nil || !reflect.DeepEqual(got.Language, tt.want) || got.IsHearingImpaired != tt.sdh {
				t.Fatalf("ParseExternalSubtitleWithLanguageLookup(%q) = %#v, want language/SDH %v/%t", tt.tokens, got, tt.want, tt.sdh)
			}
		})
	}
}

// Emby.Naming.ExternalFiles.ExternalPathParser uses the stock localization
// provider by default, not a hard-coded language subset or a caller-only seam.
func TestSourcePinnedExternalParserDefaultLookupCoversStockPortuguese(t *testing.T) {
	got := naming.ParseExternalSubtitle("Movie.pt.srt", ".pt", naming.DefaultOptions())
	if got == nil || !reflect.DeepEqual(got.Language, mediaString("por")) {
		t.Fatalf("ParseExternalSubtitle(default pt lookup) = %#v, want Portuguese por", got)
	}
}

// Emby.Naming.ExternalFiles.ExternalPathParser.ParseFile obtains stock Dutch
// from the default localization provider, yielding nld with no title token.
func TestSourcePinnedExternalParserDefaultLookupCoversStockDutch(t *testing.T) {
	got := naming.ParseExternalSubtitle("Movie.nl.srt", ".nl", naming.DefaultOptions())
	if got == nil || !reflect.DeepEqual(got.Language, mediaString("nld")) || got.Title != nil {
		t.Fatalf("ParseExternalSubtitle(default nl lookup) = %#v, want Dutch nld and nil title", got)
	}
}

// Emby.Naming.ExternalFiles.ExternalPathParser.ParseFile
// (ExternalPathParser.cs:95-112) resolves the default localization provider;
// its pinned ISO 639-2 table maps sv to swe (iso6392.txt:418).
func TestSourcePinnedExternalParserDefaultLookupCoversStockSwedish(t *testing.T) {
	got := naming.ParseExternalSubtitle("Movie.sv.srt", ".sv", naming.DefaultOptions())
	if got == nil || !reflect.DeepEqual(got.Language, mediaString("swe")) || got.Title != nil {
		t.Fatalf("ParseExternalSubtitle(default sv lookup) = %#v, want Swedish swe and nil title", got)
	}
}

// ACTIVE SOURCE EXCLUSION — Emby.Naming.Book.BookFileNameParser.Parse
// (BookFileNameParser.cs:35-42) accepts a null string and returns an entirely
// unset result. Go's public ParseBook(string) cannot represent null; empty is
// observably different upstream and remains covered. No caller requires a
// widened optional-input API, so this one active source-input behavior is
// intentionally not represented by a Go execution case.
