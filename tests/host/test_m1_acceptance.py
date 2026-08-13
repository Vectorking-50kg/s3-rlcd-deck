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
    evidence.event = mock.Mock(
        side_effect=[m1.AcceptanceTimeout("missed"), {"type": "boot_ok"}]
    )
    marker = object()
    assert evidence.fresh_boot(marker, "/dev/cu.Deck", 12.0) == {"type": "boot_ok"}
    assert commands == [m1.HIL_READY, b"DECK_RESTART\n"]
    assert reopens == [(marker, "/dev/cu.Deck", 12.0)]

    commands.clear()
    reopens.clear()
    evidence.event = mock.Mock(return_value={"type": "boot_ok"})
    assert evidence.fresh_boot(marker, "/dev/cu.Deck", 12.0) == {"type": "boot_ok"}
    assert commands == [m1.HIL_READY]
    assert reopens == []


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
    reopens: list[tuple[object, str, float]] = []
    serial_module = mock.Mock(SerialException=OSError)
    evidence.command = commands.append
    evidence.reopen = lambda module, port, timeout: reopens.append((module, port, timeout))
    evidence.event = mock.Mock(
        side_effect=[m1.SerialDisconnected("USB endpoint detached"), {"type": "boot_ok"}]
    )

    assert evidence.fresh_boot(serial_module, "/dev/cu.Deck", 12.0) == {
        "type": "boot_ok"
    }
    assert commands == [m1.HIL_READY]
    assert reopens == [(serial_module, "/dev/cu.Deck", 12.0)]


def test_post_flash_usb_reenumeration_can_interrupt_the_ready_write() -> None:
    evidence = m1.SerialEvidence.__new__(m1.SerialEvidence)
    serial_module = mock.Mock(SerialException=OSError)
    evidence.command = mock.Mock(
        side_effect=m1.SerialDisconnected("USB endpoint detached")
    )
    evidence.reopen = mock.Mock()
    evidence.event = mock.Mock(return_value={"type": "boot_ok"})

    assert evidence.fresh_boot(serial_module, "/dev/cu.Deck", 12.0) == {
        "type": "boot_ok"
    }
    evidence.reopen.assert_called_once_with(serial_module, "/dev/cu.Deck", 12.0)
    evidence.event.assert_called_once()


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


def test_wifi_switch_allows_slow_macos_association() -> None:
    result = mock.Mock(returncode=0)
    with mock.patch.object(m1.subprocess, "run", return_value=result) as run:
        m1.connect_wifi("Setup", "password")
    assert run.call_args.kwargs["timeout"] >= 45


def test_wifi_switch_timeout_never_exposes_the_password() -> None:
    secret = "TOP-SECRET-SETUP-PASSWORD"
    timeout = m1.subprocess.TimeoutExpired(
        ["networksetup", "-setairportnetwork", "en0", "Setup", secret], 60
    )
    with mock.patch.object(m1.subprocess, "run", side_effect=timeout):
        try:
            m1.connect_wifi("Setup", secret)
        except m1.AcceptanceFailure as error:
            assert secret not in str(error)
            assert "timed out" in str(error)
        else:
            raise AssertionError("association timeout must fail")


def test_dev_setup_window_outlives_slow_association() -> None:
    defaults = (
        m1.REPOSITORY_ROOT / "firmware/sdkconfig.defaults.dev"
    ).read_text(encoding="utf-8")
    assert "CONFIG_DECK_SETUP_INACTIVITY_TIMEOUT_SECONDS=120" in defaults


def test_setup_wifi_recovery_requires_the_target_host_to_be_reachable() -> None:
    operations: list[tuple[str, object]] = []
    reachable = iter([False, False, True])

    def connect(ssid: str, password: str | None, timeout: float) -> None:
        operations.append(("connect", (ssid, password, timeout)))

    def power(enabled: bool, timeout: float) -> None:
        operations.append(("power", (enabled, timeout)))

    with mock.patch.object(m1, "connect_wifi", side_effect=connect), mock.patch.object(
        m1, "set_wifi_power", side_effect=power
    ), mock.patch.object(
        m1, "host_is_reachable", side_effect=lambda _host, _timeout: next(reachable)
    ), mock.patch.object(m1.time, "sleep"), mock.patch.object(
        m1.time, "monotonic", side_effect=lambda: 1.0
    ):
        m1.connect_wifi_for_host("Setup", "password", "192.168.4.1", 90)

    assert operations[0][0] == "connect"
    assert any(name == "power" and value[0] is False for name, value in operations)
    assert any(name == "power" and value[0] is True for name, value in operations)
    assert sum(name == "connect" for name, _ in operations) == 2


def test_wifi_power_is_restored_when_reassociation_fails() -> None:
    operations: list[bool] = []

    def power(enabled: bool, _timeout: float) -> None:
        operations.append(enabled)
        if enabled:
            raise m1.AcceptanceFailure("power on failed")

    with mock.patch.object(m1, "connect_wifi"), mock.patch.object(
        m1, "host_is_reachable", return_value=False
    ), mock.patch.object(m1, "set_wifi_power", side_effect=power), mock.patch.object(
        m1.time, "sleep"
    ):
        try:
            m1.connect_wifi_for_host("Setup", "password", "192.168.4.1", 90)
        except m1.AcceptanceFailure:
            pass
        else:
            raise AssertionError("power-on failure must fail")
    assert operations == [False, True]


def test_original_lan_recovery_uses_the_reachability_helper() -> None:
    source = (m1.REPOSITORY_ROOT / "tools/m1_acceptance.py").read_text(encoding="utf-8")
    assert source.count("restore_original_wifi(") == 4
    assert '"192.168.31.1",\n        timeout,\n        deadline=deadline' in source


def test_wifi_operations_share_one_deadline_budget() -> None:
    clock = iter([1.0, 1.0, 5.0])
    with mock.patch.object(m1.time, "monotonic", side_effect=lambda: next(clock)):
        assert m1.remaining_timeout(10.0, 60.0) == 9.0
        assert m1.remaining_timeout(10.0, 60.0, reserve=3.0) == 6.0
        try:
            m1.remaining_timeout(10.0, 60.0, reserve=6.0)
        except m1.AcceptanceFailure as error:
            assert "timed out" in str(error)
        else:
            raise AssertionError("exhausted Wi-Fi deadline must fail")


def test_target_ssid_is_selected_before_reachability_can_pass() -> None:
    operations: list[str] = []
    with mock.patch.object(
        m1,
        "connect_wifi",
        side_effect=lambda *_args: operations.append("connect"),
    ), mock.patch.object(
        m1,
        "host_is_reachable",
        side_effect=lambda *_args: operations.append("ping") or True,
    ), mock.patch.object(m1.time, "monotonic", side_effect=lambda: 1.0):
        m1.connect_wifi_for_host("Setup", "password", "192.168.4.1", 30)
    assert operations == ["connect", "ping"]


def test_wifi_reachability_requires_an_en0_address_on_the_target_subnet() -> None:
    def command_output(command: list[str], _timeout: float) -> str:
        if command[:2] == ["ipconfig", "getifaddr"]:
            return "192.168.31.45"
        return "route to: 192.168.4.1\ninterface: en0\n"

    with mock.patch.object(
        m1, "wifi_command_output", side_effect=command_output
    ), mock.patch.object(m1.subprocess, "run", return_value=mock.Mock(returncode=0)):
        assert not m1.host_is_reachable("192.168.4.1", 2)

    def setup_output(command: list[str], _timeout: float) -> str:
        if command[:2] == ["ipconfig", "getifaddr"]:
            return "192.168.4.2"
        return "route to: 192.168.4.1\ninterface: en0\n"

    with mock.patch.object(
        m1, "wifi_command_output", side_effect=setup_output
    ), mock.patch.object(m1.subprocess, "run", return_value=mock.Mock(returncode=0)):
        assert m1.host_is_reachable("192.168.4.1", 2)


def test_original_wifi_power_and_association_share_one_deadline() -> None:
    observed: dict[str, object] = {}

    def power(_enabled: bool, timeout: float) -> None:
        observed["power_timeout"] = timeout

    def connect(
        _ssid: str,
        _password: str | None,
        _host: str,
        timeout: float,
        *,
        deadline: float | None = None,
    ) -> None:
        observed["connect_timeout"] = timeout
        observed["deadline"] = deadline

    clock = iter([10.0, 11.0])
    with mock.patch.object(m1, "set_wifi_power", side_effect=power), mock.patch.object(
        m1, "connect_wifi_for_host", side_effect=connect
    ), mock.patch.object(m1.time, "monotonic", side_effect=lambda: next(clock)):
        m1.restore_original_wifi("LAN", 30)
    assert observed["power_timeout"] == 10
    assert observed["connect_timeout"] == 30
    assert observed["deadline"] == 40.0


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
    cleanup.observe_setup_access("S3Deck-111111")
    cleanup.observe_setup_access("S3Deck-222222")
    assert cleanup.setup_ssids == {"S3Deck-111111", "S3Deck-222222"}
    cleanup.observe_setup_closed()
    assert cleanup.restored()


def test_setup_restart_requires_explicit_inactive_observation() -> None:
    cleanup = m1.CleanupTransaction()
    cleanup.begin_setup()
    evidence = mock.Mock()
    evidence.event.side_effect = [
        {"type": "boot_ok"},
        {"type": "setup_state", "active": False},
    ]
    m1.close_setup_by_restart(evidence, object(), "/dev/cu.Deck", 12.0, cleanup)
    assert cleanup.restored()
    assert evidence.event.call_args_list[1].args[0]({"type": "boot_ok"}) is False
    assert evidence.event.call_args_list[1].args[0](
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


def test_expected_setup_access_does_not_trip_secret_observation() -> None:
    raw = '{"type":"hil_setup_access","ssid":"Deck","password":"secret","address":"192.168.4.1"}'
    sanitized, _ = m1.sanitize_serial_line(raw)
    assert not m1.serial_line_may_contain_secret(raw, sanitized)
    assert m1.serial_line_may_contain_secret("device token=bare", m1.REDACTED_NON_DIAGNOSTIC)
    assert m1.serial_line_may_contain_secret("pairing code 123456", m1.REDACTED_NON_DIAGNOSTIC)


def test_cleanup_attempts_every_setup_network_when_one_removal_fails() -> None:
    cleanup = m1.CleanupTransaction()
    cleanup.setup_ssids.update({"Deck-A", "Deck-B"})
    attempted: list[str] = []

    def removal(ssid: str) -> bool:
        attempted.append(ssid)
        return ssid != "Deck-A"

    with mock.patch.object(m1, "forget_wifi", side_effect=removal):
        cleanup_ok, failures = m1.forget_setup_networks(cleanup.setup_ssids)
    assert not cleanup_ok
    assert failures == ["Deck-A"]
    assert set(attempted) == {"Deck-A", "Deck-B"}


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
    test_serial_evidence_keeps_redacted_link_state_and_drops_setup_secret()
    test_summary_passes_only_with_every_real_gate_and_hashes_redacted_log()
    test_evidence_redaction_gate_rejects_every_secret_field()
    test_build_identity_is_only_retained_for_an_exact_full_commit()
    test_current_source_and_companion_identity_are_observed_not_assumed()
    test_command_output_can_use_the_pinned_toolchain_environment()
    test_summary_toolchain_identity_uses_the_pinned_environment()
    test_serial_module_falls_back_to_the_pinned_idf_environment()
    test_native_run_requires_same_commit_and_both_real_platform_jobs()
    test_companion_logs_are_drained_redacted_and_secret_observation_fails_gate()
    test_profile_cleanup_revokes_only_temporary_profile_and_reselects_original()
    test_post_flash_monitor_requests_a_fresh_boot_after_ready_handshake()
    test_boot_gate_accepts_only_the_expected_software_reset()
    test_post_flash_usb_reenumeration_reopens_without_a_second_reset()
    test_post_flash_usb_reenumeration_can_interrupt_the_ready_write()
    test_initial_serial_open_waits_for_usb_reenumeration()
    test_initial_serial_open_timeout_fails_closed()
    test_evidence_write_failure_is_not_mistaken_for_usb_reenumeration()
    test_fatal_boot_evidence_is_not_retried()
    test_serial_command_never_calls_unbounded_flush()
    test_wifi_switch_allows_slow_macos_association()
    test_wifi_switch_timeout_never_exposes_the_password()
    test_dev_setup_window_outlives_slow_association()
    test_setup_wifi_recovery_requires_the_target_host_to_be_reachable()
    test_wifi_power_is_restored_when_reassociation_fails()
    test_original_lan_recovery_uses_the_reachability_helper()
    test_wifi_operations_share_one_deadline_budget()
    test_target_ssid_is_selected_before_reachability_can_pass()
    test_wifi_reachability_requires_an_en0_address_on_the_target_subnet()
    test_original_wifi_power_and_association_share_one_deadline()
    test_cleanup_transaction_records_intent_before_observation()
    test_setup_restart_requires_explicit_inactive_observation()
    test_exact_link_error_gate_rejects_unrelated_failures()
    test_secret_tracker_catches_value_leaked_before_it_was_known()
    test_expected_setup_access_does_not_trip_secret_observation()
    test_cleanup_attempts_every_setup_network_when_one_removal_fails()
    test_preflight_uses_the_users_zsh_for_idf_activation()
    print("M1 acceptance contract passed")
