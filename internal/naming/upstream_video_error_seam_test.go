// SPDX-License-Identifier: GPL-2.0-only

package naming_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func mustResolveVideo(t *testing.T, path string, opts naming.NamingOptions) *naming.VideoFileInfo {
	t.Helper()
	got, err := naming.ResolveVideoWithError(path, opts)
	if err != nil {
		t.Fatalf("ResolveVideoWithError(%q) error = %v", path, err)
	}
	return got
}

func mustResolveVideoDirectory(t *testing.T, path string, opts naming.NamingOptions, parseName bool, libraryRoot string) *naming.VideoFileInfo {
	t.Helper()
	got, err := naming.ResolveVideoDirectoryWithError(path, opts, parseName, libraryRoot)
	if err != nil {
		t.Fatalf("ResolveVideoDirectoryWithError(%q) error = %v", path, err)
	}
	return got
}

func mustResolveVideoList(t *testing.T, paths []string, opts naming.NamingOptions) []naming.VideoInfo {
	t.Helper()
	got, err := naming.ResolveVideoList(paths, opts)
	if err != nil {
		t.Fatalf("ResolveVideoList(%q) error = %v", paths, err)
	}
	return got
}

func mustResolveVideoListForCollection(t *testing.T, paths []string, opts naming.NamingOptions, collection string) []naming.VideoInfo {
	t.Helper()
	got, err := naming.ResolveVideoListForCollection(paths, opts, collection)
	if err != nil {
		t.Fatalf("ResolveVideoListForCollection(%q, %q) error = %v", paths, collection, err)
	}
	return got
}

func mustResolveVideoInfoList(t *testing.T, infos []naming.VideoFileInfo, opts naming.NamingOptions, collection string) []naming.VideoInfo {
	t.Helper()
	got, err := naming.ResolveVideoInfoList(infos, opts, collection)
	if err != nil {
		t.Fatalf("ResolveVideoInfoList error = %v", err)
	}
	return got
}

func mustResolveStacks(t *testing.T, paths []string, opts naming.NamingOptions) []naming.FileStack {
	t.Helper()
	got, err := naming.ResolveStacks(paths, opts)
	if err != nil {
		t.Fatalf("ResolveStacks(%q) error = %v", paths, err)
	}
	return got
}

func mustResolveVideoInfoStacks(t *testing.T, infos []naming.VideoFileInfo, opts naming.NamingOptions) []naming.FileStack {
	t.Helper()
	got, err := naming.ResolveVideoInfoStacks(infos, opts)
	if err != nil {
		t.Fatalf("ResolveVideoInfoStacks error = %v", err)
	}
	return got
}
