// SPDX-License-Identifier: GPL-2.0-only

package storetest

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// JellyfinFixture writes a jellyfin.db with one user, one device token, one
// API key, and one empty playlist, in the column shapes Jellyfin 10.11 writes (see
// internal/store/jellyfinsource/source_test.go). It returns the file path.
func JellyfinFixture(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jellyfin.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE Users (Id TEXT PRIMARY KEY, Username TEXT, Password TEXT, AuthenticationProviderId TEXT, PasswordResetProviderId TEXT,
			AudioLanguagePreference TEXT, SubtitleLanguagePreference TEXT, SubtitleMode INTEGER, CastReceiverId TEXT,
			DisplayCollectionsView INTEGER, DisplayMissingEpisodes INTEGER, EnableLocalPassword INTEGER, EnableNextEpisodeAutoPlay INTEGER,
			EnableUserPreferenceAccess INTEGER, HidePlayedInLatest INTEGER, PlayDefaultAudioTrack INTEGER, RememberAudioSelections INTEGER,
			RememberSubtitleSelections INTEGER, InvalidLoginAttemptCount INTEGER, LoginAttemptsBeforeLockout INTEGER, MaxActiveSessions INTEGER,
			MaxParentalRatingScore INTEGER, MaxParentalRatingSubScore INTEGER, RemoteClientBitrateLimit INTEGER, SyncPlayAccess INTEGER,
			LastLoginDate TEXT, LastActivityDate TEXT, InternalId INTEGER)`,
		`CREATE TABLE Permissions (Id INTEGER PRIMARY KEY, Kind INTEGER, UserId TEXT, Value INTEGER)`,
		`CREATE TABLE Preferences (Id INTEGER PRIMARY KEY, Kind INTEGER, UserId TEXT, Value TEXT)`,
		`CREATE TABLE Devices (Id INTEGER PRIMARY KEY, UserId TEXT, AccessToken TEXT, AppName TEXT, AppVersion TEXT, DeviceName TEXT, DeviceId TEXT,
			IsActive INTEGER, DateCreated TEXT, DateModified TEXT, DateLastActivity TEXT)`,
		`CREATE TABLE ApiKeys (Id INTEGER PRIMARY KEY, Name TEXT, AccessToken TEXT, DateCreated TEXT, DateLastActivity TEXT)`,
		`CREATE TABLE BaseItems (Id TEXT PRIMARY KEY, Type TEXT, Name TEXT, Path TEXT, Data TEXT, DateCreated TEXT)`,
		`CREATE TABLE LinkedChildren (ParentId TEXT, ChildId TEXT, ChildType INTEGER, SortOrder INTEGER)`,
		`CREATE TABLE UserData (ItemId TEXT, UserId TEXT, CustomDataKey TEXT, AudioStreamIndex INTEGER, IsFavorite INTEGER, LastPlayedDate TEXT,
			Likes INTEGER, PlayCount INTEGER, PlaybackPositionTicks INTEGER, Played INTEGER, Rating REAL, SubtitleStreamIndex INTEGER)`,
		`INSERT INTO Users VALUES ('4E703B0E-6E78-4EA6-9C48-43A2654C5CB1','whiskey','$PBKDF2-SHA512$iterations=210000$AA$BB','Default','Reset',
			'jpn','eng',4,'',1,0,0,1,1,1,1,1,1,0,NULL,0,NULL,NULL,NULL,2,'2026-10-09 06:37:31.7425077',NULL,1)`,
		`INSERT INTO Permissions (Kind, UserId, Value) VALUES (0,'4E703B0E-6E78-4EA6-9C48-43A2654C5CB1',1)`,
		`INSERT INTO Devices (UserId, AccessToken, AppName, AppVersion, DeviceName, DeviceId, IsActive, DateCreated, DateModified, DateLastActivity)
			VALUES ('4E703B0E-6E78-4EA6-9C48-43A2654C5CB1','token-1','Jellyfin tvOS','1.0','AppleTV','tv-1',0,'2025-01-01 00:00:00','2025-01-01 00:00:00','2026-10-09 06:37:31.7425077')`,
		`INSERT INTO BaseItems VALUES ('CCCCCCCC-0000-0000-0000-000000000001','MediaBrowser.Controller.Playlists.Playlist','Road trip',
			'/config/data/playlists/Road trip','{"OwnerUserId":"4e703b0e6e784ea69c4843a2654c5cb1","PlaylistMediaType":"Video"}','2025-03-04 05:06:07')`,
		`INSERT INTO ApiKeys (Name, AccessToken, DateCreated, DateLastActivity) VALUES ('Jellyseerr','key-1','2024-01-01 00:00:00','2026-10-09 18:25:06.2571647')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture: %v\n%s", err, statement)
		}
	}
	return path
}
