// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// nvencEncoders are the NVENC encoders that the HLS builder can select.
func nvencEncoders() []string { return []string{"h264_nvenc", "hevc_nvenc", "av1_nvenc"} }

// NvidiaCapabilities is the result of ProbeNvidia. The zero value means
// that no CUDA device is usable.
type NvidiaCapabilities struct {
	// Device reports that ffmpeg opened CUDA device 0.
	Device bool
	// Encoders lists the NVENC encoders that encoded a test frame on the
	// device. A listed encoder can still be unsupported by the GPU: an
	// Ampere card rejects av1_nvenc.
	Encoders []string
	// OverlayCudaAlphaFormat is the OverlayCudaAlphaFormat filter option
	// probe of EncoderValidator.cs:167.
	OverlayCudaAlphaFormat bool
}

// ProbeNvidia opens CUDA device 0 and encodes one test frame with each NVENC
// encoder that caps lists. Upstream Jellyfin selects an NVENC encoder by name
// only; the frame test keeps a transcode off an encoder that the GPU does
// not have (D-37). A failed device or encoder probe is a capability fact,
// not an error. The error result is only for a canceled context.
func ProbeNvidia(ctx context.Context, path string, caps Capabilities) (NvidiaCapabilities, error) {
	var result NvidiaCapabilities
	if !containsName(caps.HWAccels, "cuda") {
		return result, nil
	}
	options := normalizeCapabilityOptions(CapabilityOptions{})
	device := []string{"-nostdin", "-v", "error", "-hide_banner", "-init_hw_device", "cuda=cu:0"}
	_, err := runProbeCommand(ctx, path, options, false, append(device, "-f", "lavfi", "-i", "nullsrc=s=256x256:d=1", "-frames:v", "1", "-f", "null", "-")...)
	if err != nil {
		return result, contextError(ctx, err)
	}
	result.Device = true
	for _, encoder := range nvencEncoders() {
		if !containsName(caps.Encoders, encoder) {
			continue
		}
		// NVENC needs at least 145x49 on some codecs; 1280x720 suits all.
		_, err := runProbeCommand(ctx, path, options, false, append(device,
			"-f", "lavfi", "-i", "color=c=black:s=1280x720:r=25:d=1", "-frames:v", "1",
			"-c:v", encoder, "-f", "null", "-")...)
		if err != nil {
			if ctxErr := contextError(ctx, err); ctxErr != nil {
				return result, ctxErr
			}
			continue
		}
		result.Encoders = append(result.Encoders, encoder)
	}
	if containsName(caps.Filters, "overlay_cuda") {
		output, err := runProbeCommand(ctx, path, options, true, "-nostdin", "-hide_banner", "-h", "filter=overlay_cuda")
		if err != nil {
			return result, contextError(ctx, err)
		}
		result.OverlayCudaAlphaFormat = strings.Contains(string(output), "alpha_format")
	}
	return result, nil
}

// contextError returns the context error when err comes from cancellation,
// and nil for a probe that ran and failed.
func contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("ffmpeg NVIDIA probe: %w", errors.Join(ctxErr, err))
	}
	return nil
}

func containsName(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
