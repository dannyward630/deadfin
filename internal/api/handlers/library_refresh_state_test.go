// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/library"
)

// refreshStateTrigger is a fake libraryRefreshTrigger that returns a fixed
// refresh-state snapshot. The barrier channel is closed when RefreshStates
// runs, so an orderingTopology can block until the snapshot completes.
type refreshStateTrigger struct {
	mu      sync.Mutex
	states  map[dto.UUID]library.FolderRefreshState
	barrier chan struct{}
}

func newRefreshStateTrigger() *refreshStateTrigger {
	return &refreshStateTrigger{
		states:  map[dto.UUID]library.FolderRefreshState{},
		barrier: make(chan struct{}),
	}
}

func (t *refreshStateTrigger) Trigger() error { return nil }

func (t *refreshStateTrigger) RefreshStates() map[dto.UUID]library.FolderRefreshState {
	t.mu.Lock()
	defer t.mu.Unlock()
	close(t.barrier)
	copy := make(map[dto.UUID]library.FolderRefreshState, len(t.states))
	for key, value := range t.states {
		copy[key] = value
	}
	return copy
}

// orderingTopology records that VirtualFolders was called after the trigger
// snapshot. It blocks until the trigger signals that RefreshStates ran.
// orderingTopology verifies that RefreshStates runs before VirtualFolders.
// The snapshot barrier is a channel that RefreshStates closes; VirtualFolders
// blocks on it. If the handler calls VirtualFolders first, the test
// deadlocks and fails on the context deadline. No wall-clock sleep is used.
type orderingTopology struct {
	fakeLibraryTopology
	snapshotBarrier chan struct{}
	topologyRan     chan struct{}
}

func newOrderingTopology(folders []library.VirtualFolder) *orderingTopology {
	return &orderingTopology{
		fakeLibraryTopology: fakeLibraryTopology{folders: folders},
		snapshotBarrier:     make(chan struct{}),
		topologyRan:         make(chan struct{}),
	}
}

func (t *orderingTopology) VirtualFolders(ctx context.Context) ([]library.VirtualFolder, error) {
	select {
	case <-t.snapshotBarrier:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	close(t.topologyRan)
	return t.fakeLibraryTopology.VirtualFolders(ctx)
}

func TestLibraryVirtualFoldersRefreshState(t *testing.T) {
	activeID := dto.UUID{0xaa, 0x01}
	queuedID := dto.UUID{0xaa, 0x02}
	idleID := dto.UUID{0xaa, 0x03}
	unknownID := dto.UUID{0xaa, 0x04}

	t.Run("active projects RefreshStatus and numeric RefreshProgress including zero", func(t *testing.T) {
		trigger := newRefreshStateTrigger()
		trigger.states[activeID] = library.FolderRefreshState{Status: library.FolderRefreshActive, Progress: 0}
		topology := &fakeLibraryTopology{folders: []library.VirtualFolder{
			{Name: "Active", CollectionFolderID: activeID, MediaPaths: []string{"/media/active"}},
		}}
		rt := router.New()
		handlers.RegisterLibrary(rt, trigger, topology)

		response := libraryRequest(t, rt, http.MethodGet, "/Library/VirtualFolders", "", "")
		assertLibraryStatus(t, response, http.StatusOK)
		var body []map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(body) != 1 {
			t.Fatalf("response length = %d, want 1", len(body))
		}
		if body[0]["RefreshStatus"] != "Active" {
			t.Fatalf("RefreshStatus = %#v, want Active", body[0]["RefreshStatus"])
		}
		progress, ok := body[0]["RefreshProgress"]
		if !ok {
			t.Fatal("RefreshProgress absent for Active folder")
		}
		if progress != float64(0) {
			t.Fatalf("RefreshProgress = %#v, want 0", progress)
		}
	})

	t.Run("active projects non-zero progress", func(t *testing.T) {
		trigger := newRefreshStateTrigger()
		trigger.states[activeID] = library.FolderRefreshState{Status: library.FolderRefreshActive, Progress: 50}
		topology := &fakeLibraryTopology{folders: []library.VirtualFolder{
			{Name: "Active", CollectionFolderID: activeID, MediaPaths: []string{"/media/active"}},
		}}
		rt := router.New()
		handlers.RegisterLibrary(rt, trigger, topology)

		response := libraryRequest(t, rt, http.MethodGet, "/Library/VirtualFolders", "", "")
		assertLibraryStatus(t, response, http.StatusOK)
		var body []map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body[0]["RefreshStatus"] != "Active" {
			t.Fatalf("RefreshStatus = %#v, want Active", body[0]["RefreshStatus"])
		}
		if body[0]["RefreshProgress"] != float64(50) {
			t.Fatalf("RefreshProgress = %#v, want 50", body[0]["RefreshProgress"])
		}
	})

	t.Run("queued projects RefreshStatus without RefreshProgress", func(t *testing.T) {
		trigger := newRefreshStateTrigger()
		trigger.states[queuedID] = library.FolderRefreshState{Status: library.FolderRefreshQueued}
		topology := &fakeLibraryTopology{folders: []library.VirtualFolder{
			{Name: "Queued", CollectionFolderID: queuedID, MediaPaths: []string{"/media/queued"}},
		}}
		rt := router.New()
		handlers.RegisterLibrary(rt, trigger, topology)

		response := libraryRequest(t, rt, http.MethodGet, "/Library/VirtualFolders", "", "")
		assertLibraryStatus(t, response, http.StatusOK)
		var body []map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body[0]["RefreshStatus"] != "Queued" {
			t.Fatalf("RefreshStatus = %#v, want Queued", body[0]["RefreshStatus"])
		}
		if _, present := body[0]["RefreshProgress"]; present {
			t.Fatalf("RefreshProgress present for Queued folder: %#v", body[0]["RefreshProgress"])
		}
	})

	t.Run("idle projects RefreshStatus without RefreshProgress", func(t *testing.T) {
		trigger := newRefreshStateTrigger()
		topology := &fakeLibraryTopology{folders: []library.VirtualFolder{
			{Name: "Idle", CollectionFolderID: idleID, MediaPaths: []string{"/media/idle"}},
		}}
		rt := router.New()
		handlers.RegisterLibrary(rt, trigger, topology)

		response := libraryRequest(t, rt, http.MethodGet, "/Library/VirtualFolders", "", "")
		assertLibraryStatus(t, response, http.StatusOK)
		var body []map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body[0]["RefreshStatus"] != "Idle" {
			t.Fatalf("RefreshStatus = %#v, want Idle", body[0]["RefreshStatus"])
		}
		if _, present := body[0]["RefreshProgress"]; present {
			t.Fatalf("RefreshProgress present for Idle folder: %#v", body[0]["RefreshProgress"])
		}
	})

	t.Run("unknown status projects Idle without RefreshProgress", func(t *testing.T) {
		trigger := newRefreshStateTrigger()
		// An entry with an unrecognized status value projects as Idle.
		trigger.states[unknownID] = library.FolderRefreshState{Status: "Unknown"}
		topology := &fakeLibraryTopology{folders: []library.VirtualFolder{
			{Name: "Unknown", CollectionFolderID: unknownID, MediaPaths: []string{"/media/unknown"}},
		}}
		rt := router.New()
		handlers.RegisterLibrary(rt, trigger, topology)

		response := libraryRequest(t, rt, http.MethodGet, "/Library/VirtualFolders", "", "")
		assertLibraryStatus(t, response, http.StatusOK)
		var body []map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body[0]["RefreshStatus"] != "Idle" {
			t.Fatalf("RefreshStatus = %#v, want Idle for unknown status", body[0]["RefreshStatus"])
		}
		if _, present := body[0]["RefreshProgress"]; present {
			t.Fatalf("RefreshProgress present for unknown-status folder: %#v", body[0]["RefreshProgress"])
		}
	})

	t.Run("snapshot read before topology list", func(t *testing.T) {
		trigger := newRefreshStateTrigger()
		trigger.states[activeID] = library.FolderRefreshState{Status: library.FolderRefreshActive, Progress: 25}
		topology := newOrderingTopology([]library.VirtualFolder{
			{Name: "Active", CollectionFolderID: activeID, MediaPaths: []string{"/media/active"}},
		})
		// Wire the trigger barrier to the topology barrier so VirtualFolders
		// blocks until RefreshStates closes it. If the handler calls
		// VirtualFolders first, the request never completes and the deadline
		// fires.
		topology.snapshotBarrier = trigger.barrier
		rt := router.New()
		handlers.RegisterLibrary(rt, trigger, topology)

		// Run the request with a deadline so a wrong ordering fails instead
		// of hanging forever. No wall-clock sleep is used.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		request := httptest.NewRequest(http.MethodGet, "/Library/VirtualFolders", nil)
		request = request.WithContext(ctx)
		response := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			rt.ServeHTTP(response, request)
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("VirtualFolders blocked on the snapshot barrier: RefreshStates did not run first")
		}
		assertLibraryStatus(t, response, http.StatusOK)
		select {
		case <-topology.topologyRan:
		default:
			t.Fatal("VirtualFolders was not called")
		}
	})

	t.Run("nil trigger projects Idle for every folder", func(t *testing.T) {
		topology := &fakeLibraryTopology{folders: []library.VirtualFolder{
			{Name: "Alpha", CollectionFolderID: activeID, MediaPaths: []string{"/media/alpha"}},
			{Name: "Bravo", CollectionFolderID: queuedID, MediaPaths: []string{"/media/bravo"}},
		}}
		rt := router.New()
		handlers.RegisterLibrary(rt, nil, topology)

		response := libraryRequest(t, rt, http.MethodGet, "/Library/VirtualFolders", "", "")
		assertLibraryStatus(t, response, http.StatusOK)
		var body []map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(body) != 2 {
			t.Fatalf("response length = %d, want 2", len(body))
		}
		for i, entry := range body {
			if entry["RefreshStatus"] != "Idle" {
				t.Fatalf("folder %d RefreshStatus = %#v, want Idle for nil trigger", i, entry["RefreshStatus"])
			}
			if _, present := entry["RefreshProgress"]; present {
				t.Fatalf("folder %d RefreshProgress present, want absent for nil trigger", i)
			}
		}
	})

	t.Run("mixed states project per folder", func(t *testing.T) {
		trigger := newRefreshStateTrigger()
		trigger.states[activeID] = library.FolderRefreshState{Status: library.FolderRefreshActive, Progress: 75}
		trigger.states[queuedID] = library.FolderRefreshState{Status: library.FolderRefreshQueued}
		topology := &fakeLibraryTopology{folders: []library.VirtualFolder{
			{Name: "Active", CollectionFolderID: activeID, MediaPaths: []string{"/media/active"}},
			{Name: "Queued", CollectionFolderID: queuedID, MediaPaths: []string{"/media/queued"}},
			{Name: "Idle", CollectionFolderID: idleID, MediaPaths: []string{"/media/idle"}},
		}}
		rt := router.New()
		handlers.RegisterLibrary(rt, trigger, topology)

		response := libraryRequest(t, rt, http.MethodGet, "/Library/VirtualFolders", "", "")
		assertLibraryStatus(t, response, http.StatusOK)
		var body []map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(body) != 3 {
			t.Fatalf("response length = %d, want 3", len(body))
		}
		if body[0]["RefreshStatus"] != "Active" || body[0]["RefreshProgress"] != float64(75) {
			t.Fatalf("active folder = %#v, want Active/75", body[0])
		}
		if body[1]["RefreshStatus"] != "Queued" {
			t.Fatalf("queued folder RefreshStatus = %#v, want Queued", body[1]["RefreshStatus"])
		}
		if _, present := body[1]["RefreshProgress"]; present {
			t.Fatalf("queued folder RefreshProgress present: %#v", body[1]["RefreshProgress"])
		}
		if body[2]["RefreshStatus"] != "Idle" {
			t.Fatalf("idle folder RefreshStatus = %#v, want Idle", body[2]["RefreshStatus"])
		}
		if _, present := body[2]["RefreshProgress"]; present {
			t.Fatalf("idle folder RefreshProgress present: %#v", body[2]["RefreshProgress"])
		}
	})
}
