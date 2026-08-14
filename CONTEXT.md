# S3 RLCD Deck

S3 RLCD Deck is a dual-purpose desktop terminal that presents normalized AI usage state and mediates explicitly armed serial sessions while keeping account credentials off the device.

## Language

**Deck**:
The physical S3 RLCD Deck device, including its display, controls, local recovery surface, and serial connection to a target.
_Avoid_: Board, terminal device

**Companion**:
The trusted desktop application that collects provider data, manages credentials, serves the full Web console, and coordinates connected Decks.
_Avoid_: Agent, PC service, host app

**Provider**:
An AI service or local AI tool whose quota, balance, usage, or session state is normalized for presentation.
_Avoid_: Account, collector

**AI Snapshot**:
A timestamped, display-safe view of normalized Provider data sent by a Companion to a Deck. It excludes upstream credentials and raw private content.
_Avoid_: Raw snapshot, provider response

**Snapshot Store**:
The Deck-owned module that immediately retains the latest valid AI Snapshot in memory and transactionally checkpoints it to Flash at a bounded rate. It alone applies offline visibility policy to cached quota data.
_Avoid_: Raw cache, Provider database

**Quota Window**:
A Provider-defined interval whose normalized usage, remaining allowance, duration, and reset time may be presented in an AI Snapshot. Unknown values remain null rather than becoming zero.
_Avoid_: Limit bucket, rate-limit response

**Session Observation**:
A privacy-safe, anonymous description of one Provider session with an explicit source and confidence. It never contains prompts, replies, commands, tool arguments, or absolute paths.
_Avoid_: Thread record, conversation log

**Active Companion**:
The single paired Companion to which a Deck is currently connected. Other paired Companions are connection candidates, not simultaneous data sources.
_Avoid_: Primary server, current agent

**Pairing**:
The user-authorized establishment of scoped trust between one Deck and one Companion. A successful Pairing creates a Companion Profile on the Deck.
_Avoid_: Login, device discovery

**Companion Profile**:
A Deck-owned record of one paired Companion's identity, trust material, connection location, preference, and last successful contact.
_Avoid_: Server profile, account

**Device Hub**:
The Companion-facing surface dedicated to paired Deck connections and minimal device health. It is distinct from the management Web used by people.
_Avoid_: Management API, Web console

**AI Page**:
A Deck screen that presents an AI Snapshot and keeps the target serial transmitter disarmed.
_Avoid_: Dashboard, provider screen

**Serial Session**:
The bounded period that begins when the user enters the serial page and ends when they leave it. Serial counters, buffers, and transmission authority belong to this period.
_Avoid_: Terminal session, UART mode

**TX Owner**:
The sole source currently authorized to transmit to the target during a Serial Session. The owner is either USB or Web, never both.
_Avoid_: Controller, writer

**Web TX Lease**:
The temporary, exclusive grant that allows one authenticated Web client to act as the Web TX Owner during a Serial Session.
_Avoid_: Browser lock, Web session

**Target**:
The external 3.3 V TTL UART device connected to the Deck for monitoring or controlled transmission.
_Avoid_: Client device, downstream board

**Verified State**:
Session state reported through an official source that directly owns or observes the relevant session.

**Inferred State**:
Session state estimated from indirect evidence and explicitly presented with lower confidence.
_Avoid_: Verified state, exact state

**Unavailable State**:
The explicit absence of sufficiently trustworthy current data. It is not represented as a numeric zero or a fabricated state.
_Avoid_: Empty state, zero state

**Setup Mode**:
The temporary, locally initiated state in which a Deck exposes its recovery surface for Wi-Fi and device-owned settings without discarding the last valid configuration.
_Avoid_: Factory reset, pairing mode, captive portal

**Degraded Operation**:
Continued Deck operation with an explicitly unavailable non-critical capability, while healthy capabilities and the recovery surface remain usable.
_Avoid_: Normal operation, reboot loop, factory reset
