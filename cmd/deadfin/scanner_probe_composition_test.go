// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/probe"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
	"github.com/deadfin-org/deadfin/internal/store"
)

func TestApplicationScannerComposesStandaloneProbeFactsAndPlaybackSources(t *testing.T) {
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skipf("ffprobe is required for real scanner probe composition: %v", err)
	}
	fixtures := applicationProbeFixtureRoot(t)
	source := t.TempDir()
	cases := []struct {
		name       string
		collection string
		relative   string
		kind       item.Kind
		isAudio    bool
	}{
		{
			name:       "movie",
			collection: "movies",
			relative:   filepath.Join("movies", "Subtitle Sample (2024)", "Subtitle Sample (2024).mkv"),
			kind:       item.KindMovie,
		},
		{
			name:       "audio",
			collection: "music",
			relative:   filepath.Join("music", "Goldenrec Generator", "Synthetic Album", "1 - Synthetic Track 1.mp3"),
			kind:       item.KindAudio,
			isAudio:    true,
		},
		{
			name:       "episode",
			collection: "tvshows",
			relative:   filepath.Join("tv", "Synthetic Show (2024)", "Season 01", "Synthetic Show S01E01 - First Signal.mkv"),
			kind:       item.KindEpisode,
		},
	}
	for index := range cases {
		target := filepath.Join(source, cases[index].name, filepath.Base(cases[index].relative))
		if cases[index].kind == item.KindEpisode {
			target = filepath.Join(source, cases[index].name, "Synthetic Show (2024)", "Season 01", filepath.Base(cases[index].relative))
		}
		copyApplicationProbeFixture(t, filepath.Join(fixtures, cases[index].relative), target)
		cases[index].relative = target
	}

	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	for _, test := range cases {
		if _, err := app.topology.AddVirtualFolder(t.Context(), test.name, test.collection, library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: applicationProbeLibraryRoot(test.relative, test.kind)}}}); err != nil {
			t.Fatalf("add %s probe fixture library: %v", test.name, err)
		}
	}
	if result, err := app.scanner.Scan(t.Context()); err != nil {
		t.Fatalf("scan real probe fixtures: %v", err)
	} else if len(result.Errors) != 0 {
		t.Fatalf("scan real probe fixture errors = %#v", result.Errors)
	}

	prober, err := ffmpeg.NewProber(t.Context(), ffprobePath)
	if err != nil {
		t.Fatalf("create standalone ffprobe: %v", err)
	}
	items := store.NewItemStore(app.stores)
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			want := applicationStandaloneProbe(t, prober, test.relative, test.isAudio)
			stored := applicationScannedItem(t, items, test.relative, test.kind)
			got, err := items.MediaSourceRecord(t.Context(), stored.ID)
			if err != nil {
				t.Fatalf("load scanned %s media source: %v", test.name, err)
			}
			assertApplicationScannedProbeFacts(t, got, want)

			assembled, err := playback.AssembleMediaSource(t.Context(), items, stored.ID)
			if err != nil {
				t.Fatalf("assemble scanned %s playback source: %v", test.name, err)
			}
			if assembled.ID == "" || assembled.Path != test.relative || len(assembled.MediaStreams) == 0 {
				t.Fatalf("assembled scanned %s playback source = %#v, want canonical path %q and populated streams", test.name, assembled, test.relative)
			}
			wantStreams := applicationHydratedProbeStreams(want.MediaStreams)
			if !reflect.DeepEqual(assembled.MediaStreams, wantStreams) || !applicationEquivalentProbeAttachments(assembled.MediaAttachments, want.MediaAttachments) {
				t.Fatalf("assembled scanned %s media facts differ from hydrated standalone probe\n got: %#v\nwant: %#v", test.name, assembled, want.MediaSourceInfo)
			}
		})
	}
}

func TestApplicationScannerPersistsMinedProbeMetadata(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	source := t.TempDir()
	audioPath := filepath.Join(source, "music", "Embedded Artist", "Embedded Album", "01 - Embedded Track.mp3")
	videoPath := filepath.Join(source, "movies", "Embedded Movie", "Embedded Movie.mkv")
	for _, path := range []string{audioPath, videoPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create mined metadata fixture directory: %v", err)
		}
		if err := os.WriteFile(path, []byte("probe metadata fixture"), 0o600); err != nil {
			t.Fatalf("write mined metadata fixture: %v", err)
		}
	}
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Music", "music", library.LibraryOptions{
		PathInfos: []library.MediaPathInfo{{Path: filepath.Join(source, "music")}},
	}); err != nil {
		t.Fatalf("add mined music library: %v", err)
	}
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{
		PathInfos: []library.MediaPathInfo{{Path: filepath.Join(source, "movies")}},
	}); err != nil {
		t.Fatalf("add mined movie library: %v", err)
	}

	audioOverview := "Embedded audio overview"
	audioRating := "Clean"
	audioRole := "Piano"
	audioInfo := probe.MediaInfo{
		MediaSourceInfo: playback.MediaSourceInfo{Container: "mp3"},
		Artists:         []string{"Track Artist"},
		AlbumArtists:    []string{"Album Artist"},
		Studios:         []string{"Embedded Label"},
		Genres:          []string{"Electronic"},
		People:          []probe.MediaPerson{{Name: "Élodie", Role: &audioRole, Type: "Composer"}},
		ProviderIDs: map[string]string{
			"MusicBrainzRecording": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			"MusicBrainzTrack":     "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		},
		Overview:       &audioOverview,
		OfficialRating: &audioRating,
	}
	videoOverview := "Embedded video overview"
	videoRating := "TV-14"
	videoIndex := 4
	videoInfo := probe.MediaInfo{
		MediaSourceInfo: playback.MediaSourceInfo{Container: "mkv"},
		Studios:         []string{"Embedded Studio"},
		People:          []probe.MediaPerson{{Name: "Actor One", Type: "Actor"}},
		ProviderIDs: map[string]string{
			"MusicBrainzTrack": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		},
		Overview:       &videoOverview,
		OfficialRating: &videoRating,
		IndexNumber:    &videoIndex,
	}
	probeCalls := 0
	applicationReplaceScannerProbe(t, app, applicationProbeFunc(func(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
		probeCalls++
		if input.Item.Kind == item.KindAudio {
			return applicationMediaProbeResult(audioInfo), nil
		}
		return applicationMediaProbeResult(videoInfo), nil
	}))
	if result, err := app.scanner.Scan(t.Context()); err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan mined probe metadata = (%#v, %v), want no errors", result, err)
	}

	items := store.NewItemStore(app.stores)
	audio := applicationScannedItem(t, items, audioPath, item.KindAudio)
	if audio.Overview == nil || *audio.Overview != audioOverview || audio.OfficialRating == nil || *audio.OfficialRating != audioRating {
		t.Fatalf("persisted audio overview/rating = %v/%v", audio.Overview, audio.OfficialRating)
	}
	if !reflect.DeepEqual(audio.Values.Studios, []string{"Embedded Label"}) {
		t.Fatalf("persisted audio studios = %#v", audio.Values.Studios)
	}
	if value, ok := audio.ProviderIDs.Get("MusicBrainzRecording"); !ok || value != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("persisted audio provider IDs = %#v", audio.ProviderIDs.Snapshot())
	}
	if len(audio.People) != 3 || audio.People[0].Name != "Élodie" || audio.People[0].Type != item.PersonKindComposer || audio.People[0].Role == nil || *audio.People[0].Role != audioRole {
		t.Fatalf("persisted audio people = %#v", audio.People)
	}

	video := applicationScannedItem(t, items, videoPath, item.KindMovie)
	if video.Overview == nil || *video.Overview != videoOverview || video.OfficialRating == nil || *video.OfficialRating != videoRating || video.IndexNumber == nil || *video.IndexNumber != videoIndex {
		t.Fatalf("persisted video overview/rating/index = %v/%v/%v", video.Overview, video.OfficialRating, video.IndexNumber)
	}
	if !reflect.DeepEqual(video.Values.Studios, []string{"Embedded Studio"}) || len(video.People) != 1 || video.People[0].Name != "Actor One" {
		t.Fatalf("persisted video studios/people = %#v/%#v", video.Values.Studios, video.People)
	}
	if value, ok := video.ProviderIDs.Get("MusicBrainzTrack"); !ok || value != "cccccccc-cccc-4ccc-8ccc-cccccccccccc" {
		t.Fatalf("persisted video provider IDs = %#v", video.ProviderIDs.Snapshot())
	}

	if result, err := app.scanner.Scan(t.Context()); err != nil || result.Inserted != 0 || result.Updated != 0 || result.Deleted != 0 || len(result.Errors) != 0 {
		t.Fatalf("rescan mined probe metadata = (%#v, %v), want exact no-op", result, err)
	}
	if probeCalls != 2 {
		t.Fatalf("mined metadata probe calls after unchanged rescan = %d, want 2", probeCalls)
	}
	audio = applicationScannedItem(t, items, audioPath, item.KindAudio)
	video = applicationScannedItem(t, items, videoPath, item.KindMovie)
	if audio.Overview == nil || *audio.Overview != audioOverview || len(audio.People) != 3 || len(audio.Values.Studios) != 1 {
		t.Fatalf("unchanged audio lost mined metadata: %#v", audio)
	}
	if video.Overview == nil || *video.Overview != videoOverview || len(video.People) != 1 || len(video.Values.Studios) != 1 {
		t.Fatalf("unchanged video lost mined metadata: %#v", video)
	}
}

func TestApplicationScannerProbeFailureAndCancellationPreserveGoodMediaFacts(t *testing.T) {
	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	source := t.TempDir()
	media := filepath.Join(source, "atomic-media.mkv")
	if err := os.WriteFile(media, []byte("initial-media"), 0o600); err != nil {
		t.Fatalf("write probe failure fixture: %v", err)
	}
	if _, err := app.topology.AddVirtualFolder(t.Context(), "Movies", "movies", library.LibraryOptions{PathInfos: []library.MediaPathInfo{{Path: source}}}); err != nil {
		t.Fatalf("add probe failure library: %v", err)
	}

	var successfulCalls int
	applicationReplaceScannerProbe(t, app, applicationProbeFunc(func(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
		successfulCalls++
		size := input.Entry.Size
		runtime := int64(12_345_678)
		bitrate := 98_765
		videoType := item.VideoTypeVideoFile
		return library.MediaProbeResult{
			Container: "mkv", Size: &size, RunTimeTicks: &runtime, TotalBitrate: &bitrate, VideoType: &videoType,
			Streams:     []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "mpeg4"}, {Index: 1, Type: playback.StreamTypeAudio, Codec: "aac"}},
			Attachments: []playback.MediaAttachment{{Index: 0, FileName: "cover.jpg", Codec: "mjpeg"}},
		}, nil
	}))
	if result, err := app.scanner.Scan(t.Context()); err != nil || len(result.Errors) != 0 {
		t.Fatalf("initial good probe scan = (%#v, %v), want no errors", result, err)
	}
	if successfulCalls != 1 {
		t.Fatalf("initial successful probe calls = %d, want 1", successfulCalls)
	}
	items := store.NewItemStore(app.stores)
	stored := applicationScannedItem(t, items, media, item.KindMovie)
	baseline := applicationProbeFactsForItem(t, items, stored.ID)

	var reuseCalls int
	applicationReplaceScannerProbe(t, app, applicationProbeFunc(func(context.Context, library.MediaProbeInput) (library.MediaProbeResult, error) {
		reuseCalls++
		return library.MediaProbeResult{}, errors.New("unchanged media was reprobed")
	}))
	if result, err := app.scanner.Scan(t.Context()); err != nil || result.Inserted != 0 || result.Updated != 0 || result.Deleted != 0 || len(result.Errors) != 0 {
		t.Fatalf("unchanged probe rescan = (%#v, %v), want exact no-op", result, err)
	}
	if reuseCalls != 0 {
		t.Fatalf("unchanged media probe calls = %d, want 0 source-compatible reuse calls", reuseCalls)
	}
	if got := applicationProbeFactsForItem(t, items, stored.ID); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("unchanged probe facts = %#v, want %#v", got, baseline)
	}

	if err := os.WriteFile(media, []byte("media bytes changed to make the existing source stale"), 0o600); err != nil {
		t.Fatalf("change probe failure fixture: %v", err)
	}
	applicationReplaceScannerProbe(t, app, applicationProbeFunc(func(context.Context, library.MediaProbeInput) (library.MediaProbeResult, error) {
		return library.MediaProbeResult{}, errors.New("ffprobe unavailable")
	}))
	if result, err := app.scanner.Scan(t.Context()); err != nil || len(result.Errors) != 0 {
		t.Fatalf("probe-failing scan = (%#v, %v), want reconciliation without scanner errors", result, err)
	}
	if got := applicationProbeFactsForItem(t, items, stored.ID); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("probe-failing scan facts = %#v, want prior good facts %#v", got, baseline)
	}

	if err := os.WriteFile(media, []byte("media changed again so cancellation enters the media probe"), 0o600); err != nil {
		t.Fatalf("change cancellation fixture: %v", err)
	}
	probeStarted := make(chan struct{})
	applicationReplaceScannerProbe(t, app, applicationProbeFunc(func(ctx context.Context, _ library.MediaProbeInput) (library.MediaProbeResult, error) {
		close(probeStarted)
		<-ctx.Done()
		return library.MediaProbeResult{}, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := app.scanner.Scan(ctx)
		finished <- err
	}()
	select {
	case <-probeStarted:
		cancel()
	case <-t.Context().Done():
		t.Fatalf("wait for cancellable media probe: %v", t.Context().Err())
	}
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe scan error = %v, want context.Canceled", err)
	}
	if got := applicationProbeFactsForItem(t, items, stored.ID); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("cancelled probe scan facts = %#v, want prior good facts %#v", got, baseline)
	}
}

func TestApplicationMediaProbeRejectsMismatchedEnumeratedIdentityBeforeLaunch(t *testing.T) {
	media := filepath.Join(t.TempDir(), "identity.mkv")
	if err := os.WriteFile(media, []byte("pinned media"), 0o600); err != nil {
		t.Fatalf("write media fixture: %v", err)
	}
	launch := filepath.Join(t.TempDir(), "ffprobe-launched")
	t.Setenv("LAUNCH_FILE", launch)
	probe := applicationMediaProbe{prober: ffmpeg.Prober{Path: applicationProbeFixtureFFProbe(t), Timeout: time.Second, MaxOutput: 4096}}
	_, err := probe.Probe(t.Context(), library.MediaProbeInput{
		Entry: library.Entry{Path: media, Size: int64(len("pinned media")), Identity: library.FileIdentity{Valid: true, Inode: 1, Changed: time.Unix(0, 0).UTC()}},
		Item:  item.Item{Kind: item.KindMovie},
	})
	if !errors.Is(err, sourcefile.ErrStaleSource) {
		t.Fatalf("mismatched enumerated identity error = %v, want sourcefile.ErrStaleSource", err)
	}
	if _, statErr := os.Stat(launch); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("ffprobe launch marker stat = %v, want no launch", statErr)
	}
}

func TestApplicationMediaProbeReturnsCancellationWithoutLaunchingFFProbe(t *testing.T) {
	media := filepath.Join(t.TempDir(), "cancelled.mkv")
	if err := os.WriteFile(media, []byte("pinned media"), 0o600); err != nil {
		t.Fatalf("write media fixture: %v", err)
	}
	launch := filepath.Join(t.TempDir(), "ffprobe-launched")
	t.Setenv("LAUNCH_FILE", launch)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	probe := applicationMediaProbe{prober: ffmpeg.Prober{Path: applicationProbeFixtureFFProbe(t), Timeout: time.Second, MaxOutput: 4096}}
	result, err := probe.Probe(ctx, library.MediaProbeInput{Entry: library.Entry{Path: media, Size: int64(len("pinned media"))}, Item: item.Item{Kind: item.KindMovie}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled media probe error = %v, want context.Canceled", err)
	}
	if result.Container != "" || result.Size != nil || result.RunTimeTicks != nil || len(result.Streams) != 0 {
		t.Fatalf("cancelled media probe result = %#v, want no facts", result)
	}
	if _, statErr := os.Stat(launch); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("ffprobe launch marker stat = %v, want no launch", statErr)
	}
}

func applicationProbeFixtureFFProbe(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n[ -z \"${LAUNCH_FILE:-}\" ] || : > \"$LAUNCH_FILE\"\nprintf '{\"streams\":[],\"format\":{\"format_name\":\"matroska\"}}'\n"), 0o700); err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}
	return path
}

type applicationProbeFunc func(context.Context, library.MediaProbeInput) (library.MediaProbeResult, error)

func (probe applicationProbeFunc) Probe(ctx context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	return probe(ctx, input)
}

type applicationProbeFacts struct {
	Container        *string
	Size             *int64
	RunTimeTicks     *int64
	TotalBitrate     *int
	VideoType        *playback.VideoType
	IsoType          *playback.IsoType
	Video3DFormat    *playback.Video3DFormat
	MediaStreams     []playback.MediaStream
	MediaAttachments []playback.MediaAttachment
}

func applicationProbeFactsForItem(t *testing.T, items *store.ItemStore, id dto.UUID) applicationProbeFacts {
	t.Helper()
	record, err := items.MediaSourceRecord(t.Context(), id)
	if err != nil {
		t.Fatalf("load probe facts: %v", err)
	}
	return applicationProbeFacts{Container: record.Container, Size: record.Size, RunTimeTicks: record.RunTimeTicks, TotalBitrate: record.TotalBitrate, VideoType: record.VideoType, IsoType: record.IsoType, Video3DFormat: record.Video3DFormat, MediaStreams: record.MediaStreams, MediaAttachments: record.MediaAttachments}
}

func applicationReplaceScannerProbe(t *testing.T, app *application, probe library.MediaProbe) {
	t.Helper()
	if err := app.scanner.Close(); err != nil {
		t.Fatalf("close application scanner before replacing probe: %v", err)
	}
	scanner, err := library.NewScanner(app.topology, store.NewItemStore(app.stores), library.NewDefaultChain(naming.DefaultOptions()), library.ScannerOptions{Concurrency: 1, MediaProbe: probe})
	if err != nil {
		t.Fatalf("create application scanner with probe: %v", err)
	}
	app.scanner = scanner
}

// applicationProbeFixtureRoot returns the generated goldenrec fixture tree.
// GOLDENREC_FIXTURES_DIR overrides the default <repo>/tools/goldenrec/fixtures/out,
// the same variable tools/goldenrec/fixtures/gen.sh writes to.
func applicationProbeFixtureRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("GOLDENREC_FIXTURES_DIR")
	if root == "" {
		root = filepath.Join("..", "..", "tools", "goldenrec", "fixtures", "out")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("resolve generated probe fixture root: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("generated probe fixtures unavailable at %q: %v", root, err)
	}
	return root
}

func copyApplicationProbeFixture(t *testing.T, source, target string) {
	t.Helper()
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read real probe fixture %q: %v", source, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("create scanner fixture directory for %q: %v", target, err)
	}
	if err := os.WriteFile(target, contents, 0o600); err != nil {
		t.Fatalf("write scanner fixture %q: %v", target, err)
	}
}

func applicationProbeLibraryRoot(path string, kind item.Kind) string {
	if kind == item.KindEpisode {
		return filepath.Dir(filepath.Dir(path))
	}
	return filepath.Dir(path)
}

func applicationStandaloneProbe(t *testing.T, prober ffmpeg.Prober, path string, isAudio bool) probe.MediaInfo {
	t.Helper()
	raw, err := prober.ProbeAndValidate(t.Context(), path)
	if err != nil {
		t.Fatalf("standalone probe %q: %v", path, err)
	}
	info, err := probe.Normalize(t.Context(), raw, probe.Options{IsAudio: isAudio, Path: path, Protocol: "File", VideoType: "VideoFile"})
	if err != nil {
		t.Fatalf("normalize standalone probe %q: %v", path, err)
	}
	return info
}

func applicationScannedItem(t *testing.T, items *store.ItemStore, path string, kind item.Kind) item.Item {
	t.Helper()
	result, err := items.Query(t.Context(), store.ItemQuery{Path: path})
	if err != nil {
		t.Fatalf("query scanned item %q: %v", path, err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("scanned item count at %q = %d, want 1", path, len(result.Items))
	}
	if got := result.Items[0].Kind; got != kind {
		t.Fatalf("scanned item kind at %q = %s, want %s", path, got, kind)
	}
	return result.Items[0]
}

func assertApplicationScannedProbeFacts(t *testing.T, got playback.MediaSourceRecord, want probe.MediaInfo) {
	wantContainer := want.Container
	if !reflect.DeepEqual(got.Container, &wantContainer) || !reflect.DeepEqual(got.Size, want.Size) || !reflect.DeepEqual(got.RunTimeTicks, want.RunTimeTicks) || !reflect.DeepEqual(got.TotalBitrate, want.Bitrate) || !reflect.DeepEqual(got.VideoType, want.VideoType) {
		t.Fatalf("scanned media source facts = %#v, want standalone probe %#v", got, want.MediaSourceInfo)
	}
	wantStreams := applicationHydratedProbeStreams(want.MediaStreams)
	if !reflect.DeepEqual(got.MediaStreams, wantStreams) {
		t.Fatalf("scanned media streams = %#v, want hydrated standalone probe %#v", got.MediaStreams, wantStreams)
	}
	if !applicationEquivalentProbeAttachments(got.MediaAttachments, want.MediaAttachments) {
		t.Fatalf("scanned media attachments = %#v, want standalone probe %#v", got.MediaAttachments, want.MediaAttachments)
	}
}

// applicationHydratedProbeStreams makes the scan's post-ffprobe contract
// explicit: subtitle retrieval capability and localized labels are applied
// before persistence, rather than being raw Normalize output.
func applicationHydratedProbeStreams(values []playback.MediaStream) []playback.MediaStream {
	projected := append([]playback.MediaStream(nil), values...)
	for index := range projected {
		stream := &projected[index]
		if stream.Type != playback.StreamTypeSubtitle {
			continue
		}
		stream.SupportsExternalStream = playback.MediaStreamSupportsExternalStream(*stream)
		stream.LocalizedUndefined = "Undefined"
		stream.LocalizedDefault = "Default"
		stream.LocalizedForced = "Forced"
		stream.LocalizedExternal = "External"
		stream.LocalizedHearingImpaired = "Hearing Impaired"
		if stream.Language != nil {
			stream.LocalizedLanguage = naming.LanguageDisplayName(*stream.Language)
		}
	}
	return projected
}

func applicationEquivalentProbeAttachments(got, want []playback.MediaAttachment) bool {
	return len(got) == 0 && len(want) == 0 || reflect.DeepEqual(got, want)
}
