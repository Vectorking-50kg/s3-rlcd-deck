# Device protocol

This directory is the cross-end source of truth for versioned messages exchanged by a Deck and a Companion.

- `schema/` defines the wire constraints.
- `fixtures/` contains accepted and rejected examples that every Go and ESP-IDF parser must run unchanged.
- Control messages are UTF-8 JSON objects no larger than 16 KiB.
- Unknown fields are accepted for compatible minor evolution.
- Unknown protocol major versions, duplicate object keys, trailing documents, malformed JSON, and oversized messages are rejected.

Raw Serial Session bytes use versioned WebSocket binary frames and are specified separately from control JSON.
