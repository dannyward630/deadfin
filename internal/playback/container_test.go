// SPDX-License-Identifier: GPL-2.0-only

package playback

// Ported from tests/Jellyfin.Model.Tests/Dlna/ContainerHelperTests.cs @ b04614e.

import "testing"

func TestContainsContainer_EmptyProfileAcceptsAll(t *testing.T) {
	for _, input := range []string{"", "mp4"} {
		if !containsContainerWithNegation("", false, "mp4") {
			t.Fatalf("empty profile must accept %q", input)
		}
	}
	// Via the public entry: profile "" + non-empty input => true.
	if !ContainsContainer("", "mp4") {
		t.Error(`ContainsContainer("", "mp4") must be true`)
	}
	// Quirk preserved: bare "-" strips to empty profile => accepts everything.
	if !ContainsContainer("-", "mp4") {
		t.Error(`ContainsContainer("-", "mp4") must be true (strip happens before empty check)`)
	}
}

func TestContainsContainer_InList_ReturnsTrue(t *testing.T) {
	cases := [][2]string{
		{"mp3,mpeg", "mp3"},
		{"mp3,mpeg,avi", "mp3,avi"},
		{"-mp3,mpeg", "avi"},
		{"-mp3,mpeg,avi", "mp4,jpg"},
	}
	for _, c := range cases {
		if !ContainsContainer(c[0], c[1]) {
			t.Errorf("ContainsContainer(%q, %q) = false, want true", c[0], c[1])
		}
	}
}

func TestContainsContainer_NotInList_ReturnsFalse(t *testing.T) {
	cases := [][2]string{
		{"mp3,mpeg", "avi"},
		{"mp3,mpeg,avi", "mp4,jpg"},
		{"mp3,mpeg", ""},
		{"-mp3,mpeg", "mp3"},
		{"-mp3,mpeg,avi", "mpeg,avi"},
		{",mp3,", ",avi,"}, // empty values discarded
		{"-,mp3,", ",mp3,"},
	}
	for _, c := range cases {
		if ContainsContainer(c[0], c[1]) {
			t.Errorf("ContainsContainer(%q, %q) = true, want false", c[0], c[1])
		}
	}
}

func TestContainsContainer_EmptyInput(t *testing.T) {
	// Empty input returns isNegativeList (checked before profile-empty).
	if ContainsContainer("mp3,mpeg", "") {
		t.Error("positive list + empty input => false")
	}
	if !ContainsContainer("-mp3,mpeg", "") {
		t.Error("negative list + empty input => true")
	}
	if ContainsContainer("", "") {
		t.Error("empty profile + empty input => false (input check first)")
	}
}

func TestContainsContainer_CaseInsensitive(t *testing.T) {
	if !ContainsContainer("MP3,MpEg", "mp3") {
		t.Error("matching is OrdinalIgnoreCase")
	}
	if !ContainsContainer("mkv", "MKV") {
		t.Error("matching is OrdinalIgnoreCase (input side)")
	}
}

func TestContainsContainerList(t *testing.T) {
	trueCases := []struct {
		profiles []string
		negative bool
		input    string
	}{
		{[]string{"mp3", "mpeg"}, false, "mpeg"},
		{[]string{"mp3", "mpeg", "avi"}, false, "avi"},
		{[]string{"mp3", "", "avi"}, false, "mp3"},
		{[]string{"mp3", "mpeg"}, true, "avi"},
		{[]string{"mp3", "mpeg", "avi"}, true, "mkv"},
		{[]string{"mp3", "", "avi"}, true, ""},
	}
	for _, c := range trueCases {
		if !ContainsContainerList(c.profiles, c.negative, c.input) {
			t.Errorf("ContainsContainerList(%v, %v, %q) = false, want true", c.profiles, c.negative, c.input)
		}
	}

	falseCases := []struct {
		profiles []string
		negative bool
		input    string
	}{
		{[]string{"mp3", "mpeg"}, false, "avi"},
		{[]string{"mp3", "mpeg", "avi"}, false, "mkv"},
		{[]string{"mp3", "", "avi"}, false, ""},
		{[]string{"mp3", "mpeg"}, true, "mpeg"},
		{[]string{"mp3", "mpeg", "avi"}, true, "mp3"},
		{[]string{"mp3", "", "avi"}, true, "avi"},
	}
	for _, c := range falseCases {
		if ContainsContainerList(c.profiles, c.negative, c.input) {
			t.Errorf("ContainsContainerList(%v, %v, %q) = true, want false", c.profiles, c.negative, c.input)
		}
	}

	// nil list accepts everything.
	if !ContainsContainerList(nil, false, "anything") {
		t.Error("nil profile list must accept all")
	}
}

func TestSplitContainers(t *testing.T) {
	got := SplitContainers(",mp4,,mkv,")
	if len(got) != 2 || got[0] != "mp4" || got[1] != "mkv" {
		t.Errorf("SplitContainers: got %v", got)
	}
	if len(SplitContainers("")) != 0 {
		t.Error("empty input => empty slice")
	}
}
