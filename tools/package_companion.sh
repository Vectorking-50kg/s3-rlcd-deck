#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ -n "$(git -C "$repository_root" status --porcelain)" ]] && \
   [[ "${S3DECK_ALLOW_DIRTY_PACKAGE:-0}" != "1" ]]; then
    echo "release packaging requires a clean source tree" >&2
    exit 2
fi

"$repository_root/tools/test_companion.sh"

build_version="${S3DECK_BUILD_VERSION:-0.1.0-dev}"
build_commit="${S3DECK_BUILD_COMMIT:-$(git -C "$repository_root" rev-parse --short=12 HEAD)}"
source_date_epoch="${SOURCE_DATE_EPOCH:-$(git -C "$repository_root" show -s --format=%ct HEAD)}"
package_output="$repository_root/build/packages"

# Packages are derived build output. Start from an exact empty destination so
# the packager can fail closed if any stale, unhashed artifact would survive.
if [[ -e "$package_output" ]]; then
    rm -rf -- "$package_output"
fi

python3 "$repository_root/tools/package_companion.py" \
    --repository-root "$repository_root" \
    --artifact-root "$repository_root/build/companion" \
    --output-root "$package_output" \
    --version "$build_version" \
    --commit "$build_commit" \
    --source-date-epoch "$source_date_epoch"

echo "Deterministic Companion application archives:"
find "$package_output" -maxdepth 1 -type f -print | sort
