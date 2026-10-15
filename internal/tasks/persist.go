// SPDX-License-Identifier: GPL-2.0-only

package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// persistedTrigger is the on-disk JSON shape for trigger overrides. It uses
// the existing TriggerInfo wire tags (PascalCase) for consistency with the
// wire projection, matching upstream's ScheduledTaskWorker.js files.
type persistedTrigger = TriggerInfo

// persist is the per-scheduler persistence layer. It reads and writes
// trigger overrides and last-result records as PascalCase JSON, one file per
// task, named by the dashed form of the task ID.
type persist struct {
	configDir string
	dataDir   string
}

func newPersist(configDir, dataDir string) *persist {
	return &persist{configDir: configDir, dataDir: dataDir}
}

// ensureDirs creates the trigger and result directories at NewScheduler time.
func (p *persist) ensureDirs() error {
	if err := os.MkdirAll(p.configDir, 0o700); err != nil {
		return fmt.Errorf("tasks: create trigger dir: %w", err)
	}
	if err := os.MkdirAll(p.dataDir, 0o700); err != nil {
		return fmt.Errorf("tasks: create result dir: %w", err)
	}
	return nil
}

// dashedID converts a 32-hex task ID to the dashed GUID form used for
// filenames: 7738148ffcd07979c7ceb148e06b3aed →
// 7738148f-fcd0-7979-c7ce-b148e06b3aed.
func dashedID(id string) string {
	if len(id) != 32 {
		return id
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", id[0:8], id[8:12], id[12:16], id[16:20], id[20:32])
}

func (p *persist) triggerPath(id string) string {
	return filepath.Join(p.configDir, dashedID(id)+".js")
}

func (p *persist) resultPath(id string) string {
	return filepath.Join(p.dataDir, dashedID(id)+".js")
}

// saveTriggers writes trigger overrides as PascalCase JSON. It only writes
// when UpdateTriggers is called (defaults are never persisted). The write is
// atomic: write to *.tmp, fsync, os.Rename.
func (p *persist) saveTriggers(id string, triggers []TriggerInfo) error {
	body, err := json.Marshal(triggers)
	if err != nil {
		return fmt.Errorf("tasks: marshal triggers for %s: %w", id, err)
	}
	return atomicWrite(p.triggerPath(id), body)
}

// loadTriggers reads persisted trigger overrides. A missing file returns nil,
// nil (no overrides — use defaults). A corrupt/unparseable file is logged and
// also returns nil, nil (upstream behavior).
func (p *persist) loadTriggers(id string) ([]TriggerInfo, error) {
	body, err := os.ReadFile(p.triggerPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("tasks: read triggers for %s: %w", id, err)
	}
	var triggers []TriggerInfo
	if err := json.Unmarshal(body, &triggers); err != nil {
		log.Printf("tasks: corrupt trigger file for %s: %v; using defaults", id, err)
		return nil, nil
	}
	return triggers, nil
}

// saveResult writes the last execution result as PascalCase JSON.
func (p *persist) saveResult(id string, result Result) error {
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("tasks: marshal result for %s: %w", id, err)
	}
	return atomicWrite(p.resultPath(id), body)
}

// loadResult reads the last execution result. A missing file returns nil, nil.
// A corrupt/unparseable file is logged and returns nil, nil.
func (p *persist) loadResult(id string) (*Result, error) {
	body, err := os.ReadFile(p.resultPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("tasks: read result for %s: %w", id, err)
	}
	var result Result
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("tasks: corrupt result file for %s: %v; no last result", id, err)
		return nil, nil
	}
	return &result, nil
}

func atomicWrite(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("tasks: create dir %s: %w", dir, err)
	}
	tmp := filepath.Join(dir, strings.TrimSuffix(filepath.Base(path), ".js")+".tmp")
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("tasks: write temp file: %w", err)
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("tasks: open temp for fsync: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("tasks: fsync temp: %w", err)
	}
	f.Close()
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("tasks: rename temp: %w", err)
	}
	return nil
}

// newResult constructs a Result from an execution outcome.
func newResult(id, key, name string, start, end time.Time, status, errMsg, longErrMsg string) Result {
	return Result{
		StartTimeUTC:     dto.NewTime(start),
		EndTimeUTC:       dto.NewTime(end),
		Status:           status,
		Name:             name,
		Key:              key,
		ID:               id,
		ErrorMessage:     errMsg,
		LongErrorMessage: longErrMsg,
	}
}
