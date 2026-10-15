// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/library"
)

// storageLibraryReader supplies the virtual-folder topology for storage
// reporting. *library.Topology satisfies this interface.
type storageLibraryReader interface {
	VirtualFolders(context.Context) ([]library.VirtualFolder, error)
}

// RegisterSystemStorage registers the GET /System/Info/Storage route
// (GetSystemStorage) with the RequiresElevation policy. Passing a nil library
// reader registers the route with a 503 handler, which keeps the pure route
// ledger path valid.
func RegisterSystemStorage(rt *router.Router, infoConfig SystemInfoConfig, libraries storageLibraryReader) {
	policy := string(auth.PolicyRequiresElevation)
	h := systemStorageHandler{infoConfig: infoConfig, libraries: libraries}
	rt.Handle(http.MethodGet, "/System/Info/Storage", router.Route{
		OperationID: "GetSystemStorage",
		Policy:      policy,
	}, h.storage)
}

type systemStorageHandler struct {
	infoConfig SystemInfoConfig
	libraries  storageLibraryReader
}

// systemStorageDto is the wire response for GetSystemStorage.
type systemStorageDto struct {
	ProgramDataFolder      folderStorageDto    `json:"ProgramDataFolder"`
	WebFolder              folderStorageDto    `json:"WebFolder"`
	ImageCacheFolder       folderStorageDto    `json:"ImageCacheFolder"`
	CacheFolder            folderStorageDto    `json:"CacheFolder"`
	LogFolder              folderStorageDto    `json:"LogFolder"`
	InternalMetadataFolder folderStorageDto    `json:"InternalMetadataFolder"`
	TranscodingTempFolder  folderStorageDto    `json:"TranscodingTempFolder"`
	Libraries              []libraryStorageDto `json:"Libraries"`
}

// folderStorageDto reports one filesystem path and its space metrics.
// StorageType and DeviceId are nullable in the pinned schema; this
// implementation always omits them.
type folderStorageDto struct {
	Path        string  `json:"Path"`
	FreeSpace   int64   `json:"FreeSpace"`
	UsedSpace   int64   `json:"UsedSpace"`
	StorageType *string `json:"StorageType,omitempty"`
	DeviceId    *string `json:"DeviceId,omitempty"`
}

// libraryStorageDto projects one virtual folder and its media paths.
type libraryStorageDto struct {
	Id      dto.UUID           `json:"Id"`
	Name    string             `json:"Name"`
	Folders []folderStorageDto `json:"Folders"`
}

func (h systemStorageHandler) storage(w http.ResponseWriter, r *http.Request) {
	if h.libraries == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	cfg := h.infoConfig
	dto := systemStorageDto{
		ProgramDataFolder:      probeFolder(cfg.Paths.Root),
		WebFolder:              probeFolder(cfg.effectiveWebPath()),
		ImageCacheFolder:       probeFolder(filepath.Join(cfg.Paths.Cache, "images")),
		CacheFolder:            probeFolder(cfg.Paths.Cache),
		LogFolder:              probeFolder(cfg.Paths.Log),
		InternalMetadataFolder: probeFolder(cfg.Paths.Metadata),
		TranscodingTempFolder:  probeFolder(cfg.Paths.Transcodes),
		Libraries:              []libraryStorageDto{},
	}

	folders, err := h.libraries.VirtualFolders(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	for _, vf := range folders {
		lib := libraryStorageDto{
			Id:      vf.CollectionFolderID,
			Name:    vf.Name,
			Folders: make([]folderStorageDto, 0, len(vf.MediaPaths)),
		}
		for _, mediaPath := range vf.MediaPaths {
			lib.Folders = append(lib.Folders, probeFolder(mediaPath))
		}
		dto.Libraries = append(dto.Libraries, lib)
	}
	_ = render.JSON(w, r, http.StatusOK, dto)
}

// probeFolder resolves symlinks and probes the filesystem for free and used
// space. A resolution or stat failure returns -1 for both space values,
// matching the upstream catch branch. StorageType and DeviceId stay nil.
func probeFolder(path string) folderStorageDto {
	if path == "" {
		return folderStorageDto{Path: path, FreeSpace: -1, UsedSpace: -1}
	}
	free, used, ok := storageSpace(path)
	if !ok {
		return folderStorageDto{Path: path, FreeSpace: -1, UsedSpace: -1}
	}
	return folderStorageDto{Path: path, FreeSpace: free, UsedSpace: used}
}
