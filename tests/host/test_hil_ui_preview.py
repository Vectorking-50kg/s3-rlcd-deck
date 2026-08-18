#!/usr/bin/env python3

import importlib.util
import json
import pathlib


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
        if value == MODULE.HIL_READY and not self.lines:
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


for page in MODULE.PAGES:
    connection = PreviewConnection(page)
    assert MODULE.show_preview(connection, page, 0.25) == 8
    assert connection.writes[:2] == [MODULE.HIL_READY, MODULE.preview_command(page)]
    assert connection.flushed

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
