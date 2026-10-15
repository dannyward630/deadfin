#!/usr/bin/env python3
"""Convert goldenrec's sanitized mitmproxy JSONL format into JSON-as-YAML scenarios.

The emitted format is valid YAML 1.2 because JSON is a YAML subset. This deliberately
skips authenticated and body-bearing requests: credentials are scrubbed from captures,
and an automated replay must not pretend to recreate a human login session.
"""
import argparse
import json
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("input", type=Path, help="sanitized .jsonl written by sanitize.py")
parser.add_argument("output", type=Path, help="scenario .yaml to create")
parser.add_argument("--name", default=None, help="scenario name (default: input stem)")
args = parser.parse_args()

steps = []
for line_number, line in enumerate(args.input.read_text(encoding="utf-8").splitlines(), 1):
    try:
        record = json.loads(line)
    except json.JSONDecodeError as exc:
        raise SystemExit(f"{args.input}:{line_number}: invalid sanitized JSONL: {exc}") from exc
    method, path, status = record.get("method"), record.get("path"), record.get("status")
    if method not in {"GET", "HEAD"} or not isinstance(path, str) or not isinstance(status, int):
        continue
    # Only unauthenticated public requests are mechanically replayable. Authorization
    # material is intentionally removed by sanitize.py; auth_role reserves a safe seam
    # for a future explicit authenticated-scenario format without replaying it as public.
    if record.get("requires_auth") or record.get("auth_role") or path.startswith("/Users/Authenticate"):
        continue
    request_headers = record.get("request_headers")
    if not isinstance(request_headers, dict):
        request_headers = {}
    if any(str(name).lower() == "upgrade" and str(value).lower() == "websocket" for name, value in request_headers.items()):
        continue
    safe_headers = {
        str(name): str(value)
        for name, value in request_headers.items()
        if str(name).lower() in {"accept", "content-type"}
    }
    step = {
        "name": f"captured-{len(steps) + 1}",
        "operation_id": f"SHADOW:{method}:{path.partition('?')[0]}",
        "method": method,
        "path": path,
        "expect": {"status": status},
    }
    if safe_headers:
        step["headers"] = safe_headers
    steps.append(step)

if not steps:
    raise SystemExit("no safe GET/HEAD requests were eligible; choose a public stock request or create an explicit auth scenario")
scenario = {
    "format": "goldenrec-scenario/v1",
    "name": args.name or args.input.stem.replace(".sanitized", ""),
    "base_url_env": "JELLYFIN_BASE_URL",
    "steps": steps,
}
args.output.parent.mkdir(parents=True, exist_ok=True)
args.output.write_text(json.dumps(scenario, indent=2, sort_keys=False) + "\n", encoding="utf-8")
print(f"wrote {len(steps)} replayable request(s) to {args.output}")
