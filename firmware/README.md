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
scan, and a credential form; it does not pair a Companion.

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
seconds. When Setup closes after a failed candidate, the service restores and reconnects the
last committed station configuration.

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
