// SPDX-License-Identifier: GPL-2.0-only

package dto

// ExternalIdInfo is the wire shape of GET /Items/{itemId}/ExternalIdInfos,
// mirroring the pinned ExternalIdInfo schema. Type is a nullable
// ExternalIdMediaType name string; stock's serializer uses
// DefaultIgnoreCondition = WhenWritingNull, so a nil Type is omitted from
// the wire (not rendered as null), matching the pinned OpenAPI nullable
// annotation and upstream's JsonDefaults.
type ExternalIdInfo struct {
	Name string  `json:"Name"`
	Key  string  `json:"Key"`
	Type *string `json:"Type,omitempty"`
}
