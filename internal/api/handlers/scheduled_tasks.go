// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/tasks"
)

// scheduledTaskService is the narrow scheduler seam required by the
// ScheduledTasks REST operations.
type scheduledTaskService interface {
	List() []tasks.Info
	Get(id string) (tasks.Info, bool)
	Run(id string) error
	Cancel(id string) error
	UpdateTriggers(id string, triggers []tasks.TriggerInfo) error
}

// RegisterScheduledTasks registers the five stock /ScheduledTasks operations
// on rt. All routes carry PolicyRequiresElevation.
func RegisterScheduledTasks(rt *router.Router, service scheduledTaskService) {
	h := scheduledTasksHandler{service: service}
	rt.Handle(http.MethodGet, "/ScheduledTasks", router.Route{
		OperationID: "GetTasks",
		Policy:      string(auth.PolicyRequiresElevation),
	}, h.getTasks)
	rt.Handle(http.MethodGet, "/ScheduledTasks/{taskId}", router.Route{
		OperationID: "GetTask",
		Policy:      string(auth.PolicyRequiresElevation),
	}, h.getTask)
	rt.Handle(http.MethodPost, "/ScheduledTasks/{taskId}/Triggers", router.Route{
		OperationID: "UpdateTask",
		Policy:      string(auth.PolicyRequiresElevation),
	}, h.updateTriggers)
	rt.Handle(http.MethodPost, "/ScheduledTasks/Running/{taskId}", router.Route{
		OperationID: "StartTask",
		Policy:      string(auth.PolicyRequiresElevation),
	}, h.startTask)
	rt.Handle(http.MethodDelete, "/ScheduledTasks/Running/{taskId}", router.Route{
		OperationID: "StopTask",
		Policy:      string(auth.PolicyRequiresElevation),
	}, h.stopTask)
}

type scheduledTasksHandler struct {
	service scheduledTaskService
}

func (h scheduledTasksHandler) getTasks(w http.ResponseWriter, r *http.Request) {
	var query struct {
		IsHidden  *bool `query:"isHidden"`
		IsEnabled *bool `query:"isEnabled"`
	}
	if err := bind.QueryRequest(r, &query); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	all := h.service.List()
	// isHidden filter: unset includes hidden (upstream semantics).
	if query.IsHidden != nil {
		filtered := make([]tasks.Info, 0, len(all))
		for _, info := range all {
			if info.IsHidden == *query.IsHidden {
				filtered = append(filtered, info)
			}
		}
		all = filtered
	}
	// isEnabled filter: every Deadfin task is enabled, so isEnabled=false → empty.
	if query.IsEnabled != nil && !*query.IsEnabled {
		all = []tasks.Info{}
	}
	_ = render.JSON(w, r, http.StatusOK, all)
}

func (h scheduledTasksHandler) getTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	info, ok := h.service.Get(taskID)
	if !ok {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, info)
}

func (h scheduledTasksHandler) updateTriggers(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	var triggers []tasks.TriggerInfo
	if err := bind.JSONBody(r, &triggers); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if err := h.service.UpdateTriggers(taskID, triggers); err != nil {
		if errors.Is(err, tasks.ErrTaskNotFound) {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h scheduledTasksHandler) startTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if err := h.service.Run(taskID); err != nil {
		if errors.Is(err, tasks.ErrTaskNotFound) {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h scheduledTasksHandler) stopTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if err := h.service.Cancel(taskID); err != nil {
		if errors.Is(err, tasks.ErrTaskNotFound) {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
