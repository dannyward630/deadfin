// SPDX-License-Identifier: GPL-2.0-only

// Package sessioncontrol authorizes and routes remote-control commands without
// coupling HTTP handlers to WebSocket connection state.
package sessioncontrol

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/user"
	"github.com/deadfin-org/deadfin/internal/ws"
)

var (
	ErrBadRequest = errors.New("sessioncontrol: bad request")
	ErrForbidden  = errors.New("sessioncontrol: forbidden")
)

// Sender delivers an authorized command to the target session controller.
type Sender interface {
	Send(context.Context, string, ws.MessageType, any) error
}

// Service owns AssertCanControl and command construction.
type Service struct {
	sessions *session.Manager
	users    *user.Service
	sender   Sender
}

// New constructs the remote-control application service.
func New(sessions *session.Manager, users *user.Service, sender Sender) *Service {
	return &Service{sessions: sessions, users: users, sender: sender}
}

// Play sends one Jellyfin Play request.
func (service *Service) Play(ctx context.Context, actor user.Actor, targetID string, request dto.PlayRequest) error {
	command, valid := canonicalPlayCommand(request.PlayCommand)
	if len(request.ItemIDs) == 0 || !valid {
		return ErrBadRequest
	}
	if _, err := service.assertCanControl(ctx, actor, targetID); err != nil {
		return err
	}
	request.PlayCommand = command
	request.ControllingUserID = actor.UserID
	return service.sender.Send(ctx, targetID, ws.MessagePlay, request)
}

// Playstate sends one Jellyfin Playstate request.
func (service *Service) Playstate(ctx context.Context, actor user.Actor, targetID string, request dto.PlaystateRequest) error {
	command, valid := canonicalPlaystateCommand(request.Command)
	if !valid {
		return ErrBadRequest
	}
	request.Command = command
	if _, err := service.assertCanControl(ctx, actor, targetID); err != nil {
		return err
	}
	if !actor.UserID.IsNil() {
		value := actor.UserID.String()
		request.ControllingUserID = &value
	}
	return service.sender.Send(ctx, targetID, ws.MessagePlaystate, request)
}

// General sends a named GeneralCommand.
func (service *Service) General(ctx context.Context, actor user.Actor, targetID string, command dto.GeneralCommand) error {
	name, valid := canonicalGeneralCommand(command.Name)
	if !valid {
		return ErrBadRequest
	}
	command.Name = name
	if _, err := service.assertCanControl(ctx, actor, targetID); err != nil {
		return err
	}
	command.ControllingUserID = actor.UserID
	if command.Arguments == nil {
		command.Arguments = map[string]string{}
	}
	return service.sender.Send(ctx, targetID, ws.MessageGeneralCommand, command)
}

// Message translates the HTTP MessageCommand into DisplayMessage.
func (service *Service) Message(ctx context.Context, actor user.Actor, targetID string, command dto.MessageCommand) error {
	if strings.TrimSpace(command.Text) == "" {
		return ErrBadRequest
	}
	header := strings.TrimSpace(command.Header)
	if header == "" {
		header = "Message from Server"
	}
	arguments := map[string]string{"Header": header, "Text": command.Text}
	if command.TimeoutMS != nil {
		arguments["TimeoutMs"] = strconv.FormatInt(*command.TimeoutMS, 10)
	}
	return service.General(ctx, actor, targetID, dto.GeneralCommand{Name: "DisplayMessage", Arguments: arguments})
}

// View translates the browse route into DisplayContent.
func (service *Service) View(ctx context.Context, actor user.Actor, targetID, itemType, itemID, itemName string) error {
	if strings.TrimSpace(itemType) == "" || strings.TrimSpace(itemID) == "" || strings.TrimSpace(itemName) == "" {
		return ErrBadRequest
	}
	return service.General(ctx, actor, targetID, dto.GeneralCommand{
		Name: "DisplayContent",
		Arguments: map[string]string{
			"ItemId": itemID, "ItemName": itemName, "ItemType": itemType,
		},
	})
}

func (service *Service) assertCanControl(ctx context.Context, actor user.Actor, targetID string) (session.Snapshot, error) {
	target, found := service.sessions.SessionByID(targetID)
	if !found {
		return session.Snapshot{}, session.ErrNotFound
	}
	if actor.APIKey {
		return target, nil
	}
	if actor.UserID.IsNil() {
		return session.Snapshot{}, ErrForbidden
	}
	visible, err := service.users.FilterSessions(ctx, actor, &actor.UserID, []session.Snapshot{target})
	if err != nil {
		if errors.Is(err, user.ErrForbidden) || errors.Is(err, user.ErrUnauthorized) {
			return session.Snapshot{}, ErrForbidden
		}
		return session.Snapshot{}, err
	}
	if len(visible) != 1 {
		return session.Snapshot{}, ErrForbidden
	}
	return target, nil
}

func canonicalPlayCommand(value string) (string, bool) {
	switch strings.ToLower(value) {
	case "playnow":
		return "PlayNow", true
	case "playnext":
		return "PlayNext", true
	case "playlast":
		return "PlayLast", true
	case "playinstantmix":
		return "PlayInstantMix", true
	case "playshuffle":
		return "PlayShuffle", true
	default:
		return "", false
	}
}

func canonicalPlaystateCommand(value string) (string, bool) {
	switch strings.ToLower(value) {
	case "stop":
		return "Stop", true
	case "pause":
		return "Pause", true
	case "unpause":
		return "Unpause", true
	case "nexttrack":
		return "NextTrack", true
	case "previoustrack":
		return "PreviousTrack", true
	case "seek":
		return "Seek", true
	case "rewind":
		return "Rewind", true
	case "fastforward":
		return "FastForward", true
	case "playpause":
		return "PlayPause", true
	default:
		return "", false
	}
}

var generalCommands = []string{
	"MoveUp", "MoveDown", "MoveLeft", "MoveRight", "PageUp", "PageDown",
	"PreviousLetter", "NextLetter", "ToggleOsd", "ToggleContextMenu", "Select", "Back",
	"TakeScreenshot", "SendKey", "SendString", "GoHome", "GoToSettings", "VolumeUp",
	"VolumeDown", "Mute", "Unmute", "ToggleMute", "SetVolume", "SetAudioStreamIndex",
	"SetSubtitleStreamIndex", "ToggleFullscreen", "DisplayContent", "GoToSearch",
	"DisplayMessage", "SetRepeatMode", "ChannelUp", "ChannelDown", "Guide", "ToggleStats",
	"PlayMediaSource", "PlayTrailers", "SetShuffleQueue", "PlayState", "PlayNext",
	"ToggleOsdMenu", "Play", "SetMaxStreamingBitrate", "SetPlaybackOrder",
}

func canonicalGeneralCommand(value string) (string, bool) {
	for _, command := range generalCommands {
		if strings.EqualFold(command, strings.TrimSpace(value)) {
			return command, true
		}
	}
	return "", false
}
