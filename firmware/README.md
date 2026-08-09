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
frames during the 20-second observation window:

```bash
./tools/hil_boot_smoke.sh /dev/cu.usbmodemXXXX --expect-display
```

`display_ready` is emitted only after the first panel IO completion callback;
`display_progress` then reports later completions. The harness rejects wrong
resolution/frame size, fewer than three frames, any timeout/start failure/rejected update,
or a second `boot_ok` indicating an unexpected reset. Landscape orientation, black/white
appearance, mirroring, and clipping still require observing the physical Deck screen.
