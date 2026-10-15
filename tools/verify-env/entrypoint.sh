#!/bin/sh
# Prepare the gate container, then run the requested command.
set -eu

# The image is built for the Docker daemon's native Linux architecture. Install
# exactly that architecture's pinned SkiaSharp library; never fall back to
# another architecture. skia-fetch verifies the NuGet package and the library
# by SHA-256 and caches the library under XDG_CACHE_HOME (the /cache volume).
. /usr/local/bin/lib.sh
container_arch=$(verify_env_linux_arch "$(uname -m)")
if [ ! -f /work/tools/skia-fetch/main.go ]; then
    echo "verify-env: /work/tools/skia-fetch not found; is the deadfin checkout mounted?" >&2
    exit 1
fi
(cd /work && go run ./tools/skia-fetch -arch "$container_arch" -o /usr/local/lib/deadfin/libSkiaSharp.so)
printf 'verify-env: Skia architecture: linux-%s\n' "$container_arch"

sdk_version=$(dotnet --version)
if [ "$sdk_version" != "$VERIFY_ENV_DOTNET_SDK_VERSION" ]; then
    echo "verify-env: expected .NET SDK $VERIFY_ENV_DOTNET_SDK_VERSION, found $sdk_version" >&2
    exit 1
fi
printf 'verify-env: .NET SDK: %s\n' "$sdk_version"

exec "$@"
