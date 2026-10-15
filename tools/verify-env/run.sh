#!/bin/sh
# Run a verify gate inside the Docker gate environment.
#
#   tools/verify-env/run.sh                # runs `make verify-p6`
#   tools/verify-env/run.sh make lint      # any other command
#
# Set VERIFY_ENV_REPO to run against a different Deadfin checkout.
set -eu

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
. "$script_dir/lib.sh"
VERIFY_ENV_REPO=${VERIFY_ENV_REPO:-$(cd "$script_dir/../.." && pwd)}

if [ ! -f "$VERIFY_ENV_REPO/go.mod" ]; then
    echo "run.sh: no deadfin checkout at $VERIFY_ENV_REPO (go.mod missing)" >&2
    exit 1
fi

VERIFY_ENV_UID=$(id -u)
VERIFY_ENV_GID=$(id -g)
docker_socket=/var/run/docker.sock
if [ ! -S "$docker_socket" ]; then
    echo "run.sh: no Docker socket at $docker_socket" >&2
    exit 1
fi
VERIFY_ENV_DOCKER_GID=$(verify_env_docker_socket_gid "$docker_socket")
case $VERIFY_ENV_DOCKER_GID in
    ''|*[!0-9]*)
        echo "run.sh: Docker socket group ID is not numeric: $VERIFY_ENV_DOCKER_GID" >&2
        exit 1
        ;;
esac
export VERIFY_ENV_REPO VERIFY_ENV_UID VERIFY_ENV_GID VERIFY_ENV_DOCKER_GID

compose() {
    docker compose -f "$script_dir/compose.yaml" "$@"
}

# Keep the named cache volume; remove only containers and the network.
cleanup() {
    compose down >/dev/null 2>&1 || true
}
trap cleanup EXIT

compose build gate

# Docker Desktop can remap the socket's group while bind-mounting it into its
# Linux VM. Probe the mounted socket after the image build, then give the final
# non-root gate its effective group rather than assuming host metadata survives.
if ! mounted_socket_gid=$(compose run --rm --no-deps --entrypoint stat gate -c %g "$docker_socket"); then
    echo "run.sh: cannot read the Docker socket group inside the gate" >&2
    exit 1
fi
VERIFY_ENV_DOCKER_GID=$(verify_env_effective_docker_socket_gid "$mounted_socket_gid")
export VERIFY_ENV_DOCKER_GID

# Allocate the database port before starting a host-network gate. In CI the
# requested port is 0 (Docker chooses a free port); local default stays 5433.
compose up -d postgres
postgres_address=$(compose port postgres 5432)
VERIFY_ENV_POSTGRES_DSN=$(verify_env_postgres_dsn "$postgres_address")
export VERIFY_ENV_POSTGRES_DSN
compose run --rm gate "$@"
