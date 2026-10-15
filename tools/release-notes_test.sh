#!/bin/sh
# Acceptance test for tools/release-notes.sh. Uses local fixtures only.
set -eu
helper=$(cd "$(dirname "$0")" && pwd)/release-notes.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() { echo "release-notes test: $*" >&2; exit 1; }

cat > "$tmp/CHANGELOG.md" <<'MD'
# Changelog

Intro text.

## [Unreleased]

Next things.

## [0.4.0] - 2026-12-01

### Added

- Four.

## [0.3.0-rc.2] - 2026-10-14

- Two.

## [0.3.0-rc.1] - 2026-10-14

## [0.2.0] - 2026-09-01

- Old.

[Unreleased]: https://example.invalid/compare
[0.4.0]: https://example.invalid/0.4.0
MD

check() {
  got=$("$helper" "$1" "$tmp/CHANGELOG.md") || fail "$1 failed"
  [ "$got" = "$2" ] || fail "$1: got [$got], want [$2]"
}

check v0.4.0 '### Added

- Four.'
check 0.4.0 '### Added

- Four.'
check v0.3.0-rc.2 '- Two.'
check v0.4.0-rc.1 '### Added

- Four.'
check v0.5.0-rc.1 'Next things.'
check v0.2.0 '- Old.'
# An empty rc section falls back to Unreleased (no 0.3.0 section).
check v0.3.0-rc.1 'Next things.'

for tag in v0.5.0 v0.3.0 v0.2.0-beta; do
  if "$helper" "$tag" "$tmp/CHANGELOG.md" >/dev/null 2>&1; then fail "accepted $tag with no section"; fi
done
if "$helper" v0.4.0 "$tmp/missing.md" >/dev/null 2>&1; then fail 'accepted a missing changelog'; fi

# The repository changelog serves the published tags.
root=$(cd "$(dirname "$0")/.." && pwd)
for tag in v0.3.0-rc.1 v0.3.0-rc.2 v0.3.0-rc.3; do
  "$helper" "$tag" "$root/CHANGELOG.md" >/dev/null || fail "repository CHANGELOG.md has no section for $tag"
done
echo 'release notes tests passed'
