// SPDX-License-Identifier: GPL-2.0-only

package store

import "testing"

func TestScanIntegerRange(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want uint8
	}{
		{int64(3), 3},
		{"255", 255},
		{[]byte("256"), 0},
		{"-1", 0},
		{int64(1 << 40), 0},
	} {
		if got := asUint8(tc.in); got != tc.want {
			t.Errorf("asUint8(%#v) = %d, want %d", tc.in, got, tc.want)
		}
	}
	if got := asInt("12"); got != 12 {
		t.Errorf("asInt(%q) = %d, want 12", "12", got)
	}
}
