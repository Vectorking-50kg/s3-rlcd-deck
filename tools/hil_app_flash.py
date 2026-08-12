#!/usr/bin/env python3

"""Safely program and verify every OTA application slot over the Deck USB-JTAG link."""

import argparse
from dataclasses import dataclass
import pathlib
import shutil
import signal
import struct
import subprocess
import sys
from typing import Any, Iterable


REPOSITORY_ROOT = pathlib.Path(__file__).resolve().parents[1]
PARTITION_ENTRY_BYTES = 32
PARTITION_MAGIC = 0x50AA
PARTITION_MD5_MAGIC = 0xEBEB
APP_PARTITION_TYPE = 0x00
OTA_SUBTYPE_MINIMUM = 0x10
OTA_SUBTYPE_MAXIMUM = 0x1F
EXPECTED_OTA_LABELS = ("ota_0", "ota_1")
ESPRESSIF_USB_VID = 0x303A
ESP32_S3_USB_SERIAL_JTAG_PID = 0x1001
PARTITION_TABLE_FLASH_OFFSET = 0x8000


class AppFlashFailure(RuntimeError):
    pass


@dataclass(frozen=True)
class AppPartition:
    label: str
    subtype: int
    offset: int
    size: int


def parse_ota_app_partitions(data: bytes) -> tuple[AppPartition, ...]:
    partitions: list[AppPartition] = []
    for entry_offset in range(0, len(data), PARTITION_ENTRY_BYTES):
        entry = data[entry_offset : entry_offset + PARTITION_ENTRY_BYTES]
        if len(entry) != PARTITION_ENTRY_BYTES:
            raise AppFlashFailure("partition table has a truncated entry")
        magic = struct.unpack_from("<H", entry)[0]
        if magic in (0xFFFF, PARTITION_MD5_MAGIC):
            break
        if magic != PARTITION_MAGIC:
            raise AppFlashFailure("partition table contains an invalid entry")
        partition_type, subtype, offset, size, raw_label = struct.unpack_from(
            "<BBII16s", entry, 2
        )
        if partition_type != APP_PARTITION_TYPE or not (
            OTA_SUBTYPE_MINIMUM <= subtype <= OTA_SUBTYPE_MAXIMUM
        ):
            continue
        try:
            label = raw_label.split(b"\0", 1)[0].decode("ascii")
        except UnicodeDecodeError as error:
            raise AppFlashFailure("OTA partition label is not ASCII") from error
        partitions.append(AppPartition(label, subtype, offset, size))

    partitions.sort(key=lambda partition: partition.subtype)
    if tuple(partition.label for partition in partitions) != EXPECTED_OTA_LABELS:
        raise AppFlashFailure("expected exactly the ota_0 and ota_1 application slots")
    for index, partition in enumerate(partitions):
        if partition.subtype != OTA_SUBTYPE_MINIMUM + index:
            raise AppFlashFailure("OTA application subtypes are not ota_0 and ota_1")
        if partition.offset <= 0 or partition.offset % 0x10000 != 0:
            raise AppFlashFailure("OTA application offset is not 64 KiB aligned")
        if partition.size <= 0 or partition.size % 0x1000 != 0:
            raise AppFlashFailure("OTA application size is not sector aligned")
    if partitions[0].offset + partitions[0].size > partitions[1].offset:
        raise AppFlashFailure("OTA application slots overlap")
    return tuple(partitions)


def validate_application_image(
    image: pathlib.Path, partitions: tuple[AppPartition, ...]
) -> int:
    try:
        image_size = image.stat().st_size
    except OSError as error:
        raise AppFlashFailure(f"application image is unavailable: {error}") from error
    if image_size <= 0:
        raise AppFlashFailure("application image is empty")
    if any(image_size > partition.size for partition in partitions):
        raise AppFlashFailure("application image does not fit every OTA slot")
    return image_size


def usb_jtag_serial_for_port(port: str, ports: Iterable[Any] | None = None) -> str:
    if ports is None:
        try:
            from serial.tools import list_ports
        except ImportError as error:
            raise AppFlashFailure("app flashing requires pyserial") from error
        ports = list_ports.comports()
    matches = [candidate for candidate in ports if str(candidate.device) == port]
    if len(matches) != 1:
        raise AppFlashFailure("explicit serial port does not identify exactly one device")
    match = matches[0]
    if (
        getattr(match, "vid", None) != ESPRESSIF_USB_VID
        or getattr(match, "pid", None) != ESP32_S3_USB_SERIAL_JTAG_PID
    ):
        raise AppFlashFailure("serial port is not the Deck ESP32-S3 USB Serial/JTAG device")
    serial_number = str(getattr(match, "serial_number", "") or "")
    if not serial_number or any(character in serial_number for character in "{}\r\n"):
        raise AppFlashFailure("USB-JTAG serial number is unavailable or unsafe")
    return serial_number


def tcl_braced(value: pathlib.Path | str) -> str:
    text = str(value)
    if any(character in text for character in "{}\r\n"):
        raise AppFlashFailure("OpenOCD argument contains unsupported characters")
    return "{" + text + "}"


def openocd_flash_command(
    openocd: str,
    adapter_serial: str,
    image: pathlib.Path,
    partitions: tuple[AppPartition, ...],
) -> list[str]:
    command = [
        openocd,
        "-f",
        "board/esp32s3-builtin.cfg",
        "-c",
        f"adapter serial {tcl_braced(adapter_serial)}",
        "-c",
        "adapter speed 10000",
        "-c",
        "init",
        "-c",
        "reset halt",
    ]
    for partition in partitions:
        command.extend(
            [
                "-c",
                "program_esp "
                f"{tcl_braced(image.resolve())} 0x{partition.offset:x} "
                "verify no_clock_boost",
            ]
        )
    command.extend(["-c", "reset run", "-c", "shutdown"])
    return command


def openocd_partition_verify_command(
    openocd: str,
    adapter_serial: str,
    partition_table: pathlib.Path,
) -> list[str]:
    return [
        openocd,
        "-f",
        "board/esp32s3-builtin.cfg",
        "-c",
        f"adapter serial {tcl_braced(adapter_serial)}",
        "-c",
        "adapter speed 10000",
        "-c",
        "init",
        "-c",
        "reset halt",
        "-c",
        "esp flash_stub_clock_boost off",
        "-c",
        "reset halt",
        "-c",
        "esp verify_bank_hash 0 "
        f"{tcl_braced(partition_table.resolve())} "
        f"0x{PARTITION_TABLE_FLASH_OFFSET:x}",
        "-c",
        "reset run",
        "-c",
        "shutdown",
    ]


def openocd_reset_run_command(openocd: str, adapter_serial: str) -> list[str]:
    return [
        openocd,
        "-f",
        "board/esp32s3-builtin.cfg",
        "-c",
        f"adapter serial {tcl_braced(adapter_serial)}",
        "-c",
        "adapter speed 10000",
        "-c",
        "init",
        "-c",
        "reset run",
        "-c",
        "shutdown",
    ]


def best_effort_reset_run(
    openocd: str,
    adapter_serial: str,
    runner: Any = subprocess.run,
) -> None:
    try:
        runner(
            openocd_reset_run_command(openocd, adapter_serial),
            cwd=REPOSITORY_ROOT,
            check=False,
        )
    except Exception:
        pass


def run_openocd_checked(
    command: list[str],
    openocd: str,
    adapter_serial: str,
    failure_message: str,
    runner: Any = subprocess.run,
) -> None:
    try:
        result = runner(command, cwd=REPOSITORY_ROOT, check=False)
    except BaseException:
        best_effort_reset_run(openocd, adapter_serial, runner)
        raise
    if result.returncode != 0:
        best_effort_reset_run(openocd, adapter_serial, runner)
        raise AppFlashFailure(failure_message)


def interrupt_app_flash(_signum: int, _frame: Any) -> None:
    raise AppFlashFailure("app flashing was interrupted; target recovery was requested")


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Program both verified Deck OTA application slots over USB-JTAG."
    )
    parser.add_argument("--port", required=True)
    parser.add_argument(
        "--build-dir",
        type=pathlib.Path,
        default=REPOSITORY_ROOT / "build" / "dev",
    )
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    previous_sigterm_handler = signal.signal(signal.SIGTERM, interrupt_app_flash)
    try:
        partition_table = arguments.build_dir / "partition_table" / "partition-table.bin"
        application_image = arguments.build_dir / "s3_rlcd_deck.bin"
        try:
            partition_data = partition_table.read_bytes()
        except OSError as error:
            raise AppFlashFailure(f"partition table is unavailable: {error}") from error
        partitions = parse_ota_app_partitions(partition_data)
        image_size = validate_application_image(application_image, partitions)
        adapter_serial = usb_jtag_serial_for_port(arguments.port)
        openocd = shutil.which("openocd")
        if openocd is None:
            raise AppFlashFailure("openocd is unavailable; activate ESP-IDF 6.0.2")
        verify_command = openocd_partition_verify_command(
            openocd,
            adapter_serial,
            partition_table,
        )
        run_openocd_checked(
            verify_command,
            openocd,
            adapter_serial,
            (
                "device partition table does not exactly match the development build; "
                "refusing app-slot writes"
            ),
        )
        command = openocd_flash_command(
            openocd,
            adapter_serial,
            application_image,
            partitions,
        )
        run_openocd_checked(
            command,
            openocd,
            adapter_serial,
            "OpenOCD app-slot programming or verification failed",
        )
    except AppFlashFailure as error:
        print(f"Deck app flash failed: {error}", file=sys.stderr)
        return 1
    finally:
        signal.signal(signal.SIGTERM, previous_sigterm_handler)

    slots = ",".join(f"{partition.label}@0x{partition.offset:x}" for partition in partitions)
    print(f"Deck app slots verified: {slots} image_bytes={image_size}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
