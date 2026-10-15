// SPDX-License-Identifier: GPL-2.0-only

package handlers

import "testing"

func TestDownloadContentDispositionSerialization(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{
			name: "stock ASCII filename",
			path: "1 - Synthetic Track 1.mp3",
			want: `attachment; filename="1 - Synthetic Track 1.mp3"; filename*=UTF-8''1%20-%20Synthetic%20Track%201.mp3`,
		},
		{
			name: "strips controls and escapes backslash",
			path: "quoted\"\r\n\x01\\track.mp3",
			want: `attachment; filename="quoted\\track.mp3"; filename*=UTF-8''quoted%5Ctrack.mp3`,
		},
		{
			name: "preserves Unicode through RFC 5987 encoding",
			path: "Beyoncé 世界.mp3",
			want: `attachment; filename="Beyonc? ??.mp3"; filename*=UTF-8''Beyonc%C3%A9%20%E4%B8%96%E7%95%8C.mp3`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := downloadContentDisposition(tc.path); got != tc.want {
				t.Errorf("downloadContentDisposition(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
