#!/usr/bin/env python3

import argparse
import json
import pathlib
import re
import struct
import sys
import time
import zlib
from typing import Any


HIL_READY = b"DECK_HIL_READY\n"
CAPTURE_COMMAND = b"DECK_UI_CAPTURE\n"
DISPLAY_WIDTH = 400
DISPLAY_HEIGHT = 300
FRAME_BYTES = 15_000
CAPTURE_CHUNK_BYTES = 256
CAPTURE_CHUNKS = (FRAME_BYTES + CAPTURE_CHUNK_BYTES - 1) // CAPTURE_CHUNK_BYTES
HEX_BYTES = re.compile(r"[0-9a-f]*")
PAGES = (
    "board",
    "pairing",
    "pairing-authenticating",
    "pairing-verified",
    "pairing-success",
    "pairing-expired",
    "pairing-error",
    "setup",
    "setup-validating",
    "setup-error",
    "ai",
    "ai-stale",
    "provider",
    "configuration",
    "serial",
    "offline",
    "error",
    "clear",
)
GALLERY_PAGES = tuple(page for page in PAGES if page != "clear")
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


def wait_for_preview_ack(connection: Any, page: str, timeout_seconds: float) -> None:
    deadline = time.monotonic() + timeout_seconds
    while time.monotonic() < deadline:
        event = read_json_event(connection, deadline)
        if event is None or event.get("type") != "ui_preview":
            continue
        if not valid_preview_event(event, page):
            raise PreviewFailure("Deck rejected or malformed the UI preview command")
        return
    raise PreviewFailure("timed out waiting for UI preview acknowledgement")


def show_preview(connection: Any, page: str, timeout_seconds: float) -> int:
    if timeout_seconds <= 0:
        raise PreviewFailure("timeout must be greater than zero")

    # A deterministic preview is intentionally static. Restore the live owner
    # first so a second invocation can observe a fresh physical completion and
    # prove that the requested page, rather than the prior frozen page, reached
    # the panel.
    connection.write(preview_command("clear"))
    connection.flush()
    wait_for_preview_ack(connection, "clear", timeout_seconds)
    baseline = wait_for_display_baseline(connection, timeout_seconds)
    if page == "clear":
        return baseline

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


def show_gallery(
    connection: Any,
    timeout_seconds: float,
    hold_seconds: float,
) -> list[tuple[str, int]]:
    if hold_seconds <= 0 or hold_seconds > 60:
        raise PreviewFailure("gallery hold must be greater than zero and at most 60 seconds")
    completed: list[tuple[str, int]] = []
    for page in GALLERY_PAGES:
        completed_frames = show_preview(connection, page, timeout_seconds)
        completed.append((page, completed_frames))
        print(
            f"UI gallery page ready: {len(completed)}/{len(GALLERY_PAGES)} "
            f"page={page} completed_frames={completed_frames}",
            flush=True,
        )
        time.sleep(hold_seconds)
    return completed


def valid_capture_begin(event: dict[str, Any]) -> bool:
    return (
        set(event)
        == {
            "type",
            "width",
            "height",
            "frame_bytes",
            "chunk_bytes",
            "chunks",
            "crc32",
        }
        and event.get("type") == "ui_capture_begin"
        and type(event.get("width")) is int
        and event["width"] == DISPLAY_WIDTH
        and type(event.get("height")) is int
        and event["height"] == DISPLAY_HEIGHT
        and type(event.get("frame_bytes")) is int
        and event["frame_bytes"] == FRAME_BYTES
        and type(event.get("chunk_bytes")) is int
        and event["chunk_bytes"] == CAPTURE_CHUNK_BYTES
        and type(event.get("chunks")) is int
        and event["chunks"] == CAPTURE_CHUNKS
        and type(event.get("crc32")) is int
        and 0 <= event["crc32"] <= 0xFFFFFFFF
    )


def capture_preview_frame(connection: Any, timeout_seconds: float) -> bytes:
    if timeout_seconds <= 0:
        raise PreviewFailure("timeout must be greater than zero")
    connection.write(CAPTURE_COMMAND)
    connection.flush()
    deadline = time.monotonic() + timeout_seconds
    expected_crc: int | None = None
    chunks: list[bytes] = []
    while time.monotonic() < deadline:
        event = read_json_event(connection, deadline)
        if event is None:
            continue
        event_type = event.get("type")
        if event_type == "ui_capture_error":
            if set(event) != {"type", "reason"} or event.get("reason") not in {
                "allocation_failed",
                "preview_unavailable",
                "encoding_failed",
            }:
                raise PreviewFailure("malformed UI capture error")
            raise PreviewFailure(f"Deck UI capture failed: {event['reason']}")
        if event_type == "ui_capture_begin":
            if expected_crc is not None or not valid_capture_begin(event):
                raise PreviewFailure("malformed or duplicate UI capture header")
            expected_crc = event["crc32"]
            continue
        if event_type == "ui_capture_chunk":
            if expected_crc is None or set(event) != {"type", "index", "data"}:
                raise PreviewFailure("UI capture chunk arrived before a valid header")
            index = event.get("index")
            encoded = event.get("data")
            expected_size = min(
                CAPTURE_CHUNK_BYTES,
                FRAME_BYTES - len(chunks) * CAPTURE_CHUNK_BYTES,
            )
            if (
                type(index) is not int
                or index != len(chunks)
                or not isinstance(encoded, str)
                or len(encoded) != expected_size * 2
                or HEX_BYTES.fullmatch(encoded) is None
            ):
                raise PreviewFailure("malformed or out-of-order UI capture chunk")
            chunks.append(bytes.fromhex(encoded))
            continue
        if event_type == "ui_capture_end":
            if (
                expected_crc is None
                or set(event) != {"type", "chunks", "crc32"}
                or type(event.get("chunks")) is not int
                or event["chunks"] != CAPTURE_CHUNKS
                or type(event.get("crc32")) is not int
                or event["crc32"] != expected_crc
                or len(chunks) != CAPTURE_CHUNKS
            ):
                raise PreviewFailure("malformed or premature UI capture trailer")
            frame = b"".join(chunks)
            if len(frame) != FRAME_BYTES or zlib.crc32(frame) & 0xFFFFFFFF != expected_crc:
                raise PreviewFailure("UI capture checksum mismatch")
            return frame
        if isinstance(event_type, str) and event_type.startswith("ui_capture_"):
            raise PreviewFailure("unknown UI capture event")
    raise PreviewFailure("timed out waiting for a complete UI capture")


def unpack_pixel(frame: bytes, x: int, y: int) -> int:
    inverted_y = DISPLAY_HEIGHT - 1 - y
    frame_index = (x // 2) * (DISPLAY_HEIGHT // 4) + inverted_y // 4
    bit = 7 - ((inverted_y % 4) * 2 + x % 2)
    return 255 if frame[frame_index] & (1 << bit) else 0


def png_chunk(kind: bytes, payload: bytes) -> bytes:
    return (
        struct.pack(">I", len(payload))
        + kind
        + payload
        + struct.pack(">I", zlib.crc32(kind + payload) & 0xFFFFFFFF)
    )


def encode_png(frame: bytes) -> bytes:
    if len(frame) != FRAME_BYTES:
        raise PreviewFailure("captured frame has the wrong size")
    rows = bytearray()
    for y in range(DISPLAY_HEIGHT):
        rows.append(0)
        rows.extend(unpack_pixel(frame, x, y) for x in range(DISPLAY_WIDTH))
    header = struct.pack(">IIBBBBB", DISPLAY_WIDTH, DISPLAY_HEIGHT, 8, 0, 0, 0, 0)
    return (
        b"\x89PNG\r\n\x1a\n"
        + png_chunk(b"IHDR", header)
        + png_chunk(b"IDAT", zlib.compress(bytes(rows), level=9))
        + png_chunk(b"IEND", b"")
    )


def write_png(path: pathlib.Path, frame: bytes, overwrite: bool) -> None:
    if path.suffix.lower() != ".png":
        raise PreviewFailure("capture output must use a .png extension")
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("wb" if overwrite else "xb") as output:
        output.write(encode_png(frame))


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Freeze or cycle deterministic development Deck UI pages for visual review."
    )
    parser.add_argument("--port", required=True, help="Deck USB serial port")
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--page", choices=PAGES)
    mode.add_argument(
        "--gallery",
        action="store_true",
        help="Cycle every deterministic page once in the physical acceptance order",
    )
    parser.add_argument("--timeout", type=float, default=12.0)
    parser.add_argument(
        "--hold-seconds",
        type=float,
        default=4.0,
        help="Seconds to hold each --gallery page on the physical panel (default: 4)",
    )
    parser.add_argument(
        "--output",
        type=pathlib.Path,
        help="Capture the deterministic panel framebuffer as a PNG after rendering",
    )
    parser.add_argument(
        "--overwrite",
        action="store_true",
        help="Replace an existing --output file",
    )
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    if arguments.output is not None and (arguments.gallery or arguments.page == "clear"):
        print("UI preview failed: gallery/live UI capture is forbidden", file=sys.stderr)
        return 2
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
            if arguments.gallery:
                gallery = show_gallery(
                    connection,
                    arguments.timeout,
                    arguments.hold_seconds,
                )
                completed_frames = gallery[-1][1]
            else:
                completed_frames = show_preview(connection, arguments.page, arguments.timeout)
                if arguments.output is not None:
                    frame = capture_preview_frame(connection, arguments.timeout)
                    write_png(arguments.output, frame, arguments.overwrite)
    except (OSError, serial.SerialException, PreviewFailure) as error:
        print(f"UI preview failed: {error}", file=sys.stderr)
        return 1
    if arguments.gallery:
        print(
            f"UI gallery complete: pages={len(GALLERY_PAGES)} "
            f"completed_frames={completed_frames}"
        )
    else:
        print(
            f"UI preview ready: page={arguments.page} completed_frames={completed_frames}"
            + (f" output={arguments.output}" if arguments.output is not None else "")
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
