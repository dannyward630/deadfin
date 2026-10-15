// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
)

// RegisterBackup registers honest disabled stubs for the pinned Backup API.
// The capture corpus has no observed Backup requests, and Deadfin does not own
// a backup engine in this phase.
func RegisterBackup(rt *router.Router) {
	h := backupStubHandler{}
	policy := string(auth.PolicyRequiresElevation)

	rt.Handle(http.MethodGet, "/Backup", router.Route{OperationID: "ListBackups", Policy: policy}, h.list)
	rt.Handle(http.MethodPost, "/Backup/Create", router.Route{OperationID: "CreateBackup", Policy: policy}, h.create)
	rt.Handle(http.MethodGet, "/Backup/Manifest", router.Route{OperationID: "GetBackup", Policy: policy}, h.missing)
	rt.Handle(http.MethodPost, "/Backup/Restore", router.Route{OperationID: "StartRestoreBackup", Policy: policy}, h.missing)
}

type backupStubHandler struct{}

func (backupStubHandler) list(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, []struct{}{})
}

func (backupStubHandler) create(w http.ResponseWriter, r *http.Request) {
	renderProblem(w, r, http.StatusServiceUnavailable, "Backup support is not available.")
}

func (backupStubHandler) missing(w http.ResponseWriter, r *http.Request) {
	renderProblem(w, r, http.StatusNotFound, "Backup not found.")
}
