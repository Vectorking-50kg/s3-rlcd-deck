#!/usr/bin/env python3

import argparse
import json
import pathlib
import re
import sys
import time
from collections.abc import Iterable
from typing import Any

HIL_READY = b"DECK_HIL_READY\n"
HIL_READY_INTERVAL_SECONDS = 0.5
DISPLAY_STABILITY_WINDOW_SECONDS = 20.0
SERIAL_IO_TIMEOUT_SECONDS = 0.25


def valid_boot_event(event: dict[str, Any]) -> bool:
    required = {
        "firmware_version": str,
        "reset_reason": str,
        "uptime_ms": int,
        "minimum_free_heap_bytes": int,
    }
    return event.get("type") == "boot_ok" and all(
        isinstance(event.get(name), expected) for name, expected in required.items()
    )


def display_event_has_fields(event: dict[str, Any], event_type: str) -> bool:
    required = {
        "width": int,
        "height": int,
        "frame_bytes": int,
        "submitted_frames": int,
        "completed_frames": int,
        "transfer_timeouts": int,
        "start_failures": int,
        "rejected_updates": int,
    }
    return event.get("type") == event_type and all(
        isinstance(event.get(name), expected) for name, expected in required.items()
    )


def valid_display_event(
    event: dict[str, Any] | None, event_type: str, minimum_frames: int
) -> bool:
    if event is None or not display_event_has_fields(event, event_type):
        return False
    return (
        event["width"] == 400
        and event["height"] == 300
        and event["frame_bytes"] == 15000
        and event["submitted_frames"] >= minimum_frames
        and event["completed_frames"] >= minimum_frames
        and event["completed_frames"] <= event["submitted_frames"]
        and event["transfer_timeouts"] == 0
        and event["start_failures"] == 0
        and event["rejected_updates"] == 0
    )


def peripheral_event_has_fields(event: dict[str, Any]) -> bool:
    required = {
        "rtc_available": bool,
        "rtc_hour": int,
        "rtc_minute": int,
        "sensor_available": bool,
        "raw_temperature_tenths_c": int,
        "calibrated_temperature_tenths_c": int,
        "humidity_tenths_percent": int,
        "buttons_available": bool,
        "key_event": str,
        "key_event_count": int,
        "boot_event": str,
        "boot_event_count": int,
        "rtc_errors": int,
        "sensor_errors": int,
    }
    if event.get("type") != "peripheral_state":
        return False
    return all(type(event.get(name)) is expected for name, expected in required.items())


def valid_peripheral_event(event: dict[str, Any] | None) -> bool:
    if event is None or not peripheral_event_has_fields(event):
        return False
    rtc_valid = not event["rtc_available"] or (
        0 <= event["rtc_hour"] <= 23 and 0 <= event["rtc_minute"] <= 59
    )
    return (
        rtc_valid
        and event["sensor_available"]
        and event["buttons_available"]
        and -400 <= event["raw_temperature_tenths_c"] <= 1250
        and -440 <= event["calibrated_temperature_tenths_c"] <= 1210
        and event["calibrated_temperature_tenths_c"]
        == event["raw_temperature_tenths_c"] - 40
        and 0 <= event["humidity_tenths_percent"] <= 1000
        and event["key_event"] in {"none", "short_press", "long_press"}
        and event["key_event_count"] >= 0
        and event["boot_event"] in {"none", "short_press", "long_press"}
        and event["boot_event_count"] >= 0
        and event["rtc_errors"] == 0
        and event["sensor_errors"] == 0
    )


def setup_event_has_fields(event: dict[str, Any]) -> bool:
    required = {
        "active": bool,
        "reason": str,
        "session_id": int,
        "ssid": str,
        "address": str,
        "error_stage": str,
    }
    return event.get("type") == "setup_state" and all(
        type(event.get(name)) is expected for name, expected in required.items()
    )


def valid_setup_event(event: dict[str, Any], active: bool) -> bool:
    if not setup_event_has_fields(event) or event["active"] is not active:
        return False
    common = (
        event["session_id"] >= 1
        and event["address"] == "192.168.4.1"
        and event["error_stage"] == ""
        and "password" not in event
    )
    if active:
        return (
            common
            and event["reason"] in {"no_wifi_config", "boot_long_press"}
            and re.fullmatch(r"S3-RLCD-[0-9A-F]{4}", event["ssid"]) is not None
        )
    return common and event["reason"] == "none" and event["ssid"] == ""


def diagnostic_events(
    lines: Iterable[str],
    expect_display: bool,
    expect_peripherals: bool = False,
    expect_setup: bool = False,
    display_deadline_seconds: float | None = None,
    monotonic: Any = time.monotonic,
) -> tuple[
    dict[str, Any] | None,
    dict[str, Any] | None,
    dict[str, Any] | None,
    dict[str, Any] | None,
    int,
    int,
    bool,
    dict[str, Any] | None,
    dict[str, Any] | None,
    bool,
    bool,
]:
    boot = None
    display = None
    progress = None
    peripheral = None
    boot_count = 0
    peripheral_count = 0
    peripheral_samples_valid = True
    setup_started = None
    setup_stopped = None
    setup_events_valid = True
    fatal_log_detected = False
    observation_started = monotonic()
    for line in lines:
        normalized_line = line.lower()
        fatal_log_detected = fatal_log_detected or any(
            marker in normalized_line
            for marker in (
                "task_wdt:",
                "guru meditation error",
                "panic'ed",
                "assert failed",
            )
        )
        candidate = line.strip()
        if not candidate.startswith("{"):
            continue
        try:
            event = json.loads(candidate)
        except json.JSONDecodeError:
            continue
        if valid_boot_event(event):
            if boot is None:
                boot = event
            boot_count += 1
        elif display_event_has_fields(event, "display_ready"):
            display = event
        elif display_event_has_fields(event, "display_progress"):
            if (
                display_deadline_seconds is None
                or monotonic() - observation_started <= display_deadline_seconds
            ):
                progress = event
        elif peripheral_event_has_fields(event):
            peripheral = event
            peripheral_count += 1
            peripheral_samples_valid = peripheral_samples_valid and valid_peripheral_event(event)
        elif setup_event_has_fields(event):
            if event["active"]:
                setup_events_valid = setup_events_valid and valid_setup_event(event, True)
                setup_started = event
                setup_stopped = None
            else:
                setup_events_valid = setup_events_valid and valid_setup_event(event, False)
                if setup_started is not None and event["session_id"] == setup_started["session_id"]:
                    setup_stopped = event
        if boot is not None and not expect_display and not expect_peripherals and not expect_setup:
            break
    return (
        boot,
        display,
        progress,
        peripheral,
        boot_count,
        peripheral_count,
        peripheral_samples_valid,
        setup_started,
        setup_stopped,
        setup_events_valid,
        fatal_log_detected,
    )


def boot_event(lines: Iterable[str]) -> dict[str, Any] | None:
    return diagnostic_events(lines, False)[0]


def serial_lines(
    port: str,
    timeout_seconds: float,
    serial_factory: Any | None = None,
    monotonic: Any = time.monotonic,
    write_timeout_exception: type[BaseException] | None = None,
) -> Iterable[str]:
    if serial_factory is None:
        try:
            import serial
        except ImportError as error:
            raise RuntimeError("live HIL requires pyserial from the ESP-IDF environment") from error
        serial_factory = serial.Serial
        write_timeout_exception = serial.SerialTimeoutException
    elif write_timeout_exception is None:
        write_timeout_exception = TimeoutError

    deadline = monotonic() + timeout_seconds
    initial_io_timeout = min(SERIAL_IO_TIMEOUT_SECONDS, timeout_seconds)
    with serial_factory(
        port=port,
        baudrate=115200,
        timeout=initial_io_timeout,
        write_timeout=initial_io_timeout,
    ) as connection:
        next_ready = 0.0
        while True:
            now = monotonic()
            if now >= deadline:
                break
            if now >= next_ready:
                connection.write_timeout = min(
                    SERIAL_IO_TIMEOUT_SECONDS, deadline - now
                )
                try:
                    connection.write(HIL_READY)
                except write_timeout_exception:
                    # USB CDC can briefly reject writes while the target recovers
                    # from reset. Keep observing until the overall deadline.
                    pass
                now = monotonic()
                next_ready = now + HIL_READY_INTERVAL_SECONDS
                if now >= deadline:
                    break
            remaining_seconds = deadline - monotonic()
            if remaining_seconds <= 0:
                break
            connection.timeout = min(SERIAL_IO_TIMEOUT_SECONDS, remaining_seconds)
            raw_line = connection.readline()
            if monotonic() > deadline:
                break
            if raw_line:
                yield raw_line.decode("utf-8", errors="replace")


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Observe Deck boot and optional display diagnostics.")
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--input-file", type=pathlib.Path, help="Replay a captured console log.")
    source.add_argument("--port", help="Read a live Deck serial port after flashing.")
    parser.add_argument("--timeout", type=float, default=15.0, help="Live serial timeout in seconds.")
    parser.add_argument(
        "--expect-display",
        action="store_true",
        help="Also require at least three clean full-frame completions and no reset.",
    )
    parser.add_argument(
        "--expect-peripherals",
        action="store_true",
        help="Also require at least three clean RTC/SHTC3 peripheral-state samples.",
    )
    parser.add_argument(
        "--expect-setup",
        action="store_true",
        help="Require a clean ephemeral Setup AP start followed by inactivity stop.",
    )
    parser.add_argument(
        "--expect-key-event",
        choices=("short_press", "long_press"),
        help="Require the latest physical KEY event while observing peripherals.",
    )
    parser.add_argument(
        "--minimum-key-events",
        type=int,
        default=0,
        help="Require at least this many physical KEY events.",
    )
    parser.add_argument(
        "--expect-boot-event",
        choices=("short_press", "long_press"),
        help="Require the latest physical BOOT event while observing peripherals.",
    )
    parser.add_argument(
        "--minimum-boot-events",
        type=int,
        default=0,
        help="Require at least this many physical BOOT events.",
    )
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    if arguments.timeout <= 0:
        print("timeout must be greater than zero", file=sys.stderr)
        return 2
    expects_buttons = (
        arguments.expect_key_event is not None
        or arguments.minimum_key_events > 0
        or arguments.expect_boot_event is not None
        or arguments.minimum_boot_events > 0
    )
    if arguments.minimum_key_events < 0 or arguments.minimum_boot_events < 0:
        print("minimum button event counts must not be negative", file=sys.stderr)
        return 2
    if expects_buttons and not arguments.expect_peripherals:
        print("button expectations require --expect-peripherals", file=sys.stderr)
        return 2

    try:
        if arguments.input_file is not None:
            with arguments.input_file.open(encoding="utf-8", errors="replace") as capture:
                event, display, progress, peripheral, boot_count, peripheral_count, peripheral_valid, setup_started, setup_stopped, setup_valid, fatal_log_detected = diagnostic_events(
                    capture,
                    arguments.expect_display,
                    arguments.expect_peripherals,
                    arguments.expect_setup,
                )
        else:
            event, display, progress, peripheral, boot_count, peripheral_count, peripheral_valid, setup_started, setup_stopped, setup_valid, fatal_log_detected = diagnostic_events(
                serial_lines(arguments.port, arguments.timeout),
                arguments.expect_display,
                arguments.expect_peripherals,
                arguments.expect_setup,
                display_deadline_seconds=(
                    DISPLAY_STABILITY_WINDOW_SECONDS
                    if arguments.expect_display
                    else None
                ),
            )
    except (OSError, RuntimeError) as error:
        print(f"boot smoke failed: {error}", file=sys.stderr)
        return 2

    if fatal_log_detected:
        print("boot smoke failed: fatal target log observed", file=sys.stderr)
        return 1
    if event is None:
        print("boot smoke failed: no valid boot_ok event observed", file=sys.stderr)
        return 1
    if boot_count != 1:
        print(
            f"boot smoke failed: observed {boot_count} boot_ok events (unexpected reset)",
            file=sys.stderr,
        )
        return 1

    print(
        "boot_ok observed: "
        f"version={event['firmware_version']} reset_reason={event['reset_reason']}"
    )
    if arguments.expect_display:
        if not valid_display_event(display, "display_ready", 1):
            print("display smoke failed: no valid display_ready event observed", file=sys.stderr)
            return 1
        print(
            "display_ready observed: "
            f"frames={display['completed_frames']} timeouts={display['transfer_timeouts']}"
        )
        if not valid_display_event(progress, "display_progress", 3):
            print(
                "display smoke failed: fewer than three clean completed frames observed",
                file=sys.stderr,
            )
            return 1
        print(
            "display_progress observed: "
            f"frames={progress['completed_frames']} timeouts={progress['transfer_timeouts']}"
        )
    if arguments.expect_peripherals:
        if peripheral_count < 3:
            print(
                f"peripheral smoke failed: observed {peripheral_count} peripheral_state samples (need 3)",
                file=sys.stderr,
            )
            return 1
        if not peripheral_valid or not valid_peripheral_event(peripheral):
            print(
                "peripheral smoke failed: sensor unavailable or invalid, RTC invalid, or I2C errors observed",
                file=sys.stderr,
            )
            return 1
        if (
            arguments.expect_key_event is not None
            and peripheral["key_event"] != arguments.expect_key_event
        ) or peripheral["key_event_count"] < arguments.minimum_key_events:
            print("peripheral smoke failed: KEY event evidence missing", file=sys.stderr)
            return 1
        if (
            arguments.expect_boot_event is not None
            and peripheral["boot_event"] != arguments.expect_boot_event
        ) or peripheral["boot_event_count"] < arguments.minimum_boot_events:
            print("peripheral smoke failed: BOOT event evidence missing", file=sys.stderr)
            return 1
        rtc = f"{peripheral['rtc_hour']:02d}:{peripheral['rtc_minute']:02d}" if peripheral["rtc_available"] else "--:--"
        print(
            "peripheral_state observed: "
            f"rtc={rtc} "
            f"raw_temperature={peripheral['raw_temperature_tenths_c'] / 10:.1f}C "
            f"calibrated_temperature={peripheral['calibrated_temperature_tenths_c'] / 10:.1f}C "
            f"humidity={peripheral['humidity_tenths_percent'] / 10:.1f}% "
            f"samples={peripheral_count}"
        )
        if expects_buttons:
            print(
                "button evidence observed: "
                f"key={peripheral['key_event']}#{peripheral['key_event_count']} "
                f"boot={peripheral['boot_event']}#{peripheral['boot_event_count']}"
            )
    if arguments.expect_setup:
        if (
            not setup_valid
            or setup_started is None
            or setup_stopped is None
            or not valid_setup_event(setup_started, True)
            or not valid_setup_event(setup_stopped, False)
        ):
            print(
                "setup smoke failed: clean active-to-inactive Setup transition missing",
                file=sys.stderr,
            )
            return 1
        print(
            "setup_state observed: "
            f"ssid={setup_started['ssid']} address={setup_started['address']} "
            f"session={setup_started['session_id']} stopped=true"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
