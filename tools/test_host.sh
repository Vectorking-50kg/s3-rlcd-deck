#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
build_directory="$repository_root/build/host"

cmake -S "$repository_root/tests/host" -B "$build_directory"
cmake --build "$build_directory" --parallel
ctest --test-dir "$build_directory" --output-on-failure
