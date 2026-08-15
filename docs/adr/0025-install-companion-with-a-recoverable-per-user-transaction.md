# Install Companion with a recoverable per-user transaction

The Companion is its own per-user installer. `--install` stages an immutable executable under a
version-and-commit directory, snapshots every schema-bearing data file, writes an owner-only
Installation Journal, migrates data, installs a disabled Login Startup Registration, commits the
new Installation State, and only then enables startup. No step requires administrator privileges.
The registration identifier contains a stable hash of the installation root, so development and
native-smoke roots cannot overwrite the user's normal registration.

The installer holds a Maintenance Fence and the normal single-instance lock for the complete
migration transaction. A newly bootstrapped process recognizes that bounded fence and waits for
the installer to release the instance lock; a real second instance remains an immediate error.
Enable/disable changes only future login startup and remains available while the app is running.
Uninstall removes/ends the registration and Installation State but deliberately retains user data,
migration snapshots, and every staged executable version.

An incomplete Installation Journal always means the transaction is uncommitted. The next manager
open restores the exact pre-migration files and prior startup registration before accepting another
operation. Enable, disable, and uninstall use the same journal without copying unchanged user data.
Rollback errors remain visible and leave the journal for an idempotent retry. Capacity, file type,
symlink/reparse identity, permissions, and bounded input size are checked before mutation.

Installed defaults bind both the management Web and Device Hub to loopback. A LAN Device Hub is an
explicit install option and therefore an explicit firewall/network decision; installation never
opens a firewall rule. Deterministic application archives carry build identity, SPDX SBOM, bundled
license texts, per-file hashes, archive SHA-256, and the exact reproducible-build inputs. Platform
code signing/notarization is a publication credential step over those audited contents, not a
source-controlled private-key operation.
