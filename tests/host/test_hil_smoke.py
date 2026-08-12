#!/usr/bin/env python3

import hashlib
import json
import os
import pathlib
import subprocess
import sys
import tempfile


REPOSITORY_ROOT = pathlib.Path(__file__).resolve().parents[2]
HARNESS = REPOSITORY_ROOT / "tools" / "hil_smoke.py"
COMMIT = "0123456789abcdef0123456789abcdef01234567"
DEFAULT_CONFIG = REPOSITORY_ROOT / "tools" / "hil_smoke_2h.json"
SOAK_CONFIG = REPOSITORY_ROOT / "tools" / "hil_smoke_24h.json"
DEV_CONFIG = REPOSITORY_ROOT / "tools" / "hil_smoke_dev.json"


def captured(elapsed_seconds: float, event: dict[str, object] | str) -> str:
    line = json.dumps(event, separators=(",", ":")) if isinstance(event, dict) else event
    return json.dumps(
        {
            "captured_at": "2026-08-12T00:00:00Z",
            "elapsed_seconds": elapsed_seconds,
            "line": line,
        },
        separators=(",", ":"),
    )


def setup_event(**overrides: object) -> dict[str, object]:
    event: dict[str, object] = {
        "type": "setup_state",
        "active": True,
        "reason": "boot_long_press",
        "session_id": 4,
        "ssid": "S3-RLCD-A1B2",
        "address": "192.168.4.1",
        "error_stage": "",
        "wifi_config_state": "active",
        "wifi_record_status": "valid",
        "wifi_candidate_record_status": "empty",
        "wifi_has_active": True,
        "wifi_has_candidate": False,
        "wifi_generation": 5,
        "device_settings_state": "active",
        "device_settings_record_status": "valid",
        "device_settings_candidate_record_status": "empty",
        "device_settings_has_active": True,
        "device_settings_has_candidate": False,
        "device_settings_generation": 1,
        "temperature_offset_tenths_c": -40,
    }
    event.update(overrides)
    return event


def peripheral_event(
    key_count: int,
    boot_count: int,
    free_heap: int,
    minimum_heap: int,
) -> dict[str, object]:
    return {
        "type": "peripheral_state",
        "rtc_available": True,
        "rtc_hour": 9,
        "rtc_minute": 41,
        "sensor_available": True,
        "raw_temperature_tenths_c": 237,
        "calibrated_temperature_tenths_c": 197,
        "humidity_tenths_percent": 630,
        "buttons_available": True,
        "key_event": "short_press" if key_count else "none",
        "key_event_count": key_count,
        "boot_event": "long_press" if boot_count else "none",
        "boot_event_count": boot_count,
        "rtc_errors": 0,
        "sensor_errors": 0,
        "free_heap_bytes": free_heap,
        "minimum_free_heap_bytes": minimum_heap,
    }


def display_event(event_type: str, completed: int) -> dict[str, object]:
    return {
        "type": event_type,
        "width": 400,
        "height": 300,
        "frame_bytes": 15000,
        "submitted_frames": completed,
        "completed_frames": completed,
        "transfer_timeouts": 0,
        "start_failures": 0,
        "rejected_updates": 0,
    }


with tempfile.TemporaryDirectory() as temporary_directory:
    default_config = json.loads(DEFAULT_CONFIG.read_text(encoding="utf-8"))
    assert default_config["schema_version"] == 2
    assert default_config["duration_seconds"] == 7200
    assert default_config["heap_warmup_seconds"] == 60
    soak_config = json.loads(SOAK_CONFIG.read_text(encoding="utf-8"))
    assert soak_config["schema_version"] == 2
    assert soak_config["duration_seconds"] == 86400
    assert soak_config["heap_warmup_seconds"] == 60
    dev_config = json.loads(DEV_CONFIG.read_text(encoding="utf-8"))
    assert dev_config["schema_version"] == 2
    assert dev_config["duration_seconds"] == 90
    assert dev_config["heap_warmup_seconds"] == 60
    ignored_result = subprocess.run(
        ["git", "check-ignore", ".hil-results/probe/serial.jsonl"],
        cwd=REPOSITORY_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    assert ignored_result.returncode == 0

    temporary = pathlib.Path(temporary_directory)
    config_path = temporary / "config.json"
    config_path.write_text(
        json.dumps(
            {
                "schema_version": 2,
                "duration_seconds": 7200,
                "heap_warmup_seconds": 60,
                "minimum_display_updates": 3,
                "minimum_peripheral_samples": 3,
                "require_key_event": True,
                "require_boot_event": True,
                "require_setup_cycle": True,
                "require_wifi_failure_recovery": True,
                "maximum_heap_drop_bytes": 8192,
            }
        ),
        encoding="utf-8",
    )
    capture_path = temporary / "capture.jsonl"
    lines = [
        captured(
            0.4,
            {
                "type": "boot_ok",
                "firmware_version": "0.2.0-dev",
                "reset_reason": "software",
                "uptime_ms": 42,
                "minimum_free_heap_bytes": 120000,
            },
        ),
        captured(1, display_event("display_ready", 1)),
        captured(2, peripheral_event(0, 0, 120000, 99000)),
        captured(
            3,
            setup_event(
                session_id=3,
                wifi_config_state="connection_failed",
                wifi_has_candidate=True,
            ),
        ),
        captured(
            4,
            setup_event(
                session_id=3,
                active=False,
                reason="none",
                ssid="",
                wifi_config_state="connection_failed",
                wifi_has_candidate=True,
            ),
        ),
        captured(
            5,
            setup_event(
                session_id=3,
                active=False,
                reason="none",
                ssid="",
                wifi_config_state="active",
            ),
        ),
        captured(10, setup_event(wifi_config_state="validating", wifi_has_candidate=True)),
        captured(
            20,
            setup_event(
                wifi_config_state="connection_failed",
                wifi_has_candidate=True,
            ),
        ),
        captured(
            30,
            setup_event(active=False, reason="none", ssid="", wifi_config_state="active"),
        ),
        captured(
            40,
            setup_event(
                session_id=6,
                wifi_config_state="connection_failed",
                wifi_has_candidate=True,
            ),
        ),
        captured(
            41,
            setup_event(
                session_id=6,
                active=False,
                reason="none",
                ssid="",
                wifi_config_state="connection_failed",
                wifi_has_candidate=True,
            ),
        ),
        captured(
            42,
            setup_event(
                session_id=6,
                active=False,
                reason="none",
                ssid="",
                wifi_config_state="active",
            ),
        ),
        captured(60, display_event("display_progress", 10)),
        captured(61, peripheral_event(1, 1, 99500, 98500)),
        captured(62, "unstructured console value: hunter2"),
        captured(7200, display_event("display_progress", 20)),
        captured(7200, peripheral_event(1, 1, 99000, 98000)),
    ]
    capture_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    result_directory = temporary / "result"
    result = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "replay",
            "--input-file",
            str(capture_path),
            "--config",
            str(config_path),
            "--result-dir",
            str(result_directory),
            "--firmware-commit",
            COMMIT,
            "--toolchain-version",
            "ESP-IDF v6.0.2",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise SystemExit(f"expected valid smoke replay to pass: {result.stderr}")

    summary = json.loads((result_directory / "summary.json").read_text(encoding="utf-8"))
    evidence = (result_directory / "serial.jsonl").read_bytes()
    assert summary["status"] == "passed"
    assert summary["schema_version"] == 2
    assert summary["firmware_commit"] == COMMIT
    assert summary["toolchain_version"] == "ESP-IDF v6.0.2"
    assert summary["duration_seconds"] == 7200
    assert summary["reset_count"] == 0
    assert summary["watchdog_count"] == 0
    assert summary["display_error_count"] == 0
    assert summary["i2c_error_count"] == 0
    assert summary["wifi_error_count"] == 1
    assert summary["minimum_free_heap_bytes"] == 98000
    assert summary["initial_free_heap_bytes"] == 120000
    assert summary["heap_baseline_elapsed_seconds"] == 61
    assert summary["heap_baseline_free_heap_bytes"] == 99500
    assert summary["heap_warmup_seconds"] == 60
    assert summary["heap_drop_bytes"] == 21000
    assert summary["stabilized_heap_drop_bytes"] == 500
    assert summary["display_updates"] == 19
    assert summary["peripheral_samples"] == 3
    assert summary["setup_cycles"] == 3
    assert summary["wifi_failure_recoveries"] == 1
    assert summary["key_event_delta"] == 1
    assert summary["boot_event_delta"] == 1
    assert summary["redacted_line_count"] == 1
    assert summary["raw_log_sha256"] == hashlib.sha256(evidence).hexdigest()
    assert (result_directory / "config.json").is_file()
    assert "password" not in evidence.decode("utf-8").lower()
    assert "hunter2" not in evidence.decode("utf-8")

    interrupted_capture = temporary / "interrupted.jsonl"
    interrupted_capture.write_text(
        "\n".join([captured(0, "[host] observation started"), *lines]) + "\n",
        encoding="utf-8",
    )
    interrupted_directory = temporary / "interrupted-result"
    interrupted = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "replay",
            "--input-file",
            str(interrupted_capture),
            "--config",
            str(config_path),
            "--result-dir",
            str(interrupted_directory),
            "--firmware-commit",
            COMMIT,
            "--toolchain-version",
            "ESP-IDF v6.0.2",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    assert interrupted.returncode != 0
    interrupted_summary = json.loads(
        (interrupted_directory / "summary.json").read_text(encoding="utf-8")
    )
    assert any("did not complete" in value for value in interrupted_summary["failures"])

    extra_failure_capture = temporary / "extra-failure.jsonl"
    runtime_disconnect = [
        captured(
            90,
            setup_event(
                session_id=5,
                reason="no_wifi_config",
                wifi_config_state="connection_failed",
            ),
        ),
        captured(
            110,
            setup_event(
                session_id=5,
                active=False,
                reason="none",
                ssid="",
                wifi_config_state="active",
            ),
        ),
    ]
    extra_failure_capture.write_text(
        "\n".join([*lines[:-2], *runtime_disconnect, *lines[-2:]]) + "\n",
        encoding="utf-8",
    )
    extra_failure_directory = temporary / "extra-failure-result"
    extra_failure = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "replay",
            "--input-file",
            str(extra_failure_capture),
            "--config",
            str(config_path),
            "--result-dir",
            str(extra_failure_directory),
            "--firmware-commit",
            COMMIT,
            "--toolchain-version",
            "ESP-IDF v6.0.2",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    assert extra_failure.returncode != 0
    extra_summary = json.loads(
        (extra_failure_directory / "summary.json").read_text(encoding="utf-8")
    )
    assert extra_summary["wifi_error_count"] == 2
    assert any("additional Wi-Fi" in failure for failure in extra_summary["failures"])

    storage_failure_capture = temporary / "storage-failure.jsonl"
    storage_failure_lines = list(lines)
    storage_failure_lines[3] = captured(
        3,
        setup_event(
            session_id=3,
            wifi_config_state="storage_error",
            error_stage="wifi_load",
        ),
    )
    storage_failure_capture.write_text(
        "\n".join(storage_failure_lines) + "\n", encoding="utf-8"
    )
    storage_failure_directory = temporary / "storage-failure-result"
    storage_failure = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "replay",
            "--input-file",
            str(storage_failure_capture),
            "--config",
            str(config_path),
            "--result-dir",
            str(storage_failure_directory),
            "--firmware-commit",
            COMMIT,
            "--toolchain-version",
            "ESP-IDF v6.0.2",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    assert storage_failure.returncode != 0
    storage_summary = json.loads(
        (storage_failure_directory / "summary.json").read_text(encoding="utf-8")
    )
    assert storage_summary["wifi_error_count"] == 2

    startup_failure_capture = temporary / "startup-failure.jsonl"
    startup_failure_lines = list(lines)
    startup_failure_lines[3] = captured(
        3,
        setup_event(
            session_id=3,
            reason="no_wifi_config",
            wifi_config_state="connection_failed",
            wifi_has_candidate=False,
        ),
    )
    startup_failure_capture.write_text(
        "\n".join(startup_failure_lines) + "\n", encoding="utf-8"
    )
    startup_failure_directory = temporary / "startup-failure-result"
    startup_failure = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "replay",
            "--input-file",
            str(startup_failure_capture),
            "--config",
            str(config_path),
            "--result-dir",
            str(startup_failure_directory),
            "--firmware-commit",
            COMMIT,
            "--toolchain-version",
            "ESP-IDF v6.0.2",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    assert startup_failure.returncode != 0
    startup_failure_summary = json.loads(
        (startup_failure_directory / "summary.json").read_text(encoding="utf-8")
    )
    assert startup_failure_summary["wifi_error_count"] == 2

    chained_storage_capture = temporary / "chained-storage.jsonl"
    chained_storage_lines = list(lines)
    chained_storage_lines.insert(
        8,
        captured(
            21,
            setup_event(
                wifi_config_state="storage_error",
                error_stage="wifi_commit",
                wifi_has_candidate=True,
            ),
        ),
    )
    chained_storage_capture.write_text(
        "\n".join(chained_storage_lines) + "\n", encoding="utf-8"
    )
    chained_storage_directory = temporary / "chained-storage-result"
    chained_storage = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "replay",
            "--input-file",
            str(chained_storage_capture),
            "--config",
            str(config_path),
            "--result-dir",
            str(chained_storage_directory),
            "--firmware-commit",
            COMMIT,
            "--toolchain-version",
            "ESP-IDF v6.0.2",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    assert chained_storage.returncode != 0
    chained_storage_summary = json.loads(
        (chained_storage_directory / "summary.json").read_text(encoding="utf-8")
    )
    assert chained_storage_summary["wifi_error_count"] == 2

    boot_runtime_failure_capture = temporary / "boot-runtime-failure.jsonl"
    boot_runtime_failure = [
        captured(
            90,
            setup_event(
                session_id=7,
                wifi_config_state="active",
                wifi_has_candidate=True,
            ),
        ),
        captured(
            100,
            setup_event(
                session_id=7,
                wifi_config_state="connection_failed",
                wifi_has_candidate=True,
            ),
        ),
        captured(
            110,
            setup_event(
                session_id=7,
                active=False,
                reason="none",
                ssid="",
                wifi_config_state="active",
            ),
        ),
    ]
    boot_runtime_failure_capture.write_text(
        "\n".join([*lines[:-2], *boot_runtime_failure, *lines[-2:]]) + "\n",
        encoding="utf-8",
    )
    boot_runtime_failure_directory = temporary / "boot-runtime-failure-result"
    boot_runtime_result = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "replay",
            "--input-file",
            str(boot_runtime_failure_capture),
            "--config",
            str(config_path),
            "--result-dir",
            str(boot_runtime_failure_directory),
            "--firmware-commit",
            COMMIT,
            "--toolchain-version",
            "ESP-IDF v6.0.2",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    assert boot_runtime_result.returncode != 0
    boot_runtime_summary = json.loads(
        (boot_runtime_failure_directory / "summary.json").read_text(encoding="utf-8")
    )
    assert boot_runtime_summary["wifi_error_count"] == 2

    missing_baseline_config_path = temporary / "missing-baseline-config.json"
    missing_baseline_config = json.loads(config_path.read_text(encoding="utf-8"))
    missing_baseline_config["duration_seconds"] = 30
    missing_baseline_config["heap_warmup_seconds"] = 20
    missing_baseline_config_path.write_text(
        json.dumps(missing_baseline_config), encoding="utf-8"
    )
    missing_baseline_capture = temporary / "missing-baseline.jsonl"
    missing_baseline_lines = [
        captured(0.4, json.loads(json.loads(lines[0])["line"])),
        captured(1, display_event("display_ready", 1)),
        captured(2, peripheral_event(0, 0, 100000, 99000)),
        captured(30, display_event("display_progress", 10)),
    ]
    missing_baseline_capture.write_text(
        "\n".join(missing_baseline_lines) + "\n", encoding="utf-8"
    )
    missing_baseline_directory = temporary / "missing-baseline-result"
    missing_baseline = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "replay",
            "--input-file",
            str(missing_baseline_capture),
            "--config",
            str(missing_baseline_config_path),
            "--result-dir",
            str(missing_baseline_directory),
            "--firmware-commit",
            COMMIT,
            "--toolchain-version",
            "ESP-IDF v6.0.2",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    assert missing_baseline.returncode != 0
    missing_baseline_summary = json.loads(
        (missing_baseline_directory / "summary.json").read_text(encoding="utf-8")
    )
    assert missing_baseline_summary["heap_baseline_elapsed_seconds"] is None
    assert missing_baseline_summary["stabilized_heap_drop_bytes"] is None
    assert any("baseline missing" in failure for failure in missing_baseline_summary["failures"])

    invalid_schema_capture = temporary / "invalid-schema.jsonl"
    invalid_schema_lines = list(lines)
    invalid_display = display_event("display_ready", 1)
    invalid_display["transfer_timeouts"] = "7"
    invalid_peripheral = peripheral_event(0, 0, 100000, 99000)
    del invalid_peripheral["rtc_errors"]
    invalid_schema_lines[1] = captured(1, invalid_display)
    invalid_schema_lines[2] = captured(2, invalid_peripheral)
    unexpected_field_display = display_event("display_progress", 10)
    unexpected_field_display["password"] = "hunter2"
    invalid_schema_lines[12] = captured(60, unexpected_field_display)
    invalid_schema_capture.write_text(
        "\n".join(invalid_schema_lines) + "\n", encoding="utf-8"
    )
    invalid_schema_directory = temporary / "invalid-schema-result"
    invalid_schema = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "replay",
            "--input-file",
            str(invalid_schema_capture),
            "--config",
            str(config_path),
            "--result-dir",
            str(invalid_schema_directory),
            "--firmware-commit",
            COMMIT,
            "--toolchain-version",
            "ESP-IDF v6.0.2",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    assert invalid_schema.returncode != 0
    invalid_schema_summary = json.loads(
        (invalid_schema_directory / "summary.json").read_text(encoding="utf-8")
    )
    assert invalid_schema_summary["diagnostic_schema_error_count"] == 3
    assert any("schema" in failure for failure in invalid_schema_summary["failures"])
    invalid_schema_evidence = (invalid_schema_directory / "serial.jsonl").read_text(
        encoding="utf-8"
    )
    assert "hunter2" not in invalid_schema_evidence

    fake_modules = temporary / "fake-modules"
    (fake_modules / "serial" / "tools").mkdir(parents=True)
    (fake_modules / "serial" / "__init__.py").write_text(
        "import json, os, pathlib, time\n"
        "class SerialException(OSError): pass\n"
        "class Serial:\n"
        "    def __init__(self, **options):\n"
        "        self.device = options['port']\n"
        "        self.timeout = options.get('timeout', 0.01)\n"
        "        self.lines = []\n"
        "    def write(self, data):\n"
        "        if data == b'DECK_IDENTIFY\\n' and os.environ.get('DECK_FAKE_IDENTITY_MODE') != 'unknown':\n"
        "            self.lines.append((json.dumps({'type':'deck_identity','model':'s3-rlcd-deck','protocol':1}) + '\\n').encode())\n"
        "        return len(data)\n"
        "    def readline(self):\n"
        "        if self.lines: return self.lines.pop(0)\n"
        "        time.sleep(min(self.timeout, 0.01))\n"
        "        return b''\n"
        "    def close(self): pass\n",
        encoding="utf-8",
    )
    (fake_modules / "serial" / "tools" / "__init__.py").write_text("", encoding="utf-8")
    (fake_modules / "serial" / "tools" / "list_ports.py").write_text(
        "import os\n"
        "class Port:\n"
        "    def __init__(self, device, vid, manufacturer):\n"
        "        self.device = device\n"
        "        self.vid = vid\n"
        "        self.manufacturer = manufacturer\n"
        "        self.product = 'USB JTAG/serial debug unit'\n"
        "        self.hwid = 'USB VID:PID=303A:1001'\n"
        "def comports():\n"
        "    ports = [Port('/dev/fakeDeck', 0x303A, 'Espressif')]\n"
        "    if os.environ.get('DECK_FAKE_PORT_MODE') == 'multiple':\n"
        "        ports.append(Port('/dev/secondDeck', 0x303A, 'Espressif'))\n"
        "    return ports\n",
        encoding="utf-8",
    )
    environment = os.environ.copy()
    environment["PYTHONPATH"] = str(fake_modules)
    discovered = subprocess.run(
        [sys.executable, str(HARNESS), "discover"],
        check=False,
        capture_output=True,
        text=True,
        env=environment,
    )
    assert discovered.returncode == 0
    assert discovered.stdout.strip() == "/dev/fakeDeck"
    environment["DECK_FAKE_PORT_MODE"] = "multiple"
    ambiguous = subprocess.run(
        [sys.executable, str(HARNESS), "discover"],
        check=False,
        capture_output=True,
        text=True,
        env=environment,
    )
    assert ambiguous.returncode != 0
    assert "multiple" in ambiguous.stderr.lower()
    environment["DECK_FAKE_PORT_MODE"] = "unique"
    environment["DECK_FAKE_IDENTITY_MODE"] = "unknown"
    unrelated_espressif = subprocess.run(
        [sys.executable, str(HARNESS), "discover"],
        check=False,
        capture_output=True,
        text=True,
        env=environment,
    )
    assert unrelated_espressif.returncode != 0
    assert "verified" in unrelated_espressif.stderr.lower()
    del environment["DECK_FAKE_IDENTITY_MODE"]

    plan = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "plan",
            "--config",
            str(config_path),
            "--port",
            "/dev/fakeDeck",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    assert plan.returncode == 0
    planned = json.loads(plan.stdout)
    assert planned == {
        "commands": [
            ["tools/test_host.sh"],
            ["tools/idf.sh", "dev", "build"],
            ["tools/hil_app_flash.py", "--port", "/dev/fakeDeck"],
        ],
        "duration_seconds": 7200.0,
        "port": "/dev/fakeDeck",
    }
    serialized_plan = plan.stdout.lower()
    assert "erase" not in serialized_plan
    assert "efuse" not in serialized_plan
    assert '"flash"' not in serialized_plan

    live_config_path = temporary / "live-config.json"
    live_config = json.loads(config_path.read_text(encoding="utf-8"))
    live_config["duration_seconds"] = 0.1
    live_config["heap_warmup_seconds"] = 0
    live_config["minimum_display_updates"] = 1
    live_config["minimum_peripheral_samples"] = 2
    live_config_path.write_text(json.dumps(live_config), encoding="utf-8")
    fake_serial_lines = temporary / "fake-serial-lines.txt"
    fake_serial_lines.write_text(
        "\n".join(
            [
                json.dumps(
                    {
                        "type": "boot_ok",
                        "firmware_version": "0.2.0-dev",
                        "reset_reason": "software",
                        "uptime_ms": 42,
                        "minimum_free_heap_bytes": 120000,
                    },
                    separators=(",", ":"),
                ),
                json.dumps(display_event("display_ready", 1), separators=(",", ":")),
                json.dumps(
                    peripheral_event(0, 0, 100000, 99000), separators=(",", ":")
                ),
                json.dumps(
                    setup_event(
                        active=False,
                        reason="none",
                        ssid="",
                        wifi_config_state="connection_failed",
                        wifi_has_candidate=True,
                    ),
                    separators=(",", ":"),
                ),
                json.dumps(
                    setup_event(
                        wifi_config_state="connection_failed",
                        wifi_has_candidate=True,
                    ),
                    separators=(",", ":"),
                ),
                json.dumps(
                    setup_event(active=False, reason="none", ssid=""),
                    separators=(",", ":"),
                ),
                json.dumps(
                    setup_event(wifi_config_state="validating"),
                    separators=(",", ":"),
                ),
                json.dumps(
                    setup_event(wifi_config_state="connection_failed"),
                    separators=(",", ":"),
                ),
                json.dumps(
                    setup_event(
                        active=False,
                        reason="none",
                        ssid="",
                        wifi_config_state="active",
                    ),
                    separators=(",", ":"),
                ),
                json.dumps(display_event("display_progress", 2), separators=(",", ":")),
                json.dumps(
                    peripheral_event(1, 1, 99500, 98500), separators=(",", ":")
                ),
            ]
        )
        + "\n",
        encoding="utf-8",
    )
    (fake_modules / "serial" / "__init__.py").write_text(
        "import os, pathlib, time\n"
        "class SerialException(OSError): pass\n"
        "class Serial:\n"
        "    def __init__(self, **options):\n"
        "        if os.environ.get('DECK_FAKE_SERIAL_MODE') == 'open_fail': raise SerialException('fake disconnect')\n"
        "        self.interrupt = os.environ.get('DECK_FAKE_SERIAL_MODE') == 'interrupt'\n"
        "        self.timeout = options.get('timeout', 0.01)\n"
        "        self.write_timeout = options.get('write_timeout', 0.01)\n"
        "        encoded = [line.encode() for line in pathlib.Path(os.environ['DECK_FAKE_SERIAL_FILE']).read_text().splitlines(True)]\n"
        "        self.lines = encoded\n"
        "        if os.environ.get('DECK_FAKE_SERIAL_OVERLONG') == '1':\n"
        "            self.lines = [b'x' * 4097, encoded[0], *encoded[1:]]\n"
        "        if os.environ.get('DECK_FAKE_SERIAL_FRAGMENT') == '1':\n"
        "            self.lines = [part for line in encoded for part in (line[:len(line)//2], line[len(line)//2:])]\n"
        "    def __enter__(self): return self\n"
        "    def __exit__(self, *args): self.close()\n"
        "    def write(self, data):\n"
        "        with pathlib.Path(os.environ['DECK_FAKE_SERIAL_WRITES']).open('ab') as output: output.write(data)\n"
        "        return len(data)\n"
        "    def readline(self):\n"
        "        if self.interrupt:\n"
        "            self.interrupt = False\n"
        "            raise KeyboardInterrupt()\n"
        "        if self.lines: return self.lines.pop(0)\n"
        "        time.sleep(self.timeout)\n"
        "        return b''\n"
        "    def close(self): pass\n",
        encoding="utf-8",
    )
    fake_bin = temporary / "fake-bin"
    fake_bin.mkdir()
    fake_idf = fake_bin / "idf.py"
    fake_idf.write_text("#!/bin/sh\necho 'ESP-IDF v6.0.2'\n", encoding="utf-8")
    fake_idf.chmod(0o755)
    fake_git = fake_bin / "git"
    fake_git.write_text(
        "#!/bin/sh\n"
        "if [ \"$1\" = status ]; then\n"
        "    if [ \"${DECK_FAKE_GIT_DIRTY:-0}\" = 1 ]; then printf ' M tools/fake.py\\n'; fi\n"
        "    exit 0\n"
        "fi\n"
        "if [ \"$1\" = rev-parse ]; then\n"
        f"    echo '{COMMIT}'\n"
        "    exit 0\n"
        "fi\n"
        "exit 1\n",
        encoding="utf-8",
    )
    fake_git.chmod(0o755)
    environment = os.environ.copy()
    environment["PYTHONPATH"] = str(fake_modules)
    environment["PATH"] = f"{fake_bin}{os.pathsep}{environment['PATH']}"
    environment["DECK_FAKE_SERIAL_FILE"] = str(fake_serial_lines)
    fake_serial_writes = temporary / "fake-serial-writes.txt"
    environment["DECK_FAKE_SERIAL_WRITES"] = str(fake_serial_writes)
    environment["DECK_FAKE_GIT_DIRTY"] = "1"
    dirty_result_directory = temporary / "dirty-live-result"
    dirty_live = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "run",
            "--monitor-only",
            "--port",
            "/dev/fakeDeck",
            "--config",
            str(live_config_path),
            "--result-dir",
            str(dirty_result_directory),
        ],
        check=False,
        capture_output=True,
        text=True,
        env=environment,
    )
    assert dirty_live.returncode != 0
    assert "uncommitted" in dirty_live.stderr.lower()

    live_result_directory = temporary / "live-result"
    live = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "run",
            "--monitor-only",
            "--allow-dirty",
            "--port",
            "/dev/fakeDeck",
            "--config",
            str(live_config_path),
            "--result-dir",
            str(live_result_directory),
        ],
        check=False,
        capture_output=True,
        text=True,
        env=environment,
    )
    if live.returncode != 0:
        raise SystemExit(f"expected monitor-only live smoke to pass: {live.stderr}")
    assert "press key" in live.stderr.lower()
    assert "press boot" in live.stderr.lower()
    live_summary = json.loads(
        (live_result_directory / "summary.json").read_text(encoding="utf-8")
    )
    assert live_summary["status"] == "passed"
    assert live_summary["source_dirty"] is True
    assert live_summary["toolchain_version"] == "ESP-IDF v6.0.2"
    assert live_summary["duration_seconds"] >= 0.1
    assert live_summary["raw_log_sha256"] == hashlib.sha256(
        (live_result_directory / "serial.jsonl").read_bytes()
    ).hexdigest()
    live_evidence = [
        json.loads(line)["line"]
        for line in (live_result_directory / "serial.jsonl").read_text(
            encoding="utf-8"
        ).splitlines()
    ]
    first_peripheral = next(
        index
        for index, line in enumerate(live_evidence)
        if line.startswith("{") and json.loads(line).get("type") == "peripheral_state"
    )
    assert live_evidence.index("[host] physical actions requested") > first_peripheral
    writes = fake_serial_writes.read_text(encoding="ascii")
    assert "DECK_HIL_READY\n" in writes
    assert "DECK_SETUP\n" in writes
    assert "DECK_WIFI " in writes and " -\n" in writes

    del environment["DECK_FAKE_GIT_DIRTY"]
    environment["DECK_FAKE_SERIAL_FRAGMENT"] = "1"
    clean_result_directory = temporary / "clean-live-result"
    clean_live = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "run",
            "--monitor-only",
            "--port",
            "/dev/fakeDeck",
            "--config",
            str(live_config_path),
            "--result-dir",
            str(clean_result_directory),
        ],
        check=False,
        capture_output=True,
        text=True,
        env=environment,
    )
    if clean_live.returncode != 0:
        raise SystemExit(f"expected clean monitor-only live smoke to pass: {clean_live.stderr}")
    clean_summary = json.loads(
        (clean_result_directory / "summary.json").read_text(encoding="utf-8")
    )
    assert clean_summary["status"] == "passed"
    assert clean_summary["source_dirty"] is False
    del environment["DECK_FAKE_SERIAL_FRAGMENT"]

    environment["DECK_FAKE_SERIAL_OVERLONG"] = "1"
    overlong_result_directory = temporary / "overlong-live-result"
    overlong_live = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "run",
            "--monitor-only",
            "--port",
            "/dev/fakeDeck",
            "--config",
            str(live_config_path),
            "--result-dir",
            str(overlong_result_directory),
        ],
        check=False,
        capture_output=True,
        text=True,
        env=environment,
    )
    assert overlong_live.returncode != 0
    overlong_summary = json.loads(
        (overlong_result_directory / "summary.json").read_text(encoding="utf-8")
    )
    assert overlong_summary["diagnostic_schema_error_count"] == 1
    overlong_evidence = [
        json.loads(line)["line"]
        for line in (overlong_result_directory / "serial.jsonl").read_text(
            encoding="utf-8"
        ).splitlines()
    ]
    assert overlong_evidence.count("[REDACTED INVALID JSON LINE]") == 1
    assert not any(
        line.startswith("{") and json.loads(line).get("type") == "boot_ok"
        for line in overlong_evidence
    )
    assert any(
        line.startswith("{") and json.loads(line).get("type") == "display_ready"
        for line in overlong_evidence
    )
    del environment["DECK_FAKE_SERIAL_OVERLONG"]

    environment["DECK_FAKE_SERIAL_MODE"] = "interrupt"
    interrupted_live_directory = temporary / "interrupted-live-result"
    interrupted_live = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "run",
            "--monitor-only",
            "--port",
            "/dev/fakeDeck",
            "--config",
            str(live_config_path),
            "--result-dir",
            str(interrupted_live_directory),
        ],
        check=False,
        capture_output=True,
        text=True,
        env=environment,
    )
    assert interrupted_live.returncode != 0
    interrupted_live_summary = json.loads(
        (interrupted_live_directory / "summary.json").read_text(encoding="utf-8")
    )
    assert interrupted_live_summary["status"] == "failed"
    assert "monitor interrupted" in interrupted_live_summary["failures"]
    assert (interrupted_live_directory / "config.json").is_file()
    assert (interrupted_live_directory / "serial.jsonl").is_file()

    environment["DECK_FAKE_SERIAL_MODE"] = "open_fail"
    failed_result_directory = temporary / "failed-live-result"
    failed_live = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "run",
            "--monitor-only",
            "--allow-dirty",
            "--port",
            "/dev/fakeDeck",
            "--config",
            str(live_config_path),
            "--result-dir",
            str(failed_result_directory),
        ],
        check=False,
        capture_output=True,
        text=True,
        env=environment,
    )
    assert failed_live.returncode != 0
    failed_summary = json.loads(
        (failed_result_directory / "summary.json").read_text(encoding="utf-8")
    )
    failed_evidence = (failed_result_directory / "serial.jsonl").read_bytes()
    assert failed_summary["status"] == "failed"
    assert any("fake disconnect" in failure for failure in failed_summary["failures"])
    assert failed_summary["raw_log_sha256"] == hashlib.sha256(failed_evidence).hexdigest()
    assert (failed_result_directory / "config.json").is_file()
