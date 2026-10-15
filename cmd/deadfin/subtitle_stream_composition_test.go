// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
	"github.com/deadfin-org/deadfin/internal/store"
)

// TestP6ApplicationStreamsEmbeddedSubtitlesThroughPinnedFD3 protects the
// application composition boundary: persisted source facts select a real
// container, the authenticated Stream routes pass its inherited descriptor to
// the production converter, and each output is windowed before it is served.
func TestP6ApplicationStreamsEmbeddedSubtitlesThroughPinnedFD3(t *testing.T) {
	fakeDir := t.TempDir()
	fd3Contents := filepath.Join(fakeDir, "fd3-contents")
	formats := filepath.Join(fakeDir, "formats")
	p6WriteSubtitleCompositionFFmpeg(t, filepath.Join(fakeDir, "ffmpeg"))
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SUBTITLE_FD3_FILE", fd3Contents)
	t.Setenv("SUBTITLE_FORMATS_FILE", formats)

	app := newCompositionApplication(t, filepath.Join(t.TempDir(), "program-data"))
	configureComposedAdministrator(t, app.Router())

	itemID := composedItemEnrichmentUUID(t, 0xd007)
	source := p6SubtitleCompositionSource(t)
	items := store.NewItemStore(app.stores)
	p6SeedSubtitleCompositionSource(t, items, itemID, source)
	p6AssertSubtitleCompositionSourceFacts(t, items, itemID, source)

	token := loginComposedAdministrator(t, app.Router())
	sourceID := itemID.String()
	for _, tc := range []struct {
		name, path, contentType, body string
	}{
		{
			name:        "windowed VTT tick route",
			path:        "/Videos/" + itemID.Dashed() + "/" + sourceID + "/Subtitles/2/10000000/Stream.vtt?EndPositionTicks=20000000&CopyTimestamps=true&AddVTTTimeMap=true",
			contentType: "text/vtt",
			body:        "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000\n\ninside\n00:00:01.250 --> 00:00:01.750\nkeep\n",
		},
		{
			name:        "windowed SRT plain route",
			path:        "/Videos/" + itemID.Dashed() + "/" + sourceID + "/Subtitles/2/Stream.srt?StartPositionTicks=10000000&EndPositionTicks=20000000&CopyTimestamps=true",
			contentType: "application/x-subrip",
			body:        "2\n00:00:01,250 --> 00:00:01,750\nkeep\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := authenticatedCompositionRequest(app.Router(), http.MethodGet, tc.path, nil, token)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d; body: %s", tc.path, rec.Code, http.StatusOK, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != tc.contentType {
				t.Errorf("GET %s Content-Type = %q, want %q", tc.path, got, tc.contentType)
			}
			if got := rec.Body.String(); got != tc.body {
				t.Errorf("GET %s body = %q, want bounded subtitle %q", tc.path, got, tc.body)
			}
		})
	}

	if got, err := os.ReadFile(fd3Contents); err != nil || string(got) != source.contents {
		t.Fatalf("converter fd3 contents = %q, %v; want pinned source bytes %q", got, err, source.contents)
	}
	if got, err := os.ReadFile(formats); err != nil || string(got) != "webvtt\nsrt\n" {
		t.Fatalf("converter output formats = %q, %v; want webvtt then srt", got, err)
	}

	replacement := filepath.Join(filepath.Dir(source.path), "replacement.mkv")
	if err := os.WriteFile(replacement, []byte(source.contents), 0o600); err != nil {
		t.Fatalf("write replacement subtitle source: %v", err)
	}
	if err := os.Rename(replacement, source.path); err != nil {
		t.Fatalf("replace persisted subtitle source: %v", err)
	}
	rec := authenticatedCompositionRequest(app.Router(), http.MethodGet, "/Videos/"+itemID.Dashed()+"/"+sourceID+"/Subtitles/2/Stream.vtt", nil, token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET stale subtitle source status = %d, want %d; body: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

type p6SubtitleCompositionSourceFixture struct {
	path, contents string
	fingerprint    sourcefile.Fingerprint
}

func p6SubtitleCompositionSource(t *testing.T) p6SubtitleCompositionSourceFixture {
	t.Helper()

	const contents = "embedded-container-bytes-for-fd3"
	path := filepath.Join(t.TempDir(), "embedded-subtitles.mkv")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write embedded subtitle source: %v", err)
	}
	pinned, err := sourcefile.Open(path, sourcefile.Expected{})
	if err != nil {
		t.Fatalf("pin embedded subtitle source: %v", err)
	}
	fingerprint := pinned.Fingerprint()
	if err := pinned.Close(); err != nil {
		t.Fatalf("close seed source descriptor: %v", err)
	}
	return p6SubtitleCompositionSourceFixture{path: path, contents: contents, fingerprint: fingerprint}
}

func p6SeedSubtitleCompositionSource(t *testing.T, items *store.ItemStore, itemID dto.UUID, source p6SubtitleCompositionSourceFixture) {
	t.Helper()
	name, container := "Embedded subtitle composition", "mkv"
	runtime := int64(90_000_000)
	size, device, inode := source.fingerprint.Size, int64(source.fingerprint.Dev), int64(source.fingerprint.Ino)
	modified, changed := source.fingerprint.MTime, source.fingerprint.CTime
	mediaType := item.MediaTypeVideo
	if err := items.Upsert(t.Context(), item.Item{
		ID:            itemID,
		Kind:          item.KindMovie,
		IsMovie:       true,
		Name:          &name,
		Path:          &source.path,
		Container:     &container,
		Size:          &size,
		DateModified:  &modified,
		FileDevice:    &device,
		FileInode:     &inode,
		FileChangedAt: &changed,
		RunTimeTicks:  &runtime,
		MediaType:     &mediaType,
		SourceType:    item.SourceTypeLibrary,
	}); err != nil {
		t.Fatalf("upsert embedded subtitle source: %v", err)
	}
	if err := items.ReplaceStreams(t.Context(), itemID, []playback.MediaStream{
		{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
		{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "subrip"},
	}); err != nil {
		t.Fatalf("persist embedded subtitle stream: %v", err)
	}
}

func p6AssertSubtitleCompositionSourceFacts(t *testing.T, items *store.ItemStore, itemID dto.UUID, source p6SubtitleCompositionSourceFixture) {
	t.Helper()
	records, err := items.MediaSourceRecords(t.Context(), itemID)
	if err != nil {
		t.Fatalf("load persisted subtitle source facts: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("persisted subtitle source records = %d, want 1", len(records))
	}
	record := records[0]
	wantSize, wantDevice, wantInode := source.fingerprint.Size, int64(source.fingerprint.Dev), int64(source.fingerprint.Ino)
	wantModified := source.fingerprint.MTime.UTC().Truncate(time.Microsecond)
	wantChanged := source.fingerprint.CTime.UTC().Truncate(time.Microsecond)
	if record.SourceRevision == "" || record.Path == nil || *record.Path != source.path || record.Size == nil || *record.Size != wantSize || record.FileDevice == nil || *record.FileDevice != wantDevice || record.FileInode == nil || *record.FileInode != wantInode || record.DateModified == nil || !record.DateModified.Equal(wantModified) || record.FileChangedAt == nil || !record.FileChangedAt.Equal(wantChanged) {
		t.Fatalf("persisted subtitle source facts = %#v, want complete revision and fingerprint path=%q size=%d device=%d inode=%d modified=%s changed=%s", record, source.path, wantSize, wantDevice, wantInode, wantModified, wantChanged)
	}
}

func p6WriteSubtitleCompositionFFmpeg(t *testing.T, path string) {
	t.Helper()

	const script = `#!/bin/sh
set -eu
cat /dev/fd/3 > "$SUBTITLE_FD3_FILE"
printf '%s\n' "${10}" >> "$SUBTITLE_FORMATS_FILE"
case "${10}" in
webvtt)
	printf 'WEBVTT\n\nold\n00:00:00.000 --> 00:00:00.999\ndrop\n\ninside\n00:00:01.250 --> 00:00:01.750\nkeep\n\nlate\n00:00:02.001 --> 00:00:03.000\ndrop\n'
	;;
srt)
	printf '1\n00:00:00,000 --> 00:00:00,999\ndrop\n\n2\n00:00:01,250 --> 00:00:01,750\nkeep\n\n3\n00:00:02,001 --> 00:00:03,000\ndrop\n'
	;;
*)
	printf 'unexpected subtitle format %s\n' "${10}" >&2
	exit 1
	;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
}
