#!/usr/bin/env python3
"""Record and offline-verify a secret-free Jellyfin public /Items corpus."""
from __future__ import annotations

import argparse
import hashlib
import ipaddress
import json
import os
import posixpath
import re
import shutil
import sqlite3
import subprocess
import sys
import time
import tempfile
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit, urlunsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener

ROOT = Path(__file__).resolve().parent
GOLDEN_RELATIVE = Path("internal/store/storetest/testdata/golden")
REQUEST_FORMAT = "goldenrec-itemquery-requests/v1"
EXPECTATION_FORMAT = "goldenrec-itemquery-expectations/v1"
PROVENANCE_FORMAT = "goldenrec-itemquery-provenance/v1"
FIXTURE_FORMAT = "goldenrec-itemquery-fixtures/v1"
FIXTURE_FIELDS = (
    "Path,MediaSources,MediaStreams,ProviderIds,People,Genres,Studios,Tags,UserData,DateCreated,PremiereDate,"
    "EndDate,ProductionLocations,ParentId,SeriesId,SeasonId,AlbumId,ArtistItems,AlbumArtists,Chapters,"
    "Overview,CommunityRating,CriticRating,OfficialRating,SortName,OriginalTitle,PresentationUniqueKey,"
    "SeriesPresentationUniqueKey,SeriesSortName,ExternalId,MediaType,Width,Height,Video3DFormat,IsPlaceholder,"
    "IsMovie,IsSeries,IsVirtualItem,IsFolder,IsLocked,LocationType,RunTimeTicks,TotalBitrate,ProductionYear,"
    "IndexNumber,ParentIndexNumber,AirTime,Status,VideoType,DateLastSaved,DateLastMediaAdded,DateLastRefreshed,"
    "DateLastPlayed,Album,AlbumArtist,SeriesName,SeasonName"
)
SHA256_HEX_LENGTH = 64
CLIENT_AUTHORIZATION = 'MediaBrowser Client="Goldenrec", Device="Goldenrec Seed", DeviceId="goldenrec-seed-v1", Version="1.0.0"'
STOCK_VERSION = "12.0.0"
STOCK_IMAGE_DIGEST = "sha256:2d1a69be5aa2695d0c2df90ebeb76bbcc6efc0a798cb09bfa65906680e1d785a"
UPSTREAM_COMMIT = "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb"
FIXTURE_OWNER_MARKER = ".goldenrec-fixtures-owner"
FIXTURE_OWNER_FORMAT = "goldenrec-fixtures-owner-v1"
SUBPROCESS_SECRET_ENV_NAMES = frozenset({
    "JELLYFIN_ADMIN",
    "JELLYFIN_PASSWORD",
    "GOLDENREC_ADMIN",
    "GOLDENREC_PASSWORD",
    "GOLDENREC_DISABLED_ADMIN",
    "GOLDENREC_DISABLED_PASSWORD",
    "JELLYFIN_DISABLED_ADMIN",
    "JELLYFIN_DISABLED_PASSWORD",
    "ADMIN",
    "PASSWORD",
    "DISABLED_ADMIN",
    "DISABLED_PASSWORD",
    "DISABLED_NAME",
    "GOLDENREC_DISABLED_NAME",
    "JELLYFIN_DISABLED_NAME",
})
BOXSET_EXPECTATION_NAMES = ("movie-collapse-boxset", "movie-no-collapse-boxset")
BOXSET_COLLECTION_MEMBERS = frozenset({
    "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film One (2020).mkv",
    "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Two (2021).mkv",
    "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Three (2022).mkv",
})
BOXSET_COLLECTION_IDENTITY = "BoxSet:/media/movies/Synthetic Collection [boxset]"



class RecorderError(RuntimeError):
    """A safe, user-facing recording or validation failure."""


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req: Request, fp: Any, code: int, msg: str, headers: Any, newurl: str) -> None:
        raise RecorderError("redirects are forbidden while recording")


def parse_fixture_manifest(data: bytes) -> dict[str, str]:
    """Parse canonical sha256sum lines without permitting filesystem escapes."""
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError as error:
        raise RecorderError("fixture manifest is not valid UTF-8") from error
    require(text.endswith("\n") and "\r" not in text, "fixture manifest must use newline-terminated SHA256SUMS lines")
    entries: dict[str, str] = {}
    for line in text.splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([^\x00]+)", line)
        require(match is not None, "fixture manifest contains an invalid SHA256SUMS line")
        digest, relative = match.groups()
        path = PurePosixPath(relative)
        require(
            path.parts
            and not path.is_absolute()
            and "\\" not in relative
            and all(part not in {"", ".", ".."} for part in path.parts)
            and path.as_posix() == relative
            and relative != "MANIFEST.sha256",
            "fixture manifest contains an unsafe path",
        )
        require(relative not in entries, "fixture manifest repeats a path")
        entries[relative] = digest
    require(entries, "fixture manifest must attest at least one fixture file")
    return entries


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def json_bytes(value: Any) -> bytes:
    return (json.dumps(value, ensure_ascii=False, indent=2, separators=(",", ": ")) + "\n").encode("utf-8")


def is_sha256(value: object) -> bool:
    return isinstance(value, str) and len(value) == SHA256_HEX_LENGTH and all(c in "0123456789abcdef" for c in value)


def scrubbed_subprocess_env() -> dict[str, str]:
    """Return a child environment that cannot disclose recorder credentials."""
    return {
        name: value
        for name, value in os.environ.items()
        if name.upper() not in SUBPROCESS_SECRET_ENV_NAMES
    }


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RecorderError(message)


def canonical_loopback_ip(host: str, context: str) -> str:
    """Return the canonical form of a literal loopback address."""
    try:
        address = ipaddress.ip_address(host)
    except ValueError as error:
        raise RecorderError(f"{context} must be a literal loopback IP address") from error
    require(address.is_loopback, f"{context} must be a literal loopback IP address")
    return str(address)


def canonical_media_path(path: object) -> str:
    require(isinstance(path, str) and path.startswith("/media/") and "\x00" not in path, "canonical identity must have a /media path")
    require(posixpath.normpath(path) == path and "/./" not in path and "/../" not in path, "canonical identity path is not normalized")
    return path


def canonical_identity(item: object) -> str:
    require(isinstance(item, dict), "item map entry must be an object")
    runtime_id = item.get("Id")
    item_type = item.get("Type")
    require(isinstance(runtime_id, str) and runtime_id, "item map entry is missing an ID")
    require(isinstance(item_type, str) and item_type and ":" not in item_type, "item map entry is missing a Type")
    return f"{item_type}:{canonical_media_path(item.get('Path'))}"


def parse_identity(value: object) -> tuple[str, str]:
    require(isinstance(value, str) and ":" in value, "canonical identity must be Type:/media/path")
    item_type, path = value.split(":", 1)
    require(item_type and ":" not in item_type, "canonical identity must be Type:/media/path")
    return item_type, canonical_media_path(path)


def load_expected_inventory(path: Path) -> tuple[str, ...]:
    try:
        value = json.loads(path.read_bytes())
    except (OSError, json.JSONDecodeError) as error:
        raise RecorderError("cannot read required expected inventory provenance input") from error
    require(isinstance(value, list) and value, "expected inventory must be a non-empty canonical identity array")
    identities = tuple(value)
    require(all(isinstance(identity, str) for identity in identities), "expected inventory identities must be strings")
    for identity in identities:
        parse_identity(identity)
    require(len(set(identities)) == len(identities), "expected inventory repeats a canonical identity")
    require(list(identities) == sorted(identities), "expected inventory must be sorted canonically")
    return identities


def public_name(name: str) -> str:
    require(isinstance(name, str) and name, "request parameter names must be non-empty strings")
    return name[0].upper() + name[1:]


def safe_manifest_value(value: object, parameter_name: str) -> object:
    if isinstance(value, dict):
        require(set(value) == {"$item"}, f"{parameter_name} has an unsupported object value")
        parse_identity(value["$item"])
        return {"$item": value["$item"]}
    if isinstance(value, list):
        return [safe_manifest_value(entry, parameter_name) for entry in value]
    require(value is None or isinstance(value, (str, int, float, bool)), f"{parameter_name} has an unsupported value")
    if parameter_name not in {"ExcludeProviderIds", "HasAnyProviderId", "HasAnyProviderIds", "HasImdbId", "HasTmdbId", "HasTvdbId"} and parameter_name.lower().endswith(("id", "ids")):
        raise RecorderError(f"{parameter_name} must use a symbolic $item reference, never a runtime ID")
    return value


def wire_value(value: object, item_map: dict[str, str], resolve_item_ref: object) -> str:
    if isinstance(value, dict):
        return resolve_item_ref(value, item_map)
    if isinstance(value, list):
        return ",".join(wire_value(entry, item_map, resolve_item_ref) for entry in value)
    if isinstance(value, bool):
        return "true" if value else "false"
    if value is None:
        return ""
    return str(value)


@dataclass(frozen=True)
class Case:
    name: str
    operation_id: str
    family: str
    params: dict[str, object]


class CaseManifest:
    def __init__(self, cases: list[Case], source_bytes: bytes) -> None:
        self.cases = cases
        self.source_bytes = source_bytes

    @classmethod
    def load(cls, path: str | Path) -> "CaseManifest":
        manifest_path = Path(path)
        try:
            source_bytes = manifest_path.read_bytes()
            raw = json.loads(source_bytes)
        except (OSError, json.JSONDecodeError) as error:
            raise RecorderError(f"cannot read request manifest: {error}") from error
        require(isinstance(raw, dict) and set(raw) == {"format", "cases"}, "request manifest must contain only format and cases")
        require(raw["format"] == REQUEST_FORMAT, "unsupported request manifest format")
        require(isinstance(raw["cases"], list) and raw["cases"], "request manifest cases must be a non-empty array")
        cases: list[Case] = []
        names: set[str] = set()
        semantic_requests: dict[str, str] = {}
        for raw_case in raw["cases"]:
            require(isinstance(raw_case, dict) and set(raw_case) == {"name", "operation_id", "family", "params"}, "each request case has an invalid schema")
            name = raw_case["name"]
            require(isinstance(name, str) and name and all(c.islower() or c.isdigit() or c == "-" for c in name), "case name must be stable kebab-case")
            require(name not in names, f"duplicate case name: {name}")
            names.add(name)
            require(raw_case["operation_id"] == "GetItems", "only the public GetItems operation is supported")
            require(raw_case["family"] == "translator-direct", "controller-only cases are not part of this corpus")
            require(isinstance(raw_case["params"], dict), "case params must be an object")
            params = {public_name(key): safe_manifest_value(value, public_name(key)) for key, value in raw_case["params"].items()}
            semantic_request = json.dumps({"method": "GET", "path": "/Items", "params": params}, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
            prior_name = semantic_requests.get(semantic_request)
            require(prior_name is None, f"duplicate semantic request: {prior_name}, {name}")
            semantic_requests[semantic_request] = name
            cases.append(Case(name, "GetItems", "translator-direct", params))
        return cls(cases, source_bytes)


class JellyfinClient:
    def __init__(self, base_url: str, timeout: float = 30.0) -> None:
        try:
            parts = urlsplit(base_url)
        except ValueError as error:
            raise RecorderError("base URL must be an origin URL") from error
        require(parts.scheme in {"http", "https"} and not parts.username and not parts.password and not parts.query and not parts.fragment, "base URL must be an origin URL")
        require(parts.path in {"", "/"}, "base URL must not include a path")
        try:
            explicit_port = parts.port
        except ValueError as error:
            raise RecorderError("base URL must include a valid port") from error
        require(explicit_port is None or explicit_port > 0, "base URL must include a valid port")
        self.base_host = canonical_loopback_ip(parts.hostname or "", "base URL host")
        self.base_port = explicit_port if explicit_port is not None else (443 if parts.scheme.lower() == "https" else 80)
        authority = f"[{self.base_host}]" if ":" in self.base_host else self.base_host
        if explicit_port is not None:
            authority += f":{explicit_port}"
        self.origin = urlunsplit((parts.scheme.lower(), authority, "", "", ""))
        self.timeout = timeout
        self.token: str | None = None
        self.opener = build_opener(ProxyHandler({}), NoRedirect())

    def request_payload(self, method: str, path: str, params: dict[str, str] | None = None, body: dict[str, object] | None = None) -> tuple[int, object]:
        require(path.startswith("/") and not path.startswith("//"), "request path must be origin-relative")
        url = self.origin + path
        if params:
            url += "?" + urlencode(params)
        headers = {"Accept": "application/json", "Authorization": CLIENT_AUTHORIZATION}
        data: bytes | None = None
        if self.token is not None:
            headers["Authorization"] += f', Token="{self.token}"'
        if body is not None:
            data = json.dumps(body, separators=(",", ":")).encode("utf-8")
            headers["Content-Type"] = "application/json"
        request = Request(url, data=data, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=self.timeout) as response:
                final = urlsplit(response.geturl())
                require(urlunsplit((final.scheme.lower(), final.netloc.lower(), "", "", "")) == self.origin, "request escaped the configured origin")
                status = response.status
                payload = json.loads(response.read().decode("utf-8"))
        except RecorderError:
            raise
        except HTTPError as error:
            if 300 <= error.code < 400:
                raise RecorderError("redirects are forbidden while recording") from error
            raise RecorderError(f"stock request failed with HTTP status {error.code}") from error
        except (URLError, OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
            raise RecorderError("stock request failed or returned invalid JSON") from error
        return status, payload

    def request_json(self, method: str, path: str, params: dict[str, str] | None = None, body: dict[str, object] | None = None) -> tuple[int, dict[str, object]]:
        status, payload = self.request_payload(method, path, params, body)
        require(isinstance(payload, dict), "stock response must be a JSON object")
        return status, payload

    def request_list(self, method: str, path: str, params: dict[str, str] | None = None) -> tuple[int, list[object]]:
        status, payload = self.request_payload(method, path, params)
        require(isinstance(payload, list), "stock response must be a JSON array")
        return status, payload

    def wait_for_public_info(self) -> None:
        deadline = time.monotonic() + 60
        last_error: RecorderError | None = None
        while time.monotonic() < deadline:
            try:
                self.public_info()
                return
            except RecorderError as error:
                last_error = error
                time.sleep(0.5)
        raise RecorderError("stock did not become available after consistent database snapshot capture") from last_error

    def public_info(self) -> dict[str, object]:
        status, info = self.request_json("GET", "/System/Info/Public")
        require(status == 200 and info.get("Version") == STOCK_VERSION, f"stock must be pinned Jellyfin {STOCK_VERSION}")
        return info

    def authenticate(self, username: str, password: str) -> None:
        require(username and password, "JELLYFIN_ADMIN and JELLYFIN_PASSWORD must both be set")
        status, result = self.request_json("POST", "/Users/AuthenticateByName", body={"Username": username, "Pw": password})
        token = result.get("AccessToken")
        require(status == 200 and isinstance(token, str) and token, "stock authentication failed")
        self.token = token
    def resolve_item_ref(self, ref: dict[str, object], item_map: dict[str, str]) -> str:
        require(set(ref) == {"$item"}, "symbolic item reference must contain only $item")
        identity = ref["$item"]
        parse_identity(identity)
        runtime_id = item_map.get(identity)
        require(runtime_id is not None, f"symbolic item reference is not in the canonical item map: {identity}")
        return runtime_id

    def build_item_map(self) -> dict[str, str]:
        by_identity: dict[str, str] = {}
        seen_ids: set[str] = set()
        start = 0
        total: int | None = None
        while total is None or start < total:
            status, response = self.request_json("GET", "/Items", {"Recursive": "true", "Fields": "Path,ProviderIds", "StartIndex": str(start), "Limit": "1000"})
            require(status == 200, f"item-map request returned HTTP status {status}")
            items = response.get("Items")
            response_total = response.get("TotalRecordCount")
            require(isinstance(items, list) and isinstance(response_total, int) and response_total >= 0, "item-map response is missing pagination fields")
            if total is None:
                total = response_total
            else:
                require(total == response_total, "item-map TotalRecordCount changed during pagination")
            require(items or start == total, "item-map pagination returned an empty page before completion")
            for raw_item in items:
                identity = canonical_identity(raw_item)
                require(raw_item.get("ProviderIds") in (None, {}, []), "item map contains nonempty ProviderIds evidence")
                runtime_id = raw_item["Id"]
                require(runtime_id not in seen_ids, "duplicate runtime item identity in item map")
                require(identity not in by_identity, f"duplicate canonical identity in item map: {identity}")
                seen_ids.add(runtime_id)
                by_identity[identity] = runtime_id
            start += len(items)
            require(start <= total, "item-map returned more items than TotalRecordCount")
        require(total is not None and len(by_identity) == total, "item-map did not contain every canonical identity")
        return by_identity

    def build_fixture_metadata(self, item_map: dict[str, str]) -> dict[str, object]:
        by_runtime_id = {runtime_id: identity for identity, runtime_id in item_map.items()}
        status, response = self.request_json("GET", "/Items", {"Recursive": "true", "Fields": FIXTURE_FIELDS, "StartIndex": "0", "Limit": "1000"})
        raw_items, total = response.get("Items"), response.get("TotalRecordCount")
        require(status == 200 and isinstance(raw_items, list) and isinstance(total, int) and total == len(item_map), "fixture metadata response is incomplete")
        raw_by_identity: dict[str, dict[str, object]] = {}
        for raw_item in raw_items:
            identity = canonical_identity(raw_item)
            require(identity in item_map and identity not in raw_by_identity, "fixture metadata contains an unknown or duplicate canonical item")
            raw_by_identity[identity] = raw_item
        require(set(raw_by_identity) == set(item_map), "fixture metadata does not cover the canonical inventory")
        scalar_keys = ("Name", "SortName", "OriginalTitle", "Path", "Type", "MediaType", "Container", "DateCreated", "DateLastMediaAdded", "DateLastRefreshed", "DateLastSaved", "DateLastPlayed", "PremiereDate", "EndDate", "ProductionYear", "RunTimeTicks", "TotalBitrate", "IndexNumber", "ParentIndexNumber", "Overview", "CommunityRating", "CriticRating", "OfficialRating", "Width", "Height", "VideoType", "Video3DFormat", "IsFolder", "IsPlaceHolder", "IsMovie", "IsSeries", "IsVirtualItem", "IsLocked", "LocationType", "HasSubtitles", "HasLyrics", "AirDays", "AirTime", "Status", "DisplayOrder", "ImageOrientation", "ProductionLocations", "Album", "AlbumArtist", "SeriesName", "SeasonName", "SeriesSortName", "PresentationUniqueKey", "SeriesPresentationUniqueKey", "ExternalId")
        def primitive(value: object, context: str) -> object:
            if value is None or isinstance(value, (str, int, float, bool)):
                return value
            if isinstance(value, list):
                return [primitive(entry, context) for entry in value]
            if isinstance(value, dict):
                return {key: primitive(value[key], f"{context}.{key}") for key in sorted(value) if isinstance(key, str) and key.lower() not in {"id", "itemid", "serverid", "userid", "key", "etag"} and "token" not in key.lower() and "password" not in key.lower() and "image" not in key.lower() and "blurhash" not in key.lower()}
            raise RecorderError(f"fixture metadata {context} contains an unsupported value")
        def item_ref(runtime_id: object, context: str) -> dict[str, str] | None:
            if runtime_id is None:
                return None
            require(isinstance(runtime_id, str), f"fixture metadata {context} contains a malformed runtime item reference")
            return {"$item": by_runtime_id[runtime_id]} if runtime_id in by_runtime_id else None
        def top_parent(identity: str) -> dict[str, str]:
            current, seen = identity, set()
            while current not in seen:
                seen.add(current)
                parent = item_ref(raw_by_identity[current].get("ParentId"), f"{current}.ParentId")
                if parent is None:
                    return {"$item": current}
                current = parent["$item"]
            raise RecorderError("fixture metadata contains a cyclic item hierarchy")
        entries: list[dict[str, object]] = []
        for identity in sorted(raw_by_identity):
            raw = raw_by_identity[identity]
            scalars = {key: primitive(raw[key], f"{identity}.{key}") for key in scalar_keys if key in raw}
            if "TotalBitrate" not in scalars:
                bitrates = [source.get("Bitrate") for source in raw.get("MediaSources") or [] if isinstance(source, dict) and isinstance(source.get("Bitrate"), int)]
                if bitrates:
                    scalars["TotalBitrate"] = max(bitrates)
            people = [{key: primitive(person[key], f"{identity}.people.{key}") for key in ("Name", "Type", "Role") if key in person} for person in raw.get("People") or [] if isinstance(person, dict)]
            names = lambda key: [{"Name": primitive(entry["Name"], f"{identity}.{key}.Name")} for entry in raw.get(key) or [] if isinstance(entry, dict) and isinstance(entry.get("Name"), str)]
            user_values = primitive(raw.get("UserData") or {}, f"{identity}.user_data")
            entries.append({"identity": identity, "scalars": scalars, "relations": {"parent": item_ref(raw.get("ParentId"), f"{identity}.ParentId"), "top_parent": top_parent(identity), "series": item_ref(raw.get("SeriesId"), f"{identity}.SeriesId"), "season": item_ref(raw.get("SeasonId"), f"{identity}.SeasonId"), "album": item_ref(raw.get("AlbumId"), f"{identity}.AlbumId"), "people": people, "provider_ids": primitive(raw.get("ProviderIds") or {}, f"{identity}.provider_ids"), "values": {"genres": primitive(raw.get("Genres") or [], f"{identity}.genres"), "studios": primitive(raw.get("Studios") or [], f"{identity}.studios"), "tags": primitive(raw.get("Tags") or [], f"{identity}.tags"), "artists": primitive(raw.get("Artists") or [], f"{identity}.artists"), "album_artists": primitive(raw.get("AlbumArtists") or [], f"{identity}.album_artists"), "artist_items": names("ArtistItems")}}, "media": {"sources": primitive(raw.get("MediaSources") or [], f"{identity}.media_sources"), "streams": primitive(raw.get("MediaStreams") or [], f"{identity}.media_streams"), "chapters": [{"has_image": bool(chapter.get("ImagePath"))} for chapter in raw.get("Chapters") or [] if isinstance(chapter, dict)], "image_types": sorted((raw.get("ImageTags") or {}).keys())}, "user_data": {"user": "GoldenrecAdmin", "key": {"$item": identity}, "values": user_values}})
        reference_times = sorted(raw.get("DateCreated") for raw in raw_by_identity.values() if isinstance(raw.get("DateCreated"), str))
        require(reference_times, "fixture metadata lacks a source-derived reference time")
        return {"format": FIXTURE_FORMAT, "reference_time": reference_times[0], "items": entries, "query_bindings": []}


class Recorder:
    def __init__(self, client: JellyfinClient, cases: CaseManifest, output_root: str | Path) -> None:
        self.client = client
        self.cases = cases
        self.output_root = Path(output_root)

    @staticmethod
    def _required_source_hash(path: Path, label: str) -> str:
        require(path.is_file(), f"required {label} provenance input is missing")
        try:
            return sha256_file(path)
        except OSError as error:
            raise RecorderError(f"cannot hash required {label} provenance input") from error

    @staticmethod
    def _expected_inventory() -> tuple[str, ...]:
        return load_expected_inventory(ROOT / "fixtures" / "expected-inventory.json")

    @staticmethod
    def _provider_policy() -> dict[str, object]:
        path = ROOT / "provider-policy.json"
        try:
            value = json.loads(path.read_bytes())
        except (OSError, json.JSONDecodeError) as error:
            raise RecorderError("cannot read required provider policy provenance input") from error
        require(isinstance(value, dict) and isinstance(value.get("TypeOptions"), list), "provider policy must define TypeOptions")
        return value

    @staticmethod
    def _type_options_by_type(value: object, label: str) -> dict[str, dict[str, object]]:
        require(isinstance(value, list), f"{label} TypeOptions must be an array")
        by_type: dict[str, dict[str, object]] = {}
        for option in value:
            require(isinstance(option, dict), f"{label} TypeOptions entry must be an object")
            item_type = option.get("Type")
            require(isinstance(item_type, str) and item_type, f"{label} TypeOptions entry lacks Type")
            require(item_type not in by_type, f"{label} repeats TypeOptions type {item_type}")
            by_type[item_type] = option
        return by_type

    def _validate_stock_topology_snapshot(self, libraries: list[object]) -> None:
        policy = self._provider_policy()
        expected_type_options = self._type_options_by_type(policy["TypeOptions"], "provider policy")
        expected_libraries = {
            "Golden Movies": ("movies", "/media/movies"),
            "Golden TV": ("tvshows", "/media/tv"),
            "Golden Music": ("music", "/media/music"),
            "Golden Photos": ("homevideos", "/media/photos"),
        }
        require(len(libraries) == len(expected_libraries), "stock virtual-folder topology must contain exactly the Golden libraries")
        by_name: dict[str, dict[str, object]] = {}
        for library in libraries:
            require(isinstance(library, dict), "stock virtual-folder entry must be an object")
            name = library.get("Name")
            require(isinstance(name, str) and name in expected_libraries, "stock virtual-folder topology contains an unexpected library")
            require(name not in by_name, f"stock virtual-folder topology repeats library {name}")
            require(library.get("RefreshStatus") == "Idle", f"stock virtual-folder {name} refresh status must be Idle")
            by_name[name] = library
        require(set(by_name) == set(expected_libraries), "stock virtual-folder topology is missing a Golden library")
        for name, (collection_type, location) in expected_libraries.items():
            library = by_name[name]
            require(library.get("CollectionType") == collection_type and library.get("Locations") == [location], f"stock virtual-folder {name} has the wrong type or location")
            options = library.get("LibraryOptions")
            require(isinstance(options, dict), f"stock virtual-folder {name} lacks LibraryOptions")
            for key, expected_value in policy.items():
                require(key in options, f"stock virtual-folder {name} LibraryOptions is missing committed policy {key}")
                if key != "TypeOptions":
                    require(options[key] == expected_value, f"stock virtual-folder {name} LibraryOptions differs for committed policy {key}")
            actual_type_options = self._type_options_by_type(options["TypeOptions"], f"stock virtual-folder {name}")
            require(set(actual_type_options) == set(expected_type_options), f"stock virtual-folder {name} has missing or extra TypeOptions")
            for item_type, expected_option in expected_type_options.items():
                actual_option = actual_type_options[item_type]
                for key in ("MetadataFetchers", "ImageFetchers", "SimilarItemProviders"):
                    require(key in expected_option and key in actual_option, f"stock virtual-folder {name} TypeOptions {item_type} is missing {key}")
                    require(actual_option[key] == expected_option[key], f"stock virtual-folder {name} TypeOptions {item_type} differs for {key}")

    def _validate_stock_topology(self) -> None:
        for _ in range(2):
            status, libraries = self.client.request_list("GET", "/Library/VirtualFolders")
            require(status == 200, f"virtual-folder request returned HTTP status {status}")
            self._validate_stock_topology_snapshot(libraries)

    @staticmethod
    def _validate_exact_inventory(item_map: dict[str, str], expected_inventory: tuple[str, ...]) -> None:
        actual = set(item_map)
        expected = set(expected_inventory)
        expected_types = {path: item_type for item_type, path in map(parse_identity, expected_inventory)}
        actual_types = {path: item_type for item_type, path in map(parse_identity, actual)}
        missing = sorted(expected - actual)
        extra = sorted(actual - expected)
        type_changed = sorted(
            f"{path}: expected {expected_types[path]}, got {actual_types[path]}"
            for path in expected_types.keys() & actual_types.keys()
            if expected_types[path] != actual_types[path]
        )
        require(not missing and not extra and not type_changed, f"stock item inventory differs from expected manifest: missing={missing}; extra={extra}; type_changed={type_changed}")

    def _source_hashes(self) -> dict[str, str]:
        seed = ROOT / "seed.sh"
        provider_policy = ROOT / "provider-policy.json"
        expected_inventory = ROOT / "fixtures" / "expected-inventory.json"
        fixture_manifest = ROOT / "fixtures" / "source-manifest.sha256"
        self._expected_inventory()
        return {
            "recorder_sha256": self._required_source_hash(Path(__file__), "recorder"),
            "request_manifest_sha256": sha256_bytes(self.cases.source_bytes),
            "seed_sha256": self._required_source_hash(seed, "seed"),
            "provider_policy_sha256": self._required_source_hash(provider_policy, "provider policy"),
            "expected_inventory_sha256": self._required_source_hash(expected_inventory, "expected inventory"),
            "fixture_manifest_sha256": self._required_source_hash(fixture_manifest, "fixture manifest"),
        }

    def provenance(self, fixtures: dict[str, object]) -> dict[str, object]:
        self._validate_fixtures(fixtures)
        return {
            "format": PROVENANCE_FORMAT,
            "stock_version": STOCK_VERSION,
            "stock_image_digest": STOCK_IMAGE_DIGEST,
            "upstream_commit": UPSTREAM_COMMIT,
            "upstream_commit_authority": "api-parity-source",
            "fixture_sha256": sha256_bytes(json_bytes(fixtures)),
            **self._source_hashes(),
        }

    @staticmethod
    def _pinned_image_config_id(child_env: dict[str, str]) -> str:
        """Resolve the Docker config ID and repo digest for the pinned manifest."""
        expected_reference = f"jellyfin/jellyfin@{STOCK_IMAGE_DIGEST}"
        try:
            resolved = subprocess.run(
                ["docker", "image", "inspect", expected_reference, "--format", "{{.Id}}"],
                check=False,
                capture_output=True,
                text=True,
                timeout=10,
                env=child_env,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise RecorderError("cannot resolve the pinned stock image config ID with Docker") from error
        require(resolved.returncode == 0, "Docker could not resolve the pinned stock image config ID")
        config_id = resolved.stdout.strip()
        require(re.fullmatch(r"sha256:[0-9a-f]{64}", config_id) is not None, "Docker returned a malformed pinned stock image config ID")

        try:
            repo_digests = subprocess.run(
                ["docker", "image", "inspect", config_id, "--format", "{{json .RepoDigests}}"],
                check=False,
                capture_output=True,
                text=True,
                timeout=10,
                env=child_env,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise RecorderError("cannot inspect the resolved pinned stock image with Docker") from error
        require(repo_digests.returncode == 0, "Docker could not inspect the resolved pinned stock image")
        try:
            resolved_repo_digests = json.loads(repo_digests.stdout)
        except json.JSONDecodeError as error:
            raise RecorderError("Docker returned malformed pinned stock image repository digests") from error
        require(
            isinstance(resolved_repo_digests, list)
            and all(isinstance(digest, str) for digest in resolved_repo_digests)
            and expected_reference in resolved_repo_digests,
            "resolved stock image does not retain the required pinned repository manifest digest",
        )
        return config_id

    @staticmethod
    def _attest_media_mount(container: dict[str, object], verified_fixture_root: Path) -> None:
        """Require the stock runtime to use the fixture tree already attested on disk."""
        mounts = container.get("Mounts")
        require(isinstance(mounts, list) and all(isinstance(mount, dict) for mount in mounts), "Docker attested stock container has malformed mount inspection data")
        media_mounts = [mount for mount in mounts if mount.get("Destination") == "/media"]
        require(media_mounts, "Docker attested stock container has no /media mount")
        require(len(media_mounts) == 1, "Docker attested stock container has multiple /media mounts")
        media_mount = media_mounts[0]
        require(media_mount.get("Type") == "bind", "Docker attested stock container /media mount must be a bind mount")
        require(media_mount.get("RW") is False, "Docker attested stock container /media bind must be read-only")
        source = media_mount.get("Source")
        require(isinstance(source, str) and source and "\x00" not in source, "Docker attested stock container /media bind source is missing or malformed")
        source_path = Path(source)
        require(source_path.is_absolute(), "Docker attested stock container /media bind source must be absolute")
        try:
            canonical_source = source_path.resolve(strict=True)
        except OSError as error:
            raise RecorderError("cannot resolve Docker attested stock container /media bind source") from error
        require(source_path == canonical_source, "Docker attested stock container /media bind source must be canonical and must not traverse a symlink")
        require(canonical_source.is_dir(), "Docker attested stock container /media bind source must be a directory")
        require(canonical_source == verified_fixture_root, "Docker attested stock container /media bind source does not match the verified fixture directory")

    def _attest_runtime_container(self, child_env: dict[str, str], verified_fixture_root: Path) -> str:
        """Bind the validated loopback HTTP peer to one running pinned container."""
        expected_reference = f"jellyfin/jellyfin@{STOCK_IMAGE_DIGEST}"
        expected_config_id = self._pinned_image_config_id(child_env)
        try:
            listed = subprocess.run(
                ["docker", "ps", "--all", "--format", "{{.ID}}"],
                check=False,
                capture_output=True,
                text=True,
                timeout=10,
                env=child_env,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise RecorderError("cannot invoke Docker to attest the local stock container") from error
        require(listed.returncode == 0, "Docker could not list local stock container candidates")
        container_ids = [line.strip() for line in listed.stdout.splitlines() if line.strip()]
        require(container_ids, "Docker found no local stock container candidates")
        require(
            len(container_ids) == len(set(container_ids))
            and all(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", container_id) for container_id in container_ids),
            "Docker returned malformed local stock container identifiers",
        )
        try:
            inspected = subprocess.run(
                ["docker", "inspect", "--", *container_ids],
                check=False,
                capture_output=True,
                text=True,
                timeout=10,
                env=child_env,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise RecorderError("cannot inspect local stock container candidates with Docker") from error
        require(inspected.returncode == 0, "Docker could not inspect local stock container candidates")
        try:
            containers = json.loads(inspected.stdout)
        except json.JSONDecodeError as error:
            raise RecorderError("Docker returned malformed stock container inspection JSON") from error
        require(isinstance(containers, list) and len(containers) == len(container_ids), "Docker inspection did not return every local stock container candidate")

        expected_host = self.client.base_host
        expected_port = self.client.base_port
        expected_publication = f"[{expected_host}]:{expected_port}" if ":" in expected_host else f"{expected_host}:{expected_port}"
        accepted: list[object] = []
        for container in containers:
            require(isinstance(container, dict), "Docker inspection contains a malformed container")
            config = container.get("Config")
            state = container.get("State")
            network = container.get("NetworkSettings")
            if not isinstance(config, dict) or config.get("Image") != expected_reference:
                continue
            if not isinstance(state, dict) or state.get("Running") is not True:
                continue
            if container.get("Image") != expected_config_id:
                continue
            if not isinstance(network, dict):
                continue
            ports = network.get("Ports")
            if not isinstance(ports, dict):
                continue
            bindings = ports.get("8096/tcp")
            if not isinstance(bindings, list) or not bindings:
                continue
            publication_matches = True
            for binding in bindings:
                if not isinstance(binding, dict) or not isinstance(binding.get("HostIp"), str):
                    publication_matches = False
                    break
                try:
                    published_host = canonical_loopback_ip(binding["HostIp"], "Docker published host")
                except RecorderError:
                    publication_matches = False
                    break
                if published_host != expected_host or binding.get("HostPort") != str(expected_port):
                    publication_matches = False
                    break
            if publication_matches:
                self._attest_media_mount(container, verified_fixture_root)
                accepted.append(container)
        require(
            len(accepted) == 1,
            f"Docker runtime attestation requires exactly one running digest-pinned Jellyfin container with 8096/tcp published on {expected_publication}",
        )
        container = accepted[0]
        require(isinstance(container, dict) and isinstance(container.get("Id"), str) and container["Id"], "Docker attestation returned no usable pinned container ID")
        return container["Id"]

    @staticmethod
    def _validate_live_fixture_manifest(output: Path = ROOT / "fixtures" / "out") -> Path:
        committed_manifest = ROOT / "fixtures" / "source-manifest.sha256"
        try:
            require(committed_manifest.is_file() and not committed_manifest.is_symlink(), "committed fixture manifest is not a regular file")
            committed_bytes = committed_manifest.read_bytes()
        except OSError as error:
            raise RecorderError("cannot read required committed fixture manifest attestation") from error
        require(not output.is_symlink(), "generated fixture output must not be a symlink")
        require(output.exists(), "generated fixture output directory is missing")
        require(output.is_dir(), "generated fixture output is not a real directory")
        try:
            output = output.resolve(strict=True)
        except OSError as error:
            raise RecorderError("cannot canonicalize generated fixture output directory") from error
        try:
            output_stat = output.stat()
        except OSError as error:
            raise RecorderError("cannot stat generated fixture output directory") from error
        require(output_stat.st_mode & 0o7777 == 0o700, "generated fixture output directory must have mode 0700")
        require(output_stat.st_uid == os.getuid(), "generated fixture output directory must be owned by the current user")
        owner_marker = output / FIXTURE_OWNER_MARKER
        require(owner_marker.is_file() and not owner_marker.is_symlink(), "generated fixture owner marker is not a regular file")
        try:
            owner_marker_stat = owner_marker.stat()
            require(owner_marker_stat.st_mode & 0o7777 == 0o600, "generated fixture owner marker must have mode 0600")
            require(owner_marker_stat.st_uid == os.getuid(), "generated fixture owner marker must be owned by the current user")
            expected_owner_marker = (
                json.dumps(
                    {"format": FIXTURE_OWNER_FORMAT, "root": str(output.resolve()), "uid": os.getuid()},
                    separators=(",", ":"),
                    sort_keys=True,
                )
                + "\n"
            ).encode("utf-8")
            require(owner_marker.read_bytes() == expected_owner_marker, "generated fixture owner marker contents are invalid")
        except OSError as error:
            raise RecorderError("cannot read generated fixture owner marker") from error
        live_manifest = output / "MANIFEST.sha256"
        require(live_manifest.is_file() and not live_manifest.is_symlink(), "generated fixture manifest is not a regular file")
        try:
            require(live_manifest.read_bytes() == committed_bytes, "generated fixture manifest differs from the committed attestation")
        except OSError as error:
            raise RecorderError("cannot read generated fixture manifest") from error
        expected = parse_fixture_manifest(committed_bytes)
        expected_directories: set[str] = set()
        for relative in expected:
            expected_directories.update(
                parent.as_posix()
                for parent in PurePosixPath(relative).parents
                if parent != PurePosixPath(".")
            )

        actual_directories: set[str] = set()
        actual_files: set[str] = set()
        for current, directories, files in os.walk(output, followlinks=False):
            directory = Path(current)
            for name in directories:
                path = directory / name
                require(not path.is_symlink() and path.is_dir(), "generated fixture output contains a non-directory")
                actual_directories.add(path.relative_to(output).as_posix())
            for name in files:
                path = directory / name
                require(not path.is_symlink() and path.is_file(), "generated fixture output contains a non-regular file")
                relative = path.relative_to(output).as_posix()
                if relative not in {"MANIFEST.sha256", FIXTURE_OWNER_MARKER}:
                    actual_files.add(relative)
        require(actual_directories == expected_directories, "generated fixture output directory set differs from the committed attestation")
        require(actual_files == set(expected), "generated fixture output file set differs from the committed attestation")
        for relative, digest in expected.items():
            path = output / Path(relative)
            require(path.is_file() and not path.is_symlink(), "generated fixture output is missing an attested regular file")
            require(sha256_file(path) == digest, f"generated fixture output hash mismatch for {relative}")
        return output

    @staticmethod
    def _merge_stock_snapshot(fixtures: dict[str, object], snapshot_path: Path, item_map: dict[str, str]) -> None:
        def database_item_id(value: object) -> str:
            require(isinstance(value, str) and value, "attested stock database item ID is malformed")
            return value.replace("-", "").lower()
        try:
            connection = sqlite3.connect(snapshot_path)
            rows = {database_item_id(runtime_id): (path, date_last_saved, total_bitrate) for runtime_id, path, date_last_saved, total_bitrate in connection.execute("SELECT Id, Path, DateLastSaved, TotalBitrate FROM BaseItems")}
        except sqlite3.Error as error:
            raise RecorderError("cannot read DateLastSaved and TotalBitrate from the attested stock Jellyfin database") from error
        finally:
            try:
                connection.close()
            except UnboundLocalError:
                pass

        items = fixtures.get("items")
        require(isinstance(items, list), "fixture metadata has no items for stock scalar capture")
        for entry in items:
            require(isinstance(entry, dict) and isinstance(entry.get("identity"), str) and isinstance(entry.get("scalars"), dict), "fixture metadata entry is malformed during stock scalar capture")
            runtime_id = item_map.get(entry["identity"])
            database_id = database_item_id(runtime_id) if runtime_id is not None else None
            require(database_id is not None and database_id in rows, "attested stock database does not contain a captured canonical item")
            path, date_last_saved, total_bitrate = rows[database_id]
            scalars = entry["scalars"]
            require(isinstance(path, str) and scalars.get("Path") == path, "attested stock database path does not match the captured canonical item")
            require(date_last_saved is None or isinstance(date_last_saved, str), "attested stock database DateLastSaved is malformed")
            require(total_bitrate is None or isinstance(total_bitrate, int), "attested stock database TotalBitrate is malformed")
            if date_last_saved is None:
                scalars["DateLastSaved"] = None
            else:
                match = re.fullmatch(r"(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2}(?:\.\d{1,7})?)", date_last_saved)
                require(match is not None, "attested stock database DateLastSaved is not UTC SQLite text")
                scalars["DateLastSaved"] = match.group(1) + "T" + match.group(2) + "Z"
            scalars["TotalBitrate"] = total_bitrate

    @staticmethod
    def _capture_stock_snapshot_scalars(fixtures: dict[str, object], container_id: str, item_map: dict[str, str], child_env: dict[str, str]) -> None:
        with tempfile.TemporaryDirectory(prefix="goldenrec-stock-db-") as directory:
            snapshot_root = Path(directory)
            stopped = subprocess.run(["docker", "stop", container_id], check=False, capture_output=True, text=True, timeout=30, env=child_env)
            require(stopped.returncode == 0, "cannot stop the attested stock Jellyfin container for scalar capture")
            try:
                copied = subprocess.run(["docker", "cp", f"{container_id}:/config/data/data", str(snapshot_root)], check=False, capture_output=True, text=True, timeout=30, env=child_env)
                database = snapshot_root / "data" / "jellyfin.db"
                require(copied.returncode == 0 and database.is_file(), "cannot copy the stopped attested stock Jellyfin data directory for scalar capture")
                Recorder._merge_stock_snapshot(fixtures, database, item_map)
            finally:
                restarted = subprocess.run(["docker", "start", container_id], check=False, capture_output=True, text=True, timeout=30, env=child_env)
                require(restarted.returncode == 0, "cannot restart the attested stock Jellyfin container after scalar capture")


    def record(self) -> None:
        username = os.environ.pop("JELLYFIN_ADMIN", "")
        password = os.environ.pop("JELLYFIN_PASSWORD", "")
        child_env = scrubbed_subprocess_env()
        expected_inventory = self._expected_inventory()
        verified_fixture_root = self._validate_live_fixture_manifest()
        container_id = self._attest_runtime_container(child_env, verified_fixture_root)
        self.client.public_info()
        self.client.authenticate(username, password)
        self._validate_stock_topology()
        item_map = self.client.build_item_map()
        self._validate_exact_inventory(item_map, expected_inventory)
        fixtures = self.client.build_fixture_metadata(item_map)
        self._capture_stock_snapshot_scalars(fixtures, container_id, item_map, child_env)
        self.client.wait_for_public_info()
        self._validate_fixtures(fixtures)
        provenance = self.provenance(fixtures)
        corpus_cases: list[dict[str, object]] = []
        for case in self.cases.cases:
            wire_params = {name: wire_value(value, item_map, self.client.resolve_item_ref) for name, value in case.params.items()}
            status, response = self.client.request_json("GET", "/Items", wire_params)
            require(status == 200, f"case {case.name} returned HTTP status {status}")
            items = response.get("Items")
            start_index = response.get("StartIndex")
            total = response.get("TotalRecordCount")
            require(isinstance(items, list) and isinstance(start_index, int) and isinstance(total, int), f"case {case.name} response is missing pagination fields")
            identities: list[str] = []
            seen: set[str] = set()
            for raw_item in items:
                identity = canonical_identity(raw_item)
                runtime_id = raw_item.get("Id") if isinstance(raw_item, dict) else None
                require(item_map.get(identity) == runtime_id, f"case {case.name} returned an unknown output item")
                require(identity not in seen, f"case {case.name} returned duplicate canonical identity")
                seen.add(identity)
                identities.append(identity)
            corpus_cases.append({
                "name": case.name,
                "operation_id": case.operation_id,
                "family": case.family,
                "request": {"method": "GET", "path": "/Items", "params": case.params},
                "expect": {"status": status, "start_index": start_index, "total_record_count": total, "items": identities},
            })
        corpus = {"format": EXPECTATION_FORMAT, "cases": corpus_cases}
        self._validate_corpus(corpus)
        self._validate_boxset_replacement(corpus)
        self._publish(corpus, fixtures, provenance)

    @staticmethod
    def _validate_boxset_replacement(corpus: dict[str, object]) -> None:
        cases = corpus.get("cases")
        require(isinstance(cases, list), "itemquery corpus cases must be an array")
        matching = {
            name: [case for case in cases if isinstance(case, dict) and case.get("name") == name]
            for name in BOXSET_EXPECTATION_NAMES
        }
        if not any(matching.values()):
            return
        for name, expected_cases in matching.items():
            require(len(expected_cases) == 1, f"BoxSet expectation {name} must occur exactly once")

        def expectation_identities(case: dict[str, object], name: str) -> set[str]:
            expectation = case.get("expect")
            require(isinstance(expectation, dict), f"BoxSet expectation {name} is malformed")
            require(expectation.get("status") == 200, f"BoxSet expectation {name} did not return HTTP status 200")
            items = expectation.get("items")
            require(isinstance(items, list), f"BoxSet expectation {name} items are malformed")
            identities: set[str] = set()
            for identity in items:
                parse_identity(identity)
                require(identity not in identities, f"BoxSet expectation {name} repeats a canonical identity")
                identities.add(identity)
            return identities

        collapsed = expectation_identities(matching["movie-collapse-boxset"][0], "movie-collapse-boxset")
        uncollapsed = expectation_identities(matching["movie-no-collapse-boxset"][0], "movie-no-collapse-boxset")
        require(uncollapsed - collapsed == BOXSET_COLLECTION_MEMBERS, "uncollapsed BoxSet expectation must add exactly the three Synthetic Collection member Movies")
        require(collapsed - uncollapsed == {BOXSET_COLLECTION_IDENTITY}, "collapsed BoxSet expectation must add exactly the physical Synthetic Collection BoxSet")
        require(collapsed - {BOXSET_COLLECTION_IDENTITY} == uncollapsed - BOXSET_COLLECTION_MEMBERS, "BoxSet expectations must have identical common Movie identities")

    @staticmethod
    def _remove_path(path: Path) -> None:
        if path.is_dir() and not path.is_symlink():
            shutil.rmtree(path)
        else:
            path.unlink()

    def _directory_is_valid(self, golden: Path) -> bool:
        try:
            self._validate_directory(golden)
            self._validate_source_hashes(golden)
            return True
        except (OSError, RecorderError):
            return False

    def _recover_previous(self, target: Path) -> None:
        previous = target.parent / f"{target.name}.previous"
        if not previous.exists() or not self._directory_is_valid(previous):
            return
        if self._directory_is_valid(target):
            return

        displaced = target.parent / f".{target.name}.interrupted"
        if displaced.exists():
            self._remove_path(displaced)
        try:
            if target.exists():
                os.replace(target, displaced)
            os.replace(previous, target)
        except OSError as error:
            if not target.exists() and displaced.exists():
                os.replace(displaced, target)
            raise RecorderError("cannot recover the prior valid golden corpus") from error
        if displaced.exists():
            self._remove_path(displaced)

    def _publish(self, corpus: dict[str, object], fixtures: dict[str, object], provenance: dict[str, object]) -> None:
        target = self.output_root / GOLDEN_RELATIVE
        target.parent.mkdir(parents=True, exist_ok=True)
        self._recover_previous(target)
        stage = Path(tempfile.mkdtemp(prefix=".itemquery-golden-", dir=target.parent))
        backup = target.parent / f"{target.name}.previous"
        try:
            itemquery = stage / "itemquery.json"
            fixtures_path = stage / "itemquery-fixtures.json"
            provenance_path = stage / "provenance.json"
            itemquery.write_bytes(json_bytes(corpus))
            fixtures_path.write_bytes(json_bytes(fixtures))
            provenance_path.write_bytes(json_bytes(provenance))
            entries = [(path.name, sha256_file(path)) for path in sorted((itemquery, fixtures_path, provenance_path), key=lambda value: value.name)]
            (stage / "SHA256SUMS").write_text("".join(f"{digest}  {name}\n" for name, digest in entries), encoding="utf-8")
            self._validate_directory(stage)
            target_valid = self._directory_is_valid(target)
            backup_valid = self._directory_is_valid(backup)
            require(not backup.exists() or not backup_valid or target_valid, "refusing to discard the only valid prior golden corpus")
            if backup.exists():
                self._remove_path(backup)
            if target.exists():
                os.replace(target, backup)
            os.replace(stage, target)
            if backup.exists():
                self._remove_path(backup)
        except Exception:
            if not target.exists() and backup.exists():
                os.replace(backup, target)
            if stage.exists():
                shutil.rmtree(stage, ignore_errors=True)
            raise

    @staticmethod
    def _forbidden(value: object, context: str = "") -> None:
        if isinstance(value, dict):
            for key, nested in value.items():
                lowered = key.lower()
                require(key == "stock_image_digest" or not any(term in lowered for term in ("token", "password", "authorization", "base_url", "timestamp", "body", "image", "database")), f"forbidden committed field: {key}")
                Recorder._forbidden(nested, key)
        elif isinstance(value, list):
            for nested in value:
                Recorder._forbidden(nested, context)
        elif isinstance(value, str):
            require("http://" not in value.lower() and "https://" not in value.lower(), "committed output must not contain base URLs")
            if context not in {"operation_id", "ExcludeProviderIds", "HasAnyProviderId", "HasAnyProviderIds", "HasImdbId", "HasTmdbId", "HasTvdbId"} and context.lower().endswith("id"):
                raise RecorderError("committed output must not contain runtime identifiers")

    @classmethod
    def _validate_corpus(cls, corpus: object) -> None:
        require(isinstance(corpus, dict) and set(corpus) == {"format", "cases"} and corpus.get("format") == EXPECTATION_FORMAT, "invalid itemquery corpus schema")
        cases = corpus.get("cases")
        require(isinstance(cases, list) and cases, "itemquery corpus cases must be a non-empty array")
        names: set[str] = set()
        for case in cases:
            require(isinstance(case, dict) and set(case) == {"name", "operation_id", "family", "request", "expect"}, "invalid itemquery case schema")
            name = case["name"]
            require(isinstance(name, str) and name not in names, "itemquery case names must be unique")
            names.add(name)
            require(case["operation_id"] == "GetItems" and case["family"] == "translator-direct", "invalid itemquery case operation or family")
            request = case["request"]
            require(isinstance(request, dict) and set(request) == {"method", "path", "params"} and request["method"] == "GET" and request["path"] == "/Items" and isinstance(request["params"], dict), "invalid public /Items request")
            for key, value in request["params"].items():
                public_name(key)
                safe_manifest_value(value, key)
            expect = case["expect"]
            require(isinstance(expect, dict) and set(expect) == {"status", "start_index", "total_record_count", "items"}, "invalid itemquery expectation")
            require(expect["status"] == 200 and isinstance(expect["start_index"], int) and expect["start_index"] >= 0 and isinstance(expect["total_record_count"], int) and expect["total_record_count"] >= 0 and isinstance(expect["items"], list), "invalid itemquery pagination expectation")
            identities: set[str] = set()
            for identity in expect["items"]:
                parse_identity(identity)
                require(identity not in identities, "duplicate canonical identity in itemquery expectation")
                identities.add(identity)
        cls._forbidden(corpus)

    @classmethod
    def _validate_fixtures(cls, fixtures: object) -> None:
        require(isinstance(fixtures, dict) and set(fixtures) == {"format", "reference_time", "items", "query_bindings"} and fixtures.get("format") == FIXTURE_FORMAT and isinstance(fixtures.get("reference_time"), str) and isinstance(fixtures.get("query_bindings"), list), "invalid itemquery fixture schema")
        items = fixtures.get("items")
        require(isinstance(items, list) and items, "itemquery fixtures must have items")
        identities: set[str] = set()
        ordered_identities: list[str] = []
        for entry in items:
            require(isinstance(entry, dict) and set(entry) == {"identity", "scalars", "relations", "media", "user_data"}, "invalid itemquery fixture entry")
            identity = entry["identity"]
            parse_identity(identity)
            require(identity not in identities, "itemquery fixtures repeat a canonical identity")
            identities.add(identity)
            ordered_identities.append(identity)
            require(isinstance(entry["scalars"], dict), "itemquery fixture scalars must be an object")
            relations = entry["relations"]
            require(isinstance(relations, dict) and set(relations) == {"parent", "top_parent", "series", "season", "album", "people", "provider_ids", "values"}, "invalid itemquery fixture relations")
            for key in ("parent", "top_parent", "series", "season", "album"):
                value = relations[key]
                require(value is None or isinstance(value, dict) and set(value) == {"$item"}, "fixture item relation must be a symbolic item reference")
                if value is not None:
                    parse_identity(value["$item"])
            require(isinstance(relations["people"], list) and isinstance(relations["provider_ids"], dict), "invalid itemquery fixture people or provider ids")
            values = relations["values"]
            require(isinstance(values, dict) and set(values) == {"genres", "studios", "tags", "artists", "album_artists", "artist_items"}, "invalid itemquery fixture values")
            media = entry["media"]
            require(isinstance(media, dict) and set(media) == {"sources", "streams", "chapters", "image_types"}, "invalid itemquery fixture media")
            require(all(isinstance(media[key], list) for key in media), "itemquery fixture media satellites must be arrays")
            user_data = entry["user_data"]
            require(isinstance(user_data, dict) and set(user_data) == {"user", "key", "values"} and user_data["user"] == "GoldenrecAdmin" and isinstance(user_data["key"], dict) and set(user_data["key"]) == {"$item"} and user_data["key"]["$item"] == identity and isinstance(user_data["values"], dict), "invalid itemquery fixture user data")
        require(ordered_identities == sorted(identities), "itemquery fixtures must be sorted by canonical identity")

    @staticmethod
    def _validate_provenance(provenance: object) -> None:
        required_fields = {
            "format",
            "stock_version",
            "upstream_commit_authority",
            "stock_image_digest",
            "upstream_commit",
            "recorder_sha256",
            "request_manifest_sha256",
            "seed_sha256",
            "provider_policy_sha256",
            "expected_inventory_sha256",
            "fixture_sha256",
            "fixture_manifest_sha256",
        }
        require(isinstance(provenance, dict), "invalid provenance schema")
        missing_fields = sorted(required_fields - set(provenance))
        extra_fields = sorted(set(provenance) - required_fields)
        require(not missing_fields and not extra_fields, f"invalid provenance schema: missing={missing_fields}; extra={extra_fields}")
        require(provenance["format"] == PROVENANCE_FORMAT, "unsupported provenance format")
        require(provenance["stock_version"] == STOCK_VERSION, "provenance stock version does not match the exact pinned stock version")
        require(provenance["upstream_commit_authority"] == "api-parity-source", "provenance upstream commit authority is invalid")
        require(provenance["stock_image_digest"] == STOCK_IMAGE_DIGEST, "provenance stock image digest does not match the pinned stock image")
        require(isinstance(provenance["upstream_commit"], str) and re.fullmatch(r"[0-9a-f]{40}", provenance["upstream_commit"]) is not None and provenance["upstream_commit"] == UPSTREAM_COMMIT, "provenance upstream commit does not match the exact pinned upstream commit")
        for key in ("recorder_sha256", "request_manifest_sha256", "seed_sha256", "provider_policy_sha256", "expected_inventory_sha256", "fixture_manifest_sha256", "fixture_sha256"):
            require(is_sha256(provenance[key]), f"invalid provenance hash: {key}")
        Recorder._forbidden(provenance)

    def _validate_manifest_projection(self, corpus: dict[str, object]) -> None:
        cases = corpus["cases"]
        require(isinstance(cases, list) and len(cases) == len(self.cases.cases), "itemquery corpus case count does not match the bound request manifest")
        for actual, expected in zip(cases, self.cases.cases):
            require(isinstance(actual, dict), "itemquery corpus case is malformed")
            require(
                actual.get("name") == expected.name
                and actual.get("operation_id") == expected.operation_id
                and actual.get("family") == expected.family,
                "itemquery corpus case identity does not match the bound request manifest",
            )
            require(
                actual.get("request") == {"method": "GET", "path": "/Items", "params": expected.params},
                "itemquery corpus request does not match the bound request manifest",
            )

    def _validate_directory(self, golden: Path) -> None:
        require(golden.is_dir(), "golden itemquery directory is missing")
        expected_files = {"itemquery.json", "itemquery-fixtures.json", "provenance.json", "SHA256SUMS"}
        require({path.name for path in golden.iterdir()} == expected_files and all(path.is_file() for path in golden.iterdir()), "golden directory has an invalid file set")
        try:
            corpus = json.loads((golden / "itemquery.json").read_bytes())
            fixtures = json.loads((golden / "itemquery-fixtures.json").read_bytes())
            provenance = json.loads((golden / "provenance.json").read_bytes())
        except (OSError, json.JSONDecodeError) as error:
            raise RecorderError("golden corpus contains invalid JSON") from error
        self._validate_corpus(corpus)
        self._validate_manifest_projection(corpus)
        self._validate_boxset_replacement(corpus)
        self._validate_fixtures(fixtures)
        require({entry["identity"] for entry in fixtures["items"]} == set(self._expected_inventory()), "fixture artifact identities must match the expected inventory")
        self._validate_provenance(provenance)
        require(provenance["fixture_sha256"] == sha256_file(golden / "itemquery-fixtures.json"), "fixture artifact SHA-256 mismatch")
        lines = (golden / "SHA256SUMS").read_text(encoding="utf-8").splitlines()
        expected_lines = []
        for name in ("itemquery-fixtures.json", "itemquery.json", "provenance.json"):
            expected_lines.append(f"{sha256_file(golden / name)}  {name}")
        require(lines == expected_lines, "SHA256SUMS integrity mismatch")

    def _validate_source_hashes(self, golden: Path) -> None:
        try:
            provenance = json.loads((golden / "provenance.json").read_bytes())
        except (OSError, json.JSONDecodeError) as error:
            raise RecorderError("golden provenance contains invalid JSON") from error
        self._validate_provenance(provenance)
        source_labels = {
            "recorder_sha256": "recorder",
            "request_manifest_sha256": "request manifest",
            "seed_sha256": "seed",
            "provider_policy_sha256": "provider policy",
            "expected_inventory_sha256": "expected inventory",
            "fixture_manifest_sha256": "fixture manifest",
        }
        for key, expected in self._source_hashes().items():
            require(provenance[key] == expected, f"{source_labels[key]} SHA-256 mismatch")

    def verify(self) -> None:
        target = self.output_root / GOLDEN_RELATIVE
        self._recover_previous(target)
        self._validate_directory(target)
        self._validate_source_hashes(target)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subcommands = parser.add_subparsers(dest="command", required=True)
    record = subcommands.add_parser("record", help="authenticate locally and record /Items expectations")
    record.add_argument("--base-url", required=True, help="local Jellyfin origin URL")
    record.add_argument("--cases", required=True, type=Path, help="symbolic itemquery request manifest")
    record.add_argument("--output-root", required=True, type=Path, help="Deadfin output root")
    verify = subcommands.add_parser("verify", help="validate a corpus without contacting stock")
    verify.add_argument("--cases", type=Path, default=ROOT / "itemquery-cases.json", help="symbolic itemquery request manifest to bind during offline verification")
    verify.add_argument("--output-root", required=True, type=Path, help="Deadfin output root")
    fixtures = subcommands.add_parser("validate-fixtures", help="validate generated fixtures against the committed source attestation")
    fixtures.add_argument("--fixtures", required=True, type=Path, help="generated fixture root to validate")
    return parser


def main(argv: list[str] | None = None) -> int:
    arguments = build_parser().parse_args(argv)
    try:
        if arguments.command == "record":
            Recorder(JellyfinClient(arguments.base_url), CaseManifest.load(arguments.cases), arguments.output_root).record()
        elif arguments.command == "verify":
            recorder = Recorder.__new__(Recorder)
            recorder.cases = CaseManifest.load(arguments.cases)
            recorder.output_root = arguments.output_root
            recorder.verify()
        else:
            Recorder._validate_live_fixture_manifest(arguments.fixtures)
        return 0
    except RecorderError as error:
        print(f"goldenrec itemquery: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
