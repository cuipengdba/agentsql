#!/usr/bin/env python3
"""Check local binder provenance against the artifact, Git head and source bytes.

This verifies integrity and consistency, not the identity of a signer or the
trustworthiness of the local builder. Production release requires both.
"""

import argparse
import hashlib
import json
import re
from pathlib import Path

SOURCE_FILES = (
    "Makefile",
    "agentsql_binder.c",
    "agentsql_binder.control",
    "agentsql_binder--0.4.sql",
)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def sha256_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def source_slice_sha256(root):
    lines = b"".join(
        f"{sha256_file(root / name)}  {name}\n".encode("ascii")
        for name in SOURCE_FILES
    )
    return hashlib.sha256(lines).hexdigest()


def verify(artifact, provenance, source_root, revision):
    if not re.fullmatch(r"[0-9a-f]{40}", revision):
        raise ValueError("source revision must be a full lowercase Git commit")
    if provenance.stat().st_size > 1 << 20:
        raise ValueError("provenance exceeds 1 MiB")
    statement = json.loads(provenance.read_text(encoding="utf-8"), object_pairs_hook=unique_object)
    if statement.get("_type") != "https://in-toto.io/Statement/v1" or statement.get("predicateType") != "https://slsa.dev/provenance/v1":
        raise ValueError("unexpected provenance type")
    expected_subject = {"name": artifact.name, "digest": {"sha256": sha256_file(artifact)}}
    if statement.get("subject") != [expected_subject]:
        raise ValueError("artifact subject does not match")
    definition = statement.get("predicate", {}).get("buildDefinition", {})
    params = definition.get("externalParameters", {})
    if params.get("sourceRevision") != revision or params.get("postgresMajor") not in {"14", "15", "16", "17", "18"}:
        raise ValueError("source revision or PostgreSQL major does not match")
    if params.get("architecture") not in {"x86_64", "aarch64"} or params.get("ecosystem") not in {"deb", "rpm", "apk"}:
        raise ValueError("architecture or package ecosystem is invalid")
    if artifact.suffix != "." + params["ecosystem"]:
        raise ValueError("artifact extension does not match ecosystem")
    expected_dependencies = [
        {
            "uri": f"git+https://github.com/cuipengdba/agentsql@{revision}",
            "digest": {"gitCommit": revision},
        },
        {
            "uri": "urn:agentsql:dbext/postgres/agentsql_binder:source-file-list",
            "digest": {"sha256": source_slice_sha256(source_root)},
        },
    ]
    if definition.get("resolvedDependencies") != expected_dependencies:
        raise ValueError("source dependency or source-byte digest does not match")
    if not statement.get("predicate", {}).get("runDetails", {}).get("builder", {}).get("id"):
        raise ValueError("builder identity is missing")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifact", type=Path, required=True)
    parser.add_argument("--provenance", type=Path, required=True)
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--source-revision", required=True)
    args = parser.parse_args()
    verify(args.artifact, args.provenance, args.source_root, args.source_revision)
    print("BINDER_PROVENANCE=PASS local_consistency_only=true")


if __name__ == "__main__":
    main()
