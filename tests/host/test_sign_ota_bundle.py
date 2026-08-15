#!/usr/bin/env python3

import base64
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, os.fspath(ROOT / "tools"))
import sign_ota_bundle as signer  # noqa: E402


class SignOTABundleContract(unittest.TestCase):
    def test_signed_archive_has_fixed_manifest_and_private_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            key = root / "key.pem"
            image = root / "firmware.bin"
            output = root / "firmware.s3ota"
            subprocess.run(
                ["openssl", "ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", key],
                check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )
            image.write_bytes(bytes(range(256)) * 16)
            args = type("Args", (), {
                "image": image, "version": "0.3.0-dev", "private_key": key,
                "output": output, "board": signer.BOARD, "key_id": 1,
                "minimum_protocol": 1,
            })()
            signer.build_bundle(args)
            archive = json.loads(output.read_bytes())
            decoded = base64.b64decode(archive["image"], validate=True)
            manifest = archive["manifest"]
            self.assertEqual(decoded, image.read_bytes())
            self.assertEqual(manifest["image_length"], len(decoded))
            self.assertEqual(manifest["image_sha256"], hashlib.sha256(decoded).hexdigest())
            self.assertEqual(len(base64.b64decode(manifest["signature"], validate=True)), 64)
            self.assertEqual(len(signer.canonical_manifest(
                version=manifest["version"], board=manifest["board"],
                image_length=manifest["image_length"], digest=bytes.fromhex(manifest["image_sha256"]),
                key_id=manifest["signing_key_id"], minimum_protocol=manifest["minimum_protocol_version"],
            )), 134)
            self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)

    def test_invalid_board_and_oversize_image_fail_before_signing(self):
        with self.assertRaises(signer.BundleFailure):
            signer.canonical_manifest(
                version="0.3.0", board="wrong", image_length=1,
                digest=bytes(32), key_id=1, minimum_protocol=1,
            )
        with self.assertRaises(signer.BundleFailure):
            signer.canonical_manifest(
                version="0.3.0", board=signer.BOARD,
                image_length=signer.MAXIMUM_IMAGE_BYTES + 1,
                digest=bytes(32), key_id=1, minimum_protocol=1,
            )


if __name__ == "__main__":
    unittest.main()
