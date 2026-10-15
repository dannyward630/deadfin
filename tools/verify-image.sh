#!/bin/sh
# Verify a locally loaded Deadfin image can serve HTTP on the requested platform.
set -eu

usage() {
    printf '%s\n' "usage: $0 IMAGE_REFERENCE EXPECTED_PLATFORM" >&2
    exit 2
}

fail() {
    printf 'verify-image: %s\n' "$*" >&2
    exit 1
}

if [ "$#" -ne 2 ]; then
    usage
fi

image_reference=$1
expected_platform=$2
case "$expected_platform" in
    */*)
        expected_os=${expected_platform%%/*}
        expected_arch=${expected_platform#*/}
        case "$expected_os:$expected_arch" in
            :*|*::|*:*/*) usage ;;
        esac
        ;;
    *)
        usage
        ;;
esac

case "${VERIFY_IMAGE_TIMEOUT_SECONDS:-30}" in
    ''|*[!0-9]*) fail "VERIFY_IMAGE_TIMEOUT_SECONDS must be a positive integer" ;;
esac
timeout_seconds=${VERIFY_IMAGE_TIMEOUT_SECONDS:-30}
if [ "$timeout_seconds" -le 0 ]; then
    fail "VERIFY_IMAGE_TIMEOUT_SECONDS must be a positive integer"
fi

DOCKER=${DOCKER:-docker}
CURL=${CURL:-curl}
command -v "$DOCKER" >/dev/null 2>&1 || fail "Docker command '$DOCKER' is not available"
command -v "$CURL" >/dev/null 2>&1 || fail "HTTP client command '$CURL' is not available"

container_name="deadfin-image-verify-$$-$(date +%s)"
container_id=
run_attempted=0

cleanup() {
    status=$?
    if [ "$run_attempted" -eq 1 ]; then
        if ! "$DOCKER" rm --force "${container_id:-$container_name}" >/dev/null 2>&1; then
            if [ "$status" -eq 0 ]; then
                printf "verify-image: failed to remove container '%s'; remove it manually\n" \
                    "${container_id:-$container_name}" >&2
                trap - EXIT
                exit 1
            fi
        fi
    fi
    trap - EXIT
    exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

image_platform=$("$DOCKER" image inspect --format '{{.Os}}/{{.Architecture}}' "$image_reference") \
    || fail "unable to inspect local image '$image_reference'"
if [ "$image_platform" != "$expected_platform" ]; then
    fail "image platform is '$image_platform'; expected '$expected_platform'"
fi

run_attempted=1
container_id=$("$DOCKER" run --detach --name "$container_name" --platform "$expected_platform" \
    --publish '127.0.0.1::8096' "$image_reference") \
    || fail "failed to launch image '$image_reference' for platform '$expected_platform'"

port_mapping=$("$DOCKER" port "$container_id" '8096/tcp' 2>/dev/null || :)
if [ -z "$port_mapping" ]; then
    fail "container did not publish required TCP port 8096"
fi
host_port=${port_mapping##*:}
case "$host_port" in
    ''|*[!0-9]*) fail "container published port 8096 with an unusable host mapping '$port_mapping'" ;;
esac

container_running() {
    "$DOCKER" inspect --format '{{.State.Running}}' "$container_id" 2>/dev/null
}

startup_exit() {
    exit_code=$("$DOCKER" inspect --format '{{.State.ExitCode}}' "$container_id" 2>/dev/null || printf 'unknown')
    printf "verify-image: container exited during startup (exit code %s)\n" "$exit_code" >&2
    "$DOCKER" logs "$container_id" >&2 || :
    exit 1
}

deadline=$(( $(date +%s) + timeout_seconds ))
url="http://127.0.0.1:${host_port}/__deadfin_image_verifier__"
while :; do
    if [ "$(container_running || printf 'false')" != 'true' ]; then
        startup_exit
    fi

    http_status=
    if http_status=$("$CURL" --silent --show-error --output /dev/null --write-out '%{http_code}' \
        --max-time 1 "$url"); then
        if [ "$http_status" = '404' ]; then
            printf "verify-image: image '%s' serves HTTP on %s (%s)\n" \
                "$image_reference" "$expected_platform" "$url"
            exit 0
        fi
        if [ "$http_status" != '000' ]; then
            fail "unexpected HTTP status '$http_status' from $url; expected '404'"
        fi
    fi

    if [ "$(date +%s)" -ge "$deadline" ]; then
        fail "timed out after ${timeout_seconds}s waiting for HTTP 404 from $url"
    fi
    sleep 1
done
