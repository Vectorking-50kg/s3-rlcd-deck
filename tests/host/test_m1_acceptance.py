#!/usr/bin/env python3

import hashlib
import importlib.util
import json
import pathlib
import tempfile


ROOT = pathlib.Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("m1_acceptance", ROOT / "tools/m1_acceptance.py")
assert SPEC and SPEC.loader
m1 = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(m1)


def test_serial_evidence_keeps_redacted_link_state_and_drops_setup_secret() -> None:
    safe = json.dumps(
        {
            "type": "companion_link_state",
            "state": "online",
            "has_active_profile": True,
            "profile_generation": 2,
            "reconnect_attempts": 0,
            "error_count": 1,
            "last_heartbeat_monotonic_ms": 1234,
        }
    )
    sanitized, event = m1.sanitize_serial_line(safe)
    assert event["state"] == "online"
    assert "token" not in sanitized.lower()
    assert "fingerprint" not in sanitized.lower()

    secret = '{"type":"hil_setup_access","ssid":"Deck","password":"secret","address":"192.168.4.1"}'
    sanitized, event = m1.sanitize_serial_line(secret)
    assert sanitized == m1.REDACTED_SETUP_ACCESS
    assert event is not None
    assert "secret" not in sanitized


def test_summary_passes_only_with_every_real_gate_and_hashes_redacted_log() -> None:
    checks = {name: True for name in m1.REQUIRED_CHECKS}
    with tempfile.TemporaryDirectory() as directory:
        log = pathlib.Path(directory) / "serial.jsonl"
        log.write_text('{"line":"safe"}\n', encoding="utf-8")
        summary = m1.build_summary(
            firmware_commit="a" * 40,
            companion_commit="a" * 40,
            started_at="2026-08-13T01:00:00Z",
            ended_at="2026-08-13T01:05:00Z",
            checks=checks,
            reconnect_count=2,
            deck_error_count=1,
            companion_error_count=3,
            serial_log=log,
            source_dirty=False,
        )
    assert summary["status"] == "passed"
    assert summary["raw_log_sha256"] == hashlib.sha256(b'{"line":"safe"}\n').hexdigest()
    assert "token" not in json.dumps(summary).lower()

    checks["wrong_certificate_rejected"] = False
    failed = m1.build_summary(
        firmware_commit="a" * 40,
        companion_commit="a" * 40,
        started_at="2026-08-13T01:00:00Z",
        ended_at="2026-08-13T01:05:00Z",
        checks=checks,
        reconnect_count=2,
        deck_error_count=1,
        companion_error_count=3,
        serial_log=None,
        source_dirty=False,
    )
    assert failed["status"] == "failed"
    assert failed["failures"] == ["wrong_certificate_rejected"]


if __name__ == "__main__":
    test_serial_evidence_keeps_redacted_link_state_and_drops_setup_secret()
    test_summary_passes_only_with_every_real_gate_and_hashes_redacted_log()
    print("M1 acceptance contract passed")
