#!/bin/sh
set -eu
. "$(dirname "$0")/lib.sh"
for port in 5433 32001 65535; do
    test "$(verify_env_postgres_dsn "127.0.0.1:$port")" = "postgres://deadfin:deadfin@127.0.0.1:$port/deadfin?sslmode=disable"
done
for address in 0.0.0.0:5433 127.0.0.1:0 127.0.0.1:65536 127.0.0.1:abc '127.0.0.1:12 extra'; do
    if verify_env_postgres_dsn "$address" >/dev/null 2>&1; then
        echo "accepted invalid address: $address" >&2
        exit 1
    fi
done
echo 'postgres address tests passed'
