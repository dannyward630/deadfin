// SPDX-License-Identifier: GPL-2.0-only

package musicbrainz

import (
	"encoding/xml"
	"net/url"
	"os"
	"strings"
)

const defaultServer = "https://musicbrainz.org"

// Config mirrors Jellyfin.Plugin.MusicBrainz.xml.
type Config struct {
	Server            string  `xml:"Server"`
	RateLimit         float64 `xml:"RateLimit"`
	ReplaceArtistName bool    `xml:"ReplaceArtistName"`
}

// DefaultConfig returns the pinned Jellyfin 12 defaults.
func DefaultConfig() Config {
	return Config{Server: defaultServer, RateLimit: 1}
}

// LoadConfig resolves MusicBrainz's provider configuration from the established
// config/plugins XML location. A missing or malformed file retains defaults,
// matching BasePluginConfiguration startup behavior.
func LoadConfig(pluginConfigPath string) Config {
	config := DefaultConfig()
	if pluginConfigPath != "" {
		if contents, err := os.ReadFile(pluginConfigPath); err == nil {
			_ = xml.Unmarshal(contents, &config)
		}
	}
	return normalizeConfig(config)
}

func normalizeConfig(config Config) Config {
	config.Server = strings.TrimRight(strings.TrimSpace(config.Server), "/")
	parsed, err := url.Parse(config.Server)
	if err != nil || parsed.Host == "" || parsed.Scheme == "" {
		config.Server = defaultServer
	}
	if config.Server == defaultServer && config.RateLimit < 1 {
		config.RateLimit = 1
	}
	return config
}
