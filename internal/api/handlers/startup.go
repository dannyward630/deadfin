// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"errors"
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/user"
)

var errStartupAlreadyCompleted = errors.New("startup wizard already completed")

// RegisterStartup registers the initial server configuration endpoints on rt.
// The optional user service retains the configuration-only seam used before the
// composition root wires startup identity handling.
func RegisterStartup(rt *router.Router, manager *config.Manager, services ...*user.Service) {
	h := startup{manager: manager}
	if len(services) != 0 {
		h.users = services[0]
	}

	rt.Handle(http.MethodGet, "/Startup/Configuration", router.Route{
		OperationID: "GetStartupConfiguration",
		Policy:      "FirstTimeSetupOrElevated",
	}, h.configuration)
	rt.Handle(http.MethodPost, "/Startup/Configuration", router.Route{
		OperationID: "UpdateInitialConfiguration",
		Policy:      "FirstTimeSetupOrElevated",
	}, h.updateConfiguration)
	rt.Handle(http.MethodPost, "/Startup/RemoteAccess", router.Route{
		OperationID: "SetRemoteAccess",
		Policy:      "FirstTimeSetupOrElevated",
	}, h.updateRemoteAccess)
	if h.users == nil {
		return
	}
	rt.Handle(http.MethodGet, "/Startup/User", router.Route{
		OperationID: "GetFirstUser",
		Policy:      "FirstTimeSetupOrElevated",
	}, h.firstUser)
	rt.Handle(http.MethodGet, "/Startup/FirstUser", router.Route{
		OperationID: "GetFirstUser_2",
		Policy:      "FirstTimeSetupOrElevated",
	}, h.firstUser)
	rt.Handle(http.MethodPost, "/Startup/User", router.Route{
		OperationID: "UpdateStartupUser",
		Policy:      "FirstTimeSetupOrElevated",
	}, h.updateStartupUser)
	rt.Handle(http.MethodPost, "/Startup/Complete", router.Route{
		OperationID: "CompleteWizard",
		Policy:      "FirstTimeSetupOrElevated",
	}, h.complete)
}

type startup struct {
	manager *config.Manager
	users   *user.Service
}

func (h startup) configuration(w http.ResponseWriter, r *http.Request) {
	configuration := h.manager.System()
	_ = render.JSON(w, r, http.StatusOK, dto.StartupConfiguration{
		ServerName:                &configuration.ServerName,
		UICulture:                 &configuration.UICulture,
		MetadataCountryCode:       &configuration.MetadataCountryCode,
		PreferredMetadataLanguage: &configuration.PreferredMetadataLanguage,
	})
}

func (h startup) updateConfiguration(w http.ResponseWriter, r *http.Request) {
	var request *dto.StartupConfiguration
	if !bindStartupJSON(w, r, &request) {
		return
	}
	if request == nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if err := h.manager.UpdateSystem(r.Context(), func(configuration *config.ServerConfiguration) error {
		configuration.ServerName = startupString(request.ServerName)
		configuration.UICulture = startupString(request.UICulture)
		configuration.MetadataCountryCode = startupString(request.MetadataCountryCode)
		configuration.PreferredMetadataLanguage = startupString(request.PreferredMetadataLanguage)
		return nil
	}); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h startup) updateRemoteAccess(w http.ResponseWriter, r *http.Request) {
	var request *dto.StartupRemoteAccess
	if !bindStartupJSON(w, r, &request) {
		return
	}
	if request == nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if err := h.manager.UpdateNetwork(r.Context(), func(configuration *config.NetworkConfiguration) error {
		configuration.EnableRemoteAccess = request.EnableRemoteAccess
		return nil
	}); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h startup) firstUser(w http.ResponseWriter, r *http.Request) {
	if _, err := h.users.Initialize(r.Context()); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	first, err := h.users.FirstUser(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	_ = render.JSON(w, r, http.StatusOK, dto.StartupUser{Name: &first.Name})
}

func (h startup) updateStartupUser(w http.ResponseWriter, r *http.Request) {
	var request *dto.StartupUser
	if !bindStartupJSON(w, r, &request) {
		return
	}
	if request == nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	err := h.users.UpdateStartupUser(r.Context(), user.StartupUserUpdate{
		Name:     request.Name,
		Password: startupString(request.Password),
	})
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
	case errors.Is(err, user.ErrStartupUserConfigured):
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
	case errors.Is(err, user.ErrStartupPasswordRequired):
		http.Error(w, "Password must not be empty", http.StatusBadRequest)
	case errors.Is(err, user.ErrInvalidUsername):
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
	case errors.Is(err, store.ErrDuplicateName):
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
	default:
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func (h startup) complete(w http.ResponseWriter, r *http.Request) {
	err := h.manager.UpdateSystem(r.Context(), func(configuration *config.ServerConfiguration) error {
		if configuration.IsStartupWizardCompleted {
			return errStartupAlreadyCompleted
		}
		configuration.IsStartupWizardCompleted = true
		return nil
	})
	switch {
	case err == nil, errors.Is(err, errStartupAlreadyCompleted):
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func bindStartupJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := bind.JSONBody(r, dst); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, bind.ErrUnsupportedMediaType) {
			status = http.StatusUnsupportedMediaType
		}
		http.Error(w, http.StatusText(status), status)
		return false
	}
	return true
}

func startupString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
