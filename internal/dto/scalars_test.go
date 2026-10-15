// SPDX-License-Identifier: GPL-2.0-only

package dto

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUUIDFormats(t *testing.T) {
	want := "8a1b4a2f9dbe4f379f6b2b1c3d4e5f60"
	inputs := []string{
		"8a1b4a2f9dbe4f379f6b2b1c3d4e5f60",         // N
		"8A1B4A2F9DBE4F379F6B2B1C3D4E5F60",         // N upper
		"8a1b4a2f-9dbe-4f37-9f6b-2b1c3d4e5f60",     // D
		"8A1B4A2F-9DBE-4F37-9F6B-2B1C3D4E5F60",     // D upper
		"{8a1b4a2f-9dbe-4f37-9f6b-2b1c3d4e5f60}",   // B
		"(8a1b4a2f-9dbe-4f37-9f6b-2b1c3d4e5f60)",   // P
		"  8a1b4a2f-9dbe-4f37-9f6b-2b1c3d4e5f60  ", // Guid.Parse trims
	}
	for _, in := range inputs {
		u, err := ParseUUID(in)
		if err != nil {
			t.Fatalf("ParseUUID(%q): %v", in, err)
		}
		if u.String() != want {
			t.Errorf("ParseUUID(%q) => %s, want %s", in, u, want)
		}
	}

	bad := []string{"", "xyz", "8a1b4a2f-9dbe-4f37-9f6b", "8a1b4a2f9dbe4f379f6b2b1c3d4e5f6"}
	for _, in := range bad {
		if _, err := ParseUUID(in); err == nil {
			t.Errorf("ParseUUID(%q) must fail", in)
		}
	}
}

func TestUUIDJSON(t *testing.T) {
	u, _ := ParseUUID("8a1b4a2f-9dbe-4f37-9f6b-2b1c3d4e5f60")
	out, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	// N format, lowercase, no dashes — JsonGuidConverter.
	if string(out) != `"8a1b4a2f9dbe4f379f6b2b1c3d4e5f60"` {
		t.Errorf("marshal: %s", out)
	}

	var back UUID
	if err := json.Unmarshal([]byte(`"8A1B4A2F-9DBE-4F37-9F6B-2B1C3D4E5F60"`), &back); err != nil {
		t.Fatal(err)
	}
	if back != u {
		t.Error("dashed-upper unmarshal mismatch")
	}

	// Map key usage (TextMarshaler): ProviderIds-style maps carry UUID values
	// as strings; UUID keys must render N-format.
	m := map[UUID]string{u: "x"}
	out, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"8a1b4a2f9dbe4f379f6b2b1c3d4e5f60":"x"}` {
		t.Errorf("map key marshal: %s", out)
	}
}

func TestTimeMarshalDotNetShape(t *testing.T) {
	cases := []struct {
		name  string
		nanos int
		want  string
	}{
		// JsonDateTimeConverter emits seven ticks whenever the millisecond
		// component is zero, even for DateTime.MinValue and sub-tick input.
		{name: "zero", nanos: 0, want: `"2026-07-25T01:02:03.0000000Z"`},
		{name: "ninety-nine nanoseconds", nanos: 99, want: `"2026-07-25T01:02:03.0000000Z"`},
		{name: "one tick", nanos: 100, want: `"2026-07-25T01:02:03.0000001Z"`},
		{name: "five hundred milliseconds", nanos: 500_000_000, want: `"2026-07-25T01:02:03.5Z"`},
		{name: "seven tick digits", nanos: 123_456_700, want: `"2026-07-25T01:02:03.1234567Z"`},
		{name: "trimmed trailing tick zeros", nanos: 120_000_000, want: `"2026-07-25T01:02:03.12Z"`},
	}
	for _, tc := range cases {
		tm := NewTime(time.Date(2026, 7, 25, 1, 2, 3, tc.nanos, time.UTC))
		out, err := json.Marshal(tm)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != tc.want {
			t.Errorf("%s (nanos=%d): got %s want %s", tc.name, tc.nanos, out, tc.want)
		}
	}

	// Non-UTC input is emitted as UTC.
	loc := time.FixedZone("x", 7*3600)
	tm := NewTime(time.Date(2026, 7, 25, 8, 2, 3, 0, loc))
	out, _ := json.Marshal(tm)
	if string(out) != `"2026-07-25T01:02:03.0000000Z"` {
		t.Errorf("zone conversion: got %s", out)
	}

	minimum, err := json.Marshal(NewTime(time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	if string(minimum) != `"0001-01-01T00:00:00.0000000Z"` {
		t.Errorf("minimum time: got %s", minimum)
	}
}

func TestTimeUnmarshalShapes(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{`"2026-07-25T01:02:03Z"`, time.Date(2026, 7, 25, 1, 2, 3, 0, time.UTC)},
		{`"2026-07-25T01:02:03.1234567Z"`, time.Date(2026, 7, 25, 1, 2, 3, 123_456_700, time.UTC)},
		{`"2026-07-25T01:02:03"`, time.Date(2026, 7, 25, 1, 2, 3, 0, time.UTC)},       // no offset => UTC
		{`"2026-07-25T08:02:03+07:00"`, time.Date(2026, 7, 25, 1, 2, 3, 0, time.UTC)}, // offset normalized
		{`"2026-07-25"`, time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		var tm Time
		if err := json.Unmarshal([]byte(tc.in), &tm); err != nil {
			t.Fatalf("unmarshal %s: %v", tc.in, err)
		}
		if !tm.Time.Equal(tc.want) {
			t.Errorf("%s: got %v want %v", tc.in, tm.Time, tc.want)
		}
	}

	var tm Time
	if err := json.Unmarshal([]byte(`"garbage"`), &tm); err == nil {
		t.Error("garbage time must fail")
	}
}
