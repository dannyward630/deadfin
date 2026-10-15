// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeFakeNvidiaFFmpeg answers the device, encoder, and filter-help probes.
// The device exits with deviceStatus; each encoder in rejected exits 1, as
// av1_nvenc does on a GPU without an AV1 encoder.
func writeFakeNvidiaFFmpeg(t *testing.T, deviceStatus string, rejected ...string) string {
	t.Helper()
	body := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"*'-h filter=overlay_cuda'*) echo '  alpha_format      <int>        ..FV....... alpha format'; exit 0 ;;\n" +
		"*'-init_hw_device cuda=cu:0 -f lavfi -i nullsrc=s=256x256:d=1 -frames:v 1 -f null -') exit " + deviceStatus + " ;;\n"
	for _, encoder := range rejected {
		body += "*'-c:v " + encoder + " -f null -') echo 'OpenEncodeSessionEx failed: unsupported device (2)' >&2; exit 1 ;;\n"
	}
	body += "*'-init_hw_device cuda=cu:0 -f lavfi -i color=c=black:s=1280x720:r=25:d=1 -frames:v 1 -c:v '*) exit 0 ;;\n" +
		"esac\nexit 99\n"
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func nvidiaFFmpegCapabilities() Capabilities {
	return Capabilities{
		Encoders: []string{"av1_nvenc", "h264_nvenc", "hevc_nvenc", "libx264"},
		Filters:  []string{"overlay_cuda", "scale_cuda"},
		HWAccels: []string{"cuda", "vaapi"},
	}
}

func TestProbeNvidiaKeepsEncodersTheDeviceAccepts(t *testing.T) {
	for _, test := range []struct {
		name     string
		rejected []string
		want     []string
	}{
		{"Ada", nil, []string{"h264_nvenc", "hevc_nvenc", "av1_nvenc"}},
		{"Ampere has no AV1 encoder", []string{"av1_nvenc"}, []string{"h264_nvenc", "hevc_nvenc"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ProbeNvidia(context.Background(), writeFakeNvidiaFFmpeg(t, "0", test.rejected...), nvidiaFFmpegCapabilities())
			if err != nil {
				t.Fatalf("ProbeNvidia: %v", err)
			}
			want := NvidiaCapabilities{Device: true, Encoders: test.want, OverlayCudaAlphaFormat: true}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ProbeNvidia = %#v, want %#v", got, want)
			}
		})
	}
}

func TestProbeNvidiaWithoutDevice(t *testing.T) {
	got, err := ProbeNvidia(context.Background(), writeFakeNvidiaFFmpeg(t, "1"), nvidiaFFmpegCapabilities())
	if err != nil {
		t.Fatalf("ProbeNvidia: %v", err)
	}
	if !reflect.DeepEqual(got, NvidiaCapabilities{}) {
		t.Fatalf("ProbeNvidia = %#v, want no device", got)
	}
}

func TestProbeNvidiaSkipsBuildsWithoutCuda(t *testing.T) {
	caps := nvidiaFFmpegCapabilities()
	caps.HWAccels = []string{"vaapi"}
	// The path does not exist: a probe that ran would fail.
	got, err := ProbeNvidia(context.Background(), filepath.Join(t.TempDir(), "missing"), caps)
	if err != nil || !reflect.DeepEqual(got, NvidiaCapabilities{}) {
		t.Fatalf("ProbeNvidia = %#v, %v; want no probe", got, err)
	}
}

func TestProbeNvidiaHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProbeNvidia(ctx, writeFakeNvidiaFFmpeg(t, "0"), nvidiaFFmpegCapabilities()); err == nil {
		t.Fatal("ProbeNvidia ignored a canceled context")
	}
}
