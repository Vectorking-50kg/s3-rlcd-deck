# Bound diagnostics to fixed redacted events

Diagnostics are a data model rather than formatted log text. The Companion accepts only a closed
`Diagnostic Event` schema: bounded level, module, code, HTTP status, latency, schema version,
redacted error code, numeric count, and a short SHA-256 identifier projection. There is no public
field for an exception string, path, URL, header, request/response, Provider raw value, prompt,
tool argument, Pairing/device Token, or Serial payload. Panic recovery records a fixed code and
never the recovered value.

One private Companion worker owns an in-memory queue and writes owner-only JSONL `Diagnostic Log
Segment`s. Producers never wait for disk I/O; queue or storage pressure drops new events within a
fixed bound and later records only the numeric dropped count. The current segment is atomically
replaced for durability and sealed after one hour or 256 KiB; sealed segments are immutable.
Segments retain at most seven days or 50 MiB by default and are strictly revalidated before export.
A restart cannot replace a segment that already exists.

Release firmware owns one volatile 64-event `Deck Diagnostic Ring` whose public API accepts only
enums and integers. An Active Device Link advertising the `diagnostics` capability answers an exact
`diagnostics.request` ID with the fixed ring snapshot. The Companion validates the shared wire
contract and keeps at most 32 recent Deck rings in memory; raw Device IDs are hashed only at the
bundle boundary.

The authenticated System surface builds a `Diagnostic Bundle` directly in bounded memory, so no
export temporary file exists. The archive has four fixed relative paths, includes at most the last
24 hours of Companion events, and carries a manifest with byte length and SHA-256 for every content
file. Configuration contributes schema keys only. Collection, Serial, and Device Hub owners remain
independent of export; failure returns no partial success and the product never uploads a bundle.
