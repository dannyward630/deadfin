// SPDX-License-Identifier: GPL-2.0-only

package path

import (
	"path/filepath"
	"testing"
)

func TestJoinLocal(t *testing.T) {
	root := filepath.FromSlash("/srv/root")
	for _, tc := range []struct {
		rel  string
		want string
		ok   bool
	}{
		{"a", "/srv/root/a", true},
		{"a/b/c.js", "/srv/root/a/b/c.js", true},
		{"a/../b", "/srv/root/b", true},
		{"a/", "/srv/root/a", true},
		{"..1", "/srv/root/..1", true},
		{"", "", false},
		{".", "/srv/root", true},
		{"..", "", false},
		{"../x", "", false},
		{"a/../../x", "", false},
		{"/etc/passwd", "", false},
	} {
		got, ok := JoinLocal(root, filepath.FromSlash(tc.rel))
		want := tc.want
		if want != "" {
			want = filepath.FromSlash(want)
		}
		if got != want || ok != tc.ok {
			t.Errorf("JoinLocal(%q) = %q, %v; want %q, %v", tc.rel, got, ok, want, tc.ok)
		}
	}
}
