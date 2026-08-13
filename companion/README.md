# S3 RLCD Deck Companion

The Companion is the computer-owned half of S3 RLCD Deck. It collects and normalizes Provider state, keeps credentials on macOS or Windows, serves the management Web, and coordinates paired Decks.

The current M1 runtime provides:

- a Go background executable with bounded shutdown;
- a macOS 13+ menu-bar and Windows 11 notification-area shell with live Deck
  connection count, Open Console, Start/Stop, and Quit actions;
- a loopback-only management listener at `127.0.0.1:7777` by default;
- an independently authenticated TLS Device Hub listener at `0.0.0.0:7780` by default;
- management login sessions with strict Origin/CSRF checks on writes;
- bounded Device Hub headers, bodies, timeouts, concurrency, and per-IP request rate;
- short-lived, one-time Pairing codes and revocable per-device trust;
- a persistent self-signed Device Hub identity with a stable SHA-256 fingerprint;
- a token-authenticated, fingerprint-pinned WSS Device Link with strict `device.hello`,
  bidirectional heartbeat, duplicate-device rejection, revocation, and bounded frames;
- an embedded, offline Web application;
- an authenticated, read-only `/api/v1/status` endpoint and public `/api/v1/bootstrap`;
- a size-limited, versioned control-message envelope parser backed by shared fixtures.

Provider credentials and raw private content must never be sent to a Deck.

## Run

Go 1.26.x is the development baseline.

```bash
./tools/package_companion.sh
./build/companion/darwin-arm64/s3deck-companion
```

Use the matching Intel macOS or Windows x64 artifact on those platforms. The
desktop shell starts the runtime, remains in the menu bar or notification area,
and exchanges a 30-second single-use grant when **Open Console** launches the
browser. **Stop Companion** stops both listeners without quitting the shell;
**Start Companion** creates a fresh runtime generation. **Quit** performs the
same bounded shutdown before removing the native tray item.

The first desktop run generates a protected local management token inside the
Companion data directory. `S3DECK_MANAGEMENT_TOKEN` can still explicitly
override it for development and HIL, but is no longer required for the normal
desktop launch.

The management token must contain at least 24 bytes and never appears in process
arguments or the browser URL. Device Hub access is never authorized by this
token: redeeming a six-digit one-time code produces an independent 256-bit token
for exactly one Device ID.

The Device Hub always uses the persistent Companion TLS identity when exposed beyond
loopback. During Pairing, the computer running Companion must also be connected to the
Deck's random WPA2 Setup AP. That isolated network authorizes one certificate-discovery
redeem; the Deck verifies that the returned DER certificate hashes to the returned
fingerprint before committing the Companion Profile. Every normal Device Link connection
then requires the exact saved certificate plus the independently issued device Token.

The Companion stores its certificate identity, token verifiers, and bounded redacted Pairing audit under the platform user-configuration directory. Files and their directory are protected with owner-only permissions. Override the location for development with `--data-directory`; neither Pairing codes nor device Tokens are stored in plaintext.

Open <http://127.0.0.1:7777>. To inspect the build identity without starting a listener:

```bash
go run ./cmd/s3deck-companion --version
```

For CI/HIL or foreground diagnostics, bypass all native desktop APIs while
retaining the same runtime:

```bash
S3DECK_MANAGEMENT_TOKEN="$(openssl rand -hex 32)" \
  go run ./cmd/s3deck-companion --headless
```

Only one Companion may own a data directory. A repeated launch fails with an
explicit `already running` error instead of competing for listeners or trust
files. Login-start installation is deliberately deferred to M5.

The authenticated management API issues codes at `POST /api/v1/pairing/codes`, issues a device-bound rotation code at `POST /api/v1/devices/{device_id}/rotate`, and revokes trust at `DELETE /api/v1/devices/{device_id}`. A Deck redeems a code once at the rate-limited Device Hub route `POST /api/v1/pairing/redeem`. Management writes require the login session, exact Origin, and CSRF token described above; a successful redeem response is the only place a plaintext device Token is returned. Device requests authenticate the complete Device ID + Token + identity + protocol-version binding.

The Device Link endpoint is `GET /api/v1/device/link` with WebSocket subprotocol
`s3-rlcd-deck.v1`. An authenticated Deck must send `device.hello` first and continue with
strict, size-limited `device.heartbeat` control messages. Missing heartbeats close the
session after 30 seconds; a revoked trust is rechecked and disconnected without waiting
for a Companion restart.

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

The command enforces formatting, runs `go vet`, regular and race-enabled tests,
then cross-compiles menu-bar/tray executables for macOS arm64/amd64 and Windows
amd64 under the ignored `build/companion/` directory. The SPA, build version,
third-party notices, static assets, and native icon are embedded in each single
executable. The local macOS arm64 artifact executes `--version`; the other
cross-compiled artifacts receive executable metadata and embedded build-identity checks.
The `Companion desktop native smoke` GitHub Actions workflow then runs the matching
artifact on macOS arm64, macOS amd64, and Windows amd64, verifies `--version`, starts
the real menu-bar/tray path, reads its embedded management bootstrap, and exercises
bounded shutdown. Windows development builds retain a console so their build identity
and shutdown failures stay observable; M5 owns the signed GUI-only installer.

The native tray adapter is pinned to `gogpu/systray` commit
`a3901e26a16407483bcb765d35cba446e60c6932`, which includes the macOS
NSApplication-before-NSStatusItem initialization fix and snapshot-safe dynamic
menu updates. Its MIT notice and the notices of all other shipped Go modules are
available from the embedded `/third-party-licenses.txt` route.
