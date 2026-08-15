# Fan out UART RX through a fixed block Router

Each Serial Session creates one UART event queue, one fixed ingress block set, and one
Router-owned PSRAM pool. The `serial_rx` task is the only UART RX consumer. It copies available
bytes into 256-byte ingress blocks without waiting for a downstream transport, while the
`serial_router` task assigns one nonzero per-session sequence and fans the immutable block out to
four independently bounded sink rings: USB, WSS, reconnect history, and statistics. Sink adapters
can only copy a routed block or read counters through the Router interface; they never receive a
FreeRTOS queue, a pool pointer, or ownership of shared storage.

A full sink discards only its own oldest reference and increments its own byte/block overwrite
counters before the new reference is installed. A disconnected WSS client, unopened USB port, or
slow statistics consumer therefore cannot consume the history budget or stall UART RX. The
history sink is non-destructive and reports a gap plus the current oldest block when a reconnect
cursor has already been overwritten. Session ID, sequence, monotonic receive time, length, and raw
bytes are copied from the same immutable block, including across sequence wrap.

The Router performs no transport callback, logging, Flash write, or UI work. All payload,
metadata, and ring-index allocations occur before UART tasks start; submit performs no allocation.
The history payload budget defaults to 512 KiB and is configurable from 64 to 2048 KiB. Pool
metadata and ring indices are additional PSRAM overhead so the maximum setting does not consume
scarce internal RAM. Exiting the Serial Session first stops and joins both data tasks, then deletes
the UART driver and zeroes/frees every block and ring. A bounded shutdown failure keeps the full
owner for a later retry and restores GPIO17 to input/high-impedance.

UART FIFO overflow and driver-buffer-full events are global severe errors because bytes were lost
before per-sink fan-out. The owner samples those monotonic counters into a latest-only UI event;
the active Serial page places a data-loss warning and both bounded display counts directly below
its title. Exhaustion of the fixed ingress handoff or Router pool is tracked
separately as Router starvation. Per-sink overwrites remain local diagnostics and cannot be used to
claim a global UART failure. This design spends PSRAM on one shared payload copy plus bounded
reference rings, avoiding four payload copies while keeping consumer backpressure independent.
