"""Export SQLBot v1.10.2 identities for an isolated AgentSQL migration rehearsal.

Run inside the operator's existing SQLBot environment. The output contains
password digests, the JWT signing secret and the existing RSA login key. Keep
it private and never attach it to an issue or public release. This exports only
identity foundations, not a complete SQLBot migration. Database reads use a
read-only transaction; the application's key accessor reads its active key.
"""
from __future__ import annotations

import argparse
import asyncio
import hashlib
import importlib.metadata
import json
import os
import tomllib
from pathlib import Path


def verify_baseline():
    """Reject a modified identity implementation before claiming this baseline.

    These hashes cover the pinned community identity contract, not a claim that
    every installed upstream file or the proprietary xpack package was audited.
    """
    import common.core.security
    root = Path(common.core.security.__file__).resolve().parents[2]
    if tomllib.loads((root / "pyproject.toml").read_text(encoding="utf-8"))["project"]["version"] != "1.10.2":
        raise RuntimeError("Only SQLBot Community v1.10.2 is supported by this identity exporter")
    expected = {
        "apps/system/api/login.py": "2e0876f00ec394c0ff8244e67f663e182d17e06e64526bdc8c40780081011eee",
        "common/core/security.py": "af57d24a781981531650ad480e85862447d0646059cc956cba8d4e841a3495ef",
        "apps/system/models/user.py": "561c7364984748586b9ec4f229357c633132f23da935e5e41cee36256b1e46d7",
    }
    for name, digest in expected.items():
        if hashlib.sha256((root / name).read_bytes()).hexdigest() != digest:
            raise RuntimeError("The installed SQLBot identity source differs from the supported baseline")
    if importlib.metadata.version("sqlbot-xpack") != "0.0.5.37":
        raise RuntimeError("This SQLBot xpack dependency needs separate compatibility verification")


async def export():
    # Preserve upstream initialization order without copying its implementation.
    import sqlbot_xpack  # noqa: F401
    from sqlbot_xpack import core
    from common.core.config import settings
    from common.core.db import engine
    from sqlalchemy import text

    verify_baseline()

    with engine.connect() as connection:
        connection.execute(text("SET TRANSACTION READ ONLY"))
        users = connection.execute(text(
            "SELECT id, account, name, oid, language, status, origin, password FROM sys_user ORDER BY id"
        )).mappings().all()
        workspaces = dict(connection.execute(text("SELECT id, name FROM sys_workspace")).all())
        memberships = connection.execute(text("SELECT uid, oid, weight FROM sys_user_ws")).mappings().all()
        connection.rollback()
    exported = []
    for user in users:
        members = [
            {"oid": str(row["oid"]), "name": workspaces[row["oid"]], "weight": row["weight"]}
            for row in memberships if row["uid"] == user["id"] and row["oid"] in workspaces
        ]
        if user["id"] == 1:
            members = [{"oid": str(oid), "name": name, "weight": 1} for oid, name in workspaces.items()]
        if not user["oid"] or str(user["oid"]) not in {m["oid"] for m in members}:
            raise RuntimeError("An identity has no current workspace; resolve it before this foundation import")
        password = user["password"]
        password_format = "bcrypt" if password.startswith(("$2a$", "$2b$", "$2y$")) else "sqlbot-md5"
        if password_format == "sqlbot-md5" and (len(password) != 32 or any(c not in "0123456789abcdefABCDEF" for c in password)):
            raise RuntimeError("An unsupported password format needs explicit migration handling")
        exported.append({
            "id": str(user["id"]), "account": user["account"], "name": user["name"],
            "oid": str(user["oid"]), "language": user["language"], "status": user["status"],
            "origin": user["origin"], "isAdmin": user["id"] == 1, "memberships": members,
            "password_hash": password, "password_format": password_format,
            "auth_revision": 1, "allow_legacy_tokens": True,
        })
    keypair = await core.get_rsa_keypair().get_private_key()
    if len(keypair) != 3 or "PRIVATE KEY" not in keypair[0]:
        raise RuntimeError("Unsupported login key export contract")
    return {
        "format_version": 1,
        "source_version": "v1.10.2",
        "source_commit": "aa41fc1033bb974a703df9200a98ec8b9eec5f25",
        "identity_keys": {"signing_key": settings.SECRET_KEY, "login_private_key_pem": keypair[0]},
        "users": exported, "bindings": [], "models": [],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    data = asyncio.run(export())
    descriptor = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as output:
        json.dump(data, output, ensure_ascii=False, indent=2)
        output.write("\n")
        output.flush()
        os.fsync(output.fileno())
    print(json.dumps({"exported_users": len(data["users"]), "contains_secrets": True,
                      "complete_migration": False, "source_database_modified": False}))


if __name__ == "__main__":
    main()
