// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/tasks"
	"github.com/deadfin-org/deadfin/internal/trickplay"
)

// TestP6TrickplayTaskGeneratorMapsGenerationFacts verifies the process
// composition preserves every generation fact while crossing into the
// generator domain request.
func TestP6TrickplayTaskGeneratorMapsGenerationFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		rate float32
	}{
		{name: "23.976fps", rate: 23.976},
		{name: "24fps", rate: 24},
		{name: "60fps", rate: 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			videoWidth := 1_920
			runtimeTicks := int64(1_234_567_890)
			fixture := p6WriteSourceFixture(t, "feature.mkv", "task generation source")
			generation := tasks.Generation{
				Candidate: tasks.Candidate{
					ID:                 dto.UUID{0x8a, 0x73, 0x04, 0x11, 0x44, 0x55, 0x46, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff},
					SourcePath:         fixture.path,
					SourceRevision:     "adapter-source-revision-" + tc.name,
					SourceSize:         &fixture.size,
					SourceModified:     &fixture.modified,
					SourceInode:        &fixture.inode,
					SourceChangedAt:    &fixture.changedAt,
					VideoWidth:         &videoWidth,
					RunTimeTicks:       &runtimeTicks,
					ReferenceFrameRate: &tc.rate,
				},
				SourceRevision:       "adapter-source-revision-" + tc.name,
				RequestedWidth:       640,
				TileWidth:            12,
				TileHeight:           8,
				IntervalMilliseconds: 7_500,
				Quality:              73,
			}
			executor := &p6TrickplayGenerationExecutor{}
			adapter := trickplayTaskGenerator{generator: executor}

			if err := adapter.Generate(t.Context(), generation); err != nil {
				t.Fatalf("generate task rendition: %v", err)
			}
			if executor.request.Source == nil || executor.request.Source.File() == nil || executor.request.Source.File().Name() != fixture.path {
				t.Fatalf("task generation source = %#v, want pinned fixture descriptor %q", executor.request.Source, fixture.path)
			}
			want := trickplay.GenerateRequest{
				ItemID:               generation.Candidate.ID,
				SourceRevision:       generation.SourceRevision,
				VideoWidth:           generation.Candidate.VideoWidth,
				RunTimeTicks:         generation.Candidate.RunTimeTicks,
				RequestedWidth:       generation.RequestedWidth,
				TileWidth:            generation.TileWidth,
				TileHeight:           generation.TileHeight,
				IntervalMilliseconds: generation.IntervalMilliseconds,
				ReferenceFrameRate:   generation.ReferenceFrameRate,
				JPEGQuality:          generation.Quality,
			}
			got := executor.request
			got.Source = nil
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("trickplay task generation request = %#v, want %#v", got, want)
			}
			if _, err := executor.request.Source.File().Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("task generator source descriptor after Generate = %v, want closed", err)
			}
		})
	}
}

// TestP6TrickplayTaskGeneratorRejectsStaleSourceFactsBeforeGenerator proves
// a stale task snapshot is rejected when the source is opened, before any
// generation side effect or durable publication can begin.
func TestP6TrickplayTaskGeneratorRejectsStaleSourceFactsBeforeGenerator(t *testing.T) {
	fixture := p6WriteSourceFixture(t, "stale-task-source.mkv", "stale task source")
	for _, tc := range []struct {
		name     string
		size     *int64
		modified *time.Time
	}{
		{name: "size", size: func() *int64 { value := fixture.size + 1; return &value }(), modified: &fixture.modified},
		{name: "modified", size: &fixture.size, modified: func() *time.Time { value := fixture.modified.Add(time.Second); return &value }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &p6TrickplayGenerationExecutor{}
			generation := tasks.Generation{Candidate: tasks.Candidate{ID: p6AdapterUUID(t, 0x63a), SourcePath: fixture.path, SourceRevision: "stale-task-source", SourceSize: tc.size, SourceModified: tc.modified, SourceInode: &fixture.inode, SourceChangedAt: &fixture.changedAt}, SourceRevision: "stale-task-source", RequestedWidth: 320, TileWidth: 1, TileHeight: 1, IntervalMilliseconds: 10_000, Quality: 73}
			err := (trickplayTaskGenerator{generator: executor}).Generate(t.Context(), generation)
			if !errors.Is(err, trickplay.ErrStaleSourceRevision) {
				t.Fatalf("Generate() stale %s source facts error = %v, want trickplay.ErrStaleSourceRevision", tc.name, err)
			}
			if executor.calls != 0 {
				t.Fatalf("generator calls after stale %s source facts = %d, want 0", tc.name, executor.calls)
			}
		})
	}
}

// TestP6TrickplayTaskGeneratorRejectsIncompleteFingerprintBeforeOpeningSource
// proves a legacy snapshot cannot turn an unchecked pathname into a rendition.
// The invalid pathname is intentional: a stale-revision result demonstrates the
// mandatory identity gate ran before any source-open attempt.
func TestP6TrickplayTaskGeneratorRejectsIncompleteFingerprintBeforeOpeningSource(t *testing.T) {
	fixture := p6WriteSourceFixture(t, "incomplete-task-source.mkv", "incomplete task source")
	for _, tc := range []struct {
		name   string
		mutate func(*tasks.Candidate)
	}{
		{name: "neither", mutate: func(candidate *tasks.Candidate) { candidate.SourceInode, candidate.SourceChangedAt = nil, nil }},
		{name: "inode only", mutate: func(candidate *tasks.Candidate) { candidate.SourceChangedAt = nil }},
		{name: "changed-at only", mutate: func(candidate *tasks.Candidate) { candidate.SourceInode = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := tasks.Candidate{ID: p6AdapterUUID(t, 0x63b), SourcePath: "\x00must-not-open", SourceRevision: "legacy-partial", SourceSize: &fixture.size, SourceModified: &fixture.modified, SourceInode: &fixture.inode, SourceChangedAt: &fixture.changedAt}
			tc.mutate(&candidate)
			executor := &p6TrickplayGenerationExecutor{}
			err := (trickplayTaskGenerator{generator: executor}).Generate(t.Context(), tasks.Generation{Candidate: candidate, SourceRevision: candidate.SourceRevision, RequestedWidth: 320, TileWidth: 1, TileHeight: 1, IntervalMilliseconds: 10_000, Quality: 73})
			if !errors.Is(err, trickplay.ErrStaleSourceRevision) {
				t.Fatalf("Generate() incomplete %s fingerprint error = %v, want trickplay.ErrStaleSourceRevision before opening invalid source", tc.name, err)
			}
			if executor.calls != 0 {
				t.Fatalf("generator calls after incomplete %s fingerprint = %d, want 0", tc.name, executor.calls)
			}
		})
	}
}

type p6TrickplayGenerationExecutor struct {
	request trickplay.GenerateRequest
	calls   int
}

func (executor *p6TrickplayGenerationExecutor) Generate(_ context.Context, request trickplay.GenerateRequest) (trickplay.Metadata, error) {
	executor.calls++
	executor.request = request
	return trickplay.Metadata{}, nil
}
