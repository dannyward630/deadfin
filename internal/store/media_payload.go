// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/deadfin-org/deadfin/internal/playback"
)

var streamColumnJSONFields = []string{
	"Index", "Type", "Codec", "Language", "Title", "IsDefault", "IsForced", "IsExternal",
	"BitRate", "Width", "Height", "Channels", "SampleRate",
}

type rawMediaStream playback.MediaStream

func streamExtra(stream playback.MediaStream) (string, error) {
	fields, err := objectFields(rawMediaStream(stream))
	if err != nil {
		return "", err
	}
	for _, field := range streamColumnJSONFields {
		delete(fields, field)
	}
	return encodeFields(fields)
}

func attachmentExtra(attachment playback.MediaAttachment) (string, error) {
	fields, err := objectFields(attachment)
	if err != nil {
		return "", err
	}
	delete(fields, "Index")
	delete(fields, "FileName")
	delete(fields, "MimeType")
	return encodeFields(fields)
}

// streamFromColumns rebuilds a stream from its columns and its extra JSON.
// It decodes extra once and assigns the column values, which replace the
// same-named extra fields. The merged decode in mergedStreamFromColumns
// produced the same stream but cost five JSON passes per stream, which
// dominated a 100-item browse page. Two cases keep the merged decode, because
// its result depends on it: an extra key that names a column field in another
// letter case (the merge order then decides), and a column integer outside
// the int32 range of the wire normalization (the merge then fails).
func streamFromColumns(index int, streamType string, codec, language, title, defaultValue, forced, external, bitrate, width, height, channels, sampleRate any, extra string) (playback.MediaStream, error) {
	ints := []*int{optionalInt(bitrate), optionalInt(width), optionalInt(height), optionalInt(channels), optionalInt(sampleRate)}
	if streamExtraNamesColumn(extra) || !int32Wire(&index) || !int32Wire(ints...) {
		return mergedStreamFromColumns(index, streamType, codec, language, title, defaultValue, forced, external, bitrate, width, height, channels, sampleRate, extra)
	}
	stream, err := decodeStreamExtra(extra)
	if err != nil {
		return playback.MediaStream{}, fmt.Errorf("decode media stream: %w", err)
	}
	if err := stream.Type.UnmarshalJSON(mustJSON(streamType)); err != nil {
		return playback.MediaStream{}, fmt.Errorf("decode media stream: %w", err)
	}
	stream.Index = index
	stream.Codec = asString(codec)
	stream.Language = optionalString(language)
	stream.Title = optionalString(title)
	stream.IsDefault = asBool(defaultValue)
	stream.IsForced = asBool(forced)
	stream.IsExternal = asBool(external)
	stream.BitRate, stream.Width, stream.Height, stream.Channels, stream.SampleRate = ints[0], ints[1], ints[2], ints[3], ints[4]
	return stream, nil
}

// decodeStreamExtra decodes the extra fields of a stream. MediaStream's
// decoder first rewrites numeric fields to their canonical wire form, which
// re-encodes the whole object. For extra that streamExtra wrote (one key per
// field), a plain decode that succeeds yields the same stream, with one
// exception: the rewrite rejects an integer outside int32, which needs ten or
// more digits. Extra with such a digit run, and extra that the plain decode
// rejects (a number written as a string, for example), keep the rewrite.
func decodeStreamExtra(extra string) (playback.MediaStream, error) {
	var stream playback.MediaStream
	if extra == "" {
		return stream, nil
	}
	if !hasLongDigitRun(extra, 10) {
		var plain rawMediaStream
		if json.Unmarshal([]byte(extra), &plain) == nil {
			return playback.MediaStream(plain), nil
		}
	}
	err := json.Unmarshal([]byte(extra), &stream)
	return stream, err
}

func hasLongDigitRun(value string, length int) bool {
	run := 0
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			run = 0
			continue
		}
		if run++; run >= length {
			return true
		}
	}
	return false
}

// streamExtraNamesColumn reports whether extra may hold a top-level key that
// matches a column field without regard to letter case. It may report a
// match for a string value; that only selects the slower merged decode.
func streamExtraNamesColumn(extra string) bool {
	if extra == "" || extra == "{}" {
		return false
	}
	lower := strings.ToLower(extra)
	for _, field := range streamColumnJSONFields {
		if strings.Contains(lower, `"`+strings.ToLower(field)+`"`) {
			return true
		}
	}
	return false
}

func int32Wire(values ...*int) bool {
	for _, value := range values {
		if value != nil && (*value < math.MinInt32 || *value > math.MaxInt32) {
			return false
		}
	}
	return true
}

// mergedStreamFromColumns merges the column values into the extra fields and
// decodes the merged object.
func mergedStreamFromColumns(index int, streamType string, codec, language, title, defaultValue, forced, external, bitrate, width, height, channels, sampleRate any, extra string) (playback.MediaStream, error) {
	fields, err := decodeFields(extra)
	if err != nil {
		return playback.MediaStream{}, err
	}
	fields["Index"] = mustJSON(index)
	fields["Type"] = mustJSON(streamType)
	fields["Codec"] = mustJSON(asString(codec))
	fields["Language"] = mustJSON(optionalString(language))
	fields["Title"] = mustJSON(optionalString(title))
	fields["IsDefault"] = mustJSON(asBool(defaultValue))
	fields["IsForced"] = mustJSON(asBool(forced))
	fields["IsExternal"] = mustJSON(asBool(external))
	fields["BitRate"] = mustJSON(optionalInt(bitrate))
	fields["Width"] = mustJSON(optionalInt(width))
	fields["Height"] = mustJSON(optionalInt(height))
	fields["Channels"] = mustJSON(optionalInt(channels))
	fields["SampleRate"] = mustJSON(optionalInt(sampleRate))
	data, err := encodeFields(fields)
	if err != nil {
		return playback.MediaStream{}, err
	}
	var stream playback.MediaStream
	if err := json.Unmarshal([]byte(data), &stream); err != nil {
		return playback.MediaStream{}, fmt.Errorf("decode media stream: %w", err)
	}
	return stream, nil
}

func attachmentFromColumns(index int, filename, mimeType any, extra string) (playback.MediaAttachment, error) {
	fields, err := decodeFields(extra)
	if err != nil {
		return playback.MediaAttachment{}, err
	}
	fields["Index"] = mustJSON(index)
	fields["FileName"] = mustJSON(asString(filename))
	fields["MimeType"] = mustJSON(asString(mimeType))
	data, err := encodeFields(fields)
	if err != nil {
		return playback.MediaAttachment{}, err
	}
	var attachment playback.MediaAttachment
	if err := json.Unmarshal([]byte(data), &attachment); err != nil {
		return playback.MediaAttachment{}, fmt.Errorf("decode media attachment: %w", err)
	}
	return attachment, nil
}

func objectFields(value any) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return decodeFields(string(data))
}

func decodeFields(raw string) (map[string]json.RawMessage, error) {
	fields := make(map[string]json.RawMessage)
	if raw == "" {
		return fields, nil
	}
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func encodeFields(fields map[string]json.RawMessage) (string, error) {
	data, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
