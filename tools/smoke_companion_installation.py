#!/usr/bin/env python3
"""Exercise a real per-user install, upgrade, disable, enable, and uninstall."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

from smoke_companion_artifact import delete_smoke_credential


class InstallationCommandError(subprocess.SubprocessError):
    """A bounded, redacted lifecycle command failure safe for retry/reporting."""


def run(executable: Path, arguments: list[str], timeout: float = 120) -> subprocess.CompletedProcess[str]:
    completed = subprocess.run(
        [str(executable), *arguments],
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=timeout,
        env=os.environ.copy(),
    )
    if completed.returncode != 0:
        # Lifecycle commands emit bounded, fixed messages and never credentials.
        # Preserve those diagnostics without echoing argv, which contains local paths.
        raise InstallationCommandError(
            "installation command failed "
            f"(exit={completed.returncode}, stdout={completed.stdout.strip()!r}, "
            f"stderr={completed.stderr.strip()!r})"
        )
    return completed


def common(root: Path, data: Path) -> list[str]:
    return ["--installation-root", str(root), "--data-directory", str(data)]


def status(executable: Path, root: Path, data: Path) -> dict[str, object]:
    result = run(executable, ["--installation-status", *common(root, data)])
    return json.loads(result.stdout)


def registration_id(root: Path) -> str:
    # filepath.EvalSymlinks canonicalizes macOS /var to /private/var; normcase
    # mirrors the Windows case-insensitive registration identity.
    normalized = os.path.normcase(str(root.resolve()))
    return hashlib.sha256(normalized.encode()).hexdigest()[:12]


def stop_registered_process(root: Path) -> None:
    identifier = registration_id(root)
    if sys.platform == "darwin":
        service = f"gui/{os.getuid()}/com.vectorking.s3-rlcd-deck-companion.{identifier}"
        subprocess.run(
            ["/bin/launchctl", "bootout", service],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15, check=False,
        )
    elif sys.platform == "win32":
        subprocess.run(
            ["schtasks.exe", "/End", "/TN", f"S3 RLCD Deck Companion {identifier}"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15, check=False,
        )
    else:
        raise RuntimeError("installation smoke requires macOS or Windows")


def start_registered_process(root: Path) -> None:
    identifier = registration_id(root)
    if sys.platform == "darwin":
        label = f"com.vectorking.s3-rlcd-deck-companion.{identifier}"
        plist = Path.home() / "Library" / "LaunchAgents" / f"{label}.plist"
        subprocess.run(
            ["/bin/launchctl", "bootstrap", f"gui/{os.getuid()}", str(plist)],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15, check=True,
        )
    elif sys.platform == "win32":
        subprocess.run(
            ["schtasks.exe", "/Run", "/TN", f"S3 RLCD Deck Companion {identifier}"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15, check=True,
        )
    else:
        raise RuntimeError("installation smoke requires macOS or Windows")


def wait_for_status(
    executable: Path,
    root: Path,
    data: Path,
    predicate,
    timeout: float = 15,
) -> dict[str, object]:
    deadline = time.monotonic() + timeout
    last: dict[str, object] = {}
    while time.monotonic() < deadline:
        try:
            last = status(executable, root, data)
            if predicate(last):
                return last
        except (OSError, subprocess.SubprocessError, json.JSONDecodeError):
            pass
        time.sleep(0.2)
    raise RuntimeError(f"installation state did not converge: {last!r}")


def wait_for_bootstrap(expected_version: str, timeout: float = 15) -> None:
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with opener.open("http://127.0.0.1:7777/api/v1/bootstrap", timeout=0.5) as response:
                document = json.loads(response.read())
            if document.get("version") == expected_version:
                return
        except (OSError, urllib.error.URLError, json.JSONDecodeError):
            pass
        time.sleep(0.2)
    raise RuntimeError("installed Companion did not publish its expected bootstrap")


def wait_for_bootstrap_closed(timeout: float = 10) -> None:
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with opener.open("http://127.0.0.1:7777/api/v1/bootstrap", timeout=0.5):
                pass
        except (OSError, urllib.error.URLError):
            return
        time.sleep(0.2)
    raise RuntimeError("uninstalled Companion remained active")


def verify_private(path: Path) -> None:
    if sys.platform != "win32":
        mode = stat.S_IMODE(path.stat().st_mode)
        if mode & 0o077:
            raise RuntimeError(f"installation path is not owner-only: {path}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--previous-executable", type=Path, required=True)
    parser.add_argument("--executable", type=Path, required=True)
    parser.add_argument("--previous-version", required=True)
    parser.add_argument("--expected-version", required=True)
    parser.add_argument("--expected-commit", required=True)
    args = parser.parse_args()
    previous = args.previous_executable.resolve(strict=True)
    current = args.executable.resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="S3 Deck 安装 smoke ") as directory:
        temporary = Path(directory)
        root = temporary / "安装 根"
        data = temporary / "用户 数据"
        data.mkdir()
        marker = data / "user-data-marker"
        marker.write_text("retain me", encoding="utf-8")
        cleanup_executables = [current, previous]
        runtime_started = False
        try:
            run(previous, ["--install", *common(root, data)])
            start_registered_process(root)
            first = wait_for_status(
                previous, root, data,
                lambda value: value.get("installed") is True and value.get("enabled") is True,
            )
            if first.get("version") != args.previous_version:
                raise RuntimeError(f"unexpected previous version: {first!r}")
            wait_for_bootstrap(args.previous_version)
            runtime_started = True
            verify_private(root)
            verify_private(root / "installation.json")

            stop_registered_process(root)
            wait_for_bootstrap_closed()
            run(current, ["--install", *common(root, data)])
            start_registered_process(root)
            upgraded = wait_for_status(
                current, root, data,
                lambda value: value.get("version") == args.expected_version,
            )
            if upgraded.get("commit") != args.expected_commit or \
                    upgraded.get("previous_version") != args.previous_version:
                raise RuntimeError(f"upgrade identity mismatch: {upgraded!r}")
            wait_for_bootstrap(args.expected_version)

            run(current, ["--disable-login", *common(root, data)])
            wait_for_status(current, root, data, lambda value: value.get("enabled") is False)
            run(current, ["--enable-login", *common(root, data)])
            wait_for_status(current, root, data, lambda value: value.get("enabled") is True)

            run(current, ["--uninstall", *common(root, data)])
            uninstalled = wait_for_status(
                current, root, data,
                lambda value: value.get("installed") is False,
            )
            if uninstalled.get("enabled") is not False:
                raise RuntimeError(f"uninstall left startup enabled: {uninstalled!r}")
            wait_for_bootstrap_closed()
            if marker.read_text(encoding="utf-8") != "retain me":
                raise RuntimeError("uninstall removed user data")
            versions = [entry for entry in (root / "versions").iterdir() if entry.is_dir()]
            if len(versions) != 2:
                raise RuntimeError("upgrade did not retain both executable versions")
        finally:
            stop_registered_process(root)
            try:
                wait_for_bootstrap_closed(timeout=15)
            except RuntimeError:
                pass
            for executable in cleanup_executables:
                try:
                    run(executable, ["--uninstall", *common(root, data)], timeout=30)
                except (OSError, subprocess.SubprocessError):
                    pass
            if runtime_started:
                delete_smoke_credential(str(data.resolve()))
    print("Companion per-user installation smoke passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
