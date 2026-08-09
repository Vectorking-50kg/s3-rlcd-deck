import importlib.util
import pathlib
import subprocess
import sys
import tempfile


REPOSITORY_ROOT = pathlib.Path(__file__).resolve().parents[2]
HARNESS = REPOSITORY_ROOT / "tools" / "hil_boot_smoke.py"

module_spec = importlib.util.spec_from_file_location("deck_hil_boot_smoke", HARNESS)
if module_spec is None or module_spec.loader is None:
    raise SystemExit("failed to load the HIL harness module")
harness_module = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(harness_module)


class FakeClock:
    def __init__(self) -> None:
        self.now = 0.0

    def monotonic(self) -> float:
        return self.now


class FakeSerial:
    def __init__(self, clock: FakeClock) -> None:
        self.clock = clock
        self.writes: list[bytes] = []

    def __enter__(self) -> "FakeSerial":
        return self

    def __exit__(self, *_: object) -> None:
        return None

    def write(self, data: bytes) -> None:
        self.writes.append(data)

    def flush(self) -> None:
        return None

    def readline(self) -> bytes:
        self.clock.now += 0.25
        return b""


fake_clock = FakeClock()
fake_serial = FakeSerial(fake_clock)
list(
    harness_module.serial_lines(
        "fake-port",
        1.6,
        serial_factory=lambda **_: fake_serial,
        monotonic=fake_clock.monotonic,
    )
)
if len(fake_serial.writes) < 3 or any(
    write != harness_module.HIL_READY for write in fake_serial.writes
):
    raise SystemExit("expected the live harness to repeat the ready handshake")


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
        '"transfer_timeouts":0,"start_failures":0,"rejected_updates":0}\n'
        + '{"type":"display_progress","width":400,"height":300,'
        '"frame_bytes":15000,"submitted_frames":3,"completed_frames":3,'
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

    reset_capture = pathlib.Path(temporary_directory) / "unexpected-reset.log"
    reset_capture.write_text(
        display_capture.read_text(encoding="utf-8")
        + '{"type":"boot_ok","firmware_version":"0.1.0-dev",'
        '"reset_reason":"watchdog","uptime_ms":1,'
        '"minimum_free_heap_bytes":120000}\n',
        encoding="utf-8",
    )
    reset_result = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "--input-file",
            str(reset_capture),
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

display_expected = (
    expected
    + "display_ready observed: frames=1 timeouts=0\n"
    + "display_progress observed: frames=3 timeouts=0\n"
)
if display_result.stdout != display_expected:
    raise SystemExit(f"unexpected display harness output: {display_result.stdout!r}")

if reset_result.returncode == 0 or "unexpected reset" not in reset_result.stderr:
    raise SystemExit("expected the display harness to reject a second boot_ok event")
