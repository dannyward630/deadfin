// SPDX-License-Identifier: GPL-2.0-only

package probe_test

// This opt-in contract compares Deadfin's ffprobe/Normalize boundary against the
// generated, digest-pinned Jellyfin reference environment. It deliberately keeps
// generated media and stock state outside Git. Enable only with:
//   DEADFIN_PROBE_LIVE=1
//   DEADFIN_PROBE_FIXTURES_DIR=/absolute/path/to/tools/goldenrec/fixtures/out
//   DEADFIN_PROBE_STOCK_DB=/absolute/path/to/.../jellyfin.db
//   DEADFIN_PROBE_CONTAINER=goldenrec-jellyfin-1

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/attest"
	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/probe"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

func TestCommittedFixtureSourceManifestBytes(t *testing.T) {
	jellyport := attest.GateForTest(t)
	fixtureRoot := attest.AdjacentPath(jellyport, filepath.Join("tools", "goldenrec", "fixtures"))
	manifest, err := os.ReadFile(filepath.Join(fixtureRoot, "source-manifest.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	verifyFixtureManifestBytes(t, filepath.Join(fixtureRoot, "out"), manifest, "committed source-manifest.sha256")
}

func TestLiveManifestProbeSweep(t *testing.T) {
	fixtures, _, ffprobePath := liveProbeInputs(t)
	media := liveManifestMediaFiles(t, fixtures)
	if got, want := len(media), 30; got != want {
		t.Fatalf("source-manifest probeable media count = %d, want %d", got, want)
	}
	prober, err := ffmpeg.NewProber(context.Background(), ffprobePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mediaFile := range media {
		mediaFile := mediaFile
		t.Run(filepath.ToSlash(mediaFile.relative), func(t *testing.T) {
			raw, err := prober.ProbeAndValidate(context.Background(), mediaFile.hostPath)
			if err != nil {
				t.Fatalf("pinned-container ffprobe: %v", err)
			}
			if _, err := probe.Normalize(context.Background(), raw, probe.Options{IsAudio: strings.EqualFold(filepath.Ext(mediaFile.hostPath), ".mp3"), Path: mediaFile.hostPath, Protocol: "File", VideoType: "VideoFile"}); err != nil {
				t.Fatalf("Normalize: %v", err)
			}
		})
	}
}

func TestLiveFixtureProbeParityAgainstStockDatabase(t *testing.T) {
	fixtures, database, ffprobePath := liveProbeInputs(t)
	// expected-inventory indexes 29 media identities. source-manifest adds the
	// unindexed 720p alternate as the 30th probeable file; the one .disc entry
	// is a non-probe marker and is intentionally excluded by both sweeps.
	media := liveInventoryMediaFiles(t, fixtures)
	if got, want := len(media), 29; got != want {
		t.Fatalf("stock-indexed media count = %d, want %d", got, want)
	}

	snapshot, err := storetest.OpenSQLiteSnapshot(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.Close(); err != nil {
			t.Errorf("close stock SQLite snapshot: %v", err)
		}
	})

	prober, err := ffmpeg.NewProber(context.Background(), ffprobePath)
	if err != nil {
		t.Fatal(err)
	}
	var streamRows, aggregateFields, sidecarRows int
	for _, mediaFile := range media {
		mediaFile := mediaFile
		t.Run(filepath.ToSlash(mediaFile.relative), func(t *testing.T) {
			stock, err := snapshot.Media(t.Context(), mediaFile.stockPath)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := prober.ProbeAndValidate(context.Background(), mediaFile.hostPath)
			if err != nil {
				t.Fatalf("pinned-container ffprobe: %v", err)
			}
			info, err := probe.Normalize(context.Background(), raw, probe.Options{
				IsAudio:   strings.EqualFold(filepath.Ext(mediaFile.hostPath), ".mp3"),
				Path:      mediaFile.hostPath,
				Protocol:  "File",
				VideoType: "VideoFile",
			})
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			aggregateFields += compareStockAggregate(t, info, stock)
			embedded, external := embeddedStockStreams(t, stock.Streams)
			if external != 0 {
				sidecarRows += external
				t.Logf("excluding %d external subtitle row(s): P2-06 external-file merge owns sidecar discovery", external)
			}
			if got, want := len(info.MediaStreams), len(embedded); got != want {
				t.Fatalf("normalized embedded stream count = %d, stock embedded rows = %d", got, want)
			}
			for index, want := range embedded {
				if index >= len(info.MediaStreams) {
					break
				}
				streamRows++
				compareStockStream(t, info.MediaStreams[index], want, index)
			}
		})
	}
	t.Logf("live parity compared %d inventory-classified media files, %d embedded media-stream rows, %d aggregate fields; explicitly excluded %d P2-06 sidecar row(s)", len(media), streamRows, aggregateFields, sidecarRows)
}

func embeddedStockStreams(t *testing.T, streams []map[string]any) ([]map[string]any, int) {
	t.Helper()
	embedded := make([]map[string]any, 0, len(streams))
	var external int
	for _, stream := range streams {
		if stream["IsExternal"] == int64(0) {
			embedded = append(embedded, stream)
			continue
		}
		streamType, ok := stream["StreamType"].(int64)
		if !ok || streamType != int64(playback.StreamTypeSubtitle) {
			t.Fatalf("external stock stream type = %#v, want subtitle", stream["StreamType"])
		}
		external++
	}
	if external != 0 {
		// P2-06 inserts sidecars and owns their final global StreamIndex allocation.
		// Compare these remaining rows by their embedded ffprobe order and fields.
		for _, stream := range embedded {
			delete(stream, "StreamIndex")
		}
	}
	return embedded, external
}

func liveProbeInputs(t *testing.T) (fixtures, database, ffprobePath string) {
	t.Helper()
	if os.Getenv("DEADFIN_PROBE_LIVE") != "1" {
		t.Skip("set DEADFIN_PROBE_LIVE=1 with generated fixture, stock database, and container paths to run live parity")
	}
	fixtures = os.Getenv("DEADFIN_PROBE_FIXTURES_DIR")
	database = os.Getenv("DEADFIN_PROBE_STOCK_DB")
	container := os.Getenv("DEADFIN_PROBE_CONTAINER")
	if fixtures == "" || database == "" || container == "" {
		t.Fatal("DEADFIN_PROBE_FIXTURES_DIR, DEADFIN_PROBE_STOCK_DB, and DEADFIN_PROBE_CONTAINER are required when DEADFIN_PROBE_LIVE=1")
	}
	for _, path := range []string{fixtures, database} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	validateLiveReferenceProvenance(t, fixtures, database, container)
	ffprobePath = filepath.Join(t.TempDir(), "pinned-container-ffprobe")
	wrapper := `#!/usr/bin/env python3
import os
import sys

root = os.environ["DEADFIN_PROBE_FIXTURES_DIR"].rstrip("/")
args = ["/media/" + arg[len(root):].lstrip("/") if arg == root or arg.startswith(root + "/") else arg for arg in sys.argv[1:]]
os.execvp("docker", ["docker", "exec", os.environ["DEADFIN_PROBE_CONTAINER"], "/usr/lib/jellyfin-ffmpeg/ffprobe", *args])
`
	if err := os.WriteFile(ffprobePath, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	return fixtures, database, ffprobePath
}

type liveMediaFile struct {
	relative  string
	hostPath  string
	stockPath string
}

func liveInventoryMediaFiles(t *testing.T, root string) []liveMediaFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(root), "expected-inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory []string
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	var media []liveMediaFile
	for _, entry := range inventory {
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 || (parts[0] != "Audio" && parts[0] != "Movie" && parts[0] != "Episode") {
			continue
		}
		stockPath := parts[1]
		if extension := strings.ToLower(filepath.Ext(stockPath)); extension != ".mkv" && extension != ".mp3" {
			continue // Inventory marker (.disc) is not a probeable media file.
		}
		relative := strings.TrimPrefix(stockPath, "/media/")
		hostPath := filepath.Join(root, filepath.FromSlash(relative))
		if _, err := os.Stat(hostPath); err != nil {
			t.Fatalf("inventory media %q: %v", hostPath, err)
		}
		media = append(media, liveMediaFile{relative: relative, hostPath: hostPath, stockPath: stockPath})
	}
	sort.Slice(media, func(left, right int) bool { return media[left].relative < media[right].relative })
	return media
}

func liveManifestMediaFiles(t *testing.T, root string) []liveMediaFile {
	t.Helper()
	manifest, err := os.ReadFile(filepath.Join(root, "MANIFEST.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	var media []liveMediaFile
	var discMarkers int
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			t.Fatalf("malformed fixture manifest line %q", line)
		}
		relative := parts[1]
		fixtureBytes, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read manifest fixture %q: %v", relative, err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(fixtureBytes)); got != parts[0] {
			t.Fatalf("fixture digest %q = %s, want %s", relative, got, parts[0])
		}
		switch strings.ToLower(filepath.Ext(relative)) {
		case ".mkv", ".mp3":
			hostPath := filepath.Join(root, filepath.FromSlash(relative))
			if _, err := os.Stat(hostPath); err != nil {
				t.Fatalf("manifest media %q: %v", hostPath, err)
			}
			media = append(media, liveMediaFile{relative: relative, hostPath: hostPath, stockPath: "/media/" + relative})
		case ".disc":
			discMarkers++
		}
	}
	if got, want := discMarkers, 1; got != want {
		t.Fatalf("source-manifest .disc marker count = %d, want %d", got, want)
	}
	sort.Slice(media, func(left, right int) bool { return media[left].relative < media[right].relative })
	return media
}

func validateLiveReferenceProvenance(t *testing.T, fixtures, database, container string) {
	t.Helper()
	fixtureRoot := filepath.Dir(fixtures)
	sourceManifest := filepath.Join(fixtureRoot, "source-manifest.sha256")
	wantManifest, err := os.ReadFile(sourceManifest)
	if err != nil {
		t.Fatal(err)
	}
	gotManifest, err := os.ReadFile(filepath.Join(fixtures, "MANIFEST.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotManifest, wantManifest) {
		t.Fatalf("generated MANIFEST.sha256 does not match committed %s", sourceManifest)
	}
	verifyFixtureManifestBytes(t, fixtures, wantManifest, sourceManifest)
	if data, err := os.ReadFile(filepath.Join(fixtures, "FFMPEG.version")); err != nil || !strings.Contains(string(data), "ffmpeg version") {
		t.Fatalf("fixture generator provenance FFMPEG.version: %v", err)
	}
	image, err := exec.Command("docker", "inspect", "--format", "{{.Config.Image}}", container).Output()
	if err != nil {
		t.Fatalf("inspect reference container %q: %v", container, err)
	}
	const stockImage = "jellyfin/jellyfin@sha256:2d1a69be5aa2695d0c2df90ebeb76bbcc6efc0a798cb09bfa65906680e1d785a"
	if got := strings.TrimSpace(string(image)); got != stockImage {
		t.Fatalf("reference container image = %q, want %q", got, stockImage)
	}
	databaseManifest := filepath.Join(filepath.Dir(database), "..", "..", "DATABASES.sha256")
	manifest, err := os.ReadFile(databaseManifest)
	if err != nil {
		t.Fatal(err)
	}
	const databaseEntry = "databases/data/jellyfin.db"
	var wantHash string
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) == 2 && parts[1] == databaseEntry {
			wantHash = parts[0]
			break
		}
	}
	if wantHash == "" {
		t.Fatalf("%s missing from %s", databaseEntry, databaseManifest)
	}
	databaseBytes, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(databaseBytes)); got != wantHash {
		t.Fatalf("stock database digest = %s, want %s", got, wantHash)
	}
}

func verifyFixtureManifestBytes(t *testing.T, root string, manifest []byte, source string) {
	t.Helper()
	entries := 0
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 || len(parts[0]) != 64 {
			t.Fatalf("malformed %s entry %q", source, line)
		}
		fixturePath := filepath.Join(root, filepath.FromSlash(parts[1]))
		fixtureBytes, err := os.ReadFile(fixturePath)
		if err != nil {
			t.Fatalf("read %s fixture %q: %v", source, parts[1], err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(fixtureBytes)); got != parts[0] {
			t.Fatalf("%s fixture digest %q = %s, want %s", source, parts[1], got, parts[0])
		}
		entries++
	}
	if entries == 0 {
		t.Fatalf("%s has no fixture entries", source)
	}
}

func compareStockAggregate(t *testing.T, info probe.MediaInfo, stock storetest.SQLiteMedia) int {
	t.Helper()
	got := map[string]any{
		"Container":    info.Container,
		"Size":         databaseValue(info.Size),
		"RunTimeTicks": databaseValue(info.RunTimeTicks),
		"TotalBitrate": databaseValue(info.Bitrate),
	}
	want := map[string]any{
		"Container":    stock.Container,
		"Size":         stock.Size,
		"RunTimeTicks": stock.RunTimeTicks,
		"TotalBitrate": stock.TotalBitrate,
	}
	compareDatabaseFields(t, "aggregate", got, want)
	return len(want)
}

func compareStockStream(t *testing.T, stream playback.MediaStream, want map[string]any, index int) {
	t.Helper()
	got := map[string]any{
		"StreamIndex":               int64(stream.Index),
		"AspectRatio":               databaseValue(stream.AspectRatio),
		"AverageFrameRate":          databaseValue(stream.AverageFrameRate),
		"BitDepth":                  databaseValue(stream.BitDepth),
		"BitRate":                   databaseValue(stream.BitRate),
		"BlPresentFlag":             databaseValue(stream.BlPresentFlag),
		"ChannelLayout":             databaseValue(stream.ChannelLayout),
		"Channels":                  databaseValue(stream.Channels),
		"Codec":                     databaseValue(stream.Codec),
		"CodecTag":                  databaseValue(stream.CodecTag),
		"CodecTimeBase":             databaseValue(stream.CodecTimeBase),
		"ColorPrimaries":            databaseValue(stream.ColorPrimaries),
		"ColorSpace":                databaseValue(stream.ColorSpace),
		"ColorTransfer":             databaseValue(stream.ColorTransfer),
		"Comment":                   databaseValue(stream.Comment),
		"DvBlSignalCompatibilityId": databaseValue(stream.DvBlSignalCompatibilityID),
		"DvLevel":                   databaseValue(stream.DvLevel),
		"DvProfile":                 databaseValue(stream.DvProfile),
		"DvVersionMajor":            databaseValue(stream.DvVersionMajor),
		"DvVersionMinor":            databaseValue(stream.DvVersionMinor),
		"ElPresentFlag":             databaseValue(stream.ElPresentFlag),
		"Height":                    databaseValue(stream.Height),
		"IsAnamorphic":              databaseValue(stream.IsAnamorphic),
		"IsAvc":                     databaseValue(stream.IsAVC),
		"IsDefault":                 databaseValue(stream.IsDefault),
		"IsExternal":                databaseValue(stream.IsExternal),
		"IsForced":                  databaseValue(stream.IsForced),
		"IsHearingImpaired":         databaseValue(stream.IsHearingImpaired),
		"IsInterlaced":              databaseValue(stream.IsInterlaced),
		"Language":                  databaseValue(stream.Language),
		"Level":                     databaseValue(stream.Level),
		"NalLengthSize":             databaseValue(stream.NalLengthSize),
		"PixelFormat":               databaseValue(stream.PixelFormat),
		"Profile":                   databaseValue(stream.Profile),
		"RealFrameRate":             databaseValue(stream.RealFrameRate),
		"RefFrames":                 databaseValue(stream.RefFrames),
		"Rotation":                  databaseValue(stream.Rotation),
		"RpuPresentFlag":            databaseValue(stream.RpuPresentFlag),
		"SampleRate":                databaseValue(stream.SampleRate),
		"StreamType":                int64(stream.Type),
		"TimeBase":                  databaseValue(stream.TimeBase),
		"Title":                     databaseValue(stream.Title),
		"Width":                     databaseValue(stream.Width),
		"Hdr10PlusPresentFlag":      databaseValue(stream.Hdr10PlusPresentFlag),
		"IsOriginal":                databaseValue(stream.IsOriginal),
	}
	compareDatabaseFields(t, fmt.Sprintf("stream %d", index), got, want)
}

func compareDatabaseFields(t *testing.T, scope string, got, want map[string]any) {
	t.Helper()
	for field, expected := range want {
		actual := got[field]
		if !reflect.DeepEqual(actual, expected) {
			t.Errorf("%s %s = %#v, stock = %#v", scope, field, actual, expected)
		}
	}
}

func databaseValue(value any) any {
	if value == nil {
		return nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return nil
		}
		return databaseValue(reflected.Elem().Interface())
	}
	switch typed := value.(type) {
	case bool:
		if typed {
			return int64(1)
		}
		return int64(0)
	case float32:
		return float64(typed)
	case int:
		return int64(typed)
	case playback.StreamType:
		return int64(typed)
	default:
		return value
	}
}
