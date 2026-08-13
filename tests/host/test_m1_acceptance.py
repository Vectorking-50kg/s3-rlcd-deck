#!/usr/bin/env python3

import hashlib
import importlib.util
import json
import pathlib
import tempfile
from unittest import mock


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


def test_evidence_redaction_gate_rejects_every_secret_field() -> None:
    with tempfile.TemporaryDirectory() as directory:
        evidence = pathlib.Path(directory) / "serial.jsonl"
        evidence.write_text('{"line":"safe"}\n', encoding="utf-8")
        assert m1.evidence_is_redacted(evidence)
        for secret in ('"password":"x"', '"token":"x"', '"certificate_der":"x"', '"fingerprint":"x"', '"code":"123456"', 'Authorization: Bearer x'):
            evidence.write_text(secret, encoding="utf-8")
            assert not m1.evidence_is_redacted(evidence), secret


def test_build_identity_is_only_retained_for_an_exact_full_commit() -> None:
    full = "a" * 40
    sanitized, event = m1.sanitize_serial_line(
        json.dumps({"type": "deck_build_identity", "firmware_commit": full})
    )
    assert event == {"type": "deck_build_identity", "firmware_commit": full}
    assert full in sanitized

    sanitized, event = m1.sanitize_serial_line(
        json.dumps({"type": "deck_build_identity", "firmware_commit": "a" * 12})
    )
    assert sanitized == m1.REDACTED_NON_DIAGNOSTIC
    assert event is not None


def test_current_source_and_companion_identity_are_observed_not_assumed() -> None:
    with mock.patch.object(m1, "command_output", return_value=""):
        assert m1.source_tree_clean()
    with mock.patch.object(m1, "command_output", return_value=" M .github/workflows/ci.yml"):
        assert not m1.source_tree_clean()

    full = "b" * 40
    expected = f"s3deck-companion 0.1.0-dev (commit {full})"
    with mock.patch.object(m1.platform, "system", return_value="Darwin"), mock.patch.object(
        m1, "command_output", side_effect=["arm64", expected]
    ):
        executable = m1.companion_for_current_host(full)
    assert executable.name == "s3deck-companion"


def test_native_run_requires_same_commit_and_both_real_platform_jobs() -> None:
    full = "c" * 40
    document = {
        "headSha": full,
        "conclusion": "success",
        "jobs": [
            {"name": "macOS native (arm64)", "conclusion": "success"},
            {"name": "Windows native (amd64)", "conclusion": "success"},
        ],
    }
    with mock.patch.object(m1, "command_output", return_value=json.dumps(document)):
        assert m1.verified_native_run(
            "https://github.com/owner/repo/actions/runs/123", full
        ) == (True, True)
    document["jobs"].pop()
    with mock.patch.object(m1, "command_output", return_value=json.dumps(document)):
        assert m1.verified_native_run(
            "https://github.com/owner/repo/actions/runs/123", full
        ) == (True, False)


def test_companion_logs_are_drained_redacted_and_secret_observation_fails_gate() -> None:
    with tempfile.TemporaryDirectory() as directory:
        output = pathlib.Path(directory) / "companion.jsonl"
        process = m1.CompanionProcess(
            pathlib.Path("companion"), pathlib.Path(directory), "secret-value", output
        )
        process._log = output.open("w", encoding="utf-8")
        stream = __import__("io").BytesIO(b"ordinary line\nsecret-value\n")
        process._drain_output(stream)
        process._log.close()
        assert not process.logs_redacted()
        assert "ordinary line" not in output.read_text(encoding="utf-8")
        assert "secret-value" not in output.read_text(encoding="utf-8")


def test_profile_cleanup_revokes_only_temporary_profile_and_reselects_original() -> None:
    original = {"profile_ids": ["sha256:" + "a" * 64], "active_profile_id": "sha256:" + "a" * 64}
    temporary = "sha256:" + "b" * 64
    snapshots = [
        {"profile_ids": original["profile_ids"] + [temporary], "active_profile_id": temporary},
        original,
    ]
    operations: list[tuple[str, dict[str, str]]] = []

    def fake_form(_base: str, path: str, fields: dict[str, str]) -> int:
        operations.append((path, fields))
        return 202

    with mock.patch.object(m1, "snapshot_companion_profiles", side_effect=snapshots), mock.patch.object(
        m1, "http_form", side_effect=fake_form
    ):
        m1.restore_companion_profiles("http://192.168.4.1", original, 1)
    assert operations == [
        ("/api/companions/revoke", {"profile_id": temporary}),
        ("/api/companions/select", {"profile_id": original["active_profile_id"]}),
    ]


def test_post_flash_monitor_requests_a_fresh_boot_after_ready_handshake() -> None:
    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)
    commands: list[bytes] = []
    reopens: list[tuple[object, str, float]] = []
    evidence.command = commands.append
    evidence.reopen = lambda module, port, timeout: reopens.append((module, port, timeout))
    marker = object()
    evidence.fresh_boot(marker, "/dev/cu.Deck", 12.0)
    assert commands == [m1.HIL_READY, b"DECK_RESTART\n"]
    assert reopens == [(marker, "/dev/cu.Deck", 12.0)]


if __name__ == "__main__":
    test_serial_evidence_keeps_redacted_link_state_and_drops_setup_secret()
    test_summary_passes_only_with_every_real_gate_and_hashes_redacted_log()
    test_evidence_redaction_gate_rejects_every_secret_field()
    test_build_identity_is_only_retained_for_an_exact_full_commit()
    test_current_source_and_companion_identity_are_observed_not_assumed()
    test_native_run_requires_same_commit_and_both_real_platform_jobs()
    test_companion_logs_are_drained_redacted_and_secret_observation_fails_gate()
    test_profile_cleanup_revokes_only_temporary_profile_and_reselects_original()
    test_post_flash_monitor_requests_a_fresh_boot_after_ready_handshake()
    print("M1 acceptance contract passed")
