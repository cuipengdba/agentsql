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
from pathlib import Path, PurePosixPath
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
    result = {
        stem + "-linux-amd64.tar.gz", stem + "-linux-amd64.tar.gz.sha256",
        stem + "-linux-arm64.tar.gz", stem + "-linux-arm64.tar.gz.sha256",
        stem + ".spdx.json", stem + ".spdx.json.sig.json",
        "ed25519-release-public-key.json", "go-version-metadata.txt", "install.sh",
        "provenance.json", "provenance.json.sig.json", "SBOM-GENERATION.txt",
        "VERIFYING-SIGNATURES.md", "SHA256SUMS", "SHA256SUMS.sig.json",
    }
    if tuple(map(int, version.removeprefix("v").split("."))) >= (4, 5, 0):
        model = stem + "-chatbi-model.tar.gz"
        result.update((model, model + ".sha256"))
    return result


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
        raise ValueError("offline bundle asset set does not match version")
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
    if len(expected) == 17:
        model = "agentsql-" + version + "-chatbi-model.tar.gz"
        sidecar = regular_bytes(assets / (model + ".sha256"), 1024)
        if sidecar != (checked[model] + "  " + model + "\n").encode("ascii"):
            raise ValueError("model checksum sidecar does not match signed inventory")
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


def stage_model_archive(archive, version, scratch):
    """Extract only signed, pinned model files into a private staging directory."""
    root = "agentsql-" + version + "-chatbi-model"
    manifest_name = root + "/pinned_embedding_model.json"
    with tarfile.open(archive, "r:gz") as package:
        members = {}
        for member in package:
            name = member.name.rstrip("/")
            if name in members or name.startswith("/") or "\\" in name or ":" in name or any(
                    part in ("", ".", "..") for part in name.split("/")):
                raise ValueError("unsafe or duplicate model archive member")
            if not (member.isdir() or member.isfile()):
                raise ValueError("model archive contains a link or unsupported member")
            members[name] = member
        manifest_member = members.get(manifest_name)
        if manifest_member is None or not manifest_member.isfile() or manifest_member.size > 16384:
            raise ValueError("model archive has no bounded pinned inventory")
        manifest_stream = package.extractfile(manifest_member)
        if manifest_stream is None:
            raise ValueError("unreadable pinned model inventory")
        with manifest_stream as stream:
            manifest = json.load(stream)
        if not isinstance(manifest, dict):
            raise ValueError("invalid pinned model inventory")
        files = manifest.get("files")
        if manifest.get("license") != "Apache-2.0" or not isinstance(files, list) or len(files) != 9:
            raise ValueError("invalid pinned model inventory")
        expected = {root, root + "/1_Pooling", manifest_name}
        destination = scratch / "chatbi-model"
        destination.mkdir(mode=0o700)
        (destination / "1_Pooling").mkdir(mode=0o700)
        total_size = 0
        for item in files:
            if not isinstance(item, dict):
                raise ValueError("invalid pinned model file")
            path, size, want = item.get("path"), item.get("size"), item.get("sha256")
            if (not isinstance(path, str) or not path or path.startswith("/") or
                    "\\" in path or ":" in path or any(part in ("", ".", "..") for part in path.split("/")) or
                    not isinstance(size, int) or isinstance(size, bool) or size < 1 or size > MAX_PACKAGE or
                    not isinstance(want, str) or not re.fullmatch(r"[0-9a-f]{64}", want)):
                raise ValueError("unsafe pinned model file")
            total_size += size
            if total_size > 1 << 30:
                raise ValueError("pinned model exceeds extraction size limit")
            expected.add(root + "/" + path)
            source = members.get(root + "/" + path)
            if source is None or not source.isfile() or source.size != size:
                raise ValueError("missing or mismatched model file")
            target = destination.joinpath(*PurePosixPath(path).parts)
            if target.parent not in (destination, destination / "1_Pooling"):
                raise ValueError("unexpected model subdirectory")
            state = hashlib.sha256()
            copied = 0
            incoming = package.extractfile(source)
            if incoming is None:
                raise ValueError("unreadable model archive member")
            with incoming, target.open("xb") as outgoing:
                while block := incoming.read(1 << 20):
                    copied += len(block)
                    if copied > size:
                        raise ValueError("model file exceeded pinned size")
                    state.update(block)
                    outgoing.write(block)
            if copied != size or state.hexdigest() != want:
                raise ValueError("model file does not match pinned inventory")
        if set(members) != expected or not members[root].isdir() or not members[root + "/1_Pooling"].isdir():
            raise ValueError("model archive has unexpected or missing members")
    return destination


def stage_release_app_for_model(archive, version, arch, scratch):
    """Stage the authenticated application and its packaged client libraries."""
    root = "agentsql-" + version + "-linux-" + arch + "/"
    app_name = root + "agentsql"
    library_prefix = root + "lib/yashandb/"
    app = scratch / "agentsql-model-verifier"
    library_dir = scratch / "yashan-libs"
    library_dir.mkdir(mode=0o700)
    libraries = set()
    total = 0
    with tarfile.open(archive, "r:gz") as package:
        members = package.getmembers()
        matches = [member for member in members if member.name == app_name]
        if len(matches) != 1:
            raise ValueError("release package has no unique application binary")
        member = matches[0]
        if not member.isfile() or member.size > 150 << 20:
            raise ValueError("invalid release application binary")
        stream = package.extractfile(member)
        if stream is None:
            raise ValueError("unreadable release application binary")
        with stream, app.open("xb") as outgoing:
            shutil.copyfileobj(stream, outgoing, 1 << 20)
        if app.stat().st_size != member.size:
            raise ValueError("release application binary changed during staging")
        app.chmod(0o700)
        for member in members:
            if not member.name.startswith(library_prefix) or member.isdir():
                continue
            name = member.name[len(library_prefix):]
            if (not member.isfile() or not name or name.startswith("/") or
                    "\\" in name or ":" in name or any(part in ("", ".", "..") for part in name.split("/")) or
                    name in libraries or member.size > 150 << 20):
                raise ValueError("unsafe release client library")
            libraries.add(name)
            total += member.size
            if total > 512 << 20:
                raise ValueError("release client libraries exceed staging limit")
            target = library_dir.joinpath(*PurePosixPath(name).parts)
            target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            incoming = package.extractfile(member)
            if incoming is None:
                raise ValueError("unreadable release client library")
            with incoming, target.open("xb") as outgoing:
                shutil.copyfileobj(incoming, outgoing, 1 << 20)
            if target.stat().st_size != member.size:
                raise ValueError("release client library changed during staging")
    if not libraries:
        raise ValueError("release package has no client libraries")
    return app, library_dir


def run(args):
    if args.external_backup_done and args.action != "upgrade":
        raise ValueError("--external-backup-done requires upgrade")
    if args.external_backup_done and args.verify_only:
        raise ValueError("--external-backup-done is only meaningful for installation")
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
        model_directory = None
        if len(expected) == 17:
            model_archive = assets / ("agentsql-" + version + "-chatbi-model.tar.gz")
            model_directory = stage_model_archive(model_archive, version, scratch)
            app, libraries = stage_release_app_for_model(archive, version, arch, scratch)
            environment = os.environ.copy()
            environment["LD_LIBRARY_PATH"] = str(libraries)
            result = subprocess.run([str(app), "chatbi", "verify-embedding-model", "--path", str(model_directory)],
                                    env=environment, capture_output=True, text=True, timeout=300, check=False)
            if result.returncode != 0 or result.stdout.strip() != "CHATBI_MODEL=VERIFIED":
                raise ValueError("release binary rejected the pinned offline model")
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
        if model_directory is not None:
            command.extend(("--chatbi-model-dir", str(model_directory)))
        if args.action == "install":
            command.append("--yes")
        if args.no_start:
            command.append("--no-start")
        if args.external_backup_done:
            command.append("--external-backup-done")
        subprocess.run(command, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "upgrade"), nargs="?", default="install")
    parser.add_argument("--trusted-public-key", help="independently obtained release key outside this bundle")
    parser.add_argument("--verify-only", action="store_true")
    parser.add_argument("--no-start", action="store_true")
    parser.add_argument("--external-backup-done", action="store_true",
                        help="confirm an external database backup was completed before upgrade")
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
