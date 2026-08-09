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

**Active Companion**:
The single paired Companion to which a Deck is currently connected. Other paired Companions are connection candidates, not simultaneous data sources.
_Avoid_: Primary server, current agent

**AI Page**:
A Deck screen that presents an AI Snapshot and keeps the target serial transmitter disarmed.
_Avoid_: Dashboard, provider screen

**Serial Session**:
The bounded period that begins when the user enters the serial page and ends when they leave it. Serial counters, buffers, and transmission authority belong to this period.
_Avoid_: Terminal session, UART mode

**TX Owner**:
The sole source currently authorized to transmit to the target during a Serial Session. The owner is either USB or Web, never both.
_Avoid_: Controller, writer

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
