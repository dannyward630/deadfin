#!/usr/bin/env bash
# Record deterministic upstream-derived playback and encoding corpora.
set -Eeuo pipefail
IFS=$'\n\t'

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$ROOT/../.." && pwd)
UPSTREAM=${GOLDENREC_UPSTREAM_ROOT:-"$REPO/upstream/jellyfin"}
OUT="$REPO/deadfin"
MODE=${1:-}

usage() {
    cat <<'EOF'
Usage: tools/goldenrec/record.sh {streambuilder|encoding|all|verify} [--output-root DEADFIN_ROOT]

The default output root is the nested deadfin checkout. Recording requires the
.NET 10 SDK and the pinned upstream/jellyfin worktree. `verify` is read-only.
EOF
}

[[ -n "$MODE" && "$MODE" != --help && "$MODE" != -h ]] || { usage; exit "${MODE:+0}"; }
shift
while (($#)); do
    case "$1" in
        --output-root)
            (($# >= 2)) || { printf 'goldenrec: --output-root requires a path\n' >&2; exit 64; }
            OUT=$2
            shift 2
            ;;
        *)
            printf 'goldenrec: unexpected argument %s\n' "$1" >&2
            usage >&2
            exit 64
            ;;
    esac
done
OUT=$(cd "$(dirname "$OUT")" && pwd)/$(basename "$OUT")
UPSTREAM_INPUT=$UPSTREAM
if ! UPSTREAM=$(cd "$UPSTREAM_INPUT" && pwd -P); then
    printf 'goldenrec: pinned upstream tree is missing at %s\n' "$UPSTREAM_INPUT" >&2
    exit 1
fi

case "$MODE" in
    verify)
        exec python3 "$ROOT/provenance.py" verify --root "$OUT" --upstream "$UPSTREAM"
        ;;
    streambuilder|encoding|all)
        command -v dotnet >/dev/null 2>&1 || { printf 'goldenrec: dotnet SDK 10 is required to record; run verify on existing corpora instead\n' >&2; exit 1; }
        [[ -f "$UPSTREAM/MediaBrowser.Model/MediaBrowser.Model.csproj" ]] || { printf 'goldenrec: pinned upstream tree is missing at %s\n' "$UPSTREAM" >&2; exit 1; }
        mkdir -p "$OUT"
        case "$MODE" in
            streambuilder) rm -rf "$OUT/internal/playback/testdata/golden" ;;
            encoding) rm -rf "$OUT/internal/encoding/testdata/golden" ;;
            all) rm -rf "$OUT/internal/playback/testdata/golden" "$OUT/internal/encoding/testdata/golden" ;;
        esac
        dotnet run --configuration Release --project "$ROOT/GoldenRec.csproj" -p:UpstreamRoot="$UPSTREAM" -- "$MODE" --root "$OUT" --upstream "$UPSTREAM"
        python3 "$ROOT/provenance.py" write --corpus "$MODE" --root "$OUT" --upstream "$UPSTREAM"
        exec python3 "$ROOT/provenance.py" verify --corpus "$MODE" --root "$OUT" --upstream "$UPSTREAM"
        ;;
    *)
        printf 'goldenrec: unknown mode %s\n' "$MODE" >&2
        usage >&2
        exit 64
        ;;
esac
