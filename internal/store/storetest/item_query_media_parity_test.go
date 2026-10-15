// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	storetest "github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestItemStoreQueryMediaParitySQLite executes the source-pinned media-query
// contracts through the public SQLite store boundary.
func TestItemStoreQueryMediaParitySQLite(t *testing.T) {
	runItemStoreQueryMediaParity(t, openMediaSQLiteItemStore)
}

// TestItemStoreQueryMediaParityPostgres runs the same public contracts against
// PostgreSQL when the opt-in integration DSN is available.
func TestItemStoreQueryMediaParityPostgres(t *testing.T) {
	runItemStoreQueryMediaParity(t, openMediaPostgresItemStore)
}

func runItemStoreQueryMediaParity(t *testing.T, open func(*testing.T) *store.ItemStore) {
	t.Helper()
	t.Run("video type ISO and 3D predicates include leaves owned descendants and direct linked children", func(t *testing.T) {
		fixture := newMediaVideoFixture(t, open(t))
		trueValue, falseValue := true, false
		fixture.assertQuery(t, store.ItemQuery{VideoTypes: []item.VideoType{item.VideoTypeDvd}}, []dto.UUID{
			fixture.ownedFolder.ID, fixture.linkedFolder.ID, fixture.directDvd.ID, fixture.descendantDvd3D.ID, fixture.linkedDvdIso3D.ID,
		})
		fixture.assertQuery(t, store.ItemQuery{Is3D: &trueValue}, []dto.UUID{
			fixture.ownedFolder.ID, fixture.linkedFolder.ID, fixture.descendantDvd3D.ID, fixture.linkedDvdIso3D.ID, fixture.direct3D.ID,
		})
		fixture.assertQuery(t, store.ItemQuery{Is3D: &falseValue}, []dto.UUID{fixture.directDvd.ID, fixture.plain.ID})
	})

	t.Run("HD and 4K classifications use source thresholds for leaves folders and linked children", func(t *testing.T) {
		fixture := newMediaResolutionFixture(t, open(t))
		trueValue, falseValue := true, false
		fixture.assertQuery(t, store.ItemQuery{IsHD: &falseValue}, []dto.UUID{fixture.sd.ID, fixture.tallSDAnd4K.ID})
		fixture.assertQuery(t, store.ItemQuery{IsHD: &trueValue}, []dto.UUID{fixture.ownedHDFolder.ID, fixture.hd.ID})
		fixture.assertQuery(t, store.ItemQuery{Is4K: &trueValue}, []dto.UUID{fixture.linked4KFolder.ID, fixture.fourK.ID, fixture.tallSDAnd4K.ID})
		fixture.assertQuery(t, store.ItemQuery{Is4K: &falseValue}, []dto.UUID{})
	})

	t.Run("subtitle and audio language filters treat und as literal null or empty and preserve folder traversal", func(t *testing.T) {
		fixture := newMediaStreamFixture(t, open(t))
		fixture.assertQuery(t, store.ItemQuery{HasSubtitles: mediaBool(true)}, []dto.UUID{
			fixture.ownedFolder.ID, fixture.linkedFolder.ID, fixture.undOwnedFolder.ID, fixture.undLinkedFolder.ID,
			fixture.internalSubtitle.ID, fixture.externalSubtitle.ID, fixture.bothSubtitle.ID, fixture.nullSubtitle.ID, fixture.emptySubtitle.ID, fixture.literalUndSubtitle.ID,
		})
		fixture.assertQuery(t, store.ItemQuery{HasSubtitles: mediaBool(false)}, []dto.UUID{fixture.audioEnglish.ID, fixture.clean.ID, fixture.nullAudio.ID, fixture.emptyAudio.ID, fixture.literalUndAudio.ID})
		fixture.assertQuery(t, store.ItemQuery{SubtitleLanguages: []string{"und"}}, []dto.UUID{
			fixture.undOwnedFolder.ID, fixture.undLinkedFolder.ID, fixture.nullSubtitle.ID, fixture.emptySubtitle.ID, fixture.literalUndSubtitle.ID,
		})
		fixture.assertQuery(t, store.ItemQuery{AudioLanguages: []string{"und"}}, []dto.UUID{fixture.nullAudio.ID, fixture.emptyAudio.ID, fixture.literalUndAudio.ID})
	})

	t.Run("negative stream language predicates distinguish audio internal subtitles external subtitles and either subtitle", func(t *testing.T) {
		fixture := newMediaStreamFixture(t, open(t))
		fixture.assertQuery(t, store.ItemQuery{HasNoAudioTrackWithLanguage: "eng"}, []dto.UUID{
			fixture.linkedFolder.ID, fixture.undOwnedFolder.ID, fixture.undLinkedFolder.ID, fixture.externalSubtitle.ID, fixture.bothSubtitle.ID, fixture.clean.ID,
			fixture.nullSubtitle.ID, fixture.emptySubtitle.ID, fixture.literalUndSubtitle.ID, fixture.nullAudio.ID, fixture.emptyAudio.ID, fixture.literalUndAudio.ID,
		})
		fixture.assertQuery(t, store.ItemQuery{HasNoInternalSubtitleTrackWithLanguage: "eng"}, []dto.UUID{
			fixture.linkedFolder.ID, fixture.undOwnedFolder.ID, fixture.undLinkedFolder.ID, fixture.externalSubtitle.ID, fixture.audioEnglish.ID, fixture.clean.ID,
			fixture.nullSubtitle.ID, fixture.emptySubtitle.ID, fixture.literalUndSubtitle.ID, fixture.nullAudio.ID, fixture.emptyAudio.ID, fixture.literalUndAudio.ID,
		})
		fixture.assertQuery(t, store.ItemQuery{HasNoExternalSubtitleTrackWithLanguage: "eng"}, []dto.UUID{
			fixture.ownedFolder.ID, fixture.undOwnedFolder.ID, fixture.undLinkedFolder.ID, fixture.internalSubtitle.ID, fixture.audioEnglish.ID, fixture.clean.ID,
			fixture.nullSubtitle.ID, fixture.emptySubtitle.ID, fixture.literalUndSubtitle.ID, fixture.nullAudio.ID, fixture.emptyAudio.ID, fixture.literalUndAudio.ID,
		})
		fixture.assertQuery(t, store.ItemQuery{HasNoSubtitleTrackWithLanguage: "eng"}, []dto.UUID{
			fixture.undOwnedFolder.ID, fixture.undLinkedFolder.ID, fixture.audioEnglish.ID, fixture.clean.ID,
			fixture.nullSubtitle.ID, fixture.emptySubtitle.ID, fixture.literalUndSubtitle.ID, fixture.nullAudio.ID, fixture.emptyAudio.ID, fixture.literalUndAudio.ID,
		})
	})

	t.Run("chapter image true and false predicates use descendant and linked leaf facts rather than a folder chapter", func(t *testing.T) {
		fixture := newMediaChapterFixture(t, open(t))
		fixture.assertQuery(t, store.ItemQuery{HasChapterImages: mediaBool(true)}, []dto.UUID{fixture.ownedFolder.ID, fixture.linkedFolder.ID, fixture.ownedImage.ID, fixture.linkedImage.ID})
		fixture.assertQuery(t, store.ItemQuery{HasChapterImages: mediaBool(false)}, []dto.UUID{fixture.folderWithOwnImage.ID, fixture.noImage.ID})
	})

	t.Run("extra predicates and their complements classify owners while default ownership excludes plain owned and alternate items", func(t *testing.T) {
		fixture := newMediaExtrasFixture(t, open(t))
		trueValue, falseValue := true, false
		cases := []struct {
			name  string
			query store.ItemQuery
			want  []dto.UUID
		}{
			{name: "special feature true", query: store.ItemQuery{HasSpecialFeature: &trueValue}, want: []dto.UUID{fixture.ownedFolder.ID, fixture.featureOwner.ID}},
			{name: "special feature false", query: store.ItemQuery{HasSpecialFeature: &falseValue}, want: []dto.UUID{fixture.linkedFolder.ID, fixture.trailerOwner.ID, fixture.themeSongOwner.ID, fixture.themeVideoOwner.ID, fixture.ordinary.ID, fixture.unknownOwner.ID}},
			{name: "trailer true", query: store.ItemQuery{HasTrailer: &trueValue}, want: []dto.UUID{fixture.linkedFolder.ID, fixture.trailerOwner.ID}},
			{name: "trailer false", query: store.ItemQuery{HasTrailer: &falseValue}, want: []dto.UUID{fixture.ownedFolder.ID, fixture.featureOwner.ID, fixture.themeSongOwner.ID, fixture.themeVideoOwner.ID, fixture.ordinary.ID, fixture.unknownOwner.ID}},
			{name: "theme song true", query: store.ItemQuery{HasThemeSong: &trueValue}, want: []dto.UUID{fixture.themeSongOwner.ID}},
			{name: "theme song false", query: store.ItemQuery{HasThemeSong: &falseValue}, want: []dto.UUID{fixture.ownedFolder.ID, fixture.linkedFolder.ID, fixture.featureOwner.ID, fixture.trailerOwner.ID, fixture.themeVideoOwner.ID, fixture.ordinary.ID, fixture.unknownOwner.ID}},
			{name: "theme video true", query: store.ItemQuery{HasThemeVideo: &trueValue}, want: []dto.UUID{fixture.themeVideoOwner.ID}},
			{name: "theme video false", query: store.ItemQuery{HasThemeVideo: &falseValue}, want: []dto.UUID{fixture.ownedFolder.ID, fixture.linkedFolder.ID, fixture.featureOwner.ID, fixture.trailerOwner.ID, fixture.themeSongOwner.ID, fixture.ordinary.ID, fixture.unknownOwner.ID}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) { fixture.assertQuery(t, tc.query, tc.want) })
		}
		fixture.assertQuery(t, store.ItemQuery{ItemIDs: []dto.UUID{fixture.ordinary.ID, fixture.plainOwned.ID, fixture.alternate.ID, fixture.trailerExtra.ID}}, []dto.UUID{fixture.ordinary.ID, fixture.trailerExtra.ID})
	})
}

type mediaQueryFixture struct {
	items *store.ItemStore
	all   []dto.UUID
}

func (f mediaQueryFixture) assertQuery(t *testing.T, query store.ItemQuery, want []dto.UUID) {
	t.Helper()
	if query.ItemIDs == nil {
		query.ItemIDs = f.all
	}
	query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
	query.SortOrder = []store.SortOrder{store.SortOrderAscending}
	query.EnableTotalRecordCount = true
	got, err := f.items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("querying media fixture: %v", err)
	}
	ids := make([]dto.UUID, len(got.Items))
	for i := range got.Items {
		ids[i] = got.Items[i].ID
	}
	if !reflect.DeepEqual(ids, want) || got.TotalRecordCount != len(want) {
		t.Fatalf("query IDs = %v (count %d), want %v (count %d)", ids, got.TotalRecordCount, want, len(want))
	}
}

type mediaVideoFixture struct {
	mediaQueryFixture
	ownedFolder, linkedFolder, directDvd, descendantDvd3D, linkedDvdIso3D, plain, direct3D item.Item
}

func newMediaVideoFixture(t *testing.T, items *store.ItemStore) mediaVideoFixture {
	t.Helper()
	ownedFolder := mediaFolder(t, 1)
	linkedFolder := mediaFolder(t, 2)
	directDvd := mediaItem(t, 10)
	directDvd.VideoType = mediaVideoType(item.VideoTypeDvd)
	descendant := mediaItem(t, 11)
	descendant.ParentID = &ownedFolder.ID
	descendant.VideoType = mediaVideoType(item.VideoTypeDvd)
	descendant.Video3DFormat = media3D(item.Video3DFormatMVC)
	linked := mediaItem(t, 12)
	linked.IsoType = mediaIsoType(item.IsoTypeDvd)
	linked.Video3DFormat = media3D(item.Video3DFormatMVC)
	plain := mediaItem(t, 13)
	direct3D := mediaItem(t, 14)
	direct3D.Video3DFormat = media3D(item.Video3DFormatMVC)
	linkedFolder.LinkedChildren = []item.LinkedChildRef{{ItemID: linked.ID, Type: item.LinkedChildTypeManual}}
	mediaUpsert(t, items, ownedFolder, directDvd, descendant, linked, plain, direct3D, linkedFolder)
	return mediaVideoFixture{mediaQueryFixture: mediaQueryFixture{items: items, all: mediaIDs(ownedFolder, linkedFolder, directDvd, descendant, linked, plain, direct3D)}, ownedFolder: ownedFolder, linkedFolder: linkedFolder, directDvd: directDvd, descendantDvd3D: descendant, linkedDvdIso3D: linked, plain: plain, direct3D: direct3D}
}

type mediaResolutionFixture struct {
	mediaQueryFixture
	ownedHDFolder, linked4KFolder, sd, hd, fourK, tallSDAnd4K, unknown item.Item
}

func newMediaResolutionFixture(t *testing.T, items *store.ItemStore) mediaResolutionFixture {
	t.Helper()
	owned := mediaFolder(t, 1)
	linkedFolder := mediaFolder(t, 2)
	sd := mediaItem(t, 10)
	sd.Width = mediaInt(1199)
	hd := mediaItem(t, 11)
	hd.ParentID = &owned.ID
	hd.Width = mediaInt(1200)
	hd.Height = mediaInt(1080)
	fourK := mediaItem(t, 12)
	fourK.Width = mediaInt(3800)
	tall := mediaItem(t, 13)
	tall.Width = mediaInt(100)
	tall.Height = mediaInt(2100)
	unknown := mediaItem(t, 14)
	linkedFolder.LinkedChildren = []item.LinkedChildRef{{ItemID: fourK.ID, Type: item.LinkedChildTypeManual}}
	mediaUpsert(t, items, owned, sd, hd, fourK, tall, unknown, linkedFolder)
	return mediaResolutionFixture{mediaQueryFixture: mediaQueryFixture{items: items, all: mediaIDs(owned, linkedFolder, sd, hd, fourK, tall, unknown)}, ownedHDFolder: owned, linked4KFolder: linkedFolder, sd: sd, hd: hd, fourK: fourK, tallSDAnd4K: tall, unknown: unknown}
}

type mediaStreamFixture struct {
	mediaQueryFixture
	ownedFolder, linkedFolder, internalSubtitle, externalSubtitle, bothSubtitle, audioEnglish, clean                         item.Item
	undOwnedFolder, undLinkedFolder, nullSubtitle, emptySubtitle, literalUndSubtitle, nullAudio, emptyAudio, literalUndAudio item.Item
}

func newMediaStreamFixture(t *testing.T, items *store.ItemStore) mediaStreamFixture {
	t.Helper()
	owned := mediaFolder(t, 1)
	linkedFolder := mediaFolder(t, 2)
	internal := mediaItem(t, 10)
	internal.ParentID = &owned.ID
	external := mediaItem(t, 11)
	both := mediaItem(t, 12)
	audioEnglish := mediaItem(t, 13)
	clean := mediaItem(t, 14)
	undOwned := mediaFolder(t, 3)
	undLinked := mediaFolder(t, 4)
	nullSubtitle := mediaItem(t, 20)
	nullSubtitle.ParentID = &undOwned.ID
	emptySubtitle := mediaItem(t, 21)
	literalUndSubtitle := mediaItem(t, 22)
	nullAudio := mediaItem(t, 23)
	emptyAudio := mediaItem(t, 24)
	literalUndAudio := mediaItem(t, 25)
	linkedFolder.LinkedChildren = []item.LinkedChildRef{{ItemID: external.ID, Type: item.LinkedChildTypeManual}}
	undLinked.LinkedChildren = []item.LinkedChildRef{{ItemID: emptySubtitle.ID, Type: item.LinkedChildTypeManual}}
	mediaUpsert(t, items, owned, undOwned, internal, external, both, audioEnglish, clean, nullSubtitle, emptySubtitle, literalUndSubtitle, nullAudio, emptyAudio, literalUndAudio, linkedFolder, undLinked)
	mediaReplaceStreams(t, items, internal.ID, mediaStream(0, playback.StreamTypeSubtitle, mediaString("eng"), false), mediaStream(1, playback.StreamTypeAudio, mediaString("eng"), false))
	mediaReplaceStreams(t, items, external.ID, mediaStream(0, playback.StreamTypeSubtitle, mediaString("eng"), true))
	mediaReplaceStreams(t, items, both.ID, mediaStream(0, playback.StreamTypeSubtitle, mediaString("eng"), false), mediaStream(1, playback.StreamTypeSubtitle, mediaString("eng"), true))
	mediaReplaceStreams(t, items, audioEnglish.ID, mediaStream(0, playback.StreamTypeAudio, mediaString("eng"), false))
	mediaReplaceStreams(t, items, nullSubtitle.ID, mediaStream(0, playback.StreamTypeSubtitle, nil, false))
	mediaReplaceStreams(t, items, emptySubtitle.ID, mediaStream(0, playback.StreamTypeSubtitle, mediaString(""), false))
	mediaReplaceStreams(t, items, literalUndSubtitle.ID, mediaStream(0, playback.StreamTypeSubtitle, mediaString("und"), false))
	mediaReplaceStreams(t, items, nullAudio.ID, mediaStream(0, playback.StreamTypeAudio, nil, false))
	mediaReplaceStreams(t, items, emptyAudio.ID, mediaStream(0, playback.StreamTypeAudio, mediaString(""), false))
	mediaReplaceStreams(t, items, literalUndAudio.ID, mediaStream(0, playback.StreamTypeAudio, mediaString("und"), false))
	return mediaStreamFixture{mediaQueryFixture: mediaQueryFixture{items: items, all: mediaIDs(owned, linkedFolder, internal, external, both, audioEnglish, clean, undOwned, undLinked, nullSubtitle, emptySubtitle, literalUndSubtitle, nullAudio, emptyAudio, literalUndAudio)}, ownedFolder: owned, linkedFolder: linkedFolder, internalSubtitle: internal, externalSubtitle: external, bothSubtitle: both, audioEnglish: audioEnglish, clean: clean, undOwnedFolder: undOwned, undLinkedFolder: undLinked, nullSubtitle: nullSubtitle, emptySubtitle: emptySubtitle, literalUndSubtitle: literalUndSubtitle, nullAudio: nullAudio, emptyAudio: emptyAudio, literalUndAudio: literalUndAudio}
}

type mediaChapterFixture struct {
	mediaQueryFixture
	ownedFolder, linkedFolder, ownedImage, linkedImage, folderWithOwnImage, noImage item.Item
}

func newMediaChapterFixture(t *testing.T, items *store.ItemStore) mediaChapterFixture {
	t.Helper()
	owned := mediaFolder(t, 1)
	linkedFolder := mediaFolder(t, 2)
	ownedImage := mediaItem(t, 10)
	ownedImage.ParentID = &owned.ID
	linkedImage := mediaItem(t, 11)
	noImage := mediaItem(t, 12)
	folderOwnImage := mediaFolder(t, 3)
	linkedFolder.LinkedChildren = []item.LinkedChildRef{{ItemID: linkedImage.ID, Type: item.LinkedChildTypeManual}}
	mediaUpsert(t, items, owned, ownedImage, linkedImage, noImage, folderOwnImage, linkedFolder)
	mediaReplaceChapters(t, items, ownedImage.ID, []store.Chapter{{Index: 0, ImagePath: mediaString("owned.jpg")}})
	mediaReplaceChapters(t, items, linkedImage.ID, []store.Chapter{{Index: 0, ImagePath: mediaString("linked.jpg")}})
	mediaReplaceChapters(t, items, folderOwnImage.ID, []store.Chapter{{Index: 0, ImagePath: mediaString("folder.jpg")}})
	return mediaChapterFixture{mediaQueryFixture: mediaQueryFixture{items: items, all: mediaIDs(owned, linkedFolder, ownedImage, linkedImage, noImage, folderOwnImage)}, ownedFolder: owned, linkedFolder: linkedFolder, ownedImage: ownedImage, linkedImage: linkedImage, folderWithOwnImage: folderOwnImage, noImage: noImage}
}

type mediaExtrasFixture struct {
	mediaQueryFixture
	ownedFolder, linkedFolder, featureOwner, trailerOwner, themeSongOwner, themeVideoOwner, ordinary, unknownOwner, trailerExtra, plainOwned, alternate item.Item
}

func newMediaExtrasFixture(t *testing.T, items *store.ItemStore) mediaExtrasFixture {
	t.Helper()
	owned := mediaFolder(t, 1)
	linkedFolder := mediaFolder(t, 2)
	feature := mediaItem(t, 10)
	feature.ParentID = &owned.ID
	trailer := mediaItem(t, 11)
	song := mediaItem(t, 12)
	video := mediaItem(t, 13)
	ordinary := mediaItem(t, 14)
	unknown := mediaItem(t, 15)
	featureExtra := mediaItem(t, 20)
	featureExtra.OwnerID = &feature.ID
	featureExtra.ExtraType = mediaExtraType(item.ExtraTypeClip)
	trailerExtra := mediaItem(t, 21)
	trailerExtra.OwnerID = &trailer.ID
	trailerExtra.ExtraType = mediaExtraType(item.ExtraTypeTrailer)
	songExtra := mediaItem(t, 22)
	songExtra.OwnerID = &song.ID
	songExtra.ExtraType = mediaExtraType(item.ExtraTypeThemeSong)
	videoExtra := mediaItem(t, 23)
	videoExtra.OwnerID = &video.ID
	videoExtra.ExtraType = mediaExtraType(item.ExtraTypeThemeVideo)
	unknownExtra := mediaItem(t, 24)
	unknownExtra.OwnerID = &unknown.ID
	unknownExtra.ExtraType = mediaExtraType(item.ExtraTypeUnknown)
	plainOwned := mediaItem(t, 30)
	plainOwned.OwnerID = &ordinary.ID
	alternate := mediaItem(t, 31)
	alternate.PrimaryVersionID = &ordinary.ID
	linkedFolder.LinkedChildren = []item.LinkedChildRef{{ItemID: trailer.ID, Type: item.LinkedChildTypeManual}}
	mediaUpsert(t, items, owned, feature, trailer, song, video, ordinary, unknown, featureExtra, trailerExtra, songExtra, videoExtra, unknownExtra, plainOwned, alternate, linkedFolder)
	return mediaExtrasFixture{mediaQueryFixture: mediaQueryFixture{items: items, all: mediaIDs(owned, linkedFolder, feature, trailer, song, video, ordinary, unknown)}, ownedFolder: owned, linkedFolder: linkedFolder, featureOwner: feature, trailerOwner: trailer, themeSongOwner: song, themeVideoOwner: video, ordinary: ordinary, unknownOwner: unknown, trailerExtra: trailerExtra, plainOwned: plainOwned, alternate: alternate}
}

func openMediaSQLiteItemStore(t *testing.T) *store.ItemStore {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("opening SQLite media fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("closing SQLite media fixture: %v", err)
		}
	})
	return store.NewItemStore(stores)
}

func openMediaPostgresItemStore(t *testing.T) *store.ItemStore {
	t.Helper()
	return store.NewItemStore(storetest.OpenPostgres(t))
}

func mediaItem(t *testing.T, value int) item.Item {
	t.Helper()
	name := "same"
	return item.Item{ID: mediaUUID(t, value), Kind: item.KindMovie, Name: &name, SortName: &name}
}

func mediaFolder(t *testing.T, value int) item.Item {
	t.Helper()
	it := mediaItem(t, value)
	it.Kind, it.IsFolder = item.KindFolder, true
	return it
}

func mediaUpsert(t *testing.T, items *store.ItemStore, values ...item.Item) {
	t.Helper()
	for _, value := range values {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upserting media item %s: %v", value.ID, err)
		}
	}
}

func mediaReplaceStreams(t *testing.T, items *store.ItemStore, id dto.UUID, streams ...playback.MediaStream) {
	t.Helper()
	if err := items.ReplaceStreams(t.Context(), id, streams); err != nil {
		t.Fatalf("replacing streams for %s: %v", id, err)
	}
}

func mediaReplaceChapters(t *testing.T, items *store.ItemStore, id dto.UUID, chapters []store.Chapter) {
	t.Helper()
	if err := items.ReplaceChapters(t.Context(), id, chapters); err != nil {
		t.Fatalf("replacing chapters for %s: %v", id, err)
	}
}

func mediaStream(index int, kind playback.StreamType, language *string, external bool) playback.MediaStream {
	return playback.MediaStream{Index: index, Type: kind, Language: language, IsExternal: external}
}

func mediaIDs(values ...item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(values))
	for i := range values {
		ids[i] = values[i].ID
	}
	return ids
}

func mediaUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing media fixture UUID %d: %v", value, err)
	}
	return id
}

func mediaBool(value bool) *bool                           { return &value }
func mediaInt(value int) *int                              { return &value }
func mediaString(value string) *string                     { return &value }
func mediaVideoType(value item.VideoType) *item.VideoType  { return &value }
func mediaIsoType(value item.IsoType) *item.IsoType        { return &value }
func media3D(value item.Video3DFormat) *item.Video3DFormat { return &value }
func mediaExtraType(value item.ExtraType) *item.ExtraType  { return &value }
