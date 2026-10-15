// SPDX-License-Identifier: GPL-2.0-only

package keyframes

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// Matroska element IDs, MatroskaConstants.cs. IDs keep their VINT marker bits.
const (
	mkvSegment               = 0x18538067
	mkvSeekHead              = 0x114D9B74
	mkvSeek                  = 0x4DBB
	mkvSeekID                = 0x53AB
	mkvSeekPosition          = 0x53AC
	mkvInfo                  = 0x1549A966
	mkvTimestampScale        = 0x2AD7B1
	mkvDuration              = 0x4489
	mkvTracks                = 0x1654AE6B
	mkvTrackEntry            = 0xAE
	mkvTrackNumber           = 0xD7
	mkvTrackType             = 0x83
	mkvTrackTypeVideo        = 0x1
	mkvCues                  = 0x1C53BB6B
	mkvCuePoint              = 0xBB
	mkvCueTime               = 0xB3
	mkvCueTrackPositions     = 0xB7
	mkvCueTrack              = 0xF7
	mkvDefaultTimestampScale = 1_000_000
)

// maxMatroskaMetadataBytes bounds the Info, Tracks, and Cues bodies read into
// memory. A three-hour film with a cue on every keyframe needs well under 1 MiB.
const maxMatroskaMetadataBytes = 64 << 20

// maxSeekHeadScan bounds the top-level elements visited before the Segment and
// SeekHead are found. Only EBML header, Void, and CRC elements precede them.
const maxSeekHeadScan = 64

var (
	// ErrMatroskaMalformed means the file is not a Matroska file the Cues reader
	// can use: a missing or truncated element, or a SeekHead without Info,
	// Tracks, and Cues positions.
	ErrMatroskaMalformed = errors.New("malformed matroska metadata")

	// ErrNoKeyframes means the container metadata lists no video keyframes.
	// Upstream treats this as an extraction miss, not as data.
	ErrNoKeyframes = errors.New("no keyframes in container metadata")
)

// MatroskaExtractor reads keyframes from the Cues of the first video track. It
// reads only the SeekHead, Info, Tracks, and Cues, never the clusters. Port of
// Jellyfin.MediaEncoding.Keyframes/Matroska/MatroskaKeyframeExtractor.cs.
type MatroskaExtractor struct{}

// Extract reads keyframe data from the borrowed source descriptor.
func (MatroskaExtractor) Extract(ctx context.Context, source *os.File) (Data, error) {
	if source == nil {
		return Data{}, errors.New("matroska source file is nil")
	}
	info, err := source.Stat()
	if err != nil {
		return Data{}, fmt.Errorf("stat matroska source: %w", err)
	}
	return ExtractMatroska(ctx, source, info.Size())
}

// ExtractMatroska reads keyframe data from a Matroska file of size bytes.
// Keyframes are CueTime values of the first video track, scaled to ticks.
// The total duration is the Segment Info Duration, or zero when absent.
func ExtractMatroska(ctx context.Context, source io.ReaderAt, size int64) (Data, error) {
	if ctx == nil {
		return Data{}, errors.New("matroska extraction context is nil")
	}
	reader := ebmlReader{source: source, size: size}
	segmentStart, segmentEnd, seek, err := reader.readSeekHead()
	if err != nil {
		return Data{}, err
	}
	if err := ctx.Err(); err != nil {
		return Data{}, err
	}

	infoBody, err := reader.readTopLevel(segmentStart, segmentEnd, seek.info, mkvInfo)
	if err != nil {
		return Data{}, err
	}
	timestampScale, duration, err := parseInfo(infoBody)
	if err != nil {
		return Data{}, err
	}
	tracksBody, err := reader.readTopLevel(segmentStart, segmentEnd, seek.tracks, mkvTracks)
	if err != nil {
		return Data{}, err
	}
	videoTrack, err := firstVideoTrack(tracksBody)
	if err != nil {
		return Data{}, err
	}
	if err := ctx.Err(); err != nil {
		return Data{}, err
	}
	cuesBody, err := reader.readTopLevel(segmentStart, segmentEnd, seek.cues, mkvCues)
	if err != nil {
		return Data{}, err
	}
	keyframes, err := cueKeyframes(ctx, cuesBody, videoTrack, timestampScale)
	if err != nil {
		return Data{}, err
	}
	if len(keyframes) == 0 {
		return Data{}, ErrNoKeyframes
	}
	total, err := durationToTicks(duration, timestampScale)
	if err != nil {
		return Data{}, err
	}
	return Data{TotalDurationTicks: total, KeyframeTicks: keyframes}, nil
}

type seekPositions struct{ info, tracks, cues int64 }

type ebmlReader struct {
	source io.ReaderAt
	size   int64
}

type ebmlHeader struct {
	id        uint64
	dataStart int64
	dataSize  int64
	unknown   bool
}

// readHeader reads the element header at pos. The element must start before
// limit; its body may extend past limit only when the caller checks.
func (reader ebmlReader) readHeader(pos, limit int64) (ebmlHeader, error) {
	if pos < 0 || pos >= limit {
		return ebmlHeader{}, fmt.Errorf("%w: element offset %d outside [0,%d)", ErrMatroskaMalformed, pos, limit)
	}
	var raw [12]byte
	n, err := reader.source.ReadAt(raw[:min(int64(len(raw)), limit-pos)], pos)
	if n == 0 && err != nil {
		return ebmlHeader{}, fmt.Errorf("%w: read element header at %d: %w", ErrMatroskaMalformed, pos, err)
	}
	id, idLength, ok := readVINT(raw[:n], true)
	if !ok {
		return ebmlHeader{}, fmt.Errorf("%w: invalid element ID at %d", ErrMatroskaMalformed, pos)
	}
	size, sizeLength, ok := readVINT(raw[idLength:n], false)
	if !ok {
		return ebmlHeader{}, fmt.Errorf("%w: invalid element size at %d", ErrMatroskaMalformed, pos)
	}
	header := ebmlHeader{id: id, dataStart: pos + int64(idLength+sizeLength)}
	if isUnknownSize(size, sizeLength) {
		header.unknown = true
		return header, nil
	}
	if size > uint64(math.MaxInt64-header.dataStart) {
		return ebmlHeader{}, fmt.Errorf("%w: element size overflows at %d", ErrMatroskaMalformed, pos)
	}
	header.dataSize = int64(size)
	return header, nil
}

// readSeekHead finds the Segment and its first SeekHead, then reads the Info,
// Tracks, and Cues positions. EbmlReaderExtensions.cs ReadSeekHead.
func (reader ebmlReader) readSeekHead() (int64, int64, seekPositions, error) {
	var segment ebmlHeader
	pos := int64(0)
	for scanned := 0; ; scanned++ {
		if scanned == maxSeekHeadScan {
			return 0, 0, seekPositions{}, fmt.Errorf("%w: expected a segment container", ErrMatroskaMalformed)
		}
		header, err := reader.readHeader(pos, reader.size)
		if err != nil {
			return 0, 0, seekPositions{}, err
		}
		if header.id == mkvSegment {
			segment = header
			break
		}
		if header.unknown {
			return 0, 0, seekPositions{}, fmt.Errorf("%w: unknown-size element before segment", ErrMatroskaMalformed)
		}
		pos = header.dataStart + header.dataSize
	}
	segmentStart := segment.dataStart
	segmentEnd := reader.size
	if !segment.unknown && segment.dataStart+segment.dataSize < segmentEnd {
		segmentEnd = segment.dataStart + segment.dataSize
	}

	var seekHead ebmlHeader
	pos = segmentStart
	for scanned := 0; ; scanned++ {
		if scanned == maxSeekHeadScan || pos >= segmentEnd {
			return 0, 0, seekPositions{}, fmt.Errorf("%w: expected a SeekHead", ErrMatroskaMalformed)
		}
		header, err := reader.readHeader(pos, segmentEnd)
		if err != nil {
			return 0, 0, seekPositions{}, err
		}
		if header.unknown {
			return 0, 0, seekPositions{}, fmt.Errorf("%w: expected a SeekHead", ErrMatroskaMalformed)
		}
		if header.id == mkvSeekHead {
			seekHead = header
			break
		}
		pos = header.dataStart + header.dataSize
	}
	body, err := reader.readBody(seekHead, segmentEnd)
	if err != nil {
		return 0, 0, seekPositions{}, err
	}

	positions := seekPositions{info: -1, tracks: -1, cues: -1}
	err = walkChildren(body, func(id uint64, seek []byte) (bool, error) {
		if id != mkvSeek {
			return true, nil
		}
		var target uint64
		position := int64(-1)
		if err := walkChildren(seek, func(id uint64, value []byte) (bool, error) {
			switch id {
			case mkvSeekID:
				target = readUnsigned(value)
			case mkvSeekPosition:
				if len(value) > 8 {
					return false, fmt.Errorf("%w: SeekPosition too long", ErrMatroskaMalformed)
				}
				unsigned := readUnsigned(value)
				if unsigned > math.MaxInt64 {
					return false, fmt.Errorf("%w: SeekPosition overflows", ErrMatroskaMalformed)
				}
				position = int64(unsigned)
			}
			return true, nil
		}); err != nil {
			return false, err
		}
		if position >= 0 {
			switch target {
			case mkvInfo:
				positions.info = position
			case mkvTracks:
				positions.tracks = position
			case mkvCues:
				positions.cues = position
			}
		}
		return positions.info < 0 || positions.tracks < 0 || positions.cues < 0, nil
	})
	if err != nil {
		return 0, 0, seekPositions{}, err
	}
	if positions.info < 0 || positions.tracks < 0 || positions.cues < 0 {
		return 0, 0, seekPositions{}, fmt.Errorf("%w: SeekHead is missing or does not contain Info, Tracks and Cues positions", ErrMatroskaMalformed)
	}
	return segmentStart, segmentEnd, positions, nil
}

// readTopLevel reads the body of the Segment child at the SeekPosition
// relative to the Segment data start and checks its ID.
func (reader ebmlReader) readTopLevel(segmentStart, segmentEnd, relative int64, want uint64) ([]byte, error) {
	if relative > math.MaxInt64-segmentStart {
		return nil, fmt.Errorf("%w: seek position overflows", ErrMatroskaMalformed)
	}
	header, err := reader.readHeader(segmentStart+relative, segmentEnd)
	if err != nil {
		return nil, err
	}
	if header.id != want {
		return nil, fmt.Errorf("%w: element %#x at seek position %d, want %#x", ErrMatroskaMalformed, header.id, relative, want)
	}
	if header.unknown {
		return nil, fmt.Errorf("%w: unknown-size element %#x", ErrMatroskaMalformed, want)
	}
	return reader.readBody(header, segmentEnd)
}

func (reader ebmlReader) readBody(header ebmlHeader, limit int64) ([]byte, error) {
	if header.dataSize > limit-header.dataStart {
		return nil, fmt.Errorf("%w: element %#x is truncated", ErrMatroskaMalformed, header.id)
	}
	if header.dataSize > maxMatroskaMetadataBytes {
		return nil, fmt.Errorf("%w: element %#x exceeds %d bytes", ErrMatroskaMalformed, header.id, maxMatroskaMetadataBytes)
	}
	body := make([]byte, header.dataSize)
	if n, err := reader.source.ReadAt(body, header.dataStart); n < len(body) {
		return nil, fmt.Errorf("%w: read element %#x: %w", ErrMatroskaMalformed, header.id, err)
	}
	return body, nil
}

// parseInfo reads TimestampScale and Duration. EbmlReaderExtensions.cs ReadInfo.
// An absent TimestampScale takes the Matroska default of 1 ms.
func parseInfo(body []byte) (int64, *float64, error) {
	scale := int64(mkvDefaultTimestampScale)
	var duration *float64
	err := walkChildren(body, func(id uint64, value []byte) (bool, error) {
		switch id {
		case mkvTimestampScale:
			unsigned := readUnsigned(value)
			if len(value) > 8 || unsigned == 0 || unsigned > math.MaxInt64 {
				return false, fmt.Errorf("%w: invalid TimestampScale", ErrMatroskaMalformed)
			}
			scale = int64(unsigned)
		case mkvDuration:
			var parsed float64
			switch len(value) {
			case 0:
				parsed = 0
			case 4:
				parsed = float64(math.Float32frombits(binary.BigEndian.Uint32(value)))
			case 8:
				parsed = math.Float64frombits(binary.BigEndian.Uint64(value))
			default:
				return false, fmt.Errorf("%w: invalid Duration length %d", ErrMatroskaMalformed, len(value))
			}
			duration = &parsed
		}
		return true, nil
	})
	return scale, duration, err
}

// firstVideoTrack returns the TrackNumber of the first video TrackEntry.
// EbmlReaderExtensions.cs FindFirstTrackNumberByType.
func firstVideoTrack(body []byte) (uint64, error) {
	var found bool
	var number uint64
	err := walkChildren(body, func(id uint64, entry []byte) (bool, error) {
		if id != mkvTrackEntry {
			return true, nil
		}
		var trackNumber, trackType uint64
		if err := walkChildren(entry, func(id uint64, value []byte) (bool, error) {
			switch id {
			case mkvTrackNumber:
				trackNumber = readUnsigned(value)
			case mkvTrackType:
				trackType = readUnsigned(value)
			}
			return true, nil
		}); err != nil {
			return false, err
		}
		if trackType == mkvTrackTypeVideo {
			found, number = true, trackNumber
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("%w: no stream with type %d found", ErrMatroskaMalformed, mkvTrackTypeVideo)
	}
	return number, nil
}

// cueKeyframes returns the scaled CueTime of every CuePoint whose first
// CueTrackPositions names the video track. Upstream reads only the first
// CueTrackPositions of each CuePoint (MatroskaKeyframeExtractor.cs).
func cueKeyframes(ctx context.Context, body []byte, videoTrack uint64, timestampScale int64) ([]int64, error) {
	keyframes := make([]int64, 0, len(body)/16)
	err := walkChildren(body, func(id uint64, point []byte) (bool, error) {
		if id != mkvCuePoint {
			return true, nil
		}
		if len(keyframes)%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return false, err
			}
		}
		var cueTime uint64
		var haveTime, havePositions, haveTrack bool
		var track uint64
		if err := walkChildren(point, func(id uint64, value []byte) (bool, error) {
			switch {
			case id == mkvCueTime && !haveTime:
				cueTime, haveTime = readUnsigned(value), true
			case id == mkvCueTrackPositions && !havePositions:
				havePositions = true
				return true, walkChildren(value, func(id uint64, value []byte) (bool, error) {
					if id == mkvCueTrack {
						track, haveTrack = readUnsigned(value), true
						return false, nil
					}
					return true, nil
				})
			}
			return true, nil
		}); err != nil {
			return false, err
		}
		if !haveTime || !haveTrack || track != videoTrack {
			return true, nil
		}
		ticks, err := scaleToTicks(cueTime, timestampScale)
		if err != nil {
			return false, err
		}
		keyframes = append(keyframes, ticks)
		return true, nil
	})
	return keyframes, err
}

// scaleToTicks converts a scaled timestamp to 100 ns ticks. TimestampScale is
// in nanoseconds. MatroskaKeyframeExtractor.cs ScaleToTicks(ulong, long).
func scaleToTicks(value uint64, timestampScale int64) (int64, error) {
	if value > math.MaxInt64 || (value != 0 && int64(value) > math.MaxInt64/timestampScale) {
		return 0, fmt.Errorf("%w: timestamp %d overflows ticks", ErrMatroskaMalformed, value)
	}
	return int64(value) * timestampScale / 100, nil
}

// durationToTicks converts the floating Info Duration to ticks with
// Convert.ToInt64 rounding (to nearest, ties to even). An absent Duration is
// zero, as upstream's `info.Duration ?? 0`.
func durationToTicks(duration *float64, timestampScale int64) (int64, error) {
	if duration == nil {
		return 0, nil
	}
	value := math.RoundToEven(*duration * float64(timestampScale) / 100)
	if math.IsNaN(value) || value < math.MinInt64 || value >= math.MaxInt64 {
		return 0, fmt.Errorf("%w: invalid Duration", ErrMatroskaMalformed)
	}
	return int64(value), nil
}

// walkChildren calls visit for each child element in body until visit returns
// false. A child that is truncated or of unknown size is an error.
func walkChildren(body []byte, visit func(id uint64, value []byte) (bool, error)) error {
	for len(body) > 0 {
		id, idLength, ok := readVINT(body, true)
		if !ok {
			return fmt.Errorf("%w: invalid child element ID", ErrMatroskaMalformed)
		}
		size, sizeLength, ok := readVINT(body[idLength:], false)
		if !ok || isUnknownSize(size, sizeLength) {
			return fmt.Errorf("%w: invalid child element size", ErrMatroskaMalformed)
		}
		body = body[idLength+sizeLength:]
		if size > uint64(len(body)) {
			return fmt.Errorf("%w: child element %#x is truncated", ErrMatroskaMalformed, id)
		}
		more, err := visit(id, body[:size])
		if err != nil || !more {
			return err
		}
		body = body[size:]
	}
	return nil
}

// readVINT decodes an EBML variable-length integer. IDs keep the length
// marker and allow at most 4 bytes; sizes drop it and allow at most 8 bytes.
func readVINT(data []byte, keepMarker bool) (uint64, int, bool) {
	if len(data) == 0 || data[0] == 0 {
		return 0, 0, false
	}
	length := 1
	for mask := byte(0x80); data[0]&mask == 0; mask >>= 1 {
		length++
	}
	if (keepMarker && length > 4) || length > len(data) {
		return 0, 0, false
	}
	value := uint64(data[0])
	if !keepMarker {
		value &= uint64(0xFF >> length)
	}
	for _, next := range data[1:length] {
		value = value<<8 | uint64(next)
	}
	return value, length, true
}

func isUnknownSize(size uint64, length int) bool {
	return size == (uint64(1)<<(7*length))-1
}

func readUnsigned(value []byte) uint64 {
	var result uint64
	for _, next := range value {
		result = result<<8 | uint64(next)
	}
	return result
}
