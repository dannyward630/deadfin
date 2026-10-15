#!/bin/sh
# Print the floating image tags that a release tag moves.
#
# Usage: git tag --list | tools/release-tags.sh TAG
#
# Standard input lists every tag of the repository. ":alpha" moves when TAG
# is the newest release or release candidate. ":latest" moves when TAG is a
# release (no -rc.N) and the newest release. An older tag that runs again
# moves neither, so a rerun never moves a floating tag backward. The output
# is one line, "floating_tags=" and a space-separated list, possibly empty.
set -eu
tag=${1:?tag required}

key() {
  printf '%s\n' "$1" | awk '
    match($0, /^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$/) {
      s = $0; sub(/^v/, "", s)
      rc = 999999
      if (index(s, "-rc.")) { rc = substr(s, index(s, "-rc.") + 4); s = substr(s, 1, index(s, "-rc.") - 1) }
      split(s, p, ".")
      printf "%09d%09d%09d%09d\n", p[1], p[2], p[3], rc
    }'
}

own=$(key "$tag")
[ -n "$own" ] || { echo "release-tags: $tag is not a release or rc tag" >&2; exit 1; }
case "$own" in *999999) release=1 ;; *) release=0 ;; esac

newest_any=$own
newest_release=$own
while IFS= read -r other; do
  k=$(key "$other")
  [ -n "$k" ] || continue
  [ "$k" \> "$newest_any" ] && newest_any=$k
  case "$k" in *999999) [ "$k" \> "$newest_release" ] && newest_release=$k ;; esac
done

out=""
[ "$own" = "$newest_any" ] && out="alpha"
[ "$release" = 1 ] && [ "$own" = "$newest_release" ] && out="${out:+$out }latest"
printf 'floating_tags=%s\n' "$out"
