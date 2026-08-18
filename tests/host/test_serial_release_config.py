#!/usr/bin/env python3

from pathlib import Path


REPOSITORY = Path(__file__).resolve().parents[2]
FIRMWARE = REPOSITORY / "firmware"

common = (FIRMWARE / "sdkconfig.defaults").read_text(encoding="utf-8")
release = (FIRMWARE / "sdkconfig.defaults.release").read_text(encoding="utf-8")
dev = (FIRMWARE / "sdkconfig.defaults.dev").read_text(encoding="utf-8")

assert "CONFIG_USJ_ENABLE_USB_SERIAL_JTAG=y" in common
assert "CONFIG_SPIRAM_MALLOC_ALWAYSINTERNAL=8192" in common
assert "CONFIG_MBEDTLS_DEFAULT_MEM_ALLOC=y" in common
assert "CONFIG_MBEDTLS_ASYMMETRIC_CONTENT_LEN=y" in common
assert "CONFIG_MBEDTLS_SSL_IN_CONTENT_LEN=16384" in common
assert "CONFIG_MBEDTLS_SSL_OUT_CONTENT_LEN=4096" in common
assert "# CONFIG_MBEDTLS_HARDWARE_AES is not set" in common
assert "# CONFIG_MBEDTLS_AES_USE_INTERRUPT is not set" in common
assert "CONFIG_MBEDTLS_INTERNAL_MEM_ALLOC=y" not in common
assert "CONFIG_MBEDTLS_EXTERNAL_MEM_ALLOC=y" not in common
assert "CONFIG_ESP_CONSOLE_NONE=y" in release
assert "# CONFIG_DECK_DIAGNOSTIC_CONSOLE is not set" in release
assert "CONFIG_ESP_CONSOLE_USB_SERIAL_JTAG=y" not in release
assert "CONFIG_DECK_DIAGNOSTIC_CONSOLE=y" in dev

production_sources = "\n".join(
    path.read_text(encoding="utf-8", errors="strict")
    for path in FIRMWARE.rglob("*")
    if path.is_file()
    and path.suffix in {".c", ".cc", ".cpp", ".h", ".hpp"}
    and "managed_components" not in path.parts
)
assert "tinyusb" not in production_sources.lower()
