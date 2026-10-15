// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// These executable vectors pin the source-owned ItemQuery relation families:
// referenced item values and artist rules are from
// BaseItemRepository.TranslateQuery.cs:588-659; linked/ancestor, presentation,
// inherited-tag, series, and boxset predicates are from :1046-1122; folder
// rating propagation is from FolderAwareFilterExtensions.cs:24-82. They seed
// only ItemStore public APIs, so SQLite and PostgreSQL exercise the same
// satellite persistence and hierarchy closure a real caller uses.
func TestItemStoreQueryValuesSQLite(t *testing.T) {
	runItemStoreQueryValues(t, openItemStore)
}

func TestItemStoreQueryValuesPostgres(t *testing.T) {
	runItemStoreQueryValues(t, openPostgresItemStore)
}

func runItemStoreQueryValues(t *testing.T, open func(*testing.T) *store.ItemStore) {
	fixture := newValuesQueryFixture(t, open)
	hasRating, lacksRating := true, false
	cases := []struct {
		name  string
		query store.ItemQuery
		want  []dto.UUID
		total int
	}{
		{
			name:  "genre IDs resolve the referenced item clean name",
			query: store.ItemQuery{GenreIDs: []dto.UUID{fixture.genre.ID}},
			want:  []dto.UUID{fixture.genreOnly.ID, fixture.genreStudio.ID}, total: 2,
		},
		{
			name:  "studio IDs resolve the referenced item clean name",
			query: store.ItemQuery{StudioIDs: []dto.UUID{fixture.studio.ID}},
			want:  []dto.UUID{fixture.studioOnly.ID, fixture.genreStudio.ID}, total: 2,
		},
		{
			name:  "independent referenced relations intersect on one item",
			query: store.ItemQuery{GenreIDs: []dto.UUID{fixture.genre.ID}, StudioIDs: []dto.UUID{fixture.studio.ID}},
			want:  []dto.UUID{fixture.genreStudio.ID}, total: 1,
		},
		{
			name:  "artist IDs match artist and album artist relations",
			query: store.ItemQuery{ItemIDs: fixture.artistCandidates(), ArtistIDs: []dto.UUID{fixture.artist.ID}},
			want:  []dto.UUID{fixture.artistOnly.ID, fixture.albumArtistOnly.ID, fixture.bothArtistRelations.ID}, total: 3,
		},
		{
			name:  "album artist IDs match only album artist relations",
			query: store.ItemQuery{ItemIDs: fixture.artistCandidates(), AlbumArtistIDs: []dto.UUID{fixture.artist.ID}},
			want:  []dto.UUID{fixture.albumArtistOnly.ID, fixture.bothArtistRelations.ID}, total: 2,
		},
		{
			name:  "contributing artist IDs reject the same name as an album artist",
			query: store.ItemQuery{ItemIDs: fixture.artistCandidates(), ContributingArtistIDs: []dto.UUID{fixture.artist.ID}},
			want:  []dto.UUID{fixture.artistOnly.ID}, total: 1,
		},
		{
			name:  "excluded artist IDs complement both artist relation kinds",
			query: store.ItemQuery{ItemIDs: fixture.artistCandidates(), ExcludeArtistIDs: []dto.UUID{fixture.artist.ID}},
			want:  []dto.UUID{fixture.noArtistRelation.ID}, total: 1,
		},
		{
			name:  "album IDs match direct parent identity",
			query: store.ItemQuery{AlbumIDs: []dto.UUID{fixture.album.ID}},
			want:  []dto.UUID{fixture.albumTrack.ID}, total: 1,
		},
		{
			name:  "trailer type matches its normalized relation",
			query: store.ItemQuery{TrailerTypes: []item.TrailerType{item.TrailerTypeLocalTrailer}},
			want:  []dto.UUID{fixture.trailer.ID}, total: 1,
		},
		{
			name:  "image type keeps one outer row and counts before paging",
			query: store.ItemQuery{ImageTypes: []item.ImageType{item.ImageTypePrimary}, Limit: valuesQueryInt(1)},
			want:  []dto.UUID{fixture.imageDouble.ID}, total: 2,
		},
		{
			name:  "external series ID is an exact scalar relation key",
			query: store.ItemQuery{ExternalSeriesID: "series:source-42"},
			want:  []dto.UUID{fixture.externalSeries.ID}, total: 1,
		},
		{
			name:  "series status matches persisted series fact",
			query: store.ItemQuery{SeriesStatuses: []item.SeriesStatus{item.SeriesStatusEnded}},
			want:  []dto.UUID{fixture.endedSeries.ID}, total: 1,
		},
		{
			name:  "linked child ancestor IDs use child hierarchy closure",
			query: store.ItemQuery{LinkedChildAncestorIDs: []dto.UUID{fixture.linkedAncestor.ID}},
			want:  []dto.UUID{fixture.linkedParent.ID}, total: 1,
		},
		{
			name:  "official ratings include direct matches ancestors and linked parents",
			query: store.ItemQuery{ItemIDs: fixture.ratingCandidates(), OfficialRatings: []string{"TV-MA"}},
			want:  fixture.ratingMatches(), total: 4,
		},
		{
			name:  "has official rating preserves descendant and linked child semantics",
			query: store.ItemQuery{ItemIDs: fixture.ratingCandidates(), HasOfficialRating: &hasRating},
			want:  fixture.ratingMatches(), total: 4,
		},
		{
			name:  "missing official rating is the folder aware complement",
			query: store.ItemQuery{ItemIDs: append(fixture.ratingCandidates(), fixture.noRating.ID), HasOfficialRating: &lacksRating},
			want:  []dto.UUID{fixture.noRating.ID}, total: 1,
		},
		{
			name:  "presentation unique key matches the exact item key",
			query: store.ItemQuery{PresentationUniqueKey: "presentation:key"},
			want:  []dto.UUID{fixture.presentationKey.ID}, total: 1,
		},
		{
			name:  "ancestor presentation key matches a direct child",
			query: store.ItemQuery{AncestorWithPresentationUniqueKey: "ancestor:key"},
			want:  []dto.UUID{fixture.presentationChild.ID}, total: 1,
		},
		{
			name:  "series presentation key matches the persisted series key",
			query: store.ItemQuery{SeriesPresentationUniqueKey: "series:presentation-key"},
			want:  []dto.UUID{fixture.seriesKey.ID}, total: 1,
		},
		{
			name:  "inherited tags clean case and punctuation before inclusion",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.inheritedAllowed.ID, fixture.inheritedBlocked.ID}, IncludeInheritedTags: []string{"FEATURED / PRIME"}},
			want:  []dto.UUID{fixture.inheritedAllowed.ID, fixture.inheritedBlocked.ID}, total: 2,
		},
		{
			name:  "excluded inherited tags complement an included inherited relation",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.inheritedAllowed.ID, fixture.inheritedBlocked.ID}, IncludeInheritedTags: []string{"featured prime"}, ExcludeInheritedTags: []string{"blocked prime"}},
			want:  []dto.UUID{fixture.inheritedAllowed.ID}, total: 1,
		},
		{
			name:  "inherited inclusion follows direct series ancestor and top parent tags",
			query: store.ItemQuery{ItemIDs: fixture.inheritedCandidates(), IncludeInheritedTags: []string{"FEATURED / PRIME"}},
			want:  fixture.inheritedMatches(), total: 5,
		},
		{
			name:  "inherited exclusion removes tagged ancestors as well as direct tags",
			query: store.ItemQuery{ItemIDs: fixture.inheritedCandidates(), IncludeInheritedTags: []string{"featured prime"}, ExcludeInheritedTags: []string{"blocked prime"}},
			want:  []dto.UUID{fixture.inheritedAllowed.ID, fixture.inheritedSeriesChild.ID, fixture.inheritedTopChild.ID}, total: 3,
		},
		{
			name:  "boxset library folders match any supplied relation identity",
			query: store.ItemQuery{BoxSetLibraryFolders: []dto.UUID{fixture.unusedFolderID, fixture.boxsetFolderID}},
			want:  []dto.UUID{fixture.boxset.ID}, total: 1,
		},
		{
			name:  "empty relation list leaves an explicit item scope unconstrained",
			query: store.ItemQuery{ItemIDs: []dto.UUID{fixture.genreOnly.ID, fixture.studioOnly.ID}, GenreIDs: []dto.UUID{}},
			want:  []dto.UUID{fixture.genreOnly.ID, fixture.studioOnly.ID}, total: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query := tc.query
			query.SortBy = []store.ItemSortBy{store.ItemSortBySortName}
			query.SortOrder = []store.SortOrder{store.SortOrderAscending}
			query.EnableTotalRecordCount = true
			got, err := fixture.items.Query(t.Context(), query)
			if err != nil {
				t.Fatalf("querying relation-value fixture: %v", err)
			}
			if ids := valuesQueryIDs(got.Items); !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("item IDs = %v, want %v", ids, tc.want)
			}
			if got.TotalRecordCount != tc.total {
				t.Fatalf("total record count = %d, want %d before paging", got.TotalRecordCount, tc.total)
			}
		})
	}
}

type valuesQueryFixture struct {
	items                  *store.ItemStore
	genre                  item.Item
	studio                 item.Item
	artist                 item.Item
	album                  item.Item
	genreOnly              item.Item
	studioOnly             item.Item
	genreStudio            item.Item
	artistOnly             item.Item
	albumArtistOnly        item.Item
	bothArtistRelations    item.Item
	noArtistRelation       item.Item
	albumTrack             item.Item
	trailer                item.Item
	imageDouble            item.Item
	imageSingle            item.Item
	externalSeries         item.Item
	endedSeries            item.Item
	linkedAncestor         item.Item
	linkedParent           item.Item
	ratingRoot             item.Item
	ratingSeries           item.Item
	ratingChild            item.Item
	ratingLinkedParent     item.Item
	noRating               item.Item
	presentationKey        item.Item
	presentationChild      item.Item
	seriesKey              item.Item
	inheritedAllowed       item.Item
	inheritedBlocked       item.Item
	inheritedSeries        item.Item
	inheritedSeriesChild   item.Item
	inheritedAncestor      item.Item
	inheritedAncestorChild item.Item
	inheritedTop           item.Item
	inheritedTopChild      item.Item
	boxset                 item.Item
	boxsetFolderID         dto.UUID
	unusedFolderID         dto.UUID
}

func newValuesQueryFixture(t *testing.T, open func(*testing.T) *store.ItemStore) valuesQueryFixture {
	t.Helper()
	items := open(t)
	genre := valuesQueryItem(t, 101, item.KindGenre, "reference genre", nil)
	genre.Name = valuesQueryString("Génre / Target")
	studio := valuesQueryItem(t, 102, item.KindStudio, "reference studio", nil)
	studio.Name = valuesQueryString("Stüdio / Target")
	artist := valuesQueryItem(t, 103, item.KindMusicArtist, "reference artist", nil)
	artist.Name = valuesQueryString("Béla Artist")
	album := valuesQueryItem(t, 104, item.KindMusicAlbum, "album", nil)

	genreOnly := valuesQueryItem(t, 1, item.KindMovie, "a genre only", nil)
	genreOnly.Values.Genres = []string{"genre target"}
	studioOnly := valuesQueryItem(t, 2, item.KindMovie, "b studio only", nil)
	studioOnly.Values.Studios = []string{"studio target"}
	genreStudio := valuesQueryItem(t, 3, item.KindMovie, "c genre studio", nil)
	genreStudio.Values = item.ItemValues{Genres: []string{"GENRE TARGET"}, Studios: []string{"STUDIO TARGET"}}

	artistOnly := valuesQueryItem(t, 4, item.KindAudio, "d artist only", nil)
	artistOnly.Values.Artists = []string{"BELA ARTIST"}
	albumArtistOnly := valuesQueryItem(t, 5, item.KindAudio, "e album artist only", nil)
	albumArtistOnly.Values.AlbumArtists = []string{"béla artist"}
	bothArtistRelations := valuesQueryItem(t, 6, item.KindAudio, "f both artist relations", nil)
	bothArtistRelations.Values = item.ItemValues{Artists: []string{"Bela Artist"}, AlbumArtists: []string{"BELA ARTIST"}}
	noArtistRelation := valuesQueryItem(t, 7, item.KindAudio, "g no artist relation", nil)
	albumTrack := valuesQueryItem(t, 8, item.KindAudio, "h album track", &album.ID)
	trailer := valuesQueryItem(t, 9, item.KindMovie, "i trailer", nil)
	trailer.TrailerTypes = []item.TrailerType{item.TrailerTypeLocalTrailer}
	imageDouble := valuesQueryItem(t, 10, item.KindMovie, "j image double", nil)
	imageDouble.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "primary-0.jpg"}, {Type: item.ImageTypePrimary, Index: 1, Path: "primary-1.jpg"}}
	imageSingle := valuesQueryItem(t, 11, item.KindMovie, "k image single", nil)
	imageSingle.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "primary.jpg"}}
	externalSeries := valuesQueryItem(t, 12, item.KindEpisode, "l external series", nil)
	externalSeries.ExternalSeriesID = valuesQueryString("series:source-42")
	ended := item.SeriesStatusEnded
	endedSeries := valuesQueryItem(t, 13, item.KindSeries, "m ended series", nil)
	endedSeries.SeriesStatus = &ended

	linkedAncestor := valuesQueryItem(t, 14, item.KindFolder, "n linked ancestor", nil)
	linkedChild := valuesQueryItem(t, 15, item.KindMovie, "o linked child", &linkedAncestor.ID)
	linkedParent := valuesQueryItem(t, 16, item.KindBoxSet, "p linked parent", nil)
	linkedParent.LinkedChildren = []item.LinkedChildRef{{ItemID: linkedChild.ID, Type: item.LinkedChildTypeManual}}

	ratingRoot := valuesQueryItem(t, 17, item.KindFolder, "q rating root", nil)
	ratingSeries := valuesQueryItem(t, 18, item.KindSeries, "r rating series", &ratingRoot.ID)
	ratingChild := valuesQueryItem(t, 19, item.KindEpisode, "s rating child", &ratingSeries.ID)
	ratingChild.OfficialRating = valuesQueryString("TV-MA")
	ratingLinkedParent := valuesQueryItem(t, 20, item.KindBoxSet, "t rating linked parent", nil)
	ratingLinkedParent.LinkedChildren = []item.LinkedChildRef{{ItemID: ratingChild.ID, Type: item.LinkedChildTypeManual}}
	noRating := valuesQueryItem(t, 21, item.KindMovie, "u no rating", nil)

	presentationKey := valuesQueryItem(t, 22, item.KindMovie, "v presentation key", nil)
	presentationKey.PresentationUniqueKey = valuesQueryString("presentation:key")
	presentationParent := valuesQueryItem(t, 23, item.KindFolder, "w presentation parent", nil)
	presentationParent.PresentationUniqueKey = valuesQueryString("ancestor:key")
	presentationChild := valuesQueryItem(t, 24, item.KindMovie, "x presentation child", &presentationParent.ID)
	seriesKey := valuesQueryItem(t, 25, item.KindEpisode, "y series key", nil)
	seriesKey.SeriesPresentationUniqueKey = valuesQueryString("series:presentation-key")
	inheritedAllowed := valuesQueryItem(t, 26, item.KindMovie, "z inherited allowed", nil)
	inheritedAllowed.Values.Tags = []string{"Féatured / Prime"}
	inheritedBlocked := valuesQueryItem(t, 27, item.KindMovie, "za inherited blocked", nil)
	inheritedBlocked.Values.Tags = []string{"featured prime", "blocked prime"}
	inheritedSeries := valuesQueryItem(t, 29, item.KindSeries, "zb inherited series", nil)
	inheritedSeries.Values.Tags = []string{"featured prime"}
	inheritedSeriesChild := valuesQueryItem(t, 30, item.KindEpisode, "zc inherited series child", &inheritedSeries.ID)
	inheritedAncestor := valuesQueryItem(t, 31, item.KindFolder, "zd inherited ancestor", nil)
	inheritedAncestor.Values.Tags = []string{"featured prime", "blocked prime"}
	inheritedAncestorChild := valuesQueryItem(t, 32, item.KindMovie, "ze inherited ancestor child", &inheritedAncestor.ID)
	inheritedTop := valuesQueryItem(t, 33, item.KindFolder, "zf inherited top", nil)
	inheritedTop.Values.Tags = []string{"featured prime"}
	inheritedTopChild := valuesQueryItem(t, 34, item.KindMovie, "zg inherited top child", &inheritedTop.ID)
	boxset := valuesQueryItem(t, 28, item.KindBoxSet, "zzz boxset", nil)
	boxsetFolderID := valuesQueryUUID(t, 201)
	unusedFolderID := valuesQueryUUID(t, 202)

	for _, it := range []item.Item{
		genre, studio, artist, album,
		genreOnly, studioOnly, genreStudio,
		artistOnly, albumArtistOnly, bothArtistRelations, noArtistRelation, albumTrack,
		trailer, imageDouble, imageSingle, externalSeries, endedSeries,
		linkedAncestor, linkedChild, linkedParent,
		ratingRoot, ratingSeries, ratingChild, ratingLinkedParent, noRating,
		presentationKey, presentationParent, presentationChild, seriesKey,
		inheritedAllowed, inheritedBlocked, inheritedSeries, inheritedSeriesChild, inheritedAncestor, inheritedAncestorChild, inheritedTop, inheritedTopChild, boxset,
	} {
		if err := items.Upsert(t.Context(), it); err != nil {
			t.Fatalf("upserting relation-value fixture item %s: %v", it.ID, err)
		}
	}
	if err := items.ReplaceBoxSetLibraryFolders(t.Context(), boxset.ID, []dto.UUID{boxsetFolderID}); err != nil {
		t.Fatalf("adding boxset folder relation: %v", err)
	}
	return valuesQueryFixture{
		items: items, genre: genre, studio: studio, artist: artist, album: album,
		genreOnly: genreOnly, studioOnly: studioOnly, genreStudio: genreStudio,
		artistOnly: artistOnly, albumArtistOnly: albumArtistOnly, bothArtistRelations: bothArtistRelations, noArtistRelation: noArtistRelation,
		albumTrack: albumTrack, trailer: trailer, imageDouble: imageDouble, imageSingle: imageSingle, externalSeries: externalSeries, endedSeries: endedSeries,
		linkedAncestor: linkedAncestor, linkedParent: linkedParent,
		ratingRoot: ratingRoot, ratingSeries: ratingSeries, ratingChild: ratingChild, ratingLinkedParent: ratingLinkedParent, noRating: noRating,
		presentationKey: presentationKey, presentationChild: presentationChild, seriesKey: seriesKey,
		inheritedAllowed: inheritedAllowed, inheritedBlocked: inheritedBlocked,
		inheritedSeries: inheritedSeries, inheritedSeriesChild: inheritedSeriesChild, inheritedAncestor: inheritedAncestor, inheritedAncestorChild: inheritedAncestorChild, inheritedTop: inheritedTop, inheritedTopChild: inheritedTopChild,
		boxset: boxset, boxsetFolderID: boxsetFolderID, unusedFolderID: unusedFolderID,
	}
}

func (f valuesQueryFixture) artistCandidates() []dto.UUID {
	return []dto.UUID{f.artistOnly.ID, f.albumArtistOnly.ID, f.bothArtistRelations.ID, f.noArtistRelation.ID}
}

func (f valuesQueryFixture) ratingCandidates() []dto.UUID {
	return []dto.UUID{f.ratingRoot.ID, f.ratingSeries.ID, f.ratingChild.ID, f.ratingLinkedParent.ID}
}

func (f valuesQueryFixture) ratingMatches() []dto.UUID {
	return []dto.UUID{f.ratingRoot.ID, f.ratingSeries.ID, f.ratingChild.ID, f.ratingLinkedParent.ID}
}

func (f valuesQueryFixture) inheritedCandidates() []dto.UUID {
	return []dto.UUID{f.inheritedAllowed.ID, f.inheritedBlocked.ID, f.inheritedSeriesChild.ID, f.inheritedAncestorChild.ID, f.inheritedTopChild.ID}
}

func (f valuesQueryFixture) inheritedMatches() []dto.UUID {
	return []dto.UUID{f.inheritedAllowed.ID, f.inheritedBlocked.ID, f.inheritedSeriesChild.ID, f.inheritedAncestorChild.ID, f.inheritedTopChild.ID}
}

func valuesQueryItem(t *testing.T, value int, kind item.Kind, sortName string, parentID *dto.UUID) item.Item {
	t.Helper()
	name := fmt.Sprintf("item %d", value)
	return item.Item{ID: valuesQueryUUID(t, value), Kind: kind, Name: &name, SortName: &sortName, ParentID: parentID}
}

func valuesQueryUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing fixture UUID %d: %v", value, err)
	}
	return id
}

func valuesQueryIDs(items []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(items))
	for index, it := range items {
		ids[index] = it.ID
	}
	return ids
}

func valuesQueryString(value string) *string { return &value }
func valuesQueryInt(value int) *int          { return &value }
