#!/usr/bin/env python3
"""Run a native Companion artifact through its version and desktop startup contract."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


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
        return_code = process.wait(timeout=8)
    except subprocess.TimeoutExpired as error:
        process.kill()
        process.wait(timeout=2)
        raise RuntimeError("desktop process exceeded its bounded shutdown") from error
    if return_code != 0:
        raise RuntimeError(f"desktop process returned {return_code} during shutdown")


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

    management_port = reserve_loopback_port()
    device_port = reserve_loopback_port()
    while device_port == management_port:
        device_port = reserve_loopback_port()
    management_address = f"127.0.0.1:{management_port}"
    device_address = f"127.0.0.1:{device_port}"
    creation_flags = (
        subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
    )
    with tempfile.TemporaryDirectory(prefix="s3deck-native-smoke-") as data_directory:
        process = subprocess.Popen(
            [
                str(executable),
                "--management-address",
                management_address,
                "--device-hub-address",
                device_address,
                "--data-directory",
                data_directory,
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
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
