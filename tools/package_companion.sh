#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

"$repository_root/tools/test_companion.sh"

echo "Development artifacts:"
echo "  $repository_root/build/companion/darwin-arm64/s3deck-companion"
echo "  $repository_root/build/companion/darwin-amd64/s3deck-companion"
echo "  $repository_root/build/companion/windows-amd64/s3deck-companion.exe"
echo "macOS .app signing/notarization and Windows installer packaging remain M5 release work."
