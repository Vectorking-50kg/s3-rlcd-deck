# Pairing v2 discovery and PAKE interoperability spike

**Status:** NOT RUN. This spike proves technical feasibility only; it cannot satisfy the final
Pairing v2 acceptance or authorize replacement of ADR 0007.

**Tracking:** GitHub Issue #86. The parent Pairing v2 specification is GitHub Issue #85.

The spike runs on one ESP32-S3-RLCD-4.2 Deck and one Mac connected to the same mutually reachable
normal LAN. The Mac stays on that LAN for the complete run. The Deck must already have a validated
Wi-Fi configuration; Setup Mode and its AP are not started. Source, firmware, Companion, harness,
and evidence must come from the same clean commit.

## Required observations

1. The Deck publishes `_s3rlcd-pair._tcp.local.` only while a bounded Pairing Window is open.
2. The Companion process discovers it through a production macOS DNS-SD adapter. Browser code does
   not open sockets or submit an arbitrary address.
3. The DNS-SD record contains only the allow-listed Pairing v2 protocol, model capability,
   pairable state, and per-window random instance value. Captured records contain no stable Device
   ID, MAC address, Pairing code, certificate data, Token, Wi-Fi identity, or firmware commit.
4. Starting a candidate makes the real Deck generate and display a six-digit code with leading
   zero support. Exactly one session is active and the code expires without persistence.
5. A Go/macOS client and ESP-IDF 6.0.2 complete the exact `protocomm_security2` SRP6a handshake and
   exchange one bounded authenticated test document under AES-GCM when the code is correct.
6. A wrong code, replayed handshake, expired session, duplicate confirm, altered transcript, or
   fourth online attempt never opens the authenticated endpoint and leaves no reusable state.
7. Pairing code, SRP secret state, salt/verifier, session key, and decrypted payload do not appear
   in firmware diagnostics, Companion logs, command arguments, URLs, DNS-SD, or retained evidence.
8. Window cancellation, expiry, client disconnect, successful completion, task stop, and Deck
   restart all release sockets/tasks and clear session-owned sensitive buffers. A second fresh
   session works after each cleanup path.

## Resource and timing gate

- Scan returns a candidate or a bounded unavailable result within 5 seconds.
- Begin displays a code or a bounded error within 5 seconds.
- A correct confirm reaches the authenticated test endpoint within 15 seconds.
- Cancellation and normal stop complete within 2 seconds and are retryable after a timeout.
- Dev and release firmware still fit both 1700 KiB OTA slots with at least 10% headroom.
- Ten alternating correct/wrong sessions complete without reset, watchdog, task-stack warning,
  monotonic internal-heap decline, or a declining largest free internal block after cleanup.
- The evidence records binary size, begin/confirm latency, task high-water marks, minimum free
  internal heap, largest free internal block, PSRAM minimum, reset reason, and fixed error counters.

## Decision

The spike is PASS only if every required observation and resource gate is supported by retained,
redacted evidence from the real Mac and Deck. A Host fake, Python-only server, static fixture,
successful build, or discovery without PAKE is insufficient. If `protocomm_security2` fails the
gate, Pairing v2 remains active but implementation pauses at the crypto Seam until an audited
equivalent PAKE and cross-language vectors are specified.
