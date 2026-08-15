# Reconcile Provider configuration into complete snapshots

The authenticated management Web edits Structured HTTP Providers through one `Service` that owns
configuration, credential transactions, and collector lifecycle. A create or update writes new
credential bytes to the platform vault, publishes one protected non-secret configuration, and only
then wakes the runtime supervisor. Delete and reorder follow the same configuration-first boundary.
Management responses expose safe definitions and `secret_configured` booleans, never Secret
References or credential bytes.

The supervisor reconciles the committed ordered definition set into one independent collector per
Provider. Changed and deleted collectors are canceled; unchanged collectors keep their last state.
A request, schema, authentication, or lifecycle failure publishes `UNAVAILABLE` for that Provider
without terminating siblings, the management Web, Device Hub, history, or serial features. Runtime
owns a complete ordered clone of the latest normalized Provider states and captures history only
when one state actually changes.

Every Codex, Cursor, or Structured HTTP update causes Runtime to encode one complete `snapshot.ai`
document. Device Hub retains only the latest validated document and wakes each authenticated Deck
through its existing sole WebSocket writer. Intermediate documents may be coalesced under
backpressure; partial Provider deltas and concurrent WebSocket writers are forbidden. This keeps a
Deck's `provider_order` and `providers` arrays atomic and bounded to the shared 16 KiB contract.

The Deck validates the complete document, projects every Provider into fixed-size display fields,
and preserves the selected Provider by ID across reorder. KEY short press cycles the projected
order while Setup is inactive; with only Codex configured it alternates between Codex and a local
configuration hint. Removed Providers fall back to Codex. Every page keeps `TX DISARMED`, hides
unknown numbers, and renders degraded, stale, unavailable, and experimental state as text.

This chooses configuration-first eventual activation and latest-state coalescing over synchronous
collector startup or delta delivery. Management saves therefore remain bounded by local vault and
configuration commits, while slow upstream Providers and slow Deck connections cannot hold the
configuration transaction or block one another.
