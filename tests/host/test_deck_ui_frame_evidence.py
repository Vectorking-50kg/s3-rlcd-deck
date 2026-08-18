#!/usr/bin/env python3

import hashlib
import json
import re
import struct
import unittest
import zlib
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]
EVIDENCE_DIRECTORY = REPOSITORY_ROOT / "docs/acceptance/ui-frames/bde4983"
MANIFEST_PATH = EVIDENCE_DIRECTORY / "manifest.json"
EXPECTED_PAGES = (
    "board",
    "pairing",
    "pairing-authenticating",
    "pairing-verified",
    "pairing-success",
    "pairing-expired",
    "pairing-error",
    "setup",
    "setup-validating",
    "setup-error",
    "ai",
    "ai-stale",
    "provider",
    "configuration",
    "serial",
    "offline",
    "error",
)
COMMIT_ID = re.compile(r"[0-9a-f]{40}")
SHA256 = re.compile(r"[0-9a-f]{64}")


def decode_png(path: Path) -> tuple[tuple[int, ...], bytes, tuple[bytes, ...]]:
    document = path.read_bytes()
    if not document.startswith(b"\x89PNG\r\n\x1a\n"):
        raise AssertionError(f"{path.name}: invalid PNG signature")
    offset = 8
    header: tuple[int, ...] | None = None
    compressed = bytearray()
    kinds: list[bytes] = []
    while offset < len(document):
        if offset + 12 > len(document):
            raise AssertionError(f"{path.name}: truncated PNG chunk")
        size = struct.unpack(">I", document[offset : offset + 4])[0]
        kind = document[offset + 4 : offset + 8]
        payload_start = offset + 8
        payload_end = payload_start + size
        crc_end = payload_end + 4
        if crc_end > len(document):
            raise AssertionError(f"{path.name}: PNG chunk exceeds file")
        payload = document[payload_start:payload_end]
        checksum = struct.unpack(">I", document[payload_end:crc_end])[0]
        if zlib.crc32(kind + payload) & 0xFFFFFFFF != checksum:
            raise AssertionError(f"{path.name}: PNG chunk checksum mismatch")
        kinds.append(kind)
        if kind == b"IHDR":
            if header is not None or size != 13:
                raise AssertionError(f"{path.name}: malformed PNG header")
            header = struct.unpack(">IIBBBBB", payload)
        elif kind == b"IDAT":
            compressed.extend(payload)
        elif kind == b"IEND":
            if size != 0 or crc_end != len(document):
                raise AssertionError(f"{path.name}: malformed PNG trailer")
            break
        offset = crc_end
    if header is None or not compressed:
        raise AssertionError(f"{path.name}: incomplete PNG")
    return header, zlib.decompress(bytes(compressed)), tuple(kinds)


class DeckUiFrameEvidenceContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.manifest = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))

    def test_manifest_is_bounded_and_explicit_about_the_remaining_optical_gate(self) -> None:
        self.assertEqual(
            set(self.manifest),
            {
                "schema_version",
                "evidence_kind",
                "firmware_commit",
                "firmware_version",
                "capture_tool_commit",
                "display",
                "optical_photo_status",
                "frames",
            },
        )
        self.assertEqual(self.manifest["schema_version"], 1)
        self.assertEqual(self.manifest["evidence_kind"], "deck_display_successful_frame")
        self.assertTrue(COMMIT_ID.fullmatch(self.manifest["firmware_commit"]))
        self.assertTrue(COMMIT_ID.fullmatch(self.manifest["capture_tool_commit"]))
        self.assertEqual(self.manifest["firmware_commit"][:7], EVIDENCE_DIRECTORY.name)
        self.assertEqual(
            self.manifest["capture_tool_commit"], self.manifest["firmware_commit"]
        )
        self.assertEqual(self.manifest["optical_photo_status"], "pending")
        self.assertEqual(
            self.manifest["display"],
            {
                "width": 400,
                "height": 300,
                "frame_bytes": 15_000,
                "pixel_format": "rlcd-packed-1bpp-monochrome",
            },
        )

    def test_every_required_page_is_an_exact_monochrome_400_by_300_frame(self) -> None:
        frames = self.manifest["frames"]
        self.assertEqual(tuple(frame["page"] for frame in frames), EXPECTED_PAGES)
        hashes: set[str] = set()
        for frame in frames:
            self.assertEqual(
                set(frame),
                {"page", "file", "completed_frames", "bytes", "sha256"},
            )
            self.assertEqual(frame["file"], f"{frame['page']}.png")
            self.assertGreaterEqual(frame["completed_frames"], 1)
            self.assertTrue(SHA256.fullmatch(frame["sha256"]))
            image_path = EVIDENCE_DIRECTORY / frame["file"]
            document = image_path.read_bytes()
            self.assertEqual(len(document), frame["bytes"])
            self.assertEqual(hashlib.sha256(document).hexdigest(), frame["sha256"])
            header, scanlines, kinds = decode_png(image_path)
            self.assertEqual(header, (400, 300, 8, 0, 0, 0, 0))
            self.assertEqual(kinds, (b"IHDR", b"IDAT", b"IEND"))
            self.assertEqual(len(scanlines), 300 * 401)
            pixels: set[int] = set()
            for row in range(300):
                start = row * 401
                self.assertEqual(scanlines[start], 0)
                pixels.update(scanlines[start + 1 : start + 401])
            self.assertEqual(pixels, {0, 255}, frame["file"])
            hashes.add(frame["sha256"])
        self.assertEqual(len(hashes), len(EXPECTED_PAGES))


if __name__ == "__main__":
    unittest.main()
