// SPDX-License-Identifier: GPL-2.0-only

package remotetest_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
)

func TestCassetteMatchesIgnoringApiKeyAndQueryOrder(t *testing.T) {
	cassette := remotetest.NewCassette(remotetest.Interaction{
		Method: http.MethodGet,
		URL:    "https://api.themoviedb.org/3/movie/603?api_key=RECORDING_KEY&language=en-US&append_to_response=credits",
		Status: http.StatusOK,
		Header: map[string][]string{"Content-Type": {"application/json"}},
		Body:   json.RawMessage(`{"id":603,"title":"The Matrix"}`),
	})

	// A different key and a different query-parameter order still match.
	request, err := http.NewRequest(http.MethodGet, "https://api.themoviedb.org/3/movie/603?append_to_response=credits&api_key=LIVE_KEY&language=en-US", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := cassette.Do(request)
	if err != nil {
		t.Fatalf("cassette replay: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("replayed status = %d, want 200", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), "The Matrix") {
		t.Fatalf("replayed body = %q, want the recorded movie", body)
	}
}

func TestCassetteFailsLoudlyOnMissingInteraction(t *testing.T) {
	cassette := remotetest.NewCassette()
	request, _ := http.NewRequest(http.MethodGet, "https://api.themoviedb.org/3/movie/1?api_key=SECRET", nil)
	_, err := cassette.Do(request)
	if err == nil {
		t.Fatal("cassette replay of unknown request returned nil error, want a loud miss")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("cassette error leaked the api key: %v", err)
	}
}
