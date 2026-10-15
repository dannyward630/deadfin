#!/bin/sh
# Prepare the gate container, then run the requested command.
set -eu

# The image is built for the Docker daemon's native Linux architecture. Select
# exactly that checked-in native asset; never fall back to another architecture.
. /usr/local/bin/lib.sh
container_arch=$(verify_env_linux_arch "$(uname -m)")
skia_root=/work/internal/images/native/skia
if [ ! -d "$skia_root" ]; then
    echo "verify-env: $skia_root not found; is the deadfin checkout mounted?" >&2
    exit 1
fi
so=$(verify_env_skia_library "$skia_root" "$container_arch")
cp "$so" /usr/local/lib/deadfin/libSkiaSharp.so
printf 'verify-env: Skia architecture: linux-%s\n' "$container_arch"

sdk_version=$(dotnet --version)
if [ "$sdk_version" != "$VERIFY_ENV_DOTNET_SDK_VERSION" ]; then
    echo "verify-env: expected .NET SDK $VERIFY_ENV_DOTNET_SDK_VERSION, found $sdk_version" >&2
    exit 1
fi
printf 'verify-env: .NET SDK: %s\n' "$sdk_version"

exec "$@"
