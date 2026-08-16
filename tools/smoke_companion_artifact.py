#!/usr/bin/env python3
"""Run a native Companion artifact through its version and desktop startup contract."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


CREDENTIAL_CLEANUP_TIMEOUT_SECONDS = 30
PROCESS_SHUTDOWN_TIMEOUT_SECONDS = 15


def reserve_loopback_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def read_bootstrap(address: str, process: subprocess.Popen[bytes]) -> dict[str, object]:
    deadline = time.monotonic() + 10.0
    last_error = "management endpoint did not respond"
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"desktop process exited early with {process.returncode}")
        try:
            with urllib.request.urlopen(
                f"http://{address}/api/v1/bootstrap", timeout=0.5
            ) as response:
                return json.loads(response.read())
        except (OSError, urllib.error.URLError, json.JSONDecodeError) as error:
            last_error = str(error)
            time.sleep(0.1)
    raise RuntimeError(last_error)


def stop_process(process: subprocess.Popen[bytes]) -> None:
    if os.name == "nt":
        process.send_signal(signal.CTRL_BREAK_EVENT)
    else:
        process.send_signal(signal.SIGTERM)
    try:
        # Hosted Intel macOS runners occasionally need more than eight seconds
        # to drain native UI resources under concurrent test load. The product
        # shutdown remains bounded; this smoke allows the same platform budget
        # used by other native cleanup operations.
        return_code = process.wait(timeout=PROCESS_SHUTDOWN_TIMEOUT_SECONDS)
    except subprocess.TimeoutExpired as error:
        process.kill()
        process.wait(timeout=2)
        raise RuntimeError("desktop process exceeded its bounded shutdown") from error
    if return_code != 0:
        raise RuntimeError(f"desktop process returned {return_code} during shutdown")


def credential_account(data_directory: str) -> str:
    # Match Go filepath.Abs(filepath.Clean(...)): make the path absolute without
    # resolving symlinks such as macOS /var -> /private/var.
    absolute = os.path.abspath(os.path.normpath(data_directory))
    return "management-" + hashlib.sha256(absolute.encode()).hexdigest()[:32]


def delete_smoke_credential(data_directory: str) -> None:
    account = credential_account(data_directory)
    if os.name == "nt":
        script = (
            "$target='S3 RLCD Deck Companion:" + account + "'; "
            "$source=@'\n"
            "using System; using System.Runtime.InteropServices; "
            "public static class Native { "
            "[DllImport(\"advapi32.dll\", CharSet=CharSet.Unicode, SetLastError=true)] "
            "public static extern bool CredDelete(string target, int type, int flags); "
            "[DllImport(\"advapi32.dll\", CharSet=CharSet.Unicode, SetLastError=true)] "
            "public static extern bool CredRead(string target, int type, int flags, out IntPtr credential); "
            "[DllImport(\"advapi32.dll\")] public static extern void CredFree(IntPtr credential); }\n"
            "'@; Add-Type $source; "
            "if (-not [Native]::CredDelete($target,1,0)) { "
            "$code=[Runtime.InteropServices.Marshal]::GetLastWin32Error(); "
            "throw \"CredDelete failed: $code\" }; "
            "$credential=[IntPtr]::Zero; "
            "if ([Native]::CredRead($target,1,0,[ref]$credential)) { "
            "[Native]::CredFree($credential); throw 'credential still exists' }; "
            "$code=[Runtime.InteropServices.Marshal]::GetLastWin32Error(); "
            "if ($code -ne 1168) { throw \"CredRead verification failed: $code\" }"
        )
        subprocess.run(
            ["powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            # Windows Defender can make the one-time Add-Type compilation
            # exceed ten seconds on a cold hosted runner. Keep cleanup bounded
            # while leaving enough time for compilation plus vault I/O.
            timeout=CREDENTIAL_CLEANUP_TIMEOUT_SECONDS,
        )
    elif sys.platform == "darwin":
        subprocess.run(
            [
                "/usr/bin/security", "delete-generic-password",
                "-s", "S3 RLCD Deck Companion", "-a", account,
            ],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=CREDENTIAL_CLEANUP_TIMEOUT_SECONDS,
        )
        probe = subprocess.run(
            [
                "/usr/bin/security", "find-generic-password",
                "-s", "S3 RLCD Deck Companion", "-a", account,
            ],
            check=False,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=CREDENTIAL_CLEANUP_TIMEOUT_SECONDS,
        )
        if probe.returncode == 0:
            raise RuntimeError("native smoke credential still exists after cleanup")


def run_desktop_generation(
    executable: Path, expected_version: str, data_directory: str
) -> None:
    management_port = reserve_loopback_port()
    device_port = reserve_loopback_port()
    while device_port == management_port:
        device_port = reserve_loopback_port()
    management_address = f"127.0.0.1:{management_port}"
    device_address = f"127.0.0.1:{device_port}"
    creation_flags = subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
    environment = dict(os.environ)
    environment.pop("S3DECK_MANAGEMENT_TOKEN", None)
    process = subprocess.Popen(
        [
            str(executable),
            "--management-address", management_address,
            "--device-hub-address", device_address,
            "--data-directory", data_directory,
        ],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=environment,
        creationflags=creation_flags,
    )
    try:
        bootstrap = read_bootstrap(management_address, process)
        if bootstrap.get("version") != expected_version:
            raise RuntimeError(f"bootstrap identity mismatch: {bootstrap!r}")
        time.sleep(1.0)
        if process.poll() is not None:
            raise RuntimeError(
                f"native tray process did not remain running: {process.returncode}"
            )
        stop_process(process)
    finally:
        if process.poll() is None:
            process.kill()
            process.wait(timeout=2)
        stdout, stderr = process.communicate(timeout=2)
        if process.returncode != 0:
            print(stdout.decode("utf-8", errors="replace"), end="")
            print(stderr.decode("utf-8", errors="replace"), end="")


def run(executable: Path, expected_version: str, expected_commit: str) -> None:
    version_result = subprocess.run(
        [str(executable), "--version"],
        check=True,
        capture_output=True,
        text=True,
        timeout=10,
    )
    expected_identity = (
        f"s3deck-companion {expected_version} (commit {expected_commit})"
    )
    if version_result.stdout.strip() != expected_identity:
        raise RuntimeError(
            f"version output {version_result.stdout.strip()!r}, want {expected_identity!r}"
        )

    with tempfile.TemporaryDirectory(prefix="s3deck-native-smoke-") as data_directory:
        try:
            run_desktop_generation(executable, expected_version, data_directory)
            run_desktop_generation(executable, expected_version, data_directory)
        finally:
            delete_smoke_credential(data_directory)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--executable", type=Path, required=True)
    parser.add_argument("--expected-version", required=True)
    parser.add_argument("--expected-commit", required=True)
    arguments = parser.parse_args()
    run(
        arguments.executable.resolve(),
        arguments.expected_version,
        arguments.expected_commit,
    )
    print(
        "native Companion version, desktop startup, bootstrap, and shutdown passed"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
