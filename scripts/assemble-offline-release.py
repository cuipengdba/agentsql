#!/usr/bin/env python3
"""Copy, verify and archive an AgentSQL release for offline transport.

Requires a trusted agentsqlctl with `release verify`. No downloads or installs.
The independently trusted public key must be supplied separately to recipients.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import tarfile
import tempfile

PINNED_KEY_SHA256 = "83d507dd59659aff490725184b2e65a5c1036181fc79c350028905df723a1542"


def assemble(args):
    source = Path(args.directory)
    output = Path(args.output)
    if source.is_symlink() or not source.is_dir():
        raise ValueError("asset source must be a directory, not a symlink")
    if Path(args.public_key).resolve().is_relative_to(source.resolve()):
        raise ValueError("trusted public key must be supplied outside the source directory")
    if output.exists() or output.is_symlink():
        raise ValueError("output must be a new file")
    if args.installable and hashlib.sha256(Path(args.public_key).read_bytes()).hexdigest() != PINNED_KEY_SHA256:
        raise ValueError("one-command bundle requires the pinned AgentSQL release key")
    # Private staging prevents input changes after verification from changing
    # the archive. tempfile uses the selected output parent, not a system disk.
    with tempfile.TemporaryDirectory(prefix=".agentsql-offline-", dir=output.parent) as staging:
        bundle = Path(staging) / "bundle"
        if args.installable:
            bundle.mkdir(mode=0o700)
            (bundle / "VERSION").write_text(args.version + "\n", encoding="ascii")
            shutil.copyfile(Path(__file__).with_name("install-offline-release.py"), bundle / "install-offline.py")
            stage = bundle / "assets"
        else:
            stage = Path(staging) / "assets"
        stage.mkdir(mode=0o700)
        entries = list(source.iterdir())
        if len(entries) != 15:
            raise ValueError("expected exactly 15 assets")
        for entry in entries:
            original = entry.lstat()
            if not stat.S_ISREG(original.st_mode) or original.st_size > 2 << 30:
                raise ValueError("source contains a non-regular or oversized asset")
            with entry.open("rb") as incoming, (stage / entry.name).open("xb") as outgoing:
                current = os.fstat(incoming.fileno())
                if not os.path.samestat(original, current):
                    raise ValueError("source changed while copying")
                copied = 0
                while chunk := incoming.read(128 << 10):
                    copied += len(chunk)
                    if copied > 2 << 30:
                        raise ValueError("asset exceeds size limit")
                    outgoing.write(chunk)
        process = subprocess.run(
            [args.ctl, "release", "verify", "--directory", str(stage),
             "--public-key", str(Path(args.public_key).resolve()), "--version", args.version],
            capture_output=True, text=True, timeout=300, check=False,
        )
        if process.returncode != 0:
            raise ValueError("staged release did not pass agentsqlctl signature verification")
        report = json.loads(process.stdout)
        if report.get("status") != "verified" or report.get("version") != args.version:
            raise ValueError("unexpected verifier result")
        # Create-only and private permissions. Recipients reverify after unpacking.
        descriptor = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "wb") as stream:
            with tarfile.open(fileobj=stream, mode="w:gz", format=tarfile.PAX_FORMAT) as archive:
                def normalize(member):
                    member.uid = member.gid = member.mtime = 0
                    member.uname = member.gname = ""
                    member.mode = 0o755 if member.isdir() or member.name.endswith("/install-offline.py") else 0o644
                    return member
                archive.add(bundle if args.installable else stage,
                            arcname="agentsql-" + args.version + "-offline", recursive=True, filter=normalize)
        return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for argument in ("directory", "public-key", "version", "ctl", "output"):
        parser.add_argument("--" + argument, required=True)
    parser.add_argument("--installable", action="store_true",
                        help="include a one-command, post-verification offline installer")
    args = parser.parse_args()
    try:
        report = assemble(args)
    except (OSError, ValueError, subprocess.SubprocessError):
        parser.exit(1, "offline assembly failed; inspect inputs and use a new output path\n")
    print(json.dumps({"status": "assembled", "version": report["version"],
                      "source_commit": report["source_commit"], "assets": report["assets"]}))


if __name__ == "__main__":
    main()
