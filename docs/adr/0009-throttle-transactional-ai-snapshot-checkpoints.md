# Throttle transactional AI Snapshot checkpoints

The Deck keeps every valid AI Snapshot immediately in a bounded in-memory Snapshot Store, but
checkpoints to Flash no more than once per 30 minutes. Checkpoints reuse the transactional
candidate/two-slot record format with a version, encoded length, CRC, readback verification,
and atomic active marker in a dedicated 128 KiB `snapshot_nvs` partition. This preserves the
last committed Snapshot across interrupted writes without coupling live display updates to NVS
latency or wear. A private worker owns Flash open, recovery reads, writes, and close; Store
creation therefore returns a bounded volatile view while recovery completes. A small
versioned/CRC-protected attempt watermark preserves the 30-minute success-or-failure throttle
across restart. If that watermark is missing or corrupt while a transaction exists, the first
trusted UTC observation starts a conservative 30-minute window before another attempt.
Recovery reconciles an already published live document by timestamp: the newer document wins,
and a same-timestamp byte conflict fails closed to the committed record.
After the window expires, a faulted transactional Store is reconstructed from the last valid
marker/candidate so a transient NVS failure can recover without weakening the wear limit.

Snapshot freshness is a Store policy rather than a UI convention. Data younger than 24 hours is
Fresh while the Active Companion is online and Stale while it is offline. At 24 hours, with an
invalid clock, or after a wall-clock rollback, the Store retains the
bytes internally but no longer exposes the document or quota values. Invalid contracts, private
data, future timestamps, regressing timestamps, and unknown schema majors never replace memory
or Flash state. The Store keeps separate trusted-time high-water marks for authenticated
Snapshot publication and display reads; either source moving backward hides the document until
that source recovers its previous high-water mark.

Shutdown drains an already queued checkpoint within a two-second budget. If the storage driver
remains stalled, ownership is retained and shutdown reports failure so the same owner can retry
cleanup later; the Store or NVS adapter is never freed underneath the worker.
