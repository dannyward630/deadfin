#!/usr/bin/env python3
"""Regenerate source-bound ProbeResultNormalizer MediaInfo goldens."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import tempfile
from pathlib import Path

PIN = "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb"
ROOT = Path(__file__).resolve().parents[3]
TOOL = ROOT / "tools" / "compat" / "probeoracle"
PROJECT_NAME = "ProbeOracle.csproj"
EXPECTED_OUTPUT = ROOT / "deadfin" / "internal" / "probe" / "testdata" / "upstream-b04614e" / "expected-media-info"
FIXTURE_ROOT = ROOT / "deadfin" / "internal" / "probe" / "testdata" / "upstream-b04614e"


def run(arguments: list[str], *, cwd: Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(arguments, cwd=cwd, text=True, capture_output=True, check=False)


def require_process(completed: subprocess.CompletedProcess[str], description: str) -> None:
    if completed.returncode != 0:
        raise RuntimeError(f"{description} failed:\n{completed.stdout}{completed.stderr}")


def require_pinned_clean_upstream(upstream: Path) -> None:
    if not upstream.is_dir():
        raise RuntimeError(f"upstream directory is missing: {upstream}")
    head = run(["git", "rev-parse", "HEAD"], cwd=upstream)
    require_process(head, "git rev-parse HEAD")
    if head.stdout.strip() != PIN:
        raise RuntimeError(f"upstream HEAD is {head.stdout.strip()!r}, want {PIN}")
    status = run(["git", "status", "--porcelain", "--untracked-files=all"], cwd=upstream)
    require_process(status, "git status")
    if status.stdout:
        raise RuntimeError("upstream worktree is dirty")


def assert_no_build_artifacts(root: Path) -> None:
    stale = sorted(path for path in root.rglob("*") if path.name in {"bin", "obj"} and path.is_dir())
    if stale:
        raise RuntimeError("fresh source copy contains stale bin/obj artifacts: " + ", ".join(str(path) for path in stale))


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(64 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def copy_oracle_source(destination: Path) -> None:
    shutil.copytree(TOOL, destination, ignore=shutil.ignore_patterns("bin", "obj", "__pycache__", "*.pyc"))
    assert_no_build_artifacts(destination)

def materialize_mime_types_source(destination: Path) -> None:
    package_root = Path(os.environ.get("NUGET_PACKAGES", Path.home() / ".nuget" / "packages"))
    template = package_root / "mimetypes" / "2.5.2" / "contentFiles" / "cs" / "net5.0" / "MimeTypes.Nullable.cs.pp"
    if not template.is_file():
        raise RuntimeError(f"pinned MimeTypes template is missing: {template}")
    content = template.read_text(encoding="utf-8-sig")
    if content.count("$rootnamespace$") != 1:
        raise RuntimeError("pinned MimeTypes template does not contain exactly one root namespace token")
    (destination / "MimeTypes.Nullable.cs").write_text(content.replace("$rootnamespace$", "MediaBrowser.Model"), encoding="utf-8", newline="\n")


def clone_pinned_upstream(source: Path, destination: Path) -> None:
    completed = run(["git", "clone", "--quiet", "--no-hardlinks", str(source), str(destination)], cwd=ROOT)
    require_process(completed, "git clone pinned upstream")
    completed = run(["git", "checkout", "--quiet", "--detach", PIN], cwd=destination)
    require_process(completed, "git checkout pinned upstream")
    require_pinned_clean_upstream(destination)
    assert_no_build_artifacts(destination)


def build_fresh_oracle(work: Path, upstream: Path) -> tuple[Path, Path, Path]:
    source_root = work / "oracle-source"
    copied_upstream = work / "upstream-source"
    build_root = work / "fresh-build"
    copy_oracle_source(source_root)
    materialize_mime_types_source(source_root)
    clone_pinned_upstream(upstream, copied_upstream)
    build_root.mkdir()
    output_root = build_root / "output"
    completed = run(
        [
            "dotnet", "build", str(source_root / PROJECT_NAME), "--configuration", "Release", "--no-incremental",
            "-p:UseSharedCompilation=false", "-p:Deterministic=true", "-p:ContinuousIntegrationBuild=true",
            f"-p:UpstreamRoot={copied_upstream}", f"-p:OutputPath={output_root}{os.sep}",
            f"-p:CustomAfterMicrosoftCommonTargets={source_root / 'FreshBuild.targets'}",
            f"-p:PathMap={work}=/source", 
        ],
        cwd=ROOT,
    )
    require_process(completed, "fresh ProbeOracle build")
    oracle_dll = output_root / "ProbeOracle.dll"
    if not oracle_dll.is_file():
        raise RuntimeError(f"fresh build did not produce {oracle_dll}")
    assemblies = {path.name: sha256(path) for path in sorted(output_root.glob("*.dll"), key=lambda path: path.name)}
    if "ProbeOracle.dll" not in assemblies:
        raise RuntimeError("fresh build assembly manifest lacks ProbeOracle.dll")
    manifest = build_root / "manifest.json"
    manifest.write_text(json.dumps({"BuildRoot": str(build_root), "Assemblies": assemblies}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return copied_upstream, source_root, manifest


def regenerate(output_root: Path, upstream: Path) -> None:
    require_pinned_clean_upstream(upstream)
    with tempfile.TemporaryDirectory(prefix="probeoracle-", dir=ROOT) as directory:
        work = Path(directory)
        copied_upstream, oracle_source, manifest = build_fresh_oracle(work, upstream)
        staging = work / "expected-media-info"
        command = "python3 tools/compat/probeoracle/regenerate.py --output-root deadfin/internal/probe/testdata/upstream-b04614e/expected-media-info"
        completed = run(
            [
                "dotnet", str(work / "fresh-build" / "output" / "ProbeOracle.dll"),
                "--fixture-root", str(FIXTURE_ROOT), "--output-root", str(staging),
                "--upstream", str(copied_upstream), "--oracle-source-root", str(oracle_source),
                "--build-manifest", str(manifest), "--command", command,
            ],
            cwd=ROOT,
        )
        require_process(completed, "source-bound ProbeOracle invocation")
        required = {"provenance.json", *(json.loads((FIXTURE_ROOT / "inventory.json").read_text(encoding="utf-8"))["fixtures"])}
        actual = {path.name for path in staging.iterdir() if path.is_file()}
        if actual != required:
            raise RuntimeError(f"oracle output inventory is {sorted(actual)}, want {sorted(required)}")
        output_root.parent.mkdir(parents=True, exist_ok=True)
        if output_root.exists():
            shutil.rmtree(output_root)
        shutil.copytree(staging, output_root)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-root", type=Path, default=EXPECTED_OUTPUT)
    parser.add_argument("--upstream", type=Path, default=ROOT / "upstream" / "jellyfin")
    arguments = parser.parse_args()
    regenerate(arguments.output_root.resolve(), arguments.upstream.resolve())


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        raise SystemExit(f"probeoracle: {error}")
