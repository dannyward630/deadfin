#!/usr/bin/env python3
"""Capture the deterministic P3-01 stock-Jellyfin image corpus.

The only image bytes accepted as outputs are successful raw curl responses from the
loopback, digest-pinned stock container.  Sources are deterministic local sidecars
made by fixtures/gen.sh; no provider or live-library image is ever contacted.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import struct
import subprocess
import sys
import tempfile
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.parse import urlencode, urlparse

ROOT = Path(__file__).resolve().parents[2]
GOLDENREC = ROOT / "tools" / "goldenrec"
COMPOSE = GOLDENREC / "compose.yaml"
FIXTURE_GENERATOR = GOLDENREC / "fixtures" / "gen.sh"
SEED = GOLDENREC / "seed.sh"
FIXTURES = GOLDENREC / "fixtures" / "out"
CORPUS = ROOT / "deadfin" / "internal" / "images" / "testdata" / "stock-corpus"
MANIFEST = CORPUS / "manifest.json"
EXPECTED_DIGEST = "sha256:2d1a69be5aa2695d0c2df90ebeb76bbcc6efc0a798cb09bfa65906680e1d785a"
UPSTREAM_COMMIT = "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb"
FIXED_SOURCE_TIME = "2025-01-02T03:04:05Z"
SCRIPT_VERSION = "goldenrec-capture-images/1"
CLIENT_AUTHORIZATION = 'MediaBrowser Client="Goldenrec", Device="Goldenrec Image Corpus", DeviceId="goldenrec-stock-corpus-v1", Version="1.0.0"'


class CaptureError(RuntimeError):
    pass


@dataclass(frozen=True)
class SourceSpec:
    input_name: str
    fixture_relative: str
    item_type: str
    media_path: str


SOURCES = {
    "landscape.jpg": SourceSpec(
        "landscape.jpg",
        "movies/Standalone Movie (2023)/Standalone Movie (2023).jpg",
        "Movie",
        "/media/movies/Standalone Movie (2023)/Standalone Movie (2023).mkv",
    ),
    "alpha.png": SourceSpec(
        "alpha.png",
        "movies/HLG Sample (2024)/HLG Sample (2024).png",
        "Movie",
        "/media/movies/HLG Sample (2024)/HLG Sample (2024).mkv",
    ),
    "texture.webp": SourceSpec(
        "texture.webp",
        "movies/Subtitle Sample (2024)/Subtitle Sample (2024).webp",
        "Movie",
        "/media/movies/Subtitle Sample (2024)/Subtitle Sample (2024).mkv",
    ),
    "orientation-1.jpg": SourceSpec("orientation-1.jpg", "photos/orientation-1.jpg", "Photo", "/media/photos/orientation-1.jpg"),
    "orientation-2.jpg": SourceSpec("orientation-2.jpg", "photos/orientation-2.jpg", "Photo", "/media/photos/orientation-2.jpg"),
    "orientation-3.jpg": SourceSpec("orientation-3.jpg", "photos/orientation-3.jpg", "Photo", "/media/photos/orientation-3.jpg"),
    "orientation-4.jpg": SourceSpec("orientation-4.jpg", "photos/orientation-4.jpg", "Photo", "/media/photos/orientation-4.jpg"),
    "orientation-5.jpg": SourceSpec("orientation-5.jpg", "photos/orientation-5.jpg", "Photo", "/media/photos/orientation-5.jpg"),
    "orientation-6.jpg": SourceSpec("orientation-6.jpg", "photos/orientation-6.jpg", "Photo", "/media/photos/orientation-6.jpg"),
    "orientation-7.jpg": SourceSpec("orientation-7.jpg", "photos/orientation-7.jpg", "Photo", "/media/photos/orientation-7.jpg"),
    "orientation-8.jpg": SourceSpec("orientation-8.jpg", "photos/orientation-8.jpg", "Photo", "/media/photos/orientation-8.jpg"),
}


def case(identifier: str, source: str, output_format: str, quality: int, *, resize: dict[str, int] | None = None, auto_orient: bool = False) -> dict[str, Any]:
    return {"id": identifier, "source": source, "outputFormat": output_format, "quality": quality, "resize": resize or {}, "autoOrient": auto_orient}


CASES = [
    case("jpeg-landscape-width-320", "landscape.jpg", "jpeg", 90, resize={"width": 320}),
    case("jpeg-landscape-height-240", "landscape.jpg", "jpeg", 90, resize={"height": 240}),
    case("jpeg-landscape-box-480x360", "landscape.jpg", "jpeg", 90, resize={"width": 480, "height": 360}),
    case("jpeg-landscape-max-width-640", "landscape.jpg", "jpeg", 90, resize={"maxWidth": 640}),
    case("jpeg-landscape-max-height-360", "landscape.jpg", "jpeg", 90, resize={"maxHeight": 360}),
    case("jpeg-landscape-max-box-640x360", "landscape.jpg", "jpeg", 90, resize={"maxWidth": 640, "maxHeight": 360}),
    case("jpeg-landscape-fill-square-320", "landscape.jpg", "jpeg", 90, resize={"fillWidth": 320, "fillHeight": 320}),
    case("jpeg-landscape-fill-wide-640x360", "landscape.jpg", "jpeg", 90, resize={"fillWidth": 640, "fillHeight": 360}),
    case("jpeg-landscape-quality-30", "landscape.jpg", "jpeg", 30),
    case("jpeg-landscape-noop", "landscape.jpg", "jpeg", 90),
    case("png-alpha-noop", "alpha.png", "png", 90),
    case("png-alpha-width-320", "alpha.png", "png", 90, resize={"width": 320}),
    case("png-alpha-height-240", "alpha.png", "png", 90, resize={"height": 240}),
    case("png-alpha-box-480x360", "alpha.png", "png", 90, resize={"width": 480, "height": 360}),
    case("png-alpha-max-width-640", "alpha.png", "png", 90, resize={"maxWidth": 640}),
    case("png-alpha-max-height-360", "alpha.png", "png", 90, resize={"maxHeight": 360}),
    case("png-alpha-max-box-640x360", "alpha.png", "png", 90, resize={"maxWidth": 640, "maxHeight": 360}),
    case("png-alpha-fill-square-320", "alpha.png", "png", 90, resize={"fillWidth": 320, "fillHeight": 320}),
    case("png-alpha-fill-wide-640x360", "alpha.png", "png", 90, resize={"fillWidth": 640, "fillHeight": 360}),
    case("png-alpha-quality-0", "alpha.png", "png", 0),
    case("png-alpha-quality-100", "alpha.png", "png", 100),
    case("png-webp-noop", "texture.webp", "png", 90),
    case("png-webp-width-320", "texture.webp", "png", 90, resize={"width": 320}),
    case("png-webp-fill-square-320", "texture.webp", "png", 90, resize={"fillWidth": 320, "fillHeight": 320}),
    case("png-webp-max-width-640", "texture.webp", "png", 90, resize={"maxWidth": 640}),
    case("png-webp-quality-50", "texture.webp", "png", 50),
    case("webp-landscape-noop", "landscape.jpg", "webp", 90),
    case("webp-landscape-width-320", "landscape.jpg", "webp", 90, resize={"width": 320}),
    case("webp-landscape-height-240", "landscape.jpg", "webp", 90, resize={"height": 240}),
    case("webp-landscape-box-480x360", "landscape.jpg", "webp", 90, resize={"width": 480, "height": 360}),
    case("webp-landscape-max-width-640", "landscape.jpg", "webp", 90, resize={"maxWidth": 640}),
    case("webp-landscape-max-height-360", "landscape.jpg", "webp", 90, resize={"maxHeight": 360}),
    case("webp-landscape-max-box-640x360", "landscape.jpg", "webp", 90, resize={"maxWidth": 640, "maxHeight": 360}),
    case("webp-landscape-fill-square-320", "landscape.jpg", "webp", 90, resize={"fillWidth": 320, "fillHeight": 320}),
    case("webp-landscape-fill-wide-640x360", "landscape.jpg", "webp", 90, resize={"fillWidth": 640, "fillHeight": 360}),
    case("webp-landscape-quality-0", "landscape.jpg", "webp", 0),
    case("webp-landscape-quality-100", "landscape.jpg", "webp", 100),
    case("webp-alpha-noop", "alpha.png", "webp", 90),
    case("webp-alpha-width-320", "alpha.png", "webp", 90, resize={"width": 320}),
    case("webp-alpha-fill-square-320", "alpha.png", "webp", 90, resize={"fillWidth": 320, "fillHeight": 320}),
    case("webp-webp-noop", "texture.webp", "webp", 90),
    case("webp-webp-width-320", "texture.webp", "webp", 90, resize={"width": 320}),
    *[case(f"jpeg-orientation-{number}", f"orientation-{number}.jpg", "jpeg", 90, auto_orient=True) for number in range(1, 9)],
]


def run(args: list[str], *, cwd: Path = ROOT, capture: bool = False) -> str:
    result = subprocess.run(args, cwd=cwd, text=True, stdout=subprocess.PIPE if capture else None, stderr=subprocess.PIPE if capture else None)
    if result.returncode:
        detail = (result.stderr or result.stdout or "command failed").strip()
        raise CaptureError(f"command failed ({args[0]}): {detail}")
    return result.stdout.strip() if capture else ""


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def require_loopback(base: str) -> str:
    parsed = urlparse(base)
    if parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "::1", "localhost"} or parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
        raise CaptureError("JELLYFIN_BASE_URL must be a bare loopback http URL; refusing a non-local capture target")
    return base.rstrip("/")


def validate_compose() -> None:
    text = COMPOSE.read_text(encoding="utf-8")
    required = f"jellyfin/jellyfin@{EXPECTED_DIGEST}"
    if required not in text:
        raise CaptureError(f"compose must pin exactly {required}")


def compose_image_present() -> None:
    repo_digests = run(["docker", "image", "inspect", "--format", "{{json .RepoDigests}}", f"jellyfin/jellyfin@{EXPECTED_DIGEST}"], capture=True)
    if EXPECTED_DIGEST not in repo_digests:
        raise CaptureError("the running image is not the required pinned Jellyfin OCI digest")


def prepare(base: str) -> None:
    validate_compose()
    run([str(FIXTURE_GENERATOR)])
    run(["docker", "compose", "-f", str(COMPOSE), "up", "-d"])
    compose_image_present()
    environment = os.environ.copy()
    environment["JELLYFIN_BASE_URL"] = base
    result = subprocess.run([str(SEED)], cwd=ROOT, env=environment)
    if result.returncode:
        raise CaptureError("local stock seed/scan failed; no corpus was written")


def curl_json(base: str, token: str, path: str) -> Any:
    with tempfile.TemporaryDirectory(prefix="goldenrec-corpus-json-") as temporary:
        body = Path(temporary) / "body.json"
        command = ["curl", "--noproxy", "*", "-sS", "--connect-timeout", "5", "--max-time", "30", "--fail-with-body", "-o", str(body), "-H", "Accept: application/json", "-H", f"Authorization: {CLIENT_AUTHORIZATION}, Token=\"{token}\"", base + path]
        result = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if result.returncode:
            raise CaptureError(f"stock request failed for {path}: {result.stderr.strip() or 'curl failure'}")
        try:
            return json.loads(body.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            raise CaptureError(f"stock returned invalid JSON for {path}: {error}") from error


def authenticate(base: str) -> str:
    username = os.environ.get("JELLYFIN_ADMIN", "GoldenrecAdmin")
    password = os.environ.get("JELLYFIN_PASSWORD", "goldenrec-change-me")
    payload = json.dumps({"Username": username, "Pw": password}, separators=(",", ":"))
    with tempfile.TemporaryDirectory(prefix="goldenrec-corpus-auth-") as temporary:
        body = Path(temporary) / "auth.json"
        command = ["curl", "--noproxy", "*", "-sS", "--connect-timeout", "5", "--max-time", "30", "--fail-with-body", "-o", str(body), "-X", "POST", "-H", "Accept: application/json", "-H", "Content-Type: application/json", "-H", f"Authorization: {CLIENT_AUTHORIZATION}", "--data", payload, base + "/Users/AuthenticateByName"]
        for attempt in range(30):
            result = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            if not result.returncode:
                try:
                    token = json.loads(body.read_text(encoding="utf-8")).get("AccessToken")
                except (OSError, json.JSONDecodeError) as error:
                    raise CaptureError(f"stock authentication returned invalid JSON: {error}") from error
                if isinstance(token, str) and token:
                    return token
            if attempt < 29:
                time.sleep(1)
    raise CaptureError("stock authentication did not become available after 30 seconds")


def resolve_items(base: str, token: str) -> dict[tuple[str, str], str]:
    payload = curl_json(base, token, "/Items?Recursive=true&Fields=Path&EnableTotalRecordCount=true&StartIndex=0&Limit=200")
    items = payload.get("Items") if isinstance(payload, dict) else None
    if not isinstance(items, list):
        raise CaptureError("stock item query did not return Items")
    resolved: dict[tuple[str, str], str] = {}
    for item in items:
        if not isinstance(item, dict):
            continue
        item_type, path, item_id = item.get("Type"), item.get("Path"), item.get("Id")
        if isinstance(item_type, str) and isinstance(path, str) and isinstance(item_id, str):
            resolved[(item_type, path)] = item_id
    for source in SOURCES.values():
        if (source.item_type, source.media_path) not in resolved:
            raise CaptureError(f"stock scan lacks required local source item {source.item_type}:{source.media_path}")
    return resolved


def image_info(data: bytes) -> tuple[str, int, int, int]:
    if data.startswith(b"\x89PNG\r\n\x1a\n") and len(data) >= 24 and data[12:16] == b"IHDR":
        width, height = struct.unpack(">II", data[16:24])
        return "image/png", width, height, 1
    if data.startswith(b"\xff\xd8"):
        offset = 2
        orientation = 1
        while offset + 4 <= len(data):
            if data[offset] != 0xFF:
                offset += 1
                continue
            marker = data[offset + 1]
            offset += 2
            if marker in {0xD8, 0xD9}:
                continue
            length = struct.unpack(">H", data[offset:offset + 2])[0]
            if length < 2 or offset + length > len(data):
                break
            segment = data[offset + 2:offset + length]
            if marker == 0xE1 and segment.startswith(b"Exif\x00\x00MM\x00*\x00\x00\x00\x08") and len(segment) >= 32:
                count = struct.unpack(">H", segment[14:16])[0]
                for index in range(count):
                    entry = segment[16 + index * 12:28 + index * 12]
                    if len(entry) == 12 and entry[:2] == b"\x01\x12" and entry[2:4] == b"\x00\x03":
                        orientation = struct.unpack(">H", entry[8:10])[0]
            if marker in {0xC0, 0xC1, 0xC2, 0xC3, 0xC5, 0xC6, 0xC7, 0xC9, 0xCA, 0xCB, 0xCD, 0xCE, 0xCF} and len(segment) >= 5:
                height, width = struct.unpack(">HH", segment[1:5])
                return "image/jpeg", width, height, orientation
            offset += length
        raise CaptureError("JPEG response lacks a valid dimensions marker")
    if data.startswith(b"RIFF") and data[8:12] == b"WEBP":
        offset = 12
        while offset + 8 <= len(data):
            kind = data[offset:offset + 4]
            length = struct.unpack("<I", data[offset + 4:offset + 8])[0]
            body = data[offset + 8:offset + 8 + length]
            if len(body) != length:
                break
            if kind == b"VP8X" and len(body) >= 10:
                width = 1 + int.from_bytes(body[4:7], "little")
                height = 1 + int.from_bytes(body[7:10], "little")
                return "image/webp", width, height, 1
            if kind == b"VP8 " and len(body) >= 10 and body[3:6] == b"\x9d\x01\x2a":
                width, height = struct.unpack("<HH", body[6:10])
                return "image/webp", width & 0x3FFF, height & 0x3FFF, 1
            if kind == b"VP8L" and len(body) >= 5 and body[0] == 0x2F:
                bits = int.from_bytes(body[1:5], "little")
                return "image/webp", (bits & 0x3FFF) + 1, ((bits >> 14) & 0x3FFF) + 1, 1
            offset += 8 + length + (length & 1)
        raise CaptureError("WebP response lacks a valid dimensions chunk")
    raise CaptureError("response is not PNG, JPEG, or WebP")


def copy_sources(target: Path) -> dict[str, str]:
    inputs = target / "inputs"
    inputs.mkdir(parents=True, exist_ok=False)
    hashes: dict[str, str] = {}
    timestamp = datetime.fromisoformat(FIXED_SOURCE_TIME.replace("Z", "+00:00")).timestamp()
    for source in SOURCES.values():
        destination = inputs / source.input_name
        original = FIXTURES / source.fixture_relative
        if not original.is_file():
            raise CaptureError(f"fixture generator did not produce required source {source.fixture_relative}")
        shutil.copyfile(original, destination)
        os.utime(destination, (timestamp, timestamp))
        hashes[source.input_name] = sha256(destination)
    return hashes


def output_extension(output_format: str) -> str:
    return {"jpeg": "jpg", "png": "png", "webp": "webp"}[output_format]


def tool_version() -> str:
    curl = run(["curl", "--version"], capture=True).splitlines()[0]
    return f"{SCRIPT_VERSION}; python={sys.version.split()[0]}; {curl}"


def capture_case(base: str, token: str, item_id: str, source: SourceSpec, definition: dict[str, Any], template_case: dict[str, Any], stock_dir: Path, captured_at: str, tool: str) -> dict[str, Any]:
    request = template_case.get("request")
    source_field = template_case.get("source")
    stock_field = template_case.get("stock")
    if not isinstance(request, dict) or not isinstance(source_field, dict) or not isinstance(stock_field, dict):
        raise CaptureError(f"manifest case {definition['id']} lacks required source/request/stock objects")
    expected_path = f"stock/{definition['id']}.{output_extension(definition['outputFormat'])}"
    if (template_case.get("id") != definition["id"] or source_field.get("path") != f"inputs/{source.input_name}" or stock_field.get("path") != expected_path):
        raise CaptureError(f"manifest case does not preserve the agreed ID/source/stock path: {definition['id']}")
    for key in ("sourceModifiedUTC", "outputFormat", "quality", "resize", "autoOrient"):
        if request.get(key) != (FIXED_SOURCE_TIME if key == "sourceModifiedUTC" else definition[key]):
            raise CaptureError(f"manifest request differs from agreed corpus definition: {definition['id']}.{key}")
    if request.get("itemKey") != source.input_name.rsplit(".", 1)[0] or request.get("imageType") != "Primary":
        raise CaptureError(f"manifest item route differs from agreed corpus definition: {definition['id']}")
    query = dict(definition["resize"])
    query.update({"quality": definition["quality"], "format": definition["outputFormat"]})
    path = f"/Items/{item_id}/Images/Primary?{urlencode(query)}"
    output = stock_dir / expected_path.removeprefix("stock/")
    headers = stock_dir / f".{definition['id']}.headers"
    command = ["curl", "--noproxy", "*", "-sS", "--connect-timeout", "5", "--max-time", "30", "--fail-with-body", "-D", str(headers), "-o", str(output), "-H", "Accept: image/*", "-H", f"Authorization: {CLIENT_AUTHORIZATION}, Token=\"{token}\"", base + path]
    result = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode:
        raise CaptureError(f"stock image response failed for {definition['id']}: {result.stderr.strip() or 'curl failure'}")
    header_lines = headers.read_text(encoding="iso-8859-1").splitlines()
    headers.unlink()
    status = next((line for line in reversed(header_lines) if line.startswith("HTTP/")), "")
    if " 200 " not in status:
        raise CaptureError(f"stock image response for {definition['id']} was not HTTP 200")
    header_map: dict[str, str] = {}
    for line in header_lines:
        if ":" in line:
            key, value = line.split(":", 1)
            header_map[key.lower()] = value.strip()
    payload = output.read_bytes()
    mime, width, height, orientation = image_info(payload)
    expected_mime = {"jpeg": "image/jpeg", "png": "image/png", "webp": "image/webp"}[definition["outputFormat"]]
    content_type = header_map.get("content-type", "").split(";", 1)[0].strip().lower()
    if mime != expected_mime or content_type != expected_mime:
        raise CaptureError(f"stock image response for {definition['id']} MIME/signature mismatch: header={content_type!r}, signature={mime!r}, expected={expected_mime!r}")
    if width <= 0 or height <= 0:
        raise CaptureError(f"stock image response for {definition['id']} has invalid dimensions {width}x{height}")
    captured = json.loads(json.dumps(template_case))
    captured["categories"] = ["stock-http", "local-fixture", definition["outputFormat"]]
    captured["source"]["sha256"] = sha256(stock_dir.parent / "inputs" / source.input_name)
    captured["stock"].update({"sha256": sha256(output), "ociDigest": EXPECTED_DIGEST, "upstreamCommit": UPSTREAM_COMMIT, "capturedAt": captured_at, "toolVersion": tool})
    captured["request"]["capture"] = {"transport": "curl --noproxy *", "method": "GET", "url": base + path, "path": path, "itemIdentity": f"{source.item_type}:{source.media_path}", "imageType": "Primary", "imageIndex": None, "httpStatus": 200, "contentType": content_type}
    captured["expected"] = {"mimeType": mime, "width": width, "height": height, "orientation": orientation}
    return captured


def validate_cases() -> None:
    if len(CASES) != 50 or len({entry["id"] for entry in CASES}) != 50:
        raise CaptureError("internal corpus definition must contain exactly 50 unique cases")
    formats = {"jpeg": 18, "png": 16, "webp": 16}
    actual = {key: sum(case["outputFormat"] == key for case in CASES) for key in formats}
    if actual != formats:
        raise CaptureError(f"internal corpus format counts mismatch: {actual}")
    for entry in CASES:
        if entry["source"] not in SOURCES:
            raise CaptureError(f"internal corpus case has unknown source: {entry['id']}")


def load_template() -> dict[str, Any]:
    if not MANIFEST.is_file():
        raise CaptureError(f"tester corpus template is missing: {MANIFEST}")
    try:
        template = json.loads(MANIFEST.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        raise CaptureError(f"tester corpus template is invalid JSON: {error}") from error
    cases = template.get("cases") if isinstance(template, dict) else None
    if template.get("schemaVersion") != 1 or not isinstance(cases, list) or len(cases) != 50:
        raise CaptureError("tester corpus template must be a v1 manifest with exactly 50 cases")
    if [entry.get("id") for entry in cases if isinstance(entry, dict)] != [entry["id"] for entry in CASES]:
        raise CaptureError("tester corpus template case IDs/order differs from the agreed 50-case matrix")
    if any(not isinstance(entry, dict) for entry in cases):
        raise CaptureError("tester corpus template contains a non-object case")
    return template


def compare_existing(stage: Path, manifest_bytes: bytes) -> bool:
    if not MANIFEST.is_file():
        return False
    try:
        existing = json.loads(MANIFEST.read_text(encoding="utf-8"))
        candidate = json.loads(manifest_bytes)
    except json.JSONDecodeError as error:
        raise CaptureError(f"existing or candidate manifest is invalid: {error}") from error
    if existing.get("schemaVersion") != 1 or len(existing.get("cases", [])) != 50:
        raise CaptureError("existing corpus manifest is not a complete v1 50-case corpus")
    if [case["id"] for case in existing["cases"]] != [case["id"] for case in candidate["cases"]]:
        raise CaptureError("existing corpus case identities differ; refusing to overwrite it")
    if any(case.get("stock", {}).get("sha256") == "PENDING_CAPTURE" for case in existing["cases"]):
        return False
    for old, new in zip(existing["cases"], candidate["cases"], strict=True):
        old_path = CORPUS / old["stock"]["path"]
        new_path = stage / new["stock"]["path"]
        if not old_path.is_file() or old["stock"].get("sha256") != sha256(old_path):
            raise CaptureError(f"existing corpus output is missing or hash-corrupt: {old.get('id', '<unknown>')}")
        if old["stock"].get("sha256") != new["stock"].get("sha256") or sha256(new_path) != new["stock"].get("sha256"):
            raise CaptureError(f"rerun stock response hash mismatch for {new['id']}; refusing to replace corpus")
    return True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=os.environ.get("JELLYFIN_BASE_URL", "http://127.0.0.1:8096"))
    parser.add_argument("--no-prepare", action="store_true", help="require an already-generated, local seeded stock server")
    parser.add_argument("--replace", action="store_true", help="replace an existing corpus only after all 50 new stock responses validate")
    arguments = parser.parse_args()
    try:
        validate_cases()
        template = load_template()
        base = require_loopback(arguments.base_url)
        validate_compose()
        if not arguments.no_prepare:
            prepare(base)
        compose_image_present()
        token = authenticate(base)
        item_ids = resolve_items(base, token)
        captured_at = datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
        version = tool_version()
        with tempfile.TemporaryDirectory(prefix="goldenrec-stock-corpus-") as temporary:
            stage = Path(temporary) / "stock-corpus"
            stock_dir = stage / "stock"
            stock_dir.mkdir(parents=True)
            copy_sources(stage)
            cases = []
            for definition, template_case in zip(CASES, template["cases"], strict=True):
                source = SOURCES[definition["source"]]
                item_id = item_ids[(source.item_type, source.media_path)]
                cases.append(capture_case(base, token, item_id, source, definition, template_case, stock_dir, captured_at, version))
            template["cases"] = cases
            encoded = (json.dumps(template, indent=2, sort_keys=True) + "\n").encode("utf-8")
            (stage / "manifest.json").write_bytes(encoded)
            if len(list(stock_dir.iterdir())) != 50 or len(cases) != 50:
                raise CaptureError("stock capture did not produce exactly 50 response bodies")
            if not arguments.replace and compare_existing(stage, encoded):
                print(f"verified 50 stock HTTP response bodies against existing corpus at {CORPUS}")
                return 0
            if (CORPUS / "inputs").exists() or (CORPUS / "stock").exists():
                if not arguments.replace:
                    raise CaptureError("existing corpus differs; refusing to overwrite it without --replace")
                shutil.rmtree(CORPUS / "inputs")
                shutil.rmtree(CORPUS / "stock")
            shutil.copytree(stage / "inputs", CORPUS / "inputs")
            shutil.copytree(stage / "stock", CORPUS / "stock")
            MANIFEST.write_bytes(encoded)
        print(f"captured and verified 50 stock HTTP response bodies at {CORPUS}; digest={EXPECTED_DIGEST}")
        return 0
    except CaptureError as error:
        print(f"goldenrec image capture: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
