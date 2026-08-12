#!/usr/bin/env python3

"""Generate the auditable M0 acceptance report from redacted evidence summaries."""

import argparse
import datetime
import hashlib
import json
import math
import pathlib
import re
import sys
from typing import Any


class ReportFailure(RuntimeError):
    pass


HIL_SUMMARY_SCHEMA: dict[str, type | tuple[type, ...]] = {
    "schema_version": int,
    "status": str,
    "firmware_commit": str,
    "firmware_version": str,
    "toolchain_version": str,
    "source_dirty": bool,
    "started_at": str,
    "ended_at": str,
    "duration_seconds": (int, float),
    "reset_count": int,
    "watchdog_count": int,
    "display_error_count": int,
    "i2c_error_count": int,
    "wifi_error_count": int,
    "setup_error_count": int,
    "diagnostic_schema_error_count": int,
    "minimum_free_heap_bytes": int,
    "initial_free_heap_bytes": int,
    "heap_warmup_seconds": (int, float),
    "heap_baseline_elapsed_seconds": (int, float),
    "heap_baseline_free_heap_bytes": int,
    "final_free_heap_bytes": int,
    "heap_drop_bytes": int,
    "stabilized_heap_drop_bytes": int,
    "display_updates": int,
    "peripheral_samples": int,
    "setup_cycles": int,
    "wifi_failure_recoveries": int,
    "key_event_delta": int,
    "boot_event_delta": int,
    "redacted_line_count": int,
    "failures": list,
    "raw_log_sha256": str,
    "config_sha256": str,
}
SETUP_CHECKS = {
    "random_password_connection",
    "recovery_page_and_scan",
    "invalid_credentials_preserved_recovery",
    "valid_credentials_persisted",
    "temperature_offset_updated",
    "wifi_clear_double_confirmed",
    "wifi_clear_reentered_setup",
}
SETUP_CHECK_LABELS = (
    ("random_password_connection", "Random-password AP connection"),
    ("recovery_page_and_scan", "Recovery page and network scan"),
    (
        "invalid_credentials_preserved_recovery",
        "Invalid credentials preserve recovery and active config",
    ),
    ("valid_credentials_persisted", "Valid credentials persist after restart"),
    ("temperature_offset_updated", "Temperature offset updates the Deck"),
    ("wifi_clear_double_confirmed", "Wi-Fi clear requires double confirmation"),
    ("wifi_clear_reentered_setup", "Wi-Fi clear re-enters Setup Mode"),
)
SETUP_RESULT_SCHEMA: dict[str, type] = {
    "schema_version": int,
    "status": str,
    "firmware_commit": str,
    "device": str,
    "started_at": str,
    "ended_at": str,
    "checks": dict,
    "redaction_statement": str,
}
MANIFEST_SCHEMA: dict[str, object] = {
    "schema_version": int,
    "firmware_commit": str,
    "esp_idf_version": str,
    "lvgl_version": str,
    "test_commands": dict,
    "smoke_summary": (str, type(None)),
    "soak_summary": (str, type(None)),
    "setup_result": (str, type(None)),
    "known_limitations": list,
    "out_of_scope": list,
}
SECRET_COMMAND_PATTERNS = (
    re.compile(r"--(?:password|token|credential|ssid)(?:=|\s)", re.IGNORECASE),
    re.compile(r"(?:password|token|credential|ssid)\s*[:=]", re.IGNORECASE),
    re.compile(r"\bDECK_WIFI\s", re.IGNORECASE),
)
RFC3339_TIMESTAMP = re.compile(
    r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})"
)
REPOSITORY_ROOT = pathlib.Path(__file__).resolve().parents[1]
CONTROLLED_CONFIG_PATHS = {
    "2-hour smoke": REPOSITORY_ROOT / "tools" / "hil_smoke_2h.json",
    "24-hour soak": REPOSITORY_ROOT / "tools" / "hil_smoke_24h.json",
}
CONTROLLED_CONFIG_SCHEMA: dict[str, object] = {
    "schema_version": int,
    "duration_seconds": (int, float),
    "heap_warmup_seconds": (int, float),
    "minimum_display_updates": int,
    "minimum_peripheral_samples": int,
    "require_key_event": bool,
    "require_boot_event": bool,
    "require_setup_cycle": bool,
    "require_wifi_failure_recovery": bool,
    "maximum_heap_drop_bytes": int,
}


def load_json(path: pathlib.Path) -> dict[str, Any]:
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise ReportFailure(f"cannot read {path}: {error}") from error
    if not isinstance(document, dict):
        raise ReportFailure(f"{path} must contain a JSON object")
    return document


def evidence_path(manifest_path: pathlib.Path, value: object) -> pathlib.Path:
    if type(value) is not str or value == "":
        raise ReportFailure("manifest evidence paths must be non-empty strings")
    path = pathlib.Path(value)
    if path.is_absolute():
        raise ReportFailure("manifest evidence paths must stay in the evidence directory")
    evidence_directory = (manifest_path.parent / "evidence").resolve()
    candidate = (manifest_path.parent / path).resolve()
    try:
        candidate.relative_to(evidence_directory)
    except ValueError as error:
        raise ReportFailure(
            "manifest evidence paths must stay in the evidence directory"
        ) from error
    return candidate


def load_optional_evidence(
    manifest_path: pathlib.Path, value: object
) -> dict[str, Any] | None:
    if value is None:
        return None
    return load_json(evidence_path(manifest_path, value))


def is_finite_number(value: int | float) -> bool:
    try:
        return math.isfinite(value)
    except OverflowError:
        return False


def controlled_config(label: str) -> tuple[dict[str, Any], str]:
    path = CONTROLLED_CONFIG_PATHS[label]
    document = load_json(path)
    if (
        not exact_schema(document, CONTROLLED_CONFIG_SCHEMA)
        or document["schema_version"] != 2
        or not is_finite_number(document["duration_seconds"])
        or not is_finite_number(document["heap_warmup_seconds"])
        or document["duration_seconds"] <= 0
        or document["heap_warmup_seconds"] < 0
        or document["heap_warmup_seconds"] >= document["duration_seconds"]
        or document["minimum_display_updates"] < 1
        or document["minimum_peripheral_samples"] < 1
        or document["maximum_heap_drop_bytes"] < 0
        or not all(
            document[name]
            for name in (
                "require_key_event",
                "require_boot_event",
                "require_setup_cycle",
                "require_wifi_failure_recovery",
            )
        )
    ):
        raise ReportFailure(f"controlled {label} config is invalid")
    canonical = (json.dumps(document, indent=2, sort_keys=True) + "\n").encode("utf-8")
    return document, hashlib.sha256(canonical).hexdigest()


def parse_timestamp(value: str) -> datetime.datetime | None:
    if RFC3339_TIMESTAMP.fullmatch(value) is None:
        return None
    try:
        parsed = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    return parsed if parsed.tzinfo is not None else None


def valid_timestamp(value: str) -> bool:
    return parse_timestamp(value) is not None


def markdown_escape(value: str) -> str:
    escaped = value.replace("\\", "\\\\")
    for marker in ("`", "*", "_", "[", "]", "|", "<", ">"):
        escaped = escaped.replace(marker, f"\\{marker}")
    return escaped


def contains_control_character(value: str) -> bool:
    return any(ord(character) < 32 or ord(character) == 127 for character in value)


def exact_schema(document: dict[str, Any], schema: dict[str, object]) -> bool:
    if set(document) != set(schema):
        return False
    for name, expected in schema.items():
        expected_types = expected if isinstance(expected, tuple) else (expected,)
        if type(document[name]) not in expected_types:
            return False
    return True


def validate_manifest(manifest: dict[str, Any]) -> None:
    if not exact_schema(manifest, MANIFEST_SCHEMA):
        raise ReportFailure("manifest schema is invalid")
    if (
        manifest["schema_version"] != 1
        or re.fullmatch(r"[0-9a-f]{40}", manifest["firmware_commit"]) is None
        or manifest["esp_idf_version"] != "6.0.2"
        or manifest["lvgl_version"] != "9.4.0"
    ):
        raise ReportFailure("manifest versions or firmware commit are invalid")
    commands = manifest["test_commands"]
    if set(commands) != {"host", "smoke", "soak", "setup"} or any(
        type(value) is not str
        or not value
        or contains_control_character(value)
        or "`" in value
        or len(value) > 512
        for value in commands.values()
    ):
        raise ReportFailure("manifest test commands are invalid")
    if any(
        pattern.search(value)
        for value in commands.values()
        for pattern in SECRET_COMMAND_PATTERNS
    ):
        raise ReportFailure("manifest test commands may contain credential material")
    for name in ("known_limitations", "out_of_scope"):
        if any(
            type(value) is not str
            or not value
            or contains_control_character(value)
            or len(value) > 256
            for value in manifest[name]
        ):
            raise ReportFailure(f"manifest {name} entries are invalid")
        if any(
            pattern.search(value)
            for value in manifest[name]
            for pattern in SECRET_COMMAND_PATTERNS
        ):
            raise ReportFailure(f"manifest {name} entries may contain credential material")


def validate_hil_summary(
    label: str,
    summary: dict[str, Any],
    expected_commit: str,
    config: dict[str, Any],
    expected_config_hash: str,
) -> list[str]:
    if not hil_summary_safe_to_render(summary):
        return [f"{label} evidence schema is invalid."]
    blockers: list[str] = []
    if summary["schema_version"] != 2 or summary["status"] not in {"passed", "failed"}:
        blockers.append(f"{label} evidence schema is invalid.")
        return blockers
    if summary["status"] != "passed":
        blockers.append(f"{label} evidence status is not passed.")
    if summary["firmware_commit"] != expected_commit:
        blockers.append(f"{label} firmware commit does not match manifest.")
    if summary["toolchain_version"] != "ESP-IDF v6.0.2":
        blockers.append(f"{label} toolchain is not ESP-IDF v6.0.2.")
    if summary["source_dirty"]:
        blockers.append(f"{label} source tree was dirty.")
    if not is_finite_number(summary["duration_seconds"]):
        blockers.append(f"{label} duration is not finite.")
    elif summary["duration_seconds"] < config["duration_seconds"]:
        blockers.append(f"{label} duration is shorter than required.")
    started = parse_timestamp(summary["started_at"])
    ended = parse_timestamp(summary["ended_at"])
    if started is None or ended is None or ended < started:
        blockers.append(f"{label} timestamps are out of order.")
    elif (ended - started).total_seconds() + 1 < config["duration_seconds"]:
        blockers.append(f"{label} timestamp interval is shorter than required.")
    if summary["config_sha256"] != expected_config_hash:
        blockers.append(f"{label} config hash does not match the controlled config.")
    if (
        not is_finite_number(summary["heap_warmup_seconds"])
        or not is_finite_number(summary["heap_baseline_elapsed_seconds"])
        or summary["heap_warmup_seconds"] != config["heap_warmup_seconds"]
        or summary["heap_baseline_elapsed_seconds"]
        < summary["heap_warmup_seconds"]
        or summary["heap_baseline_elapsed_seconds"] > summary["duration_seconds"]
    ):
        blockers.append(f"{label} stabilized heap timing is invalid.")
    numeric_fields = (
        "reset_count",
        "watchdog_count",
        "display_error_count",
        "i2c_error_count",
        "wifi_error_count",
        "setup_error_count",
        "diagnostic_schema_error_count",
        "minimum_free_heap_bytes",
        "initial_free_heap_bytes",
        "heap_warmup_seconds",
        "heap_baseline_elapsed_seconds",
        "heap_baseline_free_heap_bytes",
        "final_free_heap_bytes",
        "heap_drop_bytes",
        "stabilized_heap_drop_bytes",
        "display_updates",
        "peripheral_samples",
        "setup_cycles",
        "wifi_failure_recoveries",
        "key_event_delta",
        "boot_event_delta",
        "redacted_line_count",
    )
    if any(summary[field] < 0 for field in numeric_fields):
        blockers.append(f"{label} numeric counters are invalid.")
    for field, description in (
        ("reset_count", "reset count"),
        ("watchdog_count", "watchdog count"),
        ("display_error_count", "display error count"),
        ("i2c_error_count", "I²C error count"),
        ("setup_error_count", "Setup error count"),
        ("diagnostic_schema_error_count", "diagnostic schema error count"),
    ):
        if summary[field] != 0:
            blockers.append(f"{label} {description} is non-zero.")
    if summary["wifi_error_count"] != 1 or summary["wifi_failure_recoveries"] < 1:
        blockers.append(f"{label} recoverable Wi-Fi failure coverage is invalid.")
    if summary["display_updates"] < config["minimum_display_updates"]:
        blockers.append(f"{label} page update coverage is insufficient.")
    if summary["peripheral_samples"] < config["minimum_peripheral_samples"]:
        blockers.append(f"{label} peripheral sample coverage is insufficient.")
    if summary["setup_cycles"] < 1:
        blockers.append(f"{label} Setup Mode cycle is missing.")
    if summary["key_event_delta"] < 1 or summary["boot_event_delta"] < 1:
        blockers.append(f"{label} physical button evidence is missing.")
    if summary["failures"]:
        blockers.append(f"{label} summary contains failures.")
    expected_heap_drop = max(
        0, summary["initial_free_heap_bytes"] - summary["final_free_heap_bytes"]
    )
    expected_stabilized_heap_drop = max(
        0,
        summary["heap_baseline_free_heap_bytes"]
        - summary["final_free_heap_bytes"],
    )
    if (
        summary["minimum_free_heap_bytes"] <= 0
        or summary["initial_free_heap_bytes"] <= 0
        or summary["heap_baseline_free_heap_bytes"] <= 0
        or summary["final_free_heap_bytes"] <= 0
        or summary["minimum_free_heap_bytes"] > summary["initial_free_heap_bytes"]
        or summary["minimum_free_heap_bytes"]
        > summary["heap_baseline_free_heap_bytes"]
        or summary["minimum_free_heap_bytes"] > summary["final_free_heap_bytes"]
        or summary["heap_drop_bytes"] != expected_heap_drop
        or summary["stabilized_heap_drop_bytes"]
        != expected_stabilized_heap_drop
    ):
        blockers.append(f"{label} heap values are invalid.")
    if summary["stabilized_heap_drop_bytes"] > config["maximum_heap_drop_bytes"]:
        blockers.append(f"{label} heap decline exceeds the controlled config.")
    return blockers


def hil_summary_safe_to_render(summary: dict[str, Any]) -> bool:
    return (
        exact_schema(summary, HIL_SUMMARY_SCHEMA)
        and valid_timestamp(summary["started_at"])
        and valid_timestamp(summary["ended_at"])
        and all(type(value) is str for value in summary["failures"])
        and re.fullmatch(r"[0-9a-f]{64}", summary["raw_log_sha256"]) is not None
        and re.fullmatch(r"[0-9a-f]{64}", summary["config_sha256"]) is not None
    )


def validate_setup_result(
    setup: dict[str, Any], expected_commit: str
) -> list[str]:
    if not setup_result_safe_to_render(setup):
        return ["Human Setup Mode evidence schema is invalid."]
    if (
        setup["schema_version"] != 1
        or setup["status"] not in {"passed", "failed"}
        or re.fullmatch(r"[0-9a-f]{40}", setup["firmware_commit"]) is None
        or setup["redaction_statement"]
        != "No Wi-Fi passwords or credentials recorded."
    ):
        return ["Human Setup Mode evidence schema is invalid."]
    blockers: list[str] = []
    if setup["status"] != "passed":
        blockers.append("Human Setup Mode evidence status is not passed.")
    if setup["firmware_commit"] != expected_commit:
        blockers.append("Human Setup Mode firmware commit does not match manifest.")
    started = parse_timestamp(setup["started_at"])
    ended = parse_timestamp(setup["ended_at"])
    if started is None or ended is None or ended < started:
        blockers.append("Human Setup Mode timestamps are out of order.")
    if not all(setup["checks"].values()):
        blockers.append("Human Setup Mode checklist is incomplete.")
    return blockers


def setup_result_safe_to_render(setup: dict[str, Any]) -> bool:
    return (
        exact_schema(setup, SETUP_RESULT_SCHEMA)
        and setup["status"] in {"passed", "failed"}
        and re.fullmatch(r"[0-9a-f]{40}", setup["firmware_commit"]) is not None
        and bool(setup["device"])
        and not contains_control_character(setup["device"])
        and len(setup["device"]) <= 128
        and valid_timestamp(setup["started_at"])
        and valid_timestamp(setup["ended_at"])
        and set(setup["checks"]) == SETUP_CHECKS
        and all(type(value) is bool for value in setup["checks"].values())
        and setup["redaction_statement"]
        == "No Wi-Fi passwords or credentials recorded."
    )


def result_row(
    name: str, summary: dict[str, Any] | None, validation_blockers: list[str]
) -> str:
    if summary is None:
        return f"| {name} | MISSING |  |  |  |  |  |  |  |  |  |"
    status = (
        "PASS"
        if summary.get("status") == "passed" and not validation_blockers
        else "BLOCKED"
    )
    return (
        f"| {name} | {status} | {summary.get('started_at', '')} | "
        f"{summary.get('ended_at', '')} | {summary.get('reset_count', '')} | "
        f"{summary.get('watchdog_count', '')} | {summary.get('display_error_count', '')} | "
        f"{summary.get('i2c_error_count', '')} | {summary.get('wifi_error_count', '')} | "
        f"{summary.get('minimum_free_heap_bytes', '')} | "
        f"`{summary.get('raw_log_sha256', '')}` |"
    )


def render_report(
    manifest: dict[str, Any],
    smoke: dict[str, Any] | None,
    soak: dict[str, Any] | None,
    setup: dict[str, Any] | None,
    smoke_blockers: list[str],
    soak_blockers: list[str],
    setup_blockers: list[str],
) -> tuple[str, bool]:
    validation_blockers = [*smoke_blockers, *soak_blockers, *setup_blockers]
    passed = all(
        document is not None and document.get("status") == "passed"
        for document in (smoke, soak, setup)
    )
    passed = passed and not validation_blockers
    status = "PASS" if passed else "BLOCKED"
    smoke_view = smoke or {}
    setup_view = setup or {}
    blockers: list[str] = []
    for name, document in (
        ("2-hour smoke", smoke),
        ("24-hour soak", soak),
        ("Human Setup Mode", setup),
    ):
        if document is None:
            blockers.append(f"{name} evidence is missing.")
        elif document.get("status") != "passed":
            blockers.append(f"{name} evidence did not pass.")
    blockers.extend(validation_blockers)
    commands = manifest.get("test_commands", {})
    limitations = manifest.get("known_limitations", [])
    out_of_scope = manifest.get("out_of_scope", [])
    lines = [
        "# M0 Acceptance Report",
        "",
        f"**Status:** {status}",
        "",
        "## Tracking",
        "",
        "- Parent specification: [#1](https://github.com/Vectorking-50kg/s3-rlcd-deck/issues/1)",
        "- Acceptance report ticket: [#11](https://github.com/Vectorking-50kg/s3-rlcd-deck/issues/11)",
        "- Evidence gates: [#8](https://github.com/Vectorking-50kg/s3-rlcd-deck/issues/8), [#9](https://github.com/Vectorking-50kg/s3-rlcd-deck/issues/9), and [#10](https://github.com/Vectorking-50kg/s3-rlcd-deck/issues/10)",
        "",
        "## Versions",
        "",
        f"- Firmware commit: `{manifest.get('firmware_commit', '')}`",
        f"- ESP-IDF {manifest.get('esp_idf_version', '')}",
        f"- LVGL {manifest.get('lvgl_version', '')}",
        "- HIL toolchain: "
        + (
            "ESP-IDF v6.0.2"
            if smoke_view.get("toolchain_version") == "ESP-IDF v6.0.2"
            else "Unavailable"
        ),
        "",
        "## Automated evidence",
        "",
        "| Test | Status | Started | Ended | Reset | Watchdog | Display errors | I²C errors | Wi-Fi errors | Minimum heap | Raw log SHA-256 |",
        "| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |",
        result_row("2-hour smoke", smoke, smoke_blockers),
        result_row("24-hour soak", soak, soak_blockers),
        "",
        "## Human Setup Mode acceptance",
        "",
        "- Status: "
        + (
            "PASS"
            if setup_view.get("status") == "passed" and not setup_blockers
            else "BLOCKED"
        ),
        f"- Firmware commit: `{setup_view.get('firmware_commit', 'Unavailable')}`",
        f"- Device: {markdown_escape(setup_view.get('device', 'Unavailable'))}",
        f"- Started: {setup_view.get('started_at', 'Unavailable')}",
        f"- Ended: {setup_view.get('ended_at', 'Unavailable')}",
        f"- Redaction: {setup_view.get('redaction_statement', 'Unavailable')}",
        "",
        "| Check | Result |",
        "| --- | --- |",
    ]
    setup_checks = setup_view.get("checks", {})
    for key, label in SETUP_CHECK_LABELS:
        check_status = "PASS" if setup_checks.get(key) is True else "BLOCKED"
        lines.append(f"| {label} | {check_status} |")
    lines.extend(["", "## Test commands", ""])
    for name in ("host", "smoke", "soak", "setup"):
        lines.append(f"- {name}: `{commands.get(name, '')}`")
    lines.extend(["", "## Blockers", ""])
    lines.extend(f"- {item}" for item in blockers)
    if not blockers:
        lines.append("- None.")
    lines.extend(["", "## Known limitations", ""])
    lines.extend(f"- {markdown_escape(item)}" for item in limitations)
    if not limitations:
        lines.append("- None.")
    lines.extend(["", "## Out of Scope", ""])
    lines.extend(f"- {markdown_escape(item)}" for item in out_of_scope)
    if not out_of_scope:
        lines.append("- None.")
    lines.extend(
        [
            "",
            "## Conclusion",
            "",
            (
                "All M0 release gates are satisfied."
                if passed
                else "M0 remains blocked until every release gate has passed."
            ),
            "",
        ]
    )
    return "\n".join(lines), passed


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Generate the M0 acceptance report.")
    parser.add_argument("--manifest", required=True, type=pathlib.Path)
    parser.add_argument("--output", required=True, type=pathlib.Path)
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    try:
        manifest = load_json(arguments.manifest)
        validate_manifest(manifest)
        smoke = load_optional_evidence(arguments.manifest, manifest.get("smoke_summary"))
        soak = load_optional_evidence(arguments.manifest, manifest.get("soak_summary"))
        setup = load_optional_evidence(arguments.manifest, manifest.get("setup_result"))
        smoke_config, smoke_config_hash = controlled_config("2-hour smoke")
        soak_config, soak_config_hash = controlled_config("24-hour soak")
        smoke_blockers: list[str] = []
        soak_blockers: list[str] = []
        setup_blockers: list[str] = []
        if smoke is not None:
            smoke_blockers.extend(
                validate_hil_summary(
                    "2-hour smoke",
                    smoke,
                    manifest["firmware_commit"],
                    smoke_config,
                    smoke_config_hash,
                )
            )
        if soak is not None:
            soak_blockers.extend(
                validate_hil_summary(
                    "24-hour soak",
                    soak,
                    manifest["firmware_commit"],
                    soak_config,
                    soak_config_hash,
                )
            )
        if setup is not None:
            setup_blockers.extend(
                validate_setup_result(setup, manifest["firmware_commit"])
            )
        smoke_view = (
            smoke
            if smoke is None or hil_summary_safe_to_render(smoke)
            else {"status": "invalid"}
        )
        soak_view = (
            soak
            if soak is None or hil_summary_safe_to_render(soak)
            else {"status": "invalid"}
        )
        setup_view = (
            setup
            if setup is None or setup_result_safe_to_render(setup)
            else {"status": "invalid"}
        )
        report, passed = render_report(
            manifest,
            smoke_view,
            soak_view,
            setup_view,
            smoke_blockers,
            soak_blockers,
            setup_blockers,
        )
        arguments.output.parent.mkdir(parents=True, exist_ok=True)
        arguments.output.write_text(report, encoding="utf-8")
    except (OSError, ReportFailure) as error:
        print(f"M0 acceptance report failed: {error}", file=sys.stderr)
        return 2
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
