// SPDX-License-Identifier: GPL-2.0-only

package dto

import "time"

// QuickConnectResult is the pending QuickConnect request state returned by
// Initiate and Connect. Field names and order match the pinned stock response
// (MediaBrowser.Model/QuickConnect/QuickConnectResult.cs).
//
// The default JSON tags are PascalCase, matching the stock .NET serializer's
// default output. The dual-casing render layer negotiates CamelCase when the
// client requests it.
type QuickConnectResult struct {
	Authenticated bool   `json:"Authenticated"`
	Secret        string `json:"Secret"`
	Code          string `json:"Code"`
	DateAdded     Time   `json:"DateAdded"`
	DeviceId      string `json:"DeviceId"`
	DeviceName    string `json:"DeviceName"`
	AppName       string `json:"AppName"`
	AppVersion    string `json:"AppVersion"`
}

// NewQuickConnectResult constructs a QuickConnectResult from the raw fields.
// DateAdded is converted to dto.Time for upstream-compatible UTC ISO-8601
// serialization.
func NewQuickConnectResult(authenticated bool, secret, code string, dateAdded time.Time, deviceID, deviceName, appName, appVersion string) QuickConnectResult {
	return QuickConnectResult{
		Authenticated: authenticated,
		Secret:        secret,
		Code:          code,
		DateAdded:     NewTime(dateAdded),
		DeviceId:      deviceID,
		DeviceName:    deviceName,
		AppName:       appName,
		AppVersion:    appVersion,
	}
}

// QuickConnectRedemptionRequest is the JSON body for
// POST /Users/AuthenticateWithQuickConnect. The stock field name is PascalCase
// "Secret" (Jellyfin.Api/Models/UserDtos/QuickConnectDto.cs).
type QuickConnectRedemptionRequest struct {
	Secret string `json:"Secret"`
}
