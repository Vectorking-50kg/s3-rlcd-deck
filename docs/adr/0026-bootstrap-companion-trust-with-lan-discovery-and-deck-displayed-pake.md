# Bootstrap Companion trust with LAN discovery and a Deck-displayed PAKE

**Status:** proposed. This decision supersedes ADR 0007 only after Pairing v2 passes the
same-clean-commit real-Deck acceptance defined for the migration.

**Tracking:** GitHub Issue #85 is the parent specification. Issues #86 through #91 implement and
accept the discovery, PAKE, protocol, Deck, Companion, Web, migration, and physical-HIL slices.

A Deck that already has a validated normal Wi-Fi configuration and a Mac running Companion on the
same mutually reachable local network must be able to complete Pairing without either computer or
Deck changing networks. Setup Mode remains the recovery surface for Wi-Fi, calibration, and
device-owned settings; it is no longer the authorization channel for new Companion trust.

The Deck opens a bounded Pairing Window and advertises one untrusted Pairing Candidate using
DNS-SD service type `_s3rlcd-pair._tcp.local.`. The record contains only protocol compatibility,
product capability, pairable state, and a per-window random instance value. It never contains a
stable Device ID, MAC address, Pairing code, certificate fingerprint, Token, Wi-Fi identity, or
trust state. The Companion backend—not browser JavaScript—browses DNS-SD and gives the management
Web an opaque candidate reference. Discovery therefore provides a route to a candidate, never
identity or trust, and a manual-IP fallback must use the same authenticated protocol.

Selecting a candidate starts one Pairing Session. The Deck generates a uniform six-digit code with
its cryptographic random source, displays it with a deadline, and retains only the short-lived
secret state required by that session. The code is entered only into the authenticated loopback
management Web. It is never sent in cleartext, persisted, logged, advertised, placed in a URL, or
included in diagnostic evidence. Companion and Deck use an audited password-authenticated key
exchange with transcript binding, mutual key confirmation, and authenticated encryption. The
first implementation candidate is ESP-IDF `protocomm_security2` (SRP6a plus AES-GCM); it may be
adopted only after a Go/macOS-to-ESP32-S3 interoperability spike proves correct-code, wrong-code,
resource, cleanup, and replay behaviour. A different PAKE requires equivalent public analysis,
cross-language vectors, and an ADR update. Plain HTTP code submission, unauthenticated TLS,
home-grown HMAC/ECDH code checks, and discovery-derived trust are rejected.

After PAKE confirmation, Companion creates Provisional Trust and sends the per-Deck Token,
Companion certificate DER, independently verifiable fingerprint, Device Hub locator, and protocol
identity only inside the authenticated channel. The Deck stages a Companion Profile without
replacing any existing Active Companion. Deck Profile commit, Companion trust commit, and the first
certificate-pinned, Token-authenticated Device Link form one recoverable transaction: neither UI
may report successful Pairing until the exact new trust establishes an authenticated WSS hello and
heartbeat. Failures, expiry, restart, capacity exhaustion, storage errors, and lost receipts leave
the previous Profile set and Active Companion unchanged, and all provisional secrets are cleared.

TLS verification must also work after a cold boot when the board RTC has no backup time. The Deck
does not disable certificate verification or trust unauthenticated network time. If its system wall
clock is older than the immutable firmware-build lower bound, the exact PAKE-authenticated or
committed pinned certificate may seed the clock only inside the intersection of its validity window
and that build lower bound. A credible wall clock is never moved to make a future or expired
certificate pass. The first heartbeat received through that exact pinned WSS transport then becomes
the trusted UTC sample. This closes the first-connection time bootstrap without weakening the
certificate, Token, or expiration checks.

An unpaired Deck may open its first Pairing Window automatically for a bounded period after normal
Wi-Fi becomes usable. A paired Deck requires local user action to open another Pairing Window, so
arbitrary LAN clients cannot continually replace the display with Pairing requests. One Deck has
at most one active Pairing Session, one code, three online attempts, and a bounded cooldown.

Normal operation keeps ADR 0006: the Deck initiates the certificate-pinned WSS connection. To avoid
turning a DHCP address into persistent identity, Companion also advertises a stable
`_s3rlcd-hub._tcp.local.` service instance and the Companion Profile records that service identity
alongside the last known address. DNS-SD spoofing can redirect a connection attempt but cannot pass
the committed certificate, Token, Device Identity, or protocol checks. Guest networks, multicast
filtering, and client isolation can still prevent discovery or connectivity and must produce an
explicit unavailable result rather than weakening authentication.

On macOS, Hub publication uses the system Bonjour `DNSServiceRegister` API and is scoped to the
same selected physical LAN interface as the advertised address. A generic user-space mDNS server
that opens its own UDP 5353 listener is rejected because it cannot reliably coexist with
`mDNSResponder`. Pairing credentials remain local until Bonjour has delivered its asynchronous
registration-success callback; startup, policy, name-conflict, or publication failures surface as
Hub unavailable rather than being misreported as a post-staging Device Link failure.

The external Companion PairingCoordinator Interface is limited to discovering candidates,
starting a session, confirming the Deck-displayed code, observing status, and cancellation. Its
Implementation owns DNS-SD, interface selection, PAKE, candidate/session expiry, Provisional Trust,
transaction recovery, and Device Link confirmation. The Deck LanPairing Interface owns Pairing
Window lifecycle and a display-safe state projection; its Implementation owns advertisement,
secure transport, random code state, credential validation, Profile staging, and cleanup.
CompanionProfiles accepts only already-authenticated credentials and owns their transactional
persistence; it no longer performs an unauthenticated network redeem.

Pairing v1 remains available only as a versioned migration fallback for old firmware during one
compatibility release. Existing Companion Profiles continue to connect without re-Pairing. Once
Pairing v2 real-Deck evidence is complete, ADR 0007, the Setup recovery Pairing form, peer-IP
authorization, response-ack barrier, and the dual-homed Setup-client acceptance path are retired.
