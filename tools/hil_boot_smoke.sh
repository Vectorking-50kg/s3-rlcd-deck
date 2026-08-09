#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ $# -lt 1 ]]; then
    echo "usage: $0 <serial-port> [hil-smoke-options...]" >&2
    exit 2
fi

port="$1"
shift
hil_arguments=("$@")
if [[ ! -c "$port" ]]; then
    echo "serial port is not a character device: $port" >&2
    exit 2
fi

if [[ -z "${IDF_PYTHON_ENV_PATH:-}" ]]; then
    echo "IDF_PYTHON_ENV_PATH is unset; activate ESP-IDF 6.0.2 first" >&2
    exit 2
fi

"$repository_root/tools/test_host.sh"
"$repository_root/tools/idf.sh" dev build

(
    cd "$repository_root/build/dev"
    "$IDF_PYTHON_ENV_PATH/bin/python" -m esptool \
        --chip esp32s3 \
        --port "$port" \
        --baud 460800 \
        --before no-reset \
        --after watchdog-reset \
        write-flash @flash_args
)

"$IDF_PYTHON_ENV_PATH/bin/python" \
    "$repository_root/tools/hil_boot_smoke.py" \
    --port "$port" \
    --timeout 30 \
    "${hil_arguments[@]}"
