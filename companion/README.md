# S3 RLCD Deck Companion

The Companion is the computer-owned half of S3 RLCD Deck. It collects and normalizes Provider state, keeps credentials on macOS or Windows, serves the management Web, and coordinates paired Decks.

The current M1 skeleton provides:

- a Go background executable with bounded shutdown;
- a loopback-only management listener at `127.0.0.1:7777` by default;
- an embedded, offline Web application;
- a read-only `/api/v1/status` endpoint;
- a size-limited, versioned control-message envelope parser backed by shared fixtures.

Pairing, the Device Hub, WSS, Provider collectors, Serial and OTA are delivered by their linked GitHub Issues. Provider credentials and raw private content must never be sent to a Deck.

## Run

Go 1.26.x is the development baseline.

```bash
cd companion
go run ./cmd/s3deck-companion
```

Open <http://127.0.0.1:7777>. To inspect the build identity without starting a listener:

```bash
go run ./cmd/s3deck-companion --version
```

## Verify

From the repository root:

```bash
./tools/test_companion.sh
```

The command enforces formatting, runs `go vet`, regular and race-enabled tests, then cross-compiles macOS arm64/amd64 and Windows amd64 binaries under the ignored `build/companion/` directory.
