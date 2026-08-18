# S3 RLCD Deck Companion

The Companion is the computer-owned half of S3 RLCD Deck. It collects and normalizes Provider state, keeps credentials on macOS or Windows, serves the management Web, and coordinates paired Decks.

The current M1 runtime provides:

- a Go background executable with bounded shutdown;
- a macOS 13+ menu-bar and Windows 11 notification-area shell with live Deck
  connection count, Open Console, Start/Stop, and Quit actions;
- a loopback-only management listener at `127.0.0.1:7777` by default;
- an independently authenticated TLS Device Hub listener at `0.0.0.0:7780` by default;
- a separately reported routable Device Hub endpoint, re-resolved from the physical default-route
  IPv4 address when Pairing is requested or pinned with `--device-hub-advertised-address IP:port`;
  ambiguous, virtual, wildcard, loopback, reserved, and documentation addresses fail closed;
- management login sessions with strict Origin/CSRF checks on writes;
- bounded Device Hub headers, bodies, timeouts, concurrency, and per-IP request rate;
- same-LAN Pairing v2 discovery, Deck-displayed short-lived codes, Security2 PAKE, and revocable
  per-device trust;
- a persistent self-signed Device Hub identity with a stable SHA-256 fingerprint;
- a token-authenticated, fingerprint-pinned WSS Device Link with strict `device.hello`,
  bidirectional heartbeat, duplicate-device rejection, revocation, and bounded frames;
- an embedded, offline Web application;
- an authenticated, read-only `/api/v1/status` endpoint and public `/api/v1/bootstrap`;
- an authenticated, redacted `GET /api/v1/devices` inventory and Device Link
  lifecycle counters without Tokens, identity verifiers, or certificate material;
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

The embedded management Web configures these Providers through authenticated
`/api/v1/providers` create/list/update/delete routes, `/api/v1/providers/order`, and the bounded
`/api/v1/providers/{id}/test` route. Writes require the management session, exact Origin, CSRF, and
sensitive-operation rate limiting. API responses contain editable non-secret request/mapping fields
and `secret_configured` booleans, but never vault references or credential bytes. One dynamic
supervisor reconciles the committed ordered definitions into isolated collectors without a
Companion restart. Runtime composes Codex, Cursor, and structured states into one complete
`snapshot.ai`; Device Hub coalesces only the latest document per authenticated Deck through that
connection's sole WebSocket writer.

### Management Web UI

The embedded UI implements the selected Scheme C instrument-panel design as an offline,
Chinese-first application. Its login surface and five stable domains provide 17 UI views: overview;
Provider list/editor, history, and Codex sessions; serial terminal/presets; Deck inventory,
network trust, Setup recovery, and RLCD preview; plus system settings, updates, backup,
diagnostics, and tray guidance. At widths below 920 px the domain dock and context navigation
become one mobile drawer without changing the task hierarchy.

`GET /api/v1/console` is the narrow read-only ViewModel seam for overview, session, device,
network, Deck-preview, system, and diagnostics surfaces. It exposes only `Status`, normalized
AI Snapshot Provider/Session DTOs, and capability booleans; credentials, raw responses, prompts,
paths, and serial bodies cannot be represented. A tray-created HttpOnly session can obtain a
fresh CSRF token through same-origin `POST /api/v1/session/refresh`; rotation does not extend the
eight-hour session lifetime and invalidates the previous CSRF token.

Provider management, history, pairing, backup, and Serial controls call their authenticated APIs.
The Serial view embeds local xterm.js assets for Text/ANSI/Unicode/scroll/search, offers safe HEX
and Text+HEX projections, and remains read-only until the Deck confirms this observer's exact Web
TX Lease. Signed OTA is connected to its preview/apply/status API. The diagnostics view reads the
bounded rotation state and exports a local fixed-schema bundle with a per-file SHA-256 manifest.
Neither view
invents connections, versions, progress, or success states. All destructive operations use a scoped confirmation dialog,
unknown metrics render as unavailable rather than zero, and the UI includes visible focus states,
reduced-motion handling, and responsive table containment.

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
only non-secret restorable configuration and opaque references—never credential bytes.

Encrypted configuration migration is owned by `internal/backup`. A Backup Archive uses the binary
`age` v1 scrypt format and schema 1.1 (schema 1.0 remains importable) and contains only user-entered
Structured HTTP Provider definitions and credentials, their explicit display order and polling
interval, management Web settings, Companion
application settings, and the non-secret Device Profile cache. It cannot represent Codex/Cursor
OAuth/Cookie/tokens, Pairing trust or Tokens, Web sessions, SQLite Provider Hours, raw responses, or
serial buffers. The encrypted/plaintext limits are 8 MiB/4 MiB.

The authenticated management flow is `POST /api/v1/backups/export`, then for import
`POST /api/v1/backups/preview` followed by `POST /api/v1/backups/import`. Preview contains only safe
labels, counts, exclusions, conflict keys, and a short-lived single-use receipt bound to the archive,
mode, and current configuration. Import stages fresh Vault references, publishes the entire
non-secret configuration with one protected-file replacement, and then idempotently cleans retired
references. Imported collectors/listeners/settings take effect after restart. Direct import,
incomplete per-item conflict decisions, stale/replayed Preview, wrong password, tampering, unknown
schema, and capacity violations fail closed.

A native file export never places the passphrase in argv or an environment variable. Supply its
exact bytes through an existing owner-only file; the command atomically creates only the selected
backup file and does not change its parent directory permissions:

```sh
s3deck-companion --data-directory "$DATA_DIR" \
  --backup-export-file "$DESTINATION.age" \
  --backup-passphrase-file "$PRIVATE_PASSPHRASE_FILE"
```

Provider history is owned by `internal/history`. Runtime transfers only a validated Provider DTO
into its bounded queue; no Session DTO or upstream response can be represented at that seam. One
private SQLite writer keeps the latest observation for each Provider and UTC hour, including only
normalized status, error code, balance, Token counters, and quota windows. The default 90-day
retention keeps the exact boundary hour and deletes older hours. Recording can be disabled and the
setting survives restart. Disable and clear are writer-generation barriers: no capture admitted
before either successful operation can be committed afterward. Retention uses current UTC at
startup, during capture, after settings changes, and in an hourly maintenance sweep, so an idle or
disabled collector cannot retain expired rows indefinitely.

SQLite runs in WAL mode with a protected database, lock, and migration backup. Schema upgrades
create and verify a consistent `VACUUM INTO` backup before entering the migration transaction; an
upgrade failure rolls back the original schema and retains the backup. Queries have mandatory time
and row bounds. CSV export copies its bounded query result and closes the database read before
writing to a potentially slow client, and it neutralizes spreadsheet formula prefixes. Corruption
or history backpressure degrades history alone and never stops Provider collection, Device Hub, or
the management recovery surface. Runtime status exposes only fixed
`history_available`/`history_enabled` booleans; unavailable query/export endpoints return 503 rather
than serving an unacknowledged stale view.

## Signed firmware updates

The management Web exposes a side-effect-free `POST /api/v1/ota/preview`, explicit-confirmation
`POST /api/v1/ota/apply`, and read-only `GET /api/v1/ota/status?device_id=...`. Preview validates the
complete signed archive and retains at most eight short-lived receipts in memory; it sends no bytes
to a Deck. Apply consumes one receipt, permits one global OTA transaction, and streams 3072-byte
chunks only after the exact prior `ota.result`. Archive, image, signature, and receipt ownership is
cleared on terminal failure, success, or Runtime shutdown. Unconsumed preview archives and receipts
are cleared at expiry or logout. The Web uses a second
danger confirmation and never offers background or silent updates. Each transaction has an
independent ten-minute total deadline in addition to the per-result timeout.

Release signing is intentionally outside the Companion. The private key must remain outside the
repository and is passed explicitly to `tools/sign_ota_bundle.py`; only the versioned public catalog
is shipped. For example:

```bash
python3 tools/sign_ota_bundle.py \
  --image build/release/s3_rlcd_deck.bin \
  --version 0.3.0-dev \
  --private-key /secure/path/release-v1-private.pem \
  --output build/release/s3_rlcd_deck-0.3.0-dev.s3ota
```

## Run

Go 1.26.x is the development baseline.

```bash
./tools/package_companion.sh
open "build/packages/s3deck-companion_0.1.0-dev_darwin-arm64.zip"
```

Each archive contains a macOS `.app` or Windows x64 application, installation instructions,
third-party notices, an SPDX 2.3 SBOM, a per-file manifest, and reproducible-build inputs;
`build/packages/SHA256SUMS` authenticates the archives. Use the matching Intel macOS or Windows
x64 artifact on those platforms. The
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

The Device Hub always uses the persistent Companion TLS identity when exposed beyond loopback.
Pairing v2 keeps Mac and Deck on the same mutually reachable normal LAN. Companion browses the
Deck's bounded `_s3rlcd-pair._tcp.local.` advertisement on the backend, exposes only opaque
candidate/session references to the management Web, and submits credentials only through the
Security2 PAKE channel after the user enters the code displayed on the Deck. Trust and Profile
remain provisional until the exact new Device Link proves its pinned certificate, independent
device Token, hello, and heartbeat. The Setup AP is not part of this flow.
The browser's candidate countdown is only the lifetime of its opaque discovery reference, and the
Companion session countdown is only a local upper bound. The Deck display is the authoritative
Pairing Window and code deadline; the Web must never label either local timer as Deck time.

On macOS, Companion publishes `_s3rlcd-hub._tcp.local.` through the native Bonjour
`DNSServiceRegister` API on the selected physical LAN interface. It does not open a second UDP
5353 listener alongside `mDNSResponder`; registration must receive the system success callback
before Pairing v2 credentials may leave the Mac.

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
files. Install the unpacked application for the current user without administrator rights:

```bash
s3deck-companion --install
s3deck-companion --installation-status
s3deck-companion --disable-login   # current process keeps running
s3deck-companion --enable-login
s3deck-companion --uninstall       # user data and rollback versions are retained
```

The installed management Web and Device Hub both default to loopback. Exposing the Device Hub to
the LAN is an explicit install-time choice such as `--device-hub-address 0.0.0.0:7780`; the
installer never changes firewall rules. Quit a running Companion before upgrading. Migration
snapshots and the owner-only Installation Journal restore the prior data and startup target after
failure or process interruption.

The authenticated management API lists redacted paired Decks at `GET /api/v1/devices`,
exposes Pairing v2 scan/session operations below `/api/v1/pairing-v2`, and keeps the old
`POST /api/v1/pairing/codes` endpoint only for the Deck Setup page's explicit
`/compat/pairing-v1` migration flow. It issues a device-bound rotation code at
`POST /api/v1/devices/{device_id}/rotate`, and revokes trust at
`DELETE /api/v1/devices/{device_id}`. The list contains only Device ID, protocol version,
and trust timestamps. Provider management uses `/api/v1/providers`,
`/api/v1/providers/order`, and `/api/v1/providers/{id}/test`. Provider Hour data is read
from `GET /api/v1/history`, exported from `GET /api/v1/history/export.csv`, enabled or
disabled through `/api/v1/history/settings`, and cleared with `DELETE /api/v1/history`.
Encrypted migration uses `/api/v1/backups/export`, `/api/v1/backups/preview`, and
`/api/v1/backups/import`. Pairing v2 management writes require the login session, exact Origin,
CSRF token, and sensitive-operation rate limit. The browser never receives the device Token or
certificate. Device requests authenticate the complete Device ID + Token + identity +
protocol-version binding.

The Device Link endpoint is `GET /api/v1/device/link` with WebSocket subprotocol
`s3-rlcd-deck.v1`. An authenticated Deck must send `device.hello` first and continue with
strict, size-limited `device.heartbeat` control messages. Missing heartbeats close the
session after 30 seconds; a revoked trust is rechecked and disconnected without waiting
for a Companion restart.

## Redacted diagnostics

The Companion writes only fixed `Diagnostic Event` fields through one nonblocking worker under
`<data-directory>/diagnostics`. The owner-only active JSONL segment is atomically replaced for
durability, sealed after one hour or 256 KiB, and then remains immutable; sealed segments retain at
most seven days or 50 MiB by default. Queue/storage pressure is bounded and represented only by a
dropped-event count. Arbitrary log strings, errors, paths, URLs, request/response content,
credentials, Provider raw values, prompts, tool arguments, and Serial payloads cannot enter the
diagnostic schema. Provider events contain only status, latency, adapter schema, a fixed redacted
error code, and a hashed Provider identifier.

Authenticated management clients read `GET /api/v1/diagnostics` and export through the
Origin/CSRF/rate-limited `POST /api/v1/diagnostics/export`. Export builds the ZIP in bounded memory;
it creates no temporary archive and never uploads it. `manifest.json` records build identity, the
24-hour event window and whether its bounded newest-event projection was truncated, plus the byte
length and SHA-256 of `companion/events.jsonl`, `deck/ring.json`, and
`configuration/schema-keys.json`. The Device Hub collects only exact-response, shared-contract Deck
memory rings and retains at most 32 in memory. See
[`ADR 0024`](../docs/adr/0024-bound-diagnostics-to-fixed-redacted-events.md).

## Volatile Serial Hub

An authenticated Deck with an active Serial Session may publish fixed, versioned binary frames to
the Companion's volatile Serial Hub. The Hub owns at most 8 MiB of payload for the current Session,
rejects wrong Session/channel/order/monotonic metadata, and clears all bytes when that Session ends
or the Runtime stops. It never writes captured or transmitted Session bytes to logs, Provider Hour
SQLite, configuration, backup archives, or diagnostics.

User-authored Serial Presets are a separate protected configuration object, not captured Session
bytes. At most 32 bounded presets may be stored and included in an encrypted Backup Archive; they
are never inferred from the Serial Hub and never transmit without the current observer's exact Web
TX Lease.

Authenticated management clients use `/api/v1/serial/observe` with subprotocol
`s3deck.serial.v1`; every observer has an independent overwrite-aware cursor. Current-session raw
download is bounded to 1 MiB through `/api/v1/serial/download`, and `/api/v1/serial/status` excludes
browser IDs, Lease IDs, and pending request capabilities. A slow observer can lose its own oldest
bytes but cannot block Deck ingest or another observer.

Serial Presets use authenticated metadata-only `GET /api/v1/serial/presets`; an explicit editor or
send action reads one protected body through `GET /api/v1/serial/presets/{id}`. Per-ID PUT/DELETE
and the bounded whole-collection PUT additionally require the exact Origin, CSRF token, and
sensitive-operation rate limit. List rows expose only name, mode, bounded byte count, and
line-ending metadata until the user explicitly opens an editor.

Only one observer may hold the ten-minute Web TX Lease. Acquire/release results remain
`transitioning` until the Deck's sole owner acknowledges the exact request. Observer disconnect and
Lease expiry request Deck revocation before USB is reported locally; Device Link disconnect and the
Deck's independent deadline also return the target owner to USB. Raw input from any other observer
is rejected. A transport write failure leaves the request pending for exact-result retry. Runtime
shutdown closes and joins every observer, then waits within its common deadline for Deck revocation
before closing Device Link and clearing the Hub. See
[`ADR 0020`](../docs/adr/0020-keep-serial-hub-history-volatile-and-lease-web-transmit.md).

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
then reproducibly cross-compiles menu-bar/tray executables for macOS arm64/amd64 and Windows
amd64 under the ignored `build/companion/` directory. `package_companion.sh` additionally emits
deterministic audited archives under `build/packages/`. The SPA, build version,
third-party notices, static assets, and native icon are embedded in each single
executable. The local macOS artifact for the host architecture executes `--version`; the other
cross-compiled artifacts receive executable metadata and embedded build-identity checks.
The `Companion desktop native smoke` GitHub Actions workflow then runs the matching
artifact on macOS arm64, macOS amd64, and Windows amd64, verifies `--version`, starts
the real menu-bar/tray path, reads its embedded management bootstrap, and exercises
bounded shutdown. Windows development builds retain a console so their build identity
and shutdown failures stay observable. Publication signs/notarizes the audited application
contents with external platform credentials; development archives never claim a release signature.

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
