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
address supplied by form data cannot redirect the one-time trust bootstrap.

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
`deck_companions` NVS namespace. A Profile contains its redacted display fields plus an
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
seconds. All credentials remain private to the Profile and Device Link modules.

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
