#!/usr/bin/env python3
"""Build and inspect the pinned ChatBI ONNX asset for a signed offline release."""

from __future__ import annotations

import argparse
import gzip
import hashlib
import io
import json
import os
import re
import stat
import tarfile
import tempfile
from pathlib import Path, PurePosixPath


VERSION = "v4.5.0"
ROOT = f"agentsql-{VERSION}-chatbi-model"
MANIFEST = Path(__file__).resolve().parents[1] / "internal/chatbi/pinned_embedding_model.json"


def pinned_files() -> tuple[bytes, list[dict]]:
    raw = MANIFEST.read_bytes()
    document = json.loads(raw)
    files = document["files"]
    if document["license"] != "Apache-2.0" or len(files) != 9:
        raise ValueError("unexpected pinned model manifest")
    seen: set[str] = set()
    for item in files:
        name = item["path"]
        parts = PurePosixPath(name)
        if (not name or parts.is_absolute() or str(parts) != name or
                any(part in (".", "..") for part in parts.parts) or
                "\\" in name or ":" in name or name in seen or
                type(item["size"]) is not int or item["size"] < 1 or
                len(item["sha256"]) != 64):
            raise ValueError("unsafe pinned model manifest")
        seen.add(name)
    return raw, files


def sha256_stream(stream: io.BufferedReader) -> str:
    digest = hashlib.sha256()
    for block in iter(lambda: stream.read(1024 * 1024), b""):
        digest.update(block)
    return digest.hexdigest()


def validate_source(source: Path, files: list[dict]) -> None:
    if source.is_symlink() or not source.is_dir():
        raise ValueError("model source must be a regular directory")
    expected = {item["path"] for item in files}
    expected_root = {PurePosixPath(name).parts[0] for name in expected}
    if {item.name for item in source.iterdir()} != expected_root:
        raise ValueError("model source has missing or extra entries")
    pool = source / "1_Pooling"
    if pool.is_symlink() or not pool.is_dir() or {p.name for p in pool.iterdir()} != {"config.json"}:
        raise ValueError("pooling directory is missing or changed")
    for item in files:
        path = source.joinpath(*PurePosixPath(item["path"]).parts)
        mode = path.lstat().st_mode
        if not stat.S_ISREG(mode) or path.stat().st_size != item["size"]:
            raise ValueError(f"model file type or size mismatch: {item['path']}")
        with path.open("rb") as stream:
            if sha256_stream(stream) != item["sha256"]:
                raise ValueError(f"model file hash mismatch: {item['path']}")


def tar_info(name: str, size: int, epoch: int, directory: bool = False) -> tarfile.TarInfo:
    info = tarfile.TarInfo(name + ("/" if directory else ""))
    info.size = size
    info.mode = 0o755 if directory else 0o644
    info.uid = info.gid = 0
    info.uname = info.gname = ""
    info.mtime = epoch
    info.type = tarfile.DIRTYPE if directory else tarfile.REGTYPE
    return info


def verify_archive(archive: Path, manifest: bytes, files: list[dict]) -> None:
    expected = {f"{ROOT}/", f"{ROOT}/1_Pooling/", f"{ROOT}/pinned_embedding_model.json"}
    expected.update(f"{ROOT}/{item['path']}" for item in files)
    by_name = {item["path"]: item for item in files}
    seen: set[str] = set()
    with tarfile.open(archive, mode="r:gz") as bundle:
        for member in bundle:
            name = member.name + ("/" if member.isdir() and not member.name.endswith("/") else "")
            if name not in expected or name in seen or not (member.isdir() or member.isfile()):
                raise ValueError("unsafe or unexpected model archive member")
            seen.add(name)
            if member.isdir():
                continue
            stream = bundle.extractfile(member)
            if stream is None:
                raise ValueError("unreadable model archive member")
            if name == f"{ROOT}/pinned_embedding_model.json":
                if member.size != len(manifest) or stream.read() != manifest:
                    raise ValueError("model archive manifest mismatch")
                continue
            pinned = by_name[name.removeprefix(f"{ROOT}/")]
            if member.size != pinned["size"] or sha256_stream(stream) != pinned["sha256"]:
                raise ValueError("model archive file mismatch")
    if seen != expected:
        raise ValueError("model archive is incomplete")


def build(source: Path, output: Path, epoch: int) -> None:
    manifest, files = pinned_files()
    if epoch < 0 or output.suffixes[-2:] != [".tar", ".gz"]:
        raise ValueError("expected a .tar.gz output and a nonnegative epoch")
    if output.resolve().is_relative_to(source.resolve()):
        raise ValueError("output must be outside the model source")
    validate_source(source, files)
    output.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=".chatbi-model-", suffix=".tar.gz", dir=output.parent)
    try:
        with os.fdopen(descriptor, "wb") as raw:
            with gzip.GzipFile(fileobj=raw, filename="", mode="wb", mtime=epoch) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as bundle:
                    bundle.addfile(tar_info(ROOT, 0, epoch, directory=True))
                    bundle.addfile(tar_info(f"{ROOT}/1_Pooling", 0, epoch, directory=True))
                    bundle.addfile(tar_info(f"{ROOT}/pinned_embedding_model.json", len(manifest), epoch), io.BytesIO(manifest))
                    for item in files:
                        name = item["path"]
                        with source.joinpath(*PurePosixPath(name).parts).open("rb") as stream:
                            bundle.addfile(tar_info(f"{ROOT}/{name}", item["size"], epoch), stream)
        verify_archive(Path(temporary), manifest, files)
        os.replace(temporary, output)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    with output.open("rb") as stream:
        digest = sha256_stream(stream)
    output.with_name(output.name + ".sha256").write_bytes(f"{digest}  {output.name}\n".encode("ascii"))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--source-date-epoch", type=int)
    parser.add_argument("--verify", type=Path, help="inspect an existing model archive without extracting it")
    parser.add_argument("--version", default=VERSION, help="AgentSQL release version, default v4.5.0")
    args = parser.parse_args()
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", args.version):
        parser.error("invalid release version")
    global ROOT
    ROOT = f"agentsql-{args.version}-chatbi-model"
    if args.verify is not None:
        if args.source is not None or args.output is not None or args.source_date_epoch is not None:
            parser.error("--verify cannot be combined with build options")
        manifest, files = pinned_files()
        verify_archive(args.verify, manifest, files)
        with args.verify.open("rb") as stream:
            print(sha256_stream(stream))
    else:
        if args.source is None or args.output is None or args.source_date_epoch is None:
            parser.error("build requires --source, --output, and --source-date-epoch")
        build(args.source, args.output, args.source_date_epoch)


if __name__ == "__main__":
    main()
