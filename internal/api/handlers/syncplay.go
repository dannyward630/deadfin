// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"net/http"

	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
)

// RegisterSyncPlay registers the deferred SyncPlay surface. Deadfin does not
// implement SyncPlay groups, so the capability is disabled. The route policy
// metadata preserves the upstream authorization structure for the ledger, and
// the handlers enforce the disabled state: List returns an empty group array,
// a single-group lookup reports that the group does not exist, and every
// mutation reports that SyncPlay is disabled.
func RegisterSyncPlay(rt *router.Router) {
	h := syncPlayStub{}
	hasAccess := string(auth.PolicySyncPlayHasAccess)
	canCreate := string(auth.PolicySyncPlayCreateGroup)
	canJoin := string(auth.PolicySyncPlayJoinGroup)

	rt.Handle(http.MethodGet, "/SyncPlay/List", router.Route{OperationID: "SyncPlayGetGroups", Policy: canJoin}, h.emptyList)
	rt.Handle(http.MethodGet, "/SyncPlay/{id}", router.Route{OperationID: "SyncPlayGetGroup", Policy: canJoin}, h.groupNotFound)
	rt.Handle(http.MethodPost, "/SyncPlay/New", router.Route{OperationID: "SyncPlayCreateGroup", Policy: canCreate}, h.disabled)
	rt.Handle(http.MethodPost, "/SyncPlay/Join", router.Route{OperationID: "SyncPlayJoinGroup", Policy: canJoin}, h.disabled)

	// The controller-level HasAccess policy is the useful declarative check for
	// the deferred existing-group commands. Deadfin does not own group state,
	// so there is no in-memory IsInGroup fact to evaluate or persist. Every
	// mutation reports the disabled capability.
	for _, route := range []struct {
		pattern     string
		operationID string
	}{
		{"/SyncPlay/Buffering", "SyncPlayBuffering"},
		{"/SyncPlay/Leave", "SyncPlayLeaveGroup"},
		{"/SyncPlay/MovePlaylistItem", "SyncPlayMovePlaylistItem"},
		{"/SyncPlay/NextItem", "SyncPlayNextItem"},
		{"/SyncPlay/Pause", "SyncPlayPause"},
		{"/SyncPlay/Ping", "SyncPlayPing"},
		{"/SyncPlay/PreviousItem", "SyncPlayPreviousItem"},
		{"/SyncPlay/Queue", "SyncPlayQueue"},
		{"/SyncPlay/Ready", "SyncPlayReady"},
		{"/SyncPlay/RemoveFromPlaylist", "SyncPlayRemoveFromPlaylist"},
		{"/SyncPlay/Seek", "SyncPlaySeek"},
		{"/SyncPlay/SetIgnoreWait", "SyncPlaySetIgnoreWait"},
		{"/SyncPlay/SetNewQueue", "SyncPlaySetNewQueue"},
		{"/SyncPlay/SetPlaylistItem", "SyncPlaySetPlaylistItem"},
		{"/SyncPlay/SetRepeatMode", "SyncPlaySetRepeatMode"},
		{"/SyncPlay/SetShuffleMode", "SyncPlaySetShuffleMode"},
		{"/SyncPlay/Stop", "SyncPlayStop"},
		{"/SyncPlay/Unpause", "SyncPlayUnpause"},
	} {
		rt.Handle(http.MethodPost, route.pattern, router.Route{OperationID: route.operationID, Policy: hasAccess}, h.disabled)
	}
}

type syncPlayStub struct{}

func (syncPlayStub) emptyList(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusOK, []struct{}{})
}

func (syncPlayStub) groupNotFound(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusNotFound, deferredProblem{
		Title:  http.StatusText(http.StatusNotFound),
		Status: http.StatusNotFound,
		Detail: "sync play group not found",
	})
}

func (syncPlayStub) disabled(w http.ResponseWriter, r *http.Request) {
	_ = render.JSON(w, r, http.StatusForbidden, deferredProblem{
		Title:  http.StatusText(http.StatusForbidden),
		Status: http.StatusForbidden,
		Detail: "sync play is not enabled",
	})
}
