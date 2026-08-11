#!/usr/bin/env python3

"""Configuration, redacted evidence, and analysis for Deck HIL smoke runs."""

import datetime
import hashlib
import json
import pathlib
from dataclasses import dataclass
from typing import Any


WATCHDOG_REASONS = {"interrupt_watchdog", "task_watchdog", "watchdog"}
WIFI_FAILURE_STATES = {
    "auth_failed",
    "timed_out",
    "connection_failed",
    "storage_error",
}
FATAL_MARKERS = (
    "task_wdt:",
    "guru meditation error",
    "panic'ed",
    "assert failed",
)
REDACTED_LINE = "[REDACTED NON-DIAGNOSTIC LINE]"
REDACTED_INVALID_JSON = "[REDACTED INVALID JSON LINE]"
REDACTED_INVALID_DIAGNOSTIC = "[REDACTED INVALID DIAGNOSTIC LINE]"
REDACTED_FATAL_LINE = "[REDACTED FATAL TARGET LINE]"
HOST_LINES = {
    "[host] observation started",
    "[host] physical actions requested",
    "[host] observation complete",
}


class SmokeFailure(RuntimeError):
    pass


@dataclass(frozen=True)
class SmokeConfig:
    duration_seconds: float
    minimum_display_updates: int
    minimum_peripheral_samples: int
    require_key_event: bool
    require_boot_event: bool
    require_setup_cycle: bool
    require_wifi_failure_recovery: bool
    maximum_heap_drop_bytes: int


EVENT_SCHEMAS: dict[str, dict[str, type]] = {
    "boot_ok": {
        "type": str,
        "firmware_version": str,
        "reset_reason": str,
        "uptime_ms": int,
        "minimum_free_heap_bytes": int,
    },
    "display_ready": {
        "type": str,
        "width": int,
        "height": int,
        "frame_bytes": int,
        "submitted_frames": int,
        "completed_frames": int,
        "transfer_timeouts": int,
        "start_failures": int,
        "rejected_updates": int,
    },
    "display_progress": {
        "type": str,
        "width": int,
        "height": int,
        "frame_bytes": int,
        "submitted_frames": int,
        "completed_frames": int,
        "transfer_timeouts": int,
        "start_failures": int,
        "rejected_updates": int,
    },
    "display_error": {"type": str, "stage": str},
    "peripheral_state": {
        "type": str,
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
        "free_heap_bytes": int,
        "minimum_free_heap_bytes": int,
    },
    "setup_state": {
        "type": str,
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
        "device_settings_state": str,
        "device_settings_record_status": str,
        "device_settings_candidate_record_status": str,
        "device_settings_has_active": bool,
        "device_settings_has_candidate": bool,
        "device_settings_generation": int,
        "temperature_offset_tenths_c": int,
    },
    "diagnostic_error": {"type": str, "stage": str},
    "deck_identity": {"type": str, "model": str, "protocol": int},
}


def load_config(path: pathlib.Path) -> tuple[SmokeConfig, dict[str, Any]]:
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise SmokeFailure(f"invalid smoke config: {error}") from error
    required = {
        "schema_version": int,
        "duration_seconds": (int, float),
        "minimum_display_updates": int,
        "minimum_peripheral_samples": int,
        "require_key_event": bool,
        "require_boot_event": bool,
        "require_setup_cycle": bool,
        "require_wifi_failure_recovery": bool,
        "maximum_heap_drop_bytes": int,
    }
    if not isinstance(document, dict) or any(
        type(document.get(name)) not in (expected if isinstance(expected, tuple) else (expected,))
        for name, expected in required.items()
    ):
        raise SmokeFailure("smoke config is missing a required field or has a wrong type")
    if document["schema_version"] != 1:
        raise SmokeFailure("unsupported smoke config schema")
    if (
        document["duration_seconds"] <= 0
        or document["minimum_display_updates"] < 1
        or document["minimum_peripheral_samples"] < 1
        or document["maximum_heap_drop_bytes"] < 0
    ):
        raise SmokeFailure("smoke config numeric limits are invalid")
    if set(document) != set(required):
        raise SmokeFailure("smoke config contains unknown fields")
    return (
        SmokeConfig(
            float(document["duration_seconds"]),
            document["minimum_display_updates"],
            document["minimum_peripheral_samples"],
            document["require_key_event"],
            document["require_boot_event"],
            document["require_setup_cycle"],
            document["require_wifi_failure_recovery"],
            document["maximum_heap_drop_bytes"],
        ),
        document,
    )


def parsed_event(line: str) -> dict[str, Any] | None:
    candidate = line.strip()
    if not candidate.startswith("{"):
        return None
    try:
        event = json.loads(candidate)
    except json.JSONDecodeError:
        return None
    return event if isinstance(event, dict) else None


def sanitize_line(line: str) -> tuple[str, bool]:
    """Persist only canonical, allow-listed diagnostics and fixed host markers."""
    if line in HOST_LINES:
        return line, False
    if line in {
        REDACTED_LINE,
        REDACTED_INVALID_JSON,
        REDACTED_INVALID_DIAGNOSTIC,
        REDACTED_FATAL_LINE,
    }:
        return line, True
    normalized = line.lower()
    if any(marker in normalized for marker in FATAL_MARKERS):
        return REDACTED_FATAL_LINE, True
    event = parsed_event(line)
    if event is None:
        return (REDACTED_INVALID_JSON if line.lstrip().startswith("{") else REDACTED_LINE, True)
    event_type = event.get("type")
    schema = EVENT_SCHEMAS.get(event_type) if type(event_type) is str else None
    if schema is None:
        return REDACTED_LINE, True
    if set(event) != set(schema):
        return REDACTED_INVALID_DIAGNOSTIC, True
    safe_event = {name: event[name] for name in schema if name in event}
    return json.dumps(safe_event, separators=(",", ":"), ensure_ascii=False), False


def _valid_timestamp(value: str) -> bool:
    try:
        parsed = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return False
    return parsed.tzinfo is not None


def load_capture(path: pathlib.Path) -> tuple[list[dict[str, Any]], bytes, int]:
    envelopes: list[dict[str, Any]] = []
    redacted = 0
    previous_elapsed = -1.0
    try:
        source = path.open(encoding="utf-8", errors="replace")
    except OSError as error:
        raise SmokeFailure(f"cannot open smoke capture: {error}") from error
    with source:
        for line_number, encoded in enumerate(source, start=1):
            try:
                envelope = json.loads(encoded)
            except json.JSONDecodeError as error:
                raise SmokeFailure(f"capture line {line_number} is not a JSON object") from error
            if (
                not isinstance(envelope, dict)
                or type(envelope.get("captured_at")) is not str
                or not _valid_timestamp(envelope.get("captured_at", ""))
                or type(envelope.get("elapsed_seconds")) not in (int, float)
                or type(envelope.get("line")) is not str
                or envelope["elapsed_seconds"] < 0
                or float(envelope["elapsed_seconds"]) < previous_elapsed
            ):
                raise SmokeFailure(f"capture line {line_number} has an invalid envelope")
            elapsed = float(envelope["elapsed_seconds"])
            sanitized, changed = sanitize_line(envelope["line"])
            envelope = {
                "captured_at": envelope["captured_at"],
                "elapsed_seconds": elapsed,
                "line": sanitized,
            }
            envelopes.append(envelope)
            redacted += int(changed)
            previous_elapsed = elapsed
    if not envelopes:
        raise SmokeFailure("smoke capture is empty")
    evidence = b"".join(
        (json.dumps(envelope, separators=(",", ":"), ensure_ascii=False) + "\n").encode("utf-8")
        for envelope in envelopes
    )
    return envelopes, evidence, redacted


def valid_event_schema(event: dict[str, Any]) -> bool:
    event_type = event.get("type")
    schema = EVENT_SCHEMAS.get(event_type) if type(event_type) is str else None
    if schema is None or set(event) != set(schema):
        return False
    if any(type(event[name]) is not expected for name, expected in schema.items()):
        return False
    for name, value in event.items():
        if type(value) is int and name not in {
            "raw_temperature_tenths_c",
            "calibrated_temperature_tenths_c",
            "temperature_offset_tenths_c",
        } and value < 0:
            return False
    return True


def analyze_capture(
    envelopes: list[dict[str, Any]],
    config: SmokeConfig,
    firmware_commit: str,
    toolchain_version: str,
    redacted_line_count: int,
    source_dirty: bool = False,
) -> dict[str, Any]:
    failures: list[str] = []
    boot_events: list[dict[str, Any]] = []
    first_display_frames: int | None = None
    last_display_frames: int | None = None
    display_error_count = 0
    peripheral_samples = 0
    first_key_count: int | None = None
    last_key_count: int | None = None
    first_boot_count: int | None = None
    last_boot_count: int | None = None
    first_free_heap: int | None = None
    last_free_heap: int | None = None
    minimum_free_heap: int | None = None
    i2c_error_count = 0
    active_setup_sessions: set[int] = set()
    setup_cycles = 0
    setup_error_count = 0
    wifi_error_count = 0
    wifi_failure_generation: int | None = None
    wifi_failure_recoveries = 0
    diagnostic_schema_error_count = 0

    for envelope in envelopes:
        line = envelope["line"]
        if line == REDACTED_FATAL_LINE:
            failures.append("fatal target log observed")
            continue
        if line in {REDACTED_INVALID_JSON, REDACTED_INVALID_DIAGNOSTIC}:
            diagnostic_schema_error_count += 1
            continue
        event = parsed_event(line)
        if event is None:
            continue
        if not valid_event_schema(event):
            diagnostic_schema_error_count += 1
            continue
        event_type = event["type"]
        if event_type == "boot_ok":
            boot_events.append(event)
            heap = event["minimum_free_heap_bytes"]
            minimum_free_heap = heap if minimum_free_heap is None else min(minimum_free_heap, heap)
        elif event_type in {"display_ready", "display_progress"}:
            completed = event["completed_frames"]
            if first_display_frames is None:
                first_display_frames = completed
            last_display_frames = completed
            display_error_count = max(
                display_error_count,
                event["transfer_timeouts"] + event["start_failures"] + event["rejected_updates"],
            )
        elif event_type == "display_error":
            display_error_count += 1
        elif event_type == "diagnostic_error":
            failures.append("target diagnostic error observed")
        elif event_type == "peripheral_state":
            peripheral_samples += 1
            if first_key_count is None:
                first_key_count = event["key_event_count"]
            last_key_count = event["key_event_count"]
            if first_boot_count is None:
                first_boot_count = event["boot_event_count"]
            last_boot_count = event["boot_event_count"]
            if first_free_heap is None:
                first_free_heap = event["free_heap_bytes"]
            last_free_heap = event["free_heap_bytes"]
            minimum_free_heap = (
                event["minimum_free_heap_bytes"]
                if minimum_free_heap is None
                else min(minimum_free_heap, event["minimum_free_heap_bytes"])
            )
            i2c_error_count = max(
                i2c_error_count, event["rtc_errors"] + event["sensor_errors"]
            )
        elif event_type == "setup_state":
            session_id = event["session_id"]
            if event["active"]:
                active_setup_sessions.add(session_id)
            elif session_id in active_setup_sessions:
                active_setup_sessions.remove(session_id)
                setup_cycles += 1
            wifi_state = event["wifi_config_state"]
            if event["error_stage"] and wifi_state not in WIFI_FAILURE_STATES:
                setup_error_count += 1
            if wifi_state in WIFI_FAILURE_STATES:
                if wifi_failure_generation is None:
                    wifi_error_count += 1
                wifi_failure_generation = event["wifi_generation"]
            elif wifi_state == "active" and wifi_failure_generation is not None:
                if (
                    event["wifi_generation"] == wifi_failure_generation
                    and event["wifi_has_active"] is True
                ):
                    wifi_failure_recoveries += 1
                    wifi_failure_generation = None

    duration_seconds = envelopes[-1]["elapsed_seconds"]
    reset_count = max(0, len(boot_events) - 1)
    watchdog_count = sum(
        1 for event in boot_events if event["reset_reason"] in WATCHDOG_REASONS
    )
    display_updates = (
        max(0, last_display_frames - first_display_frames)
        if first_display_frames is not None and last_display_frames is not None
        else 0
    )
    key_event_delta = (
        max(0, last_key_count - first_key_count)
        if first_key_count is not None and last_key_count is not None
        else 0
    )
    boot_event_delta = (
        max(0, last_boot_count - first_boot_count)
        if first_boot_count is not None and last_boot_count is not None
        else 0
    )
    heap_drop_bytes = (
        max(0, first_free_heap - last_free_heap)
        if first_free_heap is not None and last_free_heap is not None
        else 0
    )

    if not boot_events:
        failures.append("no boot_ok event observed")
    if duration_seconds < config.duration_seconds:
        failures.append("configured smoke duration was not reached")
    if reset_count:
        failures.append("unexpected reset observed")
    if watchdog_count:
        failures.append("watchdog reset observed")
    if display_updates < config.minimum_display_updates:
        failures.append("insufficient page updates")
    if display_error_count:
        failures.append("display errors observed")
    if peripheral_samples < config.minimum_peripheral_samples:
        failures.append("insufficient peripheral samples")
    if i2c_error_count:
        failures.append("I2C errors observed")
    if diagnostic_schema_error_count:
        failures.append("invalid diagnostic event schema observed")
    if setup_error_count:
        failures.append("Setup service errors observed")
    if config.require_key_event and key_event_delta < 1:
        failures.append("physical KEY event missing")
    if config.require_boot_event and boot_event_delta < 1:
        failures.append("physical BOOT event missing")
    if config.require_setup_cycle and setup_cycles < 1:
        failures.append("Setup Mode cycle missing")
    if config.require_wifi_failure_recovery and wifi_failure_recoveries < 1:
        failures.append("Wi-Fi failure recovery missing")
    if config.require_wifi_failure_recovery and wifi_error_count > 1:
        failures.append("additional Wi-Fi failure observed")
    if not config.require_wifi_failure_recovery and wifi_error_count:
        failures.append("unexpected Wi-Fi failure observed")
    if heap_drop_bytes > config.maximum_heap_drop_bytes:
        failures.append("sustained free-heap decline exceeded limit")
    if first_free_heap is None or last_free_heap is None or minimum_free_heap is None:
        failures.append("heap evidence missing")

    firmware_version = boot_events[0]["firmware_version"] if boot_events else ""
    return {
        "schema_version": 1,
        "status": "passed" if not failures else "failed",
        "firmware_commit": firmware_commit,
        "firmware_version": firmware_version,
        "toolchain_version": toolchain_version,
        "source_dirty": source_dirty,
        "started_at": envelopes[0]["captured_at"],
        "ended_at": envelopes[-1]["captured_at"],
        "duration_seconds": duration_seconds,
        "reset_count": reset_count,
        "watchdog_count": watchdog_count,
        "display_error_count": display_error_count,
        "i2c_error_count": i2c_error_count,
        "wifi_error_count": wifi_error_count,
        "setup_error_count": setup_error_count,
        "diagnostic_schema_error_count": diagnostic_schema_error_count,
        "minimum_free_heap_bytes": minimum_free_heap,
        "initial_free_heap_bytes": first_free_heap,
        "final_free_heap_bytes": last_free_heap,
        "heap_drop_bytes": heap_drop_bytes,
        "display_updates": display_updates,
        "peripheral_samples": peripheral_samples,
        "setup_cycles": setup_cycles,
        "wifi_failure_recoveries": wifi_failure_recoveries,
        "key_event_delta": key_event_delta,
        "boot_event_delta": boot_event_delta,
        "redacted_line_count": redacted_line_count,
        "failures": failures,
    }


def empty_failure_summary(
    firmware_commit_value: str,
    toolchain_version_value: str,
    failure: str,
    now: str,
    source_dirty: bool = False,
) -> dict[str, Any]:
    return {
        "schema_version": 1,
        "status": "failed",
        "firmware_commit": firmware_commit_value,
        "firmware_version": "",
        "toolchain_version": toolchain_version_value,
        "source_dirty": source_dirty,
        "started_at": now,
        "ended_at": now,
        "duration_seconds": 0.0,
        "reset_count": 0,
        "watchdog_count": 0,
        "display_error_count": 0,
        "i2c_error_count": 0,
        "wifi_error_count": 0,
        "setup_error_count": 0,
        "diagnostic_schema_error_count": 0,
        "minimum_free_heap_bytes": None,
        "initial_free_heap_bytes": None,
        "final_free_heap_bytes": None,
        "heap_drop_bytes": 0,
        "display_updates": 0,
        "peripheral_samples": 0,
        "setup_cycles": 0,
        "wifi_failure_recoveries": 0,
        "key_event_delta": 0,
        "boot_event_delta": 0,
        "redacted_line_count": 0,
        "failures": [failure],
    }


def write_evidence(
    result_directory: pathlib.Path,
    evidence: bytes,
    config_document: dict[str, Any],
    summary: dict[str, Any],
    create_directory: bool = True,
) -> None:
    try:
        if create_directory:
            result_directory.mkdir(parents=True, exist_ok=False)
        raw_path = result_directory / "serial.jsonl"
        raw_path.write_bytes(evidence)
        config_bytes = (json.dumps(config_document, indent=2, sort_keys=True) + "\n").encode("utf-8")
        (result_directory / "config.json").write_bytes(config_bytes)
        summary["raw_log_sha256"] = hashlib.sha256(evidence).hexdigest()
        summary["config_sha256"] = hashlib.sha256(config_bytes).hexdigest()
        (result_directory / "summary.json").write_text(
            json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )
    except OSError as error:
        raise SmokeFailure(f"cannot write smoke evidence: {error}") from error
