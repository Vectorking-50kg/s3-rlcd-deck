#!/usr/bin/env python3

import argparse
import json
import pathlib
import sys
import time
from collections.abc import Iterable
from typing import Any

HIL_READY = b"DECK_HIL_READY\n"
HIL_READY_INTERVAL_SECONDS = 0.5


def valid_boot_event(event: dict[str, Any]) -> bool:
    required = {
        "firmware_version": str,
        "reset_reason": str,
        "uptime_ms": int,
        "minimum_free_heap_bytes": int,
    }
    return event.get("type") == "boot_ok" and all(
        isinstance(event.get(name), expected) for name, expected in required.items()
    )


def display_event_has_fields(event: dict[str, Any], event_type: str) -> bool:
    required = {
        "width": int,
        "height": int,
        "frame_bytes": int,
        "submitted_frames": int,
        "completed_frames": int,
        "transfer_timeouts": int,
        "start_failures": int,
        "rejected_updates": int,
    }
    return event.get("type") == event_type and all(
        isinstance(event.get(name), expected) for name, expected in required.items()
    )


def valid_display_event(
    event: dict[str, Any] | None, event_type: str, minimum_frames: int
) -> bool:
    if event is None or not display_event_has_fields(event, event_type):
        return False
    return (
        event["width"] == 400
        and event["height"] == 300
        and event["frame_bytes"] == 15000
        and event["submitted_frames"] >= minimum_frames
        and event["completed_frames"] >= minimum_frames
        and event["completed_frames"] <= event["submitted_frames"]
        and event["transfer_timeouts"] == 0
        and event["start_failures"] == 0
        and event["rejected_updates"] == 0
    )


def diagnostic_events(
    lines: Iterable[str], expect_display: bool
) -> tuple[
    dict[str, Any] | None,
    dict[str, Any] | None,
    dict[str, Any] | None,
    int,
]:
    boot = None
    display = None
    progress = None
    boot_count = 0
    for line in lines:
        candidate = line.strip()
        if not candidate.startswith("{"):
            continue
        try:
            event = json.loads(candidate)
        except json.JSONDecodeError:
            continue
        if valid_boot_event(event):
            if boot is None:
                boot = event
            boot_count += 1
        elif display_event_has_fields(event, "display_ready"):
            display = event
        elif display_event_has_fields(event, "display_progress"):
            progress = event
        if boot is not None and not expect_display:
            break
    return boot, display, progress, boot_count


def boot_event(lines: Iterable[str]) -> dict[str, Any] | None:
    return diagnostic_events(lines, False)[0]


def serial_lines(
    port: str,
    timeout_seconds: float,
    serial_factory: Any | None = None,
    monotonic: Any = time.monotonic,
) -> Iterable[str]:
    if serial_factory is None:
        try:
            import serial
        except ImportError as error:
            raise RuntimeError("live HIL requires pyserial from the ESP-IDF environment") from error
        serial_factory = serial.Serial

    deadline = monotonic() + timeout_seconds
    with serial_factory(port=port, baudrate=115200, timeout=0.25) as connection:
        next_ready = 0.0
        while monotonic() < deadline:
            now = monotonic()
            if now >= next_ready:
                connection.write(HIL_READY)
                connection.flush()
                next_ready = now + HIL_READY_INTERVAL_SECONDS
            raw_line = connection.readline()
            if raw_line:
                yield raw_line.decode("utf-8", errors="replace")


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Observe Deck boot and optional display diagnostics.")
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--input-file", type=pathlib.Path, help="Replay a captured console log.")
    source.add_argument("--port", help="Read a live Deck serial port after flashing.")
    parser.add_argument("--timeout", type=float, default=15.0, help="Live serial timeout in seconds.")
    parser.add_argument(
        "--expect-display",
        action="store_true",
        help="Also require at least three clean full-frame completions and no reset.",
    )
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    if arguments.timeout <= 0:
        print("timeout must be greater than zero", file=sys.stderr)
        return 2

    try:
        if arguments.input_file is not None:
            with arguments.input_file.open(encoding="utf-8", errors="replace") as capture:
                event, display, progress, boot_count = diagnostic_events(
                    capture, arguments.expect_display
                )
        else:
            event, display, progress, boot_count = diagnostic_events(
                serial_lines(arguments.port, arguments.timeout), arguments.expect_display
            )
    except (OSError, RuntimeError) as error:
        print(f"boot smoke failed: {error}", file=sys.stderr)
        return 2

    if event is None:
        print("boot smoke failed: no valid boot_ok event observed", file=sys.stderr)
        return 1
    if boot_count != 1:
        print(
            f"boot smoke failed: observed {boot_count} boot_ok events (unexpected reset)",
            file=sys.stderr,
        )
        return 1

    print(
        "boot_ok observed: "
        f"version={event['firmware_version']} reset_reason={event['reset_reason']}"
    )
    if arguments.expect_display:
        if not valid_display_event(display, "display_ready", 1):
            print("display smoke failed: no valid display_ready event observed", file=sys.stderr)
            return 1
        print(
            "display_ready observed: "
            f"frames={display['completed_frames']} timeouts={display['transfer_timeouts']}"
        )
        if not valid_display_event(progress, "display_progress", 3):
            print(
                "display smoke failed: fewer than three clean completed frames observed",
                file=sys.stderr,
            )
            return 1
        print(
            "display_progress observed: "
            f"frames={progress['completed_frames']} timeouts={progress['transfer_timeouts']}"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
