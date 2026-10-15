#!/bin/sh
# Regression tests for the sourceable verify-env portability helpers.
# Run with: sh tools/verify-env/portability_test.sh

set -u

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
helper="$script_dir/lib.sh"
failures=0

pass() {
    printf 'PASS: %s\n' "$1"
}

fail() {
    printf 'FAIL: %s\n' "$1" >&2
    failures=$((failures + 1))
}

if [ ! -f "$helper" ]; then
    printf 'FAIL: sourceable helper is missing: %s\n' "$helper" >&2
    exit 1
fi

tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/verify-env-portability.XXXXXX") || exit 1
trap 'rm -rf "$tmpdir"' 0 HUP INT TERM
mock_bin="$tmpdir/mock-bin"
mkdir -p "$mock_bin"

cat >"$mock_bin/uname" <<'EOF'
#!/bin/sh
case "${1-}" in
    ''|-s) printf '%s\n' "${VERIFY_ENV_TEST_OS:?}" ;;
    *) printf 'unexpected mock uname arguments: %s\n' "$*" >&2; exit 97 ;;
esac
EOF

cat >"$mock_bin/stat" <<'EOF'
#!/bin/sh
case "${1-}:${2-}" in
    -f:%g)
        if [ "${3-}" != "${VERIFY_ENV_TEST_SOCKET:?}" ]; then
            printf 'mock stat received unexpected socket path: %s\n' "${3-}" >&2
            exit 98
        fi
        printf '%s %s %s\n' "$1" "$2" "$3" >>"${VERIFY_ENV_TEST_STAT_LOG:?}"
        if [ "${VERIFY_ENV_TEST_FAIL_BSD_STAT:-0}" = 1 ]; then
            printf 'mock BSD stat is unavailable\n' >&2
            exit 1
        fi
        printf '%s\n' "${VERIFY_ENV_TEST_GID:?}"
        ;;
    -c:%g)
        if [ "${3-}" != "${VERIFY_ENV_TEST_SOCKET:?}" ]; then
            printf 'mock stat received unexpected socket path: %s\n' "${3-}" >&2
            exit 98
        fi
        printf '%s %s %s\n' "$1" "$2" "$3" >>"${VERIFY_ENV_TEST_STAT_LOG:?}"
        printf '%s\n' "${VERIFY_ENV_TEST_GID:?}"
        ;;
    *)
        printf 'unexpected mock stat arguments: %s\n' "$*" >&2
        exit 97
        ;;
esac
EOF
chmod 755 "$mock_bin/uname" "$mock_bin/stat"

PATH="$mock_bin:$PATH"
export PATH

# The helper's public API must be available after a normal shell source.
# shellcheck disable=SC1090
. "$helper"

socket_fixture="$tmpdir/docker.sock"
: >"$socket_fixture"
export VERIFY_ENV_TEST_SOCKET="$socket_fixture"
export VERIFY_ENV_TEST_STAT_LOG="$tmpdir/stat.log"

run_helper() {
    result_stdout_file="$tmpdir/stdout"
    result_stderr_file="$tmpdir/stderr"
    : >"$result_stdout_file"
    : >"$result_stderr_file"
    if "$@" >"$result_stdout_file" 2>"$result_stderr_file"; then
        result_status=0
    else
        result_status=$?
    fi
    result_stdout=$(cat "$result_stdout_file")
    result_stderr=$(cat "$result_stderr_file")
}

expect_output() {
    test_name=$1
    expected=$2
    shift 2
    run_helper "$@"
    if [ "$result_status" -ne 0 ]; then
        fail "$test_name (exit $result_status: $result_stderr)"
    elif [ "$result_stdout" != "$expected" ]; then
        fail "$test_name (got '$result_stdout', want '$expected')"
    else
        pass "$test_name"
    fi
}

expect_failure() {
    test_name=$1
    diagnostic=$2
    shift 2
    run_helper "$@"
    if [ "$result_status" -eq 0 ]; then
        fail "$test_name (unexpected success: $result_stdout)"
    elif [ -n "$result_stdout" ]; then
        fail "$test_name (failure wrote stdout: $result_stdout)"
    elif case "$result_stderr" in *"$diagnostic"*) true ;; *) false ;; esac; then
        pass "$test_name"
    else
        fail "$test_name (stderr '$result_stderr' does not mention '$diagnostic')"
    fi
}

# Socket ownership must use GNU stat on Linux.
: >"$VERIFY_ENV_TEST_STAT_LOG"
export VERIFY_ENV_TEST_OS=Linux
export VERIFY_ENV_TEST_GID=2468
expect_output "Linux socket gid uses GNU stat" "2468" verify_env_docker_socket_gid "$socket_fixture"
if [ "$(cat "$VERIFY_ENV_TEST_STAT_LOG")" != "-c %g $socket_fixture" ]; then
    fail "Linux socket gid invokes stat -c %g"
fi

# Socket ownership must use BSD stat on Darwin rather than Linux-only flags.
: >"$VERIFY_ENV_TEST_STAT_LOG"
export VERIFY_ENV_TEST_OS=Darwin
export VERIFY_ENV_TEST_GID=1357
export VERIFY_ENV_TEST_FAIL_BSD_STAT=0
expect_output "Darwin socket gid uses BSD stat" "1357" verify_env_docker_socket_gid "$socket_fixture"
if [ "$(cat "$VERIFY_ENV_TEST_STAT_LOG")" != "-f %g $socket_fixture" ]; then
    fail "Darwin socket gid invokes stat -f %g"
fi

# Some Darwin hosts resolve GNU stat first. Fall back when the BSD flags fail.
: >"$VERIFY_ENV_TEST_STAT_LOG"
export VERIFY_ENV_TEST_FAIL_BSD_STAT=1
expect_output "Darwin falls back to GNU stat" "1357" verify_env_docker_socket_gid "$socket_fixture"
if [ "$(cat "$VERIFY_ENV_TEST_STAT_LOG")" != "-f %g $socket_fixture
-c %g $socket_fixture" ]; then
    fail "Darwin fallback tries BSD stat then GNU stat"
fi
export VERIFY_ENV_TEST_FAIL_BSD_STAT=0

# Unsupported hosts must not quietly select either stat dialect.
export VERIFY_ENV_TEST_OS=FreeBSD
expect_failure "unsupported host rejects socket gid lookup" "unsupported" verify_env_docker_socket_gid "$socket_fixture"

# Docker Desktop can remap the host socket GID inside the gate container.
# The Compose probe output is therefore the authoritative group_add value.
expect_output "effective container socket gid is emitted" "991" verify_env_effective_docker_socket_gid "991"
expect_failure "empty effective socket gid is rejected" "numeric" verify_env_effective_docker_socket_gid ""
expect_failure "non-numeric effective socket gid is rejected" "numeric" verify_env_effective_docker_socket_gid "gid=991"
multiline_probe='991
992'
expect_failure "multiline effective socket gid is rejected" "numeric" verify_env_effective_docker_socket_gid "$multiline_probe"

skia_root="$tmpdir/skia"
amd64_library="$skia_root/3.119.4/linux-amd64/libSkiaSharp.so"
arm64_library="$skia_root/3.119.4/linux-arm64/libSkiaSharp.so"
mkdir -p "$(dirname "$amd64_library")" "$(dirname "$arm64_library")"
: >"$amd64_library"
: >"$arm64_library"

# Architecture aliases must select only the matching Linux native asset.
expect_output "x86_64 selects linux-amd64 Skia" "$amd64_library" verify_env_skia_library "$skia_root" x86_64
expect_output "aarch64 selects linux-arm64 Skia" "$arm64_library" verify_env_skia_library "$skia_root" aarch64

# Never fall back to the other architecture when the requested asset is absent.
mismatch_root="$tmpdir/mismatch"
mkdir -p "$mismatch_root/3.119.4/linux-arm64"
: >"$mismatch_root/3.119.4/linux-arm64/libSkiaSharp.so"
expect_failure "missing requested architecture rejects mismatch" "linux-amd64/libSkiaSharp.so" verify_env_skia_library "$mismatch_root" amd64

# An unknown architecture is not a license to guess a library path.
expect_failure "unsupported architecture is rejected" "unsupported" verify_env_skia_library "$skia_root" ppc64le

if [ "$failures" -ne 0 ]; then
    printf 'FAIL: %s portability regression(s) failed\n' "$failures" >&2
    exit 1
fi

printf 'PASS: verify-env portability regressions complete\n'
