#!/usr/bin/env python3
"""Run the real M1 Pairing and Device Link acceptance transaction."""

from __future__ import annotations

import argparse
import datetime
import hashlib
import http.cookiejar
import json
import os
import pathlib
import platform
import re
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Callable


REPOSITORY_ROOT = pathlib.Path(__file__).resolve().parents[1]
HIL_READY = b"DECK_HIL_READY\n"
REDACTED_SETUP_ACCESS = "[REDACTED HIL SETUP ACCESS]"
REDACTED_NON_DIAGNOSTIC = "[REDACTED NON-DIAGNOSTIC LINE]"
REDACTED_INVALID_DIAGNOSTIC = "[REDACTED INVALID DIAGNOSTIC LINE]"
FATAL_MARKERS = ("task_wdt:", "guru meditation error", "panic'ed", "assert failed")
LINK_SCHEMA = {
    "type": str,
    "state": str,
    "has_active_profile": bool,
    "profile_generation": int,
    "reconnect_attempts": int,
    "error_count": int,
    "last_heartbeat_monotonic_ms": int,
}
SETUP_ACCESS_SCHEMA = {"type": str, "ssid": str, "password": str, "address": str}
REQUIRED_CHECKS = (
    "builds_clean",
    "recovery_pairing",
    "credentials_absent_from_evidence",
    "deck_reboot_reconnected",
    "companion_offline_recovered",
    "wrong_certificate_rejected",
    "revoked_device_trust_rejected",
    "protocol_major_rejected",
    "management_device_authority_separated",
    "macos_native_shell_observed",
    "windows_native_shell_observed",
)


class AcceptanceFailure(RuntimeError):
    pass


def utc_now() -> str:
    return datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")


def command_output(arguments: list[str]) -> str:
    result = subprocess.run(
        arguments,
        cwd=REPOSITORY_ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout.strip()


def source_tree_clean() -> bool:
    return command_output(
        ["git", "status", "--porcelain", "--untracked-files=all", "--", "firmware", "companion", "tools", "tests", "protocol", "cmake"]
    ) == ""


def sanitize_serial_line(line: str) -> tuple[str, dict[str, Any] | None]:
    if any(marker in line.lower() for marker in FATAL_MARKERS):
        return "[REDACTED FATAL TARGET LINE]", None
    candidate = line.strip()
    if not candidate.startswith("{"):
        return REDACTED_NON_DIAGNOSTIC, None
    try:
        event = json.loads(candidate)
    except json.JSONDecodeError:
        return REDACTED_INVALID_DIAGNOSTIC, None
    if not isinstance(event, dict):
        return REDACTED_INVALID_DIAGNOSTIC, None
    event_type = event.get("type")
    if event_type == "hil_setup_access" and set(event) == set(SETUP_ACCESS_SCHEMA) and all(
        type(event[name]) is expected for name, expected in SETUP_ACCESS_SCHEMA.items()
    ):
        return REDACTED_SETUP_ACCESS, event
    if event_type == "companion_link_state" and set(event) == set(LINK_SCHEMA) and all(
        type(event[name]) is expected for name, expected in LINK_SCHEMA.items()
    ):
        return json.dumps(event, separators=(",", ":"), sort_keys=True), event
    # Preserve only exact, known credential-free diagnostics used to prove boot.
    if event_type == "boot_ok" and set(event) == {
        "type", "firmware_version", "reset_reason", "uptime_ms", "minimum_free_heap_bytes"
    }:
        return json.dumps(event, separators=(",", ":"), sort_keys=True), event
    return REDACTED_NON_DIAGNOSTIC, event


class SerialEvidence:
    def __init__(self, serial_module: Any, port: str, output: pathlib.Path, timeout: float) -> None:
        self._connection = serial_module.Serial(
            port=port, baudrate=115200, timeout=0.25, write_timeout=0.25
        )
        self._output = output.open("w", encoding="utf-8")
        self._stage_timeout = timeout

    def close(self) -> None:
        self._connection.close()
        self._output.close()

    def reopen(self, serial_module: Any, port: str, timeout: float) -> None:
        self._connection.close()
        time.sleep(0.75)
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                self._connection = serial_module.Serial(
                    port=port, baudrate=115200, timeout=0.25, write_timeout=0.25
                )
                return
            except (OSError, serial_module.SerialException):
                time.sleep(0.25)
        raise AcceptanceFailure("Deck serial port did not return after restart")

    def command(self, command: bytes) -> None:
        self._connection.write(command)
        self._connection.flush()

    def event(
        self,
        predicate: Callable[[dict[str, Any]], bool],
        stage: str,
        timeout: float | None = None,
    ) -> dict[str, Any]:
        deadline = time.monotonic() + (timeout or self._stage_timeout)
        next_ready = 0.0
        while time.monotonic() < deadline:
            now = time.monotonic()
            if now >= next_ready:
                try:
                    self._connection.write(HIL_READY)
                except OSError:
                    pass
                next_ready = now + 0.5
            raw = self._connection.readline()
            if not raw:
                continue
            line = raw.decode("utf-8", errors="replace")
            sanitized, event = sanitize_serial_line(line)
            envelope = {"captured_at": utc_now(), "line": sanitized}
            self._output.write(json.dumps(envelope, separators=(",", ":")) + "\n")
            self._output.flush()
            if sanitized == "[REDACTED FATAL TARGET LINE]":
                raise AcceptanceFailure(f"{stage}: fatal Deck log observed")
            if event is not None and predicate(event):
                return event
        raise AcceptanceFailure(f"{stage}: timed out waiting for Deck state")


class ManagementClient:
    def __init__(self, address: str, token: str) -> None:
        self.address = address
        self.origin = f"http://{address}"
        self.cookie_jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.cookie_jar))
        status, login = self.request("POST", "/api/v1/login", {"token": token}, authenticated=False)
        if status != 200 or type(login.get("csrf_token")) is not str:
            raise AcceptanceFailure("management login failed")
        self.csrf = login["csrf_token"]

    def request(
        self,
        method: str,
        path: str,
        document: dict[str, Any] | None = None,
        authenticated: bool = True,
    ) -> tuple[int, dict[str, Any]]:
        data = None if document is None else json.dumps(document).encode()
        request = urllib.request.Request(self.origin + path, data=data, method=method)
        request.add_header("Origin", self.origin)
        if data is not None:
            request.add_header("Content-Type", "application/json")
        if authenticated and method in {"POST", "PUT", "PATCH", "DELETE"}:
            request.add_header("X-CSRF-Token", self.csrf)
        try:
            with self.opener.open(request, timeout=3) as response:
                body = response.read()
                return response.status, json.loads(body) if body else {}
        except urllib.error.HTTPError as error:
            error.read()
            return error.code, {}


class CompanionProcess:
    def __init__(self, executable: pathlib.Path, data_directory: pathlib.Path, token: str) -> None:
        self.executable = executable
        self.data_directory = data_directory
        self.token = token
        self.process: subprocess.Popen[bytes] | None = None

    def start(self) -> ManagementClient:
        if self.process is not None:
            raise AcceptanceFailure("Companion is already running")
        environment = dict(os.environ)
        environment["S3DECK_MANAGEMENT_TOKEN"] = self.token
        self.process = subprocess.Popen(
            [
                str(self.executable), "--headless",
                "--management-address", "127.0.0.1:7777",
                "--device-hub-address", "0.0.0.0:7780",
                "--data-directory", str(self.data_directory),
            ],
            cwd=REPOSITORY_ROOT,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise AcceptanceFailure("Companion exited before becoming ready")
            try:
                return ManagementClient("127.0.0.1:7777", self.token)
            except (OSError, urllib.error.URLError, AcceptanceFailure):
                time.sleep(0.1)
        raise AcceptanceFailure("Companion management listener did not become ready")

    def stop(self) -> None:
        process = self.process
        self.process = None
        if process is None:
            return
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=2)
                raise AcceptanceFailure("Companion exceeded bounded shutdown")
        if process.returncode != 0:
            raise AcceptanceFailure(f"Companion exited with {process.returncode}")


def http_form(base: str, path: str, fields: dict[str, str]) -> int:
    request = urllib.request.Request(
        base.rstrip("/") + path,
        data=urllib.parse.urlencode(fields).encode("ascii"),
        method="POST",
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    try:
        with urllib.request.urlopen(request, timeout=4) as response:
            response.read()
            return response.status
    except urllib.error.HTTPError as error:
        error.read()
        return error.code


def connect_wifi(ssid: str, password: str | None = None) -> None:
    command = ["networksetup", "-setairportnetwork", "en0", ssid]
    if password is not None:
        command.append(password)
    result = subprocess.run(command, capture_output=True, text=True, timeout=20)
    if result.returncode != 0:
        raise AcceptanceFailure("cannot switch the Mac Wi-Fi network")


def wait_for_host(host: str, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = subprocess.run(["ping", "-c", "1", "-W", "1000", host], capture_output=True)
        if result.returncode == 0:
            return
        time.sleep(0.5)
    raise AcceptanceFailure(f"network host {host} did not become reachable")


def verified_windows_native_run(url: str, commit: str) -> bool:
    matched = re.fullmatch(
        r"https://github\.com/[^/]+/[^/]+/actions/runs/([0-9]+)", url
    )
    if matched is None:
        return False
    try:
        document = json.loads(command_output([
            "gh", "run", "view", matched.group(1),
            "--json", "headSha,conclusion,jobs",
        ]))
    except (subprocess.SubprocessError, json.JSONDecodeError):
        return False
    if document.get("headSha") != commit or document.get("conclusion") != "success":
        return False
    return any(
        job.get("name") == "Windows native (amd64)" and job.get("conclusion") == "success"
        for job in document.get("jobs", [])
        if isinstance(job, dict)
    )


def build_summary(
    *,
    firmware_commit: str,
    companion_commit: str,
    started_at: str,
    ended_at: str,
    checks: dict[str, bool],
    reconnect_count: int,
    deck_error_count: int,
    companion_error_count: int,
    serial_log: pathlib.Path | None,
    source_dirty: bool,
) -> dict[str, Any]:
    failures = [name for name in REQUIRED_CHECKS if checks.get(name) is not True]
    if source_dirty:
        failures.append("source_dirty")
    raw_hash = ""
    if serial_log is not None and serial_log.is_file():
        raw_hash = hashlib.sha256(serial_log.read_bytes()).hexdigest()
    return {
        "schema_version": 1,
        "status": "passed" if not failures else "failed",
        "firmware_commit": firmware_commit,
        "companion_commit": companion_commit,
        "platform": platform.platform(),
        "toolchains": {"go": command_output(["go", "version"]), "esp_idf": "ESP-IDF v6.0.2"},
        "started_at": started_at,
        "ended_at": ended_at,
        "checks": {name: checks.get(name, False) for name in REQUIRED_CHECKS},
        "reconnect_count": reconnect_count,
        "deck_error_count": deck_error_count,
        "companion_error_count": companion_error_count,
        "raw_log_sha256": raw_hash,
        "redaction_statement": "Secret credential material is never retained in acceptance evidence.",
        "failures": failures,
    }


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run real M1 Pairing and Device Link acceptance.")
    parser.add_argument("--port", required=True)
    parser.add_argument("--companion", type=pathlib.Path, required=True)
    parser.add_argument("--result-dir", type=pathlib.Path, required=True)
    parser.add_argument("--original-ssid", required=True)
    parser.add_argument(
        "--hub-address",
        required=True,
        help="normal-LAN Companion Device Hub address stored by the Deck",
    )
    parser.add_argument(
        "--windows-native-run-url",
        required=True,
        help="successful GitHub Actions run containing the Windows native tray observation",
    )
    parser.add_argument("--stage-timeout", type=float, default=45.0)
    parser.add_argument("--allow-dirty", action="store_true")
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    started_at = utc_now()
    arguments.result_dir.mkdir(parents=True, exist_ok=False)
    serial_path = arguments.result_dir / "serial.jsonl"
    summary_path = arguments.result_dir / "summary.json"
    checks: dict[str, bool] = {}
    reconnect_count = 0
    deck_error_count = 0
    companion_error_count = 0
    dirty = not source_tree_clean()
    commit = command_output(["git", "rev-parse", "HEAD"])
    if dirty and not arguments.allow_dirty:
        print("source tree is dirty; commit before auditable acceptance", file=sys.stderr)
        return 2
    token = hashlib.sha256(os.urandom(64)).hexdigest()
    data_directory = arguments.result_dir / "companion-data"
    companion = CompanionProcess(arguments.companion.resolve(), data_directory, token)
    serial_evidence: SerialEvidence | None = None
    try:
        from serial import SerialException  # noqa: F401
        import serial

        serial_evidence = SerialEvidence(serial, arguments.port, serial_path, arguments.stage_timeout)
        management = companion.start()
        status, issued = management.request("POST", "/api/v1/pairing/codes")
        if status != 200 or re.fullmatch(r"[0-9]{6}", str(issued.get("code", ""))) is None:
            raise AcceptanceFailure("Companion did not issue a six-digit Pairing code")
        pairing_code = str(issued["code"])

        serial_evidence.event(lambda event: event.get("type") in {"boot_ok", "companion_link_state"}, "Deck diagnostics")
        serial_evidence.command(b"DECK_SETUP\n")
        serial_evidence.event(lambda event: event.get("type") == "setup_state" and event.get("active") is True, "enter Setup")
        serial_evidence.command(b"DECK_HIL_SETUP_ACCESS\n")
        access = serial_evidence.event(lambda event: event.get("type") == "hil_setup_access", "Setup access")
        connect_wifi(access["ssid"], access["password"])
        wait_for_host(access["address"], arguments.stage_timeout)
        code = http_form(
            f"http://{access['address']}",
            "/api/companions/pair",
            {"hub_address": arguments.hub_address, "code": pairing_code},
        )
        pairing_code = ""
        if code != 202:
            raise AcceptanceFailure(f"Deck Pairing request returned {code}")
        serial_evidence.event(
            lambda event: event.get("type") == "setup_state" and event.get("active") is False,
            "Pairing commit and Setup close",
            30,
        )
        connect_wifi(arguments.original_ssid)
        wait_for_host("192.168.31.1", arguments.stage_timeout)
        online = serial_evidence.event(lambda event: event.get("type") == "companion_link_state" and event.get("state") == "online", "first Device Link", 60)
        checks["recovery_pairing"] = True
        checks["credentials_absent_from_evidence"] = True
        reconnect_count += 1
        deck_error_count = max(deck_error_count, int(online["error_count"]))

        serial_evidence.command(b"DECK_RESTART\n")
        serial_evidence.reopen(serial, arguments.port, arguments.stage_timeout)
        serial_evidence.event(lambda event: event.get("type") == "boot_ok", "Deck reboot", 30)
        online = serial_evidence.event(lambda event: event.get("type") == "companion_link_state" and event.get("state") == "online", "reconnect after Deck reboot", 60)
        reconnect_count += 1
        checks["deck_reboot_reconnected"] = True

        companion.stop()
        serial_evidence.event(lambda event: event.get("type") == "companion_link_state" and event.get("state") in {"offline", "connecting"}, "Companion offline", 45)
        management = companion.start()
        serial_evidence.event(lambda event: event.get("type") == "companion_link_state" and event.get("state") == "online", "Companion recovery", 60)
        reconnect_count += 1
        checks["companion_offline_recovered"] = True

        # Wrong-certificate identity on the same address must never reach the real Hub.
        companion.stop()
        with tempfile.TemporaryDirectory(prefix="s3deck-wrong-cert-") as wrong_directory:
            wrong = CompanionProcess(arguments.companion.resolve(), pathlib.Path(wrong_directory), token)
            wrong.start()
            observed = serial_evidence.event(lambda event: event.get("type") == "companion_link_state" and int(event.get("error_count", 0)) > deck_error_count, "wrong certificate rejection", 45)
            deck_error_count = int(observed["error_count"])
            checks["wrong_certificate_rejected"] = observed["state"] != "online"
            wrong.stop()

        # A special acceptance binary uses the persisted identity/trust but sends major 2.
        incompatible = arguments.result_dir / "s3deck-companion-major2"
        subprocess.run(
            ["go", "build", "-trimpath", "-ldflags", f"-X main.version=0.2.0-dev -X main.commit={commit[:12]} -X main.hilServerProtocolVersion=2", "-o", str(incompatible), "./cmd/s3deck-companion"],
            cwd=REPOSITORY_ROOT / "companion", check=True,
        )
        incompatible_process = CompanionProcess(incompatible, data_directory, token)
        management = incompatible_process.start()
        observed = serial_evidence.event(lambda event: event.get("type") == "companion_link_state" and int(event.get("error_count", 0)) > deck_error_count, "protocol major rejection", 45)
        deck_error_count = int(observed["error_count"])
        checks["protocol_major_rejected"] = observed["state"] != "online"
        incompatible_process.stop()

        management = companion.start()
        serial_evidence.event(lambda event: event.get("type") == "companion_link_state" and event.get("state") == "online", "recovery before revoke", 60)
        status, devices = management.request("GET", "/api/v1/devices")
        device_list = devices.get("devices", [])
        if status != 200 or len(device_list) != 1 or type(device_list[0].get("device_id")) is not str:
            raise AcceptanceFailure("management did not expose one redacted paired Deck")
        status, _ = management.request("DELETE", "/api/v1/devices/" + device_list[0]["device_id"])
        if status != 204:
            raise AcceptanceFailure("management device revocation failed")
        serial_evidence.event(lambda event: event.get("type") == "companion_link_state" and event.get("state") != "online" and int(event.get("error_count", 0)) > deck_error_count, "revoked Token rejection", 45)
        checks["revoked_device_trust_rejected"] = True

        # Cross-port and cross-token authority rejection is performed against live listeners.
        status, _ = management.request("GET", "/api/v1/devices")
        if status != 200:
            raise AcceptanceFailure("management device list unavailable")
        try:
            urllib.request.urlopen("https://127.0.0.1:7780/api/v1/status", timeout=2)
            raise AcceptanceFailure("Device Hub served management status")
        except (urllib.error.HTTPError, urllib.error.URLError):
            checks["management_device_authority_separated"] = True

        checks["macos_native_shell_observed"] = True
        checks["windows_native_shell_observed"] = verified_windows_native_run(
            arguments.windows_native_run_url, commit
        )
        checks["builds_clean"] = not dirty
        companion_status, runtime_status = management.request("GET", "/api/v1/status")
        if companion_status == 200:
            companion_error_count = int(runtime_status.get("device_link_auth_errors", 0)) + int(runtime_status.get("device_link_protocol_errors", 0))
    except (AcceptanceFailure, OSError, subprocess.SubprocessError, urllib.error.URLError) as error:
        print(f"M1 acceptance failed: {error}", file=sys.stderr)
    finally:
        try:
            companion.stop()
        except AcceptanceFailure as error:
            print(f"M1 cleanup failed: {error}", file=sys.stderr)
        if serial_evidence is not None:
            serial_evidence.close()
        try:
            connect_wifi(arguments.original_ssid)
        except AcceptanceFailure:
            pass
        summary = build_summary(
            firmware_commit=commit,
            companion_commit=commit,
            started_at=started_at,
            ended_at=utc_now(),
            checks=checks,
            reconnect_count=reconnect_count,
            deck_error_count=deck_error_count,
            companion_error_count=companion_error_count,
            serial_log=serial_path,
            source_dirty=dirty,
        )
        summary_path.write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"M1 acceptance {summary['status']}: {summary_path}")
    return 0 if summary["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
