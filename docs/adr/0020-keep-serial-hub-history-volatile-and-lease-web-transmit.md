# Keep Serial Hub history volatile and lease Web transmit

The Companion owns one volatile Serial Hub for the current Deck Serial Session. Target RX arrives
as versioned WebSocket binary frames whose fixed 32-byte big-endian header is defined by
`protocol/catalog/serial-frame-v1.json`. The header binds every non-empty payload to a channel,
nonzero Session ID, nonzero sequence, and monotonic receive timestamp. Go and ESP-IDF execute the
same accepted/rejected fixtures. Text controls remain strict Device Link JSON.

The Hub retains at most 8 MiB of payload and 65,536 bounded frame records in RAM. A new Session,
explicit Session end, application stop, or Hub close zeroes and releases all payloads and observer
cursors. Serial bytes cannot enter logs, configuration, history SQLite, backups, crash evidence, or
any other persistent owner. Downloads copy only a bounded range from the current Session and carry
`no-store` response policy.

Every authenticated Web observer receives an independent ordinal cursor. Ring overwrite advances
only observers that fell behind and records their lost byte count; a slow or disconnected browser
never waits on Device Link ingest or another observer. Session ID, sequence order, monotonic order,
channel, and payload size are checked before publication. Reconnect requests begin after the
Companion's newest retained sequence. The Deck first replays its non-destructive current-session
history, then discards duplicate live-sink references before resuming live delivery. A lost cursor
starts at the Deck's oldest retained block and remains visible as overwrite/gap evidence.

Web transmit is a separate authority path. One authenticated observer may request a ten-minute Web
TX Lease. The Companion reports `transitioning` until the Deck's sole Serial owner returns the exact
request result; it reports USB only after the Deck confirms USB with no Lease. A disconnect, release,
expiry, Device Link failure, or Companion shutdown requests Deck revocation first. The Deck also
revokes autonomously when WSS disconnects or its Lease expires. Unsent USB or Web source queues are
cleared during the serialized owner transition, while target-RX rings remain independent.

A Device Link write failure is an ambiguous delivery result: the Companion retains the exact
pending transition and retries instead of publishing a guessed owner. The Deck maps each
process-local Companion request ID onto a Link-lifetime monotonic service request ID, so a restarted
Companion cannot be rejected as stale; replies still carry the originating external request ID.
Each new authenticated transport generation also resets only the Web-frame ordering fence, allowing
the new process epoch to begin at sequence one without weakening ordering inside a connection.
Runtime shutdown first closes and joins all hijacked observer WebSockets under one deadline, then
requests and waits for exact owner revocation before closing Device Link and zeroing the Hub.

Lease IDs, browser IDs, and pending request capabilities never appear in the management status
document. Raw Web input is accepted only from the exact Lease holder, wrapped by the Companion in a
bounded binary frame, and revalidated by the Deck owner immediately before UART FIFO submission.
No observer, download, retry, or transport callback receives Router pointers or internal queue
ownership.
