// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// A minimal ISO BMFF reader for HLS fMP4: the init segment gives each
// track's handler, timescale and sample entry; each media segment gives
// each track's base decode time and duration. That is enough to check
// the timeline of a segment sequence.

type box struct {
	typ  string
	body []byte
}

func boxes(b []byte) ([]box, error) {
	var out []box
	for len(b) > 0 {
		if len(b) < 8 {
			return out, errors.New("truncated box header")
		}
		size := uint64(binary.BigEndian.Uint32(b))
		typ := string(b[4:8])
		hdr := uint64(8)
		switch size {
		case 1:
			if len(b) < 16 {
				return out, errors.New("truncated large box header")
			}
			size = binary.BigEndian.Uint64(b[8:])
			hdr = 16
		case 0:
			size = uint64(len(b))
		}
		if size < hdr || size > uint64(len(b)) {
			return out, fmt.Errorf("box %q size %d exceeds %d bytes", typ, size, len(b))
		}
		out = append(out, box{typ: typ, body: b[hdr:size]})
		b = b[size:]
	}
	return out, nil
}

func child(b []byte, path ...string) []byte {
	for _, want := range path {
		bs, _ := boxes(b)
		found := false
		for _, x := range bs {
			if x.typ == want {
				b, found = x.body, true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return b
}

func children(b []byte, typ string) [][]byte {
	bs, _ := boxes(b)
	var out [][]byte
	for _, x := range bs {
		if x.typ == typ {
			out = append(out, x.body)
		}
	}
	return out
}

// track is one trak of an init segment.
type track struct {
	ID              uint32
	Handler         string // vide, soun, text, subt
	Timescale       uint32
	SampleEntry     string // hvc1, hev1, dvh1, avc1, mp4a, ac-3, ec-3, fLaC, Opus
	DefaultDuration uint32 // trex default_sample_duration
}

func parseInit(b []byte) (map[uint32]*track, error) {
	moov := child(b, "moov")
	if moov == nil {
		return nil, errors.New("init segment has no moov")
	}
	out := map[uint32]*track{}
	for _, trak := range children(moov, "trak") {
		t := &track{}
		if tkhd := child(trak, "tkhd"); len(tkhd) >= 24 {
			if tkhd[0] == 1 {
				t.ID = binary.BigEndian.Uint32(tkhd[20:])
			} else {
				t.ID = binary.BigEndian.Uint32(tkhd[12:])
			}
		}
		if mdhd := child(trak, "mdia", "mdhd"); len(mdhd) >= 24 {
			if mdhd[0] == 1 {
				t.Timescale = binary.BigEndian.Uint32(mdhd[20:])
			} else {
				t.Timescale = binary.BigEndian.Uint32(mdhd[12:])
			}
		}
		if hdlr := child(trak, "mdia", "hdlr"); len(hdlr) >= 12 {
			t.Handler = string(hdlr[8:12])
		}
		if stsd := child(trak, "mdia", "minf", "stbl", "stsd"); len(stsd) >= 16 {
			t.SampleEntry = string(stsd[12:16])
		}
		if t.ID == 0 || t.Timescale == 0 {
			return nil, fmt.Errorf("trak without id or timescale")
		}
		out[t.ID] = t
	}
	for _, trex := range children(child(moov, "mvex"), "trex") {
		if len(trex) >= 20 {
			if t := out[binary.BigEndian.Uint32(trex[4:])]; t != nil {
				t.DefaultDuration = binary.BigEndian.Uint32(trex[12:])
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("init segment has no tracks")
	}
	return out, nil
}

// span is one track's decode-time range in a media segment, in seconds.
type span struct {
	Start, End float64
	Samples    int
}

// parseSegment returns each track's span over all moof boxes of a segment.
func parseSegment(b []byte, tracks map[uint32]*track) (map[uint32]span, error) {
	top, err := boxes(b)
	if err != nil {
		return nil, err
	}
	out := map[uint32]span{}
	for _, m := range top {
		if m.typ != "moof" {
			continue
		}
		for _, traf := range children(m.body, "traf") {
			tfhd := child(traf, "tfhd")
			if len(tfhd) < 8 {
				return nil, errors.New("traf without tfhd")
			}
			flags := binary.BigEndian.Uint32(tfhd) & 0xffffff
			id := binary.BigEndian.Uint32(tfhd[4:])
			t := tracks[id]
			if t == nil {
				return nil, fmt.Errorf("traf for unknown track %d", id)
			}
			defDur := t.DefaultDuration
			off := 8
			if flags&0x01 != 0 {
				off += 8
			}
			if flags&0x02 != 0 {
				off += 4
			}
			if flags&0x08 != 0 && len(tfhd) >= off+4 {
				defDur = binary.BigEndian.Uint32(tfhd[off:])
			}
			tfdt := child(traf, "tfdt")
			if len(tfdt) < 8 {
				return nil, fmt.Errorf("track %d: traf without tfdt", id)
			}
			// ffmpeg writes -copyts audio with a negative base (encoder
			// priming), so a version 1 tfdt is read as signed.
			var base int64
			if tfdt[0] == 1 && len(tfdt) >= 12 {
				base = int64(binary.BigEndian.Uint64(tfdt[4:]))
			} else {
				base = int64(binary.BigEndian.Uint32(tfdt[4:]))
			}
			var dur uint64
			n := 0
			for _, trun := range children(traf, "trun") {
				d, c, err := trunDuration(trun, defDur)
				if err != nil {
					return nil, fmt.Errorf("track %d: %w", id, err)
				}
				dur += d
				n += c
			}
			ts := float64(t.Timescale)
			s := span{Start: float64(base) / ts, End: float64(base+int64(dur)) / ts, Samples: n}
			if prev, ok := out[id]; ok {
				s.Start = prev.Start
				s.Samples += prev.Samples
			}
			out[id] = s
		}
	}
	if len(out) == 0 {
		return nil, errors.New("segment has no moof")
	}
	return out, nil
}

func trunDuration(trun []byte, defDur uint32) (uint64, int, error) {
	if len(trun) < 8 {
		return 0, 0, errors.New("short trun")
	}
	flags := binary.BigEndian.Uint32(trun) & 0xffffff
	count := int(binary.BigEndian.Uint32(trun[4:]))
	off := 8
	if flags&0x001 != 0 {
		off += 4
	}
	if flags&0x004 != 0 {
		off += 4
	}
	per := 0
	for _, f := range []uint32{0x100, 0x200, 0x400, 0x800} {
		if flags&f != 0 {
			per += 4
		}
	}
	if flags&0x100 == 0 {
		if defDur == 0 {
			return 0, count, errors.New("trun without durations and no default duration")
		}
		return uint64(defDur) * uint64(count), count, nil
	}
	if len(trun) < off+per*count {
		return 0, count, errors.New("trun shorter than its sample table")
	}
	var total uint64
	for i := 0; i < count; i++ {
		total += uint64(binary.BigEndian.Uint32(trun[off+i*per:]))
	}
	return total, count, nil
}
