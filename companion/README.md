# S3 RLCD Deck Companion

The Companion is the computer-owned half of S3 RLCD Deck. It collects and normalizes Provider state, keeps credentials on macOS or Windows, serves the management Web, and coordinates paired Decks.

The current M1 runtime provides:

- a Go background executable with bounded shutdown;
- a loopback-only management listener at `127.0.0.1:7777` by default;
- an independently authenticated Device Hub listener at `127.0.0.1:7780` by default;
- management login sessions with strict Origin/CSRF checks on writes;
- bounded Device Hub headers, bodies, timeouts, concurrency, and per-IP request rate;
- short-lived, one-time Pairing codes and revocable per-device trust;
- a persistent self-signed Device Hub identity with a stable SHA-256 fingerprint;
- an embedded, offline Web application;
- an authenticated, read-only `/api/v1/status` endpoint and public `/api/v1/bootstrap`;
- a size-limited, versioned control-message envelope parser backed by shared fixtures.

Pinned WSS transport is delivered by its linked GitHub Issue. Provider credentials and raw private content must never be sent to a Deck.

## Run

Go 1.26.x is the development baseline.

```bash
cd companion
export S3DECK_MANAGEMENT_TOKEN="$(openssl rand -hex 32)"
go run ./cmd/s3deck-companion
```

The management token must contain at least 24 bytes and is read from the environment so it does not appear in process arguments. Device Hub access is never authorized by this token: redeeming a six-digit one-time code produces an independent 256-bit token for exactly one Device ID.

Until #29 installs fingerprint-pinned TLS/WSS, the Device Hub is deliberately restricted to loopback. This keeps the one-time redeem response off untrusted LAN links; configuring a non-loopback Device Hub fails closed rather than sending the Pairing code, device identity, or Token over plaintext HTTP.

The Companion stores its certificate identity, token verifiers, and bounded redacted Pairing audit under the platform user-configuration directory. Files and their directory are protected with owner-only permissions. Override the location for development with `--data-directory`; neither Pairing codes nor device Tokens are stored in plaintext.

Open <http://127.0.0.1:7777>. To inspect the build identity without starting a listener:

```bash
go run ./cmd/s3deck-companion --version
```

The authenticated management API issues codes at `POST /api/v1/pairing/codes`, issues a device-bound rotation code at `POST /api/v1/devices/{device_id}/rotate`, and revokes trust at `DELETE /api/v1/devices/{device_id}`. A Deck redeems a code once at the rate-limited Device Hub route `POST /api/v1/pairing/redeem`. Management writes require the login session, exact Origin, and CSRF token described above; a successful redeem response is the only place a plaintext device Token is returned. Device requests authenticate the complete Device ID + Token + identity + protocol-version binding.

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
