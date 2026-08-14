# Isolate Codex App Server behind a versioned private adapter

The Companion starts the official `codex app-server --stdio` process behind a versioned,
message-oriented adapter. The adapter owns JSONL framing, initialization, request correlation,
notifications, reconnects, and strict response validation. Its only outward value is the shared
Provider/Session DTO; raw App Server responses, account metadata, user-agent values, local paths,
thread identifiers, prompts, and authentication state cannot cross the module boundary.
The child receives an explicit cross-platform environment allowlist for executable lookup,
Codex/user configuration, locale, and temporary storage; Companion management Tokens and other
Provider credentials are not inherited.

Rate-limit notifications trigger a fresh `account/rateLimits/read` and `account/usage/read`
transaction rather than becoming an independent source of truth. Dynamic upstream limit IDs are
reduced to bounded identifier-safe window names, numeric values are range checked, and unknown
schema fields fail closed. Timeout, process exit, authentication, permission, and schema failures
produce a degraded Codex Provider only; they do not stop the Companion, other Providers, the
Device Link, or the recovery surface.

Verified State authority is connection-local. A thread becomes eligible only after this adapter
successfully resumes it through the same live App Server connection. Unowned notifications are
ignored, identifiers are one-way anonymized, and reconnect discards all prior authority. Pending
and owned authority share the AI Snapshot limit of 16 threads, including concurrent resumes. The
Companion never reads, refreshes, rotates, or writes `auth.json`; authentication remains owned by
Codex itself.
