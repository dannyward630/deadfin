// SPDX-License-Identifier: GPL-2.0-only

package dto

import (
	"fmt"
	"strings"
	"time"
)

// Time is a Jellyfin wire timestamp.
//
// Upstream's JsonDateTimeConverter writes seven fractional digits when the
// millisecond component is zero; otherwise System.Text.Json's DateTime writer
// emits an ISO 8601 UTC timestamp with trailing fractional zeros trimmed.
// Reading accepts ISO 8601 with or without fraction/offset; a missing offset
// is treated as UTC (upstream re-stamps Unspecified kinds as UTC on read).
type Time struct{ time.Time }

// NewTime wraps a time.Time, normalizing to UTC.
func NewTime(t time.Time) Time { return Time{t.UTC()} }

// StdPtr unwraps to the standard library type the store layer uses, preserving
// nil. Query structs bind dto.Time so a date-only value parses (see
// timeReadLayouts); the store takes time.Time because it must not depend on
// wire types. This is the seam between them.
func (t *Time) StdPtr() *time.Time {
	if t == nil {
		return nil
	}
	inner := t.Time
	return &inner
}

// IsZero lets omitzero inspect a Time value without invoking time.Time's
// promoted method on a nullable *Time receiver.
func (t Time) IsZero() bool { return t.Time.IsZero() }

// MarshalJSON reproduces JsonDateTimeConverter's DateTime emission for UTC
// values. See upstream/jellyfin/src/Jellyfin.Extensions/Json/Converters/
// JsonDateTimeConverter.cs:21-31.
func (t Time) MarshalJSON() ([]byte, error) {
	u := t.Time.UTC()
	base := u.Format("2006-01-02T15:04:05")
	frac := formatDotNetFraction(u.Nanosecond())
	if u.Nanosecond()/int(time.Millisecond) == 0 {
		frac = fmt.Sprintf(".%07d", u.Nanosecond()/100)
	}
	return []byte(`"` + base + frac + `Z"`), nil
}

// formatDotNetFraction renders nanoseconds as .NET 100ns "ticks" fraction
// for the non-exact-millisecond branch. Sub-tick precision (below 100ns) is
// truncated, exactly as .NET cannot represent it.
func formatDotNetFraction(nanos int) string {
	ticks := nanos / 100 // 100ns units, 0..9_999_999
	if ticks == 0 {
		return ""
	}
	s := fmt.Sprintf("%07d", ticks)
	s = strings.TrimRight(s, "0")
	return "." + s
}

// ItemPremiereDate preserves the wire distinction between an absent premiere
// date and Audio's stock-required explicit zero DateTime value.
type ItemPremiereDate struct {
	Time
	emitZero bool
}

// NewItemPremiereDate normalizes an item premiere date for wire projection.
// emitZero is reserved for Audio records whose absent source date serializes as
// DateTime.MinValue in Jellyfin's BaseItemDto.
func NewItemPremiereDate(value time.Time, emitZero bool) *ItemPremiereDate {
	return &ItemPremiereDate{Time: NewTime(value), emitZero: emitZero}
}

// IsZero honors the ordinary sparse contract while retaining Audio's explicit
// DateTime.MinValue response field.
func (t ItemPremiereDate) IsZero() bool {
	return !t.emitZero && t.Time.IsZero()
}

// MarshalJSON delegates exact Jellyfin timestamp formatting to Time.
func (t ItemPremiereDate) MarshalJSON() ([]byte, error) {
	return t.Time.MarshalJSON()
}

var timeReadLayouts = []string{
	"2006-01-02T15:04:05.9999999Z07:00",
	"2006-01-02T15:04:05.9999999", // no offset => UTC
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02", // date-only (some NFO-derived fields)
}

// UnmarshalJSON accepts the ISO 8601 shapes .NET emits or accepts.
func (t *Time) UnmarshalJSON(data []byte) error {
	s := string(data)
	if s == "null" {
		return nil
	}
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return fmt.Errorf("invalid time JSON %s", s)
	}
	return t.UnmarshalText([]byte(s[1 : len(s)-1]))
}

// UnmarshalText implements encoding.TextUnmarshaler (query binder support).
func (t *Time) UnmarshalText(text []byte) error {
	s := string(text)
	for _, layout := range timeReadLayouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			// No offset in the layout => Go assumed UTC already; offsets are
			// normalized to UTC to match upstream's ToUniversalTime on write.
			t.Time = parsed.UTC()
			return nil
		}
	}
	return fmt.Errorf("invalid time %q", s)
}

// MarshalText mirrors MarshalJSON without quotes.
func (t Time) MarshalText() ([]byte, error) {
	b, err := t.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return b[1 : len(b)-1], nil
}
