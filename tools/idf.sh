#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ $# -lt 2 ]]; then
    echo "usage: $0 <dev|release> <idf.py arguments...>" >&2
    exit 2
fi

variant="$1"
shift

case "$variant" in
    dev|release) ;;
    *)
        echo "unknown build variant: $variant (expected dev or release)" >&2
        exit 2
        ;;
esac

if ! command -v idf.py >/dev/null 2>&1; then
    echo "idf.py is unavailable; activate the ESP-IDF 6.0.2 environment first" >&2
    exit 2
fi

idf_version="$(idf.py --version)"
if [[ "$idf_version" != "ESP-IDF v6.0.2" ]]; then
    echo "expected ESP-IDF v6.0.2, found: $idf_version" >&2
    exit 2
fi

build_directory="$repository_root/build/$variant"
defaults="sdkconfig.defaults;sdkconfig.defaults.$variant"
firmware_commit="$(git -C "$repository_root" rev-parse HEAD)"
firmware_build_unix="$(git -C "$repository_root" show -s --format=%ct HEAD)"

# Generated sdkconfig files are disposable. Recreate them from the committed defaults on
# every invocation so a variant cannot retain values from an older baseline.
cmake -E remove -f "$build_directory/sdkconfig" "$build_directory/sdkconfig.old"

exec idf.py \
    -C "$repository_root/firmware" \
    -B "$build_directory" \
    -DIDF_TARGET=esp32s3 \
    -DDECK_FIRMWARE_COMMIT="$firmware_commit" \
    -DDECK_FIRMWARE_BUILD_UNIX="$firmware_build_unix" \
    -DSDKCONFIG="$build_directory/sdkconfig" \
    -DSDKCONFIG_DEFAULTS="$defaults" \
    "$@"
