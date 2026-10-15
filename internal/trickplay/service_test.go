// SPDX-License-Identifier: GPL-2.0-only

package trickplay_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/trickplay"
	"golang.org/x/sys/unix"
)

type readerMetadataCall struct {
	itemID dto.UUID
	width  int
}

type readerMetadataSource struct {
	metadata trickplay.Metadata
	err      error
	calls    []readerMetadataCall
	access   *readerAccessSource
}

func (source *readerMetadataSource) Trickplay(_ context.Context, itemID dto.UUID, width int) (trickplay.Metadata, error) {
	if source.access != nil && len(source.access.calls) == 0 {
		return trickplay.Metadata{}, errors.New("metadata was queried before authorization")
	}
	source.calls = append(source.calls, readerMetadataCall{itemID: itemID, width: width})
	if source.err != nil {
		return trickplay.Metadata{}, source.err
	}
	return source.metadata, nil
}

type readerPublicationStore struct {
	mu            sync.Mutex
	metadata      *trickplay.Metadata
	commitEntered chan struct{}
	commitRelease chan struct{}
	commitErr     error
	releaseOnce   sync.Once
}

func (store *readerPublicationStore) ReplaceTrickplay(_ context.Context, metadata trickplay.Metadata) error {
	if store.commitEntered != nil {
		close(store.commitEntered)
		<-store.commitRelease
	}
	if store.commitErr != nil {
		return store.commitErr
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	copy := metadata
	store.metadata = &copy
	return nil
}

func (store *readerPublicationStore) ReadTrickplay(_ context.Context, itemID dto.UUID, width int) (*trickplay.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.metadata == nil || store.metadata.ItemID != itemID || store.metadata.Width != width {
		return nil, nil
	}
	copy := *store.metadata
	return &copy, nil
}

func (store *readerPublicationStore) Trickplay(ctx context.Context, itemID dto.UUID, width int) (trickplay.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return trickplay.Metadata{}, err
	}
	metadata, err := store.ReadTrickplay(ctx, itemID, width)
	if err != nil {
		return trickplay.Metadata{}, err
	}
	if metadata == nil {
		return trickplay.Metadata{}, trickplay.ErrNotFound
	}
	return *metadata, nil
}

func (store *readerPublicationStore) unblockCommit() {
	if store.commitRelease != nil {
		store.releaseOnce.Do(func() { close(store.commitRelease) })
	}
}

type readerPublicationExtractor struct {
	frameCount int
}

func (extractor readerPublicationExtractor) Extract(ctx context.Context, request trickplay.FrameRequest) (trickplay.FrameExtraction, error) {
	if err := ctx.Err(); err != nil {
		return trickplay.FrameExtraction{}, err
	}
	names := make([]string, extractor.frameCount)
	for index := range names {
		name := fmt.Sprintf("%08d.jpg", index+1)
		file, err := request.Output.CreateExclusive(name, 0o644)
		if err != nil {
			return trickplay.FrameExtraction{}, err
		}
		if _, err := file.Write([]byte("frame")); err != nil {
			_ = file.Close()
			return trickplay.FrameExtraction{}, err
		}
		if err := file.Close(); err != nil {
			return trickplay.FrameExtraction{}, err
		}
		names[index] = name
	}
	return trickplay.FrameExtraction{Names: names, Height: 180}, nil
}

type readerPublicationComposer struct {
	contents []byte
}

func (composer readerPublicationComposer) Compose(ctx context.Context, request images.TileRequest) (images.TileResult, error) {
	if err := ctx.Err(); err != nil {
		return images.TileResult{}, err
	}
	file, err := request.OutputDir.CreateExclusive(request.OutputName, 0o644)
	if err != nil {
		return images.TileResult{}, err
	}
	if _, err := file.Write(composer.contents); err != nil {
		_ = file.Close()
		return images.TileResult{}, err
	}
	if err := file.Close(); err != nil {
		return images.TileResult{}, err
	}
	return images.TileResult{Path: filepath.Join(request.OutputDir.Path(), request.OutputName), ByteSize: int64(len(composer.contents))}, nil
}

func readerPublicationRequest(t *testing.T, itemID dto.UUID) trickplay.GenerateRequest {
	t.Helper()
	videoWidth := 319
	runtimeTicks := int64(30_000_000)
	return trickplay.GenerateRequest{
		ItemID: itemID, Source: openPinnedGeneratorSource(t, writePinnedGeneratorSource(t, t.TempDir(), "source.mkv", "pinned test source")), SourceRevision: "test-source-revision", VideoWidth: &videoWidth,
		RunTimeTicks: &runtimeTicks, RequestedWidth: 321, TileWidth: 3, TileHeight: 2, IntervalMilliseconds: 1_500,
	}
}

type readerAccessCall struct {
	userID        dto.UUID
	itemID        dto.UUID
	administrator bool
}

type readerAccessSource struct {
	expectedUserID        dto.UUID
	expectedItemID        dto.UUID
	expectedAdministrator bool
	visible               bool
	err                   error
	calls                 []readerAccessCall
}

func (source *readerAccessSource) Visible(_ context.Context, userID, itemID dto.UUID, administrator bool) (bool, error) {
	source.calls = append(source.calls, readerAccessCall{userID: userID, itemID: itemID, administrator: administrator})
	if !source.expectedUserID.IsNil() && (userID != source.expectedUserID || itemID != source.expectedItemID || administrator != source.expectedAdministrator) {
		return false, fmt.Errorf("authorization received actor (%s, administrator=%t) for item %s, want (%s, administrator=%t) for item %s", userID, administrator, itemID, source.expectedUserID, source.expectedAdministrator, source.expectedItemID)
	}
	return source.visible, source.err
}

type readerAllowAccess struct{}

func (readerAllowAccess) Visible(context.Context, dto.UUID, dto.UUID, bool) (bool, error) {
	return true, nil
}

func readerPublication() *trickplay.PublicationCoordinator {
	return trickplay.NewPublicationCoordinator()
}

func TestReaderServiceRendersPlaylistForRequestedMediaSourceWithEscapedAPIKey(t *testing.T) {
	const apiKey = "opaque+key&unescaped=✓"
	itemID := readerUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	mediaSourceID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := readerMetadata(mediaSourceID, 320, 5)
	source := &readerMetadataSource{metadata: metadata}
	service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: source, Access: readerAllowAccess{}, Publication: readerPublication(), Root: t.TempDir()})

	playlist, found, err := service.Playlist(t.Context(), trickplay.ReadRequest{
		ItemID:        itemID,
		MediaSourceID: &mediaSourceID,
		Width:         metadata.Width,
		APIKey:        apiKey,
	})
	if err != nil {
		t.Fatalf("Playlist(): %v", err)
	}
	if !found {
		t.Fatal("Playlist() found = false, want playlist for persisted thumbnails")
	}
	wantURI := "0.jpg?MediaSourceId=" + mediaSourceID.String() + "&ApiKey=opaque%2Bkey%26unescaped%3D%E2%9C%93"
	if !strings.Contains(playlist, wantURI+"\n") {
		t.Fatalf("playlist does not contain the canonical tile URI %q:\n%s", wantURI, playlist)
	}
	uri, parseErr := url.Parse("https://example.invalid/" + wantURI)
	if parseErr != nil {
		t.Fatalf("parse tile URI: %v", parseErr)
	}
	query := uri.Query()
	if got := query["ApiKey"]; len(got) != 1 || got[0] != apiKey {
		t.Fatalf("ApiKey values = %#v, want exactly %#v", got, []string{apiKey})
	}
	if got := query["MediaSourceId"]; len(got) != 1 || got[0] != mediaSourceID.String() {
		t.Fatalf("MediaSourceId values = %#v, want unchanged media source ID", got)
	}
	if len(query) != 2 {
		t.Fatalf("query fields = %#v, want only MediaSourceId and ApiKey", query)
	}
	if got, want := source.calls, []readerMetadataCall{{itemID: mediaSourceID, width: metadata.Width}}; !readerMetadataCallsEqual(got, want) {
		t.Fatalf("metadata calls = %#v, want %#v", got, want)
	}
}

func TestReaderServicePlaylistMapsAbsentAndEmptyTrickplayToNoPlaylist(t *testing.T) {
	itemID := readerUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	for _, test := range []struct {
		name       string
		source     *readerMetadataSource
		wantErr    error
		wantFound  bool
		wantOutput string
	}{
		{
			name:    "metadata is absent",
			source:  &readerMetadataSource{err: trickplay.ErrNotFound},
			wantErr: trickplay.ErrNotFound,
		},
		{
			name:       "metadata records zero thumbnails",
			source:     &readerMetadataSource{metadata: readerMetadata(itemID, 320, 0)},
			wantFound:  false,
			wantOutput: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: test.source, Access: readerAllowAccess{}, Publication: readerPublication(), Root: t.TempDir()})
			playlist, found, err := service.Playlist(t.Context(), trickplay.ReadRequest{ItemID: itemID, Width: 320, APIKey: "raw"})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Playlist() error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
			if test.wantErr != nil {
				return
			}
			if found != test.wantFound || playlist != test.wantOutput {
				t.Fatalf("Playlist() = (%q, %t), want (%q, %t)", playlist, found, test.wantOutput, test.wantFound)
			}
		})
	}
}

func TestReaderServiceOpensExactOwnedTileAsClosableJPEGArtifact(t *testing.T) {
	itemID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := readerMetadata(itemID, 320, 5)
	root := t.TempDir()
	wantJPEG := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	readerWriteTile(t, root, metadata, 0, wantJPEG)
	wantModified := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(readerTilePath(root, metadata, 0), wantModified, wantModified); err != nil {
		t.Fatalf("set tile modification time: %v", err)
	}

	// A plausible, but wrong, pre-bucket layout must not win over the owned
	// PathManager layout rooted by the first two dashed UUID characters.
	legacy := filepath.Join(root, itemID.Dashed(), fmt.Sprintf("%d - %dx%d", metadata.Width, metadata.TileWidth, metadata.TileHeight), "0.jpg")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatalf("create legacy tile directory: %v", err)
	}
	if err := os.WriteFile(legacy, []byte("wrong artifact"), 0o644); err != nil {
		t.Fatalf("write legacy tile: %v", err)
	}

	service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: &readerMetadataSource{metadata: metadata}, Access: readerAllowAccess{}, Publication: readerPublication(), Root: root})
	artifact, err := service.Tile(t.Context(), trickplay.ReadRequest{ItemID: itemID, Width: metadata.Width}, 0)
	if err != nil {
		t.Fatalf("Tile(): %v", err)
	}
	if artifact.ContentType != "image/jpeg" {
		t.Errorf("tile content type = %q, want image/jpeg", artifact.ContentType)
	}
	if artifact.Size != int64(len(wantJPEG)) {
		t.Errorf("tile size = %d, want descriptor size %d", artifact.Size, len(wantJPEG))
	}
	if !artifact.Modified.Equal(wantModified) {
		t.Errorf("tile modified = %s, want descriptor modification time %s", artifact.Modified, wantModified)
	}
	if artifact.Body == nil {
		t.Fatal("tile body = nil, want an open JPEG artifact")
	}
	if offset, seekErr := artifact.Body.Seek(2, io.SeekStart); seekErr != nil || offset != 2 {
		t.Fatalf("seek tile body = (%d, %v), want (2, nil)", offset, seekErr)
	}
	partial := make([]byte, 3)
	if _, readErr := io.ReadFull(artifact.Body, partial); readErr != nil {
		t.Fatalf("read seeked tile body: %v", readErr)
	}
	if string(partial) != string(wantJPEG[2:5]) {
		t.Fatalf("seeked tile bytes = %x, want %x", partial, wantJPEG[2:5])
	}
	if _, seekErr := artifact.Body.Seek(0, io.SeekStart); seekErr != nil {
		t.Fatalf("rewind tile body: %v", seekErr)
	}
	gotJPEG, readErr := io.ReadAll(artifact.Body)
	if readErr != nil {
		t.Fatalf("read tile body: %v", readErr)
	}
	if string(gotJPEG) != string(wantJPEG) {
		t.Fatalf("tile JPEG bytes = %x, want bytes from exact owned layout %x", gotJPEG, wantJPEG)
	}
	if err := artifact.Body.Close(); err != nil {
		t.Fatalf("close tile body: %v", err)
	}
	if _, err := artifact.Body.Read(make([]byte, 1)); err == nil {
		t.Fatal("read after closing tile body succeeded, want closed descriptor error")
	}
}

// A generation token identifies the persisted publication rather than the
// tile's timestamp. Replacements may preserve timestamp precision, so callers
// need the current token from the same metadata read that selected the file.
func TestReaderServicePinsPersistedGenerationTokenToOpenedTile(t *testing.T) {
	itemID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := readerMetadata(itemID, 320, 1)
	metadata.GenerationToken = "generation-first"
	root := t.TempDir()
	path := readerTilePath(root, metadata, 0)
	first := []byte("first JPEG tile")
	second := []byte("next JPEG tile!")
	modified := time.Date(2024, time.February, 29, 12, 34, 56, 0, time.UTC)
	readerWriteTile(t, root, metadata, 0, first)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("set first tile modification time: %v", err)
	}

	source := &readerMetadataSource{metadata: metadata}
	service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: source, Access: readerAllowAccess{}, Publication: readerPublication(), Root: root})
	read := func(wantToken string, wantBody []byte) {
		t.Helper()
		artifact, err := service.Tile(t.Context(), trickplay.ReadRequest{ItemID: itemID, Width: metadata.Width}, 0)
		if err != nil {
			t.Fatalf("Tile(): %v", err)
		}
		defer func() {
			if err := artifact.Body.Close(); err != nil {
				t.Errorf("close tile body: %v", err)
			}
		}()
		if got := artifact.GenerationToken; got != wantToken {
			t.Errorf("tile generation token = %q, want %q", got, wantToken)
		}
		gotBody, err := io.ReadAll(artifact.Body)
		if err != nil {
			t.Fatalf("read tile body: %v", err)
		}
		if string(gotBody) != string(wantBody) {
			t.Errorf("tile body = %q, want %q", gotBody, wantBody)
		}
	}

	read("generation-first", first)
	if err := os.WriteFile(path, second, 0o644); err != nil {
		t.Fatalf("replace tile: %v", err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("preserve replacement modification time: %v", err)
	}
	source.metadata.GenerationToken = "generation-second"
	read("generation-second", second)
}

func TestReaderServiceRejectsReplacementRootPathAfterConstruction(t *testing.T) {
	itemID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	actorID := readerUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	metadata := readerMetadata(itemID, 320, 1)
	workspace := t.TempDir()
	root := filepath.Join(workspace, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create original artifact root: %v", err)
	}
	readerWriteTile(t, root, metadata, 0, []byte("original root tile"))

	access := &readerAccessSource{
		expectedUserID:        actorID,
		expectedItemID:        itemID,
		expectedAdministrator: true,
		visible:               true,
	}
	service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{
		Metadata: &readerMetadataSource{metadata: metadata, access: access}, Access: access, Publication: readerPublication(), Root: root,
	})

	movedRoot := filepath.Join(workspace, "original-root")
	if err := os.Rename(root, movedRoot); err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
			t.Skipf("root rename is unsupported on this filesystem: %v", err)
		}
		t.Fatalf("move original artifact root: %v", err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create replacement artifact root: %v", err)
	}
	const replacement = "replacement root tile"
	readerWriteTile(t, root, metadata, 0, []byte(replacement))

	request := trickplay.ReadRequest{ItemID: itemID, UserID: actorID, Administrator: true, Width: metadata.Width}
	t.Run("playlist", func(t *testing.T) {
		playlist, found, err := service.Playlist(t.Context(), request)
		if !errors.Is(err, trickplay.ErrUnsafeArtifact) {
			t.Fatalf("Playlist() error = %v, want ErrUnsafeArtifact after root replacement", err)
		}
		if found || playlist != "" {
			t.Fatalf("Playlist() = (%q, %t), want rejected empty result", playlist, found)
		}
	})
	t.Run("tile", func(t *testing.T) {
		artifact, err := service.Tile(t.Context(), request, 0)
		if !errors.Is(err, trickplay.ErrUnsafeArtifact) {
			t.Errorf("Tile() error = %v, want ErrUnsafeArtifact after root replacement", err)
		}
		if artifact.Body != nil {
			got, readErr := io.ReadAll(artifact.Body)
			closeErr := artifact.Body.Close()
			if readErr != nil {
				t.Fatalf("read unexpectedly opened replacement tile: %v", readErr)
			}
			if closeErr != nil {
				t.Fatalf("close unexpectedly opened replacement tile: %v", closeErr)
			}
			if string(got) == replacement {
				t.Fatalf("Tile() returned replacement-root bytes %q", got)
			}
			t.Fatalf("Tile() opened bytes %q after root replacement", got)
		}
	})
}

func TestReaderServiceRejectsNegativeAndOutOfRangeTileIndexesEvenWhenFilesExist(t *testing.T) {
	itemID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := readerMetadata(itemID, 320, 5) // 2x2 tiles permit indexes 0 and 1 only.
	root := t.TempDir()
	readerWriteTile(t, root, metadata, -1, []byte("must not serve negative index"))
	readerWriteTile(t, root, metadata, 2, []byte("must not serve beyond final tile"))
	service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: &readerMetadataSource{metadata: metadata}, Access: readerAllowAccess{}, Publication: readerPublication(), Root: root})

	for _, index := range []int{-1, 2} {
		t.Run(fmt.Sprintf("index %d", index), func(t *testing.T) {
			artifact, err := service.Tile(t.Context(), trickplay.ReadRequest{ItemID: itemID, Width: metadata.Width}, index)
			if !errors.Is(err, trickplay.ErrNotFound) {
				t.Fatalf("Tile(index=%d) error = %v, want ErrNotFound", index, err)
			}
			if artifact.Body != nil {
				_ = artifact.Body.Close()
				t.Fatalf("Tile(index=%d) returned an artifact for an invalid index", index)
			}
		})
	}
}

func TestReaderServiceMapsMissingMetadataAndMissingTileToNotFound(t *testing.T) {
	itemID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := readerMetadata(itemID, 320, 1)
	for _, test := range []struct {
		name   string
		source *readerMetadataSource
		root   string
	}{
		{name: "metadata", source: &readerMetadataSource{err: trickplay.ErrNotFound}, root: t.TempDir()},
		{name: "tile file", source: &readerMetadataSource{metadata: metadata}, root: t.TempDir()},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: test.source, Access: readerAllowAccess{}, Publication: readerPublication(), Root: test.root})
			artifact, err := service.Tile(t.Context(), trickplay.ReadRequest{ItemID: itemID, Width: metadata.Width}, 0)
			if !errors.Is(err, trickplay.ErrNotFound) {
				t.Fatalf("Tile() error = %v, want ErrNotFound", err)
			}
			if artifact.Body != nil {
				_ = artifact.Body.Close()
				t.Fatal("Tile() returned an artifact for a missing resource")
			}
		})
	}
}

func TestReaderServiceAuthorizesEffectiveSourceBeforeMetadataAndVisibleReadContinues(t *testing.T) {
	actorID := readerUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	itemID := readerUUID(t, "11112233-4455-6677-8899-aabbccddeeff")
	mediaSourceID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := readerMetadata(mediaSourceID, 320, 1)
	wantJPEG := []byte{0xff, 0xd8, 0xff, 0xd9}

	for _, test := range []struct {
		name string
		read func(*testing.T, *trickplay.ReaderService, trickplay.ReadRequest)
	}{
		{
			name: "playlist",
			read: func(t *testing.T, service *trickplay.ReaderService, request trickplay.ReadRequest) {
				t.Helper()
				playlist, found, err := service.Playlist(t.Context(), request)
				if err != nil {
					t.Fatalf("Playlist(): %v", err)
				}
				if !found || !strings.Contains(playlist, mediaSourceID.String()) {
					t.Fatalf("Playlist() = (%q, %t), want playlist for effective media source %s", playlist, found, mediaSourceID)
				}
			},
		},
		{
			name: "tile",
			read: func(t *testing.T, service *trickplay.ReaderService, request trickplay.ReadRequest) {
				t.Helper()
				artifact, err := service.Tile(t.Context(), request, 0)
				if err != nil {
					t.Fatalf("Tile(): %v", err)
				}
				if artifact.Body == nil {
					t.Fatal("Tile() body = nil, want visible tile artifact")
				}
				defer func() { _ = artifact.Body.Close() }()
				gotJPEG, err := io.ReadAll(artifact.Body)
				if err != nil {
					t.Fatalf("read visible tile: %v", err)
				}
				if string(gotJPEG) != string(wantJPEG) {
					t.Fatalf("visible tile bytes = %x, want %x", gotJPEG, wantJPEG)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			readerWriteTile(t, root, metadata, 0, wantJPEG)
			access := &readerAccessSource{
				expectedUserID:        actorID,
				expectedItemID:        mediaSourceID,
				expectedAdministrator: true,
				visible:               true,
			}
			source := &readerMetadataSource{metadata: metadata, access: access}
			service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: source, Access: access, Publication: readerPublication(), Root: root})

			test.read(t, service, trickplay.ReadRequest{
				ItemID:        itemID,
				MediaSourceID: &mediaSourceID,
				UserID:        actorID,
				Administrator: true,
				Width:         metadata.Width,
			})
			if got, want := source.calls, []readerMetadataCall{{itemID: mediaSourceID, width: metadata.Width}}; !readerMetadataCallsEqual(got, want) {
				t.Fatalf("metadata calls = %#v, want %#v", got, want)
			}
		})
	}
}

func TestReaderServiceHidesInvisibleSourceWithoutMetadataOrTileOpen(t *testing.T) {
	actorID := readerUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	itemID := readerUUID(t, "11112233-4455-6677-8899-aabbccddeeff")
	mediaSourceID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := readerMetadata(mediaSourceID, 320, 1)

	for _, test := range []struct {
		name string
		read func(*trickplay.ReaderService, context.Context, trickplay.ReadRequest) (io.ReadCloser, error)
	}{
		{
			name: "playlist",
			read: func(service *trickplay.ReaderService, ctx context.Context, request trickplay.ReadRequest) (io.ReadCloser, error) {
				_, _, err := service.Playlist(ctx, request)
				return nil, err
			},
		},
		{
			name: "tile",
			read: func(service *trickplay.ReaderService, ctx context.Context, request trickplay.ReadRequest) (io.ReadCloser, error) {
				artifact, err := service.Tile(ctx, request, 0)
				return artifact.Body, err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			readerWriteTile(t, root, metadata, 0, []byte("must remain unopened"))
			access := &readerAccessSource{
				expectedUserID: actorID,
				expectedItemID: mediaSourceID,
				visible:        false,
			}
			source := &readerMetadataSource{metadata: metadata, access: access}
			service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: source, Access: access, Publication: readerPublication(), Root: root})

			body, err := test.read(service, t.Context(), trickplay.ReadRequest{ItemID: itemID, MediaSourceID: &mediaSourceID, UserID: actorID, Width: metadata.Width})
			if !errors.Is(err, trickplay.ErrNotFound) {
				t.Fatalf("invisible %s error = %v, want ErrNotFound", test.name, err)
			}
			if body != nil {
				_ = body.Close()
				t.Fatalf("invisible %s opened a tile artifact", test.name)
			}
			if len(source.calls) != 0 {
				t.Fatalf("metadata calls for invisible source = %#v, want none", source.calls)
			}
		})
	}
}

func TestReaderServicePropagatesAccessFailureWithoutMetadata(t *testing.T) {
	actorID := readerUUID(t, "00112233-4455-6677-8899-aabbccddeeff")
	itemID := readerUUID(t, "11112233-4455-6677-8899-aabbccddeeff")
	mediaSourceID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := readerMetadata(mediaSourceID, 320, 1)
	accessErr := errors.New("access backend unavailable")

	for _, test := range []struct {
		name string
		read func(*trickplay.ReaderService, context.Context, trickplay.ReadRequest) (io.ReadCloser, error)
	}{
		{
			name: "playlist",
			read: func(service *trickplay.ReaderService, ctx context.Context, request trickplay.ReadRequest) (io.ReadCloser, error) {
				_, _, err := service.Playlist(ctx, request)
				return nil, err
			},
		},
		{
			name: "tile",
			read: func(service *trickplay.ReaderService, ctx context.Context, request trickplay.ReadRequest) (io.ReadCloser, error) {
				artifact, err := service.Tile(ctx, request, 0)
				return artifact.Body, err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			access := &readerAccessSource{
				expectedUserID: actorID,
				expectedItemID: mediaSourceID,
				visible:        true,
				err:            accessErr,
			}
			source := &readerMetadataSource{metadata: metadata, access: access}
			service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: source, Access: access, Publication: readerPublication(), Root: t.TempDir()})

			body, err := test.read(service, t.Context(), trickplay.ReadRequest{ItemID: itemID, MediaSourceID: &mediaSourceID, UserID: actorID, Width: metadata.Width})
			if !errors.Is(err, accessErr) {
				t.Fatalf("%s access error = %v, want errors.Is(_, %v)", test.name, err, accessErr)
			}
			if body != nil {
				_ = body.Close()
				t.Fatalf("%s opened a tile artifact after access failure", test.name)
			}
			if len(source.calls) != 0 {
				t.Fatalf("metadata calls after access failure = %#v, want none", source.calls)
			}
		})
	}
}

func TestReaderServiceRejectsFIFOTileWithoutWaitingForWriter(t *testing.T) {
	itemID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := readerMetadata(itemID, 320, 1)
	root := t.TempDir()
	path := readerTilePath(root, metadata, 0)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create FIFO tile directory: %v", err)
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create FIFO tile: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove FIFO tile: %v", err)
		}
	})

	service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: &readerMetadataSource{metadata: metadata}, Access: readerAllowAccess{}, Publication: readerPublication(), Root: root})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	type tileResult struct {
		artifact trickplay.Artifact
		err      error
	}
	result := make(chan tileResult, 1)
	go func() {
		artifact, err := service.Tile(ctx, trickplay.ReadRequest{ItemID: itemID, Width: metadata.Width}, 0)
		result <- tileResult{artifact: artifact, err: err}
	}()

	select {
	case got := <-result:
		if got.artifact.Body != nil {
			_ = got.artifact.Body.Close()
			t.Fatal("Tile() opened a FIFO artifact")
		}
		if !errors.Is(got.err, trickplay.ErrUnsafeArtifact) {
			t.Fatalf("Tile() error = %v, want ErrUnsafeArtifact for FIFO", got.err)
		}
	case <-ctx.Done():
		writer, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == nil {
			_ = unix.Close(writer)
		} else if !errors.Is(err, unix.ENXIO) {
			t.Fatalf("nonblocking FIFO cleanup open: %v", err)
		}
		select {
		case got := <-result:
			if got.artifact.Body != nil {
				_ = got.artifact.Body.Close()
			}
			t.Fatalf("Tile() did not reject the FIFO before its deadline; after unblocking it returned %v", got.err)
		case <-time.After(time.Second):
			t.Fatal("Tile() remained blocked after FIFO cleanup")
		}
	}
}

func TestReaderServiceFailsClosedWhenAccessIsMissing(t *testing.T) {
	itemID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := readerMetadata(itemID, 320, 1)

	for _, test := range []struct {
		name string
		read func(*trickplay.ReaderService, context.Context, trickplay.ReadRequest) (io.ReadCloser, error)
	}{
		{
			name: "playlist",
			read: func(service *trickplay.ReaderService, ctx context.Context, request trickplay.ReadRequest) (io.ReadCloser, error) {
				_, _, err := service.Playlist(ctx, request)
				return nil, err
			},
		},
		{
			name: "tile",
			read: func(service *trickplay.ReaderService, ctx context.Context, request trickplay.ReadRequest) (io.ReadCloser, error) {
				artifact, err := service.Tile(ctx, request, 0)
				return artifact.Body, err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			readerWriteTile(t, root, metadata, 0, []byte("must remain undiscoverable"))
			source := &readerMetadataSource{metadata: metadata}
			service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: source, Publication: readerPublication(), Root: root})

			body, err := test.read(service, t.Context(), trickplay.ReadRequest{ItemID: itemID, Width: metadata.Width})
			if !errors.Is(err, trickplay.ErrNotFound) {
				t.Fatalf("missing Access %s error = %v, want ErrNotFound", test.name, err)
			}
			if body != nil {
				_ = body.Close()
				t.Fatalf("missing Access %s returned a tile artifact", test.name)
			}
			if len(source.calls) != 0 {
				t.Fatalf("metadata calls with missing Access = %#v, want none", source.calls)
			}
		})
	}
}

func TestReaderServiceFailsClosedWhenPublicationIsMissing(t *testing.T) {
	itemID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := readerMetadata(itemID, 320, 1)
	root := t.TempDir()
	readerWriteTile(t, root, metadata, 0, []byte("must remain undiscoverable"))
	source := &readerMetadataSource{metadata: metadata}
	service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: source, Access: readerAllowAccess{}, Root: root})

	artifact, err := service.Tile(t.Context(), trickplay.ReadRequest{ItemID: itemID, Width: metadata.Width}, 0)
	if !errors.Is(err, trickplay.ErrNotFound) {
		t.Fatalf("Tile() without Publication error = %v, want ErrNotFound", err)
	}
	if artifact.Body != nil {
		_ = artifact.Body.Close()
		t.Fatal("Tile() without Publication opened an artifact")
	}
	if len(source.calls) != 0 {
		t.Fatalf("metadata calls without Publication = %#v, want none", source.calls)
	}
}

func TestReaderServiceCoordinatesPublicationAcrossCommitAndRollback(t *testing.T) {
	itemID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	oldOnlyTile := []byte("old-only-tile")
	commitErr := errors.New("commit rejected")

	for _, test := range []struct {
		name         string
		commitErr    error
		wantReadErr  error
		wantTileBody []byte
	}{
		{
			name:        "commit publishes metadata that removes the old-only index",
			wantReadErr: trickplay.ErrNotFound,
		},
		{
			name:         "failed commit rolls back old metadata and artifacts",
			commitErr:    commitErr,
			wantTileBody: oldOnlyTile,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			oldMetadata := readerMetadata(itemID, 318, 7)
			oldMetadata.TileWidth = 3
			oldMetadata.TileHeight = 2
			readerWriteTile(t, root, oldMetadata, 0, []byte("old-first-tile"))
			readerWriteTile(t, root, oldMetadata, 1, oldOnlyTile)

			publication := readerPublication()
			store := &readerPublicationStore{
				metadata:      &oldMetadata,
				commitEntered: make(chan struct{}),
				commitRelease: make(chan struct{}),
				commitErr:     test.commitErr,
			}
			generator, err := trickplay.NewGenerator(trickplay.GeneratorConfig{
				Root: root, Extractor: readerPublicationExtractor{frameCount: 1}, Composer: readerPublicationComposer{contents: []byte("fresh-only-tile")},
				Store: store, Reader: store, Publication: publication,
			})
			if err != nil {
				t.Fatalf("NewGenerator(): %v", err)
			}
			service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{
				Metadata: store, Access: readerAllowAccess{}, Publication: publication, Root: root,
			})

			generation := make(chan error, 1)
			generationDone := make(chan struct{})
			go func() {
				_, err := generator.Generate(t.Context(), readerPublicationRequest(t, itemID))
				generation <- err
				close(generationDone)
			}()
			t.Cleanup(func() {
				store.unblockCommit()
				select {
				case <-generationDone:
				case <-time.After(time.Second):
					t.Error("generation did not finish after commit cleanup")
				}
			})

			select {
			case <-store.commitEntered:
			case <-time.After(time.Second):
				t.Fatal("generation did not reach the blocked metadata commit")
			}

			type tileResult struct {
				artifact trickplay.Artifact
				err      error
			}
			readerStarted := make(chan struct{})
			reader := make(chan tileResult, 1)
			go func() {
				close(readerStarted)
				artifact, err := service.Tile(t.Context(), trickplay.ReadRequest{ItemID: itemID, Width: oldMetadata.Width}, 1)
				reader <- tileResult{artifact: artifact, err: err}
			}()
			<-readerStarted
			select {
			case result := <-reader:
				if result.artifact.Body != nil {
					_ = result.artifact.Body.Close()
				}
				t.Fatalf("Tile() returned during the blocked commit: %v, want it to wait for a coherent generation", result.err)
			case <-time.After(500 * time.Millisecond):
			}

			store.unblockCommit()
			generationErr := <-generation
			if test.commitErr == nil {
				if generationErr != nil {
					t.Fatalf("Generate(): %v", generationErr)
				}
			} else if !errors.Is(generationErr, test.commitErr) {
				t.Fatalf("Generate() error = %v, want errors.Is(_, %v)", generationErr, test.commitErr)
			}

			select {
			case result := <-reader:
				if !errors.Is(result.err, test.wantReadErr) {
					if result.artifact.Body != nil {
						_ = result.artifact.Body.Close()
					}
					t.Fatalf("Tile() after publication settled error = %v, want errors.Is(_, %v)", result.err, test.wantReadErr)
				}
				if test.wantReadErr != nil {
					if result.artifact.Body != nil {
						_ = result.artifact.Body.Close()
						t.Fatal("Tile() returned an artifact for the removed index")
					}
					return
				}
				if result.artifact.Body == nil {
					t.Fatal("Tile() body = nil after rollback restored the old generation")
				}
				defer func() { _ = result.artifact.Body.Close() }()
				body, err := io.ReadAll(result.artifact.Body)
				if err != nil {
					t.Fatalf("read restored old-only tile: %v", err)
				}
				if string(body) != string(test.wantTileBody) {
					t.Fatalf("restored old-only tile = %q, want %q", body, test.wantTileBody)
				}
			case <-time.After(time.Second):
				t.Fatal("Tile() did not resume after publication settled")
			}
		})
	}
}

func TestReaderServiceHidesInvalidLookupIdentityAndWidth(t *testing.T) {
	itemID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	var zeroID dto.UUID

	for _, test := range []struct {
		name    string
		request trickplay.ReadRequest
	}{
		{name: "missing item identity", request: trickplay.ReadRequest{Width: 320}},
		{name: "missing media source override", request: trickplay.ReadRequest{ItemID: itemID, MediaSourceID: &zeroID, Width: 320}},
		{name: "zero rendition width", request: trickplay.ReadRequest{ItemID: itemID, Width: 0}},
		{name: "negative rendition width", request: trickplay.ReadRequest{ItemID: itemID, Width: -320}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, operation := range []struct {
				name string
				read func(*trickplay.ReaderService, context.Context, trickplay.ReadRequest) (io.ReadCloser, error)
			}{
				{
					name: "playlist",
					read: func(service *trickplay.ReaderService, ctx context.Context, request trickplay.ReadRequest) (io.ReadCloser, error) {
						_, _, err := service.Playlist(ctx, request)
						return nil, err
					},
				},
				{
					name: "tile",
					read: func(service *trickplay.ReaderService, ctx context.Context, request trickplay.ReadRequest) (io.ReadCloser, error) {
						artifact, err := service.Tile(ctx, request, 0)
						return artifact.Body, err
					},
				},
			} {
				t.Run(operation.name, func(t *testing.T) {
					source := &readerMetadataSource{metadata: readerMetadata(itemID, 320, 1)}
					service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: source, Access: readerAllowAccess{}, Publication: readerPublication(), Root: t.TempDir()})

					body, err := operation.read(service, t.Context(), test.request)
					if !errors.Is(err, trickplay.ErrNotFound) {
						t.Fatalf("%s error = %v, want ErrNotFound", operation.name, err)
					}
					if body != nil {
						_ = body.Close()
						t.Fatalf("%s opened an artifact for invalid lookup", operation.name)
					}
					if len(source.calls) != 0 {
						t.Fatalf("metadata calls for invalid lookup = %#v, want none", source.calls)
					}
				})
			}
		})
	}
}

func TestReaderServiceRejectsDirectoryAndFileSymlinkEscapes(t *testing.T) {
	itemID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	metadata := readerMetadata(itemID, 320, 1)

	for _, test := range []struct {
		name  string
		setup func(*testing.T, string, trickplay.Metadata)
	}{
		{
			name: "resolution directory points outside owned root",
			setup: func(t *testing.T, root string, metadata trickplay.Metadata) {
				t.Helper()
				outside := t.TempDir()
				if err := os.WriteFile(filepath.Join(outside, "0.jpg"), []byte("outside directory tile"), 0o644); err != nil {
					t.Fatalf("write outside tile: %v", err)
				}
				resolution := readerResolutionPath(root, metadata)
				if err := os.MkdirAll(filepath.Dir(resolution), 0o755); err != nil {
					t.Fatalf("create resolution parent: %v", err)
				}
				if err := os.Symlink(outside, resolution); err != nil {
					t.Fatalf("symlink resolution directory: %v", err)
				}
			},
		},
		{
			name: "tile file points outside owned root",
			setup: func(t *testing.T, root string, metadata trickplay.Metadata) {
				t.Helper()
				outside := filepath.Join(t.TempDir(), "outside.jpg")
				if err := os.WriteFile(outside, []byte("outside file tile"), 0o644); err != nil {
					t.Fatalf("write outside tile: %v", err)
				}
				path := readerTilePath(root, metadata, 0)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("create tile directory: %v", err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatalf("symlink tile file: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root, metadata)
			service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: &readerMetadataSource{metadata: metadata}, Access: readerAllowAccess{}, Publication: readerPublication(), Root: root})
			artifact, err := service.Tile(t.Context(), trickplay.ReadRequest{ItemID: itemID, Width: metadata.Width}, 0)
			if !errors.Is(err, trickplay.ErrUnsafeArtifact) {
				t.Fatalf("Tile() error = %v, want ErrUnsafeArtifact", err)
			}
			if artifact.Body != nil {
				_ = artifact.Body.Close()
				t.Fatal("Tile() opened a symlink escape artifact")
			}
		})
	}
}

func TestReaderServiceHonorsPreCanceledContextBeforeMetadataOrFilesystemAccess(t *testing.T) {
	itemID := readerUUID(t, "b6d1efb1-7908-4e4e-92ce-8fa5d40f8b4f")
	for _, test := range []struct {
		name string
		call func(*trickplay.ReaderService, context.Context, trickplay.ReadRequest) (io.ReadCloser, error)
	}{
		{
			name: "playlist",
			call: func(service *trickplay.ReaderService, ctx context.Context, request trickplay.ReadRequest) (io.ReadCloser, error) {
				_, _, err := service.Playlist(ctx, request)
				return nil, err
			},
		},
		{
			name: "tile",
			call: func(service *trickplay.ReaderService, ctx context.Context, request trickplay.ReadRequest) (io.ReadCloser, error) {
				artifact, err := service.Tile(ctx, request, 0)
				return artifact.Body, err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &readerMetadataSource{metadata: readerMetadata(itemID, 320, 1)}
			service := trickplay.NewReaderService(trickplay.ReaderServiceConfig{Metadata: source, Access: readerAllowAccess{}, Publication: readerPublication(), Root: t.TempDir()})
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			body, err := test.call(service, ctx, trickplay.ReadRequest{ItemID: itemID, Width: 320})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s error = %v, want context.Canceled", test.name, err)
			}
			if body != nil {
				_ = body.Close()
				t.Fatalf("%s opened an artifact after cancellation", test.name)
			}
			if len(source.calls) != 0 {
				t.Fatalf("metadata calls after cancellation = %#v, want no work after caller canceled", source.calls)
			}
		})
	}
}

func readerMetadata(itemID dto.UUID, width, thumbnails int) trickplay.Metadata {
	return trickplay.Metadata{
		ItemID:         itemID,
		Width:          width,
		Height:         180,
		TileWidth:      2,
		TileHeight:     2,
		ThumbnailCount: thumbnails,
		Interval:       1_500,
		Bandwidth:      1_335,
	}
}

func readerUUID(t *testing.T, text string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(text)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", text, err)
	}
	return id
}

func readerResolutionPath(root string, metadata trickplay.Metadata) string {
	id := metadata.ItemID.Dashed()
	return filepath.Join(root, id[:2], id, fmt.Sprintf("%d - %dx%d", metadata.Width, metadata.TileWidth, metadata.TileHeight))
}

func readerTilePath(root string, metadata trickplay.Metadata, index int) string {
	return filepath.Join(readerResolutionPath(root, metadata), fmt.Sprintf("%d.jpg", index))
}

func readerWriteTile(t *testing.T, root string, metadata trickplay.Metadata, index int, contents []byte) {
	t.Helper()
	path := readerTilePath(root, metadata, index)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create tile directory: %v", err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("write tile: %v", err)
	}
}

func readerMetadataCallsEqual(got, want []readerMetadataCall) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
