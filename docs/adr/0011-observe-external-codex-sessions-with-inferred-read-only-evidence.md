# Observe external Codex sessions with inferred read-only evidence

The Companion observes independently owned Codex sessions through a private, read-only module.
It enumerates existing Codex process identities and bounded session-file metadata; it never starts,
resumes, signals, attaches to, or takes ownership of those sessions. Process identity is the pair of
PID and process start time so PID reuse cannot inherit activity.

Only a unique macOS process-to-file mapping whose size grows across two polls can produce Running.
A first observation or a file that stops growing is Recent while it remains inside the recency
window. A file without a live owner becomes Ended after that window. Multiple files for one session,
one process mapped to multiple candidates, multiple owners, file rotation without continuity, and
Windows weak process/file correlation produce Unknown. This source can never produce waiting,
approval, input, success, or failure states.

The observer reads only one complete, bounded `session_meta` line. It extracts the upstream session
identifier solely to derive the same one-way anonymous ID used by the official adapter, then clears
the input buffer. JSONL bodies, titles, prompts, replies, filenames, absolute paths, commands, tool
arguments, and process names cannot cross the module boundary. Session-tree traversal uses pinned
directory handles and relative no-follow opens on macOS and Windows; symlinks are not followed.
Scanning, process counts, candidates, child-process time, and captured output are bounded, and
malformed, partial, rotated, inaccessible, or future-dated evidence fails closed.

Official Codex App Server collection and inferred observation have independent supervisors. An
observer error clears inferred sessions and retries without modifying the official Provider, quota,
usage, or Verified sessions. Runtime deduplicates by anonymous ID with Verified data winning and
keeps the shared 16-session bound.
