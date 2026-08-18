# Pairing v2 same-LAN acceptance

**Status:** BLOCKED pending one same-clean-commit macOS + real-Deck run.

Tracking: [Pairing v2 specification #85](https://github.com/Vectorking-50kg/s3-rlcd-deck/issues/85),
[physical acceptance #91](https://github.com/Vectorking-50kg/s3-rlcd-deck/issues/91).

This gate proves the production user path: Mac and Deck remain on the same mutually reachable
normal LAN; Companion discovers an anonymous Deck candidate; the Deck displays a random six-digit
code; the user enters that code only in the authenticated loopback management Web; and Pairing is
reported complete only after transactional Profile/Trust commit and the first certificate-pinned,
Token-authenticated Device Link heartbeat.

The controller builds and tests the clean commit, builds both dev and release firmware, safely
programs only both OTA application slots with the same-commit diagnostic build, starts the exact
same-commit macOS Companion, and records allow-listed diagnostics. The diagnostic build is needed
only to prove the physical transaction; Pairing v2 production code and release resource fit are
covered by the same preflight. It never changes the Mac network, starts Setup Mode, asks for the
code on the command line, or stores a code, Token, certificate, Device ID, Wi-Fi identity, address,
or raw Companion output.

## User procedure

1. Connect the Mac and Deck to the same ordinary home/office LAN. Do not join a Deck Setup AP.
   Client isolation, guest Wi-Fi isolation, or multicast/DNS-SD filtering must be disabled.
2. Close any already running Companion. Connect the Deck over USB and note its explicit
   `/dev/cu.usbmodem...` port.
3. From a clean checkout run:

   ```bash
   python tools/pairing_v2_acceptance.py \
     --port /dev/cu.usbmodemXXXX \
     --result-dir .hil-results/pairing-v2-YYYYMMDD-HHMM
   ```

4. Wait for the controller to finish builds, safe app-only programming, one fresh Deck boot, and
   Companion startup. Keep the Mac on the same LAN for the entire run.
5. If the Deck already has a Companion Profile, short-press BOOT once to open its Pairing Window.
   An unpaired Deck opens its first bounded window automatically after normal Wi-Fi is usable.
6. Open the management console from the Companion menu-bar item. If needed, browse to
   `http://127.0.0.1:7777` and unlock it using the normal local management flow.
7. Open **Deck 清单** or **网络与信任**, select **扫描并配对**, choose the anonymous Deck candidate,
   and wait for a six-digit code to appear on the Deck screen.
8. Enter that code in the Companion Web and select **验证并配对**. Do not type the code anywhere
   else and do not photograph or copy it into evidence.
9. Accept only the final message **Deck 配对完成**. The Deck must leave the Pairing page, the new
   Device Link must become online, and the Setup AP must never appear.

The command returns zero and writes `status: passed` only when all gates are observed in order.
Cancellation, expiry, wrong code, Setup entry, an extra boot, identity mismatch, network change,
missing authenticated Device Link, redaction failure, or cleanup failure returns non-zero and keeps
only a fixed-schema failed summary plus redacted logs.

## Evidence publication

Raw evidence remains under ignored `.hil-results/`. After a passing run, copy only its
`summary.json` to `docs/acceptance/evidence/`, set the `summary` field in
`pairing-v2-manifest.json`, and link that same file/hash from Issues #85 and #91. Never publish the
serial capture, management token, Pairing code, Profile, certificate, Token, addresses, network
name, or network fingerprint input.

Until this report passes, ADR 0026 remains proposed and ADR 0007 Pairing v1 remains an explicit
compatibility fallback. Host fixtures, the discovery/Security2 spike, or the old Setup-AP Pairing
flow cannot satisfy this physical gate.
