#!/bin/sh
# Print the CHANGELOG.md section for one release tag.
#
# Usage: tools/release-notes.sh TAG [CHANGELOG]
#
# A vX.Y.Z tag needs a "## [X.Y.Z]" section. A vX.Y.Z-rc.N tag uses
# "## [X.Y.Z-rc.N]", then "## [X.Y.Z]", then "## [Unreleased]". The output
# is the section body without its heading and without link references.
# Exit 1 when no section applies or the section is empty.
set -eu
tag=${1:?tag required}
changelog=${2:-CHANGELOG.md}
version=${tag#v}
[ -r "$changelog" ] || { echo "release-notes: cannot read $changelog" >&2; exit 1; }

section() {
  awk -v want="$1" '
    /^## \[/ {
      if (on) exit
      name = $0
      sub(/^## \[/, "", name)
      sub(/\].*$/, "", name)
      if (name == want) { on = 1; next }
    }
    /^\[[^]]+\]: / { if (on) exit }
    on { print }
  ' "$changelog" | awk 'NF { seen = 1 } seen' | awk '
    { lines[NR] = $0 } NF { last = NR }
    END { for (i = 1; i <= last; i++) print lines[i] }
  '
}

case "$version" in
  *-rc.*) candidates="$version ${version%-rc.*} Unreleased" ;;
  *) candidates=$version ;;
esac

for name in $candidates; do
  body=$(section "$name")
  if [ -n "$body" ]; then
    printf '%s\n' "$body"
    exit 0
  fi
done
echo "release-notes: $changelog has no section for $tag (looked for: $candidates)" >&2
exit 1
