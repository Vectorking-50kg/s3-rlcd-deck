#!/usr/bin/env python3

import importlib.util
import pathlib


MODULE_PATH = pathlib.Path(__file__).parents[2] / "tools" / "hil_recovery_settings.py"
SPEC = importlib.util.spec_from_file_location("hil_recovery_settings", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def setup_event(**overrides):
    event = {
        "type": "setup_state",
        "active": True,
        "reason": "boot_long_press",
        "session_id": 2,
        "ssid": "S3-RLCD-A1B2",
        "address": "192.168.4.1",
        "error_stage": "",
        "wifi_config_state": "active",
        "wifi_record_status": "valid",
        "wifi_candidate_record_status": "empty",
        "wifi_has_active": True,
        "wifi_has_candidate": False,
        "wifi_generation": 3,
        "device_settings_state": "active",
        "device_settings_record_status": "valid",
        "device_settings_candidate_record_status": "empty",
        "device_settings_has_active": True,
        "device_settings_has_candidate": False,
        "device_settings_generation": 4,
        "temperature_offset_tenths_c": -35,
    }
    event.update(overrides)
    return event


def status(**overrides):
    value = {
        "active": True,
        "reason": "boot_long_press",
        "session_id": 2,
        "address": "192.168.4.1",
        "pairing": "m1_not_available",
        "wifi": {
            "state": "active",
            "record": "valid",
            "candidate_record": "empty",
            "has_active": True,
            "has_candidate": False,
            "generation": 3,
            "active_ssid": "Office",
            "candidate_ssid": "",
        },
        "device_settings": {
            "state": "active",
            "record": "valid",
            "candidate_record": "empty",
            "has_active": True,
            "has_candidate": False,
            "generation": 4,
            "temperature_offset_tenths_c": -35,
        },
        "networks": [],
    }
    value.update(overrides)
    return value


assert MODULE.valid_setup_event(setup_event())
assert not MODULE.valid_setup_event(setup_event(password="secret"))
assert not MODULE.valid_setup_event(setup_event(device_settings_state="unknown"))
assert MODULE.valid_http_status(status())
assert not MODULE.valid_http_status(status(device_settings={"state": "active"}))
assert MODULE.peripheral_matches_offset(
    {
        "type": "peripheral_state",
        "sensor_available": True,
        "raw_temperature_tenths_c": 237,
        "calibrated_temperature_tenths_c": 202,
    },
    -35,
)
assert not MODULE.peripheral_matches_offset(
    {
        "type": "peripheral_state",
        "sensor_available": True,
        "raw_temperature_tenths_c": 237,
        "calibrated_temperature_tenths_c": 197,
    },
    -35,
)
assert MODULE.parse_offset("-3.5") == -35
assert MODULE.parse_offset("15") == 150
for invalid in ("15.1", "1.25", "nan", "-15.1"):
    try:
        MODULE.parse_offset(invalid)
    except ValueError:
        pass
    else:
        raise AssertionError(f"expected invalid offset: {invalid}")
