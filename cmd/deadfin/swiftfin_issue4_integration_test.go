// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestApplicationSwiftfinItemDetailAndSeriesFallbackParity reproduces the
// source-derived Swiftfin item flow. Movie playback starts with a no-Fields
// full-item GET; an unwatched series falls through NextUp and Resume to a
// recursive first-episode query whose SortOrder has no matching SortBy.
func TestApplicationSwiftfinItemDetailAndSeriesFallbackParity(t *testing.T) {
	fixtures := applicationProbeFixtureRoot(t)
	programData := filepath.Join(t.TempDir(), "program-data")
	app := newCompositionApplication(t, programData)
	for _, libraryFixture := range []struct {
		name, collection, path string
	}{
		{name: "Issue 4 Movies", collection: "movies", path: filepath.Join(fixtures, "movies")},
		{name: "Issue 4 Television", collection: "tvshows", path: filepath.Join(fixtures, "tv")},
	} {
		if _, err := app.topology.AddVirtualFolder(t.Context(), libraryFixture.name, libraryFixture.collection, library.LibraryOptions{
			PathInfos: []library.MediaPathInfo{{Path: libraryFixture.path}},
		}); err != nil {
			t.Fatalf("add %s library: %v", libraryFixture.name, err)
		}
	}
	configureComposedAdministrator(t, app.Router())
	token := loginComposedAdministrator(t, app.Router())
	user, err := app.users.FirstUser(t.Context())
	if err != nil {
		t.Fatalf("load issue #4 user: %v", err)
	}
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan fixture tree: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("scan fixture tree errors = %#v", result.Errors)
	}

	items := store.NewItemStore(app.stores)
	movie := issue4ItemAtPath(t, items, filepath.Join(fixtures, "movies", "Standalone Movie (2023)", "Standalone Movie (2023).mkv"), item.KindMovie)
	series := issue4ItemAtPath(t, items, filepath.Join(fixtures, "tv", "Synthetic Show (2024)"), item.KindSeries)
	season := issue4ItemAtPath(t, items, filepath.Join(fixtures, "tv", "Synthetic Show (2024)", "Season 01"), item.KindSeason)

	movieDetail := authenticatedCompositionRequest(
		app.Router(),
		http.MethodGet,
		"/Items/"+movie.ID.Dashed()+"?UserId="+user.ID.Dashed(),
		nil,
		token,
	)
	issue4AssertStatus(t, "Swiftfin movie full item", movieDetail.Code, movieDetail.Body.Bytes(), http.StatusOK)
	detail := issue4DecodeItem(t, movieDetail.Body.Bytes())
	issue4AssertPlayableVideo(t, "Swiftfin movie full item", detail)
	if detail.Path == "" || detail.Width != 320 || detail.Height != 180 {
		t.Fatalf("Swiftfin movie full item Path/dimensions = %q/%dx%d, want a physical 320x180 file", detail.Path, detail.Width, detail.Height)
	}

	playbackBody, err := json.Marshal(map[string]any{
		"DeviceProfile": map[string]any{
			"DirectPlayProfiles": []map[string]any{{
				"Container":  "mkv",
				"Type":       "Video",
				"VideoCodec": "mpeg4",
				"AudioCodec": "aac",
			}},
			"TranscodingProfiles": []any{},
		},
		"UserId":               user.ID.String(),
		"MediaSourceId":        detail.MediaSources[0].ID,
		"IsAutoOpenLiveStream": true,
	})
	if err != nil {
		t.Fatalf("encode Swiftfin PlaybackInfo request: %v", err)
	}
	playback := authenticatedCompositionRequest(app.Router(), http.MethodPost, "/Items/"+movie.ID.Dashed()+"/PlaybackInfo", playbackBody, token)
	issue4AssertStatus(t, "Swiftfin movie PlaybackInfo", playback.Code, playback.Body.Bytes(), http.StatusOK)
	var playbackResponse struct {
		MediaSources  []issue4WireMediaSource `json:"MediaSources"`
		PlaySessionID string                  `json:"PlaySessionId"`
	}
	if err := json.Unmarshal(playback.Body.Bytes(), &playbackResponse); err != nil {
		t.Fatalf("decode Swiftfin movie PlaybackInfo: %v; body=%s", err, playback.Body.String())
	}
	if len(playbackResponse.MediaSources) != 1 || playbackResponse.PlaySessionID == "" {
		t.Fatalf("Swiftfin movie PlaybackInfo sources/session = %d/%q, want one source and a session", len(playbackResponse.MediaSources), playbackResponse.PlaySessionID)
	}

	for _, request := range []struct {
		name string
		path string
	}{
		{name: "series full item", path: "/Items/" + series.ID.Dashed() + "?UserId=" + user.ID.Dashed()},
		{name: "series next up", path: "/Shows/NextUp?Fields=MediaSources,ParentId&SeriesId=" + series.ID.Dashed()},
		{name: "series resume", path: "/UserItems/Resume?Fields=MediaSources,ParentId&Limit=1&ParentId=" + series.ID.Dashed()},
	} {
		response := authenticatedCompositionRequest(app.Router(), http.MethodGet, request.path, nil, token)
		issue4AssertStatus(t, request.name, response.Code, response.Body.Bytes(), http.StatusOK)
	}

	firstAvailable := authenticatedCompositionRequest(
		app.Router(),
		http.MethodGet,
		"/Items?Fields=MediaSources,ParentId&IncludeItemTypes=Episode&IsMissing=false&Recursive=true&Limit=1&ParentId="+series.ID.Dashed()+"&SortOrder=Ascending",
		nil,
		token,
	)
	issue4AssertStatus(t, "series first available", firstAvailable.Code, firstAvailable.Body.Bytes(), http.StatusOK)
	var firstPage struct {
		Items []issue4WireItem `json:"Items"`
	}
	if err := json.Unmarshal(firstAvailable.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode series first available: %v; body=%s", err, firstAvailable.Body.String())
	}
	if len(firstPage.Items) != 1 || firstPage.Items[0].Type != "Episode" {
		t.Fatalf("series first available items = %#v, want one Episode", firstPage.Items)
	}
	issue4AssertMediaSource(t, "series first available episode", firstPage.Items[0])

	seasons := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Shows/"+series.ID.Dashed()+"/Seasons?UserId="+user.ID.Dashed()+"&IsMissing=false", nil, token)
	issue4AssertStatus(t, "series seasons", seasons.Code, seasons.Body.Bytes(), http.StatusOK)
	issue4AssertPageCount(t, "series seasons", seasons.Body.Bytes(), 3)

	episodes := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Shows/"+season.ID.Dashed()+"/Episodes?UserId="+user.ID.Dashed()+"&SeasonId="+season.ID.Dashed()+"&EnableUserData=true&Fields=MediaSources,ParentId,Overview&IsMissing=false", nil, token)
	issue4AssertStatus(t, "season episodes", episodes.Code, episodes.Body.Bytes(), http.StatusOK)
	issue4AssertPageCount(t, "season episodes", episodes.Body.Bytes(), 3)
}

type issue4WireItem struct {
	Type         string                  `json:"Type"`
	Path         string                  `json:"Path"`
	Width        int                     `json:"Width"`
	Height       int                     `json:"Height"`
	MediaSources []issue4WireMediaSource `json:"MediaSources"`
	MediaStreams []issue4WireMediaStream `json:"MediaStreams"`
}

type issue4WireMediaSource struct {
	ID           string                  `json:"Id"`
	Path         string                  `json:"Path"`
	Protocol     string                  `json:"Protocol"`
	Type         string                  `json:"Type"`
	MediaStreams []issue4WireMediaStream `json:"MediaStreams"`
}

type issue4WireMediaStream struct {
	Type  string `json:"Type"`
	Codec string `json:"Codec"`
}

func issue4DecodeItem(t *testing.T, body []byte) issue4WireItem {
	t.Helper()
	var result issue4WireItem
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode Swiftfin full item: %v; body=%s", err, body)
	}
	return result
}

func issue4AssertPlayableVideo(t *testing.T, name string, value issue4WireItem) {
	t.Helper()
	if len(value.MediaSources) != 1 || len(value.MediaStreams) != 2 {
		t.Fatalf("%s sources/top-level streams = %d/%d, want 1/2", name, len(value.MediaSources), len(value.MediaStreams))
	}
	issue4AssertMediaSource(t, name, value)
}

func issue4AssertMediaSource(t *testing.T, name string, value issue4WireItem) {
	t.Helper()
	if len(value.MediaSources) != 1 {
		t.Fatalf("%s sources = %d, want 1", name, len(value.MediaSources))
	}
	source := value.MediaSources[0]
	if source.ID == "" || source.Path == "" || source.Protocol != "File" || source.Type != "Default" {
		t.Fatalf("%s source = %#v, want identified local default file", name, source)
	}
	if len(source.MediaStreams) != 2 ||
		source.MediaStreams[0].Type != "Video" || source.MediaStreams[0].Codec != "mpeg4" ||
		source.MediaStreams[1].Type != "Audio" || source.MediaStreams[1].Codec != "aac" {
		t.Fatalf("%s source streams = %#v, want mpeg4 video plus aac audio", name, source.MediaStreams)
	}
}

func issue4AssertStatus(t *testing.T, name string, got int, body []byte, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s status = %d, want %d; body=%s", name, got, want, body)
	}
}

func issue4AssertPageCount(t *testing.T, name string, body []byte, want int) {
	t.Helper()
	var result struct {
		Items []json.RawMessage `json:"Items"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode %s: %v; body=%s", name, err, body)
	}
	if len(result.Items) != want {
		t.Fatalf("%s item count = %d, want %d; body=%s", name, len(result.Items), want, body)
	}
}

func issue4ItemAtPath(t *testing.T, items *store.ItemStore, path string, kind item.Kind) item.Item {
	t.Helper()
	result, err := items.Query(t.Context(), store.ItemQuery{Path: path, IncludeItemTypes: []item.Kind{kind}})
	if err != nil {
		t.Fatalf("query %s at %q: %v", kind, path, err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("query %s at %q returned %d items", kind, path, len(result.Items))
	}
	return result.Items[0]
}
