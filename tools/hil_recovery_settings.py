#!/usr/bin/env python3

import argparse
import decimal
import json
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Callable


HIL_READY = b"DECK_HIL_READY\n"
FATAL_MARKERS = (
    "task_wdt:",
    "guru meditation error",
    "panic'ed",
    "assert failed",
)
SETTINGS_STATES = {"default", "active", "storage_error"}
RECORD_STATES = {
    "empty",
    "valid",
    "recovered_previous",
    "corrupt",
    "unsupported_schema",
    "migration_failed",
    "io_error",
}


class HilFailure(RuntimeError):
    pass


def parse_offset(value: str) -> int:
    try:
        parsed = decimal.Decimal(value)
    except decimal.InvalidOperation as error:
        raise ValueError("offset must be a decimal number") from error
    if not parsed.is_finite() or parsed < decimal.Decimal("-15.0") or parsed > decimal.Decimal("15.0"):
        raise ValueError("offset must be between -15.0 and +15.0 C")
    tenths = parsed * 10
    if tenths != tenths.to_integral_value():
        raise ValueError("offset must be exactly representable in 0.1 C")
    return int(tenths)


def valid_setup_event(event: dict[str, Any]) -> bool:
    required = {
        "active": bool,
        "reason": str,
        "session_id": int,
        "ssid": str,
        "address": str,
        "error_stage": str,
        "wifi_has_active": bool,
        "wifi_has_candidate": bool,
        "wifi_generation": int,
        "device_settings_state": str,
        "device_settings_record_status": str,
        "device_settings_candidate_record_status": str,
        "device_settings_has_active": bool,
        "device_settings_has_candidate": bool,
        "device_settings_generation": int,
        "temperature_offset_tenths_c": int,
    }
    if event.get("type") != "setup_state" or any(
        type(event.get(name)) is not expected for name, expected in required.items()
    ):
        return False
    if "password" in event or event["error_stage"] != "":
        return False
    return (
        event["address"] == "192.168.4.1"
        and event["session_id"] >= 0
        and event["wifi_generation"] >= 0
        and event["device_settings_generation"] >= 0
        and event["device_settings_state"] in SETTINGS_STATES
        and event["device_settings_record_status"] in RECORD_STATES
        and event["device_settings_candidate_record_status"] in RECORD_STATES
        and -150 <= event["temperature_offset_tenths_c"] <= 150
    )


def valid_http_status(value: dict[str, Any]) -> bool:
    if not isinstance(value, dict) or type(value.get("active")) is not bool:
        return False
    wifi = value.get("wifi")
    settings = value.get("device_settings")
    if not isinstance(wifi, dict) or not isinstance(settings, dict):
        return False
    if any(name in wifi or name in settings for name in ("password", "token")):
        return False
    wifi_required = {
        "has_active": bool,
        "has_candidate": bool,
        "generation": int,
    }
    settings_required = {
        "state": str,
        "record": str,
        "candidate_record": str,
        "has_active": bool,
        "has_candidate": bool,
        "generation": int,
        "temperature_offset_tenths_c": int,
    }
    return (
        all(type(wifi.get(name)) is expected for name, expected in wifi_required.items())
        and all(
            type(settings.get(name)) is expected
            for name, expected in settings_required.items()
        )
        and settings["state"] in SETTINGS_STATES
        and settings["record"] in RECORD_STATES
        and settings["candidate_record"] in RECORD_STATES
        and -150 <= settings["temperature_offset_tenths_c"] <= 150
    )


def peripheral_matches_offset(event: dict[str, Any], offset_tenths_c: int) -> bool:
    return (
        event.get("type") == "peripheral_state"
        and event.get("sensor_available") is True
        and type(event.get("raw_temperature_tenths_c")) is int
        and type(event.get("calibrated_temperature_tenths_c")) is int
        and event["calibrated_temperature_tenths_c"]
        == event["raw_temperature_tenths_c"] + offset_tenths_c
    )


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
        if any(marker in line.lower() for marker in FATAL_MARKERS):
            raise HilFailure(f"{stage}: fatal Deck log observed")
        candidate = line.strip()
        if not candidate.startswith("{"):
            continue
        try:
            event = json.loads(candidate)
        except json.JSONDecodeError:
            continue
        if event.get("type") == "setup_state" and not valid_setup_event(event):
            raise HilFailure(f"{stage}: invalid or sensitive diagnostic event")
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


def http_json(
    base_url: str,
    method: str,
    path: str,
    fields: dict[str, str] | None,
    timeout_seconds: float,
) -> tuple[int, dict[str, Any]]:
    data = None if fields is None else urllib.parse.urlencode(fields).encode("ascii")
    request = urllib.request.Request(
        base_url.rstrip("/") + path,
        data=data,
        method=method,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout_seconds) as response:
            return response.status, json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as error:
        try:
            body = json.loads(error.read().decode("utf-8"))
        except (UnicodeError, json.JSONDecodeError):
            body = {"error": "invalid_http_error"}
        return error.code, body


def wait_http_status(base_url: str, timeout_seconds: float) -> dict[str, Any]:
    deadline = time.monotonic() + timeout_seconds
    while time.monotonic() < deadline:
        try:
            code, status = http_json(base_url, "GET", "/api/status", None, 1.0)
            if code == 200 and valid_http_status(status):
                return status
        except (OSError, urllib.error.URLError, json.JSONDecodeError):
            pass
        time.sleep(0.25)
    raise HilFailure(
        "recovery HTTP unavailable; connect the Mac to the Setup SSID/password shown on the Deck"
    )


def restart(serial_module: Any, connection: Any, port: str, timeout_seconds: float) -> Any:
    connection.write(b"DECK_RESTART\n")
    connection.close()
    time.sleep(0.75)
    return open_serial(serial_module, port, time.monotonic() + timeout_seconds)


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Validate recovery-page temperature calibration and confirmed Wi-Fi clear. "
            "The Mac must be connected manually to the Deck Setup AP."
        )
    )
    parser.add_argument("--port", required=True)
    parser.add_argument("--base-url", default="http://192.168.4.1")
    parser.add_argument("--offset", default="-3.5")
    parser.add_argument("--stage-timeout", type=float, default=60.0)
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    if arguments.stage_timeout <= 0:
        print("stage timeout must be greater than zero", file=sys.stderr)
        return 2
    try:
        offset_tenths_c = parse_offset(arguments.offset)
        import serial

        connection = open_serial(
            serial,
            arguments.port,
            time.monotonic() + arguments.stage_timeout,
        )
        try:
            initial = read_event(
                connection,
                lambda event: event.get("type") == "setup_state",
                arguments.stage_timeout,
                "initial state",
            )
            if not initial["active"]:
                connection.write(b"DECK_SETUP\n")
                initial = read_event(
                    connection,
                    lambda event: event.get("type") == "setup_state"
                    and event.get("active") is True,
                    arguments.stage_timeout,
                    "enter Setup Mode",
                )
            print(
                "Setup Mode is active; connect this Mac to the SSID/password shown on the Deck.",
                file=sys.stderr,
            )
            status = wait_http_status(arguments.base_url, arguments.stage_timeout)
            if not status["wifi"]["has_active"]:
                raise HilFailure("Wi-Fi clear smoke requires an existing active Wi-Fi record")
            initial_settings_generation = status["device_settings"]["generation"]

            code, response = http_json(
                arguments.base_url,
                "POST",
                "/api/temperature",
                {"offset": arguments.offset},
                2.0,
            )
            if code != 202 or response.get("accepted") is not True:
                raise HilFailure("temperature submission was not accepted")
            deadline = time.monotonic() + arguments.stage_timeout
            while True:
                status = wait_http_status(arguments.base_url, min(2.0, arguments.stage_timeout))
                settings = status["device_settings"]
                if (
                    settings["state"] == "active"
                    and settings["generation"] > initial_settings_generation
                    and settings["temperature_offset_tenths_c"] == offset_tenths_c
                ):
                    settings_generation = settings["generation"]
                    break
                if time.monotonic() >= deadline:
                    raise HilFailure("temperature offset did not commit")
            read_event(
                connection,
                lambda event: peripheral_matches_offset(event, offset_tenths_c),
                arguments.stage_timeout,
                "calibrated peripheral state",
            )

            code, response = http_json(
                arguments.base_url,
                "POST",
                "/api/wifi/clear/request",
                None,
                2.0,
            )
            token = response.get("token")
            if code != 200 or not isinstance(token, str) or len(token) != 16:
                raise HilFailure("Wi-Fi clear confirmation was not issued")
            wrong_token = token[:-1] + ("0" if token[-1] != "0" else "1")
            wrong_code, wrong_response = http_json(
                arguments.base_url,
                "POST",
                "/api/wifi/clear/confirm",
                {"token": wrong_token},
                2.0,
            )
            if wrong_code != 403 or wrong_response.get("error") != "confirmation_mismatch":
                raise HilFailure("incorrect confirmation token was not rejected")
            code, response = http_json(
                arguments.base_url,
                "POST",
                "/api/wifi/clear/confirm",
                {"token": token},
                2.0,
            )
            token = ""
            if code != 202 or response.get("accepted") is not True:
                raise HilFailure("confirmed Wi-Fi clear was not accepted")

            deadline = time.monotonic() + arguments.stage_timeout
            while True:
                status = wait_http_status(arguments.base_url, min(2.0, arguments.stage_timeout))
                if (
                    status["active"] is True
                    and status["wifi"]["has_active"] is False
                    and status["wifi"]["has_candidate"] is False
                ):
                    break
                if time.monotonic() >= deadline:
                    raise HilFailure("confirmed Wi-Fi records were not cleared")

            connection = restart(serial, connection, arguments.port, arguments.stage_timeout)
            read_event(
                connection,
                lambda event: event.get("type") == "setup_state"
                and event.get("active") is True
                and event.get("wifi_has_active") is False
                and event.get("wifi_has_candidate") is False
                and event.get("device_settings_generation") == settings_generation
                and event.get("temperature_offset_tenths_c") == offset_tenths_c,
                arguments.stage_timeout,
                "post-clear reboot recovery",
            )
            read_event(
                connection,
                lambda event: peripheral_matches_offset(event, offset_tenths_c),
                arguments.stage_timeout,
                "post-reboot calibrated peripheral state",
            )
        finally:
            connection.close()
    except (HilFailure, OSError, UnicodeError, ValueError, urllib.error.URLError) as error:
        print(f"recovery settings HIL failed: {error}", file=sys.stderr)
        return 1

    print(
        "recovery settings observed: "
        f"offset_tenths_c={offset_tenths_c} settings_generation={settings_generation} "
        "wrong_confirmation_rejected=true wifi_cleared=true reboot_setup=true"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
