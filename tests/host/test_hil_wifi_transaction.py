#!/usr/bin/env python3

import importlib.util
import pathlib


SCRIPT = pathlib.Path(__file__).parents[2] / "tools" / "hil_wifi_transaction.py"
SPEC = importlib.util.spec_from_file_location("hil_wifi_transaction", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


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
