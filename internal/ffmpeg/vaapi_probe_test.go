// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeFakeVaapiFFmpeg(t *testing.T, output string, status int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	body := "#!/bin/sh\n" +
		"[ \"$*\" = \"-nostdin -v verbose -hide_banner -init_hw_device vaapi=va:/dev/dri/renderD128\" ] || exit 99\n" +
		"printf '%s\\n' '" + output + "' >&2\n" +
		"exit " + string(rune('0'+status)) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeVaapiDriverReadsDriverFromNonzeroExit(t *testing.T) {
	for _, test := range []struct {
		output string
		want   string
	}{
		{"[AVHWDeviceContext @ 0x1] VAAPI driver: Intel iHD driver for Intel(R) Gen Graphics - 24.1.0 ().", VaapiDriverIntelIHD},
		{"[AVHWDeviceContext @ 0x1] VAAPI driver: Intel i965 driver for Intel(R) Coffee Lake - 2.4.1.", VaapiDriverIntelI965},
		{"[AVHWDeviceContext @ 0x1] VAAPI driver: Mesa Gallium driver 24.0.0 for AMD Radeon.", ""},
	} {
		// ffmpeg exits 1 because the probe names no output file.
		path := writeFakeVaapiFFmpeg(t, test.output, 1)
		got, err := ProbeVaapiDriver(context.Background(), path, "/dev/dri/renderD128")
		if err != nil {
			t.Fatalf("ProbeVaapiDriver: %v", err)
		}
		if got != test.want {
			t.Fatalf("ProbeVaapiDriver = %q, want %q", got, test.want)
		}
	}
}

func TestProbeVaapiDriverHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProbeVaapiDriver(ctx, writeFakeVaapiFFmpeg(t, "Intel iHD driver", 0), "/dev/dri/renderD128"); err == nil {
		t.Fatal("ProbeVaapiDriver ignored a canceled context")
	}
}
