# Device protocol

This directory is the cross-end source of truth for versioned messages exchanged by a Deck and a Companion.

- `schema/` defines the wire constraints.
- `fixtures/` contains accepted and rejected envelope and Pairing examples that every Go and ESP-IDF parser must run unchanged.
- Control messages are UTF-8 JSON objects no larger than 16 KiB.
- Unknown envelope fields are accepted for compatible minor evolution; security-sensitive Pairing documents reject unknown fields.
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
