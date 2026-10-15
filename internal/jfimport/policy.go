// SPDX-License-Identifier: GPL-2.0-only

// Package jfimport is the one-way Jellyfin importer (D3, docs/02-storage.md
// §Importer). It converts users, devices, API keys, and per-user item state
// from a stock Jellyfin database and matches the item state to the items
// Deadfin's own scan found: by user-data key first, then by path.
package jfimport

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store/jellyfinsource"
)

// PermissionKind values (Jellyfin.Database.Implementations/Enums/PermissionKind.cs).
const (
	permIsAdministrator = iota
	permIsHidden
	permIsDisabled
	permEnableSharedDeviceControl
	permEnableRemoteAccess
	permEnableLiveTvManagement
	permEnableLiveTvAccess
	permEnableMediaPlayback
	permEnableAudioPlaybackTranscoding
	permEnableVideoPlaybackTranscoding
	permEnableContentDeletion
	permEnableContentDownloading
	permEnableSyncTranscoding
	permEnableMediaConversion
	permEnableAllDevices
	permEnableAllChannels
	permEnableAllFolders
	permEnablePublicSharing
	permEnableRemoteControlOfOtherUsers
	permEnablePlaybackRemuxing
	permForceRemoteSourceTranscoding
	permEnableCollectionManagement
	permEnableSubtitleManagement
	permEnableLyricManagement
)

// PreferenceKind values (Jellyfin.Database.Implementations/Enums/PreferenceKind.cs).
const (
	prefBlockedTags = iota
	prefBlockedChannels
	prefBlockedMediaFolders
	prefEnabledDevices
	prefEnabledChannels
	prefEnabledFolders
	prefEnableContentDeletionFromFolders
	prefLatestItemExcludes
	prefMyMediaExcludes
	prefGroupedFolders
	prefBlockUnratedItems
	prefOrderedViews
	prefAllowedTags
)

var subtitleModes = []dto.SubtitleMode{dto.SubtitleModeDefault, dto.SubtitleModeAlways, dto.SubtitleModeOnlyForced, dto.SubtitleModeNone, dto.SubtitleModeSmart}

var syncPlayAccess = []dto.SyncPlayAccess{dto.SyncPlayAccessCreateAndJoinGroups, dto.SyncPlayAccessJoinGroups, dto.SyncPlayAccessNone}

// FolderMap translates Jellyfin library ids to Deadfin library ids. Ids that
// do not translate are dropped and reported.
type FolderMap map[dto.UUID]dto.UUID

// NewFolderMap pairs Jellyfin and Deadfin libraries by name.
func NewFolderMap(jellyfin []jellyfinsource.Library, deadfinByName map[string]dto.UUID) FolderMap {
	out := FolderMap{}
	for _, library := range jellyfin {
		if id, ok := deadfinByName[library.Name]; ok {
			out[library.ID] = id
		}
	}
	return out
}

// Policy builds the Deadfin user policy as UserManager.GetUserDto does.
func Policy(user jellyfinsource.User, folders FolderMap) (dto.UserPolicy, []string) {
	var warnings []string
	has := func(kind int) bool { return user.Permissions[kind] }
	policy := dto.DefaultUserPolicy()
	policy.MaxParentalRating = user.MaxParentalRatingScore
	policy.MaxParentalSubRating = user.MaxParentalRatingSubScore
	policy.EnableUserPreferenceAccess = user.EnableUserPreferenceAccess
	policy.RemoteClientBitrateLimit = 0
	if user.RemoteClientBitrateLimit != nil {
		policy.RemoteClientBitrateLimit = *user.RemoteClientBitrateLimit
	}
	policy.AuthenticationProviderID = user.AuthenticationProviderID
	policy.PasswordResetProviderID = user.PasswordResetProviderID
	policy.InvalidLoginAttemptCount = user.InvalidLoginAttemptCount
	policy.LoginAttemptsBeforeLockout = -1
	if user.LoginAttemptsBeforeLockout != nil {
		policy.LoginAttemptsBeforeLockout = *user.LoginAttemptsBeforeLockout
	}
	policy.MaxActiveSessions = user.MaxActiveSessions
	policy.IsAdministrator = has(permIsAdministrator)
	policy.IsHidden = has(permIsHidden)
	policy.IsDisabled = has(permIsDisabled)
	policy.EnableSharedDeviceControl = has(permEnableSharedDeviceControl)
	policy.EnableRemoteAccess = has(permEnableRemoteAccess)
	policy.EnableLiveTvManagement = has(permEnableLiveTvManagement)
	policy.EnableLiveTvAccess = has(permEnableLiveTvAccess)
	policy.EnableMediaPlayback = has(permEnableMediaPlayback)
	policy.EnableAudioPlaybackTranscoding = has(permEnableAudioPlaybackTranscoding)
	policy.EnableVideoPlaybackTranscoding = has(permEnableVideoPlaybackTranscoding)
	policy.EnableContentDeletion = has(permEnableContentDeletion)
	policy.EnableContentDownloading = has(permEnableContentDownloading)
	policy.EnableSyncTranscoding = has(permEnableSyncTranscoding)
	policy.EnableMediaConversion = has(permEnableMediaConversion)
	policy.EnableAllChannels = has(permEnableAllChannels)
	policy.EnableAllDevices = has(permEnableAllDevices)
	policy.EnableAllFolders = has(permEnableAllFolders)
	policy.EnableRemoteControlOfOtherUsers = has(permEnableRemoteControlOfOtherUsers)
	policy.EnablePlaybackRemuxing = has(permEnablePlaybackRemuxing)
	policy.ForceRemoteSourceTranscoding = has(permForceRemoteSourceTranscoding)
	policy.EnablePublicSharing = has(permEnablePublicSharing)
	policy.EnableCollectionManagement = has(permEnableCollectionManagement)
	policy.EnableSubtitleManagement = has(permEnableSubtitleManagement)
	policy.EnableLyricManagement = has(permEnableLyricManagement)
	policy.BlockedTags = list(user.Preferences[prefBlockedTags])
	policy.AllowedTags = list(user.Preferences[prefAllowedTags])
	policy.EnabledDevices = list(user.Preferences[prefEnabledDevices])
	policy.EnableContentDeletionFromFolders = list(user.Preferences[prefEnableContentDeletionFromFolders])
	policy.EnabledChannels = uuids(user.Preferences[prefEnabledChannels], nil, nil)
	policy.BlockedChannels = uuids(user.Preferences[prefBlockedChannels], nil, nil)
	policy.EnabledFolders = uuids(user.Preferences[prefEnabledFolders], folders, &warnings)
	policy.BlockedMediaFolders = uuids(user.Preferences[prefBlockedMediaFolders], folders, &warnings)
	policy.BlockUnratedItems = ints(user.Preferences[prefBlockUnratedItems])
	if user.SyncPlayAccess >= 0 && user.SyncPlayAccess < len(syncPlayAccess) {
		policy.SyncPlayAccess = syncPlayAccess[user.SyncPlayAccess]
	}
	if !policy.EnableAllFolders && len(policy.EnabledFolders) == 0 {
		warnings = append(warnings, "user can see no library: none of its enabled libraries exist in Deadfin")
	}
	return policy, warnings
}

// Configuration builds the Deadfin user configuration as GetUserDto does.
func Configuration(user jellyfinsource.User, folders FolderMap) dto.UserConfiguration {
	config := dto.DefaultUserConfiguration()
	if user.SubtitleMode >= 0 && user.SubtitleMode < len(subtitleModes) {
		config.SubtitleMode = subtitleModes[user.SubtitleMode]
	}
	config.HidePlayedInLatest = user.HidePlayedInLatest
	config.EnableLocalPassword = user.EnableLocalPassword
	config.PlayDefaultAudioTrack = user.PlayDefaultAudioTrack
	config.DisplayCollectionsView = user.DisplayCollectionsView
	config.DisplayMissingEpisodes = user.DisplayMissingEpisodes
	if user.AudioLanguagePreference != "" {
		audio := user.AudioLanguagePreference
		config.AudioLanguagePreference = &audio
	}
	subtitle := user.SubtitleLanguagePreference
	config.SubtitleLanguagePreference = &subtitle
	config.RememberAudioSelections = user.RememberAudioSelections
	config.EnableNextEpisodeAutoPlay = user.EnableNextEpisodeAutoPlay
	config.RememberSubtitleSelections = user.RememberSubtitleSelections
	config.OrderedViews = uuids(user.Preferences[prefOrderedViews], folders, nil)
	config.GroupedFolders = uuids(user.Preferences[prefGroupedFolders], folders, nil)
	config.MyMediaExcludes = uuids(user.Preferences[prefMyMediaExcludes], folders, nil)
	config.LatestItemsExcludes = uuids(user.Preferences[prefLatestItemExcludes], folders, nil)
	if user.CastReceiverID != "" {
		config.CastReceiverID = user.CastReceiverID
	}
	return config
}

// list splits a Jellyfin preference value (UserEntityExtensions.Delimiter ',').
func list(value string) []string {
	if value == "" {
		return []string{}
	}
	return strings.Split(value, ",")
}

// uuids parses ids. With folders set, it translates library ids and drops the
// ones that have no Deadfin library.
func uuids(value string, folders FolderMap, warnings *[]string) []dto.UUID {
	out := []dto.UUID{}
	for _, raw := range list(value) {
		id, err := dto.ParseUUID(raw)
		if err != nil {
			continue
		}
		if folders != nil {
			mapped, ok := folders[id]
			if !ok {
				if warnings != nil {
					*warnings = append(*warnings, fmt.Sprintf("library %s has no Deadfin library of the same name; access to it is dropped", id))
				}
				continue
			}
			id = mapped
		}
		out = append(out, id)
	}
	return out
}

func ints(value string) []int {
	out := []int{}
	for _, raw := range list(value) {
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			out = append(out, parsed)
		}
	}
	return out
}
