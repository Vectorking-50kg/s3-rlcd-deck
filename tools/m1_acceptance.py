#!/usr/bin/env python3
"""Run the real M1 Pairing and Device Link acceptance transaction."""

from __future__ import annotations

import argparse
import datetime
import hashlib
import http.cookiejar
import importlib
import json
import os
import pathlib
import platform
import re
import signal
import ssl
import subprocess
import sys
import tempfile
import threading
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
    "last_error": str,
    "error_generation": int,
    "last_heartbeat_monotonic_ms": int,
}
SETUP_ACCESS_SCHEMA = {"type": str, "ssid": str, "password": str, "address": str}
BUILD_IDENTITY_SCHEMA = {"type": str, "firmware_commit": str}
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
    "cleanup_restored",
)


class AcceptanceFailure(RuntimeError):
    pass


class AcceptanceTimeout(AcceptanceFailure):
    pass


class SerialDisconnected(AcceptanceFailure):
    pass


def accept_boot_event(event: dict[str, Any], stage: str) -> bool:
    if event.get("type") != "boot_ok":
        return False
    reason = str(event.get("reset_reason", "unknown"))
    # Every M1 boot is initiated through esp_restart(), including the first
    # boot after OpenOCD's app-only flash. Anything else is an unexplained
    # reset and cannot serve as acceptance evidence.
    if reason != "software":
        raise AcceptanceFailure(f"{stage}: unexpected reset reason {reason} observed")
    return True


class SensitiveValueTracker:
    _bare_secret = re.compile(
        r"(?i)(?:authorization\s*:\s*bearer|"
        r"\b(?:device[ _-]?)?token\b\s*[:=]|"
        r"\b(?:certificate[ _-]?)?fingerprint\b\s*[:=]|"
        r"\b(?:pairing[ _-]?)?code\b\s*[:=]\s*[0-9]{6}|"
        r"sha256:[0-9a-f]{64}|"
        r"(?<![A-Za-z0-9_-])[A-Za-z0-9_-]{43}(?![A-Za-z0-9_-]))"
    )

    def __init__(self, *values: str) -> None:
        self._lock = threading.Lock()
        self._values: set[str] = set()
        self._recent_output: list[str] = []
        self._observed = False
        for value in values:
            self.add(value)

    def add(self, value: str) -> None:
        if len(value) >= 4:
            with self._lock:
                self._values.add(value)
                self._observed = self._observed or any(
                    value in text for text in self._recent_output
                )

    def observe(self, text: str) -> bool:
        with self._lock:
            self._recent_output.append(text[-4096:])
            if len(self._recent_output) > 256:
                del self._recent_output[0]
            leaked = self._bare_secret.search(text) is not None or any(
                value in text for value in self._values
            )
            self._observed = self._observed or leaked
            return leaked

    def clean(self) -> bool:
        with self._lock:
            return not self._observed

    def values(self) -> tuple[str, ...]:
        with self._lock:
            return tuple(self._values)


class CleanupTransaction:
    def __init__(self) -> None:
        self.setup_pending = False
        self.setup_ssids: set[str] = set()
        self.trust_may_need_restore = False
        self.original_profiles: dict[str, Any] | None = None
        self.profiles_may_need_restore = False

    def begin_setup(self) -> None:
        self.setup_pending = True

    def observe_setup_access(self, ssid: str) -> None:
        self.setup_ssids.add(ssid)

    def observe_setup_closed(self) -> None:
        self.setup_pending = False

    def needs_compensation(self) -> bool:
        return (
            self.trust_may_need_restore
            or self.profiles_may_need_restore
            or self.setup_pending
        )

    def restored(self) -> bool:
        return not self.needs_compensation()


def utc_now() -> str:
    return datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")


def command_output(
    arguments: list[str],
    environment: dict[str, str] | None = None,
) -> str:
    result = subprocess.run(
        arguments,
        cwd=REPOSITORY_ROOT,
        check=True,
        capture_output=True,
        text=True,
        env=environment,
    )
    return result.stdout.strip()


def toolchain_identity(
    environment: dict[str, str] | None = None,
) -> dict[str, str]:
    identity: dict[str, str] = {}
    for name, command in (("go", ["go", "version"]), ("esp_idf", ["idf.py", "--version"])):
        try:
            identity[name] = command_output(command, environment)
        except (OSError, subprocess.SubprocessError):
            identity[name] = "unavailable"
    return identity


def run_preflight(output: pathlib.Path) -> dict[str, str]:
    commands = (
        ["./tools/test_host.sh"],
        ["./tools/test_companion.sh"],
        ["./tools/idf.sh", "dev", "build"],
        ["./tools/idf.sh", "release", "build"],
    )
    with output.open("w", encoding="utf-8") as log:
        environment = dict(os.environ)
        idf_root = environment.get("IDF_PATH")
        if not idf_root:
            default_idf = pathlib.Path.home() / ".espressif/v6.0.2/esp-idf"
            if default_idf.is_dir():
                export = subprocess.run(
                    ["zsh", "-lc", f"source {default_idf}/export.sh >/dev/null 2>&1 && env -0"],
                    check=True,
                    capture_output=True,
                ).stdout
                environment.update(
                    entry.decode().split("=", 1) for entry in export.split(b"\0") if b"=" in entry
                )
        for command in commands:
            log.write("$ " + " ".join(command) + "\n")
            log.flush()
            result = subprocess.run(
                command,
                cwd=REPOSITORY_ROOT,
                stdout=log,
                stderr=subprocess.STDOUT,
                text=True,
                env=environment,
            )
            if result.returncode != 0:
                raise AcceptanceFailure(f"preflight command failed: {' '.join(command)}")
    return environment


def run_app_flash(port: str, output: pathlib.Path, environment: dict[str, str]) -> None:
    python = pathlib.Path(environment.get("IDF_PYTHON_ENV_PATH", "")) / "bin" / "python"
    if not python.is_file():
        python = pathlib.Path(sys.executable)
    command = [
        str(python),
        str(REPOSITORY_ROOT / "tools/hil_app_flash.py"),
        "--port",
        port,
        "--build-dir",
        str(REPOSITORY_ROOT / "build/dev"),
    ]
    with output.open("w", encoding="utf-8") as log:
        log.write("$ tools/hil_app_flash.py --port [EXPLICIT DECK] --build-dir build/dev\n")
        log.flush()
        result = subprocess.run(
            command,
            cwd=REPOSITORY_ROOT,
            stdout=log,
            stderr=subprocess.STDOUT,
            text=True,
            env=environment,
        )
    if result.returncode != 0:
        raise AcceptanceFailure("safe app-only Deck flashing failed")


def load_serial_module(environment: dict[str, str]) -> Any:
    def valid(module: Any) -> bool:
        serial_factory = getattr(module, "Serial", None)
        serial_exception = getattr(module, "SerialException", None)
        return callable(serial_factory) and isinstance(serial_exception, type) and issubclass(
            serial_exception, Exception
        )

    try:
        ambient = importlib.import_module("serial")
    except ModuleNotFoundError:
        ambient = None
    if valid(ambient):
        return ambient
    python = pathlib.Path(environment.get("IDF_PYTHON_ENV_PATH", "")) / "bin/python"
    if not python.is_file():
        raise AcceptanceFailure("pyserial is unavailable in the pinned toolchain")
    try:
        site_packages = command_output(
            [
                str(python),
                "-c",
                "import site; print(site.getsitepackages()[0])",
            ],
            environment,
        )
        sys.path.insert(0, site_packages)
        sys.modules.pop("serial", None)
        pinned = importlib.import_module("serial")
        if valid(pinned):
            return pinned
    except (ModuleNotFoundError, OSError, subprocess.SubprocessError):
        pass
    raise AcceptanceFailure("pyserial is unavailable in the pinned toolchain")


def text_is_redacted(text: str, sensitive_values: tuple[str, ...] = ()) -> bool:
    lowered = text.lower()
    if any(value and value in text for value in sensitive_values) or \
       SensitiveValueTracker._bare_secret.search(text) is not None:
        return False
    return not any(
        forbidden in lowered
        for forbidden in (
            '"password"',
            '"token"',
            '"certificate',
            '"fingerprint"',
            '"code"',
            "authorization:",
        )
    )


def evidence_is_redacted(
    path: pathlib.Path,
    sensitive_values: tuple[str, ...] = (),
) -> bool:
    if not path.is_file():
        return False
    try:
        return text_is_redacted(path.read_text(encoding="utf-8"), sensitive_values)
    except (OSError, UnicodeError):
        return False


def evidence_directory_is_redacted(
    directory: pathlib.Path,
    sensitive_values: tuple[str, ...] = (),
) -> bool:
    evidence = [path for path in directory.rglob("*") if path.is_file() and path.name != "summary.json"]
    return bool(evidence) and all(
        evidence_is_redacted(path, sensitive_values) for path in evidence
    )


def source_tree_clean() -> bool:
    return command_output(
        ["git", "status", "--porcelain", "--untracked-files=all"]
    ) == ""


def companion_for_current_host(commit: str) -> pathlib.Path:
    if platform.system() != "Darwin":
        raise AcceptanceFailure("real M1 Deck acceptance currently requires the macOS host")
    architecture = command_output(["go", "env", "GOARCH"])
    executable = REPOSITORY_ROOT / "build/companion" / f"darwin-{architecture}" / "s3deck-companion"
    expected = f"s3deck-companion 0.1.0-dev (commit {commit})"
    try:
        observed = command_output([str(executable), "--version"])
    except (OSError, subprocess.SubprocessError) as error:
        raise AcceptanceFailure("current Companion artifact is unavailable") from error
    if observed != expected:
        raise AcceptanceFailure("Companion artifact does not match the clean source commit")
    return executable


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
    if event_type == "deck_build_identity" and set(event) == set(BUILD_IDENTITY_SCHEMA) and all(
        type(event[name]) is expected for name, expected in BUILD_IDENTITY_SCHEMA.items()
    ):
        if re.fullmatch(r"[0-9a-f]{40}", event["firmware_commit"]) is not None:
            return json.dumps(event, separators=(",", ":"), sort_keys=True), event
    # Preserve only exact, known credential-free diagnostics used to prove boot.
    if event_type == "boot_ok" and set(event) == {
        "type", "firmware_version", "reset_reason", "uptime_ms", "minimum_free_heap_bytes"
    }:
        return json.dumps(event, separators=(",", ":"), sort_keys=True), event
    return REDACTED_NON_DIAGNOSTIC, event


def serial_line_may_contain_secret(line: str, sanitized: str) -> bool:
    return sanitized != REDACTED_SETUP_ACCESS


class SerialEvidence:
    def __init__(
        self,
        serial_module: Any,
        port: str,
        output: pathlib.Path,
        timeout: float,
        secrets: SensitiveValueTracker,
    ) -> None:
        self._serial_exception = serial_module.SerialException
        self._connection = self.open_connection(serial_module, port, timeout)
        self._output = output.open("w", encoding="utf-8")
        self._stage_timeout = timeout
        self._secrets = secrets

    @staticmethod
    def open_connection(serial_module: Any, port: str, timeout: float) -> Any:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                return serial_module.Serial(
                    port=port, baudrate=115200, timeout=0.25, write_timeout=0.25
                )
            except (OSError, serial_module.SerialException):
                time.sleep(0.25)
        raise AcceptanceFailure("Deck serial port did not enumerate after app flash")

    def close(self) -> None:
        self._connection.close()
        self._output.close()

    def reopen(self, serial_module: Any, port: str, timeout: float) -> None:
        self._connection.close()
        time.sleep(0.75)
        self._connection = self.open_connection(serial_module, port, timeout)

    def command(self, command: bytes) -> None:
        try:
            self._connection.write(command)
        except (OSError, self._serial_exception) as error:
            raise SerialDisconnected("Deck serial endpoint disconnected") from error

    def fresh_boot(self, serial_module: Any, port: str, timeout: float) -> dict[str, Any]:
        # The app-only flasher resets before this monitor can open. Establish the
        # diagnostic host handshake and first try to catch that pending one-shot
        # event. Only request a second reset when the original event was already
        # emitted before the monitor opened.
        try:
            self.command(HIL_READY)
        except SerialDisconnected:
            self.reopen(serial_module, port, timeout)
            return self.event(
                lambda event: accept_boot_event(event, "post-flash boot"),
                "post-flash reenumerated boot",
                30.0,
            )
        try:
            return self.event(
                lambda event: accept_boot_event(event, "post-flash boot"),
                "post-flash pending boot",
                2.0,
            )
        except SerialDisconnected:
            # The diagnostic firmware deliberately performs a USB detach after
            # an app-only JTAG flash so the host cannot retain OpenOCD's stale
            # CDC endpoint. Reopen the newly enumerated endpoint and preserve
            # this boot; another reset would discard the evidence we need.
            self.reopen(serial_module, port, timeout)
            return self.event(
                lambda event: accept_boot_event(event, "post-flash boot"),
                "post-flash reenumerated boot",
                30.0,
            )
        except AcceptanceTimeout:
            self.command(b"DECK_RESTART\n")
            self.reopen(serial_module, port, timeout)
            return self.event(
                lambda event: accept_boot_event(event, "post-flash boot"),
                "post-flash fresh boot",
                30.0,
            )

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
                except (OSError, self._serial_exception) as error:
                    raise SerialDisconnected("Deck serial endpoint disconnected") from error
                next_ready = now + 0.5
            try:
                raw = self._connection.readline()
            except (OSError, self._serial_exception) as error:
                raise SerialDisconnected("Deck serial endpoint disconnected") from error
            if not raw:
                continue
            line = raw.decode("utf-8", errors="replace")
            sanitized, event = sanitize_serial_line(line)
            if serial_line_may_contain_secret(line, sanitized) and self._secrets.observe(line):
                sanitized = "[REDACTED SECRET TARGET LINE]"
            envelope = {"captured_at": utc_now(), "line": sanitized}
            self._output.write(json.dumps(envelope, separators=(",", ":")) + "\n")
            self._output.flush()
            if sanitized == "[REDACTED FATAL TARGET LINE]":
                raise AcceptanceFailure(f"{stage}: fatal Deck log observed")
            if sanitized == "[REDACTED SECRET TARGET LINE]":
                raise AcceptanceFailure(f"{stage}: credential appeared in Deck output")
            if event is not None and predicate(event):
                return event
        raise AcceptanceTimeout(f"{stage}: timed out waiting for Deck state")


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
    def __init__(
        self,
        executable: pathlib.Path,
        data_directory: pathlib.Path,
        token: str,
        output: pathlib.Path,
        secrets: SensitiveValueTracker | None = None,
    ) -> None:
        self.executable = executable
        self.data_directory = data_directory
        self.token = token
        self.output = output
        self.process: subprocess.Popen[bytes] | None = None
        self._reader: threading.Thread | None = None
        self._log: Any = None
        self._secrets = secrets or SensitiveValueTracker(token)
        self._secrets.add(token)

    def _drain_output(self, stream: Any) -> None:
        assert self._log is not None
        for raw in iter(stream.readline, b""):
            line = raw.decode("utf-8", errors="replace")
            self._secrets.observe(line)
            envelope = {"captured_at": utc_now(), "line": REDACTED_NON_DIAGNOSTIC}
            self._log.write(json.dumps(envelope, separators=(",", ":")) + "\n")
            self._log.flush()
        stream.close()

    def logs_redacted(self) -> bool:
        return self._secrets.clean()

    def start(self) -> ManagementClient:
        if self.process is not None:
            raise AcceptanceFailure("Companion is already running")
        environment = dict(os.environ)
        environment["S3DECK_MANAGEMENT_TOKEN"] = self.token
        self._log = self.output.open("a", encoding="utf-8")
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
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        assert self.process.stdout is not None
        self._reader = threading.Thread(
            target=self._drain_output,
            args=(self.process.stdout,),
            name="m1-companion-log",
            daemon=True,
        )
        self._reader.start()
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
        failure: AcceptanceFailure | None = None
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=2)
                failure = AcceptanceFailure("Companion exceeded bounded shutdown")
        if process.returncode != 0:
            failure = failure or AcceptanceFailure(
                f"Companion exited with {process.returncode}"
            )
        if self._reader is not None:
            self._reader.join(timeout=2)
            if self._reader.is_alive():
                failure = failure or AcceptanceFailure(
                    "Companion output drain exceeded its bound"
                )
        self._reader = None
        if self._log is not None:
            self._log.close()
            self._log = None
        if failure is not None:
            raise failure

    def ensure_started(self) -> ManagementClient:
        if self.process is None:
            return self.start()
        return ManagementClient("127.0.0.1:7777", self.token)


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


def setup_json(base: str, path: str = "/api/status") -> tuple[int, dict[str, Any]]:
    request = urllib.request.Request(base.rstrip("/") + path, method="GET")
    try:
        with urllib.request.urlopen(request, timeout=4) as response:
            body = response.read()
            return response.status, json.loads(body) if body else {}
    except urllib.error.HTTPError as error:
        error.read()
        return error.code, {}


def snapshot_companion_profiles(setup_base: str) -> dict[str, Any]:
    status, document = setup_json(setup_base)
    companions = document.get("companions")
    if status != 200 or not isinstance(companions, dict):
        raise AcceptanceFailure("Setup did not expose the Companion Profile snapshot")
    profiles = companions.get("profiles")
    active_profile = companions.get("active_profile_id")
    if not isinstance(profiles, list) or not isinstance(active_profile, str):
        raise AcceptanceFailure("Setup Companion Profile snapshot is malformed")
    profile_ids: list[str] = []
    for profile in profiles:
        if not isinstance(profile, dict) or not isinstance(profile.get("profile_id"), str):
            raise AcceptanceFailure("Setup Companion Profile entry is malformed")
        profile_ids.append(profile["profile_id"])
    return {"profile_ids": profile_ids, "active_profile_id": active_profile}


def restore_companion_profiles(
    setup_base: str,
    original: dict[str, Any],
    stage_timeout: float,
) -> None:
    current = snapshot_companion_profiles(setup_base)
    original_ids = set(original["profile_ids"])
    extras = [profile_id for profile_id in current["profile_ids"] if profile_id not in original_ids]
    for profile_id in extras:
        if http_form(setup_base, "/api/companions/revoke", {"profile_id": profile_id}) != 202:
            raise AcceptanceFailure("cannot revoke the temporary M1 Companion Profile")
    original_active = str(original["active_profile_id"])
    if original_active:
        if http_form(
            setup_base, "/api/companions/select", {"profile_id": original_active}
        ) != 202:
            raise AcceptanceFailure("cannot restore the original active Companion Profile")
    deadline = time.monotonic() + stage_timeout
    while time.monotonic() < deadline:
        observed = snapshot_companion_profiles(setup_base)
        if set(observed["profile_ids"]) == original_ids and (
            not original_active or observed["active_profile_id"] == original_active
        ):
            return
        time.sleep(0.25)
    raise AcceptanceFailure("Companion Profile restoration did not commit")


def enter_setup_access(
    serial_evidence: SerialEvidence,
    cleanup: CleanupTransaction,
    stage: str,
) -> dict[str, Any]:
    cleanup.begin_setup()
    serial_evidence.command(b"DECK_SETUP\n")
    serial_evidence.event(
        lambda event: event.get("type") == "setup_state" and event.get("active") is True,
        f"{stage}: enter Setup",
    )
    serial_evidence.command(b"DECK_HIL_SETUP_ACCESS\n")
    access = serial_evidence.event(
        lambda event: event.get("type") == "hil_setup_access",
        f"{stage}: Setup access",
    )
    cleanup.observe_setup_access(access["ssid"])
    return access


def device_hub_json(
    method: str,
    path: str,
    document: dict[str, Any] | None = None,
    headers: dict[str, str] | None = None,
) -> tuple[int, dict[str, Any]]:
    data = None if document is None else json.dumps(document).encode()
    request = urllib.request.Request(
        "https://127.0.0.1:7780" + path,
        data=data,
        method=method,
        headers=headers or {},
    )
    if data is not None:
        request.add_header("Content-Type", "application/json")
    context = ssl.create_default_context()
    context.check_hostname = False
    context.verify_mode = ssl.CERT_NONE
    try:
        with urllib.request.urlopen(request, timeout=3, context=context) as response:
            body = response.read()
            return response.status, json.loads(body) if body else {}
    except urllib.error.HTTPError as error:
        error.read()
        return error.code, {}


def plain_http_status(origin: str, path: str) -> int:
    request = urllib.request.Request(origin.rstrip("/") + path, method="GET")
    try:
        with urllib.request.urlopen(request, timeout=3) as response:
            response.read()
            return response.status
    except urllib.error.HTTPError as error:
        error.read()
        return error.code


def remaining_timeout(
    deadline: float,
    operation_limit: float,
    reserve: float = 0,
) -> float:
    remaining = deadline - time.monotonic() - reserve
    if remaining <= 0:
        raise AcceptanceFailure("Mac Wi-Fi association timed out")
    return min(operation_limit, remaining)


def connect_wifi(
    ssid: str,
    password: str | None = None,
    timeout: float = 60,
) -> None:
    command = ["networksetup", "-setairportnetwork", "en0", ssid]
    if password is not None:
        command.append(password)
    # CoreWLAN can take longer than 20 seconds to leave the normal LAN and
    # associate with a newly-created WPA2 Setup AP, especially immediately
    # after a previous failed association. Keep this bounded but allow the
    # platform to complete its own retry cycle.
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired as error:
        raise AcceptanceFailure("Mac Wi-Fi association timed out") from error
    if result.returncode != 0:
        raise AcceptanceFailure("cannot switch the Mac Wi-Fi network")


def set_wifi_power(enabled: bool, timeout: float = 10) -> None:
    try:
        result = subprocess.run(
            ["networksetup", "-setairportpower", "en0", "on" if enabled else "off"],
            capture_output=True,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired as error:
        raise AcceptanceFailure("Mac Wi-Fi interface reset timed out") from error
    if result.returncode != 0:
        raise AcceptanceFailure("cannot reset the Mac Wi-Fi interface")


def host_is_reachable(host: str, timeout: float) -> bool:
    try:
        return subprocess.run(
            ["ping", "-c", "1", "-W", "1000", host],
            capture_output=True,
            timeout=timeout,
        ).returncode == 0
    except subprocess.TimeoutExpired:
        return False


def connect_wifi_for_host(
    ssid: str,
    password: str | None,
    host: str,
    timeout: float,
    *,
    deadline: float | None = None,
) -> None:
    deadline = deadline if deadline is not None else time.monotonic() + timeout
    # Select the requested SSID before using reachability as proof. A stale
    # route or VPN may make the same private address reachable via another
    # interface and must never satisfy this transaction.
    connect_wifi(ssid, password, remaining_timeout(deadline, 60))
    recovery_attempted = False
    while time.monotonic() < deadline:
        if host_is_reachable(host, remaining_timeout(deadline, 2)):
            return
        if not recovery_attempted:
            # CoreWLAN occasionally reports success while retaining the prior
            # association. Reset the interface once, then retry the same target
            # and prove success by reachability instead of command exit status.
            powered_off = False
            try:
                # Reserve the complete power-on budget before turning the
                # interface off. This keeps the whole recovery inside the
                # caller's deadline without risking a disabled host radio.
                powered_off = True
                set_wifi_power(False, remaining_timeout(deadline, 10, reserve=10))
                time.sleep(min(2, remaining_timeout(deadline, 2, reserve=10)))
            finally:
                if powered_off:
                    set_wifi_power(True, remaining_timeout(deadline, 10))
            time.sleep(min(3, remaining_timeout(deadline, 3)))
            connect_wifi(ssid, password, remaining_timeout(deadline, 60))
            recovery_attempted = True
        time.sleep(min(0.5, remaining_timeout(deadline, 0.5)))
    raise AcceptanceFailure(f"network host {host} did not become reachable")


def restore_original_wifi(ssid: str, timeout: float) -> None:
    # Always repair a half-finished power cycle before selecting and proving
    # the original LAN. This is used by normal, compensation, and final paths.
    deadline = time.monotonic() + timeout
    set_wifi_power(True, remaining_timeout(deadline, 10))
    connect_wifi_for_host(
        ssid,
        None,
        "192.168.31.1",
        timeout,
        deadline=deadline,
    )


def forget_wifi(ssid: str) -> bool:
    if not ssid:
        return True
    result = subprocess.run(
        ["networksetup", "-removepreferredwirelessnetwork", "en0", ssid],
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        timeout=10,
    )
    return result.returncode == 0


def forget_setup_networks(ssids: set[str]) -> tuple[bool, list[str]]:
    failures: list[str] = []
    for ssid in sorted(ssids):
        try:
            if not forget_wifi(ssid):
                failures.append(ssid)
        except Exception:
            failures.append(ssid)
    return not failures, failures


def pair_deck(
    management: ManagementClient,
    serial_evidence: SerialEvidence,
    original_ssid: str,
    hub_address: str,
    stage_timeout: float,
    cleanup: CleanupTransaction,
    secrets: SensitiveValueTracker,
    stage: str,
    before_submit: Callable[[str], None] | None = None,
) -> dict[str, Any]:
    status, issued = management.request("POST", "/api/v1/pairing/codes")
    if status != 200 or re.fullmatch(r"[0-9]{6}", str(issued.get("code", ""))) is None:
        raise AcceptanceFailure(f"{stage}: Companion did not issue a six-digit Pairing code")
    secrets.add(str(issued["code"]))
    access = enter_setup_access(serial_evidence, cleanup, stage)
    secrets.add(access["password"])
    connect_wifi_for_host(
        access["ssid"], access["password"], access["address"], stage_timeout
    )
    setup_base = f"http://{access['address']}"
    if before_submit is not None:
        before_submit(setup_base)
    code = http_form(
        setup_base,
        "/api/companions/pair",
        {"hub_address": hub_address, "code": str(issued["code"])},
    )
    issued = {}
    if code != 202:
        raise AcceptanceFailure(f"{stage}: Deck Pairing request returned {code}")
    serial_evidence.event(
        lambda event: event.get("type") == "setup_state" and event.get("active") is False,
        f"{stage}: Pairing commit and Setup close",
        30,
    )
    cleanup.observe_setup_closed()
    restore_original_wifi(original_ssid, stage_timeout)
    return serial_evidence.event(
        lambda event: event.get("type") == "companion_link_state"
        and event.get("state") == "online",
        f"{stage}: Device Link online",
        60,
    )


def close_setup_by_restart(
    serial_evidence: SerialEvidence,
    serial_module: Any,
    port: str,
    timeout: float,
    cleanup: CleanupTransaction,
) -> None:
    serial_evidence.command(b"DECK_RESTART\n")
    serial_evidence.reopen(serial_module, port, timeout)
    serial_evidence.event(
        lambda event: accept_boot_event(event, "Setup close restart"),
        "Setup close restart",
        30,
    )
    serial_evidence.event(
        lambda event: event.get("type") == "setup_state"
        and event.get("active") is False,
        "Setup inactive after restart",
        timeout,
    )
    cleanup.observe_setup_closed()


def is_new_link_error(
    event: dict[str, Any],
    baseline_generation: int,
    expected_error: str,
) -> bool:
    return (
        event.get("type") == "companion_link_state"
        and event.get("last_error") == expected_error
        and int(event.get("error_generation", 0)) > baseline_generation
        and event.get("state") != "online"
    )


def wait_for_host(host: str, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = subprocess.run(["ping", "-c", "1", "-W", "1000", host], capture_output=True)
        if result.returncode == 0:
            return
        time.sleep(0.5)
    raise AcceptanceFailure(f"network host {host} did not become reachable")


def verified_native_run(url: str, commit: str) -> tuple[bool, bool]:
    matched = re.fullmatch(
        r"https://github\.com/[^/]+/[^/]+/actions/runs/([0-9]+)", url
    )
    if matched is None:
        return False, False
    try:
        document = json.loads(command_output([
            "gh", "run", "view", matched.group(1),
            "--json", "headSha,conclusion,jobs",
        ]))
    except (subprocess.SubprocessError, json.JSONDecodeError):
        return False, False
    if document.get("headSha") != commit or document.get("conclusion") != "success":
        return False, False
    jobs = [job for job in document.get("jobs", []) if isinstance(job, dict)]
    macos = any(
        job.get("name") in {"macOS native (arm64)", "macOS native (amd64)"}
        and job.get("conclusion") == "success"
        for job in jobs
    )
    windows = any(
        job.get("name") == "Windows native (amd64)" and job.get("conclusion") == "success"
        for job in jobs
    )
    return macos, windows


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
    toolchain_environment: dict[str, str] | None = None,
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
        "toolchains": toolchain_identity(toolchain_environment),
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
    parser.add_argument("--result-dir", type=pathlib.Path, required=True)
    parser.add_argument("--original-ssid", required=True)
    parser.add_argument(
        "--hub-address",
        required=True,
        help="normal-LAN Companion Device Hub address stored by the Deck",
    )
    parser.add_argument(
        "--native-run-url",
        required=True,
        help="successful same-commit Actions run containing macOS and Windows native observations",
    )
    parser.add_argument("--stage-timeout", type=float, default=45.0)
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
    link_error_generation = 0
    companion_error_count = 0
    dirty = not source_tree_clean()
    commit = command_output(["git", "rev-parse", "HEAD"])
    observed_firmware_commit = ""
    observed_companion_commit = ""
    token = hashlib.sha256(os.urandom(64)).hexdigest()
    secrets = SensitiveValueTracker(token)
    data_root = tempfile.TemporaryDirectory(prefix="s3deck-m1-companion-")
    data_directory = pathlib.Path(data_root.name)
    companion_log = arguments.result_dir / "companion.jsonl"
    companion: CompanionProcess | None = None
    managed_processes: list[CompanionProcess] = []
    serial_evidence: SerialEvidence | None = None
    cleanup = CleanupTransaction()
    summary: dict[str, Any] = {"status": "failed"}
    summary_written = False
    toolchain_environment: dict[str, str] | None = None
    try:
        if dirty:
            raise AcceptanceFailure("source tree is dirty; commit before auditable acceptance")
        environment = run_preflight(arguments.result_dir / "preflight.log")
        toolchain_environment = environment
        if command_output(["idf.py", "--version"], environment) != "ESP-IDF v6.0.2":
            raise AcceptanceFailure("M1 acceptance requires ESP-IDF v6.0.2")
        executable = companion_for_current_host(commit)
        observed_companion_commit = commit
        serial = load_serial_module(environment)
        run_app_flash(arguments.port, arguments.result_dir / "app-flash.log", environment)
        checks["builds_clean"] = True

        serial_evidence = SerialEvidence(
            serial,
            arguments.port,
            serial_path,
            arguments.stage_timeout,
            secrets,
        )
        serial_evidence.fresh_boot(serial, arguments.port, arguments.stage_timeout)
        serial_evidence.command(b"DECK_BUILD_IDENTITY\n")
        build_identity = serial_evidence.event(
            lambda event: event.get("type") == "deck_build_identity",
            "Deck build identity",
        )
        observed_firmware_commit = str(build_identity["firmware_commit"])
        if observed_firmware_commit != commit:
            raise AcceptanceFailure("Deck firmware does not match the clean source commit")

        companion = CompanionProcess(
            executable, data_directory, token, companion_log, secrets
        )
        managed_processes.append(companion)
        management = companion.start()

        def remember_original_profiles(setup_base: str) -> None:
            cleanup.original_profiles = snapshot_companion_profiles(setup_base)
            cleanup.profiles_may_need_restore = True

        online = pair_deck(
            management,
            serial_evidence,
            arguments.original_ssid,
            arguments.hub_address,
            arguments.stage_timeout,
            cleanup,
            secrets,
            "first Pairing",
            remember_original_profiles,
        )
        checks["recovery_pairing"] = True
        reconnect_count += 1
        deck_error_count = max(deck_error_count, int(online["error_count"]))
        link_error_generation = int(online["error_generation"])

        serial_evidence.command(b"DECK_RESTART\n")
        serial_evidence.reopen(serial, arguments.port, arguments.stage_timeout)
        serial_evidence.event(
            lambda event: accept_boot_event(event, "Deck reboot"),
            "Deck reboot",
            30,
        )
        online = serial_evidence.event(lambda event: event.get("type") == "companion_link_state" and event.get("state") == "online", "reconnect after Deck reboot", 60)
        reconnect_count += 1
        checks["deck_reboot_reconnected"] = True

        companion.stop()
        serial_evidence.event(lambda event: event.get("type") == "companion_link_state" and event.get("state") in {"offline", "connecting"}, "Companion offline", 45)
        management = companion.start()
        online = serial_evidence.event(lambda event: event.get("type") == "companion_link_state" and event.get("state") == "online", "Companion recovery", 60)
        link_error_generation = int(online["error_generation"])
        reconnect_count += 1
        checks["companion_offline_recovered"] = True

        # Wrong-certificate identity on the same address must never reach the real Hub.
        companion.stop()
        with tempfile.TemporaryDirectory(prefix="s3deck-wrong-cert-") as wrong_directory:
            wrong = CompanionProcess(
                executable,
                pathlib.Path(wrong_directory),
                token,
                companion_log,
                secrets,
            )
            managed_processes.append(wrong)
            try:
                wrong.start()
                observed = serial_evidence.event(
                    lambda event: is_new_link_error(
                        event, link_error_generation, "tls_pin_mismatch"
                    ),
                    "wrong certificate rejection",
                    45,
                )
                deck_error_count = int(observed["error_count"])
                link_error_generation = int(observed["error_generation"])
                checks["wrong_certificate_rejected"] = True
            finally:
                wrong.stop()

        # A special acceptance binary uses the persisted identity/trust but sends major 2.
        incompatible = data_directory / "s3deck-companion-major2"
        subprocess.run(
            ["go", "build", "-trimpath", "-ldflags", f"-X main.version=0.2.0-dev -X main.commit={commit} -X main.hilServerProtocolVersion=2", "-o", str(incompatible), "./cmd/s3deck-companion"],
            cwd=REPOSITORY_ROOT / "companion", check=True,
        )
        incompatible_process = CompanionProcess(
            incompatible,
            data_directory,
            token,
            companion_log,
            secrets,
        )
        managed_processes.append(incompatible_process)
        try:
            management = incompatible_process.start()
            observed = serial_evidence.event(
                lambda event: is_new_link_error(
                    event, link_error_generation, "protocol_major_rejected"
                ),
                "protocol major rejection",
                45,
            )
            deck_error_count = int(observed["error_count"])
            link_error_generation = int(observed["error_generation"])
            checks["protocol_major_rejected"] = True
        finally:
            incompatible_process.stop()

        management = companion.start()
        online = serial_evidence.event(lambda event: event.get("type") == "companion_link_state" and event.get("state") == "online", "recovery before revoke", 60)
        link_error_generation = int(online["error_generation"])
        status, devices = management.request("GET", "/api/v1/devices")
        device_list = devices.get("devices", [])
        if status != 200 or len(device_list) != 1 or type(device_list[0].get("device_id")) is not str:
            raise AcceptanceFailure("management did not expose one redacted paired Deck")
        status, _ = management.request("DELETE", "/api/v1/devices/" + device_list[0]["device_id"])
        if status != 204:
            raise AcceptanceFailure("management device revocation failed")
        cleanup.trust_may_need_restore = True
        observed = serial_evidence.event(
            lambda event: is_new_link_error(
                event, link_error_generation, "auth_rejected"
            ),
            "revoked Token rejection",
            45,
        )
        deck_error_count = int(observed["error_count"])
        link_error_generation = int(observed["error_generation"])
        checks["revoked_device_trust_rejected"] = True

        # Restore a valid device trust/Profile before leaving the user's Deck.
        pair_deck(
            management,
            serial_evidence,
            arguments.original_ssid,
            arguments.hub_address,
            arguments.stage_timeout,
            cleanup,
            secrets,
            "trust restoration",
        )
        cleanup.trust_may_need_restore = False
        reconnect_count += 1

        # Cross-port and cross-token rejection is performed against live listeners.
        status, _ = device_hub_json("GET", "/api/v1/status")
        if status != 404:
            raise AcceptanceFailure("Device Hub exposed a management route")
        if plain_http_status(
            "http://127.0.0.1:7777", "/api/v1/device/health"
        ) != 404:
            raise AcceptanceFailure("management listener exposed a Device Hub route")
        status, _ = device_hub_json(
            "GET",
            "/api/v1/device/health",
            headers={
                "Authorization": "Bearer " + token,
                "X-Device-ID": "deck-authority1",
                "X-Device-Identity": "YXV0aG9yaXR5LXNlcGFyYXRpb24taWRlbnRpdHk",
                "X-Protocol-Version": "1",
            },
        )
        if status != 401:
            raise AcceptanceFailure("management credential authorized Device Hub")
        status, temporary_code = management.request("POST", "/api/v1/pairing/codes")
        if status != 200:
            raise AcceptanceFailure("temporary authority code unavailable")
        secrets.add(str(temporary_code.get("code", "")))
        status, temporary_credential = device_hub_json(
            "POST",
            "/api/v1/pairing/redeem",
            {
                "code": temporary_code.get("code"),
                "device_id": "deck-authority1",
                "device_identity": "YXV0aG9yaXR5LXNlcGFyYXRpb24taWRlbnRpdHk",
                "protocol_version": 1,
            },
        )
        if status != 200 or type(temporary_credential.get("token")) is not str:
            raise AcceptanceFailure("temporary device credential unavailable")
        temporary_token = temporary_credential["token"]
        secrets.add(temporary_token)
        unauthorized_management = ManagementClient.__new__(ManagementClient)
        unauthorized_management.address = "127.0.0.1:7777"
        unauthorized_management.origin = "http://127.0.0.1:7777"
        unauthorized_management.cookie_jar = http.cookiejar.CookieJar()
        unauthorized_management.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(unauthorized_management.cookie_jar)
        )
        login_status, _ = unauthorized_management.request(
            "POST", "/api/v1/login", {"token": temporary_token}, authenticated=False
        )
        temporary_token = ""
        temporary_credential = {}
        temporary_code = {}
        if login_status != 401:
            raise AcceptanceFailure("device credential authorized management Web")
        revoke_status, _ = management.request("DELETE", "/api/v1/devices/deck-authority1")
        if revoke_status != 204:
            raise AcceptanceFailure("temporary device trust cleanup failed")
        checks["management_device_authority_separated"] = True

        native_macos, native_windows = verified_native_run(
            arguments.native_run_url, commit
        )
        checks["macos_native_shell_observed"] = native_macos
        checks["windows_native_shell_observed"] = native_windows
        companion_status, runtime_status = management.request("GET", "/api/v1/status")
        if companion_status == 200:
            companion_error_count = int(runtime_status.get("device_link_auth_errors", 0)) + int(runtime_status.get("device_link_protocol_errors", 0))

        # Leave the Deck with exactly the Profile set and active selection it had before M1.
        cleanup_access = enter_setup_access(
            serial_evidence,
            cleanup,
            "restore original Companion Profiles",
        )
        secrets.add(cleanup_access["password"])
        connect_wifi_for_host(
            cleanup_access["ssid"],
            cleanup_access["password"],
            cleanup_access["address"],
            arguments.stage_timeout,
        )
        restore_companion_profiles(
            f"http://{cleanup_access['address']}",
            cleanup.original_profiles,
            arguments.stage_timeout,
        )
        cleanup.profiles_may_need_restore = False
        close_setup_by_restart(
            serial_evidence,
            serial,
            arguments.port,
            arguments.stage_timeout,
            cleanup,
        )
        restore_original_wifi(arguments.original_ssid, arguments.stage_timeout)
    except (
        AcceptanceFailure,
        KeyboardInterrupt,
        OSError,
        subprocess.SubprocessError,
        urllib.error.URLError,
    ) as error:
        print(f"M1 acceptance failed: {error}", file=sys.stderr)
    finally:
        cleanup_ok = True
        for process in reversed(managed_processes[1:]):
            try:
                process.stop()
            except Exception as error:
                cleanup_ok = False
                print(f"M1 cleanup failed: {error}", file=sys.stderr)
        if serial_evidence is not None and companion is not None and cleanup.needs_compensation():
            try:
                management = companion.ensure_started()
                if cleanup.trust_may_need_restore:
                    pair_deck(
                        management,
                        serial_evidence,
                        arguments.original_ssid,
                        arguments.hub_address,
                        arguments.stage_timeout,
                        cleanup,
                        secrets,
                        "cleanup trust compensation",
                    )
                    cleanup.trust_may_need_restore = False
                if cleanup.profiles_may_need_restore:
                    access = enter_setup_access(
                        serial_evidence,
                        cleanup,
                        "cleanup Profile compensation",
                    )
                    secrets.add(access["password"])
                    connect_wifi_for_host(
                        access["ssid"],
                        access["password"],
                        access["address"],
                        arguments.stage_timeout,
                    )
                    restore_companion_profiles(
                        f"http://{access['address']}",
                        cleanup.original_profiles,
                        arguments.stage_timeout,
                    )
                    cleanup.profiles_may_need_restore = False
                if cleanup.setup_pending:
                    close_setup_by_restart(
                        serial_evidence,
                        serial,
                        arguments.port,
                        arguments.stage_timeout,
                        cleanup,
                    )
            except Exception as error:
                cleanup_ok = False
                print(f"M1 compensation failed: {error}", file=sys.stderr)
                try:
                    serial_evidence.command(b"DECK_RESTART\n")
                except Exception:
                    pass
        if companion is not None:
            try:
                companion.stop()
            except Exception as error:
                cleanup_ok = False
                print(f"M1 cleanup failed: {error}", file=sys.stderr)
        try:
            restore_original_wifi(arguments.original_ssid, arguments.stage_timeout)
        except Exception as error:
            cleanup_ok = False
            print(f"M1 Wi-Fi restore failed: {error}", file=sys.stderr)
        setup_networks_removed, failed_ssids = forget_setup_networks(cleanup.setup_ssids)
        if not setup_networks_removed:
            cleanup_ok = False
            print(
                f"M1 Setup Wi-Fi cleanup failed for {len(failed_ssids)} network(s)",
                file=sys.stderr,
            )
        if serial_evidence is not None:
            try:
                serial_evidence.close()
            except Exception as error:
                cleanup_ok = False
                print(f"M1 serial cleanup failed: {error}", file=sys.stderr)
        checks["cleanup_restored"] = cleanup_ok and cleanup.restored()
        try:
            checks["credentials_absent_from_evidence"] = (
                all(process.logs_redacted() for process in managed_processes)
                and secrets.clean()
                and evidence_directory_is_redacted(arguments.result_dir, secrets.values())
            )
        except Exception as error:
            checks["credentials_absent_from_evidence"] = False
            print(f"M1 evidence scan failed: {error}", file=sys.stderr)
        try:
            data_root.cleanup()
        except Exception as error:
            checks["cleanup_restored"] = False
            print(f"M1 temporary-data cleanup failed: {error}", file=sys.stderr)
        try:
            summary = build_summary(
                firmware_commit=observed_firmware_commit,
                companion_commit=observed_companion_commit,
                started_at=started_at,
                ended_at=utc_now(),
                checks=checks,
                reconnect_count=reconnect_count,
                deck_error_count=deck_error_count,
                companion_error_count=companion_error_count,
                serial_log=serial_path,
                source_dirty=dirty,
                toolchain_environment=toolchain_environment,
            )
        except Exception as error:
            print(f"M1 summary assembly failed: {error}", file=sys.stderr)
            summary = {
                "schema_version": 1,
                "status": "failed",
                "failures": ["summary_assembly_failed"],
                "started_at": started_at,
                "ended_at": utc_now(),
            }
        try:
            summary_path.write_text(
                json.dumps(summary, indent=2, sort_keys=True) + "\n",
                encoding="utf-8",
            )
            summary_written = True
        except OSError as error:
            print(f"M1 summary write failed: {error}", file=sys.stderr)
    print(f"M1 acceptance {summary['status']}: {summary_path}")
    return 0 if summary_written and summary["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
