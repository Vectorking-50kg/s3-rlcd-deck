#!/usr/bin/env python3

import argparse
import json
import os
import sys
import time
from typing import Any, Callable


HIL_READY = b"DECK_HIL_READY\n"
FATAL_MARKERS = (
    "task_wdt:",
    "guru meditation error",
    "panic'ed",
    "assert failed",
)
WIFI_STATES = {
    "no_active",
    "active",
    "validating",
    "auth_failed",
    "timed_out",
    "connection_failed",
    "storage_error",
}
RECORD_STATES = {
    "empty",
    "valid",
    "recovered_previous",
    "corrupt",
    "unsupported_schema",
    "migration_failed",
    "io_error",
}
RUNTIME_DIAGNOSTIC_TYPES = {
    "boot_ok",
    "display_ready",
    "display_progress",
    "peripheral_state",
    "setup_state",
}


class HilFailure(RuntimeError):
    pass


def valid_wifi_transaction_event(event: dict[str, Any]) -> bool:
    required = {
        "active": bool,
        "reason": str,
        "session_id": int,
        "ssid": str,
        "address": str,
        "error_stage": str,
        "wifi_config_state": str,
        "wifi_record_status": str,
        "wifi_candidate_record_status": str,
        "wifi_has_active": bool,
        "wifi_has_candidate": bool,
        "wifi_generation": int,
    }
    if event.get("type") != "setup_state" or any(
        type(event.get(name)) is not expected for name, expected in required.items()
    ):
        return False
    if any(name in event for name in ("password", "active_ssid", "candidate_ssid")):
        return False
    return (
        event["address"] == "192.168.4.1"
        and event["error_stage"] == ""
        and event["wifi_config_state"] in WIFI_STATES
        and event["wifi_record_status"] in RECORD_STATES
        and event["wifi_candidate_record_status"] in RECORD_STATES
        and event["wifi_generation"] >= 0
        and event["session_id"] >= 0
    )


def wifi_command(ssid: str, password: str) -> bytes:
    ssid_bytes = ssid.encode("utf-8")
    password_bytes = password.encode("utf-8")
    if not 1 <= len(ssid_bytes) <= 32:
        raise HilFailure("test AP SSID must be 1..32 UTF-8 bytes")
    if len(password_bytes) != 0 and not 8 <= len(password_bytes) <= 63:
        raise HilFailure("test AP password must be empty or 8..63 UTF-8 bytes")
    password_field = password_bytes.hex() if password_bytes else "-"
    return f"DECK_WIFI {ssid_bytes.hex()} {password_field}\n".encode("ascii")


def read_event(
    connection: Any,
    predicate: Callable[[dict[str, Any]], bool],
    timeout_seconds: float,
    stage: str,
) -> dict[str, Any]:
    deadline = time.monotonic() + timeout_seconds
    next_ready = 0.0
    while time.monotonic() < deadline:
        now = time.monotonic()
        if now >= next_ready:
            try:
                connection.write(HIL_READY)
            except OSError:
                pass
            next_ready = now + 0.5
        raw_line = connection.readline()
        if not raw_line:
            continue
        line = raw_line.decode("utf-8", errors="replace")
        normalized = line.lower()
        if any(marker in normalized for marker in FATAL_MARKERS):
            raise HilFailure(f"{stage}: fatal Deck log observed")
        candidate = line.strip()
        if not candidate.startswith("{"):
            continue
        try:
            event = json.loads(candidate)
        except json.JSONDecodeError:
            continue
        if event.get("type") == "setup_state" and not valid_wifi_transaction_event(event):
            raise HilFailure(f"{stage}: invalid or credential-bearing diagnostic event")
        if predicate(event):
            return event
    raise HilFailure(f"{stage}: timed out waiting for expected Deck state")


def open_serial(serial_module: Any, port: str, deadline: float) -> Any:
    while time.monotonic() < deadline:
        try:
            return serial_module.Serial(
                port=port,
                baudrate=115200,
                timeout=0.25,
                write_timeout=0.25,
            )
        except (OSError, serial_module.SerialException):
            time.sleep(0.25)
    raise HilFailure("serial port did not return after Deck restart")


def restart(serial_module: Any, connection: Any, port: str, timeout_seconds: float) -> Any:
    connection.write(b"DECK_RESTART\n")
    connection.close()
    time.sleep(0.75)
    return open_serial(serial_module, port, time.monotonic() + timeout_seconds)


def ensure_setup_active(connection: Any, event: dict[str, Any], timeout_seconds: float) -> dict[str, Any]:
    if event["active"]:
        return event
    connection.write(b"DECK_SETUP\n")
    return read_event(
        connection,
        lambda current: current.get("type") == "setup_state" and current.get("active") is True,
        timeout_seconds,
        "enter Setup Mode",
    )


def enter_setup_session(connection: Any, timeout_seconds: float) -> dict[str, Any]:
    """Start a fresh Setup session without relying on a one-shot startup event."""
    read_event(
        connection,
        lambda current: current.get("type") in RUNTIME_DIAGNOSTIC_TYPES,
        timeout_seconds,
        "diagnostic runtime readiness",
    )
    connection.write(b"DECK_SETUP\n")
    return read_event(
        connection,
        lambda current: current.get("type") == "setup_state"
        and current.get("active") is True,
        timeout_seconds,
        "enter Setup Mode",
    )


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Validate transactional Deck Wi-Fi activation without switching the Mac Wi-Fi."
    )
    parser.add_argument("--port", required=True)
    parser.add_argument("--ssid-env", default="DECK_HIL_WIFI_SSID")
    parser.add_argument("--password-env", default="DECK_HIL_WIFI_PASSWORD")
    parser.add_argument("--stage-timeout", type=float, default=30.0)
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    if arguments.stage_timeout <= 0:
        print("stage timeout must be greater than zero", file=sys.stderr)
        return 2
    ssid = os.environ.get(arguments.ssid_env)
    password = os.environ.get(arguments.password_env)
    if ssid is None or password is None:
        print(
            f"set {arguments.ssid_env} and {arguments.password_env} for the temporary HIL AP",
            file=sys.stderr,
        )
        return 2
    try:
        import serial

        correct_command = wifi_command(ssid, password)
        missing_ssid = f"DECK-HIL-MISSING-{os.urandom(4).hex()}"
        failure_command = wifi_command(missing_ssid, "not-a-real-key")
        connection = open_serial(
            serial,
            arguments.port,
            time.monotonic() + arguments.stage_timeout,
        )
        try:
            active_setup = enter_setup_session(connection, arguments.stage_timeout)
            initial_generation = active_setup["wifi_generation"]

            connection.write(correct_command)
            activated_predicate = lambda event: (
                event.get("type") == "setup_state"
                and event.get("active") is False
                and event.get("wifi_config_state") == "active"
                and event.get("wifi_has_active") is True
                and event.get("wifi_generation", -1) > initial_generation
            )
            activation_progress = read_event(
                connection,
                lambda event: event.get("type") == "setup_state"
                and (
                    event.get("wifi_config_state") == "validating"
                    or activated_predicate(event)
                ),
                arguments.stage_timeout,
                "candidate validation start",
            )
            activated = (
                activation_progress
                if activated_predicate(activation_progress)
                else read_event(
                    connection,
                    activated_predicate,
                    arguments.stage_timeout,
                    "candidate activation",
                )
            )
            generation = activated["wifi_generation"]

            connection = restart(serial, connection, arguments.port, arguments.stage_timeout)
            recovered = read_event(
                connection,
                lambda event: event.get("type") == "setup_state"
                and event.get("active") is False
                and event.get("wifi_config_state") == "active"
                and event.get("wifi_has_active") is True
                and event.get("wifi_generation") == generation,
                arguments.stage_timeout,
                "active reboot recovery",
            )
            active_setup = ensure_setup_active(connection, recovered, arguments.stage_timeout)
            if active_setup["wifi_generation"] != generation:
                raise HilFailure("Setup Mode changed the active generation")

            connection.write(failure_command)
            failure_predicate = lambda event: (
                event.get("type") == "setup_state"
                and event.get("active") is True
                and event.get("wifi_config_state")
                in {"auth_failed", "timed_out", "connection_failed"}
                and event.get("wifi_has_active") is True
                and event.get("wifi_generation") == generation
            )
            failure_progress = read_event(
                connection,
                lambda event: event.get("type") == "setup_state"
                and (
                    event.get("wifi_config_state") == "validating"
                    or failure_predicate(event)
                ),
                arguments.stage_timeout,
                "failed candidate validation start",
            )
            if not failure_predicate(failure_progress):
                read_event(
                    connection,
                    failure_predicate,
                    arguments.stage_timeout,
                    "failed candidate preservation",
                )

            connection = restart(serial, connection, arguments.port, arguments.stage_timeout)
            read_event(
                connection,
                lambda event: event.get("type") == "setup_state"
                and event.get("active") is True
                and event.get("wifi_has_active") is True
                and event.get("wifi_has_candidate") is True
                and event.get("wifi_generation") == generation,
                arguments.stage_timeout,
                "post-failure recovery page",
            )
            read_event(
                connection,
                lambda event: event.get("type") == "setup_state"
                and event.get("active") is False
                and event.get("wifi_config_state") == "active"
                and event.get("wifi_has_active") is True
                and event.get("wifi_generation") == generation,
                arguments.stage_timeout,
                "post-failure active reconnect",
            )
        finally:
            connection.close()
    except (HilFailure, OSError, UnicodeError) as error:
        print(f"wifi transaction HIL failed: {error}", file=sys.stderr)
        return 1

    print(
        "wifi transaction observed: "
        f"generation={generation} activation=true failed_candidate_preserved=true "
        "recovery_page=true reboot_recovery=true"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
