# Device protocol

This directory is the cross-end source of truth for versioned messages exchanged by a Deck and a Companion.

- `schema/` defines the wire constraints.
- `fixtures/` contains accepted and rejected envelope and Pairing examples that every Go and ESP-IDF parser must run unchanged.
- Control messages are UTF-8 JSON objects no larger than 16 KiB.
- Forward compatibility is contract-specific: AI Snapshot defines bounded higher-minor scalar
  extensions, while security- or authority-sensitive Pairing and Device Link control documents
  (including signed OTA) reject unknown fields.
- Unknown protocol major versions, duplicate object keys, trailing documents, malformed JSON, and oversized messages are rejected.

## AI Snapshot v1

`schema/ai-snapshot-v1.schema.json` is the normalized `snapshot.ai` wire contract. Percentages
are integer basis points (`0..10000`), money is non-negative micro-units plus an uppercase
three-letter currency, and counts are bounded by JSON's `2^53-1` safe-integer limit. All wire
timestamps are canonical UTC RFC 3339 values ending in `Z`.
JSON Schema `maxLength` limits and both parsers count decoded Unicode code points, not UTF-8
bytes. Higher-minor extension names use the ASCII pattern `[a-z][a-z0-9_-]{0,31}`.

Every required field is present even when its value is `null`; null is the only unknown numeric
value and is never replaced with zero. Unknown schema majors are rejected. A higher minor may
add only null, boolean, or bounded integer fields. Unknown strings, arrays, and objects are
rejected as private data. The deny boundary also rejects prompt/reply/command/tool fields,
credentials, upstream raw/attributes, and absolute paths anywhere in the document.
Each versioned object may add at most 16 forward fields and a document may contain at most
2048 JSON syntax nodes, bounding parser memory while covering every declared Provider/window/
Session maximum.
Both implementations expose a retained-snapshot seam whose previous document changes only
after a candidate passes the entire contract.

`fixtures/ai-snapshot-v1/manifest.json` assigns the authoritative parser result to each canonical
fixture. Go and the ESP-IDF component execute that same manifest unchanged.

Raw Serial Session bytes use versioned WebSocket binary frames and are specified separately from control JSON.
`catalog/serial-frame-v1.json` is the authoritative fixed-header catalog: big-endian fields bind every
payload to a channel, nonzero Serial Session ID, nonzero sequence, device monotonic timestamp, and
exact payload length. V1 payloads are bounded to the Deck Router's 256-byte immutable block size.

## Signed OTA v1

`schema/device-link-v1.schema.json` defines `ota.offer`, `ota.chunk`, and `ota.result`. The
authoritative public-key catalog is `catalog/ota-signing-keys-v1.json`; firmware and Go contract
tests must prove their compiled projections match it. The canonical manifest is fixed-width and
binds key ID, minimum protocol, image length, board, version, and SHA-256. Signature bytes are raw
P-256 `r || s`; JSON carries them as canonical padded base64.

Chunks contain at most 3072 decoded bytes and use exact increasing offsets. Companion waits for the
matching Deck result before sending the next chunk, so transport and firmware queues remain bounded.
Both ends enforce a ten-minute total transaction deadline and the Deck additionally enforces a
30-second inactivity deadline. An error result terminates the transaction; only `ready_to_reboot` with the full image length allows
the Deck to reboot into ESP-IDF pending-verify state.

## Redacted diagnostics v1

`schema/device-link-v1.schema.json` also defines `diagnostics.request` and
`diagnostics.snapshot`. Only an authenticated Active connection that advertised the `diagnostics`
capability participates. The Companion allocates a nonzero request ID; the Deck echoes that exact
ID with at most 64 chronological events. Each event contains only monotonic milliseconds, bounded
level/component/code enums, and one unsigned numeric value. Unknown fields, strings outside those
enums, duplicate keys, out-of-order time, oversized rings, and a response for any other request or
transport generation fail closed. Canonical shared fixtures cover both accepted and rejected
documents.
