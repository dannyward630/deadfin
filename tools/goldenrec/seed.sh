#!/usr/bin/env bash
# Seeds only the local digest-pinned goldenrec stock container. API shapes are from
# upstream/jellyfin-openapi-stable.json: Startup/*, Users/AuthenticateByName,
# Library/VirtualFolders, Library/Refresh, and Items.
set -Eeuo pipefail
IFS=$'\n\t'

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
BASE=${JELLYFIN_BASE_URL:-http://127.0.0.1:8096}
SEED_ADMIN=${JELLYFIN_ADMIN:-GoldenrecAdmin}
SEED_PASSWORD=${JELLYFIN_PASSWORD:-goldenrec-change-me}
CREDENTIAL_FILE=${GOLDENREC_CREDENTIAL_FILE:-}
unset JELLYFIN_ADMIN JELLYFIN_PASSWORD ADMIN PASSWORD
export -n SEED_ADMIN SEED_PASSWORD
STATE="$ROOT/state"
COMPOSE="$ROOT/compose.yaml"
CLIENT_AUTHORIZATION='Authorization: MediaBrowser Client="Goldenrec", Device="Goldenrec Seed", DeviceId="goldenrec-seed-v1", Version="1.0.0"'
POLICY="$ROOT/provider-policy.json"
INVENTORY="$ROOT/fixtures/expected-inventory.json"
ITEM_PAGE_SIZE=200
MAX_ITEM_PAGES=100
STOCK_VERSION=12.0.0
STOCK_STOPPED=0
TMP=$(mktemp -d "${TMPDIR:-/tmp}/goldenrec-seed.XXXXXX")
cleanup() {
  local status=$?
  if (( STOCK_STOPPED )); then
    docker compose -f "$COMPOSE" start jellyfin >&2 || printf 'goldenrec seed: failed to restart Jellyfin after snapshot failure\n' >&2
  fi
  rm -rf "$TMP"
  return "$status"
}
trap cleanup EXIT
TOKEN=

usage() {
  cat <<'EOF'
Usage: tools/goldenrec/seed.sh [--help]

Waits for stock Jellyfin, completes its first-run wizard when necessary, logs in as
JELLYFIN_ADMIN/JELLYFIN_PASSWORD, creates or validates the Golden Movies, Golden TV,
and Golden Music libraries, starts a scan, waits for synthetic content, and snapshots
stock state under tools/goldenrec/state/snapshots when GOLDENREC_SNAPSHOT is 1 (default).

Safe rerun: matching libraries are retained and refreshed. A completed server that
cannot authenticate with the supplied admin credentials fails without changing state.
Requires curl, python3, and Docker Compose v2. Set JELLYFIN_BASE_URL to target a
non-default local host port.
EOF
}
die() { printf 'goldenrec seed: %s\n' "$*" >&2; exit 1; }
[[ ${1:-} != --help ]] || { usage; exit 0; }
[[ $# -eq 0 ]] || { usage >&2; exit 64; }
command -v curl >/dev/null 2>&1 || die 'curl is required'
command -v python3 >/dev/null 2>&1 || die 'python3 is required'
command -v docker >/dev/null 2>&1 || die 'Docker is required to extract the named-volume snapshot'
docker compose version >/dev/null 2>&1 || die 'Docker Compose v2 is required to extract the named-volume snapshot'
# GOLDENREC_SNAPSHOT controls whether seed.sh extracts the named-volume
# database snapshot after the scan converges. The default (1) preserves
# snapshot creation for tools/goldenrec/reference-env.sh. Set
# GOLDENREC_SNAPSHOT=0 to skip the stop, copy, and restart cycle.
SNAPSHOT=${GOLDENREC_SNAPSHOT-1}
case "$SNAPSHOT" in
  0|1) ;;
  '') die "GOLDENREC_SNAPSHOT must be 0 or 1, got an empty value" ;;
  *) die "GOLDENREC_SNAPSHOT must be 0 or 1, got: $SNAPSHOT" ;;
esac
if ! BASE=$(python3 - "$BASE" <<'PY'
import ipaddress
import sys
from urllib.parse import urlsplit, urlunsplit

raw = sys.argv[1]
try:
    parts = urlsplit(raw)
    if (
        parts.scheme.lower() not in {"http", "https"}
        or parts.username is not None
        or parts.password is not None
        or parts.path not in {"", "/"}
        or parts.query
        or parts.fragment
        or not parts.hostname
        or "%" in parts.hostname
        or parts.netloc.endswith(":")
    ):
        raise ValueError
    port = parts.port
    if port is not None and not 1 <= port <= 65535:
        raise ValueError
    address = ipaddress.ip_address(parts.hostname)
    if not address.is_loopback:
        raise ValueError
except (ValueError, UnicodeError):
    raise SystemExit("JELLYFIN_BASE_URL must be an http/https literal loopback origin with an optional valid port")

host = f"[{address.compressed}]" if address.version == 6 else address.compressed
authority = host if port is None else f"{host}:{port}"
print(urlunsplit((parts.scheme.lower(), authority, "", "", "")))
PY
); then
  die 'JELLYFIN_BASE_URL must be an http/https literal loopback origin with an optional valid port'
fi

stock_curl() {
  command curl --noproxy '*' "$@"
}

request() {
  local method=$1 path=$2 data=${3-} expected=${4-200}
  local body="$TMP/response.json" code authorization="$CLIENT_AUTHORIZATION"
  [[ -z "$TOKEN" ]] || authorization+=", Token=\"$TOKEN\""
  local -a args=(-sS --connect-timeout 5 --max-time 30 -o "$body" -w '%{http_code}' -X "$method" -H 'Accept: application/json' -H "$authorization")
  if [[ -n "$data" ]]; then
    code=$(printf '%s' "$data" | stock_curl "${args[@]}" -H 'Content-Type: application/json' --data-binary @- "$BASE$path") || die "$method $path could not reach $BASE"
  else
    code=$(stock_curl "${args[@]}" "$BASE$path") || die "$method $path could not reach $BASE"
  fi
  [[ "$code" == "$expected" ]] || die "$method $path expected HTTP $expected, got $code"
  cat "$body"
}
credential_payload() {
  local purpose=$1
  if [[ -n "$CREDENTIAL_FILE" ]]; then
    [[ -r "$CREDENTIAL_FILE" ]] || die 'credential file is unreadable'
    python3 - "$CREDENTIAL_FILE" "$purpose" <<'PY'
import json
import sys

path, purpose = sys.argv[1:]
try:
    credentials = json.load(open(path, encoding="utf-8"))
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit(f"credential file is invalid: {error}")
required = {"admin_name", "admin_password", "disabled_name", "disabled_password"}
if not isinstance(credentials, dict) or set(credentials) != required or any(
    not isinstance(credentials[key], str) or not credentials[key] for key in required
):
    raise SystemExit("credential file has an invalid schema")
if purpose == "authenticate":
    payload = {"Username": credentials["admin_name"], "Pw": credentials["admin_password"]}
elif purpose == "startup":
    payload = {"Name": credentials["admin_name"], "Password": credentials["admin_password"]}
else:
    raise SystemExit("credential payload purpose is invalid")
print(json.dumps(payload, separators=(",", ":")))
PY
  else
    printf '%s\0%s\0' "$SEED_ADMIN" "$SEED_PASSWORD" | python3 -c '
import json
import sys

purpose = sys.argv[1]
parts = sys.stdin.buffer.read().split(b"\0")
if len(parts) != 3 or parts[-1] or not all(parts[:2]):
    raise SystemExit("credential input is invalid")
admin, password = (part.decode("utf-8") for part in parts[:2])
if purpose == "authenticate":
    payload = {"Username": admin, "Pw": password}
elif purpose == "startup":
    payload = {"Name": admin, "Password": password}
else:
    raise SystemExit("credential payload purpose is invalid")
print(json.dumps(payload, separators=(",", ":")))
' "$purpose"
  fi
}
read_all_items() {
  local out=$1 query=$2 page="$TMP/items-page.json" merged="$TMP/items-merged.json"
  local start=0 total= page_count=0 item_count= meta
  printf '[]\n' >"$merged"
  while :; do
    (( page_count < MAX_ITEM_PAGES )) || die "item pagination exceeded ${MAX_ITEM_PAGES} pages before completion"
    request GET "${query}&StartIndex=${start}&Limit=${ITEM_PAGE_SIZE}" '' 200 >"$page"
    if ! meta=$(python3 - "$page" "$merged" "$start" "$total" "$ITEM_PAGE_SIZE" <<'PY'
import json, pathlib, sys

page_path, merged_path, expected_start, prior_total, page_limit = sys.argv[1:]
expected_start = int(expected_start)
page_limit = int(page_limit)
try:
    page = json.load(open(page_path))
    merged = json.load(open(merged_path))
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit(f"invalid item pagination JSON: {error}")
if not isinstance(page, dict) or not isinstance(merged, list):
    raise SystemExit("item pagination response must be an object with an item array")
items = page.get("Items")
total = page.get("TotalRecordCount")
response_start = page.get("StartIndex", expected_start)
if not isinstance(items, list) or type(total) is not int or total < 0:
    raise SystemExit("item pagination response is missing a non-negative Items/TotalRecordCount pair")
if type(response_start) is not int or response_start != expected_start:
    raise SystemExit(f"item pagination response StartIndex {response_start!r} does not match requested {expected_start}")
if prior_total and total != int(prior_total):
    raise SystemExit(f"item pagination TotalRecordCount changed from {prior_total} to {total}")
if total > page_limit * 100:
    raise SystemExit(f"item pagination TotalRecordCount {total} exceeds the bounded scan limit")
if not items and expected_start < total:
    raise SystemExit(f"item pagination returned an empty page at {expected_start} before TotalRecordCount {total}")
if len(items) > page_limit:
    raise SystemExit(f"item pagination returned {len(items)} items beyond requested limit {page_limit}")
seen_ids = set()
for item in merged:
    if not isinstance(item, dict):
        raise SystemExit("item pagination accumulated a non-object item")
    item_id = item.get("Id")
    if not isinstance(item_id, str) or not item_id:
        raise SystemExit("item pagination item lacks a stable runtime Id")
    seen_ids.add(item_id)
for item in items:
    if not isinstance(item, dict):
        raise SystemExit("item pagination returned a non-object item")
    item_id = item.get("Id")
    if not isinstance(item_id, str) or not item_id:
        raise SystemExit("item pagination item lacks a stable runtime Id")
    if item_id in seen_ids:
        raise SystemExit(f"item pagination returned duplicate runtime Id {item_id!r}")
    seen_ids.add(item_id)
merged.extend(items)
if len(merged) > total:
    raise SystemExit(f"item pagination overran TotalRecordCount {total}")
pathlib.Path(merged_path).write_text(json.dumps(merged, ensure_ascii=False), encoding="utf-8")
print(f"{total}\t{len(items)}")
PY
); then
      die "item pagination rejected /Items: $meta"
    fi
    IFS=$'\t' read -r total item_count <<<"$meta"
    (( start += item_count, 1 ))
    (( start <= total )) || die "item pagination overran TotalRecordCount $total"
    (( page_count += 1 ))
    (( start == total )) && break
    (( item_count > 0 )) || die "item pagination made no progress at StartIndex $start"
  done
  [[ -n "$total" && "$start" == "$total" ]] || die 'item pagination did not reach TotalRecordCount'
  cp "$merged" "$out"
}
json() { python3 -c 'import json,sys; d=json.load(sys.stdin); p=sys.argv[1].split(".");
for x in p: d=d[int(x)] if x.isdigit() else d[x]
print(d)' "$1"; }
validate_policy() {
  [[ -r "$POLICY" ]] || die "provider policy is missing or unreadable: $POLICY"
  python3 - "$POLICY" <<'PY'
import json, sys

path = sys.argv[1]
try:
    policy = json.load(open(path))
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit(f"invalid provider policy: {error}")

required = {
    "EnableInternetProviders": False,
    "EnableAutomaticSeriesGrouping": False,
    "EnableEmbeddedTitles": True,
    "EnableEmbeddedEpisodeInfos": True,
    "DisabledLocalMetadataReaders": [],
}
for key, value in required.items():
    if policy.get(key) != value:
        raise SystemExit(f"provider policy must set {key} to {value!r}")

type_options = policy.get("TypeOptions")
if not isinstance(type_options, list) or not type_options:
    raise SystemExit("provider policy must contain TypeOptions")
seen = set()
for option in type_options:
    if not isinstance(option, dict) or not isinstance(option.get("Type"), str) or not option["Type"]:
        raise SystemExit("provider policy TypeOptions must name each item type")
    if option["Type"] in seen:
        raise SystemExit(f"provider policy repeats TypeOptions type {option['Type']!r}")
    seen.add(option["Type"])
    for key in ("MetadataFetchers", "ImageFetchers", "SimilarItemProviders"):
        if option.get(key) != []:
            raise SystemExit(f"provider policy must disable {key} for {option['Type']}")
PY
}
library_id() {
  local name=$1 kind=$2 path=$3 libraries=$4
  python3 - "$name" "$kind" "$path" "$libraries" <<'PY'
import json, sys

name, kind, path, source = sys.argv[1:]
matches = [library for library in json.load(open(source)) if library.get("Name") == name]
if not matches:
    raise SystemExit(1)
if len(matches) != 1:
    raise SystemExit(4)
library = matches[0]
locations = library.get("Locations") or library.get("Paths") or []
if locations != [path]:
    raise SystemExit(2)
if library.get("CollectionType") != kind:
    raise SystemExit(3)
item_id = library.get("ItemId") or library.get("Id")
if not isinstance(item_id, str) or not item_id:
    raise SystemExit(5)
print(item_id)
PY
}
policy_matches() {
  local libraries=$1 id=$2
  python3 - "$POLICY" "$libraries" "$id" <<'PY'
import json, sys

policy_path, libraries_path, item_id = sys.argv[1:]
policy = json.load(open(policy_path))
library = next((entry for entry in json.load(open(libraries_path))
                if entry.get("ItemId", entry.get("Id")) == item_id), None)
if library is None:
    raise SystemExit("library disappeared while applying provider policy")
options = library.get("LibraryOptions") or {}
for key in ("EnableInternetProviders", "EnableAutomaticSeriesGrouping",
            "EnableEmbeddedTitles", "EnableEmbeddedEpisodeInfos", "DisabledLocalMetadataReaders"):
    if options.get(key) != policy[key]:
        raise SystemExit(f"{key} differs")

expected_types = {option["Type"]: option for option in policy["TypeOptions"]}
actual_types = {option.get("Type"): option for option in options.get("TypeOptions") or []
                if isinstance(option, dict) and option.get("Type")}
for item_type, expected in expected_types.items():
    actual = actual_types.get(item_type)
    if actual is None:
        raise SystemExit(f"missing TypeOptions for {item_type}")
    for key in ("MetadataFetchers", "ImageFetchers", "SimilarItemProviders"):
        if actual.get(key) != expected[key]:
            raise SystemExit(f"{key} differs for {item_type}")
for item_type, actual in actual_types.items():
    for key in ("MetadataFetchers", "ImageFetchers", "SimilarItemProviders"):
        if actual.get(key, []) != []:
            raise SystemExit(f"{key} remains enabled for {item_type}")
PY
}
library_options_payload() {
  local id=$1
  python3 - "$POLICY" "$id" <<'PY'
import json, sys
print(json.dumps({"Id": sys.argv[2], "LibraryOptions": json.load(open(sys.argv[1]))}, separators=(",", ":")))
PY
}
add_library_payload() {
  python3 - "$POLICY" <<'PY'
import json, sys
print(json.dumps({"LibraryOptions": json.load(open(sys.argv[1]))}, separators=(",", ":")))
PY
}
wait_for_stock() {
  local attempt=1 version startup_code
  while (( attempt <= 60 )); do
    if stock_curl -fsS --connect-timeout 2 --max-time 5 "$BASE/System/Info/Public" >"$TMP/public.json" 2>/dev/null; then
      version=$(json Version <"$TMP/public.json" 2>/dev/null || true)
      if [[ -n "$version" ]]; then
        [[ "$version" == "$STOCK_VERSION" ]] || die "refusing to seed Jellyfin $version at $BASE; this reference script requires $STOCK_VERSION"
        startup_code=$(stock_curl -sS --connect-timeout 2 --max-time 5 -o "$TMP/startup-user.json" -w '%{http_code}' "$BASE/Startup/User" 2>/dev/null || true)
        case "$startup_code" in
          200|401|403) return ;;
        esac
      fi
    fi
    sleep 1
    ((attempt++))
  done
  die "stock Jellyfin did not complete startup at $BASE within 60 seconds; inspect docker compose -f tools/goldenrec/compose.yaml logs"
}
authenticate() {
  local data code attempt=1
  data=$(credential_payload authenticate) || die 'could not construct authentication request body'
  while (( attempt <= 30 )); do
    code=$(printf '%s' "$data" | stock_curl -sS --connect-timeout 5 --max-time 15 -o "$TMP/auth.json" -w '%{http_code}' -X POST -H 'Accept: application/json' -H 'Content-Type: application/json' -H "$CLIENT_AUTHORIZATION" --data-binary @- "$BASE/Users/AuthenticateByName") || die "could not reach $BASE while authenticating"
    if [[ "$code" == 200 ]]; then
      TOKEN=$(json AccessToken <"$TMP/auth.json") || die 'login response lacked AccessToken'
      USER_ID=$(json User.Id <"$TMP/auth.json") || die 'login response lacked User.Id'
      return
    fi
    # The RC2 wizard can expose a temporary 400 while persisting Startup/Complete.
    # A wrong completed-state credential is rejected immediately with its non-transient status.
    [[ "$code" == 503 || "$code" == 400 ]] || die "could not authenticate (HTTP $code). Supply the configured JELLYFIN_ADMIN and JELLYFIN_PASSWORD."
    sleep 1
    ((attempt++))
  done
  die "stock Jellyfin remained unavailable for authentication at $BASE after 30 seconds"
}
setup_wizard() {
  local complete credentials
  complete=$(json StartupWizardCompleted <"$TMP/public.json")
  [[ "$complete" == True || "$complete" == true ]] && return
  request POST '/Startup/Configuration' '{"ServerName":"Goldenrec Stock 12 RC2","UICulture":"en-US","MetadataCountryCode":"US","PreferredMetadataLanguage":"en"}' 204 >/dev/null
  credentials=$(credential_payload startup) || die 'could not construct startup-user request body'
  request POST '/Startup/User' "$credentials" 204 >/dev/null
  request POST '/Startup/RemoteAccess' '{"EnableRemoteAccess":true}' 204 >/dev/null
  request POST '/Startup/Complete' '' 204 >/dev/null
}
ensure_library() {
  local name=$1 kind=$2 path=$3 id status
  request GET '/Library/VirtualFolders' '' 200 >"$TMP/libraries.json"
  if id=$(library_id "$name" "$kind" "$path" "$TMP/libraries.json"); then
    :
  else
    status=$?
    case "$status" in
      1)
        request POST "/Library/VirtualFolders?name=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))' "$name")&collectionType=$kind&paths=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))' "$path")&refreshLibrary=false" "$(add_library_payload)" 204 >/dev/null
        request GET '/Library/VirtualFolders' '' 200 >"$TMP/libraries.json"
        id=$(library_id "$name" "$kind" "$path" "$TMP/libraries.json") || die "created library '$name' could not be validated"
        ;;
      2) die "existing library '$name' does not point exclusively at $path; remove or correct it before rerunning" ;;
      3) die "existing library '$name' has the wrong collection type; expected $kind. Remove or correct it before rerunning" ;;
      4) die "multiple libraries are named '$name'; remove duplicates before rerunning" ;;
      5) die "existing library '$name' lacks a stable item id" ;;
      *) die "could not inspect existing library '$name'" ;;
    esac
  fi
  if ! policy_matches "$TMP/libraries.json" "$id" 2>/dev/null; then
    request POST '/Library/VirtualFolders/LibraryOptions' "$(library_options_payload "$id")" 204 >/dev/null
    request GET '/Library/VirtualFolders' '' 200 >"$TMP/libraries.json"
  fi
  policy_matches "$TMP/libraries.json" "$id" || die "library '$name' did not read back the required local-only provider policy"
}
wait_for_scan() {
  local attempt=1 stable=0 evidence='no scan poll completed' status
  while (( attempt <= 120 )); do
    if evidence=$( ( read_all_items "$TMP/items.json" '/Items?Recursive=true&Fields=Path,ProviderIds&EnableTotalRecordCount=true' ) 2>&1); then
      request GET '/Library/VirtualFolders' '' 200 >"$TMP/libraries.json"
      if evidence=$(python3 - "$TMP/libraries.json" "$TMP/items.json" "$INVENTORY" 2>&1 <<'PY'
import json, posixpath, sys

libraries_path, items_path, inventory_path = sys.argv[1:]
try:
    libraries = {library.get("Name"): library for library in json.load(open(libraries_path))}
    items = json.load(open(items_path))
    expected_inventory = json.load(open(inventory_path))
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit(f"could not read stock inventory evidence: {error}")
if not isinstance(items, list):
    raise SystemExit("merged item inventory is not an array")
if not isinstance(expected_inventory, list) or len(expected_inventory) != 52:
    raise SystemExit("expected inventory must be a JSON array of exactly 52 Type:/media/path identities")
if any(not isinstance(identity, str) or ":" not in identity for identity in expected_inventory):
    raise SystemExit("expected inventory contains an invalid canonical identity")
if len(set(expected_inventory)) != len(expected_inventory):
    raise SystemExit("expected inventory repeats a canonical identity")

expected_libraries = ("Golden Movies", "Golden TV", "Golden Music", "Golden Photos")
statuses = {name: (libraries.get(name) or {}).get("RefreshStatus", "missing") for name in expected_libraries}
summary = f"item_count={len(items)}; " + ", ".join(f"{name}={statuses[name]}" for name in expected_libraries)
def reject(message):
    print(message, file=sys.stderr)
    raise SystemExit(2)

for item in items:
    provider_ids = item.get("ProviderIds") if isinstance(item, dict) else None
    if provider_ids not in (None, {}, []):
        reject(f"remote provider evidence on item {item.get('Id', '<unknown>')}: {provider_ids!r}")

print(summary)
if any(statuses[name] == "missing" for name in expected_libraries):
    raise SystemExit(1)
if any(status in {"Active", "Queued"} for status in statuses.values()):
    raise SystemExit(1)

canonical_identities = set()
actual_types_by_path = {}
roots = ("/media/movies", "/media/tv", "/media/music", "/media/photos")
for item in items:
    if not isinstance(item, dict):
        reject("non-object item in canonical inventory")
    item_type = item.get("Type")
    if not isinstance(item_type, str) or not item_type:
        reject(f"missing item type for canonical identity {item.get('Id', '<unknown>')}")
    path = item.get("Path")
    if not isinstance(path, str) or not path:
        reject(f"missing canonical path for item {item.get('Id', '<unknown>')}")
    if "\x00" in path or not path.startswith("/") or "/./" in path or "/../" in path or path.endswith("/.") or path.endswith("/.."):
        reject(f"invalid canonical path {path!r}")
    canonical = posixpath.normpath(path)
    if canonical != path:
        reject(f"non-canonical media path {path!r}")
    if not any(canonical == root or canonical.startswith(root + "/") for root in roots):
        reject(f"media path outside synthetic roots: {canonical!r}")
    identity = f"{item_type}:{canonical}"
    if identity in canonical_identities:
        reject(f"duplicate canonical media identity: {identity!r}")
    if canonical in actual_types_by_path:
        reject(f"multiple item types share canonical path: {canonical!r}")
    canonical_identities.add(identity)
    actual_types_by_path[canonical] = item_type

expected_set = set(expected_inventory)
expected_types_by_path = {identity.split(":", 1)[1]: identity.split(":", 1)[0] for identity in expected_inventory}
if len(expected_types_by_path) != len(expected_inventory):
    reject("expected inventory assigns multiple types to one canonical path")
missing = sorted(expected_set - canonical_identities)
extra = sorted(canonical_identities - expected_set)
type_changed = sorted(
    path for path in expected_types_by_path.keys() & actual_types_by_path.keys()
    if expected_types_by_path[path] != actual_types_by_path[path]
)
if missing or extra or type_changed:
    reject("stock inventory differs from expected manifest: "
           f"missing={missing!r}; extra={extra!r}; type_changed={type_changed!r}")
PY
); then
        ((stable += 1))
        if (( stable >= 2 )); then
          printf '%s\n' "$evidence"
          return
        fi
      else
        status=$?
        (( status == 1 || status == 2 )) || die "library scan rejected polluted or invalid stock evidence: $evidence"
        stable=0
      fi
    else
      stable=0
      evidence=${evidence%%$'\n'*}
      evidence=${evidence:-'item pagination read failed'}
    fi
    sleep 1
    ((attempt++))
  done
  die "library scan did not become stably idle after 120 seconds; last poll: $evidence. Inspect stock logs and fixture mount permissions."
}
ensure_boxset_membership() {
  local membership collection_id encoded_members
  if ! membership=$(python3 - "$TMP/items.json" <<'PY'
import json, sys, urllib.parse

boxset_identity = "BoxSet:/media/movies/Synthetic Collection [boxset]"
member_identities = (
    "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film One (2020).mkv",
    "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Two (2021).mkv",
    "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Three (2022).mkv",
)
try:
    items = json.load(open(sys.argv[1]))
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit(f"could not read scanned item identities: {error}")
item_ids = {}
for item in items:
    if not isinstance(item, dict):
        raise SystemExit("scanned item identity is not an object")
    item_type, path, item_id = item.get("Type"), item.get("Path"), item.get("Id")
    if not isinstance(item_type, str) or not isinstance(path, str) or not isinstance(item_id, str) or not item_id:
        raise SystemExit("scanned item identity lacks Type, Path, or Id")
    identity = f"{item_type}:{path}"
    if identity in item_ids:
        raise SystemExit(f"scanned item identity is ambiguous: {identity!r}")
    item_ids[identity] = item_id
try:
    collection_id = item_ids[boxset_identity]
    member_ids = [item_ids[identity] for identity in member_identities]
except KeyError as error:
    raise SystemExit(f"scanned inventory is missing required collection identity {error.args[0]!r}")
if len(set(member_ids)) != len(member_ids) or collection_id in member_ids:
    raise SystemExit("collection runtime identities are not distinct")
print(urllib.parse.quote(collection_id, safe="") + "\t" + urllib.parse.urlencode({"ids": ",".join(member_ids)}))
PY
); then
    die "could not resolve physical collection membership: $membership"
  fi
  IFS=$'\t' read -r collection_id encoded_members <<<"$membership"
  [[ -n "$collection_id" && -n "$encoded_members" ]] || die 'could not resolve collection runtime identities'
  # AddToCollection is idempotent upstream: existing linked children are not added again.
  request POST "/Collections/${collection_id}/Items?${encoded_members}" '' 204 >/dev/null
}
verify_boxset_collapse() {
  read_all_items "$TMP/movies-collapsed.json" '/Items?IncludeItemTypes=Movie&CollapseBoxSetItems=true&Fields=Path&EnableTotalRecordCount=true'
  read_all_items "$TMP/movies-uncollapsed.json" '/Items?IncludeItemTypes=Movie&CollapseBoxSetItems=false&Fields=Path&EnableTotalRecordCount=true'
  python3 - "$TMP/movies-collapsed.json" "$TMP/movies-uncollapsed.json" <<'PY'
import json, posixpath, sys

collapsed_path, uncollapsed_path = sys.argv[1:]
expected_boxset = "BoxSet:/media/movies/Synthetic Collection [boxset]"
expected_members = {
    "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film One (2020).mkv",
    "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Two (2021).mkv",
    "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Three (2022).mkv",
}
allowed_types = {"BoxSet", "Movie"}

def identities(path, query_name):
    try:
        with open(path) as evidence:
            items = json.load(evidence)
    except (OSError, json.JSONDecodeError) as error:
        raise SystemExit(f"could not read {query_name} movie collapse evidence: {error}")
    if not isinstance(items, list):
        raise SystemExit(f"{query_name} movie collapse evidence is not an array")

    result = set()
    for index, item in enumerate(items):
        if not isinstance(item, dict):
            raise SystemExit(f"{query_name} movie collapse item {index} is not an object")
        item_type = item.get("Type")
        if not isinstance(item_type, str) or item_type not in allowed_types:
            raise SystemExit(
                f"{query_name} movie collapse item {index} has unexpected Type {item_type!r}; "
                f"expected one of {sorted(allowed_types)!r}"
            )
        item_path = item.get("Path")
        if (
            not isinstance(item_path, str)
            or not item_path.startswith("/")
            or posixpath.normpath(item_path) != item_path
        ):
            raise SystemExit(
                f"{query_name} movie collapse item {index} has invalid canonical Path {item_path!r}"
            )
        identity = f"{item_type}:{item_path}"
        if identity in result:
            raise SystemExit(f"{query_name} movie collapse query returned duplicate identity {identity!r}")
        result.add(identity)
    return result

collapsed = identities(collapsed_path, "collapsed")
uncollapsed = identities(uncollapsed_path, "uncollapsed")
collapsed_non_movies = {identity for identity in collapsed if not identity.startswith("Movie:")}
uncollapsed_non_movies = {identity for identity in uncollapsed if not identity.startswith("Movie:")}

if collapsed_non_movies != {expected_boxset}:
    raise SystemExit(
        "collapsed movie query must contain exactly the physical Synthetic Collection BoxSet "
        f"as its sole non-Movie identity; got {sorted(collapsed_non_movies)!r}"
    )
if uncollapsed_non_movies:
    raise SystemExit(
        "uncollapsed movie query must contain only Movie identities; found "
        f"{sorted(uncollapsed_non_movies)!r}"
    )

missing_members = expected_members - uncollapsed
collapsed_members = expected_members & collapsed
if missing_members or collapsed_members:
    raise SystemExit(
        "Synthetic Collection member Movies must occur only in the uncollapsed query; "
        f"missing uncollapsed={sorted(missing_members)!r}, "
        f"present collapsed={sorted(collapsed_members)!r}"
    )
if expected_boxset not in collapsed or expected_boxset in uncollapsed:
    raise SystemExit(
        "physical Synthetic Collection BoxSet must occur only in the collapsed query; "
        f"collapsed={expected_boxset in collapsed}, uncollapsed={expected_boxset in uncollapsed}"
    )

collapsed_remainder = collapsed - {expected_boxset}
uncollapsed_remainder = uncollapsed - expected_members
only_collapsed = collapsed_remainder - uncollapsed_remainder
only_uncollapsed = uncollapsed_remainder - collapsed_remainder
if only_collapsed or only_uncollapsed:
    raise SystemExit(
        "CollapseBoxSetItems changed identities beyond the physical BoxSet/member-movie topology; "
        f"only collapsed={sorted(only_collapsed)!r}, "
        f"only uncollapsed={sorted(only_uncollapsed)!r}"
    )
PY
}
snapshot_destination() {
  local path=$1
  [[ ! -L "$path" && ( ! -e "$path" || -f "$path" ) ]] || die "snapshot destination must be absent or a regular file: $path"
}
snapshot() {
  local out="$STATE/snapshots" id_map libraries metadata checksums databases
  local id_map_tmp libraries_tmp metadata_tmp checksums_tmp
  [[ ! -L "$out" && ( ! -e "$out" || -d "$out" ) ]] || die "snapshot directory must be absent or a directory: $out"
  mkdir -p "$out"
  [[ ! -L "$out" && -d "$out" ]] || die "snapshot directory must be a non-symlink directory: $out"
  id_map="$out/id-map.json"
  libraries="$out/libraries.json"
  metadata="$out/metadata.json"
  checksums="$out/DATABASES.sha256"
  databases="$out/databases"
  snapshot_destination "$id_map"
  snapshot_destination "$libraries"
  snapshot_destination "$metadata"
  snapshot_destination "$checksums"
  [[ ! -L "$databases" && ( ! -e "$databases" || -d "$databases" ) ]] || die "snapshot databases destination must be absent or a directory: $databases"

  id_map_tmp=$(mktemp "$out/.id-map.json.XXXXXX")
  read_all_items "$id_map_tmp" '/Items?Recursive=true&Fields=Path,ProviderIds,MediaSources&EnableTotalRecordCount=true'
  mv -f "$id_map_tmp" "$id_map"
  libraries_tmp=$(mktemp "$out/.libraries.json.XXXXXX")
  request GET '/Library/VirtualFolders' '' 200 >"$libraries_tmp"
  mv -f "$libraries_tmp" "$libraries"

  # Jellyfin uses SQLite WAL. Stop the service cleanly before copying the complete data
  # directory so its transaction state is checkpointed and the copy is self-consistent.
  rm -rf "$databases"
  docker compose -f "$COMPOSE" stop --timeout 30 jellyfin
  STOCK_STOPPED=1
  mkdir -p "$databases"
  docker compose -f "$COMPOSE" cp jellyfin:/config/data/. "$databases"
  find "$databases" -type f -name '*.db' -print -quit | grep -q . || die 'stock data database was not found in the extracted named volume'
  docker compose -f "$COMPOSE" start jellyfin || die 'could not restart Jellyfin after consistent snapshot extraction'
  STOCK_STOPPED=0

  metadata_tmp=$(mktemp "$out/.metadata.json.XXXXXX")
  python3 - "$metadata_tmp" "$BASE" "$USER_ID" "$TMP/public.json" "$id_map" "$POLICY" <<'PY'
import hashlib, json, pathlib, sys
out, base, user_id, public, items, policy = sys.argv[1:]
payload = {"source": "digest-pinned stock Jellyfin 12.0.0", "base_url": base, "seed_user_id": user_id,
           "public_info": json.load(open(public)), "item_count": len(json.load(open(items))),
           "provider_policy_sha256": hashlib.sha256(pathlib.Path(policy).read_bytes()).hexdigest()}
pathlib.Path(out).write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")
PY
  mv -f "$metadata_tmp" "$metadata"
  checksums_tmp=$(mktemp "$out/.DATABASES.sha256.XXXXXX")
  python3 - "$databases" >"$checksums_tmp" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
for path in sorted(item for item in root.rglob('*') if item.is_file()):
    print(f'{hashlib.sha256(path.read_bytes()).hexdigest()}  databases/{path.relative_to(root)}')
PY
  mv -f "$checksums_tmp" "$checksums"
}

validate_policy
wait_for_stock
setup_wizard
authenticate
ensure_library 'Golden Movies' movies /media/movies
ensure_library 'Golden TV' tvshows /media/tv
ensure_library 'Golden Music' music /media/music
ensure_library 'Golden Photos' homevideos /media/photos
request POST '/Library/Refresh?metadataRefreshMode=Default&imageRefreshMode=Default' '' 204 >/dev/null
wait_for_scan
ensure_boxset_membership
verify_boxset_collapse
if (( SNAPSHOT )); then
  snapshot
  printf 'seeded stock Jellyfin at %s; snapshots are in %s/snapshots\n' "$BASE" "$STATE"
else
  printf 'seeded stock Jellyfin at %s; snapshot disabled (GOLDENREC_SNAPSHOT=0)\n' "$BASE"
fi
