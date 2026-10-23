"""Exercise revocation and fail-closed behavior against an isolated live gateway.

Requires the local synthetic fixture produced by the companion setup script.
No credential or delegated token is included in the resulting evidence JSON.
Only the explicitly supplied loopback gateway and its SQLite fixture are changed.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import os
import re
import subprocess
import urllib.error
import urllib.request
from pathlib import Path
from types import SimpleNamespace


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--directory", required=True, type=Path)
    parser.add_argument("--url", default="http://127.0.0.1:8314")
    args = parser.parse_args()
    directory = args.directory.resolve()
    container = json.loads(subprocess.check_output(
        ["docker", "inspect", "agentsql-v310-gateway"], text=True, encoding="utf-8"))[0]
    if (container["Config"]["Image"] != "agentsql:v3.1.0-acceptance3" or
            container["Config"]["Cmd"] != ["serve", "--config", "/etc/agentsql/config.demo.yaml"] or
            container["HostConfig"]["NetworkMode"] != "agentsql-v310-acceptance" or
            not any(m["Type"] == "volume" and m["Name"] == "agentsql-v310-gateway-data-linux"
                    and m["Destination"] == "/var/lib/agentsql" for m in container["Mounts"])):
        raise RuntimeError("live acceptance requires the isolated v3.1.0 Linux-volume gateway")
    repo = Path(__file__).resolve().parents[1]
    setup = module("gateway_setup", Path(__file__).with_name("v3.1.0-gateway-acceptance.py"))
    adapter = module("acceptance_adapter", repo / "integrations/sqlbot/agentsql_sqlbot.py")
    compose = (directory / "gateway-compose.yml").read_text(encoding="utf-8-sig")
    password = re.search(r"AGENTSQL_ADMIN_PASSWORD:\s*([^\r\n]+)", compose)[1].strip()
    api = setup.Gateway(args.url, password)
    state = json.loads((directory / "gateway-fixture.private.json").read_text())
    config = json.loads((directory / "adapter-config.json").read_text())
    config.update(endpoint=args.url + "/mcp", ca_file=None)
    identities = json.loads((directory / "sqlbot-data/identities.json").read_text())["identities"]
    report = {"environment": "isolated synthetic loopback gateway", "checks": []}
    report_path = directory / "live-security.json"

    def record(name, details=None):
        report["checks"].append({"name": name, "passed": True, "details": details})
        report_path.write_text(json.dumps(report, indent=2), encoding="utf-8")
        print("PASS " + name, flush=True)

    def rejected(name, action):
        try:
            action()
        except adapter.AdapterError as error:
            record(name, error.feedback())
        else:
            raise AssertionError(name + " unexpectedly allowed")

    bindings = {}
    for suffix in ("a", "b"):
        identifier = state["bindings"][suffix]["binding"]["id"]
        b = next(b for b in config["bindings"] if b["id"] == identifier)
        os.environ[b["agent_key_env"]] = state["agent_" + suffix]["key"]
        os.environ[b["connector_token_env"]] = state["bindings"][suffix]["connector_token"]
        bindings[suffix] = b

    def resolve(suffix="a", **changes):
        i = identities[0 if suffix == "a" else 1]
        user = SimpleNamespace(oid=i["workspace_id"], id=i["user_id"])
        for k, v in changes.items():
            setattr(user, k, v)
        return adapter._resolve(config, bindings[suffix], user)

    def raw_status(path, token, body=None):
        request = urllib.request.Request(args.url + path, method="GET" if body is None else "POST",
            data=None if body is None else json.dumps(body).encode(),
            headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                return response.status
        except urllib.error.HTTPError as error:
            return error.code

    sql = "SELECT id, full_name, phone, email FROM public.demo_b2_customers WHERE id <= 3 ORDER BY id LIMIT 3"
    def query(client):
        return client.tool("query", {"datasource_id": "ds-demo-pg", "sql": sql})

    lease = resolve()
    assert raw_status("/api/v1/users", lease["user_token"]) == 401
    record("delegated token cannot authenticate management API")
    rejected("cross-workspace resolution rejected", lambda: resolve(oid=identities[1]["workspace_id"]))
    rejected("cross-user resolution rejected", lambda: resolve(id=identities[1]["user_id"]))
    mismatch = dict(lease, key_env=bindings["b"]["agent_key_env"])
    rejected("delegated human token bound to Agent", lambda: adapter.Client(config, mismatch).__enter__())
    with adapter.Client(config, lease) as client:
        result = query(client)
        assert result.get("audit_id", 0) > 0 and result.get("result", {}).get("Rows")
        columns = result["result"]["Columns"]
        for row in result["result"]["Rows"]:
            assert "*" in row[columns.index("phone")] and "*" in row[columns.index("email")]
        record("real scoped query returns persistently audited rows", {"audit_id": result["audit_id"], "result": result["result"]})
        rejected("mapped datasource cannot be replaced", lambda: client.tool("query", {"datasource_id": "ds-demo-mysql", "sql": "SELECT id FROM customers WHERE id <= 1"}))

    binding_path = "/api/v1/integrations/sqlbot/bindings/" + bindings["a"]["id"]
    with adapter.Client(config, resolve()) as client:
        b = api.request("GET", binding_path)
        changed = api.request("PUT", binding_path, dict(b, enabled=False))
        try:
            rejected("disabled binding revokes an already-open MCP session", lambda: query(client))
            rejected("disabled binding refuses fresh resolution", resolve)
        finally:
            api.request("PUT", binding_path, dict(changed, enabled=True))
    with adapter.Client(config, resolve()) as client:
        query(client)
    record("re-enabled binding requires fresh lease and recovers")

    user_path = "/api/v1/users/" + state["users"]["a"]["id"]
    with adapter.Client(config, resolve()) as client:
        api.request("PUT", user_path, {"status": "disabled"})
        try:
            rejected("disabled mapped human revokes live session", lambda: query(client))
            rejected("disabled mapped human refuses resolution", resolve)
        finally:
            api.request("PUT", user_path, {"status": "active"})
    with adapter.Client(config, resolve()) as client:
        api.request("PUT", user_path + "/roles", {"role_ids": []})
        try:
            rejected("removed query permission revokes live session", lambda: query(client))
            rejected("removed query permission refuses resolution", resolve)
        finally:
            api.request("PUT", user_path + "/roles", {"role_ids": [state["role_id"]]})

    # Execute the fault injector inside the isolated gateway's Linux volume.
    # Cross-OS writes against Docker Desktop's Windows SQLite bind mount are
    # not a reliable fault-injection method. Always remove the trigger.
    def fault(action):
        subprocess.run(["docker", "exec", "--env", "AGENTSQL_ACCEPTANCE_AUDIT_FAULT=isolated-v3.1.0",
                        "agentsql-v310-gateway", "/tmp/auditfault", action], check=True,
                       stdout=subprocess.DEVNULL)

    with adapter.Client(config, resolve()) as client:
        before = api.request("GET", "/api/v1/audit?page=1&page_size=1")["total"]
        fault("on")
        try:
            rejected("audit persistence failure exposes no query result", lambda: query(client))
            rejected("audit persistence failure refuses new delegated identity", resolve)
            assert api.request("GET", "/api/v1/audit?page=1&page_size=1")["total"] == before
            record("fault did not create a success audit record")
        finally:
            fault("off")
    with adapter.Client(config, resolve()) as client:
        result = query(client)
        assert result.get("audit_id", 0) > 0
    record("audit recovery restores normal audited execution")
    report["completed"] = True
    report_path.write_text(json.dumps(report, indent=2), encoding="utf-8")


if __name__ == "__main__":
    main()
