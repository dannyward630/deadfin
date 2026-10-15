// SPDX-License-Identifier: GPL-2.0-only

package listenbrainz_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playstate"
	"github.com/deadfin-org/deadfin/internal/providers/listenbrainz"
	"github.com/deadfin-org/deadfin/internal/session"
)

type cassetteInteraction struct {
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	RequestHeaders map[string]string `json:"requestHeaders"`
	RequestBody    json.RawMessage   `json:"requestBody"`
	Status         int               `json:"status"`
	ResponseBody   json.RawMessage   `json:"responseBody"`
}

type sequenceCassette struct {
	mu           sync.Mutex
	interactions []cassetteInteraction
	index        int
}

func loadSequenceCassette(t *testing.T, path string) *sequenceCassette {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var interactions []cassetteInteraction
	if err := json.Unmarshal(contents, &interactions); err != nil {
		t.Fatal(err)
	}
	return &sequenceCassette{interactions: interactions}
}

func (cassette *sequenceCassette) Do(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	cassette.mu.Lock()
	defer cassette.mu.Unlock()
	if cassette.index >= len(cassette.interactions) {
		return nil, fmt.Errorf("listenbrainz cassette: unexpected %s %s body %s", request.Method, request.URL, body)
	}
	interaction := cassette.interactions[cassette.index]
	cassette.index++
	if request.Method != interaction.Method || request.URL.String() != interaction.URL {
		return nil, fmt.Errorf("listenbrainz cassette interaction %d = %s %s, want %s %s", cassette.index, request.Method, request.URL, interaction.Method, interaction.URL)
	}
	for name, want := range interaction.RequestHeaders {
		if got := request.Header.Get(name); got != want {
			return nil, fmt.Errorf("listenbrainz cassette interaction %d header %s = %q, want %q", cassette.index, name, got, want)
		}
	}
	var gotBody, wantBody any
	if err := json.Unmarshal(body, &gotBody); err != nil {
		return nil, fmt.Errorf("listenbrainz cassette request JSON: %w", err)
	}
	if err := json.Unmarshal(interaction.RequestBody, &wantBody); err != nil {
		return nil, fmt.Errorf("listenbrainz cassette expected JSON: %w", err)
	}
	if !reflect.DeepEqual(gotBody, wantBody) {
		return nil, fmt.Errorf("listenbrainz cassette interaction %d body = %s, want %s", cassette.index, body, interaction.RequestBody)
	}
	return &http.Response{
		StatusCode: interaction.Status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(interaction.ResponseBody)),
		Request:    request,
	}, nil
}

func (cassette *sequenceCassette) assertConsumed(t *testing.T) {
	t.Helper()
	cassette.mu.Lock()
	defer cassette.mu.Unlock()
	if cassette.index != len(cassette.interactions) {
		t.Fatalf("ListenBrainz cassette consumed %d/%d interactions", cassette.index, len(cassette.interactions))
	}
}

type itemReader struct {
	item item.Item
	err  error
}

func (reader itemReader) Get(_ context.Context, id dto.UUID) (item.Item, error) {
	if reader.err != nil {
		return item.Item{}, reader.err
	}
	if id != reader.item.ID {
		return item.Item{}, fmt.Errorf("unexpected item %s", id)
	}
	return reader.item, nil
}

type fakeApplication struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (application *fakeApplication) Start(context.Context, playstate.Actor, playstate.Report) error {
	return application.call("start")
}

func (application *fakeApplication) Progress(context.Context, playstate.Actor, playstate.Report) error {
	return application.call("progress")
}

func (application *fakeApplication) Stop(context.Context, playstate.Actor, playstate.Report) error {
	return application.call("stop")
}

func (application *fakeApplication) call(name string) error {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.calls = append(application.calls, name)
	return application.err
}

func (application *fakeApplication) snapshot() []string {
	application.mu.Lock()
	defer application.mu.Unlock()
	return append([]string(nil), application.calls...)
}

type errorCollector struct {
	mu     sync.Mutex
	values []error
}

func (collector *errorCollector) add(err error) {
	collector.mu.Lock()
	collector.values = append(collector.values, err)
	collector.mu.Unlock()
}

func (collector *errorCollector) snapshot() []error {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return append([]error(nil), collector.values...)
}

func TestScrobblerConsumesP5EventsAndReplaysCassette(t *testing.T) {
	cassette := loadSequenceCassette(t, filepath.Join("testdata", "playback-scrobble.json"))
	config := listenbrainz.DefaultConfig()
	config.Token = "cassette-token"
	client := listenbrainz.NewClient(listenbrainz.ClientOptions{
		Doer:      cassette,
		Config:    config,
		UserAgent: "Deadfin cassette test",
	})
	startedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	fakeClock := clock.NewFake(startedAt)
	track := cassetteTrack(t)
	failures := &errorCollector{}
	scrobbler := listenbrainz.NewScrobbler(listenbrainz.ScrobblerOptions{
		Client:           client,
		Items:            itemReader{item: track},
		Clock:            fakeClock,
		SubmitPlayingNow: true,
		OnError:          failures.add,
	})
	t.Cleanup(func() { closeScrobbler(t, scrobbler) })
	inner := &fakeApplication{}
	application := listenbrainz.Wrap(inner, scrobbler)
	actor := playstate.Actor{
		Client: "Finamp",
		Target: session.Target{ID: "session", Generation: 1},
	}

	if err := application.Start(t.Context(), actor, playstate.Report{PlaybackState: session.PlaybackState{ItemID: track.ID}}); err != nil {
		t.Fatal(err)
	}
	flushScrobbler(t, scrobbler)
	fakeClock.Advance(3 * time.Minute)
	half := int64(3 * time.Minute / (100 * time.Nanosecond))
	actor.Target.Generation = 2
	if err := application.Progress(t.Context(), actor, playstate.Report{PlaybackState: session.PlaybackState{ItemID: track.ID, PositionTicks: &half}}); err != nil {
		t.Fatal(err)
	}
	flushScrobbler(t, scrobbler)
	fakeClock.Advance(3 * time.Minute)
	finished := int64(6 * time.Minute / (100 * time.Nanosecond))
	actor.Target.Generation = 3
	if err := application.Stop(t.Context(), actor, playstate.Report{PlaybackState: session.PlaybackState{ItemID: track.ID, PositionTicks: &finished}}); err != nil {
		t.Fatal(err)
	}
	flushScrobbler(t, scrobbler)

	if got, want := inner.snapshot(), []string{"start", "progress", "stop"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("P5 application calls = %#v, want %#v", got, want)
	}
	if got := failures.snapshot(); len(got) != 0 {
		t.Fatalf("ListenBrainz cassette failures = %v", got)
	}
	cassette.assertConsumed(t)
}

func TestScrobblerEvictsPlaybackReusedByAnotherUser(t *testing.T) {
	cassette := &sequenceCassette{}
	config := listenbrainz.DefaultConfig()
	config.Token = "cassette-token"
	failures := &errorCollector{}
	track := cassetteTrack(t)
	scrobbler := listenbrainz.NewScrobbler(listenbrainz.ScrobblerOptions{
		Client:  listenbrainz.NewClient(listenbrainz.ClientOptions{Doer: cassette, Config: config}),
		Items:   itemReader{item: track},
		Clock:   clock.NewFake(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)),
		OnError: failures.add,
	})
	t.Cleanup(func() { closeScrobbler(t, scrobbler) })
	application := listenbrainz.Wrap(&fakeApplication{}, scrobbler)
	first := playstate.Actor{
		UserID: mustUUID(t, "22222222-2222-4222-8222-222222222222"),
		Target: session.Target{ID: "reused-session", Generation: 1},
	}
	second := playstate.Actor{
		UserID: mustUUID(t, "33333333-3333-4333-8333-333333333333"),
		Target: session.Target{ID: "reused-session", Generation: 2},
	}
	start := playstate.Report{PlaybackState: session.PlaybackState{ItemID: track.ID}}
	threshold := playstate.Report{PlaybackState: session.PlaybackState{
		ItemID:        track.ID,
		PositionTicks: ticksPointer(4 * time.Minute),
	}}

	if err := application.Start(t.Context(), first, start); err != nil {
		t.Fatal(err)
	}
	if err := application.Progress(t.Context(), second, threshold); err != nil {
		t.Fatal(err)
	}
	if err := application.Stop(t.Context(), first, threshold); err != nil {
		t.Fatal(err)
	}
	flushScrobbler(t, scrobbler)

	if got := failures.snapshot(); len(got) != 0 {
		t.Fatalf("reused session submitted stale playback: %v", got)
	}
	cassette.assertConsumed(t)
}

func TestScrobblerThresholdStopAndMediaEligibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*item.Item)
		stop   playstate.Report
	}{
		{
			name: "short playback",
			stop: playstate.Report{PlaybackState: session.PlaybackState{PositionTicks: ticksPointer(time.Minute)}},
		},
		{
			name: "failed stop at threshold",
			stop: playstate.Report{PlaybackState: session.PlaybackState{PositionTicks: ticksPointer(4 * time.Minute)}, Failed: true},
		},
		{
			name: "non music item",
			mutate: func(value *item.Item) {
				value.Kind = item.KindMovie
			},
			stop: playstate.Report{PlaybackState: session.PlaybackState{PositionTicks: ticksPointer(4 * time.Minute)}},
		},
		{
			name: "missing artist",
			mutate: func(value *item.Item) {
				value.Values.Artists = nil
				value.Values.AlbumArtists = nil
			},
			stop: playstate.Report{PlaybackState: session.PlaybackState{PositionTicks: ticksPointer(4 * time.Minute)}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cassette := &sequenceCassette{}
			config := listenbrainz.DefaultConfig()
			config.Token = "cassette-token"
			track := cassetteTrack(t)
			if tc.mutate != nil {
				tc.mutate(&track)
			}
			fakeClock := clock.NewFake(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
			scrobbler := listenbrainz.NewScrobbler(listenbrainz.ScrobblerOptions{
				Client: listenbrainz.NewClient(listenbrainz.ClientOptions{Doer: cassette, Config: config}),
				Items:  itemReader{item: track},
				Clock:  fakeClock,
			})
			t.Cleanup(func() { closeScrobbler(t, scrobbler) })
			application := listenbrainz.Wrap(&fakeApplication{}, scrobbler)
			actor := playstate.Actor{Target: session.Target{ID: tc.name, Generation: 1}}
			if err := application.Start(t.Context(), actor, playstate.Report{PlaybackState: session.PlaybackState{ItemID: track.ID}}); err != nil {
				t.Fatal(err)
			}
			tc.stop.ItemID = track.ID
			if err := application.Stop(t.Context(), actor, tc.stop); err != nil {
				t.Fatal(err)
			}
			flushScrobbler(t, scrobbler)
			cassette.assertConsumed(t)
		})
	}
}

func TestListenBrainzFailuresNeverChangeP5Result(t *testing.T) {
	config := listenbrainz.DefaultConfig()
	config.Token = "bad-token"
	upstream := &statusDoer{status: http.StatusUnauthorized, body: `{"code":401,"error":"invalid token"}`}
	failures := &errorCollector{}
	fakeClock := clock.NewFake(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
	track := cassetteTrack(t)
	scrobbler := listenbrainz.NewScrobbler(listenbrainz.ScrobblerOptions{
		Client:           listenbrainz.NewClient(listenbrainz.ClientOptions{Doer: upstream, Config: config}),
		Items:            itemReader{item: track},
		Clock:            fakeClock,
		SubmitPlayingNow: true,
		OnError:          failures.add,
	})
	t.Cleanup(func() { closeScrobbler(t, scrobbler) })
	inner := &fakeApplication{}
	application := listenbrainz.Wrap(inner, scrobbler)
	actor := playstate.Actor{Target: session.Target{ID: "failure", Generation: 1}}
	if err := application.Start(t.Context(), actor, playstate.Report{PlaybackState: session.PlaybackState{ItemID: track.ID}}); err != nil {
		t.Fatalf("P5 start changed by ListenBrainz 401: %v", err)
	}
	if err := application.Progress(t.Context(), actor, playstate.Report{PlaybackState: session.PlaybackState{ItemID: track.ID, PositionTicks: ticksPointer(4 * time.Minute)}}); err != nil {
		t.Fatalf("P5 progress changed by ListenBrainz 401: %v", err)
	}
	flushScrobbler(t, scrobbler)
	if got := failures.snapshot(); len(got) != 2 || !strings.Contains(got[0].Error(), "status 401") || !strings.Contains(got[1].Error(), "status 401") {
		t.Fatalf("isolated ListenBrainz failures = %v, want two 401 reports", got)
	}

	sentinel := errors.New("P5 rejected report")
	failingInner := &fakeApplication{err: sentinel}
	emptyCassette := &sequenceCassette{}
	okConfig := listenbrainz.DefaultConfig()
	okConfig.Token = "cassette-token"
	other := listenbrainz.NewScrobbler(listenbrainz.ScrobblerOptions{
		Client:           listenbrainz.NewClient(listenbrainz.ClientOptions{Doer: emptyCassette, Config: okConfig}),
		Items:            itemReader{item: track},
		Clock:            fakeClock,
		SubmitPlayingNow: true,
	})
	t.Cleanup(func() { closeScrobbler(t, other) })
	if err := listenbrainz.Wrap(failingInner, other).Start(t.Context(), actor, playstate.Report{PlaybackState: session.PlaybackState{ItemID: track.ID}}); !errors.Is(err, sentinel) {
		t.Fatalf("wrapped P5 error = %v, want %v", err, sentinel)
	}
	flushScrobbler(t, other)
	emptyCassette.assertConsumed(t)
}

func TestScrobblerDropsFullQueueWithoutBlockingP5(t *testing.T) {
	config := listenbrainz.DefaultConfig()
	config.Token = "cassette-token"
	blocking := newBlockingDoer()
	failures := &errorCollector{}
	track := cassetteTrack(t)
	fakeClock := clock.NewFake(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
	scrobbler := listenbrainz.NewScrobbler(listenbrainz.ScrobblerOptions{
		Client:           listenbrainz.NewClient(listenbrainz.ClientOptions{Doer: blocking, Config: config}),
		Items:            itemReader{item: track},
		Clock:            fakeClock,
		SubmitPlayingNow: true,
		QueueSize:        1,
		OnError:          failures.add,
	})
	t.Cleanup(func() { closeScrobbler(t, scrobbler) })
	application := listenbrainz.Wrap(&fakeApplication{}, scrobbler)
	actor := playstate.Actor{Target: session.Target{ID: "bounded", Generation: 1}}
	if err := application.Start(t.Context(), actor, playstate.Report{PlaybackState: session.PlaybackState{ItemID: track.ID}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocking.arrived:
	case <-time.After(time.Second):
		t.Fatal("ListenBrainz worker did not reach blocking request")
	}
	if err := application.Progress(t.Context(), actor, playstate.Report{PlaybackState: session.PlaybackState{ItemID: track.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := application.Progress(t.Context(), actor, playstate.Report{PlaybackState: session.PlaybackState{ItemID: track.ID}}); err != nil {
		t.Fatal(err)
	}
	if got := failures.snapshot(); len(got) != 1 || !strings.Contains(got[0].Error(), "queue is full") {
		t.Fatalf("queue failures = %v, want one full-queue report", got)
	}
	close(blocking.release)
	flushScrobbler(t, scrobbler)
}

func TestClientValidationAndConfig(t *testing.T) {
	disabled := listenbrainz.NewClient(listenbrainz.ClientOptions{Config: listenbrainz.DefaultConfig()})
	if disabled.Enabled() {
		t.Fatal("tokenless client is enabled")
	}
	if err := disabled.Submit(t.Context(), listenbrainz.Submission{}); err == nil {
		t.Fatal("tokenless client accepted a submission")
	}

	config := listenbrainz.DefaultConfig()
	config.Token = "token"
	client := listenbrainz.NewClient(listenbrainz.ClientOptions{Doer: &statusDoer{status: http.StatusOK, body: `{"status":"ok"}`}, Config: config})
	for _, submission := range []listenbrainz.Submission{
		{ListenType: "invalid", Payload: []listenbrainz.Listen{{TrackMetadata: listenbrainz.TrackMetadata{ArtistName: "Artist", TrackName: "Track"}}}},
		{ListenType: "single"},
		{ListenType: "single", Payload: []listenbrainz.Listen{{TrackMetadata: listenbrainz.TrackMetadata{ArtistName: "", TrackName: "Track"}}}},
	} {
		if err := client.Submit(t.Context(), submission); err == nil {
			t.Fatalf("client accepted invalid submission %#v", submission)
		}
	}
	valid := listenbrainz.Submission{ListenType: "playing_now", Payload: []listenbrainz.Listen{{TrackMetadata: listenbrainz.TrackMetadata{ArtistName: "Artist", TrackName: "Track"}}}}
	for _, responseBody := range []string{"{", `{"status":"unexpected"}`} {
		badResponseClient := listenbrainz.NewClient(listenbrainz.ClientOptions{
			Doer:   &statusDoer{status: http.StatusOK, body: responseBody},
			Config: config,
		})
		if err := badResponseClient.Submit(t.Context(), valid); err == nil {
			t.Fatalf("client accepted malformed success response %q", responseBody)
		}
	}

	t.Setenv("LISTENBRAINZ_TOKEN", "")
	t.Setenv("LISTENBRAINZ_SERVER", "")
	defaults := listenbrainz.LoadConfig(filepath.Join(t.TempDir(), "missing.xml"))
	if defaults.Server != "https://api.listenbrainz.org" || defaults.Token != "" || !defaults.SubmitPlayingNow {
		t.Fatalf("default config = %#v", defaults)
	}
	path := filepath.Join(t.TempDir(), "Jellyfin.Plugin.ListenBrainz.xml")
	if err := os.WriteFile(path, []byte(`<PluginConfiguration><Server>https://listen.example/</Server><Token>file-token</Token><SubmitPlayingNow>false</SubmitPlayingNow></PluginConfiguration>`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := listenbrainz.LoadConfig(path)
	if loaded.Server != "https://listen.example" || loaded.Token != "file-token" || loaded.SubmitPlayingNow {
		t.Fatalf("loaded config = %#v", loaded)
	}
	t.Setenv("LISTENBRAINZ_TOKEN", " env-token ")
	t.Setenv("LISTENBRAINZ_SERVER", "https://env-listen.example/")
	loaded = listenbrainz.LoadConfig(path)
	if loaded.Server != "https://env-listen.example" || loaded.Token != "env-token" {
		t.Fatalf("environment config = %#v", loaded)
	}
}

type statusDoer struct {
	status int
	body   string
}

func (doer *statusDoer) Do(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: doer.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(doer.body)),
		Request:    request,
	}, nil
}

type blockingDoer struct {
	arrived chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingDoer() *blockingDoer {
	return &blockingDoer{arrived: make(chan struct{}), release: make(chan struct{})}
}

func (doer *blockingDoer) Do(request *http.Request) (*http.Response, error) {
	doer.once.Do(func() { close(doer.arrived) })
	select {
	case <-doer.release:
	case <-request.Context().Done():
		return nil, request.Context().Err()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
		Request:    request,
	}, nil
}

func cassetteTrack(t *testing.T) item.Item {
	t.Helper()
	value := item.Item{
		ID:           mustUUID(t, "11111111-1111-4111-8111-111111111111"),
		Kind:         item.KindAudio,
		Name:         stringPointer("Fixture Track"),
		Album:        stringPointer("Fixture Album"),
		RunTimeTicks: ticksPointer(6 * time.Minute),
		IndexNumber:  intPointer(7),
		Values: item.ItemValues{
			Artists:      []string{"Fixture Artist"},
			AlbumArtists: []string{"Fixture Artist"},
		},
	}
	for name, id := range map[string]string{
		"MusicBrainzArtist":       "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"MusicBrainzAlbumArtist":  "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"MusicBrainzAlbum":        "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"MusicBrainzReleaseGroup": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"MusicBrainzRecording":    "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		"MusicBrainzTrack":        "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
	} {
		value.ProviderIDs.Set(name, id)
	}
	return value
}

func mustUUID(t *testing.T, value string) dto.UUID {
	t.Helper()
	parsed, err := dto.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func flushScrobbler(t *testing.T, scrobbler *listenbrainz.Scrobbler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scrobbler.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

func closeScrobbler(t *testing.T, scrobbler *listenbrainz.Scrobbler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scrobbler.Close(ctx); err != nil {
		t.Errorf("close scrobbler: %v", err)
	}
}

func ticksPointer(duration time.Duration) *int64 {
	value := int64(duration / (100 * time.Nanosecond))
	return &value
}

func intPointer(value int) *int          { return &value }
func stringPointer(value string) *string { return &value }
