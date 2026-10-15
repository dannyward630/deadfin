// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

// fakeScheduledTaskService implements handlers.scheduledTaskService for tests.
type fakeScheduledTaskService struct {
	tasks     []tasks.Info
	byID      map[string]tasks.Info
	runErr    error
	cancelErr error
	updateErr error
}

func (f *fakeScheduledTaskService) List() []tasks.Info { return f.tasks }
func (f *fakeScheduledTaskService) Get(id string) (tasks.Info, bool) {
	info, ok := f.byID[id]
	return info, ok
}
func (f *fakeScheduledTaskService) Run(_ string) error    { return f.runErr }
func (f *fakeScheduledTaskService) Cancel(_ string) error { return f.cancelErr }
func (f *fakeScheduledTaskService) UpdateTriggers(_ string, _ []tasks.TriggerInfo) error {
	return f.updateErr
}

func newScheduledTasksRouter() (*router.Router, *fakeScheduledTaskService) {
	svc := &fakeScheduledTaskService{
		byID: make(map[string]tasks.Info),
	}
	rt := router.New()
	handlers.RegisterScheduledTasks(rt, svc)
	return rt, svc
}

func TestScheduledTasksRoutesRegisteredWithElevationPolicy(t *testing.T) {
	rt, _ := newScheduledTasksRouter()
	want := map[string]router.Route{
		"GET /ScheduledTasks":                     {OperationID: "GetTasks", Policy: string(auth.PolicyRequiresElevation)},
		"GET /ScheduledTasks/{taskId}":            {OperationID: "GetTask", Policy: string(auth.PolicyRequiresElevation)},
		"POST /ScheduledTasks/{taskId}/Triggers":  {OperationID: "UpdateTask", Policy: string(auth.PolicyRequiresElevation)},
		"POST /ScheduledTasks/Running/{taskId}":   {OperationID: "StartTask", Policy: string(auth.PolicyRequiresElevation)},
		"DELETE /ScheduledTasks/Running/{taskId}": {OperationID: "StopTask", Policy: string(auth.PolicyRequiresElevation)},
	}
	got := make(map[string]router.Route)
	for _, route := range rt.Routes() {
		key := route.Method + " " + route.Pattern
		got[key] = router.Route{OperationID: route.OperationID, Policy: route.Policy}
	}
	for key, wantRoute := range want {
		gotRoute, ok := got[key]
		if !ok {
			t.Errorf("route %s not registered", key)
			continue
		}
		if gotRoute.OperationID != wantRoute.OperationID {
			t.Errorf("%s OperationID = %q, want %q", key, gotRoute.OperationID, wantRoute.OperationID)
		}
		if gotRoute.Policy != wantRoute.Policy {
			t.Errorf("%s Policy = %q, want %q", key, gotRoute.Policy, wantRoute.Policy)
		}
		if route, exists := got[key]; exists && route.Anonymous {
			t.Errorf("%s must not be anonymous", key)
		}
	}
}

func TestScheduledTasksGetTasksReturns200(t *testing.T) {
	rt, svc := newScheduledTasksRouter()
	svc.tasks = []tasks.Info{
		{Name: "Task A", ID: "aaa", Key: "A", State: tasks.StateIdle, Triggers: []tasks.TriggerInfo{}},
	}
	req := httptest.NewRequest(http.MethodGet, "/ScheduledTasks", nil)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var result []tasks.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 || result[0].ID != "aaa" {
		t.Fatalf("result = %+v, want one task with ID aaa", result)
	}
}

func TestScheduledTasksIsHiddenFilter(t *testing.T) {
	rt, svc := newScheduledTasksRouter()
	svc.tasks = []tasks.Info{
		{Name: "Visible", ID: "v1", IsHidden: false, Triggers: []tasks.TriggerInfo{}},
		{Name: "Hidden", ID: "h1", IsHidden: true, Triggers: []tasks.TriggerInfo{}},
	}
	req := httptest.NewRequest(http.MethodGet, "/ScheduledTasks?isHidden=true", nil)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var result []tasks.Info
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	if len(result) != 1 || result[0].ID != "h1" {
		t.Fatalf("isHidden=true result = %+v, want only h1", result)
	}

	req = httptest.NewRequest(http.MethodGet, "/ScheduledTasks?isHidden=false", nil)
	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	if len(result) != 1 || result[0].ID != "v1" {
		t.Fatalf("isHidden=false result = %+v, want only v1", result)
	}

	req = httptest.NewRequest(http.MethodGet, "/ScheduledTasks", nil)
	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	if len(result) != 2 {
		t.Fatalf("unset isHidden result = %d tasks, want 2", len(result))
	}
}

func TestScheduledTasksIsEnabledFilter(t *testing.T) {
	rt, svc := newScheduledTasksRouter()
	svc.tasks = []tasks.Info{
		{Name: "Task A", ID: "aaa", Triggers: []tasks.TriggerInfo{}},
	}
	req := httptest.NewRequest(http.MethodGet, "/ScheduledTasks?isEnabled=false", nil)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var result []tasks.Info
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	if len(result) != 0 {
		t.Fatalf("isEnabled=false result = %d tasks, want 0", len(result))
	}
}

func TestScheduledTasksGetTask200(t *testing.T) {
	rt, svc := newScheduledTasksRouter()
	svc.byID["abc"] = tasks.Info{
		Name: "Scan Media Library", ID: "abc", Triggers: []tasks.TriggerInfo{},
	}
	req := httptest.NewRequest(http.MethodGet, "/ScheduledTasks/abc", nil)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestScheduledTasksGetTask404(t *testing.T) {
	rt, _ := newScheduledTasksRouter()
	req := httptest.NewRequest(http.MethodGet, "/ScheduledTasks/nonexistent", nil)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestScheduledTasksStartTask204(t *testing.T) {
	rt, _ := newScheduledTasksRouter()
	req := httptest.NewRequest(http.MethodPost, "/ScheduledTasks/Running/abc", nil)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestScheduledTasksStopTask204(t *testing.T) {
	rt, _ := newScheduledTasksRouter()
	req := httptest.NewRequest(http.MethodDelete, "/ScheduledTasks/Running/abc", nil)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestScheduledTasksUpdateTriggers204(t *testing.T) {
	rt, _ := newScheduledTasksRouter()
	body, _ := json.Marshal([]tasks.TriggerInfo{
		{Type: "DailyTrigger", TimeOfDayTicks: int64Ptr(14 * 3600 * 10000000)},
	})
	req := httptest.NewRequest(http.MethodPost, "/ScheduledTasks/abc/Triggers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
}

func TestScheduledTasksUpdateTriggers400OnServiceError(t *testing.T) {
	rt, svc := newScheduledTasksRouter()
	svc.updateErr = errors.New("validation failed")
	body, _ := json.Marshal([]tasks.TriggerInfo{
		{Type: "DailyTrigger"},
	})
	req := httptest.NewRequest(http.MethodPost, "/ScheduledTasks/abc/Triggers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestScheduledTasksStartTask404OnNotFound(t *testing.T) {
	rt, svc := newScheduledTasksRouter()
	svc.runErr = tasks.ErrTaskNotFound
	req := httptest.NewRequest(http.MethodPost, "/ScheduledTasks/Running/nonexistent", nil)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func int64Ptr(v int64) *int64 { return &v }

var _ = chi.URLParam
