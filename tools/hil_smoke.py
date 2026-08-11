#!/usr/bin/env python3

"""CLI for auditable Deck long-duration hardware-in-the-loop smoke runs."""

import argparse
import json
import pathlib
import re
import sys

from hil_smoke_contract import (
    SmokeFailure,
    analyze_capture,
    empty_failure_summary,
    load_capture,
    load_config,
    write_evidence,
)
from hil_smoke_live import (
    discover_deck_port,
    firmware_commit,
    monitor_live,
    prepare_firmware,
    run_plan,
    source_tree_clean,
    toolchain_version,
    utc_now,
)


def valid_commit(value: str) -> bool:
    return re.fullmatch(r"[0-9a-f]{40}", value) is not None


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run or replay the Deck long-duration HIL smoke.")
    subparsers = parser.add_subparsers(dest="command", required=True)
    replay = subparsers.add_parser("replay", help="Analyze an existing timestamped capture.")
    replay.add_argument("--input-file", required=True, type=pathlib.Path)
    replay.add_argument("--config", required=True, type=pathlib.Path)
    replay.add_argument("--result-dir", required=True, type=pathlib.Path)
    replay.add_argument("--firmware-commit", required=True)
    replay.add_argument("--toolchain-version", required=True)
    subparsers.add_parser("discover", help="Print the unique verified Deck serial port.")
    plan = subparsers.add_parser("plan", help="Print the non-destructive live run plan.")
    plan.add_argument("--config", required=True, type=pathlib.Path)
    plan.add_argument("--port")
    run = subparsers.add_parser("run", help="Build, app-flash, and monitor a real Deck.")
    run.add_argument("--config", required=True, type=pathlib.Path)
    run.add_argument("--result-dir", required=True, type=pathlib.Path)
    run.add_argument("--port")
    run.add_argument(
        "--allow-dirty",
        action="store_true",
        help="Allow non-auditable local source changes for troubleshooting only.",
    )
    run.add_argument(
        "--monitor-only",
        action="store_true",
        help="Resume evidence collection without building or flashing.",
    )
    return parser.parse_args()


def print_summary(summary: dict[str, object]) -> None:
    print(
        f"HIL smoke {summary['status']}: "
        f"duration={summary['duration_seconds']:.0f}s "
        f"resets={summary['reset_count']} watchdogs={summary['watchdog_count']} "
        f"minimum_heap={summary['minimum_free_heap_bytes']}"
    )


def run_live(arguments: argparse.Namespace) -> int:
    config, config_document = load_config(arguments.config)
    port = arguments.port or discover_deck_port()
    dirty = not source_tree_clean()
    if not arguments.allow_dirty and dirty:
        raise SmokeFailure(
            "firmware/tools/tests contain uncommitted changes; commit them or use "
            "--allow-dirty only for troubleshooting"
        )
    commit = firmware_commit()
    if not valid_commit(commit):
        raise SmokeFailure("git did not return a full lowercase firmware commit")
    version = toolchain_version()
    try:
        arguments.result_dir.mkdir(parents=True, exist_ok=False)
    except OSError as error:
        raise SmokeFailure(f"cannot create result directory: {error}") from error
    capture_path = arguments.result_dir / "serial.jsonl"
    try:
        if not arguments.monitor_only:
            prepare_firmware(port, arguments.result_dir / "preparation.log")
        monitor_live(
            port,
            config.duration_seconds,
            capture_path,
            config.require_key_event,
            config.require_boot_event,
        )
        envelopes, evidence, redacted = load_capture(capture_path)
        summary = analyze_capture(
            envelopes,
            config,
            commit,
            version,
            redacted,
            source_dirty=dirty,
        )
    except (SmokeFailure, OSError) as error:
        evidence = capture_path.read_bytes() if capture_path.is_file() else b""
        summary = empty_failure_summary(
            commit,
            version,
            str(error),
            utc_now(),
            source_dirty=dirty,
        )
    write_evidence(
        arguments.result_dir,
        evidence,
        config_document,
        summary,
        create_directory=False,
    )
    print_summary(summary)
    return 0 if summary["status"] == "passed" else 1


def replay(arguments: argparse.Namespace) -> int:
    if not valid_commit(arguments.firmware_commit):
        raise SmokeFailure("firmware commit must be a lowercase 40-character SHA")
    if arguments.toolchain_version != "ESP-IDF v6.0.2":
        raise SmokeFailure("HIL requires ESP-IDF v6.0.2")
    config, config_document = load_config(arguments.config)
    envelopes, evidence, redacted = load_capture(arguments.input_file)
    summary = analyze_capture(
        envelopes,
        config,
        arguments.firmware_commit,
        arguments.toolchain_version,
        redacted,
    )
    write_evidence(arguments.result_dir, evidence, config_document, summary)
    print_summary(summary)
    return 0 if summary["status"] == "passed" else 1


def main() -> int:
    arguments = parse_arguments()
    try:
        if arguments.command == "discover":
            print(discover_deck_port())
            return 0
        if arguments.command == "plan":
            config, _ = load_config(arguments.config)
            port = arguments.port or discover_deck_port()
            print(json.dumps(run_plan(port, config.duration_seconds), sort_keys=True))
            return 0
        if arguments.command == "run":
            return run_live(arguments)
        return replay(arguments)
    except SmokeFailure as error:
        print(f"HIL smoke failed: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
