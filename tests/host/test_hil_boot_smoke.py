import importlib.util
import pathlib
import subprocess
import sys
import tempfile
from collections.abc import Iterator


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
    def __init__(
        self,
        clock: FakeClock,
        failed_writes: int = 0,
        fatal_write: bool = False,
    ) -> None:
        self.clock = clock
        self.failed_writes = failed_writes
        self.fatal_write = fatal_write
        self.timeout = 0.25
        self.write_timeout = 0.25
        self.writes: list[bytes] = []

    def __enter__(self) -> "FakeSerial":
        return self

    def __exit__(self, *_: object) -> None:
        return None

    def write(self, data: bytes) -> None:
        if self.fatal_write:
            raise OSError("serial device disconnected")
        if self.failed_writes > 0:
            self.failed_writes -= 1
            raise TimeoutError("temporary serial write timeout")
        self.writes.append(data)

    def flush(self) -> None:
        raise AssertionError("the live HIL harness must not use blocking serial flush")

    def readline(self) -> bytes:
        self.clock.now += self.timeout
        return b""


class BudgetSerial(FakeSerial):
    def write(self, data: bytes) -> None:
        self.clock.now += self.write_timeout
        super().write(data)


fake_clock = FakeClock()
fake_serial = FakeSerial(fake_clock, failed_writes=1)
serial_options: dict[str, object] = {}


def fake_serial_factory(**options: object) -> FakeSerial:
    serial_options.update(options)
    return fake_serial


list(
    harness_module.serial_lines(
        "fake-port",
        1.6,
        serial_factory=fake_serial_factory,
        monotonic=fake_clock.monotonic,
    )
)
if serial_options.get("write_timeout") != 0.25:
    raise SystemExit("expected live serial writes to have a finite timeout")
if len(fake_serial.writes) < 3 or any(
    write != harness_module.HIL_READY for write in fake_serial.writes
):
    raise SystemExit("expected the live harness to repeat the ready handshake")

fatal_clock = FakeClock()
fatal_serial = FakeSerial(fatal_clock, fatal_write=True)
try:
    list(
        harness_module.serial_lines(
            "fake-port",
            1.0,
            serial_factory=lambda **_: fatal_serial,
            monotonic=fatal_clock.monotonic,
        )
    )
except OSError as error:
    if "disconnected" not in str(error):
        raise
else:
    raise SystemExit("expected a fatal serial write error to propagate")

budget_clock = FakeClock()
budget_serial = BudgetSerial(budget_clock)
list(
    harness_module.serial_lines(
        "fake-port",
        0.6,
        serial_factory=lambda **_: budget_serial,
        monotonic=budget_clock.monotonic,
    )
)
if budget_clock.now > 0.6:
    raise SystemExit("expected serial I/O to stay within the overall deadline")

late_clock = FakeClock()


def late_display_lines() -> Iterator[str]:
    late_clock.now = 1.0
    yield (
        '{"type":"boot_ok","firmware_version":"0.1.0-dev",'
        '"reset_reason":"power_on","uptime_ms":42,'
        '"minimum_free_heap_bytes":131072}'
    )
    late_clock.now = 2.0
    yield (
        '{"type":"display_ready","width":400,"height":300,'
        '"frame_bytes":15000,"submitted_frames":1,"completed_frames":1,'
        '"transfer_timeouts":0,"start_failures":0,"rejected_updates":0}'
    )
    late_clock.now = 25.0
    yield (
        '{"type":"display_progress","width":400,"height":300,'
        '"frame_bytes":15000,"submitted_frames":3,"completed_frames":3,'
        '"transfer_timeouts":0,"start_failures":0,"rejected_updates":0}'
    )


late_progress = harness_module.diagnostic_events(
    late_display_lines(),
    True,
    display_deadline_seconds=20.0,
    monotonic=late_clock.monotonic,
)[2]
if late_progress is not None:
    raise SystemExit("expected a third frame after 20 seconds to miss the stability window")


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

    peripheral_event = (
        '{"type":"peripheral_state","rtc_available":true,"rtc_hour":9,'
        '"rtc_minute":41,"sensor_available":true,'
        '"raw_temperature_tenths_c":237,'
        '"calibrated_temperature_tenths_c":197,'
        '"humidity_tenths_percent":630,"buttons_available":true,'
        '"key_event":"none",'
        '"key_event_count":0,"boot_event":"none","boot_event_count":0,'
        '"rtc_errors":0,"sensor_errors":0}\n'
    )
    peripheral_capture = pathlib.Path(temporary_directory) / "peripheral-console.log"
    peripheral_capture.write_text(
        display_capture.read_text(encoding="utf-8") + peripheral_event * 3,
        encoding="utf-8",
    )
    peripheral_result = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "--input-file",
            str(peripheral_capture),
            "--expect-display",
            "--expect-peripherals",
        ],
        check=False,
        capture_output=True,
        text=True,
    )

    bad_peripheral_capture = pathlib.Path(temporary_directory) / "bad-peripheral-console.log"
    bad_peripheral_capture.write_text(
        display_capture.read_text(encoding="utf-8")
        + peripheral_event.replace('"sensor_errors":0', '"sensor_errors":1') * 3,
        encoding="utf-8",
    )
    bad_peripheral_result = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "--input-file",
            str(bad_peripheral_capture),
            "--expect-peripherals",
        ],
        check=False,
        capture_output=True,
        text=True,
    )

    bad_calibration_capture = pathlib.Path(temporary_directory) / "bad-calibration-console.log"
    bad_calibration_capture.write_text(
        display_capture.read_text(encoding="utf-8")
        + peripheral_event.replace(
            '"calibrated_temperature_tenths_c":197',
            '"calibrated_temperature_tenths_c":198',
        )
        * 3,
        encoding="utf-8",
    )
    bad_calibration_result = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "--input-file",
            str(bad_calibration_capture),
            "--expect-peripherals",
        ],
        check=False,
        capture_output=True,
        text=True,
    )

    button_event = (
        peripheral_event.replace('"key_event":"none"', '"key_event":"long_press"')
        .replace('"key_event_count":0', '"key_event_count":2')
        .replace('"boot_event":"none"', '"boot_event":"long_press"')
        .replace('"boot_event_count":0', '"boot_event_count":2')
    )
    button_capture = pathlib.Path(temporary_directory) / "button-console.log"
    button_capture.write_text(
        display_capture.read_text(encoding="utf-8") + button_event * 3,
        encoding="utf-8",
    )
    button_result = subprocess.run(
        [
            sys.executable,
            str(HARNESS),
            "--input-file",
            str(button_capture),
            "--expect-peripherals",
            "--expect-key-event",
            "long_press",
            "--minimum-key-events",
            "2",
            "--expect-boot-event",
            "long_press",
            "--minimum-boot-events",
            "2",
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

if peripheral_result.returncode != 0:
    print(peripheral_result.stdout, end="", file=sys.stderr)
    print(peripheral_result.stderr, end="", file=sys.stderr)
    raise SystemExit("expected the harness to accept three clean peripheral samples")

peripheral_expected = (
    display_expected
    + "peripheral_state observed: rtc=09:41 raw_temperature=23.7C "
    + "calibrated_temperature=19.7C humidity=63.0% samples=3\n"
)
if peripheral_result.stdout != peripheral_expected:
    raise SystemExit(f"unexpected peripheral harness output: {peripheral_result.stdout!r}")

if bad_peripheral_result.returncode == 0 or "sensor unavailable or invalid" not in bad_peripheral_result.stderr:
    raise SystemExit("expected the peripheral harness to reject sensor I2C errors")

if (
    bad_calibration_result.returncode == 0
    or "sensor unavailable or invalid" not in bad_calibration_result.stderr
):
    raise SystemExit("expected the peripheral harness to reject the wrong calibration offset")

if button_result.returncode != 0:
    print(button_result.stdout, end="", file=sys.stderr)
    print(button_result.stderr, end="", file=sys.stderr)
    raise SystemExit("expected the peripheral harness to accept physical button evidence")

if reset_result.returncode == 0 or "unexpected reset" not in reset_result.stderr:
    raise SystemExit("expected the display harness to reject a second boot_ok event")
