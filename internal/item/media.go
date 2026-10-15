// SPDX-License-Identifier: GPL-2.0-only

package item

// MediaType is the canonical media classification for an item.
type MediaType uint8

const (
	MediaTypeUnknown MediaType = iota
	MediaTypeVideo
	MediaTypeAudio
	MediaTypePhoto
	MediaTypeBook
)

// String returns the canonical media type name, or empty for an unknown value.
func (m MediaType) String() string {
	switch m {
	case MediaTypeUnknown:
		return "Unknown"
	case MediaTypeAudio:
		return "Audio"
	case MediaTypeVideo:
		return "Video"
	case MediaTypePhoto:
		return "Photo"
	case MediaTypeBook:
		return "Book"
	default:
		return ""
	}
}

// SourceType identifies where an item originated.
type SourceType uint8

const (
	SourceTypeLibrary SourceType = iota
	SourceTypeChannel
	SourceTypeLiveTV
)

// String returns the canonical source type name, or empty for an unknown value.
func (s SourceType) String() string {
	switch s {
	case SourceTypeLibrary:
		return "Library"
	case SourceTypeChannel:
		return "Channel"
	case SourceTypeLiveTV:
		return "LiveTV"
	default:
		return ""
	}
}

// ChannelType identifies the type of a live TV channel.
type ChannelType uint8

const (
	ChannelTypeTV ChannelType = iota
	ChannelTypeRadio
)

// String returns the canonical channel type name, or empty for an unknown value.
func (c ChannelType) String() string {
	switch c {
	case ChannelTypeTV:
		return "TV"
	case ChannelTypeRadio:
		return "Radio"
	default:
		return ""
	}
}
