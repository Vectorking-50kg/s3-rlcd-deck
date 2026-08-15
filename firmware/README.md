# Firmware development

The firmware baseline is ESP-IDF 6.0.2 with LVGL locked to the 9.4 release line. Activate the matching ESP-IDF environment before using the repository tools.

## Build variants

Development builds enable the machine-readable diagnostics consumed by HIL tests. At
startup they wait up to ten seconds for the harness's `DECK_HIL_READY` serial handshake
before emitting the one-shot event, so a post-flash monitor cannot race the event:

```bash
./tools/idf.sh dev build
```

Release builds disable both the development diagnostic channel and the ESP-IDF console:

```bash
./tools/idf.sh release build
```

Each variant owns an isolated generated `sdkconfig` and build directory. The committed `sdkconfig.defaults` files are the source of truth; generated configuration and managed components are not versioned.

## Host tests

```bash
./tools/test_host.sh
```

Host tests exercise firmware behavior through public C-compatible interfaces and replay captured HIL console data without requiring a Deck.

The display tests cover the 400×300 landscape bit mapping, bounds checks, unchanged
frames, asynchronous frame ownership, late completion after timeout, transfer-start
failure, ViewModel equality, diagnostic text, and the Chinese glyph manifest.

## Setup Mode

With no committed Wi-Fi configuration, or after the committed network cannot be reached,
the Deck starts a temporary Setup AP. A BOOT long press also starts a fresh session. Each
session uses a new `S3-RLCD-XXXX` SSID and a readable WPA2 password shown only on the Deck
screen. The ordinary HTTP page at `http://192.168.4.1/` exposes status, an explicit network
scan, Wi-Fi and calibration forms, and Companion Profile management. Pairing accepts only
an explicit `host:port` and a six-digit one-time code. The Setup HTTP peer must be the
computer running that Device Hub on the current `192.168.4.0/24` Setup network; a remote
address supplied by form data cannot redirect the one-time trust bootstrap. The same page can
transactionally select Active, revoke a Profile, or edit its integer failover priority (higher
values are attempted first); every successful edit advances the Profile generation.

Submitted credentials are first stored as a candidate and tested as a station. A successful
connection writes the new record into the inactive slot and switches a CRC-protected active
marker last. The marker records both the new generation and the exact previous committed
generation, so an uncommitted slot can never become a recovery fallback. Candidate, slot,
and marker writes are read back before the in-memory active record changes. Authentication
failure, connection failure, timeout, corrupt data, and unsupported schema leave the last
committed record unchanged and keep recovery available. Stored network passwords are never
included in diagnostic events, screen errors, or HIL reports.

Handled page, status, scan, and submission requests refresh the inactivity timer.
Development builds use 12 seconds for automated HIL; release builds use the required 600
seconds. Wi-Fi validation independently allows 20 seconds for association and DHCP in both
variants, and an in-flight validation keeps Setup active until it reaches a result. When
Setup closes after a failed candidate, the service restores and reconnects the last
committed station configuration.

The same recovery page owns the Deck's temperature calibration. Offsets are parsed as
decimal tenths of a degree, range checked from -15.0 C through +15.0 C, and committed in
the independent `deck_settings` NVS namespace using a versioned candidate, alternating
slots, CRC, readback, and an active marker written last. With no committed settings record,
the runtime default is -4.0 C. A settings write failure leaves the previous offset active
and is visible in both `/api/status` and the `setup_state` diagnostic event.

Wi-Fi and device settings share the private `transaction_store` implementation for record
CRC, candidate staging, alternating slots, marker verification, rollback, fallback, and
the NVS adapter. Their namespaces, magic values, payload validation, and public states stay
separate; the Wi-Fi codec remains byte-compatible with its schema-v1 records.

Clearing Wi-Fi requires a fresh, session-bound 60-second confirmation token followed by a
second confirmation request. Loading the page, scanning, changing temperature calibration,
or entering Setup with BOOT never clears configuration. A confirmed clear removes only the
candidate, active marker, and two slots in `deck_wifi`; it leaves `deck_settings` and other
device-owned settings intact and keeps Setup Mode running.

## Display architecture

The `display` module exposes a narrow C-compatible interface and owns 30 KiB of bounded
1bpp state: one mutable 15 KiB logical framebuffer and one immutable 15 KiB
last-successful recovery snapshot. LVGL renders RGB565 into two 400×24 partial buffers
(19.2 KiB each); its sole owner task packs those areas directly into the logical frame.
While a candidate frame is in flight, higher-level ViewModel changes coalesce without
modifying that frame. If a timed-out candidate completes late, the service first
retransmits the last-successful snapshot; while recovery is in flight, the latest render
may merge into the logical frame and is submitted after recovery. Each transfer always
owns a complete 15,000-byte immutable frame until the panel callback, and the service
never reuses or destroys that memory early.

The M0 page uses a checked-in 1bpp font subset generated from the Source Han Sans SC copy
in the locked LVGL dependency. `application_ui/assets/m0_glyphs.txt` is the single source
of truth for its Chinese glyph set. Regenerate the font after resolving managed
dependencies with:

```bash
./tools/generate_m0_font.sh
```

## Serial Session authority

`serial_service` owns the `DISARMED → USB_TX ↔ WEB_TX` transition graph in one FreeRTOS
task. GPIO17 is configured as input/high-impedance before that task starts. While the
Deck remains on an AI Page, UART1 is not installed and no target data-path task runs.
A KEY long press queues a new Serial Session; successful UART1 installation on GPIO44
RX/GPIO17 TX assigns a new Session ID and always selects USB as the sole TX Owner.

Web owner requests carry the current Session ID and a monotonic request ID. The owner
clears pending USB TX in the same serialized transition that grants a Web TX Lease.
Duplicate requests only replay while their owner generation is still current; stale
Session, request, activity, and disconnect messages cannot revive authority. A Web
release, disconnect, or the ten-minute Lease boundary clears pending Web TX and returns
to USB. USB input observed during WEB TX is rejected and counted without retaining its
bytes.

BOOT short press advances a control epoch before its urgent exit is queued. The owner
therefore rejects any older KEY entry even if queue scheduling delivers that entry after
BOOT. Exit revokes the TX Owner, clears both pending source queues, uninstalls UART1, and
restores GPIO17 to input/high-impedance. Installation failure takes the same fail-closed
path, does not allocate a Session ID, and leaves the AI Page visible. Only an owner
snapshot confirming USB or Web authority activates the bounded Serial status page; no
payload bytes are rendered. Service unavailability and UART installation failure remain
visible in the AI Page TX footer; a later successful installation clears the current
fault while preserving its cumulative counter. Serial and peripheral tasks start only
after an explicit UI READY event, so asynchronous UI failure cannot leave an invisible,
armed target. Owner events cross a depth-one latest-state mailbox, so a stalled UI/model
consumer cannot block BOOT revocation or Lease expiry. The state module contains no
serial payload buffers, logs, or persistence. The full ownership decision is recorded in
[`ADR 0017`](../docs/adr/0017-centralize-serial-session-authority-in-one-owner-task.md).

## UART Router and current-session history

Each successful Serial Session creates an ESP-IDF UART event queue, 16 fixed 256-byte
ingress blocks, and one Router task. The RX task is the sole consumer of GPIO44 data and
never writes USB, WSS, Flash, logs, or UI synchronously. The Router copies each input once
into a shared PSRAM pool, assigns a per-session nonzero sequence, and installs references
in independent USB, WSS, history, and statistics rings. A full ring overwrites only its
own oldest reference and counter; a stalled or disconnected sink cannot wait on or evict
another sink. The statistics ring is exactly one block, so the screen side can retain
only the latest observation.

The history ring is retained but non-destructive, contains only the current Serial
Session, and defaults to 512 KiB. `CONFIG_DECK_SERIAL_HISTORY_KIB` accepts 64–2048 KiB;
metadata and ring indices use additional PSRAM. Reconnect copies preserve Session ID,
sequence, monotonic receive time, length, and raw bytes, and explicitly report when the
requested cursor has fallen behind the oldest retained sequence. FIFO overflow and UART
driver-buffer-full are global severe counters. The owner publishes changes through the
depth-one UI mailbox, and the Serial page places the resulting data-loss warning and
bounded FIFO/driver counts immediately below its title. Local sink overwrite bytes/blocks
remain independent. No Router allocation occurs after UART tasks start.

Exit first signals and joins RX/Router tasks, then deletes the UART driver and zeroes all
session blocks before freeing them. A bounded teardown failure preserves the complete
service for retry and GPIO17 is restored to input/high-impedance. USB/WSS transports use
the service's copy APIs and never receive internal queues or block ownership. The data
path decision is recorded in
[`ADR 0018`](../docs/adr/0018-fan-out-uart-rx-through-a-fixed-block-router.md).

## USB Serial/JTAG bridge

The release firmware installs the ESP-IDF USB Serial/JTAG driver only inside an
active Serial Session. Two low-priority tasks adapt that single CDC endpoint to
the Router without ever running on the UART RX task: the output task holds at
most one copied 256-byte Router block across partial writes or USB reconnects,
and the input task moves raw bytes through a fixed 16-block queue to the sole
Session owner. No path decodes UTF-8, adds line endings, logs payload, or writes
it to Flash.

USB bus presence is not treated as proof that a computer has opened the COM
port. If the port is unopened, occupied, or stops reading, the 4 KiB driver TX
ring eventually applies zero-progress backpressure; only the USB sink then
overwrites its own oldest Router references. The Router, WSS, history, and UART
RX continue independently. A disconnect does not end the Serial Session. A
partially handed-off block remains bridge-owned and resumes after reconnect,
while exit zeroes it together with every current-session queue.

USB input is stamped at read time with the owner's published generation.
Generation-matching blocks are serialized by the owner into UART1's unbuffered hardware
FIFO with partial-progress handling. Bytes observed while Web owns TX are never
queued for later transmission: their count is transferred to the owner and
added to `usb_tx_rejected`, even if the Lease returns to USB before the owner
processes that count. A read spanning a complete `USB → WEB → USB` transition
also fails its generation check and is rejected. Owner switches clear only
unsent source queues; target RX output rings remain independent.

The development/HIL firmware reserves USB Serial/JTAG for its structured
diagnostic console and therefore does not install this bridge. The release
configuration sets the ESP-IDF application console to `None` and enables this
bridge instead. ROM boot text may still precede the application after reset;
target bridging begins only after a physical Serial Session entry, so ROM or
HIL text is never sent to GPIO17. The project neither burns USB eFuses nor links
a second TinyUSB device stack. The decision is recorded in
[`ADR 0019`](../docs/adr/0019-isolate-the-release-usb-serial-jtag-bridge.md).

## Device Link Serial stream

The active Companion Link publishes target RX as binary `SRD1` frames defined by the shared
`protocol/catalog/serial-frame-v1.json`. It is the only WSS writer. A newly connected or newly
announced Session remains stream-gated until the Companion supplies `serial.history.request`; the
Link then copies the non-destructive history cursor before resuming its live WSS sink. Live
references at or behind the completed history cursor are discarded instead of being sent twice.

Companion Web input uses the opposite binary channel. The Link rejects a wrong channel, Session,
sequence, monotonic timestamp, or inactive owner and copies at most one 256-byte frame into the
fixed Web source queue. The sole Serial owner rechecks the exact Session and Lease immediately
before each partial UART FIFO write. Owner changes clear this queue but never a target-RX Router
sink. WSS disconnect also queues a Web-owner revocation; the ten-minute owner-side expiry remains
the independent final safety boundary. No serial payload is logged or written to Flash. See
[`ADR 0020`](../docs/adr/0020-keep-serial-hub-history-volatile-and-lease-web-transmit.md).

## Safe boot smoke test

With exactly one Deck connected and its serial port free:

1. Hold **BOOT**, power-cycle the Deck, then release **BOOT**. This board requires that
   physical action to enter the ROM download mode over its built-in USB Serial/JTAG port.
2. Run:

```bash
./tools/hil_boot_smoke.sh /dev/cu.usbmodemXXXX
```

The script builds, flashes only the generated bootloader/partition/application regions,
performs a software-watchdog reset, and waits for one `boot_ok` JSON Line. It never
erases the whole Flash, writes eFuses, or changes irreversible security settings.

For the RLCD ticket, require the boot event plus at least three clean completed full
frames during a 20-second stability window followed by 10 seconds of reset-detection
grace:

```bash
./tools/hil_boot_smoke.sh /dev/cu.usbmodemXXXX --expect-display
```

`display_ready` is emitted only after the first panel IO completion callback;
`display_progress` then reports later completions. The harness rejects wrong
resolution/frame size, fewer than three frames, any timeout/start failure/rejected update,
or a second `boot_ok` indicating an unexpected reset. It repeats the HIL handshake every
500 ms so a reboot late in the observation window cannot hide behind the firmware's
startup wait. Landscape orientation, black/white appearance, mirroring, and clipping
still require observing the physical Deck screen.

To additionally require one clean Setup AP active-to-inactive transition without changing
the development Mac's Wi-Fi connection:

```bash
./tools/hil_boot_smoke.sh /dev/cu.usbmodemXXXX \
  --expect-display --expect-peripherals --expect-setup
```

Setup active/inactive events are published only after the ESP-IDF Wi-Fi driver reports
`WIFI_EVENT_AP_START`/`WIFI_EVENT_AP_STOP`; missing events and stop failures are errors.
During this extended observation, the live harness also rejects Task WDT, panic, Guru
Meditation, and assertion logs even if all expected JSON diagnostic events were emitted.
This first-boot check assumes the `deck_wifi` NVS namespace has no active configuration; use
the transactional test below once the Deck already has an active record.

## Transactional Wi-Fi HIL

The development-only control channel can validate a reachable 2.4 GHz test AP without
switching the Mac's Wi-Fi connection. Provide the test credentials through environment
variables, then run the harness against an already-flashed development build:

```bash
export DECK_HIL_WIFI_SSID='test-ap-name'
export DECK_HIL_WIFI_PASSWORD='test-ap-password'
"$IDF_PYTHON_ENV_PATH/bin/python" tools/hil_wifi_transaction.py \
  --port /dev/cu.usbmodemXXXX
```

The harness activates that network in the Deck's `deck_wifi` NVS namespace, verifies the
committed generation after a software restart, submits a generated unreachable candidate,
and verifies after another restart that the committed generation still reconnects. It does
not print the SSID or password, and diagnostic events are rejected if they contain stored or
candidate credentials. This test intentionally changes the Deck's active Wi-Fi record; it
does not erase other NVS namespaces, the full Flash, or eFuses.

## Recovery settings HIL

Run the transactional Wi-Fi HIL first so the Deck has an active test network. Then enter
Setup Mode and run the recovery smoke against the already-flashed development build:

```bash
"$IDF_PYTHON_ENV_PATH/bin/python" tools/hil_recovery_settings.py \
  --port /dev/cu.usbmodemXXXX --offset=-3.5
```

When prompted, manually connect the Mac to the Setup SSID/password displayed on the Deck;
the harness does not change the Mac's Wi-Fi configuration itself. It submits the offset
through the real HTTP recovery route, verifies the calibrated peripheral diagnostic, proves
that a wrong clear token is rejected, confirms the real clear token, and restarts the Deck
to verify both persistent calibration and automatic Setup entry.

This smoke intentionally deletes the active and candidate records in the Deck's `deck_wifi`
namespace. It preserves `deck_settings`, other NVS namespaces, the rest of Flash, and eFuses.

## Companion Pairing and Device Link

The Deck transactionally stores at most five versioned Companion Profiles in the
`deck_companion` namespace of the dedicated 64 KiB `companion_nvs` partition. The
partition is provisioned for the candidate and both committed maximum-size Profile-set
records plus NVS replacement/GC overhead; Wi-Fi and device settings remain in the legacy
NVS partition. A Profile contains its redacted display fields plus an
independent device Token and the exact Device Hub certificate DER/fingerprint. Status and
Setup responses never expose the Token or certificate bytes. A failed redeem, malformed
credential, capacity limit, NVS error, or interrupted write leaves the last committed
Profile set and Wi-Fi configuration unchanged.

The one-time certificate-discovery request is allowed only while the computer is connected
to the Deck's fresh random WPA2 Setup AP. The Deck validates the returned certificate hash
before committing it and closes Setup after successful Pairing. Normal operation never
uses discovery trust: the `companion_link` module initiates WSS with the exact stored
certificate, device identity, and per-Deck Token. It sends `device.hello` first, accepts
only strict version-1 heartbeat frames up to 16 KiB, marks the Companion offline after 30
seconds without a valid heartbeat, and reconnects with exponential delay capped at 30
seconds. After 30 continuous offline seconds, a bounded Failover Round tries the other
Profiles by priority, last-success, and stable Profile order. A candidate becomes the sticky
Active only when its first authenticated heartbeat and generation-fenced Profile transaction
both succeed; all-offline rounds return to the previous Active and wait another full window.
Manual recovery selection, Pairing/address changes, and revocation cancel stale rounds, and
transport generations reject queued events from older WSS clients. A replacement transport is
heartbeat-only until its generation-fenced Active commit succeeds; only then may it publish
Snapshot or Serial traffic. Snapshot data remains stale until that exact transport publishes a
valid snapshot. An independent Serial Web-transport epoch rejects old queued Web-owner requests
without superseding physical exit/stop, and the owner task must acknowledge USB ownership before
the replacement transport starts. The ESP-IDF
`tcp_transport` component is compiled without log calls because its
stock handshake error path prints custom headers; Deck-owned diagnostics remain redacted.
All credentials remain private to the Profile and Device Link modules.

Release firmware also advertises the `diagnostics` capability. The `health` component owns one
volatile 64-event `Deck Diagnostic Ring`; callers can record only fixed level/component/code enums,
monotonic time, and one numeric value. No string, path, credential, Provider value, prompt, tool
argument, or Serial payload can be represented. After the exact transport becomes Active, the Link
answers a strict `diagnostics.request` with the matching `diagnostics.snapshot`; malformed,
duplicate, oversized, stale-request, or unknown-enum documents fail closed. The ring is never
written to Flash and is read through the Companion's authenticated System bundle flow. See
[`ADR 0024`](../docs/adr/0024-bound-diagnostics-to-fixed-redacted-events.md).

## AI Snapshot contract

The `ai_snapshot` component validates `snapshot.ai` before any state can replace the Deck's last
valid AI Snapshot. It enforces the shared 16 KiB bound, schema versions, canonical UTC times,
provider/session relationships, enum/source/confidence combinations, numeric ranges, and the
privacy field deny boundary. Stateless validation does not retain the input; the caller-owned
retained slot replaces its last valid document only after validation succeeds. Unknown schema
majors fail closed without changing that slot. Compatible higher minors may add only null,
boolean, or bounded integer fields. Host and firmware builds run the same fixture manifest from
`protocol/fixtures`.

The `Snapshot Store` is the only stateful sink used by Companion Link for `snapshot.ai`.
It replaces the in-memory document immediately after full validation, rejects future or
regressing timestamps, and checkpoints through versioned candidate/two-slot records with
length, CRC, readback verification, and an atomic active marker. The dedicated 128 KiB
`snapshot_nvs` partition has room for three maximum 16 KiB records plus NVS replacement/GC
overhead. A private worker owns Flash open, recovery reads, writes, and close, so Store creation
returns a bounded volatile view while recovery completes. A versioned, CRC-protected attempt
watermark keeps both successful and failed attempts limited to one per 30 minutes across restart;
an existing transaction with a missing or corrupt watermark starts a conservative 30-minute
window at the first trusted UTC observation. Asynchronous recovery keeps the newer of the live
and committed timestamps and resolves a same-time byte conflict to the committed record. A storage
failure remains visible as degraded state but does not block the live memory update or UI copy;
after the interval, the transactional state is reopened from the last valid record so transient
failures can recover. Shutdown drains queued work within two seconds and otherwise retains the
complete owner for an idempotent retry instead of freeing storage under a stalled worker.
Offline documents remain readable
as `STALE` for less than 24 hours, then the Store withholds the document and quota values while
retaining the last valid bytes internally. Any trusted wall-clock source moving below its
high-water mark also withholds the document until that source recovers. Snapshot documents and
private fields are never written to diagnostics.

## Codex AI Page

The `application_ui` component owns the bounded Snapshot-to-ViewModel projection and the fixed
400x300 monochrome formatter. The runtime copies the Snapshot Store without blocking Flash work,
reprojects only when the retained document changes, and publishes through the existing single
LVGL owner. Setup temporarily overrides the AI Page; otherwise the first readable Snapshot makes
Codex the default page. Four dynamic quota windows, aggregate tokens, and one privacy-safe featured
session fit within thirteen text lines. Quota rows use `R`/`U` for remaining/used and `@` for the
reset countdown; both names and durations are pixel-bounded against the generated font. Unknown
values are hidden or rendered as `--`, never as zero, and all confidence, stale, degraded, and
unavailable states use explicit text instead of color. `tests/host/snapshots/ai-page-*.txt` are the
exact layout contract. The final line remains
`TX DISARMED`; rendering a Snapshot never grants serial transmit authority.
Trusted UTC is converted with the validated Snapshot timezone for the bounded set of firmware
rules; an unsupported zone falls back to the cached RTC and then to `--:--` rather than guessing
an offset. The projection worker owns a cancellable task and PSRAM document buffer, joins within
two seconds before Companion Link teardown, and retains its complete owner when a join times out.

The same validated document is also projected into a bounded ordered Provider page set. KEY short
press cycles Codex and every configured Provider while Setup is inactive, preserving the selected
Provider ID across a live reorder and falling back to Codex after deletion. With no extra Provider,
KEY alternates Codex with a configuration hint. Generic pages show the fixed Provider name, textual
status/confidence/update age, then only the available balance, quota, Token, and bounded error
fields. `EXPERIMENTAL`, `DEGRADED`, `STALE`, and `UNAVAILABLE` never depend on grayscale, and every
page ends with `TX DISARMED`.

## Signed A/B OTA

The `ota_service` component owns one bounded Firmware Bundle transaction at a time. Device Link
accepts OTA controls only from the authenticated Active transport, then a private worker validates
the board, protocol, strictly newer semantic version, catalog key, P-256 signature, partition size,
sequential offset, 30-second inactivity deadline, and ten-minute total deadline before and during the write. It streams directly to the
inactive application slot while computing SHA-256; `esp_ota_end`, the image's embedded version, and
the complete digest must all agree before the boot partition changes. A second offer is `busy`, a
wrong transaction ID cannot mutate the active write, and every failure aborts the open OTA handle.

`CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE` keeps the candidate in pending-verify state. A 60-second guard
marks it valid only after the UI/display, peripheral service, Wi-Fi, and the authenticated Active
Companion Link are healthy. Otherwise ESP-IDF marks the candidate invalid and reboots into the prior
slot. OTA never touches the partition table, NVS, bootloader, eFuse, GPIO0 BOOT behavior, or release
USB Serial/JTAG recovery. See
[`ADR 0023`](../docs/adr/0023-sign-application-ota-and-confirm-first-boot-health.md).

## Long-duration HIL

For fast development feedback, use the 90-second contract. It exercises the display and
peripheral diagnostics, Setup cycle, recoverable Wi-Fi failure, and stabilized-heap gate without
waiting for manual button input. The recommended command includes the safe app-only flash so the
observation starts from a fresh boot. It is not release evidence and cannot replace the physical
button coverage or duration of the two-hour and 24-hour gates:

```bash
"$IDF_PYTHON_ENV_PATH/bin/python" tools/hil_smoke.py run \
  --config tools/hil_smoke_dev.json \
  --result-dir ".hil-results/dev-$(date -u +%Y%m%dT%H%M%SZ)"
```

After the transactional Wi-Fi HIL has committed a reachable test network, the unified
harness can build, app-flash, monitor, exercise an unreachable Wi-Fi candidate, and produce
auditable evidence for the two-hour M0 smoke. Activate ESP-IDF 6.0.2 and inspect the
non-destructive plan before starting:

```bash
"$IDF_PYTHON_ENV_PATH/bin/python" tools/hil_smoke.py plan \
  --config tools/hil_smoke_2h.json
"$IDF_PYTHON_ENV_PATH/bin/python" tools/hil_smoke.py run \
  --config tools/hil_smoke_2h.json \
  --result-dir ".hil-results/$(date -u +%Y%m%dT%H%M%SZ)"
```

The port may be supplied with `--port`; otherwise the harness sends a read-only identity
handshake to candidate Espressif serial devices and requires exactly one response identifying
`s3-rlcd-deck`. Firmware older than this harness does not implement that handshake: run
`discover` while the new development firmware is active, or supply the physically inspected
port explicitly for the initial app-only flash. The default run executes Host tests and the
development build, resolves the exact USB-JTAG adapter serial from that port, reads the Deck's
partition table without modifying it, and requires an exact match with the generated table.
Only then does it program and verify the same image in both OTA application slots. Writing both
slots guarantees the monitored image is current regardless of the OTA selection record. It
never requests whole-chip erase, eFuse changes, bootloader/partition/NVS writes, or irreversible
security configuration.

During the run, physically press KEY at least once and long-press BOOT at least once when
prompted. The harness enters Setup when necessary, submits one generated nonexistent open
network even when an older failed candidate is already visible, then requires that newly
observed validation transaction to fail and the previous committed Wi-Fi generation to recover.
It never reads, prints, sends, or stores the committed Wi-Fi password.

Results are written beneath the ignored `.hil-results/` directory. `serial.jsonl` contains
timestamped evidence restricted to an allow-list of structured Deck diagnostics; unknown
console lines and fatal-log details are replaced with fixed redaction markers. `summary.json`
contains the firmware commit, ESP-IDF version, run interval, reset/watchdog counts,
display/I2C/Wi-Fi error counts, minimum heap plus initial, configured warm-up baseline, and final
free-heap data, coverage counters, and SHA-256 hashes. The checked-in contracts use a 60-second
warm-up. `heap_drop_bytes` retains the initial-to-final measurement; the sustained-decline gate
uses `stabilized_heap_drop_bytes` from the warm-up baseline so one-time Wi-Fi/Setup initialization
allocations are not reported as a leak. A missing warm-up sample fails the run. `config.json` is
the exact reusable run contract. Failed preparation or monitoring returns nonzero and still
retains a failed summary and available evidence.
`tools/hil_smoke_24h.json` is the checked-in configuration for the later soak ticket.
