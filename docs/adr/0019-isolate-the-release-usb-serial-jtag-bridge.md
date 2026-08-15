# Isolate the release USB Serial/JTAG bridge

The Deck uses the ESP32-S3 built-in USB Serial/JTAG peripheral as the single
USB transport for a target Serial Session. Release firmware disables the
ESP-IDF application console and installs the ESP-IDF USB Serial/JTAG driver
only after the owner has successfully armed UART1. Development/HIL firmware
uses that same endpoint for structured boot diagnostics and does not start the
target bridge. The two byte streams therefore cannot coexist in one artifact.

Target RX reaches USB only through the Router's independent USB sink. A
dedicated low-priority output task copies one routed 256-byte block into bridge
state and retains its unsent suffix across zero-progress writes and USB
disconnects. The driver's fixed 4 KiB TX ring absorbs a bounded amount of host
latency. Because USB SOF presence cannot prove that a host has opened the COM
port, an unopened or occupied port is handled exactly like any other slow
consumer: the USB sink eventually overwrites only its own oldest references
and cannot block UART RX, WSS, history, or statistics.

A second task reads raw USB bytes into a fixed 16-block source handoff. It
captures the owner's published generation before each driver read. A block is
rechecked by the sole owner immediately before UART transmission and is valid
only if the complete generation still matches. A read spanning even a complete
`USB → WEB → USB` transition therefore fails closed. A block observed while Web owns TX bypasses the
source queue and contributes only to an atomic pending rejection count; the
owner folds that count into `usb_tx_rejected` even if a Lease transition was
processed first. This prevents bytes sent during Web ownership from becoming
eligible merely because task scheduling delayed their accounting.

UART1 has no software TX ring. The owner advances one pending source block only
by the bytes accepted by the hardware FIFO, so a switch can zero every byte
that has not yet crossed the peripheral boundary. Owner changes never clear a
Router RX sink. Exiting a Session first stops and joins both USB tasks, then
uninstalls the USB and UART drivers on the owner core, zeroes the bridge and
source queues, and restores GPIO17 to input/high-impedance. A bounded join or
uninstall failure preserves the service for a later cleanup retry.

USB disconnect/reconnect is a transport condition, not a new Serial Session;
Session ID, TX Owner, and current-session Router history remain unchanged.
Application bridging starts only after explicit physical entry, so reset-time
ROM output is never forwarded to the target. Suppressing ROM USB output would
require an irreversible policy outside this feature. The project does not burn
USB eFuses and does not add TinyUSB alongside the built-in driver. A future
multi-CDC design requires a separate architecture decision.
