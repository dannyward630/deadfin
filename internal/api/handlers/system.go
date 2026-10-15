// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
)

// RegisterSystem registers anonymous system discovery endpoints on rt.
func RegisterSystem(rt *router.Router, identity config.ServerIdentity, startup interface{ StartupWizardCompleted() bool }) {
	RegisterSystemWithConfig(rt, identity, startup, SystemInfoConfig{})
}

// SystemInfoConfig supplies process-owned paths, capabilities, and live
// configuration accessors for SystemInfo responses.
type SystemInfoConfig struct {
	Paths                             config.Paths
	WebPath                           string
	CastReceiverApplications          func() []config.CastReceiverApplication
	SupportsLibraryMonitor            bool
	WebSocketPortNumber               int
	Identity                          func() config.ServerIdentity
	LocalAddress                      string
	EnablePublishedServerURIByRequest func() bool
	Network                           auth.LocalNetwork
}

// effectiveWebPath resolves the directory that /web serves and SystemInfo
// reports as WebPath; both callers share this single computation so the
// served directory and the reported path cannot drift.
func (c SystemInfoConfig) effectiveWebPath() string {
	if c.WebPath != "" {
		return c.WebPath
	}
	if c.Paths.Root != "" {
		return filepath.Join(c.Paths.Root, "web")
	}
	return ""
}

func RegisterSystemWithConfig(rt *router.Router, identity config.ServerIdentity, startup interface{ StartupWizardCompleted() bool }, infoConfig SystemInfoConfig) {
	h := system{identity: identity, startup: startup, infoConfig: infoConfig}

	rt.Handle(http.MethodGet, "/System/Info/Public", router.Route{
		OperationID: "GetPublicSystemInfo",
		Anonymous:   true,
	}, h.publicInfo)
	rt.Handle(http.MethodGet, "/System/Info", router.Route{
		OperationID: "GetSystemInfo",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}, h.info)
	rt.Handle(http.MethodGet, "/System/Endpoint", router.Route{
		OperationID: "GetEndpointInfo",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}, h.endpoint)
	rt.Handle(http.MethodGet, "/System/Ping", router.Route{
		OperationID: "GetPingSystem",
		Anonymous:   true,
	}, h.ping)
	rt.Handle(http.MethodPost, "/System/Ping", router.Route{
		OperationID: "PostPingSystem",
		Anonymous:   true,
	}, h.ping)

	registerWeb(rt, infoConfig.effectiveWebPath())
}

type system struct {
	identity   config.ServerIdentity
	startup    interface{ StartupWizardCompleted() bool }
	infoConfig SystemInfoConfig
}

func (h system) currentIdentity() config.ServerIdentity {
	if h.infoConfig.Identity != nil {
		return h.infoConfig.Identity()
	}
	return h.identity
}

func (h system) publicInfo(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, h.publicIdentity(r))
}

func (h system) publicIdentity(r *http.Request) dto.PublicSystemInfo {
	identity := h.currentIdentity()
	return dto.PublicSystemInfo{
		LocalAddress:           h.localAddress(r),
		ServerName:             identity.ServerName,
		Version:                identity.Version,
		ProductName:            "Jellyfin Server",
		OperatingSystem:        "",
		ID:                     identity.ID,
		StartupWizardCompleted: h.startup.StartupWizardCompleted(),
	}
}

// info exposes the authenticated SystemInfo contract from immutable process
// configuration; discovery remains intentionally independent of these paths.
func (h system) info(w http.ResponseWriter, r *http.Request) {
	info := dto.SystemInfo{
		PublicSystemInfo:           h.publicIdentity(r),
		OperatingSystemDisplayName: "",
		SupportsLibraryMonitor:     h.infoConfig.SupportsLibraryMonitor,
		WebSocketPortNumber:        h.infoConfig.WebSocketPortNumber,
		CompletedInstallations:     []any{},
		CanSelfRestart:             false,
		CanSelfUpdate:              false,
		CastReceiverApplications:   h.castReceiverApplications(),
		EncoderLocation:            "System",
		SystemArchitecture:         jellyfinArchitecture(runtime.GOARCH),
	}
	if root := h.infoConfig.Paths.Root; root != "" {
		info.ProgramDataPath = systemString(root)
		info.ItemsByNamePath = systemString(h.infoConfig.Paths.Metadata)
		info.CachePath = systemString(h.infoConfig.Paths.Cache)
		info.LogPath = systemString(h.infoConfig.Paths.Log)
		info.InternalMetadataPath = systemString(h.infoConfig.Paths.Metadata)
		info.TranscodingTempPath = systemString(h.infoConfig.Paths.Transcodes)
	}
	webPath := h.infoConfig.effectiveWebPath()
	if webPath != "" {
		info.WebPath = systemString(webPath)
	}
	_ = render.JSON(w, r, http.StatusOK, info)
}

func (h system) castReceiverApplications() []dto.CastReceiverApplication {
	if h.infoConfig.CastReceiverApplications == nil {
		return []dto.CastReceiverApplication{}
	}
	configured := h.infoConfig.CastReceiverApplications()
	if len(configured) == 0 {
		return []dto.CastReceiverApplication{}
	}
	applications := make([]dto.CastReceiverApplication, len(configured))
	for index, application := range configured {
		applications[index] = dto.CastReceiverApplication{ID: application.ID, Name: application.Name}
	}
	return applications
}

func systemString(value string) *string { return &value }

func (h system) localAddress(r *http.Request) string {
	base := router.PathBase(r.Context())
	configured := h.infoConfig.LocalAddress
	if configured != "" && !wildcardListenerAddress(configured) && (h.infoConfig.EnablePublishedServerURIByRequest == nil || !h.infoConfig.EnablePublishedServerURIByRequest()) {
		return "http://" + configured + base
	}
	return requestAddress(r) + base
}

func wildcardListenerAddress(address string) bool {
	if parsed, err := netip.ParseAddr(address); err == nil && parsed.IsUnspecified() {
		return true
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "" {
		return true
	}
	parsed, err := netip.ParseAddr(host)
	return err == nil && parsed.IsUnspecified()
}

func jellyfinArchitecture(goarch string) string {
	switch goarch {
	case "386":
		return "X86"
	case "amd64":
		return "X64"
	case "arm":
		return "Arm"
	case "arm64":
		return "Arm64"
	default:
		return goarch
	}
}

func requestAddress(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (system) ping(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, "Jellyfin Server")
}

// endpoint reports transport locality for the authenticated caller. The
// response uses bare application/json (no charset) so it matches the stock
// capture exactly; render.JSON would append charset=utf-8 and perturb the
// media type of every JSON route, so the handler writes its own response.
func (h system) endpoint(w http.ResponseWriter, r *http.Request) {
	remote := middleware.ClientAddressFromContext(r.Context())
	local := localAddr(r)

	// IsLocal mirrors upstream HttpContext.IsLocal(): true when the
	// connection's local and remote IPs are equal, or when both are
	// unavailable. Unmap 4-in-6 on both sides before the comparison.
	remoteIP := remote.Unmap()
	localIP := local.Unmap()
	bothUnavailable := !remoteIP.IsValid() && !localIP.IsValid()
	info := dto.EndPointInfo{
		IsLocal:     bothUnavailable || remoteIP == localIP,
		IsInNetwork: h.isInLocalNetwork(remote),
	}

	var body []byte
	if render.NegotiateRequest(r) == render.CasingCamel {
		var err error
		body, err = render.MarshalCamel(info)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", `application/json; profile="CamelCase"`)
	} else {
		var err error
		body, err = json.Marshal(info)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// isInLocalNetwork reports whether the remote peer is in the local network.
// An invalid or missing remote defaults to loopback, matching upstream's
// GetNormalizedRemoteIP behavior. A nil Network returns false so the
// ledger-only router does not panic.
func (h system) isInLocalNetwork(remote netip.Addr) bool {
	if h.infoConfig.Network == nil {
		return false
	}
	if !remote.IsValid() {
		remote = netip.AddrFrom4([4]byte{127, 0, 0, 1})
	}
	return h.infoConfig.Network.IsInLocalNetwork(remote)
}

// localAddr extracts the local socket address the request arrived on from
// the stdlib server's context.
func localAddr(r *http.Request) netip.Addr {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return netip.Addr{}
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	parsed, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return parsed
}
