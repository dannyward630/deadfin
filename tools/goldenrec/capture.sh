#!/usr/bin/env bash
# Records a local stock-session trace through mitmproxy and writes a sanitized sibling JSONL.
set -Eeuo pipefail
IFS=$'\n\t'
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
usage() {
  cat <<'EOF'
Usage: tools/goldenrec/capture.sh CAPTURE.mitm

Runs a regular proxy on :8080 using tools/goldenrec/mitmproxy/config.yaml. Its
generated CA and other mitmproxy state stay under the ignored runtime state directory.
Configure one dedicated Swiftfin or jellyfin-web test client to use this proxy and trust
that CA. Use only against the generated synthetic reference server. Ctrl-C ends capture.
The raw .mitm and sanitized .jsonl are both ignored by Git; distill the latter before
reviewing any corpus candidate.
EOF
}
[[ ${1:-} != --help ]] || { usage; exit 0; }
[[ $# -eq 1 ]] || { usage >&2; exit 64; }
command -v mitmdump >/dev/null 2>&1 || { printf 'goldenrec capture: mitmdump is required (install mitmproxy)\n' >&2; exit 1; }
OUT=$1
TARGET=${JELLYFIN_BASE_URL:-http://127.0.0.1:8096}
mkdir -p "$(dirname "$OUT")"
SANITIZED="${OUT%.mitm}.sanitized.jsonl"
CONFDIR="$ROOT/state/mitmproxy"
mkdir -p "$CONFDIR"
ln -sfn "$ROOT/mitmproxy/config.yaml" "$CONFDIR/config.yaml"
: >"$SANITIZED"
exec mitmdump --set "confdir=$CONFDIR" --set "goldenrec_capture_output=$SANITIZED" --set "goldenrec_capture_target=$TARGET" -s "$ROOT/mitmproxy/sanitize.py" -w "$OUT"
