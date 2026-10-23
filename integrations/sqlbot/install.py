#!/usr/bin/env python3
"""Install the auditable datasource adapter into the exact SQLBot v1.10.2 source.

Run at image build time, never against an unreviewed live installation.
"""
import argparse
import ast
import hashlib
import json
import shutil
from pathlib import Path

SOURCE_SHA256 = "3fd067afeaee4f5a15fbadc8ef1fbb7c0a1f711a842021590e64938b4eb208c1"
MARKER = "# AgentSQL SQLBot adapter v2; upstream database access is disabled."
UPSTREAM = {
    "apps/db/db.py": SOURCE_SHA256,
    "apps/chat/task/llm.py": "85abc8c3261f43b9b138132c0fc6c96be4929f97345e0d1075ba0d4707e31fc9",
    "apps/datasource/crud/datasource.py": "132b67548e7f7a45f2e13a0ec9b06c081c4c3dc575c23b8fc0ac4fae3dd9a4ad",
    "main.py": "e8d2398b7cf468772c45cbd3a02e2151202b0082bca57945f927203b1c20ec03",
}
SUPPORT = ("agentsql_sqlbot.py", "agentsql_sqlbot_context.py", "agentsql_sqlbot_routes.py", "agentsql_sqlbot_console.html")
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


def patch_support(relative, source):
    source = source.replace("\r\n", "\n")
    if relative not in UPSTREAM or hashlib.sha256(source.encode()).hexdigest() != UPSTREAM[relative]:
        raise ValueError("Unsupported SQLBot source hash: " + relative)
    if relative == "apps/chat/task/llm.py":
        result = source + "\nfrom agentsql_sqlbot_context import protect_llm_service as _agentsql_protect_llm\n_agentsql_protect_llm(LLMService)\n"
    elif relative == "apps/datasource/crud/datasource.py":
        tree = ast.parse(source)
        target = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == "get_table_obj_by_ds")
        returns = [n for n in ast.walk(target) if isinstance(n, ast.Return)]
        if len(returns) != 1 or not isinstance(returns[0].value, ast.Name) or returns[0].value.id != "_list":
            raise ValueError("SQLBot cached-schema return path changed")
        lines = source.splitlines(keepends=True)
        lines[returns[0].lineno - 1] = "    return _agentsql_filter_table_objects(_list, ds)\n"
        result = "from agentsql_sqlbot_context import filter_table_objects as _agentsql_filter_table_objects\n" + "".join(lines)
    elif relative == "main.py":
        anchor = "mcp.setup_server()\n"
        if (source.count(anchor) != 1 or
                source.count("sqlbot_xpack.init_fastapi_app(app)") != 1 or
                source.index(anchor) > source.index("sqlbot_xpack.init_fastapi_app(app)")):
            raise ValueError("SQLBot route registration order changed")
        # The xpack initializer mounts the frontend at '/'. Starlette checks
        # routes in order, so any API route added after that mount returns 404.
        result = source.replace(anchor, "from agentsql_sqlbot_routes import router as _agentsql_router\napp.include_router(_agentsql_router, prefix=settings.API_V1_STR)\n\n" + anchor)
    else:
        raise ValueError("Unknown SQLBot patch target")
    ast.parse(result)
    return result


def verify_install(root):
    manifest = json.loads((root / "agentsql-sqlbot-manifest.json").read_text(encoding="utf-8"))
    expected = set(UPSTREAM) | set(SUPPORT)
    if manifest.get("adapter_version") != 2 or manifest.get("upstream_version") != "v1.10.2" or set(manifest.get("files", {})) != expected:
        raise ValueError("SQLBot adapter installation manifest is invalid")
    for relative in sorted(expected):
        if hashlib.sha256((root / relative).read_bytes()).hexdigest() != manifest["files"][relative]:
            raise ValueError("SQLBot installed file drift detected: " + relative)
    return {"result": "PASS", "adapter_version": 2, "upstream_version": "v1.10.2", "checked_files": len(expected)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sqlbot-app", required=True, type=Path)
    parser.add_argument("--check", action="store_true", help="Verify installed adapter and pinned source files without modifying them")
    args = parser.parse_args()
    root = args.sqlbot_app.resolve(strict=True)
    if args.check:
        print(json.dumps(verify_install(root)))
        return
    if any((root / name).exists() for name in SUPPORT) or (root / "agentsql-sqlbot-manifest.json").exists():
        raise ValueError("Adapter already exists; build from a fresh upstream image")
    patched = {}
    for relative in UPSTREAM:
        target = root / relative
        if target.with_suffix(".py.agentsql-backup").exists():
            raise ValueError("Upstream backup already exists")
        source = target.read_bytes().decode("utf-8")
        patched[relative] = patch(source) if relative == "apps/db/db.py" else patch_support(relative, source)
    for name in SUPPORT:
        if not Path(__file__).with_name(name).is_file():
            raise ValueError("Adapter support file is missing")
    # All upstream hashes and destinations were checked before any mutation.
    for relative, result in patched.items():
        target = root / relative
        shutil.copyfile(target, target.with_suffix(".py.agentsql-backup"))
        target.write_bytes(result.encode("utf-8"))
    for name in SUPPORT:
        shutil.copyfile(Path(__file__).with_name(name), root / name)
    files = {relative: hashlib.sha256((root / relative).read_bytes()).hexdigest() for relative in set(UPSTREAM) | set(SUPPORT)}
    (root / "agentsql-sqlbot-manifest.json").write_text(json.dumps({"adapter_version": 2, "upstream_version": "v1.10.2", "files": files}, indent=2), encoding="utf-8")
    print("SQLBOT_ADAPTER_INSTALL=PASS SQLBot=v1.10.2 adapter=2 routes=6 blocked_connectors=7 identity=personal approvals=explicit")


if __name__ == "__main__":
    main()
