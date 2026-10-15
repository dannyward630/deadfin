#!/usr/bin/env bash
# Prepares fresh, comparable stock and Deadfin environments.  This intentionally
# stops before scenario replay: Main owns the decision to run the corpus.
set -Eeuo pipefail
IFS=$'\n\t'

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
REFERENCE_ENV=${GOLDENREC_REFERENCE_ENV_SCRIPT:-"$REPO/tools/goldenrec/reference-env.sh"}
FIXTURES=${GOLDENREC_FIXTURES_DIR:-"$REPO/tools/goldenrec/fixtures/out"}
INVENTORY=${GOLDENREC_INVENTORY:-"$REPO/tools/goldenrec/fixtures/expected-inventory.json"}
ITEM_CASES=${GOLDENREC_ITEM_CASES:-"$REPO/tools/goldenrec/itemquery-cases.json"}
if [[ ${GOLDENREC_FINAL_REPLAY_ROOT+x} ]]; then
  STATE_ROOT=$GOLDENREC_FINAL_REPLAY_ROOT
  STATE_ROOT_FRESH=0
else
  STATE_ROOT=
  STATE_ROOT_FRESH=1
fi
DEADFIN_IMAGE=${DEADFIN_IMAGE:-deadfin:goldenrec-final-replay}
STOCK_PORT=${GOLDENREC_PORT:-18096}
DEADFIN_PORT=${DEADFIN_PORT:-18097}
PREFLIGHT_ADMIN=${JELLYFIN_ADMIN:-GoldenrecAdmin}
PREFLIGHT_PASSWORD=${JELLYFIN_PASSWORD:-goldenrec-change-me}
DISABLED_NAME=GoldenrecDisabled
DISABLED_PASSWORD=${GOLDENREC_DISABLED_PASSWORD:-goldenrec-disabled-change-me}
CREDENTIAL_FILE=
unset JELLYFIN_ADMIN JELLYFIN_PASSWORD GOLDENREC_DISABLED_PASSWORD ADMIN PASSWORD
export -n PREFLIGHT_ADMIN PREFLIGHT_PASSWORD DISABLED_NAME DISABLED_PASSWORD
DEADFIN_NAME="goldenrec-final-replay-deadfin-$DEADFIN_PORT"
DEADFIN_STARTED=0
PREPARED=0

usage() {
  cat <<'EOF'
Usage: tools/goldenrec/final-replay-preflight.sh [--help]

Creates fresh stock and Deadfin state, then verifies their canonical 52-item
inventories and initial authenticated state. It does not replay any corpus.

Optional environment:
  GOLDENREC_FINAL_REPLAY_ROOT     durable disposable workspace (default tools/goldenrec/state/final-replay.*)
  GOLDENREC_REFERENCE_ENV_SCRIPT  stock reset-and-seed command override
  GOLDENREC_FIXTURES_DIR          generated fixture tree (default tools/goldenrec/fixtures/out)
  GOLDENREC_INVENTORY             expected 52-item Type:/media/path JSON list
  GOLDENREC_ITEM_CASES            canonical item request manifest
  GOLDENREC_BOOTSTRAP_BIN         prebuilt finalreplaybootstrap override (optional)
  GOLDENREC_SEED_USER_BIN         prebuilt goldenrec-seed-user override (optional)
  DEADFIN_IMAGE                   local Deadfin output image tag (always rebuilt)
  GOLDENREC_PORT, DEADFIN_PORT    loopback ports (defaults 18096, 18097)
  JELLYFIN_ADMIN, JELLYFIN_PASSWORD, GOLDENREC_DISABLED_PASSWORD
EOF
}
die() { printf 'goldenrec preflight: %s\n' "$*" >&2; exit 1; }
reject_mount_source() {
  [[ "$1" != *,* && "$1" != *$'\n'* ]] || die "Docker bind source cannot contain a comma or newline: $2"
}
prepare_default_state_parent() {
  python3 - "$REPO/tools/goldenrec/state" <<'PY'
import os
import stat
import sys
from pathlib import Path

path = Path(sys.argv[1])
try:
    details = os.lstat(path)
except FileNotFoundError:
    os.mkdir(path, 0o700)
    details = os.lstat(path)
if stat.S_ISLNK(details.st_mode) or not stat.S_ISDIR(details.st_mode):
    raise SystemExit("default replay-state parent must be a real directory")
if details.st_uid != os.geteuid() or details.st_mode & 0o022:
    raise SystemExit("default replay-state parent has unsafe ownership or mode")
print(Path(os.path.realpath(path)))
PY
}
claim_state_root() {
  local claimed
  if ! claimed=$(python3 - "$STATE_ROOT" "$REPO" "$STATE_ROOT_FRESH" <<'PY'
import json
import os
import stat
import sys
from pathlib import Path

requested, repository, fresh = (Path(sys.argv[1]).expanduser(), Path(sys.argv[2]).expanduser(), sys.argv[3] == "1")
if not requested.is_absolute():
    requested = Path.cwd() / requested
requested = Path(os.path.abspath(requested))
repository = Path(os.path.realpath(repository))
dangerous = {Path("/"), Path("/home"), Path("/home/repo"), repository, *repository.parents}
if requested in dangerous:
    raise SystemExit("replay root is a protected path")
try:
    root_stat = os.lstat(requested)
except FileNotFoundError:
    parent_stat = os.lstat(requested.parent)
    if stat.S_ISLNK(parent_stat.st_mode) or not stat.S_ISDIR(parent_stat.st_mode):
        raise SystemExit("replay root parent is not a directory")
    os.mkdir(requested, 0o700)
    fresh = True
    root_stat = os.lstat(requested)
if stat.S_ISLNK(root_stat.st_mode) or not stat.S_ISDIR(root_stat.st_mode):
    raise SystemExit("replay root must be a non-symlink directory")
if root_stat.st_uid != os.geteuid() or root_stat.st_mode & 0o022:
    raise SystemExit("replay root is not exclusively owned by this user")
root = Path(os.path.realpath(requested))
marker = root / ".goldenrec-final-replay-owner"
expected = {"format": "goldenrec-final-replay-owner/v1", "uid": os.geteuid(), "root": str(root)}
encoded = (json.dumps(expected, separators=(",", ":")) + "\n").encode()
try:
    marker_stat = os.lstat(marker)
except FileNotFoundError:
    if not fresh and any(root.iterdir()):
        raise SystemExit("replay root is unclaimed")
    fresh = True
    try:
        descriptor = os.open(marker, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        marker_stat = os.lstat(marker)
    else:
        with os.fdopen(descriptor, "wb") as output:
            output.write(encoded)
        os.chmod(marker, 0o600)
        marker_stat = os.lstat(marker)
if stat.S_ISLNK(marker_stat.st_mode) or not stat.S_ISREG(marker_stat.st_mode):
    raise SystemExit("replay root ownership marker is not a regular file")
if marker_stat.st_uid != os.geteuid() or stat.S_IMODE(marker_stat.st_mode) != 0o600:
    raise SystemExit("replay root ownership marker has unsafe ownership or mode")
try:
    actual = json.loads(marker.read_bytes())
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit("replay root ownership marker is invalid") from error
if actual != expected:
    raise SystemExit("replay root ownership marker does not belong to this root")
print(root)
PY
); then
    die 'could not safely claim GOLDENREC_FINAL_REPLAY_ROOT'
  fi
  STATE_ROOT=$claimed
}
[[ ${1:-} != --help ]] || { usage; exit 0; }
[[ $# -eq 0 ]] || { usage >&2; exit 64; }
for command in docker python3; do command -v "$command" >/dev/null 2>&1 || die "$command is required"; done
[[ -x "$REFERENCE_ENV" ]] || die "stock reference command is not executable: $REFERENCE_ENV"
[[ -f "$INVENTORY" ]] || die "inventory is not a file: $INVENTORY"
[[ -f "$ITEM_CASES" ]] || die "item-case manifest is not a file: $ITEM_CASES"
[[ "$STOCK_PORT" =~ ^[1-9][0-9]{0,4}$ ]] && (( STOCK_PORT <= 65535 )) || die "GOLDENREC_PORT must be a TCP port"
[[ "$DEADFIN_PORT" =~ ^[1-9][0-9]{0,4}$ ]] && (( DEADFIN_PORT <= 65535 )) || die "DEADFIN_PORT must be a TCP port"
[[ "$STOCK_PORT" != "$DEADFIN_PORT" ]] || die 'stock and Deadfin ports must differ'

cleanup_on_failure() {
  local status=$?
  if (( PREPARED == 0 && DEADFIN_STARTED == 1 )); then
    docker rm -f "$DEADFIN_NAME" >/dev/null 2>&1 || true
  fi
  [[ -z "$CREDENTIAL_FILE" ]] || rm -f -- "$CREDENTIAL_FILE"
  return "$status"
}
trap cleanup_on_failure EXIT
if (( STATE_ROOT_FRESH )); then
  DEFAULT_STATE_PARENT=$(prepare_default_state_parent) || die 'could not prepare the default replay-state parent'
  STATE_ROOT=$(mktemp -d "$DEFAULT_STATE_PARENT/final-replay.XXXXXX")
  STATE_ROOT=$(cd -P "$STATE_ROOT" && pwd -P)
fi

reject_mount_source "$STATE_ROOT" state-root
claim_state_root
DEADFIN_STATE="$STATE_ROOT/deadfin-data"
BOOTSTRAP_BIN=${GOLDENREC_BOOTSTRAP_BIN:-}
SEED_USER_BIN=${GOLDENREC_SEED_USER_BIN:-}
INVENTORY=$(cd "$(dirname "$INVENTORY")" && pwd)/$(basename "$INVENTORY")
ITEM_CASES=$(cd "$(dirname "$ITEM_CASES")" && pwd)/$(basename "$ITEM_CASES")
reject_mount_source "$DEADFIN_STATE" deadfin-state
[[ -z "$BOOTSTRAP_BIN" ]] || reject_mount_source "$BOOTSTRAP_BIN" bootstrap-helper
[[ -z "$SEED_USER_BIN" ]] || reject_mount_source "$SEED_USER_BIN" seed-user-helper
# The replay corpus is source-bound: a syntactically plausible manifest is not
# enough unless its golden/provenance tree and emitted scenario match exactly.
python3 - "$ITEM_CASES" <<'PY'
import json
import sys
from pathlib import Path

MINIMUM_SEMANTIC_REQUESTS = 200

def validate_cases(cases):
    if not isinstance(cases, list):
        raise SystemExit("item request manifest cases must be an array")
    names = [case.get("name") for case in cases if isinstance(case, dict)]
    if len(names) != len(cases) or any(not isinstance(name, str) or not name for name in names) or len(set(names)) != len(names):
        raise SystemExit(f"item request manifest must contain {len(cases)} unique nonempty names")
    semantic_requests = {}
    for case in cases:
        if set(case) != {"name", "operation_id", "family", "params"}:
            raise SystemExit("item request manifest case has an invalid schema")
        if case["operation_id"] != "GetItems" or case["family"] != "translator-direct" or not isinstance(case["params"], dict):
            raise SystemExit("item request manifest case has an unsupported request shape")
        params = {}
        for key, value in case["params"].items():
            if not isinstance(key, str) or not key:
                raise SystemExit("item request manifest parameter names must be nonempty strings")
            key = key[0].upper() + key[1:]
            if key in params:
                raise SystemExit("item request manifest case repeats a canonical parameter name")
            params[key] = value
        if "Random" in params.get("SortBy", []):
            raise SystemExit("item request manifest must exclude nondeterministic Random cases")
        semantic = json.dumps({"method": "GET", "path": "/Items", "params": params}, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False)
        prior = semantic_requests.get(semantic)
        if prior is not None:
            raise SystemExit(f"item request manifest repeats a duplicate semantic request: {prior}, {case['name']}")
        semantic_requests[semantic] = case["name"]
    if len(semantic_requests) < MINIMUM_SEMANTIC_REQUESTS:
        raise SystemExit(f"item request manifest must contain at least {MINIMUM_SEMANTIC_REQUESTS} unique semantic requests")

try:
    manifest = json.loads(Path(sys.argv[1]).read_bytes())
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit(f"item request manifest is invalid: {error}")
validate_cases(manifest.get("cases") if isinstance(manifest, dict) else None)
PY
# The successful server intentionally remains running for replay. Remove only
# this port's deterministically named prior instance before replacing its state.
docker rm -f "$DEADFIN_NAME" >/dev/null 2>&1 || true
[[ ! -L "$DEADFIN_STATE" && ( ! -e "$DEADFIN_STATE" || -d "$DEADFIN_STATE" ) ]] || die "Deadfin state must be absent or a non-symlink directory: $DEADFIN_STATE"
rm -rf "$DEADFIN_STATE"
mkdir "$DEADFIN_STATE"
chmod 700 "$DEADFIN_STATE"
python3 - "$DEADFIN_STATE" <<'PY'
import os
import stat
import sys

state = sys.argv[1]
details = os.lstat(state)
if stat.S_ISLNK(details.st_mode) or not stat.S_ISDIR(details.st_mode):
    raise SystemExit("Deadfin state is not a real directory")
if details.st_uid != os.geteuid() or stat.S_IMODE(details.st_mode) != 0o700:
    raise SystemExit("Deadfin state has unsafe ownership or mode")
PY

# The stock helper recreates its named configuration volume before it authenticates
# as the seed device. It is an explicit preflight dependency so stale PostCapabilities
# or DisplayPreferences cannot become the first corpus read.
# The stock seed receives the credential path, never credentials in its argv or
# environment. It constructs HTTP bodies from the mode-0600 file and streams them.
CREDENTIAL_FILE=$(mktemp "$STATE_ROOT/.goldenrec-credentials.XXXXXX")
chmod 600 "$CREDENTIAL_FILE"
reject_mount_source "$CREDENTIAL_FILE" credential-file
printf '%s\0%s\0%s\0%s\0' "$PREFLIGHT_ADMIN" "$PREFLIGHT_PASSWORD" "$DISABLED_NAME" "$DISABLED_PASSWORD" | python3 - "$CREDENTIAL_FILE" 3<&0 <<'PY'
import json
import os
import sys

path = sys.argv[1]
parts = os.fdopen(3, "rb").read().split(b"\0")
if len(parts) != 5 or parts[-1] or any(not value for value in parts[:4]):
    raise SystemExit("credential input is invalid")
try:
    admin_name, admin_password, disabled_name, disabled_password = (value.decode("utf-8") for value in parts[:4])
except UnicodeDecodeError as error:
    raise SystemExit("credential input is not UTF-8") from error
with open(path, "w", encoding="utf-8") as output:
    json.dump({
        "admin_name": admin_name,
        "admin_password": admin_password,
        "disabled_name": disabled_name,
        "disabled_password": disabled_password,
    }, output, separators=(",", ":"))
    output.write("\n")
os.chmod(path, 0o600)
PY
env -u JELLYFIN_ADMIN -u JELLYFIN_PASSWORD -u GOLDENREC_DISABLED_PASSWORD \
GOLDENREC_CREDENTIAL_FILE="$CREDENTIAL_FILE" \
GOLDENREC_PORT="$STOCK_PORT" \
JELLYFIN_BASE_URL="http://127.0.0.1:$STOCK_PORT" \
"$REFERENCE_ENV"

# The reference reset-and-seed creates the ignored fixture tree plus the stock
# snapshot and identifier map consumed by the corpus verifiers below.
[[ -d "$FIXTURES" ]] || die "fixture tree is not a directory: $FIXTURES"
FIXTURES=$(cd -P "$FIXTURES" && pwd -P)
reject_mount_source "$FIXTURES" fixtures
# The server can mount only a fixture tree that exactly matches the committed
# source attestation; validate this actual generated root before any verifier or
# Deadfin runtime can consume it.
python3 "$REPO/tools/goldenrec/record-itemquery.py" validate-fixtures --fixtures "$FIXTURES"
python3 "$REPO/tools/goldenrec/record-itemquery.py" verify --cases "$ITEM_CASES" --output-root "$REPO/deadfin"
python3 "$REPO/tools/goldenrec/emit-item-corpus.py" --check --requests "$ITEM_CASES" --output "$REPO/corpora/items.yaml"

docker build -t "$DEADFIN_IMAGE" "$REPO/deadfin"
DEADFIN_ARCH=$(docker image inspect --format '{{.Architecture}}' "$DEADFIN_IMAGE")
case "$DEADFIN_ARCH" in
  amd64|arm64) ;;
  *) die "Deadfin image architecture is unsupported by the replay helpers: $DEADFIN_ARCH" ;;
esac

# Replay helpers must use Deadfin's packaged Linux native image stack rather
# than a host cross-build that silently loses cgo-backed image providers.
# Explicit executables remain an intentional fake-runner seam for script tests.
if [[ ${GOLDENREC_BOOTSTRAP_BIN+x} ]]; then
  [[ -x "$BOOTSTRAP_BIN" ]] || die "bootstrap helper override is not executable: $BOOTSTRAP_BIN"
fi
if [[ ${GOLDENREC_SEED_USER_BIN+x} ]]; then
  [[ -x "$SEED_USER_BIN" ]] || die "seed-user helper override is not executable: $SEED_USER_BIN"
fi

REPLAY_TOOLS_IMAGE="${DEADFIN_IMAGE}-replay-tools"
docker build --platform "linux/$DEADFIN_ARCH" --target replay-tools --tag "$REPLAY_TOOLS_IMAGE" "$REPO/deadfin"

BOOTSTRAP_HELPER_MOUNT=()
if [[ ${GOLDENREC_BOOTSTRAP_BIN+x} ]]; then
  BOOTSTRAP_HELPER_MOUNT=(--mount "type=bind,src=$BOOTSTRAP_BIN,dst=/usr/local/bin/finalreplaybootstrap,readonly")
fi
SEED_USER_HELPER_MOUNT=()
if [[ ${GOLDENREC_SEED_USER_BIN+x} ]]; then
  SEED_USER_HELPER_MOUNT=(--mount "type=bind,src=$SEED_USER_BIN,dst=/usr/local/bin/goldenrec-seed-user,readonly")
fi
CREDENTIAL_MOUNT=(--mount "type=bind,src=$CREDENTIAL_FILE,dst=/run/secrets/goldenrec.json,readonly")

# Run both state-changing helpers with the exact runtime assets and read-only
# /media mount the server will receive. This prevents host fixture paths from
# entering item records while retaining executable override seams for tests.
CONTAINER_USER="$(id -u):$(id -g)"
docker run --rm --user "$CONTAINER_USER" \
  --mount "type=bind,src=$DEADFIN_STATE,dst=/state" \
  --mount "type=bind,src=$FIXTURES,dst=/media,readonly" \
  "${BOOTSTRAP_HELPER_MOUNT[@]}" \
  --entrypoint /usr/local/bin/finalreplaybootstrap "$REPLAY_TOOLS_IMAGE" -state /state

docker run --rm --user "$CONTAINER_USER" \
  --mount "type=bind,src=$DEADFIN_STATE,dst=/state" \
  "${SEED_USER_HELPER_MOUNT[@]}" \
  "${CREDENTIAL_MOUNT[@]}" \
  --entrypoint /usr/local/bin/goldenrec-seed-user "$REPLAY_TOOLS_IMAGE" \
  -program-data /state -credentials-file /run/secrets/goldenrec.json

docker run --detach --name "$DEADFIN_NAME" --user "$CONTAINER_USER" \
  --publish "127.0.0.1:$DEADFIN_PORT:8096" \
  --env JELLYFIN_DATA_DIR=/state \
  --mount "type=bind,src=$DEADFIN_STATE,dst=/state" \
  --mount "type=bind,src=$FIXTURES,dst=/media,readonly" \
  "$DEADFIN_IMAGE" --program-data /state >/dev/null
DEADFIN_STARTED=1

python3 - "$INVENTORY" "$ITEM_CASES" "$CREDENTIAL_FILE" "http://127.0.0.1:$STOCK_PORT" "http://127.0.0.1:$DEADFIN_PORT" <<'PY'
import json
import sys
import time
from pathlib import Path
from urllib.error import URLError
from urllib.request import Request, urlopen

inventory_path, cases_path, credential_path, stock_base, deadfin_base = sys.argv[1:]
expected_inventory = json.loads(Path(inventory_path).read_text(encoding="utf-8"))
if not isinstance(expected_inventory, list) or len(expected_inventory) != 52 or len(set(expected_inventory)) != 52:
    raise SystemExit("expected inventory must contain exactly 52 unique canonical identities")
try:
    manifest = json.loads(Path(cases_path).read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit(f"item request manifest is invalid: {error}")
cases = manifest.get("cases") if isinstance(manifest, dict) else None
if not isinstance(cases, list):
    raise SystemExit("item request manifest cases must be an array")
names = [case.get("name") for case in cases if isinstance(case, dict)]
if len(names) != len(cases) or any(not isinstance(name, str) or not name for name in names) or len(set(names)) != len(names):
    raise SystemExit(f"item request manifest must contain {len(cases)} unique nonempty names")
semantic_requests = {}
for case in cases:
    if set(case) != {"name", "operation_id", "family", "params"}:
        raise SystemExit("item request manifest case has an invalid schema")
    if case["operation_id"] != "GetItems" or case["family"] != "translator-direct" or not isinstance(case["params"], dict):
        raise SystemExit("item request manifest case has an unsupported request shape")
    params = {}
    for key, value in case["params"].items():
        if not isinstance(key, str) or not key:
            raise SystemExit("item request manifest parameter names must be nonempty strings")
        key = key[0].upper() + key[1:]
        if key in params:
            raise SystemExit("item request manifest case repeats a canonical parameter name")
        params[key] = value
    if "Random" in params.get("SortBy", []):
        raise SystemExit("item request manifest must exclude nondeterministic Random cases")
    semantic = json.dumps({"method": "GET", "path": "/Items", "params": params}, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False)
    prior = semantic_requests.get(semantic)
    if prior is not None:
        raise SystemExit(f"item request manifest repeats a duplicate semantic request: {prior}, {case['name']}")
    semantic_requests[semantic] = case["name"]
if len(semantic_requests) < 200:
    raise SystemExit("item request manifest must contain at least 200 unique semantic requests")
try:
    credentials = json.loads(Path(credential_path).read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit(f"credential file is invalid: {error}")
required_credentials = {"admin_name", "admin_password", "disabled_name", "disabled_password"}
if not isinstance(credentials, dict) or set(credentials) != required_credentials or any(
    not isinstance(credentials[key], str) or not credentials[key] for key in required_credentials
):
    raise SystemExit("credential file has an invalid schema")
admin = credentials["admin_name"]
password = credentials["admin_password"]
AUTHORIZATION = 'MediaBrowser Client="Goldenrec preflight", Device="Goldenrec preflight", DeviceId="goldenrec-preflight-v1", Version="1.0.0"'

def request(url, method="GET", payload=None, headers=None):
    data = None if payload is None else json.dumps(payload, separators=(",", ":")).encode()
    request_headers = {"Accept": "application/json"}
    if payload is not None:
        request_headers["Content-Type"] = "application/json"
    if headers:
        request_headers.update(headers)
    with urlopen(Request(url, data=data, method=method, headers=request_headers), timeout=5) as response:
        return json.loads(response.read())

def login(base):
    last = None
    for _ in range(60):
        try:
            return request(
                base + "/Users/AuthenticateByName",
                "POST",
                {"Username": admin, "Pw": password},
                {"Authorization": AUTHORIZATION},
            )
        except (URLError, TimeoutError, json.JSONDecodeError) as error:
            last = error
            time.sleep(1)
    raise SystemExit(f"{base}: login did not become ready: {last}")

def initial_state(base):
    authenticated = login(base)
    try:
        token = authenticated["AccessToken"]
        user = authenticated["User"]
        session = authenticated["SessionInfo"]
    except (KeyError, TypeError) as error:
        raise SystemExit(f"{base}: login response omitted initial state: {error}") from error
    display = request(
        base + "/DisplayPreferences/usersettings?client=GoldenrecPreflight",
        headers={"Authorization": f'{AUTHORIZATION}, Token="{token}"'},
    )
    capabilities = session.get("Capabilities")
    if not isinstance(capabilities, dict):
        raise SystemExit(f"{base}: initial session omitted capabilities")
    policy = user.get("Policy")
    configuration = user.get("Configuration")
    if not isinstance(policy, dict) or not isinstance(configuration, dict):
        raise SystemExit(f"{base}: initial user omitted policy or configuration")
    expected_policy = {
        "IsAdministrator": True,
        "IsHidden": True,
        "IsDisabled": False,
        "EnableContentDeletion": True,
        "EnableRemoteControlOfOtherUsers": True,
    }
    for key, expected in expected_policy.items():
        if policy.get(key) != expected:
            raise SystemExit(f"{base}: admin policy {key}={policy.get(key)!r}, expected {expected!r}")
    return {"policy": policy, "configuration": configuration, "display": display, "capabilities": capabilities}

def canonical_inventory(base, token):
    data = request(
        base + "/Items?Recursive=true&Fields=Path&EnableTotalRecordCount=true&Limit=1000",
        headers={"Authorization": f'{AUTHORIZATION}, Token="{token}"'},
    )
    items = data.get("Items")
    if not isinstance(items, list):
        raise SystemExit(f"{base}: item inventory response omitted Items")
    if len(items) != 52:
        raise SystemExit(f"{base}: item inventory response must contain exactly 52 rows")
    identities = []
    for item in items:
        if not isinstance(item, dict):
            raise SystemExit(f"{base}: item inventory row must be an object")
        item_type = item.get("Type")
        path = item.get("Path")
        if not isinstance(item_type, str) or not isinstance(path, str):
            raise SystemExit(f"{base}: item inventory row omitted a string canonical identity")
        identities.append(f"{item_type}:{path}")
    actual = set(identities)
    if len(actual) != 52:
        raise SystemExit(f"{base}: item inventory response repeats a canonical identity")
    expected = set(expected_inventory)
    if actual != expected:
        missing = sorted(expected - actual)
        extra = sorted(actual - expected)
        raise SystemExit(f"{base}: canonical inventory mismatch; missing={missing!r} extra={extra!r}")
    return len(actual)

stock = initial_state(stock_base)
deadfin = initial_state(deadfin_base)
if stock != deadfin:
    raise SystemExit("initial hidden-admin policy/configuration/display-preference/session-capability state differs")
stock_login = login(stock_base)
deadfin_login = login(deadfin_base)
stock_count = canonical_inventory(stock_base, stock_login["AccessToken"])
deadfin_count = canonical_inventory(deadfin_base, deadfin_login["AccessToken"])
print(f"preflight: stock inventory: {stock_count} canonical items")
print(f"preflight: Deadfin inventory: {deadfin_count} canonical items")
print("preflight: initial hidden-admin policy/configuration/display-preference/session-capability state matches")
print(f"preflight: item manifest: {len(semantic_requests)} unique deterministic cases")
PY
rm -f -- "$CREDENTIAL_FILE"
CREDENTIAL_FILE=

PREPARED=1
trap - EXIT
printf 'preflight complete; stock=http://127.0.0.1:%s deadfin=http://127.0.0.1:%s state=%s\n' "$STOCK_PORT" "$DEADFIN_PORT" "$STATE_ROOT"
