#!/usr/bin/env python3

import importlib.util
import pathlib
import subprocess


MODULE_PATH = pathlib.Path(__file__).parents[2] / "tools" / "smoke_companion_artifact.py"
SPEC = importlib.util.spec_from_file_location("smoke_companion_artifact", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


calls: list[tuple[list[str], dict[str, object]]] = []


def capture_run(command: list[str], **options: object) -> subprocess.CompletedProcess[bytes]:
    calls.append((command, options))
    return subprocess.CompletedProcess(command, 0)


original_name = MODULE.os.name
original_run = MODULE.subprocess.run
try:
    MODULE.os.name = "nt"
    MODULE.subprocess.run = capture_run
    MODULE.delete_smoke_credential("C:/bounded-smoke")
finally:
    MODULE.os.name = original_name
    MODULE.subprocess.run = original_run

assert len(calls) == 1
command, options = calls[0]
assert command[:3] == ["powershell.exe", "-NoProfile", "-NonInteractive"]
assert options["check"] is True
assert options["timeout"] == MODULE.CREDENTIAL_CLEANUP_TIMEOUT_SECONDS == 30


def timeout_run(command: list[str], **options: object) -> subprocess.CompletedProcess[bytes]:
    raise subprocess.TimeoutExpired(command, options["timeout"])


try:
    MODULE.os.name = "nt"
    MODULE.subprocess.run = timeout_run
    MODULE.delete_smoke_credential("C:/bounded-smoke")
except subprocess.TimeoutExpired:
    pass
else:
    raise AssertionError("credential cleanup timeout did not fail closed")
finally:
    MODULE.os.name = original_name
    MODULE.subprocess.run = original_run
