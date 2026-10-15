// SPDX-License-Identifier: GPL-2.0-only

package tmdb

import (
	"encoding/xml"
	"os"
	"strings"
)

// apiKeyEnv is the environment variable that supplies the TMDb API key when no
// plugin configuration file sets one. Deadfin never embeds a key (D-11).
const apiKeyEnv = "TMDB_API_KEY"

// Config parameters the TMDb provider. It mirrors the subset of upstream's
// PluginConfiguration that affects the ported movie/series/episode/person paths;
// the remaining upstream fields (similar-item caching, image-size preferences
// beyond the download default) are out of scope this sprint.
type Config struct {
	// APIKey authenticates every request; empty makes the provider inert (D-11).
	APIKey string `xml:"TmdbApiKey"`
	// IncludeAdult forwards TMDb's include_adult search flag.
	IncludeAdult bool `xml:"IncludeAdult"`
	// ExcludeTagsMovies/Series suppress keyword import as tags.
	ExcludeTagsMovies bool `xml:"ExcludeTagsMovies"`
	ExcludeTagsSeries bool `xml:"ExcludeTagsSeries"`
	// MaxCastMembers/MaxCrewMembers bound imported people (upstream default 15).
	MaxCastMembers int `xml:"MaxCastMembers"`
	MaxCrewMembers int `xml:"MaxCrewMembers"`
	// HideMissing* drop people without a profile image.
	HideMissingCastMembers bool `xml:"HideMissingCastMembers"`
	HideMissingCrewMembers bool `xml:"HideMissingCrewMembers"`
	// ImportSeasonName imports the TMDb season name (upstream default false).
	ImportSeasonName bool `xml:"ImportSeasonName"`
	// ImageSize is the TMDb size segment used when downloading artwork; empty
	// selects "original" (GetUrl's default when no size preference is set).
	ImageSize string `xml:"-"`
}

// DefaultConfig returns upstream's PluginConfiguration defaults.
func DefaultConfig() Config {
	return Config{
		MaxCastMembers: 15,
		MaxCrewMembers: 15,
	}
}

// LoadConfig returns the provider configuration, layering (lowest to highest
// precedence) the defaults, an optional plugin XML file, and the TMDB_API_KEY
// environment variable. A missing file is not an error. The provider
// stays inert until a key is supplied.
func LoadConfig(pluginConfigPath string) Config {
	config := DefaultConfig()
	if pluginConfigPath != "" {
		if contents, err := os.ReadFile(pluginConfigPath); err == nil {
			_ = xml.Unmarshal(contents, &config)
			if config.MaxCastMembers <= 0 {
				config.MaxCastMembers = 15
			}
			if config.MaxCrewMembers <= 0 {
				config.MaxCrewMembers = 15
			}
		}
	}
	if key := strings.TrimSpace(os.Getenv(apiKeyEnv)); key != "" {
		config.APIKey = key
	}
	config.APIKey = strings.TrimSpace(config.APIKey)
	return config
}

// imageSizeOrOriginal returns the configured image size or TMDb's "original".
func (config Config) imageSizeOrOriginal() string {
	if size := strings.TrimSpace(config.ImageSize); size != "" {
		return size
	}
	return "original"
}
