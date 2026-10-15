#!/usr/bin/env python3
"""Project attested goldenrec /Items requests into difftool scenarios."""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parent
REQUESTS = ROOT / "itemquery-cases.json"
ITEM_MAP = ROOT / "state" / "snapshots" / "id-map.json"
EXPECTATIONS = ROOT.parent.parent / "deadfin" / "internal" / "store" / "storetest" / "testdata" / "golden" / "itemquery.json"
OUTPUT = ROOT.parent.parent / "corpora" / "items.yaml"
NORMALIZERS = [
    "item-id-map",
    "server-id",
    "entity-tags",
    "item-created-dates",
    "user-data-keys",
    "response-time-headers",
    "response-time-ms-header",
    "process-time-headers",
    "date-header",
    "server-stack-header",
    "content-length-header",
    "content-language-header",
    "vary-header",
]


def read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_bytes())
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError(f"cannot read JSON {path}: {error}") from error


def item_ids(entries: Any) -> dict[str, str]:
    if not isinstance(entries, list) or not entries:
        raise ValueError("snapshot item map must be a non-empty array")
    resolved: dict[str, str] = {}
    for entry in entries:
        if not isinstance(entry, dict):
            raise ValueError("snapshot item map entry must be an object")
        item_id, item_type, path = entry.get("Id"), entry.get("Type"), entry.get("Path")
        if not all(isinstance(value, str) and value for value in (item_id, item_type, path)):
            raise ValueError("snapshot item map entry requires Id, Type, and Path strings")
        identity = f"{item_type}:{path}"
        if identity in resolved:
            raise ValueError(f"snapshot item map repeats {identity!r}")
        resolved[identity] = item_id
    return resolved


def public_name(name: str) -> str:
    if not isinstance(name, str) or not name:
        raise ValueError("request parameter name must be a non-empty string")
    return name[0].upper() + name[1:]


def resolve_item_refs(value: Any, ids: dict[str, str]) -> Any:
    if isinstance(value, dict):
        if set(value) == {"$item"}:
            identity = value["$item"]
            if not isinstance(identity, str) or identity not in ids:
                raise ValueError(f"unresolved canonical item reference {identity!r}")
            return ids[identity]
        return {key: resolve_item_refs(entry, ids) for key, entry in value.items()}
    if isinstance(value, list):
        return [resolve_item_refs(entry, ids) for entry in value]
    return value


def project(requests: Any, snapshot: Any, expectations: Any) -> dict[str, Any]:
    if not isinstance(requests, dict) or requests.get("format") != "goldenrec-itemquery-requests/v1":
        raise ValueError("unsupported request manifest")
    request_cases = requests.get("cases")
    if not isinstance(expectations, dict) or expectations.get("format") != "goldenrec-itemquery-expectations/v1":
        raise ValueError("unsupported expectation manifest")
    expectation_cases = expectations.get("cases")
    if not isinstance(request_cases, list) or not request_cases:
        raise ValueError("request manifest has no cases")
    if not isinstance(expectation_cases, list) or len(expectation_cases) != len(request_cases):
        raise ValueError("expectation manifest case count differs from request manifest")
    ids = item_ids(snapshot)
    steps: list[dict[str, Any]] = []
    for index, (request_case, expectation_case) in enumerate(zip(request_cases, expectation_cases), 1):
        if not isinstance(request_case, dict) or not isinstance(expectation_case, dict):
            raise ValueError(f"case {index} must be an object")
        expected_request = {"method": "GET", "path": "/Items", "params": {public_name(key): value for key, value in request_case.get("params", {}).items()}}
        if (request_case.get("name"), request_case.get("operation_id"), request_case.get("family")) != (
            expectation_case.get("name"), expectation_case.get("operation_id"), expectation_case.get("family")
        ) or expectation_case.get("request") != expected_request:
            raise ValueError(f"case {index} request metadata differs from attested expectation")
        if request_case.get("operation_id") != "GetItems" or request_case.get("family") != "translator-direct":
            raise ValueError(f"case {index} is not an attested GetItems translator case")
        name = request_case.get("name")
        params = expected_request["params"]
        if not isinstance(name, str) or not isinstance(params, dict):
            raise ValueError(f"case {index} has invalid name or params")
        steps.append({
            "name": name,
            "method": "GET",
            "path": "/Items",
            "params": resolve_item_refs(params, ids),
            "headers": {"Accept": "application/json"},
            "auth_role": "admin",
            "normalizers": NORMALIZERS,
            "operation_id": "GetItems",
        })
    return {
        "format": "goldenrec-scenario/v1",
        "name": "attested item query matrix",
        "base_url_env": "JELLYFIN_BASE_URL",
        "steps": steps,
    }


def rendered(scenario: dict[str, Any]) -> bytes:
    return (json.dumps(scenario, ensure_ascii=False, indent=2) + "\n").encode()


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--requests", type=Path, default=REQUESTS)
    parser.add_argument("--item-map", type=Path, default=ITEM_MAP)
    parser.add_argument("--expectations", type=Path, default=EXPECTATIONS)
    parser.add_argument("--output", type=Path, default=OUTPUT)
    parser.add_argument("--check", action="store_true", help="require --output to be exact without writing it")
    args = parser.parse_args(argv)
    try:
        output = rendered(project(read_json(args.requests), read_json(args.item_map), read_json(args.expectations)))
        if args.check:
            try:
                existing = args.output.read_bytes()
            except OSError as error:
                raise ValueError(f"cannot read generated corpus {args.output}: {error}") from error
            if existing != output:
                raise ValueError(f"generated corpus differs from {args.output}")
        else:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_bytes(output)
    except ValueError as error:
        print(f"emit-item-corpus: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
