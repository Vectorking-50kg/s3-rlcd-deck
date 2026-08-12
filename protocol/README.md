# Device protocol

This directory is the cross-end source of truth for versioned messages exchanged by a Deck and a Companion.

- `schema/` defines the wire constraints.
- `fixtures/` contains accepted and rejected envelope and Pairing examples that every Go and ESP-IDF parser must run unchanged.
- Control messages are UTF-8 JSON objects no larger than 16 KiB.
- Unknown envelope fields are accepted for compatible minor evolution; security-sensitive Pairing documents reject unknown fields.
- Unknown protocol major versions, duplicate object keys, trailing documents, malformed JSON, and oversized messages are rejected.

Raw Serial Session bytes use versioned WebSocket binary frames and are specified separately from control JSON.
