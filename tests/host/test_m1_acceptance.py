#!/usr/bin/env python3

import hashlib
import importlib.util
import json
import pathlib
import re
import tempfile
import urllib.request
from unittest import mock


ROOT = pathlib.Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("m1_acceptance", ROOT / "tools/m1_acceptance.py")
assert SPEC and SPEC.loader
m1 = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(m1)


def test_private_acceptance_endpoints_never_inherit_system_proxies() -> None:
    marker = object()
    with mock.patch.object(m1.urllib.request, "build_opener", return_value=marker) as build:
        assert m1.direct_opener() is marker
    proxy_handler = build.call_args.args[0]
    assert isinstance(proxy_handler, urllib.request.ProxyHandler)
    assert proxy_handler.proxies == {}


def test_setup_client_command_is_an_exact_json_argv_without_secrets() -> None:
    with tempfile.TemporaryDirectory() as directory:
        config = pathlib.Path(directory) / "setup-client.json"
        config.write_text(
            json.dumps(["ssh", "-T", "m1-helper", "python3", "/opt/m1_setup_client.py"]),
            encoding="utf-8",
        )
        assert m1.load_setup_client_command(config) == [
            "ssh",
            "-T",
            "m1-helper",
            "python3",
            "/opt/m1_setup_client.py",
        ]
        for malformed in ({"command": ["ssh"]}, ["ssh", ""], ["ssh", "bad\narg"]):
            config.write_text(json.dumps(malformed), encoding="utf-8")
            try:
                m1.load_setup_client_command(config)
            except m1.AcceptanceFailure:
                pass
            else:
                raise AssertionError("malformed helper command must fail closed")


def test_setup_client_proves_same_helper_and_keeps_credentials_off_argv() -> None:
    source_hash = "a" * 64
    response = {
        "protocol_version": 1,
        "action": "probe",
        "ok": True,
        "helper_sha256": source_hash,
        "control_path": "wired",
    }
    completed = mock.Mock(
        returncode=0,
        stdout=json.dumps(response) + "\n",
        stderr="",
    )
    secrets = m1.SensitiveValueTracker()
    client = m1.SetupClientAdapter(["ssh", "m1-helper"], secrets, source_hash)
    cleanup_completed = mock.Mock(
        returncode=0,
        stdout=json.dumps(
            {
                "protocol_version": 1,
                "action": "cleanup",
                "ok": True,
                "network_restored": True,
            }
        )
        + "\n",
        stderr="",
    )
    with mock.patch.object(
        m1.subprocess, "run", side_effect=[completed, cleanup_completed]
    ) as run:
        client.probe(35)
    assert run.call_args_list[0].args[0] == ["ssh", "m1-helper"]
    request = json.loads(run.call_args_list[0].kwargs["input"])
    assert request["protocol_version"] == 1
    assert request["action"] == "probe"
    assert request["expected_helper_sha256"] == source_hash
    assert re.fullmatch(r"[0-9a-f]{32}", request["transaction_id"])
    cleanup_request = json.loads(run.call_args_list[1].kwargs["input"])
    assert cleanup_request["action"] == "cleanup"
    assert cleanup_request["transaction_id"] == request["transaction_id"]

    access = {
        "ssid": "S3Deck-1234",
        "password": "SETUP-SECRET",
        "address": "192.168.4.1",
    }
    snapshot_response = {
        "protocol_version": 1,
        "action": "snapshot",
        "ok": True,
        "recovery_page": True,
        "profiles": {"profile_ids": [], "active_profile_id": ""},
    }
    completed.stdout = json.dumps(snapshot_response) + "\n"
    with mock.patch.object(
        m1.subprocess, "run", side_effect=[completed, cleanup_completed]
    ) as run:
        original = client.snapshot(access, 20)
    assert original == {"profile_ids": [], "active_profile_id": ""}
    assert "SETUP-SECRET" not in " ".join(run.call_args_list[0].args[0])
    request = json.loads(run.call_args_list[0].kwargs["input"])
    assert request["access"] == access

    pair_response = {
        "protocol_version": 1,
        "action": "pair",
        "ok": True,
        "recovery_page": True,
        "response_acknowledged": True,
    }
    completed.stdout = json.dumps(pair_response) + "\n"
    with mock.patch.object(
        m1.subprocess, "run", side_effect=[completed, cleanup_completed]
    ) as run:
        client.pair(access, "192.168.31.45:7780", "012345", 20)
    assert "SETUP-SECRET" not in " ".join(run.call_args_list[0].args[0])
    assert "012345" not in " ".join(run.call_args_list[0].args[0])
    request = json.loads(run.call_args_list[0].kwargs["input"])
    assert request["pairing_code"] == "012345"

    completed.stdout = json.dumps(
        {
            "protocol_version": 1,
            "action": "restore",
            "ok": True,
            "profiles_restored": True,
        }
    ) + "\n"
    with mock.patch.object(
        m1.subprocess, "run", side_effect=[completed, cleanup_completed]
    ) as run:
        client.restore(access, original, 20)
    assert "SETUP-SECRET" not in " ".join(run.call_args_list[0].args[0])
    assert (
        json.loads(run.call_args_list[0].kwargs["input"])["original_profiles"]
        == original
    )


def test_setup_client_timeout_runs_a_fresh_ssh_cleanup_transaction() -> None:
    source_hash = "a" * 64
    client = m1.SetupClientAdapter(
        ["ssh", "m1-helper"], m1.SensitiveValueTracker(), source_hash
    )
    cleanup_completed = mock.Mock(
        returncode=0,
        stdout=json.dumps(
            {
                "protocol_version": 1,
                "action": "cleanup",
                "ok": True,
                "network_restored": True,
            }
        )
        + "\n",
        stderr="",
    )
    with mock.patch.object(
        m1.subprocess,
        "run",
        side_effect=[m1.subprocess.TimeoutExpired(["ssh"], 40), cleanup_completed],
    ) as run:
        try:
            client.snapshot(
                {
                    "ssid": "S3Deck-1234",
                    "password": "SETUP-SECRET",
                    "address": "192.168.4.1",
                },
                35,
            )
        except m1.AcceptanceFailure:
            pass
        else:
            raise AssertionError("the primary timeout must fail after compensation")
    primary = json.loads(run.call_args_list[0].kwargs["input"])
    cleanup = json.loads(run.call_args_list[1].kwargs["input"])
    assert cleanup["action"] == "cleanup"
    assert cleanup["transaction_id"] == primary["transaction_id"]
    assert cleanup["expected_helper_sha256"] == source_hash


def test_m1_source_never_switches_the_controller_mac_wifi() -> None:
    source = (m1.REPOSITORY_ROOT / "tools/m1_acceptance.py").read_text(
        encoding="utf-8"
    )
    assert "networksetup" not in source
    assert "-setairport" not in source
    main = source.index("def main()")
    probe = source.index("setup_client.probe(", main)
    preflight = source.index("run_preflight(", probe)
    flash = source.index("run_app_flash(", preflight)
    assert probe < preflight < flash


def test_pairing_records_the_profile_snapshot_before_issuing_the_code() -> None:
    operations: list[str] = []
    management = mock.Mock()
    management.request.side_effect = lambda *_args: (
        operations.append("issue-code") or (200, {"code": "012345"})
    )
    serial = mock.Mock()
    serial.event.side_effect = [
        {"type": "setup_state", "active": True},
        {
            "type": "hil_setup_access",
            "ssid": "S3Deck-1234",
            "password": "SETUP-SECRET",
            "address": "192.168.4.1",
        },
        {"type": "setup_state", "active": False},
        {"type": "companion_link_state", "state": "online"},
    ]
    setup_client = mock.Mock()
    original = {"profile_ids": [], "active_profile_id": ""}
    setup_client.snapshot.side_effect = lambda *_args: (
        operations.append("snapshot") or original
    )
    setup_client.pair.side_effect = lambda *_args: operations.append("pair")
    cleanup = m1.CleanupTransaction()
    observed: list[dict[str, object]] = []

    m1.pair_deck(
        management,
        serial,
        setup_client,
        "192.168.31.45:7780",
        20,
        cleanup,
        m1.SensitiveValueTracker(),
        "pair",
        lambda profiles: (operations.append("record"), observed.append(profiles)),
    )
    assert operations == ["snapshot", "record", "issue-code", "pair"]
    assert observed == [original]


def test_serial_evidence_keeps_redacted_link_state_and_drops_setup_secret() -> None:
    safe = json.dumps(
        {
            "type": "companion_link_state",
            "state": "online",
            "has_active_profile": True,
            "profile_generation": 2,
            "reconnect_attempts": 0,
            "error_count": 1,
            "last_error": "tls_pin_mismatch",
            "error_generation": 1,
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
    native_run = {
        "repository": m1.NATIVE_RUN_REPOSITORY,
        "workflow": m1.NATIVE_RUN_WORKFLOW,
        "workflow_id": 321,
        "workflow_path": m1.NATIVE_RUN_WORKFLOW_PATH,
        "run_id": 123,
        "run_url": "https://github.com/Vectorking-50kg/s3-rlcd-deck/actions/runs/123",
        "event": "pull_request",
        "head_sha": "a" * 40,
        "jobs": {
            "macos": {"job_id": 456},
            "windows": {"job_id": 789},
        },
    }
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
            native_run_evidence=native_run,
        )
    assert summary["status"] == "passed"
    assert summary["raw_log_sha256"] == hashlib.sha256(b'{"line":"safe"}\n').hexdigest()
    assert summary["native_run"] == native_run
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
        for bare in (
            "device token=PAIRING_SECRET",
            "fingerprint=sha256:" + "a" * 64,
            "certificate-fingerprint: hidden",
            "unlabelled " + "A" * 43 + " value",
        ):
            evidence.write_text(bare, encoding="utf-8")
            assert not m1.evidence_is_redacted(evidence), bare


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


def test_command_output_can_use_the_pinned_toolchain_environment() -> None:
    environment = {"PATH": "/pinned/bin", "ESP_IDF_VERSION": "6.0.2"}
    with mock.patch.object(m1.subprocess, "run", return_value=mock.Mock(stdout="ok\n")) as run:
        assert m1.command_output(["idf.py", "--version"], environment) == "ok"
    assert run.call_args.kwargs["env"] is environment


def test_summary_toolchain_identity_uses_the_pinned_environment() -> None:
    checks = {name: True for name in m1.REQUIRED_CHECKS}
    environment = {"PATH": "/pinned/bin", "ESP_IDF_VERSION": "6.0.2"}
    with mock.patch.object(
        m1, "command_output", side_effect=["go pinned", "ESP-IDF v6.0.2"]
    ) as output:
        summary = m1.build_summary(
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
            toolchain_environment=environment,
        )
    assert summary["toolchains"]["esp_idf"] == "ESP-IDF v6.0.2"
    assert all(call.args[1] is environment for call in output.call_args_list)


def test_serial_module_falls_back_to_the_pinned_idf_environment() -> None:
    environment = {"IDF_PYTHON_ENV_PATH": "/pinned", "PATH": "/pinned/bin"}
    serial_module = mock.Mock(
        Serial=mock.Mock(),
        SerialException=type("SerialException", (Exception,), {}),
    )
    patched_path = list(m1.sys.path)
    with mock.patch.object(pathlib.Path, "is_file", return_value=True), mock.patch.object(
        m1.importlib,
        "import_module",
        side_effect=[object(), serial_module],
    ), mock.patch.object(
        m1, "command_output", return_value="/pinned/lib/python3.14/site-packages"
    ) as output, mock.patch.object(
        m1.sys, "path", patched_path
    ):
        assert m1.load_serial_module(environment) is serial_module
        assert patched_path[0] == "/pinned/lib/python3.14/site-packages"
    output.assert_called_once()
    assert output.call_args.args[1] is environment


def test_native_run_requires_same_commit_and_both_real_platform_jobs() -> None:
    full = "c" * 40
    url = (
        "https://github.com/Vectorking-50kg/s3-rlcd-deck/actions/runs/123"
    )
    macos_steps = [
        {"name": name, "conclusion": "success"}
        for name in m1.NATIVE_RUN_REQUIRED_STEPS["macos"]
    ]
    windows_steps = [
        {"name": name, "conclusion": "success"}
        for name in m1.NATIVE_RUN_REQUIRED_STEPS["windows"]
    ]
    workflow = {
        "id": 321,
        "name": m1.NATIVE_RUN_WORKFLOW,
        "path": m1.NATIVE_RUN_WORKFLOW_PATH,
        "state": "active",
    }
    document = {
        "databaseId": 123,
        "workflowDatabaseId": 321,
        "workflowName": m1.NATIVE_RUN_WORKFLOW,
        "url": url,
        "event": "pull_request",
        "headSha": full,
        "conclusion": "success",
        "jobs": [
            {
                "name": "macOS native (arm64)",
                "databaseId": 456,
                "url": url + "/job/456",
                "conclusion": "success",
                "steps": macos_steps,
            },
            {
                "name": "Windows native (amd64)",
                "databaseId": 789,
                "url": url + "/job/789",
                "conclusion": "success",
                "steps": windows_steps,
            },
        ],
    }
    with mock.patch.object(
        m1,
        "command_output",
        side_effect=[json.dumps(workflow), json.dumps(document)],
    ):
        evidence = m1.verified_native_run(url, full)
        assert evidence is not None
        assert evidence["workflow_id"] == 321
        assert evidence["workflow_path"] == m1.NATIVE_RUN_WORKFLOW_PATH
        assert evidence["workflow"] == m1.NATIVE_RUN_WORKFLOW
        assert evidence["jobs"]["macos"]["job_id"] == 456
        assert evidence["jobs"]["windows"]["job_id"] == 789
    document["jobs"].pop()
    with mock.patch.object(
        m1,
        "command_output",
        side_effect=[json.dumps(workflow), json.dumps(document)],
    ):
        evidence = m1.verified_native_run(url, full)
        assert evidence is not None
        assert evidence["jobs"]["macos"] is not None
        assert evidence["jobs"]["windows"] is None
    document["workflowName"] = "Untrusted lookalike workflow"
    with mock.patch.object(
        m1,
        "command_output",
        side_effect=[json.dumps(workflow), json.dumps(document)],
    ):
        assert m1.verified_native_run(url, full) is None
    document["workflowName"] = m1.NATIVE_RUN_WORKFLOW
    workflow["path"] = ".github/workflows/lookalike.yml"
    with mock.patch.object(
        m1,
        "command_output",
        side_effect=[json.dumps(workflow), json.dumps(document)],
    ):
        assert m1.verified_native_run(url, full) is None
    assert m1.verified_native_run(
        "https://github.com/other/repo/actions/runs/123", full
    ) is None


def test_failure_after_result_directory_creation_still_writes_summary() -> None:
    with tempfile.TemporaryDirectory() as directory:
        result = pathlib.Path(directory) / "result"
        arguments = m1.argparse.Namespace(
            port="/dev/null",
            result_dir=result,
            setup_client_command_file=pathlib.Path("unused.json"),
            hub_address="192.168.1.10:7780",
            native_run_url=(
                "https://github.com/Vectorking-50kg/s3-rlcd-deck/actions/runs/123"
            ),
            stage_timeout=75.0,
        )
        with mock.patch.object(
            m1, "parse_arguments", return_value=arguments
        ), mock.patch.object(
            m1, "source_tree_clean", side_effect=OSError("git unavailable")
        ):
            assert m1.main() == 1
        summary = json.loads((result / "summary.json").read_text(encoding="utf-8"))
        assert summary["status"] == "failed"
        assert "source_dirty" in summary["failures"]


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


def test_post_flash_monitor_requests_a_fresh_boot_after_ready_handshake() -> None:
    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)
    commands: list[bytes] = []
    evidence.command = commands.append
    evidence.event = mock.Mock(side_effect=m1.AcceptanceTimeout("missed"))
    evidence.restart_and_wait = mock.Mock(return_value={"type": "boot_ok"})
    marker = object()
    assert evidence.fresh_boot(marker, "/dev/cu.Deck", 12.0) == {"type": "boot_ok"}
    assert commands == [m1.HIL_READY]
    evidence.restart_and_wait.assert_called_once_with(
        marker, "/dev/cu.Deck", 12.0, "post-flash fresh boot"
    )

    commands.clear()
    evidence.event = mock.Mock(return_value={"type": "boot_ok"})
    assert evidence.fresh_boot(marker, "/dev/cu.Deck", 12.0) == {"type": "boot_ok"}
    assert commands == [m1.HIL_READY]


def test_boot_gate_accepts_only_the_expected_software_reset() -> None:
    unexpected_reasons = {
        "unknown",
        "power_on",
        "external",
        "panic",
        "interrupt_watchdog",
        "task_watchdog",
        "watchdog",
        "deep_sleep",
        "brownout",
        "sdio",
        "usb",
        "jtag",
        "efuse",
        "power_glitch",
        "cpu_lockup",
    }
    for reason in unexpected_reasons:
        try:
            m1.accept_boot_event({"type": "boot_ok", "reset_reason": reason}, "boot")
        except m1.AcceptanceFailure as error:
            assert reason in str(error)
        else:
            raise AssertionError(f"unexpected reset reason {reason} was accepted")
    assert m1.accept_boot_event(
        {"type": "boot_ok", "reset_reason": "software"}, "boot"
    )


def test_post_flash_usb_reenumeration_reopens_without_a_second_reset() -> None:
    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)
    commands: list[bytes] = []
    serial_module = mock.Mock(SerialException=OSError)
    evidence.command = commands.append
    evidence.event = mock.Mock(side_effect=m1.SerialDisconnected("USB endpoint detached"))
    evidence.event_after_reenumeration = mock.Mock(return_value={"type": "boot_ok"})

    assert evidence.fresh_boot(serial_module, "/dev/cu.Deck", 12.0) == {
        "type": "boot_ok"
    }
    assert commands == [m1.HIL_READY]
    evidence.event_after_reenumeration.assert_called_once()


def test_post_flash_usb_reenumeration_can_interrupt_the_ready_write() -> None:
    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)
    serial_module = mock.Mock(SerialException=OSError)
    evidence.command = mock.Mock(
        side_effect=m1.SerialDisconnected("USB endpoint detached")
    )
    evidence.event_after_reenumeration = mock.Mock(return_value={"type": "boot_ok"})

    assert evidence.fresh_boot(serial_module, "/dev/cu.Deck", 12.0) == {
        "type": "boot_ok"
    }
    evidence.event_after_reenumeration.assert_called_once()


def test_reenumerated_boot_skips_a_stale_endpoint_within_one_deadline() -> None:
    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)
    evidence.reopen = mock.Mock()
    evidence.event = mock.Mock(
        side_effect=[m1.SerialDisconnected("stale endpoint"), {"type": "boot_ok"}]
    )
    serial_module = mock.Mock(SerialException=OSError)
    with mock.patch.object(
        m1.time,
        "monotonic",
        side_effect=[0.0, 0.1, 0.2, 0.3, 0.4],
    ):
        observed = evidence.event_after_reenumeration(
            serial_module,
            "/dev/cu.Deck",
            lambda event: event.get("type") == "boot_ok",
            "boot",
            10.0,
        )
    assert observed == {"type": "boot_ok"}
    assert evidence.reopen.call_count == 2


def test_reenumerated_boot_reopens_a_silent_stale_endpoint() -> None:
    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)
    evidence.reopen = mock.Mock()
    evidence.event = mock.Mock(
        side_effect=[m1.AcceptanceTimeout("stale endpoint"), {"type": "boot_ok"}]
    )
    serial_module = mock.Mock(SerialException=OSError)
    with mock.patch.object(m1.time, "monotonic", side_effect=lambda: 1.0):
        observed = evidence.event_after_reenumeration(
            serial_module,
            "/dev/cu.Deck",
            lambda event: event.get("type") == "boot_ok",
            "boot",
            10.0,
        )
    assert observed == {"type": "boot_ok"}
    assert evidence.reopen.call_count == 2


def test_initial_serial_open_waits_for_usb_reenumeration() -> None:
    class Reenumerated(Exception):
        pass

    connection = object()
    serial_module = mock.Mock(SerialException=Reenumerated)
    serial_module.Serial.side_effect = [
        Reenumerated("not enumerated"),
        OSError("not enumerated"),
        connection,
    ]
    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)

    with mock.patch.object(m1.time, "sleep"), mock.patch.object(
        m1.time, "monotonic", side_effect=[0.0, 0.0, 0.1, 0.2]
    ):
        evidence._connection = m1.SerialEvidence.open_connection(
            serial_module, "/dev/cu.Deck", 1.0
        )
    assert evidence._connection is connection
    assert serial_module.Serial.call_count == 3


def test_initial_serial_open_retries_macos_termios_detach() -> None:
    class TermiosDetached(Exception):
        pass

    connection = object()
    serial_module = mock.Mock(SerialException=OSError)
    serial_module.Serial.side_effect = [TermiosDetached("Device not configured"), connection]
    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)

    with mock.patch.object(m1.termios, "error", TermiosDetached), mock.patch.object(
        m1.time, "sleep"
    ), mock.patch.object(m1.time, "monotonic", side_effect=[0.0, 0.0, 0.1]):
        evidence._connection = m1.SerialEvidence.open_connection(
            serial_module, "/dev/cu.Deck", 1.0
        )
    assert evidence._connection is connection


def test_initial_serial_open_timeout_fails_closed() -> None:
    serial_module = mock.Mock(SerialException=OSError)
    serial_module.Serial.side_effect = OSError("not enumerated")
    with mock.patch.object(m1.time, "sleep"), mock.patch.object(
        m1.time, "monotonic", side_effect=[0.0, 0.0, 1.0]
    ):
        try:
            m1.SerialEvidence.open_connection(serial_module, "/dev/cu.Deck", 0.5)
        except m1.AcceptanceFailure as error:
            assert "did not enumerate" in str(error)
        else:
            raise AssertionError("missing USB endpoint must fail closed")


def test_evidence_write_failure_is_not_mistaken_for_usb_reenumeration() -> None:
    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)
    evidence.command = mock.Mock()
    evidence.reopen = mock.Mock()
    evidence.event = mock.Mock(side_effect=OSError("evidence disk full"))

    try:
        evidence.fresh_boot(mock.Mock(SerialException=OSError), "/dev/cu.Deck", 12.0)
    except OSError as error:
        assert "disk full" in str(error)
    else:
        raise AssertionError("evidence I/O failure must fail closed")
    evidence.reopen.assert_not_called()


def test_fatal_boot_evidence_is_not_retried() -> None:
    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)
    evidence.command = mock.Mock()
    evidence.reopen = mock.Mock()
    evidence.event = mock.Mock(side_effect=m1.AcceptanceFailure("fatal Deck log"))
    try:
        evidence.fresh_boot(object(), "/dev/cu.Deck", 12.0)
    except m1.AcceptanceFailure as error:
        assert "fatal" in str(error)
    else:
        raise AssertionError("fatal evidence must fail closed")
    evidence.command.assert_called_once_with(m1.HIL_READY)
    evidence.reopen.assert_not_called()


def test_serial_command_never_calls_unbounded_flush() -> None:
    class Connection:
        def __init__(self) -> None:
            self.writes: list[bytes] = []

        def write(self, value: bytes) -> None:
            self.writes.append(value)

        def flush(self) -> None:
            raise AssertionError("flush must not be called")

    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)
    evidence._connection = Connection()
    evidence.command(b"DECK_SETUP\n")
    assert evidence._connection.writes == [b"DECK_SETUP\n"]


def test_restart_waits_for_device_ack_before_closing_the_endpoint() -> None:
    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)
    operations: list[str] = []
    evidence.command = lambda value: operations.append(f"command:{value.decode().strip()}")

    def event(predicate, _stage, _timeout):
        operations.append("ack")
        observed = {"type": "restart_ack"}
        assert predicate(observed)
        return observed

    evidence.event = event
    evidence.event_after_reenumeration = mock.Mock(
        side_effect=lambda *_args: operations.append("reopen") or {"type": "boot_ok"}
    )
    observed = evidence.restart_and_wait(object(), "/dev/cu.Deck", 12, "restart")
    assert observed == {"type": "boot_ok"}
    assert operations == ["command:DECK_RESTART", "ack", "reopen"]


def test_restart_ack_is_emitted_before_the_firmware_resets() -> None:
    source = (m1.REPOSITORY_ROOT / "firmware/main/app_main.cpp").read_text(
        encoding="utf-8"
    )
    command = source.index('std::strcmp(line, "DECK_RESTART")')
    acknowledgement = source.index('"{\\\"type\\\":\\\"restart_ack\\\"}\\n"', command)
    reset = source.index("esp_restart()", acknowledgement)
    assert command < acknowledgement < reset


def test_dev_setup_window_outlives_slow_association() -> None:
    defaults = (
        m1.REPOSITORY_ROOT / "firmware/sdkconfig.defaults.dev"
    ).read_text(encoding="utf-8")
    assert "CONFIG_DECK_SETUP_INACTIVITY_TIMEOUT_SECONDS=120" in defaults


def test_diagnostic_console_resets_usb_after_app_only_jtag_flash() -> None:
    source = (
        m1.REPOSITORY_ROOT / "firmware/main/app_main.cpp"
    ).read_text(encoding="utf-8")
    detach = source.index("usb_serial_jtag_ll_phy_enable_pull_override(&detached)")
    atomic = source.index("PERIPH_RCC_ATOMIC()")
    reset = source.index("usb_serial_jtag_ll_reset_register()")
    attach = source.index("usb_serial_jtag_ll_phy_disable_pull_override()")
    driver_install = source.index("usb_serial_jtag_driver_install(&configuration)")
    assert detach < atomic < reset < attach < driver_install


def test_cleanup_transaction_records_intent_before_observation() -> None:
    cleanup = m1.CleanupTransaction()
    cleanup.begin_setup()
    assert cleanup.needs_compensation()
    cleanup.observe_setup_closed()
    assert cleanup.restored()


def test_setup_restart_requires_explicit_inactive_observation() -> None:
    cleanup = m1.CleanupTransaction()
    cleanup.begin_setup()
    evidence = mock.Mock()
    evidence.restart_and_wait.return_value = {"type": "boot_ok"}
    evidence.event.return_value = {"type": "setup_state", "active": False}
    m1.close_setup_by_restart(evidence, object(), "/dev/cu.Deck", 12.0, cleanup)
    assert cleanup.restored()
    assert evidence.event.call_args.args[0]({"type": "boot_ok"}) is False
    assert evidence.event.call_args.args[0](
        {"type": "setup_state", "active": False}
    ) is True


def test_exact_link_error_gate_rejects_unrelated_failures() -> None:
    event = {
        "type": "companion_link_state",
        "state": "offline",
        "last_error": "transport",
        "error_generation": 4,
    }
    assert not m1.is_new_link_error(event, 3, "tls_pin_mismatch")
    event["last_error"] = "tls_pin_mismatch"
    assert m1.is_new_link_error(event, 3, "tls_pin_mismatch")


def test_secret_tracker_catches_value_leaked_before_it_was_known() -> None:
    tracker = m1.SensitiveValueTracker()
    tracker.observe("ordinary prefix future-secret ordinary suffix")
    assert tracker.clean()
    tracker.add("future-secret")
    assert not tracker.clean()


def test_idf_kconfig_identifier_is_not_mistaken_for_a_device_token() -> None:
    warning = (
        "duplicate rename mapping: new target "
        "CONFIG_BT_CTRL_COEX_PHY_CODED_TX_RX_TLIM_EN - last mapping is used"
    )
    assert m1.text_is_redacted(warning)
    tracker = m1.SensitiveValueTracker()
    assert not tracker.observe(warning)
    assert tracker.clean()
    assert not m1.text_is_redacted("unlabelled AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")


def test_expected_setup_access_does_not_trip_secret_observation() -> None:
    raw = '{"type":"hil_setup_access","ssid":"Deck","password":"secret","address":"192.168.4.1"}'
    sanitized, _ = m1.sanitize_serial_line(raw)
    assert not m1.serial_line_may_contain_secret(raw, sanitized)
    assert m1.serial_line_may_contain_secret("device token=bare", m1.REDACTED_NON_DIAGNOSTIC)
    assert m1.serial_line_may_contain_secret("pairing code 123456", m1.REDACTED_NON_DIAGNOSTIC)


def test_preflight_uses_the_users_zsh_for_idf_activation() -> None:
    with tempfile.TemporaryDirectory() as directory, mock.patch.dict(
        m1.os.environ, {}, clear=True
    ), mock.patch.object(
        pathlib.Path, "home", return_value=pathlib.Path(directory)
    ), mock.patch.object(m1.subprocess, "run") as run:
        idf = pathlib.Path(directory) / ".espressif/v6.0.2/esp-idf"
        idf.mkdir(parents=True)
        environment_result = mock.Mock(stdout=b"IDF_PATH=/idf\0PATH=/idf/bin\0")
        command_result = mock.Mock(returncode=1)
        run.side_effect = [environment_result, command_result]
        try:
            m1.run_preflight(pathlib.Path(directory) / "preflight.log")
        except m1.AcceptanceFailure:
            pass
        assert run.call_args_list[0].args[0][0] == "zsh"


if __name__ == "__main__":
    test_private_acceptance_endpoints_never_inherit_system_proxies()
    test_setup_client_command_is_an_exact_json_argv_without_secrets()
    test_setup_client_proves_same_helper_and_keeps_credentials_off_argv()
    test_setup_client_timeout_runs_a_fresh_ssh_cleanup_transaction()
    test_m1_source_never_switches_the_controller_mac_wifi()
    test_pairing_records_the_profile_snapshot_before_issuing_the_code()
    test_serial_evidence_keeps_redacted_link_state_and_drops_setup_secret()
    test_summary_passes_only_with_every_real_gate_and_hashes_redacted_log()
    test_evidence_redaction_gate_rejects_every_secret_field()
    test_build_identity_is_only_retained_for_an_exact_full_commit()
    test_current_source_and_companion_identity_are_observed_not_assumed()
    test_command_output_can_use_the_pinned_toolchain_environment()
    test_summary_toolchain_identity_uses_the_pinned_environment()
    test_serial_module_falls_back_to_the_pinned_idf_environment()
    test_native_run_requires_same_commit_and_both_real_platform_jobs()
    test_failure_after_result_directory_creation_still_writes_summary()
    test_companion_logs_are_drained_redacted_and_secret_observation_fails_gate()
    test_post_flash_monitor_requests_a_fresh_boot_after_ready_handshake()
    test_boot_gate_accepts_only_the_expected_software_reset()
    test_post_flash_usb_reenumeration_reopens_without_a_second_reset()
    test_post_flash_usb_reenumeration_can_interrupt_the_ready_write()
    test_reenumerated_boot_skips_a_stale_endpoint_within_one_deadline()
    test_reenumerated_boot_reopens_a_silent_stale_endpoint()
    test_initial_serial_open_waits_for_usb_reenumeration()
    test_initial_serial_open_retries_macos_termios_detach()
    test_initial_serial_open_timeout_fails_closed()
    test_evidence_write_failure_is_not_mistaken_for_usb_reenumeration()
    test_fatal_boot_evidence_is_not_retried()
    test_serial_command_never_calls_unbounded_flush()
    test_restart_waits_for_device_ack_before_closing_the_endpoint()
    test_restart_ack_is_emitted_before_the_firmware_resets()
    test_dev_setup_window_outlives_slow_association()
    test_cleanup_transaction_records_intent_before_observation()
    test_setup_restart_requires_explicit_inactive_observation()
    test_exact_link_error_gate_rejects_unrelated_failures()
    test_secret_tracker_catches_value_leaked_before_it_was_known()
    test_idf_kconfig_identifier_is_not_mistaken_for_a_device_token()
    test_expected_setup_access_does_not_trip_secret_observation()
    test_preflight_uses_the_users_zsh_for_idf_activation()
    print("M1 acceptance contract passed")
