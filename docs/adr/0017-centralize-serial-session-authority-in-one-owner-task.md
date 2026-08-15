# Centralize Serial Session authority in one owner task

The Deck represents target transmission authority with one bounded `DISARMED → USB_TX ↔ WEB_TX`
state machine. Exactly one `serial_owner` task may mutate that state, install or uninstall UART1,
change GPIO17 between UART TX and input/high-impedance, clear pending source queues, renew a Web TX
Lease, or count rejected USB input. Button handling, Device Link control, USB, WebSocket, Router,
and UI code submit commands and consume immutable snapshots; none of them may infer or assign a TX
Owner independently. The state module retains counters and generation identifiers only, never
serial payload bytes.

Every successful entry creates a new nonzero Serial Session ID and starts with USB as the sole TX
Owner. A Web owner request carries that Session ID plus a monotonically increasing request ID. The
owner clears the unsent USB queue and publishes one result from the same serialized transition.
Exact retries replay only while that transition remains current; old-session, old-request, and
old-Lease commands fail closed. A Web disconnect, explicit release, or Lease deadline returns to
USB after clearing pending Web TX. Activity exactly at an expired deadline cannot revive the old
Lease.

BOOT exit advances a control epoch before the urgent command is placed ahead of ordinary work.
Every KEY entry is stamped with the epoch that was current when it was queued, so an older entry
that is delivered after BOOT is rejected instead of rearming the target. The owner first revokes
authority, then clears both pending source queues, uninstalls UART1, and restores GPIO17 to
input/high-impedance. UART installation failure performs the same fail-closed cleanup, never
allocates a Session ID, and leaves the AI Page visible. Only a successful owner snapshot activates
the bounded Serial status page; unavailable/install-failed states remain explicit in the AI footer.
Successful retry clears the current install-failed state without erasing the cumulative counter.
The application does not create the Serial owner, its view consumer, or button producers until the
display UI publishes READY; an asynchronous UI failure therefore cannot leave an invisible target
armed.
Owner events cross a depth-one latest-state mailbox before any application/UI lock, so a stalled
view consumer cannot delay BOOT revocation or Lease expiry. Stopping the service drains through
that exit transition and retains the complete service owner if its bounded join times out,
allowing cleanup to be retried.

The owner task exists while the application runs, but it is dormant in `DISARMED`; UART1 and the
target data path do not exist on the AI Page. Later Router, USB bridge, WebSocket, and history work
must attach behind this owner boundary instead of introducing another authorization flag. This
adds one command hop and generation fields, but makes simultaneous USB/Web transmission, stale
browser control, and partial teardown structurally unavailable.
