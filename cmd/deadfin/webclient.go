// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// defaultBundledWebDir is the web client directory that the container image
// installs (the Dockerfile copies the pinned deadfin-web release there).
// Deadfin serves it at /web/ when <data root>/web does not exist.
const defaultBundledWebDir = "/usr/share/deadfin/web"

// maxWebBuildInfoBytes limits the read of a web client's build.json.
const maxWebBuildInfoBytes = 64 << 10

// Web client sources, as the startup log reports them.
const (
	webSourceDataRoot = "data root"
	webSourceBundled  = "bundled"
	webSourceNone     = "none"
)

// webClient is the web client directory that /web/ serves, with the build
// identity that the directory's build.json reports. jellyfin-web has no
// build.json, so Component, Version and Revision can be empty.
type webClient struct {
	Path      string
	Source    string
	Component string
	Version   string
	Revision  string
	// IgnoredOverride is <data root>/web when that directory exists without
	// an index.html and the bundled client is served instead.
	IgnoredOverride string
}

// resolveWebClient selects the web client directory. <dataRoot>/web wins when
// it holds an index.html, so an operator can put jellyfin-web (or another
// deadfin-web build) there. Otherwise the bundled directory is used when it
// holds an index.html. An empty <dataRoot>/web (for example a bind mount whose
// host directory Docker created) does not hide the bundled client;
// IgnoredOverride reports it. With neither, Path is <dataRoot>/web and Source is
// webSourceNone, and /web/ serves what that directory holds, as before.
func resolveWebClient(dataRoot, bundled string) webClient {
	override := filepath.Join(dataRoot, "web")
	client := webClient{Path: override, Source: webSourceNone}
	switch {
	case hasWebIndex(override):
		client.Source = webSourceDataRoot
	case bundled != "" && hasWebIndex(bundled):
		if isDirectory(override) {
			client.IgnoredOverride = override
		}
		client.Path = bundled
		client.Source = webSourceBundled
	default:
		return client
	}
	client.Component, client.Version, client.Revision = readWebBuildInfo(client.Path)
	return client
}

func hasWebIndex(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "index.html"))
	return err == nil && info.Mode().IsRegular()
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// readWebBuildInfo reads Component, Version and Revision from
// <dir>/build.json. A missing or malformed file gives empty values.
func readWebBuildInfo(dir string) (component, version, revision string) {
	file, err := os.Open(filepath.Join(dir, "build.json"))
	if err != nil {
		return "", "", ""
	}
	defer func() { _ = file.Close() }()
	var info struct {
		Component string
		Version   string
		Revision  string
	}
	if err := json.NewDecoder(io.LimitReader(file, maxWebBuildInfoBytes)).Decode(&info); err != nil {
		return "", "", ""
	}
	return info.Component, info.Version, info.Revision
}

// logWebClient logs the web client that /web/ serves, or that no web client
// is present.
func logWebClient(ctx context.Context, logger *slog.Logger, client webClient) {
	if client.Source == webSourceNone {
		logger.LogAttrs(ctx, slog.LevelInfo, "no web client; /web/ is not served",
			slog.String("path", client.Path),
		)
		return
	}
	if client.IgnoredOverride != "" {
		logger.LogAttrs(ctx, slog.LevelWarn, "data-root web directory has no index.html; serving the bundled web client",
			slog.String("path", client.IgnoredOverride),
		)
	}
	attrs := []slog.Attr{
		slog.String("source", client.Source),
		slog.String("path", client.Path),
	}
	if client.Component != "" {
		attrs = append(attrs, slog.String("component", client.Component))
	}
	if client.Version != "" {
		attrs = append(attrs, slog.String("version", client.Version))
	}
	if client.Revision != "" {
		attrs = append(attrs, slog.String("revision", client.Revision))
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "web client", attrs...)
}
