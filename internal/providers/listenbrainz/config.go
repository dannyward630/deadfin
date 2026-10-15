// SPDX-License-Identifier: GPL-2.0-only

package listenbrainz

import (
	"encoding/xml"
	"net/url"
	"os"
	"strings"
)

const (
	defaultServer = "https://api.listenbrainz.org"
	tokenEnv      = "LISTENBRAINZ_TOKEN"
	serverEnv     = "LISTENBRAINZ_SERVER"
)

const (
	PluginName = "ListenBrainz"
	// PluginID retains the stable identity in the pinned Jellyfin 12 source.
	PluginID = "a5b2e8c1-9d4f-4a3b-8c7e-6f1a2b3c4d5e"
)

// Config is persisted in config/plugins/Jellyfin.Plugin.ListenBrainz.xml.
// Token is intentionally external configuration and never compiled in.
type Config struct {
	Server           string `xml:"Server"`
	Token            string `xml:"Token"`
	SubmitPlayingNow bool   `xml:"SubmitPlayingNow"`
}

func DefaultConfig() Config {
	return Config{Server: defaultServer, SubmitPlayingNow: true}
}

func LoadConfig(pluginConfigPath string) Config {
	config := DefaultConfig()
	if pluginConfigPath != "" {
		if contents, err := os.ReadFile(pluginConfigPath); err == nil {
			_ = xml.Unmarshal(contents, &config)
		}
	}
	if server := strings.TrimSpace(os.Getenv(serverEnv)); server != "" {
		config.Server = server
	}
	if token := strings.TrimSpace(os.Getenv(tokenEnv)); token != "" {
		config.Token = token
	}
	return normalizeConfig(config)
}

func normalizeConfig(config Config) Config {
	config.Server = strings.TrimRight(strings.TrimSpace(config.Server), "/")
	parsed, err := url.Parse(config.Server)
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		config.Server = defaultServer
	}
	config.Token = strings.TrimSpace(config.Token)
	return config
}
