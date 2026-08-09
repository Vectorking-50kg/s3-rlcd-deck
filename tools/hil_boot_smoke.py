#!/usr/bin/env python3

import argparse
import json
import pathlib
import sys
import time
from collections.abc import Iterable
from typing import Any

HIL_READY = b"DECK_HIL_READY\n"


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


def valid_display_event(event: dict[str, Any]) -> bool:
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
    if event.get("type") != "display_ready" or not all(
        isinstance(event.get(name), expected) for name, expected in required.items()
    ):
        return False
    return (
        event["width"] == 400
        and event["height"] == 300
        and event["frame_bytes"] == 15000
        and event["submitted_frames"] >= 1
        and event["completed_frames"] >= 1
        and event["completed_frames"] <= event["submitted_frames"]
        and event["transfer_timeouts"] == 0
        and event["start_failures"] == 0
        and event["rejected_updates"] == 0
    )


def diagnostic_events(
    lines: Iterable[str], expect_display: bool
) -> tuple[dict[str, Any] | None, dict[str, Any] | None]:
    boot = None
    display = None
    for line in lines:
        candidate = line.strip()
        if not candidate.startswith("{"):
            continue
        try:
            event = json.loads(candidate)
        except json.JSONDecodeError:
            continue
        if valid_boot_event(event):
            boot = event
        elif valid_display_event(event):
            display = event
        if boot is not None and (not expect_display or display is not None):
            break
    return boot, display


def boot_event(lines: Iterable[str]) -> dict[str, Any] | None:
    return diagnostic_events(lines, False)[0]


def serial_lines(port: str, timeout_seconds: float) -> Iterable[str]:
    try:
        import serial
    except ImportError as error:
        raise RuntimeError("live HIL requires pyserial from the ESP-IDF environment") from error

    deadline = time.monotonic() + timeout_seconds
    with serial.Serial(port=port, baudrate=115200, timeout=0.25) as connection:
        connection.write(HIL_READY)
        connection.flush()
        while time.monotonic() < deadline:
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
        help="Also require one successful full-frame display completion.",
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
                event, display = diagnostic_events(capture, arguments.expect_display)
        else:
            event, display = diagnostic_events(
                serial_lines(arguments.port, arguments.timeout), arguments.expect_display
            )
    except (OSError, RuntimeError) as error:
        print(f"boot smoke failed: {error}", file=sys.stderr)
        return 2

    if event is None:
        print("boot smoke failed: no valid boot_ok event observed", file=sys.stderr)
        return 1

    print(
        "boot_ok observed: "
        f"version={event['firmware_version']} reset_reason={event['reset_reason']}"
    )
    if arguments.expect_display:
        if display is None:
            print("display smoke failed: no valid display_ready event observed", file=sys.stderr)
            return 1
        print(
            "display_ready observed: "
            f"frames={display['completed_frames']} timeouts={display['transfer_timeouts']}"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
