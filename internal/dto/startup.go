// SPDX-License-Identifier: GPL-2.0-only

package dto

// StartupConfiguration is the first-run server configuration exposed by the
// startup wizard. Nil string fields represent omitted or null JSON values.
type StartupConfiguration struct {
	ServerName                *string `json:"ServerName"`
	UICulture                 *string `json:"UICulture"`
	MetadataCountryCode       *string `json:"MetadataCountryCode"`
	PreferredMetadataLanguage *string `json:"PreferredMetadataLanguage"`
}

// StartupRemoteAccess is the startup wizard's network-access preference.
type StartupRemoteAccess struct {
	EnableRemoteAccess bool `json:"EnableRemoteAccess"`
}

// StartupUser carries the startup wizard's optional account rename and required
// password. Password is intentionally omitted from GET responses.
type StartupUser struct {
	Name     *string `json:"Name,omitempty"`
	Password *string `json:"Password,omitempty"`
}
