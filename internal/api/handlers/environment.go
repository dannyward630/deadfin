// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
)

// DriveLister lists the mount points that GET /Environment/Drives reports.
// drives.System satisfies it.
type DriveLister interface {
	Drives(context.Context) ([]string, error)
}

// RegisterEnvironment registers the EnvironmentController routes
// (EnvironmentController.cs). The controller carries the
// FirstTimeSetupOrElevated policy, so the startup wizard can browse and
// validate library paths before an administrator exists.
func RegisterEnvironment(rt *router.Router, drives DriveLister) {
	h := environmentHandler{drives: drives}
	for _, route := range []struct {
		method, pattern, operationID string
		handler                      http.HandlerFunc
	}{
		{http.MethodGet, "/Environment/DirectoryContents", "GetDirectoryContents", h.directoryContents},
		{http.MethodPost, "/Environment/ValidatePath", "ValidatePath", h.validatePath},
		{http.MethodGet, "/Environment/Drives", "GetDrives", h.drivesList},
		{http.MethodGet, "/Environment/ParentPath", "GetParentPath", h.parentPath},
		{http.MethodGet, "/Environment/DefaultDirectoryBrowser", "GetDefaultDirectoryBrowser", h.defaultDirectoryBrowser},
	} {
		rt.Handle(route.method, route.pattern, router.Route{
			OperationID: route.operationID,
			Policy:      string(auth.PolicyFirstTimeSetupOrElevated),
		}, route.handler)
	}
}

type environmentHandler struct {
	drives DriveLister
}

// fileSystemEntryInfo is MediaBrowser.Model.IO.FileSystemEntryInfo. Type is
// the FileSystemEntryType enum, which serializes as its name.
type fileSystemEntryInfo struct {
	Name string `json:"Name"`
	Path string `json:"Path"`
	Type string `json:"Type"`
}

const (
	fileSystemEntryFile      = "File"
	fileSystemEntryDirectory = "Directory"
)

// defaultDirectoryBrowserInfoDto is DefaultDirectoryBrowserInfoDto. Upstream
// never sets Path, and WhenWritingNull omits it.
type defaultDirectoryBrowserInfoDto struct {
	Path *string `json:"Path,omitempty"`
}

// validatePathDto is ValidatePathDto.
type validatePathDto struct {
	ValidateWritable bool    `json:"ValidateWritable"`
	Path             *string `json:"Path"`
	IsFile           *bool   `json:"IsFile"`
}

type directoryContentsQuery struct {
	Path               *string `query:"path"`
	IncludeFiles       bool    `query:"includeFiles"`
	IncludeDirectories bool    `query:"includeDirectories"`
}

type parentPathQuery struct {
	Path *string `query:"path"`
}

// directoryContents ports GetDirectoryContents (EnvironmentController.cs:49-67).
func (h environmentHandler) directoryContents(w http.ResponseWriter, r *http.Request) {
	var query directoryContentsQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		renderProblem(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if query.Path == nil || *query.Path == "" {
		writeValidationError(w, "path")
		return
	}
	path := *query.Path
	// A bare UNC server name ("\\server") has no listable contents.
	if strings.HasPrefix(path, `\\`) && strings.LastIndexByte(path, '\\') == 1 {
		_ = render.JSON(w, r, http.StatusOK, []fileSystemEntryInfo{})
		return
	}
	entries, err := fileSystemEntries(path)
	if err != nil {
		writeEnvironmentError(w, err)
		return
	}
	result := make([]fileSystemEntryInfo, 0, len(entries))
	for _, entry := range entries {
		isDirectory := entry.Type == fileSystemEntryDirectory
		if (isDirectory && query.IncludeDirectories) || (!isDirectory && query.IncludeFiles) {
			result = append(result, entry)
		}
	}
	// Upstream orders by FullName with the current culture's comparer.
	// Deadfin orders by ordinal comparison (divergence D-58).
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	_ = render.JSON(w, r, http.StatusOK, result)
}

// fileSystemEntries ports ManagedFileSystem.GetFileSystemEntries with
// IgnoreInaccessible and no skipped attributes (ManagedFileSystem.cs:626-635,
// 711-720): hidden entries are listed, an inaccessible directory lists as
// empty, and a symlink is a directory when its target is a directory.
func fileSystemEntries(path string) ([]fileSystemEntryInfo, error) {
	// DirectoryInfo resolves the path with Path.GetFullPath, which makes it
	// absolute and removes "." and ".." segments without resolving links.
	directory, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	handle, err := os.Open(directory)
	if err != nil {
		if isAccessError(err) {
			return nil, nil
		}
		return nil, err
	}
	defer handle.Close()
	dirEntries, err := handle.ReadDir(-1)
	if err != nil {
		if isAccessError(err) {
			return nil, nil
		}
		return nil, err
	}
	entries := make([]fileSystemEntryInfo, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		full := filepath.Join(directory, dirEntry.Name())
		isDirectory := dirEntry.IsDir()
		if dirEntry.Type()&fs.ModeSymlink != 0 {
			target, statErr := os.Stat(full)
			isDirectory = statErr == nil && target.IsDir()
		}
		entryType := fileSystemEntryFile
		if isDirectory {
			entryType = fileSystemEntryDirectory
		}
		entries = append(entries, fileSystemEntryInfo{Name: dirEntry.Name(), Path: full, Type: entryType})
	}
	return entries, nil
}

// validatePath ports ValidatePath (EnvironmentController.cs:78-130).
func (h environmentHandler) validatePath(w http.ResponseWriter, r *http.Request) {
	var request validatePathDto
	present, err := bind.OptionalJSONBody(r, &request)
	switch {
	case errors.Is(err, bind.ErrUnsupportedMediaType):
		renderProblem(w, r, http.StatusUnsupportedMediaType, "Unsupported Media Type")
		return
	case err != nil || !present:
		renderProblem(w, r, http.StatusBadRequest, "A non-empty request body is required.")
		return
	}
	path := ""
	if request.Path != nil {
		path = *request.Path
	}
	if request.IsFile != nil {
		if *request.IsFile && !fileExists(path) || !*request.IsFile && !directoryExists(path) {
			writeNotFoundProblem(w)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !fileExists(path) && !directoryExists(path) {
		writeNotFoundProblem(w)
		return
	}
	if request.ValidateWritable {
		if err := probeWritable(path); err != nil {
			writeEnvironmentError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// probeWritable creates and deletes an empty file named by a new GUID in
// path, as File.WriteAllText then File.Delete do upstream.
func probeWritable(path string) error {
	name, err := newGUIDString()
	if err != nil {
		return err
	}
	file := filepath.Join(path, name)
	writeErr := os.WriteFile(file, nil, 0o666)
	if _, statErr := os.Lstat(file); statErr == nil {
		if removeErr := os.Remove(file); removeErr != nil && writeErr == nil {
			return removeErr
		}
	}
	return writeErr
}

// newGUIDString returns a random GUID in Guid.ToString() "D" form.
func newGUIDString() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

// fileExists is File.Exists: a non-empty path that names an existing
// non-directory after symlink resolution.
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// directoryExists is Directory.Exists.
func directoryExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// drivesList ports GetDrives (EnvironmentController.cs:137-141).
func (h environmentHandler) drivesList(w http.ResponseWriter, r *http.Request) {
	var mountPoints []string
	if h.drives != nil {
		listed, err := h.drives.Drives(r.Context())
		if err != nil {
			writeEnvironmentError(w, err)
			return
		}
		mountPoints = listed
	}
	result := make([]fileSystemEntryInfo, 0, len(mountPoints))
	for _, mountPoint := range mountPoints {
		result = append(result, fileSystemEntryInfo{Name: mountPoint, Path: mountPoint, Type: fileSystemEntryDirectory})
	}
	_ = render.JSON(w, r, http.StatusOK, result)
}

// parentPath ports GetParentPath (EnvironmentController.cs:148-169). The
// ActionResult<string?> value goes through the JSON formatter, which upstream
// places before the string formatter (ApiServiceCollectionExtensions.cs:
// 125-126): the body is a JSON string, or null for a root path. jellyfin-web
// parses the body with JSON.parse.
func (h environmentHandler) parentPath(w http.ResponseWriter, r *http.Request) {
	var query parentPathQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		renderProblem(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if query.Path == nil || *query.Path == "" {
		writeValidationError(w, "path")
		return
	}
	_ = render.JSON(w, r, http.StatusOK, environmentParentPath(*query.Path))
}

// environmentParentPath is the GetParentPath body: Path.GetDirectoryName
// with the controller's UNC fallback.
func environmentParentPath(path string) *string {
	parent, ok := unixDirectoryName(path)
	if ok && parent != "" {
		return &parent
	}
	index := strings.LastIndexByte(path, '\\')
	if index != -1 && path[0] == '\\' {
		unc := path[:index]
		if strings.TrimSpace(strings.TrimLeft(unc, `\`)) == "" {
			return nil
		}
		return &unc
	}
	if !ok {
		return nil
	}
	return &parent
}

// unixDirectoryName ports .NET Path.GetDirectoryName on Unix. It returns
// ok=false where .NET returns null: for a root path.
func unixDirectoryName(path string) (string, bool) {
	rootLength := 0
	if strings.HasPrefix(path, "/") {
		rootLength = 1
	}
	end := len(path)
	if end <= rootLength {
		return "", false
	}
	for end > rootLength {
		end--
		if path[end] == '/' {
			break
		}
	}
	for end > rootLength && path[end-1] == '/' {
		end--
	}
	return collapseSeparators(path[:end]), true
}

// collapseSeparators is PathInternal.NormalizeDirectorySeparators on Unix:
// it removes repeated '/' characters.
func collapseSeparators(path string) string {
	if !strings.Contains(path, "//") {
		return path
	}
	var builder strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] == '/' && i > 0 && path[i-1] == '/' {
			continue
		}
		builder.WriteByte(path[i])
	}
	return builder.String()
}

// defaultDirectoryBrowser ports GetDefaultDirectoryBrowser
// (EnvironmentController.cs:176-180).
func (h environmentHandler) defaultDirectoryBrowser(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, defaultDirectoryBrowserInfoDto{})
}

// isAccessError reports the errors that EnumerationOptions.IgnoreInaccessible
// skips: EACCES, EPERM, and EBADF.
func isAccessError(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EBADF)
}

// writeEnvironmentError writes the ExceptionMiddleware response for a
// filesystem failure (ExceptionMiddleware.cs:96-101,121-133): a missing
// directory or file is 404, any other failure is 500, and the body is the
// non-development text.
func writeEnvironmentError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		status = http.StatusNotFound
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	_, _ = w.Write([]byte("Error processing request."))
}

// writeNotFoundProblem writes the body of ControllerBase.NotFound() under
// [ApiController]: the client-error ProblemDetails, which only the default
// JSON formatter can write as application/problem+json.
func writeNotFoundProblem(w http.ResponseWriter) {
	body := []byte(`{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.5","title":"Not Found","status":404,"traceId":"` +
		generateTraceID() + `"}`)
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(body)
}
