// SPDX-License-Identifier: GPL-2.0-only

package ws

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// MessageType is Jellyfin's SessionMessageType string representation.
type MessageType string

const (
	MessageUserDataChanged              MessageType = "UserDataChanged"
	MessageLibraryChanged               MessageType = "LibraryChanged"
	MessageSessions                     MessageType = "Sessions"
	MessageScheduledTasksInfo           MessageType = "ScheduledTasksInfo"
	MessageRefreshProgress              MessageType = "RefreshProgress"
	MessagePlay                         MessageType = "Play"
	MessagePlaystate                    MessageType = "Playstate"
	MessageGeneralCommand               MessageType = "GeneralCommand"
	MessageSyncPlayCommand              MessageType = "SyncPlayCommand"
	MessageSyncPlayGroupUpdate          MessageType = "SyncPlayGroupUpdate"
	MessageTimerCreated                 MessageType = "TimerCreated"
	MessageSeriesTimerCreated           MessageType = "SeriesTimerCreated"
	MessageTimerCancelled               MessageType = "TimerCancelled"
	MessageSeriesTimerCancelled         MessageType = "SeriesTimerCancelled"
	MessagePackageInstalling            MessageType = "PackageInstalling"
	MessagePackageInstallationCompleted MessageType = "PackageInstallationCompleted"
	MessagePackageInstallationFailed    MessageType = "PackageInstallationFailed"
	MessagePackageInstallationCancelled MessageType = "PackageInstallationCancelled"
	MessageServerShuttingDown           MessageType = "ServerShuttingDown"
	MessageServerRestarting             MessageType = "ServerRestarting"
	MessageRestartRequired              MessageType = "RestartRequired"

	MessageForceKeepAlive MessageType = "ForceKeepAlive"
	MessageKeepAlive      MessageType = "KeepAlive"

	MessageSessionsStart           MessageType = "SessionsStart"
	MessageSessionsStop            MessageType = "SessionsStop"
	MessageActivityLogEntry        MessageType = "ActivityLogEntry"
	MessageActivityLogEntryStart   MessageType = "ActivityLogEntryStart"
	MessageActivityLogEntryStop    MessageType = "ActivityLogEntryStop"
	MessageScheduledTasksInfoStart MessageType = "ScheduledTasksInfoStart"
	MessageScheduledTasksInfoStop  MessageType = "ScheduledTasksInfoStop"
)

// WebOutboundMessageTypes is the complete set consumed by jellyfin-web.
var WebOutboundMessageTypes = [...]MessageType{
	MessageUserDataChanged,
	MessageLibraryChanged,
	MessageSessions,
	MessageScheduledTasksInfo,
	MessageRefreshProgress,
	MessagePlay,
	MessagePlaystate,
	MessageGeneralCommand,
	MessageSyncPlayCommand,
	MessageSyncPlayGroupUpdate,
	MessageTimerCreated,
	MessageSeriesTimerCreated,
	MessageTimerCancelled,
	MessageSeriesTimerCancelled,
	MessagePackageInstalling,
	MessagePackageInstallationCompleted,
	MessagePackageInstallationFailed,
	MessagePackageInstallationCancelled,
	MessageServerShuttingDown,
	MessageServerRestarting,
	MessageRestartRequired,
}

// InboundEnvelope is the common shape sent by Jellyfin clients.
type InboundEnvelope struct {
	MessageType MessageType     `json:"MessageType"`
	Data        json.RawMessage `json:"Data,omitzero"`
}

// Envelope is the generic Jellyfin outbound shape. Data is omitted for
// no-payload variants such as KeepAlive.
type Envelope struct {
	MessageType MessageType `json:"MessageType"`
	MessageID   dto.UUID    `json:"MessageId"`
	Data        any         `json:"Data,omitempty"`
}

type envelopeFactory struct {
	random io.Reader
}

func (factory envelopeFactory) new(messageType MessageType, data any) (Envelope, error) {
	var id dto.UUID
	if _, err := io.ReadFull(factory.random, id[:]); err != nil {
		return Envelope{}, fmt.Errorf("ws message id: %w", err)
	}
	return Envelope{MessageType: messageType, MessageID: id, Data: data}, nil
}

// UserDataChangeInfo is UserDataChanged's fully hydrated payload.
type UserDataChangeInfo struct {
	UserID       dto.UUID              `json:"UserId"`
	UserDataList []dto.UserItemDataDto `json:"UserDataList"`
}

// RefreshProgressInfo is the WebSocket projection of scanner progress.
type RefreshProgressInfo struct {
	ItemID   dto.UUID `json:"ItemId"`
	Progress float64  `json:"Progress"`
}
