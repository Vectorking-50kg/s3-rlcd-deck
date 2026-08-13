#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
companion_root="$repository_root/companion"
artifact_root="$repository_root/build/companion"

if ! command -v go >/dev/null 2>&1; then
    echo "Go is unavailable; install the Go 1.26.x toolchain" >&2
    exit 2
fi

go_version="$(go env GOVERSION)"
if [[ "$go_version" != go1.26.* ]]; then
    echo "expected Go 1.26.x, found: $go_version" >&2
    exit 2
fi

unformatted="$(cd "$companion_root" && gofmt -l .)"
if [[ -n "$unformatted" ]]; then
    echo "Go source requires gofmt:" >&2
    echo "$unformatted" >&2
    exit 1
fi

cd "$companion_root"
go vet ./...
go test ./...
go test -race ./...

mkdir -p \
    "$artifact_root/darwin-arm64" \
    "$artifact_root/darwin-amd64" \
    "$artifact_root/windows-amd64"

build_version="${S3DECK_BUILD_VERSION:-0.1.0-dev}"
build_commit="${S3DECK_BUILD_COMMIT:-$(git -C "$repository_root" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)}"
link_identity="-X main.version=$build_version -X main.commit=$build_commit"

CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
    -trimpath \
	-ldflags "$link_identity" \
    -o "$artifact_root/darwin-arm64/s3deck-companion" \
    ./cmd/s3deck-companion
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build \
    -trimpath \
	-ldflags "$link_identity" \
    -o "$artifact_root/darwin-amd64/s3deck-companion" \
    ./cmd/s3deck-companion
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
    -trimpath \
	-ldflags "$link_identity" \
    -o "$artifact_root/windows-amd64/s3deck-companion.exe" \
    ./cmd/s3deck-companion

native_arch="$(go env GOARCH)"
native_darwin_artifact="$artifact_root/darwin-$native_arch/s3deck-companion"
darwin_version="$($native_darwin_artifact --version)"
expected_version="s3deck-companion $build_version (commit $build_commit)"
if [[ "$darwin_version" != "$expected_version" ]]; then
    echo "darwin arm64 version mismatch: $darwin_version" >&2
    exit 1
fi
darwin_amd64_metadata="$(go version -m "$artifact_root/darwin-amd64/s3deck-companion")"
if [[ -z "$darwin_amd64_metadata" ]]; then
    echo "darwin amd64 artifact is not a valid Go executable" >&2
    exit 1
fi
windows_amd64_metadata="$(go version -m "$artifact_root/windows-amd64/s3deck-companion.exe")"
if [[ -z "$windows_amd64_metadata" ]]; then
    echo "windows amd64 artifact is not a valid Go executable" >&2
    exit 1
fi
for artifact_path in \
    "$artifact_root/darwin-amd64/s3deck-companion" \
    "$artifact_root/windows-amd64/s3deck-companion.exe"; do
    if ! LC_ALL=C grep -aFq -- "$build_version" "$artifact_path" || \
       ! LC_ALL=C grep -aFq -- "$build_commit" "$artifact_path"; then
        echo "cross-compiled artifact is missing its embedded build identity" >&2
        exit 1
    fi
done

echo "Companion verification and desktop packaging passed: $go_version ($build_commit)"
