#!/usr/bin/env python3
"""Compare NFO-backed library projections by (kind, path).

The comparator intentionally ignores database IDs and volatile dates.  It accepts
stock `/Items` JSON and a Deadfin projection JSON produced by a focused scanner
harness, then reports only durable local-provider fields.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

FIELDS = ("name", "productionYear", "genres", "people", "providerIds", "images")


def first(value: dict[str, Any], *names: str, default: Any = None) -> Any:
    for name in names:
        if name in value:
            return value[name]
    return default


def normalize_people(values: Any) -> list[dict[str, str]]:
    if not isinstance(values, list):
        return []
    out = []
    for value in values:
        if isinstance(value, dict):
            out.append({"name": str(first(value, "Name", "name", default="")), "role": str(first(value, "Role", "role", default="")), "type": str(first(value, "Type", "type", default=""))})
    return sorted(out, key=lambda value: (value["name"], value["role"], value["type"]))


def normalize_images(value: Any) -> list[dict[str, Any]]:
    if isinstance(value, dict):
        return sorted(({"type": str(kind), "tag": str(tag)} for kind, tag in value.items()), key=lambda image: (image["type"], image["tag"]))
    if not isinstance(value, list):
        return []
    out = []
    for image in value:
        if isinstance(image, dict):
            out.append({"type": str(first(image, "Type", "type", default="")), "path": str(first(image, "Path", "path", default="")), "index": first(image, "Index", "index", default=0)})
    return sorted(out, key=lambda image: (image["type"], image.get("index", 0), image.get("path", image.get("tag", ""))))


def projection(item: dict[str, Any]) -> tuple[tuple[str, str], dict[str, Any]]:
    kind = str(first(item, "Type", "type", "Kind", "kind", default=""))
    path = str(first(item, "Path", "path", default=""))
    if not kind or not path:
        raise ValueError(f"item lacks kind/path: {item!r}")
    provider_ids = first(item, "ProviderIds", "providerIds", default={})
    return (kind, path), {
        "name": first(item, "Name", "name"),
        "productionYear": first(item, "ProductionYear", "productionYear"),
        "genres": sorted(str(value) for value in first(item, "Genres", "genres", default=[])),
        "people": normalize_people(first(item, "People", "people", default=[])),
        "providerIds": dict(sorted((str(key).lower(), str(value)) for key, value in (provider_ids if isinstance(provider_ids, dict) else {}).items())),
        "images": normalize_images(first(item, "ImageInfos", "imageInfos", "ImageTags", "imageTags", default=[])),
    }


def load(path: Path, prefix: tuple[str, str] | None = None) -> dict[tuple[str, str], dict[str, Any]]:
    raw = json.loads(path.read_text(encoding="utf-8"))
    items = raw.get("Items", raw.get("items", raw)) if isinstance(raw, dict) else raw
    if not isinstance(items, list):
        raise ValueError(f"{path}: expected list or Items array")
    out: dict[tuple[str, str], dict[str, Any]] = {}
    for item in items:
        if isinstance(item, dict):
            key, value = projection(item)
            if prefix is not None and key[1].startswith(prefix[0]):
                key = (key[0], prefix[1] + key[1][len(prefix[0]):])
            out[key] = value
    return out


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("stock", type=Path)
    parser.add_argument("deadfin", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--deadfin-path-prefix", metavar="HOST=STOCK", help="rewrite Deadfin fixture root to stock's mount path")
    args = parser.parse_args()
    prefix = None
    if args.deadfin_path_prefix:
        host, separator, stock_root = args.deadfin_path_prefix.partition("=")
        if not separator or not host or not stock_root:
            parser.error("--deadfin-path-prefix must be HOST=STOCK")
        prefix = (host, stock_root)
    stock, deadfin = load(args.stock), load(args.deadfin, prefix)
    differences = []
    for key in sorted(set(stock) | set(deadfin)):
        if key not in stock:
            differences.append({"key": key, "missing": "stock"})
        elif key not in deadfin:
            differences.append({"key": key, "missing": "deadfin"})
        elif stock[key] != deadfin[key]:
            differences.append({"key": key, "stock": stock[key], "deadfin": deadfin[key]})
    report = {"compared": len(set(stock) & set(deadfin)), "differences": differences}
    payload = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(payload, encoding="utf-8")
    else:
        sys.stdout.write(payload)
    return 1 if differences else 0


if __name__ == "__main__":
    raise SystemExit(main())
