#!/usr/bin/env python3
"""Build a signed, self-contained S3 RLCD Deck OTA bundle.

The P-256 private key is supplied explicitly and is never copied into the
repository or output archive. The archive contains only the public manifest,
detached signature, and firmware image.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

BOARD = "esp32-s3-rlcd-4.2"
MAXIMUM_IMAGE_BYTES = 1_740_800
VERSION_PATTERN = re.compile(r"^[0-9][0-9A-Za-z.+-]{0,30}$")


class BundleFailure(RuntimeError):
    pass


def canonical_manifest(
    *, version: str, board: str, image_length: int, digest: bytes,
    key_id: int, minimum_protocol: int,
) -> bytes:
    try:
        version_bytes = version.encode("ascii")
        board_bytes = board.encode("ascii")
    except UnicodeEncodeError as error:
        raise BundleFailure("version and board must be ASCII") from error
    if (
        not VERSION_PATTERN.fullmatch(version)
        or board != BOARD
        or not 0 < image_length <= MAXIMUM_IMAGE_BYTES
        or len(digest) != 32
        or not 0 < key_id <= 0xFFFFFFFF
        or not 0 < minimum_protocol <= 0xFFFFFFFF
        or len(version_bytes) >= 32
        or len(board_bytes) >= 48
    ):
        raise BundleFailure("invalid OTA manifest metadata")
    return b"".join((
        b"S3RLCDOTA1",
        key_id.to_bytes(4, "big"),
        minimum_protocol.to_bytes(4, "big"),
        image_length.to_bytes(4, "big"),
        board_bytes.ljust(48, b"\0"),
        version_bytes.ljust(32, b"\0"),
        digest,
    ))


def der_length(document: bytes, offset: int) -> tuple[int, int]:
    if offset >= len(document):
        raise BundleFailure("truncated ECDSA signature")
    first = document[offset]
    if first < 0x80:
        return first, offset + 1
    octets = first & 0x7F
    if octets == 0 or octets > 2 or offset + 1 + octets > len(document):
        raise BundleFailure("invalid ECDSA signature length")
    value = int.from_bytes(document[offset + 1:offset + 1 + octets], "big")
    if value < 0x80:
        raise BundleFailure("non-canonical ECDSA signature length")
    return value, offset + 1 + octets


def der_integer(document: bytes, offset: int) -> tuple[bytes, int]:
    if offset >= len(document) or document[offset] != 0x02:
        raise BundleFailure("invalid ECDSA integer")
    size, offset = der_length(document, offset + 1)
    end = offset + size
    if size == 0 or end > len(document) or document[offset] & 0x80:
        raise BundleFailure("invalid ECDSA integer")
    value = document[offset:end]
    if len(value) > 1 and value[0] == 0:
        if not value[1] & 0x80:
            raise BundleFailure("non-canonical ECDSA integer")
        value = value[1:]
    if len(value) > 32:
        raise BundleFailure("ECDSA integer exceeds P-256")
    return value.rjust(32, b"\0"), end


def raw_ecdsa_signature(document: bytes) -> bytes:
    if not document or document[0] != 0x30:
        raise BundleFailure("invalid ECDSA signature")
    size, offset = der_length(document, 1)
    if offset + size != len(document):
        raise BundleFailure("invalid ECDSA signature envelope")
    r, offset = der_integer(document, offset)
    s, offset = der_integer(document, offset)
    if offset != len(document):
        raise BundleFailure("trailing ECDSA signature data")
    return r + s


def sign(canonical: bytes, private_key: Path) -> bytes:
    if not private_key.is_file():
        raise BundleFailure("private key is not a regular file")
    try:
        result = subprocess.run(
            ["openssl", "dgst", "-sha256", "-sign", os.fspath(private_key)],
            input=canonical, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            check=False, timeout=10,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise BundleFailure("OpenSSL signing failed") from error
    if result.returncode != 0:
        raise BundleFailure("OpenSSL rejected the OTA signing key")
    return raw_ecdsa_signature(result.stdout)


def replace_private_file(path: Path, document: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "wb", closefd=True) as output:
            descriptor = -1
            output.write(document)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary_name, path)
    except BaseException:
        if descriptor >= 0:
            os.close(descriptor)
        try:
            os.unlink(temporary_name)
        except FileNotFoundError:
            pass
        raise


def build_bundle(args: argparse.Namespace) -> None:
    image = bytearray(args.image.read_bytes())
    try:
        digest = hashlib.sha256(image).digest()
        canonical = canonical_manifest(
            version=args.version, board=args.board, image_length=len(image),
            digest=digest, key_id=args.key_id,
            minimum_protocol=args.minimum_protocol,
        )
        signature = sign(canonical, args.private_key)
        archive = {
            "bundle_version": 1,
            "manifest": {
                "version": args.version,
                "board": args.board,
                "image_length": len(image),
                "image_sha256": digest.hex(),
                "signature": base64.b64encode(signature).decode("ascii"),
                "signing_key_id": args.key_id,
                "minimum_protocol_version": args.minimum_protocol,
            },
            "image": base64.b64encode(image).decode("ascii"),
        }
        document = json.dumps(archive, separators=(",", ":"), ensure_ascii=True).encode("ascii") + b"\n"
        replace_private_file(args.output, document)
    finally:
        image[:] = b"\0" * len(image)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--private-key", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--board", default=BOARD)
    parser.add_argument("--key-id", type=int, default=1)
    parser.add_argument("--minimum-protocol", type=int, default=1)
    return parser.parse_args()


def main() -> int:
    try:
        build_bundle(parse_args())
    except (BundleFailure, OSError) as error:
        print(f"sign_ota_bundle: {error}", file=__import__("sys").stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
