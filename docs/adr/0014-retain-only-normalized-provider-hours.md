# Retain only normalized Provider Hours

Companion history stores the latest validated Provider state per UTC hour in normalized SQLite tables, never raw responses, AI Snapshots, sessions, prompts, or serial content. A single WAL writer accepts bounded asynchronous captures while bounded readers copy results before CSV delivery; schema upgrades first create a consistent protected SQLite backup. This deliberately trades raw replay fidelity for a small privacy surface, predictable 90-day capacity, and collection that remains independent from slow history consumers.
