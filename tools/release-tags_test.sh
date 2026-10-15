#!/bin/sh
# Acceptance test for tools/release-tags.sh. Uses fixed tag lists only.
set -eu
helper=$(cd "$(dirname "$0")" && pwd)/release-tags.sh

fail() { echo "release-tags test: $*" >&2; exit 1; }

check() {
  got=$(printf '%s\n' $2 | "$helper" "$1") || fail "$1 failed"
  [ "$got" = "floating_tags=$3" ] || fail "$1 with [$2]: got [$got], want [floating_tags=$3]"
}

check v0.3.0-rc.3 'v0.2.36 v0.3.0-rc.1 v0.3.0-rc.2 v0.3.0-rc.3' alpha
check v0.3.0 'v0.3.0-rc.3 v0.3.0' 'alpha latest'
check v0.3.0 'v0.3.0-rc.3 v0.3.0 v0.3.1-rc.1' latest
check v0.3.1-rc.1 'v0.3.0 v0.3.1-rc.1' alpha
check v0.3.0-rc.2 'v0.3.0-rc.2 v0.3.0-rc.3' ''
check v0.3.0-rc.10 'v0.3.0-rc.9 v0.3.0-rc.10' alpha
check v0.2.36 'v0.2.36 v0.3.0-rc.1' latest
check v0.2.36 'v0.2.36 v0.3.0' ''
check v0.10.0 'v0.9.9 v0.10.0' 'alpha latest'
check 0.4.0 '0.4.0 v0.3.9 not-a-tag train-x v1.0.0-beta.1' 'alpha latest'
check v0.4.0 '' 'alpha latest'

for tag in master v0.3.0-rc v0.3.0-beta.1 v0.03.0; do
  if printf '' | "$helper" "$tag" >/dev/null 2>&1; then fail "accepted $tag"; fi
done
echo 'release tags tests passed'
