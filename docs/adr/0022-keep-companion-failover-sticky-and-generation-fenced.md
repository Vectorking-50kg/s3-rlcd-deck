# Keep Companion failover sticky and generation-fenced

The persisted Active Companion remains authoritative while it reconnects and for 30 continuous
offline seconds. After that window, one Failover Round tries every other Profile by descending
priority, then descending last-success, with stable Profile order as the final tie-break; if all
are offline, the Deck returns to the persisted Active and waits another full window. A candidate
does not become Active until its first valid authenticated heartbeat and the new Active plus
monotonic last-success commit in one Profile-set transaction. This avoids preemption when an old
higher-priority Companion recovers, keeps Snapshot state stale throughout switching, and lets the
Serial owner revoke before another transport starts.

Every replacement transport begins in probe-only authority: only a strict heartbeat is
accepted. Snapshot, Serial control, and Serial binary frames are rejected until that heartbeat
and the generation-fenced Active transaction both succeed. The new source's Snapshot remains
stale until that exact transport supplies a valid `snapshot.ai`. Disconnect advances an
independent Serial Web-transport epoch, invalidates already queued Web-owner requests, and waits
for the owner task's USB revoke acknowledgement before opening the replacement WSS connection.
That network epoch is separate from the physical lifecycle epoch, so BOOT exit/stop always retains
priority and drives UART TX high-impedance even if a disconnect revoke is queued later.

Every candidate secret read and success commit is fenced by the observed Profile generation.
Pairing, address changes, revocation, or a recovery-page manual selection therefore cancels an
in-flight decision; a delayed heartbeat cannot overwrite the newer user decision. WebSocket events
also carry a Link-local transport generation so queued events from the previous candidate cannot
enter the replacement connection. We reject changing Active before connectivity is proven and
probing every Profile continuously because either choice would lose sticky intent and amplify an
all-offline network failure.

Recovery exposes both explicit Active selection and transactional priority editing. Changing a
priority advances the Profile generation, so an in-flight round must restart from the user's new
ordering rather than completing an obsolete decision.
