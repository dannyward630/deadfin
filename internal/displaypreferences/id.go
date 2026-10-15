// SPDX-License-Identifier: GPL-2.0-only

// Package displaypreferences manages user- and client-scoped Jellyfin display preferences.
package displaypreferences

import (
	"crypto/md5"
	"encoding/binary"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// ResolveID parses a Jellyfin display-preference ID or derives its stable GUID
// from a non-GUID route value with the upstream UTF-16LE MD5 algorithm.
func ResolveID(value string) (dto.UUID, error) {
	if id, err := dto.ParseUUID(value); err == nil {
		return id, nil
	}

	digest := md5.Sum(utf16LE(value)) //nolint:gosec // Jellyfin persistence compatibility requires MD5.
	return dotNetGUID(digest), nil
}

func utf16LE(value string) []byte {
	units := make([]uint16, 0, len(value))
	for _, rune := range value {
		if rune <= 0xffff {
			units = append(units, uint16(rune))
			continue
		}
		rune -= 0x10000
		units = append(units, uint16(0xd800+rune>>10), uint16(0xdc00+rune&0x3ff))
	}

	encoded := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(encoded[index*2:], unit)
	}
	return encoded
}

func dotNetGUID(digest [md5.Size]byte) dto.UUID {
	return dto.UUID{
		digest[3], digest[2], digest[1], digest[0],
		digest[5], digest[4],
		digest[7], digest[6],
		digest[8], digest[9], digest[10], digest[11],
		digest[12], digest[13], digest[14], digest[15],
	}
}
