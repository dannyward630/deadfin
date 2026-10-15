#!/usr/bin/env bash
# Replays JSON-as-YAML goldenrec scenarios through curl; no YAML package is required.
set -Eeuo pipefail
IFS=$'\n\t'
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
usage() {
  cat <<'EOF'
Usage: tools/goldenrec/replay.sh SCENARIO.yaml

Replays a goldenrec-scenario/v1 against JELLYFIN_BASE_URL (default
http://127.0.0.1:8096). Scenarios are JSON formatted so they are valid YAML without
requiring a parser dependency. It fails on every unexpected status, malformed
scenario, missing required JSON key, or failed exact JSON field assertion.
EOF
}
[[ ${1:-} != --help ]] || { usage; exit 0; }
[[ $# -eq 1 ]] || { usage >&2; exit 64; }
command -v curl >/dev/null 2>&1 || { printf 'goldenrec replay: curl is required\n' >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { printf 'goldenrec replay: python3 is required\n' >&2; exit 1; }
SCENARIO=$1
[[ -f "$SCENARIO" ]] || { printf 'goldenrec replay: scenario not found: %s\n' "$SCENARIO" >&2; exit 1; }
TMP=$(mktemp -d "${TMPDIR:-/tmp}/goldenrec-replay.XXXXXX")
trap 'rm -rf "$TMP"' EXIT
python3 - "$SCENARIO" "$TMP" <<'PY'
import json, os, re, subprocess, sys
from pathlib import Path
from urllib.parse import urlsplit

scenario_path, tmp = map(Path, (sys.argv[1], sys.argv[2]))
try:
    scenario = json.loads(scenario_path.read_text(encoding="utf-8"))
except json.JSONDecodeError as exc:
    raise SystemExit(f"{scenario_path}: scenarios must use JSON syntax (valid YAML): {exc}") from exc
if scenario.get("format") != "goldenrec-scenario/v1" or not isinstance(scenario.get("steps"), list):
    raise SystemExit(f"{scenario_path}: expected goldenrec-scenario/v1 with steps array")
base_url_env = scenario.get("base_url_env")
if not isinstance(base_url_env, str) or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", base_url_env):
    raise SystemExit(f"{scenario_path}: base_url_env must be a valid environment-variable name")
if base_url_env == "JELLYFIN_BASE_URL":
    base = os.environ.get(base_url_env, "http://127.0.0.1:8096")
else:
    base = os.environ.get(base_url_env)
    if not base:
        raise SystemExit(f"{scenario_path}: required base URL environment variable {base_url_env} is unset")
parsed_base = urlsplit(base)
if parsed_base.scheme not in {"http", "https"} or not parsed_base.netloc:
    raise SystemExit(f"{scenario_path}: {base_url_env} must be an absolute http(s) URL")
base = base.rstrip("/")
for index, step in enumerate(scenario["steps"], 1):
    if not isinstance(step, dict) or not isinstance(step.get("method"), str) or not isinstance(step.get("path"), str):
        raise SystemExit(f"step {index}: method and path are required strings")
    expect = step.get("expect", {})
    status = expect.get("status")
    if not isinstance(status, int):
        raise SystemExit(f"step {index}: expect.status must be an integer")
    body = tmp / f"{index}.body"
    command = ["curl", "-sS", "--connect-timeout", "5", "--max-time", "30", "-o", str(body), "-w", "%{http_code}", "-X", step["method"]]
    for name, value in step.get("headers", {}).items():
        command += ["-H", f"{name}: {value}"]
    if "body" in step:
        command += ["-H", "Content-Type: application/json", "--data", json.dumps(step["body"], separators=(",", ":"))]
    command.append(base + step["path"])
    result = subprocess.run(command, capture_output=True, text=True)
    if result.returncode:
        raise SystemExit(f"step {index} ({step.get('name', step['path'])}): curl failed: {result.stderr.strip()}")
    actual = int(result.stdout)
    if actual != status:
        detail = body.read_text(encoding="utf-8", errors="replace")[:500].replace("\n", " ")
        raise SystemExit(f"step {index} ({step.get('name', step['path'])}): expected HTTP {status}, got {actual}: {detail}")
    if expect.get("required_json_keys") or expect.get("json_fields"):
        try:
            payload = json.loads(body.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            raise SystemExit(f"step {index}: expected JSON response: {exc}") from exc
        for key in expect.get("required_json_keys", []):
            if key not in payload:
                raise SystemExit(f"step {index}: response is missing required JSON key {key!r}")
        for key, expected in expect.get("json_fields", {}).items():
            if payload.get(key) != expected:
                raise SystemExit(f"step {index}: JSON field {key!r} was {payload.get(key)!r}, expected {expected!r}")
    print(f"ok {index}/{len(scenario['steps'])}: {step.get('name', step['path'])}")
PY
