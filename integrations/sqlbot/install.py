#!/usr/bin/env python3
"""Install the auditable datasource adapter into the exact SQLBot v1.10.2 source.

Run at image build time, never against an unreviewed live installation.
"""
import argparse
import ast
import hashlib
import shutil
from pathlib import Path

SOURCE_SHA256 = "3fd067afeaee4f5a15fbadc8ef1fbb7c0a1f711a842021590e64938b4eb208c1"
MARKER = "# AgentSQL SQLBot adapter v1; upstream database access is disabled."
ROUTES = {
    "check_connection": 'return _agentsql_dispatch("check_connection", ds, is_raise=is_raise)',
    "get_version": 'return _agentsql_dispatch("get_version", ds)',
    "get_schema": 'return _agentsql_dispatch("get_schema", ds)',
    "get_tables": 'return _agentsql_dispatch("get_tables", ds)',
    "get_fields": 'return _agentsql_dispatch("get_fields", ds, table_name=table_name)',
    "exec_sql": 'return _agentsql_dispatch("exec_sql", ds, sql=sql, origin_column=origin_column)',
}
BLOCKED = {"get_uri", "get_uri_from_config", "get_origin_connect", "get_engine", "get_session",
           "get_driver_connection", "get_driver_pool"}


def patch(source):
    source = source.replace("\r\n", "\n")
    if MARKER in source:
        raise ValueError("SQLBot adapter is already installed; use a fresh upstream image")
    if hashlib.sha256(source.encode()).hexdigest() != SOURCE_SHA256:
        raise ValueError("Unsupported SQLBot source hash; only the verified v1.10.2 file is accepted")
    tree = ast.parse(source)
    functions = {n.name: n for n in tree.body if isinstance(n, ast.FunctionDef)}
    if not (set(ROUTES) | BLOCKED).issubset(functions):
        raise ValueError("SQLBot datasource entry points changed")
    lines = source.splitlines(keepends=True)
    insertions = []
    for name in sorted(set(ROUTES) | BLOCKED):
        node = functions[name]
        first = node.body[0]
        insertions.append((first.lineno - 1, "    " + ROUTES.get(name, "return _agentsql_deny_direct()") + "\n"))
    for index, content in sorted(insertions, reverse=True):
        lines.insert(index, content)
    result = (MARKER + "\nfrom agentsql_sqlbot import dispatch as _agentsql_dispatch, deny_direct as _agentsql_deny_direct\n"
              + "".join(lines))
    ast.parse(result)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sqlbot-app", required=True, type=Path)
    args = parser.parse_args()
    root = args.sqlbot_app.resolve(strict=True)
    target = root / "apps" / "db" / "db.py"
    raw = target.read_bytes()
    # Read/write bytes deliberately: Windows newline conversion cannot change the pinned hash.
    source = raw.decode("utf-8")
    result = patch(source)
    adapter = root / "agentsql_sqlbot.py"
    if adapter.exists() or target.with_suffix(".py.agentsql-backup").exists():
        raise ValueError("Adapter or backup already exists")
    shutil.copyfile(target, target.with_suffix(".py.agentsql-backup"))
    shutil.copyfile(Path(__file__).with_name("agentsql_sqlbot.py"), adapter)
    target.write_bytes(result.encode("utf-8"))
    print("SQLBOT_ADAPTER_INSTALL=PASS SQLBot=v1.10.2 routes=6 blocked_connectors=7")


if __name__ == "__main__":
    main()
