# S3 RLCD Deck Companion

The Companion is the computer-owned half of S3 RLCD Deck. It collects and normalizes Provider state, keeps credentials on macOS or Windows, serves the management Web, and coordinates paired Decks.

The current M1 runtime provides:

- a Go background executable with bounded shutdown;
- a loopback-only management listener at `127.0.0.1:7777` by default;
- an independently authenticated Device Hub listener at `127.0.0.1:7780` by default;
- management login sessions with strict Origin/CSRF checks on writes;
- bounded Device Hub headers, bodies, timeouts, concurrency, and per-IP request rate;
- an embedded, offline Web application;
- an authenticated, read-only `/api/v1/status` endpoint and public `/api/v1/bootstrap`;
- a size-limited, versioned control-message envelope parser backed by shared fixtures.

Per-Deck pairing and WSS are delivered by their linked GitHub Issues. Provider credentials and raw private content must never be sent to a Deck.

## Run

Go 1.26.x is the development baseline.

```bash
cd companion
export S3DECK_MANAGEMENT_TOKEN="$(openssl rand -hex 32)"
export S3DECK_DEVICE_HUB_TOKEN="$(openssl rand -hex 32)"
go run ./cmd/s3deck-companion
```

The two tokens are separate trust domains and must be at least 24 bytes. They are read from the environment so they do not appear in process arguments. The Device Hub bootstrap token is temporary infrastructure for M1 pairing and will be replaced by revocable per-Companion trust in #27.

Open <http://127.0.0.1:7777>. To inspect the build identity without starting a listener:

```bash
go run ./cmd/s3deck-companion --version
```

LAN management is off by default. Enabling it requires all three explicit options and causes the status document to report a security warning:

```bash
go run ./cmd/s3deck-companion \
  --management-address 0.0.0.0:7777 \
  --allow-lan-management \
  --management-origin http://192.0.2.10:7777
```

## Verify

From the repository root:

```bash
./tools/test_companion.sh
```

The command enforces formatting, runs `go vet`, regular and race-enabled tests, then cross-compiles macOS arm64/amd64 and Windows amd64 binaries under the ignored `build/companion/` directory.
