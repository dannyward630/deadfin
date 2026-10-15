// SPDX-License-Identifier: GPL-2.0-only

package handlers

import "testing"

func TestDirectFileContentTypePinsFLACBeforeHostLookup(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name            string
		outputContainer string
		sourceContainer string
		path            string
	}{
		{
			name:            "output container",
			outputContainer: "flac",
			sourceContainer: "mp3",
			path:            "audio.mp3",
		},
		{
			name:            "normalized output container",
			outputContainer: ".FLAC",
			sourceContainer: "mp3",
			path:            "audio.mp3",
		},
		{
			name:            "source container",
			sourceContainer: " FlAc ",
			path:            "audio.mp3",
		},
		{
			name: "source path",
			path: "audio.FlAc",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lookupCalled := false
			got := directFileContentTypeWithLookup(test.outputContainer, test.sourceContainer, test.path, func(string) string {
				lookupCalled = true
				return "audio/x-flac"
			})
			if got != "audio/flac" {
				t.Errorf("FLAC Content-Type = %q, want audio/flac", got)
			}
			if lookupCalled {
				t.Error("FLAC Content-Type consulted the host MIME database")
			}
		})
	}
}

func TestDirectFileContentTypePreservesHostLookupForOtherContainers(t *testing.T) {
	t.Parallel()

	var gotExtension string
	got := directFileContentTypeWithLookup("mp3", "flac", "audio.flac", func(extension string) string {
		gotExtension = extension
		return "audio/mpeg"
	})
	if got != "audio/mpeg" {
		t.Errorf("MP3 Content-Type = %q, want audio/mpeg", got)
	}
	if gotExtension != ".mp3" {
		t.Errorf("host MIME lookup extension = %q, want .mp3", gotExtension)
	}
}
