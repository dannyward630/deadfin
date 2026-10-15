#!/bin/sh
# Acceptance test for tools/image-smoke.
#
# The test never pulls or runs an image. A fake docker on PATH records every
# call so argument validation is proved to fail before any pull. The
# identity comparison function is exercised with fixture bodies.
set -eu

script_dir=$(cd "$(dirname "$0")" && pwd)
helper="${script_dir}/image-smoke"
sha=0123456789abcdef0123456789abcdef01234567
version="v0.2.3-dev-0123456789ab"

fail_count=0
pass_count=0
pass() { pass_count=$((pass_count + 1)); }
fail() {
    fail_count=$((fail_count + 1))
    printf 'FAIL: %s\n' "$1" >&2
}

work=$(mktemp -d)
trap 'rm -rf "${work}"' EXIT
mkdir -p "${work}/bin"
calls="${work}/docker-calls"
: > "${calls}"
cat > "${work}/bin/docker" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >> "${calls}"
exit 0
EOF
chmod +x "${work}/bin/docker"
export PATH="${work}/bin:${PATH}"

# --- Argument validation: every missing argument exits before docker pull --

run_expect_fail() {
    desc=$1; shift
    : > "${calls}"
    if bash "${helper}" "$@" >/dev/null 2>&1; then
        fail "${desc}: exited 0"
        return
    fi
    if grep -q '^pull' "${calls}"; then
        fail "${desc}: docker pull ran before validation"
        return
    fi
    pass
}

run_expect_fail "no arguments"
run_expect_fail "missing sha" img
run_expect_fail "missing version" img "${sha}"
run_expect_fail "missing channel" img "${sha}" "${version}"
run_expect_fail "missing dirty" img "${sha}" "${version}" train
run_expect_fail "dirty=maybe rejected" img "${sha}" "${version}" train maybe
run_expect_fail "dirty=yes rejected" img "${sha}" "${version}" train yes

# A complete argument list reaches docker pull (the fake docker then makes
# the label check fail, which is expected here).
: > "${calls}"
bash "${helper}" img "${sha}" "${version}" train false >/dev/null 2>&1 || true
if grep -q '^pull img$' "${calls}"; then
    pass
else
    fail "five valid arguments must reach docker pull"
fi

# --- compare_build_identity -------------------------------------------

# Load the comparison function without running the smoke path.
IMAGE_SMOKE_LIBRARY=1
export IMAGE_SMOKE_LIBRARY

public='{"Version":"12.0.0","ServerName":"deadfin","Id":"x"}'
build_body() {
    printf '{"Component":"deadfin-server","Version":"%s","Channel":"%s","Revision":"%s","Dirty":%s,"BuildTime":"2024-01-01T00:00:00Z","CompatibilityTarget":"%s","ApiVersion":1,"Capabilities":["build-info"]}' "$2" "$3" "$1" "$4" "$5"
}

compare() {
    bash -c '. "$0"; compare_build_identity "$1" "$2" "$3" "$4" "$5" "$6"' "${helper}" "$@"
}

if compare "${public}" "$(build_body "${sha}" "${version}" train false 12.0.0)" "${sha}" "${version}" train false; then
    pass
else
    fail "matching identity must pass"
fi
if compare "${public}" "$(build_body "${sha}" "${version}" train true 12.0.0)" "${sha}" "${version}" train true; then
    pass
else
    fail "matching dirty identity must pass"
fi
if compare "${public}" "$(build_body "ffffffffffffffffffffffffffffffffffffffff" "${version}" train false 12.0.0)" "${sha}" "${version}" train false 2>/dev/null; then
    fail "revision mismatch must fail"
else
    pass
fi
if compare "${public}" "$(build_body "${sha}" "${version}" master false 12.0.0)" "${sha}" "${version}" train false 2>/dev/null; then
    fail "channel mismatch must fail"
else
    pass
fi
if compare "${public}" "$(build_body "${sha}" "${version}" train true 12.0.0)" "${sha}" "${version}" train false 2>/dev/null; then
    fail "dirty mismatch must fail"
else
    pass
fi
if compare "${public}" "$(build_body "${sha}" "v0.2.4-dev-0123456789ab" train false 12.0.0)" "${sha}" "${version}" train false 2>/dev/null; then
    fail "version mismatch must fail"
else
    pass
fi
if compare "${public}" "$(build_body "${sha}" "${version}" train false 11.0.0)" "${sha}" "${version}" train false 2>/dev/null; then
    fail "compatibility-target mismatch must fail"
else
    pass
fi
if compare "${public}" '{"Component":"deadfin-server","Version":"'"${version}"'","Channel":"train","Revision":"'"${sha}"'","Dirty":false,"CompatibilityTarget":"12.0.0","ApiVersion":1,"Capabilities":[]}' "${sha}" "${version}" train false 2>/dev/null; then
    fail "missing build-info capability must fail"
else
    pass
fi
# A release candidate reports channel rc and a prerelease version; the
# public info Version stays the compatibility target.
if compare "${public}" "$(build_body "${sha}" 0.3.0-rc.1 rc false 12.0.0)" "${sha}" 0.3.0-rc.1 rc false; then
    pass
else
    fail "matching release-candidate identity must pass"
fi
if compare "${public}" "$(build_body "${sha}" 0.3.0-rc.1 release false 12.0.0)" "${sha}" 0.3.0-rc.1 rc false 2>/dev/null; then
    fail "release-candidate channel mismatch must fail"
else
    pass
fi
if compare "${public}" "$(build_body "${sha}" 0.3.0-rc.2 rc false 12.0.0)" "${sha}" 0.3.0-rc.1 rc false 2>/dev/null; then
    fail "release-candidate version mismatch must fail"
else
    pass
fi

# --- Result -------------------------------------------------------------

if [ "${fail_count}" -gt 0 ]; then
    printf 'image-smoke: %d passed, %d failed\n' "${pass_count}" "${fail_count}" >&2
    exit 1
fi
printf 'image-smoke: PASS (%d checks)\n' "${pass_count}"
