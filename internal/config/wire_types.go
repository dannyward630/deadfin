// SPDX-License-Identifier: GPL-2.0-only

package config

import "encoding/json"

// NameValuePair is a named string pair in ServerConfiguration.ContentTypes.
// It matches upstream/jellyfin/MediaBrowser.Model/Dto/NameValuePair.cs:6-29.
type NameValuePair struct {
	Name  string `xml:"Name" json:"Name"`
	Value string `xml:"Value" json:"Value"`
}

// RepositoryInfo describes a plugin repository in ServerConfiguration.
// It matches upstream/jellyfin/MediaBrowser.Model/Updates/RepositoryInfo.cs:6-25.
type RepositoryInfo struct {
	Name    *string `xml:"Name" json:"Name,omitzero"`
	URL     *string `xml:"Url" json:"Url,omitzero"`
	Enabled bool    `xml:"Enabled" json:"Enabled"`
}

// PathSubstitution is a single path substitution entry.
// It matches upstream/jellyfin/MediaBrowser.Model/Configuration/
// PathSubstitution.cs:6-17.
type PathSubstitution struct {
	From string `xml:"From" json:"From"`
	To   string `xml:"To" json:"To"`
}

// ImageSavingConvention names the convention for saved image files.
// It matches upstream/jellyfin/MediaBrowser.Model/Configuration/
// ImageSavingConvention.cs:6-17.
type ImageSavingConvention string

const (
	ImageSavingConventionLegacy     ImageSavingConvention = "Legacy"
	ImageSavingConventionCompatible ImageSavingConvention = "Compatible"
)

// ImageResolution names a target resolution for extracted images.
// It matches upstream/jellyfin/MediaBrowser.Model/Drawing/
// ImageResolution.cs:6-52.
type ImageResolution string

const (
	ImageResolutionMatchSource ImageResolution = "MatchSource"
	ImageResolutionP144        ImageResolution = "P144"
	ImageResolutionP240        ImageResolution = "P240"
	ImageResolutionP360        ImageResolution = "P360"
	ImageResolutionP480        ImageResolution = "P480"
	ImageResolutionP720        ImageResolution = "P720"
	ImageResolutionP1080       ImageResolution = "P1080"
	ImageResolutionP1440       ImageResolution = "P1440"
	ImageResolutionP2160       ImageResolution = "P2160"
)

// TrickplayScanBehavior controls when trickplay generation runs during a scan.
// It matches upstream/jellyfin/MediaBrowser.Model/Configuration/
// TrickplayScanBehavior.cs:6-17.
type TrickplayScanBehavior string

const (
	TrickplayScanBehaviorBlocking    TrickplayScanBehavior = "Blocking"
	TrickplayScanBehaviorNonBlocking TrickplayScanBehavior = "NonBlocking"
)

// ProcessPriorityClass names a process priority for ffmpeg.
// It matches the System.Diagnostics.ProcessPriorityClass enum serialized in
// upstream/jellyfin-openapi-stable.json:56944-56954.
type ProcessPriorityClass string

const (
	ProcessPriorityNormal      ProcessPriorityClass = "Normal"
	ProcessPriorityIdle        ProcessPriorityClass = "Idle"
	ProcessPriorityHigh        ProcessPriorityClass = "High"
	ProcessPriorityRealTime    ProcessPriorityClass = "RealTime"
	ProcessPriorityBelowNormal ProcessPriorityClass = "BelowNormal"
	ProcessPriorityAboveNormal ProcessPriorityClass = "AboveNormal"
)

// TrickplayOptions holds trickplay image generation settings.
// It matches upstream/jellyfin/MediaBrowser.Model/Configuration/
// TrickplayOptions.cs:9-71.
type TrickplayOptions struct {
	EnableHwAcceleration         bool                  `xml:"EnableHwAcceleration" json:"EnableHwAcceleration"`
	EnableHwEncoding             bool                  `xml:"EnableHwEncoding" json:"EnableHwEncoding"`
	EnableKeyFrameOnlyExtraction bool                  `xml:"EnableKeyFrameOnlyExtraction" json:"EnableKeyFrameOnlyExtraction"`
	ScanBehavior                 TrickplayScanBehavior `xml:"ScanBehavior" json:"ScanBehavior"`
	ProcessPriority              ProcessPriorityClass  `xml:"ProcessPriority" json:"ProcessPriority"`
	Interval                     int                   `xml:"Interval" json:"Interval"`
	WidthResolutions             []int                 `xml:"WidthResolutions>int" json:"WidthResolutions"`
	TileWidth                    int                   `xml:"TileWidth" json:"TileWidth"`
	TileHeight                   int                   `xml:"TileHeight" json:"TileHeight"`
	Qscale                       int                   `xml:"Qscale" json:"Qscale"`
	JpegQuality                  int                   `xml:"JpegQuality" json:"JpegQuality"`
	ProcessThreads               int                   `xml:"ProcessThreads" json:"ProcessThreads"`
}

// DefaultTrickplayOptions returns the upstream-compatible defaults
// (TrickplayOptions.cs:14-70): ScanBehavior NonBlocking, ProcessPriority
// BelowNormal, Interval 10000, WidthResolutions [320], TileWidth 10,
// TileHeight 10, Qscale 4, JpegQuality 90, ProcessThreads 1.
func DefaultTrickplayOptions() TrickplayOptions {
	return TrickplayOptions{
		ScanBehavior:     TrickplayScanBehaviorNonBlocking,
		ProcessPriority:  ProcessPriorityBelowNormal,
		Interval:         10000,
		WidthResolutions: []int{320},
		TileWidth:        10,
		TileHeight:       10,
		Qscale:           4,
		JpegQuality:      90,
		ProcessThreads:   1,
	}
}

// MetadataOptions holds per-type metadata fetcher and image settings.
// It matches upstream/jellyfin/MediaBrowser.Model/Configuration/
// MetadataOptions.cs:11-36.
type MetadataOptions struct {
	ItemType                 string   `xml:"ItemType" json:"ItemType"`
	DisabledMetadataSavers   []string `xml:"DisabledMetadataSavers>string" json:"DisabledMetadataSavers"`
	LocalMetadataReaderOrder []string `xml:"LocalMetadataReaderOrder>string" json:"LocalMetadataReaderOrder"`
	DisabledMetadataFetchers []string `xml:"DisabledMetadataFetchers>string" json:"DisabledMetadataFetchers"`
	MetadataFetcherOrder     []string `xml:"MetadataFetcherOrder>string" json:"MetadataFetcherOrder"`
	DisabledImageFetchers    []string `xml:"DisabledImageFetchers>string" json:"DisabledImageFetchers"`
	ImageFetcherOrder        []string `xml:"ImageFetcherOrder>string" json:"ImageFetcherOrder"`
}

// MarshalJSON emits nil slice fields as empty arrays so the wire shape matches
// upstream's Array.Empty<T>() serialization. The XML model keeps nil slices
// because encoding/xml cannot round-trip empty slices; the JSON wire model
// must emit [] to match stock Jellyfin.
func (o MetadataOptions) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ItemType                 string   `json:"ItemType"`
		DisabledMetadataSavers   []string `json:"DisabledMetadataSavers"`
		LocalMetadataReaderOrder []string `json:"LocalMetadataReaderOrder"`
		DisabledMetadataFetchers []string `json:"DisabledMetadataFetchers"`
		MetadataFetcherOrder     []string `json:"MetadataFetcherOrder"`
		DisabledImageFetchers    []string `json:"DisabledImageFetchers"`
		ImageFetcherOrder        []string `json:"ImageFetcherOrder"`
	}{
		ItemType:                 o.ItemType,
		DisabledMetadataSavers:   nonNilStrings(o.DisabledMetadataSavers),
		LocalMetadataReaderOrder: nonNilStrings(o.LocalMetadataReaderOrder),
		DisabledMetadataFetchers: nonNilStrings(o.DisabledMetadataFetchers),
		MetadataFetcherOrder:     nonNilStrings(o.MetadataFetcherOrder),
		DisabledImageFetchers:    nonNilStrings(o.DisabledImageFetchers),
		ImageFetcherOrder:        nonNilStrings(o.ImageFetcherOrder),
	})
}

// nonNilStrings returns the slice unchanged when non-nil, or an empty non-nil
// slice when nil so the JSON encoder emits [] instead of null.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// nonNilNameValuePairs returns the slice unchanged when non-nil, or an empty
// non-nil slice when nil so the JSON encoder emits [] instead of null. This
// preserves the upstream Array.Empty<NameValuePair>() contract through XML
// round-trip, because encoding/xml turns an empty element into a nil slice.
func nonNilNameValuePairs(s []NameValuePair) []NameValuePair {
	if s == nil {
		return []NameValuePair{}
	}
	return s
}

// nonNilRepositories returns the slice unchanged when non-nil, or an empty
// non-nil slice when nil so the JSON encoder emits [] instead of null.
func nonNilRepositories(s []RepositoryInfo) []RepositoryInfo {
	if s == nil {
		return []RepositoryInfo{}
	}
	return s
}

// nonNilPathSubstitutions returns the slice unchanged when non-nil, or an
// empty non-nil slice when nil so the JSON encoder emits [] instead of null.
func nonNilPathSubstitutions(s []PathSubstitution) []PathSubstitution {
	if s == nil {
		return []PathSubstitution{}
	}
	return s
}

// nonNilMetadataOptions returns an empty non-nil slice for nil.
func nonNilMetadataOptions(s []MetadataOptions) []MetadataOptions {
	if s == nil {
		return []MetadataOptions{}
	}
	return s
}

// nonNilCastReceivers returns an empty non-nil slice for nil.
func nonNilCastReceivers(s []CastReceiverApplication) []CastReceiverApplication {
	if s == nil {
		return []CastReceiverApplication{}
	}
	return s
}
