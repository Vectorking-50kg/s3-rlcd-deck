import pathlib
import subprocess
import sys
import tempfile


REPOSITORY_ROOT = pathlib.Path(__file__).resolve().parents[2]
HARNESS = REPOSITORY_ROOT / "tools" / "hil_boot_smoke.py"


with tempfile.TemporaryDirectory() as temporary_directory:
    capture = pathlib.Path(temporary_directory) / "console.log"
    capture.write_text(
        "I (22) boot: ESP-IDF v6.0.2\n"
        '{"type":"boot_ok","firmware_version":"0.1.0-dev",'
        '"reset_reason":"power_on","uptime_ms":42,'
        '"minimum_free_heap_bytes":131072}\n',
        encoding="utf-8",
    )

    result = subprocess.run(
        [sys.executable, str(HARNESS), "--input-file", str(capture)],
        check=False,
        capture_output=True,
        text=True,
    )

    display_capture = pathlib.Path(temporary_directory) / "display-console.log"
    display_capture.write_text(
        capture.read_text(encoding="utf-8")
        + '{"type":"display_ready","width":400,"height":300,'
        '"frame_bytes":15000,"submitted_frames":1,"completed_frames":1,'
        '"transfer_timeouts":0,"start_failures":0,"rejected_updates":0}\n',
        encoding="utf-8",
    )
    display_result = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "--input-file",
            str(display_capture),
            "--expect-display",
        ],
        check=False,
        capture_output=True,
        text=True,
    )

if result.returncode != 0:
    print(result.stdout, end="", file=sys.stderr)
    print(result.stderr, end="", file=sys.stderr)
    raise SystemExit("expected the harness to accept a captured boot_ok event")

expected = "boot_ok observed: version=0.1.0-dev reset_reason=power_on\n"
if result.stdout != expected:
    raise SystemExit(f"unexpected harness output: {result.stdout!r}")

if display_result.returncode != 0:
    print(display_result.stdout, end="", file=sys.stderr)
    print(display_result.stderr, end="", file=sys.stderr)
    raise SystemExit("expected the harness to accept a completed display frame")

display_expected = expected + "display_ready observed: frames=1 timeouts=0\n"
if display_result.stdout != display_expected:
    raise SystemExit(f"unexpected display harness output: {display_result.stdout!r}")
