// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

// ExtractorSourceFD is the descriptor that carries a pinned source into an
// extraction job. Pass sourcefile.FDPath(ExtractorSourceFD) as the -i operand.
const ExtractorSourceFD = 3

const extractorStderrLimit = 64 << 10

// ExitError reports an ffmpeg extraction job that exited with a nonzero code.
type ExitError struct {
	Code   int
	Stderr string
}

// ExitCode returns the ffmpeg exit code.
func (e *ExitError) ExitCode() int { return e.Code }

func (e *ExitError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("ffmpeg exited with code %d", e.Code)
	}
	return fmt.Sprintf("ffmpeg exited with code %d: %s", e.Code, e.Stderr)
}

// Extractor runs one-shot ffmpeg jobs that write files: subtitle cache
// extraction and attachment dumps. It holds no state between calls and is safe
// for concurrent use.
type Extractor struct {
	Path string
}

// Run runs ffmpeg with args and waits for it to exit. A non-nil source is
// passed as descriptor ExtractorSourceFD; the caller keeps ownership. The
// context owns the process lifetime: cancellation kills the whole process group,
// and Run returns only after ffmpeg is reaped.
func (e Extractor) Run(ctx context.Context, args []string, source *sourcefile.Source) error {
	if strings.TrimSpace(e.Path) == "" {
		return errors.New("ffmpeg path is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, e.Path, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	stderr := cappedBuffer{limit: extractorStderrLimit}
	command.Stderr = &stderr
	if source != nil {
		if err := source.Rewind(); err != nil {
			return fmt.Errorf("rewinding ffmpeg source: %w", err)
		}
		command.ExtraFiles = []*os.File{source.File()}
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("starting ffmpeg: %w", err)
	}
	waitErr := command.Wait()
	// Wait reaps only the leader. Kill the group on every path so that no
	// helper process keeps the source or an output file open.
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if err := ctx.Err(); err != nil {
		return err
	}
	if waitErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return &ExitError{Code: exitErr.ExitCode(), Stderr: strings.TrimSpace(string(stderr.Bytes()))}
	}
	return fmt.Errorf("ffmpeg: %w", waitErr)
}
