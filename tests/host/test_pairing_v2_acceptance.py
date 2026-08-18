#!/usr/bin/env python3

import importlib.util
import json
import pathlib


ROOT = pathlib.Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "pairing_v2_acceptance", ROOT / "tools/pairing_v2_acceptance.py"
)
assert SPEC and SPEC.loader
pairing = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(pairing)


def complete_observation() -> pairing.PairingObservation:
    value = pairing.PairingObservation()
    value.observe({
        "type": "boot_ok", "minimum_free_heap_bytes": 8000000,
    })
    value.observe({
        "type": "deck_build_identity", "firmware_commit": "a" * 40,
    })
    for state in pairing.PAIRING_STATES:
        value.observe({"type": "pairing_v2", "state": state})
    value.observe({"type": "companion_link_state", "state": "online"})
    return value


def test_complete_same_lan_transaction_passes() -> None:
    checks = complete_observation().checks("a" * 40, True, True, True, True, True)
    assert all(checks.values()), checks


def test_online_before_commit_and_setup_mode_fail_closed() -> None:
    value = pairing.PairingObservation()
    value.observe({"type": "boot_ok", "minimum_free_heap_bytes": 8000000})
    value.observe({"type": "deck_build_identity", "firmware_commit": "a" * 40})
    value.observe({"type": "companion_link_state", "state": "online"})
    for state in pairing.PAIRING_STATES:
        value.observe({"type": "pairing_v2", "state": state})
    value.observe({"type": "setup_state", "active": True})
    checks = value.checks("a" * 40, True, True, True, True, True)
    assert checks["transaction_committed"]
    assert not checks["device_link_online"]
    assert not checks["setup_mode_not_entered"]


def test_pairing_diagnostic_is_strict_and_secret_free() -> None:
    safe = (
        '{"type":"pairing_v2","state":"proof_verified",'
        '"remaining_seconds":60,"proof_count":1,"error_stage":""}\n'
    )
    sanitized, event = pairing.m1.sanitize_serial_line(safe)
    assert event is not None and event["state"] == "proof_verified"
    assert json.loads(sanitized) == json.loads(safe)
    unsafe = safe.replace('"error_stage":""', '"error_stage":"bad-code"')
    sanitized, _ = pairing.m1.sanitize_serial_line(unsafe)
    assert sanitized == pairing.m1.REDACTED_NON_DIAGNOSTIC


if __name__ == "__main__":
    test_complete_same_lan_transaction_passes()
    test_online_before_commit_and_setup_mode_fail_closed()
    test_pairing_diagnostic_is_strict_and_secret_free()
