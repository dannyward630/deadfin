// SPDX-License-Identifier: GPL-2.0-only

// Package dto holds the hand-written wire types shared by all handlers.
// Scalar types here reproduce upstream JSON behavior exactly
// (src/Jellyfin.Extensions/Json/JsonDefaults.cs and its converters).
package dto

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// UUID is a Jellyfin wire GUID.
//
// Marshals as .NET "N" format: 32 lowercase hex digits, no dashes
// (JsonGuidConverter). Unmarshals any format Guid.Parse accepts: N, D
// (dashed), B ({braced dashed}), P ((parenthesized dashed)) — case-insensitive.
//
// Byte layout: this is a plain big-endian textual GUID (hex digits in string
// order). Upstream SQLite BLOB storage uses .NET's mixed-endian Guid byte
// order — that concern belongs to the store/importer, never to the wire type.
type UUID [16]byte

// NilUUID is the zero GUID.
var NilUUID UUID

// IsNil reports whether the UUID is all zeros (Guid.Empty).
func (u UUID) IsNil() bool { return u == NilUUID }

// String renders N format (32 lowercase hex), matching upstream wire output.
func (u UUID) String() string {
	var dst [32]byte
	hex.Encode(dst[:], u[:])
	return string(dst[:])
}

// Dashed renders D format (8-4-4-4-12 lowercase).
func (u UUID) Dashed() string {
	s := u.String()
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// ParseUUID accepts N, D, B and P formats, case-insensitively, like Guid.Parse.
func ParseUUID(s string) (UUID, error) {
	orig := s
	s = strings.TrimSpace(s)
	// B and P wrappers.
	if len(s) >= 2 && ((s[0] == '{' && s[len(s)-1] == '}') || (s[0] == '(' && s[len(s)-1] == ')')) {
		s = s[1 : len(s)-1]
	}
	// D format: dashes at fixed positions.
	if len(s) == 36 {
		if s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
			return NilUUID, fmt.Errorf("invalid GUID %q", orig)
		}
		s = s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	}
	if len(s) != 32 {
		return NilUUID, fmt.Errorf("invalid GUID %q", orig)
	}
	var u UUID
	if _, err := hex.Decode(u[:], []byte(s)); err != nil {
		return NilUUID, fmt.Errorf("invalid GUID %q", orig)
	}
	return u, nil
}

// MarshalJSON emits the N-format string (JsonGuidConverter.Write).
func (u UUID) MarshalJSON() ([]byte, error) {
	return []byte(`"` + u.String() + `"`), nil
}

// UnmarshalJSON accepts any Guid.Parse format; JSON null leaves the value
// untouched (matching STJ nullable handling at the field level).
func (u *UUID) UnmarshalJSON(data []byte) error {
	s := string(data)
	if s == "null" {
		return nil
	}
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return fmt.Errorf("invalid GUID JSON %s", s)
	}
	parsed, err := ParseUUID(s[1 : len(s)-1])
	if err != nil {
		return err
	}
	*u = parsed
	return nil
}

// MarshalText/UnmarshalText let UUID work as a map key and with the query
// binder (encoding.TextMarshaler/TextUnmarshaler).
func (u UUID) MarshalText() ([]byte, error) { return []byte(u.String()), nil }

func (u *UUID) UnmarshalText(text []byte) error {
	parsed, err := ParseUUID(string(text))
	if err != nil {
		return err
	}
	*u = parsed
	return nil
}
