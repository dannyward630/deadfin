// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
)

// RegisterLiveTv registers the deferred LiveTv surface. D2 keeps LiveTv
// disabled, so the handlers publish empty capability and collection shapes.
func RegisterLiveTv(rt *router.Router) {
	h := liveTvStub{}
	access := string(auth.PolicyLiveTVAccess)
	manage := string(auth.PolicyLiveTVManagement)
	elevated := string(auth.PolicyRequiresElevation)

	rt.Handle(http.MethodGet, "/LiveTv/Info", router.Route{OperationID: "GetLiveTvInfo", Policy: access}, h.info)

	for _, route := range []struct {
		method      string
		pattern     string
		operationID string
	}{
		{http.MethodGet, "/LiveTv/Channels", "GetLiveTvChannels"},
		{http.MethodGet, "/LiveTv/Programs", "GetLiveTvPrograms"},
		{http.MethodPost, "/LiveTv/Programs", "GetPrograms"},
		{http.MethodGet, "/LiveTv/Programs/Recommended", "GetRecommendedPrograms"},
		{http.MethodGet, "/LiveTv/Recordings", "GetRecordings"},
		{http.MethodGet, "/LiveTv/Recordings/Folders", "GetRecordingFolders"},
		{http.MethodGet, "/LiveTv/SeriesTimers", "GetSeriesTimers"},
		{http.MethodGet, "/LiveTv/Timers", "GetTimers"},
	} {
		rt.Handle(route.method, route.pattern, router.Route{OperationID: route.operationID, Policy: access}, h.emptyQueryResult)
	}

	for _, route := range []struct {
		pattern     string
		operationID string
		policy      string
	}{
		{"/LiveTv/ListingProviders/Lineups", "GetLineups", access},
		{"/LiveTv/TunerHosts/Types", "GetTunerHostTypes", access},
		{"/LiveTv/Tuners/Discover", "DiscoverTuners", elevated},
		{"/LiveTv/Tuners/Discvover", "DiscvoverTuners", elevated},
	} {
		rt.Handle(http.MethodGet, route.pattern, router.Route{OperationID: route.operationID, Policy: route.policy}, h.emptyArray)
	}

	rt.Handle(http.MethodGet, "/LiveTv/GuideInfo", router.Route{OperationID: "GetGuideInfo", Policy: access}, h.emptyObject)
	rt.Handle(http.MethodGet, "/LiveTv/ListingProviders/Default", router.Route{OperationID: "GetDefaultListingProvider", Policy: access}, h.emptyObject)
	rt.Handle(http.MethodGet, "/LiveTv/Timers/{timerId}", router.Route{OperationID: "GetTimer", Policy: access}, h.emptyObject)
	rt.Handle(http.MethodGet, "/LiveTv/Timers/Defaults", router.Route{OperationID: "GetDefaultTimer", Policy: access}, h.emptyObject)
	rt.Handle(http.MethodGet, "/LiveTv/ListingProviders/SchedulesDirect/Countries", router.Route{OperationID: "GetSchedulesDirectCountries", Policy: elevated}, h.emptyObject)
	rt.Handle(http.MethodGet, "/LiveTv/ChannelMappingOptions", router.Route{OperationID: "GetChannelMappingOptions", Policy: elevated}, h.channelMappingOptions)

	// These administrative creates have only a documented 200 response. An
	// empty object is valid for each optional-property response DTO and avoids
	// inventing a configured provider, tuner, or mapping.
	rt.Handle(http.MethodPost, "/LiveTv/ListingProviders", router.Route{OperationID: "AddListingProvider", Policy: elevated}, h.emptyObject)
	rt.Handle(http.MethodPost, "/LiveTv/TunerHosts", router.Route{OperationID: "AddTunerHost", Policy: elevated}, h.emptyObject)
	rt.Handle(http.MethodPost, "/LiveTv/ChannelMappings", router.Route{OperationID: "SetChannelMapping", Policy: elevated}, h.emptyObject)

	// A 404 is observable on these OpenAPI operations. Report the deferred
	// no-tuner state instead of pretending that an item or stream exists.
	for _, route := range []struct {
		method      string
		pattern     string
		operationID string
		policy      string
		anonymous   bool
	}{
		{http.MethodGet, "/LiveTv/Channels/{channelId}", "GetChannel", access, false},
		{http.MethodGet, "/LiveTv/Programs/{programId}", "GetProgram", access, false},
		{http.MethodGet, "/LiveTv/Recordings/{recordingId}", "GetRecording", access, false},
		{http.MethodDelete, "/LiveTv/Recordings/{recordingId}", "DeleteRecording", manage, false},
		{http.MethodGet, "/LiveTv/SeriesTimers/{timerId}", "GetSeriesTimer", access, false},
		{http.MethodGet, "/LiveTv/LiveRecordings/{recordingId}/stream", "GetLiveRecordingFile", "", true},
		{http.MethodGet, "/LiveTv/LiveStreamFiles/{streamId}/stream.{container}", "GetLiveStreamFile", "", true},
	} {
		rt.Handle(route.method, route.pattern, router.Route{
			OperationID: route.operationID,
			Policy:      route.policy,
			Anonymous:   route.anonymous,
		}, h.notConfigured)
	}

	// These mutations expose only 204 in the pinned spec. Keep their documented
	// empty-success shape; returning an undocumented 404 would break clients.
	for _, route := range []struct {
		method      string
		pattern     string
		operationID string
		policy      string
	}{
		{http.MethodDelete, "/LiveTv/ListingProviders", "DeleteListingProvider", elevated},
		{http.MethodPost, "/LiveTv/SeriesTimers", "CreateSeriesTimer", manage},
		{http.MethodDelete, "/LiveTv/SeriesTimers/{timerId}", "CancelSeriesTimer", manage},
		{http.MethodPost, "/LiveTv/SeriesTimers/{timerId}", "UpdateSeriesTimer", manage},
		{http.MethodPost, "/LiveTv/Timers", "CreateTimer", manage},
		{http.MethodDelete, "/LiveTv/Timers/{timerId}", "CancelTimer", manage},
		{http.MethodPost, "/LiveTv/Timers/{timerId}", "UpdateTimer", manage},
		{http.MethodDelete, "/LiveTv/TunerHosts", "DeleteTunerHost", elevated},
		{http.MethodPost, "/LiveTv/Tuners/{tunerId}/Reset", "ResetTuner", elevated},
	} {
		rt.Handle(route.method, route.pattern, router.Route{OperationID: route.operationID, Policy: route.policy}, h.noContent)
	}
}

type liveTvStub struct{}

type liveTvInfo struct {
	Services     []struct{} `json:"Services"`
	IsEnabled    bool       `json:"IsEnabled"`
	EnabledUsers []string   `json:"EnabledUsers"`
}

type channelMappingOptions struct {
	TunerChannels    []struct{} `json:"TunerChannels"`
	ProviderChannels []struct{} `json:"ProviderChannels"`
	Mappings         []struct{} `json:"Mappings"`
}

type deferredProblem struct {
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func (liveTvStub) info(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, liveTvInfo{
		Services:     []struct{}{},
		EnabledUsers: []string{},
	})
}

func (liveTvStub) emptyQueryResult(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, dto.NewQueryResult[struct{}]())
}

func (liveTvStub) emptyArray(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, []struct{}{})
}

func (liveTvStub) emptyObject(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, struct{}{})
}

func (liveTvStub) channelMappingOptions(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, channelMappingOptions{
		TunerChannels:    []struct{}{},
		ProviderChannels: []struct{}{},
		Mappings:         []struct{}{},
	})
}

func (liveTvStub) notConfigured(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusNotFound, deferredProblem{
		Title:  http.StatusText(http.StatusNotFound),
		Status: http.StatusNotFound,
		Detail: "no tuners configured",
	})
}

func (liveTvStub) noContent(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
