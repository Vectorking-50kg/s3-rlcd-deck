#!/usr/bin/env python3
"""Build deterministic, self-installing Companion application archives."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import zipfile


TARGETS = (
    ("darwin-arm64", "s3deck-companion"),
    ("darwin-amd64", "s3deck-companion"),
    ("windows-amd64", "s3deck-companion.exe"),
)
IDENTITY = re.compile(r"^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$")
COMMIT = re.compile(r"^[0-9a-f]{7,40}$")
SHORT_VERSION = re.compile(r"^(\d+)(?:\.(\d+))?(?:\.(\d+))?")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def write(path: Path, contents: bytes, mode: int = 0o600) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(contents)
    path.chmod(mode)


def module_documents(companion: Path) -> list[dict[str, object]]:
    raw = subprocess.run(
        ["go", "list", "-m", "-json", "all"],
        cwd=companion,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        timeout=60,
    ).stdout.decode("utf-8")
    decoder = json.JSONDecoder()
    documents: list[dict[str, object]] = []
    offset = 0
    while offset < len(raw):
        while offset < len(raw) and raw[offset].isspace():
            offset += 1
        if offset == len(raw):
            break
        document, offset = decoder.raw_decode(raw, offset)
        documents.append(document)
    return documents


def make_sbom(
    companion: Path,
    notices: Path,
    version: str,
    commit: str,
    created: str,
) -> bytes:
    packages = [{
        "SPDXID": "SPDXRef-Companion",
        "name": "S3 RLCD Deck Companion",
        "versionInfo": version,
        "downloadLocation": "NOASSERTION",
        "filesAnalyzed": False,
        "licenseConcluded": "NOASSERTION",
        "licenseDeclared": "NOASSERTION",
        "copyrightText": "NOASSERTION",
    }]
    relationships = []
    for index, module in enumerate(module_documents(companion), start=1):
        if module.get("Main"):
            continue
        identifier = f"SPDXRef-GoModule-{index}"
        packages.append({
            "SPDXID": identifier,
            "name": module["Path"],
            "versionInfo": module.get("Version", "unknown"),
            "downloadLocation": "NOASSERTION",
            "filesAnalyzed": False,
            "licenseConcluded": "NOASSERTION",
            "licenseDeclared": "NOASSERTION",
            "copyrightText": "NOASSERTION",
        })
        relationships.append({
            "spdxElementId": "SPDXRef-Companion",
            "relationshipType": "DEPENDS_ON",
            "relatedSpdxElement": identifier,
        })
    license_files = []
    for path in sorted(notices.glob("*.txt")):
        license_files.append({"name": path.name, "sha256": sha256(path)})
    document = {
        "spdxVersion": "SPDX-2.3",
        "dataLicense": "CC0-1.0",
        "SPDXID": "SPDXRef-DOCUMENT",
        "name": f"s3deck-companion-{version}",
        "documentNamespace": (
            "https://github.com/Vectorking-50kg/s3-rlcd-deck/"
            f"spdx/{version}/{commit}"
        ),
        "creationInfo": {"created": created, "creators": ["Tool: tools/package_companion.py"]},
        "packages": packages,
        "relationships": relationships,
        "annotations": [{
            "annotationType": "OTHER",
            "annotator": "Tool: tools/package_companion.py",
            "annotationDate": created,
            "comment": "Bundled license text checksums: " + json.dumps(license_files, sort_keys=True),
        }],
    }
    return (json.dumps(document, indent=2, sort_keys=True) + "\n").encode()


def combined_notices(notices: Path) -> bytes:
    result = bytearray(b"S3 RLCD Deck Companion third-party notices\n\n")
    for path in sorted(notices.glob("*.txt")):
        result.extend(f"===== {path.name} =====\n".encode())
        contents = path.read_bytes()
        result.extend(contents)
        if not contents.endswith(b"\n"):
            result.extend(b"\n")
        result.extend(b"\n")
    return bytes(result)


def reproducibility(version: str, commit: str, epoch: int) -> bytes:
    return f"""S3 RLCD Deck Companion reproducible build

Source commit: {commit}
Version: {version}
SOURCE_DATE_EPOCH: {epoch}
Toolchains: Go 1.26.x and Python 3.11+

From a clean checkout of Source commit:
  SOURCE_DATE_EPOCH={epoch} S3DECK_BUILD_VERSION={version} ./tools/package_companion.sh

Go executables use CGO_ENABLED=0, -trimpath, -buildvcs=false, and an empty build ID.
ZIP entries are sorted and carry SOURCE_DATE_EPOCH rather than wall-clock time.
SHA256SUMS authenticates the resulting archives; release signing is an additional,
platform-specific publication step and is intentionally not claimed reproducible.
""".encode()


def file_manifest(root: Path, target: str, version: str, commit: str, epoch: int) -> bytes:
    files = []
    for path in sorted(item for item in root.rglob("*") if item.is_file()):
        files.append({
            "path": path.relative_to(root).as_posix(),
            "bytes": path.stat().st_size,
            "sha256": sha256(path),
        })
    document = {
        "schema_version": 1,
        "product": "S3 RLCD Deck Companion",
        "version": version,
        "commit": commit,
        "target": target,
        "source_date_epoch": epoch,
        "installer": "Run the bundled executable with --install; no administrator rights are used.",
        "default_management_listener": "127.0.0.1:7777",
        "default_installed_device_hub_listener": "127.0.0.1:7780",
        "files": files,
    }
    return (json.dumps(document, indent=2, sort_keys=True) + "\n").encode()


def zip_tree(source: Path, destination: Path, epoch: int) -> None:
    timestamp = dt.datetime.fromtimestamp(max(epoch, 315532800), dt.UTC)
    date_time = (timestamp.year, timestamp.month, timestamp.day, timestamp.hour, timestamp.minute, timestamp.second)
    with zipfile.ZipFile(destination, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        for path in sorted(item for item in source.rglob("*") if item.is_file()):
            relative = path.relative_to(source).as_posix()
            info = zipfile.ZipInfo(relative, date_time)
            info.compress_type = zipfile.ZIP_DEFLATED
            info.create_system = 3
            mode = stat.S_IMODE(path.stat().st_mode)
            info.external_attr = (stat.S_IFREG | mode) << 16
            archive.writestr(info, path.read_bytes(), compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)


def adhoc_sign_darwin(path: Path) -> None:
    """Create a structurally valid local signature without claiming release identity."""
    if sys.platform != "darwin" or shutil.which("codesign") is None:
        raise RuntimeError("Darwin application packaging requires macOS codesign")
    subprocess.run(
        ["codesign", "--force", "--sign", "-", "--timestamp=none", str(path)],
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        timeout=60,
    )


def package_target(
    artifact_root: Path,
    output_root: Path,
    target: str,
    binary_name: str,
    version: str,
    commit: str,
    epoch: int,
    notices: bytes,
    sbom: bytes,
    reproduce: bytes,
) -> Path:
    source = artifact_root / target / binary_name
    if not source.is_file() or source.is_symlink():
        raise RuntimeError(f"missing bounded build artifact: {source}")
    staging = output_root / "staging" / target
    if staging.exists():
        shutil.rmtree(staging)
    if target.startswith("darwin-"):
        root = staging / "S3 RLCD Deck Companion.app"
        executable = root / "Contents" / "MacOS" / binary_name
        resources = root / "Contents" / "Resources"
        match = SHORT_VERSION.match(version)
        short_version = ".".join(part or "0" for part in match.groups()) if match else "0.0.0"
        info = {
            "CFBundleDevelopmentRegion": "en",
            "CFBundleDisplayName": "S3 RLCD Deck Companion",
            "CFBundleExecutable": binary_name,
            "CFBundleIdentifier": "com.vectorking.s3-rlcd-deck-companion",
            "CFBundleInfoDictionaryVersion": "6.0",
            "CFBundleName": "S3 RLCD Deck Companion",
            "CFBundlePackageType": "APPL",
            "CFBundleShortVersionString": short_version,
            "CFBundleVersion": str(epoch),
            "LSMinimumSystemVersion": "13.0",
            "LSUIElement": True,
        }
        write(root / "Contents" / "Info.plist", plistlib.dumps(info, sort_keys=True))
    else:
        root = staging / "S3 RLCD Deck Companion"
        executable = root / binary_name
        resources = root
    write(executable, source.read_bytes(), 0o700)
    write(resources / "THIRD_PARTY_NOTICES.txt", notices)
    write(resources / "sbom.spdx.json", sbom)
    write(resources / "REPRODUCIBLE_BUILD.txt", reproduce)
    instructions = (
        "Install for the current user (no administrator rights):\n"
        f"  {binary_name} --install\n\n"
        "Inspect or change login startup:\n"
        f"  {binary_name} --installation-status\n"
        f"  {binary_name} --disable-login\n"
        f"  {binary_name} --enable-login\n\n"
        "Uninstall login startup while retaining user data and rollback files:\n"
        f"  {binary_name} --uninstall\n"
    ).encode()
    write(resources / "INSTALL.txt", instructions)
    if target.startswith("darwin-"):
        # Go emits a linker signature for Mach-O executables. That signature is
        # not valid once the executable is placed inside an application bundle
        # with resources, so replace it before recording the file digest.
        adhoc_sign_darwin(executable)
    write(resources / "manifest.json", file_manifest(root, target, version, commit, epoch))
    if target.startswith("darwin-"):
        # Seal Info.plist, the already-signed executable, and every resource.
        # This remains an ad-hoc acceptance signature; publication signing and
        # Apple notarization are separate release steps.
        adhoc_sign_darwin(root)
        subprocess.run(
            ["codesign", "--verify", "--deep", "--strict", "--verbose=4", str(root)],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            timeout=60,
        )
    archive = output_root / f"s3deck-companion_{version}_{target}.zip"
    zip_tree(staging, archive, epoch)
    return archive


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repository-root", type=Path, required=True)
    parser.add_argument("--artifact-root", type=Path, required=True)
    parser.add_argument("--output-root", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--source-date-epoch", type=int, required=True)
    args = parser.parse_args()
    if not IDENTITY.fullmatch(args.version) or not COMMIT.fullmatch(args.commit):
        raise SystemExit("version or commit is not safe for a release artifact")
    repository = args.repository_root.resolve(strict=True)
    companion = repository / "companion"
    notices_path = companion / "notices" / "licenses"
    output = args.output_root.absolute()
    output.parent.mkdir(parents=True, exist_ok=True)
    if output.is_symlink() or output.exists() and not output.is_dir():
        raise SystemExit("release output must be an ordinary directory path")
    if output.exists() and any(output.iterdir()):
        raise SystemExit("release output must be empty to prevent stale unhashed artifacts")
    if output.exists():
        output.rmdir()
    staging = Path(tempfile.mkdtemp(prefix=".companion-packages-", dir=output.parent))
    created = dt.datetime.fromtimestamp(args.source_date_epoch, dt.UTC).isoformat().replace("+00:00", "Z")
    try:
        notices = combined_notices(notices_path)
        sbom = make_sbom(companion, notices_path, args.version, args.commit, created)
        reproduce = reproducibility(args.version, args.commit, args.source_date_epoch)
        archives = [
            package_target(
                args.artifact_root, staging, target, binary, args.version, args.commit,
                args.source_date_epoch, notices, sbom, reproduce,
            )
            for target, binary in TARGETS
        ]
        sums = "".join(f"{sha256(path)}  {path.name}\n" for path in sorted(archives))
        write(staging / "SHA256SUMS", sums.encode())
        os.replace(staging, output)
    except BaseException:
        shutil.rmtree(staging, ignore_errors=True)
        raise
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
