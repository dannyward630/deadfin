#!/usr/bin/env bash
# Generates long-duration synthetic media for T3 checkpoints that need
# media long enough to seek, transcode, and resume across an app restart.
#
# The canonical generator (gen.sh) produces 1-second files. Several T3 rows
# (S7-S10, S15) need longer media. This script produces the same codec profile
# as the canonical generator — mpeg4/mkv at 320x180 — with subtitle cues offset
# 5 seconds between the external and embedded tracks.
#
# These are NOT canonical fixtures. They do not write to fixtures/out and do
# not change source-manifest.sha256 or expected-inventory.json. If you register
# the longmedia libraries while the canonical libraries are present,
# make reference-env trips its 52-item assertion. Register the longmedia
# libraries in a separate library set, or run them against a standalone
# compose stack that does not seed the canonical tree.
#
# Usage: tools/goldenrec/fixtures/gen-t3-longmedia.sh OUTPUT_DIR
#
# OUTPUT_DIR is required. A nonexistent destination is created; an existing
# destination is replaced only when it carries this generator's ownership
# marker. Requires ffmpeg with the native aac/mpeg4 encoders and python3.
set -Eeuo pipefail
IFS=$'\n\t'

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
REPO_ROOT=$(cd "$ROOT/../.." && pwd -P)
CANONICAL_OUT="$ROOT/fixtures/out"
OUT=${1:-}
FFMPEG=${FFMPEG:-ffmpeg}
DURATION=300
SUBTITLE_DURATION=303
MARKER_NAME='.t3-longmedia-owner'
MARKER_FORMAT='t3-longmedia-owner-v1'

usage() {
  cat <<'EOF'
Usage: tools/goldenrec/fixtures/gen-t3-longmedia.sh OUTPUT_DIR

Creates a long-duration synthetic media tree for T3 checkpoints that
need seek, transcode, or resume-across-restart media. Produces three items:
  - Long Feature (2024).mkv           — movie, no subtitles (S7, S8), 300 s
  - Long Subtitle (2024).mkv          — movie, external .en.srt + embedded SUBRIP (S9), 303 s
  - Long Show S01E01 - Long Signal.mkv — TV episode, no subtitles (S10), 300 s

Codec profile matches the canonical generator: mpeg4/mkv at 320x180, 24 fps.
Subtitle cues are offset 5 s between the external and embedded tracks so the
active track is identifiable from content.

OUTPUT_DIR is required. A nonexistent destination is created; an existing
destination is replaced only when it carries this generator's ownership
marker. The canonical fixtures/out is always rejected. These are NOT
canonical fixtures. Requires ffmpeg (native aac/mpeg4 encoders) and python3.
EOF
}

die() { printf 'gen-t3-longmedia: %s\n' "$*" >&2; exit 1; }

canonical_path() {
  python3 - "$1" <<'PY'
import os, sys
print(os.path.realpath(os.path.abspath(sys.argv[1])))
PY
}

owned_output() {
  python3 - "$OUT_REAL" "$MARKER_NAME" "$MARKER_FORMAT" "$CURRENT_UID" <<'PY'
import json
import os
import stat
import sys

root = sys.argv[1]
marker_name = sys.argv[2]
marker_format = sys.argv[3]
uid = int(sys.argv[4])
expected = (json.dumps(
    {"format": marker_format, "root": root, "uid": uid},
    separators=(",", ":"),
    sort_keys=True,
) + "\n").encode("utf-8")
try:
    root_stat = os.lstat(root)
    if not stat.S_ISDIR(root_stat.st_mode) or root_stat.st_uid != uid or stat.S_IMODE(root_stat.st_mode) != 0o700:
        raise ValueError("unsafe fixture root")
    marker = os.path.join(root, marker_name)
    flags = os.O_RDONLY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = os.open(marker, flags)
    with os.fdopen(descriptor, "rb") as file:
        marker_stat = os.fstat(file.fileno())
        if not stat.S_ISREG(marker_stat.st_mode) or marker_stat.st_uid != uid or stat.S_IMODE(marker_stat.st_mode) != 0o600:
            raise ValueError("unsafe fixture marker")
        if file.read() != expected:
            raise ValueError("mismatched fixture marker")
except (OSError, ValueError):
    sys.exit(1)
PY
}

write_owned_marker() {
  python3 - "$OUT_REAL" "$MARKER_NAME" "$MARKER_FORMAT" "$CURRENT_UID" <<'PY'
import json
import os
import stat
import sys

root = sys.argv[1]
marker_name = sys.argv[2]
marker_format = sys.argv[3]
uid = int(sys.argv[4])
root_stat = os.lstat(root)
if not stat.S_ISDIR(root_stat.st_mode) or root_stat.st_uid != uid:
    raise SystemExit("refusing unsafe newly-created fixture root")
os.chmod(root, 0o700)
contents = (json.dumps(
    {"format": marker_format, "root": root, "uid": uid},
    separators=(",", ":"),
    sort_keys=True,
) + "\n").encode("utf-8")
flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
if hasattr(os, "O_NOFOLLOW"):
    flags |= os.O_NOFOLLOW
descriptor = os.open(os.path.join(root, marker_name), flags, 0o600)
with os.fdopen(descriptor, "wb") as file:
    os.fchmod(file.fileno(), 0o600)
    file.write(contents)
PY
}

[[ ${1:-} == --help || ${1:-} == -h ]] && { usage; exit 0; }
[[ $# -eq 1 ]] || { usage >&2; exit 64; }
OUT=$1
command -v python3 >/dev/null 2>&1 || die 'python3 is required'
command -v "$FFMPEG" >/dev/null 2>&1 || die "ffmpeg is required (install an ffmpeg build with native aac/mpeg4)"
OUT_REAL=$(canonical_path "$OUT")
CANONICAL_OUT_REAL=$(canonical_path "$CANONICAL_OUT")
HOME_REAL=$(canonical_path "$HOME")
CURRENT_UID=$(id -u)
[[ "$OUT_REAL" != / && "$OUT_REAL" != "$HOME_REAL" && "$OUT_REAL" != "$REPO_ROOT" ]] \
  || die "refusing unsafe output path: $OUT"
[[ "$OUT_REAL" != "$CANONICAL_OUT_REAL" ]] \
  || die 'refusing canonical fixtures/out; use tools/verify-env/generate-fixtures.sh for the committed tree'

if [[ -e "$OUT" || -L "$OUT" ]]; then
  [[ -d "$OUT" && ! -L "$OUT" ]] || die "refusing non-directory output path: $OUT"
  owned_output || die "refusing unowned existing output path: $OUT (delete it first or choose a new path)"
  rm -rf "$OUT_REAL"
fi
(umask 077; mkdir -p "$OUT_REAL")
write_owned_marker
OUT=$OUT_REAL
"$FFMPEG" -hide_banner -version >"$OUT/FFMPEG.version"

# Generate a 300-second video with mpeg4 at 320x180, 24 fps — same profile as gen.sh.
long_video() {
  local target=$1 title=$2
  mkdir -p "$(dirname "$target")"
  "$FFMPEG" -hide_banner -loglevel error -y -cpuflags 0 -threads 1 \
    -f lavfi -i 'testsrc2=size=320x180:rate=24' -f lavfi -i 'sine=frequency=440:sample_rate=48000' \
    -t "$DURATION" -map 0:v:0 -map 1:a:0 -c:v mpeg4 -q:v 5 -c:a aac \
    -metadata title="$title" -fflags +bitexact "$target"
}

# --- Long Feature (2024): movie, no subtitles (S7 forced transcode, S8 seek) ---

FEATURE_DIR="$OUT/movies/Long Feature (2024)"
long_video "$FEATURE_DIR/Long Feature (2024).mkv" 'Long Feature'

# --- Long Subtitle (2024): movie, external .en.srt + embedded SUBRIP (S9) ---
#
# Long Subtitle runs 303 s (3 s longer than Long Feature and Long Show) so a
# reader can distinguish it from the 300 s items by duration alone. Two
# subtitle tracks: an external .en.srt sidecar and an embedded SUBRIP track.
# The cues are offset 5 s between the two tracks so the active track is
# identifiable from content. Each track is labelled "Sidecar cue N" or
# "Embedded cue N" rather than by menu label.

SUBTITLE_DIR="$OUT/movies/Long Subtitle (2024)"
mkdir -p "$SUBTITLE_DIR"

# External sidecar .srt: cues start at 0 s, one cue every 10 s, each cue lasts 5 s.
{
  cue=0
  for start in $(seq 0 10 $((SUBTITLE_DURATION - 10))); do
    end=$((start + 5))
    printf '%d\n' $((cue + 1))
    printf '%02d:%02d:%02d,000 --> %02d:%02d:%02d,000\n' \
      $((start / 3600)) $(((start % 3600) / 60)) $((start % 60)) \
      $((end / 3600)) $(((end % 3600) / 60)) $((end % 60))
    printf 'Sidecar cue %d\n\n' "$((cue + 1))"
    cue=$((cue + 1))
  done
} >"$SUBTITLE_DIR/Long Subtitle (2024).en.srt"

# Embedded SUBRIP .srt: cues start at 5 s (offset 5 s from the sidecar), one cue every 10 s.
EMBEDDED_SRT="$SUBTITLE_DIR/embedded.srt"
{
  cue=0
  for start in $(seq 5 10 $((SUBTITLE_DURATION - 5))); do
    end=$((start + 5))
    printf '%d\n' $((cue + 1))
    printf '%02d:%02d:%02d,000 --> %02d:%02d:%02d,000\n' \
      $((start / 3600)) $(((start % 3600) / 60)) $((start % 60)) \
      $((end / 3600)) $(((end % 3600) / 60)) $((end % 60))
    printf 'Embedded cue %d\n\n' "$((cue + 1))"
    cue=$((cue + 1))
  done
} >"$EMBEDDED_SRT"

"$FFMPEG" -hide_banner -loglevel error -y -cpuflags 0 -threads 1 \
  -f lavfi -i 'testsrc2=size=320x180:rate=24' -f lavfi -i 'sine=frequency=660:sample_rate=48000' \
  -i "$EMBEDDED_SRT" -t "$SUBTITLE_DURATION" -map 0:v:0 -map 1:a:0 -map 2:0 \
  -c:v mpeg4 -q:v 5 -c:a aac -c:s srt -metadata title='Long Subtitle' -fflags +bitexact \
  "$SUBTITLE_DIR/Long Subtitle (2024).mkv"
rm -f "$EMBEDDED_SRT"

# --- Long Show (2024): TV episode, no subtitles (S10 resume after app restart) ---

SHOW_DIR="$OUT/tv/Long Show (2024)/Season 01"
long_video "$SHOW_DIR/Long Show S01E01 - Long Signal.mkv" 'Long Signal'

MEDIA_COUNT=$(find "$OUT" -type f -name '*.mkv' | wc -l | tr -d ' ')
[[ "$MEDIA_COUNT" == 3 ]] || die "internal generator error: expected 3 media files, got $MEDIA_COUNT"

printf 'generated %s long-duration synthetic media files at %s\n' "$MEDIA_COUNT" "$OUT"
