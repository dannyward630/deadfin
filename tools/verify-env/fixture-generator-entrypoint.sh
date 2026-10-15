#!/bin/sh
# Enforce the canonical platform and package before publication.
set -eu

expected_ffmpeg_version=7:5.1.9-0+deb12u1

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
    echo "fixture-generator: canonical generation requires Linux/x86_64" >&2
    exit 1
fi

actual_ffmpeg_version=$(dpkg-query -W -f='${Version}' ffmpeg)
if [ "$actual_ffmpeg_version" != "$expected_ffmpeg_version" ]; then
    echo "fixture-generator: ffmpeg package is $actual_ffmpeg_version, want $expected_ffmpeg_version" >&2
    exit 1
fi

unset FFMPEG
unset GOLDENREC_ATTEST_SOURCE_MANIFEST
unset GOLDENREC_CANONICAL_GENERATOR
unset GOLDENREC_FIXTURES_DIR

exec python3 tools/verify-env/publish-fixtures.py "$@"
