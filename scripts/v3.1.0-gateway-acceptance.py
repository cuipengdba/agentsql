"""Prepare isolated synthetic v3.0.2 -> v3.1.0 SQLBot acceptance fixtures.

Requires a local-only quickstart deployment, its Compose file, and the SQLBot
identities produced by v3.1.0-sqlbot-business-acceptance.py. Private material is
written only to the supplied local fixture directory, never printed.
"""
from __future__ import annotations

import argparse
import json
import re
import secrets
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


class Gateway:
    def __init__(self, url, password):
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme != "http" or parsed.hostname != "127.0.0.1":
            raise ValueError("Acceptance fixture mutations require an explicit loopback gateway")
        self.url = url.rstrip("/")
        self.token = ""
        login = self.request("POST", "/api/v1/auth/login", {"username": "admin", "password": password})
        self.token = login.get("token") or login.get("access_token")
        assert self.token, "No management login token"

    def request(self, method, path, body=None):
        headers = {"Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        request = urllib.request.Request(self.url + path, method=method, headers=headers,
            data=None if body is None else json.dumps(body).encode())
        try:
            with urllib.request.urlopen(request, timeout=60) as response:
                value = json.load(response)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"Gateway {method} {path} returned HTTP {error.code}") from None
        if isinstance(value, dict) and "data" in value:
            return value["data"]
        return value


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=["baseline", "bindings"])
    parser.add_argument("--url", default="http://127.0.0.1:8314")
    parser.add_argument("--directory", required=True, type=Path)
    args = parser.parse_args()
    root = args.directory
    compose = (root / "gateway-compose.yml").read_text(encoding="utf-8-sig")
    password = re.search(r"AGENTSQL_ADMIN_PASSWORD:\s*([^\r\n]+)", compose)[1].strip()
    demo_key = re.search(r"AGENTSQL_DEMO_RO_KEY:\s*([^\r\n]+)", compose)[1].strip()
    api = Gateway(args.url, password)
    private_path = root / "gateway-fixture.private.json"
    state = json.loads(private_path.read_text()) if private_path.exists() else {}
    if args.action == "baseline":
        roles = api.request("GET", "/api/v1/roles")
        role = next((r for r in roles if r["name"] == "SQLBot acceptance query only"), None)
        if role is None:
            role = api.request("POST", "/api/v1/roles", {"name": "SQLBot acceptance query only", "permissions": ["query.execute"]})
        users = api.request("GET", "/api/v1/users")
        state["role_id"] = role["id"]
        state.setdefault("users", {})
        for suffix in ("a", "b"):
            username = "sqlbot_qa_" + suffix
            user = next((u for u in users if u["username"] == username), None)
            if user is None:
                user_password = secrets.token_urlsafe(24)
                user = api.request("POST", "/api/v1/users", {"username": username,
                    "display_name": "SQLBot QA " + suffix.upper(), "password": user_password,
                    "role_ids": [role["id"]], "status": "active"})
                state["users"][suffix] = {"password": user_password}
            state["users"].setdefault(suffix, {}).update({"id": user["id"], "username": username})
        state["agent_a"] = {"id": "agent-demo-ro", "key": demo_key}
        if "agent_b" not in state:
            agent = api.request("POST", "/api/v1/agents", {"id": "agent-v310-sqlbot-b", "name": "SQLBot acceptance B", "level": "readonly"})
            state["agent_b"] = {"id": agent["id"], "key": agent.get("api_key") or agent.get("key")}
            assert state["agent_b"]["key"]
            api.request("POST", "/api/v1/policies", {"id": "policy-v310-products-b", "agent_id": agent["id"],
                "datasource_id": "ds-demo-pg", "object_type": "table", "object_name": "public.products", "action": "allow"})
        private_path.write_text(json.dumps(state, indent=2), encoding="utf-8")
        print(json.dumps({"action": "baseline", "users": {k: v["id"] for k, v in state["users"].items()}, "completed": True}))
        return
    identities = json.loads((root / "sqlbot-data" / "identities.json").read_text())["identities"]
    state.setdefault("bindings", {})
    for suffix, external in zip(("a", "b"), identities):
        if suffix in state["bindings"]:
            continue
        result = api.request("POST", "/api/v1/integrations/sqlbot/bindings", {
            "workspace_id": external["workspace_id"], "sqlbot_user_id": external["user_id"],
            "datasource_name": "AgentSQL PostgreSQL", "agentsql_user_id": state["users"][suffix]["id"],
            "agent_id": state["agent_" + suffix]["id"], "datasource_id": "ds-demo-pg",
            "schema_name": "public", "numeric_columns": ["id", "order_id", "customer_id", "count", "total", "amount", "price"],
            "agent_key_env": "AGENTSQL_SQLBOT_KEY_" + suffix.upper(),
            "connector_token_env": "AGENTSQL_SQLBOT_CONNECTOR_" + suffix.upper(), "enabled": True,
        })
        state["bindings"][suffix] = result
        private_path.write_text(json.dumps(state, indent=2), encoding="utf-8")
    config = api.request("POST", "/api/v1/integrations/sqlbot/export", {"endpoint": "https://agentsql-v310-proxy/mcp"})
    config["ca_file"] = "/run/agentsql-tls/server.crt"
    (root / "adapter-config.json").write_text(json.dumps(config, indent=2), encoding="utf-8")
    environment = ["AGENTSQL_SQLBOT_CONFIG=/run/agentsql-sqlbot/adapter-config.json"]
    for suffix in ("a", "b"):
        environment.extend(["AGENTSQL_SQLBOT_KEY_" + suffix.upper() + "=" + state["agent_" + suffix]["key"],
            "AGENTSQL_SQLBOT_CONNECTOR_" + suffix.upper() + "=" + state["bindings"][suffix]["connector_token"]])
    (root / "adapter.env").write_text("\n".join(environment) + "\n", encoding="utf-8")
    print(json.dumps({"action": "bindings", "count": len(config["bindings"]), "completed": True}))


if __name__ == "__main__":
    main()
