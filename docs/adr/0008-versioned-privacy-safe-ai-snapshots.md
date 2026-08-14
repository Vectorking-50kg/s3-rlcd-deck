# Use a versioned, privacy-safe normalized AI Snapshot

The Companion sends the Deck only the shared `snapshot.ai` DTO, never upstream Provider
responses. Percentages use integer basis points, money uses integer micro-units plus an ISO
currency code, counts stay within the JSON safe-integer range, and unknown values are null or
an explicit Unavailable State. This avoids Go/ESP32 floating-point drift and keeps absence
distinct from zero.

Schema major changes fail closed so the Deck can retain its last valid snapshot. Each versioned
object in a higher minor may add at most 16 null, boolean, or bounded integer fields; unversioned
subobjects remain closed. New strings and containers require a new reviewed major contract
because they could carry prompts, paths, credentials, or other private content. Canonical
fixtures are the cross-end acceptance source for both Go and firmware.
