#!/usr/bin/env python3
"""Record a secret-free, same-LAN Pairing v2 real-Deck acceptance run."""

from __future__ import annotations

import argparse
import datetime
import hashlib
import ipaddress
import json
import os
import pathlib
import re
import socket
import subprocess
import sys
import time
from typing import Any

TOOLS_ROOT = pathlib.Path(__file__).resolve().parent
if str(TOOLS_ROOT) not in sys.path:
    sys.path.insert(0, str(TOOLS_ROOT))

import m1_acceptance as m1


REPOSITORY_ROOT = TOOLS_ROOT.parent
PAIRING_STATES = ("active", "authenticating", "proof_verified", "paired")
REQUIRED_CHECKS = (
    "clean_source",
    "preflight_passed",
    "companion_identity_matches",
    "firmware_identity_matches",
    "single_expected_boot",
    "mac_normal_lan_unchanged",
    "setup_mode_not_entered",
    "pairing_window_observed",
    "authentication_observed",
    "device_link_proof_observed",
    "transaction_committed",
    "device_link_online",
    "credentials_absent_from_evidence",
)


def utc_now() -> str:
    return datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")


class PairingObservation:
    def __init__(self) -> None:
        self.events: list[tuple[str, str]] = []
        self.boot_count = 0
        self.minimum_free_heap_bytes = 0
        self.firmware_commit = ""
        self.setup_entered = False

    def observe(self, event: dict[str, Any]) -> None:
        event_type = event.get("type")
        if event_type == "boot_ok":
            self.boot_count += 1
            heap = event.get("minimum_free_heap_bytes")
            if type(heap) is int and heap >= 0:
                self.minimum_free_heap_bytes = heap
        elif event_type == "deck_build_identity":
            commit = event.get("firmware_commit")
            if isinstance(commit, str):
                self.firmware_commit = commit
        elif event_type == "setup_state" and event.get("active") is True:
            self.setup_entered = True
        elif event_type == "pairing_v2":
            state = event.get("state")
            if isinstance(state, str):
                self.events.append(("pairing_v2", state))
        elif event_type == "companion_link_state":
            state = event.get("state")
            if isinstance(state, str):
                self.events.append(("companion_link_state", state))

    def checks(
        self,
        commit: str,
        clean_source: bool,
        preflight_passed: bool,
        companion_matches: bool,
        network_unchanged: bool,
        evidence_clean: bool,
    ) -> dict[str, bool]:
        positions: dict[str, int] = {}
        for index, (event_type, state) in enumerate(self.events):
            if event_type == "pairing_v2" and state in PAIRING_STATES and state not in positions:
                positions[state] = index
        active_observed = "active" in positions
        authentication_observed = active_observed and "authenticating" in positions and \
            positions["active"] < positions["authenticating"]
        proof_observed = authentication_observed and "proof_verified" in positions and \
            positions["authenticating"] < positions["proof_verified"]
        committed = proof_observed and "paired" in positions and \
            positions["proof_verified"] < positions["paired"]
        paired_at = positions.get("paired", len(self.events))
        online_after_pairing = any(
            index > paired_at and event_type == "companion_link_state" and state == "online"
            for index, (event_type, state) in enumerate(self.events)
        )
        return {
            "clean_source": clean_source,
            "preflight_passed": preflight_passed,
            "companion_identity_matches": companion_matches,
            "firmware_identity_matches": self.firmware_commit == commit,
            "single_expected_boot": self.boot_count == 1 and self.minimum_free_heap_bytes > 0,
            "mac_normal_lan_unchanged": network_unchanged,
            "setup_mode_not_entered": not self.setup_entered,
            "pairing_window_observed": active_observed,
            "authentication_observed": authentication_observed,
            "device_link_proof_observed": proof_observed,
            "transaction_committed": committed,
            "device_link_online": online_after_pairing,
            "credentials_absent_from_evidence": evidence_clean,
        }


def network_fingerprint(interface: str) -> str:
    if sys.platform != "darwin" or re.fullmatch(r"en[0-9]+", interface) is None:
        raise m1.AcceptanceFailure("Pairing v2 physical acceptance requires an explicit macOS enN interface")
    try:
        address = subprocess.run(
            ["/usr/sbin/ipconfig", "getifaddr", interface],
            check=True,
            capture_output=True,
            text=True,
            timeout=3,
        ).stdout.strip()
        route = subprocess.run(
            ["/sbin/route", "-n", "get", "default"],
            check=True,
            capture_output=True,
            text=True,
            timeout=3,
        ).stdout
    except (OSError, subprocess.SubprocessError) as error:
        raise m1.AcceptanceFailure("normal LAN identity is unavailable") from error
    try:
        parsed = ipaddress.ip_address(address)
    except ValueError as error:
        raise m1.AcceptanceFailure("normal LAN address is invalid") from error
    route_interface = ""
    for line in route.splitlines():
        if line.strip().startswith("interface:"):
            route_interface = line.split(":", 1)[1].strip()
            break
    if not isinstance(parsed, ipaddress.IPv4Address) or parsed.is_loopback or parsed.is_link_local or \
       parsed.is_multicast or route_interface != interface:
        raise m1.AcceptanceFailure("selected interface is not the active normal LAN route")
    return hashlib.sha256(f"{interface}\0{address}".encode()).hexdigest()


def companion_identity(executable: pathlib.Path, commit: str) -> bool:
    expected = f"s3deck-companion 0.1.0-dev (commit {commit})"
    try:
        observed = subprocess.run(
            [str(executable), "--version"],
            check=True,
            capture_output=True,
            text=True,
            timeout=5,
        ).stdout.strip()
    except (OSError, subprocess.SubprocessError):
        return False
    return observed == expected


def companion_command(executable: pathlib.Path) -> list[str]:
    # The native shell issues a one-time browser grant and opens the exact
    # loopback management console. No long-lived management Token enters argv,
    # stdout, evidence, or the user procedure.
    return [str(executable), "--open-console"]


def listener_is_free() -> bool:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as connection:
        connection.settimeout(0.25)
        return connection.connect_ex(("127.0.0.1", 7777)) != 0


def pairing_terminal(event: dict[str, Any]) -> bool:
    if event.get("type") != "pairing_v2":
        return False
    state = event.get("state")
    if state in {"error", "expired"}:
        raise m1.AcceptanceFailure(f"Deck Pairing v2 ended in the safe terminal state {state}")
    return state == "paired"


def write_summary(path: pathlib.Path, document: dict[str, Any]) -> None:
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(document, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.chmod(temporary, 0o600)
    temporary.replace(path)


def wait_for_user_ready(input_stream: Any, output_stream: Any) -> None:
    output_stream.write(
        "Preflight 已完成。确认你已在 Mac 前、Deck 屏幕可见，然后按 Enter 开始烧录和限时配对。\n"
        "不要在此输入六位码；验证码只能输入 Companion 管理网页。\n"
    )
    output_stream.flush()
    if input_stream.readline() == "":
        raise m1.AcceptanceFailure("user readiness confirmation was unavailable before app flash")


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", required=True, help="explicit Deck /dev/cu.usbmodem... port")
    parser.add_argument("--result-dir", required=True, type=pathlib.Path)
    parser.add_argument("--interface", default="en0")
    parser.add_argument("--timeout", type=float, default=240.0)
    parser.add_argument(
        "--wait-for-user",
        action="store_true",
        help="pause after preflight; press Enter before app-only flash starts the bounded Pairing Window",
    )
    parser.add_argument(
        "--development",
        action="store_true",
        help="skip preflight and app-only flash; result can never be formal PASS",
    )
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    result_dir = arguments.result_dir.resolve()
    result_dir.mkdir(parents=True, exist_ok=True)
    os.chmod(result_dir, 0o700)
    started_at = utc_now()
    commit = "unknown"
    checks = {name: False for name in REQUIRED_CHECKS}
    errors: list[str] = []
    observation = PairingObservation()
    secrets = m1.SensitiveValueTracker()
    serial_evidence: m1.SerialEvidence | None = None
    companion_process: subprocess.Popen[bytes] | None = None
    summary: dict[str, Any] = {}
    summary_write_failed = False
    clean_source = False
    preflight_passed = False
    companion_matches = False
    initial_network = ""
    final_network = ""
    try:
        commit = m1.command_output(["git", "rev-parse", "HEAD"])
        clean_source = m1.source_tree_clean()
        if not clean_source:
            raise m1.AcceptanceFailure("formal Pairing v2 acceptance requires a clean source tree")
        initial_network = network_fingerprint(arguments.interface)
        if arguments.development:
            environment = dict(os.environ)
            preflight_passed = False
        else:
            environment = m1.run_preflight(result_dir / "preflight.log")
            preflight_passed = True
            if arguments.wait_for_user:
                wait_for_user_ready(sys.stdin, sys.stdout)
            m1.run_app_flash(arguments.port, result_dir / "app-flash.log", environment)
        executable = m1.companion_for_current_host(commit)
        companion_matches = companion_identity(executable, commit)
        if not companion_matches:
            raise m1.AcceptanceFailure("Companion artifact does not match the clean source commit")
        if not listener_is_free():
            raise m1.AcceptanceFailure("close the currently running Companion before formal acceptance")
        serial_module = m1.load_serial_module(environment)
        serial_evidence = m1.SerialEvidence(
            serial_module,
            arguments.port,
            result_dir / "serial.jsonl",
            20.0,
            secrets,
            observation.observe,
        )
        serial_evidence.fresh_boot(serial_module, arguments.port, 20.0)
        serial_evidence.command(b"DECK_BUILD_IDENTITY\n")
        serial_evidence.event(
            lambda event: event.get("type") == "deck_build_identity" and
            event.get("firmware_commit") == commit,
            "Deck build identity",
            5.0,
        )
        companion_process = subprocess.Popen(
            companion_command(executable),
            cwd=REPOSITORY_ROOT,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
        deadline = time.monotonic() + 10.0
        while listener_is_free() and time.monotonic() < deadline:
            if companion_process.poll() is not None:
                raise m1.AcceptanceFailure("same-commit Companion exited before opening management Web")
            time.sleep(0.1)
        if listener_is_free():
            raise m1.AcceptanceFailure("same-commit Companion did not open management Web")
        print("保持 Mac 在当前家庭局域网。若 Deck 已配对，请短按 BOOT 打开 Pairing Window。")
        serial_evidence.event(
            lambda event: event.get("type") == "pairing_v2" and event.get("state") == "active",
            "Pairing Window",
            min(arguments.timeout, 120.0),
        )
        print("打开 http://127.0.0.1:7777，在设备页扫描并选择 Deck；把 Deck 屏幕六位码输入网页。")
        serial_evidence.event(pairing_terminal, "Pairing v2 transaction", arguments.timeout)
        serial_evidence.event(
            lambda event: event.get("type") == "companion_link_state" and
            event.get("state") == "online" and event.get("has_active_profile") is True,
            "post-Pairing authenticated Device Link",
            30.0,
        )
        final_network = network_fingerprint(arguments.interface)
        if arguments.development:
            errors.append("development mode cannot satisfy the formal preflight/flash gate")
    except (m1.AcceptanceFailure, OSError, subprocess.SubprocessError) as error:
        errors.append(str(error))
    finally:
        if serial_evidence is not None:
            try:
                serial_evidence.close()
            except Exception:  # Preserve a failed summary even if cleanup fails.
                errors.append("serial evidence close failed")
        if companion_process is not None:
            try:
                companion_process.terminate()
                try:
                    companion_process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    companion_process.kill()
                    companion_process.wait(timeout=5)
                    errors.append("Companion required forced shutdown")
            except (OSError, subprocess.SubprocessError):
                errors.append("Companion shutdown failed")
        if initial_network and not final_network:
            try:
                final_network = network_fingerprint(arguments.interface)
            except m1.AcceptanceFailure:
                pass
        evidence_clean = secrets.clean() and m1.evidence_directory_is_redacted(
            result_dir,
            secrets.values(),
        )
        checks = observation.checks(
            commit,
            clean_source,
            preflight_passed,
            companion_matches,
            bool(initial_network) and initial_network == final_network,
            evidence_clean,
        )
        if not arguments.development and not errors and not all(checks.values()):
            errors.append("one or more Pairing v2 evidence gates were not satisfied")
        summary = {
            "schema_version": 1,
            "issue": 91,
            "status": "passed" if not errors and all(checks.values()) else "failed",
            "source_commit": commit,
            "started_at": started_at,
            "ended_at": utc_now(),
            "checks": checks,
            "pairing_states": [state for event_type, state in observation.events if event_type == "pairing_v2"],
            "minimum_free_heap_bytes": observation.minimum_free_heap_bytes,
            "errors": errors,
            "credentials_retained": False,
            "network_identity": "sha256-only",
        }
        try:
            write_summary(result_dir / "summary.json", summary)
        except OSError as error:
            print(f"Pairing v2 acceptance could not write summary: {error}", file=sys.stderr)
            summary_write_failed = True
    if summary_write_failed or summary["status"] != "passed":
        print("Pairing v2 acceptance FAILED; see the redacted summary.", file=sys.stderr)
        return 1
    print("Pairing v2 acceptance PASSED.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
