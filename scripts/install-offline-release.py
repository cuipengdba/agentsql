#!/usr/bin/env python3
"""Verify and install an extracted AgentSQL offline release with no network access.

Run as root: python3 install-offline.py install (or upgrade).
For independent publisher authentication, pass --trusted-public-key with a key
obtained outside this bundle. The default pins AgentSQL's current release key.
"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile

PINNED_KEY_SHA256 = "83d507dd59659aff490725184b2e65a5c1036181fc79c350028905df723a1542"
POSTGRES_PREFLIGHT_SHA256 = "4cbab38eeb5b4db09e2a79c1ce4dd9bb5df23dc9e20eb6a27d3d3d1d79d7abc2"
VERSION_RE = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+$")
MAX_DOCUMENT = 32 << 20
MAX_PACKAGE = 2 << 30


def names(version):
    stem = "agentsql-" + version
    return {
        stem + "-linux-amd64.tar.gz", stem + "-linux-amd64.tar.gz.sha256",
        stem + "-linux-arm64.tar.gz", stem + "-linux-arm64.tar.gz.sha256",
        stem + ".spdx.json", stem + ".spdx.json.sig.json",
        "ed25519-release-public-key.json", "go-version-metadata.txt", "install.sh",
        "provenance.json", "provenance.json.sig.json", "SBOM-GENERATION.txt",
        "VERIFYING-SIGNATURES.md", "SHA256SUMS", "SHA256SUMS.sig.json",
    }


def digest(data):
    return hashlib.sha256(data).hexdigest()


def decode64(value, size):
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9+/]+={0,2}", value):
        raise ValueError("invalid release key or signature encoding")
    raw = base64.b64decode(value + "=" * (-len(value) % 4), validate=True)
    if len(raw) != size:
        raise ValueError("invalid release key or signature size")
    return raw


def regular_bytes(path, limit):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
        raise ValueError("unexpected or oversized release file: " + path.name)
    data = path.read_bytes()
    if len(data) != info.st_size:
        raise ValueError("release file changed: " + path.name)
    return data


def file_digest(path, limit):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
        raise ValueError("unexpected or oversized release file: " + path.name)
    state = hashlib.sha256()
    size = 0
    with path.open("rb") as stream:
        while chunk := stream.read(1 << 20):
            size += len(chunk)
            if size > limit:
                raise ValueError("oversized release file: " + path.name)
            state.update(chunk)
    if size != info.st_size:
        raise ValueError("release file changed: " + path.name)
    return state.hexdigest()


def verify_signature(assets, artifact, key_doc, pub, openssl, scratch):
    payload = regular_bytes(assets / artifact, MAX_DOCUMENT)
    signed = json.loads(regular_bytes(assets / (artifact + ".sig.json"), 16384))
    if (signed.get("schemaVersion") != 1 or signed.get("algorithm") != "Ed25519" or
            signed.get("keyClass") != "release" or signed.get("keyId") != key_doc["keyId"] or
            signed.get("signedArtifact") != artifact or signed.get("artifactSHA256") != digest(payload) or
            signed.get("signedPayload") != "raw artifact bytes"):
        raise ValueError("invalid signature metadata: " + artifact)
    signature = decode64(signed.get("signatureBase64"), 64)
    payload_path = scratch / "payload"
    signature_path = scratch / "signature"
    key_path = scratch / "public.der"
    payload_path.write_bytes(payload)
    signature_path.write_bytes(signature)
    # RFC 8410 SubjectPublicKeyInfo for a raw Ed25519 public key.
    key_path.write_bytes(bytes.fromhex("302a300506032b6570032100") + pub)
    result = subprocess.run([openssl, "pkeyutl", "-verify", "-pubin", "-keyform", "DER",
                             "-inkey", str(key_path), "-rawin", "-in", str(payload_path),
                             "-sigfile", str(signature_path)], capture_output=True, timeout=30, check=False)
    if result.returncode != 0:
        raise ValueError("release signature rejected: " + artifact)
    return payload


def verify_assets(assets, version, trusted_key, openssl, scratch):
    expected = names(version)
    if {entry.name for entry in assets.iterdir()} != expected:
        raise ValueError("offline bundle does not contain exactly 15 expected release assets")
    trusted = regular_bytes(trusted_key, 16384)
    bundled = regular_bytes(assets / "ed25519-release-public-key.json", 16384)
    if trusted != bundled:
        raise ValueError("bundled release key differs from trusted release key")
    key_doc = json.loads(trusted)
    pub = decode64(key_doc.get("publicKeyBase64"), 32)
    if (key_doc.get("schemaVersion") != 1 or key_doc.get("algorithm") != "Ed25519" or
            key_doc.get("keyClass") != "release" or key_doc.get("keyId") != "sha256:" + digest(pub)):
        raise ValueError("invalid release public key")
    manifest = verify_signature(assets, "SHA256SUMS", key_doc, pub, openssl, scratch)
    lines = manifest.decode("ascii").splitlines()
    checked = {}
    for line in lines:
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9._-]+)", line)
        if not match or match.group(2) in checked:
            raise ValueError("invalid signed asset inventory")
        checked[match.group(2)] = match.group(1)
    if set(checked) != expected - {"SHA256SUMS", "SHA256SUMS.sig.json"}:
        raise ValueError("signed asset inventory does not match release")
    for name, want in checked.items():
        limit = MAX_PACKAGE if name.endswith(".tar.gz") else MAX_DOCUMENT
        if file_digest(assets / name, limit) != want:
            raise ValueError("release asset digest mismatch: " + name)
    verify_signature(assets, "provenance.json", key_doc, pub, openssl, scratch)
    verify_signature(assets, "agentsql-" + version + ".spdx.json", key_doc, pub, openssl, scratch)


def private_copy(source, destination, limit):
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    with os.fdopen(os.open(source, flags), "rb") as incoming:
        info = os.fstat(incoming.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            raise ValueError("invalid release asset: " + source.name)
        with destination.open("xb") as outgoing:
            copied = 0
            while chunk := incoming.read(1 << 20):
                copied += len(chunk)
                if copied > limit:
                    raise ValueError("release asset grew while staging: " + source.name)
                outgoing.write(chunk)
        if destination.stat().st_size != info.st_size:
            raise ValueError("release asset changed while staging: " + source.name)


def run(args):
    root = Path(__file__).resolve().parent
    version = (root / "VERSION").read_text(encoding="ascii").strip()
    if not VERSION_RE.fullmatch(version) or not (root / "assets").is_dir():
        raise ValueError("invalid offline bundle layout or version")
    if os.geteuid() != 0 and not args.verify_only:
        raise ValueError("installation requires root; run with sudo")
    if args.runtime_role is not None and not args.require_postgres_binder:
        raise ValueError("--runtime-role requires --require-postgres-binder")
    openssl = shutil.which("openssl")
    if not openssl:
        raise ValueError("OpenSSL 3 is required for offline release signature verification")
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    if arch is None:
        raise ValueError("supported Linux architectures are amd64 and arm64")
    source = root / "assets"
    expected = names(version)
    if {entry.name for entry in source.iterdir()} != expected:
        raise ValueError("offline bundle asset inventory is incomplete")
    if args.trusted_public_key:
        trusted = Path(args.trusted_public_key).resolve()
        if trusted.is_relative_to(root):
            raise ValueError("independently trusted key must be outside the bundle")
    else:
        trusted = source / "ed25519-release-public-key.json"
        if digest(regular_bytes(trusted, 16384)) != PINNED_KEY_SHA256:
            raise ValueError("bundled release key differs from the pinned AgentSQL key")
    with tempfile.TemporaryDirectory(prefix="agentsql-offline-", dir="/var/tmp") as temporary:
        scratch = Path(temporary)
        assets = scratch / "assets"
        assets.mkdir(mode=0o700)
        for name in expected:
            private_copy(source / name, assets / name,
                         MAX_PACKAGE if name.endswith(".tar.gz") else MAX_DOCUMENT)
        trusted_copy = scratch / "trusted-release-key.json"
        private_copy(trusted, trusted_copy, 16384)
        verify_assets(assets, version, trusted_copy, openssl, scratch)
        archive = assets / ("agentsql-" + version + "-linux-" + arch + ".tar.gz")
        # The package is authenticated before this binary is extracted or run.
        with tarfile.open(archive, "r:gz") as package:
            member = package.getmember("agentsql-" + version + "-linux-" + arch + "/agentsqlctl")
            if not member.isfile() or member.size > 150 << 20:
                raise ValueError("invalid release verifier in package")
            verifier = scratch / "agentsqlctl"
            with package.extractfile(member) as incoming, verifier.open("xb") as outgoing:
                shutil.copyfileobj(incoming, outgoing, 1 << 20)
        verifier.chmod(0o700)
        result = subprocess.run([str(verifier), "release", "verify", "--directory", str(assets),
                                 "--public-key", str(trusted_copy), "--version", version],
                                capture_output=True, text=True, timeout=300, check=False)
        if result.returncode != 0 or json.loads(result.stdout).get("status") != "verified":
            raise ValueError("release verifier rejected the offline assets")
        if args.require_postgres_binder:
            preflight = scratch / "check-postgres-binder.sh"
            private_copy(root / "check-postgres-binder.sh", preflight, MAX_DOCUMENT)
            if file_digest(preflight, MAX_DOCUMENT) != POSTGRES_PREFLIGHT_SHA256:
                raise ValueError("PostgreSQL binder preflight differs from the pinned script")
            command = ["sh", str(preflight)]
            if args.runtime_role is not None:
                command.extend(("--runtime-role", args.runtime_role))
            subprocess.run(command, check=True, timeout=60)
        if args.verify_only:
            print("OFFLINE_RELEASE=VERIFIED version=" + version + " architecture=" + arch)
            return
        command = ["sh", str(assets / "install.sh"), args.action, "--from", str(archive), "--with-chatbi"]
        if args.action == "install":
            command.append("--yes")
        if args.no_start:
            command.append("--no-start")
        subprocess.run(command, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "upgrade"), nargs="?", default="install")
    parser.add_argument("--trusted-public-key", help="independently obtained release key outside this bundle")
    parser.add_argument("--verify-only", action="store_true")
    parser.add_argument("--no-start", action="store_true")
    parser.add_argument("--require-postgres-binder", action="store_true",
                        help="check the configured PostgreSQL server and runtime role before installation")
    parser.add_argument("--runtime-role", help="PostgreSQL runtime role to test via SET ROLE")
    args = parser.parse_args()
    try:
        run(args)
    except (OSError, ValueError, subprocess.SubprocessError, tarfile.TarError) as error:
        parser.exit(1, "offline installation stopped: " + str(error) + "\n")


if __name__ == "__main__":
    main()
