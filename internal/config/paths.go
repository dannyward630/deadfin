// SPDX-License-Identifier: GPL-2.0-only

package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Paths identifies Deadfin's independently managed application stores beneath
// a program-data root.
type Paths struct {
	Root       string
	Data       string
	Config     string
	Cache      string
	Log        string
	Metadata   string
	Transcodes string
	Trickplay  string
}

// NewPaths derives every application store path from root.
func NewPaths(root string) (Paths, error) {
	if strings.TrimSpace(root) == "" {
		return Paths{}, errors.New("config: program-data root is required")
	}

	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return Paths{}, fmt.Errorf("config: resolve program-data root: %w", err)
	}

	return Paths{
		Root:       root,
		Data:       filepath.Join(root, "data"),
		Config:     filepath.Join(root, "config"),
		Cache:      filepath.Join(root, "cache"),
		Log:        filepath.Join(root, "log"),
		Metadata:   filepath.Join(root, "metadata"),
		Transcodes: filepath.Join(root, "cache", "transcodes"),
		Trickplay:  filepath.Join(root, "data", "trickplay"),
	}, nil
}
