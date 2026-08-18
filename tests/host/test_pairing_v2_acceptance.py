#!/usr/bin/env python3

import importlib.util
import io
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


def test_expired_window_preserves_partial_evidence_without_claiming_pairing() -> None:
    value = pairing.PairingObservation()
    value.observe({"type": "boot_ok", "minimum_free_heap_bytes": 8000000})
    value.observe({"type": "deck_build_identity", "firmware_commit": "a" * 40})
    value.observe({"type": "pairing_v2", "state": "active"})
    value.observe({"type": "pairing_v2", "state": "expired"})
    checks = value.checks("a" * 40, True, True, True, True, True)
    assert checks["pairing_window_observed"]
    assert not checks["authentication_observed"]
    assert not checks["device_link_proof_observed"]
    assert not checks["transaction_committed"]


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


def test_user_readiness_gate_accepts_only_a_non_secret_enter() -> None:
    output = io.StringIO()
    pairing.wait_for_user_ready(io.StringIO("\n"), output)
    assert "按 Enter" in output.getvalue()
    assert "已授权 Companion 管理页已经打开" in output.getvalue()
    assert "验证码只能输入 Companion 管理网页" in output.getvalue()


def test_user_readiness_gate_fails_closed_without_a_terminal() -> None:
    try:
        pairing.wait_for_user_ready(io.StringIO(""), io.StringIO())
    except pairing.m1.AcceptanceFailure as error:
        assert "readiness confirmation" in str(error)
    else:
        raise AssertionError("missing user readiness input was accepted")


def test_formal_companion_opens_a_one_time_authorized_console() -> None:
    executable = pathlib.Path("/private/verified/s3deck-companion")
    assert pairing.companion_command(executable) == [
        str(executable), "--open-console",
    ]


if __name__ == "__main__":
    test_complete_same_lan_transaction_passes()
    test_online_before_commit_and_setup_mode_fail_closed()
    test_expired_window_preserves_partial_evidence_without_claiming_pairing()
    test_pairing_diagnostic_is_strict_and_secret_free()
    test_user_readiness_gate_accepts_only_a_non_secret_enter()
    test_user_readiness_gate_fails_closed_without_a_terminal()
    test_formal_companion_opens_a_one_time_authorized_console()
