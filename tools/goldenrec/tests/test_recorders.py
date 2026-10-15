import hashlib
import itertools
import json
import os
import shutil
import subprocess
import tempfile
import platform
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[3]
DEADFIN = ROOT / "deadfin"
UPSTREAM = ROOT / "upstream" / "jellyfin"
RECORDER = ROOT / "tools" / "goldenrec" / "record.sh"
UPSTREAM_TEST_DATA = UPSTREAM / "tests" / "Jellyfin.Model.Tests" / "Test Data"
PIN = "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb"

PROFILE_GLOB = "DeviceProfile-*.json"
SOURCE_GLOB = "MediaSourceInfo-*.json"

ENCODING_FACT_KEYS = {
    "subtitlePath",
    "subtitleInputIndex",
    "subtitleStreamIndex",
    "subtitleCodec",
    "audioCodec",
    "audioStreamIndex",
    "audioChannels",
    "downmixAudioBoost",
    "nalLengthSize",
    "capabilities",
    "selectedVideoEncoder",
    "selectedAudioEncoder",
}

# These five rows are verbatim assertions from StreamBuilderTests.cs at the pinned
# upstream commit. They intentionally cover direct play, audio-only transcoding,
# multiple reasons, a stream-count limit, and rotation handling.
UPSTREAM_SPOT_CHECKS = (
    (
        "Chrome",
        "mp4-h264-ac3-aac-srt-2600k",
        "Transcode",
        ("AudioCodecNotSupported",),
        "DirectStream",
        "HLS.mp4",
    ),
    (
        "Firefox",
        "mp4-hevc-ac3-aac-srt-15200k",
        "Transcode",
        ("AudioCodecNotSupported", "VideoCodecNotSupported"),
        "Transcode",
        "HLS.mp4",
    ),
    (
        "SafariNext",
        "mkv-h264-ac3-srt-2600k",
        "Transcode",
        ("AudioChannelsNotSupported", "ContainerNotSupported"),
        "DirectStream",
        "HLS.mp4",
    ),
    (
        "Tizen3-stereo",
        "numstreams-33",
        "Transcode",
        ("StreamCountExceedsLimit",),
        "Remux",
        None,
    ),
    (
        "AndroidTVExoPlayer-NoHevcRotation",
        "mp4-hevc-aac-4000k-r180",
        "Transcode",
        ("VideoCodecNotSupported", "VideoRotationNotSupported"),
        "Transcode",
        None,
    ),
)


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def load_json(path):
    return json.loads(path.read_text(encoding="utf-8"))


def tree_digest(root):
    return {
        path.relative_to(root).as_posix(): sha256(path)
        for path in sorted(root.rglob("*"))
        if path.is_file()
    }


def canonical_reasons(value):
    if value in (None, "", 0, []):
        return ()
    if isinstance(value, str):
        return tuple(sorted(part.strip() for part in value.split("|") if part.strip()))
    return tuple(sorted(value))


class GoldenRecorderBoundaryTests(unittest.TestCase):
    maxDiff = None

    def run_recorder(self, *args, output_root=DEADFIN):
        return subprocess.run(
            [str(RECORDER), *args, "--output-root", str(output_root)],
            cwd=ROOT,
            capture_output=True,
            text=True,
            env=os.environ | {"GOLDENREC_UPSTREAM": str(UPSTREAM)},
        )

    def require_success(self, result):
        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)

    def stage_corpora(self, output):
        for subsystem in ("playback", "encoding"):
            source = DEADFIN / "internal" / subsystem / "testdata"
            destination = output / "internal" / subsystem / "testdata"
            shutil.copytree(source, destination, dirs_exist_ok=True)

    @unittest.skipUnless(
        shutil.which("dotnet"),
        "dotnet SDK is required to verify assembly provenance",
    )
    @unittest.skipUnless(
        platform.machine() in ("x86_64", "amd64"),
        "committed provenance attests canonical linux/amd64 assembly hashes; "
        "a non-amd64 build produces different assembly hashes by design",
    )
    def test_committed_provenance_matches_current_sdk_assemblies(self):
        """Both corpora attest the deterministic assemblies built by the recorder."""
        with tempfile.TemporaryDirectory() as directory:
            generated_root = Path(directory)
            result = self.run_recorder("streambuilder", output_root=generated_root)
            self.require_success(result)
            expected = load_json(
                generated_root / "internal" / "playback" / "testdata" / "golden" / "provenance.json"
            )["upstream"]["assemblies"]

        for subsystem in ("playback", "encoding"):
            with self.subTest(subsystem=subsystem):
                committed = load_json(
                    DEADFIN / "internal" / subsystem / "testdata" / "golden" / "provenance.json"
                )
                self.assertEqual(committed["upstream"]["assemblies"], expected)

    def test_verify_accepts_committed_corpora(self):
        """The public verifier is the gate; it must accept the checked-in data."""
        result = self.run_recorder("verify")
        self.require_success(result)

    def test_streambuilder_fixtures_are_complete_verbatim_upstream_copies(self):
        expected_profiles = sorted(UPSTREAM_TEST_DATA.glob(PROFILE_GLOB))
        expected_sources = sorted(UPSTREAM_TEST_DATA.glob(SOURCE_GLOB))
        actual_root = DEADFIN / "internal" / "playback" / "testdata"
        actual_profiles = sorted(actual_root.glob(PROFILE_GLOB))
        actual_sources = sorted(actual_root.glob(SOURCE_GLOB))

        self.assertEqual(len(expected_profiles), 19)
        self.assertEqual(len(expected_sources), 33)
        self.assertEqual([p.name for p in actual_profiles], [p.name for p in expected_profiles])
        self.assertEqual([p.name for p in actual_sources], [p.name for p in expected_sources])
        for source in (*expected_profiles, *expected_sources):
            copied = actual_root / source.name
            self.assertEqual(sha256(copied), sha256(source), source.name)

    def test_streambuilder_matrix_covers_full_cross_product_and_upstream_spot_checks(self):
        matrix = load_json(DEADFIN / "internal" / "playback" / "testdata" / "golden" / "streambuilder.json")
        self.assertEqual(matrix["format"], "deadfin.streambuilder.golden/v1")
        cases = matrix["cases"]
        self.assertEqual(len(cases), 19 * 33)
        pairs = {(case["profile"], case["source"]) for case in cases}
        expected_pairs = {
            (profile.stem.removeprefix("DeviceProfile-"), source.stem.removeprefix("MediaSourceInfo-"))
            for profile in UPSTREAM_TEST_DATA.glob(PROFILE_GLOB)
            for source in UPSTREAM_TEST_DATA.glob(SOURCE_GLOB)
        }
        self.assertEqual(pairs, expected_pairs)

        assertions = load_json(DEADFIN / "internal" / "playback" / "testdata" / "streambuilder-assertions.yaml")
        self.assertEqual(assertions["format"], "deadfin.streambuilder.assertions/v1")
        indexed = {
            (case["profile"], case["source"]): case["expect"]
            for case in assertions["cases"]
            if case["method"] == "BuildVideoItemSimple"
        }
        for profile, source, play_method, reasons, kind, protocol in UPSTREAM_SPOT_CHECKS:
            with self.subTest(profile=profile, source=source):
                actual = indexed[(profile, source)]
                self.assertEqual(actual["playMethod"], play_method)
                self.assertEqual(canonical_reasons(actual["transcodeReasons"]), reasons)
                self.assertEqual(actual["kind"], kind)
                self.assertEqual(actual["protocol"], protocol)

    def test_encoding_matrix_covers_declared_software_only_cartesian_product(self):
        matrix = load_json(DEADFIN / "internal" / "encoding" / "testdata" / "golden" / "encoding.json")
        self.assertEqual(matrix["format"], "deadfin.encoding.golden/v1")
        dimensions = matrix["dimensions"]
        expected_dimensions = {
            "inputCodec": ["h264", "hevc", "av1"],
            "subtitleBurnIn": ["none", "text", "pgs"],
            "toneMap": ["sdr", "hdr10", "hlg", "dv"],
            "audio": ["copy", "aac", "downmix"],
            "container": ["ts", "fmp4"],
            "seekOffsetTicks": [0, 900000000],
        }
        self.assertEqual(dimensions, expected_dimensions)
        expected_cases = set(itertools.product(
            dimensions["inputCodec"],
            dimensions["subtitleBurnIn"],
            dimensions["toneMap"],
            dimensions["audio"],
            dimensions["container"],
            ("start", "offset"),
        ))
        actual_cases = {
            (case["inputCodec"], case["subtitle"], case["tonemap"], case["audio"], case["container"], case["seek"])
            for case in matrix["cases"]
        }
        self.assertEqual(len(matrix["cases"]), len(expected_cases))
        self.assertEqual(actual_cases, expected_cases)

    def test_progressive_text_subtitles_use_a_pinned_nonempty_path(self):
        matrix = load_json(DEADFIN / "internal" / "encoding" / "testdata" / "golden" / "encoding.json")
        text_cases = [case for case in matrix["cases"] if case["subtitle"] == "text"]
        expected_filter = "subtitles=f='/cache/goldenrec-subtitles.srt'"

        self.assertTrue(text_cases)
        for case in text_cases:
            with self.subTest(id=case["id"]):
                progressive = case["argv"]["progressive"]
                self.assertIn(expected_filter, progressive)
                self.assertNotIn("f=''", progressive)

    def test_encoding_rows_record_numeric_downmix_boost_configuration(self):
        matrix = load_json(DEADFIN / "internal" / "encoding" / "testdata" / "golden" / "encoding.json")
        rows = (
            matrix["cases"]
            + matrix["progressiveRemuxCases"]
            + matrix["progressiveEdgeCases"]
        )

        self.assertTrue(rows)
        for case in rows:
            with self.subTest(id=case["id"]):
                boost = case["downmixAudioBoost"]
                self.assertIsInstance(boost, (int, float))
                self.assertNotIsInstance(boost, bool)
                self.assertEqual(boost, 2)

    def test_progressive_edge_case_rounds_fractional_text_subtitle_seek_to_even_second(self):
        matrix = load_json(DEADFIN / "internal" / "encoding" / "testdata" / "golden" / "encoding.json")
        cases = matrix["progressiveEdgeCases"]
        expected_cases = {
            (
                "progressive-edge/text-subtitle/1.5s",
                "mp4-h264-ac3-aac-srt-2600k",
                "text",
                "aac",
                "ts",
                15_000_000,
            )
        }
        actual_cases = {
            (
                case["id"],
                case["fixture"],
                case["subtitle"],
                case["audio"],
                case["container"],
                case["seekOffsetTicks"],
            )
            for case in cases
        }

        self.assertEqual(len(cases), len(expected_cases))
        self.assertEqual(actual_cases, expected_cases)
        case = cases[0]
        self.assertEqual(
            set(case),
            {"id", "fixture", "subtitle", "audio", "container", "seekOffsetTicks", "argv"}
            | ENCODING_FACT_KEYS,
        )
        self.assertEqual(set(case["argv"]), {"progressive"})

        progressive = case["argv"]["progressive"]
        self.assertIn("-ss 00:00:01.500", progressive)
        self.assertIn("subtitles=f='/cache/goldenrec-subtitles.srt'", progressive)
        self.assertNotIn("f=''", progressive)
        self.assertIn("setpts=PTS -2/TB", progressive)
        self.assertIn(' -af "asetpts=PTS-2/TB"', progressive)

    def test_progressive_remux_matrix_records_true_stream_copy(self):
        matrix = load_json(DEADFIN / "internal" / "encoding" / "testdata" / "golden" / "encoding.json")
        cases = matrix["progressiveRemuxCases"]
        fixture = "mp4-h264-ac3-aac-srt-2600k"
        expected_cases = {
            (
                f"progressive-remux/h264/{container}/{seek}",
                fixture,
                container,
                seek,
                seek_offset_ticks,
                "h264",
                "ac3",
                "copy",
                "copy",
            )
            for container in ("ts", "fmp4")
            for seek, seek_offset_ticks in (("start", 0), ("offset", 900000000))
        }
        actual_cases = {
            (
                case["id"],
                case["fixture"],
                case["container"],
                case["seek"],
                case["seekOffsetTicks"],
                case["initialVideoCodec"],
                case["initialAudioCodec"],
                case["selectedVideoCodec"],
                case["selectedAudioCodec"],
            )
            for case in cases
        }

        self.assertEqual(len(cases), len(expected_cases))
        self.assertEqual(actual_cases, expected_cases)
        for case in cases:
            with self.subTest(id=case["id"]):
                self.assertEqual(
                    set(case),
                    {
                        "id",
                        "fixture",
                        "container",
                        "seek",
                        "seekOffsetTicks",
                        "initialVideoCodec",
                        "initialAudioCodec",
                        "selectedVideoCodec",
                        "selectedAudioCodec",
                        "argv",
                    }
                    | ENCODING_FACT_KEYS,
                )
                argv = case["argv"]
                self.assertEqual(set(argv), {"input", "map", "video", "audio", "progressive"})
                self.assertIn("-i ", argv["input"])
                self.assertIn("-map 0:0", argv["map"])
                self.assertIn("-map 0:1", argv["map"])

                self.assertEqual(argv["video"].count("-codec:v:0 copy"), 1)
                self.assertEqual(argv["audio"].count("-codec:a:0 copy"), 1)
                progressive = argv["progressive"]
                self.assertEqual(progressive.count("-codec:v:0 copy"), 1)
                self.assertEqual(progressive.count("-codec:a:0 copy"), 1)
                for forbidden in (
                    "libx264",
                    "libx265",
                    "libvpx",
                    "libaom",
                    "-vf",
                    "-filter",
                    "scale",
                    "tonemap",
                ):
                    self.assertNotIn(forbidden, " ".join((argv["video"], argv["audio"], progressive)))

                if case["seek"] == "offset":
                    self.assertIn("-ss 00:01:30.000", progressive)
                    self.assertLess(progressive.index("-ss 00:01:30.000"), progressive.index("-i "))
                else:
                    self.assertNotIn("-ss ", progressive)

                if case["container"] == "fmp4":
                    self.assertIn("-f mp4", progressive)
                    self.assertIn("-movflags", progressive)
                    self.assertTrue(progressive.endswith('"/cache/goldenrec-progressive.mp4"'))
                else:
                    self.assertTrue(progressive.endswith('"/cache/goldenrec-progressive.ts"'))

    def test_provenance_pins_upstream_and_hash_manifests_cover_every_corpus_file(self):
        for subsystem in ("playback", "encoding"):
            testdata = DEADFIN / "internal" / subsystem / "testdata"
            golden = testdata / "golden"
            provenance = load_json(golden / "provenance.json")
            self.assertEqual(provenance["upstream"]["commit"], PIN)
            self.assertNotIn("generatedAt", provenance)
            if subsystem == "encoding":
                self.assertEqual(
                    set(provenance["upstream"]["inputs"]),
                    {
                        "Jellyfin.Api/Controllers/DynamicHlsController.cs",
                        "Jellyfin.Api/Controllers/VideosController.cs",
                        "Jellyfin.Api/Helpers/StreamingHelpers.cs",
                        "MediaBrowser.Controller/MediaEncoding/EncodingHelper.cs",
                        "tests/Jellyfin.Model.Tests/Test Data/MediaSourceInfo-mkv-av1-aac-srt-2600k.json",
                        "tests/Jellyfin.Model.Tests/Test Data/MediaSourceInfo-mp4-h264-ac3-aac-srt-2600k.json",
                        "tests/Jellyfin.Model.Tests/Test Data/MediaSourceInfo-mp4-hevc-ac3-aac-srt-15200k.json",
                    },
                )

            manifest = golden / "SHA256SUMS"
            expected = {}
            for line in manifest.read_text(encoding="utf-8").splitlines():
                digest, separator, name = line.partition("  ")
                self.assertTrue(separator and len(digest) == 64, line)
                expected[name] = digest
            files = {
                path.relative_to(testdata).as_posix(): sha256(path)
                for path in testdata.rglob("*")
                if path.is_file() and path not in {manifest, golden / "provenance.json"}
            }
            self.assertEqual(provenance["assets"], files)
            self.assertEqual(expected, files)

    def test_verify_rejects_tampered_and_incomplete_corpora(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "deadfin"
            self.stage_corpora(output)

            matrix = output / "internal" / "playback" / "testdata" / "golden" / "streambuilder.json"
            matrix.write_text(matrix.read_text(encoding="utf-8") + "\n", encoding="utf-8")
            tampered = self.run_recorder("verify", output_root=output)
            self.assertNotEqual(tampered.returncode, 0, "verify accepted a modified corpus")

            self.stage_corpora(output)
            (output / "internal" / "encoding" / "testdata" / "golden" / "encoding.json").unlink()
            incomplete = self.run_recorder("verify", output_root=output)
            self.assertNotEqual(incomplete.returncode, 0, "verify accepted an incomplete corpus")

    @unittest.skipUnless(shutil.which("dotnet"), "dotnet SDK is required to exercise recorder regeneration")
    def test_all_regenerates_byte_deterministically(self):
        with tempfile.TemporaryDirectory() as first, tempfile.TemporaryDirectory() as second:
            first_output = Path(first) / "deadfin"
            second_output = Path(second) / "deadfin"
            self.require_success(self.run_recorder("all", output_root=first_output))
            self.require_success(self.run_recorder("all", output_root=second_output))
            self.assertEqual(tree_digest(first_output), tree_digest(second_output))


if __name__ == "__main__":
    unittest.main()
