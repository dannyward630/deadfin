// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

func TestSQLiteSnapshotReadsAggregateAndOrderedStreamRows(t *testing.T) {
	database := filepath.Join(t.TempDir(), "stock.db")
	seed, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatalf("open stock fixture database: %v", err)
	}
	if _, err := seed.ExecContext(t.Context(), `
		CREATE TABLE BaseItems (
			Id TEXT PRIMARY KEY,
			Path TEXT NOT NULL,
			Size INTEGER,
			RunTimeTicks INTEGER,
			TotalBitrate INTEGER,
			Data TEXT NOT NULL
		);
		CREATE TABLE MediaStreamInfos (
			ItemId TEXT NOT NULL,
			StreamIndex INTEGER NOT NULL,
			AspectRatio TEXT,
			AverageFrameRate TEXT,
			BitDepth INTEGER,
			BitRate INTEGER,
			BlPresentFlag INTEGER,
			ChannelLayout TEXT,
			Channels INTEGER,
			Codec TEXT,
			CodecTag TEXT,
			CodecTimeBase TEXT,
			ColorPrimaries TEXT,
			ColorSpace TEXT,
			ColorTransfer TEXT,
			Comment TEXT,
			DvBlSignalCompatibilityId INTEGER,
			DvLevel INTEGER,
			DvProfile INTEGER,
			DvVersionMajor INTEGER,
			DvVersionMinor INTEGER,
			ElPresentFlag INTEGER,
			Height INTEGER,
			IsAnamorphic INTEGER,
			IsAvc INTEGER,
			IsDefault INTEGER,
			IsExternal INTEGER,
			IsForced INTEGER,
			IsHearingImpaired INTEGER,
			IsInterlaced INTEGER,
			Language TEXT,
			Level INTEGER,
			NalLengthSize INTEGER,
			PixelFormat TEXT,
			Profile TEXT,
			RealFrameRate TEXT,
			RefFrames INTEGER,
			Rotation INTEGER,
			RpuPresentFlag INTEGER,
			SampleRate INTEGER,
			StreamType INTEGER,
			TimeBase TEXT,
			Title TEXT,
			Width INTEGER,
			Hdr10PlusPresentFlag INTEGER,
			IsOriginal INTEGER
		);`); err != nil {
		t.Fatalf("create stock fixture schema: %v", err)
	}
	if _, err := seed.ExecContext(t.Context(), `INSERT INTO BaseItems (Id, Path, Size, RunTimeTicks, TotalBitrate, Data) VALUES (?, ?, ?, ?, ?, ?)`, "item-1", "/media/movie.mkv", 1234, 5678, 91011, `{"Container":"matroska"}`); err != nil {
		t.Fatalf("insert stock item: %v", err)
	}
	for _, stream := range []struct {
		index    int
		codec    string
		external int
	}{
		{index: 2, codec: "subrip", external: 1},
		{index: 0, codec: "h264", external: 0},
	} {
		if _, err := seed.ExecContext(t.Context(), `INSERT INTO MediaStreamInfos (ItemId, StreamIndex, Codec, IsExternal, StreamType) VALUES (?, ?, ?, ?, ?)`, "item-1", stream.index, stream.codec, stream.external, 1); err != nil {
			t.Fatalf("insert stream %d: %v", stream.index, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close stock fixture database: %v", err)
	}

	snapshot, err := storetest.OpenSQLiteSnapshot(t.Context(), database)
	if err != nil {
		t.Fatalf("open stock snapshot: %v", err)
	}
	t.Cleanup(func() {
		if err := snapshot.Close(); err != nil {
			t.Errorf("close stock snapshot: %v", err)
		}
	})

	media, err := snapshot.Media(t.Context(), "/media/movie.mkv")
	if err != nil {
		t.Fatalf("read stock media: %v", err)
	}
	if got, want := media.Container, "matroska"; got != want {
		t.Fatalf("container = %q, want %q", got, want)
	}
	if got, want := []any{media.Size, media.RunTimeTicks, media.TotalBitrate}, []any{int64(1234), int64(5678), int64(91011)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("aggregate fields = %#v, want %#v", got, want)
	}
	if got, want := len(media.Streams), 2; got != want {
		t.Fatalf("stream rows = %d, want %d", got, want)
	}
	got := []map[string]any{
		{"StreamIndex": media.Streams[0]["StreamIndex"], "Codec": media.Streams[0]["Codec"], "IsExternal": media.Streams[0]["IsExternal"], "StreamType": media.Streams[0]["StreamType"]},
		{"StreamIndex": media.Streams[1]["StreamIndex"], "Codec": media.Streams[1]["Codec"], "IsExternal": media.Streams[1]["IsExternal"], "StreamType": media.Streams[1]["StreamType"]},
	}
	want := []map[string]any{
		{"StreamIndex": int64(0), "Codec": "h264", "IsExternal": int64(0), "StreamType": int64(1)},
		{"StreamIndex": int64(2), "Codec": "subrip", "IsExternal": int64(1), "StreamType": int64(1)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered stream projection = %#v, want %#v", got, want)
	}
}
