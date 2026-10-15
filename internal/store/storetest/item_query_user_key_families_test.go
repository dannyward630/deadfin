// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestItemStoreQuerySQLitePlayedFollowsEveryCanonicalUserDataKey defends the
// persistence identity shared by equivalent items. Each user_data row belongs
// to a sibling item, so Query must resolve the candidate through its complete
// public item.UserDataKeys family rather than its raw UUID alone.
func TestItemStoreQuerySQLitePlayedFollowsEveryCanonicalUserDataKey(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("opening SQLite user-key fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("closing SQLite user-key fixture: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	userID := userKeyUUID(t, 100)
	trueValue := true

	series := userKeyItem(t, 1, item.KindSeries, "key series")
	series.ProviderIDs.Set("Custom", "series-custom")
	if err := items.Upsert(t.Context(), series); err != nil {
		t.Fatalf("upserting series context: %v", err)
	}
	indexOne, indexTwo := 1, 2
	extraType := item.ExtraTypeTrailer
	runtime := int64(1234)
	episodeSeason := userKeyItem(t, 2, item.KindSeason, "episode season")
	episodeSeason.ParentID = &series.ID
	episodeSeason.IndexNumber = &indexOne
	if err := items.Upsert(t.Context(), episodeSeason); err != nil {
		t.Fatalf("upserting episode season context: %v", err)
	}
	negativeOne := -1
	negativeEpisodeSeason := userKeyItem(t, 3, item.KindSeason, "negative episode season")
	negativeEpisodeSeason.ParentID = &series.ID
	negativeEpisodeSeason.IndexNumber = &negativeOne
	if err := items.Upsert(t.Context(), negativeEpisodeSeason); err != nil {
		t.Fatalf("upserting negative episode season context: %v", err)
	}
	channelSeries := userKeyItem(t, 4, item.KindSeries, "channel series")
	channelSeries.SourceType = item.SourceTypeChannel
	channelSeries.ExternalID = userKeyString("channel-series")
	if err := items.Upsert(t.Context(), channelSeries); err != nil {
		t.Fatalf("upserting channel series context: %v", err)
	}
	channelEpisodeSeason := userKeyItem(t, 5, item.KindSeason, "channel episode season")
	channelEpisodeSeason.ParentID = &channelSeries.ID
	channelEpisodeSeason.IndexNumber = &indexOne
	if err := items.Upsert(t.Context(), channelEpisodeSeason); err != nil {
		t.Fatalf("upserting channel episode season context: %v", err)
	}

	channelSource := userKeyItem(t, 10, item.KindTvChannel, "channel source")
	channelSource.SourceType = item.SourceTypeChannel
	channelSource.ExternalID = userKeyString("external-channel")
	channelCandidate := userKeyItem(t, 11, item.KindTvChannel, "channel candidate")
	channelCandidate.SourceType = item.SourceTypeChannel
	channelCandidate.ExternalID = userKeyString("external-channel")

	customSeriesSource := userKeyItem(t, 12, item.KindSeries, "custom series source")
	customSeriesSource.ProviderIDs.Set("Custom", "shared-custom")
	customSeriesCandidate := userKeyItem(t, 13, item.KindSeries, "custom series candidate")
	customSeriesCandidate.ProviderIDs.Set("Custom", "shared-custom")

	tvdbSeriesSource := userKeyItem(t, 14, item.KindSeries, "tvdb series source")
	tvdbSeriesSource.ProviderIDs.Set("Tvdb", "shared-tvdb")
	tvdbSeriesCandidate := userKeyItem(t, 15, item.KindSeries, "tvdb series candidate")
	tvdbSeriesCandidate.ProviderIDs.Set("Tvdb", "shared-tvdb")

	episodeSource := userKeyItem(t, 16, item.KindEpisode, "episode source")
	episodeSource.ParentID = &episodeSeason.ID
	episodeSource.ParentIndexNumber = &indexOne
	episodeSource.IndexNumber = &indexTwo
	episodeCandidate := userKeyItem(t, 17, item.KindEpisode, "episode candidate")
	episodeCandidate.ParentID = &episodeSeason.ID
	episodeCandidate.ParentIndexNumber = &indexOne
	episodeCandidate.IndexNumber = &indexTwo

	seasonSource := userKeyItem(t, 18, item.KindSeason, "season source")
	seasonSource.ParentID = &series.ID
	seasonSource.IndexNumber = &indexOne
	seasonCandidate := userKeyItem(t, 19, item.KindSeason, "season candidate")
	seasonCandidate.ParentID = &series.ID
	seasonCandidate.IndexNumber = &indexOne
	seasonZeroSource := userKeyItem(t, 26, item.KindSeason, "season zero source")
	seasonZeroSource.ParentID = &series.ID
	seasonZeroCandidate := userKeyItem(t, 27, item.KindSeason, "season zero candidate")
	seasonZeroCandidate.ParentID = &series.ID
	negativeSeasonSource := userKeyItem(t, 30, item.KindSeason, "negative season source")
	negativeSeasonSource.ParentID = &series.ID
	negativeSeasonSource.IndexNumber = &negativeOne
	negativeSeasonCandidate := userKeyItem(t, 31, item.KindSeason, "negative season candidate")
	negativeSeasonCandidate.ParentID = &series.ID
	negativeSeasonCandidate.IndexNumber = &negativeOne
	negativeEpisodeSource := userKeyItem(t, 32, item.KindEpisode, "negative episode source")
	negativeEpisodeSource.ParentID = &negativeEpisodeSeason.ID
	negativeEpisodeSource.ParentIndexNumber = &negativeOne
	negativeEpisodeSource.IndexNumber = &negativeOne
	negativeEpisodeCandidate := userKeyItem(t, 33, item.KindEpisode, "negative episode candidate")
	negativeEpisodeCandidate.ParentID = &negativeEpisodeSeason.ID
	negativeEpisodeCandidate.ParentIndexNumber = &negativeOne
	negativeEpisodeCandidate.IndexNumber = &negativeOne
	channelSeasonSource := userKeyItem(t, 34, item.KindSeason, "channel season source")
	channelSeasonSource.ParentID = &channelSeries.ID
	channelSeasonSource.IndexNumber = &indexOne
	channelSeasonCandidate := userKeyItem(t, 35, item.KindSeason, "channel season candidate")
	channelSeasonCandidate.ParentID = &channelSeries.ID
	channelSeasonCandidate.IndexNumber = &indexOne
	channelEpisodeSource := userKeyItem(t, 36, item.KindEpisode, "channel episode source")
	channelEpisodeSource.ParentID = &channelEpisodeSeason.ID
	channelEpisodeSource.ParentIndexNumber = &indexOne
	channelEpisodeSource.IndexNumber = &indexTwo
	channelEpisodeCandidate := userKeyItem(t, 37, item.KindEpisode, "channel episode candidate")
	channelEpisodeCandidate.ParentID = &channelEpisodeSeason.ID
	channelEpisodeCandidate.ParentIndexNumber = &indexOne
	channelEpisodeCandidate.IndexNumber = &indexTwo

	extraSource := userKeyItem(t, 20, item.KindMovie, "extra source")
	extraSource.ExtraType = &extraType
	extraSource.RunTimeTicks = &runtime
	extraSource.ProviderIDs.Set("Tmdb", "extra-603")
	extraCandidate := userKeyItem(t, 21, item.KindMovie, "extra candidate")
	extraCandidate.ExtraType = &extraType
	extraCandidate.RunTimeTicks = &runtime
	extraCandidate.ProviderIDs.Set("Tmdb", "extra-603")

	audioSource := userKeyItem(t, 22, item.KindAudio, "track")
	audioSource.IndexNumber = &indexTwo
	audioSource.Values.AlbumArtists = []string{"Artist"}
	audioSource.Album = userKeyString("Album")
	audioCandidate := userKeyItem(t, 23, item.KindAudio, "track")
	audioCandidate.IndexNumber = &indexTwo
	audioCandidate.Values.AlbumArtists = []string{"Artist"}
	audioCandidate.Album = userKeyString("Album")

	personSource := userKeyItem(t, 24, item.KindPerson, "Béla Person")
	personCandidate := userKeyItem(t, 25, item.KindPerson, "Béla Person")
	generatedNameSource := userKeyItem(t, 28, item.KindPerson, "Łukasz Person")
	generatedNameCandidate := userKeyItem(t, 29, item.KindPerson, "Łukasz Person")

	cases := []struct {
		name      string
		source    item.Item
		candidate item.Item
		inputs    item.UserDataInputs
	}{
		{name: "channel external ID", source: channelSource, candidate: channelCandidate},
		{name: "series custom provider", source: customSeriesSource, candidate: customSeriesCandidate},
		{name: "series tvdb provider", source: tvdbSeriesSource, candidate: tvdbSeriesCandidate},
		{name: "episode series key suffix", source: episodeSource, candidate: episodeCandidate, inputs: item.UserDataInputs{Series: &series}},
		{name: "season series key suffix", source: seasonSource, candidate: seasonCandidate, inputs: item.UserDataInputs{Series: &series}},
		{name: "season nil index uses zero suffix", source: seasonZeroSource, candidate: seasonZeroCandidate, inputs: item.UserDataInputs{Series: &series}},
		{name: "negative season index preserves sign padding", source: negativeSeasonSource, candidate: negativeSeasonCandidate, inputs: item.UserDataInputs{Series: &series}},
		{name: "negative episode indices preserve sign padding", source: negativeEpisodeSource, candidate: negativeEpisodeCandidate, inputs: item.UserDataInputs{Series: &series}},
		{name: "channel series external season suffix", source: channelSeasonSource, candidate: channelSeasonCandidate, inputs: item.UserDataInputs{Series: &channelSeries}},
		{name: "channel series external episode suffix", source: channelEpisodeSource, candidate: channelEpisodeCandidate, inputs: item.UserDataInputs{Series: &channelSeries}},
		{name: "extra video type and runtime", source: extraSource, candidate: extraCandidate},
		{name: "audio music identity", source: audioSource, candidate: audioCandidate},
		{name: "name derived person identity", source: personSource, candidate: personCandidate},
		{name: "name derived generated diacritic mapping", source: generatedNameSource, candidate: generatedNameCandidate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, it := range []item.Item{tc.source, tc.candidate} {
				if err := items.Upsert(t.Context(), it); err != nil {
					t.Fatalf("upserting %s: %v", it.ID, err)
				}
			}
			sharedKey := userKeyShared(t, tc.source, tc.candidate, tc.inputs)
			if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: sharedKey, ItemID: tc.source.ID, Played: true}); err != nil {
				t.Fatalf("upserting played data for %q: %v", sharedKey, err)
			}
			result, err := items.Query(t.Context(), store.ItemQuery{ItemIDs: []dto.UUID{tc.candidate.ID}, UserID: &userID, IsPlayed: &trueValue, EnableTotalRecordCount: true})
			if err != nil {
				t.Fatalf("querying candidate: %v", err)
			}
			if got, want := userKeyIDs(result.Items), []dto.UUID{tc.candidate.ID}; !reflect.DeepEqual(got, want) {
				t.Fatalf("played candidate IDs = %v, want %v", got, want)
			}
			if result.TotalRecordCount != 1 {
				t.Fatalf("played candidate count = %d, want 1", result.TotalRecordCount)
			}
		})
	}
	t.Run("episode does not revive dropped series UUID key", func(t *testing.T) {
		parentIndex, episodeIndex := 3, 4
		source := userKeyItem(t, 38, item.KindEpisode, "dropped UUID source")
		source.ParentID = &episodeSeason.ID
		source.ParentIndexNumber = &parentIndex
		source.IndexNumber = &episodeIndex
		candidate := userKeyItem(t, 39, item.KindEpisode, "dropped UUID candidate")
		candidate.ParentID = &episodeSeason.ID
		candidate.ParentIndexNumber = &parentIndex
		candidate.IndexNumber = &episodeIndex
		for _, it := range []item.Item{source, candidate} {
			if err := items.Upsert(t.Context(), it); err != nil {
				t.Fatalf("upserting dropped-key fixture %s: %v", it.ID, err)
			}
		}
		seriesKeys := item.UserDataKeys(series, item.UserDataInputs{})
		droppedKey := seriesKeys[len(seriesKeys)-1] + "003004"
		for _, key := range item.UserDataKeys(candidate, item.UserDataInputs{Series: &series}) {
			if key == droppedKey {
				t.Fatalf("dropped series UUID key %q remained canonical", droppedKey)
			}
		}
		if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: droppedKey, ItemID: source.ID, Played: true}); err != nil {
			t.Fatalf("upserting dropped-key user data: %v", err)
		}
		result, err := items.Query(t.Context(), store.ItemQuery{ItemIDs: []dto.UUID{candidate.ID}, UserID: &userID, IsPlayed: &trueValue, EnableTotalRecordCount: true})
		if err != nil {
			t.Fatalf("querying dropped-key candidate: %v", err)
		}
		if got, want := userKeyIDs(result.Items), []dto.UUID{}; !reflect.DeepEqual(got, want) {
			t.Fatalf("dropped-key candidate IDs = %v, want %v", got, want)
		}
		if result.TotalRecordCount != 0 {
			t.Fatalf("dropped-key candidate count = %d, want 0", result.TotalRecordCount)
		}
	})
}

func userKeyItem(t *testing.T, value int, kind item.Kind, name string) item.Item {
	t.Helper()
	return item.Item{ID: userKeyUUID(t, value), Kind: kind, Name: userKeyString(name), SortName: userKeyString(name)}
}

func userKeyShared(t *testing.T, source, candidate item.Item, inputs item.UserDataInputs) string {
	t.Helper()
	sourceKeys := item.UserDataKeys(source, inputs)
	candidateKeys := item.UserDataKeys(candidate, inputs)
	for _, sourceKey := range sourceKeys {
		for _, candidateKey := range candidateKeys {
			if sourceKey == candidateKey {
				return sourceKey
			}
		}
	}
	t.Fatalf("source keys %q and candidate keys %q have no shared identity", sourceKeys, candidateKeys)
	return ""
}

func userKeyUUID(t *testing.T, value int) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(fmt.Sprintf("%032x", value))
	if err != nil {
		t.Fatalf("parsing user-key UUID %d: %v", value, err)
	}
	return id
}

func userKeyString(value string) *string { return &value }

func userKeyIDs(items []item.Item) []dto.UUID {
	ids := make([]dto.UUID, len(items))
	for index, it := range items {
		ids[index] = it.ID
	}
	return ids
}
