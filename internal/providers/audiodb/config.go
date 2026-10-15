// SPDX-License-Identifier: GPL-2.0-only

package audiodb

import (
	"encoding/xml"
	"net/url"
	"os"
	"strings"
)

const (
	defaultServer = "https://www.theaudiodb.com/api/v1/json"
	apiKeyEnv     = "AUDIODB_API_KEY"
)

// Config mirrors Jellyfin.Plugin.AudioDb.xml and adds the issue-required
// external API key. Empty APIKey makes the provider inert.
type Config struct {
	Server           string `xml:"Server"`
	APIKey           string `xml:"ApiKey"`
	ReplaceAlbumName bool   `xml:"ReplaceAlbumName"`
}

func DefaultConfig() Config {
	return Config{Server: defaultServer}
}

// LoadConfig layers defaults, provider XML, and the deployment environment.
func LoadConfig(pluginConfigPath string) Config {
	config := DefaultConfig()
	if pluginConfigPath != "" {
		if contents, err := os.ReadFile(pluginConfigPath); err == nil {
			_ = xml.Unmarshal(contents, &config)
		}
	}
	if key := strings.TrimSpace(os.Getenv(apiKeyEnv)); key != "" {
		config.APIKey = key
	}
	return normalizeConfig(config)
}

func normalizeConfig(config Config) Config {
	config.Server = strings.TrimRight(strings.TrimSpace(config.Server), "/")
	parsed, err := url.Parse(config.Server)
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		config.Server = defaultServer
	}
	config.APIKey = strings.TrimSpace(config.APIKey)
	return config
}
