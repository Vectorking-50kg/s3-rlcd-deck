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

if result.returncode != 0:
    print(result.stdout, end="", file=sys.stderr)
    print(result.stderr, end="", file=sys.stderr)
    raise SystemExit("expected the harness to accept a captured boot_ok event")

expected = "boot_ok observed: version=0.1.0-dev reset_reason=power_on\n"
if result.stdout != expected:
    raise SystemExit(f"unexpected harness output: {result.stdout!r}")
