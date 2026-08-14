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

The M2 runtime also starts the official Codex App Server behind a private versioned adapter. It
normalizes dynamic rate-limit windows and lifetime Token usage, refreshes after rolling
notifications, and publishes only the shared AI Snapshot Provider/Session DTO. Raw App Server
responses, account metadata, prompts, thread IDs, and paths never leave the adapter. Provider
timeouts, process exits, authentication failures, permission failures, and schema drift degrade
Codex independently while the rest of Companion stays available. The adapter does not read or
write Codex authentication files.

Independently owned Codex sessions are observed by a separate read-only process/JSONL adapter. It
never starts or takes over a session and exposes only anonymous `Running`, `Recent`, `Ended`, or
`Unknown` DTOs with `confidence=inferred`. Running requires a unique macOS process/file mapping and
observed growth under the same PID-plus-start-time identity. Windows weak mapping, PID reuse,
multiple candidates, rotation, partial JSON, and permission failures fail closed. Only one bounded
`session_meta` line is inspected; titles, prompts, replies, filenames, paths, commands, and tool
arguments never leave the adapter. Observer failure clears only inferred sessions and cannot block
the official Codex quota collector.

The experimental Cursor Provider opens Cursor's `state.vscdb` through a pure-Go SQLite driver in
`mode=ro` plus `query_only`, and queries only `cursorAuth/accessToken`. Every private usage request
re-reads that access token; the adapter never queries the refresh-token key, refreshes a token, or
writes Cursor's database or platform credential store. The pinned private endpoint and its strict
response schema are versioned independently, limited to 64 KiB, and bounded by a five-second
request timeout. Authentication, permission, locked-database, timeout, oversized-response, and
schema failures update only the experimental Cursor Provider. A previously valid Cursor quota page
is retained as `degraded`; otherwise Cursor is `unavailable`. Raw responses, account fields, and
credentials never enter Runtime or logs.

AIHubMix, DeepSeek, and user-defined Structured HTTP Providers share one safe request module. The
built-in templates use the versioned AIHubMix `/api/user/self` and DeepSeek `/user/balance`
contracts; custom definitions are limited to GET/POST, structured headers, an optional bounded JSON
body, and explicit balance/used/total/reset/currency JSONPath fields. Refresh is restricted to
1/5/15/30/60-minute tiers, requests default to ten seconds, and decoded responses are capped at
256 KiB. Test Request returns only a normalized Provider preview and fixed status/latency/schema
diagnostics.

Public targets require HTTPS. Cleartext HTTP is accepted only for private-network addresses and is
always marked with a warning. The transport disables environment proxies, compression, and
connection reuse; validates every DNS answer before dialing its pinned IP; rejects loopback,
link-local, metadata, mixed-safe rebinding answers, and cross-origin redirects. Curl import is a
non-executing allowlist parser: it rejects shell operators, variables, substitutions, files,
redirects, and unknown options while separating sensitive header bytes into secret references.
Every imported header value is placed in the platform secret store; the persisted definition holds
only its reference and a tightly allowlisted authentication-scheme prefix. Sensitive URL query
parameters cannot be represented safely by the current model, so all URL query strings are rejected.
Raw responses and request credentials never enter Runtime or diagnostics,
and a failure degrades only its own last valid Provider page.

Provider credentials and raw private content must never be sent to a Deck.

Provider credentials are owned by `internal/secretstore`. Its public seam accepts only an opaque
Secret Reference for put/get/delete/list-metadata operations. A new put generates a random
reference; an update replaces the value at the same reference in one platform-vault operation, so
a failed update preserves the previous value and cannot orphan a replacement reference. Delete is
idempotent, and list-metadata returns only references. macOS uses Security.framework Keychain
items and Windows uses Credential Manager entries in a fixed Companion Provider namespace family,
with an owner sub-namespace derived from the canonical `--data-directory`. The owner scope prevents
one valid data directory from enumerating or cleaning another's credentials. The namespace and
generated-reference grammar prevent this module from discovering or modifying
Codex/Cursor-owned authentication. Authentication UI is disabled for background access: locked,
denied, and canceled operations return fixed errors without including credential bytes.
Credential Manager lacks a metadata-only enumeration API, so its adapter never copies or returns
enumerated blobs and explicitly overwrites them before releasing the native result buffer.
Structured Provider headers use the `secretstore.Reference` domain type, and collectors resolve it
through `Store.Get` for each request. Template and curl-import drafts contain empty header slots,
not persistable aliases such as `api_key`. `CommitDefinition`/`CommitCurlImport` create the vault
entries first, replace every slot with its generated reference, and publish through the protected
`structured-providers.json` owner. New references are journaled before publication; one atomic file
replacement activates them and journals references retired by an update or delete. Successful vault
deletes clear the journal, while failed cleanup remains durable and is retried at every Companion
startup. Startup also reconciles vault metadata against active and pending references, recovering a
crash after collision-free placeholder reservation but before the journal commit. The file contains
definitions and opaque references only—never credential bytes.

Provider history is owned by `internal/history`. Runtime transfers only a validated Provider DTO
into its bounded queue; no Session DTO or upstream response can be represented at that seam. One
private SQLite writer keeps the latest observation for each Provider and UTC hour, including only
normalized status, error code, balance, Token counters, and quota windows. The default 90-day
retention keeps the exact boundary hour and deletes older hours. Recording can be disabled and the
setting survives restart.

SQLite runs in WAL mode with a protected database, lock, and migration backup. Schema upgrades
create and verify a consistent `VACUUM INTO` backup before entering the migration transaction; an
upgrade failure rolls back the original schema and retains the backup. Queries have mandatory time
and row bounds. CSV export copies its bounded query result and closes the database read before
writing to a potentially slow client, and it neutralizes spreadsheet formula prefixes. Corruption
or history backpressure degrades history alone and never stops Provider collection, Device Hub, or
the management recovery surface.

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

The first desktop run generates a local management token in macOS Keychain or
Windows Credential Manager. The data-directory path only derives a stable,
non-secret credential reference. `S3DECK_MANAGEMENT_TOKEN` can still explicitly
override the vault for development and HIL, but is no longer required for normal
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

The authenticated management API issues codes at `POST /api/v1/pairing/codes`, issues a device-bound rotation code at `POST /api/v1/devices/{device_id}/rotate`, and revokes trust at `DELETE /api/v1/devices/{device_id}`. Provider Hour data is read from `GET /api/v1/history`, exported from `GET /api/v1/history/export.csv`, enabled or disabled through `/api/v1/history/settings`, and cleared with `DELETE /api/v1/history`. A Deck redeems a code once at the rate-limited Device Hub route `POST /api/v1/pairing/redeem`. Management writes require the login session, exact Origin, and CSRF token described above; a successful redeem response is the only place a plaintext device Token is returned. Device requests authenticate the complete Device ID + Token + identity + protocol-version binding.

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
executable. The local macOS artifact for the host architecture executes `--version`; the other
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

Fake App Server transcripts cover out-of-order replies, rolling notifications, reconnects,
abnormal numbers, schema drift, and connection-local thread authority. A developer who owns the
local Codex login can additionally verify the installed official process without persisting raw
responses:

```bash
cd companion
S3DECK_TEST_CODEX_APP_SERVER=1 go test -run TestInstalledCodexAppServer ./internal/codexappserver
```

A developer with a personal Cursor login can run the separately gated real adapter smoke. It emits
only pass/fail and never persists or prints the access token, account fields, or raw private
response. Run it once on both a real macOS 13+ installation and a real Windows 11 installation;
fixture tests and cross-compilation do not replace those two evidence gates.

```bash
cd companion
S3DECK_TEST_CURSOR_PERSONAL=1 go test -run TestInstalledCursorPersonalUsage ./internal/cursorprovider
```
