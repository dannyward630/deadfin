#!/usr/bin/env bash
# Generates exactly 30 tiny, royalty-free media files. Nothing in fixtures/out is checked in.
# Requires ffmpeg with the native aac/mpeg4 encoders and libmp3lame. The HLG sample is a committed artifact.
set -Eeuo pipefail
IFS=$'\n\t'

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
REPO_ROOT=$(cd "$ROOT/../.." && pwd -P)
DEFAULT_OUT="$ROOT/fixtures/out"
OUT=${GOLDENREC_FIXTURES_DIR:-}
FFMPEG=${FFMPEG:-ffmpeg}
HLG_ARTIFACT="$ROOT/fixtures/attested/hlg-sample.mkv"
HLG_ARTIFACT_SHA256='0a96f3869116102bf42aabd86e428268c1bd1dafd4d74905d0c81f16ad8ef39f'
MARKER_NAME='.goldenrec-fixtures-owner'
MARKER_FORMAT='goldenrec-fixtures-owner-v1'

usage() {
  cat <<'EOF'
Usage: tools/goldenrec/fixtures/gen.sh [--help]

Creates a synthetic Jellyfin fixture tree at GOLDENREC_FIXTURES_DIR. Use
tools/verify-env/generate-fixtures.sh for the committed-fixture output.
Direct runs must select an isolated override. They cannot write the committed
output. A nonexistent destination is created; an existing destination is
replaced only when it carries this generator's ownership marker. Requires
ffmpeg (the HLG sample is a committed artifact). Generated media hashes are
toolchain-dependent. FFMPEG.version records the exact generator build.
EOF
}
die() { printf 'goldenrec fixtures: %s\n' "$*" >&2; exit 1; }
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
[[ ${1:-} != --help ]] || { usage; exit 0; }
[[ $# -eq 0 ]] || { usage >&2; exit 64; }
[[ -n "$OUT" ]] || die 'GOLDENREC_FIXTURES_DIR must select an isolated output'
command -v python3 >/dev/null 2>&1 || die 'python3 is required'
command -v "$FFMPEG" >/dev/null 2>&1 || die "ffmpeg is required; install an ffmpeg build with libmp3lame"
OUT_REAL=$(canonical_path "$OUT")
DEFAULT_OUT_REAL=$(canonical_path "$DEFAULT_OUT")
HOME_REAL=$(canonical_path "$HOME")
CURRENT_UID=$(id -u)
[[ "$OUT_REAL" != / && "$OUT_REAL" != "$HOME_REAL" && "$OUT_REAL" != "$REPO_ROOT" ]] || die "refusing unsafe output path: $OUT"
[[ "$OUT_REAL" != "$DEFAULT_OUT_REAL" ]] \
  || die 'committed output requires tools/verify-env/generate-fixtures.sh'

if [[ -e "$OUT" || -L "$OUT" ]]; then
  [[ -d "$OUT" && ! -L "$OUT" ]] || die "refusing non-directory output path: $OUT"
  owned_output || die "refusing unsafe existing output path: $OUT"
  rm -rf "$OUT_REAL"
fi
(umask 077; mkdir -p "$OUT_REAL")
write_owned_marker
OUT=$OUT_REAL
"$FFMPEG" -hide_banner -version >"$OUT/FFMPEG.version"
video() {
  local target=$1 title=$2
  mkdir -p "$(dirname "$target")"
  "$FFMPEG" -hide_banner -loglevel error -y -cpuflags 0 -threads 1 \
    -f lavfi -i 'testsrc2=size=320x180:rate=24' -f lavfi -i 'sine=frequency=440:sample_rate=48000' \
    -t 1 -map 0:v:0 -map 1:a:0 -c:v mpeg4 -q:v 5 -c:a aac -metadata title="$title" -fflags +bitexact "$target"
}
track() {
  local target=$1 number=$2 title=$3
  mkdir -p "$(dirname "$target")"
  "$FFMPEG" -hide_banner -loglevel error -y -f lavfi -i "sine=frequency=$((300 + number * 20)):sample_rate=44100" \
    -t 1 -c:a libmp3lame -metadata album='Synthetic Album' -metadata artist='Goldenrec Generator' \
    -metadata track="$number" -metadata title="$title" "$target"
}
nfo() { printf '%s\n' "$2" >"$1"; }
# Generates a valid RGBA PNG with only Python's standard library. This avoids an
# image-processing dependency and makes every local image plainly synthetic.
artwork() {
  local target=$1 width=$2 height=$3 red=$4 green=$5 blue=$6 alpha=$7
  mkdir -p "$(dirname "$target")"
  python3 - "$target" "$width" "$height" "$red" "$green" "$blue" "$alpha" <<'PY'
import pathlib, struct, sys, zlib

target, width, height, red, green, blue, alpha = sys.argv[1:]
width, height, red, green, blue, alpha = map(int, (width, height, red, green, blue, alpha))
if min(width, height) <= 0 or any(not 0 <= value <= 255 for value in (red, green, blue, alpha)):
    raise SystemExit("invalid deterministic artwork dimensions or color")
rows = []
for y in range(height):
    row = bytearray([0])
    for x in range(width):
        # A nonuniform but repeatable pattern catches crop/resize/orientation mistakes.
        row.extend(((red + x * 7 + y * 3) & 255, (green + x * 5 + y * 11) & 255, (blue + x * 13 + y * 2) & 255, alpha if (x + y) % 19 else alpha // 2))
    rows.append(bytes(row))
def chunk(kind, payload):
    return struct.pack(">I", len(payload)) + kind + payload + struct.pack(">I", zlib.crc32(kind + payload) & 0xffffffff)
png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(b"".join(rows), 9)) + chunk(b"IEND", b"")
pathlib.Path(target).write_bytes(png)
PY
}
jpeg_from_png() {
  local source=$1 target=$2
  mkdir -p "$(dirname "$target")"
  "$FFMPEG" -hide_banner -loglevel error -y -i "$source" -frames:v 1 -c:v mjpeg -q:v 2 -fflags +bitexact "$target"
}
oriented_jpeg() {
  local source=$1 target=$2 orientation=$3
  python3 - "$source" "$target" "$orientation" <<'PY'
import pathlib, struct, sys

source, target = map(pathlib.Path, sys.argv[1:3])
orientation = int(sys.argv[3])
if not 1 <= orientation <= 8:
    raise SystemExit("EXIF orientation must be in [1, 8]")
payload = b"Exif\x00\x00MM\x00*\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01" + struct.pack(">H", orientation) + b"\x00\x00\x00\x00\x00\x00"
body = source.read_bytes()
if not body.startswith(b"\xff\xd8"):
    raise SystemExit("source is not JPEG")
target.parent.mkdir(parents=True, exist_ok=True)
target.write_bytes(body[:2] + b"\xff\xe1" + struct.pack(">H", len(payload) + 2) + payload + body[2:])
PY
}

# Multi-version movie, ordinary films, odd Emby.Naming-style punctuation, and box-set members.
video "$OUT/movies/Synthetic Feature (2024)/Synthetic Feature (2024) - 1080p.mkv" 'Synthetic Feature 1080p'
video "$OUT/movies/Synthetic Feature (2024)/Synthetic Feature (2024) - 720p.mkv" 'Synthetic Feature 720p'
nfo "$OUT/movies/Synthetic Feature (2024)/movie.nfo" '<movie><title>Synthetic Feature</title><year>2024</year><lockdata>true</lockdata></movie>'
video "$OUT/movies/Standalone Movie (2023)/Standalone Movie (2023).mkv" 'Standalone Movie'
nfo "$OUT/movies/Standalone Movie (2023)/movie.nfo" '<movie><title>Standalone Movie</title><year>2023</year><lockdata>true</lockdata><studio>Duplicate Studio</studio><studio>Duplicate Studio</studio><studio>Case Studio</studio><studio>case studio</studio><genre>Synth Genre</genre><genre>synth genre</genre><tag>Duplicate Tag</tag><tag>Duplicate Tag</tag></movie>'
video "$OUT/movies/Odd Names/Film.Name.2022.1080p.BluRay.mkv" 'Dot Delimited Film'
video "$OUT/movies/Odd Names/The [Odd] Film (2021) {edition-Director's Cut}.mkv" 'Bracketed Film'
video "$OUT/movies/Odd Names/Café déjà-vu (2020).mkv" 'Unicode Film'
nfo "$OUT/movies/Odd Names/Film.Name.2022.1080p.BluRay.nfo" '<movie><title>Dot Delimited Film</title><year>2022</year><lockdata>true</lockdata></movie>'
nfo "$OUT/movies/Odd Names/The [Odd] Film (2021) {edition-Director's Cut}.nfo" '<movie><title>Bracketed Film</title><year>2021</year><lockdata>true</lockdata></movie>'
nfo "$OUT/movies/Odd Names/Café déjà-vu (2020).nfo" '<movie><title>Café déjà-vu</title><year>2020</year><lockdata>true</lockdata></movie>'
# Upstream BoxSetResolver recognizes the [boxset] suffix (vendored source lines 24-42).
video "$OUT/movies/Synthetic Collection [boxset]/Collection Film One (2020).mkv" 'Collection Film One'
nfo "$OUT/movies/Synthetic Collection [boxset]/Collection Film One (2020).nfo" '<movie><title>Collection Film One</title><year>2020</year><lockdata>true</lockdata></movie>'
video "$OUT/movies/Synthetic Collection [boxset]/Collection Film Two (2021).mkv" 'Collection Film Two'
nfo "$OUT/movies/Synthetic Collection [boxset]/Collection Film Two (2021).nfo" '<movie><title>Collection Film Two</title><year>2021</year><lockdata>true</lockdata></movie>'
video "$OUT/movies/Synthetic Collection [boxset]/Collection Film Three (2022).mkv" 'Collection Film Three'
nfo "$OUT/movies/Synthetic Collection [boxset]/Collection Film Three (2022).nfo" '<movie><title>Collection Film Three</title><year>2022</year><lockdata>true</lockdata></movie>'

# Two seasons plus specials and NFO metadata.
SERIES="$OUT/tv/Synthetic Show (2024)"
mkdir -p "$SERIES/Specials" "$SERIES/Season 01" "$SERIES/Season 02"
nfo "$SERIES/tvshow.nfo" '<tvshow><title>Synthetic Show</title><premiered>2024-01-01</premiered></tvshow>'
nfo "$SERIES/Season 01/season.nfo" '<season><seasonnumber>1</seasonnumber><title>First Synthetic Season</title></season>'
nfo "$SERIES/Season 02/season.nfo" '<season><seasonnumber>2</seasonnumber><title>Second Synthetic Season</title></season>'
video "$SERIES/Specials/Synthetic Show S00E01 - Pilot Extra.mkv" 'Pilot Extra'
video "$SERIES/Specials/Synthetic Show S00E02 - Behind The Bars.mkv" 'Behind The Bars'
video "$SERIES/Season 01/Synthetic Show S01E01 - First Signal.mkv" 'First Signal'
video "$SERIES/Season 01/Synthetic Show S01E02 - Second Signal.mkv" 'Second Signal'
video "$SERIES/Season 01/Synthetic Show S01E03 - Third Signal.mkv" 'Third Signal'
video "$SERIES/Season 02/Synthetic Show S02E01 - Return Signal.mkv" 'Return Signal'
video "$SERIES/Season 02/Synthetic Show S02E02 - Wide Signal.mkv" 'Wide Signal'
video "$SERIES/Season 02/Synthetic Show S02E03 - Final Signal.mkv" 'Final Signal'

# Explicitly test both sidecar and embedded subtitles.
SUBDIR="$OUT/movies/Subtitle Sample (2024)"
mkdir -p "$SUBDIR"
printf '1\n00:00:00,000 --> 00:00:00,800\nSynthetic subtitle.\n' >"$SUBDIR/Subtitle Sample (2024).en.srt"
"$FFMPEG" -hide_banner -loglevel error -y -cpuflags 0 -threads 1 \
  -f lavfi -i 'testsrc2=size=320x180:rate=24' -f lavfi -i 'sine=frequency=660:sample_rate=48000' \
  -i "$SUBDIR/Subtitle Sample (2024).en.srt" -t 1 -map 0:v:0 -map 1:a:0 -map 2:0 \
  -c:v mpeg4 -q:v 5 -c:a aac -c:s srt -metadata title='Subtitle Sample' -fflags +bitexact "$SUBDIR/Subtitle Sample (2024).mkv"
nfo "$SUBDIR/Subtitle Sample (2024).nfo" '<movie><title>Subtitle Sample</title><year>2024</year><lockdata>true</lockdata></movie>'

# HLG is a committed HEVC 10-bit BT.2020/ARIB STD-B67 artifact. See its
# provenance in tools/goldenrec/REGEN.md.
# Copy it instead of encoding so the bytes do not depend on the host CPU.
HDRDIR="$OUT/movies/HLG Sample (2024)"
mkdir -p "$HDRDIR"
HLG_TARGET="$HDRDIR/HLG Sample (2024).mkv"
if [[ ! -f "$HLG_ARTIFACT" ]]; then
  die "attested HLG artifact missing at $HLG_ARTIFACT; see tools/goldenrec/REGEN.md"
fi
HLG_DIGEST=$(sha256sum "$HLG_ARTIFACT" | cut -d' ' -f1)
if [[ "$HLG_DIGEST" != "$HLG_ARTIFACT_SHA256" ]]; then
  die "attested HLG artifact does not match pinned digest; see tools/goldenrec/REGEN.md"
fi
cp "$HLG_ARTIFACT" "$HLG_TARGET"
nfo "$HDRDIR/HLG Sample (2024).nfo" '<movie><title>HLG Sample</title><year>2024</year><lockdata>true</lockdata></movie>'

# Tagged music album (11 real tracks) and an intentional Blu-ray disc-stub ignored by scans.
# Track 1 carries a probe performer tag and a sidecar NFO so the duplicate-credit
# path gets real coverage. The NFO Actor has no role. The probe performer value
# maps to an Actor with a role. The assembled-plan pass fills the empty role and
# removes the duplicate. The NFO Director for the same person must survive because
# the collapse key includes PersonKind. See scanner_quarantine.go.
PATHO_AUDIO="$OUT/music/Goldenrec Generator/Synthetic Album"
mkdir -p "$PATHO_AUDIO"
"$FFMPEG" -hide_banner -loglevel error -y -f lavfi -i "sine=frequency=320:sample_rate=44100" \
  -t 1 -c:a libmp3lame -metadata album='Synthetic Album' -metadata artist='Goldenrec Generator' \
  -metadata track=1 -metadata title='Synthetic Track 1' \
  -metadata performer='Synthetic Performer (Lead Vocalist)' "$PATHO_AUDIO/1 - Synthetic Track 1.mp3"
for n in $(seq 2 11); do track "$PATHO_AUDIO/${n} - Synthetic Track ${n}.mp3" "$n" "Synthetic Track ${n}"; done
nfo "$PATHO_AUDIO/1 - Synthetic Track 1.nfo" '<album><title>Synthetic Album</title><actor><name>Synthetic Performer</name></actor><director><name>Synthetic Performer</name></director></album>'
printf 'index: 00000001\ntitle: Synthetic Disc Stub\n' >"$OUT/movies/Synthetic Disc.bluray.disc"
# These local sidecars are source material for the stock image corpus. They are
# image files only, so the deliberate 43-identity library inventory is unchanged.
SEED_IMAGE="$OUT/.goldenrec-artwork-seed.png"
artwork "$SEED_IMAGE" 640 360 34 111 183 255
LANDSCAPE="$OUT/movies/Standalone Movie (2023)/Standalone Movie (2023).jpg"
jpeg_from_png "$SEED_IMAGE" "$LANDSCAPE"
artwork "$OUT/movies/HLG Sample (2024)/HLG Sample (2024).png" 640 360 74 151 81 160
"$FFMPEG" -hide_banner -loglevel error -y -i "$SEED_IMAGE" -frames:v 1 -c:v libwebp -quality 82 -fflags +bitexact "$OUT/movies/Subtitle Sample (2024)/Subtitle Sample (2024).webp"
# Photo items, unlike Movie item image routes, exercise stock Jellyfin's EXIF
# auto-orientation path. These are deliberately local sources for the eight
# AutoOrient corpus rows.
for orientation in 1 2 3 4 5 6 7 8; do
  oriented_jpeg "$LANDSCAPE" "$OUT/photos/orientation-${orientation}.jpg" "$orientation"
done
rm -f "$SEED_IMAGE"
ATTACHED_IMAGE_COUNT=$(find "$OUT" -type f \( -name '*.jpg' -o -name '*.png' -o -name '*.webp' \) | wc -l | tr -d ' ')
[[ "$ATTACHED_IMAGE_COUNT" == 11 ]] || die "internal generator error: expected 11 attached source images, got $ATTACHED_IMAGE_COUNT"

MEDIA_COUNT=$(find "$OUT" -type f \( -name '*.mkv' -o -name '*.mp3' \) | wc -l | tr -d ' ')
[[ "$MEDIA_COUNT" == 30 ]] || die "internal generator error: expected 30 media files, got $MEDIA_COUNT"
python3 - "$OUT" "$MARKER_NAME" >"$OUT/MANIFEST.sha256" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
excluded = {pathlib.Path('MANIFEST.sha256'), pathlib.Path(sys.argv[2])}
for path in sorted(p for p in root.rglob('*') if p.is_file() and p.relative_to(root) not in excluded):
    print(f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.relative_to(root)}')
PY
printf 'generated %s synthetic media files at %s\n' "$MEDIA_COUNT" "$OUT"
