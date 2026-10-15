#!/bin/sh
# Acceptance test for tools/extract-buildx-digest.sh.
#
# The test reads local fixtures and makes no network request. It verifies
# a recorded CI run report and the required failure cases.
set -eu

script_dir=$(cd "$(dirname "$0")" && pwd)
helper="${script_dir}/extract-buildx-digest.sh"
fixture="${script_dir}/testdata/buildx-inspect-run-548.txt"
expected_digest="sha256:0f2095fa4cea835dfdca5fc7b182bd41a82777daeb993eebdfaf1c8e9bccc8e2"

fail_count=0
pass_count=0

pass() {
    pass_count=$((pass_count + 1))
}

fail() {
    printf 'FAIL: %s\n' "$*" >&2
    fail_count=$((fail_count + 1))
}

# --- Recorded CI run report ----------------------------------------------

# The recorded report produces exactly the expected digest and exits 0.
output=$(sh "${helper}" < "${fixture}" 2>/dev/null) && rc=0 || rc=$?
if [ "${rc}" -eq 0 ]; then
    pass
else
    fail "run-548 report exited ${rc}, expected 0"
fi
if [ "${output}" = "${expected_digest}" ]; then
    pass
else
    fail "run-548 report produced '${output}', expected '${expected_digest}'"
fi

# The output contains only the digest, one line, no extra text.
line_count=$(printf '%s\n' "${output}" | grep -c . || true)
if [ "${line_count}" -eq 1 ]; then
    pass
else
    fail "run-548 output has ${line_count} lines, expected 1"
fi
# The recorded report contains indented vnd.docker.reference.digest: lines
# in its attestation manifest blocks. The helper must not select them. A
# pattern that drops the ^ anchor or matches digest: case-insensitively
# would return the attestation-manifest digests instead of the top-level
# digest. Extract the indented reference digests from the fixture and make
# sure none of them appear in the helper output.
ref_count=$(grep -c 'vnd.docker.reference.digest:' "${fixture}" || true)
if [ "${ref_count}" -gt 0 ]; then
    pass
else
    fail "fixture has no vnd.docker.reference.digest: lines to test"
fi
leaked=0
for ref_digest in $(grep 'vnd.docker.reference.digest:' "${fixture}" | sed 's/.*vnd.docker.reference.digest: //'); do
    case "${output}" in
        *"${ref_digest}"*)
            fail "helper selected an indented vnd.docker.reference.digest: value: ${ref_digest}"
            leaked=1
            ;;
    esac
done
if [ "${leaked}" -eq 0 ]; then
    pass
fi

# --- Empty input --------------------------------------------------------

if sh "${helper}" < /dev/null 2>/dev/null; then
    fail "empty input exited 0, expected nonzero"
else
    pass
fi

# Empty input produces no digest on stdout.
output=$(sh "${helper}" < /dev/null 2>/dev/null) || true
if [ -z "${output}" ]; then
    pass
else
    fail "empty input produced '${output}', expected no output"
fi

# --- Malformed digest ---------------------------------------------------

# A top-level Digest: line with a value that does not match the pattern
# must exit nonzero and produce no digest.
malformed=$(printf 'Name:      ghcr.io/deadfin-org/deadfin:test\nMediaType: application/vnd.oci.image.index.v1+json\nDigest:    not-a-digest\n')
output=$(printf '%s\n' "${malformed}" | sh "${helper}" 2>/dev/null) || true
if [ -z "${output}" ]; then
    pass
else
    fail "malformed digest produced '${output}', expected no output"
fi
if printf '%s\n' "${malformed}" | sh "${helper}" 2>/dev/null; then
    fail "malformed digest exited 0, expected nonzero"
else
    pass
fi

# --- Digest with wrong length -------------------------------------------

short_digest=$(printf 'Name:      ghcr.io/deadfin-org/deadfin:test\nMediaType: application/vnd.oci.image.index.v1+json\nDigest:    sha256:abc123\n')
output=$(printf '%s\n' "${short_digest}" | sh "${helper}" 2>/dev/null) || true
if [ -z "${output}" ]; then
    pass
else
    fail "short digest produced '${output}', expected no output"
fi
if printf '%s\n' "${short_digest}" | sh "${helper}" 2>/dev/null; then
    fail "short digest exited 0, expected nonzero"
else
    pass
fi

# --- Empty Digest value -------------------------------------------------

empty_value=$(printf 'Name:      ghcr.io/deadfin-org/deadfin:test\nMediaType: application/vnd.oci.image.index.v1+json\nDigest:\n')
output=$(printf '%s\n' "${empty_value}" | sh "${helper}" 2>/dev/null) || true
if [ -z "${output}" ]; then
    pass
else
    fail "empty digest value produced '${output}', expected no output"
fi
if printf '%s\n' "${empty_value}" | sh "${helper}" 2>/dev/null; then
    fail "empty digest value exited 0, expected nonzero"
else
    pass
fi

# --- Duplicate top-level digest lines ----------------------------------

# Two unindented Digest: lines must exit nonzero and produce no digest.
duplicate=$(printf 'Name:      ghcr.io/deadfin-org/deadfin:test\nMediaType: application/vnd.oci.image.index.v1+json\nDigest:    sha256:0f2095fa4cea835dfdca5fc7b182bd41a82777daeb993eebdfaf1c8e9bccc8e2\nDigest:    sha256:1111111111111111111111111111111111111111111111111111111111111111\n')
output=$(printf '%s\n' "${duplicate}" | sh "${helper}" 2>/dev/null) || true
if [ -z "${output}" ]; then
    pass
else
    fail "duplicate digest produced '${output}', expected no output"
fi
if printf '%s\n' "${duplicate}" | sh "${helper}" 2>/dev/null; then
    fail "duplicate digest exited 0, expected nonzero"
else
    pass
fi

# --- Indented Digest lines under Manifests are ignored ------------------

# A report with a valid top-level digest and indented entries must
# extract only the top-level digest and exit 0. The helper must not confuse
# an indented line that starts with whitespace with a top-level line.
indented=$(printf 'Name:      ghcr.io/deadfin-org/deadfin:test\nMediaType: application/vnd.oci.image.index.v1+json\nDigest:    sha256:0f2095fa4cea835dfdca5fc7b182bd41a82777daeb993eebdfaf1c8e9bccc8e2\n\nManifests:\n  Name:      ghcr.io/deadfin-org/deadfin@sha256:2222222222222222222222222222222222222222222222222222222222222222\n  MediaType: application/vnd.oci.image.manifest.v1+json\n  Platform:  linux/amd64\n')
output=$(printf '%s\n' "${indented}" | sh "${helper}" 2>/dev/null) && rc=0 || rc=$?
if [ "${rc}" -eq 0 ]; then
    pass
else
    fail "indented report exited ${rc}, expected 0"
fi
if [ "${output}" = "${expected_digest}" ]; then
    pass
else
    fail "indented report produced '${output}', expected '${expected_digest}'"
fi

# --- Regression probe: success-path exit status -------------------------

# A helper that prints the correct digest and then exits 1 must make this
# acceptance script fail. The publish workflow uses set -euo pipefail, so
# a helper that exits 1 on success stops the publish job. This probe builds
# a temporary helper that delegates to the real parser but forces exit 1 on
# the success path. The failure cases still exit nonzero with no output, so
# the probe isolates the success-path exit-status defect.
probe_dir=$(mktemp -d)
probe_helper="${probe_dir}/extract-buildx-digest.sh"
cat > "${probe_helper}" <<PROBE_EOF
#!/bin/sh
set -eu
# Delegate to the real parser. On success, print the digest and exit 1.
# On failure, the real parser exits nonzero with no output, so the
# probe helper exits 1 with no output too.
report=\$(cat)
digest=\$(printf '%s\n' "\${report}" | sh "${helper}" 2>/dev/null) || true
if [ -n "\${digest}" ]; then
    printf '%s\n' "\${digest}"
fi
exit 1
PROBE_EOF

# The bad-exit helper must print the correct digest on stdout for the fixture.
probe_output=$(sh "${probe_helper}" < "${fixture}" 2>/dev/null) || true
if [ "${probe_output}" = "${expected_digest}" ]; then
    pass
else
    fail "regression probe: bad-exit helper produced '${probe_output}', expected '${expected_digest}'"
fi

# The bad-exit helper must exit nonzero on the success path. The run-548
# case asserts this same condition for the real helper. If a future edit
# drops that assertion, a bad-exit helper would print the digest and pass.
sh "${probe_helper}" < "${fixture}" >/dev/null 2>&1 && probe_rc=0 || probe_rc=$?
if [ "${probe_rc}" -ne 0 ]; then
    pass
else
    fail "regression probe: bad-exit helper exited 0, expected nonzero"
fi

# The full acceptance script must reject the bad-exit helper. Re-run this
# script in a temporary tools directory with the bad-exit helper in place
# of the real one. The script must exit nonzero and must not print PASS.
# A guard variable makes the inner run skip this probe and avoid recursion.
if [ -z "${EXTRACT_DIGEST_PROBE_GUARD:-}" ]; then
    probe_tools=$(mktemp -d)
    cp "$0" "${probe_tools}/extract-buildx-digest_test.sh"
    cp "${probe_helper}" "${probe_tools}/extract-buildx-digest.sh"
    mkdir -p "${probe_tools}/testdata"
    cp "${fixture}" "${probe_tools}/testdata/buildx-inspect-run-548.txt"
    probe_result=$(EXTRACT_DIGEST_PROBE_GUARD=1 sh "${probe_tools}/extract-buildx-digest_test.sh" 2>/dev/null) || true
    case "${probe_result}" in
        *"extract-buildx-digest: PASS"*)
            fail "regression probe: acceptance script printed PASS for a bad-exit helper"
            ;;
        *)
            pass
            ;;
    esac
    probe_exit=0
    EXTRACT_DIGEST_PROBE_GUARD=1 sh "${probe_tools}/extract-buildx-digest_test.sh" >/dev/null 2>&1 || probe_exit=$?
    if [ "${probe_exit}" -ne 0 ]; then
        pass
    else
        fail "regression probe: acceptance script exited 0 for a bad-exit helper, expected nonzero"
    fi
    rm -rf "${probe_tools}"
else
    pass
fi

rm -rf "${probe_dir}"

# --- Result -------------------------------------------------------------

if [ "${fail_count}" -gt 0 ]; then
    printf 'extract-buildx-digest: FAIL (%d passed, %d failed)\n' "${pass_count}" "${fail_count}" >&2
    exit 1
fi

printf 'extract-buildx-digest: PASS\n'
