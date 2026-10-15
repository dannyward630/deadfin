import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


REPO = Path(__file__).resolve().parents[3]
RUNNER = REPO / "tools" / "compat" / "probeoracle" / "regenerate.py"
ORACLE = RUNNER.parent
PROJECT = ORACLE / "ProbeOracle.csproj"
UPSTREAM = REPO / "upstream" / "jellyfin"
FIXTURES = REPO / "deadfin" / "internal" / "probe" / "testdata" / "upstream-b04614e"
PIN = "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb"


def run_regeneration(output: Path, upstream: Path = UPSTREAM) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(RUNNER), "--output-root", str(output), "--upstream", str(upstream)],
        cwd=REPO,
        text=True,
        capture_output=True,
        check=False,
    )


def require_success(completed: subprocess.CompletedProcess[str], description: str) -> None:
    if completed.returncode != 0:
        raise RuntimeError(f"{description} failed:\n{completed.stdout}{completed.stderr}")


def clone_pinned_upstream(destination: Path) -> None:
    require_success(
        subprocess.run(
            ["git", "clone", "--quiet", "--no-hardlinks", str(UPSTREAM), str(destination)],
            cwd=REPO,
            text=True,
            capture_output=True,
            check=False,
        ),
        "git clone pinned upstream",
    )
    require_success(
        subprocess.run(
            ["git", "checkout", "--quiet", "--detach", PIN],
            cwd=destination,
            text=True,
            capture_output=True,
            check=False,
        ),
        "git checkout pinned upstream",
    )


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(64 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def build_fresh_oracle(work: Path) -> tuple[Path, Path, Path, Path]:
    """Build the same isolated oracle executable that the public runner invokes."""
    source = work / "oracle-source"
    upstream = work / "upstream-source"
    build = work / "fresh-build"
    output = build / "output"
    shutil.copytree(ORACLE, source, ignore=shutil.ignore_patterns("bin", "obj", "__pycache__", "*.pyc"))

    package_root = Path(os.environ.get("NUGET_PACKAGES", Path.home() / ".nuget" / "packages"))
    template = package_root / "mimetypes" / "2.5.2" / "contentFiles" / "cs" / "net5.0" / "MimeTypes.Nullable.cs.pp"
    if not template.is_file():
        raise RuntimeError(f"pinned MimeTypes template is missing: {template}")
    content = template.read_text(encoding="utf-8-sig")
    if content.count("$rootnamespace$") != 1:
        raise RuntimeError("pinned MimeTypes template does not contain exactly one root namespace token")
    (source / "MimeTypes.Nullable.cs").write_text(
        content.replace("$rootnamespace$", "MediaBrowser.Model"), encoding="utf-8", newline="\n"
    )

    clone_pinned_upstream(upstream)
    build.mkdir()
    require_success(
        subprocess.run(
            [
                "dotnet",
                "build",
                str(source / PROJECT.name),
                "--configuration",
                "Release",
                "--no-incremental",
                "-p:UseSharedCompilation=false",
                "-p:Deterministic=true",
                "-p:ContinuousIntegrationBuild=true",
                f"-p:UpstreamRoot={upstream}",
                f"-p:OutputPath={output}{os.sep}",
                f"-p:CustomAfterMicrosoftCommonTargets={source / 'FreshBuild.targets'}",
                f"-p:PathMap={work}=/source",
            ],
            cwd=REPO,
            text=True,
            capture_output=True,
            check=False,
        ),
        "fresh ProbeOracle build",
    )
    oracle_dll = output / "ProbeOracle.dll"
    if not oracle_dll.is_file():
        raise RuntimeError("fresh ProbeOracle build did not produce ProbeOracle.dll")
    manifest = build / "manifest.json"
    assemblies = {path.name: sha256(path) for path in sorted(output.glob("*.dll"), key=lambda path: path.name)}
    manifest.write_text(
        json.dumps({"BuildRoot": str(build), "Assemblies": assemblies}, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return upstream, source, manifest, oracle_dll


def output_files(root: Path) -> dict[str, bytes]:
    return {path.relative_to(root).as_posix(): path.read_bytes() for path in root.rglob("*") if path.is_file()}


class ProbeOracleRegressionTests(unittest.TestCase):
    maxDiff = None

    def test_fresh_regenerations_are_byte_identical_and_preserve_missing_video_bitrate(self):
        """A fresh source-bound oracle is reproducible and retains upstream bitrate recovery."""
        with tempfile.TemporaryDirectory(dir=REPO) as directory:
            temporary_root = Path(directory)
            first = temporary_root / "first"
            second = temporary_root / "second"
            first_run = run_regeneration(first)
            second_run = run_regeneration(second)

            self.assertEqual(first_run.returncode, 0, first_run.stderr)
            self.assertEqual(second_run.returncode, 0, second_run.stderr)
            self.assertEqual(output_files(first), output_files(second))

            media_info = json.loads((first / "video_missing_video_bitrate.json").read_text(encoding="utf-8"))
            video = next(stream for stream in media_info["MediaStreams"] if stream["Type"] == "Video")
            self.assertEqual(media_info["Protocol"], "File")
            self.assertEqual(media_info["Type"], "Default")
            self.assertEqual(media_info["VideoType"], "VideoFile")
            self.assertEqual(media_info["TranscodingSubProtocol"], "http")
            self.assertEqual(video["Type"], "Video")
            self.assertEqual(video["VideoRange"], "SDR")
            self.assertEqual(video["VideoRangeType"], "SDR")
            self.assertEqual(video["AudioSpatialFormat"], "None")
            self.assertEqual(video["BitRate"], 5000000)
            self.assertEqual(media_info["Bitrate"], 5128000)

    def test_runner_rejects_dirty_same_head_upstream_before_creating_output(self):
        """A checkout at the pinned commit is rejected when its authoritative source is dirty."""
        with tempfile.TemporaryDirectory(dir=REPO) as directory:
            temporary_root = Path(directory)
            copied_upstream = temporary_root / "upstream"
            clone_pinned_upstream(copied_upstream)
            drifted_source = copied_upstream / "Directory.Packages.props"
            drifted_source.write_bytes(drifted_source.read_bytes() + b"\n<!-- test source drift -->\n")
            output = temporary_root / "generated"
            completed = run_regeneration(output, copied_upstream)
            output_exists = output.exists()

        self.assertNotEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("upstream worktree is dirty", completed.stderr)
        self.assertFalse(output_exists, "runner generated output from a dirty same-HEAD upstream checkout")

    def test_compiled_oracle_rejects_manifest_assembly_mismatch_before_creating_output(self):
        """The fresh executable refuses an assembly manifest that cannot attest its loaded bytes."""
        with tempfile.TemporaryDirectory(dir=REPO) as directory:
            temporary_root = Path(directory)
            upstream, source, manifest, oracle_dll = build_fresh_oracle(temporary_root)
            manifest_document = json.loads(manifest.read_text(encoding="utf-8"))
            assembly_name = next(iter(manifest_document["Assemblies"]))
            manifest_document["Assemblies"][assembly_name] = "0" * 64
            manifest.write_text(json.dumps(manifest_document, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            output = temporary_root / "generated"
            completed = subprocess.run(
                [
                    "dotnet",
                    str(oracle_dll),
                    "--fixture-root",
                    str(FIXTURES),
                    "--output-root",
                    str(output),
                    "--upstream",
                    str(upstream),
                    "--oracle-source-root",
                    str(source),
                    "--build-manifest",
                    str(manifest),
                    "--command",
                    "probeoracle manifest mismatch regression test",
                ],
                cwd=REPO,
                text=True,
                capture_output=True,
                check=False,
            )
            output_exists = output.exists()

        self.assertNotEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("fresh-build assembly hash mismatch for", completed.stderr)
        self.assertFalse(output_exists, "compiled oracle wrote output before rejecting a bad assembly manifest")


if __name__ == "__main__":
    unittest.main()
