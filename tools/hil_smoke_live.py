#!/usr/bin/env python3

"""Hardware discovery, preparation, and monitoring for Deck HIL smoke runs."""

import json
import os
import pathlib
import signal
import subprocess
import sys
import time
from typing import Any, TextIO

from hil_smoke_contract import SmokeFailure, parsed_event, sanitize_line, valid_event_schema


REPOSITORY_ROOT = pathlib.Path(__file__).resolve().parents[1]
HIL_READY = b"DECK_HIL_READY\n"
DECK_IDENTIFY = b"DECK_IDENTIFY\n"
DECK_IDENTITY = {"type": "deck_identity", "model": "s3-rlcd-deck", "protocol": 1}
IDENTITY_TIMEOUT_SECONDS = 5.0
PREPARATION_TIMEOUT_SECONDS = 20 * 60
TERMINATION_GRACE_SECONDS = 5.0


def candidate_ports() -> list[str]:
    try:
        from serial.tools import list_ports
    except ImportError as error:
        raise SmokeFailure(
            "Deck discovery requires pyserial from the ESP-IDF environment"
        ) from error
    candidates: list[str] = []
    for port in list_ports.comports():
        description = " ".join(
            str(value or "")
            for value in (
                getattr(port, "manufacturer", ""),
                getattr(port, "product", ""),
                getattr(port, "hwid", ""),
            )
        ).lower()
        if getattr(port, "vid", None) == 0x303A or "espressif" in description:
            candidates.append(str(port.device))
    return candidates


def probe_deck_identity(port: str, timeout_seconds: float = IDENTITY_TIMEOUT_SECONDS) -> bool:
    try:
        import serial
    except ImportError as error:
        raise SmokeFailure(
            "Deck discovery requires pyserial from the ESP-IDF environment"
        ) from error
    io_timeout = min(0.1, timeout_seconds)
    try:
        connection = serial.Serial(
            port=port,
            baudrate=115200,
            timeout=io_timeout,
            write_timeout=io_timeout,
        )
    except (OSError, serial.SerialException):
        return False
    write_timeout = getattr(serial, "SerialTimeoutException", TimeoutError)
    try:
        deadline = time.monotonic() + timeout_seconds
        next_probe = 0.0
        while time.monotonic() < deadline:
            now = time.monotonic()
            if now >= next_probe:
                try:
                    connection.write(HIL_READY)
                    connection.write(DECK_IDENTIFY)
                except (OSError, write_timeout):
                    pass
                next_probe = now + 0.25
            line = connection.readline().decode("utf-8", errors="replace")
            event = parsed_event(line)
            if event == DECK_IDENTITY:
                return True
        return False
    except OSError:
        return False
    finally:
        connection.close()


def discover_deck_port() -> str:
    candidates = candidate_ports()
    if not candidates:
        raise SmokeFailure("no matching Espressif serial port found")
    verified = [port for port in candidates if probe_deck_identity(port)]
    if not verified:
        raise SmokeFailure(
            "no verified s3-rlcd-deck found; pass an explicit --port for the initial flash"
        )
    if len(verified) != 1:
        raise SmokeFailure(
            "multiple verified Deck serial ports found; pass an explicit --port"
        )
    return verified[0]


def run_plan(port: str, duration_seconds: float) -> dict[str, Any]:
    return {
        "commands": [
            ["tools/test_host.sh"],
            ["tools/idf.sh", "dev", "build"],
            ["tools/hil_app_flash.py", "--port", port],
        ],
        "duration_seconds": duration_seconds,
        "port": port,
    }


def command_output(arguments: list[str]) -> str:
    try:
        result = subprocess.run(
            arguments,
            cwd=REPOSITORY_ROOT,
            check=False,
            capture_output=True,
            text=True,
            timeout=30,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise SmokeFailure(f"cannot run {arguments[0]}: {error}") from error
    if result.returncode != 0:
        raise SmokeFailure(
            f"command failed ({' '.join(arguments)}): {(result.stderr or result.stdout).strip()}"
        )
    return result.stdout.strip()


def firmware_commit() -> str:
    return command_output(["git", "rev-parse", "HEAD"])


def source_tree_clean() -> bool:
    status = command_output(
        [
            "git",
            "status",
            "--porcelain",
            "--untracked-files=all",
            "--",
            "firmware",
            "tools",
            "tests",
            "cmake",
        ]
    )
    return status == ""


def toolchain_version() -> str:
    version = command_output(["idf.py", "--version"])
    if version != "ESP-IDF v6.0.2":
        raise SmokeFailure(f"HIL requires ESP-IDF v6.0.2, found: {version}")
    return version


def _stop_process_group(process: subprocess.Popen[str]) -> None:
    if process.poll() is not None:
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
        process.wait(timeout=TERMINATION_GRACE_SECONDS)
    except (ProcessLookupError, subprocess.TimeoutExpired):
        if process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait()


def _run_preparation_command(arguments: list[str], output: TextIO) -> None:
    try:
        process = subprocess.Popen(
            arguments,
            cwd=REPOSITORY_ROOT,
            stdout=output,
            stderr=subprocess.STDOUT,
            text=True,
            start_new_session=True,
        )
    except OSError as error:
        raise SmokeFailure(f"cannot run {arguments[0]}: {error}") from error
    try:
        try:
            return_code = process.wait(timeout=PREPARATION_TIMEOUT_SECONDS)
        except subprocess.TimeoutExpired as error:
            raise SmokeFailure(
                f"preparation command timed out after {PREPARATION_TIMEOUT_SECONDS}s: "
                f"{' '.join(arguments)}"
            ) from error
    finally:
        if process.poll() is None:
            _stop_process_group(process)
    if return_code != 0:
        raise SmokeFailure(f"preparation command failed: {' '.join(arguments)}")


def prepare_firmware(port: str, preparation_log: pathlib.Path) -> None:
    plan = run_plan(port, 0)["commands"]
    try:
        output = preparation_log.open("w", encoding="utf-8")
    except OSError as error:
        raise SmokeFailure(f"cannot open preparation log: {error}") from error
    with output:
        for relative_command in plan:
            executable = REPOSITORY_ROOT / relative_command[0]
            command = [str(executable), *relative_command[1:]]
            output.write(f"$ {' '.join(relative_command)}\n")
            output.flush()
            _run_preparation_command(command, output)


def utc_now() -> str:
    import datetime

    return datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")


def _write_capture_line(
    capture: TextIO, started: float, line: str, elapsed_seconds: float | None = None
) -> None:
    sanitized, _ = sanitize_line(line)
    envelope = {
        "captured_at": utc_now(),
        "elapsed_seconds": (
            time.monotonic() - started if elapsed_seconds is None else elapsed_seconds
        ),
        "line": sanitized,
    }
    capture.write(json.dumps(envelope, separators=(",", ":"), ensure_ascii=False) + "\n")
    capture.flush()


def monitor_live(
    port: str,
    duration_seconds: float,
    capture_path: pathlib.Path,
    require_key_event: bool,
    require_boot_event: bool,
) -> None:
    try:
        import serial
    except ImportError as error:
        raise SmokeFailure("live HIL requires pyserial from the ESP-IDF environment") from error
    serial_timeout = min(0.25, duration_seconds)
    try:
        connection = serial.Serial(
            port=port,
            baudrate=115200,
            timeout=serial_timeout,
            write_timeout=serial_timeout,
        )
    except (OSError, serial.SerialException) as error:
        raise SmokeFailure(f"cannot open Deck serial port: {error}") from error
    started = time.monotonic()
    deadline = started + duration_seconds
    write_timeout = getattr(serial, "SerialTimeoutException", TimeoutError)
    setup_requested = False
    wifi_failure_submitted = False
    physical_actions_requested = False
    missing_ssid = f"DECK-HIL-MISSING-{time.monotonic_ns() & 0xffffffff:08x}"
    wifi_failure_command = f"DECK_WIFI {missing_ssid.encode('ascii').hex()} -\n".encode("ascii")
    try:
        with capture_path.open("w", encoding="utf-8") as capture:
            _write_capture_line(capture, started, "[host] observation started", 0.0)
            next_ready = started
            while time.monotonic() < deadline:
                now = time.monotonic()
                if now >= next_ready:
                    try:
                        connection.write(HIL_READY)
                    except (OSError, write_timeout):
                        pass
                    next_ready = now + 0.5
                raw_line = connection.readline()
                if not raw_line:
                    continue
                decoded = raw_line.decode("utf-8", errors="replace").rstrip("\r\n")
                sanitized, _ = sanitize_line(decoded)
                _write_capture_line(capture, started, sanitized)
                event = parsed_event(sanitized)
                if (
                    not physical_actions_requested
                    and event is not None
                    and event.get("type") == "peripheral_state"
                    and valid_event_schema(event)
                ):
                    requested_actions: list[str] = []
                    if require_key_event:
                        requested_actions.append("press KEY at least once")
                    if require_boot_event:
                        requested_actions.append(
                            "press BOOT long enough to register a long press"
                        )
                    if requested_actions:
                        _write_capture_line(
                            capture, started, "[host] physical actions requested"
                        )
                        print(
                            "Physical HIL action required during the run: "
                            + "; ".join(requested_actions),
                            file=sys.stderr,
                        )
                    physical_actions_requested = True
                if event is None or event.get("type") != "setup_state":
                    continue
                has_active = event.get("wifi_has_active") is True
                wifi_active = event.get("wifi_config_state") == "active"
                if (
                    event.get("active") is False
                    and has_active
                    and wifi_active
                    and not setup_requested
                ):
                    try:
                        connection.write(b"DECK_SETUP\n")
                        setup_requested = True
                    except (OSError, write_timeout):
                        pass
                elif event.get("active") is True and has_active and wifi_active:
                    setup_requested = True
                    if not wifi_failure_submitted:
                        try:
                            connection.write(wifi_failure_command)
                            wifi_failure_submitted = True
                        except (OSError, write_timeout):
                            pass
            _write_capture_line(
                capture,
                started,
                "[host] observation complete",
                max(duration_seconds, time.monotonic() - started),
            )
    finally:
        connection.close()
