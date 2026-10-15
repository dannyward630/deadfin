// SPDX-License-Identifier: GPL-2.0-only

package images

import (
	"crypto/md5"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const dotNetUnixEpochTicks int64 = 621355968000000000

// CacheOptions contains every value that participates in Jellyfin's resized
// image cache identity.
type CacheOptions struct {
	SourcePath    string
	Quality       int
	Modified      time.Time
	Format        Format
	Resize        ResizeOptions
	PercentPlayed float64
	UnplayedCount *int
	Blur          *int
	Background    string
	Foreground    string
}

// CacheKey serializes options in Jellyfin's cache-key format. In particular,
// percent played and unplayed count intentionally share the duplicated p key.
func CacheKey(options CacheOptions) string {
	var key strings.Builder
	key.Grow(len(options.SourcePath) + 128)
	key.WriteString(options.SourcePath)
	key.WriteString(",quality=")
	writeInt(&key, options.Quality)
	key.WriteString(",datemodified=")
	writeInt64(&key, dotNetTicks(options.Modified))
	key.WriteString(",f=")
	key.WriteString(string(options.Format))

	writeOptionalInt(&key, ",width=", options.Resize.Width)
	writeOptionalInt(&key, ",height=", options.Resize.Height)
	writeOptionalInt(&key, ",maxwidth=", options.Resize.MaxWidth)
	writeOptionalInt(&key, ",maxheight=", options.Resize.MaxHeight)
	writeOptionalInt(&key, ",fillwidth=", options.Resize.FillWidth)
	writeOptionalInt(&key, ",fillheight=", options.Resize.FillHeight)

	if options.PercentPlayed > 0 {
		key.WriteString(",p=")
		if math.IsInf(options.PercentPlayed, 1) {
			key.WriteString("Infinity")
		} else {
			key.WriteString(strconv.FormatFloat(options.PercentPlayed, 'G', -1, 64))
		}
	}
	writeOptionalInt(&key, ",p=", options.UnplayedCount)
	writeOptionalInt(&key, ",blur=", options.Blur)
	if options.Background != "" {
		key.WriteString(",b=")
		key.WriteString(options.Background)
	}
	if options.Foreground != "" {
		key.WriteString(",fl=")
		key.WriteString(options.Foreground)
	}
	key.WriteString(",v=3")
	return key.String()
}

func writeOptionalInt(builder *strings.Builder, prefix string, value *int) {
	if value == nil {
		return
	}
	builder.WriteString(prefix)
	writeInt(builder, *value)
}

func writeInt(builder *strings.Builder, value int) {
	builder.WriteString(strconv.Itoa(value))
}

func writeInt64(builder *strings.Builder, value int64) {
	builder.WriteString(strconv.FormatInt(value, 10))
}

// MD5GUID returns Jellyfin's MD5 cache digest. It encodes text as UTF-16LE and
// formats those MD5 bytes through .NET Guid's mixed-endian N representation.
func MD5GUID(value string) string {
	hash := md5.New() // #nosec G401 -- this is the upstream non-security cache identifier.
	writeUTF16LE(hash, value)

	var sum [md5.Size]byte
	hash.Sum(sum[:0])
	var output [32]byte
	encodeGUIDHex(&output, sum)
	return string(output[:])
}

func writeUTF16LE(destination interface{ Write([]byte) (int, error) }, value string) {
	var buffer [256]byte
	length := 0
	flush := func() {
		if length == 0 {
			return
		}
		_, _ = destination.Write(buffer[:length])
		length = 0
	}
	writeCodeUnit := func(codeUnit uint16) {
		if length == len(buffer) {
			flush()
		}
		buffer[length] = byte(codeUnit)
		buffer[length+1] = byte(codeUnit >> 8)
		length += 2
	}

	for len(value) > 0 {
		runeValue, width := utf8.DecodeRuneInString(value)
		if runeValue == utf8.RuneError && width == 1 {
			// Encoding.Unicode's replacement fallback writes U+FFFD.
			writeCodeUnit(utf8.RuneError)
			value = value[1:]
			continue
		}
		value = value[width:]
		if runeValue < 0x10000 {
			writeCodeUnit(uint16(runeValue))
			continue
		}
		runeValue -= 0x10000
		writeCodeUnit(0xD800 + uint16(runeValue>>10))
		writeCodeUnit(0xDC00 + uint16(runeValue&0x3FF))
	}
	flush()
}

func encodeGUIDHex(output *[32]byte, sum [16]byte) {
	const hex = "0123456789abcdef"
	order := [16]byte{3, 2, 1, 0, 5, 4, 7, 6, 8, 9, 10, 11, 12, 13, 14, 15}
	for index, source := range order {
		value := sum[source]
		output[index*2] = hex[value>>4]
		output[index*2+1] = hex[value&0x0F]
	}
}

// CachePath returns the Jellyfin cache location for key and format below root.
func CachePath(root, key string, format Format) string {
	digest := MD5GUID(key)
	return filepath.Join(root, digest[:1], digest+format.extension())
}

// CacheTag is Jellyfin's image invalidation tag for sourcePath at modified.
func CacheTag(sourcePath string, modified time.Time) string {
	return MD5GUID(sourcePath + strconv.FormatInt(dotNetTicks(modified), 10))
}

func dotNetTicks(value time.Time) int64 {
	return value.UTC().Unix()*10_000_000 + int64(value.UTC().Nanosecond())/100 + dotNetUnixEpochTicks
}
