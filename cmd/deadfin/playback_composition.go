// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/jsonnum"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/user"
)

type playbackDeviceProfiles struct{ sessions *session.Manager }

func (profiles playbackDeviceProfiles) DeviceProfile(_ context.Context, client, deviceID string) (*playback.DeviceProfile, error) {
	raw, found := profiles.sessions.DeviceProfile(client, deviceID)
	if !found {
		return nil, nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	var profile playback.DeviceProfile
	if err := jsonnum.Unmarshal(raw, &profile); err != nil {
		return nil, fmt.Errorf("%w: %v", playback.ErrInvalidStoredDeviceProfile, err)
	}
	return &profile, nil
}

type playbackPolicyReader struct {
	users   *user.Service
	manager *config.Manager
}

func (reader playbackPolicyReader) PlaybackPermissions(ctx context.Context, userID dto.UUID) (playback.PlaybackPermissions, error) {
	stored, err := reader.users.Get(ctx, userID)
	if err != nil {
		return playback.PlaybackPermissions{}, err
	}
	policy := dto.DefaultUserPolicy()
	if len(stored.Policy) != 0 {
		if err := json.Unmarshal(stored.Policy, &policy); err != nil {
			return playback.PlaybackPermissions{}, err
		}
	}
	permissions := playback.PlaybackPermissions{
		EnableAudioTranscoding:       policy.EnableAudioPlaybackTranscoding,
		EnableVideoTranscoding:       policy.EnableVideoPlaybackTranscoding,
		EnableRemuxing:               policy.EnablePlaybackRemuxing,
		ForceRemoteSourceTranscoding: policy.ForceRemoteSourceTranscoding,
	}
	limit := policy.RemoteClientBitrateLimit
	if limit <= 0 && reader.manager != nil {
		limit = reader.manager.System().RemoteClientBitrateLimit
	}
	if limit > 0 {
		permissions.RemoteBitrateLimit = &limit
	}
	return permissions, nil
}

type playbackNetworkClassifier struct {
	network interface{ IsInLocalNetwork(netip.Addr) bool }
}

func (classifier playbackNetworkClassifier) IsLAN(address netip.Addr) bool {
	return classifier.network.IsInLocalNetwork(address)
}

type playbackSourceReader struct{ items *store.ItemStore }

func (reader playbackSourceReader) MediaSourceRecords(ctx context.Context, itemID dto.UUID) ([]playback.MediaSourceRecord, error) {
	records, err := reader.items.MediaSourceRecords(ctx, itemID)
	if store.IsNotFound(err) {
		return nil, playback.ErrItemNotFound
	}
	return records, err
}
