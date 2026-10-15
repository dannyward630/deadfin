// SPDX-License-Identifier: GPL-2.0-only

package images

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// SourceConverter converts a non-native source into a raster libvips can
// decode. Process owns the output path and removes it after processing.
//
// Jellyfin's pinned upstream image path does not implement this conversion;
// this is the explicit Deadfin WP-P3-01 contract.
type SourceConverter interface {
	Convert(ctx context.Context, inputPath, outputPath string) error
}

// FFmpegConverter converts an unsupported source's first video frame to PNG.
// Path must name an ffmpeg executable.
type FFmpegConverter struct {
	Path string
}

// Convert runs ffmpeg with a fixed, non-interactive argument list and verifies
// that it produced an owned PNG raster.
func (converter FFmpegConverter) Convert(ctx context.Context, inputPath, outputPath string) (err error) {
	if strings.TrimSpace(converter.Path) == "" {
		return errors.New("ffmpeg path is empty")
	}
	if inputPath == "" {
		return errors.New("ffmpeg input path is empty")
	}
	if outputPath == "" {
		return errors.New("ffmpeg output path is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	command := exec.CommandContext(ctx, converter.Path,
		"-nostdin",
		"-y",
		"-i", inputPath,
		"-map", "0:v:0",
		"-frames:v", "1",
		"-c:v", "png",
		outputPath,
	)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("run ffmpeg: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	output, err := os.Lstat(outputPath)
	if err != nil {
		return fmt.Errorf("lstat ffmpeg output %q: %w", outputPath, err)
	}
	if !output.Mode().IsRegular() || output.Size() == 0 {
		return fmt.Errorf("invalid ffmpeg output %q", outputPath)
	}
	file, err := os.Open(outputPath)
	if err != nil {
		return fmt.Errorf("open ffmpeg output %q: %w", outputPath, err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if _, format, err := image.DecodeConfig(file); err != nil || format != "png" {
		if err != nil {
			return fmt.Errorf("decode ffmpeg output %q: %w", outputPath, err)
		}
		return fmt.Errorf("invalid ffmpeg output format %q", format)
	}
	return nil
}
