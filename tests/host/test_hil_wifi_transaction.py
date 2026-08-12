#!/usr/bin/env python3

import importlib.util
import json
import pathlib


SCRIPT = pathlib.Path(__file__).parents[2] / "tools" / "hil_wifi_transaction.py"
SPEC = importlib.util.spec_from_file_location("hil_wifi_transaction", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class FakeConnection:
    def __init__(self, events: list[dict[str, object]]) -> None:
        self.lines = [json.dumps(value).encode("utf-8") + b"\n" for value in events]
        self.writes: list[bytes] = []

    def write(self, value: bytes) -> None:
        self.writes.append(value)

    def readline(self) -> bytes:
        return self.lines.pop(0) if self.lines else b""


class StartupRaceConnection:
    def __init__(self) -> None:
        self.lines: list[bytes] = []
        self.writes: list[bytes] = []

    def write(self, value: bytes) -> None:
        self.writes.append(value)
        if value == MODULE.HIL_READY and not self.lines:
            self.lines.append(b'{"type":"boot_ok"}\n')
        elif value == b"DECK_SETUP\n":
            self.lines.append(
                json.dumps(event(active=True, reason="boot_long_press", session_id=3))
                .encode("utf-8")
                + b"\n"
            )

    def readline(self) -> bytes:
        return self.lines.pop(0) if self.lines else b""


def event(**overrides: object) -> dict[str, object]:
    value: dict[str, object] = {
        "type": "setup_state",
        "active": True,
        "reason": "no_wifi_config",
        "session_id": 1,
        "ssid": "S3-RLCD-A1B2",
        "address": "192.168.4.1",
        "error_stage": "",
        "wifi_config_state": "validating",
        "wifi_record_status": "valid",
        "wifi_candidate_record_status": "valid",
        "wifi_has_active": False,
        "wifi_has_candidate": True,
        "wifi_generation": 0,
    }
    value.update(overrides)
    return value


assert MODULE.valid_wifi_transaction_event(event())
assert MODULE.valid_wifi_transaction_event(
    event(active=False, reason="none", ssid="", wifi_config_state="active",
          wifi_has_active=True, wifi_generation=2)
)
assert not MODULE.valid_wifi_transaction_event(event(password="must-not-appear"))
assert not MODULE.valid_wifi_transaction_event(event(active_ssid="must-not-appear"))
assert not MODULE.valid_wifi_transaction_event(event(wifi_generation=-1))
assert not MODULE.valid_wifi_transaction_event(event(wifi_config_state="unknown"))


connection = FakeConnection(
    [
        event(active=False, reason="none", ssid="", wifi_config_state="no_active"),
        event(active=True, reason="boot_long_press", session_id=2),
    ]
)
active = MODULE.enter_setup_session(connection, 0.1)
assert connection.writes[0] == MODULE.HIL_READY
assert b"DECK_SETUP\n" in connection.writes
assert active["active"] is True
assert active["session_id"] == 2

startup_race = StartupRaceConnection()
active_after_retry = MODULE.enter_setup_session(startup_race, 1.0)
assert startup_race.writes[:2] == [MODULE.HIL_READY, b"DECK_SETUP\n"]
assert active_after_retry["session_id"] == 3
