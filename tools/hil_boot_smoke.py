#!/usr/bin/env python3

import argparse
import json
import pathlib
import sys
import time
from collections.abc import Iterable
from typing import Any

HIL_READY = b"DECK_HIL_READY\n"


def boot_event(lines: Iterable[str]) -> dict[str, Any] | None:
    for line in lines:
        candidate = line.strip()
        if not candidate.startswith("{"):
            continue
        try:
            event = json.loads(candidate)
        except json.JSONDecodeError:
            continue
        if event.get("type") != "boot_ok":
            continue
        required = {
            "firmware_version": str,
            "reset_reason": str,
            "uptime_ms": int,
            "minimum_free_heap_bytes": int,
        }
        if all(isinstance(event.get(name), expected) for name, expected in required.items()):
            return event
    return None


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
    parser = argparse.ArgumentParser(description="Observe one Deck boot_ok diagnostic event.")
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--input-file", type=pathlib.Path, help="Replay a captured console log.")
    source.add_argument("--port", help="Read a live Deck serial port after flashing.")
    parser.add_argument("--timeout", type=float, default=15.0, help="Live serial timeout in seconds.")
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    if arguments.timeout <= 0:
        print("timeout must be greater than zero", file=sys.stderr)
        return 2

    try:
        if arguments.input_file is not None:
            with arguments.input_file.open(encoding="utf-8", errors="replace") as capture:
                event = boot_event(capture)
        else:
            event = boot_event(serial_lines(arguments.port, arguments.timeout))
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
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
