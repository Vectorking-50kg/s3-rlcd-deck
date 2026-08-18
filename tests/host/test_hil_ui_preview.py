#!/usr/bin/env python3

import importlib.util
import json
import pathlib
import struct
import zlib


MODULE_PATH = pathlib.Path(__file__).parents[2] / "tools" / "hil_ui_preview.py"
SPEC = importlib.util.spec_from_file_location("hil_ui_preview", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def display_event(frames: int) -> dict[str, object]:
    return {
        "type": "display_progress",
        "width": 400,
        "height": 300,
        "frame_bytes": 15_000,
        "submitted_frames": frames,
        "completed_frames": frames,
        "transfer_timeouts": 0,
        "start_failures": 0,
        "rejected_updates": 0,
    }


class PreviewConnection:
    def __init__(self, page: str, malformed_ack: bool = False) -> None:
        self.page = page
        self.malformed_ack = malformed_ack
        self.lines: list[bytes] = []
        self.writes: list[bytes] = []
        self.timeout = 0.0
        self.flushed = False

    def write(self, value: bytes) -> None:
        self.writes.append(value)
        if value == MODULE.preview_command("clear"):
            self.lines.append(
                json.dumps(
                    {
                        "type": "ui_preview",
                        "page": "live",
                        "active": False,
                        "accepted": True,
                    }
                ).encode()
                + b"\n"
            )
        elif value == MODULE.HIL_READY and not self.lines:
            self.lines.append(json.dumps(display_event(7)).encode() + b"\n")
        elif value == MODULE.preview_command(self.page):
            event = {
                "type": "ui_preview",
                "page": "live" if self.page == "clear" else self.page,
                "active": self.page != "clear",
                "accepted": True,
            }
            if self.malformed_ack:
                event["extra"] = "must fail closed"
            # Exercise the valid race where the physical frame completes before
            # the command acknowledgement reaches the host.
            self.lines.append(json.dumps(display_event(8)).encode() + b"\n")
            self.lines.append(json.dumps(event).encode() + b"\n")

    def flush(self) -> None:
        self.flushed = True

    def readline(self) -> bytes:
        return self.lines.pop(0) if self.lines else b""


class GalleryConnection:
    def __init__(self) -> None:
        self.lines: list[bytes] = []
        self.writes: list[bytes] = []
        self.timeout = 0.0
        self.completed_frames = 10

    def write(self, value: bytes) -> None:
        self.writes.append(value)
        if value == MODULE.preview_command("clear"):
            self.lines.append(
                json.dumps(
                    {
                        "type": "ui_preview",
                        "page": "live",
                        "active": False,
                        "accepted": True,
                    }
                ).encode()
                + b"\n"
            )
        elif value == MODULE.HIL_READY and not self.lines:
            self.lines.append(json.dumps(display_event(self.completed_frames)).encode() + b"\n")
        elif value.startswith(b"DECK_UI_PREVIEW "):
            page = value.decode("ascii").strip().split(" ", 1)[1]
            self.completed_frames += 1
            self.lines.append(json.dumps(display_event(self.completed_frames)).encode() + b"\n")
            self.lines.append(
                json.dumps(
                    {
                        "type": "ui_preview",
                        "page": page,
                        "active": True,
                        "accepted": True,
                    }
                ).encode()
                + b"\n"
            )

    def flush(self) -> None:
        pass

    def readline(self) -> bytes:
        return self.lines.pop(0) if self.lines else b""


class CaptureConnection:
    def __init__(self, frame: bytes, corrupt_crc: bool = False) -> None:
        self.frame = frame
        self.corrupt_crc = corrupt_crc
        self.lines: list[bytes] = []
        self.writes: list[bytes] = []
        self.timeout = 0.0
        self.flushed = False

    def write(self, value: bytes) -> None:
        self.writes.append(value)
        if value != MODULE.CAPTURE_COMMAND:
            return
        checksum = zlib.crc32(self.frame) & 0xFFFFFFFF
        self.lines.append(
            json.dumps(
                {
                    "type": "ui_capture_begin",
                    "width": 400,
                    "height": 300,
                    "frame_bytes": 15_000,
                    "chunk_bytes": 256,
                    "chunks": 59,
                    "crc32": checksum,
                }
            ).encode()
            + b"\n"
        )
        for index in range(59):
            chunk = self.frame[index * 256 : (index + 1) * 256]
            self.lines.append(
                json.dumps(
                    {
                        "type": "ui_capture_chunk",
                        "index": index,
                        "data": chunk.hex(),
                    }
                ).encode()
                + b"\n"
            )
        self.lines.append(
            json.dumps(
                {
                    "type": "ui_capture_end",
                    "chunks": 59,
                    "crc32": checksum ^ (1 if self.corrupt_crc else 0),
                }
            ).encode()
            + b"\n"
        )

    def flush(self) -> None:
        self.flushed = True

    def readline(self) -> bytes:
        return self.lines.pop(0) if self.lines else b""


for page in MODULE.PAGES:
    connection = PreviewConnection(page)
    expected_frame = 7 if page == "clear" else 8
    assert MODULE.show_preview(connection, page, 0.25) == expected_frame
    expected_writes = [MODULE.preview_command("clear"), MODULE.HIL_READY]
    if page != "clear":
        expected_writes.append(MODULE.preview_command(page))
    assert connection.writes == expected_writes
    assert connection.flushed

assert len(MODULE.GALLERY_PAGES) == 17
assert len(set(MODULE.GALLERY_PAGES)) == len(MODULE.GALLERY_PAGES)
assert "clear" not in MODULE.GALLERY_PAGES
gallery_connection = GalleryConnection()
original_sleep = MODULE.time.sleep
gallery_holds: list[float] = []
MODULE.time.sleep = gallery_holds.append
try:
    gallery = MODULE.show_gallery(gallery_connection, 0.25, 4.0)
finally:
    MODULE.time.sleep = original_sleep
assert [page for page, _ in gallery] == list(MODULE.GALLERY_PAGES)
assert len(gallery_holds) == len(MODULE.GALLERY_PAGES)
assert all(hold == 4.0 for hold in gallery_holds)
assert gallery_connection.completed_frames == 10 + len(MODULE.GALLERY_PAGES)

for invalid_hold in (0.0, -1.0, 60.1):
    try:
        MODULE.show_gallery(GalleryConnection(), 0.25, invalid_hold)
    except MODULE.PreviewFailure as error:
        assert "gallery hold" in str(error)
    else:
        raise AssertionError("invalid gallery hold must fail closed")

try:
    MODULE.preview_command("secrets")
except MODULE.PreviewFailure:
    pass
else:
    raise AssertionError("unknown preview page must fail closed")

try:
    MODULE.show_preview(PreviewConnection("board", malformed_ack=True), "board", 0.25)
except MODULE.PreviewFailure as error:
    assert "rejected or malformed" in str(error)
else:
    raise AssertionError("unexpected acknowledgement fields must fail closed")

failed_display = display_event(9)
failed_display["transfer_timeouts"] = 1
assert not MODULE.valid_display_event(failed_display)

white_frame = b"\xff" * MODULE.FRAME_BYTES
capture = CaptureConnection(white_frame)
assert MODULE.capture_preview_frame(capture, 0.5) == white_frame
assert capture.writes == [MODULE.CAPTURE_COMMAND]
assert capture.flushed

png = MODULE.encode_png(white_frame)
assert png.startswith(b"\x89PNG\r\n\x1a\n")
assert struct.unpack(">II", png[16:24]) == (400, 300)
assert MODULE.unpack_pixel(white_frame, 0, 0) == 255
assert MODULE.unpack_pixel(b"\x00" * MODULE.FRAME_BYTES, 399, 299) == 0

try:
    MODULE.capture_preview_frame(CaptureConnection(white_frame, corrupt_crc=True), 0.5)
except MODULE.PreviewFailure as error:
    assert "trailer" in str(error)
else:
    raise AssertionError("capture checksum mismatch must fail closed")

try:
    MODULE.encode_png(b"too short")
except MODULE.PreviewFailure as error:
    assert "wrong size" in str(error)
else:
    raise AssertionError("wrong-sized frames must fail closed")
