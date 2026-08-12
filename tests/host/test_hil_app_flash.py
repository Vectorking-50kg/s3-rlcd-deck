#!/usr/bin/env python3

import importlib.util
import pathlib
import struct
from types import SimpleNamespace


MODULE_PATH = pathlib.Path(__file__).parents[2] / "tools" / "hil_app_flash.py"
SPEC = importlib.util.spec_from_file_location("hil_app_flash", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def entry(label: str, subtype: int, offset: int, size: int) -> bytes:
    raw_label = label.encode("ascii").ljust(16, b"\0")
    return struct.pack("<HBBII16sI", 0x50AA, 0, subtype, offset, size, raw_label, 0)


partition_data = b"".join(
    (
        entry("nvs", 2, 0x9000, 0x6000),
        entry("ota_0", 0x10, 0x20000, 0x1A9000),
        entry("ota_1", 0x11, 0x1D0000, 0x1A9000),
        b"\xff" * 32,
    )
)
partitions = MODULE.parse_ota_app_partitions(partition_data)
assert [(partition.label, partition.offset) for partition in partitions] == [
    ("ota_0", 0x20000),
    ("ota_1", 0x1D0000),
]

for invalid in (
    entry("ota_0", 0x10, 0x20000, 0x1A9000) + b"\xff" * 32,
    entry("ota_0", 0x10, 0x20001, 0x1A9000)
    + entry("ota_1", 0x11, 0x1D0000, 0x1A9000)
    + b"\xff" * 32,
):
    try:
        MODULE.parse_ota_app_partitions(invalid)
    except MODULE.AppFlashFailure:
        pass
    else:
        raise AssertionError("unsafe OTA layout was accepted")

ports = [
    SimpleNamespace(
        device="/dev/fakeDeck",
        vid=0x303A,
        pid=0x1001,
        serial_number="58:E6:C5:71:DE:B8",
    )
]
assert MODULE.usb_jtag_serial_for_port("/dev/fakeDeck", ports) == "58:E6:C5:71:DE:B8"
ports[0].pid = 0x0002
try:
    MODULE.usb_jtag_serial_for_port("/dev/fakeDeck", ports)
except MODULE.AppFlashFailure:
    pass
else:
    raise AssertionError("unrelated Espressif USB device was accepted")

command = MODULE.openocd_flash_command(
    "/fake/openocd",
    "58:E6:C5:71:DE:B8",
    pathlib.Path("/tmp/deck app.bin"),
    partitions,
)
serialized = " ".join(command).lower()
assert serialized.count("program_esp") == 2
assert "0x20000 verify no_clock_boost" in serialized
assert "0x1d0000 verify no_clock_boost" in serialized
assert serialized.index("0x20000") < serialized.index("0x1d0000") < serialized.index("reset run")
assert "erase" not in serialized
assert "efuse" not in serialized
assert "partition-table" not in serialized

verify_command = MODULE.openocd_partition_verify_command(
    "/fake/openocd",
    "58:E6:C5:71:DE:B8",
    pathlib.Path("/tmp/deck partition-table.bin"),
)
serialized_verify = " ".join(verify_command).lower()
assert "esp flash_stub_clock_boost off" in serialized_verify
assert serialized_verify.count("reset halt") == 2
assert "esp verify_bank_hash 0" in serialized_verify
assert "0x8000" in serialized_verify
assert "program" not in serialized_verify
assert "erase" not in serialized_verify

reset_command = MODULE.openocd_reset_run_command(
    "/fake/openocd", "58:E6:C5:71:DE:B8"
)
serialized_reset = " ".join(reset_command).lower()
assert "reset run" in serialized_reset
assert "program" not in serialized_reset
assert "erase" not in serialized_reset
