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

CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
    -trimpath \
    -o "$artifact_root/darwin-arm64/s3deck-companion" \
    ./cmd/s3deck-companion
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build \
    -trimpath \
    -o "$artifact_root/darwin-amd64/s3deck-companion" \
    ./cmd/s3deck-companion
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
    -trimpath \
    -o "$artifact_root/windows-amd64/s3deck-companion.exe" \
    ./cmd/s3deck-companion

echo "Companion verification passed: $go_version"
