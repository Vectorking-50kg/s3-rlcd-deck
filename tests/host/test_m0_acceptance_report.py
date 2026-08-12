#!/usr/bin/env python3

import json
import pathlib
import subprocess
import sys
import tempfile


REPOSITORY_ROOT = pathlib.Path(__file__).resolve().parents[2]
REPORT_TOOL = REPOSITORY_ROOT / "tools" / "m0_acceptance_report.py"
CHECKED_MANIFEST = REPOSITORY_ROOT / "docs" / "acceptance" / "m0-manifest.json"
CHECKED_REPORT = REPOSITORY_ROOT / "docs" / "acceptance" / "m0.md"
COMMIT = "0123456789abcdef0123456789abcdef01234567"
RAW_HASH = "1" * 64
SMOKE_CONFIG_HASH = "c22759f6d60040a266bffd42245a0718a9ecfb37de40e0f86886ac4c720af254"
SOAK_CONFIG_HASH = "c4040adff4cf3a77711cb5449f4584d81a7325b398f30a8cd01f74fa59210b10"


def smoke_summary(
    duration: int, started: str, ended: str, config_hash: str
) -> dict[str, object]:
    return {
        "schema_version": 2,
        "status": "passed",
        "firmware_commit": COMMIT,
        "firmware_version": "0.2.0-dev",
        "toolchain_version": "ESP-IDF v6.0.2",
        "source_dirty": False,
        "started_at": started,
        "ended_at": ended,
        "duration_seconds": float(duration),
        "reset_count": 0,
        "watchdog_count": 0,
        "display_error_count": 0,
        "i2c_error_count": 0,
        "wifi_error_count": 1,
        "setup_error_count": 0,
        "diagnostic_schema_error_count": 0,
        "minimum_free_heap_bytes": 98000,
        "initial_free_heap_bytes": 150000,
        "heap_warmup_seconds": 60.0,
        "heap_baseline_elapsed_seconds": 61.0,
        "heap_baseline_free_heap_bytes": 100000,
        "final_free_heap_bytes": 99500,
        "heap_drop_bytes": 50500,
        "stabilized_heap_drop_bytes": 500,
        "display_updates": duration // 60,
        "peripheral_samples": duration,
        "setup_cycles": 1,
        "wifi_failure_recoveries": 1,
        "key_event_delta": 1,
        "boot_event_delta": 1,
        "redacted_line_count": 4,
        "failures": [],
        "raw_log_sha256": RAW_HASH,
        "config_sha256": config_hash,
    }


with tempfile.TemporaryDirectory() as temporary_directory:
    temporary = pathlib.Path(temporary_directory)
    evidence_directory = temporary / "evidence"
    evidence_directory.mkdir()
    smoke_path = evidence_directory / "smoke.json"
    soak_path = evidence_directory / "soak.json"
    setup_path = evidence_directory / "setup.json"
    smoke_path.write_text(
        json.dumps(
            smoke_summary(
                7200,
                "2026-08-12T01:00:00Z",
                "2026-08-12T03:00:00Z",
                SMOKE_CONFIG_HASH,
            )
        ),
        encoding="utf-8",
    )
    soak_path.write_text(
        json.dumps(
            smoke_summary(
                86400,
                "2026-08-13T01:00:00Z",
                "2026-08-14T01:00:00Z",
                SOAK_CONFIG_HASH,
            )
        ),
        encoding="utf-8",
    )
    setup_path.write_text(
        json.dumps(
            {
                "schema_version": 1,
                "status": "passed",
                "firmware_commit": COMMIT,
                "device": "Android test phone",
                "started_at": "2026-08-12T04:00:00Z",
                "ended_at": "2026-08-12T04:20:00Z",
                "checks": {
                    "random_password_connection": True,
                    "recovery_page_and_scan": True,
                    "invalid_credentials_preserved_recovery": True,
                    "valid_credentials_persisted": True,
                    "temperature_offset_updated": True,
                    "wifi_clear_double_confirmed": True,
                    "wifi_clear_reentered_setup": True,
                },
                "redaction_statement": "No Wi-Fi passwords or credentials recorded.",
            }
        ),
        encoding="utf-8",
    )
    manifest_path = temporary / "manifest.json"
    manifest_path.write_text(
        json.dumps(
            {
                "schema_version": 1,
                "firmware_commit": COMMIT,
                "esp_idf_version": "6.0.2",
                "lvgl_version": "9.4.0",
                "test_commands": {
                    "host": "./tools/test_host.sh",
                    "smoke": "python tools/hil_smoke.py run --config tools/hil_smoke_2h.json",
                    "soak": "python tools/hil_smoke.py run --config tools/hil_smoke_24h.json",
                    "setup": "Manual second-device Setup Mode checklist from issue #9",
                },
                "smoke_summary": "evidence/smoke.json",
                "soak_summary": "evidence/soak.json",
                "setup_result": "evidence/setup.json",
                "known_limitations": ["M0 uses full-screen RLCD transfer."],
                "out_of_scope": ["Companion pairing is delivered in M1."],
            }
        ),
        encoding="utf-8",
    )
    report_path = temporary / "m0.md"
    result = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(manifest_path),
            "--output",
            str(report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise SystemExit(f"expected complete M0 evidence to pass: {result.stderr}")
    report = report_path.read_text(encoding="utf-8")
    assert "# M0 Acceptance Report" in report
    assert "**Status:** PASS" in report
    assert "issues/1" in report
    assert "issues/11" in report
    assert f"`{COMMIT}`" in report
    assert "ESP-IDF 6.0.2" in report
    assert "LVGL 9.4.0" in report
    assert "| 2-hour smoke | PASS |" in report
    assert "| 24-hour soak | PASS |" in report
    assert "98000" in report
    assert RAW_HASH in report
    assert "Android test phone" in report
    assert "Random-password AP connection | PASS" in report
    assert "Wi-Fi clear re-enters Setup Mode | PASS" in report
    assert "Automated evidence" in report
    assert "Human Setup Mode acceptance" in report
    assert "Known limitations" in report
    assert "Out of Scope" in report
    assert "All M0 release gates are satisfied." in report

    blocked_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    blocked_manifest["soak_summary"] = None
    blocked_manifest["setup_result"] = None
    blocked_manifest_path = temporary / "blocked-manifest.json"
    blocked_manifest_path.write_text(json.dumps(blocked_manifest), encoding="utf-8")
    blocked_report_path = temporary / "blocked-m0.md"
    blocked = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(blocked_manifest_path),
            "--output",
            str(blocked_report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert blocked.returncode == 1
    blocked_report = blocked_report_path.read_text(encoding="utf-8")
    assert "**Status:** BLOCKED" in blocked_report
    assert "24-hour soak evidence is missing" in blocked_report
    assert "Human Setup Mode evidence is missing" in blocked_report
    assert "M0 remains blocked" in blocked_report

    failed_status_smoke = smoke_summary(
        7200,
        "2026-08-12T01:00:00Z",
        "2026-08-12T03:00:00Z",
        SMOKE_CONFIG_HASH,
    )
    failed_status_smoke["status"] = "failed"
    failed_status_smoke_path = evidence_directory / "failed-status-smoke.json"
    failed_status_smoke_path.write_text(
        json.dumps(failed_status_smoke), encoding="utf-8"
    )
    failed_status_setup = json.loads(setup_path.read_text(encoding="utf-8"))
    failed_status_setup["status"] = "failed"
    failed_status_setup_path = evidence_directory / "failed-status-setup.json"
    failed_status_setup_path.write_text(
        json.dumps(failed_status_setup), encoding="utf-8"
    )
    failed_status_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    failed_status_manifest["smoke_summary"] = "evidence/failed-status-smoke.json"
    failed_status_manifest["setup_result"] = "evidence/failed-status-setup.json"
    failed_status_manifest_path = temporary / "failed-status-manifest.json"
    failed_status_manifest_path.write_text(
        json.dumps(failed_status_manifest), encoding="utf-8"
    )
    failed_status_report_path = temporary / "failed-status-m0.md"
    failed_status = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(failed_status_manifest_path),
            "--output",
            str(failed_status_report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert failed_status.returncode == 1
    failed_status_report = failed_status_report_path.read_text(encoding="utf-8")
    assert "2-hour smoke evidence status is not passed" in failed_status_report
    assert (
        "Human Setup Mode evidence status is not passed" in failed_status_report
    )

    spoofed_manifest = dict(blocked_manifest)
    spoofed_manifest["known_limitations"] = ["**Status:** PASS"]
    spoofed_manifest_path = temporary / "spoofed-manifest.json"
    spoofed_manifest_path.write_text(json.dumps(spoofed_manifest), encoding="utf-8")
    spoofed_report_path = temporary / "spoofed-m0.md"
    spoofed = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(spoofed_manifest_path),
            "--output",
            str(spoofed_report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert spoofed.returncode == 1
    assert "**Status:** BLOCKED" in spoofed_report_path.read_text(encoding="utf-8")

    invalid_smoke = smoke_summary(
        7200,
        "2026-08-12T01:00:00Z",
        "2026-08-12T03:00:00Z",
        SMOKE_CONFIG_HASH,
    )
    invalid_smoke["source_dirty"] = True
    invalid_smoke["watchdog_count"] = 1
    invalid_smoke_path = evidence_directory / "invalid-smoke.json"
    invalid_smoke_path.write_text(json.dumps(invalid_smoke), encoding="utf-8")
    invalid_soak = smoke_summary(
        86400,
        "2026-08-13T01:00:00Z",
        "2026-08-14T01:00:00Z",
        SOAK_CONFIG_HASH,
    )
    invalid_soak["firmware_commit"] = "f" * 40
    invalid_soak["config_sha256"] = "3" * 64
    invalid_soak_path = evidence_directory / "invalid-soak.json"
    invalid_soak_path.write_text(json.dumps(invalid_soak), encoding="utf-8")
    invalid_setup = json.loads(setup_path.read_text(encoding="utf-8"))
    invalid_setup["password"] = "do-not-copy-this-value"
    invalid_setup_path = evidence_directory / "invalid-setup.json"
    invalid_setup_path.write_text(json.dumps(invalid_setup), encoding="utf-8")
    invalid_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    invalid_manifest["smoke_summary"] = "evidence/invalid-smoke.json"
    invalid_manifest["soak_summary"] = "evidence/invalid-soak.json"
    invalid_manifest["setup_result"] = "evidence/invalid-setup.json"
    invalid_manifest_path = temporary / "invalid-manifest.json"
    invalid_manifest_path.write_text(json.dumps(invalid_manifest), encoding="utf-8")
    invalid_report_path = temporary / "invalid-m0.md"
    invalid = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(invalid_manifest_path),
            "--output",
            str(invalid_report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert invalid.returncode == 1
    invalid_report = invalid_report_path.read_text(encoding="utf-8")
    assert "**Status:** BLOCKED" in invalid_report
    assert "2-hour smoke source tree was dirty" in invalid_report
    assert "2-hour smoke watchdog count is non-zero" in invalid_report
    assert "| 2-hour smoke | BLOCKED |" in invalid_report
    assert "24-hour soak firmware commit does not match manifest" in invalid_report
    assert "24-hour soak config hash does not match" in invalid_report
    assert "Human Setup Mode evidence schema is invalid" in invalid_report
    assert "do-not-copy-this-value" not in invalid_report

    unsafe_smoke = smoke_summary(
        7200,
        "2026-08-12T03:00:00Z",
        "2026-08-12T01:00:00Z",
        SMOKE_CONFIG_HASH,
    )
    unsafe_smoke["duration_seconds"] = float("nan")
    unsafe_smoke["minimum_free_heap_bytes"] = -1
    unsafe_smoke["final_free_heap_bytes"] = 1
    unsafe_smoke["heap_drop_bytes"] = 99999
    unsafe_smoke["stabilized_heap_drop_bytes"] = 99999
    unsafe_smoke_path = evidence_directory / "unsafe-smoke.json"
    unsafe_smoke_path.write_text(json.dumps(unsafe_smoke), encoding="utf-8")
    wrong_commit_setup = json.loads(setup_path.read_text(encoding="utf-8"))
    wrong_commit_setup["firmware_commit"] = "f" * 40
    wrong_commit_setup["ended_at"] = "2026-08-12T03:00:00Z"
    wrong_commit_setup_path = evidence_directory / "wrong-commit-setup.json"
    wrong_commit_setup_path.write_text(json.dumps(wrong_commit_setup), encoding="utf-8")
    unsafe_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    unsafe_manifest["smoke_summary"] = "evidence/unsafe-smoke.json"
    unsafe_manifest["setup_result"] = "evidence/wrong-commit-setup.json"
    unsafe_manifest_path = temporary / "unsafe-manifest.json"
    unsafe_manifest_path.write_text(json.dumps(unsafe_manifest), encoding="utf-8")
    unsafe_report_path = temporary / "unsafe-m0.md"
    unsafe = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(unsafe_manifest_path),
            "--output",
            str(unsafe_report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert unsafe.returncode == 1
    unsafe_report = unsafe_report_path.read_text(encoding="utf-8")
    assert "2-hour smoke duration is not finite" in unsafe_report
    assert "2-hour smoke timestamps are out of order" in unsafe_report
    assert "2-hour smoke heap values are invalid" in unsafe_report
    assert "2-hour smoke heap decline exceeds the controlled config" in unsafe_report
    assert "Human Setup Mode firmware commit does not match manifest" in unsafe_report
    assert "Human Setup Mode timestamps are out of order" in unsafe_report

    leaky_smoke = smoke_summary(
        7200,
        "sensitive-value-that-must-not-be-rendered",
        "2026-08-12T03:00:00Z",
        SMOKE_CONFIG_HASH,
    )
    leaky_smoke_path = evidence_directory / "leaky-smoke.json"
    leaky_smoke_path.write_text(json.dumps(leaky_smoke), encoding="utf-8")
    leaky_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    leaky_manifest["smoke_summary"] = "evidence/leaky-smoke.json"
    leaky_manifest_path = temporary / "leaky-manifest.json"
    leaky_manifest_path.write_text(json.dumps(leaky_manifest), encoding="utf-8")
    leaky_report_path = temporary / "leaky-m0.md"
    leaky = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(leaky_manifest_path),
            "--output",
            str(leaky_report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert leaky.returncode == 1
    leaky_report = leaky_report_path.read_text(encoding="utf-8")
    assert "2-hour smoke evidence schema is invalid" in leaky_report
    assert "sensitive-value-that-must-not-be-rendered" not in leaky_report

    oversized_smoke = smoke_summary(
        7200,
        "2026-08-12T01:00:00Z",
        "2026-08-12T03:00:00Z",
        SMOKE_CONFIG_HASH,
    )
    oversized_smoke["duration_seconds"] = 10**1000
    oversized_smoke_path = evidence_directory / "oversized-smoke.json"
    oversized_smoke_path.write_text(json.dumps(oversized_smoke), encoding="utf-8")
    oversized_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    oversized_manifest["smoke_summary"] = "evidence/oversized-smoke.json"
    oversized_manifest_path = temporary / "oversized-manifest.json"
    oversized_manifest_path.write_text(json.dumps(oversized_manifest), encoding="utf-8")
    oversized_report_path = temporary / "oversized-m0.md"
    oversized = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(oversized_manifest_path),
            "--output",
            str(oversized_report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert oversized.returncode == 1
    oversized_report = oversized_report_path.read_text(encoding="utf-8")
    assert "**Status:** BLOCKED" in oversized_report
    assert "2-hour smoke duration is not finite" in oversized_report

    injected_timestamp = smoke_summary(
        7200,
        "2026-08-12\n01:00:00Z",
        "2026-08-12T03:00:00Z",
        SMOKE_CONFIG_HASH,
    )
    injected_timestamp_path = evidence_directory / "injected-timestamp.json"
    injected_timestamp_path.write_text(
        json.dumps(injected_timestamp), encoding="utf-8"
    )
    injected_device = json.loads(setup_path.read_text(encoding="utf-8"))
    injected_device["device"] = "phone\r**Status:** PASS"
    injected_device_path = evidence_directory / "injected-device.json"
    injected_device_path.write_text(json.dumps(injected_device), encoding="utf-8")
    injected_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    injected_manifest["smoke_summary"] = "evidence/injected-timestamp.json"
    injected_manifest["setup_result"] = "evidence/injected-device.json"
    injected_manifest_path = temporary / "injected-manifest.json"
    injected_manifest_path.write_text(json.dumps(injected_manifest), encoding="utf-8")
    injected_report_path = temporary / "injected-m0.md"
    injected = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(injected_manifest_path),
            "--output",
            str(injected_report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert injected.returncode == 1
    injected_report = injected_report_path.read_text(encoding="utf-8")
    assert "2-hour smoke evidence schema is invalid" in injected_report
    assert "Human Setup Mode evidence schema is invalid" in injected_report
    assert "**Status:** PASS" not in injected_report

    unsafe_redaction_setup = json.loads(setup_path.read_text(encoding="utf-8"))
    unsafe_redaction_setup["redaction_statement"] = (
        "do-not-copy-this-sensitive-value"
    )
    unsafe_redaction_setup_path = evidence_directory / "unsafe-redaction-setup.json"
    unsafe_redaction_setup_path.write_text(
        json.dumps(unsafe_redaction_setup), encoding="utf-8"
    )
    unsafe_redaction_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    unsafe_redaction_manifest["setup_result"] = (
        "evidence/unsafe-redaction-setup.json"
    )
    unsafe_redaction_manifest_path = temporary / "unsafe-redaction-manifest.json"
    unsafe_redaction_manifest_path.write_text(
        json.dumps(unsafe_redaction_manifest), encoding="utf-8"
    )
    unsafe_redaction_report_path = temporary / "unsafe-redaction-m0.md"
    unsafe_redaction = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(unsafe_redaction_manifest_path),
            "--output",
            str(unsafe_redaction_report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert unsafe_redaction.returncode == 1
    unsafe_redaction_report = unsafe_redaction_report_path.read_text(encoding="utf-8")
    assert "Human Setup Mode evidence schema is invalid" in unsafe_redaction_report
    assert "do-not-copy-this-sensitive-value" not in unsafe_redaction_report

    secret_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    secret_manifest["test_commands"]["smoke"] = (
        "python smoke.py --password do-not-copy-this-value"
    )
    secret_manifest_path = temporary / "secret-manifest.json"
    secret_manifest_path.write_text(json.dumps(secret_manifest), encoding="utf-8")
    secret_report_path = temporary / "secret-m0.md"
    secret = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(secret_manifest_path),
            "--output",
            str(secret_report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert secret.returncode != 0
    assert not secret_report_path.exists()
    assert "do-not-copy-this-value" not in secret.stderr

    control_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    control_manifest["test_commands"]["smoke"] = "python smoke.py\r**Status:** PASS"
    control_manifest_path = temporary / "control-manifest.json"
    control_manifest_path.write_text(json.dumps(control_manifest), encoding="utf-8")
    control_report_path = temporary / "control-m0.md"
    control = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(control_manifest_path),
            "--output",
            str(control_report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert control.returncode != 0
    assert not control_report_path.exists()

    escaped_manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    escaped_manifest["smoke_summary"] = str(smoke_path)
    escaped_manifest_path = temporary / "escaped-manifest.json"
    escaped_manifest_path.write_text(json.dumps(escaped_manifest), encoding="utf-8")
    escaped_report_path = temporary / "escaped-m0.md"
    escaped = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(escaped_manifest_path),
            "--output",
            str(escaped_report_path),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert escaped.returncode != 0
    assert not escaped_report_path.exists()
    assert "evidence directory" in escaped.stderr

    generated_checked_report = temporary / "checked-m0.md"
    checked = subprocess.run(
        [
            sys.executable,
            str(REPORT_TOOL),
            "--manifest",
            str(CHECKED_MANIFEST),
            "--output",
            str(generated_checked_report),
        ],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert checked.returncode == 1
    assert generated_checked_report.read_bytes() == CHECKED_REPORT.read_bytes()
