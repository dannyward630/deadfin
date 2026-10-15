// SPDX-License-Identifier: GPL-2.0-only

package config

import (
	"encoding/xml"
	"fmt"
)

// MetadataConfiguration is the persisted Jellyfin metadata configuration.
// It matches upstream/jellyfin/MediaBrowser.Model/Configuration/
// MetadataConfiguration.cs:5-13, whose constructor sets
// UseFileCreationTimeForDateAdded to true.
type MetadataConfiguration struct {
	UseFileCreationTimeForDateAdded bool
}

// DefaultMetadataConfiguration returns the upstream-compatible default, with
// UseFileCreationTimeForDateAdded true (MetadataConfiguration.cs:9).
func DefaultMetadataConfiguration() MetadataConfiguration {
	return MetadataConfiguration{UseFileCreationTimeForDateAdded: true}
}

// MarshalXML writes the document root used by Jellyfin's metadata.xml.
func (configuration MetadataConfiguration) MarshalXML(encoder *xml.Encoder, start xml.StartElement) error {
	start.Name.Local = "MetadataConfiguration"
	type fields MetadataConfiguration
	return encoder.EncodeElement(fields(configuration), start)
}

// UnmarshalXML accepts only the metadata configuration document root.
// Fields omitted by an older source retain the defaults supplied by the caller.
func (configuration *MetadataConfiguration) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "MetadataConfiguration" {
		return fmt.Errorf("config: unexpected metadata configuration root %q", start.Name.Local)
	}
	type fields MetadataConfiguration
	return decoder.DecodeElement((*fields)(configuration), &start)
}

// UnmarshalJSON decodes a posted metadata configuration over the upstream
// default.
func (configuration *MetadataConfiguration) UnmarshalJSON(data []byte) error {
	type fields MetadataConfiguration
	decoded := fields(DefaultMetadataConfiguration())
	if err := decodeJSONOverDefaults(data, &decoded); err != nil {
		return err
	}
	*configuration = MetadataConfiguration(decoded)
	return nil
}
