// SPDX-License-Identifier: GPL-2.0-only

package encoding

import (
	"strconv"
	"strings"
)

// SubtitleExtraction is one embedded subtitle stream that a single
// extraction pass writes to its cache file.
type SubtitleExtraction struct {
	// InputIndex is the ffmpeg stream index in the input file
	// (EncodingHelper.FindIndex), not the MediaStream index.
	InputIndex int
	Codec      string
	OutputPath string
}

// ExtractableSubtitleFormat ports SubtitleEncoder.GetExtractableSubtitleFormat:
// ASS, SSA and PGS keep their codec, VobSub goes into an MKS container, and
// every other text codec becomes SRT.
func ExtractableSubtitleFormat(codec string) string {
	switch {
	case strings.EqualFold(codec, "ass"), strings.EqualFold(codec, "ssa"), strings.EqualFold(codec, "pgssub"):
		return codec
	case isVobSubCodec(codec):
		return "mks"
	default:
		return "srt"
	}
}

// ExtractableSubtitleExtension ports
// SubtitleEncoder.GetExtractableSubtitleFileExtension. ffmpeg does not accept
// .pgssub as a file extension, so PGS is written as .sup.
func ExtractableSubtitleExtension(codec string) string {
	switch {
	case strings.EqualFold(codec, "pgssub"):
		return "sup"
	case isVobSubCodec(codec):
		return "mks"
	default:
		return ExtractableSubtitleFormat(codec)
	}
}

// SubtitleExtractionArgs ports SubtitleEncoder.ExtractAllExtractableSubtitlesInternal
// and RunSubtitleExtractionProcess: one ffmpeg pass writes every output.
// There is no -copyts, so ffmpeg rebases the timestamps to the zero-based
// timeline of the served video.
func SubtitleExtractionArgs(inputPath string, outputs []SubtitleExtraction) []string {
	args := make([]string, 0, 4+len(outputs)*12)
	args = append(args, "-nostdin", "-y", "-i", inputPath)
	for _, output := range outputs {
		codec := "srt"
		if isCopyableSubtitleCodec(output.Codec) {
			codec = "copy"
		}
		args = append(args, "-map", "0:"+strconv.Itoa(output.InputIndex), "-an", "-vn", "-c:s", codec)
		// FFmpeg has no .idx/.sub muxer, so VobSub is written as MKS.
		if isVobSubCodec(output.Codec) {
			args = append(args, "-f", "matroska")
		}
		args = append(args, "-flush_packets", "1", output.OutputPath)
	}
	return args
}

// AttachmentDumpArgs ports AttachmentExtractor.ExtractAttachmentInternal. An
// input with video or audio needs a null output; ffmpeg then stops at once
// because of -t 0. -nostdin is added because a one-shot job reads no input
// (CONVENTIONS 5.4).
func AttachmentDumpArgs(inputPath string, attachmentIndex int, outputPath string, hasVideoOrAudio bool) []string {
	args := []string{"-nostdin", "-dump_attachment:" + strconv.Itoa(attachmentIndex), outputPath, "-i", inputPath}
	if hasVideoOrAudio {
		args = append(args, "-t", "0", "-f", "null", "null")
	}
	return args
}

// AttachmentDump names one attachment and the file that receives it.
type AttachmentDump struct {
	Index      int
	OutputPath string
}

// AllAttachmentsDumpArgs ports AttachmentExtractor.ExtractAllAttachmentsIndividuallyInternal:
// one ffmpeg pass dumps every listed attachment.
func AllAttachmentsDumpArgs(inputPath string, dumps []AttachmentDump, hasVideoOrAudio bool) []string {
	args := make([]string, 0, 2*len(dumps)+8)
	args = append(args, "-nostdin")
	for _, dump := range dumps {
		args = append(args, "-dump_attachment:"+strconv.Itoa(dump.Index), dump.OutputPath)
	}
	args = append(args, "-i", inputPath)
	if hasVideoOrAudio {
		args = append(args, "-t", "0", "-f", "null", "null")
	}
	return args
}

// isCopyableSubtitleCodec ports SubtitleEncoder.IsCodecCopyable.
func isCopyableSubtitleCodec(codec string) bool {
	switch strings.ToLower(codec) {
	case "ass", "ssa", "srt", "subrip", "pgssub":
		return true
	default:
		return isVobSubCodec(codec)
	}
}

// isVobSubCodec ports MediaStream.IsVobSubFormat.
func isVobSubCodec(codec string) bool {
	lower := strings.ToLower(codec)
	return strings.Contains(lower, "dvdsub") || strings.Contains(lower, "vobsub")
}
