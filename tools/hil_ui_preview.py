#!/usr/bin/env python3

import argparse
import json
import sys
import time
from typing import Any


HIL_READY = b"DECK_HIL_READY\n"
PAGES = (
    "board",
    "pairing",
    "setup",
    "ai",
    "provider",
    "configuration",
    "serial",
    "offline",
    "error",
    "clear",
)
FATAL_MARKERS = (
    "task_wdt:",
    "guru meditation error",
    "panic'ed",
    "assert failed",
)


class PreviewFailure(RuntimeError):
    pass


def preview_command(page: str) -> bytes:
    if page not in PAGES:
        raise PreviewFailure("unknown UI preview page")
    if page == "clear":
        return b"DECK_UI_PREVIEW clear\n"
    return f"DECK_UI_PREVIEW {page}\n".encode("ascii")


def valid_preview_event(event: dict[str, Any], page: str) -> bool:
    if set(event) != {"type", "page", "active", "accepted"}:
        return False
    expected_page = "live" if page == "clear" else page
    return (
        event.get("type") == "ui_preview"
        and event.get("page") == expected_page
        and type(event.get("active")) is bool
        and event["active"] is (page != "clear")
        and type(event.get("accepted")) is bool
        and event["accepted"] is True
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
    return (
        event.get("type") in {"display_ready", "display_progress"}
        and all(type(event.get(name)) is expected for name, expected in required.items())
        and event["width"] == 400
        and event["height"] == 300
        and event["frame_bytes"] == 15_000
        and event["completed_frames"] <= event["submitted_frames"]
        and event["transfer_timeouts"] == 0
        and event["start_failures"] == 0
        and event["rejected_updates"] == 0
    )


def read_json_event(connection: Any, deadline: float) -> dict[str, Any] | None:
    connection.timeout = min(0.25, max(0.01, deadline - time.monotonic()))
    raw_line = connection.readline()
    if not raw_line:
        return None
    line = raw_line.decode("utf-8", errors="replace")
    if any(marker in line.lower() for marker in FATAL_MARKERS):
        raise PreviewFailure("fatal Deck log observed")
    candidate = line.strip()
    if not candidate.startswith("{"):
        return None
    try:
        event = json.loads(candidate)
    except json.JSONDecodeError:
        return None
    if not isinstance(event, dict):
        return None
    if event.get("type") in {"display_ready", "display_progress"} and not valid_display_event(event):
        raise PreviewFailure("invalid or failed display diagnostic event")
    return event


def wait_for_display_baseline(connection: Any, timeout_seconds: float) -> int:
    deadline = time.monotonic() + timeout_seconds
    next_ready = 0.0
    while time.monotonic() < deadline:
        now = time.monotonic()
        if now >= next_ready:
            connection.write(HIL_READY)
            next_ready = now + 0.5
        event = read_json_event(connection, deadline)
        if event is not None and valid_display_event(event):
            return event["completed_frames"]
    raise PreviewFailure("timed out waiting for a clean display baseline")


def show_preview(connection: Any, page: str, timeout_seconds: float) -> int:
    if timeout_seconds <= 0:
        raise PreviewFailure("timeout must be greater than zero")
    baseline = wait_for_display_baseline(connection, timeout_seconds)
    connection.write(preview_command(page))
    connection.flush()

    deadline = time.monotonic() + timeout_seconds
    acknowledged = False
    completed_frames: int | None = None
    while time.monotonic() < deadline:
        event = read_json_event(connection, deadline)
        if event is None:
            continue
        if event.get("type") == "ui_preview":
            if not valid_preview_event(event, page):
                raise PreviewFailure("Deck rejected or malformed the UI preview command")
            acknowledged = True
        elif valid_display_event(event) and event["completed_frames"] > baseline:
            completed_frames = event["completed_frames"]
        if acknowledged and completed_frames is not None:
            return completed_frames
    if not acknowledged:
        raise PreviewFailure("timed out waiting for UI preview acknowledgement")
    raise PreviewFailure("UI preview was accepted but no completed display frame followed")


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Freeze a development Deck on one deterministic UI page for visual review."
    )
    parser.add_argument("--port", required=True, help="Deck USB serial port")
    parser.add_argument("--page", required=True, choices=PAGES)
    parser.add_argument("--timeout", type=float, default=12.0)
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    try:
        import serial
    except ImportError:
        print("UI preview failed: pyserial is required", file=sys.stderr)
        return 2
    try:
        with serial.Serial(
            port=arguments.port,
            baudrate=115200,
            timeout=0.25,
            write_timeout=0.25,
        ) as connection:
            completed_frames = show_preview(connection, arguments.page, arguments.timeout)
    except (OSError, serial.SerialException, PreviewFailure) as error:
        print(f"UI preview failed: {error}", file=sys.stderr)
        return 1
    print(
        f"UI preview ready: page={arguments.page} completed_frames={completed_frames}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
