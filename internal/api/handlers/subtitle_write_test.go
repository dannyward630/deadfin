// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/subtitleedit"
)

const subtitleWriteSRT = "1\n00:00:01,000 --> 00:00:02,000\nHello\n"

type subtitleWriteStartup struct{}

func (subtitleWriteStartup) StartupWizardCompleted() bool { return true }

type subtitleWriteLibraries struct{ folders []library.VirtualFolder }

func (l subtitleWriteLibraries) VirtualFolders(context.Context) ([]library.VirtualFolder, error) {
	return l.folders, nil
}

type subtitleWriteSources struct{ items *store.ItemStore }

func (s subtitleWriteSources) MediaSourceRecords(ctx context.Context, id dto.UUID) ([]playback.MediaSourceRecord, error) {
	records, err := s.items.MediaSourceRecords(ctx, id)
	if store.IsNotFound(err) {
		return nil, playback.ErrItemNotFound
	}
	return records, err
}

type subtitleWriteFixture struct {
	t        *testing.T
	items    *store.ItemStore
	router   *router.Router
	movie    item.Item
	mediaDir string
	metadata string
	admin    string
	manager  string
	viewer   string
	outsider string
}

// newSubtitleWriteFixture seeds one movie with an embedded subtitle at
// stream index 2 in a temp media folder, behind the real authorization
// evaluator, the subtitle write routes, and PlaybackInfo.
func newSubtitleWriteFixture(t *testing.T, saveWithMedia bool) *subtitleWriteFixture {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	items := store.NewItemStore(stores)
	root := t.TempDir()
	if err := items.BootstrapSystemFolders(t.Context(), root); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	f := &subtitleWriteFixture{
		t: t, items: items,
		mediaDir: filepath.Join(root, "media", "Feature (2020)"),
		metadata: filepath.Join(root, "metadata"),
		admin:    collUUID(t, 800).String(),
		manager:  collUUID(t, 801).String(),
		viewer:   collUUID(t, 802).String(),
		outsider: collUUID(t, 803).String(),
	}
	if err := os.MkdirAll(f.mediaDir, 0o755); err != nil {
		t.Fatalf("mkdir media: %v", err)
	}
	mediaPath := filepath.Join(f.mediaDir, "Feature (2020).mkv")
	if err := os.WriteFile(mediaPath, []byte("not a real mkv"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}

	collection := item.Item{ID: collUUID(t, 810), Kind: item.KindCollectionFolder, Name: strPtr("Movies"), IsFolder: true}
	if err := items.Upsert(t.Context(), collection); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	video := item.MediaTypeVideo
	container := "mkv"
	f.movie = collMovieItem(t, 811, "Feature")
	f.movie.ParentID, f.movie.TopParentID = &collection.ID, &collection.ID
	f.movie.MediaType, f.movie.Path, f.movie.Container = &video, &mediaPath, &container
	if err := items.Upsert(t.Context(), f.movie); err != nil {
		t.Fatalf("seed movie: %v", err)
	}
	english := "eng"
	if err := items.ReplaceStreams(t.Context(), f.movie.ID, []playback.MediaStream{
		{Index: 0, Type: playback.StreamTypeVideo, Codec: "h264"},
		{Index: 1, Type: playback.StreamTypeAudio, Codec: "aac", IsDefault: true},
		{Index: 2, Type: playback.StreamTypeSubtitle, Codec: "subrip", Language: &english},
	}); err != nil {
		t.Fatalf("seed streams: %v", err)
	}

	users := playlistUsers{}
	for id, policy := range map[string]string{
		f.admin:   `{"IsAdministrator":true}`,
		f.manager: `{"EnableSubtitleManagement":true}`,
		f.viewer:  `{}`,
		// A subtitle manager without access to the Movies library.
		f.outsider: `{"EnableSubtitleManagement":true,"EnableAllFolders":false,"EnabledFolders":[]}`,
	} {
		parsed, _ := dto.ParseUUID(id)
		users[parsed] = store.User{ID: parsed, Name: id, Policy: []byte(policy)}
	}
	options := library.LibraryOptions{SaveSubtitlesWithMedia: saveWithMedia}
	editor := subtitleedit.New(subtitleedit.Options{
		Items:        items,
		Libraries:    subtitleWriteLibraries{folders: []library.VirtualFolder{{CollectionFolderID: collection.ID, Name: "Movies", Options: options}}},
		Access:       store.NewPlaybackItemAccessReader(items, users),
		MetadataPath: f.metadata,
	})
	evaluator := auth.NewEvaluator(subtitleWriteStartup{}, startupUserLocalNetwork{}, startupUserSchedules{}, func() time.Time { return time.Unix(0, 0).UTC() })
	authorizer := middleware.NewAuthorizer(playlistResolver{}, evaluator, users)
	f.router = router.New(authorizer.Wrap)
	handlers.RegisterSubtitleWrites(f.router, editor)
	handlers.RegisterPlaybackInfo(f.router, playback.NewPlaybackInfoService(playback.PlaybackInfoServiceConfig{
		Sources:     subtitleWriteSources{items: items},
		Permissions: playbackConcretePermissionReader{},
		Network:     playbackConcreteNetwork{},
	}), nil)
	return f
}

func (f *subtitleWriteFixture) do(method, path, as, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("X-Test-User", as)
	record := httptest.NewRecorder()
	f.router.ServeHTTP(record, request)
	return record
}

func (f *subtitleWriteFixture) expect(record *httptest.ResponseRecorder, status int) {
	f.t.Helper()
	if record.Code != status {
		f.t.Fatalf("status = %d, want %d; body=%s", record.Code, status, record.Body.String())
	}
}

func (f *subtitleWriteFixture) upload(as, language string, forced bool) *httptest.ResponseRecorder {
	f.t.Helper()
	body, err := json.Marshal(map[string]any{
		"Language": language, "Format": "srt", "IsForced": forced, "IsHearingImpaired": false,
		"Data": base64.StdEncoding.EncodeToString([]byte(subtitleWriteSRT)),
	})
	if err != nil {
		f.t.Fatalf("encode body: %v", err)
	}
	return f.do(http.MethodPost, "/Videos/"+f.movie.ID.String()+"/Subtitles", as, string(body))
}

func (f *subtitleWriteFixture) internalDir() string {
	return library.InternalMetadataDir(f.metadata, f.movie.ID)
}

// playbackSubtitles returns the subtitle streams of the PlaybackInfo source.
func (f *subtitleWriteFixture) playbackSubtitles() []playback.MediaStream {
	f.t.Helper()
	record := f.do(http.MethodPost, "/Items/"+f.movie.ID.String()+"/PlaybackInfo?userId="+f.admin, f.admin, "{}")
	f.expect(record, http.StatusOK)
	var response playback.PlaybackInfoResponse
	if err := json.Unmarshal(record.Body.Bytes(), &response); err != nil {
		f.t.Fatalf("decode PlaybackInfo: %v", err)
	}
	if len(response.MediaSources) != 1 {
		f.t.Fatalf("PlaybackInfo sources = %d, want 1", len(response.MediaSources))
	}
	var subtitles []playback.MediaStream
	for _, stream := range response.MediaSources[0].MediaStreams {
		if stream.Type == playback.StreamTypeSubtitle {
			subtitles = append(subtitles, stream)
		}
	}
	return subtitles
}

func readSubtitleFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestUploadSubtitleSavesBesideWritableMedia(t *testing.T) {
	f := newSubtitleWriteFixture(t, true)
	f.expect(f.upload(f.manager, "ENG", true), http.StatusNoContent)

	want := filepath.Join(f.mediaDir, "Feature (2020).eng.forced.srt")
	if got := readSubtitleFile(t, want); got != subtitleWriteSRT {
		t.Fatalf("saved subtitle = %q", got)
	}
	if _, err := os.Stat(f.internalDir()); !os.IsNotExist(err) {
		t.Fatalf("internal metadata directory exists after a media-folder save: %v", err)
	}

	subtitles := f.playbackSubtitles()
	if len(subtitles) != 2 {
		t.Fatalf("PlaybackInfo subtitles = %+v, want external then embedded", subtitles)
	}
	external := subtitles[0]
	if !external.IsExternal || external.Path != want || external.Index != 0 || !external.IsForced || external.Language == nil || *external.Language != "eng" || external.Codec != "subrip" {
		t.Fatalf("external subtitle = %+v", external)
	}
	if subtitles[1].IsExternal || subtitles[1].Index != 3 {
		t.Fatalf("embedded subtitle after upload = %+v, want index 3", subtitles[1])
	}

	// A second upload with the same name takes a counter, as upstream.
	f.expect(f.upload(f.manager, "eng", true), http.StatusNoContent)
	readSubtitleFile(t, filepath.Join(f.mediaDir, "Feature (2020).eng.forced.0.srt"))
	if subtitles := f.playbackSubtitles(); len(subtitles) != 3 {
		t.Fatalf("PlaybackInfo subtitles after second upload = %d, want 3", len(subtitles))
	}
}

func TestUploadSubtitleFallsBackToMetadataForReadOnlyMedia(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSubtitleWriteFixture(t, true)
	if err := os.Chmod(f.mediaDir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.mediaDir, 0o755) })

	f.expect(f.upload(f.admin, "fre", false), http.StatusNoContent)
	want := filepath.Join(f.internalDir(), "Feature (2020).fre.srt")
	if got := readSubtitleFile(t, want); got != subtitleWriteSRT {
		t.Fatalf("saved subtitle = %q", got)
	}
	subtitles := f.playbackSubtitles()
	if len(subtitles) != 2 || !subtitles[0].IsExternal || subtitles[0].Path != want || subtitles[0].Language == nil || *subtitles[0].Language != "fre" {
		t.Fatalf("PlaybackInfo subtitles = %+v, want the metadata-folder subtitle", subtitles)
	}
}

func TestUploadSubtitleWithoutSaveWithMediaUsesMetadata(t *testing.T) {
	f := newSubtitleWriteFixture(t, false)
	f.expect(f.upload(f.admin, "eng", false), http.StatusNoContent)
	readSubtitleFile(t, filepath.Join(f.internalDir(), "Feature (2020).eng.srt"))
	entries, err := os.ReadDir(f.mediaDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("media folder entries = %v (%v), want only the media file", entries, err)
	}
}

func TestDeleteSubtitleRemovesExternalFile(t *testing.T) {
	f := newSubtitleWriteFixture(t, false)
	f.expect(f.upload(f.admin, "eng", false), http.StatusNoContent)
	path := filepath.Join(f.internalDir(), "Feature (2020).eng.srt")
	readSubtitleFile(t, path)

	f.expect(f.do(http.MethodDelete, "/Videos/"+f.movie.ID.String()+"/Subtitles/0", f.admin, ""), http.StatusNoContent)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("subtitle file after delete: %v", err)
	}
	subtitles := f.playbackSubtitles()
	if len(subtitles) != 1 || subtitles[0].IsExternal || subtitles[0].Index != 2 {
		t.Fatalf("PlaybackInfo subtitles after delete = %+v, want only the embedded one at index 2", subtitles)
	}
}

func TestDeleteSubtitleRefusesEmbeddedStream(t *testing.T) {
	f := newSubtitleWriteFixture(t, true)
	base := "/Videos/" + f.movie.ID.String() + "/Subtitles/"
	f.expect(f.do(http.MethodDelete, base+"2", f.admin, ""), http.StatusBadRequest)
	// Index 1 is the audio stream, not a subtitle.
	f.expect(f.do(http.MethodDelete, base+"1", f.admin, ""), http.StatusBadRequest)
	f.expect(f.do(http.MethodDelete, base+"9", f.admin, ""), http.StatusBadRequest)
	f.expect(f.do(http.MethodDelete, "/Videos/"+collUUID(t, 999).String()+"/Subtitles/0", f.admin, ""), http.StatusNotFound)
	if subtitles := f.playbackSubtitles(); len(subtitles) != 1 || subtitles[0].Index != 2 {
		t.Fatalf("PlaybackInfo subtitles = %+v, want the embedded subtitle unchanged", subtitles)
	}
}

func TestSubtitleWritePermissions(t *testing.T) {
	f := newSubtitleWriteFixture(t, true)
	// UploadSubtitle requires EnableSubtitleManagement or an administrator.
	f.expect(f.upload(f.viewer, "eng", false), http.StatusForbidden)
	f.expect(f.upload("", "eng", false), http.StatusUnauthorized)
	// GetItemById<Video>(itemId, userId) does not find an item the user cannot see.
	f.expect(f.upload(f.outsider, "eng", false), http.StatusNotFound)
	entries, err := os.ReadDir(f.mediaDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("media folder entries after refused uploads = %v (%v)", entries, err)
	}

	f.expect(f.upload(f.manager, "eng", false), http.StatusNoContent)
	// DeleteSubtitle requires an administrator, even for a subtitle manager.
	deletePath := "/Videos/" + f.movie.ID.String() + "/Subtitles/0"
	f.expect(f.do(http.MethodDelete, deletePath, f.manager, ""), http.StatusForbidden)
	f.expect(f.do(http.MethodDelete, deletePath, f.viewer, ""), http.StatusForbidden)
	readSubtitleFile(t, filepath.Join(f.mediaDir, "Feature (2020).eng.srt"))
	f.expect(f.do(http.MethodDelete, deletePath, f.admin, ""), http.StatusNoContent)
}

func TestUploadSubtitleRejectsBadRequests(t *testing.T) {
	f := newSubtitleWriteFixture(t, true)
	target := "/Videos/" + f.movie.ID.String() + "/Subtitles"
	data := base64.StdEncoding.EncodeToString([]byte(subtitleWriteSRT))
	for name, body := range map[string]string{
		"unsupported format":  `{"Language":"eng","Format":"exe","IsForced":false,"IsHearingImpaired":false,"Data":"` + data + `"}`,
		"language separator":  `{"Language":"../eng","Format":"srt","IsForced":false,"IsHearingImpaired":false,"Data":"` + data + `"}`,
		"missing data":        `{"Language":"eng","Format":"srt","IsForced":false,"IsHearingImpaired":false}`,
		"missing is forced":   `{"Language":"eng","Format":"srt","IsHearingImpaired":false,"Data":"` + data + `"}`,
		"data is not base64":  `{"Language":"eng","Format":"srt","IsForced":false,"IsHearingImpaired":false,"Data":"%%%"}`,
		"body is not an DTO":  `[]`,
		"empty language text": `{"Language":"","Format":"srt","IsForced":false,"IsHearingImpaired":false,"Data":"` + data + `"}`,
	} {
		if record := f.do(http.MethodPost, target, f.admin, body); record.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, record.Code)
		}
	}
	missing := "/Videos/" + collUUID(t, 999).String() + "/Subtitles"
	f.expect(f.do(http.MethodPost, missing, f.admin, `{"Language":"eng","Format":"srt","IsForced":false,"IsHearingImpaired":false,"Data":"`+data+`"}`), http.StatusNotFound)
	entries, err := os.ReadDir(f.mediaDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("media folder entries after rejected uploads = %v (%v)", entries, err)
	}
}
