// SPDX-License-Identifier: GPL-2.0-only

package dto

// PlayRequest is the outbound Play WebSocket payload.
type PlayRequest struct {
	ItemIDs             []UUID  `json:"ItemIds"`
	StartPositionTicks  *int64  `json:"StartPositionTicks,omitzero"`
	PlayCommand         string  `json:"PlayCommand"`
	ControllingUserID   UUID    `json:"ControllingUserId"`
	SubtitleStreamIndex *int    `json:"SubtitleStreamIndex,omitzero"`
	AudioStreamIndex    *int    `json:"AudioStreamIndex,omitzero"`
	MediaSourceID       *string `json:"MediaSourceId,omitzero"`
	StartIndex          *int    `json:"StartIndex,omitzero"`
}

// PlaystateRequest is the outbound Playstate WebSocket payload.
type PlaystateRequest struct {
	Command           string  `json:"Command"`
	SeekPositionTicks *int64  `json:"SeekPositionTicks,omitzero"`
	ControllingUserID *string `json:"ControllingUserId,omitzero"`
}

// GeneralCommand is the outbound GeneralCommand WebSocket payload.
type GeneralCommand struct {
	Name              string            `json:"Name"`
	ControllingUserID UUID              `json:"ControllingUserId"`
	Arguments         map[string]string `json:"Arguments"`
}

// MessageCommand is the HTTP body accepted by /Sessions/{id}/Message.
type MessageCommand struct {
	Header    string `json:"Header"`
	Text      string `json:"Text"`
	TimeoutMS *int64 `json:"TimeoutMs,omitzero"`
}
