# Stage age backups before one configuration publication

Backup Archive is an explicit versioned DTO encrypted with an `age` scrypt recipient, not a copy of
`structured-providers.json`, Keychain/Credential Manager, SQLite, Pairing state, or process memory.
Export resolves only user-entered Structured HTTP Provider Secret References and immediately owns
and clears those bytes. Codex/Cursor discovery credentials, pairing Tokens and verifiers, Web
sessions, Provider Hour data, raw responses, and serial buffers cannot be represented by the DTO.
Encrypted input is limited to 8 MiB and authenticated plaintext to 4 MiB.
The process admits one memory-hard scrypt operation at a time; waiters remain context-cancelable.

Import first decrypts and strictly validates the complete archive, then produces an Import Preview
containing only safe labels, conflict keys, counts, exclusions, and warnings. Preview issues a
ten-minute single-use receipt bound to the encrypted archive digest, import mode, and current
configuration digest; direct imports and stale previews fail closed. Merge and Provider-only modes
require an explicit keep-current/use-backup decision for every conflict. Replace requires a fresh
Preview that carries the destructive warning.

Commit creates fresh platform-vault references and durably journals each one before writing its
secret. One protected-file replacement then publishes Provider order, definitions, Web settings,
application settings, and non-secret Device Profile cache while activating new references and
journaling retired references. Any failure before that replacement compensates every staged
reference and leaves the previous configuration authoritative. Vault cleanup after publication is
idempotent and journaled for startup retry. Imported runtime settings take effect after a Companion
restart so live collectors and listeners never observe a partly reconfigured graph.

Export files use an owner-only Unix mode/current-user Windows DACL transaction without mutating the
user-selected parent directory and leave no temporary file after success or failure. The CLI reads
the exact passphrase only from an owner-only file, never argv or the environment. Reads use a
no-follow regular-file handle and a stable path-to-handle check. This adds a restart boundary and a
two-step management workflow, but keeps secrets in their platform owner and makes both rollback and
privacy properties auditable.
