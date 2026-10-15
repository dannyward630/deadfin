// SPDX-License-Identifier: GPL-2.0-only

package dto

// EndPointInfo is the transport locality result returned by
// GET /System/Endpoint. It matches the upstream
// MediaBrowser.Model/Net/EndPointInfo surface.
type EndPointInfo struct {
	IsLocal     bool `json:"IsLocal"`
	IsInNetwork bool `json:"IsInNetwork"`
}
