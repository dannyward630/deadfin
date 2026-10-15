#!/usr/bin/env python3
"""Write and validate reproducible provenance for WP-P0-06 golden corpora."""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path

PIN = "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb"
ROOT = Path(__file__).resolve().parent
REPO = ROOT.parent.parent


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def relative_hashes(root: Path, paths: list[Path]) -> dict[str, str]:
    return {str(path.relative_to(root)): sha256(path) for path in sorted(paths)}


def source_hashes(upstream: Path, relative: list[str]) -> dict[str, str]:
    paths = [upstream / item for item in relative]
    missing = [str(path) for path in paths if not path.is_file()]
    if missing:
        raise SystemExit("goldenrec: missing upstream input(s): " + ", ".join(missing))
    return {item: sha256(path) for item, path in zip(relative, paths, strict=True)}


def read_head(upstream: Path) -> str:
    head = (upstream / ".git" / "HEAD").read_text(encoding="utf-8").strip()
    if head.startswith("ref: "):
        head = (upstream / ".git" / head[5:]).read_text(encoding="utf-8").strip()
    return head


def corpus_specs(root: Path, corpus: str = "all") -> list[dict[str, object]]:
    playback = root / "internal/playback/testdata"
    encoding = root / "internal/encoding/testdata"
    fixtures = sorted(playback.glob("DeviceProfile-*.json")) + sorted(playback.glob("MediaSourceInfo-*.json"))
    specs = [
        {
            "name": "streambuilder",
            "base": playback,
            "golden": playback / "golden",
            "assets": fixtures + [playback / "streambuilder-assertions.yaml", playback / "golden/streambuilder.json"],
            "sources": [
                "tests/Jellyfin.Model.Tests/Dlna/StreamBuilderTests.cs",
                *[f"tests/Jellyfin.Model.Tests/Test Data/{path.name}" for path in fixtures],
            ],
        },
        {
            "name": "encoding",
            "base": encoding,
            "golden": encoding / "golden",
            "assets": [encoding / "golden/encoding.json"],
            "sources": [
                "Jellyfin.Api/Controllers/DynamicHlsController.cs",
                "Jellyfin.Api/Controllers/VideosController.cs",
                "Jellyfin.Api/Helpers/StreamingHelpers.cs",
                "MediaBrowser.Controller/MediaEncoding/EncodingHelper.cs",
                "tests/Jellyfin.Model.Tests/Test Data/MediaSourceInfo-mp4-h264-ac3-aac-srt-2600k.json",
                "tests/Jellyfin.Model.Tests/Test Data/MediaSourceInfo-mp4-hevc-ac3-aac-srt-15200k.json",
                "tests/Jellyfin.Model.Tests/Test Data/MediaSourceInfo-mkv-av1-aac-srt-2600k.json",
            ],
        },
    ]
    return specs if corpus == "all" else [spec for spec in specs if spec["name"] == corpus]


def recorder_sources() -> dict[str, str]:
    return relative_hashes(REPO, [ROOT / "GoldenRec.csproj", ROOT / "Program.cs", ROOT / "record.sh", ROOT / "provenance.py"])


def recorder_command(corpus: str) -> str:
    return f"tools/goldenrec/record.sh {corpus} --output-root deadfin"


def compiled_assembly_hashes(upstream: Path) -> dict[str, str]:
    assemblies = [
        upstream / "MediaBrowser.Model/bin/Release/net10.0/MediaBrowser.Model.dll",
        upstream / "MediaBrowser.Controller/bin/Release/net10.0/MediaBrowser.Controller.dll",
    ]
    return {str(path.relative_to(upstream)): sha256(path) for path in assemblies if path.is_file()}


def write(root: Path, upstream: Path, corpus: str = "all") -> None:
    if read_head(upstream) != PIN:
        raise SystemExit(f"goldenrec: upstream is not pin {PIN}")
    for spec in corpus_specs(root, corpus):
        assets = spec["assets"]
        missing = [str(path) for path in assets if not path.is_file()]
        if missing:
            raise SystemExit("goldenrec: recorder did not create: " + ", ".join(missing))
        golden = spec["golden"]
        golden.mkdir(parents=True, exist_ok=True)
        manifest = relative_hashes(spec["base"], assets)
        upstream_provenance = {
            "repository": "jellyfin/jellyfin",
            "commit": PIN,
            "treeHead": read_head(upstream),
            "inputs": source_hashes(upstream, spec["sources"]),
        }
        assemblies = compiled_assembly_hashes(upstream)
        if assemblies:
            upstream_provenance["assemblies"] = assemblies
        provenance = {
            "format": "deadfin.golden.provenance/v1",
            "corpus": spec["name"],
            "upstream": upstream_provenance,
            "recorder": {
                "sources": recorder_sources(),
                "command": recorder_command(spec["name"]),
            },
            "assets": manifest,
        }
        (golden / "provenance.json").write_text(json.dumps(provenance, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        (golden / "SHA256SUMS").write_text("".join(f"{digest}  {path}\n" for path, digest in manifest.items()), encoding="utf-8")


def verify(root: Path, upstream: Path, corpus: str = "all") -> None:
    if read_head(upstream) != PIN:
        raise SystemExit(f"goldenrec: upstream is not pin {PIN}")
    for spec in corpus_specs(root, corpus):
        golden = spec["golden"]
        provenance_path = golden / "provenance.json"
        manifest_path = golden / "SHA256SUMS"
        if not provenance_path.is_file() or not manifest_path.is_file():
            raise SystemExit(f"goldenrec: missing provenance for {spec['name']}")
        provenance = json.loads(provenance_path.read_text(encoding="utf-8"))
        if provenance.get("format") != "deadfin.golden.provenance/v1" or provenance.get("corpus") != spec["name"]:
            raise SystemExit(f"goldenrec: invalid provenance envelope for {spec['name']}")
        upstream_provenance = provenance.get("upstream", {})
        if upstream_provenance.get("commit") != PIN or upstream_provenance.get("treeHead") != PIN:
            raise SystemExit(f"goldenrec: wrong upstream pin in {spec['name']} provenance")
        if upstream_provenance.get("inputs") != source_hashes(upstream, spec["sources"]):
            raise SystemExit(f"goldenrec: upstream input hash mismatch for {spec['name']}")
        recorded_assemblies = upstream_provenance.get("assemblies")
        if recorded_assemblies is not None:
            if not isinstance(recorded_assemblies, dict):
                raise SystemExit(f"goldenrec: compiled assembly hash mismatch for {spec['name']}")
            for identity, digest in recorded_assemblies.items():
                assembly = upstream / identity
                if assembly.is_file() and sha256(assembly) != digest:
                    raise SystemExit(f"goldenrec: compiled assembly hash mismatch for {spec['name']}")
        recorder = provenance.get("recorder", {})
        if recorder.get("sources") != recorder_sources():
            raise SystemExit(f"goldenrec: recorder source hash mismatch for {spec['name']}")
        if recorder.get("command") != recorder_command(spec["name"]):
            raise SystemExit(f"goldenrec: recorder command hash mismatch for {spec['name']}")
        recorded = provenance.get("assets")
        actual = relative_hashes(spec["base"], spec["assets"])
        if recorded != actual:
            raise SystemExit(f"goldenrec: corpus hash mismatch for {spec['name']}")
        expected_manifest = "".join(f"{digest}  {path}\n" for path, digest in recorded.items())
        if manifest_path.read_text(encoding="utf-8") != expected_manifest:
            raise SystemExit(f"goldenrec: SHA256SUMS mismatch for {spec['name']}")


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("mode", choices=("write", "verify"))
parser.add_argument("--corpus", choices=("streambuilder", "encoding", "all"), default="all")
parser.add_argument("--root", type=Path, required=True)
parser.add_argument("--upstream", type=Path, required=True)
arguments = parser.parse_args()
if arguments.mode == "write":
    write(arguments.root.resolve(), arguments.upstream.resolve(), arguments.corpus)
else:
    verify(arguments.root.resolve(), arguments.upstream.resolve(), arguments.corpus)
