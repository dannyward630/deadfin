#!/bin/sh
# Extract the single top-level manifest digest from a Buildx imagetools
# inspect report. The workflow pipes the output of:
#
#   docker buildx imagetools inspect "$ref" --format '{{.Manifest}}'
#
# into this helper on stdin. The Buildx {{.Manifest.Digest}} format
# expression returns the full text report, not one digest.
# This helper reads that report and prints exactly one validated digest.
#
# The report has one top-level Digest: line at column 0. Manifest list
# entries under Manifests: are indented with two spaces and do not
# carry a separate Digest: field. The helper rejects empty, malformed,
# or duplicate top-level digest lines.
set -eu

fail() {
    printf 'extract-buildx-digest: %s\n' "$*" >&2
    exit 1
}

# Read the full report from stdin.
report=$(cat)

# Extract unindented Digest: lines. A line that starts at column 0
# with "Digest:" is a top-level digest. Indented lines (under
# Manifests:) begin with whitespace and are not matched.
digest_lines=$(printf '%s\n' "${report}" | sed -n '/^Digest:/p')

line_count=$(printf '%s\n' "${digest_lines}" | grep -c . || true)

if [ "${line_count}" -eq 0 ]; then
    fail "no top-level Digest: line found in the report"
fi

if [ "${line_count}" -gt 1 ]; then
    fail "found ${line_count} top-level Digest: lines; expected exactly one"
fi

# Extract the value after "Digest:" and any following whitespace.
# Buildx pads the field with a tabwriter, so the value follows one or
# more spaces after the colon.
digest=$(printf '%s' "${digest_lines}" | sed 's/^Digest:[[:space:]]*//')

if [ -z "${digest}" ]; then
    fail "the Digest: line has an empty value"
fi

if ! printf '%s' "${digest}" | grep -Eq '^sha256:[0-9a-f]{64}$'; then
    fail "rejected digest: ${digest}. The digest must start with sha256: and contain 64 hexadecimal characters."
fi

printf '%s\n' "${digest}"
