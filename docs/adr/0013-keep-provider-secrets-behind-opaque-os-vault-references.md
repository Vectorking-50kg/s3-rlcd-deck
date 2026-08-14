# Keep Provider secrets behind opaque OS-vault references

User-entered Provider credentials belong to one Companion-owned Secret Store. Its public seam is
limited to put, get, idempotent delete, and metadata-only list operations keyed by a generated
Secret Reference. Persisted Provider definitions contain only that opaque, non-secret reference;
callers cannot choose a Keychain service, Credential Manager target, file path, or upstream account
identifier, and listing returns only references. Windows Credential Manager has no metadata-only
enumeration call; that private adapter never copies or returns the blobs materialized by
`CredEnumerateW` and overwrites them before freeing the native result block.

The production adapters use macOS Security.framework Keychain items and Windows Credential Manager
entries under one fixed Companion Provider namespace. macOS queries explicitly forbid
authentication UI so a background collector cannot raise an unexpected password prompt. Platform
lock, permission denial, cancellation, absence, and corruption are reduced to fixed errors that do
not contain native messages or credential bytes. The namespace and strict random-reference grammar
also prevent this module from discovering or changing Codex/Cursor-owned OAuth, Cookie, access, or
refresh-token records.

A create either installs one new random reference or exposes no reference. An update replaces the
value at the existing reference using the platform's single successful write and never allocates a
replacement reference, so failure retains the old value without an orphan. Delete is retryable and
only reports success after the native mutation commits; cancellation observed after a successful
native mutation does not turn that commit into a reported failure. Uninstall cleanup enumerates
only this namespace's metadata and idempotently deletes each reference.

Structured Provider definitions use the Secret Reference type directly. Template/curl drafts keep
credential slots outside their JSON representation. The protected definition owner journals each
new opaque reference before publication. Its atomic file replacement then activates those
references and journals the old references retired by an update or delete. Vault deletion is
idempotent; successful cleanup removes journal entries and failed cleanup remains persisted for the
next startup retry. Thus a failed config write retains the old definition and secret, while a
committed replacement cannot silently orphan its retired reference. The unavoidable OS-vault/file
boundary is ordered conservatively: reference creation, durable cleanup intent, then publication.

Tests use an in-memory adapter to cover duplicate IDs, concurrent updates, lock/permission/cancel
failures, rollback, redaction, and cleanup. Desktop CI additionally performs create, read, update,
enumerate, delete, and absence verification against real macOS and Windows user vaults. A file-based
fallback and shell-command adapter were rejected because they weaken platform access control,
complicate scoped enumeration, and make secret-bearing process or diagnostic boundaries harder to
audit.
