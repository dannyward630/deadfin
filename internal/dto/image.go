// SPDX-License-Identifier: GPL-2.0-only

package dto

// ImageInfo is one image record in the `GET /Items/{itemId}/Images` listing,
// mirroring MediaBrowser.Model.Dto.ImageInfo. ImageType and Size are C#
// non-nullable value types (always emitted, CONVENTIONS §3.2); every other
// field is a C# nullable/reference type and is omitted when unset.
type ImageInfo struct {
	ImageType  string `json:"ImageType"`
	ImageIndex *int   `json:"ImageIndex,omitempty"`
	ImageTag   string `json:"ImageTag,omitzero"`
	Path       string `json:"Path,omitzero"`
	BlurHash   string `json:"BlurHash,omitzero"`
	Height     *int   `json:"Height,omitempty"`
	Width      *int   `json:"Width,omitempty"`
	Size       int64  `json:"Size"`
}
