// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"
)

type replayRequest struct {
	Path  string
	Query url.Values
	Body  map[string]any
}

func closeRequestBody(t *testing.T, body io.Closer) {
	t.Helper()
	if err := body.Close(); err != nil {
		t.Errorf("close request body: %v", err)
	}
}

func TestHTTPHostRewritesDestinationItemIDsAcrossRequestLocations(t *testing.T) {
	const (
		canonicalID = "Movie:/media/film.mkv"
		sourceID    = "source-item-id"
		targetID    = "destination-item-id"
	)

	var replay replayRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Items":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Items":[{"Id":"destination-item-id","Type":"Movie","Path":"/media/film.mkv"}]}`))
		default:
			defer closeRequestBody(t, r.Body)
			if err := json.NewDecoder(r.Body).Decode(&replay.Body); err != nil {
				t.Errorf("decode replay body: %v", err)
			}
			replay.Path = r.URL.Path
			replay.Query = r.URL.Query()
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(server.Close)

	host, err := newHTTPHost(context.Background(), server.URL, AuthConfig{}, nil)
	if err != nil {
		t.Fatalf("create destination host: %v", err)
	}
	itemMap, err := host.BuildItemMap(context.Background())
	if err != nil {
		t.Fatalf("build destination item map: %v", err)
	}
	if got := itemMap[targetID]; got != canonicalID {
		t.Fatalf("item map must retain the destination response ID as its key: got %#v, want %q => %q", itemMap, targetID, canonicalID)
	}
	host.SetItemIDs(itemMap)
	host.setSourceIDs(map[string]string{sourceID: canonicalID})

	_, err = host.Request(context.Background(), Step{
		Name:   "rewrite item IDs",
		Method: http.MethodPost,
		Path:   "/items/" + sourceID + "?pathItemId=" + sourceID,
		Params: map[string]any{"queryItemId": sourceID},
		Body: map[string]any{
			"ItemId": sourceID,
			"Nested": map[string]any{"OwnerId": sourceID},
		},
	}, "")
	if err != nil {
		t.Fatalf("replay mapped request: %v", err)
	}

	if replay.Path != "/items/"+targetID {
		t.Fatalf("destination path ID = %q, want %q", replay.Path, "/items/"+targetID)
	}
	for _, name := range []string{"pathItemId", "queryItemId"} {
		if got := replay.Query.Get(name); got != targetID {
			t.Fatalf("destination query %s = %q, want destination ID %q", name, got, targetID)
		}
	}
	if got := replay.Body["ItemId"]; got != targetID {
		t.Fatalf("destination JSON item ID = %#v, want %q", got, targetID)
	}
	nested, ok := replay.Body["Nested"].(map[string]any)
	if !ok {
		t.Fatalf("destination JSON nested object = %#v, want object", replay.Body["Nested"])
	}
	if got := nested["OwnerId"]; got != targetID {
		t.Fatalf("destination nested JSON ID = %#v, want %q", got, targetID)
	}
}

func TestHTTPHostSubstitutesBootstrappedUserIDPerHostAcrossRequestLocations(t *testing.T) {
	const placeholder = "<user-id>"

	for _, tc := range []struct {
		name   string
		userID string
	}{
		{name: "left host", userID: "left-user-id"},
		{name: "right host", userID: "right-user-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var replay replayRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Users/AuthenticateByName" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"AccessToken":"token","User":{"Id":"` + tc.userID + `"}}`))
					return
				}
				defer closeRequestBody(t, r.Body)
				if err := json.NewDecoder(r.Body).Decode(&replay.Body); err != nil {
					t.Errorf("decode replay body: %v", err)
				}
				replay.Path = r.URL.Path
				replay.Query = r.URL.Query()
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)

			host, err := newHTTPHost(context.Background(), server.URL, AuthConfig{Role: "fixture", Username: "fixture-user", Password: "fixture-password"}, nil)
			if err != nil {
				t.Fatalf("bootstrap host: %v", err)
			}
			_, err = host.Request(context.Background(), Step{
				Name:     "substitute current user",
				Method:   http.MethodPost,
				Path:     "/Users/" + placeholder + "/Items",
				Params:   map[string]any{"userId": placeholder},
				Body:     map[string]any{"UserId": placeholder, "Nested": map[string]any{"OwnerId": placeholder}},
				AuthRole: "fixture",
			}, "")
			if err != nil {
				t.Fatalf("replay authenticated request: %v", err)
			}

			if got, want := replay.Path, "/Users/"+tc.userID+"/Items"; got != want {
				t.Fatalf("replay path = %q, want host-specific user path %q", got, want)
			}
			if got := replay.Query.Get("userId"); got != tc.userID {
				t.Fatalf("replay query userId = %q, want host-specific user ID %q", got, tc.userID)
			}
			if got := replay.Body["UserId"]; got != tc.userID {
				t.Fatalf("replay JSON user ID = %#v, want host-specific user ID %q", got, tc.userID)
			}
			nested, ok := replay.Body["Nested"].(map[string]any)
			if !ok {
				t.Fatalf("replay JSON nested object = %#v, want object", replay.Body["Nested"])
			}
			if got := nested["OwnerId"]; got != tc.userID {
				t.Fatalf("replay nested JSON user ID = %#v, want host-specific user ID %q", got, tc.userID)
			}
		})
	}
}

func TestHTTPHostRejectsUserPlaceholderWithoutBootstrappedUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	host, err := newHTTPHost(context.Background(), server.URL, AuthConfig{}, nil)
	if err != nil {
		t.Fatalf("create unauthenticated host: %v", err)
	}
	_, err = host.Request(context.Background(), Step{
		Name:   "requires bootstrapped user",
		Method: http.MethodGet,
		Path:   "/Users/<user-id>/Items",
	}, "")
	if err == nil {
		t.Fatal("unauthenticated replay must reject a <user-id> placeholder instead of sending it to the host")
	}
}

func TestHTTPHostNilClientHasBoundedTimeoutAndCancelsStallingRequest(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	host, err := newHTTPHost(context.Background(), server.URL, AuthConfig{}, nil)
	if err != nil {
		t.Fatalf("create host with nil client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requestDone := make(chan error, 1)
	go func() {
		_, requestErr := host.Request(ctx, Step{Name: "stalled", Method: http.MethodGet, Path: "/stall"}, "")
		requestDone <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stalling server did not receive the replay request")
	}
	cancel()
	select {
	case requestErr := <-requestDone:
		if requestErr == nil {
			t.Fatal("stalling replay must surface cancellation rather than return a response")
		}
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not interrupt the stalling replay request")
	}
	if host.client.Timeout <= 0 || host.client.Timeout > 30*time.Second {
		t.Fatalf("nil-client host timeout = %s, want a finite timeout no greater than 30s", host.client.Timeout)
	}
}

func TestHTTPHostBuildItemMapPaginatesPastTenThousandItems(t *testing.T) {
	const (
		pageSize = 10_000
		laterID  = "later-item"
	)

	var startIndexes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Items" {
			http.NotFound(w, r)
			return
		}
		startIndex := r.URL.Query().Get("StartIndex")
		startIndexes = append(startIndexes, startIndex)
		w.Header().Set("Content-Type", "application/json")
		switch startIndex {
		case "", "0":
			_, _ = io.WriteString(w, `{"Items":[`)
			for index := range pageSize {
				if index > 0 {
					_, _ = io.WriteString(w, ",")
				}
				_, _ = fmt.Fprintf(w, `{"Id":"first-page-%d","Type":"Movie","Path":"/library/first-page-%d.mkv"}`, index, index)
			}
			_, _ = io.WriteString(w, `],"TotalRecordCount":10001,"StartIndex":0}`)
		case "10000":
			_, _ = io.WriteString(w, `{"Items":[{"Id":"later-item","Type":"Movie","Path":"/library/later.mkv"}],"TotalRecordCount":10001,"StartIndex":10000}`)
		default:
			http.Error(w, "unexpected StartIndex", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	host, err := newHTTPHost(context.Background(), server.URL, AuthConfig{}, nil)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	itemMap, err := host.BuildItemMap(context.Background())
	if err != nil {
		t.Fatalf("build paginated item map: %v", err)
	}
	if got, want := itemMap["first-page-0"], "Movie:/library/first-page-0.mkv"; got != want {
		t.Fatalf("item map first page entry = %q, want %q", got, want)
	}
	if got, want := itemMap[laterID], "Movie:/library/later.mkv"; got != want {
		t.Fatalf("item map later page entry = %q, want %q", got, want)
	}
	if want := []string{"0", "10000"}; !reflect.DeepEqual(startIndexes, want) {
		t.Fatalf("item map pagination StartIndex values = %#v, want %#v", startIndexes, want)
	}
}

func TestHTTPHostRequestRejectsResponsesLargerThan32MiB(t *testing.T) {
	const responseLimit = 32 << 20

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 64<<10)
		for remaining := responseLimit + 1; remaining > 0; {
			length := min(remaining, len(chunk))
			if _, err := w.Write(chunk[:length]); err != nil {
				return
			}
			remaining -= length
		}
	}))
	t.Cleanup(server.Close)

	host, err := newHTTPHost(context.Background(), server.URL, AuthConfig{}, nil)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	response, err := host.Request(context.Background(), Step{Name: "oversized response", Method: http.MethodGet, Path: "/oversized"}, "")
	if err == nil {
		t.Fatalf("oversized response was accepted as status %d with %d bytes; want rejection instead of a truncated body", response.Status, len(response.Body))
	}
}

func TestHTTPHostExpectedRedirectReturnsInitialResponse(t *testing.T) {
	var targetRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Location", "web/")
			w.WriteHeader(http.StatusFound)
		case "/web/":
			targetRequests++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	host, err := newHTTPHost(context.Background(), server.URL, AuthConfig{}, nil)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}

	status := 302
	response, err := host.Request(context.Background(), Step{
		Name:   "public root redirect",
		Method: http.MethodGet,
		Path:   "/",
		Expect: &Expect{Status: &status},
	}, "")
	if err != nil {
		t.Fatalf("request expected redirect: %v", err)
	}
	if response.Status != 302 {
		t.Fatalf("response status = %d, want 302", response.Status)
	}
	if got := response.Headers.Get("Location"); got != "web/" {
		t.Fatalf("Location header = %q, want %q", got, "web/")
	}
	if targetRequests != 0 {
		t.Fatalf("target request count = %d, want zero", targetRequests)
	}
}
