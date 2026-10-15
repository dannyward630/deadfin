#!/bin/sh
# Sourceable helpers shared by the verify-env host wrapper and entrypoint.

verify_env_docker_socket_gid() {
    socket_path=$1

    case $(uname -s) in
        Linux)
            if socket_gid=$(stat -c %g "$socket_path" 2>/dev/null); then
                printf '%s\n' "$socket_gid"
            else
                stat -f %g "$socket_path"
            fi
            ;;
        Darwin)
            if socket_gid=$(stat -f %g "$socket_path" 2>/dev/null); then
                printf '%s\n' "$socket_gid"
            else
                stat -c %g "$socket_path"
            fi
            ;;
        *)
            echo "verify-env: unsupported host for Docker socket group lookup: $(uname -s)" >&2
            return 1
            ;;
    esac
}

verify_env_effective_docker_socket_gid() {
    case $1 in
        ''|*[!0-9]*)
            echo "verify-env: effective Docker socket group ID is not numeric: $1" >&2
            return 1
            ;;
        *)
            printf '%s\n' "$1"
            ;;
    esac
}

verify_env_linux_arch() {
    case $1 in
        x86_64|amd64)
            printf '%s\n' amd64
            ;;
        aarch64|arm64)
            printf '%s\n' arm64
            ;;
        *)
            echo "verify-env: unsupported Linux architecture: $1" >&2
            return 1
            ;;
    esac
}

# Convert Docker's allocated loopback port to the host-network gate DSN.
verify_env_postgres_dsn() {
    case $1 in
        127.0.0.1:*) verify_env_pg_port=${1#127.0.0.1:} ;;
        *) echo 'verify-env: PostgreSQL must publish on loopback' >&2; return 1 ;;
    esac
    case $verify_env_pg_port in
        ''|*[!0-9]*) echo 'verify-env: invalid PostgreSQL port' >&2; return 1 ;;
    esac
    if [ "$verify_env_pg_port" -lt 1 ] || [ "$verify_env_pg_port" -gt 65535 ]; then
        echo 'verify-env: PostgreSQL port out of range' >&2
        return 1
    fi
    printf 'postgres://deadfin:deadfin@127.0.0.1:%s/deadfin?sslmode=disable\n' "$verify_env_pg_port"
}
