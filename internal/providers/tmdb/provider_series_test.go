// SPDX-License-Identifier: GPL-2.0-only

package tmdb_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/providers/remote"
	"github.com/deadfin-org/deadfin/internal/providers/tmdb"
)

// TestProviderMapsSeriesMetadataFromCassette exercises the ported series path —
// name+year search, series fetch, and field mapping — against a recorded TMDb
// cassette (no live API).
func TestProviderMapsSeriesMetadataFromCassette(t *testing.T) {
	provider := newCassetteProvider(t, "testdata/breaking-bad.json")
	target := item.Item{Kind: item.KindSeries, Name: stringPtr("Breaking Bad"), ProductionYear: intPtr(2008)}

	handled, err := provider.Refresh(t.Context(), movieRefreshContext(), &target)
	if err != nil {
		t.Fatalf("refresh series: %v", err)
	}
	if !handled {
		t.Fatal("provider did not claim the series despite a matching cassette")
	}

	if target.Name == nil || *target.Name != "Breaking Bad" {
		t.Errorf("Name = %v, want Breaking Bad", target.Name)
	}
	if target.Overview == nil || *target.Overview != "A high school chemistry teacher turned meth producer." {
		t.Errorf("Overview = %v", target.Overview)
	}
	if target.CommunityRating == nil || *target.CommunityRating != 8.9 {
		t.Errorf("CommunityRating = %v, want 8.9", target.CommunityRating)
	}
	if target.OfficialRating == nil || *target.OfficialRating != "TV-MA" {
		t.Errorf("OfficialRating = %v, want TV-MA", target.OfficialRating)
	}
	if target.SeriesStatus == nil || *target.SeriesStatus != item.SeriesStatusEnded {
		t.Errorf("SeriesStatus = %v, want Ended", target.SeriesStatus)
	}
	if target.PremiereDate == nil || target.PremiereDate.Format("2006-01-02") != "2008-01-20" {
		t.Errorf("PremiereDate = %v, want 2008-01-20", target.PremiereDate)
	}
	if target.EndDate == nil || target.EndDate.Format("2006-01-02") != "2013-09-29" {
		t.Errorf("EndDate = %v, want 2013-09-29", target.EndDate)
	}
	// EpisodeRunTime[0] = 45 minutes -> 45 * 60 * 10^7 ticks.
	if target.RunTimeTicks == nil || *target.RunTimeTicks != 45*60*10_000_000 {
		t.Errorf("RunTimeTicks = %v, want 45 minutes in ticks", target.RunTimeTicks)
	}
	if !reflect.DeepEqual(target.Values.Genres, []string{"Drama", "Crime"}) {
		t.Errorf("Genres = %v", target.Values.Genres)
	}
	// Networks then production companies.
	if !reflect.DeepEqual(target.Values.Studios, []string{"AMC", "Sony Pictures Television"}) {
		t.Errorf("Studios = %v", target.Values.Studios)
	}
	if !reflect.DeepEqual(target.Values.Tags, []string{"new mexico", "drug dealer"}) {
		t.Errorf("Tags = %v", target.Values.Tags)
	}

	if id, ok := target.ProviderIDs.Get("Tmdb"); !ok || id != "1396" {
		t.Errorf("Tmdb id = %q, want 1396", id)
	}
	if id, ok := target.ProviderIDs.Get("Imdb"); !ok || id != "tt0903747" {
		t.Errorf("Imdb id = %q, want tt0903747", id)
	}
	if id, ok := target.ProviderIDs.Get("Tvdb"); !ok || id != "81189" {
		t.Errorf("Tvdb id = %q, want 81189", id)
	}

	// Two cast, one writer, one creator (Vince Gilligan appears as both writer
	// crew and creator — the mapping keeps both, matching upstream GetPersons).
	var creators, writers, actors int
	for _, person := range target.People {
		switch person.Type {
		case item.PersonKindActor:
			actors++
		case item.PersonKindWriter:
			writers++
		case item.PersonKindCreator:
			creators++
		}
	}
	if actors != 2 || writers != 1 || creators != 1 {
		t.Errorf("people breakdown actors=%d writers=%d creators=%d, want 2/1/1", actors, writers, creators)
	}

	// Each credit carries a durable PersonID derived from its name through
	// library.PersonEntity, matching the identity the scanner and the identify
	// Apply path use. A credit with a nil PersonID would not persist through the
	// store.
	for _, credit := range target.People {
		if credit.PersonID.IsNil() {
			t.Errorf("credit %q has nil PersonID, want the durable identity", credit.Name)
		}
		if want := library.PersonEntity(credit.Name).ID; credit.PersonID != want {
			t.Errorf("credit %q PersonID = %s, want %s", credit.Name, credit.PersonID, want)
		}
	}

	// Vince Gilligan appears as both a writer crew member and a creator. The
	// two credits share one durable PersonID because the identity derives from
	// the name, but they retain different types so the store keeps both rows.
	var writer, creator *item.PersonCredit
	for index := range target.People {
		switch target.People[index].Type {
		case item.PersonKindWriter:
			writer = &target.People[index]
		case item.PersonKindCreator:
			creator = &target.People[index]
		}
	}
	if writer == nil || creator == nil {
		t.Fatalf("missing Vince Gilligan credits: writer=%v creator=%v", writer, creator)
	}
	// Both credits name Vince Gilligan. The identity derives from the name,
	// so the Name assertion precedes the shared-PersonID check.
	if writer.Name != "Vince Gilligan" {
		t.Errorf("writer Name = %q, want Vince Gilligan", writer.Name)
	}
	if creator.Name != "Vince Gilligan" {
		t.Errorf("creator Name = %q, want Vince Gilligan", creator.Name)
	}
	if writer.PersonID != creator.PersonID {
		t.Errorf("Vince Gilligan writer PersonID = %s, creator PersonID = %s, want one shared identity", writer.PersonID, creator.PersonID)
	}
	if writer.Type == creator.Type {
		t.Errorf("Vince Gilligan writer type = creator type = %s, want different types", writer.Type)
	}

	assertImage(t, target.ImageInfos, item.ImageTypePrimary, "https://image.tmdb.org/t/p/original/bb-best-poster.jpg")
	assertImage(t, target.ImageInfos, item.ImageTypeBackdrop, "https://image.tmdb.org/t/p/original/bb-backdrop.jpg")
}

// TestProviderSeriesImageDimensions proves a series refresh carries the TMDb
// image collection width and height into each attached ImageInfo: Primary,
// Backdrop, the text backdrop mapped to Thumb, and Logo.
func TestProviderSeriesImageDimensions(t *testing.T) {
	provider := newCassetteProvider(t, "testdata/breaking-bad.json")
	target := item.Item{Kind: item.KindSeries, Name: stringPtr("Breaking Bad"), ProductionYear: intPtr(2008)}

	handled, err := provider.Refresh(t.Context(), movieRefreshContext(), &target)
	if err != nil {
		t.Fatalf("refresh series: %v", err)
	}
	if !handled {
		t.Fatal("provider did not claim the series despite a matching cassette")
	}

	assertImageDimensions(t, target.ImageInfos, item.ImageTypePrimary, "https://image.tmdb.org/t/p/original/bb-best-poster.jpg", 500, 750)
	assertImageDimensions(t, target.ImageInfos, item.ImageTypeBackdrop, "https://image.tmdb.org/t/p/original/bb-backdrop.jpg", 1280, 720)
	assertImageDimensions(t, target.ImageInfos, item.ImageTypeThumb, "https://image.tmdb.org/t/p/original/bb-text-backdrop.jpg", 1920, 1080)
	assertImageDimensions(t, target.ImageInfos, item.ImageTypeLogo, "https://image.tmdb.org/t/p/original/bb-logo.png", 1000, 400)
}

// TestProviderSeriesImageDimensionsPosterFallback proves that a series with no
// usable image collection attaches poster_path as Primary with unknown (zero)
// dimensions.
func TestProviderSeriesImageDimensionsPosterFallback(t *testing.T) {
	provider := newCassetteProvider(t, "testdata/series-poster-fallback.json")
	target := item.Item{Kind: item.KindSeries, Name: stringPtr("Breaking Bad")}
	target.ProviderIDs.Set("Tmdb", "1396")

	handled, err := provider.Refresh(t.Context(), movieRefreshContext(), &target)
	if err != nil {
		t.Fatalf("refresh series: %v", err)
	}
	if !handled {
		t.Fatal("provider did not claim the series despite a matching cassette")
	}

	if len(target.ImageInfos) != 1 {
		t.Fatalf("ImageInfos = %#v, want only the poster_path Primary", target.ImageInfos)
	}
	assertImageDimensions(t, target.ImageInfos, item.ImageTypePrimary, "https://image.tmdb.org/t/p/original/bb-poster.jpg", 0, 0)
}

type tmdbDoerFunc func(*http.Request) (*http.Response, error)

func (fn tmdbDoerFunc) Do(request *http.Request) (*http.Response, error) { return fn(request) }

func TestProviderLogsSeriesFetchOutcome(t *testing.T) {
	const apiKey = "tmdb-key-opaque-7c2e"

	for _, testCase := range []struct {
		name         string
		status       int
		body         string
		wantHandled  bool
		wantError    bool
		wantFound    bool
		wantErrorLog bool
	}{
		{
			name:        "found",
			status:      http.StatusOK,
			body:        `{"id":42,"name":"Example Series"}`,
			wantHandled: true,
			wantFound:   true,
		},
		{
			name:         "upstream failure",
			status:       http.StatusServiceUnavailable,
			body:         `{"status_message":"service unavailable"}`,
			wantError:    true,
			wantErrorLog: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
			provider := tmdb.NewProvider(tmdb.Options{
				HTTP: remote.NewClient(remote.Config{
					Doer: tmdbDoerFunc(func(*http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: testCase.status,
							Header:     http.Header{"Content-Type": {"application/json"}},
							Body:       io.NopCloser(strings.NewReader(testCase.body)),
						}, nil
					}),
					Logger: logger,
				}),
				Config: tmdb.Config{APIKey: apiKey},
				Logger: logger,
			})
			target := item.Item{Kind: item.KindSeries}
			target.ProviderIDs.Set("Tmdb", "42")

			handled, err := provider.Refresh(t.Context(), movieRefreshContext(), &target)
			if handled != testCase.wantHandled {
				t.Errorf("Refresh handled = %t, want %t", handled, testCase.wantHandled)
			}
			if (err != nil) != testCase.wantError {
				t.Errorf("Refresh error = %v, want error %t", err, testCase.wantError)
			}
			if err != nil && strings.Contains(err.Error(), apiKey) {
				t.Errorf("Refresh error leaked API key: %v", err)
			}

			records := decodeSlogRecords(t, output.String())
			if !hasSeriesLog(records, testCase.wantFound, testCase.wantErrorLog) {
				t.Errorf("series fetch logs = %s, want op=series tmdbId=42 found=%t error-present=%t", output.String(), testCase.wantFound, testCase.wantErrorLog)
			}
			if strings.Contains(output.String(), apiKey) {
				t.Errorf("structured logs leaked API key: %s", output.String())
			}
		})
	}
}

func decodeSlogRecords(t *testing.T, output string) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(output))
	var records []map[string]any
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode structured log: %v", err)
		}
		records = append(records, record)
	}
	return records
}

func hasSeriesLog(records []map[string]any, found, wantError bool) bool {
	for _, record := range records {
		if record["op"] != "series" || record["tmdbId"] != float64(42) {
			continue
		}
		if foundValue, ok := record["found"].(bool); !wantError && ok && foundValue == found {
			return true
		}
		if _, ok := record["error"]; wantError && ok {
			return true
		}
	}
	return false
}
