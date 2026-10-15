// SPDX-License-Identifier: GPL-2.0-only

package dto

// AuthenticationInfo is Jellyfin's AuthenticationInfo as ApiKeyController
// projects an API key (AuthenticationManager.GetApiKeys): only AppName,
// AccessToken, and DateCreated carry data; the device fields are empty
// strings and the rest keep their defaults.
type AuthenticationInfo struct {
	ID               int64   `json:"Id"`
	AccessToken      *string `json:"AccessToken,omitempty"`
	DeviceID         *string `json:"DeviceId,omitempty"`
	AppName          *string `json:"AppName,omitempty"`
	AppVersion       *string `json:"AppVersion,omitempty"`
	DeviceName       *string `json:"DeviceName,omitempty"`
	UserID           UUID    `json:"UserId"`
	IsActive         bool    `json:"IsActive"`
	DateCreated      Time    `json:"DateCreated"`
	DateRevoked      *Time   `json:"DateRevoked,omitempty"`
	DateLastActivity Time    `json:"DateLastActivity"`
	UserName         *string `json:"UserName,omitempty"`
}
