// SPDX-License-Identifier: GPL-2.0-only

package item

import "testing"

// upstream/jellyfin/MediaBrowser.Controller/LiveTv/LiveTvChannel.cs:108-116
// uses double.TryParse with CultureInfo.InvariantCulture, then preserves the
// original channel number verbatim when parsing fails. Go-only float literals
// must take that raw fallback through the public ComputeSortName API.
func TestComputeSortNameLiveTVRejectsGoOnlyFloatLiterals(t *testing.T) {
	tests := []struct {
		number string
		want   string
	}{
		{number: "0x1p2", want: "0x1p2-Channel"},
		{number: "1_000", want: "1_000-Channel"},
		{number: "Inf", want: "Inf-Channel"},
		{number: "+Inf", want: "+Inf-Channel"},
		{number: "-Inf", want: "-Inf-Channel"},
	}

	for _, tt := range tests {
		t.Run(tt.number, func(t *testing.T) {
			got, present := ComputeSortName(Item{
				Kind:  KindLiveTvChannel,
				Extra: ExtraFields{LiveTV: &LiveTVExtra{Number: livetvNumberString(tt.number)}},
				Name:  livetvNumberString("Channel"),
			}, SortOptions{})
			if !present || got != tt.want {
				t.Fatalf("ComputeSortName(%q) = (%q, %t), want (%q, true)", tt.number, got, present, tt.want)
			}
		})
	}
}

// Source: upstream/jellyfin/MediaBrowser.Controller/LiveTv/LiveTvChannel.cs:108-116.
// double.TryParse uses invariant NumberStyles.Float | AllowThousands: it does
// not trim NBSP, but it permits adjacent thousands separators.
func TestComputeSortNameLiveTVInvariantDecimalBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		number string
		want   string
	}{
		{
			name:   "NBSP wrapped numeric text remains raw",
			number: "\u00a012\u00a0",
			want:   "\u00a012\u00a0-Channel",
		},
		{
			name:   "AllowThousands accepts adjacent separators",
			number: "1,,2",
			want:   "0000000012.00000-Channel",
		},
		{
			name:   "invariant NaN formats through successful parse",
			number: "NaN",
			want:   "NaN-Channel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, present := ComputeSortName(Item{
				Kind:  KindLiveTvChannel,
				Extra: ExtraFields{LiveTV: &LiveTVExtra{Number: livetvNumberString(tt.number)}},
				Name:  livetvNumberString("Channel"),
			}, SortOptions{})
			if !present || got != tt.want {
				t.Fatalf("ComputeSortName(%q) = (%q, %t), want (%q, true)", tt.number, got, present, tt.want)
			}
		})
	}
}

func livetvNumberString(value string) *string { return &value }
