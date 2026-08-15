# Separate user-authored Serial Presets from Session data

A Serial Preset is an explicit, bounded command template created by the user, not a capture of
Serial Session RX or TX. Companion may store at most 32 presets in its owner-only protected
configuration and include them only inside an encrypted Backup Archive; plaintext responses and
temporary buffers are bounded and cleared, and preset content never enters logs, diagnostics,
history, AI Snapshots, browser persistent storage, or automatic capture. Loading or selecting a
preset grants no authority and causes no I/O: the browser must still hold the exact current Web TX
Lease, apply the 256-byte send validation, and explicitly send. This preserves useful commands
across restarts without turning the volatile Serial Hub into a history or macro-execution system.
