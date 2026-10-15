// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestScannerItemsFreshMediaStreamParity exercises the persistence boundary
// behind the two fresh stock mismatches (055 MediaStreams, 056 MediaSources).
// The source data is intentionally supplied through MediaProbe and the public
// observations are the real scanner's stored satellites and /Items wire shape.
func TestScannerItemsFreshMediaStreamParity(t *testing.T) {
	harness := newScannerHarness(t, &scannerFixtureResolver{})
	if err := harness.scanner.Close(); err != nil {
		t.Fatalf("close fixture scanner: %v", err)
	}
	resolver := &freshMediaStreamParityResolver{}
	harness.chain = library.NewChain(resolver)
	harness.scanner = newScanner(t, harness.topology, harness.items, harness.chain, library.ScannerOptions{
		Concurrency: 1,
		MediaProbe:  freshMediaStreamParityProbe{},
	})

	ordinaryPath := scannerPath(t, harness.source, "Ordinary Video (2024)", "Ordinary Video (2024).mkv")
	hlgPath := scannerPath(t, harness.source, "HLG Sample (2024)", "HLG Sample (2024).mkv")
	subtitlePath := scannerPath(t, harness.source, "Subtitle Sample (2024)", "Subtitle Sample (2024).mkv")
	discPath := scannerPath(t, harness.source, "Synthetic Disc.bluray.disc")
	version1080Path := scannerPath(t, harness.source, "Synthetic Feature (2024)", "Synthetic Feature (2024) - 1080p.mkv")
	version720Path := scannerPath(t, harness.source, "Synthetic Feature (2024)", "Synthetic Feature (2024) - 720p.mkv")
	for _, path := range []string{ordinaryPath, hlgPath, subtitlePath, discPath, version1080Path, version720Path} {
		writeScannerFile(t, path, "fixture media")
	}
	externalSubtitlePath := scannerPath(t, filepath.Dir(subtitlePath), "Subtitle Sample (2024).en.srt")
	writeScannerFile(t, externalSubtitlePath, "1\n00:00:00,000 --> 00:00:01,000\nEnglish\n")

	result, err := harness.scanner.Scan(t.Context())
	if err != nil {
		t.Fatalf("scan fresh media parity fixture: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("scan fresh media parity fixture errors = %v, want none", result.Errors)
	}

	ordinary := scannerItemAtPath(t, harness.items, ordinaryPath)
	hlg := scannerItemAtPath(t, harness.items, hlgPath)
	subtitle := scannerItemAtPath(t, harness.items, subtitlePath)
	disc := scannerItemAtPath(t, harness.items, discPath)
	version1080 := scannerItemAtPath(t, harness.items, version1080Path)
	version720 := freshMediaStoredItem(t, harness.items, scannerID(version720Path))

	freshMediaAssertScannedStreams(t, harness.items, ordinary.ID, func(streams []playback.MediaStream) {
		video := freshMediaStreamAt(t, streams, 0)
		if video.ColorRange == nil || *video.ColorRange != "tv" {
			t.Fatalf("ordinary video ColorRange = %v, want tv", video.ColorRange)
		}
	})
	freshMediaAssertScannedStreams(t, harness.items, hlg.ID, func(streams []playback.MediaStream) {
		video := freshMediaStreamAt(t, streams, 0)
		if video.ColorSpace == nil || *video.ColorSpace != "bt2020nc" {
			t.Fatalf("HLG video ColorSpace = %v, want bt2020nc", video.ColorSpace)
		}
	})
	freshMediaAssertScannedStreams(t, harness.items, subtitle.ID, func(streams []playback.MediaStream) {
		external := freshMediaStreamAt(t, streams, 0)
		embedded := freshMediaStreamAt(t, streams, 3)
		if !external.IsExternal || !external.SupportsExternalStream || external.Path != externalSubtitlePath || external.Language == nil || *external.Language != "eng" {
			t.Fatalf("external subtitle = %#v, want English external sidecar", external)
		}
		if embedded.IsExternal || !embedded.SupportsExternalStream || !embedded.IsTextSubtitleStream() {
			t.Fatalf("embedded subtitle = %#v, want internal text subtitle supporting external delivery", embedded)
		}
		for name, value := range map[string]string{
			"external LocalizedLanguage":        external.LocalizedLanguage,
			"external LocalizedForced":          external.LocalizedForced,
			"external LocalizedHearingImpaired": external.LocalizedHearingImpaired,
			"embedded LocalizedForced":          embedded.LocalizedForced,
			"embedded LocalizedHearingImpaired": embedded.LocalizedHearingImpaired,
		} {
			if value == "" {
				t.Errorf("%s omitted from scanned subtitle stream", name)
			}
		}
	})
	if disc.Container != nil || disc.MediaSourceContainer == nil || *disc.MediaSourceContainer != "disc" || disc.VideoType == nil || *disc.VideoType != item.VideoTypeBluRay {
		t.Fatalf("scanned disc = %#v, want nil Container, disc MediaSourceContainer, and BluRay video type", disc)
	}
	if version720.PrimaryVersionID == nil || *version720.PrimaryVersionID != version1080.ID {
		t.Fatalf("720p version primary = %v, want 1080p ID %s", version720.PrimaryVersionID, version1080.ID)
	}

	itemsRoutes := router.New()
	builder := dtobuild.NewBuilder(harness.items, dtobuild.NewStoreEnricher(harness.items, nil, nil))
	handlers.RegisterItems(itemsRoutes, harness.items, builder, clock.NewReal())
	for _, profile := range freshMediaWireProfiles {
		t.Run(profile.name, func(t *testing.T) {
			ordinaryRow := freshMediaItemsRow(t, itemsRoutes, profile, ordinary.ID, "MediaStreams")
			ordinaryVideo := freshMediaNestedRow(t, ordinaryRow, profile.key("MediaStreams"), 0)
			freshMediaAssertAbsent(t, ordinaryVideo, profile.key("ColorRange"))

			hlgRow := freshMediaItemsRow(t, itemsRoutes, profile, hlg.ID, "MediaStreams")
			hlgVideo := freshMediaNestedRow(t, hlgRow, profile.key("MediaStreams"), 0)
			for name, want := range map[string]string{
				"ColorSpace": "bt2020nc", "VideoRange": "SDR", "VideoRangeType": "SDR", "DisplayTitle": "240p HEVC SDR",
			} {
				freshMediaAssertRaw(t, hlgVideo, profile.key(name), `"`+want+`"`)
			}
			for _, name := range []string{"ColorRange", "ColorTransfer", "ColorPrimaries"} {
				freshMediaAssertAbsent(t, hlgVideo, profile.key(name))
			}

			subtitleRow := freshMediaItemsRow(t, itemsRoutes, profile, subtitle.ID, "MediaStreams,MediaSources")
			topStreams := freshMediaNestedRows(t, subtitleRow, profile.key("MediaStreams"))
			external, embedded := topStreams[0], topStreams[3]
			freshMediaAssertRaw(t, external, profile.key("IsExternal"), "true")
			freshMediaAssertRaw(t, external, profile.key("SupportsExternalStream"), "true")
			freshMediaAssertRaw(t, external, profile.key("LocalizedLanguage"), `"English"`)
			freshMediaAssertRaw(t, embedded, profile.key("IsExternal"), "false")
			freshMediaAssertRaw(t, embedded, profile.key("SupportsExternalStream"), "true")
			freshMediaAssertRaw(t, embedded, profile.key("LocalizedHearingImpaired"), `"Hearing Impaired"`)
			for _, stream := range topStreams {
				freshMediaAssertAbsent(t, stream, profile.key("Score"))
			}

			sourceRows := freshMediaNestedRows(t, subtitleRow, profile.key("MediaSources"))
			if len(sourceRows) != 1 {
				t.Fatalf("subtitle media sources = %d, want one", len(sourceRows))
			}
			freshMediaAssertRaw(t, sourceRows[0], profile.key("DefaultAudioStreamIndex"), "2")
			freshMediaAssertRaw(t, sourceRows[0], profile.key("DefaultSubtitleStreamIndex"), "0")
			sourceExternal := freshMediaNestedRow(t, sourceRows[0], profile.key("MediaStreams"), 0)
			freshMediaAssertRaw(t, sourceExternal, profile.key("Score"), "111222")

			discRow := freshMediaItemsRow(t, itemsRoutes, profile, disc.ID, "MediaSources")
			discSource := freshMediaNestedRow(t, discRow, profile.key("MediaSources"), 0)
			freshMediaAssertRaw(t, discSource, profile.key("Container"), `"disc"`)
			freshMediaAssertRaw(t, discSource, profile.key("Name"), `"Synthetic Disc.bluray/Bluray"`)

			versionsRow := freshMediaItemsRow(t, itemsRoutes, profile, version1080.ID, "MediaSources")
			versions := freshMediaNestedRows(t, versionsRow, profile.key("MediaSources"))
			if len(versions) != 2 {
				t.Fatalf("version media sources = %d, want two", len(versions))
			}
			for index, want := range []struct {
				id   dto.UUID
				name string
			}{{version1080.ID, "1080p"}, {version720.ID, "720p"}} {
				freshMediaAssertRaw(t, versions[index], profile.key("Id"), `"`+want.id.String()+`"`)
				freshMediaAssertRaw(t, versions[index], profile.key("Name"), `"`+want.name+`"`)
			}
		})
	}

	playbackRoutes := router.New()
	playbackService := playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources:          harness.items,
		NewPlaySessionID: func() string { return "fresh-media-parity" },
	})
	handlers.RegisterPlaybackInfo(playbackRoutes, playbackService, nil)
	playbackRecord := httptest.NewRecorder()
	playbackRequest := httptest.NewRequest(http.MethodGet, "/Items/"+ordinary.ID.Dashed()+"/PlaybackInfo", nil)
	playbackRequest.Header.Set("Accept", `application/json; profile="CamelCase"`)
	playbackRoutes.ServeHTTP(playbackRecord, playbackRequest)
	if playbackRecord.Code != http.StatusOK {
		t.Fatalf("GET PlaybackInfo status = %d, want 200; body=%s", playbackRecord.Code, playbackRecord.Body.String())
	}
	var playbackBody map[string]json.RawMessage
	if err := json.Unmarshal(playbackRecord.Body.Bytes(), &playbackBody); err != nil {
		t.Fatalf("decode PlaybackInfo response: %v", err)
	}
	var playbackSources []map[string]json.RawMessage
	if err := json.Unmarshal(playbackBody["mediaSources"], &playbackSources); err != nil || len(playbackSources) != 1 {
		t.Fatalf("decode PlaybackInfo media sources: %v; body=%s", err, playbackRecord.Body.String())
	}
	playbackVideo := freshMediaNestedRow(t, playbackSources[0], "mediaStreams", 0)
	freshMediaAssertAbsent(t, playbackVideo, "colorRange")
}

type freshMediaWireProfile struct {
	name, accept, itemsKey string
	key                    func(string) string
}

var freshMediaWireProfiles = []freshMediaWireProfile{
	{name: "Pascal", accept: "application/json", itemsKey: "Items", key: func(value string) string { return value }},
	{name: "Camel", accept: `application/json; profile="CamelCase"`, itemsKey: "items", key: freshMediaCamelKey},
}

func freshMediaCamelKey(value string) string {
	if value == "" {
		return value
	}
	return string(value[0]+32) + value[1:]
}

func freshMediaItemsRow(t *testing.T, routes *router.Router, profile freshMediaWireProfile, id dto.UUID, fields string) map[string]json.RawMessage {
	t.Helper()
	record := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/Items?Ids="+id.String()+"&Fields="+fields, nil)
	request.Header.Set("Accept", profile.accept)
	routes.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET /Items Fields=%s status = %d, want 200; body=%s", fields, record.Code, record.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /Items response: %v", err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(body[profile.itemsKey], &rows); err != nil || len(rows) != 1 {
		t.Fatalf("decode %s items: %v; body=%s", profile.itemsKey, err, record.Body.String())
	}
	return rows[0]
}

func freshMediaNestedRows(t *testing.T, row map[string]json.RawMessage, key string) []map[string]json.RawMessage {
	t.Helper()
	var values []map[string]json.RawMessage
	if err := json.Unmarshal(row[key], &values); err != nil {
		t.Fatalf("decode nested %s: %v; row=%s", key, err, row[key])
	}
	return values
}

func freshMediaNestedRow(t *testing.T, row map[string]json.RawMessage, key string, index int) map[string]json.RawMessage {
	t.Helper()
	values := freshMediaNestedRows(t, row, key)
	if index >= len(values) {
		t.Fatalf("nested %s has %d values, want index %d; row=%s", key, len(values), index, row[key])
	}
	return values[index]
}

func freshMediaAssertRaw(t *testing.T, row map[string]json.RawMessage, key, want string) {
	t.Helper()
	if got, found := row[key]; !found || string(got) != want {
		t.Errorf("%s = %s (present=%t), want %s", key, got, found, want)
	}
}

func freshMediaAssertAbsent(t *testing.T, row map[string]json.RawMessage, key string) {
	t.Helper()
	if got, found := row[key]; found {
		t.Errorf("%s = %s, want omitted", key, got)
	}
}

func freshMediaAssertScannedStreams(t *testing.T, items *store.ItemStore, id dto.UUID, check func([]playback.MediaStream)) {
	t.Helper()
	streams, err := items.Streams(t.Context(), id)
	if err != nil {
		t.Fatalf("load scanned streams for %s: %v", id, err)
	}
	check(streams)
}

func freshMediaStoredItem(t *testing.T, items *store.ItemStore, id dto.UUID) item.Item {
	t.Helper()
	value, err := items.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("load stored item %s: %v", id, err)
	}
	return value
}

func freshMediaStreamAt(t *testing.T, streams []playback.MediaStream, index int) playback.MediaStream {
	t.Helper()
	for _, stream := range streams {
		if stream.Index == index {
			return stream
		}
	}
	t.Fatalf("stream index %d is absent from %#v", index, streams)
	return playback.MediaStream{}
}

type freshMediaStreamParityResolver struct{ scannerFixtureResolver }

func (resolver *freshMediaStreamParityResolver) Resolve(ctx context.Context, input library.ResolveContext) (*item.Item, error) {
	value, err := resolver.scannerFixtureResolver.Resolve(ctx, input)
	if err != nil || input.Entry.IsDir {
		return value, err
	}
	switch filepath.Base(input.Entry.Path) {
	case "Synthetic Disc.bluray.disc":
		value.Name = freshMediaString("Synthetic Disc")
		videoType := item.VideoTypeBluRay
		value.VideoType = &videoType
		value.IsPlaceHolder = true
	case "Synthetic Feature (2024) - 1080p.mkv", "Synthetic Feature (2024) - 720p.mkv":
		value.Name = freshMediaString("Synthetic Feature")
	}
	return value, nil
}

func (*freshMediaStreamParityResolver) ResolveMany(context.Context, library.ResolveContext) ([]*item.Item, error) {
	return nil, nil
}

func (resolver *freshMediaStreamParityResolver) ResolveCandidates(ctx context.Context, input library.ResolveContext) ([]library.ResolvedCandidate, error) {
	var primary, alternate *item.Item
	for _, entry := range input.Children {
		candidateInput := input
		candidateInput.Entry = entry
		resolved, err := resolver.Resolve(ctx, candidateInput)
		if err != nil {
			return nil, err
		}
		switch filepath.Base(entry.Path) {
		case "Synthetic Feature (2024) - 1080p.mkv":
			primary = resolved
		case "Synthetic Feature (2024) - 720p.mkv":
			alternate = resolved
		}
	}
	if primary == nil || alternate == nil {
		return nil, nil
	}
	return []library.ResolvedCandidate{
		{Item: primary},
		{Item: alternate, LocalAlternateVersion: &library.LocalAlternateVersion{PrimaryID: primary.ID, Index: 1}},
	}, nil
}

type freshMediaStreamParityProbe struct{}

func (freshMediaStreamParityProbe) Probe(_ context.Context, input library.MediaProbeInput) (library.MediaProbeResult, error) {
	result := library.MediaProbeResult{Container: "mkv"}
	switch filepath.Base(*input.Item.Path) {
	case "Ordinary Video (2024).mkv":
		result.Streams = []playback.MediaStream{freshMediaVideo(0, "mpeg4", "tv", nil, nil, nil, 180), freshMediaAudio(1, false)}
	case "HLG Sample (2024).mkv":
		space, transfer, primaries := "bt2020nc", "arib-std-b67", "bt2020"
		result.Streams = []playback.MediaStream{freshMediaVideo(0, "hevc", "tv", &space, &transfer, &primaries, 180)}
	case "Subtitle Sample (2024).mkv":
		result.Streams = []playback.MediaStream{
			freshMediaVideo(0, "mpeg4", "tv", nil, nil, nil, 180),
			freshMediaAudio(1, true),
			{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "subrip", SupportsExternalStream: true},
		}
	case "Synthetic Disc.bluray.disc":
		result.Container = "disc"
		videoType := item.VideoTypeBluRay
		result.VideoType = &videoType
	case "Synthetic Feature (2024) - 1080p.mkv":
		result.Streams = []playback.MediaStream{freshMediaVideo(0, "mpeg4", "tv", nil, nil, nil, 1080), freshMediaAudio(1, true)}
	case "Synthetic Feature (2024) - 720p.mkv":
		result.Streams = []playback.MediaStream{freshMediaVideo(0, "mpeg4", "tv", nil, nil, nil, 720), freshMediaAudio(1, true)}
	}
	return result, nil
}

func freshMediaVideo(index int, codec, colorRange string, colorSpace, colorTransfer, colorPrimaries *string, height int) playback.MediaStream {
	width := 320
	return playback.MediaStream{Index: index, Type: playback.StreamTypeVideo, Codec: codec, ColorRange: freshMediaString(colorRange), ColorSpace: colorSpace, ColorTransfer: colorTransfer, ColorPrimaries: colorPrimaries, Width: &width, Height: &height}
}

func freshMediaAudio(index int, isDefault bool) playback.MediaStream {
	return playback.MediaStream{Index: index, Type: playback.StreamTypeAudio, Codec: "aac", IsDefault: isDefault}
}

func freshMediaString(value string) *string { return &value }
