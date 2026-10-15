#!/usr/bin/env bash
# One-command local reference environment. Root Makefile delegates reference-env here.
set -Eeuo pipefail
IFS=$'\n\t'

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
COMPOSE="$ROOT/tools/goldenrec/compose.yaml"
usage() {
  cat <<'EOF'
Usage: tools/goldenrec/reference-env.sh [--help]

Requires Docker with Compose v2, curl, and python3. Generates synthetic local
media in the pinned linux/amd64 fixture container, recreates the disposable
digest-pinned Jellyfin 12.0-rc2 configuration volume, then seeds and snapshots
it. Recreating the volume is intentional: replay mutations such as device
capabilities and display preferences must never survive into the next seed.
Optional environment:
  GOLDENREC_PORT             host port (default 8096; also seeds and advertises this port)
  GOLDENREC_PUBLISHED_URL    advertised server URL (default http://127.0.0.1:GOLDENREC_PORT)
  JELLYFIN_BASE_URL          seed target (default http://127.0.0.1:GOLDENREC_PORT)
  JELLYFIN_ADMIN             admin name (default GoldenrecAdmin)
  JELLYFIN_PASSWORD          admin password (default goldenrec-change-me)
EOF
}
die() { printf 'goldenrec reference-env: %s\n' "$*" >&2; exit 1; }
[[ ${1:-} != --help ]] || { usage; exit 0; }
[[ $# -eq 0 ]] || { usage >&2; exit 64; }
command -v docker >/dev/null 2>&1 || die 'Docker is required'
docker compose version >/dev/null 2>&1 || die 'Docker Compose v2 is required (docker compose version failed)'
command -v curl >/dev/null 2>&1 || die 'curl is required'
command -v python3 >/dev/null 2>&1 || die 'python3 is required'

PORT=${GOLDENREC_PORT:-8096}
[[ "$PORT" =~ ^[1-9][0-9]{0,4}$ ]] && (( PORT <= 65535 )) || die "GOLDENREC_PORT must be a TCP port, got $PORT"
export GOLDENREC_PORT=$PORT
export JELLYFIN_BASE_URL=${JELLYFIN_BASE_URL:-"http://127.0.0.1:$PORT"}
export GOLDENREC_PUBLISHED_URL=${GOLDENREC_PUBLISHED_URL:-"http://127.0.0.1:$PORT"}

cd "$ROOT"
# `down --volumes` is scoped to the goldenrec Compose project and is the reset
# boundary for the disposable source environment; it does not touch any other
# Docker volume.
docker compose -f "$COMPOSE" down --volumes --remove-orphans
tools/verify-env/generate-fixtures.sh
docker compose -f "$COMPOSE" up -d
tools/goldenrec/seed.sh
