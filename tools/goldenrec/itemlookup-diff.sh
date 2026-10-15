#!/usr/bin/env bash
# ItemLookup differential gate wrapper. Runs the difftool three times against
# the stock reference and the Deadfin candidate, both proxied through the
# committed provider cassette. Produces results/itemlookup-acceptance.json.
#
# Required: DEADFIN_DIR (path to the candidate checkout)
# Required: DEADFIN_REF (the reviewed head SHA)
# Optional: DEADFIN_IMAGE (tag of a prebuilt Deadfin candidate image).
# If set, the wrapper runs the candidate in a detached Docker container with
# host networking. If DEADFIN_IMAGE is not set, the wrapper builds the
# candidate locally with the Go toolchain.
#
# This target runs only on the canonical goldenrec host (the native x86_64 CI
# runner). It requires docker, the fixture tree, and a provider cassette.
# Recording the cassette is an operator step, never CI.
set -Eeuo pipefail
IFS=$'\n\t'
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

DEADFIN_DIR="${DEADFIN_DIR:?DEADFIN_DIR is required (path to the candidate checkout)}"
DEADFIN_REF="${DEADFIN_REF:?DEADFIN_REF is required (the reviewed head SHA)}"

# Pin: fail unless the checked-out candidate HEAD matches the reviewed ref.
actual_ref=$(git -C "$DEADFIN_DIR" rev-parse HEAD)
if [ "$actual_ref" != "$DEADFIN_REF" ]; then
  echo "itemlookup-diff: DEADFIN_REF $DEADFIN_REF does not match HEAD $actual_ref" >&2
  exit 1
fi

# Select the Deadfin candidate mode. If DEADFIN_IMAGE is set, the wrapper
# runs the candidate in a detached Docker container. If DEADFIN_IMAGE is not
# set, build the candidate locally with the Go toolchain.
CANDIDATE_BIN="$ROOT/tools/goldenrec/state/deadfin-candidate"
CANDIDATE_NAME="goldenrec-deadfin-candidate"
DEADFIN_DATA_DIR="$ROOT/tools/goldenrec/state/deadfin-data"
# The candidate must see the same media tree as stock. compose.yaml mounts
# ./fixtures/out at /media for the stock container, and seed.sh sends
# paths=/media/movies to BOTH servers. Deadfin checks that the path exists and
# maps os.ErrNotExist to 404, so a candidate without this mount rejects the
# library creation while stock accepts it.
FIXTURE_TREE="$ROOT/tools/goldenrec/fixtures/out"
if [ -n "${DEADFIN_IMAGE:-}" ]; then
  echo "itemlookup-diff: using supplied image $DEADFIN_IMAGE"
else
  echo "itemlookup-diff: building Deadfin candidate locally"
fi

RESULTS_DIR="$ROOT/results"
CASSETTE="$ROOT/corpora/provider-cassettes/itemlookup.jsonl"
UNMATCHED_REPORT="$RESULTS_DIR/itemlookup-unmatched-flows.txt"
STOCK_IMAGE_DIGEST="sha256:2d1a69be5aa2695d0c2df90ebeb76bbcc6efc0a798cb09bfa65906680e1d785a"

# Admin credentials for the difftool bootstrap. Both stock and the Deadfin
# candidate are seeded identically by seed.sh with these defaults. The
# difftool credential file uses a separate JSON schema with a left and right
# entry, so the wrapper writes one before the difftool runs.
SEED_ADMIN=${JELLYFIN_ADMIN:-GoldenrecAdmin}
SEED_PASSWORD=${JELLYFIN_PASSWORD:-goldenrec-change-me}
DISABLED_NAME=${GOLDENREC_DISABLED_NAME:-GoldenrecDisabled}
DISABLED_PASSWORD=${GOLDENREC_DISABLED_PASSWORD:-goldenrec-disabled-change-me}
CREDENTIAL_FILE="$ROOT/tools/goldenrec/state/difftool-credentials.json"
unset JELLYFIN_ADMIN JELLYFIN_PASSWORD GOLDENREC_DISABLED_NAME GOLDENREC_DISABLED_PASSWORD ADMIN PASSWORD
export -n SEED_ADMIN SEED_PASSWORD DISABLED_NAME DISABLED_PASSWORD

mkdir -p "$RESULTS_DIR" "$(dirname "$CREDENTIAL_FILE")"
rm -f "$UNMATCHED_REPORT"

# Bootstrap the replay proxy CA certificate. Start replay-proxy alone so
# mitmproxy generates the CA, copy the CA out, and leave the proxy running.
# The full stack starts the remaining services without --force-recreate so
# the running proxy is not recreated. This keeps the on-disk CA and the
# proxy CA identical through every stack start. The CA consistency check
# asserts this invariant after each start.
COMPOSE_CMD=(docker compose -f compose.yaml -f compose.provider-replay.yaml)
CA_CERT="$ROOT/tools/goldenrec/mitmproxy/mitmproxy-ca.crt"
# The CA persists in the replay-proxy-ca Compose named volume. The proxy CA
# at /home/mitmproxy/.mitmproxy survives container replacement and stock
# reseeds, so the CA hash stays constant through the complete gate run.
# The stock container mounts the extracted host CA at its trust path.
CA_BASELINE_HASH=""
PROXY_CA_PATH="/home/mitmproxy/.mitmproxy/mitmproxy-ca-cert.pem"
STOCK_CA_PATH="/usr/local/share/ca-certificates/mitmproxy-ca.crt"

bootstrap_ca() {
  cd "$ROOT/tools/goldenrec"
  "${COMPOSE_CMD[@]}" up -d replay-proxy
  sleep 2
  "${COMPOSE_CMD[@]}" cp replay-proxy:"$PROXY_CA_PATH" "$CA_CERT"
}

# Assert the host, proxy, and stock CA certificates have one SHA-256 hash.
# The host CA is the extracted file, the proxy CA is the live certificate
# inside the replay-proxy container, and the stock CA is the mounted
# certificate inside the jellyfin container. A failed hash command or an
# empty hash is a certificate-file error. The baseline is the first common
# hash from the initial start. After that, all three hashes must equal the
# baseline. On a failed assertion, print all certificate paths and their
# available hashes before exit.
assert_ca_consistent() {
  local stage="$1"
  local disk live stock

  # Attempt all three hash commands. A failed command or an empty hash is a
  # certificate-file error. Collect every available hash so the failure
  # report can name all paths.
  disk=$(sha256sum "$CA_CERT" 2>/dev/null | cut -d' ' -f1) || disk=""
  live=$("${COMPOSE_CMD[@]}" exec -T replay-proxy sha256sum "$PROXY_CA_PATH" 2>/dev/null | cut -d' ' -f1) || live=""
  stock=$("${COMPOSE_CMD[@]}" exec -T jellyfin sha256sum "$STOCK_CA_PATH" 2>/dev/null | cut -d' ' -f1) || stock=""

  if [ -z "$disk" ] || [ -z "$live" ] || [ -z "$stock" ] || [ "$disk" != "$live" ] || [ "$disk" != "$stock" ]; then
    echo "FAIL [$stage]: CA certificate error" >&2
    echo "  host  CA ($CA_CERT): ${disk:-unavailable}" >&2
    echo "  proxy CA ($PROXY_CA_PATH): ${live:-unavailable}" >&2
    echo "  stock CA ($STOCK_CA_PATH): ${stock:-unavailable}" >&2
    exit 1
  fi

  if [ -z "$CA_BASELINE_HASH" ]; then
    CA_BASELINE_HASH="$disk"
    echo "CA baseline [$stage]: $CA_BASELINE_HASH"
  elif [ "$disk" != "$CA_BASELINE_HASH" ]; then
    echo "FAIL [$stage]: CA hash changed from baseline" >&2
    echo "  baseline: $CA_BASELINE_HASH" >&2
    echo "  current:  $disk" >&2
    echo "  host  CA ($CA_CERT): $disk" >&2
    echo "  proxy CA ($PROXY_CA_PATH): $live" >&2
    echo "  stock CA ($STOCK_CA_PATH): $stock" >&2
    exit 1
  else
    echo "CA consistent [$stage]: $disk"
  fi
}

# Resolve the stock configuration volume from the active jellyfin container.
# The stock state uses the goldenrec-config named volume at /config. This
# function returns the full Docker volume name (prefixed with the Compose
# project name). Fail if /config does not identify exactly one named volume.
resolve_stock_volume() {
  local container_id volumes count
  container_id=$(docker ps -q -f "name=goldenrec-jellyfin-1" 2>/dev/null) || container_id=""
  if [ -z "$container_id" ]; then
    echo "FAIL: no active jellyfin container found" >&2
    exit 1
  fi
  volumes=$(docker inspect "$container_id" \
    --format '{{ range .Mounts }}{{ if eq .Destination "/config" }}{{ .Name }}{{ end }}{{ end }}' 2>/dev/null) || volumes=""
  count=$(echo "$volumes" | grep -c . 2>/dev/null) || count=0
  if [ "$count" -ne 1 ]; then
    echo "FAIL: /config does not identify exactly one named volume (found $count)" >&2
    exit 1
  fi
  echo "$volumes"
}

# start_deadfin starts the Deadfin candidate. When DEADFIN_IMAGE is set,
# the candidate runs in a detached Docker container with host networking so
# it reaches the replay proxy at 127.0.0.1:8080. The container uses the
# runner UID and GID and mounts the state directory read-write. When
# DEADFIN_IMAGE is not set, the candidate runs as a local process.
start_deadfin() {
  # A bind mount requires its source to exist before `docker run`; the local
  # process path did not, because Deadfin creates its own data directory.
  # This must live here rather than beside the other mkdir at the top of the
  # script: reseed_deadfin removes the directory and calls start_deadfin again
  # before the two-sided lane, so a one-time create clears only the first lane.
  mkdir -p "$DEADFIN_DATA_DIR"
  if [ -n "${DEADFIN_IMAGE:-}" ]; then
    docker rm -f "$CANDIDATE_NAME" >/dev/null 2>&1 || true
    docker run --detach --name "$CANDIDATE_NAME" \
      --network host \
      --user "$(id -u):$(id -g)" \
      --mount "type=bind,src=$DEADFIN_DATA_DIR,dst=/var/lib/deadfin" \
      --mount "type=bind,src=$FIXTURE_TREE,dst=/media,readonly" \
      --mount "type=bind,src=$SSL_CERT_FILE,dst=/usr/local/share/ca-certificates/mitmproxy-ca.crt,readonly" \
      --env HTTPS_PROXY="$HTTPS_PROXY" \
      --env SSL_CERT_FILE=/usr/local/share/ca-certificates/mitmproxy-ca.crt \
      --env TMDB_API_KEY="$TMDB_API_KEY" \
      --env JELLYFIN_DATA_DIR=/var/lib/deadfin \
      "$DEADFIN_IMAGE" -listen :8097
  else
    JELLYFIN_DATA_DIR="$DEADFIN_DATA_DIR" \
    "$CANDIDATE_BIN" -listen :8097 &
    DEADFIN_PID=$!
  fi
}

# stop_deadfin stops the Deadfin candidate. When DEADFIN_IMAGE is set,
# the candidate container is removed. When DEADFIN_IMAGE is not set, the
# local process is killed.
stop_deadfin() {
  if [ -n "${DEADFIN_IMAGE:-}" ]; then
    docker rm -f "$CANDIDATE_NAME" >/dev/null 2>&1 || true
  else
    kill "$DEADFIN_PID" 2>/dev/null || true
    wait "$DEADFIN_PID" 2>/dev/null || true
  fi
}

cd "$ROOT/tools/goldenrec"

# Reset the compose project with both compose files so any leftover
# replay-proxy container is removed. This mirrors the reference-env.sh
# volume drop but does not start Jellyfin. The initial reset removes all
# volumes including the CA volume, so the first bootstrap starts clean.
"${COMPOSE_CMD[@]}" down --volumes --remove-orphans

# Generate the fixture tree. This step does not start any container.
"$ROOT/tools/verify-env/generate-fixtures.sh"

# Start the replay proxy alone so it generates the CA certificate.
# The proxy has no dependency on Jellyfin, so it starts independently.
# The CA persists in the replay-proxy-ca named volume.
bootstrap_ca

# Start the remaining services (jellyfin) without --force-recreate so the
# running replay-proxy keeps its CA. The stock container mounts the
# extracted CA at start.
"${COMPOSE_CMD[@]}" up -d

# Verify the host, proxy, and stock CA certificates have one hash.
# This sets the baseline hash for the complete gate run.
assert_ca_consistent "initial-start"

# Replacement probe: force-recreate only replay-proxy once and retain the
# stock container. Do not call bootstrap_ca or copy a new certificate. The
# replay-proxy-ca named volume keeps the same CA. After the probe, require
# all three hashes to equal the baseline. This makes the named-volume
# contract observable and covers the stale bind-mount failure from the
# original finding. The probe runs before the first seed.sh call so the
# CA persistence contract holds before stock state mutates.
"${COMPOSE_CMD[@]}" up -d --force-recreate --no-deps replay-proxy
sleep 2
assert_ca_consistent "proxy-replacement"

# Seed the stock reference. The ItemLookup gate does not read the snapshot
# files, so the stop, copy, and restart cycle is unnecessary here.
GOLDENREC_SNAPSHOT=0 ./seed.sh

# Build and start the Deadfin candidate with proxy environment.
# The replay proxy is a Docker service named replay-proxy on port 8080.
# The Deadfin candidate reaches the proxy at 127.0.0.1:8080. It listens on
# port 8097 so it does not conflict with the stock container on 8096.
export HTTPS_PROXY="http://127.0.0.1:8080"
export SSL_CERT_FILE="$ROOT/tools/goldenrec/mitmproxy/mitmproxy-ca.crt"
export TMDB_API_KEY="<redacted>"
if [ -z "${DEADFIN_IMAGE:-}" ]; then
  cd "$DEADFIN_DIR"
  go build -o "$CANDIDATE_BIN" ./cmd/deadfin
fi
start_deadfin
trap 'stop_deadfin' EXIT

# Wait for Deadfin to start.
sleep 3

# Seed the Deadfin candidate identically to stock. Snapshot is disabled
# because the ItemLookup gate does not read the snapshot files.
cd "$ROOT/tools/goldenrec"
JELLYFIN_BASE_URL="http://127.0.0.1:8097" GOLDENREC_SNAPSHOT=0 ./seed.sh

# Write the difftool credential file. Both hosts share the same seeded
# admin and disabled-user credentials. The file uses the difftool JSON
# schema with left and right entries. The mode is 0600 so no other user
# can read the credentials.
python3 - "$CREDENTIAL_FILE" "$SEED_ADMIN" "$SEED_PASSWORD" "$DISABLED_NAME" "$DISABLED_PASSWORD" <<'PY'
import json
import os
import sys

path, admin, password, disabled_name, disabled_password = sys.argv[1:6]
credentials = {
    "left": {
        "username": admin,
        "password": password,
        "disabled_username": disabled_name,
        "disabled_password": disabled_password,
    },
    "right": {
        "username": admin,
        "password": password,
        "disabled_username": disabled_name,
        "disabled_password": disabled_password,
    },
}
with open(path, "w", encoding="utf-8") as output:
    json.dump(credentials, output, separators=(",", ":"))
    output.write("\n")
os.chmod(path, 0o600)
PY

# reseed_stock resolves the stock configuration volume, stops and removes
# only the jellyfin container, removes the resolved stock configuration
# volume, restarts the jellyfin container, and asserts the CA. The replay
# proxy stays running through the reseed so the Deadfin candidate does not
# lose its provider replay connection. The CA volume survives the reseed
# because no --volumes flag is used. Apply and Download are not undoable,
# so each mutating lane starts from a clean state.
reseed_stock() {
  local stock_volume
  cd "$ROOT/tools/goldenrec"
  stock_volume=$(resolve_stock_volume)
  "${COMPOSE_CMD[@]}" stop jellyfin
  "${COMPOSE_CMD[@]}" rm -f jellyfin
  docker volume rm "$stock_volume"
  "${COMPOSE_CMD[@]}" up -d jellyfin
  assert_ca_consistent "stock-reseed"
  GOLDENREC_SNAPSHOT=0 ./seed.sh
}

# reseed_deadfin stops the Deadfin candidate, cleans its data directory,
# restarts it, and re-seeds it. The candidate runs either on the host or in
# a Docker container, depending on the DEADFIN_IMAGE mode.
reseed_deadfin() {
  stop_deadfin
  rm -rf "$DEADFIN_DATA_DIR"
  start_deadfin
  sleep 3
  cd "$ROOT/tools/goldenrec"
  JELLYFIN_BASE_URL="http://127.0.0.1:8097" GOLDENREC_SNAPSHOT=0 ./seed.sh
}

# Run difftool three times with distinct run IDs.
# Each mutating lane starts from a reseeded clean state because Apply and
# Download are not undoable.
cd "$ROOT/tools/difftool"

# Run 1: itemlookup self-check (stock vs stock).
# The corpus contains POST steps, so it cannot use --check-normalizers (that
# mode rejects mutations). Both --left and --right point at stock instead.
go run -mod=vendor . \
  --corpus "$ROOT/corpora/itemlookup.yaml" \
  --left "http://127.0.0.1:8096" \
  --right "http://127.0.0.1:8096" \
  --rules normalize \
  --results "$RESULTS_DIR" \
  --credentials-file "$CREDENTIAL_FILE" \
  --left-auth-role admin \
  --right-auth-role admin \
  --run-id itemlookup-self

# Reseed before run 2 because run 1 mutated stock state through Apply.
reseed_stock
cd "$ROOT/tools/difftool"

# Run 2: itemlookup-download self-check (stock vs stock).
go run -mod=vendor . \
  --corpus "$ROOT/corpora/itemlookup-download.yaml" \
  --left "http://127.0.0.1:8096" \
  --right "http://127.0.0.1:8096" \
  --rules normalize \
  --results "$RESULTS_DIR" \
  --credentials-file "$CREDENTIAL_FILE" \
  --left-auth-role admin \
  --right-auth-role admin \
  --run-id itemlookup-download-self

# Reseed before run 3 because run 2 mutated stock state through Download.
# Reseed Deadfin too because run 3 is the two-sided lane against it.
reseed_stock
reseed_deadfin
cd "$ROOT/tools/difftool"

# Run 3: itemlookup two-sided (stock vs Deadfin).
go run -mod=vendor . \
  --corpus "$ROOT/corpora/itemlookup.yaml" \
  --left "http://127.0.0.1:8096" \
  --right "http://127.0.0.1:8097" \
  --rules normalize \
  --results "$RESULTS_DIR" \
  --credentials-file "$CREDENTIAL_FILE" \
  --left-auth-role admin \
  --right-auth-role admin \
  --run-id itemlookup-diff

# Produce the acceptance artifact.
python3 - "$ROOT" "$DEADFIN_REF" "$CASSETTE" "$RESULTS_DIR" "$UNMATCHED_REPORT" "$STOCK_IMAGE_DIGEST" <<'PY'
import hashlib
import json
import subprocess
import sys
from pathlib import Path

root = Path(sys.argv[1])
deadfin_ref = sys.argv[2]
cassette_path = Path(sys.argv[3])
results_dir = Path(sys.argv[4])
unmatched_report = Path(sys.argv[5])
stock_image_digest = sys.argv[6]

jellyport_ref = subprocess.check_output(
    ["git", "-C", str(root), "rev-parse", "HEAD"], text=True
).strip()

cassette_hash = hashlib.sha256(cassette_path.read_bytes()).hexdigest()

run_ids = ["itemlookup-self", "itemlookup-download-self", "itemlookup-diff"]
run_results = {}
for run_id in run_ids:
    result_file = results_dir / f"{run_id}.json"
    if result_file.exists():
        result = json.loads(result_file.read_text())
        failed = sum(1 for s in result.get("steps", []) if s.get("status") != "pass")
        run_results[run_id] = {"failed_steps": failed}
    else:
        run_results[run_id] = {"failed_steps": -1, "error": "result file missing"}

unmatched_count = 0
if unmatched_report.exists():
    unmatched_count = sum(1 for line in unmatched_report.read_text().splitlines() if line.strip())

acceptance = {
    "jellyport_ref": jellyport_ref,
    "deadfin_ref": deadfin_ref,
    "stock_image_digest": stock_image_digest,
    "cassette_sha256": cassette_hash,
    "result_files": {
        "itemlookup-self": str(results_dir / "itemlookup-self.json"),
        "itemlookup-download-self": str(results_dir / "itemlookup-download-self.json"),
        "itemlookup-diff": str(results_dir / "itemlookup-diff.json"),
    },
    "run_results": run_results,
    "unmatched_provider_flows": unmatched_count,
}

output = results_dir / "itemlookup-acceptance.json"
output.write_text(json.dumps(acceptance, indent=2, sort_keys=True) + "\n")
print(f"Wrote {output}")

# Fail if any run has failed steps or unmatched flows.
total_failed = sum(r["failed_steps"] for r in run_results.values())
if total_failed != 0:
    print(f"FAIL: {total_failed} failed steps across runs", file=sys.stderr)
    sys.exit(1)
if unmatched_count != 0:
    print(f"FAIL: {unmatched_count} unmatched provider flows", file=sys.stderr)
    sys.exit(1)
PY
