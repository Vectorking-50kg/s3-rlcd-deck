#!/usr/bin/env python3
"""Run one M1 recovery transaction from a dual-homed Linux helper."""

from __future__ import annotations

import argparse
import errno
import fcntl
import hashlib
import ipaddress
import json
import math
import os
import pathlib
import re
import selectors
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any


PROTOCOL_VERSION = 1
SETUP_NETWORK = ipaddress.ip_network("192.168.4.0/24")
RECOVERY_TITLE = b"<title>Deck Setup / Recovery</title>"
HELPER_CLEANUP_RESERVE_SECONDS = 30.0
UUID_PATTERN = re.compile(
    r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-"
    r"[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
)


class HelperFailure(RuntimeError):
    pass


class OperationBudget:
    def __init__(self, timeout: float) -> None:
        if not math.isfinite(timeout) or not 35 <= timeout <= 180:
            raise HelperFailure("invalid_request")
        self.deadline = time.monotonic() + timeout

    def work(self, limit: float) -> float:
        remaining = self.deadline - time.monotonic() - HELPER_CLEANUP_RESERVE_SECONDS
        if remaining <= 0:
            raise HelperFailure("transaction_timeout")
        return min(limit, remaining)

    def cleanup(self, limit: float) -> float:
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise HelperFailure("network_cleanup_failed")
        return min(limit, remaining)


class TransactionLock:
    def __init__(self, identifier: str, timeout: float) -> None:
        self.identifier = transaction_id(identifier)
        self.deadline = time.monotonic() + timeout
        self.descriptor: int | None = None

    def __enter__(self) -> "TransactionLock":
        path = state_directory() / f"{self.identifier}.lock"
        try:
            self.descriptor = os.open(path, os.O_CREAT | os.O_RDWR, 0o600)
            os.fchmod(self.descriptor, 0o600)
            while True:
                try:
                    fcntl.flock(
                        self.descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB
                    )
                    return self
                except BlockingIOError:
                    if time.monotonic() >= self.deadline:
                        raise HelperFailure("transaction_lock_timeout")
                    time.sleep(0.02)
        except BaseException:
            if self.descriptor is not None:
                os.close(self.descriptor)
                self.descriptor = None
            raise

    def __exit__(self, *_args: Any) -> None:
        if self.descriptor is not None:
            fcntl.flock(self.descriptor, fcntl.LOCK_UN)
            os.close(self.descriptor)
            self.descriptor = None

def transaction_id(value: Any) -> str:
    if not isinstance(value, str) or re.fullmatch(r"[0-9a-f]{32}", value) is None:
        raise HelperFailure("invalid_request")
    return value


def state_directory() -> pathlib.Path:
    directory = pathlib.Path(tempfile.gettempdir()) / f"s3deck-m1-{os.getuid()}"
    try:
        directory.mkdir(mode=0o700, exist_ok=True)
        metadata = directory.stat()
    except OSError as error:
        raise HelperFailure("network_state_invalid") from error
    if directory.is_symlink() or metadata.st_uid != os.getuid():
        raise HelperFailure("network_state_invalid")
    os.chmod(directory, 0o700)
    return directory


def state_path(identifier: str) -> pathlib.Path:
    return state_directory() / f"{transaction_id(identifier)}.json"


def cancellation_path(identifier: str) -> pathlib.Path:
    return state_directory() / f"{transaction_id(identifier)}.cancelled"


def cancel_transaction(identifier: str) -> None:
    path = cancellation_path(identifier)
    descriptor: int | None = None
    try:
        descriptor = os.open(path, os.O_CREAT | os.O_WRONLY, 0o600)
        os.fchmod(descriptor, 0o600)
        os.fsync(descriptor)
    except OSError as error:
        raise HelperFailure("network_state_invalid") from error
    finally:
        if descriptor is not None:
            os.close(descriptor)


def require_active_transaction(identifier: str) -> None:
    if cancellation_path(identifier).exists():
        raise HelperFailure("transaction_cancelled")


def write_state(identifier: str, document: dict[str, str]) -> None:
    destination = state_path(identifier)
    descriptor, temporary_name = tempfile.mkstemp(
        prefix=destination.name + ".", dir=destination.parent
    )
    temporary = pathlib.Path(temporary_name)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as output:
            json.dump(document, output, separators=(",", ":"))
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, destination)
    except BaseException:
        try:
            os.close(descriptor)
        except OSError:
            pass
        temporary.unlink(missing_ok=True)
        raise


def read_state(identifier: str) -> dict[str, str] | None:
    path = state_path(identifier)
    try:
        if not path.exists():
            return None
        metadata = path.stat()
        if path.is_symlink() or metadata.st_uid != os.getuid() or metadata.st_mode & 0o077:
            raise HelperFailure("network_state_invalid")
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise HelperFailure("network_state_invalid") from error
    if not isinstance(document, dict) or set(document) != {
        "connection_uuid",
        "original_uuid",
    } or any(not isinstance(value, str) for value in document.values()):
        raise HelperFailure("network_state_invalid")
    return document


def source_sha256() -> str:
    return hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest()


def run_checked(arguments: list[str], timeout: float) -> str:
    try:
        result = subprocess.run(
            arguments,
            check=False,
            capture_output=True,
            text=True,
            timeout=timeout,
            env={**os.environ, "LC_ALL": "C", "LANG": "C"},
        )
    except (OSError, subprocess.SubprocessError) as error:
        raise HelperFailure("command_failed") from error
    if result.returncode != 0:
        raise HelperFailure("command_failed")
    return result.stdout.strip()


def run_checked_with_fifo(
    arguments: list[str],
    fifo: pathlib.Path,
    payload: bytes,
    timeout: float,
) -> str:
    deadline = time.monotonic() + timeout
    try:
        process = subprocess.Popen(
            arguments,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            env={**os.environ, "LC_ALL": "C", "LANG": "C"},
        )
    except OSError as error:
        raise HelperFailure("command_failed") from error
    descriptor: int | None = None
    try:
        while descriptor is None:
            if process.poll() is not None or time.monotonic() >= deadline:
                raise HelperFailure("command_failed")
            try:
                descriptor = os.open(fifo, os.O_WRONLY | os.O_NONBLOCK)
            except OSError as error:
                if error.errno not in {errno.ENXIO, errno.ENOENT}:
                    raise HelperFailure("command_failed") from error
                time.sleep(0.01)
        remaining_payload = memoryview(payload)
        while remaining_payload:
            if time.monotonic() >= deadline:
                raise HelperFailure("command_failed")
            try:
                written = os.write(descriptor, remaining_payload)
                remaining_payload = remaining_payload[written:]
            except BlockingIOError:
                time.sleep(0.01)
        # Closing the only writer is part of the passwd-file protocol: nmcli
        # consumes the line and then observes EOF.
        os.close(descriptor)
        descriptor = None
        stdout, _stderr = process.communicate(
            timeout=max(0.01, deadline - time.monotonic())
        )
    except BaseException as error:
        if descriptor is not None:
            os.close(descriptor)
        process.kill()
        process.communicate()
        if isinstance(error, HelperFailure):
            raise
        raise HelperFailure("command_failed") from error
    if process.returncode != 0:
        raise HelperFailure("command_failed")
    return stdout.strip()


def parse_hub_address(value: Any) -> tuple[str, int]:
    if not isinstance(value, str) or value.count(":") != 1:
        raise HelperFailure("invalid_request")
    host, port_text = value.rsplit(":", 1)
    try:
        address = ipaddress.ip_address(host)
        port = int(port_text)
    except ValueError as error:
        raise HelperFailure("invalid_request") from error
    if (
        address.version != 4
        or address in SETUP_NETWORK
        or address.is_loopback
        or address.is_link_local
        or address.is_multicast
        or address.is_unspecified
        or not 1 <= port <= 65535
    ):
        raise HelperFailure("invalid_request")
    return host, port


def active_connection_uuid(wifi_interface: str, timeout: float = 5) -> str:
    observed = run_checked(
        [
            "nmcli",
            "-g",
            "GENERAL.CON-UUID",
            "device",
            "show",
            wifi_interface,
        ],
        timeout,
    )
    if observed == "--":
        return ""
    if UUID_PATTERN.fullmatch(observed) is None:
        raise HelperFailure("network_state_invalid")
    return observed.lower()


def saved_connections(timeout: float = 5) -> dict[str, str]:
    observed = run_checked(
        ["nmcli", "-t", "--escape", "no", "-f", "NAME,UUID", "connection", "show"],
        timeout,
    )
    connections: dict[str, str] = {}
    for line in observed.splitlines():
        candidate = line.rstrip()
        if not candidate:
            continue
        match = re.fullmatch(r"(.*):(" + UUID_PATTERN.pattern + r")", candidate)
        if match is None or not match.group(1):
            raise HelperFailure("network_state_invalid")
        connections[match.group(1)] = match.group(2).lower()
    return connections


def validate_access(value: Any) -> dict[str, str]:
    if not isinstance(value, dict) or set(value) != {
        "ssid",
        "password",
        "address",
    }:
        raise HelperFailure("invalid_request")
    if (
        not isinstance(value["ssid"], str)
        or not 1 <= len(value["ssid"].encode("utf-8")) <= 32
        or not isinstance(value["password"], str)
        or not 8 <= len(value["password"]) <= 63
        or re.search(r"[\x00-\x1f\x7f]", value["ssid"] + value["password"])
        is not None
        or value["address"] != "192.168.4.1"
    ):
        raise HelperFailure("invalid_request")
    return value


def validate_profiles(value: Any) -> dict[str, Any]:
    if not isinstance(value, dict) or set(value) != {
        "profile_ids",
        "active_profile_id",
    }:
        raise HelperFailure("invalid_request")
    profile_ids = value.get("profile_ids")
    active = value.get("active_profile_id")
    if (
        not isinstance(profile_ids, list)
        or len(profile_ids) > 5
        or any(
            not isinstance(profile_id, str)
            or re.fullmatch(r"sha256:[0-9a-f]{64}", profile_id) is None
            for profile_id in profile_ids
        )
        or len(set(profile_ids)) != len(profile_ids)
        or not isinstance(active, str)
        or (active and active not in profile_ids)
    ):
        raise HelperFailure("invalid_request")
    return value


def direct_open(request: urllib.request.Request, timeout: float) -> Any:
    return urllib.request.build_opener(urllib.request.ProxyHandler({})).open(
        request, timeout=timeout
    )


def get_document(base: str, path: str, timeout: float = 4) -> tuple[int, bytes]:
    request = urllib.request.Request(base.rstrip("/") + path, method="GET")
    try:
        with direct_open(request, timeout) as response:
            return response.status, response.read(65537)
    except urllib.error.HTTPError as error:
        return error.code, error.read(65537)


def post_form(
    base: str, path: str, fields: dict[str, str], timeout: float = 4
) -> tuple[int, bytes]:
    request = urllib.request.Request(
        base.rstrip("/") + path,
        data=urllib.parse.urlencode(fields).encode("ascii"),
        method="POST",
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    try:
        with direct_open(request, timeout) as response:
            return response.status, response.read(8193)
    except urllib.error.HTTPError as error:
        return error.code, error.read(8193)


def companion_profiles(base: str, timeout: float = 4) -> dict[str, Any]:
    status, body = get_document(base, "/api/status", timeout)
    try:
        document = json.loads(body)
    except (UnicodeError, json.JSONDecodeError) as error:
        raise HelperFailure("setup_status_invalid") from error
    companions = document.get("companions") if isinstance(document, dict) else None
    if status != 200 or not isinstance(companions, dict):
        raise HelperFailure("setup_status_invalid")
    profiles = companions.get("profiles")
    active = companions.get("active_profile_id")
    if not isinstance(profiles, list) or not isinstance(active, str):
        raise HelperFailure("setup_status_invalid")
    snapshot = {
        "profile_ids": [
            profile.get("profile_id") if isinstance(profile, dict) else None
            for profile in profiles
        ],
        "active_profile_id": active,
    }
    return validate_profiles(snapshot)


class TcpRelay:
    def __init__(self, listen_host: str, target_host: str, port: int) -> None:
        self._listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self._listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self._listener.bind((listen_host, port))
        self._listener.listen(2)
        self._listener.settimeout(0.25)
        self._target = (target_host, port)
        self._stop = threading.Event()
        self.failure = False
        self.connections = 0
        self._active: set[socket.socket] = set()
        self._active_lock = threading.Lock()
        self._thread = threading.Thread(target=self._serve, daemon=True)

    def __enter__(self) -> "TcpRelay":
        self._thread.start()
        return self

    def __exit__(self, *_args: Any) -> None:
        self._stop.set()
        self._listener.close()
        with self._active_lock:
            active = tuple(self._active)
        for connection in active:
            try:
                connection.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            connection.close()
        self._thread.join(timeout=2)
        if self._thread.is_alive():
            raise HelperFailure("relay_cleanup_failed")

    def _serve(self) -> None:
        while not self._stop.is_set():
            try:
                source, _ = self._listener.accept()
            except socket.timeout:
                continue
            except OSError:
                return
            self._track(source)
            target: socket.socket | None = None
            try:
                target = socket.create_connection(self._target, timeout=5)
                target.settimeout(1)
                source.settimeout(1)
                self._track(target)
                self.connections += 1
                self._pump(source, target)
            except OSError:
                self.failure = True
            finally:
                self._close(source)
                if target is not None:
                    self._close(target)

    def _track(self, connection: socket.socket) -> None:
        with self._active_lock:
            self._active.add(connection)

    def _close(self, connection: socket.socket) -> None:
        with self._active_lock:
            self._active.discard(connection)
        connection.close()

    def _pump(self, left: socket.socket, right: socket.socket) -> None:
        selector = selectors.DefaultSelector()
        selector.register(left, selectors.EVENT_READ, right)
        selector.register(right, selectors.EVENT_READ, left)
        try:
            while selector.get_map() and not self._stop.is_set():
                for key, _ in selector.select(0.25):
                    peer = key.data
                    try:
                        data = key.fileobj.recv(16384)
                        if data:
                            peer.sendall(data)
                            continue
                        selector.unregister(key.fileobj)
                        peer.shutdown(socket.SHUT_WR)
                    except OSError:
                        return
        finally:
            selector.close()


class NetworkManagerSession:
    def __init__(
        self,
        wifi_interface: str,
        control_interface: str,
        access: dict[str, str],
        hub_host: str,
        identifier: str | None = None,
        budget: OperationBudget | None = None,
    ) -> None:
        self.wifi_interface = wifi_interface
        self.control_interface = control_interface
        self.access = access
        self.hub_host = hub_host
        self.identifier = transaction_id(identifier or os.urandom(16).hex())
        self.connection_name = "s3deck-m1-" + self.identifier[:12]
        self.budget = budget or OperationBudget(75)
        self.connection_uuid = ""
        self.original_uuid = ""
        self.local_address = ""

    def __enter__(self) -> "NetworkManagerSession":
        try:
            return self._open()
        except BaseException:
            try:
                self._cleanup()
            except HelperFailure as cleanup_error:
                raise HelperFailure("network_cleanup_failed") from cleanup_error
            raise

    def _open(self) -> "NetworkManagerSession":
        self.original_uuid = active_connection_uuid(
            self.wifi_interface, self.budget.work(5)
        )
        self._write_state()
        run_checked(
            [
                "nmcli",
                "--wait",
                "10",
                "connection",
                "add",
                "save",
                "no",
                "type",
                "wifi",
                "ifname",
                self.wifi_interface,
                "con-name",
                self.connection_name,
                "ssid",
                self.access["ssid"],
                "connection.autoconnect",
                "no",
                "802-11-wireless-security.key-mgmt",
                "wpa-psk",
                "ipv4.method",
                "auto",
                "ipv6.method",
                "disabled",
            ],
            self.budget.work(12),
        )
        self.connection_uuid = run_checked(
            [
                "nmcli",
                "-g",
                "connection.uuid",
                "connection",
                "show",
                "id",
                self.connection_name,
            ],
            self.budget.work(5),
        ).lower()
        if UUID_PATTERN.fullmatch(self.connection_uuid) is None:
            raise HelperFailure("network_state_invalid")
        self._write_state()
        with tempfile.TemporaryDirectory(prefix="s3deck-m1-secret-") as directory:
            fifo = pathlib.Path(directory) / "wifi.psk"
            os.mkfifo(fifo, 0o600)
            run_checked_with_fifo(
                [
                    "nmcli",
                    "--wait",
                    "25",
                    "connection",
                    "up",
                    "uuid",
                    self.connection_uuid,
                    "ifname",
                    self.wifi_interface,
                    "passwd-file",
                    str(fifo),
                ],
                fifo,
                (
                    "802-11-wireless-security.psk:"
                    + self.access["password"]
                    + "\n"
                ).encode("utf-8"),
                self.budget.work(28),
            )
        addresses = run_checked(
            [
                "nmcli",
                "-g",
                "IP4.ADDRESS",
                "device",
                "show",
                self.wifi_interface,
            ],
            self.budget.work(5),
        ).splitlines()
        for candidate in addresses:
            try:
                address = ipaddress.ip_interface(candidate).ip
            except ValueError:
                continue
            if address in SETUP_NETWORK and address != SETUP_NETWORK.network_address + 1:
                self.local_address = str(address)
                break
        if not self.local_address:
            raise HelperFailure("setup_route_invalid")
        setup_route = run_checked(
            ["ip", "-4", "route", "get", self.access["address"]],
            self.budget.work(5),
        )
        hub_route = run_checked(
            ["ip", "-4", "route", "get", self.hub_host], self.budget.work(5)
        )
        if (
            re.search(rf"\bdev\s+{re.escape(self.wifi_interface)}\b", setup_route)
            is None
            or re.search(rf"\bsrc\s+{re.escape(self.local_address)}\b", setup_route)
            is None
            or re.search(rf"\bdev\s+{re.escape(self.control_interface)}\b", hub_route)
            is None
        ):
            raise HelperFailure("setup_route_invalid")
        return self

    def __exit__(self, *_args: Any) -> None:
        self._cleanup()

    def _write_state(self) -> None:
        write_state(
            self.identifier,
            {
                "connection_uuid": self.connection_uuid,
                "original_uuid": self.original_uuid,
            },
        )

    def _cleanup(self) -> None:
        cleanup_failed = False
        if not self.connection_uuid:
            try:
                self.connection_uuid = saved_connections(
                    self.budget.cleanup(5)
                ).get(self.connection_name, "")
            except HelperFailure:
                cleanup_failed = True
        if self.connection_uuid:
            try:
                active = active_connection_uuid(
                    self.wifi_interface, self.budget.cleanup(5)
                )
            except HelperFailure:
                active = None
                cleanup_failed = True
            if active == self.connection_uuid:
                try:
                    run_checked(
                        [
                            "nmcli",
                            "--wait",
                            "10",
                            "connection",
                            "down",
                            "uuid",
                            self.connection_uuid,
                        ],
                        self.budget.cleanup(12),
                    )
                except HelperFailure:
                    cleanup_failed = True
            try:
                run_checked(
                    ["nmcli", "connection", "delete", "uuid", self.connection_uuid],
                    self.budget.cleanup(8),
                )
            except HelperFailure:
                # A runtime-only `save no` profile can disappear when it is
                # brought down. The independent list below proves deletion.
                pass
            try:
                remaining_connections = saved_connections(self.budget.cleanup(5))
                if (
                    self.connection_name in remaining_connections
                    or self.connection_uuid in remaining_connections.values()
                ):
                    cleanup_failed = True
            except HelperFailure:
                cleanup_failed = True
        else:
            try:
                if self.connection_name in saved_connections(self.budget.cleanup(5)):
                    cleanup_failed = True
            except HelperFailure:
                cleanup_failed = True

        try:
            active = active_connection_uuid(
                self.wifi_interface, self.budget.cleanup(5)
            )
        except HelperFailure:
            active = None
            cleanup_failed = True
        if self.original_uuid and active != self.original_uuid:
            try:
                run_checked(
                    [
                        "nmcli",
                        "--wait",
                        "20",
                        "connection",
                        "up",
                        "uuid",
                        self.original_uuid,
                        "ifname",
                        self.wifi_interface,
                    ],
                    self.budget.cleanup(22),
                )
            except HelperFailure:
                cleanup_failed = True
        try:
            if active_connection_uuid(
                self.wifi_interface, self.budget.cleanup(5)
            ) != self.original_uuid:
                cleanup_failed = True
        except HelperFailure:
            cleanup_failed = True
        if cleanup_failed:
            raise HelperFailure("network_cleanup_failed")


def verify_control_path(wifi_interface: str, control_interface: str) -> None:
    if (
        re.fullmatch(r"[A-Za-z0-9_.:-]{1,32}", wifi_interface) is None
        or re.fullmatch(r"[A-Za-z0-9_.:-]{1,32}", control_interface) is None
        or wifi_interface == control_interface
        or shutil.which("nmcli") is None
        or shutil.which("ip") is None
    ):
        raise HelperFailure("control_path_invalid")
    connection = os.environ.get("SSH_CONNECTION", "").split()
    if len(connection) != 4:
        raise HelperFailure("control_path_invalid")
    try:
        ipaddress.ip_address(connection[0])
        local = ipaddress.ip_address(connection[2])
    except ValueError as error:
        raise HelperFailure("control_path_invalid") from error
    if local.version != 4:
        raise HelperFailure("control_path_invalid")
    addresses = run_checked(
        ["ip", "-o", "-4", "address", "show", "dev", control_interface], 5
    )
    if re.search(rf"\binet\s+{re.escape(str(local))}/", addresses) is None:
        raise HelperFailure("control_path_invalid")
    wifi_type = run_checked(
        ["nmcli", "-g", "GENERAL.TYPE", "device", "show", wifi_interface], 5
    )
    control_type = run_checked(
        ["nmcli", "-g", "GENERAL.TYPE", "device", "show", control_interface], 5
    )
    if (
        wifi_type not in {"wifi", "802-11-wireless"}
        or control_type not in {"ethernet", "802-3-ethernet"}
    ):
        raise HelperFailure("control_path_invalid")


def request_timeout(request: dict[str, Any]) -> float:
    value = request.get("transaction_timeout_seconds")
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise HelperFailure("invalid_request")
    timeout = float(value)
    if not math.isfinite(timeout) or not 35 <= timeout <= 180:
        raise HelperFailure("invalid_request")
    return timeout


def request_budget(request: dict[str, Any]) -> OperationBudget:
    return OperationBudget(request_timeout(request))


def pair_transaction(
    wifi_interface: str,
    control_interface: str,
    request: dict[str, Any],
) -> dict[str, Any]:
    access = validate_access(request.get("access"))
    identifier = transaction_id(request.get("transaction_id"))
    budget = request_budget(request)
    hub_host, hub_port = parse_hub_address(request.get("hub_address"))
    pairing_code = request.get("pairing_code")
    if not isinstance(pairing_code, str) or re.fullmatch(r"[0-9]{6}", pairing_code) is None:
        raise HelperFailure("invalid_request")
    base = "http://" + access["address"]
    with NetworkManagerSession(
        wifi_interface, control_interface, access, hub_host, identifier, budget
    ) as network, TcpRelay(network.local_address, hub_host, hub_port) as relay:
        page_status, page = get_document(base, "/", budget.work(4))
        if page_status != 200 or RECOVERY_TITLE not in page:
            raise HelperFailure("recovery_page_invalid")
        status, body = post_form(
            base,
            "/api/companions/pair",
            {"hub_address": request["hub_address"], "code": pairing_code},
            budget.work(4),
        )
        try:
            response = json.loads(body)
        except (UnicodeError, json.JSONDecodeError) as error:
            raise HelperFailure("pair_response_invalid") from error
        acknowledgement = response.get("response_ack") if isinstance(response, dict) else None
        if (
            status != 202
            or not isinstance(acknowledgement, str)
            or re.fullmatch(r"[0-9a-f]{32}", acknowledgement) is None
        ):
            raise HelperFailure("pair_response_invalid")
        try:
            ack_status, _ = post_form(
                base,
                "/api/companions/pair/ack",
                {"response_ack": acknowledgement},
                budget.work(4),
            )
            if ack_status != 202:
                raise HelperFailure("pair_ack_failed")
        except OSError:
            pass
        deadline = min(
            time.monotonic() + 25,
            budget.deadline - HELPER_CLEANUP_RESERVE_SECONDS,
        )
        while time.monotonic() < deadline:
            if relay.failure:
                raise HelperFailure("relay_failed")
            try:
                get_document(base, "/api/status", budget.work(4))
            except OSError:
                if relay.connections == 1:
                    return {
                        "recovery_page": True,
                        "response_acknowledged": True,
                    }
                raise HelperFailure("relay_not_observed")
            time.sleep(0.25)
        raise HelperFailure("setup_close_timeout")


def snapshot_transaction(
    wifi_interface: str,
    control_interface: str,
    request: dict[str, Any],
) -> dict[str, Any]:
    access = validate_access(request.get("access"))
    identifier = transaction_id(request.get("transaction_id"))
    budget = request_budget(request)
    base = "http://" + access["address"]
    ssh_peer = os.environ["SSH_CONNECTION"].split()[0]
    with NetworkManagerSession(
        wifi_interface, control_interface, access, ssh_peer, identifier, budget
    ):
        page_status, page = get_document(base, "/", budget.work(4))
        if page_status != 200 or RECOVERY_TITLE not in page:
            raise HelperFailure("recovery_page_invalid")
        return {
            "recovery_page": True,
            "profiles": companion_profiles(base, budget.work(4)),
        }


def restore_transaction(
    wifi_interface: str,
    control_interface: str,
    request: dict[str, Any],
) -> dict[str, Any]:
    access = validate_access(request.get("access"))
    identifier = transaction_id(request.get("transaction_id"))
    budget = request_budget(request)
    original = validate_profiles(request.get("original_profiles"))
    base = "http://" + access["address"]
    # The restore transaction does not use the Hub, but route proof still needs
    # the wired peer that carries this helper's SSH control session.
    ssh_peer = os.environ["SSH_CONNECTION"].split()[0]
    with NetworkManagerSession(
        wifi_interface, control_interface, access, ssh_peer, identifier, budget
    ):
        current = companion_profiles(base, budget.work(4))
        original_ids = set(original["profile_ids"])
        for profile_id in current["profile_ids"]:
            if profile_id not in original_ids:
                status, _ = post_form(
                    base,
                    "/api/companions/revoke",
                    {"profile_id": profile_id},
                    budget.work(4),
                )
                if status != 202:
                    raise HelperFailure("profile_restore_failed")
        if original["active_profile_id"]:
            status, _ = post_form(
                base,
                "/api/companions/select",
                {"profile_id": original["active_profile_id"]},
                budget.work(4),
            )
            if status != 202:
                raise HelperFailure("profile_restore_failed")
        deadline = min(
            time.monotonic() + 15,
            budget.deadline - HELPER_CLEANUP_RESERVE_SECONDS,
        )
        while time.monotonic() < deadline:
            if companion_profiles(base, budget.work(4)) == original:
                return {"profiles_restored": True}
            time.sleep(0.25)
        raise HelperFailure("profile_restore_failed")


def cleanup_transaction(
    wifi_interface: str,
    request: dict[str, Any],
) -> dict[str, Any]:
    identifier = transaction_id(request.get("transaction_id"))
    budget = request_budget(request)
    expected_name = "s3deck-m1-" + identifier[:12]
    journal = read_state(identifier)
    if journal is not None:
        if (
            (
                journal["connection_uuid"]
                and UUID_PATTERN.fullmatch(journal["connection_uuid"]) is None
            )
            or (
                journal["original_uuid"]
                and UUID_PATTERN.fullmatch(journal["original_uuid"]) is None
            )
        ):
            raise HelperFailure("network_state_invalid")
        session = NetworkManagerSession(
            wifi_interface,
            "unused",
            {"ssid": "unused", "password": "unused", "address": "192.168.4.1"},
            "127.0.0.1",
            identifier,
            budget,
        )
        session.connection_name = expected_name
        session.connection_uuid = journal["connection_uuid"].lower()
        session.original_uuid = journal["original_uuid"].lower()
        if not session.connection_uuid:
            session.connection_uuid = saved_connections(
                budget.cleanup(5)
            ).get(expected_name, "")
        session._cleanup()
    remaining_connections = saved_connections(budget.cleanup(5))
    if expected_name in remaining_connections or (
        journal is not None
        and journal["connection_uuid"]
        and journal["connection_uuid"].lower() in remaining_connections.values()
    ):
        raise HelperFailure("network_cleanup_failed")
    if journal is not None and active_connection_uuid(
        wifi_interface, budget.cleanup(5)
    ) != journal["original_uuid"].lower():
        raise HelperFailure("network_cleanup_failed")
    state_path(identifier).unlink(missing_ok=True)
    if read_state(identifier) is not None:
        raise HelperFailure("network_cleanup_failed")
    return {"network_restored": True}


def read_request() -> dict[str, Any]:
    raw = sys.stdin.buffer.readline(16385)
    if not raw or len(raw) > 16384 or sys.stdin.buffer.read(1):
        raise HelperFailure("invalid_request")
    try:
        document = json.loads(raw)
    except (UnicodeError, json.JSONDecodeError) as error:
        raise HelperFailure("invalid_request") from error
    if (
        not isinstance(document, dict)
        or document.get("protocol_version") != PROTOCOL_VERSION
        or document.get("action")
        not in {"probe", "snapshot", "pair", "restore", "cleanup"}
    ):
        raise HelperFailure("invalid_request")
    return document


def emit(action: str, ok: bool, **fields: Any) -> None:
    response = {
        "protocol_version": PROTOCOL_VERSION,
        "action": action,
        "ok": ok,
        **fields,
    }
    sys.stdout.write(json.dumps(response, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--wifi-interface", required=True)
    parser.add_argument("--control-interface", required=True)
    return parser.parse_args()


def main() -> int:
    action = "invalid"
    try:
        arguments = parse_arguments()
        def interrupt_transaction(_signum: int, _frame: Any) -> None:
            raise HelperFailure("transaction_interrupted")

        signal.signal(signal.SIGHUP, interrupt_transaction)
        signal.signal(signal.SIGTERM, interrupt_transaction)
        request = read_request()
        action = request["action"]
        expected = request.get("expected_helper_sha256")
        observed = source_sha256()
        if (
            not isinstance(expected, str)
            or re.fullmatch(r"[0-9a-f]{64}", expected) is None
            or expected != observed
        ):
            raise HelperFailure("helper_identity_mismatch")
        verify_control_path(arguments.wifi_interface, arguments.control_interface)
        identifier = transaction_id(request.get("transaction_id"))
        with TransactionLock(identifier, request_timeout(request)):
            if action == "cleanup":
                # The marker is written under the same lock as all primary
                # mutations. A primary process that had not reached the lock
                # before its SSH channel died can never mutate afterward.
                cancel_transaction(identifier)
            else:
                require_active_transaction(identifier)
            if action == "probe":
                emit(
                    action,
                    True,
                    helper_sha256=observed,
                    control_path="wired",
                )
                return 0
            if action == "pair":
                emit(action, True, **pair_transaction(
                    arguments.wifi_interface, arguments.control_interface, request
                ))
                return 0
            if action == "cleanup":
                emit(action, True, **cleanup_transaction(
                    arguments.wifi_interface, request
                ))
                return 0
            if action == "snapshot":
                emit(action, True, **snapshot_transaction(
                    arguments.wifi_interface, arguments.control_interface, request
                ))
                return 0
            emit(action, True, **restore_transaction(
                arguments.wifi_interface, arguments.control_interface, request
            ))
            return 0
    except (HelperFailure, KeyboardInterrupt, OSError, urllib.error.URLError):
        emit(action, False, error_code="transaction_failed")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
