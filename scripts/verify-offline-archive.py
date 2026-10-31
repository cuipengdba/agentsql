#!/usr/bin/env python3
"""Authenticate an AgentSQL offline transport archive before extracting it.

Obtain this verifier and the release public key through an independent trusted
channel. The archive and its detached signature may come from offline media.
"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import tempfile

MAX_ARCHIVE = 6 << 30
MAX_DOCUMENT = 16 << 10


def regular_bytes(path, limit):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
        raise ValueError("invalid or oversized input: " + path.name)
    data = path.read_bytes()
    if len(data) != info.st_size:
        raise ValueError("input changed while reading: " + path.name)
    return data


def decode64(value, size):
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9+/]+={0,2}", value):
        raise ValueError("invalid base64 encoding")
    result = base64.b64decode(value + "=" * (-len(value) % 4), validate=True)
    if len(result) != size:
        raise ValueError("invalid key or signature size")
    return result


def archive_sha256(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size <= 0 or info.st_size > MAX_ARCHIVE:
        raise ValueError("invalid or oversized offline archive")
    digest = hashlib.sha256()
    size = 0
    with path.open("rb") as stream:
        current = os.fstat(stream.fileno())
        if not os.path.samestat(info, current):
            raise ValueError("offline archive changed before verification")
        while chunk := stream.read(1 << 20):
            size += len(chunk)
            if size > MAX_ARCHIVE:
                raise ValueError("offline archive exceeds size limit")
            digest.update(chunk)
    if size != info.st_size:
        raise ValueError("offline archive changed while verifying")
    return digest.hexdigest(), info


def verify(archive, signature, public_key, openssl):
    archive = Path(archive)
    key_doc = json.loads(regular_bytes(Path(public_key), MAX_DOCUMENT))
    pub = decode64(key_doc.get("publicKeyBase64"), 32)
    key_id = "sha256:" + hashlib.sha256(pub).hexdigest()
    if (key_doc.get("schemaVersion") != 1 or key_doc.get("algorithm") != "Ed25519" or
            key_doc.get("keyClass") != "release" or key_doc.get("keyId") != key_id):
        raise ValueError("invalid trusted release public key")
    sig_doc = json.loads(regular_bytes(Path(signature), MAX_DOCUMENT))
    archive_digest, original = archive_sha256(archive)
    if (sig_doc.get("schemaVersion") != 1 or sig_doc.get("algorithm") != "Ed25519" or
            sig_doc.get("keyClass") != "release" or sig_doc.get("keyId") != key_id or
            sig_doc.get("signedArtifact") != archive.name or
            sig_doc.get("artifactSHA256") != archive_digest or
            sig_doc.get("signedPayload") != "raw artifact bytes"):
        raise ValueError("offline archive signature metadata mismatch")
    raw_signature = decode64(sig_doc.get("signatureBase64"), 64)
    with tempfile.TemporaryDirectory(prefix="agentsql-archive-verify-") as temporary:
        scratch = Path(temporary)
        der = scratch / "release-public.der"
        raw = scratch / "archive.sig"
        der.write_bytes(bytes.fromhex("302a300506032b6570032100") + pub)
        raw.write_bytes(raw_signature)
        result = subprocess.run(
            [openssl, "pkeyutl", "-verify", "-pubin", "-keyform", "DER",
             "-inkey", str(der), "-rawin", "-in", str(archive), "-sigfile", str(raw)],
            capture_output=True, timeout=300, check=False,
        )
    if result.returncode != 0 or not os.path.samestat(original, archive.stat()):
        raise ValueError("offline archive signature rejected or archive changed")
    if archive_sha256(archive)[0] != archive_digest:
        raise ValueError("offline archive changed after signature verification")
    return key_id, archive_digest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", required=True)
    parser.add_argument("--signature", required=True)
    parser.add_argument("--public-key", required=True,
                        help="release public key obtained independently from the archive")
    args = parser.parse_args()
    openssl = shutil.which("openssl")
    if not openssl:
        parser.exit(1, "offline archive verification stopped: OpenSSL is required\n")
    try:
        key_id, digest = verify(args.archive, args.signature, args.public_key, openssl)
    except (OSError, ValueError, json.JSONDecodeError, subprocess.SubprocessError) as error:
        parser.exit(1, "offline archive verification stopped: " + str(error) + "\n")
    print("OFFLINE_ARCHIVE=VERIFIED key_id=" + key_id + " sha256=" + digest)


if __name__ == "__main__":
    main()
