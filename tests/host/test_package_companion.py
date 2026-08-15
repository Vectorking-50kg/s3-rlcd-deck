#!/usr/bin/env python3

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
import zipfile


REPOSITORY = Path(__file__).resolve().parents[2]
PACKAGER = REPOSITORY / "tools" / "package_companion.py"
TARGETS = {
    "darwin-arm64": "s3deck-companion",
    "darwin-amd64": "s3deck-companion",
    "windows-amd64": "s3deck-companion.exe",
}


class CompanionPackageContractTest(unittest.TestCase):
    def test_archives_are_reproducible_auditable_and_self_installing(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifacts = root / "artifacts"
            for target, name in TARGETS.items():
                path = artifacts / target / name
                path.parent.mkdir(parents=True)
                path.write_bytes((target + "\n").encode())
                path.chmod(0o700)
            outputs = []
            for name in ("first", "second"):
                output = root / name
                subprocess.run([
                    "python3", str(PACKAGER),
                    "--repository-root", str(REPOSITORY),
                    "--artifact-root", str(artifacts),
                    "--output-root", str(output),
                    "--version", "1.2.3-test",
                    "--commit", "0123456789ab",
                    "--source-date-epoch", "1786838400",
                ], check=True, timeout=60)
                outputs.append(output)
            first_archives = sorted(outputs[0].glob("*.zip"))
            second_archives = sorted(outputs[1].glob("*.zip"))
            self.assertEqual(3, len(first_archives))
            self.assertEqual(
                [hashlib.sha256(path.read_bytes()).hexdigest() for path in first_archives],
                [hashlib.sha256(path.read_bytes()).hexdigest() for path in second_archives],
            )
            sums = (outputs[0] / "SHA256SUMS").read_text()
            for archive in first_archives:
                self.assertIn(hashlib.sha256(archive.read_bytes()).hexdigest(), sums)

            with zipfile.ZipFile(next(path for path in first_archives if "windows" in path.name)) as archive:
                names = set(archive.namelist())
                prefix = "S3 RLCD Deck Companion/"
                self.assertIn(prefix + "INSTALL.txt", names)
                self.assertIn(prefix + "THIRD_PARTY_NOTICES.txt", names)
                self.assertIn(prefix + "sbom.spdx.json", names)
                manifest = json.loads(archive.read(prefix + "manifest.json"))
                sbom = json.loads(archive.read(prefix + "sbom.spdx.json"))
                self.assertEqual("windows-amd64", manifest["target"])
                self.assertEqual("SPDX-2.3", sbom["spdxVersion"])
                self.assertIn(b"--install", archive.read(prefix + "INSTALL.txt"))


if __name__ == "__main__":
    unittest.main()
