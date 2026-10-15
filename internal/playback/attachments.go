// SPDX-License-Identifier: GPL-2.0-only

package playback

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

// ErrAttachmentNotFound reports a media source without the requested
// attachment, or an attachment that cannot be extracted (upstream
// ResourceNotFoundException).
var ErrAttachmentNotFound = errors.New("attachment not found")

// AttachmentRequest identifies one media attachment.
type AttachmentRequest struct {
	UserID        dto.UUID
	ItemID        dto.UUID
	MediaSourceID string
	Index         int
}

// AttachmentResponse is an open attachment file. The receiver must close File.
type AttachmentResponse struct {
	ContentType string
	File        *os.File
	Size        int64
}

// AttachmentServiceConfig supplies the source selector, the ffmpeg extractor,
// and the cache folder (upstream DataPath/attachments).
type AttachmentServiceConfig struct {
	Sources   SubtitleSources
	Extractor MediaExtractor
	Path      string
}

// AttachmentService ports AttachmentExtractor.GetAttachment: it dumps one MKV
// attachment into the cache on first use and serves the cached file.
type AttachmentService struct {
	sources   SubtitleSources
	extractor MediaExtractor
	root      string
	locks     keyedLocks
}

// NewAttachmentService constructs an attachment service.
func NewAttachmentService(config AttachmentServiceConfig) *AttachmentService {
	return &AttachmentService{sources: config.Sources, extractor: config.Extractor, root: config.Path}
}

// Attachment returns the attachment file and its content type.
func (service *AttachmentService) Attachment(ctx context.Context, request AttachmentRequest) (AttachmentResponse, error) {
	if service == nil || service.sources == nil {
		return AttachmentResponse{}, ErrItemNotFound
	}
	source, err := service.sources.StaticSource(ctx, request.UserID, request.ItemID, request.MediaSourceID)
	if err != nil {
		return AttachmentResponse{}, err
	}
	var attachment *MediaAttachment
	for index := range source.MediaAttachments {
		if source.MediaAttachments[index].Index == request.Index {
			attachment = &source.MediaAttachments[index]
			break
		}
	}
	if attachment == nil || strings.EqualFold(attachment.Codec, "mjpeg") {
		return AttachmentResponse{}, ErrAttachmentNotFound
	}
	path, err := service.extract(ctx, source, *attachment)
	if err != nil {
		return AttachmentResponse{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return AttachmentResponse{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return AttachmentResponse{}, err
	}
	contentType := attachment.MimeType
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	return AttachmentResponse{ContentType: contentType, File: file, Size: info.Size()}, nil
}

// extract ports AttachmentExtractor.ExtractAttachment. ffmpeg dumps into a
// temporary name that is renamed on success, so a reader never sees a
// partial file.
func (service *AttachmentService) extract(ctx context.Context, source MediaSourceInfo, attachment MediaAttachment) (string, error) {
	folder, err := mediaSourceCacheFolder(service.root, source.ID)
	if errors.Is(err, ErrExtractionUnavailable) {
		return "", ErrAttachmentNotFound
	}
	if err != nil {
		return "", err
	}
	path := attachmentCachePath(folder, attachment)

	unlock, err := service.locks.lock(ctx, folder)
	if err != nil {
		return "", err
	}
	defer unlock()
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if service.extractor == nil {
		return "", ErrAttachmentNotFound
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return "", err
	}
	pinned, err := openPinnedSource(source)
	if err != nil {
		return "", err
	}
	defer func() { _ = pinned.Close() }()

	partial := path + ".part"
	if err := os.Remove(partial); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	hasVideoOrAudio := sourceHasVideoOrAudio(source)
	args := encoding.AttachmentDumpArgs(sourcefile.FDPath(extractorInputFD), attachment.Index, partial, hasVideoOrAudio)
	runErr := attachmentDumpResult(service.extractor.Run(ctx, args, pinned), hasVideoOrAudio, pinned)
	if runErr == nil {
		if _, err := os.Stat(partial); err != nil {
			runErr = err
		}
	}
	if runErr != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("ffmpeg attachment extraction failed for %s to %s: %w", source.Path, path, runErr)
	}
	if err := os.Rename(partial, path); err != nil {
		_ = os.Remove(partial)
		return "", err
	}
	return path, nil
}

// ExtractAll ports AttachmentExtractor.ExtractAllAttachments, which
// TranscodeManager.StartFfMpeg runs before a subtitle burn-in: one ffmpeg pass
// dumps every attachment that the cache does not hold yet. It returns the
// cache folder, which the subtitles filter takes as fontsdir.
func (service *AttachmentService) ExtractAll(ctx context.Context, source MediaSourceInfo) (string, error) {
	if service == nil {
		return "", ErrExtractionUnavailable
	}
	folder, err := mediaSourceCacheFolder(service.root, source.ID)
	if err != nil {
		return "", err
	}
	unlock, err := service.locks.lock(ctx, folder)
	if err != nil {
		return "", err
	}
	defer unlock()
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return "", err
	}
	type pending struct{ partial, path string }
	var dumps []encoding.AttachmentDump
	var outputs []pending
	for _, attachment := range source.MediaAttachments {
		if strings.EqualFold(attachment.Codec, "mjpeg") {
			continue
		}
		path := attachmentCachePath(folder, attachment)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		partial := path + ".part"
		if err := os.Remove(partial); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		dumps = append(dumps, encoding.AttachmentDump{Index: attachment.Index, OutputPath: partial})
		outputs = append(outputs, pending{partial: partial, path: path})
	}
	if len(dumps) == 0 {
		return folder, nil
	}
	if service.extractor == nil {
		return "", ErrExtractionUnavailable
	}
	pinned, err := openPinnedSource(source)
	if err != nil {
		return "", err
	}
	defer func() { _ = pinned.Close() }()
	hasVideoOrAudio := sourceHasVideoOrAudio(source)
	args := encoding.AllAttachmentsDumpArgs(sourcefile.FDPath(extractorInputFD), dumps, hasVideoOrAudio)
	runErr := attachmentDumpResult(service.extractor.Run(ctx, args, pinned), hasVideoOrAudio, pinned)
	if runErr == nil {
		for _, output := range outputs {
			if _, err := os.Stat(output.partial); err != nil {
				runErr = err
				break
			}
		}
	}
	if runErr != nil {
		for _, output := range outputs {
			_ = os.Remove(output.partial)
		}
		return "", fmt.Errorf("ffmpeg attachment extraction failed for %s to %s: %w", source.Path, folder, runErr)
	}
	for _, output := range outputs {
		if err := os.Rename(output.partial, output.path); err != nil {
			_ = os.Remove(output.partial)
			return "", err
		}
	}
	return folder, nil
}

// attachmentCachePath ports PathManager.GetAttachmentPath: the safe leaf of
// the attachment file name, or its index.
func attachmentCachePath(folder string, attachment MediaAttachment) string {
	name := safeLeafFileName(attachment.FileName)
	if name == "" {
		name = strconv.Itoa(attachment.Index)
	}
	return filepath.Join(folder, name)
}

func openPinnedSource(source MediaSourceInfo) (*sourcefile.Source, error) {
	return sourcefile.Open(source.Path, sourcefile.Expected{
		Size:       source.FileFacts.Size,
		ModifiedAt: source.FileFacts.ModifiedAt,
		Inode:      source.FileFacts.Inode,
		ChangedAt:  source.FileFacts.ChangedAt,
	})
}

func sourceHasVideoOrAudio(source MediaSourceInfo) bool {
	for _, stream := range source.MediaStreams {
		if stream.Type == StreamTypeVideo || stream.Type == StreamTypeAudio {
			return true
		}
	}
	return false
}

// attachmentDumpResult accepts exit code 1 for an input without a video or
// audio stream: ffmpeg then has no output and exits 1 after the dump, and
// upstream accepts that code. It then checks that the source did not change.
func attachmentDumpResult(runErr error, hasVideoOrAudio bool, pinned *sourcefile.Source) error {
	var exitErr interface{ ExitCode() int }
	if runErr != nil && !hasVideoOrAudio && errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
		runErr = nil
	}
	if runErr == nil {
		runErr = verifySubtitleSource(pinned)
	}
	return runErr
}

// safeLeafFileName ports PathHelper.GetSafeLeafFileName. It returns "" when
// the name has no usable leaf.
func safeLeafFileName(name string) string {
	if name == "" {
		return ""
	}
	leaf := name
	if slash := strings.LastIndexByte(leaf, '/'); slash >= 0 {
		leaf = leaf[slash+1:]
	}
	if leaf == "" || leaf == "." || leaf == ".." {
		return ""
	}
	return leaf
}
