"""Inventory the pinned public SQLBot API without importing/executing its code.

This is a coverage baseline, not a statement that AgentSQL implements a route.
Dependencies which register routes dynamically need separate runtime discovery.
"""
from __future__ import annotations

import argparse
import ast
import hashlib
import json
import subprocess
from pathlib import Path

SOURCE_COMMITS = {
    "v1.10.2": "aa41fc1033bb974a703df9200a98ec8b9eec5f25",
    "v1.10.3": "96f06c7eade60ecf3e6af0313821373761d1fb85",
}
METHODS = {"get", "post", "put", "patch", "delete", "head", "options"}


def literal(node, default=None):
    try:
        return ast.literal_eval(node)
    except (ValueError, TypeError):
        return default


def inventory(root: Path, expected_version: str | None = None) -> dict:
    head = subprocess.check_output(
        ["git", "-C", str(root), "rev-parse", "HEAD"], text=True
    ).strip()
    version = next((version for version, commit in SOURCE_COMMITS.items() if commit == head), None)
    if version is None or expected_version is not None and version != expected_version:
        raise ValueError("reference must be the pinned SQLBot commit for the selected version")
    if subprocess.check_output(
        ["git", "-C", str(root), "status", "--porcelain", "--untracked-files=no"], text=True
    ).strip():
        raise ValueError("reference tracked files must be unmodified")
    def source(path):
        # Git blobs avoid host-specific CRLF conversion in provenance hashes.
        return subprocess.check_output(["git", "-C", str(root), "show", f"{head}:{path}"])

    api = ast.parse(source("backend/apps/api.py").decode("utf-8"))
    modules = {}
    included = set()
    for node in api.body:
        if isinstance(node, ast.ImportFrom) and node.module:
            for name in node.names:
                modules[name.asname or name.name] = node.module + "." + name.name
        if isinstance(node, ast.Expr) and isinstance(node.value, ast.Call):
            call = node.value
            if isinstance(call.func, ast.Attribute) and call.func.attr == "include_router":
                if call.args and isinstance(call.args[0], ast.Attribute):
                    owner = call.args[0].value
                    if isinstance(owner, ast.Name):
                        included.add(owner.id)
    routes = []
    sources = {}
    for alias in sorted(included):
        path = Path("backend") / (modules[alias].replace(".", "/") + ".py")
        raw = source(path.as_posix())
        sources[path.as_posix()] = hashlib.sha256(raw).hexdigest()
        tree = ast.parse(raw.decode("utf-8"))
        prefixes = {}
        for node in tree.body:
            if not isinstance(node, ast.Assign) or not isinstance(node.value, ast.Call):
                continue
            if isinstance(node.value.func, ast.Name) and node.value.func.id == "APIRouter":
                for target in node.targets:
                    if isinstance(target, ast.Name):
                        prefixes[target.id] = next(
                            (literal(k.value, "") for k in node.value.keywords if k.arg == "prefix"), ""
                        )
        for node in tree.body:
            if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                continue
            for decorator in node.decorator_list:
                if not isinstance(decorator, ast.Call) or not isinstance(decorator.func, ast.Attribute):
                    continue
                func = decorator.func
                if not isinstance(func.value, ast.Name) or func.value.id not in prefixes or func.attr not in METHODS:
                    continue
                suffix = literal(decorator.args[0]) if decorator.args else None
                if not isinstance(suffix, str):
                    raise ValueError(f"dynamic route requires manual discovery: {path}:{node.lineno}")
                routes.append({
                    "method": func.attr.upper(),
                    "path": "/api/v1" + prefixes[func.value.id] + suffix,
                    "operation": node.name,
                    "source": path.as_posix(),
                    "line": decorator.lineno,
                    "implementation": "pending",
                    "differential_test": "pending",
                })
    routes.sort(key=lambda r: (r["path"], r["method"]))
    keys = [(r["method"], r["path"]) for r in routes]
    if len(set(keys)) != len(keys):
        raise ValueError("duplicate routes require manual review")
    return {
        "schema": 1,
        "upstream": {"repository": "https://github.com/dataease/SQLBot", "version": version, "commit": head},
        "scope": "public-source statically registered API routes; not a complete runtime inventory",
        "additional_required_surfaces": [
            "sqlbot_xpack community routes and login encryption contract",
            "MCP Streamable HTTP and legacy SSE tools, parameters and error behavior",
            "browser routes, static assets, iframe and assistant SDK callbacks",
            "database migrations, preserved IDs, tokens, keys and encrypted configuration",
        ],
        "source_sha256": sources,
        "routes": routes,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", choices=SOURCE_COMMITS)
    parser.add_argument("reference", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    result = inventory(args.reference, args.version)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"Inventoried {len(result['routes'])} static routes; runtime-only surfaces remain explicitly pending")


if __name__ == "__main__":
    main()
